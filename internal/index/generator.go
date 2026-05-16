// Package index orchestrates explanation generation: it walks the repo
// (lazily), invokes tree-sitter for symbol extraction, asks the LSP client
// for cross-references, consults the cache, and only calls the LLM on a miss.
package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/gitsrc"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/lsp"
	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/secrets"
	"github.com/mikethicke/explore/internal/tsparse"
)

// Generator produces Explanations for nodes, caching results.
type Generator struct {
	Root       string
	Cache      *cache.Cache
	Provider   llm.Provider
	LSP        *lsp.Pool   // may be nil; per-language clients spawned lazily
	RepoPrimer string      // README + AGENTS.md, computed once

	// Rev is the revision file reads are served from. Defaults to the live
	// working tree (byte-identical to the pre-git behavior). Snapshot mode
	// swaps in a commit-backed revision via AtRevision. The LSP-backed xref
	// paths deliberately keep reading the working tree — language servers
	// index on-disk files, not git history.
	Rev gitsrc.Revision

	// OnUsage is invoked after every successful Provider.Explain call (cache
	// misses only — cached entries don't bill). May be called from multiple
	// goroutines (the prefetcher runs in parallel), so implementations must
	// be safe for concurrent use. Optional.
	OnUsage func(llm.Usage)

	// OnSecrets is invoked when secrets.Scan flags content being sent to the
	// LLM. The policy is warn-only — Generator never aborts on findings; the
	// caller (TUI) decides what to surface. Called from the same goroutines
	// that issue requests, including the prefetcher. Optional.
	OnSecrets func([]secrets.Finding)

	// LongFunctionThreshold is the line count above which ExplainSymbol marks
	// a function/method request as long (req.IsLong=true), prompting the LLM
	// for a structural outline. 0 disables the feature.
	LongFunctionThreshold int
}

// reportUsage is a nil-safe helper to invoke OnUsage. Kept private so the
// dispatch is consistent across ExplainX methods.
func (g *Generator) reportUsage(u llm.Usage) {
	if g.OnUsage != nil && u.Total() > 0 {
		g.OnUsage(u)
	}
}

// reportSecrets scans payload for known credential patterns and fires
// OnSecrets if anything matches. Called immediately before each
// Provider.Explain so the warning fires for content actually being sent —
// matches against the post-view/truncation payload, not the on-disk file.
func (g *Generator) reportSecrets(payload string) {
	if g.OnSecrets == nil || payload == "" {
		return
	}
	if f := secrets.Scan([]byte(payload)); len(f) > 0 {
		g.OnSecrets(f)
	}
}

// isLongSymbol reports whether sym's line count exceeds the configured
// long-function threshold. Returns false when the threshold is 0 (disabled)
// or when sym is not a function/method.
func (g *Generator) isLongSymbol(sym model.Symbol) bool {
	if g.LongFunctionThreshold <= 0 {
		return false
	}
	if sym.Kind != model.SymFunc && sym.Kind != model.SymMethod {
		return false
	}
	loc := sym.EndLine - sym.StartLine + 1
	return loc > g.LongFunctionThreshold
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

func NewGenerator(root string, c *cache.Cache, p llm.Provider, l *lsp.Pool) *Generator {
	g := &Generator{Root: root, Cache: c, Provider: p, LSP: l, Rev: gitsrc.WorkingTree(root)}
	g.RepoPrimer = loadRepoPrimer(root)
	return g
}

// AtRevision returns a shallow copy of g that serves file/dir reads from rev,
// sharing the same cache, provider, and LSP pool. The content-addressed cache
// means historical content byte-identical to HEAD reuses HEAD's entry for
// free; only changed files cost an LLM call.
func (g *Generator) AtRevision(rev gitsrc.Revision) *Generator {
	cp := *g
	cp.Rev = rev
	return &cp
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
	src, err := g.Rev.ReadFile(relPath)
	if err != nil {
		return nil, nil, err
	}
	pf, err := tsparse.Parse(ctx, abs, src)
	return pf, src, err
}

// ExplainFile returns a (possibly cached) explanation for an entire file.
func (g *Generator) ExplainFile(ctx context.Context, relPath string) (*model.Explanation, error) {
	absPath := filepath.Join(g.Root, relPath)
	src, err := g.Rev.ReadFile(relPath)
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
	g.reportSecrets(view)
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
	src, err := g.Rev.ReadFile(relPath)
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
	callees := g.lookupCallees(ctx, absPath, src, sym)

	req := llm.ExplainRequest{
		Level:         llm.LevelSymbol,
		Path:          relPath,
		Symbol:        symbolName,
		Signature:     sym.Signature,
		Source:        source,
		Imports:       pf.Imports,
		Callers:       refsToStrings(callers),
		Callees:       refsToStrings(callees),
		ParentSummary: fileSummary,
		RepoPrimer:    g.RepoPrimer,
		IsLong:        g.isLongSymbol(sym),
	}
	g.reportSecrets(source)
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
			Callees:  callees,
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

// lookupCallers queries the language server for references to the symbol's
// name position. Picks the right server for the file's language via the pool;
// returns nil (silently) if no server is available for that language.
func (g *Generator) lookupCallers(ctx context.Context, absPath string, sym model.Symbol) []model.SymbolRef {
	if g.LSP == nil {
		return nil
	}
	lang := tsparse.DetectLanguage(absPath)
	langID := lang.LSPLanguageID()
	if langID == "" {
		return nil
	}
	// The pool keys by tsparse language string, not the LSP language id (they
	// agree for go/python/rust but tsparse uses "tsx" while LSP uses "typescriptreact").
	client, err := g.LSP.ClientFor(ctx, string(lang))
	if err != nil || client == nil {
		return nil
	}
	src, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	if err := client.EnsureOpen(ctx, absPath, langID, src); err != nil {
		return nil
	}
	line := sym.StartLine - 1
	col := findNameColumn(src, sym)
	locs, err := client.References(ctx, absPath, line, col, false)
	if err != nil {
		return nil
	}
	// Cache per-file parses so we don't re-parse the same file once per
	// reference inside it. Common case: many calls from the same caller.
	parseCache := make(map[string]*tsparse.ParsedFile)
	out := make([]model.SymbolRef, 0, len(locs))
	for _, loc := range locs {
		p := lsp.URIToPath(loc.URI)
		rel, err := filepath.Rel(g.Root, p)
		if err != nil {
			rel = p
		}
		line := loc.Range.Start.Line + 1
		// Resolve the symbol that *contains* the call site. The LSP reference
		// only tells us file+line of the call expression; the function the
		// user wants to navigate to is whichever top-level symbol that line
		// falls inside. Empty when the reference is at file scope (var init,
		// etc.) — picker then jumps to the file, not a specific symbol.
		callerName := g.containingSymbolName(ctx, p, line, parseCache)
		out = append(out, model.SymbolRef{
			Name: callerName,
			Path: rel,
			Line: line,
		})
	}
	return out
}

// containingSymbolName parses absPath (memoized in cache) and returns the
// name of the innermost top-level symbol that contains the given 1-based
// line, or "" if no symbol covers it. The Generator-side analog of the TUI's
// containingSymbol — kept here so the lookup is colocated with the LSP
// reference-resolution code that needs it.
func (g *Generator) containingSymbolName(ctx context.Context, absPath string, line int, cache map[string]*tsparse.ParsedFile) string {
	pf, ok := cache[absPath]
	if !ok {
		src, err := os.ReadFile(absPath)
		if err != nil {
			cache[absPath] = nil
			return ""
		}
		parsed, err := tsparse.Parse(ctx, absPath, src)
		if err != nil {
			cache[absPath] = nil
			return ""
		}
		pf = parsed
		cache[absPath] = pf
	}
	if pf == nil {
		return ""
	}
	// Innermost wins — for overlapping symbols (e.g. a method declared inside
	// a class with chunked children), pick the smallest range that covers the line.
	bestName := ""
	bestSpan := 1 << 30
	for _, s := range pf.Symbols {
		if line < s.StartLine || line > s.EndLine {
			continue
		}
		span := s.EndLine - s.StartLine
		if span < bestSpan {
			bestSpan = span
			bestName = s.Name
		}
	}
	return bestName
}

// lookupCallees finds call expressions inside the symbol's body via
// tree-sitter, then asks the language server to resolve each call's
// definition. Returns one SymbolRef per unique destination. Degrades silently
// when LSP is unavailable, just like lookupCallers.
func (g *Generator) lookupCallees(ctx context.Context, absPath string, src []byte, sym model.Symbol) []model.SymbolRef {
	sites, err := tsparse.FindCallSites(ctx, absPath, src, sym.StartByte, sym.EndByte)
	if err != nil || len(sites) == 0 {
		return nil
	}
	return g.resolveCallSites(ctx, absPath, src, sites, sym.StartLine)
}

// LineCallsResult reports both what tree-sitter saw and what LSP resolved on
// a given line. The UI uses SitesFound > 0 && len(Refs) == 0 to distinguish
// "no calls on this line" from "calls exist but LSP couldn't resolve them"
// (gopls missing, mid-indexing, etc.).
type LineCallsResult struct {
	Refs       []model.SymbolRef
	SitesFound int // call expressions tree-sitter found on the line
}

// CallersResult mirrors LineCallsResult for `u`: SymbolFound lets the UI
// distinguish "symbol not in this file" (tree-sitter didn't see it — likely
// a stale focus or a deleted symbol) from "no callers" (LSP returned empty,
// which usually means the server is unavailable or still indexing).
type CallersResult struct {
	Refs        []model.SymbolRef
	SymbolFound bool
}

// CallersOf resolves a symbol in relPath via tsparse, then asks LSP for its
// callers. Computed on demand (the TUI calls this from the `u` keybind) so
// the picker doesn't depend on a cached explanation. Returns an empty result
// without error when the file can't be parsed; the UI treats that as
// "nothing to show" rather than surfacing a panic-worthy error mid-keypress.
func (g *Generator) CallersOf(ctx context.Context, relPath, symbolName string) (CallersResult, error) {
	start := time.Now()
	debug.Logf("CallersOf: start path=%q sym=%q", relPath, symbolName)
	absPath := filepath.Join(g.Root, relPath)
	src, err := os.ReadFile(absPath)
	if err != nil {
		debug.Logf("CallersOf: read err path=%q err=%v", relPath, err)
		return CallersResult{}, err
	}
	pf, err := tsparse.Parse(ctx, absPath, src)
	if err != nil {
		debug.Logf("CallersOf: parse err path=%q err=%v", relPath, err)
		return CallersResult{}, err
	}
	sym, ok := findSymbol(pf, symbolName)
	if !ok {
		debug.Logf("CallersOf: symbol not found path=%q sym=%q", relPath, symbolName)
		return CallersResult{}, nil
	}
	refs := g.lookupCallers(ctx, absPath, sym)
	debug.Logf("CallersOf: done path=%q sym=%q refs=%d after=%s", relPath, symbolName, len(refs), time.Since(start))
	return CallersResult{SymbolFound: true, Refs: refs}, nil
}

// CallsOnLine returns every destination reachable from a call expression that
// starts on `line` (1-based) in relPath. A single line with multiple calls
// (`foo(bar())`) yields one SymbolRef per call; a single call to an interface
// method may yield multiple definitions (one per implementation) — all are
// surfaced. Used by `d` xref-down: line-scoped, computed on demand.
func (g *Generator) CallsOnLine(ctx context.Context, relPath string, line int) (LineCallsResult, error) {
	start := time.Now()
	debug.Logf("CallsOnLine: start path=%q line=%d", relPath, line)
	if line < 1 {
		return LineCallsResult{}, nil
	}
	absPath := filepath.Join(g.Root, relPath)
	src, err := os.ReadFile(absPath)
	if err != nil {
		debug.Logf("CallsOnLine: read err path=%q err=%v", relPath, err)
		return LineCallsResult{}, err
	}
	all, err := tsparse.FindCallSites(ctx, absPath, src, 0, len(src))
	if err != nil {
		debug.Logf("CallsOnLine: FindCallSites err path=%q err=%v", relPath, err)
		return LineCallsResult{}, err
	}
	// FindCallSites returns 0-based lines; input is 1-based.
	target := line - 1
	var sites []tsparse.CallSite
	for _, s := range all {
		if s.Line == target {
			sites = append(sites, s)
		}
	}
	result := LineCallsResult{SitesFound: len(sites)}
	if len(sites) == 0 {
		debug.Logf("CallsOnLine: done path=%q line=%d sites=0 after=%s", relPath, line, time.Since(start))
		return result, nil
	}
	result.Refs = g.resolveCallSites(ctx, absPath, src, sites, 0)
	debug.Logf("CallsOnLine: done path=%q line=%d sites=%d refs=%d after=%s", relPath, line, len(sites), len(result.Refs), time.Since(start))
	return result, nil
}

// resolveCallSites turns a set of CallSites into SymbolRefs by asking LSP
// textDocument/definition for each. When excludeSelfStart > 0, definitions
// pointing back at that 1-based line in the same file are filtered (used by
// lookupCallees to skip a function's recursive call to itself). All locations
// returned by LSP are kept — interface-method calls naturally produce one ref
// per implementation, which is what the picker should show.
func (g *Generator) resolveCallSites(ctx context.Context, absPath string, src []byte, sites []tsparse.CallSite, excludeSelfStart int) []model.SymbolRef {
	if g.LSP == nil || len(sites) == 0 {
		return nil
	}
	lang := tsparse.DetectLanguage(absPath)
	langID := lang.LSPLanguageID()
	if langID == "" {
		return nil
	}
	client, err := g.LSP.ClientFor(ctx, string(lang))
	if err != nil || client == nil {
		return nil
	}
	if err := client.EnsureOpen(ctx, absPath, langID, src); err != nil {
		return nil
	}
	relSelf := strings.TrimPrefix(absPath, g.Root+string(filepath.Separator))
	seen := make(map[string]struct{})
	var out []model.SymbolRef
	for _, site := range sites {
		locs, err := client.Definition(ctx, absPath, site.Line, site.Column)
		if err != nil || len(locs) == 0 {
			continue
		}
		for _, loc := range locs {
			p := lsp.URIToPath(loc.URI)
			rel, err := filepath.Rel(g.Root, p)
			if err != nil {
				rel = p
			}
			if excludeSelfStart > 0 && rel == relSelf && loc.Range.Start.Line+1 == excludeSelfStart {
				continue
			}
			key := rel + ":" + site.Name + ":" + strconv.Itoa(loc.Range.Start.Line)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, model.SymbolRef{
				Name: site.Name,
				Path: rel,
				Line: loc.Range.Start.Line + 1,
			})
		}
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
