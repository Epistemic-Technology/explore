package index

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mikethicke/explore/internal/cache"
)

func TestExplainCommit_PromptShapeAndCaching(t *testing.T) {
	root := t.TempDir()
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	p := &captureProvider{}
	g := NewGenerator(root, c, p, nil)
	ctx := context.Background()

	const sha = "abc123"
	const msg = "fix the thing"
	diff := "diff --git a/x.go b/x.go\n@@ -1 +1 @@\n-old\n+new\n"

	exp, err := g.ExplainCommit(ctx, sha, msg, diff)
	if err != nil {
		t.Fatalf("ExplainCommit: %v", err)
	}
	if exp == nil || exp.Prose != "ok" {
		t.Fatalf("unexpected explanation: %+v", exp)
	}
	req := p.lastReq.Load()
	if req == nil || !req.IsDiff {
		t.Fatalf("expected IsDiff request, got %+v", req)
	}
	if req.CommitMessage != msg || req.Diff != diff || req.Level != "commit" {
		t.Fatalf("commit request fields wrong: %+v", req)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", p.calls.Load())
	}

	// Second call with same sha+diff → cache hit, no extra provider call.
	if _, err := g.ExplainCommit(ctx, sha, msg, diff); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("expected cache hit; calls = %d", p.calls.Load())
	}

	// Regenerate bypasses the cache.
	if _, err := g.ExplainCommit(WithRegenerate(ctx), sha, msg, diff); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("regenerate should re-call provider; calls = %d", p.calls.Load())
	}
}

func TestExplainCommit_DiffTruncatedInPrompt(t *testing.T) {
	root := t.TempDir()
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	p := &captureProvider{}
	g := NewGenerator(root, c, p, nil)

	big := strings.Repeat("+line\n", commitDiffPromptCap) // well over the cap
	if _, err := g.ExplainCommit(context.Background(), "deadbeef", "huge", big); err != nil {
		t.Fatal(err)
	}
	req := p.lastReq.Load()
	if req == nil {
		t.Fatal("no request captured")
	}
	if len(req.Diff) > commitDiffPromptCap+len("\n... [diff truncated]") {
		t.Fatalf("diff not truncated: len=%d", len(req.Diff))
	}
	if !strings.Contains(req.Diff, "[diff truncated]") {
		t.Fatal("expected truncation marker in prompt diff")
	}
}

func TestExplainChange_CachesByDiff(t *testing.T) {
	root := t.TempDir()
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p := &captureProvider{}
	g := NewGenerator(root, c, p, nil)
	ctx := context.Background()

	diff := "@@ -1 +1 @@\n-a\n+b\n"
	if _, err := g.ExplainChange(ctx, "x.go", "Foo", "tweak Foo", diff); err != nil {
		t.Fatal(err)
	}
	req := p.lastReq.Load()
	if req == nil || !req.IsDiff || req.Path != "x.go" || req.Symbol != "Foo" {
		t.Fatalf("bad change request: %+v", req)
	}
	if _, err := g.ExplainChange(ctx, "x.go", "Foo", "tweak Foo", diff); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("expected cache hit; calls=%d", p.calls.Load())
	}
	// Different diff text → distinct cache entry.
	if _, err := g.ExplainChange(ctx, "x.go", "Foo", "tweak Foo", diff+"\n+more\n"); err != nil {
		t.Fatal(err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("new diff should miss cache; calls=%d", p.calls.Load())
	}
}
