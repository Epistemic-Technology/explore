package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/model"
)

func TestSelectionRange_NotSelecting(t *testing.T) {
	m := Model{sourceLine: 7}
	s, e := m.selectionRange()
	if s != 7 || e != 7 {
		t.Errorf("selectionRange = (%d,%d), want (7,7)", s, e)
	}
}

func TestSelectionRange_AnchorBelowCursor(t *testing.T) {
	m := Model{selecting: true, selectAnchor: 3, sourceLine: 9}
	s, e := m.selectionRange()
	if s != 3 || e != 9 {
		t.Errorf("selectionRange = (%d,%d), want (3,9)", s, e)
	}
}

func TestSelectionRange_AnchorAboveCursor(t *testing.T) {
	m := Model{selecting: true, selectAnchor: 12, sourceLine: 4}
	s, e := m.selectionRange()
	if s != 4 || e != 12 {
		t.Errorf("selectionRange = (%d,%d), want (4,12)", s, e)
	}
}

func TestSliceLines_InRange(t *testing.T) {
	src := "a\nbb\nccc\ndddd\n"
	got := sliceLines(src, 2, 3)
	if got != "bb\nccc" {
		t.Errorf("sliceLines = %q, want %q", got, "bb\nccc")
	}
}

func TestSliceLines_SingleLine(t *testing.T) {
	src := "a\nbb\nccc"
	got := sliceLines(src, 2, 2)
	if got != "bb" {
		t.Errorf("sliceLines = %q, want %q", got, "bb")
	}
}

func TestSliceLines_ClampsEnd(t *testing.T) {
	src := "a\nbb\nccc"
	got := sliceLines(src, 2, 100)
	if got != "bb\nccc" {
		t.Errorf("sliceLines = %q, want clamped to %q", got, "bb\nccc")
	}
}

func TestSliceLines_OutOfRange(t *testing.T) {
	src := "a\nbb"
	if got := sliceLines(src, 10, 12); got != "" {
		t.Errorf("sliceLines out-of-range = %q, want empty", got)
	}
	if got := sliceLines(src, 0, 1); got != "" {
		t.Errorf("sliceLines start<1 = %q, want empty", got)
	}
	if got := sliceLines(src, 3, 2); got != "" {
		t.Errorf("sliceLines inverted = %q, want empty", got)
	}
}

func TestSourcePane_VTogglesSelection(t *testing.T) {
	m := Model{activePane: paneSrc, currentFile: "x.go", sourceLine: 5}
	m2, _ := m.updateSourcePane("v")
	mm := m2.(Model)
	if !mm.selecting || mm.selectAnchor != 5 {
		t.Errorf("after v: selecting=%v anchor=%d, want true, 5", mm.selecting, mm.selectAnchor)
	}
	m3, _ := mm.updateSourcePane("v")
	mmm := m3.(Model)
	if mmm.selecting {
		t.Errorf("second v should exit selection mode")
	}
}

func TestSourcePane_VNoOpWhenNoFile(t *testing.T) {
	m := Model{activePane: paneSrc, currentFile: ""}
	m2, _ := m.updateSourcePane("v")
	if m2.(Model).selecting {
		t.Errorf("v with no file should not enter selection mode")
	}
}

func TestSourcePane_EscCancelsSelection(t *testing.T) {
	m := Model{activePane: paneSrc, currentFile: "x.go", selecting: true, selectAnchor: 3, sourceLine: 7}
	m2, _ := m.updateSourcePane("esc")
	if m2.(Model).selecting {
		t.Errorf("Esc should cancel selection")
	}
}

func TestExplainSelection_NoFile(t *testing.T) {
	m := Model{}
	m2, cmd := m.explainSelection()
	if cmd != nil {
		t.Errorf("explainSelection without file should return nil cmd, got %v", cmd)
	}
	if !strings.Contains(m2.(Model).statusMsg, "no file") {
		t.Errorf("expected 'no file' status; got %q", m2.(Model).statusMsg)
	}
}

func TestExplainSelection_SourceNotLoaded(t *testing.T) {
	m := Model{currentFile: "x.go", sourceCache: map[string]string{}}
	m2, _ := m.explainSelection()
	if !strings.Contains(m2.(Model).statusMsg, "not loaded") {
		t.Errorf("expected 'not loaded' status; got %q", m2.(Model).statusMsg)
	}
}

func TestExplainSelection_EmptySelection(t *testing.T) {
	// Cursor past EOF leaves an empty snippet.
	m := Model{
		currentFile: "x.go",
		sourceCache: map[string]string{"x.go": "a\nb\n"},
		sourceLine:  99,
		qa:          qaState{nodeThreads: map[model.NodeID][]llm.Turn{}},
	}
	m2, cmd := m.explainSelection()
	if cmd != nil {
		t.Errorf("empty selection should not dispatch a cmd")
	}
	if !strings.Contains(m2.(Model).statusMsg, "empty") {
		t.Errorf("expected 'empty' status; got %q", m2.(Model).statusMsg)
	}
}

func TestExplainSelection_SingleLineQueuesNodeTurn(t *testing.T) {
	id := model.NodeID{Kind: model.KindFile, Path: "x.go"}
	m := Model{
		currentFile: "x.go",
		currentID:   id,
		sourceCache: map[string]string{"x.go": "alpha\nbeta\ngamma"},
		sourceLine:  2,
		qa:          qaState{nodeThreads: map[model.NodeID][]llm.Turn{}},
	}
	m2, _ := m.explainSelection()
	mm := m2.(Model)
	turns := mm.qa.nodeThreads[id]
	if len(turns) != 1 {
		t.Fatalf("expected 1 node-thread turn, got %d", len(turns))
	}
	if turns[0].Role != "user" {
		t.Errorf("queued turn role = %q, want user", turns[0].Role)
	}
	if !strings.Contains(turns[0].Content, "line 2") {
		t.Errorf("turn content should mention 'line 2'; got %q", turns[0].Content)
	}
	if !strings.Contains(turns[0].Content, "beta") {
		t.Errorf("turn content should include snippet 'beta'; got %q", turns[0].Content)
	}
	if mm.activePane != paneExp || mm.expTab != expTabNodeQA {
		t.Errorf("should switch to node Q&A tab; pane=%d tab=%d", mm.activePane, mm.expTab)
	}
	if !mm.qa.inputActive {
		t.Errorf("explaining a selection should leave the Q&A input in insert mode")
	}
}

func TestExplainSelection_MultiLineUsesRange(t *testing.T) {
	id := model.NodeID{Kind: model.KindFile, Path: "x.go"}
	m := Model{
		currentFile:  "x.go",
		currentID:    id,
		sourceCache:  map[string]string{"x.go": "one\ntwo\nthree\nfour"},
		sourceLine:   4,
		selecting:    true,
		selectAnchor: 2,
		qa:           qaState{nodeThreads: map[model.NodeID][]llm.Turn{}},
	}
	m2, _ := m.explainSelection()
	mm := m2.(Model)
	turns := mm.qa.nodeThreads[id]
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if !strings.Contains(turns[0].Content, "lines 2-4") {
		t.Errorf("turn content should mention 'lines 2-4'; got %q", turns[0].Content)
	}
	for _, sub := range []string{"two", "three", "four"} {
		if !strings.Contains(turns[0].Content, sub) {
			t.Errorf("turn content should include %q; got %q", sub, turns[0].Content)
		}
	}
	if mm.selecting {
		t.Errorf("explain should clear selection mode")
	}
}

func TestStatusHints_SourcePane_SelectingMode(t *testing.T) {
	m := Model{activePane: paneSrc, selecting: true}
	got := m.statusHints()
	if !strings.Contains(got, "extend") {
		t.Errorf("selecting hints should mention 'extend'; got %q", got)
	}
	if !strings.Contains(got, "explain") {
		t.Errorf("selecting hints should mention 'explain'; got %q", got)
	}
}

func TestSourcePane_ColonEntersLineMode(t *testing.T) {
	m := Model{activePane: paneSrc, currentFile: "x.go"}
	m2, _ := m.updateSourcePane(":")
	mm := m2.(Model)
	if !mm.lineEntry {
		t.Fatalf("':' should enter line-entry mode")
	}
	if mm.lineBuf != "" {
		t.Errorf("line buffer should start empty; got %q", mm.lineBuf)
	}
}

func TestLineEntry_BuildsBufferAndBackspace(t *testing.T) {
	m := Model{lineEntry: true}
	m2, _ := m.updateLineEntry(key('1'))
	m3, _ := m2.(Model).updateLineEntry(key('2'))
	m4, _ := m3.(Model).updateLineEntry(key('x')) // non-digit ignored
	mm := m4.(Model)
	if mm.lineBuf != "12" {
		t.Fatalf("lineBuf = %q, want \"12\" (non-digits ignored)", mm.lineBuf)
	}
	m5, _ := mm.updateLineEntry(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := m5.(Model).lineBuf; got != "1" {
		t.Errorf("after backspace lineBuf = %q, want \"1\"", got)
	}
}

func TestLineEntry_EnterExitsMode(t *testing.T) {
	// No matching sourceCache entry: jumpSource returns early, so this
	// isolates the widget's own exit behaviour from the jump side effect.
	m := Model{lineEntry: true, lineBuf: "12", currentFile: "x.go"}
	m2, _ := m.updateLineEntry(tea.KeyMsg{Type: tea.KeyEnter})
	mm := m2.(Model)
	if mm.lineEntry {
		t.Errorf("Enter should leave line-entry mode")
	}
	if mm.lineBuf != "" {
		t.Errorf("Enter should clear the buffer; got %q", mm.lineBuf)
	}
}

func TestLineEntry_EscCancels(t *testing.T) {
	m := Model{lineEntry: true, lineBuf: "42", sourceLine: 1}
	m2, _ := m.updateLineEntry(tea.KeyMsg{Type: tea.KeyEsc})
	mm := m2.(Model)
	if mm.lineEntry || mm.lineBuf != "" {
		t.Errorf("Esc should cancel and clear buffer; lineEntry=%v buf=%q", mm.lineEntry, mm.lineBuf)
	}
	if mm.sourceLine != 1 {
		t.Errorf("Esc should not move the cursor; sourceLine=%d", mm.sourceLine)
	}
}

func TestNav_HNoLongerOpensHelp(t *testing.T) {
	m := Model{activePane: paneSrc}
	m2, _ := m.updateNav(key('h'))
	if m2.(Model).helpOpen {
		t.Errorf("'h' should no longer open help (it's pane-local now)")
	}
}
