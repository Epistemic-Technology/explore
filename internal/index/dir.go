package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mikethicke/explore/internal/cache"
	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

// maxChildrenListed caps how many entries a dir/repo view enumerates inline.
// Beyond this we render a "... and N more" line so the prompt stays bounded
// for large directories.
const maxChildrenListed = 30

// ExplainDir returns an explanation for a directory built from its immediate
// children. Child summaries come from cached file/dir explanations where
// available; everything else falls back to a tree-derived blurb (symbol count
// for source files, child counts for subdirs). The synthesized view is hashed
// so any child re-explanation naturally invalidates the parent.
func (g *Generator) ExplainDir(ctx context.Context, relPath string) (*model.Explanation, error) {
	view, err := g.buildDirView(ctx, relPath, false)
	if err != nil {
		debug.Logf("ExplainDir: buildDirView err path=%q err=%v", relPath, err)
		return nil, err
	}
	hash := cache.HashSource([]byte(view))
	key := cache.Key(hash, "dir", g.Provider.Model(), cache.PromptVersion)
	if hit, _ := g.Cache.Get(key); hit != nil {
		debug.Logf("ExplainDir: cache hit path=%q", relPath)
		return hit, nil
	}
	debug.Logf("ExplainDir: cache miss path=%q viewLen=%d", relPath, len(view))

	llmExp, err := g.Provider.Explain(ctx, llm.ExplainRequest{
		Level:      llm.LevelDir,
		Path:       relPath,
		Source:     view,
		RepoPrimer: g.RepoPrimer,
	})
	if err != nil {
		return nil, err
	}
	exp := &model.Explanation{
		NodeID: model.NodeID{Kind: model.KindDir, Path: relPath},
		Prose:  llmExp.Prose,
		Metadata: model.Metadata{
			KeyTypes: llmExp.Metadata.KeyTypes,
			Gotchas:  llmExp.Metadata.Gotchas,
		},
		SourceHash: hash,
		Model:      g.Provider.Model(),
		PromptVer:  cache.PromptVersion,
		CreatedAt:  time.Now(),
	}
	_ = g.Cache.Put(key, exp)
	return exp, nil
}

// ExplainRepo returns an explanation for the repo root. Same shape as
// ExplainDir, plus a language-stats header. RepoPrimer (README + AGENTS.md /
// CLAUDE.md) is passed through the request so the LLM has framing context.
func (g *Generator) ExplainRepo(ctx context.Context) (*model.Explanation, error) {
	view, err := g.buildDirView(ctx, "", true)
	if err != nil {
		return nil, err
	}
	hash := cache.HashSource([]byte(view))
	key := cache.Key(hash, "repo", g.Provider.Model(), cache.PromptVersion)
	if hit, _ := g.Cache.Get(key); hit != nil {
		debug.Logf("ExplainRepo: cache hit")
		return hit, nil
	}
	debug.Logf("ExplainRepo: cache miss viewLen=%d", len(view))

	llmExp, err := g.Provider.Explain(ctx, llm.ExplainRequest{
		Level:      llm.LevelRepo,
		Path:       "",
		Source:     view,
		RepoPrimer: g.RepoPrimer,
	})
	if err != nil {
		return nil, err
	}
	exp := &model.Explanation{
		NodeID: model.NodeID{Kind: model.KindRepo, Path: ""},
		Prose:  llmExp.Prose,
		Metadata: model.Metadata{
			KeyTypes: llmExp.Metadata.KeyTypes,
			Gotchas:  llmExp.Metadata.Gotchas,
		},
		SourceHash: hash,
		Model:      g.Provider.Model(),
		PromptVer:  cache.PromptVersion,
		CreatedAt:  time.Now(),
	}
	_ = g.Cache.Put(key, exp)
	return exp, nil
}

// buildDirView lists immediate children of a directory and produces the text
// the LLM sees: an enumerated list of file/dir summaries. For the repo root
// (isRepo=true) it also prepends a language-stats line walking the whole tree.
func (g *Generator) buildDirView(ctx context.Context, relPath string, isRepo bool) (string, error) {
	abs := filepath.Join(g.Root, relPath)
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}

	var subdirs, files []os.DirEntry
	for _, e := range entries {
		if dirSkipEntry(e.Name()) {
			continue
		}
		if e.IsDir() {
			subdirs = append(subdirs, e)
		} else if !dirSkipFile(e.Name()) {
			files = append(files, e)
		}
	}
	sort.Slice(subdirs, func(i, j int) bool { return subdirs[i].Name() < subdirs[j].Name() })
	sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })

	var b strings.Builder
	if isRepo {
		fmt.Fprintf(&b, "Repository: %s\n", filepath.Base(g.Root))
		if stats := g.langStats(); stats != "" {
			fmt.Fprintf(&b, "Languages: %s\n", stats)
		}
	} else {
		fmt.Fprintf(&b, "Directory: %s\n", relPath)
	}
	b.WriteString("\nChildren:\n")

	listed := 0
	total := len(subdirs) + len(files)
	// Subdirs first so the layout reads top-down.
	for _, d := range subdirs {
		if listed >= maxChildrenListed {
			break
		}
		childPath := filepath.Join(relPath, d.Name())
		fmt.Fprintf(&b, "  - %s/ — %s\n", d.Name(), g.subdirBlurb(childPath))
		listed++
	}
	for _, f := range files {
		if listed >= maxChildrenListed {
			break
		}
		childPath := filepath.Join(relPath, f.Name())
		fmt.Fprintf(&b, "  - %s — %s\n", f.Name(), g.fileBlurb(ctx, childPath))
		listed++
	}
	if listed < total {
		fmt.Fprintf(&b, "  ... and %d more\n", total-listed)
	}
	return b.String(), nil
}

// fileBlurb returns the one-line summary for a child file. Prefers the cached
// file explanation (looked up by content hash); falls back to a tree-sitter
// symbol-count blurb. Reads the file once either way.
func (g *Generator) fileBlurb(ctx context.Context, relPath string) string {
	abs := filepath.Join(g.Root, relPath)
	src, err := os.ReadFile(abs)
	if err != nil {
		return "(unreadable)"
	}
	hash := cache.HashSource(src)
	key := cache.Key(hash, "file", g.Provider.Model(), cache.PromptVersion)
	if hit, _ := g.Cache.Get(key); hit != nil && hit.Prose != "" {
		return oneLineSummary(hit.Prose)
	}
	pf, err := tsparse.Parse(ctx, abs, src)
	if err != nil || pf == nil {
		return fmt.Sprintf("%d lines", countLines(src))
	}
	if pf.Lang == tsparse.LangUnknown || len(pf.Symbols) == 0 {
		return fmt.Sprintf("%d lines", countLines(src))
	}
	return symbolCountBlurb(pf)
}

// subdirBlurb returns the one-line summary for a child subdirectory. v0.2
// stays shallow — it reports file/subdir counts rather than recursively
// trying to explain the subtree. The prefetcher will populate deeper
// explanations over time, which will be picked up via cache hits on the
// dir-level view hash on subsequent generation.
func (g *Generator) subdirBlurb(relPath string) string {
	abs := filepath.Join(g.Root, relPath)
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "(unreadable)"
	}
	var sub, fls int
	for _, e := range entries {
		if dirSkipEntry(e.Name()) {
			continue
		}
		if e.IsDir() {
			sub++
		} else if !dirSkipFile(e.Name()) {
			fls++
		}
	}
	switch {
	case sub == 0 && fls == 0:
		return "(empty)"
	case sub == 0:
		return fmt.Sprintf("%d %s", fls, plural("file", fls))
	case fls == 0:
		return fmt.Sprintf("%d %s", sub, plural("subdir", sub))
	default:
		return fmt.Sprintf("%d %s, %d %s", fls, plural("file", fls), sub, plural("subdir", sub))
	}
}

// langStats walks the repo and counts source files by extension. Capped depth
// keeps it cheap on large repos; .git, node_modules etc. are skipped via
// dirSkipEntry.
func (g *Generator) langStats() string {
	counts := map[string]int{}
	const maxDepth = 6
	const maxFiles = 5000
	scanned := 0

	var walk func(rel string, depth int)
	walk = func(rel string, depth int) {
		if depth > maxDepth || scanned >= maxFiles {
			return
		}
		entries, err := os.ReadDir(filepath.Join(g.Root, rel))
		if err != nil {
			return
		}
		for _, e := range entries {
			if dirSkipEntry(e.Name()) {
				continue
			}
			if e.IsDir() {
				walk(filepath.Join(rel, e.Name()), depth+1)
				continue
			}
			if dirSkipFile(e.Name()) {
				continue
			}
			scanned++
			if scanned >= maxFiles {
				return
			}
			lang := langForExt(filepath.Ext(e.Name()))
			if lang == "" {
				continue
			}
			counts[lang]++
		}
	}
	walk("", 0)

	if len(counts) == 0 {
		return ""
	}
	type kv struct {
		k string
		v int
	}
	pairs := make([]kv, 0, len(counts))
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].v != pairs[j].v {
			return pairs[i].v > pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	const topN = 6
	if len(pairs) > topN {
		pairs = pairs[:topN]
	}
	var parts []string
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s: %d", p.k, p.v))
	}
	return strings.Join(parts, ", ")
}

// oneLineSummary extracts a single-sentence summary from prose. Hard-capped at
// 160 chars so a runaway "sentence" doesn't blow up the parent view.
func oneLineSummary(prose string) string {
	prose = strings.TrimSpace(prose)
	if prose == "" {
		return "(empty)"
	}
	prose = strings.ReplaceAll(prose, "\n", " ")
	// Take through the first ". " (period followed by space) so e.g. "auth.go"
	// inside the sentence doesn't break early.
	if i := strings.Index(prose, ". "); i >= 0 {
		prose = prose[:i+1]
	}
	const maxLen = 160
	if len(prose) > maxLen {
		prose = prose[:maxLen-1] + "…"
	}
	return prose
}

func symbolCountBlurb(pf *tsparse.ParsedFile) string {
	var funcs, methods, types, vars, consts int
	for _, s := range pf.Symbols {
		switch s.Kind {
		case model.SymFunc:
			funcs++
		case model.SymMethod:
			methods++
		case model.SymType:
			types++
		case model.SymVar:
			vars++
		case model.SymConst:
			consts++
		}
	}
	var parts []string
	if funcs > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", funcs, plural("func", funcs)))
	}
	if methods > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", methods, plural("method", methods)))
	}
	if types > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", types, plural("type", types)))
	}
	if vars > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", vars, plural("var", vars)))
	}
	if consts > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", consts, plural("const", consts)))
	}
	if len(parts) == 0 {
		return "(no symbols)"
	}
	return strings.Join(parts, ", ")
}

func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// langForExt maps a file extension to a friendly language name. Empty result
// means "don't count in lang stats" (READMEs, configs, etc. are tracked as
// "Markdown"/"YAML" but obscure extensions are ignored).
func langForExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".go":
		return "Go"
	case ".py":
		return "Python"
	case ".rs":
		return "Rust"
	case ".js":
		return "JavaScript"
	case ".jsx":
		return "JavaScript"
	case ".ts":
		return "TypeScript"
	case ".tsx":
		return "TypeScript"
	case ".java":
		return "Java"
	case ".kt":
		return "Kotlin"
	case ".rb":
		return "Ruby"
	case ".c", ".h":
		return "C"
	case ".cc", ".cpp", ".cxx", ".hpp":
		return "C++"
	case ".cs":
		return "C#"
	case ".swift":
		return "Swift"
	case ".sh", ".bash":
		return "Shell"
	case ".md", ".markdown":
		return "Markdown"
	case ".yml", ".yaml":
		return "YAML"
	case ".toml":
		return "TOML"
	case ".json":
		return "JSON"
	case ".html":
		return "HTML"
	case ".css":
		return "CSS"
	}
	return ""
}

// dirSkipEntry / dirSkipFile mirror the tree-pane skip lists so the synthesized
// view doesn't disagree with what the user sees. Kept here (not imported from
// tui) to preserve the rule that index has no UI dependency.
func dirSkipEntry(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target":
		return true
	}
	return false
}

func dirSkipFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp",
		".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z",
		".pdf", ".mp3", ".mp4", ".mov", ".avi",
		".o", ".a", ".so", ".dylib", ".dll", ".exe", ".bin", ".class",
		".pyc", ".pyo", ".wasm":
		return true
	}
	switch name {
	case "go.sum", "package-lock.json", "yarn.lock", "Cargo.lock", "poetry.lock", "Pipfile.lock":
		return true
	}
	return false
}
