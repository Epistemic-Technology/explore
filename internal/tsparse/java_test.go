package tsparse

import (
	"context"
	"testing"

	"github.com/mikethicke/explore/internal/model"
)

func TestParseJava(t *testing.T) {
	src := []byte(`package com.example;

import java.util.List;
import java.util.Map;
import static java.lang.Math.PI;

public interface Greeter {
    String greet(String name);
}

public class User implements Greeter {
    private final String id;
    private final String name;

    public User(String id, String name) {
        this.id = id;
        this.name = name;
    }

    public String getId() {
        return id;
    }

    public String greet(String name) {
        return "hi " + name;
    }
}

enum Status { ACTIVE, DISABLED }

record Point(int x, int y) {}
`)
	pf, err := Parse(context.Background(), "User.java", src)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Lang != LangJava {
		t.Fatalf("Lang = %q, want java", pf.Lang)
	}

	type sig struct {
		kind model.SymbolKind
		recv string
	}
	got := map[string]sig{}
	for _, s := range pf.Symbols {
		key := s.Name
		if s.Receiver != "" {
			key = s.Receiver + "." + s.Name
		}
		got[key] = sig{kind: s.Kind, recv: s.Receiver}
	}

	for _, w := range []string{"User", "Greeter", "Status", "Point"} {
		if got[w].kind != model.SymType {
			t.Errorf("%q kind = %v, want type", w, got[w].kind)
		}
	}
	for _, w := range []string{"User.getId", "User.greet", "User.<init>", "Greeter.greet"} {
		if got[w].kind != model.SymMethod {
			t.Errorf("%q kind = %v, want method (full got=%v)", w, got[w].kind, got)
		}
	}

	imports := map[string]bool{}
	for _, im := range pf.Imports {
		imports[im] = true
	}
	if !imports["java.util.List"] {
		t.Errorf("expected java.util.List; got %v", pf.Imports)
	}
	if !imports["java.lang.Math.PI"] {
		t.Errorf("expected static java.lang.Math.PI; got %v", pf.Imports)
	}
}
