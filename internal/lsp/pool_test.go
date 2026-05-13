package lsp

import (
	"context"
	"testing"
)

func TestPool_UnknownLanguageReturnsNil(t *testing.T) {
	p := NewPool(t.TempDir(), []ServerConfig{})
	c, err := p.ClientFor(context.Background(), "go")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if c != nil {
		t.Fatalf("expected nil client for unconfigured language")
	}
	// And it should mark unavailable so a second call doesn't retry.
	p.mu.Lock()
	if !p.unavailable["go"] {
		t.Errorf("expected go to be marked unavailable")
	}
	p.mu.Unlock()
}

func TestPool_MissingBinaryDegrades(t *testing.T) {
	p := NewPool(t.TempDir(), []ServerConfig{
		{Language: "go", Bin: "this-binary-definitely-does-not-exist-explore-test"},
	})
	c, err := p.ClientFor(context.Background(), "go")
	if err != nil {
		t.Fatalf("ClientFor: %v", err)
	}
	if c != nil {
		t.Fatalf("expected nil client for missing binary")
	}
	// Subsequent calls should still return nil quickly without retry.
	c2, err := p.ClientFor(context.Background(), "go")
	if err != nil || c2 != nil {
		t.Fatalf("second call: c=%v err=%v", c2, err)
	}
}

func TestPool_NilSafe(t *testing.T) {
	var p *Pool
	c, err := p.ClientFor(context.Background(), "go")
	if err != nil || c != nil {
		t.Fatalf("nil pool should return (nil, nil); got c=%v err=%v", c, err)
	}
	p.Close() // must not panic
}

func TestPool_CloseIdempotent(t *testing.T) {
	p := NewPool(t.TempDir(), []ServerConfig{})
	p.Close()
	p.Close()
}

func TestDefaultServerConfigs_HasAllLanguages(t *testing.T) {
	cfgs := DefaultServerConfigs()
	got := map[string]bool{}
	for _, c := range cfgs {
		got[c.Language] = true
	}
	for _, want := range []string{"go", "python", "typescript", "tsx", "rust", "ruby", "java", "cpp"} {
		if !got[want] {
			t.Errorf("DefaultServerConfigs missing %q", want)
		}
	}
}
