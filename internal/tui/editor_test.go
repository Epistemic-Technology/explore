package tui

import (
	"testing"

	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

func TestOpenEditor_NoFileSelected(t *testing.T) {
	m := Model{currentFile: ""}
	t.Setenv("EDITOR", "vim")
	m2, cmd := m.openEditor()
	if cmd != nil {
		t.Errorf("expected no command when no file is selected, got %v", cmd)
	}
	if !contains(m2.(Model).statusMsg, "no file") {
		t.Errorf("expected status message about missing file, got %q", m2.(Model).statusMsg)
	}
}

func TestOpenEditor_NoEditorEnv(t *testing.T) {
	m := Model{currentFile: "foo.go"}
	t.Setenv("EDITOR", "")
	m2, cmd := m.openEditor()
	if cmd != nil {
		t.Errorf("expected no command when EDITOR unset, got %v", cmd)
	}
	if !contains(m2.(Model).statusMsg, "EDITOR") {
		t.Errorf("expected status message mentioning EDITOR, got %q", m2.(Model).statusMsg)
	}
}

func TestEditorLine_PrefersSourceLine(t *testing.T) {
	m := Model{
		currentFile: "f.go",
		sourceLine:  42,
		currentID:   model.NodeID{Kind: model.KindSymbol, Path: "f.go", Symbol: "Bar"},
		parsedCache: map[string]*tsparse.ParsedFile{
			"f.go": {Symbols: []model.Symbol{{Name: "Bar", StartLine: 10}}},
		},
	}
	if got := m.editorLine(); got != 42 {
		t.Errorf("editorLine = %d, want 42 (sourceLine should win)", got)
	}
}

func TestEditorLine_FallsBackToSymbolStart(t *testing.T) {
	m := Model{
		currentFile: "f.go",
		sourceLine:  0,
		currentID:   model.NodeID{Kind: model.KindSymbol, Path: "f.go", Symbol: "Bar"},
		parsedCache: map[string]*tsparse.ParsedFile{
			"f.go": {Symbols: []model.Symbol{{Name: "Bar", StartLine: 10}}},
		},
	}
	if got := m.editorLine(); got != 10 {
		t.Errorf("editorLine = %d, want 10 (symbol start)", got)
	}
}

func TestEditorLine_FileFocusReturnsZero(t *testing.T) {
	m := Model{
		currentFile: "f.go",
		sourceLine:  0,
		currentID:   model.NodeID{Kind: model.KindFile, Path: "f.go"},
	}
	if got := m.editorLine(); got != 0 {
		t.Errorf("editorLine = %d, want 0 (no line preference for file focus)", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
