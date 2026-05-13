package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mikethicke/explore/internal/model"
)

func TestMatchScore_NoMatch(t *testing.T) {
	if s, _ := matchScore("xyz", "foobar"); s != noMatch {
		t.Errorf("matchScore(xyz,foobar) = %d, want noMatch", s)
	}
}

func TestMatchScore_PrefixBeatsSubstring(t *testing.T) {
	prefix, _ := matchScore("get", "getuserid")
	mid, _ := matchScore("get", "fakegetfoo")
	if !(prefix > mid) {
		t.Errorf("prefix score %d should beat substring score %d", prefix, mid)
	}
}

func TestMatchScore_ExactBeatsPrefix(t *testing.T) {
	exact, _ := matchScore("getuser", "getuser")
	prefix, _ := matchScore("getuser", "getuserid")
	if !(exact > prefix) {
		t.Errorf("exact score %d should beat prefix score %d", exact, prefix)
	}
}

func TestMatchScore_BoundaryBonus(t *testing.T) {
	// "get" against "auth/get_user.go" should beat "get" against "buggetfoo"
	// because the latter has no word-boundary at the match.
	boundary, _ := matchScore("get", "auth/get_user.go")
	mid, _ := matchScore("get", "buggetfoo")
	if !(boundary > mid) {
		t.Errorf("boundary score %d should beat embedded score %d", boundary, mid)
	}
}

func TestMatchScore_Subsequence(t *testing.T) {
	score, pos := matchScore("gu", "getuser")
	if score == noMatch {
		t.Fatalf("expected subsequence match")
	}
	if len(pos) != 2 || pos[0] != 0 || pos[1] != 3 {
		t.Errorf("positions = %v, want [0 3]", pos)
	}
}

func TestSearch_EmptyQueryReturnsEntries(t *testing.T) {
	idx := &Index{entries: []Entry{
		{Label: "a.go", matchKey: "a.go", ID: model.NodeID{Kind: model.KindFile, Path: "a.go"}},
		{Label: "b.go", matchKey: "b.go", ID: model.NodeID{Kind: model.KindFile, Path: "b.go"}},
	}}
	got := idx.Search("", 5)
	if len(got) != 2 {
		t.Fatalf("Search(\"\") len = %d, want 2", len(got))
	}
}

func TestSearch_RanksByScore(t *testing.T) {
	idx := &Index{entries: []Entry{
		{Label: "buggetfoo", matchKey: "buggetfoo", ID: model.NodeID{Kind: model.KindFile, Path: "buggetfoo"}},
		{Label: "getuser  auth/user.go", matchKey: "getuser", ID: model.NodeID{Kind: model.KindSymbol, Path: "auth/user.go", Symbol: "GetUser"}},
		{Label: "auth/get_user.go", matchKey: "auth/get_user.go", ID: model.NodeID{Kind: model.KindFile, Path: "auth/get_user.go"}},
	}}
	got := idx.Search("get", 10)
	if len(got) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(got))
	}
	// The symbol "getuser" should beat the file with /get_ in the middle,
	// which should beat the substring buried inside "buggetfoo".
	if got[0].ID.Kind != model.KindSymbol {
		t.Errorf("top result kind = %v, want Symbol; got=%v", got[0].ID.Kind, got)
	}
}

func TestBuildIndex_FilesAndSymbols(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Foo() {}\nfunc Bar() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nested file in a non-source dir to confirm walking works.
	if err := os.MkdirAll(filepath.Join(root, "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth", "session.go"), []byte("package auth\n\ntype Session struct{}\nfunc (s *Session) New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Skipped: dotfile dir.
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildIndex(context.Background(), root)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	// 2 files + 2 funcs in main.go + 1 type + 1 method in session.go = 6
	if idx.Len() < 5 {
		t.Errorf("Len = %d, want >= 5; entries=%+v", idx.Len(), idx.entries)
	}
	// .git/config must not appear.
	for _, e := range idx.entries {
		if e.ID.Path == ".git/config" {
			t.Errorf(".git/config was indexed; skipDir should have hidden it")
		}
	}

	res := idx.Search("foo", 10)
	if len(res) == 0 || res[0].Entry.ID.Symbol != "Foo" {
		t.Errorf("Search(\"foo\") top = %+v, want Foo symbol", res)
	}
}
