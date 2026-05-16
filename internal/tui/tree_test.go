package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mikethicke/explore/internal/gitsrc"
	"github.com/mikethicke/explore/internal/model"
)

// scaffoldRepo writes a small repo layout into root and returns the tree.
func scaffoldRepo(t *testing.T) (string, *Tree) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth", "session.go"),
		[]byte("package auth\n\ntype Session struct{}\n\nfunc Verify() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := NewTree(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, tr
}

func TestReveal_FileExpandsAncestors(t *testing.T) {
	_, tr := scaffoldRepo(t)
	id := model.NodeID{Kind: model.KindFile, Path: filepath.Join("auth", "session.go")}
	row := tr.Reveal(context.Background(), id)
	if row < 0 {
		t.Fatalf("Reveal returned -1; rows=%+v", tr.Rows())
	}
	if tr.Rows()[row].ID != id {
		t.Fatalf("row %d ID = %+v, want %+v", row, tr.Rows()[row].ID, id)
	}
}

func TestReveal_SymbolExpandsFile(t *testing.T) {
	_, tr := scaffoldRepo(t)
	id := model.NodeID{Kind: model.KindSymbol, Path: filepath.Join("auth", "session.go"), Symbol: "Verify"}
	row := tr.Reveal(context.Background(), id)
	if row < 0 {
		t.Fatalf("Reveal returned -1; rows=%+v", tr.Rows())
	}
	if tr.Rows()[row].ID != id {
		t.Fatalf("row %d ID = %+v, want %+v", row, tr.Rows()[row].ID, id)
	}
	// Parent file should be expanded.
	parent := model.NodeID{Kind: model.KindFile, Path: filepath.Join("auth", "session.go")}
	pr := tr.FindRow(parent)
	if pr < 0 || !tr.Rows()[pr].Expanded {
		t.Fatalf("parent file row not expanded; pr=%d rows=%+v", pr, tr.Rows())
	}
}

func TestReveal_MissingReturnsMinusOne(t *testing.T) {
	_, tr := scaffoldRepo(t)
	id := model.NodeID{Kind: model.KindFile, Path: filepath.Join("nope", "missing.go")}
	if got := tr.Reveal(context.Background(), id); got != -1 {
		t.Fatalf("Reveal missing returned %d, want -1", got)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestSetRevision_HistoricalSnapshot proves the tree reflects the repo as it
// was at a commit, not the working tree: a file added after the commit is
// visible on the working tree but absent at HEAD.
func TestSetRevision_HistoricalSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "initial")

	// Add an uncommitted file.
	if err := os.WriteFile(filepath.Join(root, "added.go"),
		[]byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tr, err := NewTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if tr.FindRow(model.NodeID{Kind: model.KindFile, Path: "added.go"}) < 0 {
		t.Fatal("working tree should show added.go")
	}

	repo, ok := gitsrc.Open(root)
	if !ok {
		t.Fatal("Open should succeed on a freshly init'd repo")
	}
	if err := tr.SetRevision(repo.AtCommit("HEAD")); err != nil {
		t.Fatalf("SetRevision: %v", err)
	}
	if tr.FindRow(model.NodeID{Kind: model.KindFile, Path: "added.go"}) >= 0 {
		t.Fatal("HEAD snapshot must not show the uncommitted added.go")
	}
	if tr.FindRow(model.NodeID{Kind: model.KindFile, Path: "main.go"}) < 0 {
		t.Fatal("HEAD snapshot should still show committed main.go")
	}

	// Returning to the working tree restores the uncommitted file.
	if err := tr.SetRevision(gitsrc.WorkingTree(root)); err != nil {
		t.Fatalf("SetRevision back: %v", err)
	}
	if tr.FindRow(model.NodeID{Kind: model.KindFile, Path: "added.go"}) < 0 {
		t.Fatal("working tree should show added.go again")
	}
}
