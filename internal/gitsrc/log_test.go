package gitsrc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) (string, *Repo) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@x",
			"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n\nfunc One() {}\n")
	run("add", ".")
	run("commit", "-qm", "add a")
	write("a.go", "package a\n\nfunc One() { _ = 1 }\n\nfunc Two() {}\n")
	write("b.go", "package a\n")
	run("add", ".")
	run("commit", "-qm", "extend a, add b")
	run("rm", "-q", "b.go")
	run("commit", "-qm", "remove b")

	repo, ok := Open(root)
	if !ok {
		t.Fatal("Open failed on init'd repo")
	}
	return root, repo
}

func TestLog(t *testing.T) {
	_, repo := initRepo(t)
	cs, err := repo.Log(context.Background(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 {
		t.Fatalf("want 3 commits, got %d: %+v", len(cs), cs)
	}
	if cs[0].Subject != "remove b" {
		t.Fatalf("newest-first expected; got subject %q", cs[0].Subject)
	}
	if cs[0].SHA == "" || cs[0].ShortSHA == "" || cs[0].Author != "Ada" || cs[0].Date.IsZero() {
		t.Fatalf("commit fields not populated: %+v", cs[0])
	}

	// Path scope: b.go appears only in the commits that add then remove it.
	bcs, err := repo.Log(context.Background(), "b.go", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(bcs) != 2 {
		t.Fatalf("want 2 commits touching b.go, got %d: %+v", len(bcs), bcs)
	}
}

func TestCommitMeta(t *testing.T) {
	_, repo := initRepo(t)
	cs, _ := repo.Log(context.Background(), "", 50)
	// cs[1] = "extend a, add b": modifies a.go, adds b.go.
	d, err := repo.CommitMeta(context.Background(), cs[1].SHA)
	if err != nil {
		t.Fatal(err)
	}
	if d.Subject != "extend a, add b" {
		t.Fatalf("subject = %q", d.Subject)
	}
	got := map[string]string{}
	for _, c := range d.Changes {
		got[c.Path] = c.Status
	}
	if got["a.go"] != "M" {
		t.Fatalf("a.go status = %q, want M (%+v)", got["a.go"], d.Changes)
	}
	if got["b.go"] != "A" {
		t.Fatalf("b.go status = %q, want A (%+v)", got["b.go"], d.Changes)
	}
}
