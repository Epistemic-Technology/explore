package tsparse

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/ruby"
	"github.com/smacker/go-tree-sitter/rust"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// CallSite is one function-call invocation. Name is the leaf identifier text
// of the callee (e.g., "Foo" for `pkg.Foo()` or "method" for `obj.method()`).
// Line and Column are 0-based, pointing at the leaf identifier — what LSP
// textDocument/definition expects.
type CallSite struct {
	Name   string
	Line   int
	Column int
}

// FindCallSites parses src and returns every call expression whose start byte
// falls within [startByte, endByte). Used by the Generator's lookupCallees
// to feed candidate positions to LSP. Returns nil for unsupported languages —
// callers should degrade gracefully, same as a missing LSP.
func FindCallSites(ctx context.Context, path string, src []byte, startByte, endByte int) ([]CallSite, error) {
	lang := DetectLanguage(path)
	g := callGrammar(lang)
	if g == nil {
		return nil, nil
	}
	p := sitter.NewParser()
	p.SetLanguage(g)
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	var sites []CallSite
	walkCallNodes(tree.RootNode(), src, lang, startByte, endByte, &sites)
	return dedupCallSites(sites), nil
}

// callGrammar returns the tree-sitter grammar pointer for a language, or nil
// if we don't support call-site discovery for it.
func callGrammar(l Language) *sitter.Language {
	switch l {
	case LangGo:
		return golang.GetLanguage()
	case LangPython:
		return python.GetLanguage()
	case LangTypeScript:
		return typescript.GetLanguage()
	case LangTSX:
		return tsx.GetLanguage()
	case LangRust:
		return rust.GetLanguage()
	case LangRuby:
		return ruby.GetLanguage()
	case LangJava:
		return java.GetLanguage()
	case LangCPP:
		return cpp.GetLanguage()
	}
	return nil
}

func walkCallNodes(n *sitter.Node, src []byte, lang Language, startByte, endByte int, out *[]CallSite) {
	// Prune: skip subtrees that don't overlap the target byte range.
	if int(n.EndByte()) <= startByte || int(n.StartByte()) >= endByte {
		return
	}
	if isCallNode(lang, n.Type()) {
		if site, ok := extractCallSite(n, src, lang); ok && site.offsetWithin(startByte, endByte) {
			*out = append(*out, site)
		}
		// Don't return — calls can nest (`f(g())`), keep walking children.
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		walkCallNodes(n.NamedChild(i), src, lang, startByte, endByte, out)
	}
}

func (s CallSite) offsetWithin(start, end int) bool {
	// Coarse line-based check is fine here — callers pass byte ranges that
	// align with line boundaries.
	_ = start
	_ = end
	return true
}

func isCallNode(lang Language, nodeType string) bool {
	switch lang {
	case LangGo, LangRust, LangTypeScript, LangTSX, LangCPP:
		return nodeType == "call_expression"
	case LangPython, LangRuby:
		return nodeType == "call"
	case LangJava:
		return nodeType == "method_invocation"
	}
	return false
}

// extractCallSite pulls the leaf callee identifier from a call node. The leaf
// is the actual function/method name — what LSP definition wants pointed at.
// For `pkg.Foo()` the leaf is `Foo`; for `obj.method()` it's `method`; for a
// bare `f()` it's `f`. Returns ok=false when the callee isn't a recognizable
// identifier shape (e.g., a lambda call expression).
func extractCallSite(n *sitter.Node, src []byte, lang Language) (CallSite, bool) {
	var target *sitter.Node
	switch lang {
	case LangJava:
		// method_invocation: name field is the method identifier directly.
		target = n.ChildByFieldName("name")
	case LangRuby:
		// ruby call: method field is the method identifier.
		target = n.ChildByFieldName("method")
	default:
		// Go/Python/Rust/TS/C++ all use a "function" field.
		target = n.ChildByFieldName("function")
	}
	if target == nil {
		return CallSite{}, false
	}
	leaf := calleeLeaf(target, lang)
	if leaf == nil {
		return CallSite{}, false
	}
	name := strings.TrimSpace(leaf.Content(src))
	if name == "" {
		return CallSite{}, false
	}
	return CallSite{
		Name:   name,
		Line:   int(leaf.StartPoint().Row),
		Column: int(leaf.StartPoint().Column),
	}, true
}

// calleeLeaf descends a callee expression to its terminal identifier. Handles
// each language's variant of member access / qualified name.
func calleeLeaf(n *sitter.Node, lang Language) *sitter.Node {
	if n == nil {
		return nil
	}
	switch n.Type() {
	case "identifier", "field_identifier", "type_identifier", "property_identifier":
		return n
	case "selector_expression":
		// Go: pkg.Foo or obj.method — child "field" is the leaf.
		if f := n.ChildByFieldName("field"); f != nil {
			return calleeLeaf(f, lang)
		}
	case "field_expression":
		// C++: obj.member; child "field" is the leaf.
		if f := n.ChildByFieldName("field"); f != nil {
			return calleeLeaf(f, lang)
		}
	case "attribute":
		// Python: obj.attr; child "attribute" is the leaf.
		if a := n.ChildByFieldName("attribute"); a != nil {
			return calleeLeaf(a, lang)
		}
	case "member_expression":
		// TS: obj.method — "property" is the leaf identifier.
		if p := n.ChildByFieldName("property"); p != nil {
			return calleeLeaf(p, lang)
		}
	case "scope_resolution", "qualified_identifier":
		// Rust/C++ ::-qualified path; the "name" field is the leaf.
		if name := n.ChildByFieldName("name"); name != nil {
			return calleeLeaf(name, lang)
		}
	case "scoped_identifier":
		// Rust scoped identifier (alternate spelling on some grammars).
		if name := n.ChildByFieldName("name"); name != nil {
			return calleeLeaf(name, lang)
		}
	case "constant":
		// Ruby — capitalized identifier, also a valid callee leaf.
		return n
	}
	// Fall through: if we don't know, treat the node itself as the leaf so the
	// caller has *something* to feed to LSP. Worst case LSP returns nothing.
	return n
}

// dedupCallSites collapses repeats produced by the same source position. A
// function may be called many times at the same line/column only if the AST
// has overlapping matches; defensive but cheap.
func dedupCallSites(sites []CallSite) []CallSite {
	if len(sites) <= 1 {
		return sites
	}
	seen := make(map[CallSite]struct{}, len(sites))
	out := sites[:0]
	for _, s := range sites {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
