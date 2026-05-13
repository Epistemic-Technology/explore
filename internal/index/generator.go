// Package index orchestrates explanation generation: it walks the repo
// (lazily), invokes tree-sitter for symbol extraction, asks the LSP client
// for cross-references, consults the cache, and only calls the LLM on a miss.
package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/lsp"
	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

// Generator produces Explanations for nodes, caching results.
type Generator struct {
	Root       string
	Cache      *cache.Cache
	Provider   llm.Provider
	LSP        *lsp.Client // may be nil
	RepoPrimer string      // README + AGENTS.md, computed once

	// OnUsage is invoked after every successful Provider.Explain call (cache
	// misses only — cached entries don't bill). May be called from multiple
	// goroutines (the prefetcher runs in parallel), so implementations must
	// be safe for concurrent use. Optional.
	OnUsage func(llm.Usage)
}

// reportUsage is a nil-safe helper to invoke OnUsage. Kept private so the
// dispatch is consistent across ExplainX methods.
func (g *Generator) reportUsage(u llm.Usage) {
	if g.OnUsage != nil && u.Total() > 0 {
		g.OnUsage(u)
	}
}

// regenCtxKey is unexported so callers must go through WithRegenerate.
type regenCtxKey struct{}

// WithRegenerate marks ctx so that the next top-level ExplainX call skips the
// BBolt cache and overwrites whatever was there. Internal sub-lookups
// (e.g. fileBlurb pulling child summaries when building a dir view) are
// unaffected — only the entry-point node is regenerated.
func WithRegenerate(ctx context.Context) context.Context {
	return context.WithValue(ctx, regenCtxKey{}, true)
}

func shouldRegenerate(ctx context.Context) bool {
	v, _ := ctx.Value(regenCtxKey{}).(bool)
	return v
}

func NewGenerator(root string, c *cache.Cache, p llm.Provider, l *lsp.Client) *Generator {
	g := &Generator{Root: root, Cache: c, Provider: p, LSP: l}
	g.RepoPrimer = loadRepoPrimer(root)
	return g
}

func loadRepoPrimer(root string) string {
	var parts []string
	for _, name := range []string{"README.md", "README", "CLAUDE.md", "AGENTS.md"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		if len(data) > 8000 {
			data = data[:8000]
		}
		parts = append(parts, fmt.Sprintf("=== %s ===\n%s", name, string(data)))
	}
	return strings.Join(parts, "\n\n")
}

// ParseFile is a thin wrapper exposed so the TUI can populate the symbol tree
// without going through the LLM.
func (g *Generator) ParseFile(ctx context.Context, relPath string) (*tsparse.ParsedFile, []byte, error) {
	abs := filepath.Join(g.Root, relPath)
	src, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, err
	}
	pf, err := tsparse.Parse(ctx, abs, src)
	return pf, src, err
}

// ExplainFile returns a (possibly cached) explanation for an entire file.
func (g *Generator) ExplainFile(ctx context.Context, relPath string) (*model.Explanation, error) {
	absPath := filepath.Join(g.Root, relPath)
	src, err := os.ReadFile(absPath)
	if err != nil {
		debug.Logf("ExplainFile: ReadFile err path=%q err=%v", relPath, err)
		return nil, err
	}
	hash := cache.HashSource(src)
	key := cache.Key(hash, "file", g.Provider.Model(), cache.PromptVersion)
	if !shouldRegenerate(ctx) {
		if hit, _ := g.Cache.Get(key); hit != nil {
			debug.Logf("ExplainFile: cache hit path=%q", relPath)
			return hit, nil
		}
	}
	debug.Logf("ExplainFile: cache miss path=%q srcLen=%d regen=%v", relPath, len(src), shouldRegenerate(ctx))

	pf, err := tsparse.Parse(ctx, absPath, src)
	if err != nil {
		debug.Logf("ExplainFile: tsparse err path=%q err=%v", relPath, err)
		return nil, err
	}
	view := buildFileView(src, pf)
	req := llm.ExplainRequest{
		Level:      llm.LevelFile,
		Path:       relPath,
		Source:     view,
		Imports:    pf.Imports,
		RepoPrimer: g.RepoPrimer,
	}
	llmExp, err := g.Provider.Explain(ctx, req)
	if err != nil {
		return nil, err
	}
	g.reportUsage(llmExp.Usage)

	exp := &model.Explanation{
		NodeID:     model.NodeID{Kind: model.KindFile, Path: relPath},
		Prose:      llmExp.Prose,
		Metadata:   mergeMeta(llmExp.Metadata, pf),
		SourceHash: hash,
		Model:      g.Provider.Model(),
		PromptVer:  cache.PromptVersion,
		CreatedAt:  time.Now(),
	}
	exp.Metadata.LOC = countLines(src)
	_ = g.Cache.Put(key, exp)
	return exp, nil
}

// ExplainSymbol returns a (possibly cached) explanation for a single symbol.
// fileSummary is the parent file's prose; passed as priming context.
func (g *Generator) ExplainSymbol(ctx context.Context, relPath, symbolName, fileSummary string) (*model.Explanation, error) {
	absPath := filepath.Join(g.Root, relPath)
	src, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	pf, err := tsparse.Parse(ctx, absPath, src)
	if err != nil {
		return nil, err
	}
	sym, ok := findSymbol(pf, symbolName)
	if !ok {
		debug.Logf("ExplainSymbol: not found path=%q sym=%q", relPath, symbolName)
		return nil, fmt.Errorf("symbol %s not found in %s", symbolName, relPath)
	}
	source := string(tsparse.SymbolSource(src, sym))
	hash := cache.HashSource([]byte(source))
	key := cache.Key(hash, "symbol", g.Provider.Model(), cache.PromptVersion)
	if !shouldRegenerate(ctx) {
		if hit, _ := g.Cache.Get(key); hit != nil {
			debug.Logf("ExplainSymbol: cache hit path=%q sym=%q", relPath, symbolName)
			return hit, nil
		}
	}
	debug.Logf("ExplainSymbol: cache miss path=%q sym=%q sourceLen=%d regen=%v", relPath, symbolName, len(source), shouldRegenerate(ctx))

	callers := g.lookupCallers(ctx, absPath, sym)

	req := llm.ExplainRequest{
		Level:         llm.LevelSymbol,
		Path:          relPath,
		Symbol:        symbolName,
		Signature:     sym.Signature,
		Source:        source,
		Imports:       pf.Imports,
		Callers:       refsToStrings(callers),
		ParentSummary: fileSummary,
		RepoPrimer:    g.RepoPrimer,
	}
	llmExp, err := g.Provider.Explain(ctx, req)
	if err != nil {
		return nil, err
	}
	g.reportUsage(llmExp.Usage)
	exp := &model.Explanation{
		NodeID: model.NodeID{Kind: model.KindSymbol, Path: relPath, Symbol: symbolName},
		Prose:  llmExp.Prose,
		Metadata: model.Metadata{
			Imports:  llmExp.Metadata.Imports,
			Callers:  callers,
			KeyTypes: llmExp.Metadata.KeyTypes,
			Gotchas:  llmExp.Metadata.Gotchas,
			LOC:      sym.EndLine - sym.StartLine + 1,
		},
		SourceHash: hash,
		Model:      g.Provider.Model(),
		PromptVer:  cache.PromptVersion,
		CreatedAt:  time.Now(),
	}
	_ = g.Cache.Put(key, exp)
	return exp, nil
}

// SymbolSource reads the symbol's source slice. Used by Q&A.
func (g *Generator) SymbolSource(ctx context.Context, relPath, symbolName string) (string, error) {
	pf, src, err := g.ParseFile(ctx, relPath)
	if err != nil {
		return "", err
	}
	sym, ok := findSymbol(pf, symbolName)
	if !ok {
		return "", fmt.Errorf("symbol %s not found", symbolName)
	}
	return string(tsparse.SymbolSource(src, sym)), nil
}

// lookupCallers queries gopls for references to the symbol's name position.
func (g *Generator) lookupCallers(ctx context.Context, absPath string, sym model.Symbol) []model.SymbolRef {
	if g.LSP == nil {
		return nil
	}
	src, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	if err := g.LSP.EnsureOpen(ctx, absPath, "go", src); err != nil {
		return nil
	}
	line := sym.StartLine - 1
	col := findNameColumn(src, sym)
	locs, err := g.LSP.References(ctx, absPath, line, col, false)
	if err != nil {
		return nil
	}
	out := make([]model.SymbolRef, 0, len(locs))
	for _, loc := range locs {
		p := lsp.URIToPath(loc.URI)
		rel, err := filepath.Rel(g.Root, p)
		if err != nil {
			rel = p
		}
		out = append(out, model.SymbolRef{
			Name: sym.Name,
			Path: rel,
			Line: loc.Range.Start.Line + 1,
		})
	}
	return out
}

func findNameColumn(src []byte, sym model.Symbol) int {
	lineStart := 0
	curLine := 1
	for i, b := range src {
		if curLine == sym.StartLine {
			lineStart = i
			break
		}
		if b == '\n' {
			curLine++
		}
	}
	rest := src[lineStart:]
	for i, b := range rest {
		if b == '\n' {
			rest = rest[:i]
			break
		}
	}
	idx := strings.Index(string(rest), sym.Name)
	if idx < 0 {
		return 0
	}
	return idx
}

func findSymbol(pf *tsparse.ParsedFile, name string) (model.Symbol, bool) {
	for _, s := range pf.Symbols {
		if s.Name == name {
			return s, true
		}
	}
	return model.Symbol{}, false
}

func buildFileView(src []byte, pf *tsparse.ParsedFile) string {
	const fullSourceCutoff = 12000
	if len(src) <= fullSourceCutoff {
		return string(src)
	}
	var b strings.Builder
	if len(pf.Imports) > 0 {
		b.WriteString("imports:\n")
		for _, imp := range pf.Imports {
			fmt.Fprintf(&b, "  %s\n", imp)
		}
		b.WriteString("\n")
	}
	for _, s := range pf.Symbols {
		b.WriteString(s.Signature)
		b.WriteString("\n\n")
	}
	return b.String()
}

func mergeMeta(m llm.Metadata, pf *tsparse.ParsedFile) model.Metadata {
	out := model.Metadata{
		Imports:  m.Imports,
		KeyTypes: m.KeyTypes,
		Gotchas:  m.Gotchas,
	}
	if len(out.Imports) == 0 {
		out.Imports = pf.Imports
	}
	return out
}

func refsToStrings(rs []model.SymbolRef) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, fmt.Sprintf("%s:%d", r.Path, r.Line))
	}
	return out
}

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}
