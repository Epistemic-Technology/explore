package tsparse

import (
	"context"
	"testing"

	"github.com/mikethicke/explore/internal/model"
)

func TestParseTypeScript(t *testing.T) {
	src := []byte(`import { Foo } from "./foo";
import * as path from "node:path";

export const TIMEOUT = 1000;
const internalState = { count: 0 };
let counter = 0;

export function greet(name: string): string {
    return ` + "`hi ${name}`" + `;
}

export interface User {
    id: string;
    name: string;
}

export type ID = string | number;

export class Service {
    private cache = new Map<string, User>();
    constructor(public name: string) {}
    public find(id: ID): User | undefined {
        return this.cache.get(String(id));
    }
}
`)
	pf, err := Parse(context.Background(), "x.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Lang != LangTypeScript {
		t.Fatalf("Lang = %q, want typescript", pf.Lang)
	}

	got := map[string]model.SymbolKind{}
	for _, s := range pf.Symbols {
		got[s.Name] = s.Kind
	}

	tests := []struct {
		name string
		kind model.SymbolKind
	}{
		{"greet", model.SymFunc},
		{"Service", model.SymType},
		{"User", model.SymType},
		{"ID", model.SymType},
		{"TIMEOUT", model.SymConst},
		{"internalState", model.SymConst}, // const at top level
		{"counter", model.SymVar},
	}
	for _, c := range tests {
		if got[c.name] != c.kind {
			t.Errorf("symbol %q kind = %v, want %v", c.name, got[c.name], c.kind)
		}
	}

	imports := map[string]bool{}
	for _, im := range pf.Imports {
		imports[im] = true
	}
	if !imports["./foo"] || !imports["node:path"] {
		t.Errorf("imports missing; got %v", pf.Imports)
	}
}

func TestParseTSX(t *testing.T) {
	src := []byte(`import React from "react";

export function Button(props: { label: string }) {
    return <button>{props.label}</button>;
}
`)
	pf, err := Parse(context.Background(), "x.tsx", src)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Lang != LangTSX {
		t.Fatalf("Lang = %q, want tsx", pf.Lang)
	}
	names := map[string]bool{}
	for _, s := range pf.Symbols {
		names[s.Name] = true
	}
	if !names["Button"] {
		t.Errorf("expected Button; got %v", names)
	}
}
