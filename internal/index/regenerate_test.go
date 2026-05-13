package index

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/llm"
)

// countingProvider implements llm.Provider for counting Explain invocations.
type countingProvider struct {
	calls atomic.Int32
	prose string
}

func (p *countingProvider) Name() string                { return "fake" }
func (p *countingProvider) Model() string               { return "fake-model" }
func (p *countingProvider) SupportsPromptCaching() bool { return false }

func (p *countingProvider) Explain(ctx context.Context, req llm.ExplainRequest) (*llm.Explanation, error) {
	n := p.calls.Add(1)
	prose := p.prose
	if prose == "" {
		prose = "call"
	}
	return &llm.Explanation{
		Prose: prose + "-" + itoa(n),
		Usage: llm.Usage{InputTokens: 100, OutputTokens: 50},
	}, nil
}

func (p *countingProvider) Ask(ctx context.Context, req llm.AskRequest) (<-chan llm.Token, error) {
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}

func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestExplainFile_RegenerateBypassesBBoltCache(t *testing.T) {
	root := t.TempDir()
	src := "package x\n\nfunc Hello() string { return \"hi\" }\n"
	if err := writeFile(filepath.Join(root, "x.go"), src); err != nil {
		t.Fatal(err)
	}
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	p := &countingProvider{prose: "v"}
	g := NewGenerator(root, c, p, nil)

	ctx := context.Background()
	exp1, err := g.ExplainFile(ctx, "x.go")
	if err != nil {
		t.Fatalf("first ExplainFile: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("first call: provider calls = %d, want 1", p.calls.Load())
	}

	exp2, err := g.ExplainFile(ctx, "x.go")
	if err != nil {
		t.Fatalf("second ExplainFile: %v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatalf("second call should hit cache: provider calls = %d, want 1", p.calls.Load())
	}
	if exp2.Prose != exp1.Prose {
		t.Fatalf("cached result diverged: %q vs %q", exp2.Prose, exp1.Prose)
	}

	exp3, err := g.ExplainFile(WithRegenerate(ctx), "x.go")
	if err != nil {
		t.Fatalf("regenerate ExplainFile: %v", err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("regenerate should bypass cache: provider calls = %d, want 2", p.calls.Load())
	}
	if exp3.Prose == exp1.Prose {
		t.Fatalf("regenerate should produce a fresh result, got same %q", exp3.Prose)
	}

	// A subsequent normal call should now see the regenerated entry in BBolt.
	exp4, err := g.ExplainFile(ctx, "x.go")
	if err != nil {
		t.Fatalf("post-regen ExplainFile: %v", err)
	}
	if p.calls.Load() != 2 {
		t.Fatalf("post-regen should hit cache: provider calls = %d, want 2", p.calls.Load())
	}
	if exp4.Prose != exp3.Prose {
		t.Fatalf("post-regen cache returned %q, want regenerated %q", exp4.Prose, exp3.Prose)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestOnUsage_FiresOnMissNotOnHit(t *testing.T) {
	root := t.TempDir()
	if err := writeFile(filepath.Join(root, "x.go"), "package x\n\nfunc A() {}\n"); err != nil {
		t.Fatal(err)
	}
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	p := &countingProvider{}
	g := NewGenerator(root, c, p, nil)
	var total llm.Usage
	g.OnUsage = func(u llm.Usage) { total = total.Add(u) }

	ctx := context.Background()
	if _, err := g.ExplainFile(ctx, "x.go"); err != nil {
		t.Fatal(err)
	}
	if got := total.Total(); got != 150 {
		t.Fatalf("after miss: total = %d, want 150", got)
	}

	// Cache hit — OnUsage must not fire.
	if _, err := g.ExplainFile(ctx, "x.go"); err != nil {
		t.Fatal(err)
	}
	if got := total.Total(); got != 150 {
		t.Fatalf("after hit: total = %d, want 150 (no double count)", got)
	}

	// Regenerate — OnUsage fires again.
	if _, err := g.ExplainFile(WithRegenerate(ctx), "x.go"); err != nil {
		t.Fatal(err)
	}
	if got := total.Total(); got != 300 {
		t.Fatalf("after regen: total = %d, want 300", got)
	}
}
