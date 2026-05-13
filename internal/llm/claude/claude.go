// Package claude adapts the Anthropic Messages API to the llm.Provider interface.
// Uses prompt caching aggressively on the static system prompt and the repo
// primer, so warm explanation calls re-use cached prefix tokens.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/llm"
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
		debug.Logf("claude.Explain: decode err=%v bodyHead=%q", err, truncate(string(body), 200))
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
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	resp, err := p.doRequest(ctx, b, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// doRequest sends a Messages API request with exponential-backoff retry on
// transient failures (529 overloaded, 429 rate-limited, 5xx, network errors).
// On success, the caller owns resp.Body; on error it has been drained/closed.
// stream toggles the SSE accept header.
func (p *Provider) doRequest(ctx context.Context, body []byte, stream bool) (*http.Response, error) {
	var lastErr error
	var nextWait time.Duration
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if nextWait > 0 {
			t := time.NewTimer(nextWait)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", p.apiKey)
		req.Header.Set("anthropic-version", apiVersion)
		if stream {
			req.Header.Set("accept", "text/event-stream")
		}
		resp, err := p.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				debug.Logf("claude.doRequest: ctx canceled attempt=%d err=%v", attempt, ctx.Err())
				return nil, ctx.Err()
			}
			debug.Logf("claude.doRequest: transport err attempt=%d err=%v", attempt, err)
			lastErr = err
			nextWait = backoffFor(attempt)
			continue
		}
		if resp.StatusCode == 200 {
			debug.Logf("claude.doRequest: 200 attempt=%d stream=%v", attempt, stream)
			return resp, nil
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		debug.Logf("claude.doRequest: status=%s attempt=%d body=%q", resp.Status, attempt, truncate(string(respBody), 300))
		lastErr = fmt.Errorf("claude: %s: %s", resp.Status, truncate(string(respBody), 300))
		if !retryableStatus(resp.StatusCode) {
			return nil, lastErr
		}
		nextWait = backoffFor(attempt)
		if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			nextWait = d
		}
	}
	return nil, lastErr
}

func retryableStatus(code int) bool {
	switch code {
	case 408, 425, 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// backoffFor returns the delay before retry attempt (attempt+1): 1s, 2s, 4s,
// 8s, capped, plus up to 50% jitter so concurrent clients don't synchronize.
func backoffFor(attempt int) time.Duration {
	base := time.Second << attempt
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	jitter := time.Duration(rand.Int64N(int64(base / 2)))
	return base + jitter
}

func parseRetryAfter(h string) (time.Duration, bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
	resp, err := p.doRequest(ctx, b, true)
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
