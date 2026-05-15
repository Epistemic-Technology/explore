// Package claude adapts the Anthropic Messages API to the llm.Provider interface.
// Uses prompt caching aggressively on the static system prompt and the repo
// primer, so warm explanation calls re-use cached prefix tokens.
package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/llm/httpx"
)

const (
	endpoint    = "https://api.anthropic.com/v1/messages"
	apiVersion  = "2023-06-01"
	defaultMax  = 1024
	maxAttempts = 4 // initial attempt + 3 retries; total ≤ ~15s of backoff
)

// Anthropic API request/response types — only the fields we use.

type contentBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type message struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type messagesRequest struct {
	Model     string         `json:"model"`
	System    []contentBlock `json:"system,omitempty"`
	Messages  []message      `json:"messages"`
	MaxTokens int            `json:"max_tokens"`
	Stream    bool           `json:"stream,omitempty"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      anthropicUsage `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (a anthropicUsage) toLLM() llm.Usage {
	return llm.Usage{
		InputTokens:         a.InputTokens,
		OutputTokens:        a.OutputTokens,
		CacheReadTokens:     a.CacheReadInputTokens,
		CacheCreationTokens: a.CacheCreationInputTokens,
	}
}

type Provider struct {
	apiKey string
	model  string
	http   *http.Client
}

// New builds a Claude provider. model defaults to claude-sonnet-4-6 when empty.
func New(apiKey, model string) *Provider {
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return &Provider{
		apiKey: apiKey,
		model:  model,
		http:   &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *Provider) Name() string                { return "claude" }
func (p *Provider) Model() string               { return p.model }
func (p *Provider) SupportsPromptCaching() bool { return true }

func (p *Provider) Explain(ctx context.Context, req llm.ExplainRequest) (*llm.Explanation, error) {
	if p.apiKey == "" {
		debug.Logf("claude.Explain: missing API key (level=%s path=%q)", req.Level, req.Path)
		return nil, errors.New("claude: ANTHROPIC_API_KEY not set")
	}
	debug.Logf("claude.Explain: start level=%s path=%q sym=%q sourceLen=%d primerLen=%d model=%q", req.Level, req.Path, req.Symbol, len(req.Source), len(req.RepoPrimer), p.model)

	system := []contentBlock{
		{Type: "text", Text: llm.SystemPromptExplain, CacheControl: &cacheControl{Type: "ephemeral"}},
	}
	if req.RepoPrimer != "" {
		system = append(system, contentBlock{
			Type:         "text",
			Text:         "Repo context:\n" + req.RepoPrimer,
			CacheControl: &cacheControl{Type: "ephemeral"},
		})
	}

	user := llm.BuildExplainUser(req)
	apiReq := messagesRequest{
		Model:     p.model,
		System:    system,
		Messages:  []message{{Role: "user", Content: []contentBlock{{Type: "text", Text: user}}}},
		MaxTokens: defaultMax,
	}

	body, err := p.post(ctx, apiReq)
	if err != nil {
		debug.Logf("claude.Explain: post err=%v", err)
		return nil, err
	}
	debug.Logf("claude.Explain: HTTP 200, bodyLen=%d", len(body))
	var resp messagesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		debug.Logf("claude.Explain: decode err=%v bodyHead=%q", err, httpx.Truncate(string(body), 200))
		return nil, fmt.Errorf("claude: decode: %w", err)
	}
	var raw strings.Builder
	for _, c := range resp.Content {
		if c.Type == "text" {
			raw.WriteString(c.Text)
		}
	}
	debug.Logf("claude.Explain: rawLen=%d stopReason=%q in=%d out=%d cacheRead=%d", raw.Len(), resp.StopReason, resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadInputTokens)
	exp, err := llm.ParseExplainJSON(raw.String())
	if err != nil {
		return nil, err
	}
	exp.Usage = resp.Usage.toLLM()
	return exp, nil
}

func (p *Provider) post(ctx context.Context, req any) ([]byte, error) {
	return httpx.PostJSON(ctx, p.http, req, p.request(false))
}

func (p *Provider) request(stream bool) httpx.Request {
	return httpx.Request{
		URL: endpoint,
		SetHeaders: func(r *http.Request) {
			r.Header.Set("x-api-key", p.apiKey)
			r.Header.Set("anthropic-version", apiVersion)
			if stream {
				r.Header.Set("accept", "text/event-stream")
			}
		},
		MaxAttempts: maxAttempts,
		BackoffCap:  8 * time.Second,
		Retryable:   httpx.RetryableStatus,
		LogTag:      "claude",
	}
}

// Ask streams a response. Tokens are emitted as text deltas; the channel is
// closed when the stream finishes or an error occurs.
func (p *Provider) Ask(ctx context.Context, req llm.AskRequest) (<-chan llm.Token, error) {
	if p.apiKey == "" {
		debug.Logf("claude.Ask: missing API key")
		return nil, errors.New("claude: ANTHROPIC_API_KEY not set")
	}
	debug.Logf("claude.Ask: start path=%q sym=%q histLen=%d qLen=%d sourceLen=%d", req.FocusPath, req.FocusSymbol, len(req.History), len(req.Question), len(req.Source))

	system := []contentBlock{
		{Type: "text", Text: llm.SystemPromptAsk, CacheControl: &cacheControl{Type: "ephemeral"}},
	}
	if req.RepoPrimer != "" {
		system = append(system, contentBlock{
			Type:         "text",
			Text:         "Repo context:\n" + req.RepoPrimer,
			CacheControl: &cacheControl{Type: "ephemeral"},
		})
	}

	var msgs []message
	// Node-scoped Q&A: inject focus context as a stable first user turn so the
	// model can refer to it across follow-ups. Session-scoped Q&A leaves
	// Source empty because each turn carries its own focus tag + source inside
	// req.Question — see internal/tui askCmd.
	if req.Source != "" {
		focus := llm.BuildAskFocus(req)
		msgs = append(msgs, message{Role: "user", Content: []contentBlock{{Type: "text", Text: focus}}})
		msgs = append(msgs, message{Role: "assistant", Content: []contentBlock{{Type: "text", Text: "Ready."}}})
	}
	for _, t := range req.History {
		msgs = append(msgs, message{Role: t.Role, Content: []contentBlock{{Type: "text", Text: t.Content}}})
	}
	msgs = append(msgs, message{Role: "user", Content: []contentBlock{{Type: "text", Text: req.Question}}})

	apiReq := messagesRequest{
		Model:     p.model,
		System:    system,
		Messages:  msgs,
		MaxTokens: defaultMax,
		Stream:    true,
	}
	b, err := json.Marshal(apiReq)
	if err != nil {
		return nil, err
	}
	r := p.request(true)
	r.Body = b
	resp, err := httpx.Do(ctx, p.http, r)
	if err != nil {
		debug.Logf("claude.Ask: doRequest err=%v", err)
		return nil, err
	}

	out := make(chan llm.Token, 16)
	go streamSSE(resp.Body, out)
	return out, nil
}

// streamSSE parses Anthropic's event stream. Emits content_block_delta text
// to out as it arrives, accumulates usage from message_start (input tokens +
// cache reads) and message_delta (output tokens), and emits a final
// usage-only Token before closing.
func streamSSE(body io.ReadCloser, out chan<- llm.Token) {
	defer close(out)
	defer body.Close()
	tokens := 0
	var usage llm.Usage
	defer func() { debug.Logf("claude.streamSSE: done tokens=%d in=%d out=%d", tokens, usage.InputTokens, usage.OutputTokens) }()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Message struct {
				Usage anthropicUsage `json:"usage"`
			} `json:"message"`
			Usage anthropicUsage `json:"usage"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			// message_start.usage carries input/cache counts.
			u := ev.Message.Usage.toLLM()
			usage = usage.Add(u)
		case "message_delta":
			// message_delta.usage updates output count (cumulative, not delta).
			usage.OutputTokens = ev.Usage.OutputTokens
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				tokens++
				out <- llm.Token{Text: ev.Delta.Text}
			}
		}
	}
	if err := sc.Err(); err != nil {
		debug.Logf("claude.streamSSE: scanner err=%v", err)
		out <- llm.Token{Err: err}
		return
	}
	if usage.Total() > 0 {
		final := usage
		out <- llm.Token{Usage: &final}
	}
}
