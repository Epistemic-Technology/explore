// Command explore is the entrypoint TUI binary: `explore [path]`.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/config"
	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/index"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/llm/claude"
	"github.com/mikethicke/explore/internal/llm/ollama"
	"github.com/mikethicke/explore/internal/llm/openai"
	"github.com/mikethicke/explore/internal/lsp"
	"github.com/mikethicke/explore/internal/tui"
)

// reorderArgs returns args with every flag token moved ahead of every
// positional arg. The standard `flag` package stops parsing at the first
// positional, which trips users who type `explore <path> --debug` expecting
// getopt-style permissive parsing. Boolean flags are detected via fs.Lookup
// so we know when *not* to consume the next token as the flag's value.
// `--` ends flag processing, POSIX-style — everything after stays positional.
func reorderArgs(args []string, fs *flag.FlagSet) []string {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positionals = append(positionals, a)
			continue
		}
		flags = append(flags, a)
		if strings.Contains(a, "=") {
			continue // self-contained: --flag=value
		}
		name := strings.TrimLeft(a, "-")
		f := fs.Lookup(name)
		if f == nil {
			// Unknown flag — leave to fs.Parse to error on; assume bool so we
			// don't accidentally eat a positional.
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positionals...)
}

func main() {
	// Subcommand dispatch: these are one-shot operations that exit before the
	// TUI flag set is parsed. Anything else falls through to the normal path.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "export-cache":
			runExportCache(os.Args[2:])
			return
		case "import-cache":
			runImportCache(os.Args[2:])
			return
		}
	}

	// CLI flags default to "" / 0 so an unset flag means "use config or
	// built-in default", not "override with the zero value".
	cacheDir := flag.String("cache-dir", "", "override cache directory (default: <repo>/.explore)")
	configPath := flag.String("config", "", "config file path (default: $XDG_CONFIG_HOME/explore/config.toml or ~/.config/explore/config.toml)")
	providerName := flag.String("provider", "", "LLM provider: claude | openai | ollama (default from config)")
	model := flag.String("model", "", "override model name (provider-specific default if empty)")
	ollamaHost := flag.String("ollama-host", "", "Ollama host (default from config or $OLLAMA_HOST)")
	openaiEndpoint := flag.String("openai-endpoint", "", "OpenAI endpoint override (default from config)")
	noLSP := flag.Bool("no-lsp", false, "disable gopls integration")
	debugFlag := flag.Bool("debug", false, "write debug log to <cache-dir>/debug.log")
	tokenBudget := flag.Int("token-budget", -1, "session token budget; 0 = track only, -1 = use config default")
	// Move flags ahead of positionals so `explore <path> --debug` works the
	// same as `explore --debug <path>` — see reorderArgs.
	os.Args = append([]string{os.Args[0]}, reorderArgs(os.Args[1:], flag.CommandLine)...)
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

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fatal(err)
	}
	// Apply CLI overrides on top of the file/default values. Empty strings
	// and -1 ints are sentinels for "flag not set".
	if *providerName != "" {
		cfg.Provider.Default = *providerName
	}
	if *model != "" {
		switch cfg.Provider.Default {
		case "openai":
			cfg.Provider.OpenAI.Model = *model
		case "ollama":
			cfg.Provider.Ollama.Model = *model
		default:
			cfg.Provider.Claude.Model = *model
		}
	}
	if *ollamaHost != "" {
		cfg.Provider.Ollama.Host = *ollamaHost
	}
	if *openaiEndpoint != "" {
		cfg.Provider.OpenAI.Endpoint = *openaiEndpoint
	}
	if *tokenBudget >= 0 {
		cfg.UI.TokenBudget = *tokenBudget
	}
	if *noLSP {
		cfg.UI.NoLSP = true
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

	provider, err := buildProvider(cfg)
	if err != nil {
		fatal(err)
	}
	debug.Logf("startup: provider=%s model=%q root=%q tokenBudget=%d", provider.Name(), provider.Model(), absRoot, cfg.UI.TokenBudget)

	var lspPool *lsp.Pool
	if !cfg.UI.NoLSP {
		lspPool = lsp.NewPool(absRoot, nil)
		defer lspPool.Close()
	}

	gen := index.NewGenerator(absRoot, c, provider, lspPool)
	gen.LongFunctionThreshold = cfg.UI.LongFunctionThreshold
	tree, err := tui.NewTree(absRoot)
	if err != nil {
		fatal(err)
	}
	prefetcher := index.NewPrefetcher(gen, 0) // 0 → default concurrency (3)
	defer prefetcher.Close()
	m := tui.NewModel(gen, tree, prefetcher, cfg.UI.TokenBudget)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "explore:", err)
	os.Exit(1)
}

// loadConfig resolves the config path (CLI override or DefaultPath) and
// loads it. Missing file → silent default; parse error → fatal so typos are loud.
func loadConfig(path string) (config.Config, error) {
	if path == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return config.Default(), nil
		}
		path = p
	}
	return config.Load(path)
}

// runExportCache implements `explore export-cache <out.json> [repo-path]`.
// It opens the repo's cache, dumps all entries to the given path, and exits.
func runExportCache(args []string) {
	fs := flag.NewFlagSet("export-cache", flag.ExitOnError)
	cacheDir := fs.String("cache-dir", "", "override cache directory (default: <repo>/.explore)")
	fs.Parse(reorderArgs(args, fs))
	if fs.NArg() < 1 {
		fatal(fmt.Errorf("usage: explore export-cache <out.json> [repo-path]"))
	}
	outPath := fs.Arg(0)
	root := "."
	if fs.NArg() > 1 {
		root = fs.Arg(1)
	}
	cachePath, err := resolveCachePath(root, *cacheDir)
	if err != nil {
		fatal(err)
	}
	c, err := cache.Open(cachePath)
	if err != nil {
		fatal(err)
	}
	defer c.Close()

	f, err := os.Create(outPath)
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	if err := c.Export(f); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "exported cache to %s\n", outPath)
}

// runImportCache implements `explore import-cache <in.json> [repo-path]`.
// By default, existing local entries are preserved; --overwrite replaces them.
func runImportCache(args []string) {
	fs := flag.NewFlagSet("import-cache", flag.ExitOnError)
	cacheDir := fs.String("cache-dir", "", "override cache directory (default: <repo>/.explore)")
	overwrite := fs.Bool("overwrite", false, "replace existing local entries instead of skipping them")
	fs.Parse(reorderArgs(args, fs))
	if fs.NArg() < 1 {
		fatal(fmt.Errorf("usage: explore import-cache <in.json> [repo-path]"))
	}
	inPath := fs.Arg(0)
	root := "."
	if fs.NArg() > 1 {
		root = fs.Arg(1)
	}
	cachePath, err := resolveCachePath(root, *cacheDir)
	if err != nil {
		fatal(err)
	}
	c, err := cache.Open(cachePath)
	if err != nil {
		fatal(err)
	}
	defer c.Close()

	f, err := os.Open(inPath)
	if err != nil {
		fatal(err)
	}
	defer f.Close()
	res, err := c.Import(f, *overwrite)
	if err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "imported %d entries (skipped %d existing)\n", res.Added, res.Skipped)
}

// resolveCachePath mirrors the path logic in main() so subcommands and the
// TUI agree on where cache.db lives.
func resolveCachePath(root, cacheDirOverride string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(absRoot); err != nil || !st.IsDir() {
		return "", fmt.Errorf("not a directory: %s", absRoot)
	}
	if cacheDirOverride == "" {
		return filepath.Join(absRoot, ".explore", "cache.db"), nil
	}
	return filepath.Join(cacheDirOverride, "cache.db"), nil
}

// buildProvider picks an llm.Provider from the resolved config. Missing API
// keys are a warning, not a fatal — the TUI still launches and renders the
// file tree; explanations fail at request time with a clear error.
func buildProvider(cfg config.Config) (llm.Provider, error) {
	switch cfg.Provider.Default {
	case "claude", "":
		env := cfg.Provider.Claude.APIKeyEnv
		key := os.Getenv(env)
		if key == "" {
			fmt.Fprintf(os.Stderr, "warning: %s not set — explanations will fail.\n", env)
		}
		return claude.New(key, cfg.Provider.Claude.Model), nil
	case "openai":
		env := cfg.Provider.OpenAI.APIKeyEnv
		key := os.Getenv(env)
		if key == "" {
			fmt.Fprintf(os.Stderr, "warning: %s not set — explanations will fail.\n", env)
		}
		return openai.New(key, cfg.Provider.OpenAI.Model, cfg.Provider.OpenAI.Endpoint), nil
	case "ollama":
		host := cfg.Provider.Ollama.Host
		if host == "" {
			host = os.Getenv("OLLAMA_HOST")
		}
		return ollama.New(cfg.Provider.Ollama.Model, host), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want: claude | openai | ollama)", cfg.Provider.Default)
	}
}
