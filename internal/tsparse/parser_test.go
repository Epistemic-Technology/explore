package tsparse

import (
	"context"
	"os"
	"testing"
)

func TestParseGoCache(t *testing.T) {
	src, err := os.ReadFile("../cache/cache.go")
	if err != nil {
		t.Fatal(err)
	}
	pf, err := Parse(context.Background(), "cache.go", src)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Lang != LangGo {
		t.Fatalf("expected Go, got %q", pf.Lang)
	}
	if len(pf.Imports) == 0 {
		t.Errorf("expected imports, got none")
	}
	got := map[string]bool{}
	for _, s := range pf.Symbols {
		got[s.Name] = true
	}
	for _, want := range []string{"Open", "HashSource", "Key", "Cache"} {
		if !got[want] {
			t.Errorf("missing symbol %q (have %v)", want, got)
		}
	}
}
