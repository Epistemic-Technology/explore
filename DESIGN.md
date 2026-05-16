# explore — Design Doc (v0.1)

## Problem

Reading an unfamiliar codebase is a navigation problem dressed as a comprehension problem. Existing tools (LSP, grep, file tree) tell you *where* things are; nothing tells you *what they're for* at the level you're currently looking. `explore` is a TUI that pairs a hierarchical browser with auto-generated AI explanations, so the answer to "what is this?" is already on screen when you arrive.

## Non-goals

- Not an editor. Read-only by default; `e` opens the current node in `$EDITOR`.
- Not a chat UI for the whole repo. Q&A is anchored to navigation context.
- Not a refactoring or static-analysis tool. LSP is for navigation, not transforms.

## Core UX

### Layout

Three-pane TUI (lazygit-ish):

```
┌─ Tree ─────────────┬─ Explanation ─────────────────────┬─ Metadata ─┐
│ repo/              │ # auth/session.go                 │ Imports:   │
│  ├─ auth/          │                                   │  - crypto  │
│  │   ├─ session.go │ Manages the per-request session   │  - db      │
│  │   │  ├─ New()   │ lifecycle: creation, refresh,     │            │
│  │   │  └─ Verify  │ invalidation. Backed by Redis via │ Callers:   │
│  │   └─ token.go   │ the db.Pool wrapper; tokens are   │  - 12 fns  │
│  ├─ api/           │ HMAC-signed (see token.go).       │            │
│  └─ cmd/           │                                   │ Callees:   │
│                    │                                   │  - 8 fns   │
└────────────────────┴───────────────────────────────────┴────────────┘
[n]ode info  [/]search  [?]ask  [u]p-refs  [d]own-refs  [b]ack  [q]uit
```

- **Left:** hierarchy tree. Repo → dir → file → symbol (functions, types, methods). Expandable with tree-sitter-derived symbols.
- **Center:** prose explanation for the focused node.
- **Right:** structured metadata (imports, callers, callees, key types, file path, LOC).

### Navigation (vim-ish)

- `h j k l` / arrows: tree navigation
- `Enter` / `l` on a leaf: descend (file → symbols, function → callees)
- `u`: jump to callers/importers (xref-up); opens a picker if >1
- `d`: jump to a callee (xref-down); picker on call site
- `b` / `Ctrl-o`: back in nav stack; `Ctrl-i`: forward
- `gg` / `G`: top/bottom of tree
- `/`: fuzzy symbol+file search (overlay)
- `?`: open Q&A pane (overlays metadata column)
- `e`: open node in `$EDITOR`
- `y`: yank path / explanation / source
- `r`: regenerate explanation for current node

### Reverse navigation = a real stack

`u` (callers) and `d` (callees) push onto a navigation stack. The stack is visible at the top of the explanation pane as breadcrumbs (`api.Handler.ServeHTTP → auth.Verify → session.Get`). `b` pops. This is the killer feature for understanding control flow.

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  TUI (Bubble Tea)                                        │
│   panes, keymap, nav stack, Q&A overlay                  │
└───────┬──────────────────────────────────────────────────┘
        │
┌───────▼──────────┐   ┌──────────────┐   ┌────────────────┐
│  Indexer         │   │  LSP Client  │   │  TS Parsers    │
│  (orchestrator)  │◄──┤  pool        │   │  (per language)│
└───┬──────────┬───┘   └──────────────┘   └────────────────┘
    │          │
┌───▼───┐  ┌───▼─────────────┐
│ Cache │  │ Explanation Gen │──► Pluggable LLM (Claude/OpenAI/Ollama)
│ (BBolt│  │ + Prefetcher    │
│ or    │  └─────────────────┘
│ SQLite│
└───────┘
```

### Components

**1. Tree-sitter layer**

- One parser per detected language. Use `go-tree-sitter` bindings with vendored grammars (Go, Python, Rust, JS/TS, Java, C/C++, Ruby for v1).
- Queries extract: top-level symbols (functions, methods, types), imports, function bodies, call sites.
- Drives the tree pane's symbol expansion and gives the LLM well-bounded source slices.

**2. LSP client pool**

- Spawn language servers on demand (gopls, pyright, rust-analyzer, typescript-language-server, etc.) using detected project markers.
- Used exclusively for `textDocument/references`, `textDocument/definition`, `textDocument/implementation`, `workspace/symbol`.
- LSP unavailability is a graceful degrade: xref keys are disabled with a tooltip; tree-sitter symbol nav still works.
- A small adapter normalizes per-language quirks (e.g., gopls vs pyright reference semantics).

**3. Explanation generator**

- One prompt template per level (repo / dir / file / symbol). Each is short and structured.
- Input context per level:
  - **Repo:** README + top-level dir names + language stats + go.mod/package.json/etc. + (if present) CLAUDE.md / AGENTS.md.
  - **Dir:** dir name + child names + 1-line summaries of each child (recursive bottom-up).
  - **File:** file source (truncated if huge), imports, top-level symbol signatures.
  - **Symbol:** symbol source + signature + (from LSP) callers/callees list + (from cache) parent file summary.
- Output is structured: `{prose: string, metadata: {imports, callers, callees, key_types, gotchas?}}`. The TUI renders prose in the center pane and metadata on the right.
- **Bottom-up build order:** symbols → files → dirs → repo. Higher-level explanations are cheaper and better-grounded because they cite child summaries.

**4. Prefetcher**

- On focus change, immediately render cached explanation if present, else stream from LLM.
- In background: enqueue likely-next nodes (visible siblings, children of focused dir, top callers/callees of focused symbol).
- Concurrency cap (configurable, default 3) and per-session token budget with warning at 80%.

**5. Cache**

- Embedded KV store (BBolt). Keys: `sha256(source_slice) + level + prompt_version + model_id`.
- Bumping `prompt_version` or switching models invalidates cleanly without nuking the cache.
- Stored alongside the repo at `.explore/cache.db` (gitignored by default), with `--cache-dir` to override (e.g., `~/.cache/explore/<repo-hash>`).

**6. LLM provider abstraction**

Single `Provider` interface:

```go
type Provider interface {
    Explain(ctx context.Context, req ExplainRequest) (Explanation, error)
    Ask(ctx context.Context, req AskRequest) (<-chan Token, error) // streaming
    Name() string
    SupportsPromptCaching() bool
}
```

Adapters: `claude` (uses prompt caching aggressively for the per-repo system prompt + parent context), `openai`, `ollama`. Config in `~/.config/explore/config.toml`:

```toml
[provider]
default = "claude"

[provider.claude]
model = "claude-sonnet-4-6"
api_key_env = "ANTHROPIC_API_KEY"

[provider.ollama]
model = "qwen2.5-coder:14b"
host = "http://localhost:11434"
```

## Q&A

The explanation pane carries three tabs, cycled with `[` / `]`:

- **Explanation** — the auto-generated prose for the focused node.
- **Q&A (node)** — per-node thread. Context = node source + its explanation + parent file/dir summary. Question history persists per node. Cheapest, focused.
- **Q&A (session)** — one running thread across the whole session. Each turn auto-attaches the *currently focused* node as fresh context (`[focus: auth/session.go::Verify]`). Lets you ask follow-ups across nodes ("how does this differ from the OAuth path we looked at?").

`?` opens Q&A on the node tab; `[` / `]` flips to session. Both stream tokens; `Ctrl-c` interrupts generation cleanly.

## Data model (sketch)

```go
type NodeID struct { Kind Kind; Path string; Symbol string } // Kind: Repo|Dir|File|Symbol

type Explanation struct {
    NodeID     NodeID
    Prose      string
    Metadata   Metadata
    SourceHash string  // content hash this was generated from
    Model      string
    PromptVer  int
    CreatedAt  time.Time
}

type Metadata struct {
    Imports  []string
    Callers  []SymbolRef
    Callees  []SymbolRef
    KeyTypes []string
    Gotchas  []string
}
```

## Phasing

**v0.1 — vertical slice (Go only)**
- TUI panes + vim keymap, tree-sitter symbol tree, gopls integration, Claude provider only, file + symbol explanations, content-hash cache, node-scoped Q&A.

**v0.2**
- Repo + dir explanations (bottom-up build), prefetcher, session-wide Q&A toggle, OpenAI + Ollama providers.

**v0.3**
- Python, TS, Rust support, fuzzy search overlay, regenerate (`r`), token budget UI, config file.

**v0.4**
- More languages, shareable cache export.

**v0.5**
- `u` / `d` xref navigation (callers / callees, picker when >1), `e` open in `$EDITOR`, `y` yank (path / explanation / source), secret-scan warn before LLM calls (gitleaks-style, never auto-redact), symbol-level granularity for long (>200 LOC) functions.

**v0.6 — git integration** (full design in [Git integration](#git-integration-v06))
- History tab on the tree pane: the full commit history of the current branch (`git log`), not scoped to the focused node.
- Highlighting a commit shows its message + changed-file stat in the source pane and a change-focused AI explanation in the explanation pane.
- `Enter` on a commit enters a **full historical snapshot**: the whole tree/source/explanations reflect the repo as it existed at that commit (read from git objects, never the working tree), with diff coloring (vs the commit's first parent) overlaid. `b`/`Esc` returns to the live HEAD view.
- Diff view per changed node (unified, syntax-highlighted via the existing `internal/highlight` pipeline).
- LLM explanations of *changes*, cached by `sha256(commit_sha + diff + prompt_version + model_id)` so re-viewing is free.

## Git integration (v0.6)

### Goals

Let the reader answer "how did this get to be the way it is?" without leaving the tool. Two confirmed product decisions shape everything below:

1. **Diff baseline = commit vs. its first parent.** Colors, diffs, and change explanations describe what *that commit itself* changed (`<sha>^..<sha>`). The history view is about understanding individual commits, not cumulative drift.
2. **`Enter` = full historical snapshot.** The entire tree/source/explanation stack reflects the repo *as it existed at that commit*, parsed from git objects. Diff coloring is overlaid on that snapshot. This is a whole-app mode, not a side panel.

### Key enabling property: the cache is content-addressed

Cache keys are `sha256(source) | level | promptver | model`. A historical file whose bytes are identical to HEAD hashes identically, so **browsing unchanged historical code is free** — it cache-hits the same entry HEAD already produced. Only files a commit actually touched cost an LLM call. The design preserves this: historical "normal" explanations go through the *existing* `ExplainFile`/`ExplainSymbol` path, just fed different bytes. No new cache plumbing for the snapshot case.

### Core abstraction: a revision-scoped file source

Everything that currently does `os.ReadFile(filepath.Join(root, rel))` or walks the filesystem must instead go through a revision. New leaf package `internal/gitsrc` (imports `model` + stdlib + `os/exec` only — obeys the no-import-cycle rule; `index` and `tui` may import it, it imports neither):

```go
package gitsrc

type Repo struct { root string }
func Open(root string) (*Repo, bool)   // false: not a git repo / no git binary → feature hidden

type Revision interface {
    Ref() string                        // "" = working tree; else commit sha
    ReadFile(rel string) ([]byte, error)
    Tree() ([]Entry, error)             // tracked files/dirs at this revision
}
```

Two implementations:

- **`workingTree`** — delegates to the filesystem. This is the default and reproduces today's behavior byte-for-byte, so live mode carries zero regression risk.
- **`commitRev{sha}`** — `git ls-tree -r --name-only <sha>` for `Tree()`, `git show <sha>:<rel>` for `ReadFile`. No checkout, no working-tree mutation (honors the read-only promise).

`Generator` gains a `Rev Revision` field (defaults to `workingTree`). The four `ExplainX` methods and `ParseFile` read through `g.Rev`. `tsparse.Parse(ctx, absPath, src)` already takes bytes and only uses the path for extension-based language detection, so historical parsing works unchanged. The TUI's `Tree` gains a pluggable lister backed by the same `Revision` (filesystem by default).

### Git CLI, not a library

Shell out to `git` via `exec.Command`. No new heavy dependency; a git feature can assume `git` is present, and missing-git degrades gracefully (History tab hidden) exactly like missing-LSP. Note on the [[project-lsp-lifetime]] rule: that rule forbids `exec.CommandContext` only for *long-lived* processes meant to outlive a request. `git log`/`git show`/`git diff` are one-shot and request-scoped — using `CommandContext` for them is correct and gives free cancellation when the user navigates away mid-load.

### UI: History tab on the tree pane

`paneTabs[paneTree]` becomes `["Tree", "History"]`, so `[`/`]` cycles it when the tree pane is focused; a dedicated `H` key jumps straight to it. The History tab lists the **full commit history of the current branch** (`git log`, capped) — it is deliberately *not* scoped to the focused node. The list loads once and never reloads on navigation; the header shows the branch name (`git rev-parse --abbrev-ref HEAD`).

A synthetic **WORKING** row sits at the top of the list, representing the uncommitted state (working tree vs. HEAD, including untracked files as additions). It behaves exactly like a commit whose parent is HEAD: highlighting it shows the changed-file list + an AI explanation of the uncommitted changes; `Enter` enters a diff overlay on the *live* working tree (colored tree + full-file inline diffs vs. HEAD). Internally it's the sentinel logical revision `workingRef` (`m.rev`); `inSnapshot()` is true (diff UI active) but `atCommitSnapshot()` is false, so LSP xref and the prefetcher stay enabled (they correctly operate on the working tree). The data-source layer special-cases `workingRef` to use `git diff HEAD` / `WorkingChanges` / `WorkingFileDiff` instead of the commit-vs-parent commands; untracked files have no `git diff`, so their inline view is synthesized as an all-added patch from the file contents.

```go
type Commit struct {
    SHA, ShortSHA, Subject, Author string
    AuthorDate time.Time
}
```

Loaded async with the existing debounced-load + spinner pattern (`gitLogMsg`). Capped at ~300 commits for v0.6; pagination deferred.

**Highlighting** a commit (j/k): source pane shows the commit message + `git show --stat` changed-file list; explanation pane shows the commit-level change explanation.

**`Enter`** on a commit: set `m.rev = commitRev{sha}`, rebuild the tree from that revision, switch to the Tree tab, render a status banner (`@ ab12cd (historical) — b to return`). Diff coloring overlaid (below).

### Diff coloring

On entering a snapshot at commit `C` (parent `P = C^`), compute `git diff --name-status P C` once → `map[rel]status`. The historical tree = `ls-tree` at `C` **plus** files deleted by `C` shown as red, non-navigable tombstones (so the user can still see what was removed). Per-row color (new fg style in `theme.go`, applied in `renderTree`, composed under the existing cursor `selectedStyle` background):

- file: added → green, modified → yellow, deleted → red, untouched → normal
- dir: aggregate of descendants — all-added green, all-deleted red, any-changed yellow, else normal

**Symbol-level coloring** (the "if possible" stretch): for a modified file, parse `P` and `C` versions, match symbols by `(Receiver, Name, Kind)`. Only in `C` → green; only in `P` → red tombstone child; in both with different source slice → yellow; identical → normal. Pure function over two `ParsedFile`s; behind a best-effort gate (skip silently if either parse fails). First thing to cut if it destabilizes.

### Diff view in the source pane

When in a snapshot and the focused node is a changed file/symbol, the source pane renders a unified diff (`git diff P C -- <path>`, scrolled to the symbol's hunk for symbol nodes) instead of plain source. Renderer is a variant of `renderSource`: parse hunks, apply new `diffAddStyle`/`diffDelStyle`/`diffHunkStyle` backgrounds, and still run the `internal/highlight` syntax spans over each line's content. An *unchanged* node in snapshot mode renders its historical source normally (syntax-highlighted, no diff) — that is the "full snapshot" requirement.

### Change-focused explanations

Mirror the `IsLong` precedent exactly (it's the established low-risk pattern: extra `ExplainRequest` fields + a `BuildExplainUser` branch, providers untouched, distinct cache level). Add to `llm.ExplainRequest`: `IsDiff bool`, `CommitMessage string`, `Diff string`. When `IsDiff`, `BuildExplainUser` emits the commit message + diff and instructs "explain what changed and why," still returning the same JSON schema. Two entry points on `Generator`:

- `ExplainCommit(ctx, sha)` — commit-level (shown when a commit is highlighted). Context = message + `--stat` + truncated full diff (reuse the 12 KB-style cutoff with a "diff truncated" note). Cache: `Key(HashSource(sha+"\n"+diff), "commit", model, promptver)` — the DESIGN'd `sha256(commit_sha+diff)` key, no `Cache` changes.
- `ExplainChange(ctx, rev, nodeID)` — per-node diff explanation in snapshot mode, cache level `"filediff"`/`"symboldiff"`, content-addressed on the node's diff text. Unchanged nodes fall through to the normal `ExplainFile`/`ExplainSymbol` path (free via the content-hash hit described above).

### Navigation across the live↔historical boundary

`nav.Stack` currently stores bare `model.NodeID`. Widen its entry to a small frame `{NodeID; Rev string}` (`Rev == ""` = working tree). This is a contained change in one leaf package. Pushing focus in snapshot mode records the rev; `b`/`Ctrl-o`/`Ctrl-i` restore it, and the TUI rebuilds the tree whenever the popped frame's rev differs from the current one. Browser back/forward semantics stay intact straight through the mode transition. `Rev` deliberately stays out of `NodeID` (it would perturb cache keys and ripple across the codebase for no benefit — the content hash already separates historical content).

### Known limitations (v0.6, by design)

- **LSP xref disabled in snapshot mode.** gopls/etc. index the working tree, not git history, so `u`/`d` and the callers/callees metadata are suppressed when `m.rev` is a commit — same graceful degrade as a missing language server. Documented, not worked around.
- No blame view, no arbitrary commit-pair compare, no branch switching, no staging. Out of scope.
- Commit-list pagination deferred (hard cap for v0.6).
- Deleted-by-commit files are visible in the History-tab change list and commit explanation but are **not** shown as red tree tombstones in the snapshot (the snapshot tree is `ls-tree` at the commit, which by definition omits them). Deferred.
- The source pane in snapshot mode shows the **whole file** at the commit with line numbers: unchanged context lines are syntax-highlighted (reusing the normal highlight pipeline on the post-image), added lines tinted green and removed lines tinted red interleaved in place. Added/removed lines are solid-tinted rather than syntax-highlighted (clean background, consistent with the cursor-row rule).

### Phasing within v0.6

1. `internal/gitsrc` + `Revision`/lister abstraction; route `Generator` and `Tree` through it; live mode unchanged (tests assert byte-identical behavior).
2. History tab + full-branch commit list; highlight → message/stat in source pane.
3. Commit-level change explanation (LLM + cache).
4. `Enter` → snapshot mode; `nav.Stack` rev frames; `b`/`Esc` return.
5. File-level diff coloring + diff view in source pane (highlight reuse).
6. Per-node change-focused explanations in snapshot mode.
7. *(stretch — landed)* symbol-level diff coloring: parse the file at the commit and its first parent, match symbols by (receiver, name), color added/modified; best-effort (no coloring on parse/read failure).

## Open questions / proposed defaults

1. **Cache location:** propose `.explore/cache.db` in repo (gitignored), overridable. Alternative: always in `~/.cache/explore/<repo-hash>/`. Either works; in-repo makes it easy to delete with the repo.
2. **Symbol-level granularity for long functions:** for >200 LOC functions, split by inner blocks? Or one explanation per function? Propose: one per function for v1; revisit.
3. **Privacy on Claude/OpenAI:** any redaction of secrets before sending? Propose: warn-only via gitleaks-style regex scan; never auto-redact.
4. **Tree-sitter grammar distribution:** vendor as Go cgo bindings, or shell out to a parser binary? Propose: vendor; single-binary distribution is a core promise.
5. **Repo-level CLAUDE.md / AGENTS.md ingestion:** read as priming context for all explanations? Propose: yes, if present.
