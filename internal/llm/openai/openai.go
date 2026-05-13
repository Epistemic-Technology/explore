// Package openai adapts the OpenAI Chat Completions API to llm.Provider.
// Mirrors the Claude adapter's shape (SSE streaming, exponential backoff
// retry) but does not advertise prompt caching — OpenAI does some
// server-side caching transparently, but we have no per-call lever.
package openai

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

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []message       `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
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
		debug.Logf("openai.Explain: decode err=%v bodyHead=%q", err, truncate(string(body), 200))
		return nil, fmt.Errorf("openai: decode: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("openai: empty choices")
	}
	raw := resp.Choices[0].Message.Content
	debug.Logf("openai.Explain: rawLen=%d finishReason=%q", len(raw), resp.Choices[0].FinishReason)
	return llm.ParseExplainJSON(raw)
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
		Model:     p.model,
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
		debug.Logf("openai.Ask: doRequest err=%v", err)
		return nil, err
	}

	out := make(chan llm.Token, 16)
	go streamSSE(resp.Body, out)
	return out, nil
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

// doRequest sends with exponential-backoff retry on transient failures. On
// success, the caller owns resp.Body; on error it has been drained/closed.
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
		req, err := http.NewRequestWithContext(ctx, "POST", p.endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		resp, err := p.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				debug.Logf("openai.doRequest: ctx canceled attempt=%d err=%v", attempt, ctx.Err())
				return nil, ctx.Err()
			}
			debug.Logf("openai.doRequest: transport err attempt=%d err=%v", attempt, err)
			lastErr = err
			nextWait = backoffFor(attempt)
			continue
		}
		if resp.StatusCode == 200 {
			debug.Logf("openai.doRequest: 200 attempt=%d stream=%v", attempt, stream)
			return resp, nil
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		debug.Logf("openai.doRequest: status=%s attempt=%d body=%q", resp.Status, attempt, truncate(string(respBody), 300))
		lastErr = fmt.Errorf("openai: %s: %s", resp.Status, truncate(string(respBody), 300))
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
	case 408, 425, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

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

// streamSSE parses OpenAI's event stream and emits choices[0].delta.content
// tokens. Closes out and the body when done. Terminates on "data: [DONE]".
func streamSSE(body io.ReadCloser, out chan<- llm.Token) {
	defer close(out)
	defer body.Close()
	tokens := 0
	defer func() { debug.Logf("openai.streamSSE: done tokens=%d", tokens) }()
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
			return
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
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
	}
}
