Explore is an AI-assisted code exploration TUI. It allows you to navigate a codebase while automatically generating explanations at different granularities: project-directory-module-file-symbol. It uses vim-like / lazygit-like navigation.

Implemented in go / [Bubble Tea](https://github.com/charmbracelet/bubbletea).

## Requirements

- Go 1.26+
- An Anthropic API key in `ANTHROPIC_API_KEY` (explanations call Claude directly)
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
explore [path]              # defaults to current directory
```

Flags:

- `--cache-dir <dir>` — override the BBolt cache location (default `<repo>/.explore/cache.db`)
- `--model <id>` — override the Claude model (default `claude-sonnet-4-6`)
- `--no-lsp` — skip launching `gopls`
- `--debug` — write a debug log to `<cache-dir>/debug.log`

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
- `r` — regenerate the current explanation (clears the in-memory copy)
- `q` or `ctrl+c` — quit
