package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/gitsrc"
	"github.com/mikethicke/explore/internal/index"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/model"
)

type nopProvider struct{}

func (nopProvider) Name() string                { return "nop" }
func (nopProvider) Model() string               { return "nop" }
func (nopProvider) SupportsPromptCaching() bool { return false }
func (nopProvider) Explain(context.Context, llm.ExplainRequest) (*llm.Explanation, error) {
	return &llm.Explanation{Prose: "x"}, nil
}
func (nopProvider) Ask(context.Context, llm.AskRequest) (<-chan llm.Token, error) {
	ch := make(chan llm.Token)
	close(ch)
	return ch, nil
}

func TestEnterExitSnapshot(t *testing.T) {
	root := gitRepoWithHistory(t) // commits: "first" (a.go), "add F and b" (a.go,b.go)
	// An uncommitted file: present on the working tree, absent from history.
	if err := os.WriteFile(filepath.Join(root, "wip.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo, ok := gitsrc.Open(root)
	if !ok {
		t.Fatal("Open failed")
	}
	c, err := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	gen := index.NewGenerator(root, c, nopProvider{}, nil)
	tree, err := NewTree(root)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(gen, tree, nil, 0, repo)

	// Working tree shows the uncommitted file.
	if m.tree.FindRow(model.NodeID{Kind: model.KindFile, Path: "wip.go"}) < 0 {
		t.Fatal("expected wip.go on working tree")
	}
	if m.inSnapshot() {
		t.Fatal("should start live")
	}

	// Open history, load it, select the oldest commit, enter snapshot.
	_ = m.openHistory()
	lm := m.loadHistoryCmd()().(gitLogMsg)
	mi, _ := m.handleGitLogMsg(lm)
	m = mi.(Model)
	m.history.cursor = len(m.history.commits) - 1 // the "first" commit (only a.go)
	first := m.history.commits[m.history.cursor]

	mi, _ = m.enterSnapshot()
	m = mi.(Model)
	if !m.inSnapshot() {
		t.Fatal("expected to be in snapshot")
	}
	if m.currentRev() != first.SHA {
		t.Fatalf("currentRev = %q, want %q", m.currentRev(), first.SHA)
	}
	if m.treeTab != treeTabTree || m.activePane != paneTree {
		t.Fatalf("snapshot should land on the Tree tab; tab=%d pane=%d", m.treeTab, m.activePane)
	}
	// At the first commit: a.go exists, b.go and wip.go do not.
	if m.tree.FindRow(model.NodeID{Kind: model.KindFile, Path: "a.go"}) < 0 {
		t.Fatal("a.go should exist at the first commit")
	}
	if m.tree.FindRow(model.NodeID{Kind: model.KindFile, Path: "b.go"}) >= 0 {
		t.Fatal("b.go must NOT exist at the first commit")
	}
	if m.tree.FindRow(model.NodeID{Kind: model.KindFile, Path: "wip.go"}) >= 0 {
		t.Fatal("uncommitted wip.go must NOT appear in a snapshot")
	}
	if m.gen == m.baseGen {
		t.Fatal("generator should be revision-scoped in a snapshot")
	}

	// xref is disabled in snapshot mode.
	m.currentID = model.NodeID{Kind: model.KindSymbol, Path: "a.go", Symbol: "One"}
	mi, _ = m.openCallersOf()
	m2 := mi.(Model)
	if m2.xref.open {
		t.Fatal("xref must be suppressed in a snapshot")
	}

	// Exit → back to the live working tree, uncommitted file visible again.
	mi, _ = m.exitSnapshot()
	m = mi.(Model)
	if m.inSnapshot() {
		t.Fatal("should be live after exit")
	}
	if m.gen != m.baseGen {
		t.Fatal("generator should be restored to baseGen after exit")
	}
	if m.tree.FindRow(model.NodeID{Kind: model.KindFile, Path: "wip.go"}) < 0 {
		t.Fatal("wip.go should be back on the working tree after exit")
	}
}

func TestBackForwardCrossesRevisionBoundary(t *testing.T) {
	root := gitRepoWithHistory(t)
	repo, _ := gitsrc.Open(root)
	c, _ := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	t.Cleanup(func() { c.Close() })
	gen := index.NewGenerator(root, c, nopProvider{}, nil)
	tree, _ := NewTree(root)
	m := NewModel(gen, tree, nil, 0, repo)

	_ = m.openHistory()
	lm := m.loadHistoryCmd()().(gitLogMsg)
	mi, _ := m.handleGitLogMsg(lm)
	m = mi.(Model)
	m.history.cursor = len(m.history.commits) - 1
	mi, _ = m.enterSnapshot()
	m = mi.(Model)
	if !m.inSnapshot() {
		t.Fatal("expected snapshot")
	}

	// Stack back should cross the revision boundary back to the live frame.
	id, ok := m.stack.Back()
	if !ok {
		t.Fatal("expected a back frame (the pre-snapshot live focus)")
	}
	_ = m.focusStackTarget(id)
	if m.inSnapshot() {
		t.Fatalf("back across the boundary should return live; rev=%q", m.currentRev())
	}
	if m.gen != m.baseGen {
		t.Fatal("generator should be restored after crossing back to live")
	}
}

func TestSnapshotDiffColoringAndView(t *testing.T) {
	root := gitRepoWithHistory(t) // c0:"first"(a.go A) c1:"add F and b"(a.go M, b.go A)
	repo, _ := gitsrc.Open(root)
	c, _ := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	t.Cleanup(func() { c.Close() })
	gen := index.NewGenerator(root, c, nopProvider{}, nil)
	tree, _ := NewTree(root)
	m := NewModel(gen, tree, nil, 0, repo)

	_ = m.openHistory()
	lm := m.loadHistoryCmd()().(gitLogMsg)
	mi, _ := m.handleGitLogMsg(lm)
	m = mi.(Model)
	m.history.cursor = 1 // newest = "add F and b"
	sha := m.history.commits[1].SHA
	mi, _ = m.enterSnapshot()
	m = mi.(Model)

	cm := m.loadChangesCmd(sha)().(commitChangesMsg)
	mi, _ = m.handleCommitChangesMsg(cm)
	m = mi.(Model)

	if m.changeStatus("a.go") != "M" {
		t.Fatalf("a.go status = %q, want M", m.changeStatus("a.go"))
	}
	if m.changeStatus("b.go") != "A" {
		t.Fatalf("b.go status = %q, want A", m.changeStatus("b.go"))
	}

	if st, ok := m.treeRowStyle(model.NodeID{Kind: model.KindFile, Path: "a.go"}); !ok ||
		st.Render("x") != modifyStyle.Render("x") {
		t.Fatal("a.go should render in the modify color")
	}
	if st, ok := m.treeRowStyle(model.NodeID{Kind: model.KindFile, Path: "b.go"}); !ok ||
		st.Render("x") != addStyle.Render("x") {
		t.Fatal("b.go should render in the add color")
	}
	if _, ok := m.treeRowStyle(model.NodeID{Kind: model.KindFile, Path: "nope.go"}); ok {
		t.Fatal("unchanged file must not be colored")
	}

	// Diff view for a.go (a changed file).
	m.currentFile = "a.go"
	if !m.inDiffView() {
		t.Fatal("expected diff view for a changed file in snapshot")
	}
	dcmd := m.maybeLoadFileDiff()
	if dcmd == nil {
		t.Fatal("expected a file-diff load command")
	}
	fm := dcmd().(fileDiffMsg)
	mi, _ = m.handleFileDiffMsg(fm)
	m = mi.(Model)

	view := m.renderDiffView(120, 40)
	if !strings.Contains(view, "F") { // the added func F()
		t.Fatalf("diff view missing added content:\n%s", view)
	}
	if !strings.Contains(view, "package a") { // unchanged context line, full file shown
		t.Fatalf("diff view should include unchanged context (full file):\n%s", view)
	}
	if strings.Contains(view, "@@") { // hunk headers are suppressed now
		t.Fatalf("inline diff view should not show @@ headers:\n%s", view)
	}

	// G scrolls to the bottom; row count = parsed inline-diff rows.
	n := len(parseInlineDiff(m.snapshotDiff["a.go"]))
	if n > 1 {
		mi, _ = m.updateDiffScroll("G")
		m = mi.(Model)
		if m.srcScroll != n-1 {
			t.Fatalf("G should scroll to last line %d, got %d", n-1, m.srcScroll)
		}
	}
}

func TestSnapshotDirAggregateColor(t *testing.T) {
	if _, err := execLookGit(); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "pkg", "a.go"), []byte("package pkg\n"), 0o644)
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644)
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "init")
	os.WriteFile(filepath.Join(root, "pkg", "a.go"), []byte("package pkg\n\nvar X = 1\n"), 0o644)
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "touch pkg/a.go")

	repo, _ := gitsrc.Open(root)
	c, _ := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	t.Cleanup(func() { c.Close() })
	gen := index.NewGenerator(root, c, nopProvider{}, nil)
	tree, _ := NewTree(root)
	m := NewModel(gen, tree, nil, 0, repo)
	_ = m.openHistory()
	lm := m.loadHistoryCmd()().(gitLogMsg)
	mi, _ := m.handleGitLogMsg(lm)
	m = mi.(Model)
	m.history.cursor = 1
	sha := m.history.commits[1].SHA
	mi, _ = m.enterSnapshot()
	m = mi.(Model)
	cm := m.loadChangesCmd(sha)().(commitChangesMsg)
	mi, _ = m.handleCommitChangesMsg(cm)
	m = mi.(Model)

	st, ok := m.treeRowStyle(model.NodeID{Kind: model.KindDir, Path: "pkg"})
	if !ok || st.Render("x") != modifyStyle.Render("x") {
		t.Fatal("pkg/ should aggregate to the modify color (a descendant changed)")
	}
}

func execLookGit() (string, error) { return exec.LookPath("git") }

// collectMsgs runs cmd, flattening tea.Batch results one level so a test can
// inspect every message a batched command would deliver.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch v := cmd().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range v {
			if c != nil {
				out = append(out, c())
			}
		}
		return out
	case nil:
		return nil
	default:
		return []tea.Msg{v}
	}
}

func TestSnapshotNodeChangeExplain(t *testing.T) {
	root := gitRepoWithHistory(t)
	repo, _ := gitsrc.Open(root)
	c, _ := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	t.Cleanup(func() { c.Close() })
	gen := index.NewGenerator(root, c, nopProvider{}, nil)
	tree, _ := NewTree(root)
	m := NewModel(gen, tree, nil, 0, repo)

	_ = m.openHistory()
	lm := m.loadHistoryCmd()().(gitLogMsg)
	mi, _ := m.handleGitLogMsg(lm)
	m = mi.(Model)
	m.history.cursor = 1 // "add F and b": a.go modified
	sha := m.history.commits[1].SHA
	mi, _ = m.enterSnapshot()
	m = mi.(Model)
	cm := m.loadChangesCmd(sha)().(commitChangesMsg)
	mi, _ = m.handleCommitChangesMsg(cm)
	m = mi.(Model)

	m.currentFile = "a.go"
	m.currentID = model.NodeID{Kind: model.KindFile, Path: "a.go"}

	// Load the diff; handleFileDiffMsg should hand back a change-explain cmd.
	fm := m.maybeLoadFileDiff()().(fileDiffMsg)
	mi, ecmd := m.handleFileDiffMsg(fm)
	m = mi.(Model)
	if ecmd == nil {
		t.Fatal("expected a node-change-explain command after diff load")
	}
	sawExplain := false
	for _, msg := range collectMsgs(ecmd) {
		switch v := msg.(type) {
		case nodeChangeExplainMsg:
			mi, _ = m.handleNodeChangeExplainMsg(v)
			m = mi.(Model)
			sawExplain = true
		case symChangesMsg:
			mi, _ = m.handleSymChangesMsg(v)
			m = mi.(Model)
		}
	}
	if !sawExplain {
		t.Fatal("expected a node-change-explain message after diff load")
	}

	if m.snapshotNodeExp["a.go"] == nil {
		t.Fatal("change explanation not cached")
	}
	// Symbol-level coloring: a.go gained func F() in this commit.
	if syms := m.snapshotSymChanges["a.go"]; syms == nil {
		t.Fatal("expected symbol change map for a.go")
	} else if syms["F"] != "A" {
		t.Fatalf("func F should be marked added; got %q (map=%v)", syms["F"], syms)
	}
	if st, ok := m.treeRowStyle(model.NodeID{Kind: model.KindSymbol, Path: "a.go", Symbol: "F"}); !ok ||
		st.Render("x") != addStyle.Render("x") {
		t.Fatal("symbol F should render in the add color")
	}
	// renderExp must route a changed, focused file to the change explanation.
	out := m.renderExp(80, 12)
	if !strings.Contains(out, "changes · a.go") {
		t.Fatalf("renderExp not routed to node-change view:\n%s", out)
	}

	// An unchanged file falls through to the normal explanation path.
	m.currentFile = ""
	m.currentID = model.NodeID{Kind: model.KindRepo, Path: ""}
	if got := m.changeStatus("nope.go"); got != "" {
		t.Fatalf("unexpected status for unchanged file: %q", got)
	}
}

func TestInlineDiffFullFileView(t *testing.T) {
	if _, err := execLookGit(); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	v1 := "package foo\n\nfunc A() int { return 1 }\n\nfunc B() {}\n"
	v2 := "package foo\n\nfunc A() int { return 2 }\n\nfunc B() {}\n"
	os.WriteFile(filepath.Join(root, "foo.go"), []byte(v1), 0o644)
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "v1")
	os.WriteFile(filepath.Join(root, "foo.go"), []byte(v2), 0o644)
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "bump A return")

	repo, _ := gitsrc.Open(root)
	c, _ := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	t.Cleanup(func() { c.Close() })
	gen := index.NewGenerator(root, c, nopProvider{}, nil)
	tree, _ := NewTree(root)
	m := NewModel(gen, tree, nil, 0, repo)

	_ = m.openHistory()
	lm := m.loadHistoryCmd()().(gitLogMsg)
	mi, _ := m.handleGitLogMsg(lm)
	m = mi.(Model)
	m.history.cursor = 1 // newest = "bump A return"
	sha := m.history.commits[1].SHA
	mi, _ = m.enterSnapshot()
	m = mi.(Model)
	cm := m.loadChangesCmd(sha)().(commitChangesMsg)
	mi, _ = m.handleCommitChangesMsg(cm)
	m = mi.(Model)

	// Populate the post-image like setFile would, then load the diff.
	m.currentFile = "foo.go"
	m.currentID = model.NodeID{Kind: model.KindFile, Path: "foo.go"}
	m.sourceCache["foo.go"] = v2
	fm := m.maybeLoadFileDiff()().(fileDiffMsg)
	mi, _ = m.handleFileDiffMsg(fm)
	m = mi.(Model)

	view := ansi.Strip(m.renderDiffView(120, 40))
	for _, want := range []string{"package foo", "return 1", "return 2", "func B"} {
		if !strings.Contains(view, want) {
			t.Fatalf("inline diff missing %q (full file + add + remove expected):\n%s", want, view)
		}
	}
	// Removed line carries no new-file number; added/context lines do.
	if !strings.Contains(view, "1 package foo") {
		t.Fatalf("expected line-numbered context line:\n%s", view)
	}
}

func TestWorkingDiffMode(t *testing.T) {
	if _, err := execLookGit(); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "init")
	// Uncommitted: modify a.go, add an untracked file.
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc A() { _ = 1 }\n"), 0o644)
	os.WriteFile(filepath.Join(root, "new.go"), []byte("package a\n\nvar X = 9\n"), 0o644)

	repo, _ := gitsrc.Open(root)
	c, _ := cache.Open(filepath.Join(root, ".explore", "cache.db"))
	t.Cleanup(func() { c.Close() })
	gen := index.NewGenerator(root, c, nopProvider{}, nil)
	tree, _ := NewTree(root)
	m := NewModel(gen, tree, nil, 0, repo)

	_ = m.openHistory()
	lm := m.loadHistoryCmd()().(gitLogMsg)
	mi, _ := m.handleGitLogMsg(lm)
	m = mi.(Model)

	if m.history.commits[0].SHA != workingRef {
		t.Fatalf("row 0 should be WORKING, got %+v", m.history.commits[0])
	}
	m.history.cursor = 0 // WORKING
	mi, _ = m.enterSnapshot()
	m = mi.(Model)
	if !m.isWorkingDiff() || !m.inSnapshot() {
		t.Fatalf("expected working-diff mode (rev=%q)", m.currentRev())
	}
	if m.atCommitSnapshot() {
		t.Fatal("working-diff is not a commit snapshot (LSP/prefetch must stay live)")
	}
	if m.gen != m.baseGen {
		t.Fatal("working-diff must use the live generator")
	}

	cm := m.loadChangesCmd(workingRef)().(commitChangesMsg)
	mi, _ = m.handleCommitChangesMsg(cm)
	m = mi.(Model)
	if m.changeStatus("a.go") != "M" {
		t.Fatalf("a.go should be M vs HEAD, got %q", m.changeStatus("a.go"))
	}
	if m.changeStatus("new.go") != "A" {
		t.Fatalf("untracked new.go should be A, got %q", m.changeStatus("new.go"))
	}

	// Inline diff of the modified file shows old and new lines.
	m.currentFile = "a.go"
	m.sourceCache["a.go"] = "package a\n\nfunc A() { _ = 1 }\n"
	fm := m.maybeLoadFileDiff()().(fileDiffMsg)
	mi, _ = m.handleFileDiffMsg(fm)
	m = mi.(Model)
	view := ansi.Strip(m.renderDiffView(120, 40))
	if !strings.Contains(view, "func A() {}") || !strings.Contains(view, "func A() { _ = 1 }") {
		t.Fatalf("working diff should show old + new line:\n%s", view)
	}

	// Untracked file renders as all-added (synthesized patch).
	m.currentFile = "new.go"
	m.sourceCache["new.go"] = "package a\n\nvar X = 9\n"
	fm = m.maybeLoadFileDiff()().(fileDiffMsg)
	mi, _ = m.handleFileDiffMsg(fm)
	m = mi.(Model)
	if v := ansi.Strip(m.renderDiffView(120, 40)); !strings.Contains(v, "var X = 9") {
		t.Fatalf("untracked file should render its content as added:\n%s", v)
	}

	// Esc leaves working-diff back to the live view.
	mi, _ = m.exitSnapshot()
	m = mi.(Model)
	if m.inSnapshot() {
		t.Fatalf("exit should clear working-diff; rev=%q", m.currentRev())
	}
}
