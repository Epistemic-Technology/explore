package tsparse

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/ruby"

	"github.com/mikethicke/explore/internal/model"
)

func parseRuby(ctx context.Context, path string, src []byte) (*ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(ruby.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	out := &ParsedFile{Path: path, Lang: LangRuby}
	root := tree.RootNode()
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		switch n.Type() {
		case "call":
			// `require "x"` / `require_relative "x"` / `load "x"`
			if imp := rubyRequire(n, src); imp != "" {
				out.Imports = append(out.Imports, imp)
			}
		case "class", "module":
			if s := rubyClassOrModule(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
				out.Symbols = append(out.Symbols, rubyMethodsInBody(n, src, s.Name)...)
			}
		case "method":
			if s := rubyMethod(n, src, ""); s != nil {
				s.Kind = model.SymFunc
				out.Symbols = append(out.Symbols, *s)
			}
		case "singleton_method":
			// Top-level `def self.foo` — record with self receiver.
			if s := rubySingletonMethod(n, src, ""); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "assignment":
			if s := rubyConst(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		}
	}
	return out, nil
}

// rubyRequire detects `require "x"` / `require_relative "x"` / `load "x"` calls
// and returns the path as a string (without quotes). Returns "" for other calls.
func rubyRequire(n *sitter.Node, src []byte) string {
	method := n.ChildByFieldName("method")
	if method == nil {
		return ""
	}
	name := strings.TrimSpace(method.Content(src))
	if name != "require" && name != "require_relative" && name != "load" {
		return ""
	}
	args := n.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return ""
	}
	arg := args.NamedChild(0)
	// Strip surrounding quotes if it's a string literal.
	text := strings.TrimSpace(arg.Content(src))
	text = strings.Trim(text, `"'`)
	return text
}

func rubyClassOrModule(n *sitter.Node, src []byte) *model.Symbol {
	name := rubyNameText(n.ChildByFieldName("name"), src)
	if name == "" {
		return nil
	}
	// Signature: from start through the optional superclass clause, stopping
	// before the body (`body` field).
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	sig := strings.TrimSpace(string(src[n.StartByte():sigEnd]))
	return &model.Symbol{
		Name:      name,
		Kind:      model.SymType,
		Signature: sig,
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

// rubyMethodsInBody walks the body of a class/module and emits one Symbol per
// `method` or `singleton_method` inside it. Nested classes/modules are NOT
// recursed into — they're separate top-level concerns and would clutter the
// flat tree view. Receiver is set to the enclosing class/module name.
func rubyMethodsInBody(container *sitter.Node, src []byte, recv string) []model.Symbol {
	body := container.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []model.Symbol
	for i := 0; i < int(body.NamedChildCount()); i++ {
		c := body.NamedChild(i)
		switch c.Type() {
		case "method":
			if s := rubyMethod(c, src, recv); s != nil {
				out = append(out, *s)
			}
		case "singleton_method":
			if s := rubySingletonMethod(c, src, recv); s != nil {
				out = append(out, *s)
			}
		}
	}
	return out
}

func rubyMethod(n *sitter.Node, src []byte, recv string) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	kind := model.SymMethod
	if recv == "" {
		kind = model.SymFunc
	}
	return &model.Symbol{
		Name:      nameNode.Content(src),
		Kind:      kind,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
		Receiver:  recv,
	}
}

// rubySingletonMethod handles `def self.foo` or `def Klass.foo`. Inside a class
// body we use the enclosing class name as receiver; at top level we keep the
// literal object text (usually "self").
func rubySingletonMethod(n *sitter.Node, src []byte, recv string) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	r := recv
	if r == "" {
		if obj := n.ChildByFieldName("object"); obj != nil {
			r = strings.TrimSpace(obj.Content(src))
		}
	}
	return &model.Symbol{
		Name:      nameNode.Content(src),
		Kind:      model.SymMethod,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
		Receiver:  r,
	}
}

// rubyConst recognizes top-level `CONST = ...` assignments. Only `constant`
// LHS counts — Ruby has no formal const but capitalized identifiers are the
// language's convention.
func rubyConst(n *sitter.Node, src []byte) *model.Symbol {
	left := n.ChildByFieldName("left")
	if left == nil || left.Type() != "constant" {
		return nil
	}
	return &model.Symbol{
		Name:      left.Content(src),
		Kind:      model.SymConst,
		Signature: strings.TrimSpace(n.Content(src)),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

// rubyNameText extracts a class/module name. Handles both plain `constant`
// (e.g., `class Foo`) and `scope_resolution` (e.g., `class Foo::Bar`).
func rubyNameText(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	return strings.TrimSpace(n.Content(src))
}
