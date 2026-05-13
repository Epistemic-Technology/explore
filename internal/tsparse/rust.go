package tsparse

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/rust"

	"github.com/mikethicke/explore/internal/model"
)

func parseRust(ctx context.Context, path string, src []byte) (*ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(rust.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	out := &ParsedFile{Path: path, Lang: LangRust}
	root := tree.RootNode()
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		// pub-prefixed items have the item node as a named child. Unwrap so
		// we record the function/struct/etc., not the visibility wrapper.
		if n.Type() == "visibility_modifier" {
			continue
		}
		switch n.Type() {
		case "use_declaration":
			out.Imports = append(out.Imports, rustUseString(n, src))
		case "function_item":
			if s := rustFunc(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "struct_item", "enum_item", "trait_item", "type_item", "union_item":
			if s := rustNamedItem(n, src, model.SymType); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "impl_item":
			// impl blocks aren't a single symbol — record their methods individually.
			out.Symbols = append(out.Symbols, rustImplMethods(n, src)...)
		case "const_item", "static_item":
			if s := rustValue(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "mod_item":
			if s := rustNamedItem(n, src, model.SymType); s != nil {
				// Modules aren't really a "type" but we don't have a better
				// kind; treating as type keeps them visible in the tree.
				out.Symbols = append(out.Symbols, *s)
			}
		}
	}
	return out, nil
}

func rustFunc(n *sitter.Node, src []byte) *model.Symbol {
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

func rustNamedItem(n *sitter.Node, src []byte, kind model.SymbolKind) *model.Symbol {
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

// rustImplMethods scans an impl block and emits one Symbol per `fn` inside it,
// with the receiver tag set to the impl's type for display ("Foo.bar"). Trait
// impls and inherent impls are handled the same way.
func rustImplMethods(impl *sitter.Node, src []byte) []model.Symbol {
	typ := impl.ChildByFieldName("type")
	if typ == nil {
		return nil
	}
	recv := strings.TrimSpace(typ.Content(src))
	body := impl.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []model.Symbol
	for i := 0; i < int(body.NamedChildCount()); i++ {
		c := body.NamedChild(i)
		if c.Type() != "function_item" {
			continue
		}
		s := rustFunc(c, src)
		if s == nil {
			continue
		}
		s.Kind = model.SymMethod
		s.Receiver = recv
		out = append(out, *s)
	}
	return out
}

func rustValue(n *sitter.Node, src []byte) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	kind := model.SymConst
	if n.Type() == "static_item" {
		kind = model.SymVar
	}
	return &model.Symbol{
		Name:      nameNode.Content(src),
		Kind:      kind,
		Signature: strings.TrimSpace(n.Content(src)),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

// rustUseString returns the import path of a `use` declaration as a single
// string, e.g. "std::collections::HashMap". Use trees ("use a::{b, c}") are
// recorded as one entry per use block to keep the prompt compact.
func rustUseString(n *sitter.Node, src []byte) string {
	// Strip the leading "use " and trailing semicolon for a tighter view.
	text := strings.TrimSpace(n.Content(src))
	text = strings.TrimPrefix(text, "pub ")
	text = strings.TrimPrefix(text, "use ")
	text = strings.TrimSuffix(text, ";")
	return text
}
