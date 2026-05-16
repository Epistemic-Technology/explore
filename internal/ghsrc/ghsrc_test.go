package ghsrc

import (
	"encoding/json"
	"testing"
	"time"
)

// The gh-CLI methods all shell out, but the JSON→PR mapping is pure and is the
// part most likely to break if the requested --json fields change. This pins
// the nested author object, the timestamp parse, and the draft/state split.
func TestGhPRToPR(t *testing.T) {
	const raw = `[{
		"number": 7,
		"title": "Wire up retries",
		"state": "OPEN",
		"isDraft": true,
		"headRefName": "feat/retries",
		"baseRefName": "main",
		"updatedAt": "2026-05-16T12:30:00Z",
		"author": {"login": "octocat"}
	}]`
	var gprs []ghPR
	if err := json.Unmarshal([]byte(raw), &gprs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(gprs) != 1 {
		t.Fatalf("len = %d, want 1", len(gprs))
	}
	pr := gprs[0].toPR()
	if pr.Number != 7 || pr.Title != "Wire up retries" {
		t.Fatalf("number/title wrong: %+v", pr)
	}
	if pr.Author != "octocat" {
		t.Fatalf("author = %q, want octocat (nested login)", pr.Author)
	}
	if !pr.Draft || pr.State != "OPEN" {
		t.Fatalf("draft/state wrong: draft=%v state=%q", pr.Draft, pr.State)
	}
	if pr.HeadRefName != "feat/retries" || pr.BaseRefName != "main" {
		t.Fatalf("refs wrong: %+v", pr)
	}
	want := time.Date(2026, 5, 16, 12, 30, 0, 0, time.UTC)
	if !pr.UpdatedAt.Equal(want) {
		t.Fatalf("updatedAt = %v, want %v", pr.UpdatedAt, want)
	}
}

func TestPRDetailFilesMapping(t *testing.T) {
	const raw = `{
		"number": 9,
		"title": "Fix parser",
		"body": "Closes #1.",
		"state": "MERGED",
		"author": {"login": "dev"},
		"files": [
			{"path": "a.go", "additions": 10, "deletions": 2},
			{"path": "b.go", "additions": 0, "deletions": 5}
		]
	}`
	var g ghPR
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := PRDetail{PR: g.toPR(), Body: g.Body}
	for _, f := range g.Files {
		d.Files = append(d.Files, PRFile{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions})
	}
	if d.Body != "Closes #1." || d.State != "MERGED" {
		t.Fatalf("body/state wrong: %+v", d)
	}
	if len(d.Files) != 2 || d.Files[0].Path != "a.go" || d.Files[0].Additions != 10 || d.Files[1].Deletions != 5 {
		t.Fatalf("files mapping wrong: %+v", d.Files)
	}
}
