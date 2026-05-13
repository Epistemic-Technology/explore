Explore is an AI-assisted code exploration TUI. It allows you to navigate a codebase while automatically generating explanations at different granularities: project-directory-module-file-symbol. It uses vim-like / lazygit-like navigation.

Implemented in go / [Bubble Tea](https://github.com/charmbracelet/bubbletea).

## Requirements

- Go 1.26+
- One of:
  - **Claude** (default): `ANTHROPIC_API_KEY`
  - **OpenAI**: `OPENAI_API_KEY` plus `--provider openai`
  - **Ollama**: a local Ollama server (defaults to `http://localhost:11434`), `--provider ollama`
- `gopls` on `PATH` for caller/callee lookups (optional; xref is disabled without it)

v0.1 only parses Go source. Other languages still show the file tree but produce no symbol-level explanations.

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
- `--no-lsp` — skip launching `gopls`
- `--debug` — write a debug log to `<cache-dir>/debug.log`

The status bar shows a running `tok: 12.3k` total once any LLM call lands. With `--token-budget` set, the figure becomes `12.3k/100k (12%)` and turns yellow at 80%, red at 100%. Cached explanations (no new LLM call) don't add to the count.

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
token_budget = 0      # 0 = track only, no ceiling
no_lsp = false
```

Use `--config <path>` to point at a different file. A missing file is fine (defaults apply); a malformed file is fatal so typos surface immediately.

The cache key includes the model name, so switching providers (or models) never returns stale explanations from a previous run.

Explanations are content-hash cached, so re-opening a file or symbol is free until the source changes.

## Keys

Navigation is vim-ish. A few essentials:

- `h j k l` / arrows — move in the tree; `l` / `space` expands, `h` collapses or moves to parent
- `gg` / `G` — top / bottom; prefix any motion with a count (e.g. `10j`)
- `enter` — focus the source pane on the current node
- `tab` / `shift+tab` — cycle panes; `alt+1/2/3` — jump to tree / explanation / source
- `[` / `]` — cycle tabs within the explanation pane
- `b` or `ctrl+o` — back in the nav stack; `ctrl+i` — forward
- `?` — open the Q&A tab (questions are scoped to the focused node)
- `/` — fuzzy search files and Go symbols; ↑/↓ to pick, Enter to jump, Esc to close
- `r` — regenerate the current explanation (bypasses both the in-memory and on-disk caches)
- `q` or `ctrl+c` — quit

## License

MIT — see [LICENSE](LICENSE).
