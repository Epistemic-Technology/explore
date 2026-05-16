package gitsrc

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// PR-snapshot support. A PR's meaningful diff is its head vs. the *merge-base*
// with the base branch (the cumulative change), not head-vs-head^ the way a
// lone commit is viewed. PreparePR fetches both tips into private refs and
// returns the head ref plus that merge-base; the Range* helpers below let the
// snapshot layer diff against an explicit base instead of always deriving `^`.

// prHeadRef / prBaseRef namespace the fetched tips under refs/explore/ so they
// never appear as branches in the user's `git branch` output and the working
// tree is never touched.
func prHeadRef(number int) string { return "refs/explore/pr/" + strconv.Itoa(number) }
func prBaseRef(number int) string { return "refs/explore/prbase/" + strconv.Itoa(number) }

// PreparePR fetches PR `number`'s head (via the pull/<n>/head ref GitHub
// publishes) and its base branch into private refs, then computes their
// merge-base. It returns a head ref usable as a Revision and the merge-base
// sha to diff against. Network + writes to .git; the caller degrades to the
// flat `gh pr diff` view if this fails.
//
// fetchURL is the HTTPS clone URL (from ghsrc.HTTPSRemote), used instead of the
// configured `origin`: origin is commonly an SSH remote, and an SSH egress
// stall would hang here with nothing to fall back on. Auth for the HTTPS fetch
// is delegated to `gh` as a per-invocation credential helper (the first
// `-c credential.helper=` resets any inherited helper so only gh is consulted),
// which reuses the same authenticated path the rest of the PR feature uses.
func (r *Repo) PreparePR(ctx context.Context, number int, baseBranch, fetchURL string) (headRef, base string, err error) {
	hr, br := prHeadRef(number), prBaseRef(number)
	cred := []string{
		"-c", "credential.helper=",
		"-c", "credential.helper=!gh auth git-credential",
	}
	fetch := func(refspec string) error {
		args := append(append([]string{}, cred...),
			"fetch", "--no-tags", fetchURL, refspec)
		_, e := r.git(ctx, args...)
		return e
	}
	if err = fetch(fmt.Sprintf("pull/%d/head:%s", number, hr)); err != nil {
		return "", "", err
	}
	if err = fetch(fmt.Sprintf("%s:%s", baseBranch, br)); err != nil {
		return "", "", err
	}
	out, err := r.git(ctx, "merge-base", hr, br)
	if err != nil {
		return "", "", err
	}
	return hr, strings.TrimSpace(string(out)), nil
}

// RangeChanges reports the name-status of head vs. base (two-dot: the literal
// difference between the two trees). With base set to a PR's merge-base this is
// exactly the PR's cumulative change set.
func (r *Repo) RangeChanges(ctx context.Context, base, head string) ([]FileChange, error) {
	ns, err := r.git(ctx, "diff", "--no-color", "--name-status", "-M", "-z", base+".."+head)
	if err != nil {
		return nil, err
	}
	return parseNameStatus(string(ns)), nil
}

// RangeDiff returns the whole head-vs-base unified patch.
func (r *Repo) RangeDiff(ctx context.Context, base, head string) ([]byte, error) {
	return r.git(ctx, "diff", "--no-color", "-M", base+".."+head)
}

// RangeFileDiff returns the head-vs-base patch for one path with effectively
// unlimited context (whole file around the changes), matching FileDiff's
// contract so the inline diff renderer is shared. Empty means unchanged.
func (r *Repo) RangeFileDiff(ctx context.Context, base, head, path string) ([]byte, error) {
	return r.git(ctx, "diff", "--no-color", "-M", "--unified=1000000", base+".."+head, "--",
		strings.Trim(filepath.ToSlash(path), "/"))
}
