package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mikethicke/explore/internal/highlight"
)

func init() {
	// Force a color profile so style.Render emits ANSI codes even when tests
	// run without a TTY. Otherwise lipgloss auto-degrades to plain text and
	// assertions about styling become impossible.
	lipgloss.SetColorProfile(termenv.ANSI256)
}

func TestSplitLinesWithOffsets(t *testing.T) {
	src := []byte("a\nbb\n\nccc")
	lines, offsets := splitLinesWithOffsets(src)
	wantLines := []string{"a", "bb", "", "ccc"}
	wantOffsets := []int{0, 2, 5, 6}
	if len(lines) != len(wantLines) {
		t.Fatalf("lines = %v, want %v", lines, wantLines)
	}
	for i := range lines {
		if lines[i] != wantLines[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], wantLines[i])
		}
		if offsets[i] != wantOffsets[i] {
			t.Errorf("offset %d = %d, want %d", i, offsets[i], wantOffsets[i])
		}
	}
}

func TestRenderLineSpans_AppliesAndPasses(t *testing.T) {
	// Line "func foo()" starting at global byte 10. Span over "func" (bytes 10..14)
	// captures keyword; span over "foo" (15..18) captures function.
	line := "func foo()"
	lineStart := 10
	lineEnd := lineStart + len(line)
	spans := []highlight.Span{
		{Start: 10, End: 14, Kind: "keyword"},
		{Start: 15, End: 18, Kind: "function"},
	}
	out := renderLineSpans(line, lineStart, lineEnd, spans, 0)
	// Plain text "()" survives.
	if !strings.Contains(out, "()") {
		t.Errorf("output missing trailing parens: %q", out)
	}
	// ANSI escapes present (keyword + function styles are not empty).
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("expected ANSI escapes in output, got %q", out)
	}
	// All four characters of "func" and three of "foo" must be in the output
	// (they're inside styled regions).
	if !strings.Contains(out, "func") || !strings.Contains(out, "foo") {
		t.Errorf("missing source text in styled output %q", out)
	}
}

func TestRenderLineSpans_NoOverlappingSpans(t *testing.T) {
	line := "hello"
	out := renderLineSpans(line, 0, len(line), nil, 0)
	if out != line {
		t.Errorf("no spans should return text verbatim; got %q", out)
	}
}
