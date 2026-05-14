package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikethicke/explore/internal/model"
)

func TestXrefEntries_ReadsMetadata(t *testing.T) {
	id := model.NodeID{Kind: model.KindSymbol, Path: "x.go", Symbol: "Foo"}
	callers := []model.SymbolRef{{Name: "A", Path: "a.go", Line: 1}}
	callees := []model.SymbolRef{{Name: "B", Path: "b.go", Line: 2}}
	m := Model{
		currentID: id,
		expCache: map[model.NodeID]*model.Explanation{
			id: {Metadata: model.Metadata{Callers: callers, Callees: callees}},
		},
	}
	if got := m.xrefEntries(true); len(got) != 1 || got[0].Name != "A" {
		t.Errorf("xrefEntries(up) = %+v, want one caller A", got)
	}
	if got := m.xrefEntries(false); len(got) != 1 || got[0].Name != "B" {
		t.Errorf("xrefEntries(down) = %+v, want one callee B", got)
	}
}

func TestXrefEntries_NoExplanationLoaded(t *testing.T) {
	m := Model{
		currentID: model.NodeID{Kind: model.KindFile, Path: "x.go"},
		expCache:  map[model.NodeID]*model.Explanation{},
	}
	if got := m.xrefEntries(true); got != nil {
		t.Errorf("expected nil before load; got %+v", got)
	}
}

func TestOpenXref_EmptyOnFileMentionsFocusKind(t *testing.T) {
	m := Model{currentID: model.NodeID{Kind: model.KindFile, Path: "x.go"}}
	m2, cmd := m.openXref("callers", nil)
	if cmd != nil {
		t.Errorf("expected no command; got %v", cmd)
	}
	if !strings.Contains(m2.(Model).statusMsg, "focus a symbol") {
		t.Errorf("status = %q, want hint to focus a symbol", m2.(Model).statusMsg)
	}
}

func TestOpenXref_EmptyBeforeExplanationLoads(t *testing.T) {
	id := model.NodeID{Kind: model.KindSymbol, Path: "x.go", Symbol: "Foo"}
	m := Model{currentID: id, expCache: map[model.NodeID]*model.Explanation{}}
	m2, _ := m.openXref("callees", nil)
	if !strings.Contains(m2.(Model).statusMsg, "not loaded yet") {
		t.Errorf("status = %q, want 'not loaded yet'", m2.(Model).statusMsg)
	}
}

func TestOpenXref_EmptyAfterExplanationLoadedMentionsLSP(t *testing.T) {
	id := model.NodeID{Kind: model.KindSymbol, Path: "x.go", Symbol: "Foo"}
	m := Model{
		currentID: id,
		expCache:  map[model.NodeID]*model.Explanation{id: {}},
	}
	m2, _ := m.openXref("callers", nil)
	if !strings.Contains(m2.(Model).statusMsg, "LSP") {
		t.Errorf("status = %q, want LSP hint", m2.(Model).statusMsg)
	}
}

func TestOpenXref_MultiOpensOverlay(t *testing.T) {
	entries := []model.SymbolRef{
		{Name: "A", Path: "a.go", Line: 1},
		{Name: "B", Path: "b.go", Line: 2},
	}
	m := Model{}
	m2, _ := m.openXref("callees", entries)
	mm := m2.(Model)
	if !mm.xref.open {
		t.Fatalf("overlay should open with multi entries")
	}
	if mm.xref.kind != "callees" {
		t.Errorf("kind = %q, want callees", mm.xref.kind)
	}
	if len(mm.xref.entries) != 2 {
		t.Errorf("entries = %d, want 2", len(mm.xref.entries))
	}
	if mm.xref.cursor != 0 {
		t.Errorf("cursor should start at 0; got %d", mm.xref.cursor)
	}
}

func TestUpdateXref_NavigationAndClose(t *testing.T) {
	entries := []model.SymbolRef{
		{Name: "A", Path: "a.go"},
		{Name: "B", Path: "b.go"},
		{Name: "C", Path: "c.go"},
	}
	m := Model{xref: xrefUI{open: true, kind: "callers", entries: entries}}

	// down → cursor = 1
	m2, _ := m.updateXref(key('j'))
	if m2.(Model).xref.cursor != 1 {
		t.Errorf("j: cursor = %d, want 1", m2.(Model).xref.cursor)
	}
	// down again → cursor = 2
	m3, _ := m2.(Model).updateXref(key('j'))
	if m3.(Model).xref.cursor != 2 {
		t.Errorf("jj: cursor = %d, want 2", m3.(Model).xref.cursor)
	}
	// down past end → stays at 2
	m4, _ := m3.(Model).updateXref(key('j'))
	if m4.(Model).xref.cursor != 2 {
		t.Errorf("jjj: cursor = %d, want 2 (clamped)", m4.(Model).xref.cursor)
	}
	// up → cursor = 1
	m5, _ := m4.(Model).updateXref(key('k'))
	if m5.(Model).xref.cursor != 1 {
		t.Errorf("k: cursor = %d, want 1", m5.(Model).xref.cursor)
	}
	// esc closes
	m6, _ := m5.(Model).updateXref(tea.KeyMsg{Type: tea.KeyEsc})
	if m6.(Model).xref.open {
		t.Errorf("esc should close the overlay")
	}
}

func TestJumpToSymbolRef_EmptyPath(t *testing.T) {
	_, tr := scaffoldRepo(t)
	m := Model{tree: tr}
	cmd := m.jumpToSymbolRef(model.SymbolRef{})
	if cmd != nil {
		t.Errorf("expected nil cmd for empty path")
	}
	if !strings.Contains(m.statusMsg, "no path") {
		t.Errorf("expected 'no path' status; got %q", m.statusMsg)
	}
}

func TestOpenCalleesOnLine_NotInSourcePane(t *testing.T) {
	m := Model{activePane: paneTree, currentFile: "x.go", sourceLine: 5}
	m2, cmd := m.openCalleesOnLine()
	if cmd != nil {
		t.Errorf("expected no command when not in source pane")
	}
	if !strings.Contains(m2.(Model).statusMsg, "source pane") {
		t.Errorf("status = %q, want hint about source pane", m2.(Model).statusMsg)
	}
}

func TestOpenCalleesOnLine_NoFile(t *testing.T) {
	m := Model{activePane: paneSrc, currentFile: "", sourceLine: 5}
	m2, cmd := m.openCalleesOnLine()
	if cmd != nil {
		t.Errorf("expected no command when no file")
	}
	if !strings.Contains(m2.(Model).statusMsg, "no file") {
		t.Errorf("status = %q, want 'no file'", m2.(Model).statusMsg)
	}
}

func TestHandleCalleesOnLineMsg_NoSitesAndNoRefs(t *testing.T) {
	m := Model{}
	m2, _ := m.handleCalleesOnLineMsg(calleesOnLineMsg{refs: nil, sitesFound: 0, line: 42})
	if !strings.Contains(m2.(Model).statusMsg, "no calls on line 42") {
		t.Errorf("status = %q, want 'no calls on line 42'", m2.(Model).statusMsg)
	}
}

func TestHandleCalleesOnLineMsg_SitesButLSPUnresolved(t *testing.T) {
	m := Model{}
	m2, _ := m.handleCalleesOnLineMsg(calleesOnLineMsg{refs: nil, sitesFound: 2, line: 30})
	got := m2.(Model).statusMsg
	if !strings.Contains(got, "2 call") || !strings.Contains(got, "LSP couldn't resolve") {
		t.Errorf("status = %q, want LSP-unresolved diagnostic with site count", got)
	}
}

func TestOpenCallersOf_NotASymbol(t *testing.T) {
	m := Model{currentID: model.NodeID{Kind: model.KindFile, Path: "x.go"}}
	m2, cmd := m.openCallersOf()
	if cmd != nil {
		t.Errorf("expected no command when focus is a file")
	}
	if !strings.Contains(m2.(Model).statusMsg, "focus a symbol") {
		t.Errorf("status = %q, want 'focus a symbol' hint", m2.(Model).statusMsg)
	}
}

func TestHandleCallersResultMsg_SymbolNotFound(t *testing.T) {
	id := model.NodeID{Kind: model.KindSymbol, Path: "x.go", Symbol: "Foo"}
	m := Model{}
	m2, _ := m.handleCallersResultMsg(callersResultMsg{symbolFound: false, id: id})
	if !strings.Contains(m2.(Model).statusMsg, "not found in x.go") {
		t.Errorf("status = %q, want 'not found' diagnostic", m2.(Model).statusMsg)
	}
}

func TestHandleCallersResultMsg_FoundButNoRefs(t *testing.T) {
	id := model.NodeID{Kind: model.KindSymbol, Path: "x.go", Symbol: "Foo"}
	m := Model{}
	m2, _ := m.handleCallersResultMsg(callersResultMsg{symbolFound: true, refs: nil, id: id})
	got := m2.(Model).statusMsg
	if !strings.Contains(got, "no callers of Foo") || !strings.Contains(got, "LSP") {
		t.Errorf("status = %q, want LSP-empty diagnostic", got)
	}
}

func TestHandleCallersResultMsg_TimeoutShowsCleanMessage(t *testing.T) {
	id := model.NodeID{Kind: model.KindSymbol, Path: "x.go", Symbol: "Foo"}
	m := Model{}
	m2, _ := m.handleCallersResultMsg(callersResultMsg{err: context.DeadlineExceeded, id: id})
	got := m2.(Model).statusMsg
	if !strings.Contains(got, "timed out") || !strings.Contains(got, "LSP slow") {
		t.Errorf("status = %q, want timeout diagnostic", got)
	}
}

func TestHandleCalleesOnLineMsg_TimeoutShowsCleanMessage(t *testing.T) {
	m := Model{}
	m2, _ := m.handleCalleesOnLineMsg(calleesOnLineMsg{err: context.DeadlineExceeded, line: 30})
	got := m2.(Model).statusMsg
	if !strings.Contains(got, "timed out") || !strings.Contains(got, "LSP slow") {
		t.Errorf("status = %q, want timeout diagnostic", got)
	}
}

func TestHandleCallersResultMsg_MultiOpensPicker(t *testing.T) {
	refs := []model.SymbolRef{
		{Name: "Foo", Path: "a.go", Line: 1},
		{Name: "Foo", Path: "b.go", Line: 1},
	}
	id := model.NodeID{Kind: model.KindSymbol, Path: "x.go", Symbol: "Foo"}
	m := Model{}
	m2, _ := m.handleCallersResultMsg(callersResultMsg{symbolFound: true, refs: refs, id: id})
	if !m2.(Model).xref.open {
		t.Errorf("expected picker to open with multi refs")
	}
	if m2.(Model).xref.kind != "callers" {
		t.Errorf("kind = %q, want callers", m2.(Model).xref.kind)
	}
}

func TestHandleCalleesOnLineMsg_MultiOpensPicker(t *testing.T) {
	refs := []model.SymbolRef{
		{Name: "A", Path: "a.go", Line: 1},
		{Name: "B", Path: "b.go", Line: 1},
	}
	m := Model{}
	m2, _ := m.handleCalleesOnLineMsg(calleesOnLineMsg{refs: refs, line: 1})
	if !m2.(Model).xref.open {
		t.Errorf("expected picker to open with multi refs")
	}
	if m2.(Model).xref.kind != "callees" {
		t.Errorf("kind = %q, want callees", m2.(Model).xref.kind)
	}
}

func TestBuildXrefPreview_CentersOnLine(t *testing.T) {
	root, _ := scaffoldRepo(t) // creates auth/session.go with 5 lines
	ref := model.SymbolRef{Path: filepath.Join("auth", "session.go"), Line: 3, Name: "Verify"}
	got := buildXrefPreview(root, ref, xrefPreviewLines)
	// The target line "type Session struct{}" is line 3.
	if !strings.Contains(got, "type Session struct") {
		t.Errorf("preview should contain target line; got:\n%s", got)
	}
	// Marker should be on the target line.
	if !strings.Contains(got, "▸") {
		t.Errorf("preview should mark target line with ▸; got:\n%s", got)
	}
}

func TestBuildXrefPreview_MissingFile(t *testing.T) {
	root := t.TempDir()
	ref := model.SymbolRef{Path: "no/such.go", Line: 5}
	got := buildXrefPreview(root, ref, xrefPreviewLines)
	if !strings.Contains(got, "unavailable") {
		t.Errorf("expected 'unavailable' placeholder; got %q", got)
	}
}

func TestBuildXrefPreview_LineOutOfRange(t *testing.T) {
	root, _ := scaffoldRepo(t)
	ref := model.SymbolRef{Path: filepath.Join("auth", "session.go"), Line: 9999}
	got := buildXrefPreview(root, ref, xrefPreviewLines)
	if !strings.Contains(got, "out of range") {
		t.Errorf("expected 'out of range' placeholder; got %q", got)
	}
}

func TestSplitXrefWidth(t *testing.T) {
	listW, previewW := splitXrefWidth(40)
	if previewW != 0 {
		t.Errorf("narrow terminal should disable preview; got listW=%d previewW=%d", listW, previewW)
	}
	listW, previewW = splitXrefWidth(100)
	if previewW < 28 {
		t.Errorf("100-col terminal should have a preview pane; got listW=%d previewW=%d", listW, previewW)
	}
}
