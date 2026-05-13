package tsparse

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"

	"github.com/mikethicke/explore/internal/model"
)

func parseGo(ctx context.Context, path string, src []byte) (*ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(golang.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	out := &ParsedFile{Path: path, Lang: LangGo}
	root := tree.RootNode()
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		switch n.Type() {
		case "import_declaration":
			out.Imports = append(out.Imports, goExtractImports(n, src)...)
		case "function_declaration":
			s := goFunc(n, src, false)
			if s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "method_declaration":
			s := goFunc(n, src, true)
			if s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "type_declaration":
			out.Symbols = append(out.Symbols, goTypes(n, src)...)
		case "var_declaration", "const_declaration":
			kind := model.SymVar
			if n.Type() == "const_declaration" {
				kind = model.SymConst
			}
			out.Symbols = append(out.Symbols, goValues(n, src, kind)...)
		}
	}
	return out, nil
}

func goExtractImports(n *sitter.Node, src []byte) []string {
	var out []string
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		switch c.Type() {
		case "import_spec":
			path := goImportSpecPath(c, src)
			if path != "" {
				out = append(out, path)
			}
		case "import_spec_list":
			for j := 0; j < int(c.NamedChildCount()); j++ {
				spec := c.NamedChild(j)
				if spec.Type() == "import_spec" {
					path := goImportSpecPath(spec, src)
					if path != "" {
						out = append(out, path)
					}
				}
			}
		}
	}
	return out
}

func goImportSpecPath(spec *sitter.Node, src []byte) string {
	p := spec.ChildByFieldName("path")
	if p == nil {
		return ""
	}
	s := p.Content(src)
	return strings.Trim(s, `"`)
}

func goFunc(n *sitter.Node, src []byte, isMethod bool) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	sig := strings.TrimSpace(string(src[n.StartByte():sigEnd]))
	kind := model.SymFunc
	receiver := ""
	if isMethod {
		kind = model.SymMethod
		if r := n.ChildByFieldName("receiver"); r != nil {
			receiver = strings.TrimSpace(r.Content(src))
		}
	}
	return &model.Symbol{
		Name:      name,
		Kind:      kind,
		Signature: sig,
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
		Receiver:  receiver,
	}
}

func goTypes(n *sitter.Node, src []byte) []model.Symbol {
	var out []model.Symbol
	for i := 0; i < int(n.NamedChildCount()); i++ {
		spec := n.NamedChild(i)
		if spec.Type() != "type_spec" && spec.Type() != "type_alias" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := nameNode.Content(src)
		sig := strings.TrimSpace(spec.Content(src))
		out = append(out, model.Symbol{
			Name:      name,
			Kind:      model.SymType,
			Signature: sig,
			StartLine: int(spec.StartPoint().Row) + 1,
			EndLine:   int(spec.EndPoint().Row) + 1,
			StartByte: int(spec.StartByte()),
			EndByte:   int(spec.EndByte()),
		})
	}
	return out
}

func goValues(n *sitter.Node, src []byte, kind model.SymbolKind) []model.Symbol {
	var out []model.Symbol
	for i := 0; i < int(n.NamedChildCount()); i++ {
		spec := n.NamedChild(i)
		if spec.Type() != "var_spec" && spec.Type() != "const_spec" {
			continue
		}
		for j := 0; j < int(spec.NamedChildCount()); j++ {
			c := spec.NamedChild(j)
			if c.Type() != "identifier" {
				continue
			}
			name := c.Content(src)
			if name == "_" {
				continue
			}
			out = append(out, model.Symbol{
				Name:      name,
				Kind:      kind,
				Signature: strings.TrimSpace(spec.Content(src)),
				StartLine: int(spec.StartPoint().Row) + 1,
				EndLine:   int(spec.EndPoint().Row) + 1,
				StartByte: int(spec.StartByte()),
				EndByte:   int(spec.EndByte()),
			})
		}
	}
	return out
}
