// Package ollama adapts a local Ollama server to llm.Provider for fully
// offline use. Uses /api/chat with NDJSON streaming. No prompt caching;
// Ollama keeps the model in RAM between calls but has no per-call cache lever.
package ollama

import (
	"bufio"
	"context"
	"encoding/json"
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
	defaultHost = "http://localhost:11434"
	maxAttempts = 3
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Stream   bool      `json:"stream"`
	Format   string    `json:"format,omitempty"` // "json" constrains output to valid JSON
}

type chatResponse struct {
	Message         message `json:"message"`
	Done            bool    `json:"done"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
}

func (r chatResponse) usage() llm.Usage {
	return llm.Usage{InputTokens: r.PromptEvalCount, OutputTokens: r.EvalCount}
}

type Provider struct {
	model string
	host  string
	http  *http.Client
}

// New builds an Ollama provider. model defaults to qwen2.5-coder:14b; host
// defaults to http://localhost:11434. Long timeout because local CPU models
// can be slow to first-token.
func New(model, host string) *Provider {
	if model == "" {
		model = "qwen2.5-coder:14b"
	}
	if host == "" {
		host = defaultHost
	}
	host = strings.TrimRight(host, "/")
	return &Provider{
		model: model,
		host:  host,
		http:  &http.Client{Timeout: 5 * time.Minute},
	}
}

func (p *Provider) Name() string                { return "ollama" }
func (p *Provider) Model() string               { return p.model }
func (p *Provider) SupportsPromptCaching() bool { return false }

func (p *Provider) Explain(ctx context.Context, req llm.ExplainRequest) (*llm.Explanation, error) {
	debug.Logf("ollama.Explain: start level=%s path=%q sym=%q sourceLen=%d primerLen=%d model=%q host=%q", req.Level, req.Path, req.Symbol, len(req.Source), len(req.RepoPrimer), p.model, p.host)

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
		Stream: false,
		Format: "json",
	}

	body, err := p.post(ctx, "/api/chat", apiReq)
	if err != nil {
		debug.Logf("ollama.Explain: post err=%v", err)
		return nil, err
	}
	debug.Logf("ollama.Explain: HTTP 200, bodyLen=%d", len(body))
	var resp chatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		debug.Logf("ollama.Explain: decode err=%v bodyHead=%q", err, httpx.Truncate(string(body), 200))
		return nil, fmt.Errorf("ollama: decode: %w", err)
	}
	debug.Logf("ollama.Explain: rawLen=%d done=%v in=%d out=%d", len(resp.Message.Content), resp.Done, resp.PromptEvalCount, resp.EvalCount)
	exp, err := llm.ParseExplainJSON(resp.Message.Content)
	if err != nil {
		return nil, err
	}
	exp.Usage = resp.usage()
	return exp, nil
}

func (p *Provider) Ask(ctx context.Context, req llm.AskRequest) (<-chan llm.Token, error) {
	debug.Logf("ollama.Ask: start path=%q sym=%q histLen=%d qLen=%d sourceLen=%d", req.FocusPath, req.FocusSymbol, len(req.History), len(req.Question), len(req.Source))

	system := llm.SystemPromptAsk
	if req.RepoPrimer != "" {
		system += "\n\nRepo context:\n" + req.RepoPrimer
	}

	msgs := []message{{Role: "system", Content: system}}
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
		Model:    p.model,
		Messages: msgs,
		Stream:   true,
	}
	b, err := json.Marshal(apiReq)
	if err != nil {
		return nil, err
	}
	r := p.request("/api/chat")
	r.Body = b
	resp, err := httpx.Do(ctx, p.http, r)
	if err != nil {
		debug.Logf("ollama.Ask: doRequest err=%v", err)
		return nil, err
	}

	out := make(chan llm.Token, 16)
	go streamNDJSON(resp.Body, out)
	return out, nil
}

func (p *Provider) post(ctx context.Context, path string, req any) ([]byte, error) {
	return httpx.PostJSON(ctx, p.http, req, p.request(path))
}

// request builds an httpx.Request for the given path. Retryable is nil because
// 5xx from Ollama is usually a load/OOM failure that won't self-heal in
// retries — only transport errors retry.
func (p *Provider) request(path string) httpx.Request {
	return httpx.Request{
		URL:         p.host + path,
		MaxAttempts: maxAttempts,
		BackoffCap:  4 * time.Second,
		LogTag:      "ollama",
	}
}

// streamNDJSON parses Ollama's newline-delimited JSON stream, emitting each
// message.content delta. The terminating object (done:true) carries
// prompt_eval_count + eval_count; emit those as a final usage-only Token.
func streamNDJSON(body io.ReadCloser, out chan<- llm.Token) {
	defer close(out)
	defer body.Close()
	tokens := 0
	var usage llm.Usage
	defer func() { debug.Logf("ollama.streamNDJSON: done tokens=%d in=%d out=%d", tokens, usage.InputTokens, usage.OutputTokens) }()
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev chatResponse
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if t := ev.Message.Content; t != "" {
			tokens++
			out <- llm.Token{Text: t}
		}
		if ev.Done {
			usage = ev.usage()
			if usage.Total() > 0 {
				final := usage
				out <- llm.Token{Usage: &final}
			}
			return
		}
	}
	if err := sc.Err(); err != nil {
		debug.Logf("ollama.streamNDJSON: scanner err=%v", err)
		out <- llm.Token{Err: err}
	}
}
