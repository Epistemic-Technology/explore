package gitsrc

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkingTreeReadFileAndDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	w := WorkingTree(dir)
	if w.Ref() != "" {
		t.Fatalf("working tree Ref() = %q, want empty", w.Ref())
	}
	b, err := w.ReadFile("a.txt")
	if err != nil || string(b) != "hello" {
		t.Fatalf("ReadFile(a.txt) = %q, %v", b, err)
	}
	es, err := w.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range es {
		got[e.Name] = e.IsDir
	}
	if d, ok := got["sub"]; !ok || !d {
		t.Fatalf("ReadDir: sub missing or not a dir: %v", got)
	}
	if d, ok := got["a.txt"]; !ok || d {
		t.Fatalf("ReadDir: a.txt missing or marked dir: %v", got)
	}
}

func TestOpenNonGitDir(t *testing.T) {
	if _, ok := Open(t.TempDir()); ok {
		t.Fatal("Open on a non-git temp dir returned ok=true")
	}
}

// repoRoot walks up from the test's working dir to the module root (the dir
// containing go.mod), which is this project's own git repo.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("module root not found; skipping git-backed test")
		}
		dir = parent
	}
}

func TestCommitRevAgainstHEAD(t *testing.T) {
	root := repoRoot(t)
	repo, ok := Open(root)
	if !ok {
		t.Skip("not a git repo or git unavailable")
	}
	head := repo.AtCommit("HEAD")
	if head.Ref() != "HEAD" {
		t.Fatalf("Ref() = %q, want HEAD", head.Ref())
	}

	// go.mod is committed and unchanged → readable at HEAD.
	b, err := head.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("ReadFile(go.mod) at HEAD: %v", err)
	}
	if !bytes.Contains(b, []byte("module github.com/mikethicke/explore")) {
		t.Fatalf("HEAD go.mod missing module line: %q", b)
	}

	// Historical accuracy: internal/gitsrc is newly created and NOT committed,
	// so it must be absent from HEAD's tree but present in the working tree.
	headDirs := names(t, head, "internal")
	if headDirs["gitsrc"] {
		t.Fatal("internal/gitsrc should not exist in HEAD (uncommitted)")
	}
	if !headDirs["index"] {
		t.Fatalf("expected internal/index in HEAD tree, got %v", headDirs)
	}
	wtDirs := names(t, repo.WorkingTree(), "internal")
	if !wtDirs["gitsrc"] {
		t.Fatal("internal/gitsrc should exist in the working tree")
	}

	// Reading a path that doesn't exist at the revision errors.
	if _, err := head.ReadFile("internal/gitsrc/gitsrc.go"); err == nil {
		t.Fatal("expected error reading an uncommitted file at HEAD")
	}
}

func names(t *testing.T, rev Revision, dir string) map[string]bool {
	t.Helper()
	es, err := rev.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	out := map[string]bool{}
	for _, e := range es {
		out[e.Name] = true
	}
	return out
}
