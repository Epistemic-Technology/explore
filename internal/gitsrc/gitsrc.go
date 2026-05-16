// Package gitsrc provides a read-only, revision-scoped view of a repository's
// files. The working-tree revision reads the filesystem and is byte-identical
// to the pre-git behavior; the commit revision reads the git object database
// (git show / git ls-tree) without ever touching the working tree.
//
// This is a leaf package: it imports only the standard library, so both
// internal/index and internal/tui may depend on it without creating the
// import cycle that internal/model exists to avoid.
package gitsrc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// DirEntry is the minimal directory-entry shape the tree/index layers need.
// It mirrors the subset of os.DirEntry they actually use (name + is-dir).
type DirEntry struct {
	Name  string
	IsDir bool
}

// Revision is a read-only snapshot of a repository's files at some point in
// time. Paths are repo-relative; either OS-separator or slash form is accepted
// ("" means the repo root for ReadDir).
type Revision interface {
	// Ref is "" for the live working tree, or a commit sha / refname.
	Ref() string
	ReadFile(rel string) ([]byte, error)
	// ReadDir lists the immediate children of rel, sorted by name with
	// directories and files intermixed (callers group as they like).
	ReadDir(rel string) ([]DirEntry, error)
}

// WorkingTree returns the live filesystem revision for root. It needs neither
// git nor a Repo, so non-git directories and tests keep working unchanged.
func WorkingTree(root string) Revision { return workingTree{root: root} }

type workingTree struct{ root string }

func (workingTree) Ref() string { return "" }

func (w workingTree) ReadFile(rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(w.root, filepath.FromSlash(rel)))
}

func (w workingTree) ReadDir(rel string) ([]DirEntry, error) {
	es, err := os.ReadDir(filepath.Join(w.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(es))
	for _, e := range es {
		out = append(out, DirEntry{Name: e.Name(), IsDir: e.IsDir()})
	}
	return out, nil
}

// Repo wraps a git working copy. Open reports whether root is usable as a git
// repo (the git binary is on PATH and root is inside a work tree); callers
// that get ok=false simply omit git features — the same graceful-degrade
// contract used for a missing language server.
type Repo struct {
	root string
}

func Open(root string) (*Repo, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false
	}
	r := &Repo{root: root}
	out, err := r.git(context.Background(), "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil, false
	}
	return r, true
}

func (r *Repo) Root() string { return r.root }

// WorkingTree returns the live filesystem revision (Ref == "").
func (r *Repo) WorkingTree() Revision { return workingTree{root: r.root} }

// AtCommit returns a revision backed by the git object database for sha
// (sha may be any rev-parse-able ref).
func (r *Repo) AtCommit(sha string) Revision { return commitRev{repo: r, sha: sha} }

// git runs `git -C root <args>` and returns stdout. Stderr is folded into the
// error so callers see git's own diagnostic. Context cancellation kills the
// process, which is correct here: every git invocation is one-shot and
// request-scoped — this is NOT the long-lived-process case the LSP-lifetime
// rule guards against.
func (r *Repo) git(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{"-C", r.root}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

type commitRev struct {
	repo *Repo
	sha  string
}

func (c commitRev) Ref() string { return c.sha }

func (c commitRev) ReadFile(rel string) ([]byte, error) {
	p := path.Clean(filepath.ToSlash(rel))
	return c.repo.git(context.Background(), "show", c.sha+":"+p)
}

func (c commitRev) ReadDir(rel string) ([]DirEntry, error) {
	args := []string{"ls-tree", "-z", c.sha}
	if p := strings.Trim(filepath.ToSlash(rel), "/"); p != "" && p != "." {
		// Trailing slash makes ls-tree list the tree's *contents* rather than
		// the tree entry itself.
		args = append(args, "--", p+"/")
	}
	out, err := c.repo.git(context.Background(), args...)
	if err != nil {
		return nil, err
	}
	var entries []DirEntry
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		// Record format: "<mode> SP <type> SP <object> TAB <path>".
		tab := strings.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		fields := strings.Fields(rec[:tab])
		if len(fields) < 2 {
			continue
		}
		name := path.Base(rec[tab+1:])
		switch fields[1] {
		case "tree":
			entries = append(entries, DirEntry{Name: name, IsDir: true})
		case "blob", "commit": // commit = submodule; treat as a leaf
			entries = append(entries, DirEntry{Name: name, IsDir: false})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}
