package tsparse

import (
	"context"
	"testing"

	"github.com/mikethicke/explore/internal/model"
)

func TestParseRust(t *testing.T) {
	src := []byte(`use std::collections::HashMap;
use std::io::{self, Read, Write};

const MAX: u32 = 100;
static GREETING: &str = "hi";

pub struct User {
    pub id: String,
    pub name: String,
}

pub enum Status {
    Active,
    Disabled,
}

pub trait Greeter {
    fn greet(&self) -> String;
}

pub fn parse(input: &str) -> Result<User, io::Error> {
    todo!()
}

impl User {
    pub fn new(id: String, name: String) -> Self {
        User { id, name }
    }

    pub fn id(&self) -> &str {
        &self.id
    }
}

impl Greeter for User {
    fn greet(&self) -> String {
        format!("hi {}", self.name)
    }
}
`)
	pf, err := Parse(context.Background(), "x.rs", src)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Lang != LangRust {
		t.Fatalf("Lang = %q, want rust", pf.Lang)
	}

	got := map[string]model.SymbolKind{}
	receivers := map[string]string{}
	for _, s := range pf.Symbols {
		got[s.Name] = s.Kind
		receivers[s.Name] = s.Receiver
	}

	tests := []struct {
		name string
		kind model.SymbolKind
	}{
		{"User", model.SymType},
		{"Status", model.SymType},
		{"Greeter", model.SymType},
		{"parse", model.SymFunc},
		{"MAX", model.SymConst},
		{"GREETING", model.SymVar},
	}
	for _, c := range tests {
		if got[c.name] != c.kind {
			t.Errorf("symbol %q kind = %v, want %v", c.name, got[c.name], c.kind)
		}
	}
	// impl methods should appear as SymMethod with receiver "User".
	if got["new"] != model.SymMethod || receivers["new"] != "User" {
		t.Errorf("new kind=%v recv=%q, want method/User", got["new"], receivers["new"])
	}
	if got["id"] != model.SymMethod {
		t.Errorf("id kind = %v, want method", got["id"])
	}
	// `greet` is defined in two impl blocks (inherent — well, trait impl only here);
	// the trait-impl version should still be picked up.
	if got["greet"] != model.SymMethod {
		t.Errorf("greet kind = %v, want method", got["greet"])
	}

	// Imports captured as cleaned use-decl text.
	if len(pf.Imports) < 2 {
		t.Errorf("expected at least 2 imports; got %v", pf.Imports)
	}
}
