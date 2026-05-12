// Command explore is the entrypoint TUI binary: `explore [path]`.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/index"
	"github.com/mikethicke/explore/internal/llm/claude"
	"github.com/mikethicke/explore/internal/lsp"
	"github.com/mikethicke/explore/internal/tui"
)

func main() {
	cacheDir := flag.String("cache-dir", "", "override cache directory (default: <repo>/.explore)")
	model := flag.String("model", "", "override Claude model (default: claude-sonnet-4-6)")
	noLSP := flag.Bool("no-lsp", false, "disable gopls integration")
	debugFlag := flag.Bool("debug", false, "write debug log to <cache-dir>/debug.log")
	flag.Parse()

	root := "."
	if flag.NArg() > 0 {
		root = flag.Arg(0)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		fatal(err)
	}
	if st, err := os.Stat(absRoot); err != nil || !st.IsDir() {
		fatal(fmt.Errorf("not a directory: %s", absRoot))
	}

	cachePath := *cacheDir
	if cachePath == "" {
		cachePath = filepath.Join(absRoot, ".explore", "cache.db")
	} else {
		cachePath = filepath.Join(cachePath, "cache.db")
	}
	c, err := cache.Open(cachePath)
	if err != nil {
		fatal(err)
	}
	defer c.Close()

	if *debugFlag {
		logPath := filepath.Join(filepath.Dir(cachePath), "debug.log")
		if err := debug.Init(logPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: debug log unavailable: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "debug log: %s\n", logPath)
			defer debug.Close()
		}
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "warning: ANTHROPIC_API_KEY not set — explanations will fail.")
		debug.Logf("startup: ANTHROPIC_API_KEY is empty")
	} else {
		debug.Logf("startup: API key present (len=%d), model=%q, root=%q", len(apiKey), *model, absRoot)
	}
	provider := claude.New(apiKey, *model)

	var lspClient *lsp.Client
	if !*noLSP {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		lspClient, err = lsp.Start(ctx, "gopls", absRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gopls unavailable, xref disabled: %v\n", err)
			lspClient = nil
		} else {
			defer lspClient.Close()
		}
	}

	gen := index.NewGenerator(absRoot, c, provider, lspClient)
	tree, err := tui.NewTree(absRoot)
	if err != nil {
		fatal(err)
	}
	m := tui.NewModel(gen, tree)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "explore:", err)
	os.Exit(1)
}
