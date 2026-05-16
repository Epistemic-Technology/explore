package tui

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mikethicke/explore/internal/gitsrc"
	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

// Tree models the left pane. Each row is a flattened, depth-tagged view of an
// expandable hierarchy: dir → file → symbol. Children are loaded lazily.
type Tree struct {
	root  string
	rev   gitsrc.Revision // filesystem by default; a commit in snapshot mode
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
	t := &Tree{root: root, rev: gitsrc.WorkingTree(root)}
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

// Revision reports the revision the tree is currently reading from.
func (t *Tree) Revision() gitsrc.Revision { return t.rev }

// SetRevision rebuilds the tree from scratch reading from rev (used to enter
// or leave a historical snapshot). All expansion/load state is discarded; the
// caller re-reveals whatever node should be focused afterward.
func (t *Tree) SetRevision(rev gitsrc.Revision) error {
	t.rev = rev
	rootNode := &treeNode{
		id:       model.NodeID{Kind: model.KindRepo, Path: ""},
		label:    filepath.Base(t.root),
		depth:    0,
		expanded: true,
	}
	t.nodes = []*treeNode{rootNode}
	if err := t.loadChildren(rootNode); err != nil {
		return err
	}
	t.rebuildRows()
	return nil
}

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
		entries, err := t.rev.ReadDir(n.id.Path)
		if err != nil {
			return err
		}
		var dirs, files []gitsrc.DirEntry
		for _, e := range entries {
			if skipEntry(e.Name) {
				continue
			}
			if e.IsDir {
				dirs = append(dirs, e)
			} else if !skipFile(e.Name) {
				files = append(files, e)
			}
		}
		sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
		for _, d := range dirs {
			child := &treeNode{
				id:     model.NodeID{Kind: model.KindDir, Path: filepath.Join(n.id.Path, d.Name)},
				label:  d.Name + "/",
				depth:  n.depth + 1,
				parent: n,
			}
			n.children = append(n.children, child)
		}
		for _, f := range files {
			child := &treeNode{
				id:     model.NodeID{Kind: model.KindFile, Path: filepath.Join(n.id.Path, f.Name)},
				label:  f.Name,
				depth:  n.depth + 1,
				parent: n,
			}
			n.children = append(n.children, child)
		}
	case model.KindFile:
		src, err := t.rev.ReadFile(n.id.Path)
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

// Reveal expands the ancestors of id so its row becomes visible. Returns the
// new row index. Loads children lazily along the way, including parsing the
// containing file for a symbol target. -1 if the target cannot be located
// (e.g. the file was deleted on disk since indexing).
func (t *Tree) Reveal(ctx context.Context, id model.NodeID) int {
	if id.Kind == model.KindRepo {
		t.rebuildRows()
		return t.FindRow(id)
	}
	// Build the chain of ancestor directories from the root down to id.
	parts := splitPath(id.Path)
	cur := t.nodes[0] // repo root
	cur.expanded = true
	for i := range parts {
		dirPath := strings.Join(parts[:i+1], string(filepath.Separator))
		// Reaching the final part: it's either a file (when id.Kind is File or
		// Symbol) or the dir itself (when id.Kind is Dir).
		isLast := i == len(parts)-1
		wantKind := model.KindDir
		if isLast && (id.Kind == model.KindFile || id.Kind == model.KindSymbol) {
			wantKind = model.KindFile
		}
		child := findChildByID(cur, model.NodeID{Kind: wantKind, Path: dirPath})
		if child == nil {
			if !cur.loaded {
				if err := t.loadChildren(cur); err != nil {
					return -1
				}
			}
			child = findChildByID(cur, model.NodeID{Kind: wantKind, Path: dirPath})
		}
		if child == nil {
			return -1
		}
		cur = child
		// Expand intermediate dirs; the final file node only needs expansion
		// when the target is a symbol inside it.
		if !isLast || id.Kind == model.KindSymbol {
			if !cur.loaded {
				if err := t.loadChildren(cur); err != nil {
					return -1
				}
			}
			cur.expanded = true
		}
	}
	t.rebuildRows()
	return t.FindRow(id)
}

func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	parts := strings.Split(p, string(filepath.Separator))
	out := parts[:0]
	for _, s := range parts {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func findChildByID(n *treeNode, id model.NodeID) *treeNode {
	for _, c := range n.children {
		if c.id == id {
			return c
		}
	}
	return nil
}
