package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

// key builds a synthetic key message matching a single-rune press, like the
// real tea.KeyMsg you'd get from the user typing `y`.
func key(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestYankPath_FormatsNodeID(t *testing.T) {
	m := Model{
		currentID: model.NodeID{Kind: model.KindSymbol, Path: "foo/bar.go", Symbol: "Baz"},
	}
	m2, _ := m.yankPath()
	got := m2.(Model).statusMsg
	if !strings.Contains(got, "foo/bar.go::Baz") {
		t.Errorf("status = %q, want NodeID rendering", got)
	}
	if !strings.HasPrefix(got, "yanked path") {
		t.Errorf("status should start with 'yanked path'; got %q", got)
	}
}

func TestYankExplanation_NoExplanationYet(t *testing.T) {
	m := Model{
		currentID: model.NodeID{Kind: model.KindFile, Path: "x.go"},
		expCache:  map[model.NodeID]*model.Explanation{},
	}
	m2, _ := m.yankExplanation()
	if !strings.Contains(m2.(Model).statusMsg, "not loaded") {
		t.Errorf("expected 'not loaded' message; got %q", m2.(Model).statusMsg)
	}
}

func TestYankExplanation_CopiesProse(t *testing.T) {
	id := model.NodeID{Kind: model.KindFile, Path: "x.go"}
	m := Model{
		currentID: id,
		expCache: map[model.NodeID]*model.Explanation{
			id: {Prose: "hello explanation"},
		},
	}
	m2, _ := m.yankExplanation()
	got := m2.(Model).statusMsg
	if !strings.HasPrefix(got, "yanked explanation") {
		t.Errorf("status should start with 'yanked explanation'; got %q", got)
	}
	if !strings.Contains(got, "hello explanation") {
		t.Errorf("preview missing prose; got %q", got)
	}
}

func TestYankSource_SymbolSliceFromParsedFile(t *testing.T) {
	src := "package x\nfunc Foo() {}\nfunc Bar() { return }\n"
	parsed := &tsparse.ParsedFile{
		Symbols: []model.Symbol{
			{Name: "Bar", StartByte: 24, EndByte: 46},
		},
	}
	m := Model{
		currentFile: "x.go",
		currentID:   model.NodeID{Kind: model.KindSymbol, Path: "x.go", Symbol: "Bar"},
		sourceCache: map[string]string{"x.go": src},
		parsedCache: map[string]*tsparse.ParsedFile{"x.go": parsed},
	}
	m2, _ := m.yankSource()
	got := m2.(Model).statusMsg
	if !strings.HasPrefix(got, "yanked source") {
		t.Errorf("status should start with 'yanked source'; got %q", got)
	}
	// The preview should reflect part of "func Bar() { return }", not the whole file.
	if !strings.Contains(got, "Bar") {
		t.Errorf("preview missing 'Bar'; got %q", got)
	}
}

func TestYankSource_NoFile(t *testing.T) {
	m := Model{currentFile: ""}
	m2, _ := m.yankSource()
	if !strings.Contains(m2.(Model).statusMsg, "no file") {
		t.Errorf("expected 'no file' message; got %q", m2.(Model).statusMsg)
	}
}

func TestYankLeader_YpDispatchesYankPath(t *testing.T) {
	m := Model{currentID: model.NodeID{Kind: model.KindFile, Path: "x.go"}}
	m2, _ := m.updateNav(key('y'))
	if !m2.(Model).pendingY {
		t.Fatalf("expected pendingY=true after `y`")
	}
	m3, _ := m2.(Model).updateNav(key('p'))
	if !strings.HasPrefix(m3.(Model).statusMsg, "yanked path") {
		t.Errorf("expected yanked path; got %q", m3.(Model).statusMsg)
	}
	if m3.(Model).pendingY {
		t.Errorf("pendingY should be cleared after sub-key")
	}
}

func TestYankLeader_OtherKeyCancels(t *testing.T) {
	m := Model{currentID: model.NodeID{Kind: model.KindFile, Path: "x.go"}}
	m2, _ := m.updateNav(key('y'))
	if !m2.(Model).pendingY {
		t.Fatalf("expected pendingY=true after `y`")
	}
	// `j` is not p/e/s — should cancel the yank.
	m3, _ := m2.(Model).updateNav(key('j'))
	if m3.(Model).pendingY {
		t.Errorf("pendingY should be cleared after non-yank key")
	}
}
