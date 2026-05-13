package ollama

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

func TestExplain_RequestsJSONFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Format != "json" {
			t.Errorf("Explain should set format=json, got %q", req.Format)
		}
		if req.Stream {
			t.Errorf("Explain should not stream")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"{\"prose\":\"local hi\",\"metadata\":{}}"},"done":true}`))
	}))
	defer srv.Close()

	p := New("qwen2.5-coder:14b", srv.URL)
	exp, err := p.Explain(context.Background(), llm.ExplainRequest{Level: llm.LevelFile, Path: "x.go", Source: "package x"})
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if exp.Prose != "local hi" {
		t.Errorf("Prose = %q, want %q", exp.Prose, "local hi")
	}
}

func TestAsk_ParsesNDJSONAndStopsOnDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !req.Stream {
			t.Errorf("Ask should set stream=true")
		}
		flusher, _ := w.(http.Flusher)
		writeLine := func(s string) {
			_, _ = io.WriteString(w, s+"\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeLine(`{"message":{"role":"assistant","content":"Hel"},"done":false}`)
		writeLine(`{"message":{"role":"assistant","content":"lo"},"done":false}`)
		writeLine(`{"message":{"role":"assistant","content":""},"done":true}`)
		// A trailing line after done — should be ignored.
		writeLine(`{"message":{"role":"assistant","content":"after"},"done":false}`)
	}))
	defer srv.Close()

	p := New("qwen2.5-coder:14b", srv.URL)
	ch, err := p.Ask(context.Background(), llm.AskRequest{FocusPath: "x.go", Source: "package x", Question: "?"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	var got strings.Builder
	for tok := range ch {
		if tok.Err != nil {
			t.Fatalf("stream err: %v", tok.Err)
		}
		got.WriteString(tok.Text)
	}
	if got.String() != "Hello" {
		t.Errorf("streamed text = %q, want %q", got.String(), "Hello")
	}
}

func TestNew_DefaultsAreApplied(t *testing.T) {
	p := New("", "")
	if p.Model() != "qwen2.5-coder:14b" {
		t.Errorf("default model = %q", p.Model())
	}
	if p.host != defaultHost {
		t.Errorf("default host = %q", p.host)
	}
}
