// Package ghsrc provides a read-only view of a repository's GitHub pull
// requests by shelling out to the `gh` CLI. It mirrors internal/gitsrc's
// graceful-degrade contract: Open reports whether the feature is usable (gh on
// PATH, the directory is a GitHub repo, and the user is authenticated); callers
// that get ok=false simply omit the PRs tab, exactly like a missing language
// server or a non-git directory.
//
// This is a leaf package (stdlib only), so internal/tui may depend on it
// without the import cycle internal/model exists to avoid. PR-head fetching and
// merge-base computation are git operations and live on gitsrc.Repo, not here —
// this package only speaks to `gh`.
package ghsrc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// openTimeout bounds the auth/repo probe in Open so a hung gh (e.g. a network
// stall during token validation) can't wedge startup.
const openTimeout = 10 * time.Second

// Repo wraps a GitHub-backed working copy. The zero value is unusable; obtain
// one via Open.
type Repo struct {
	root string
}

// Open reports whether root can be used for the PRs feature. It requires the
// gh binary, that root resolves to a GitHub repository, and a working auth
// token (gh repo view fails on all three, so one probe covers them).
func Open(root string) (*Repo, bool) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, false
	}
	r := &Repo{root: root}
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	if _, err := r.gh(ctx, "repo", "view", "--json", "nameWithOwner"); err != nil {
		return nil, false
	}
	return r, true
}

func (r *Repo) Root() string { return r.root }

// gh runs `gh <args>` with the working directory pinned to root (gh has no
// global -C flag; it infers the repo from the cwd's git remote). Stderr is
// folded into the error so callers surface gh's own diagnostic. Context
// cancellation kills the process — every call here is one-shot and
// request-scoped, the same rationale gitsrc uses for git.
func (r *Repo) gh(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = r.root
	// Force Run to return shortly after a ctx-cancel kill even if gh leaves a
	// network child holding the output pipe (same wedge class as gitsrc).
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("gh %s: %s", strings.Join(args, " "), msg)
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// PR is one entry in the PR list. State is gh's raw value ("OPEN", "MERGED",
// "CLOSED"); Draft is surfaced separately so the UI can mark drafts without
// losing the underlying state.
type PR struct {
	Number      int
	Title       string
	Author      string
	State       string
	Draft       bool
	HeadRefName string
	BaseRefName string
	UpdatedAt   time.Time
}

// PRFile is one path a PR touches, with its line deltas (from gh's files view).
type PRFile struct {
	Path      string
	Additions int
	Deletions int
}

// PRDetail is the full body plus the per-file change list for a single PR,
// used to populate the source pane when a PR is highlighted.
type PRDetail struct {
	PR
	Body  string
	Files []PRFile
}

// ghPR is the JSON shape gh emits for the fields we request. author is a
// nested object; everything else is flat.
type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	State       string `json:"state"`
	IsDraft     bool   `json:"isDraft"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
	UpdatedAt   string `json:"updatedAt"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
	Files []struct {
		Path      string `json:"path"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	} `json:"files"`
}

func (g ghPR) toPR() PR {
	pr := PR{
		Number:      g.Number,
		Title:       g.Title,
		Author:      g.Author.Login,
		State:       g.State,
		Draft:       g.IsDraft,
		HeadRefName: g.HeadRefName,
		BaseRefName: g.BaseRefName,
	}
	if t, err := time.Parse(time.RFC3339, g.UpdatedAt); err == nil {
		pr.UpdatedAt = t
	}
	return pr
}

const listFields = "number,title,author,state,isDraft,headRefName,baseRefName,updatedAt"

// ListPRs returns open PRs (up to openLimit) followed by the most recently
// updated merged PRs (up to mergedLimit). Drafts are included in the open set,
// flagged via PR.Draft. A zero limit skips that group.
func (r *Repo) ListPRs(ctx context.Context, openLimit, mergedLimit int) ([]PR, error) {
	var out []PR
	if openLimit > 0 {
		open, err := r.listByState(ctx, "open", openLimit)
		if err != nil {
			return nil, err
		}
		out = append(out, open...)
	}
	if mergedLimit > 0 {
		merged, err := r.listByState(ctx, "merged", mergedLimit)
		if err != nil {
			return nil, err
		}
		out = append(out, merged...)
	}
	return out, nil
}

func (r *Repo) listByState(ctx context.Context, state string, limit int) ([]PR, error) {
	raw, err := r.gh(ctx, "pr", "list",
		"--state", state,
		"--limit", strconv.Itoa(limit),
		"--json", listFields)
	if err != nil {
		return nil, err
	}
	var gprs []ghPR
	if err := json.Unmarshal(raw, &gprs); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}
	prs := make([]PR, 0, len(gprs))
	for _, g := range gprs {
		prs = append(prs, g.toPR())
	}
	return prs, nil
}

// PRDetail returns the body and changed-file list for a single PR.
func (r *Repo) PRDetail(ctx context.Context, number int) (PRDetail, error) {
	raw, err := r.gh(ctx, "pr", "view", strconv.Itoa(number),
		"--json", listFields+",body,files")
	if err != nil {
		return PRDetail{}, err
	}
	var g ghPR
	if err := json.Unmarshal(raw, &g); err != nil {
		return PRDetail{}, fmt.Errorf("parse gh pr view: %w", err)
	}
	d := PRDetail{PR: g.toPR(), Body: g.Body}
	for _, f := range g.Files {
		d.Files = append(d.Files, PRFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions})
	}
	return d, nil
}

// HTTPSRemote returns the repository's HTTPS clone URL (…/owner/repo.git).
// PR-head fetching uses this instead of the configured `origin` because origin
// is frequently an SSH remote, and an SSH stall has nothing to fall back on —
// whereas gh's HTTPS path is already proven working (it's how the rest of this
// package talks to GitHub).
func (r *Repo) HTTPSRemote(ctx context.Context) (string, error) {
	raw, err := r.gh(ctx, "repo", "view", "--json", "url")
	if err != nil {
		return "", err
	}
	var v struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("parse gh repo view url: %w", err)
	}
	if v.URL == "" {
		return "", fmt.Errorf("gh repo view returned no url")
	}
	return v.URL + ".git", nil
}

// PRDiff returns the PR's full unified patch (head vs. the PR base), used as
// the flat fallback view and as the input to the review explanation.
func (r *Repo) PRDiff(ctx context.Context, number int) ([]byte, error) {
	return r.gh(ctx, "pr", "diff", strconv.Itoa(number))
}
