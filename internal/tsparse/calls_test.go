package tsparse

import (
	"context"
	"testing"
)

func TestFindCallSites_Go(t *testing.T) {
	src := []byte(`package x

import "fmt"

func Outer() {
	fmt.Println("hi")
	inner()
	obj.Method()
}

func inner() {}
`)
	// Outer body spans roughly the bytes from "func Outer" to its closing brace.
	// Pass a generous range that covers Outer but not inner.
	sites, err := FindCallSites(context.Background(), "x.go", src, 0, len(src)-len("\nfunc inner() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sites {
		names[s.Name] = true
	}
	for _, w := range []string{"Println", "inner", "Method"} {
		if !names[w] {
			t.Errorf("missing callee %q; got %v", w, names)
		}
	}
	// Verify the leaf is what byte at (Line, Column) matches Name — proves we
	// point at the identifier (not the receiver or whole expression).
	for _, s := range sites {
		off := byteOffsetForLineCol(src, s.Line, s.Column)
		if off < 0 || off+len(s.Name) > len(src) {
			t.Errorf("site %+v offset out of bounds", s)
			continue
		}
		got := string(src[off : off+len(s.Name)])
		if got != s.Name {
			t.Errorf("site %+v: bytes at offset = %q, want %q", s, got, s.Name)
		}
	}
}

func TestFindCallSites_Python(t *testing.T) {
	src := []byte(`def outer():
    print("hi")
    obj.method()
    inner()

def inner():
    pass
`)
	sites, err := FindCallSites(context.Background(), "x.py", src, 0, len(src))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sites {
		names[s.Name] = true
	}
	for _, w := range []string{"print", "method", "inner"} {
		if !names[w] {
			t.Errorf("missing callee %q; got %v", w, names)
		}
	}
}

func TestFindCallSites_Rust(t *testing.T) {
	src := []byte(`fn outer() {
    println!("hi");
    inner();
    obj.method();
}

fn inner() {}
`)
	sites, err := FindCallSites(context.Background(), "x.rs", src, 0, len(src))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sites {
		names[s.Name] = true
	}
	// println! is a macro (different AST node), so we don't catch it. inner and
	// method are normal calls.
	for _, w := range []string{"inner", "method"} {
		if !names[w] {
			t.Errorf("missing callee %q; got %v", w, names)
		}
	}
}

func TestFindCallSites_TypeScript(t *testing.T) {
	src := []byte(`function outer() {
    console.log("hi");
    inner();
    obj.method();
}

function inner() {}
`)
	sites, err := FindCallSites(context.Background(), "x.ts", src, 0, len(src))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sites {
		names[s.Name] = true
	}
	for _, w := range []string{"log", "inner", "method"} {
		if !names[w] {
			t.Errorf("missing callee %q; got %v", w, names)
		}
	}
}

func TestFindCallSites_Java(t *testing.T) {
	src := []byte(`class X {
    void outer() {
        System.out.println("hi");
        inner();
        obj.method();
    }
    void inner() {}
}
`)
	sites, err := FindCallSites(context.Background(), "X.java", src, 0, len(src))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sites {
		names[s.Name] = true
	}
	for _, w := range []string{"println", "inner", "method"} {
		if !names[w] {
			t.Errorf("missing callee %q; got %v", w, names)
		}
	}
}

func TestFindCallSites_Ruby(t *testing.T) {
	src := []byte(`def outer
  puts "hi"
  obj.method
  inner()
end

def inner
end
`)
	sites, err := FindCallSites(context.Background(), "x.rb", src, 0, len(src))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sites {
		names[s.Name] = true
	}
	// In Ruby `puts "hi"` is parsed as a command call (identifier+args), not a
	// `call` node — that's a known gap. Method calls with explicit receivers
	// and parenthesized invocations are caught.
	for _, w := range []string{"method", "inner"} {
		if !names[w] {
			t.Errorf("missing callee %q; got %v", w, names)
		}
	}
}

func TestFindCallSites_CPP(t *testing.T) {
	src := []byte(`#include <iostream>

void outer() {
    std::cout << "hi";
    inner();
    obj.method();
}

void inner() {}
`)
	sites, err := FindCallSites(context.Background(), "x.cc", src, 0, len(src))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sites {
		names[s.Name] = true
	}
	for _, w := range []string{"inner", "method"} {
		if !names[w] {
			t.Errorf("missing callee %q; got %v", w, names)
		}
	}
}

func TestFindCallSites_RangeFilter(t *testing.T) {
	src := []byte(`package x

func A() { inner() }
func B() { other() }
`)
	// Limit to just A's body — should find inner, not other.
	aIdx := indexOf(src, "func A")
	bIdx := indexOf(src, "func B")
	sites, err := FindCallSites(context.Background(), "x.go", src, aIdx, bIdx)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range sites {
		names[s.Name] = true
	}
	if !names["inner"] {
		t.Errorf("expected inner in range; got %v", names)
	}
	if names["other"] {
		t.Errorf("did not expect other outside range; got %v", names)
	}
}

func TestFindCallSites_UnsupportedLanguageReturnsNil(t *testing.T) {
	sites, err := FindCallSites(context.Background(), "x.unknown", []byte("..."), 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if sites != nil {
		t.Errorf("expected nil for unknown language; got %+v", sites)
	}
}

// indexOf returns the byte offset of the first occurrence of needle in s, or
// -1 if not found. Small util for tests.
func indexOf(s []byte, needle string) int {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(s); i++ {
		match := true
		for j := 0; j < len(n); j++ {
			if s[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// byteOffsetForLineCol converts (0-based line, 0-based byte column) into a
// byte offset in src. Returns -1 if the line doesn't exist.
func byteOffsetForLineCol(src []byte, line, col int) int {
	curLine := 0
	for i := 0; i < len(src); i++ {
		if curLine == line {
			return i + col
		}
		if src[i] == '\n' {
			curLine++
		}
	}
	return -1
}
