package index

import (
	"strings"
	"testing"

	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

func TestOneLineSummary(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", "(empty)"},
		{"single sentence no period", "Manages session lifecycle", "Manages session lifecycle"},
		{"multi-sentence keeps first", "Manages session lifecycle. Tokens are HMAC-signed.", "Manages session lifecycle."},
		{"does not split on filename", "auth.go binds the handler. Then dispatches.", "auth.go binds the handler."},
		{"newlines collapse to space", "Line one.\nLine two.", "Line one."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := oneLineSummary(c.in)
			if got != c.want {
				t.Fatalf("oneLineSummary(%q): got %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestOneLineSummary_LongTruncated(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := oneLineSummary(long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
	if len(got) > 200 {
		t.Fatalf("expected truncation, got length %d", len(got))
	}
}

func TestSymbolCountBlurb(t *testing.T) {
	pf := &tsparse.ParsedFile{Symbols: []model.Symbol{
		{Kind: model.SymFunc}, {Kind: model.SymFunc},
		{Kind: model.SymType},
		{Kind: model.SymMethod},
	}}
	got := symbolCountBlurb(pf)
	// Ordering is fixed (func, method, type, var, const).
	want := "2 funcs, 1 method, 1 type"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSymbolCountBlurb_NoSymbols(t *testing.T) {
	pf := &tsparse.ParsedFile{}
	if got := symbolCountBlurb(pf); got != "(no symbols)" {
		t.Fatalf("got %q, want (no symbols)", got)
	}
}

func TestPlural(t *testing.T) {
	if plural("file", 1) != "file" {
		t.Fatalf("plural file 1 should not add s")
	}
	if plural("file", 0) != "files" {
		t.Fatalf("plural file 0 should add s (0 files)")
	}
	if plural("file", 2) != "files" {
		t.Fatalf("plural file 2 should add s")
	}
}

func TestLangForExt(t *testing.T) {
	cases := map[string]string{
		".go":   "Go",
		".PY":   "Python", // case-insensitive
		".tsx":  "TypeScript",
		".md":   "Markdown",
		".xyz":  "",
		".lock": "",
	}
	for ext, want := range cases {
		if got := langForExt(ext); got != want {
			t.Fatalf("langForExt(%q): got %q, want %q", ext, got, want)
		}
	}
}
