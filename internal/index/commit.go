package index

import (
	"context"
	"time"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/model"
)

// commitDiffPromptCap bounds the diff text sent to the LLM. The cache key
// hashes the *full* sha+diff (commits are immutable, so this is stable), but a
// giant refactor commit shouldn't blow the context window — we send a prefix
// and tell the model it was truncated.
const commitDiffPromptCap = 16000

// ExplainCommit produces a change-focused explanation for a commit: what it
// did and why, grounded in the commit message + unified diff. Cached by
// sha256(sha+"\n"+diff) under the "commit" level, so re-viewing is free and a
// history rewrite naturally invalidates the entry. commitMsg/diff are passed
// in by the caller (which already has a git repo handle) so this package stays
// decoupled from gitsrc.Repo.
func (g *Generator) ExplainCommit(ctx context.Context, sha, commitMsg, diff string) (*model.Explanation, error) {
	key := cache.Key(cache.HashSource([]byte(sha+"\n"+diff)), "commit", g.Provider.Model(), cache.PromptVersion)
	if !shouldRegenerate(ctx) {
		if hit, _ := g.Cache.Get(key); hit != nil {
			debug.Logf("ExplainCommit: cache hit sha=%s", sha)
			return hit, nil
		}
	}
	debug.Logf("ExplainCommit: cache miss sha=%s diffLen=%d regen=%v", sha, len(diff), shouldRegenerate(ctx))

	promptDiff := diff
	if len(promptDiff) > commitDiffPromptCap {
		promptDiff = promptDiff[:commitDiffPromptCap] + "\n... [diff truncated]"
	}
	req := llm.ExplainRequest{
		Level:         llm.LevelCommit,
		Path:          sha,
		IsDiff:        true,
		CommitMessage: commitMsg,
		Diff:          promptDiff,
		RepoPrimer:    g.RepoPrimer,
	}
	g.reportSecrets(commitMsg + "\n" + promptDiff)
	llmExp, err := g.Provider.Explain(ctx, req)
	if err != nil {
		return nil, err
	}
	g.reportUsage(llmExp.Usage)

	exp := &model.Explanation{
		NodeID: model.NodeID{Kind: model.KindRepo, Path: "commit:" + sha},
		Prose:  llmExp.Prose,
		Metadata: model.Metadata{
			KeyTypes: llmExp.Metadata.KeyTypes,
			Gotchas:  llmExp.Metadata.Gotchas,
		},
		SourceHash: cache.HashSource([]byte(sha)),
		Model:      g.Provider.Model(),
		PromptVer:  cache.PromptVersion,
		CreatedAt:  time.Now(),
	}
	_ = g.Cache.Put(key, exp)
	return exp, nil
}

// ExplainChange produces a change-focused explanation for a single node (a
// file the active snapshot commit modified) from that node's slice of the
// commit diff. Cached content-addressed on the diff under the "filediff"
// level, so re-viewing is free and any history rewrite invalidates it.
// Unchanged nodes never reach here — the caller routes them through the
// normal ExplainFile/ExplainSymbol path (which cache-hits HEAD for free).
func (g *Generator) ExplainChange(ctx context.Context, path, symbol, commitMsg, diff string) (*model.Explanation, error) {
	key := cache.Key(cache.HashSource([]byte(path+"\n"+symbol+"\n"+diff)), "filediff", g.Provider.Model(), cache.PromptVersion)
	if !shouldRegenerate(ctx) {
		if hit, _ := g.Cache.Get(key); hit != nil {
			debug.Logf("ExplainChange: cache hit path=%q sym=%q", path, symbol)
			return hit, nil
		}
	}
	debug.Logf("ExplainChange: cache miss path=%q sym=%q diffLen=%d", path, symbol, len(diff))

	promptDiff := diff
	if len(promptDiff) > commitDiffPromptCap {
		promptDiff = promptDiff[:commitDiffPromptCap] + "\n... [diff truncated]"
	}
	req := llm.ExplainRequest{
		Level:         llm.LevelCommit,
		Path:          path,
		Symbol:        symbol,
		IsDiff:        true,
		CommitMessage: commitMsg,
		Diff:          promptDiff,
		RepoPrimer:    g.RepoPrimer,
	}
	g.reportSecrets(commitMsg + "\n" + promptDiff)
	llmExp, err := g.Provider.Explain(ctx, req)
	if err != nil {
		return nil, err
	}
	g.reportUsage(llmExp.Usage)
	exp := &model.Explanation{
		NodeID: model.NodeID{Kind: model.KindFile, Path: path},
		Prose:  llmExp.Prose,
		Metadata: model.Metadata{
			KeyTypes: llmExp.Metadata.KeyTypes,
			Gotchas:  llmExp.Metadata.Gotchas,
		},
		SourceHash: cache.HashSource([]byte(diff)),
		Model:      g.Provider.Model(),
		PromptVer:  cache.PromptVersion,
		CreatedAt:  time.Now(),
	}
	_ = g.Cache.Put(key, exp)
	return exp, nil
}
