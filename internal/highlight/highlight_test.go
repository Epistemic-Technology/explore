package highlight

import (
	"context"
	"strings"
	"testing"

	"github.com/mikethicke/explore/internal/tsparse"
)

func TestNew_AllQueriesCompile(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, lang := range []tsparse.Language{
		tsparse.LangGo, tsparse.LangPython, tsparse.LangTypeScript, tsparse.LangTSX, tsparse.LangRust, tsparse.LangRuby, tsparse.LangJava, tsparse.LangCPP,
	} {
		if _, ok := h.queries[lang]; !ok {
			t.Errorf("query missing for %s", lang)
		}
	}
}

func TestHighlight_GoKeywordsAndStrings(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`package main

import "fmt"

func hello() string {
	return "hi"
}
`)
	spans := h.Highlight(context.Background(), src, tsparse.LangGo)
	if len(spans) == 0 {
		t.Fatalf("no spans returned")
	}
	kinds := map[Capture]bool{}
	for _, s := range spans {
		kinds[s.Kind] = true
	}
	for _, want := range []Capture{"keyword", "string", "function"} {
		if !kinds[want] {
			t.Errorf("expected %s capture; got kinds=%v", want, kinds)
		}
	}
	// Spans should be sorted and non-overlapping.
	for i := 1; i < len(spans); i++ {
		if spans[i].Start < spans[i-1].End {
			t.Errorf("overlap at i=%d: %+v vs %+v", i, spans[i-1], spans[i])
		}
	}
}

func TestHighlight_PythonClassAndComment(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`# header
class Foo:
    def hello(self):
        return "hi"
`)
	spans := h.Highlight(context.Background(), src, tsparse.LangPython)
	kinds := map[Capture]bool{}
	for _, s := range spans {
		kinds[s.Kind] = true
	}
	for _, want := range []Capture{"comment", "keyword", "type", "function", "string"} {
		if !kinds[want] {
			t.Errorf("expected %s; got %v", want, kinds)
		}
	}
}

func TestHighlight_RustImpl(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`fn main() {
    let x = 42;
    let s = "hi";
}
`)
	spans := h.Highlight(context.Background(), src, tsparse.LangRust)
	kinds := map[Capture]bool{}
	for _, s := range spans {
		kinds[s.Kind] = true
	}
	for _, want := range []Capture{"keyword", "function", "string", "number"} {
		if !kinds[want] {
			t.Errorf("expected %s; got %v", want, kinds)
		}
	}
}

func TestHighlight_Ruby(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`# header
class User
  def initialize(name)
    @name = name
  end
end
`)
	spans := h.Highlight(context.Background(), src, tsparse.LangRuby)
	kinds := map[Capture]bool{}
	for _, s := range spans {
		kinds[s.Kind] = true
	}
	for _, want := range []Capture{"comment", "keyword", "type", "function"} {
		if !kinds[want] {
			t.Errorf("expected %s; got %v", want, kinds)
		}
	}
}

func TestHighlight_Java(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`// header
public class User {
    public String greet(String name) {
        return "hi " + name;
    }
}
`)
	spans := h.Highlight(context.Background(), src, tsparse.LangJava)
	kinds := map[Capture]bool{}
	for _, s := range spans {
		kinds[s.Kind] = true
	}
	for _, want := range []Capture{"comment", "keyword", "type", "function", "string"} {
		if !kinds[want] {
			t.Errorf("expected %s; got %v", want, kinds)
		}
	}
}

func TestHighlight_CPP(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`// header
#include <string>

namespace app {
class User {
public:
    std::string greet() { return "hi"; }
};
}
`)
	spans := h.Highlight(context.Background(), src, tsparse.LangCPP)
	kinds := map[Capture]bool{}
	for _, s := range spans {
		kinds[s.Kind] = true
	}
	for _, want := range []Capture{"comment", "keyword", "type", "function", "string"} {
		if !kinds[want] {
			t.Errorf("expected %s; got %v", want, kinds)
		}
	}
}

func TestHighlight_TypeScriptInterface(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte(`export interface User { id: string; }
export function greet(u: User): string { return "hi " + u.id; }
`)
	spans := h.Highlight(context.Background(), src, tsparse.LangTypeScript)
	kinds := map[Capture]bool{}
	for _, s := range spans {
		kinds[s.Kind] = true
	}
	for _, want := range []Capture{"keyword", "type", "function", "string"} {
		if !kinds[want] {
			t.Errorf("expected %s; got %v", want, kinds)
		}
	}
}

func TestHighlight_InnermostWins(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte("func hello() {}\n")
	spans := h.Highlight(context.Background(), src, tsparse.LangGo)
	// Find the span over "hello". It should be @function, not the outer
	// function_declaration body (which doesn't directly map but if it did
	// the inner identifier would still win).
	var found bool
	for _, s := range spans {
		if string(src[s.Start:s.End]) == "hello" {
			if s.Kind != "function" {
				t.Errorf("expected hello to be @function, got %s", s.Kind)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("did not find span over hello in %v", spans)
	}
}

func TestHighlight_CacheReuse(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	src := []byte("package x\n")
	a := h.Highlight(context.Background(), src, tsparse.LangGo)
	b := h.Highlight(context.Background(), src, tsparse.LangGo)
	if &a[0] != &b[0] {
		// Different backing arrays — cache didn't reuse. We don't require
		// pointer-equality of slices in the API contract, just check the
		// cache map populated.
		key := hashKey(src, tsparse.LangGo)
		h.mu.Lock()
		_, ok := h.cache[key]
		h.mu.Unlock()
		if !ok {
			t.Errorf("cache entry missing after Highlight")
		}
	}
	if !strings.Contains("keyword type", string(a[0].Kind)) && len(a) > 0 {
		// Just sanity — keyword "package" should be present.
		hasKeyword := false
		for _, s := range a {
			if s.Kind == "keyword" {
				hasKeyword = true
				break
			}
		}
		if !hasKeyword {
			t.Errorf("no keyword span in %+v", a)
		}
	}
}
