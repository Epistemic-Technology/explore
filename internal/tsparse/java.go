package tsparse

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/java"

	"github.com/mikethicke/explore/internal/model"
)

func parseJava(ctx context.Context, path string, src []byte) (*ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(java.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	out := &ParsedFile{Path: path, Lang: LangJava}
	root := tree.RootNode()
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		switch n.Type() {
		case "import_declaration":
			out.Imports = append(out.Imports, javaImportPath(n, src))
		case "class_declaration", "interface_declaration",
			"enum_declaration", "record_declaration", "annotation_type_declaration":
			if s := javaTypeDecl(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
				out.Symbols = append(out.Symbols, javaMethodsInBody(n, src, s.Name)...)
			}
		}
	}
	return out, nil
}

// javaImportPath returns the dotted import path of an `import a.b.C;` /
// `import static a.b.C.D;` declaration with leading "import" / "static" and
// trailing ";" stripped. Asterisk imports keep their `.*` suffix.
func javaImportPath(n *sitter.Node, src []byte) string {
	text := strings.TrimSpace(n.Content(src))
	text = strings.TrimPrefix(text, "import")
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "static")
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, ";")
	return strings.TrimSpace(text)
}

func javaTypeDecl(n *sitter.Node, src []byte) *model.Symbol {
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
		Kind:      model.SymType,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

// javaMethodsInBody walks a class/interface/enum/record body and emits one
// Symbol per `method_declaration` / `constructor_declaration`. Nested types
// are not recursed into — they show up as their own top-level concerns under
// the file (rare in Java anyway). Receiver is the enclosing type name.
func javaMethodsInBody(container *sitter.Node, src []byte, recv string) []model.Symbol {
	body := container.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []model.Symbol
	for i := 0; i < int(body.NamedChildCount()); i++ {
		c := body.NamedChild(i)
		switch c.Type() {
		case "method_declaration":
			if s := javaMethod(c, src, recv, false); s != nil {
				out = append(out, *s)
			}
		case "constructor_declaration", "compact_constructor_declaration":
			if s := javaMethod(c, src, recv, true); s != nil {
				out = append(out, *s)
			}
		}
	}
	return out
}

func javaMethod(n *sitter.Node, src []byte, recv string, isCtor bool) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	name := nameNode.Content(src)
	if isCtor {
		// Constructors share the type name; tag with a marker so they're
		// distinguishable in the symbol tree.
		name = "<init>"
	}
	return &model.Symbol{
		Name:      name,
		Kind:      model.SymMethod,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
		Receiver:  recv,
	}
}
