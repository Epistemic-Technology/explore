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
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/llm/claude"
	"github.com/mikethicke/explore/internal/llm/ollama"
	"github.com/mikethicke/explore/internal/llm/openai"
	"github.com/mikethicke/explore/internal/lsp"
	"github.com/mikethicke/explore/internal/tui"
)

func main() {
	cacheDir := flag.String("cache-dir", "", "override cache directory (default: <repo>/.explore)")
	providerName := flag.String("provider", "claude", "LLM provider: claude | openai | ollama")
	model := flag.String("model", "", "override model name (provider-specific default if empty)")
	ollamaHost := flag.String("ollama-host", "", "Ollama host (default: $OLLAMA_HOST or http://localhost:11434)")
	openaiEndpoint := flag.String("openai-endpoint", "", "OpenAI endpoint override (e.g. Azure-compatible proxy)")
	noLSP := flag.Bool("no-lsp", false, "disable gopls integration")
	debugFlag := flag.Bool("debug", false, "write debug log to <cache-dir>/debug.log")
	tokenBudget := flag.Int("token-budget", 0, "session token budget; 0 means track only (no ceiling)")
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

	provider, err := buildProvider(*providerName, *model, *ollamaHost, *openaiEndpoint)
	if err != nil {
		fatal(err)
	}
	debug.Logf("startup: provider=%s model=%q root=%q", provider.Name(), provider.Model(), absRoot)

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
	prefetcher := index.NewPrefetcher(gen, 0) // 0 → default concurrency (3)
	defer prefetcher.Close()
	m := tui.NewModel(gen, tree, prefetcher, *tokenBudget)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "explore:", err)
	os.Exit(1)
}

// buildProvider picks an llm.Provider from --provider plus env-fallback secrets.
// Missing API keys are a warning, not a fatal — the TUI still launches and
// renders the file tree; explanations fail at request time with a clear error.
func buildProvider(name, model, ollamaHost, openaiEndpoint string) (llm.Provider, error) {
	switch name {
	case "claude", "":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			fmt.Fprintln(os.Stderr, "warning: ANTHROPIC_API_KEY not set — explanations will fail.")
		}
		return claude.New(key, model), nil
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			fmt.Fprintln(os.Stderr, "warning: OPENAI_API_KEY not set — explanations will fail.")
		}
		return openai.New(key, model, openaiEndpoint), nil
	case "ollama":
		host := ollamaHost
		if host == "" {
			host = os.Getenv("OLLAMA_HOST")
		}
		return ollama.New(model, host), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want: claude | openai | ollama)", name)
	}
}
