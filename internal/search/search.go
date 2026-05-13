// Package search powers the `/` overlay: indexes the repo's files and Go
// symbols, then ranks subsequence matches against a typed query.
//
// The matcher is fzf-style: case-insensitive subsequence, with bonuses for
// matches at word boundaries (separators, camelCase transitions) and
// consecutive runs. Scores are integers; higher is better.
package search

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

// Entry is one searchable thing — a file or a symbol.
type Entry struct {
	ID    model.NodeID
	Label string // display + match target, e.g. "path/to/file.go" or "FuncName  path/to/file.go"
	// matchKey is the lowercase form of the part that matters most for ranking:
	// for files, the path; for symbols, the symbol name. The full Label is
	// rendered to the user but only matchKey is scored against. This keeps
	// ranking sane — "Get" should rank symbols named Get above files in deep
	// paths that happen to contain g/e/t.
	matchKey string
}

// Index is an in-memory list of searchable entries.
type Index struct {
	entries []Entry
}

// Len reports the number of entries; useful for status lines.
func (i *Index) Len() int {
	if i == nil {
		return 0
	}
	return len(i.entries)
}

// BuildIndex walks root using the same skip rules as the tree pane, then
// parses every Go source file to enumerate symbols. Non-Go files are still
// indexed (by path) so users can jump to them.
//
// The walk is parallelized for symbol parsing; for typical mid-size repos
// this takes well under a second. The function is synchronous so callers can
// show a status while it runs.
func BuildIndex(ctx context.Context, root string) (*Index, error) {
	var files []string
	if err := walkRepo(root, &files); err != nil {
		return nil, err
	}

	entries := make([]Entry, 0, len(files)*4)
	var mu sync.Mutex

	const workers = 4
	type job struct{ rel string }
	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				local := parseEntries(ctx, root, j.rel)
				mu.Lock()
				entries = append(entries, local...)
				mu.Unlock()
			}
		}()
	}
	for _, rel := range files {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil, ctx.Err()
		case jobs <- job{rel: rel}:
		}
	}
	close(jobs)
	wg.Wait()

	// Stable ordering by Label so equal-score results render deterministically.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Label < entries[j].Label })
	debug.Logf("search.BuildIndex: indexed %d entries from %d files", len(entries), len(files))
	return &Index{entries: entries}, nil
}

// parseEntries returns the file entry plus one symbol entry per top-level
// symbol parseable by tsparse. Non-Go files only get the file entry.
func parseEntries(ctx context.Context, root, rel string) []Entry {
	out := []Entry{{
		ID:       model.NodeID{Kind: model.KindFile, Path: rel},
		Label:    rel,
		matchKey: strings.ToLower(rel),
	}}
	abs := filepath.Join(root, rel)
	src, err := os.ReadFile(abs)
	if err != nil {
		return out
	}
	pf, err := tsparse.Parse(ctx, abs, src)
	if err != nil || pf == nil {
		return out
	}
	for _, s := range pf.Symbols {
		label := s.Name + "  " + rel
		if s.Kind == model.SymMethod && s.Receiver != "" {
			label = s.Receiver + "." + s.Name + "  " + rel
		}
		out = append(out, Entry{
			ID:       model.NodeID{Kind: model.KindSymbol, Path: rel, Symbol: s.Name},
			Label:    label,
			matchKey: strings.ToLower(s.Name),
		})
	}
	return out
}

func walkRepo(root string, out *[]string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable dirs
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "." {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if skipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if skipFile(name) {
			return nil
		}
		*out = append(*out, rel)
		return nil
	})
}

// Mirrors internal/tui/tree.go skipEntry / skipFile (kept here to avoid an
// import cycle with the tui package). If those lists drift, search will
// surface files the tree hides — annoying but not broken.
func skipDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target":
		return true
	}
	return false
}

func skipFile(name string) bool {
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

// Result is one match returned by Search. Positions are byte offsets into
// Entry.Label, suitable for highlighting matched characters.
type Result struct {
	Entry
	Score     int
	Positions []int // byte offsets in Label that matched
}

// Search returns the best matches for query, ranked highest-score-first.
// Empty query returns the first `limit` entries (useful so the overlay isn't
// blank when first opened).
func (i *Index) Search(query string, limit int) []Result {
	if i == nil {
		return nil
	}
	if limit <= 0 {
		limit = 30
	}
	if strings.TrimSpace(query) == "" {
		out := make([]Result, 0, limit)
		for _, e := range i.entries {
			if len(out) >= limit {
				break
			}
			out = append(out, Result{Entry: e})
		}
		return out
	}
	q := strings.ToLower(query)
	results := make([]Result, 0, 64)
	for _, e := range i.entries {
		score, pos := matchScore(q, e.matchKey)
		if score == noMatch {
			continue
		}
		// Translate match positions in matchKey back to Label positions. For
		// files matchKey == strings.ToLower(Label) so positions align directly;
		// for symbols matchKey is just the symbol name (the prefix of Label
		// before the two spaces), so positions still align.
		results = append(results, Result{Entry: e, Score: score, Positions: pos})
	}
	sort.SliceStable(results, func(a, b int) bool {
		if results[a].Score != results[b].Score {
			return results[a].Score > results[b].Score
		}
		return results[a].Label < results[b].Label
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

const noMatch = -1 << 30

// matchScore runs a case-insensitive subsequence match of query against
// haystack (both lowercased by caller). Returns noMatch if not all query
// chars are present in order, otherwise a score plus the matched positions
// in haystack. Scoring rewards:
//   - exact prefix matches
//   - matches at word boundaries (after / . _ - or before a capital in original casing)
//   - consecutive runs of matched chars
//
// The haystack is the lowercased form, so word-boundary detection is by
// separator char only — that's enough in practice.
func matchScore(query, haystack string) (int, []int) {
	if query == "" {
		return 0, nil
	}
	if len(query) > len(haystack) {
		return noMatch, nil
	}
	if query == haystack {
		return 1000, indexRange(len(haystack))
	}
	if strings.HasPrefix(haystack, query) {
		return 800 - len(haystack), indexRange(len(query))
	}
	if idx := strings.Index(haystack, query); idx >= 0 {
		score := 500 - idx - len(haystack)
		if idx == 0 || isBoundary(haystack[idx-1]) {
			score += 60
		}
		return score, seqRange(idx, len(query))
	}
	// Generic subsequence walk.
	positions := make([]int, 0, len(query))
	score := 0
	hi := 0
	streak := 0
	for qi := 0; qi < len(query); qi++ {
		qc := query[qi]
		found := -1
		for ; hi < len(haystack); hi++ {
			if haystack[hi] == qc {
				found = hi
				break
			}
		}
		if found < 0 {
			return noMatch, nil
		}
		positions = append(positions, found)
		if qi > 0 && positions[qi] == positions[qi-1]+1 {
			streak++
			score += 5 + streak*2 // consecutive runs compound
		} else {
			streak = 0
		}
		if found == 0 || isBoundary(haystack[found-1]) {
			score += 15
		}
		hi++
	}
	// Shorter haystacks win on ties.
	score -= len(haystack) / 4
	return score, positions
}

func isBoundary(b byte) bool {
	if b == '/' || b == '.' || b == '_' || b == '-' || b == ' ' {
		return true
	}
	return !unicode.IsLetter(rune(b)) && !unicode.IsDigit(rune(b))
}

func indexRange(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func seqRange(start, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = start + i
	}
	return out
}
