package tsparse

import (
	"context"
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/cpp"

	"github.com/mikethicke/explore/internal/model"
)

func parseCPP(ctx context.Context, path string, src []byte) (*ParsedFile, error) {
	p := sitter.NewParser()
	p.SetLanguage(cpp.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	defer tree.Close()

	out := &ParsedFile{Path: path, Lang: LangCPP}
	root := tree.RootNode()
	cppWalkTopLevel(root, src, out)
	return out, nil
}

// cppWalkTopLevel scans top-level nodes (also descends into namespace bodies
// and template_declaration wrappers so symbols inside namespaces / templated
// classes are surfaced). Nested classes-inside-classes are NOT descended into
// — they stay attached to their parent's source range.
func cppWalkTopLevel(node *sitter.Node, src []byte, out *ParsedFile) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		n := node.NamedChild(i)
		switch n.Type() {
		case "preproc_include":
			if imp := cppIncludePath(n, src); imp != "" {
				out.Imports = append(out.Imports, imp)
			}
		case "namespace_definition":
			if s := cppNamespace(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
			// Descend into the namespace body so contents surface in the file.
			if body := n.ChildByFieldName("body"); body != nil {
				cppWalkTopLevel(body, src, out)
			}
		case "class_specifier", "struct_specifier", "union_specifier":
			if s := cppRecord(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
				out.Symbols = append(out.Symbols, cppMethodsInBody(n, src, s.Name)...)
			}
		case "enum_specifier":
			if s := cppEnum(n, src); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "function_definition":
			if s := cppFunction(n, src, ""); s != nil {
				out.Symbols = append(out.Symbols, *s)
			}
		case "template_declaration":
			// Unwrap: the templated item is the last named child.
			if n.NamedChildCount() > 0 {
				inner := n.NamedChild(int(n.NamedChildCount()) - 1)
				cppWalkTopLevel(wrapNode(inner), src, out)
			}
		}
	}
}

// wrapNode returns a tiny synthetic "parent" with `n` as its only named child,
// so cppWalkTopLevel can be reused on a single inner node. We don't actually
// need a parent — we just call it directly.
func wrapNode(n *sitter.Node) *sitter.Node { return n }

// cppIncludePath strips the leading `#include` and the surrounding `<>` / `""`.
func cppIncludePath(n *sitter.Node, src []byte) string {
	text := strings.TrimSpace(n.Content(src))
	text = strings.TrimPrefix(text, "#include")
	text = strings.TrimSpace(text)
	text = strings.Trim(text, `<>"`)
	return strings.TrimSpace(text)
}

func cppNamespace(n *sitter.Node, src []byte) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	name := "<anonymous>"
	if nameNode != nil {
		name = strings.TrimSpace(nameNode.Content(src))
	}
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	return &model.Symbol{
		Name:      name,
		Kind:      model.SymType,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

func cppRecord(n *sitter.Node, src []byte) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		// Anonymous struct/union — skip rather than guess a name.
		return nil
	}
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	return &model.Symbol{
		Name:      strings.TrimSpace(nameNode.Content(src)),
		Kind:      model.SymType,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

func cppEnum(n *sitter.Node, src []byte) *model.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	return &model.Symbol{
		Name:      strings.TrimSpace(nameNode.Content(src)),
		Kind:      model.SymType,
		Signature: strings.TrimSpace(n.Content(src)),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
	}
}

// cppMethodsInBody iterates a class/struct/union body and emits one Symbol per
// inline method definition / declaration. Receiver is the enclosing type.
func cppMethodsInBody(container *sitter.Node, src []byte, recv string) []model.Symbol {
	body := container.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out []model.Symbol
	for i := 0; i < int(body.NamedChildCount()); i++ {
		c := body.NamedChild(i)
		switch c.Type() {
		case "function_definition":
			if s := cppFunction(c, src, recv); s != nil {
				out = append(out, *s)
			}
		case "field_declaration":
			// A method declaration without a body looks like a field with a
			// function_declarator inside. Inline methods take the
			// function_definition path above.
			if s := cppFieldMethod(c, src, recv); s != nil {
				out = append(out, *s)
			}
		case "template_declaration":
			if c.NamedChildCount() > 0 {
				inner := c.NamedChild(int(c.NamedChildCount()) - 1)
				if inner.Type() == "function_definition" {
					if s := cppFunction(inner, src, recv); s != nil {
						out = append(out, *s)
					}
				}
			}
		}
	}
	return out
}

// cppFunction emits a Symbol for a `function_definition`. When `recv` is "",
// it's treated as a free function (SymFunc); otherwise it's a method.
// If the declarator is `Foo::bar` (out-of-line method), the receiver portion
// is inferred from the qualified name even when `recv` is empty.
func cppFunction(n *sitter.Node, src []byte, recv string) *model.Symbol {
	decl := n.ChildByFieldName("declarator")
	if decl == nil {
		return nil
	}
	name, qualifier := cppDeclaratorName(decl, src)
	if name == "" {
		return nil
	}
	body := n.ChildByFieldName("body")
	sigEnd := n.EndByte()
	if body != nil {
		sigEnd = body.StartByte()
	}
	r := recv
	if r == "" && qualifier != "" {
		r = qualifier
	}
	kind := model.SymMethod
	if r == "" {
		kind = model.SymFunc
	}
	return &model.Symbol{
		Name:      name,
		Kind:      kind,
		Signature: strings.TrimSpace(string(src[n.StartByte():sigEnd])),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
		Receiver:  r,
	}
}

// cppFieldMethod recognizes class-body method *declarations* (no body), which
// the C++ grammar shapes as a `field_declaration` whose declarator is a
// function_declarator. Plain data fields return nil.
func cppFieldMethod(n *sitter.Node, src []byte, recv string) *model.Symbol {
	decl := n.ChildByFieldName("declarator")
	if decl == nil || !cppIsFunctionDeclarator(decl) {
		return nil
	}
	name, _ := cppDeclaratorName(decl, src)
	if name == "" {
		return nil
	}
	return &model.Symbol{
		Name:      name,
		Kind:      model.SymMethod,
		Signature: strings.TrimSpace(n.Content(src)),
		StartLine: int(n.StartPoint().Row) + 1,
		EndLine:   int(n.EndPoint().Row) + 1,
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
		Receiver:  recv,
	}
}

// cppIsFunctionDeclarator returns true if the (possibly pointer-wrapped)
// declarator ultimately contains a function_declarator. Used to distinguish
// method declarations from data-member declarations inside a class body.
func cppIsFunctionDeclarator(n *sitter.Node) bool {
	for n != nil {
		switch n.Type() {
		case "function_declarator":
			return true
		case "pointer_declarator", "reference_declarator", "parenthesized_declarator":
			n = n.ChildByFieldName("declarator")
		default:
			return false
		}
	}
	return false
}

// cppDeclaratorName walks a function_declarator (possibly wrapped in pointers
// / references / parens) and returns (name, qualifier). For `Foo::bar` the
// qualifier is "Foo" and name is "bar"; for plain `bar` qualifier is "".
func cppDeclaratorName(n *sitter.Node, src []byte) (string, string) {
	for n != nil {
		switch n.Type() {
		case "function_declarator":
			inner := n.ChildByFieldName("declarator")
			return cppLeafName(inner, src)
		case "pointer_declarator", "reference_declarator", "parenthesized_declarator":
			n = n.ChildByFieldName("declarator")
		default:
			return cppLeafName(n, src)
		}
	}
	return "", ""
}

// cppLeafName extracts a name from the innermost declarator node. Handles
// `identifier`, `field_identifier`, `qualified_identifier` (Foo::bar),
// `destructor_name`, `operator_name`, and `template_function`.
func cppLeafName(n *sitter.Node, src []byte) (string, string) {
	if n == nil {
		return "", ""
	}
	switch n.Type() {
	case "identifier", "field_identifier", "type_identifier":
		return n.Content(src), ""
	case "qualified_identifier":
		// scope is the qualifier (Foo, std::vector<int>, etc.); name is the leaf.
		scope := n.ChildByFieldName("scope")
		nameNode := n.ChildByFieldName("name")
		q := ""
		if scope != nil {
			q = strings.TrimSpace(scope.Content(src))
		}
		if nameNode != nil {
			name, innerQ := cppLeafName(nameNode, src)
			// Inner qualifier (rare nested case) prepends; usually empty.
			if innerQ != "" {
				q = strings.TrimSuffix(q, "::") + "::" + innerQ
			}
			return name, q
		}
		return "", q
	case "destructor_name":
		return strings.TrimSpace(n.Content(src)), ""
	case "operator_name":
		return strings.TrimSpace(n.Content(src)), ""
	case "template_function":
		nameNode := n.ChildByFieldName("name")
		if nameNode != nil {
			return cppLeafName(nameNode, src)
		}
	}
	return strings.TrimSpace(n.Content(src)), ""
}
