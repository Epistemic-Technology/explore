package tsparse

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/mikethicke/explore/internal/model"
)

func parseTypeScript(ctx context.Context, path string, src []byte, isTSX bool) (*ParsedFile, error) {
	p := sitter.NewParser()
	if isTSX {
		p.SetLanguage(tsx.GetLanguage())
	} else {
		p.SetLanguage(typescript.GetLanguage())
	}
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	out := &ParsedFile{Path: path, Lang: LangTypeScript}
	if isTSX {
		out.Lang = LangTSX
	}
	root := tree.RootNode()
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		// Exports wrap the actual declaration; unwrap one level.
		if n.Type() == "export_statement" {
			if inner := tsExportInner(n); inner != nil {
				out.Symbols = append(out.Symbols, tsExtractSymbol(inner, src)...)
			}
			continue
		}
		switch n.Type() {
		case "import_statement":
			out.Imports = append(out.Imports, tsImportSource(n, src)...)
		default:
			out.Symbols = append(out.Symbols, tsExtractSymbol(n, src)...)
		}
	}
	return out, nil
}

// tsExportInner returns the wrapped declaration in `export function/class/...`
// or `export default function/class/...`. Re-exports (`export { x } from ...`)
// have no declaration child and return nil.
func tsExportInner(exp *sitter.Node) *sitter.Node {
	for i := 0; i < int(exp.NamedChildCount()); i++ {
		c := exp.NamedChild(i)
		switch c.Type() {
		case "function_declaration", "class_declaration", "interface_declaration",
			"type_alias_declaration", "enum_declaration", "lexical_declaration",
			"variable_declaration":
			return c
		}
	}
	return nil
}

func tsExtractSymbol(n *sitter.Node, src []byte) []model.Symbol {
	switch n.Type() {
	case "function_declaration":
		if s := tsFunc(n, src); s != nil {
			return []model.Symbol{*s}
		}
	case "class_declaration":
		if s := tsClassLike(n, src, model.SymType); s != nil {
			return []model.Symbol{*s}
		}
	case "interface_declaration", "type_alias_declaration", "enum_declaration":
		if s := tsClassLike(n, src, model.SymType); s != nil {
			return []model.Symbol{*s}
		}
	case "lexical_declaration", "variable_declaration":
		return tsValues(n, src)
	}
	return nil
}

func tsFunc(n *sitter.Node, src []byte) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	return &model.Symbol{
		Name:      nameNode.Content(src),
		Kind:      model.SymFunc,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

// tsClassLike handles class/interface/type-alias/enum — they all expose a
// `name` field and a body block. Signature is everything up to the body.
func tsClassLike(n *sitter.Node, src []byte, kind model.SymbolKind) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	return &model.Symbol{
		Name:      nameNode.Content(src),
		Kind:      kind,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

// tsValues walks `const a = ...; let b = ...;` and records each binding
// identifier. Destructuring patterns are skipped (rare at module scope).
func tsValues(n *sitter.Node, src []byte) []model.Symbol {
	kind := model.SymVar
	// `lexical_declaration` carries its keyword (const / let) as the first
	// anonymous child; if we can see "const" choose SymConst.
	if n.Type() == "lexical_declaration" {
		if c := n.Child(0); c != nil && c.Content(src) == "const" {
			kind = model.SymConst
		}
	}
	var out []model.Symbol
	for i := 0; i < int(n.NamedChildCount()); i++ {
		dec := n.NamedChild(i)
		if dec.Type() != "variable_declarator" {
			continue
		}
		nameNode := dec.ChildByFieldName("name")
		if nameNode == nil || nameNode.Type() != "identifier" {
			continue
		}
		out = append(out, model.Symbol{
			Name:      nameNode.Content(src),
			Kind:      kind,
			Signature: strings.TrimSpace(dec.Content(src)),
			StartLine: int(dec.StartPoint().Row) + 1,
			EndLine:   int(dec.EndPoint().Row) + 1,
			StartByte: int(dec.StartByte()),
			EndByte:   int(dec.EndByte()),
		})
	}
	return out
}

// tsImportSource returns the module-path string from an import statement,
// stripped of quotes. `import "foo"` and `import x from "foo"` both yield "foo".
func tsImportSource(n *sitter.Node, src []byte) []string {
	src_field := n.ChildByFieldName("source")
	if src_field == nil {
		return nil
	}
	s := strings.TrimSpace(src_field.Content(src))
	s = strings.Trim(s, "\"'`")
	if s == "" {
		return nil
	}
	return []string{s}
}
