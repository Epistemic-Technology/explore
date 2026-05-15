// Package openai adapts the OpenAI Chat Completions API to llm.Provider.
// Mirrors the Claude adapter's shape (SSE streaming, exponential backoff
// retry) but does not advertise prompt caching — OpenAI does some
// server-side caching transparently, but we have no per-call lever.
package openai

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
	defaultEndpoint = "https://api.openai.com/v1/chat/completions"
	defaultMax      = 1024
	maxAttempts     = 4
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []message       `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	StreamOptions  *streamOptions  `json:"stream_options,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

func (u openaiUsage) toLLM() llm.Usage {
	return llm.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
}

type chatResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage openaiUsage `json:"usage"`
}

type Provider struct {
	apiKey   string
	model    string
	endpoint string
	http     *http.Client
}

// New builds an OpenAI provider. model defaults to gpt-4o-mini when empty;
// endpoint defaults to the public API. Use a custom endpoint for proxies or
// Azure-compatible deployments.
func New(apiKey, model, endpoint string) *Provider {
	if model == "" {
		model = "gpt-4o-mini"
	}
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Provider{
		apiKey:   apiKey,
		model:    model,
		endpoint: endpoint,
		http:     &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *Provider) Name() string                { return "openai" }
func (p *Provider) Model() string               { return p.model }
func (p *Provider) SupportsPromptCaching() bool { return false }

func (p *Provider) Explain(ctx context.Context, req llm.ExplainRequest) (*llm.Explanation, error) {
	if p.apiKey == "" {
		debug.Logf("openai.Explain: missing API key (level=%s path=%q)", req.Level, req.Path)
		return nil, errors.New("openai: OPENAI_API_KEY not set")
	}
	debug.Logf("openai.Explain: start level=%s path=%q sym=%q sourceLen=%d primerLen=%d model=%q", req.Level, req.Path, req.Symbol, len(req.Source), len(req.RepoPrimer), p.model)

	system := llm.SystemPromptExplain
	if req.RepoPrimer != "" {
		system += "\n\nRepo context:\n" + req.RepoPrimer
	}
	apiReq := chatRequest{
		Model: p.model,
		Messages: []message{
			{Role: "system", Content: system},
			{Role: "user", Content: llm.BuildExplainUser(req)},
		},
		MaxTokens:      defaultMax,
		ResponseFormat: &responseFormat{Type: "json_object"},
	}

	body, err := p.post(ctx, apiReq)
	if err != nil {
		debug.Logf("openai.Explain: post err=%v", err)
		return nil, err
	}
	debug.Logf("openai.Explain: HTTP 200, bodyLen=%d", len(body))
	var resp chatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		debug.Logf("openai.Explain: decode err=%v bodyHead=%q", err, httpx.Truncate(string(body), 200))
		return nil, fmt.Errorf("openai: decode: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("openai: empty choices")
	}
	raw := resp.Choices[0].Message.Content
	debug.Logf("openai.Explain: rawLen=%d finishReason=%q in=%d out=%d", len(raw), resp.Choices[0].FinishReason, resp.Usage.PromptTokens, resp.Usage.CompletionTokens)
	exp, err := llm.ParseExplainJSON(raw)
	if err != nil {
		return nil, err
	}
	exp.Usage = resp.Usage.toLLM()
	return exp, nil
}

func (p *Provider) Ask(ctx context.Context, req llm.AskRequest) (<-chan llm.Token, error) {
	if p.apiKey == "" {
		debug.Logf("openai.Ask: missing API key")
		return nil, errors.New("openai: OPENAI_API_KEY not set")
	}
	debug.Logf("openai.Ask: start path=%q sym=%q histLen=%d qLen=%d sourceLen=%d", req.FocusPath, req.FocusSymbol, len(req.History), len(req.Question), len(req.Source))

	system := llm.SystemPromptAsk
	if req.RepoPrimer != "" {
		system += "\n\nRepo context:\n" + req.RepoPrimer
	}

	msgs := []message{{Role: "system", Content: system}}
	// Same node-vs-session split as the Claude adapter — see internal/llm/claude/claude.go Ask.
	if req.Source != "" {
		msgs = append(msgs,
			message{Role: "user", Content: llm.BuildAskFocus(req)},
			message{Role: "assistant", Content: "Ready."},
		)
	}
	for _, t := range req.History {
		msgs = append(msgs, message{Role: t.Role, Content: t.Content})
	}
	msgs = append(msgs, message{Role: "user", Content: req.Question})

	apiReq := chatRequest{
		Model:         p.model,
		Messages:      msgs,
		MaxTokens:     defaultMax,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	b, err := json.Marshal(apiReq)
	if err != nil {
		return nil, err
	}
	r := p.request(true)
	r.Body = b
	resp, err := httpx.Do(ctx, p.http, r)
	if err != nil {
		debug.Logf("openai.Ask: doRequest err=%v", err)
		return nil, err
	}

	out := make(chan llm.Token, 16)
	go streamSSE(resp.Body, out)
	return out, nil
}

func (p *Provider) post(ctx context.Context, req any) ([]byte, error) {
	return httpx.PostJSON(ctx, p.http, req, p.request(false))
}

func (p *Provider) request(stream bool) httpx.Request {
	return httpx.Request{
		URL: p.endpoint,
		SetHeaders: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+p.apiKey)
			if stream {
				r.Header.Set("Accept", "text/event-stream")
			}
		},
		MaxAttempts: maxAttempts,
		BackoffCap:  8 * time.Second,
		Retryable:   httpx.RetryableStatus,
		LogTag:      "openai",
	}
}

// streamSSE parses OpenAI's event stream and emits choices[0].delta.content
// tokens. Closes out and the body when done. When stream_options.include_usage
// is set, the chunk just before "[DONE]" has an empty choices list and a
// usage field — that becomes a final usage-only Token.
func streamSSE(body io.ReadCloser, out chan<- llm.Token) {
	defer close(out)
	defer body.Close()
	tokens := 0
	var usage llm.Usage
	defer func() { debug.Logf("openai.streamSSE: done tokens=%d in=%d out=%d", tokens, usage.InputTokens, usage.OutputTokens) }()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *openaiUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if ev.Usage != nil {
			usage = ev.Usage.toLLM()
		}
		if len(ev.Choices) == 0 {
			continue
		}
		if t := ev.Choices[0].Delta.Content; t != "" {
			tokens++
			out <- llm.Token{Text: t}
		}
	}
	if err := sc.Err(); err != nil {
		debug.Logf("openai.streamSSE: scanner err=%v", err)
		out <- llm.Token{Err: err}
		return
	}
	if usage.Total() > 0 {
		final := usage
		out <- llm.Token{Usage: &final}
	}
}
