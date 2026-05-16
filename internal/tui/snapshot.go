package tui

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/mikethicke/explore/internal/gitsrc"
	"github.com/mikethicke/explore/internal/highlight"
	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/tsparse"
)

// workingRef is the sentinel logical revision for "working tree vs HEAD" diff
// mode. It is not a valid git ref, so it never collides with a real branch or
// sha; the data-source code special-cases it to use git's working-tree diff
// commands while the tree still reads the live filesystem.
const workingRef = "\x00WORKING"

// currentRev is the logical revision driving diff mode ("" live, workingRef
// for working-vs-HEAD, else a commit sha).
func (m Model) currentRev() string { return m.rev }

// inSnapshot reports whether a diff overlay is active (a historical commit OR
// the working-vs-HEAD view). Used to gate all diff UI.
func (m Model) inSnapshot() bool { return m.rev != "" }

// atCommitSnapshot is true only for a real historical commit (not the
// working-tree diff). LSP xref and the live prefetcher stay valid in
// working-diff mode (the working tree is what they operate on), so those are
// suppressed on atCommitSnapshot, not inSnapshot.
func (m Model) atCommitSnapshot() bool { return m.rev != "" && m.rev != workingRef }

// isWorkingDiff reports the working-tree-vs-HEAD view.
func (m Model) isWorkingDiff() bool { return m.rev == workingRef }

// applyRevision repoints the tree and generator at ref ("" = working tree),
// rebuilds the tree, and clears the revision-sensitive in-memory caches. The
// BBolt cache is content-addressed so it stays valid across revisions; the
// in-memory expCache/sourceCache/parsedCache are keyed by NodeID/path with no
// revision component, so they must be dropped to avoid mixing eras.
func (m *Model) applyRevision(ref string) error {
	if ref == m.currentRev() {
		return nil
	}
	var rev gitsrc.Revision
	if ref == "" || ref == workingRef {
		// Live filesystem for both normal and working-diff mode — the diff is
		// computed against HEAD but the files shown are the actual working tree.
		rev = gitsrc.WorkingTree(m.tree.Root())
		m.gen = m.baseGen
	} else {
		rev = m.repo.AtCommit(ref)
		m.gen = m.baseGen.AtRevision(rev)
	}
	if err := m.tree.SetRevision(rev); err != nil {
		return err
	}
	m.rev = ref
	m.expCache = make(map[model.NodeID]*model.Explanation)
	m.sourceCache = make(map[string]string)
	m.parsedCache = make(map[string]*tsparse.ParsedFile)
	m.currentFile = ""
	m.sourceLine = 0
	m.srcScroll = 0
	m.proseScroll = 0
	m.selecting = false
	m.cursor = 0
	m.snapshotChanges = nil
	m.snapshotDiff = map[string]string{}
	m.snapshotDiffErr = map[string]error{}
	m.loadingDiff = map[string]bool{}
	m.snapshotNodeExp = map[string]*model.Explanation{}
	m.snapshotNodeExpErr = map[string]error{}
	m.loadingNodeExp = map[string]bool{}
	m.snapshotSymChanges = map[string]map[string]string{}
	m.loadingSym = map[string]bool{}
	return nil
}

type symChangesMsg struct {
	path string
	syms map[string]string // symbol name → "A" | "M"
}

// ensureSymChanges computes per-symbol change status for a changed file by
// parsing it at the snapshot commit and at the commit's first parent, matching
// symbols by (receiver, name) and comparing their source slices. Best-effort:
// any read/parse failure yields no coloring (the map stays absent). Added
// files short-circuit — every symbol is new.
func (m *Model) ensureSymChanges(path string) tea.Cmd {
	if path == "" || m.repo == nil || !m.inSnapshot() {
		return nil
	}
	st := m.changeStatus(path)
	if st == "" {
		return nil
	}
	if _, done := m.snapshotSymChanges[path]; done {
		return nil
	}
	if m.loadingSym[path] {
		return nil
	}
	m.loadingSym[path] = true
	repo, sha := m.repo, m.currentRev()
	added := st != "" && st[0] == 'A'
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		// "cur" is the file at the commit, or the working copy in working
		// mode; "par" is the baseline it's diffed against (sha^ or HEAD).
		curRev := repo.AtCommit(sha)
		parRef := sha + "^"
		if sha == workingRef {
			curRev = repo.WorkingTree()
			parRef = "HEAD"
		}
		curSrc, err := curRev.ReadFile(path)
		if err != nil {
			return symChangesMsg{path: path, syms: map[string]string{}}
		}
		curPF, err := tsparse.Parse(ctx, path, curSrc)
		if err != nil || curPF == nil {
			return symChangesMsg{path: path, syms: map[string]string{}}
		}
		out := map[string]string{}
		if added {
			for _, s := range curPF.Symbols {
				out[s.Name] = "A"
			}
			return symChangesMsg{path: path, syms: out}
		}
		parSrc, perr := repo.AtCommit(parRef).ReadFile(path)
		if perr != nil {
			// No parent revision of this file (e.g. added via rename) → all new.
			for _, s := range curPF.Symbols {
				out[s.Name] = "A"
			}
			return symChangesMsg{path: path, syms: out}
		}
		parPF, perr := tsparse.Parse(ctx, path, parSrc)
		if perr != nil || parPF == nil {
			return symChangesMsg{path: path, syms: map[string]string{}}
		}
		type key struct{ recv, name string }
		par := map[key][]byte{}
		for _, s := range parPF.Symbols {
			par[key{s.Receiver, s.Name}] = tsparse.SymbolSource(parSrc, s)
		}
		for _, s := range curPF.Symbols {
			prev, ok := par[key{s.Receiver, s.Name}]
			switch {
			case !ok:
				out[s.Name] = "A"
			case !bytes.Equal(prev, tsparse.SymbolSource(curSrc, s)):
				out[s.Name] = "M"
			}
		}
		return symChangesMsg{path: path, syms: out}
	}
}

func (m Model) handleSymChangesMsg(msg symChangesMsg) (tea.Model, tea.Cmd) {
	delete(m.loadingSym, msg.path)
	m.snapshotSymChanges[msg.path] = msg.syms
	return m, nil
}

// snapshotCommitSubject is the subject line of the active snapshot's commit,
// used as the intent hint for change explanations.
func (m Model) snapshotCommitSubject() string {
	sha := m.currentRev()
	for _, c := range m.history.commits {
		if c.SHA == sha {
			return c.Subject
		}
	}
	return m.snapshotDesc
}

type nodeChangeExplainMsg struct {
	path string
	exp  *model.Explanation
	err  error
}

// ensureNodeChangeExplain requests a change-focused explanation for the
// focused changed file from its diff, unless cached or in flight or the diff
// hasn't loaded yet. The focused symbol (if any) is passed as a hint.
func (m *Model) ensureNodeChangeExplain(path string) tea.Cmd {
	if path == "" || m.gen == nil || !m.inSnapshot() {
		return nil
	}
	if m.changeStatus(path) == "" {
		return nil
	}
	diff, ok := m.snapshotDiff[path]
	if !ok || strings.TrimSpace(diff) == "" {
		return nil
	}
	if _, done := m.snapshotNodeExp[path]; done {
		return nil
	}
	if m.loadingNodeExp[path] {
		return nil
	}
	m.loadingNodeExp[path] = true
	gen := m.gen
	subject := m.snapshotCommitSubject()
	sym := ""
	if m.currentID.Kind == model.KindSymbol && m.currentID.Path == path {
		sym = m.currentID.Symbol
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		exp, err := gen.ExplainChange(ctx, path, sym, subject, diff)
		return nodeChangeExplainMsg{path: path, exp: exp, err: err}
	}
}

func (m Model) handleNodeChangeExplainMsg(msg nodeChangeExplainMsg) (tea.Model, tea.Cmd) {
	delete(m.loadingNodeExp, msg.path)
	if msg.err != nil {
		m.snapshotNodeExpErr[msg.path] = msg.err
		return m, nil
	}
	m.snapshotNodeExp[msg.path] = msg.exp
	return m, nil
}

type commitChangesMsg struct {
	sha     string
	changes map[string]string
	err     error
}

type fileDiffMsg struct {
	sha, path, text string
	err             error
}

// loadChangesCmd fetches the name-status map (path → A/M/D/R…) for sha vs. its
// first parent, used to color the snapshot tree.
func (m *Model) loadChangesCmd(sha string) tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		var fcs []gitsrc.FileChange
		var err error
		if sha == workingRef {
			fcs, err = repo.WorkingChanges(ctx)
		} else {
			var d gitsrc.CommitDetail
			d, err = repo.CommitMeta(ctx, sha)
			fcs = d.Changes
		}
		if err != nil {
			return commitChangesMsg{sha: sha, err: err}
		}
		changes := map[string]string{}
		for _, ch := range fcs {
			changes[ch.Path] = ch.Status
		}
		return commitChangesMsg{sha: sha, changes: changes}
	}
}

func (m Model) handleCommitChangesMsg(msg commitChangesMsg) (tea.Model, tea.Cmd) {
	// Apply only if still in the snapshot this load was for.
	if msg.err != nil || msg.sha != m.currentRev() {
		return m, nil
	}
	m.snapshotChanges = msg.changes
	return m, nil
}

// changeStatus returns the name-status of a repo-relative file path in the
// active snapshot, or "" if unchanged / not in a snapshot.
func (m Model) changeStatus(path string) string {
	if m.snapshotChanges == nil {
		return ""
	}
	return m.snapshotChanges[filepath.ToSlash(path)]
}

// treeRowStyle returns the diff color for a tree row in snapshot mode and
// whether one applies. Files use their own status; directories aggregate
// descendants (all-added → green, any other change → yellow); the repo root
// and unchanged nodes get no color.
func (m Model) treeRowStyle(id model.NodeID) (lipgloss.Style, bool) {
	if m.snapshotChanges == nil {
		return lipgloss.Style{}, false
	}
	switch id.Kind {
	case model.KindFile:
		if st := m.changeStatus(id.Path); st != "" {
			return statusStyle(st), true
		}
	case model.KindSymbol:
		if syms := m.snapshotSymChanges[id.Path]; syms != nil {
			if st := syms[id.Symbol]; st != "" {
				return statusStyle(st), true
			}
		}
	case model.KindDir:
		prefix := filepath.ToSlash(id.Path) + "/"
		sawAdd, sawOther := false, false
		for p, st := range m.snapshotChanges {
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			if st != "" && st[0] == 'A' {
				sawAdd = true
			} else {
				sawOther = true
			}
		}
		switch {
		case sawOther:
			return modifyStyle, true
		case sawAdd:
			return addStyle, true
		}
	}
	return lipgloss.Style{}, false
}

// maybeLoadFileDiff returns a command to fetch the unified diff for the
// focused file when in a snapshot and that file was changed by the commit.
// Cheap to call on every focus change — cached per path, deduped in flight.
func (m *Model) maybeLoadFileDiff() tea.Cmd {
	if !m.inSnapshot() || m.currentFile == "" {
		return nil
	}
	path := m.currentFile
	if m.changeStatus(path) == "" {
		return nil // unchanged → source pane shows plain historical source
	}
	if _, ok := m.snapshotDiff[path]; ok {
		// Diff already in hand — ensure its explanation + symbol coloring.
		return tea.Batch(m.ensureNodeChangeExplain(path), m.ensureSymChanges(path))
	}
	if m.loadingDiff[path] {
		return nil
	}
	m.loadingDiff[path] = true
	repo, sha := m.repo, m.currentRev()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		var b []byte
		var err error
		if sha == workingRef {
			b, err = repo.WorkingFileDiff(ctx, path)
			if err == nil && strings.TrimSpace(string(b)) == "" {
				// Untracked file: git diff is empty. Synthesize an all-added
				// patch from the working copy so it renders like a new file.
				if src, rerr := repo.WorkingTree().ReadFile(path); rerr == nil {
					b = []byte(synthAllAdded(src))
				}
			}
		} else {
			b, err = repo.FileDiff(ctx, sha, path)
		}
		if err != nil {
			return fileDiffMsg{sha: sha, path: path, err: err}
		}
		return fileDiffMsg{sha: sha, path: path, text: string(b)}
	}
}

// synthAllAdded builds a minimal unified-diff body that marks every line of
// src as added, so parseInlineDiff renders an untracked file as all-green.
func synthAllAdded(src []byte) string {
	lines := strings.Split(strings.TrimRight(string(src), "\n"), "\n")
	var b strings.Builder
	for _, ln := range lines {
		b.WriteString("+")
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return b.String()
}

func (m Model) handleFileDiffMsg(msg fileDiffMsg) (tea.Model, tea.Cmd) {
	delete(m.loadingDiff, msg.path)
	if msg.sha != m.currentRev() {
		return m, nil // snapshot changed under us; drop
	}
	if msg.err != nil {
		m.snapshotDiffErr[msg.path] = msg.err
		return m, nil
	}
	m.snapshotDiff[msg.path] = msg.text
	return m, tea.Batch(m.ensureNodeChangeExplain(msg.path), m.ensureSymChanges(msg.path))
}

// focusStackTarget focuses id from a back/forward move, first switching
// revisions if the popped frame was captured in a different one (the
// live↔historical boundary). No new frame is pushed — we're replaying history.
func (m *Model) focusStackTarget(id model.NodeID) tea.Cmd {
	if want := m.stack.CurRev(); want != m.currentRev() {
		if err := m.applyRevision(want); err != nil {
			m.statusMsg = "revision switch failed: " + err.Error()
			return nil
		}
		m.snapshotDesc = ""
		var extra tea.Cmd
		if want != "" {
			m.snapshotDesc = shortSHA(want)
			extra = m.loadChangesCmd(want)
		}
		if m.tree.FindRow(id) < 0 {
			id = model.NodeID{Kind: model.KindRepo, Path: ""}
		}
		return tea.Batch(m.focusID(id), extra)
	}
	return m.focusID(id)
}

// enterSnapshot switches the whole app to the selected commit's tree: the
// tree, source, and explanations all reflect the repo as it existed then
// (read from git objects). Esc / b returns to the live working tree.
func (m Model) enterSnapshot() (tea.Model, tea.Cmd) {
	c, ok := m.selectedCommit()
	if !ok {
		return m, nil
	}
	if err := m.applyRevision(c.SHA); err != nil {
		m.statusMsg = "snapshot: " + err.Error()
		return m, nil
	}
	m.treeTab = treeTabTree
	m.activePane = paneTree
	root := model.NodeID{Kind: model.KindRepo, Path: ""}
	m.currentID = root
	m.stack.Push(root, c.SHA)
	if c.SHA == workingRef {
		m.snapshotDesc = "working tree"
		m.statusMsg = "working changes vs HEAD — Esc/b returns to normal view"
	} else {
		m.snapshotDesc = c.ShortSHA + " " + c.Subject
		m.statusMsg = "snapshot @ " + c.ShortSHA + " — Esc/b returns to working tree"
	}
	return m, tea.Batch(m.scheduleLoad(root), m.loadChangesCmd(c.SHA))
}

// exitSnapshot returns to the live working tree, keeping the focused node when
// it still exists at HEAD (otherwise falling back to the repo root).
func (m Model) exitSnapshot() (tea.Model, tea.Cmd) {
	if !m.inSnapshot() {
		return m, nil
	}
	if err := m.applyRevision(""); err != nil {
		m.statusMsg = "exit snapshot: " + err.Error()
		return m, nil
	}
	m.snapshotDesc = ""
	id := m.currentID
	if m.tree.FindRow(id) < 0 {
		id = model.NodeID{Kind: model.KindRepo, Path: ""}
	}
	m.currentID = id
	m.stack.Push(id, "")
	m.statusMsg = "returned to working tree"
	return m, m.scheduleLoad(id)
}

// renderNodeChangeExplain draws the change-focused explanation for the
// focused changed file in the explanation pane (snapshot mode).
func (m Model) renderNodeChangeExplain(w, h int) string {
	path := m.currentFile
	head := dimStyle.Render(truncate("⌥ changes · "+path, w))
	var body string
	switch {
	case m.snapshotNodeExpErr[path] != nil:
		body = warnStyle.Render("explain error: ") + m.snapshotNodeExpErr[path].Error()
	case m.snapshotNodeExp[path] != nil:
		body = wrap(m.snapshotNodeExp[path].Prose, w)
	default:
		body = dimStyle.Render("Explaining what changed in this file…")
	}
	avail := h - 2
	if avail < 1 {
		avail = 1
	}
	lines := strings.Split(body, "\n")
	if m.proseScroll < len(lines) {
		lines = lines[m.proseScroll:]
	}
	if len(lines) > avail {
		lines = lines[:avail]
	}
	return head + "\n\n" + strings.Join(lines, "\n")
}

// inDiffView reports whether the source pane should show a unified diff (in a
// snapshot, on the Tree tab, focused on a file the commit changed).
func (m Model) inDiffView() bool {
	return m.inSnapshot() && m.treeTab == treeTabTree &&
		m.currentFile != "" && m.changeStatus(m.currentFile) != ""
}

// inlineRow is one display line of the full-file inline diff. kind is ' '
// (context), '+' (added) or '-' (removed). newLine is the 1-based line number
// in the file at the commit (0 for removed lines, which have no new-file
// position). text is the line content with the diff prefix stripped.
type inlineRow struct {
	kind    byte
	newLine int
	text    string
}

// parseInlineDiff turns a `git show -U<huge>` patch into ordered display rows:
// the whole post-image file as context, with added/removed lines interleaved
// in place. Diff/hunk headers and no-newline markers are dropped.
func parseInlineDiff(diff string) []inlineRow {
	var rows []inlineRow
	cur := 0 // last assigned new-file line number
	for _, ln := range strings.Split(diff, "\n") {
		switch {
		case ln == "":
			continue
		case strings.HasPrefix(ln, "@@"):
			// "@@ -a,b +c,d @@" — reset the new-file counter to c-1.
			if i := strings.IndexByte(ln, '+'); i >= 0 {
				j := i + 1
				n := 0
				for j < len(ln) && ln[j] >= '0' && ln[j] <= '9' {
					n = n*10 + int(ln[j]-'0')
					j++
				}
				cur = n - 1
			}
			continue
		case strings.HasPrefix(ln, "diff --git"), strings.HasPrefix(ln, "index "),
			strings.HasPrefix(ln, "--- "), strings.HasPrefix(ln, "+++ "),
			strings.HasPrefix(ln, "new file"), strings.HasPrefix(ln, "deleted file"),
			strings.HasPrefix(ln, "old mode"), strings.HasPrefix(ln, "new mode"),
			strings.HasPrefix(ln, "rename "), strings.HasPrefix(ln, "copy "),
			strings.HasPrefix(ln, "similarity "), strings.HasPrefix(ln, "dissimilarity "),
			strings.HasPrefix(ln, "Binary "), strings.HasPrefix(ln, `\`):
			continue
		}
		switch ln[0] {
		case '+':
			cur++
			rows = append(rows, inlineRow{kind: '+', newLine: cur, text: ln[1:]})
		case '-':
			rows = append(rows, inlineRow{kind: '-', text: ln[1:]})
		default: // ' ' context
			cur++
			rows = append(rows, inlineRow{kind: ' ', newLine: cur, text: ln[1:]})
		}
	}
	return rows
}

// renderDiffView shows the whole file at the commit with line numbers and
// syntax-highlighted context, added lines tinted green and removed lines (in
// red) interleaved where they were. Scrolls via m.srcScroll.
func (m Model) renderDiffView(w, h int) string {
	path := m.currentFile
	if err := m.snapshotDiffErr[path]; err != nil {
		return warnStyle.Render("diff error: ") + truncate(err.Error(), w)
	}
	diff, ok := m.snapshotDiff[path]
	if !ok {
		return dimStyle.Render("loading diff…")
	}
	if strings.TrimSpace(diff) == "" {
		return dimStyle.Render("(no textual diff — binary or mode-only change)")
	}
	if h < 1 {
		return ""
	}
	rows := parseInlineDiff(diff)
	if len(rows) == 0 {
		return dimStyle.Render("(no textual diff)")
	}

	// Post-image (file at the commit) for accurate bytes + syntax spans on
	// context lines. Falls back to the diff's own text if it isn't loaded.
	var postLines []string
	var postStarts []int
	var spans []highlight.Span
	if post := m.sourceCache[path]; post != "" {
		postLines, postStarts = splitLinesWithOffsets([]byte(post))
		if m.highlighter != nil {
			spans = m.highlighter.Highlight(context.Background(), []byte(post),
				tsparse.DetectLanguage(path))
		}
	}

	maxNew := 0
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].newLine > maxNew {
			maxNew = rows[i].newLine
		}
	}
	gutterW := len(fmt.Sprintf("%d", maxNew))
	if gutterW < 1 {
		gutterW = 1
	}
	contentW := w - gutterW - 1
	if contentW < 1 {
		contentW = 1
	}

	scroll := clamp(m.srcScroll, 0, max(0, len(rows)-1))
	end := min(scroll+h, len(rows))
	var b strings.Builder
	for _, r := range rows[scroll:end] {
		switch r.kind {
		case ' ':
			num := dimStyle.Render(fmt.Sprintf("%*d", gutterW, r.newLine))
			var content string
			if idx := r.newLine - 1; idx >= 0 && idx < len(postLines) {
				ls := postStarts[idx]
				styled := renderLineSpans(postLines[idx], ls, ls+len(postLines[idx]), spans, 0)
				content = ansi.Truncate(strings.ReplaceAll(styled, "\t", "    "), contentW, "")
			} else {
				content = truncate(strings.ReplaceAll(r.text, "\t", "    "), contentW)
			}
			b.WriteString(num + " " + content + "\n")
		case '+':
			num := addStyle.Render(fmt.Sprintf("%*d", gutterW, r.newLine))
			txt := truncate(strings.ReplaceAll(r.text, "\t", "    "), contentW)
			b.WriteString(num + " " + diffAddBg.Render(padRight(txt, contentW)) + "\n")
		case '-':
			gut := deleteStyle.Render(fmt.Sprintf("%*s", gutterW, "-"))
			txt := truncate(strings.ReplaceAll(r.text, "\t", "    "), contentW)
			b.WriteString(gut + " " + diffDelBg.Render(padRight(txt, contentW)) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
