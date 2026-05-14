package index

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/tsparse"
)

// captureProvider records the last ExplainRequest so tests can assert on the
// IsLong flag without re-implementing the whole Provider plumbing.
type captureProvider struct {
	calls    atomic.Int32
	lastReq  atomic.Pointer[llm.ExplainRequest]
}

func (p *captureProvider) Name() string                { return "fake" }
func (p *captureProvider) Model() string               { return "fake-model" }
func (p *captureProvider) SupportsPromptCaching() bool { return false }

func (p *captureProvider) Explain(ctx context.Context, req llm.ExplainRequest) (*llm.Explanation, error) {
	p.calls.Add(1)
	r := req
	p.lastReq.Store(&r)
	return &llm.Explanation{Prose: "ok"}, nil
}

func (p *captureProvider) Ask(ctx context.Context, req llm.AskRequest) (<-chan llm.Token, error) {
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}

func TestExplainSymbol_FlagsLongFunctionPastThreshold(t *testing.T) {
	root := t.TempDir()
	// Build a Go file with a 250-line function and a 5-line function.
	var b strings.Builder
	b.WriteString("package x\n\nfunc Big() {\n")
	for i := 0; i < 250; i++ {
		b.WriteString("    _ = 0\n")
	}
	b.WriteString("}\n\nfunc Small() {\n  _ = 0\n}\n")
	if err := writeFile(filepath.Join(root, "x.go"), b.String()); err != nil {
		t.Fatal(err)
	}

	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	p := &captureProvider{}
	g := NewGenerator(root, c, p, nil)
	g.LongFunctionThreshold = 200

	ctx := context.Background()
	// Skip file-level so the symbol fileSummary just stays empty.
	if _, err := g.ExplainSymbol(ctx, "x.go", "Big", ""); err != nil {
		t.Fatalf("Big: %v", err)
	}
	if got := p.lastReq.Load(); got == nil || !got.IsLong {
		t.Errorf("expected IsLong=true for Big function; req=%+v", got)
	}

	if _, err := g.ExplainSymbol(ctx, "x.go", "Small", ""); err != nil {
		t.Fatalf("Small: %v", err)
	}
	if got := p.lastReq.Load(); got == nil || got.IsLong {
		t.Errorf("expected IsLong=false for Small function; req=%+v", got)
	}
}

func TestExplainSymbol_CalleesNilWithoutLSP(t *testing.T) {
	root := t.TempDir()
	src := "package x\n\nfunc Outer() {\n  Inner()\n}\n\nfunc Inner() {}\n"
	if err := writeFile(filepath.Join(root, "x.go"), src); err != nil {
		t.Fatal(err)
	}
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	p := &captureProvider{}
	// LSP=nil — lookupCallees must short-circuit and produce no findings.
	g := NewGenerator(root, c, p, nil)

	exp, err := g.ExplainSymbol(context.Background(), "x.go", "Outer", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Metadata.Callees) != 0 {
		t.Errorf("expected no callees without LSP; got %+v", exp.Metadata.Callees)
	}
	// And the request should still go out cleanly with empty Callees.
	if got := p.lastReq.Load(); got == nil || len(got.Callees) != 0 {
		t.Errorf("req.Callees should be empty without LSP; got %+v", got)
	}
}

func TestContainingSymbolName_PicksInnermost(t *testing.T) {
	root := t.TempDir()
	src := `package x

func A() {
	helper()
}

func helper() {
	doThing()
}
`
	if err := writeFile(filepath.Join(root, "x.go"), src); err != nil {
		t.Fatal(err)
	}
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	g := NewGenerator(root, c, &captureProvider{}, nil)

	abs := filepath.Join(root, "x.go")
	cache := map[string]*tsparse.ParsedFile{}
	// Line 4 (helper() call) is inside A's body.
	if got := g.containingSymbolName(context.Background(), abs, 4, cache); got != "A" {
		t.Errorf("line 4 containing symbol = %q, want A", got)
	}
	// Line 8 (doThing() call) is inside helper's body.
	if got := g.containingSymbolName(context.Background(), abs, 8, cache); got != "helper" {
		t.Errorf("line 8 containing symbol = %q, want helper", got)
	}
	// Line 1 (package decl) is at file scope — no symbol contains it.
	if got := g.containingSymbolName(context.Background(), abs, 1, cache); got != "" {
		t.Errorf("line 1 containing symbol = %q, want \"\" (file scope)", got)
	}
	// Cache hit on second call to same path shouldn't crash and should return
	// the same answer.
	if got := g.containingSymbolName(context.Background(), abs, 4, cache); got != "A" {
		t.Errorf("cached line 4 = %q, want A", got)
	}
}

func TestExplainSymbol_LongFunctionThresholdZeroDisables(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package x\n\nfunc Big() {\n")
	for i := 0; i < 500; i++ {
		b.WriteString("    _ = 0\n")
	}
	b.WriteString("}\n")
	if err := writeFile(filepath.Join(root, "x.go"), b.String()); err != nil {
		t.Fatal(err)
	}
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })

	p := &captureProvider{}
	g := NewGenerator(root, c, p, nil)
	// LongFunctionThreshold left at 0 — feature disabled.

	if _, err := g.ExplainSymbol(context.Background(), "x.go", "Big", ""); err != nil {
		t.Fatalf("Big: %v", err)
	}
	if got := p.lastReq.Load(); got == nil || got.IsLong {
		t.Errorf("expected IsLong=false when threshold disabled; req.IsLong=%v", got.IsLong)
	}
}
