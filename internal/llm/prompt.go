package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Prompt text and request-formatting helpers shared by all provider adapters.
// Keeping these in one place ensures providers stay behaviorally equivalent;
// the cache key includes the provider's model name, so different providers
// never collide in the cache even though they consume the same prompts.

const SystemPromptExplain = `You are explore, an assistant that explains code at multiple zoom levels for a developer reading an unfamiliar codebase.

Rules:
- Be concrete. Reference identifiers from the source. Avoid generic phrases like "this code does X".
- Prose: 3-6 sentences, plain text, no headings. Lead with purpose, then mechanism, then anything surprising.
- Output strict JSON only, no markdown fences, matching:
  {"prose": "...", "metadata": {"imports": [], "key_types": [], "gotchas": []}}
- key_types: 0-5 most important types/structs the reader should know about.
- gotchas: 0-3 non-obvious behaviors, footguns, or invariants — only if real.
- If the snippet is trivial, say so briefly and keep metadata arrays empty.`

const SystemPromptAsk = `You are explore, helping a developer understand a specific piece of code they are looking at.

Rules:
- Answer the user's question grounded in the provided source. Quote identifiers verbatim.
- If the answer is not derivable from the source provided, say so and suggest what to look at next.
- Be concise. Plain prose, no JSON wrapper. Use short paragraphs and inline code spans for symbols.`

// BuildExplainUser renders the user-turn body for an Explain request.
func BuildExplainUser(req ExplainRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Level: %s\nPath: %s\n", req.Level, req.Path)
	if req.Symbol != "" {
		fmt.Fprintf(&b, "Symbol: %s\n", req.Symbol)
	}
	if req.IsPR {
		fmt.Fprintf(&b, "\nPull request: %s\n", strings.TrimSpace(req.PRTitle))
		if body := strings.TrimSpace(req.PRBody); body != "" {
			fmt.Fprintf(&b, "\nDescription:\n%s\n", body)
		}
		b.WriteString("\nDiff (unified, head vs. base):\n```diff\n")
		b.WriteString(req.Diff)
		b.WriteString("\n```\n")
		b.WriteString("\nReview this pull request as an experienced reviewer would. " +
			"In `prose`: lead with what the PR is trying to accomplish, then assess the approach — " +
			"correctness concerns, edge cases or inputs that look unhandled, and anything that warrants closer scrutiny before merge. " +
			"Use `key_types` for the symbols/files a reviewer should read first and `gotchas` for concrete risks, " +
			"missing test coverage, or behavioral/compatibility hazards introduced. Be specific and grounded in the diff; " +
			"do not invent issues if the change is sound — say so. Return only the JSON object described in the system prompt.")
		return b.String()
	}
	if req.IsDiff {
		if req.CommitMessage != "" {
			fmt.Fprintf(&b, "\nCommit message:\n%s\n", strings.TrimSpace(req.CommitMessage))
		}
		b.WriteString("\nDiff (unified, vs. first parent):\n```diff\n")
		b.WriteString(req.Diff)
		b.WriteString("\n```\n")
		b.WriteString("\nExplain what this change does and why, grounded in the diff and commit message. " +
			"In `prose`: lead with the change's intent, then the mechanism (which identifiers/files changed and how), then any risk or follow-up. " +
			"Use `key_types` for the most affected symbols/files and `gotchas` for behavioral or compatibility risks introduced. " +
			"Return only the JSON object described in the system prompt.")
		return b.String()
	}
	if req.Signature != "" {
		fmt.Fprintf(&b, "Signature: %s\n", req.Signature)
	}
	if len(req.Imports) > 0 {
		fmt.Fprintf(&b, "Imports: %s\n", strings.Join(req.Imports, ", "))
	}
	if len(req.Callers) > 0 {
		fmt.Fprintf(&b, "Callers: %s\n", strings.Join(req.Callers, ", "))
	}
	if len(req.Callees) > 0 {
		fmt.Fprintf(&b, "Callees: %s\n", strings.Join(req.Callees, ", "))
	}
	if req.ParentSummary != "" {
		fmt.Fprintf(&b, "Parent summary: %s\n", req.ParentSummary)
	}
	b.WriteString("\nSource:\n```\n")
	b.WriteString(req.Source)
	b.WriteString("\n```\n")
	if req.IsLong {
		b.WriteString("\nThis symbol is unusually long. In `prose`, replace the standard 3-6 sentence summary with a numbered outline of the function's inner sections (e.g., \"1. Setup: …\", \"2. Main loop: …\", \"3. Cleanup: …\"), each one short and grounded in identifiers from the source. The metadata fields stay unchanged.")
	}
	b.WriteString("\nReturn only the JSON object described in the system prompt.")
	return b.String()
}

// BuildAskFocus renders the stable focus-context turn for node-scoped Q&A.
// Session-scoped Q&A leaves Source empty and inlines focus per-question instead.
func BuildAskFocus(req AskRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Focus: %s", req.FocusPath)
	if req.FocusSymbol != "" {
		fmt.Fprintf(&b, "::%s", req.FocusSymbol)
	}
	b.WriteString("\n")
	if req.ParentSummary != "" {
		fmt.Fprintf(&b, "Parent summary: %s\n", req.ParentSummary)
	}
	b.WriteString("\nSource:\n```\n")
	b.WriteString(req.Source)
	b.WriteString("\n```\n")
	return b.String()
}

// ParseExplainJSON tolerates a stray code fence around the JSON object and
// falls back to treating the whole response as prose so the UI is never empty.
func ParseExplainJSON(s string) (*Explanation, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	var parsed struct {
		Prose    string   `json:"prose"`
		Metadata Metadata `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return &Explanation{Prose: s}, nil
	}
	return &Explanation{Prose: parsed.Prose, Metadata: parsed.Metadata}, nil
}
