package llm

import (
	"strings"
	"testing"
)

func TestBuildExplainUser_LongFunctionAddsOutlineInstruction(t *testing.T) {
	req := ExplainRequest{
		Level:  LevelSymbol,
		Path:   "x.go",
		Symbol: "Big",
		Source: "func Big() {}\n",
		IsLong: true,
	}
	got := BuildExplainUser(req)
	if !strings.Contains(got, "unusually long") {
		t.Errorf("expected long-function instruction; got:\n%s", got)
	}
	if !strings.Contains(got, "outline") {
		t.Errorf("expected 'outline' keyword in long-function instruction; got:\n%s", got)
	}
}

func TestBuildExplainUser_NormalLengthOmitsOutlineInstruction(t *testing.T) {
	req := ExplainRequest{
		Level:  LevelSymbol,
		Path:   "x.go",
		Symbol: "Small",
		Source: "func Small() {}\n",
		IsLong: false,
	}
	got := BuildExplainUser(req)
	if strings.Contains(got, "unusually long") {
		t.Errorf("instruction should be omitted when IsLong=false; got:\n%s", got)
	}
}

func TestBuildExplainUser_DiffModeEmitsCommitAndDiff(t *testing.T) {
	req := ExplainRequest{
		Level:         LevelCommit,
		Path:          "abc123",
		IsDiff:        true,
		CommitMessage: "fix the bug",
		Diff:          "@@ -1 +1 @@\n-old\n+new\n",
		Source:        "should be ignored in diff mode",
	}
	got := BuildExplainUser(req)
	if !strings.Contains(got, "fix the bug") {
		t.Errorf("expected commit message in prompt; got:\n%s", got)
	}
	if !strings.Contains(got, "+new") || !strings.Contains(got, "```diff") {
		t.Errorf("expected fenced diff in prompt; got:\n%s", got)
	}
	if !strings.Contains(got, "what this change does and why") {
		t.Errorf("expected change-explanation instruction; got:\n%s", got)
	}
	if strings.Contains(got, "should be ignored in diff mode") {
		t.Errorf("diff mode must not emit the Source block; got:\n%s", got)
	}
}
