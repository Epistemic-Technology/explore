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
- `T`: toggle Q&A scope (node-scoped ↔ session-wide)

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

Two modes, toggle with `T`.

- **Node-scoped (default):** new thread per node. Context = node source + its explanation + parent file/dir summary. Cheapest, focused. Question history persists per node.
- **Session-wide:** one running thread. Each turn auto-attaches the *currently focused* node as fresh context (`[focus: auth/session.go::Verify]`). Lets you ask follow-ups across nodes ("how does this differ from the OAuth path we looked at?").

Both modes stream tokens. `Ctrl-c` interrupts generation cleanly.

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
- More languages, optional MCP server mode (expose explanations to other tools), shareable cache export.

## Open questions / proposed defaults

1. **Cache location:** propose `.explore/cache.db` in repo (gitignored), overridable. Alternative: always in `~/.cache/explore/<repo-hash>/`. Either works; in-repo makes it easy to delete with the repo.
2. **Symbol-level granularity for long functions:** for >200 LOC functions, split by inner blocks? Or one explanation per function? Propose: one per function for v1; revisit.
3. **Privacy on Claude/OpenAI:** any redaction of secrets before sending? Propose: warn-only via gitleaks-style regex scan; never auto-redact.
4. **Tree-sitter grammar distribution:** vendor as Go cgo bindings, or shell out to a parser binary? Propose: vendor; single-binary distribution is a core promise.
5. **Repo-level CLAUDE.md / AGENTS.md ingestion:** read as priming context for all explanations? Propose: yes, if present.
