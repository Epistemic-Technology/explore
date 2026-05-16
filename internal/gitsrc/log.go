package gitsrc

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Commit is one entry in a history list. Date is the author date.
type Commit struct {
	SHA      string
	ShortSHA string
	Author   string
	Date     time.Time
	Subject  string
}

// FileChange is one path touched by a commit (status vs. its first parent).
type FileChange struct {
	Status string // "A", "M", "D", "R<score>", "C<score>"
	Path   string
	// OldPath is set for renames/copies (the pre-image path).
	OldPath string
}

// CommitDetail is the full message plus the per-file change list for a single
// commit, used to populate the source pane when a commit is highlighted.
type CommitDetail struct {
	Commit
	Body    string
	Changes []FileChange
}

// Field/record separators: ASCII US (0x1f) between fields, RS (0x1e) between
// records. Neither occurs in commit metadata, so parsing stays unambiguous
// even when subjects contain newlines, tabs, or pipes.
const (
	fieldSep  = "\x1f"
	recordSep = "\x1e"
	logFormat = "%H" + fieldSep + "%h" + fieldSep + "%an" + fieldSep + "%aI" + fieldSep + "%s" + recordSep
)

// Log returns commits touching pathspec (repo-relative; "" = whole repo),
// newest first, capped at limit.
func (r *Repo) Log(ctx context.Context, pathspec string, limit int) ([]Commit, error) {
	args := []string{"log", "--no-color", "-n", strconv.Itoa(limit), "--format=" + logFormat}
	if p := strings.Trim(pathspec, "/"); p != "" && p != "." {
		args = append(args, "--", p)
	}
	out, err := r.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

// CurrentBranch returns the checked-out branch name, or "HEAD" when detached.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func parseLog(out []byte) []Commit {
	var commits []Commit
	for _, rec := range strings.Split(string(out), recordSep) {
		rec = strings.Trim(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.Split(rec, fieldSep)
		if len(f) < 5 {
			continue
		}
		c := Commit{SHA: f[0], ShortSHA: f[1], Author: f[2], Subject: f[4]}
		if ts, err := time.Parse(time.RFC3339, f[3]); err == nil {
			c.Date = ts
		}
		commits = append(commits, c)
	}
	return commits
}

// CommitMeta returns the full message and the list of files a commit changed
// relative to its first parent. Root commits (no parent) diff against the
// empty tree, so every file shows as added.
func (r *Repo) CommitMeta(ctx context.Context, sha string) (CommitDetail, error) {
	hdr, err := r.git(ctx, "show", "-s", "--no-color", "--format="+logFormat+"%n%B", sha)
	if err != nil {
		return CommitDetail{}, err
	}
	var d CommitDetail
	if i := strings.Index(string(hdr), recordSep); i >= 0 {
		if cs := parseLog(hdr[:i+len(recordSep)]); len(cs) > 0 {
			d.Commit = cs[0]
		}
		d.Body = strings.Trim(string(hdr[i+len(recordSep):]), "\n")
	}

	// --name-status against the first parent. diff-tree handles root commits
	// (it simply lists everything as added).
	ns, err := r.git(ctx, "diff-tree", "--root", "--no-commit-id", "--name-status",
		"-r", "-M", "-z", sha)
	if err != nil {
		return CommitDetail{}, err
	}
	d.Changes = parseNameStatus(string(ns))
	return d, nil
}

// CommitDiff returns the unified patch a commit introduced relative to its
// first parent. `--format=` strips the commit header so only the patch
// remains; `git show` diffs a root commit against the empty tree, so initial
// commits come back as an all-added patch rather than empty.
func (r *Repo) CommitDiff(ctx context.Context, sha string) ([]byte, error) {
	return r.git(ctx, "show", "--no-color", "-M", "--format=", sha)
}

// FileDiff returns the patch a commit introduced for a single path (vs. its
// first parent), with effectively unlimited context so the whole file is
// included around the changes. Empty output means the path was unchanged.
func (r *Repo) FileDiff(ctx context.Context, sha, path string) ([]byte, error) {
	return r.git(ctx, "show", "--no-color", "-M", "--unified=1000000", "--format=", sha, "--",
		strings.Trim(filepath.ToSlash(path), "/"))
}

// WorkingChanges reports every path that differs between the working tree and
// HEAD: tracked modifications/additions/deletions/renames (staged or not) plus
// untracked files (reported as added). This is "everything changed since the
// last commit".
func (r *Repo) WorkingChanges(ctx context.Context) ([]FileChange, error) {
	ns, err := r.git(ctx, "diff", "HEAD", "--no-color", "--name-status", "-M", "-z")
	if err != nil {
		return nil, err
	}
	changes := parseNameStatus(string(ns))
	others, err := r.git(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(string(others), "\x00") {
		if p != "" {
			changes = append(changes, FileChange{Status: "A", Path: p})
		}
	}
	return changes, nil
}

// WorkingDiff returns the whole working-tree-vs-HEAD patch (tracked files
// only; untracked aren't included in the summary diff).
func (r *Repo) WorkingDiff(ctx context.Context) ([]byte, error) {
	return r.git(ctx, "diff", "HEAD", "--no-color", "-M", "--unified=1000000")
}

// WorkingFileDiff returns the working-tree-vs-HEAD patch for one path, full
// context. Empty for an untracked file (the caller synthesizes an all-added
// view from the file contents in that case).
func (r *Repo) WorkingFileDiff(ctx context.Context, path string) ([]byte, error) {
	return r.git(ctx, "diff", "HEAD", "--no-color", "-M", "--unified=1000000", "--",
		strings.Trim(filepath.ToSlash(path), "/"))
}

// parseNameStatus parses NUL-delimited `--name-status -z` output. For plain
// statuses a record is "<status>\x00<path>". For renames/copies git emits the
// status in one field then TWO path fields (old, new).
func parseNameStatus(s string) []FileChange {
	toks := strings.Split(s, "\x00")
	var out []FileChange
	for i := 0; i < len(toks); i++ {
		st := toks[i]
		if st == "" {
			continue
		}
		switch st[0] {
		case 'R', 'C':
			if i+2 >= len(toks) {
				break
			}
			out = append(out, FileChange{Status: st, OldPath: toks[i+1], Path: toks[i+2]})
			i += 2
		default:
			if i+1 >= len(toks) {
				break
			}
			out = append(out, FileChange{Status: st, Path: toks[i+1]})
			i++
		}
	}
	return out
}
