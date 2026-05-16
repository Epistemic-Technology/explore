Explore is an AI-assisted code exploration TUI. It pairs a hierarchical browser (repo → directory → file → symbol) with auto-generated AI explanations at every level, so the answer to "what is this?" is already on screen when you arrive. Navigation is vim-like / lazygit-like.

Implemented in Go / [Bubble Tea](https://github.com/charmbracelet/bubbletea).

## Requirements

- Go 1.26+
- One of:
  - **Claude** (default): `ANTHROPIC_API_KEY`
  - **OpenAI**: `OPENAI_API_KEY` plus `--provider openai`
  - **Ollama**: a local Ollama server (defaults to `http://localhost:11434`), `--provider ollama`
- `git` on `PATH` — optional; enables the History tab and historical snapshots. Without it (or outside a git repo) the rest of the tool works unchanged.
- Language servers on `PATH` for caller/callee lookups (all optional; xref is disabled per-language when the server is missing):
  - Go: `gopls`
  - Python: `pyright-langserver`
  - TypeScript/TSX: `typescript-language-server`
  - Rust: `rust-analyzer`
  - Ruby: `ruby-lsp`
  - Java: `jdtls`
  - C/C++: `clangd`

Symbol-level explanations and tree-sitter syntax highlighting are available for Go, Python, TypeScript (`.ts` / `.tsx`), Rust, Ruby, Java, and C/C++. Other languages still show in the file tree but won't expand to a symbol list. The focused row drops styling so the cursor highlight reads cleanly.

## Install

```sh
go install github.com/mikethicke/explore/cmd/explore@latest
```

Or from a clone:

```sh
go build -o explore ./cmd/explore
```

## Usage

```sh
export ANTHROPIC_API_KEY=sk-ant-...
explore [path]                                # Claude (default)
explore --provider openai [path]              # OPENAI_API_KEY required
explore --provider ollama --model qwen2.5-coder:14b [path]
```

Flags:

- `--cache-dir <dir>` — override the BBolt cache location (default `<repo>/.explore/cache.db`)
- `--provider <claude|openai|ollama>` — pick an LLM backend (default `claude`)
- `--model <id>` — override the model (provider-specific default if empty)
- `--ollama-host <url>` — Ollama host (default: `$OLLAMA_HOST` or `http://localhost:11434`)
- `--openai-endpoint <url>` — OpenAI endpoint override (e.g. Azure-compatible proxy)
- `--token-budget <N>` — session token ceiling for the status-bar indicator; 0 means track-only
- `--config <path>` — use a specific config file instead of the default location
- `--no-lsp` — skip launching language servers
- `--debug` — write a debug log to `<cache-dir>/debug.log`

The status bar shows a running `tok: 12.3k` total once any LLM call lands. With `--token-budget` set, the figure becomes `12.3k/100k (12%)` and turns yellow at 80%, red at 100%. Cached explanations (no new LLM call) don't add to the count. Before each request the payload is scanned for credential-shaped strings (gitleaks-style); a match shows a status-bar warning but never blocks or redacts the request.

## Layout

Three panes:

- **Tree** (left) — repo → dir → file → symbol, expanded lazily. A second **History** tab on this pane lists commits.
- **Explanation** (center-top) — prose for the focused node, with **Q&A (node)** and **Q&A (session)** tabs alongside it.
- **Source** (center-bottom) — the focused file with syntax highlighting, line numbers, and line selection.

Explanations are generated bottom-up (symbols → files → dirs → repo) and content-hash cached, so re-opening anything is free until the source changes. A background prefetcher warms the nodes you're likely to visit next. The cache key includes the model name, so switching providers or models never returns stale explanations.

Q&A has two modes: **node** (a thread scoped to the focused node) and **session** (one running thread that auto-attaches whatever you're looking at, so you can ask follow-ups across nodes). Reach them via pane `2` or by cycling tabs with `[` / `]`.

## Git integration

Press `H` (or cycle to the History tab with `[` / `]` on the tree pane) to see the full commit history of the current branch.

- A synthetic **WORKING** row sits at the top, representing everything uncommitted (working tree vs. HEAD, including untracked files).
- Highlighting a commit shows its message and changed-file list in the source pane, plus an AI explanation of *what that commit changed* in the explanation pane.
- `Enter` on a commit drops the whole app into that commit's state: the tree, source, and explanations all reflect the repo as it existed then (read from git objects — your working tree is never touched). Changed nodes are colored (green = added, yellow = modified) and the source pane shows the full file with the commit's additions/removals highlighted inline. `Enter` on WORKING does the same against your uncommitted changes.
- `Esc` or `b` returns to the live working tree. `b` / `Ctrl+O` / `Ctrl+I` navigate across the live↔historical boundary correctly.

LSP cross-references are disabled inside a historical snapshot (language servers index the working tree, not history); they stay live in WORKING mode.

## Config file

Settings can be persisted to `~/.config/explore/config.toml` (or `$XDG_CONFIG_HOME/explore/config.toml`). CLI flags override file values; file values override defaults.

```toml
[provider]
default = "claude"

[provider.claude]
model = "claude-sonnet-4-6"
api_key_env = "ANTHROPIC_API_KEY"

[provider.openai]
model = "gpt-4o-mini"
api_key_env = "OPENAI_API_KEY"
# endpoint = "https://my-azure-proxy.example.com/v1/chat/completions"

[provider.ollama]
model = "qwen2.5-coder:14b"
host = "http://localhost:11434"

[ui]
token_budget = 0              # 0 = track only, no ceiling
no_lsp = false
long_function_threshold = 200 # functions longer than this get a structured outline; 0 disables
```

Use `--config <path>` to point at a different file. A missing file is fine (defaults apply); a malformed file is fatal so typos surface immediately.

## Sharing the cache

Explanations can be exported and imported so a team shares LLM output instead of regenerating it. Both are one-shot subcommands that exit before the TUI starts:

```sh
explore export-cache cache.json [repo-path]
explore import-cache [--overwrite] cache.json [repo-path]
```

Import skips entries that already exist locally unless `--overwrite` is given. Because cache keys encode the source hash, level, prompt version, and model, imported entries only ever apply to byte-identical source under the same model.

## Keys

Navigation is vim-ish. Press `?` for the full in-app cheat sheet.

**Panes & tabs**

- `1` / `2` / `3` — focus tree / explanation / source
- `Tab` / `Shift+Tab` — next / previous pane
- `[` / `]` — previous / next tab within the focused pane
- `o` / `b` / `Ctrl+O` — back in the nav stack; `Ctrl+I` — forward

**Tree pane**

- `j` / `k` / arrows — move; `Space` / `l` / `→` expand, `h` / `←` collapse or climb to parent
- `gg` / `G` — top / bottom; `Ngg` — jump to row N (prefix any motion with a count, e.g. `10j`)
- `Enter` — open the node in the source pane
- `H` — switch to the History tab (commits + WORKING)

**Source pane**

- `j` / `k` — line down / up; `J` / `K` or `Ctrl+D` / `Ctrl+U` — page
- `gg` / `G` — first / last line; `Ngg` or `:N` — jump to line N
- `v` — start/stop line selection (extend with `j`/`k`); `x` — explain the selection (or current line)

**Actions**

- `/` — fuzzy search files & symbols; ↑/↓ to pick, Enter to jump, Esc to close
- `r` — regenerate the current explanation (bypasses the in-memory and on-disk caches)
- `e` — open the focused file in `$EDITOR`
- `u` — callers of the focused symbol; `d` — callees on the current source line (picker when >1)
- `y` — yank menu: `p` path · `e` explanation · `s` source
- `?` — full keyboard cheat sheet; `q` or `Ctrl+C` — quit

## License

MIT — see [LICENSE](LICENSE).
