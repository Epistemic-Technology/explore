package tsparse

import (
	"context"
	"testing"

	"github.com/mikethicke/explore/internal/model"
)

func TestParseRuby(t *testing.T) {
	src := []byte(`require "json"
require_relative "../foo"

MAX_RETRIES = 5
DEFAULT_NAME = "world"

def greet(name)
  "hello #{name}"
end

class User
  attr_reader :id, :name

  def initialize(id, name)
    @id = id
    @name = name
  end

  def hello
    greet(@name)
  end

  def self.find(id)
    new(id, "anon")
  end
end

module Greeter
  def self.banner
    "hi"
  end

  def shout
    "HELLO"
  end
end
`)
	pf, err := Parse(context.Background(), "u.rb", src)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Lang != LangRuby {
		t.Fatalf("Lang = %q, want ruby", pf.Lang)
	}

	type sig struct {
		kind model.SymbolKind
		recv string
	}
	got := map[string]sig{}
	for _, s := range pf.Symbols {
		got[s.Name] = sig{kind: s.Kind, recv: s.Receiver}
	}

	wantType := []string{"User", "Greeter"}
	for _, w := range wantType {
		if got[w].kind != model.SymType {
			t.Errorf("%q kind = %v, want type; got=%v", w, got[w].kind, got)
		}
	}
	if got["greet"].kind != model.SymFunc {
		t.Errorf("greet kind = %v, want func", got["greet"].kind)
	}
	if got["MAX_RETRIES"].kind != model.SymConst {
		t.Errorf("MAX_RETRIES kind = %v, want const", got["MAX_RETRIES"].kind)
	}
	if got["initialize"].kind != model.SymMethod || got["initialize"].recv != "User" {
		t.Errorf("initialize = %+v, want method/User", got["initialize"])
	}
	if got["hello"].kind != model.SymMethod || got["hello"].recv != "User" {
		t.Errorf("hello = %+v, want method/User", got["hello"])
	}
	if got["find"].kind != model.SymMethod || got["find"].recv != "User" {
		t.Errorf("find = %+v, want method/User (singleton)", got["find"])
	}
	if got["shout"].kind != model.SymMethod || got["shout"].recv != "Greeter" {
		t.Errorf("shout = %+v, want method/Greeter", got["shout"])
	}
	if got["banner"].kind != model.SymMethod || got["banner"].recv != "Greeter" {
		t.Errorf("banner = %+v, want method/Greeter (singleton)", got["banner"])
	}

	imports := map[string]bool{}
	for _, im := range pf.Imports {
		imports[im] = true
	}
	if !imports["json"] {
		t.Errorf("expected json import; got %v", pf.Imports)
	}
	if !imports["../foo"] {
		t.Errorf("expected ../foo require_relative; got %v", pf.Imports)
	}
}

func TestParseRuby_TopLevelSingleton(t *testing.T) {
	src := []byte(`def self.bare
  42
end
`)
	pf, err := Parse(context.Background(), "x.rb", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Symbols) != 1 {
		t.Fatalf("symbols = %d, want 1", len(pf.Symbols))
	}
	s := pf.Symbols[0]
	if s.Name != "bare" || s.Kind != model.SymMethod || s.Receiver != "self" {
		t.Errorf("got %+v, want bare/method/self", s)
	}
}
