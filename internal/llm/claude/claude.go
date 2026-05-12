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
	StopReason string `json:"stop_reason"`
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

func (p *Provider) Name() string                  { return "claude" }
func (p *Provider) Model() string                 { return p.model }
func (p *Provider) SupportsPromptCaching() bool   { return true }

const systemPromptExplain = `You are explore, an assistant that explains code at multiple zoom levels for a developer reading an unfamiliar codebase.

Rules:
- Be concrete. Reference identifiers from the source. Avoid generic phrases like "this code does X".
- Prose: 3-6 sentences, plain text, no headings. Lead with purpose, then mechanism, then anything surprising.
- Output strict JSON only, no markdown fences, matching:
  {"prose": "...", "metadata": {"imports": [], "key_types": [], "gotchas": []}}
- key_types: 0-5 most important types/structs the reader should know about.
- gotchas: 0-3 non-obvious behaviors, footguns, or invariants — only if real.
- If the snippet is trivial, say so briefly and keep metadata arrays empty.`

const systemPromptAsk = `You are explore, helping a developer understand a specific piece of code they are looking at.

Rules:
- Answer the user's question grounded in the provided source. Quote identifiers verbatim.
- If the answer is not derivable from the source provided, say so and suggest what to look at next.
- Be concise. Plain prose, no JSON wrapper. Use short paragraphs and inline code spans for symbols.`

func (p *Provider) Explain(ctx context.Context, req llm.ExplainRequest) (*llm.Explanation, error) {
	if p.apiKey == "" {
		debug.Logf("claude.Explain: missing API key (level=%s path=%q)", req.Level, req.Path)
		return nil, errors.New("claude: ANTHROPIC_API_KEY not set")
	}
	debug.Logf("claude.Explain: start level=%s path=%q sym=%q sourceLen=%d primerLen=%d model=%q", req.Level, req.Path, req.Symbol, len(req.Source), len(req.RepoPrimer), p.model)

	system := []contentBlock{
		{Type: "text", Text: systemPromptExplain, CacheControl: &cacheControl{Type: "ephemeral"}},
	}
	if req.RepoPrimer != "" {
		system = append(system, contentBlock{
			Type:         "text",
			Text:         "Repo context:\n" + req.RepoPrimer,
			CacheControl: &cacheControl{Type: "ephemeral"},
		})
	}

	user := buildExplainUserMessage(req)
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
	debug.Logf("claude.Explain: rawLen=%d stopReason=%q", raw.Len(), resp.StopReason)
	return parseExplainJSON(raw.String())
}

func buildExplainUserMessage(req llm.ExplainRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Level: %s\nPath: %s\n", req.Level, req.Path)
	if req.Symbol != "" {
		fmt.Fprintf(&b, "Symbol: %s\n", req.Symbol)
	}
	if req.Signature != "" {
		fmt.Fprintf(&b, "Signature: %s\n", req.Signature)
	}
	if len(req.Imports) > 0 {
		fmt.Fprintf(&b, "Imports: %s\n", strings.Join(req.Imports, ", "))
	}
	if len(req.Callers) > 0 {
		fmt.Fprintf(&b, "Callers: %s\n", strings.Join(req.Callers, ", "))
	}
	if len(req.Callees) > 0 {
		fmt.Fprintf(&b, "Callees: %s\n", strings.Join(req.Callees, ", "))
	}
	if req.ParentSummary != "" {
		fmt.Fprintf(&b, "Parent summary: %s\n", req.ParentSummary)
	}
	b.WriteString("\nSource:\n```\n")
	b.WriteString(req.Source)
	b.WriteString("\n```\n")
	b.WriteString("\nReturn only the JSON object described in the system prompt.")
	return b.String()
}

// parseExplainJSON tolerates a stray code fence around the JSON object.
func parseExplainJSON(s string) (*llm.Explanation, error) {
	s = strings.TrimSpace(s)
	// Strip leading/trailing ``` fences if the model added them.
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	var parsed struct {
		Prose    string       `json:"prose"`
		Metadata llm.Metadata `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		// Fall back to treating the whole response as prose so the UI is never empty.
		return &llm.Explanation{Prose: s}, nil
	}
	return &llm.Explanation{Prose: parsed.Prose, Metadata: parsed.Metadata}, nil
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
		{Type: "text", Text: systemPromptAsk, CacheControl: &cacheControl{Type: "ephemeral"}},
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
		focus := buildAskFocus(req)
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

func buildAskFocus(req llm.AskRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Focus: %s", req.FocusPath)
	if req.FocusSymbol != "" {
		fmt.Fprintf(&b, "::%s", req.FocusSymbol)
	}
	b.WriteString("\n")
	if req.ParentSummary != "" {
		fmt.Fprintf(&b, "Parent summary: %s\n", req.ParentSummary)
	}
	b.WriteString("\nSource:\n```\n")
	b.WriteString(req.Source)
	b.WriteString("\n```\n")
	return b.String()
}

// streamSSE parses Anthropic's event stream and emits content_block_delta text
// to out. Closes out and the body when done.
func streamSSE(body io.ReadCloser, out chan<- llm.Token) {
	defer close(out)
	defer body.Close()
	tokens := 0
	defer func() { debug.Logf("claude.streamSSE: done tokens=%d", tokens) }()
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
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if ev.Type == "content_block_delta" && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
			tokens++
			out <- llm.Token{Text: ev.Delta.Text}
		}
	}
	if err := sc.Err(); err != nil {
		debug.Logf("claude.streamSSE: scanner err=%v", err)
		out <- llm.Token{Err: err}
	}
}
