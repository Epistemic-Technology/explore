package tsparse

import (
	"context"
	"testing"

	"github.com/mikethicke/explore/internal/model"
)

func TestParsePython(t *testing.T) {
	src := []byte(`from typing import Optional
import os
import os.path as op

MAX_RETRIES = 5
default_name = "world"

def greet(name: str) -> str:
    return f"hello {name}"

@staticmethod
def make() -> "Greeter":
    return Greeter()

class Greeter:
    def __init__(self, name: str):
        self.name = name

    def hello(self) -> str:
        return greet(self.name)
`)
	pf, err := Parse(context.Background(), "g.py", src)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Lang != LangPython {
		t.Fatalf("Lang = %q, want python", pf.Lang)
	}

	got := map[string]model.SymbolKind{}
	for _, s := range pf.Symbols {
		got[s.Name] = s.Kind
	}
	wantFunc := []string{"greet", "make"}
	for _, w := range wantFunc {
		if got[w] != model.SymFunc {
			t.Errorf("symbol %q kind = %v, want func; got=%v", w, got[w], got)
		}
	}
	if got["Greeter"] != model.SymType {
		t.Errorf("Greeter kind = %v, want type", got["Greeter"])
	}
	if got["MAX_RETRIES"] != model.SymConst {
		t.Errorf("MAX_RETRIES kind = %v, want const", got["MAX_RETRIES"])
	}
	if got["default_name"] != model.SymVar {
		t.Errorf("default_name kind = %v, want var", got["default_name"])
	}

	imports := map[string]bool{}
	for _, im := range pf.Imports {
		imports[im] = true
	}
	if !imports["typing"] && !imports["os"] {
		t.Errorf("expected typing/os imports; got %v", pf.Imports)
	}
}

func TestParsePython_DecoratedFunctionIncludesDecoratorRange(t *testing.T) {
	src := []byte(`@cache
def slow():
    return 42
`)
	pf, err := Parse(context.Background(), "x.py", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Symbols) != 1 {
		t.Fatalf("symbols = %d, want 1", len(pf.Symbols))
	}
	s := pf.Symbols[0]
	if s.StartLine != 1 {
		t.Errorf("decorated function should start at line 1 (decorator), got %d", s.StartLine)
	}
}
