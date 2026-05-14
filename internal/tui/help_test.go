package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/model"
)

func TestHelp_HOpensOverlay(t *testing.T) {
	m := Model{}
	m2, _ := m.updateNav(key('h'))
	if !m2.(Model).helpOpen {
		t.Fatalf("expected helpOpen=true after 'h'")
	}
}

func TestHelp_AnyKeyCloses(t *testing.T) {
	m := Model{helpOpen: true}
	m2, _ := m.updateHelp(key('x'))
	if m2.(Model).helpOpen {
		t.Errorf("expected helpOpen=false after dismissal; got true")
	}
}

func TestHelp_EscCloses(t *testing.T) {
	m := Model{helpOpen: true}
	m2, _ := m.updateHelp(tea.KeyMsg{Type: tea.KeyEsc})
	if m2.(Model).helpOpen {
		t.Errorf("Esc should close help; helpOpen still true")
	}
}

func TestQA_EscDeactivatesInput(t *testing.T) {
	m := Model{
		activePane: paneExp,
		expTab:     expTabNodeQA,
		qa: qaState{
			inputActive: true,
			nodeThreads: map[model.NodeID][]llm.Turn{},
		},
	}
	m2, _ := m.updateQA(tea.KeyMsg{Type: tea.KeyEsc})
	if m2.(Model).qa.inputActive {
		t.Errorf("Esc should deactivate input; inputActive still true")
	}
	// And the QA tab should still be open (no auto-close on first Esc).
	if m2.(Model).expTab != expTabNodeQA {
		t.Errorf("first Esc should not leave QA tab; expTab=%d", m2.(Model).expTab)
	}
}

func TestQA_IReactivatesInput(t *testing.T) {
	m := Model{
		activePane: paneExp,
		expTab:     expTabNodeQA,
		qa:         qaState{inputActive: false},
	}
	m2, _ := m.updateNav(key('i'))
	if !m2.(Model).qa.inputActive {
		t.Errorf("'i' should reactivate input")
	}
}

func TestQA_EnterReactivatesInput(t *testing.T) {
	m := Model{
		activePane: paneExp,
		expTab:     expTabNodeQA,
		qa:         qaState{inputActive: false},
	}
	m2, _ := m.updateNav(tea.KeyMsg{Type: tea.KeyEnter})
	if !m2.(Model).qa.inputActive {
		t.Errorf("Enter should reactivate input from inactive state")
	}
}

func TestQA_SecondEscClosesTab(t *testing.T) {
	m := Model{
		activePane: paneExp,
		expTab:     expTabNodeQA,
		qa:         qaState{inputActive: false},
	}
	m2, _ := m.updateNav(tea.KeyMsg{Type: tea.KeyEsc})
	if m2.(Model).expTab != expTabPlain {
		t.Errorf("second Esc should drop back to plain explanation; expTab=%d", m2.(Model).expTab)
	}
}

func TestStatusHints_TreePane(t *testing.T) {
	m := Model{activePane: paneTree}
	got := m.statusHints()
	if !strings.Contains(got, "view") {
		t.Errorf("tree-pane hints should mention 'view'; got %q", got)
	}
	if !strings.Contains(got, "[h] help") {
		t.Errorf("tree-pane hints should advertise help; got %q", got)
	}
}

func TestStatusHints_SourcePane(t *testing.T) {
	m := Model{activePane: paneSrc}
	got := m.statusHints()
	if !strings.Contains(got, "callers") {
		t.Errorf("source-pane hints should mention callers; got %q", got)
	}
	if !strings.Contains(got, "callees") {
		t.Errorf("source-pane hints should mention callees; got %q", got)
	}
}

func TestStatusHints_QAActiveInput(t *testing.T) {
	m := Model{
		activePane: paneExp,
		expTab:     expTabNodeQA,
		qa:         qaState{inputActive: true},
	}
	got := m.statusHints()
	if !strings.Contains(got, "send") {
		t.Errorf("active-input hints should mention send; got %q", got)
	}
	if !strings.Contains(got, "stop typing") {
		t.Errorf("active-input hints should advertise Esc behaviour; got %q", got)
	}
}

func TestStatusHints_QAInactiveInput(t *testing.T) {
	m := Model{
		activePane: paneExp,
		expTab:     expTabNodeQA,
		qa:         qaState{inputActive: false},
	}
	got := m.statusHints()
	if !strings.Contains(got, "type") {
		t.Errorf("inactive-input hints should advertise i/⏎ to type; got %q", got)
	}
	if !strings.Contains(got, "close Q&A") {
		t.Errorf("inactive-input hints should advertise Esc to close; got %q", got)
	}
}

func TestStatusHints_HelpOpen(t *testing.T) {
	m := Model{helpOpen: true}
	got := m.statusHints()
	if !strings.Contains(got, "close") {
		t.Errorf("help-open hints should mention close; got %q", got)
	}
}

func TestQuestion_OpenSetsInputActive(t *testing.T) {
	m := Model{activePane: paneTree, qa: qaState{inputActive: false}}
	m2, _ := m.updateNav(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	mm := m2.(Model)
	if !mm.qa.inputActive {
		t.Errorf("? should activate input")
	}
	if mm.expTab != expTabNodeQA {
		t.Errorf("? should select node Q&A tab; expTab=%d", mm.expTab)
	}
	if mm.activePane != paneExp {
		t.Errorf("? should focus explanation pane; activePane=%d", mm.activePane)
	}
}
