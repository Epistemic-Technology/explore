package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
