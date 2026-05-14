// Package llm defines the provider abstraction the explanation generator
// targets. Adapters live in subpackages (e.g. llm/claude).
package llm

import "context"

type Level string

const (
	LevelRepo   Level = "repo"
	LevelDir    Level = "dir"
	LevelFile   Level = "file"
	LevelSymbol Level = "symbol"
)

// ExplainRequest carries everything a provider needs to produce a structured
// explanation. The generator builds this from index data; providers must not
// reach back into the indexer.
type ExplainRequest struct {
	Level Level

	// Path is human-readable (e.g. "auth/session.go"); Symbol is empty unless Level==Symbol.
	Path   string
	Symbol string

	// Source is the canonical text the explanation is for: file contents,
	// symbol body, or a synthesized summary for dir/repo levels.
	Source string

	// Signature is the symbol declaration for symbol-level requests.
	Signature string

	// Imports / Callers / Callees are surface facts pulled from tree-sitter + LSP.
	Imports []string
	Callers []string
	Callees []string

	// ParentSummary is the parent file/dir's prose summary, when available.
	// Used as priming context and benefits from prompt caching.
	ParentSummary string

	// RepoPrimer is the repo-level priming text (README + CLAUDE.md/AGENTS.md if present).
	// Stable across requests, ideal for caching.
	RepoPrimer string

	// IsLong marks the symbol as one whose line count exceeds the configured
	// long-function threshold. BuildExplainUser appends a structural-outline
	// instruction when this is set; providers themselves don't need to peek.
	IsLong bool
}

type Explanation struct {
	Prose    string
	Metadata Metadata
	Usage    Usage
}

// Usage is per-call token accounting. Providers fill in whatever their API
// reports; unsupported fields stay zero. CacheRead / CacheCreation are
// Claude-specific (prompt caching); other providers leave them at zero.
type Usage struct {
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// Total counts every input + output token. CacheRead/CacheCreation overlap
// with InputTokens in Claude's accounting, so they're informational only.
func (u Usage) Total() int { return u.InputTokens + u.OutputTokens }

// Add merges two usages by component-wise sum.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		InputTokens:         u.InputTokens + o.InputTokens,
		OutputTokens:        u.OutputTokens + o.OutputTokens,
		CacheReadTokens:     u.CacheReadTokens + o.CacheReadTokens,
		CacheCreationTokens: u.CacheCreationTokens + o.CacheCreationTokens,
	}
}

type Metadata struct {
	Imports  []string `json:"imports,omitempty"`
	KeyTypes []string `json:"key_types,omitempty"`
	Gotchas  []string `json:"gotchas,omitempty"`
}

type AskRequest struct {
	// FocusPath / FocusSymbol identify the node the question is about.
	FocusPath   string
	FocusSymbol string

	// Source is the source slice to ground the answer in.
	Source string

	// ParentSummary, RepoPrimer: same role as in ExplainRequest.
	ParentSummary string
	RepoPrimer    string

	// History is prior turns within this thread.
	History []Turn

	// Question is the user's new message.
	Question string
}

type Turn struct {
	Role    string // "user" or "assistant"
	Content string
}

// Token is one streamed delta from Ask. Most tokens carry only Text; the
// final token of a stream may have Usage set instead of (or in addition to)
// text — providers emit this once the underlying API reports counts.
type Token struct {
	Text  string
	Err   error
	Usage *Usage
}

type Provider interface {
	Explain(ctx context.Context, req ExplainRequest) (*Explanation, error)
	Ask(ctx context.Context, req AskRequest) (<-chan Token, error)
	Name() string
	Model() string
	SupportsPromptCaching() bool
}
