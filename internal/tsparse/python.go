package tsparse

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"

	"github.com/mikethicke/explore/internal/model"
)

func parsePython(ctx context.Context, path string, src []byte) (*ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(python.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	out := &ParsedFile{Path: path, Lang: LangPython}
	root := tree.RootNode()
	for i := 0; i < int(root.NamedChildCount()); i++ {
		n := root.NamedChild(i)
		switch n.Type() {
		case "import_statement", "import_from_statement", "future_import_statement":
			out.Imports = append(out.Imports, pyExtractImports(n, src)...)
		case "function_definition":
			if s := pyFunc(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "class_definition":
			if s := pyClass(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "decorated_definition":
			// Look inside for the actual function or class.
			def := n.ChildByFieldName("definition")
			if def == nil {
				continue
			}
			switch def.Type() {
			case "function_definition":
				if s := pyFunc(def, src); s != nil {
					// Include decorators in the byte range so source extraction shows them.
					s.StartLine = int(n.StartPoint().Row) + 1
					s.StartByte = int(n.StartByte())
					out.Symbols = append(out.Symbols, *s)
				}
			case "class_definition":
				if s := pyClass(def, src); s != nil {
					s.StartLine = int(n.StartPoint().Row) + 1
					s.StartByte = int(n.StartByte())
					out.Symbols = append(out.Symbols, *s)
				}
			}
		case "expression_statement":
			// Top-level assignments like `CONST = 1`. We treat ALL_CAPS as const,
			// everything else as var. Not perfect (Python has no formal const),
			// but matches reader intuition.
			out.Symbols = append(out.Symbols, pyAssignments(n, src)...)
		}
	}
	return out, nil
}

// pyExtractImports flattens both `import a, b.c` and `from x import y, z`.
// We record the module path (left side of `from`, or each name in `import`).
func pyExtractImports(n *sitter.Node, src []byte) []string {
	var out []string
	switch n.Type() {
	case "import_statement":
		// Children are dotted_name / aliased_import.
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			out = append(out, pyImportName(c, src))
		}
	case "import_from_statement":
		// Field "module_name" is the source.
		mod := n.ChildByFieldName("module_name")
		if mod == nil {
			return nil
		}
		out = append(out, strings.TrimSpace(mod.Content(src)))
	case "future_import_statement":
		out = append(out, "__future__")
	}
	// Drop empties.
	clean := out[:0]
	for _, s := range out {
		if s != "" {
			clean = append(clean, s)
		}
	}
	return clean
}

func pyImportName(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "aliased_import":
		if name := n.ChildByFieldName("name"); name != nil {
			return strings.TrimSpace(name.Content(src))
		}
	}
	return strings.TrimSpace(n.Content(src))
}

func pyFunc(n *sitter.Node, src []byte) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	// Signature: from start through the parameter list and optional return
	// annotation, stopping before the body (`block` field).
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	sig := strings.TrimRight(strings.TrimSpace(string(src[n.StartByte():sigEnd])), ":")
	return &model.Symbol{
		Name:      name,
		Kind:      model.SymFunc,
		Signature: sig,
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

func pyClass(n *sitter.Node, src []byte) *model.Symbol {
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
	sig := strings.TrimRight(strings.TrimSpace(string(src[n.StartByte():sigEnd])), ":")
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

// pyAssignments returns one Symbol per identifier targeted by a module-level
// assignment. Only simple `name = ...` patterns are recognized; tuple
// unpacking and attribute targets are ignored (they're rare at module scope).
func pyAssignments(stmt *sitter.Node, src []byte) []model.Symbol {
	var out []model.Symbol
	if stmt.NamedChildCount() == 0 {
		return nil
	}
	assign := stmt.NamedChild(0)
	if assign.Type() != "assignment" {
		return nil
	}
	left := assign.ChildByFieldName("left")
	if left == nil || left.Type() != "identifier" {
		return nil
	}
	name := left.Content(src)
	if name == "_" || strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__") {
		// Skip private/dunder.
		return nil
	}
	kind := model.SymVar
	if name == strings.ToUpper(name) && len(name) > 1 {
		kind = model.SymConst
	}
	sig := strings.TrimSpace(stmt.Content(src))
	out = append(out, model.Symbol{
		Name:      name,
		Kind:      kind,
		Signature: sig,
		StartLine: int(stmt.StartPoint().Row) + 1,
		EndLine:   int(stmt.EndPoint().Row) + 1,
		StartByte: int(stmt.StartByte()),
		EndByte:   int(stmt.EndByte()),
	})
	return out
}
