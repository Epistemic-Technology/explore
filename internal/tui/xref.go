package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/model"
)

// xrefLookupTimeout caps how long an `u`/`d` LSP roundtrip can hang before we
// give up and surface a clear status. Conservative — gopls on an indexed repo
// answers in well under a second; cold starts can take ~5s.
const xrefLookupTimeout = 10 * time.Second

// xrefUI holds picker state for `u` (callers) and `d` (callees). The same
// overlay handles both — kind drives the title and verb.
type xrefUI struct {
	open    bool
	kind    string // "callers" or "callees"
	entries []model.SymbolRef
	cursor  int

	// previewCache memoizes the rendered context snippet for each entry so
	// arrow-key navigation through a long list doesn't re-read the same files
	// on every keystroke. Keyed by "path:line".
	previewCache map[string]string
}

const (
	xrefOverlayW     = 100 // wide enough for list + preview side-by-side
	xrefOverlayH     = 18
	xrefListMaxW     = 42
	xrefPreviewLines = 5 // odd number so the target line sits in the middle
)

// openXref displays the picker. When entries is empty we set a status message
// explaining *why* (focus is wrong, explanation not loaded, or genuinely empty
// after LSP), since "no callers" by itself hides three different failures.
// When it has exactly one entry we jump directly — no picker for the trivial case.
func (m Model) openXref(kind string, entries []model.SymbolRef) (tea.Model, tea.Cmd) {
	if len(entries) == 0 {
		m.statusMsg = m.xrefEmptyReason(kind)
		return m, nil
	}
	if len(entries) == 1 {
		return m, m.jumpToSymbolRef(entries[0])
	}
	m.xref.open = true
	m.xref.kind = kind
	m.xref.entries = entries
	m.xref.cursor = 0
	m.xref.previewCache = make(map[string]string)
	return m, nil
}

// xrefEmptyReason returns a diagnostic for why no callers/callees are available
// at the current focus. Distinguishes:
//   - focus isn't a symbol (callers/callees only meaningful on functions/methods)
//   - explanation hasn't loaded yet (wait, then retry)
//   - explanation loaded but empty (LSP unavailable or genuinely empty)
func (m Model) xrefEmptyReason(kind string) string {
	if m.currentID.Kind != model.KindSymbol {
		return kind + ": focus a symbol first (currently on " + m.currentID.Kind.String() + ")"
	}
	if _, ok := m.expCache[m.currentID]; !ok {
		return kind + ": explanation not loaded yet — wait for it or press [r]"
	}
	return "no " + kind + " found (LSP may be unavailable for this language)"
}

// updateXref handles keys while the picker is open. Mirrors updateSearch's
// shape so the two overlays feel identical.
func (m Model) updateXref(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.xref.open = false
		return m, nil
	case "enter":
		if m.xref.cursor < len(m.xref.entries) {
			ref := m.xref.entries[m.xref.cursor]
			m.xref.open = false
			return m, m.jumpToSymbolRef(ref)
		}
		return m, nil
	case "up", "k", "ctrl+p":
		if m.xref.cursor > 0 {
			m.xref.cursor--
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if m.xref.cursor+1 < len(m.xref.entries) {
			m.xref.cursor++
		}
		return m, nil
	}
	return m, nil
}

// jumpToSymbolRef converts a SymbolRef (from u/d lookups) into a NodeID and
// navigates to it. Empty Name means the ref points at a whole file (e.g., a
// file-scope reference that doesn't sit inside any function).
//
// We deliberately do NOT change activePane — the user stays in whichever
// pane they invoked u/d from. We also prefer the SymbolRef's specific Line
// (call site for callers, definition line for callees) over the destination
// symbol's StartLine so the source-pane cursor lands at the point of
// interest, not just the top of the enclosing function.
func (m *Model) jumpToSymbolRef(ref model.SymbolRef) tea.Cmd {
	if ref.Path == "" {
		m.statusMsg = "xref: target has no path"
		return nil
	}
	id := model.NodeID{Kind: model.KindFile, Path: ref.Path}
	if ref.Name != "" {
		id = model.NodeID{Kind: model.KindSymbol, Path: ref.Path, Symbol: ref.Name}
	}
	row := m.tree.Reveal(context.Background(), id)
	if row < 0 {
		m.statusMsg = "xref: " + id.String() + " not found in tree"
		return nil
	}
	m.cursor = row
	m.stack.Push(id)
	cmd := m.focusID(id)
	if ref.Line > 0 && m.currentFile == ref.Path {
		m.sourceLine = ref.Line
		m.scrollLineIntoView(ref.Line)
	}
	return cmd
}

// scrollLineIntoView centers `line` (1-based) in the source viewport when
// it's not already visible. No-op when the line is on-screen or the viewport
// has no height yet (pre-render).
func (m *Model) scrollLineIntoView(line int) {
	h := m.sourceViewportH()
	if h <= 0 || line < 1 {
		return
	}
	idx := line - 1
	if idx >= m.srcScroll && idx < m.srcScroll+h {
		return
	}
	m.srcScroll = idx - h/2
	if m.srcScroll < 0 {
		m.srcScroll = 0
	}
}

// renderXref draws the picker overlay. When there's room, it's split into
// list (left) + a preview pane (right) showing context lines from the
// destination of the highlighted entry. Narrow terminals collapse to list-only.
func (m Model) renderXref(w, h int) string {
	header := titleStyle.Render(m.xref.kind+" ") + dimStyle.Render("(esc to close, ↑/↓ ⏎ to pick)")
	footer := dimStyle.Render("↑/↓ navigate · ⏎ open · esc cancel · " + shown_(len(m.xref.entries)) + " entries")

	// Reserve: header, blank, footer, and an extra line of breathing room.
	listH := h - 4
	if listH < 1 {
		listH = 1
	}

	listW, previewW := splitXrefWidth(w)
	listCol := m.renderXrefList(listW, listH)

	body := listCol
	if previewW > 0 {
		preview := m.renderXrefPreview(previewW, listH)
		body = lipgloss.JoinHorizontal(lipgloss.Top, listCol, preview)
	}

	full := header + "\n\n" + body + "\n" + footer
	return searchBoxStyle.Width(w).Height(h).Render(full)
}

// splitXrefWidth divides the overlay's interior between the list and the
// preview. Below ~70 columns the preview disappears entirely (preview empty
// is pointless on a narrow screen), and the list takes everything.
func splitXrefWidth(w int) (listW, previewW int) {
	const minPreview = 28
	if w < 70 {
		return w, 0
	}
	listW = xrefListMaxW
	if listW > w-minPreview-2 {
		listW = w - minPreview - 2
	}
	if listW < 24 {
		listW = 24
	}
	previewW = w - listW - 2 // -2 for a 1-col gap on each side
	if previewW < minPreview {
		previewW = 0
		listW = w
	}
	return listW, previewW
}

func (m Model) renderXrefList(w, h int) string {
	var b strings.Builder
	start, end := windowAround(m.xref.cursor, h, len(m.xref.entries))
	for i := start; i < end; i++ {
		r := m.xref.entries[i]
		line := truncate(formatXrefRef(r), w)
		if i == m.xref.cursor {
			line = selectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return lipgloss.NewStyle().Width(w).Render(strings.TrimRight(b.String(), "\n"))
}

// renderXrefPreview shows a short source slice around the highlighted entry's
// destination line. The target line gets a "▸" marker; other lines are dim.
// Missing files / out-of-range lines render as a placeholder instead of
// erroring.
func (m Model) renderXrefPreview(w, h int) string {
	if len(m.xref.entries) == 0 || m.xref.cursor >= len(m.xref.entries) {
		return ""
	}
	ref := m.xref.entries[m.xref.cursor]
	body := m.xrefPreviewBody(ref, h)
	return lipgloss.NewStyle().Width(w).MaxHeight(h).Render(body)
}

// xrefPreviewBody computes (and caches) the preview snippet for one ref.
func (m Model) xrefPreviewBody(ref model.SymbolRef, h int) string {
	key := ref.Path + ":" + itoaInt(ref.Line)
	if cached, ok := m.xref.previewCache[key]; ok {
		return cached
	}
	body := buildXrefPreview(m.gen.Root, ref, h)
	if m.xref.previewCache == nil {
		// Defensive: openXref initializes the map but a defaulted xrefUI
		// (e.g. test fixture) might not.
		return body
	}
	m.xref.previewCache[key] = body
	return body
}

// buildXrefPreview reads ref.Path relative to root and produces a multi-line
// snippet centered on ref.Line with the target line highlighted. Pure
// function; takes no lock; safe to call from any goroutine.
func buildXrefPreview(root string, ref model.SymbolRef, h int) string {
	if ref.Path == "" || ref.Line <= 0 {
		return dimStyle.Render("(no preview)")
	}
	absPath := filepath.Join(root, ref.Path)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return dimStyle.Render("(file unavailable: " + ref.Path + ")")
	}
	lines := strings.Split(string(data), "\n")
	if ref.Line > len(lines) {
		return dimStyle.Render("(line out of range)")
	}
	// Show xrefPreviewLines around the target, but never more than h rows.
	span := xrefPreviewLines
	if h > 0 && span > h {
		span = h
	}
	half := span / 2
	start := ref.Line - 1 - half // 0-based start
	if start < 0 {
		start = 0
	}
	end := start + span
	if end > len(lines) {
		end = len(lines)
		start = end - span
		if start < 0 {
			start = 0
		}
	}
	var b strings.Builder
	for i := start; i < end; i++ {
		marker := "  "
		styled := dimStyle.Render(lines[i])
		if i+1 == ref.Line {
			marker = titleStyle.Render("▸ ")
			styled = lines[i]
		}
		b.WriteString(marker + styled + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatXrefRef(r model.SymbolRef) string {
	label := r.Path
	if r.Name != "" {
		label = r.Path + "::" + r.Name
	}
	if r.Line > 0 {
		label += dimStyle.Render(":" + shown_(r.Line))
	}
	return label
}

// callersResultMsg carries the result of an on-demand callers lookup. Mirrors
// calleesOnLineMsg: SymbolFound distinguishes "symbol not in file" from "LSP
// returned no refs", which gives different diagnostics.
type callersResultMsg struct {
	refs        []model.SymbolRef
	symbolFound bool
	err         error
	id          model.NodeID
}

// openCallersOf kicks off an on-demand callers lookup for the currently
// focused symbol. Unlike the prior version (which read m.expCache to find
// pre-populated callers), this fires a tea.Cmd so `u` works immediately
// after `d`-jumping to a fresh symbol, before its explanation has loaded.
func (m Model) openCallersOf() (tea.Model, tea.Cmd) {
	if m.currentID.Kind != model.KindSymbol {
		m.statusMsg = "callers: focus a symbol first (currently on " + m.currentID.Kind.String() + ")"
		return m, nil
	}
	m.statusMsg = "looking up callers of " + m.currentID.Symbol + "…"
	id := m.currentID
	gen := m.gen
	debug.Logf("openCallersOf: dispatch path=%q sym=%q", id.Path, id.Symbol)
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), xrefLookupTimeout)
		defer cancel()
		start := time.Now()
		res, err := gen.CallersOf(ctx, id.Path, id.Symbol)
		debug.Logf("openCallersOf: complete path=%q sym=%q refs=%d err=%v after=%s", id.Path, id.Symbol, len(res.Refs), err, time.Since(start))
		return callersResultMsg{refs: res.Refs, symbolFound: res.SymbolFound, err: err, id: id}
	}
	return m, cmd
}

// handleCallersResultMsg routes the async result to the picker, with
// per-failure-mode diagnostics analogous to handleCalleesOnLineMsg.
func (m Model) handleCallersResultMsg(msg callersResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if errors.Is(msg.err, context.DeadlineExceeded) {
			m.statusMsg = "callers: timed out after " + xrefLookupTimeout.String() +
				" (LSP slow or unresponsive — try again once gopls/equivalent has indexed)"
			return m, nil
		}
		m.statusMsg = "callers: " + msg.err.Error()
		return m, nil
	}
	if !msg.symbolFound {
		m.statusMsg = "callers: " + msg.id.Symbol + " not found in " + msg.id.Path
		return m, nil
	}
	if len(msg.refs) == 0 {
		m.statusMsg = "no callers of " + msg.id.Symbol + " found (LSP may be unavailable or still indexing)"
		return m, nil
	}
	return m.openXref("callers", msg.refs)
}

// calleesOnLineMsg carries the result of a line-scoped callees lookup back to
// the TUI. err is set if the file couldn't be parsed. SitesFound is the
// number of call expressions tree-sitter saw on the line — used to
// distinguish "no calls here" from "LSP didn't resolve anything".
type calleesOnLineMsg struct {
	refs       []model.SymbolRef
	sitesFound int
	err        error
	line       int
}

// openCalleesOnLine kicks off the `d` lookup. Gated to the source pane: that's
// the only place a specific line is meaningful. Returns immediately and posts
// the result asynchronously via calleesOnLineMsg, so the LSP roundtrip doesn't
// block the UI.
func (m Model) openCalleesOnLine() (tea.Model, tea.Cmd) {
	if m.activePane != paneSrc {
		m.statusMsg = "callees: switch to the source pane (alt+3) to pick a line"
		return m, nil
	}
	if m.currentFile == "" {
		m.statusMsg = "callees: no file in source pane"
		return m, nil
	}
	if m.sourceLine <= 0 {
		m.statusMsg = "callees: no cursor line"
		return m, nil
	}
	m.statusMsg = "looking up callees on line " + itoaInt(m.sourceLine) + "…"
	file := m.currentFile
	line := m.sourceLine
	gen := m.gen
	debug.Logf("openCalleesOnLine: dispatch file=%q line=%d", file, line)
	cmd := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), xrefLookupTimeout)
		defer cancel()
		start := time.Now()
		res, err := gen.CallsOnLine(ctx, file, line)
		debug.Logf("openCalleesOnLine: complete file=%q line=%d sites=%d refs=%d err=%v after=%s", file, line, res.SitesFound, len(res.Refs), err, time.Since(start))
		return calleesOnLineMsg{refs: res.Refs, sitesFound: res.SitesFound, err: err, line: line}
	}
	return m, cmd
}

// handleCalleesOnLineMsg routes the async result to the picker. The message
// distinguishes three empty cases: file-parse error, no call expressions on
// the line, and call expressions present but unresolved by LSP — each gets
// its own status text so the user knows what to investigate.
func (m Model) handleCalleesOnLineMsg(msg calleesOnLineMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if errors.Is(msg.err, context.DeadlineExceeded) {
			m.statusMsg = "callees: timed out after " + xrefLookupTimeout.String() +
				" (LSP slow or unresponsive — try again once gopls/equivalent has indexed)"
			return m, nil
		}
		m.statusMsg = "callees: " + msg.err.Error()
		return m, nil
	}
	if len(msg.refs) == 0 {
		if msg.sitesFound > 0 {
			m.statusMsg = "found " + itoaInt(msg.sitesFound) + " call(s) on line " + itoaInt(msg.line) +
				" but LSP couldn't resolve them (is gopls/equivalent running and indexing done?)"
		} else {
			m.statusMsg = "no calls on line " + itoaInt(msg.line)
		}
		return m, nil
	}
	return m.openXref("callees", msg.refs)
}

// xrefOverlayDims sizes the picker against the terminal. Targets the wider
// xrefOverlayW so the side-by-side preview has room; falls back to list-only
// layout on narrow terminals (splitXrefWidth handles that).
func xrefOverlayDims(termW, termH int) (int, int) {
	w := xrefOverlayW
	if w > termW-4 {
		w = termW - 4
	}
	if w < 30 {
		w = 30
	}
	h := xrefOverlayH
	if h > termH-2 {
		h = termH - 2
	}
	if h < 8 {
		h = 8
	}
	return w, h
}
