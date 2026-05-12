package tui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

// Tree models the left pane. Each row is a flattened, depth-tagged view of an
// expandable hierarchy: dir → file → symbol. Children are loaded lazily.
type Tree struct {
	root  string
	nodes []*treeNode
	rows  []*treeNode // flattened, in display order
}

type treeNode struct {
	id       model.NodeID
	label    string
	depth    int
	parent   *treeNode
	children []*treeNode
	loaded   bool // children populated
	expanded bool
}

func NewTree(root string) (*Tree, error) {
	t := &Tree{root: root}
	rootNode := &treeNode{
		id:       model.NodeID{Kind: model.KindRepo, Path: ""},
		label:    filepath.Base(root),
		depth:    0,
		expanded: true,
	}
	t.nodes = []*treeNode{rootNode}
	if err := t.loadChildren(rootNode); err != nil {
		return nil, err
	}
	t.rebuildRows()
	return t, nil
}

func (t *Tree) Root() string { return t.root }

func (t *Tree) Rows() []TreeRow {
	out := make([]TreeRow, len(t.rows))
	for i, n := range t.rows {
		out[i] = TreeRow{
			ID:       n.id,
			Label:    n.label,
			Depth:    n.depth,
			Expanded: n.expanded,
			HasKids:  hasChildren(n),
		}
	}
	return out
}

type TreeRow struct {
	ID       model.NodeID
	Label    string
	Depth    int
	Expanded bool
	HasKids  bool
}

func hasChildren(n *treeNode) bool {
	if n.id.Kind == model.KindSymbol {
		return false
	}
	if !n.loaded {
		return n.id.Kind == model.KindDir || n.id.Kind == model.KindRepo || n.id.Kind == model.KindFile
	}
	return len(n.children) > 0
}

// Toggle expands or collapses the node at row index. Returns the new row index
// of that node (unchanged) and triggers a rebuild.
func (t *Tree) Toggle(ctx context.Context, row int) error {
	if row < 0 || row >= len(t.rows) {
		return nil
	}
	n := t.rows[row]
	if n.id.Kind == model.KindSymbol {
		return nil
	}
	if !n.loaded {
		if err := t.loadChildren(n); err != nil {
			return err
		}
	}
	n.expanded = !n.expanded
	t.rebuildRows()
	return nil
}

func (t *Tree) loadChildren(n *treeNode) error {
	n.loaded = true
	switch n.id.Kind {
	case model.KindRepo, model.KindDir:
		abs := filepath.Join(t.root, n.id.Path)
		entries, err := os.ReadDir(abs)
		if err != nil {
			return err
		}
		var dirs, files []os.DirEntry
		for _, e := range entries {
			if skipEntry(e.Name()) {
				continue
			}
			if e.IsDir() {
				dirs = append(dirs, e)
			} else if !skipFile(e.Name()) {
				files = append(files, e)
			}
		}
		sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
		for _, d := range dirs {
			child := &treeNode{
				id:     model.NodeID{Kind: model.KindDir, Path: filepath.Join(n.id.Path, d.Name())},
				label:  d.Name() + "/",
				depth:  n.depth + 1,
				parent: n,
			}
			n.children = append(n.children, child)
		}
		for _, f := range files {
			child := &treeNode{
				id:     model.NodeID{Kind: model.KindFile, Path: filepath.Join(n.id.Path, f.Name())},
				label:  f.Name(),
				depth:  n.depth + 1,
				parent: n,
			}
			n.children = append(n.children, child)
		}
	case model.KindFile:
		src, err := os.ReadFile(filepath.Join(t.root, n.id.Path))
		if err != nil {
			return err
		}
		pf, err := tsparse.Parse(context.Background(), n.id.Path, src)
		if err != nil {
			return err
		}
		for _, s := range pf.Symbols {
			label := s.Name
			if s.Kind == model.SymMethod && s.Receiver != "" {
				label = s.Receiver + "." + s.Name
			} else if s.Kind == model.SymType {
				label = s.Name + " " + s.Kind.String()
			}
			child := &treeNode{
				id:     model.NodeID{Kind: model.KindSymbol, Path: n.id.Path, Symbol: s.Name},
				label:  label,
				depth:  n.depth + 1,
				parent: n,
			}
			n.children = append(n.children, child)
		}
	}
	return nil
}

func skipEntry(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target":
		return true
	}
	return false
}

// skipFile filters obvious non-source files so the tree stays browsable.
// Anything else is shown; non-Go files just won't expand into symbols in v0.1.
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

func (t *Tree) rebuildRows() {
	t.rows = t.rows[:0]
	var walk func(n *treeNode)
	walk = func(n *treeNode) {
		t.rows = append(t.rows, n)
		if !n.expanded {
			return
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	for _, n := range t.nodes {
		walk(n)
	}
}

// FindRow returns the row index for an exact NodeID, or -1 if not visible.
func (t *Tree) FindRow(id model.NodeID) int {
	for i, n := range t.rows {
		if n.id == id {
			return i
		}
	}
	return -1
}
