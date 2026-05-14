package lsp

import (
	"context"
	"errors"
	"strings"
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

func TestClient_DeadFailsFastWithoutWrite(t *testing.T) {
	// Build a stub client by hand — Start() requires a real binary, but we
	// only need to exercise call()'s fast-path when dead.
	c := &Client{
		pending: map[int]chan rpcResponse{},
		opened:  map[string]bool{},
	}
	c.markDead(errors.New("simulated crash"))
	if c.IsAlive() {
		t.Fatalf("IsAlive should be false after markDead")
	}
	err := c.call(context.Background(), "textDocument/references", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "lsp server unavailable") {
		t.Errorf("call after death should fail fast; got err=%v", err)
	}
}

func TestPool_EvictsDeadClientOnNextRequest(t *testing.T) {
	p := NewPool(t.TempDir(), []ServerConfig{
		{Language: "go", Bin: "this-binary-definitely-does-not-exist"},
	})
	// First call: binary missing → marked unavailable.
	c1, _ := p.ClientFor(context.Background(), "go")
	if c1 != nil {
		t.Fatalf("expected nil client for missing binary")
	}
	// Manually inject a "live" stub client so we can verify eviction.
	stub := &Client{
		pending: map[int]chan rpcResponse{},
		opened:  map[string]bool{},
	}
	p.mu.Lock()
	delete(p.unavailable, "go") // un-mark so ClientFor will inspect the cached client
	p.clients["go"] = stub
	p.mu.Unlock()

	// Liveness check: stub is alive → returned as-is.
	c, err := p.ClientFor(context.Background(), "go")
	if err != nil || c != stub {
		t.Fatalf("alive cached client should be returned; got c=%v err=%v", c, err)
	}

	// Kill the stub. Next ClientFor should evict it and try to start a fresh
	// one — but our config still points at a missing binary, so it returns nil
	// and re-marks unavailable. The eviction is the part we care about.
	stub.markDead(errors.New("simulated"))
	p.mu.Lock()
	delete(p.unavailable, "go")
	p.mu.Unlock()

	c, err = p.ClientFor(context.Background(), "go")
	if err != nil {
		t.Fatalf("expected err nil; got %v", err)
	}
	if c != nil {
		t.Fatalf("expected nil (start fails due to missing binary); got %v", c)
	}
	// The dead client should no longer be in the cache.
	p.mu.Lock()
	_, stillCached := p.clients["go"]
	p.mu.Unlock()
	if stillCached {
		t.Errorf("dead client should have been evicted from pool.clients")
	}
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
