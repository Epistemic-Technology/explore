// Package highlight runs tree-sitter highlight queries over source files and
// returns capture-tagged byte spans. Modeled on Neovim's tree-sitter pipeline:
// each language has a `highlights.scm` query (embedded into the binary) whose
// captures map to named kinds like @keyword / @function / @string. Renderers
// turn those kinds into colors via a theme.
//
// Overlapping captures (e.g. `function_declaration` matches both the whole
// decl and its name) are resolved by "innermost wins" — the shortest span
// that covers a byte determines its kind. This matches reader intuition: an
// identifier inside a function should style as @function, not as the parent
// declaration's keyword tag.
package highlight

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/mikethicke/explore/internal/tsparse"
)

// Capture is a stable string identifier for a highlight class. We use plain
// strings (not enums) so adding a capture in an .scm doesn't require a code
// change — themes can either style it or fall back to default.
type Capture string

// Span is a contiguous styled byte range. Spans returned from Highlight are
// sorted by Start and non-overlapping.
type Span struct {
	Start, End int
	Kind       Capture
}

//go:embed queries/go/highlights.scm queries/python/highlights.scm queries/typescript/highlights.scm queries/rust/highlights.scm queries/ruby/highlights.scm queries/java/highlights.scm queries/cpp/highlights.scm
var queryFS embed.FS

// Highlighter owns compiled tree-sitter queries (one per language) and a
// content-hash → spans cache. Safe for concurrent use; queries themselves
// are read-only after construction, but each call holds a per-language mutex
// while it parses + executes.
type Highlighter struct {
	mu      sync.Mutex
	queries map[tsparse.Language]langEntry
	cache   map[string][]Span // sha256(src) → spans
}

type langEntry struct {
	ts    *sitter.Language
	query *sitter.Query
}

// New builds and compiles queries for every supported language. Returns an
// error if any query fails to compile — that's a programmer bug in the .scm
// file, so the caller should treat it as fatal.
func New() (*Highlighter, error) {
	h := &Highlighter{
		queries: make(map[tsparse.Language]langEntry),
		cache:   make(map[string][]Span),
	}
	type spec struct {
		lang tsparse.Language
		ts   *sitter.Language
		path string
	}
	for _, s := range []spec{
		{tsparse.LangGo, golang.GetLanguage(), "queries/go/highlights.scm"},
		{tsparse.LangPython, python.GetLanguage(), "queries/python/highlights.scm"},
		{tsparse.LangTypeScript, typescript.GetLanguage(), "queries/typescript/highlights.scm"},
		{tsparse.LangTSX, tsx.GetLanguage(), "queries/typescript/highlights.scm"},
		{tsparse.LangRust, rust.GetLanguage(), "queries/rust/highlights.scm"},
		{tsparse.LangRuby, ruby.GetLanguage(), "queries/ruby/highlights.scm"},
		{tsparse.LangJava, java.GetLanguage(), "queries/java/highlights.scm"},
		{tsparse.LangCPP, cpp.GetLanguage(), "queries/cpp/highlights.scm"},
	} {
		body, err := queryFS.ReadFile(s.path)
		if err != nil {
			return nil, fmt.Errorf("highlight: read %s: %w", s.path, err)
		}
		q, err := sitter.NewQuery(body, s.ts)
		if err != nil {
			return nil, fmt.Errorf("highlight %s: compile %s: %w", s.lang, s.path, err)
		}
		h.queries[s.lang] = langEntry{ts: s.ts, query: q}
	}
	return h, nil
}

// Highlight returns sorted, non-overlapping spans for src under lang. Empty
// for languages we don't know. The result is cached by content hash, so
// repeated renders of an unchanged file are cheap.
func (h *Highlighter) Highlight(ctx context.Context, src []byte, lang tsparse.Language) []Span {
	if h == nil {
		return nil
	}
	entry, ok := h.queries[lang]
	if !ok {
		return nil
	}
	if len(src) == 0 {
		return nil
	}

	key := hashKey(src, lang)
	h.mu.Lock()
	if spans, ok := h.cache[key]; ok {
		h.mu.Unlock()
		return spans
	}
	h.mu.Unlock()

	// Parse outside the lock so concurrent callers for different files don't
	// block each other on the parse itself; we re-acquire just to write back.
	parser := sitter.NewParser()
	parser.SetLanguage(entry.ts)
	tree, err := parser.ParseCtx(ctx, nil, src)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()

	spans := runQuery(entry.query, tree.RootNode(), src)

	h.mu.Lock()
	h.cache[key] = spans
	h.mu.Unlock()
	return spans
}

func hashKey(src []byte, lang tsparse.Language) string {
	h := sha256.Sum256(src)
	return string(lang) + ":" + hex.EncodeToString(h[:8]) // 8 bytes is plenty for collision-resistance per-session
}

// runQuery executes a compiled query against the tree and resolves overlaps
// with the "innermost wins" rule: shorter spans overwrite longer enclosing ones.
func runQuery(q *sitter.Query, root *sitter.Node, src []byte) []Span {
	cursor := sitter.NewQueryCursor()
	defer cursor.Close()
	cursor.Exec(q, root)

	type rawCap struct {
		start, end int
		name       string
	}
	var raw []rawCap
	for {
		match, ok := cursor.NextMatch()
		if !ok {
			break
		}
		match = cursor.FilterPredicates(match, src)
		for _, c := range match.Captures {
			start := int(c.Node.StartByte())
			end := int(c.Node.EndByte())
			if end <= start || start < 0 || end > len(src) {
				continue
			}
			name := q.CaptureNameForId(c.Index)
			raw = append(raw, rawCap{start: start, end: end, name: name})
		}
	}
	if len(raw) == 0 {
		return nil
	}
	// Sort largest-first so smaller, more specific captures overwrite them.
	sort.Slice(raw, func(i, j int) bool {
		li := raw[i].end - raw[i].start
		lj := raw[j].end - raw[j].start
		if li != lj {
			return li > lj
		}
		return raw[i].start < raw[j].start
	})
	// Paint a byte→capture map; smaller spans win because they come last.
	kinds := make([]string, len(src))
	for _, c := range raw {
		for i := c.start; i < c.end; i++ {
			kinds[i] = c.name
		}
	}
	// Coalesce adjacent same-kind bytes.
	var out []Span
	i := 0
	for i < len(kinds) {
		if kinds[i] == "" {
			i++
			continue
		}
		j := i + 1
		for j < len(kinds) && kinds[j] == kinds[i] {
			j++
		}
		out = append(out, Span{Start: i, End: j, Kind: Capture(kinds[i])})
		i = j
	}
	return out
}
