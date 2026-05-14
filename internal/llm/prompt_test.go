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
