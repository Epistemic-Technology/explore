package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikethicke/explore/internal/gitsrc"
	"github.com/mikethicke/explore/internal/model"
)

// gitRepoWithHistory builds a repo with three commits and returns its root.
func gitRepoWithHistory(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	w := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("a.go", "package a\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "first")
	w("a.go", "package a\n\nfunc F() {}\n")
	w("b.go", "package a\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "add F and b")
	return root
}

func drain(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command, got nil")
	}
	return cmd()
}

func TestHistoryFlow(t *testing.T) {
	root := gitRepoWithHistory(t)
	repo, ok := gitsrc.Open(root)
	if !ok {
		t.Fatal("Open failed")
	}
	m := Model{
		repo:      repo,
		history:   newHistoryUI(),
		currentID: model.NodeID{Kind: model.KindFile, Path: "a.go"},
	}

	// Open History → focuses tree pane on the History tab and loads the log.
	cmd := m.openHistory()
	if m.activePane != paneTree || m.treeTab != treeTabHistory {
		t.Fatalf("openHistory didn't focus history tab: pane=%d tab=%d", m.activePane, m.treeTab)
	}
	msg := drain(t, cmd)
	lm, ok := msg.(gitLogMsg)
	if !ok {
		t.Fatalf("expected gitLogMsg, got %T", msg)
	}
	mi, _ := m.handleGitLogMsg(lm)
	m = mi.(Model)
	// WORKING is synthesized at index 0, then full branch history.
	if len(m.history.commits) != 3 {
		t.Fatalf("want WORKING + 2 commits, got %d", len(m.history.commits))
	}
	if m.history.commits[0].SHA != workingRef || m.history.commits[0].ShortSHA != "WORKING" {
		t.Fatalf("row 0 should be the WORKING sentinel, got %+v", m.history.commits[0])
	}
	if m.history.commits[1].Subject != "add F and b" {
		t.Fatalf("newest real commit expected at index 1, got %q", m.history.commits[1].Subject)
	}

	// handleGitLogMsg kicks a detail load for the focused row (WORKING).
	if !m.history.loadingShas[workingRef] {
		t.Fatal("expected WORKING detail to be loading")
	}

	out := m.renderHistory(60, 10)
	if !strings.Contains(out, "WORKING") || !strings.Contains(out, "add F and b") {
		t.Fatalf("renderHistory missing rows:\n%s", out)
	}

	// Move down from WORKING to the newest real commit; load its detail.
	mi, cmd = m.updateHistoryPane("j")
	m = mi.(Model)
	if m.history.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", m.history.cursor)
	}
	dmsg := drain(t, cmd)
	dm, ok := dmsg.(commitDetailMsg)
	if !ok {
		t.Fatalf("expected commitDetailMsg, got %T", dmsg)
	}
	mi, _ = m.handleCommitDetailMsg(dm)
	m = mi.(Model)

	detail := m.renderCommitDetail(80, 20)
	if !strings.Contains(detail, "add F and b") {
		t.Fatalf("renderCommitDetail missing commit subject:\n%s", detail)
	}
	if !strings.Contains(detail, "a.go") {
		t.Fatalf("renderCommitDetail missing changed file a.go:\n%s", detail)
	}
}

func TestRenderCommitExplain_States(t *testing.T) {
	root := gitRepoWithHistory(t)
	repo, _ := gitsrc.Open(root)
	m := Model{repo: repo, history: newHistoryUI(), treeTab: treeTabHistory}
	m.history.commits = []gitsrc.Commit{{SHA: "deadbeef", ShortSHA: "deadbee", Subject: "do a thing"}}

	if got := m.renderCommitExplain(80, 10); !strings.Contains(got, "Explaining what this commit changed") {
		t.Fatalf("want loading state, got:\n%s", got)
	}
	m.history.explain["deadbeef"] = &model.Explanation{Prose: "This commit renames Foo to Bar."}
	if got := m.renderCommitExplain(80, 10); !strings.Contains(got, "renames Foo to Bar") {
		t.Fatalf("want cached prose, got:\n%s", got)
	}
	delete(m.history.explain, "deadbeef")
	m.history.explainErr["deadbeef"] = errTest
	if got := m.renderCommitExplain(80, 10); !strings.Contains(got, "explain error") {
		t.Fatalf("want error state, got:\n%s", got)
	}
}

var errTest = &stubErr{}

type stubErr struct{}

func (*stubErr) Error() string { return "boom" }

func TestHistoryNotAGitRepo(t *testing.T) {
	m := Model{history: newHistoryUI()} // repo == nil
	cmd := m.openHistory()
	if cmd != nil {
		t.Fatal("expected nil cmd when not a git repo")
	}
	if m.treeTab == treeTabHistory {
		t.Fatal("should not switch to History tab without a repo")
	}
	if !strings.Contains(m.statusMsg, "not a git repository") {
		t.Fatalf("expected status hint, got %q", m.statusMsg)
	}
	if got := m.renderHistory(40, 5); !strings.Contains(got, "not a git repository") {
		t.Fatalf("renderHistory = %q", got)
	}
}
