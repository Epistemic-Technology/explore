package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mikethicke/explore/internal/llm"
)

func TestExplain_DecodesJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
			t.Errorf("Explain should request response_format=json_object, got %+v", req.ResponseFormat)
		}
		if req.Stream {
			t.Errorf("Explain should not stream")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"prose\":\"hi\",\"metadata\":{\"key_types\":[\"T\"]}}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := New("test-key", "gpt-test", srv.URL)
	exp, err := p.Explain(context.Background(), llm.ExplainRequest{Level: llm.LevelFile, Path: "x.go", Source: "package x"})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if exp.Prose != "hi" {
		t.Errorf("Prose = %q, want %q", exp.Prose, "hi")
	}
	if len(exp.Metadata.KeyTypes) != 1 || exp.Metadata.KeyTypes[0] != "T" {
		t.Errorf("KeyTypes = %v, want [T]", exp.Metadata.KeyTypes)
	}
}

func TestAsk_StreamsDeltasUntilDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !req.Stream {
			t.Errorf("Ask should set stream=true")
		}
		if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Errorf("Ask should request stream_options.include_usage=true to surface token counts")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Two content deltas, an empty-content delta (ignored), a malformed line,
		// then the usage-only chunk, then [DONE].
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{}}]}\n\n")
		_, _ = io.WriteString(w, "data: not-json\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":42,\"completion_tokens\":7}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := New("k", "gpt-test", srv.URL)
	ch, err := p.Ask(context.Background(), llm.AskRequest{FocusPath: "x.go", Source: "package x", Question: "?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	var got strings.Builder
	var finalUsage *llm.Usage
	for tok := range ch {
		if tok.Err != nil {
			t.Fatalf("stream err: %v", tok.Err)
		}
		got.WriteString(tok.Text)
		if tok.Usage != nil {
			finalUsage = tok.Usage
		}
	}
	if got.String() != "Hello" {
		t.Errorf("streamed text = %q, want %q", got.String(), "Hello")
	}
	if finalUsage == nil || finalUsage.InputTokens != 42 || finalUsage.OutputTokens != 7 {
		t.Errorf("final usage = %+v, want {InputTokens:42 OutputTokens:7}", finalUsage)
	}
}

func TestExplain_MissingAPIKey(t *testing.T) {
	p := New("", "", "")
	if _, err := p.Explain(context.Background(), llm.ExplainRequest{}); err == nil {
		t.Fatal("want error for missing API key")
	}
}
