package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/index"
	"github.com/mikethicke/explore/internal/llm"
	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/nav"
	"github.com/mikethicke/explore/internal/tsparse"
)

// loadDebounce is the quiet period after a navigation move before we actually
// dispatch the LLM call. Holding j/k blows through many transient cursor
// positions; without this, each one fires its own concurrent API call and
// Anthropic returns 529.
const loadDebounce = 200 * time.Millisecond

const (
	paneTree = 1
	paneExp  = 2
	paneSrc  = 3
)

const paneCount = 3

// Tabs within the explanation pane.
const (
	expTabPlain     = 0
	expTabNodeQA    = 1
	expTabSessionQA = 2
)

var paneTabs = map[int][]string{
	paneTree: {"Tree"},
	paneExp:  {"Explanation", "Q&A (node)", "Q&A (session)"},
	paneSrc:  {"Source"},
}

// qaTabMode maps an explanation-pane tab to its Q&A mode string, or returns
// "" if the tab is not a Q&A tab.
func qaTabMode(tab int) string {
	switch tab {
	case expTabNodeQA:
		return "node"
	case expTabSessionQA:
		return "session"
	}
	return ""
}

// Model is the root Bubble Tea model.
type Model struct {
	gen        *index.Generator
	tree       *Tree
	stack      *nav.Stack
	prefetcher *index.Prefetcher

	width, height int

	// Active pane focus. 1-4, see pane* constants.
	activePane int

	// Tree pane state.
	cursor int

	// Source pane state. currentFile is the relative path of the file the user
	// is "inside". sourceLine is the 1-based file line cursor. The active
	// explanation is derived from (currentFile, sourceLine).
	currentFile string
	sourceLine  int
	srcScroll   int // first visible line index (0-based)

	// Scroll position for the prose pane.
	proseScroll int

	// currentID is the NodeID for the currently focused thing. Recomputed any
	// time the tree cursor or source line changes.
	currentID model.NodeID
	loading   bool
	loadErr   error

	// loadGen tags each scheduled/in-flight load so stale results (from a
	// navigation that has since moved on) can be ignored. loadCancel cancels
	// the in-flight Provider.Explain call when navigation moves.
	loadGen    int
	loadCancel context.CancelFunc

	// Caches.
	expCache    map[model.NodeID]*model.Explanation
	sourceCache map[string]string               // file relpath → source text
	parsedCache map[string]*tsparse.ParsedFile  // file relpath → parsed file

	// Q&A. expTab selects which tab of the explanation pane is showing.
	qa     qaState
	expTab int

	// Vim-style count prefix (e.g. 5j) and pending "g" awaiting a second "g".
	count    int
	pendingG bool

	statusMsg string

	// initialCmd is the load tick scheduled at construction time; returned
	// from Init() so the first focused node gets explained on startup.
	initialCmd tea.Cmd
}

// qaState holds Q&A UI state. The active mode is derived from m.expTab
// (expTabNodeQA / expTabSessionQA), not stored here:
//   - node:    history is per focused node; context = source + parent summary,
//              attached once as the leading turn (stable focus).
//   - session: one running thread; each new question is wrapped with its own
//              [focus: …] tag + source so the model can resolve "this" / "that"
//              across nodes.
//
// stream* fields snapshot the mode/node at askCmd time so the assistant turn
// lands in the right thread even if the user switches tabs or navigates
// while tokens are streaming.
type qaState struct {
	input string

	nodeThreads map[model.NodeID][]llm.Turn
	sessionLog  []llm.Turn

	stream     string
	streamCh   <-chan llm.Token
	cancel     context.CancelFunc
	streamMode string
	streamFor  model.NodeID
}

func (q *qaState) threadFor(mode string, id model.NodeID) []llm.Turn {
	if mode == "session" {
		return q.sessionLog
	}
	return q.nodeThreads[id]
}

// streamVisible reports whether the in-flight (or just-completed) stream
// belongs to the thread currently on screen.
func (q *qaState) streamVisible(mode string, id model.NodeID) bool {
	if q.streamCh == nil && q.stream == "" {
		return false
	}
	if q.streamMode != mode {
		return false
	}
	if mode == "node" && q.streamFor != id {
		return false
	}
	return true
}

func NewModel(gen *index.Generator, tree *Tree, prefetcher *index.Prefetcher) Model {
	m := Model{
		gen:         gen,
		tree:        tree,
		prefetcher:  prefetcher,
		stack:       nav.New(),
		activePane:  paneTree,
		expCache:    make(map[model.NodeID]*model.Explanation),
		sourceCache: make(map[string]string),
		parsedCache: make(map[string]*tsparse.ParsedFile),
		qa: qaState{
			nodeThreads: make(map[model.NodeID][]llm.Turn),
		},
	}
	if rows := tree.Rows(); len(rows) > 0 {
		m.cursor = 0
		m.currentID = rows[0].ID
		m.stack.Push(m.currentID)
		// Kick off the first explanation + warm the neighborhood. The Cmd
		// returned by scheduleLoad must be threaded through Init() so the
		// debouncedLoadMsg actually fires.
		m.initialCmd = m.scheduleLoad(m.currentID)
		m.enqueuePrefetch(m.currentID)
	}
	return m
}

func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.initialCmd != nil {
		cmds = append(cmds, m.initialCmd)
	}
	if m.prefetcher != nil {
		cmds = append(cmds, listenPrefetch(m.prefetcher.Updates()))
	}
	return tea.Batch(cmds...)
}

// --- Messages ---

type explanationMsg struct {
	id  model.NodeID
	gen int
	exp *model.Explanation
	err error
}

// debouncedLoadMsg fires after loadDebounce has elapsed since a navigation
// move. If gen has been superseded by a newer move, it's dropped.
type debouncedLoadMsg struct {
	id  model.NodeID
	gen int
}

type qaTokenMsg struct {
	text string
	done bool
	err  error
}

type qaStreamStarted struct {
	ch <-chan llm.Token
}

type prefetchUpdateMsg struct {
	update index.Update
}

type prefetchClosedMsg struct{}

func listenPrefetch(ch <-chan index.Update) tea.Cmd {
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return prefetchClosedMsg{}
		}
		return prefetchUpdateMsg{update: u}
	}
}

// --- Cache helpers ---

func (m *Model) ensureFileLoaded(path string) {
	if path == "" {
		return
	}
	if _, ok := m.parsedCache[path]; ok {
		return
	}
	pf, src, err := m.gen.ParseFile(context.Background(), path)
	if err != nil {
		return
	}
	m.sourceCache[path] = string(src)
	m.parsedCache[path] = pf
}

// containingSymbol returns the innermost top-level symbol containing the
// given 1-based file line, if any.
func (m Model) containingSymbol(file string, line int) (model.Symbol, bool) {
	pf := m.parsedCache[file]
	if pf == nil {
		return model.Symbol{}, false
	}
	var best model.Symbol
	var found bool
	bestRange := -1
	for _, s := range pf.Symbols {
		if s.StartLine <= line && line <= s.EndLine {
			r := s.EndLine - s.StartLine
			if !found || r < bestRange {
				best = s
				bestRange = r
				found = true
			}
		}
	}
	return best, found
}

// deriveID picks the NodeID for the currently-focused thing. When we're inside
// a file (the typical case), the symbol containing sourceLine wins; otherwise
// we fall back to whatever the tree cursor is on.
func (m Model) deriveID() model.NodeID {
	if m.currentFile != "" {
		if s, ok := m.containingSymbol(m.currentFile, m.sourceLine); ok {
			return model.NodeID{Kind: model.KindSymbol, Path: m.currentFile, Symbol: s.Name}
		}
		return model.NodeID{Kind: model.KindFile, Path: m.currentFile}
	}
	rows := m.tree.Rows()
	if m.cursor < len(rows) {
		return rows[m.cursor].ID
	}
	return model.NodeID{}
}

// syncCurrent recomputes currentID from inputs, pushes onto the nav stack on
// change, and schedules explanation generation if needed.
func (m *Model) syncCurrent() tea.Cmd {
	newID := m.deriveID()
	if newID == m.currentID {
		return nil
	}
	m.currentID = newID
	m.stack.Push(newID)
	m.loadErr = nil
	cmd := m.scheduleLoad(newID)
	m.enqueuePrefetch(newID)
	return cmd
}

// enqueuePrefetch asks the prefetcher to warm explanations for nodes the user
// is likely to visit next: the visible tree-row neighborhood around the
// cursor, plus the parent file when focused on a symbol so going "up" is
// instant. De-duplication and concurrency capping live in the prefetcher
// itself, so we can call this freely on every focus change.
func (m *Model) enqueuePrefetch(currentID model.NodeID) {
	if m.prefetcher == nil {
		return
	}
	const window = 5
	rows := m.tree.Rows()
	if len(rows) == 0 {
		return
	}
	lo := m.cursor - window
	if lo < 0 {
		lo = 0
	}
	hi := m.cursor + window + 1
	if hi > len(rows) {
		hi = len(rows)
	}
	ids := make([]model.NodeID, 0, hi-lo+1)
	for i := lo; i < hi; i++ {
		id := rows[i].ID
		if id == currentID {
			continue
		}
		if _, cached := m.expCache[id]; cached {
			continue
		}
		ids = append(ids, id)
	}
	if currentID.Kind == model.KindSymbol && currentID.Path != "" {
		parent := model.NodeID{Kind: model.KindFile, Path: currentID.Path}
		if _, cached := m.expCache[parent]; !cached {
			ids = append(ids, parent)
		}
	}
	if len(ids) > 0 {
		m.prefetcher.Enqueue(ids...)
	}
}

// scheduleLoad cancels any in-flight load, bumps the generation, and — for
// loadable kinds with no cached result — returns a debounced tick that will
// actually dispatch the LLM call. Always bumping the generation invalidates
// any pending tick from a previous schedule.
func (m *Model) scheduleLoad(id model.NodeID) tea.Cmd {
	if m.loadCancel != nil {
		m.loadCancel()
		m.loadCancel = nil
	}
	m.loadGen++
	switch id.Kind {
	case model.KindFile, model.KindSymbol, model.KindDir, model.KindRepo:
		// loadable
	default:
		m.loading = false
		debug.Logf("scheduleLoad: skip kind=%v path=%q sym=%q (not loadable)", id.Kind, id.Path, id.Symbol)
		return nil
	}
	if _, ok := m.expCache[id]; ok {
		m.loading = false
		debug.Logf("scheduleLoad: cache hit kind=%v path=%q sym=%q", id.Kind, id.Path, id.Symbol)
		return nil
	}
	m.loading = true
	gen := m.loadGen
	debug.Logf("scheduleLoad: dispatching tick gen=%d kind=%v path=%q sym=%q", gen, id.Kind, id.Path, id.Symbol)
	return tea.Tick(loadDebounce, func(time.Time) tea.Msg {
		return debouncedLoadMsg{id: id, gen: gen}
	})
}

func (m Model) loadCmd(ctx context.Context, id model.NodeID, loadGen int) tea.Cmd {
	indexGen := m.gen
	parentSummary := ""
	if id.Kind == model.KindSymbol {
		if pe, ok := m.expCache[model.NodeID{Kind: model.KindFile, Path: id.Path}]; ok && pe != nil {
			parentSummary = pe.Prose
		}
	}
	return func() tea.Msg {
		debug.Logf("loadCmd: invoking provider kind=%v path=%q sym=%q gen=%d", id.Kind, id.Path, id.Symbol, loadGen)
		switch id.Kind {
		case model.KindFile:
			exp, err := indexGen.ExplainFile(ctx, id.Path)
			debug.Logf("loadCmd: ExplainFile done path=%q gen=%d err=%v proseLen=%d", id.Path, loadGen, err, proseLen(exp))
			return explanationMsg{id: id, gen: loadGen, exp: exp, err: err}
		case model.KindSymbol:
			exp, err := indexGen.ExplainSymbol(ctx, id.Path, id.Symbol, parentSummary)
			debug.Logf("loadCmd: ExplainSymbol done path=%q sym=%q gen=%d err=%v proseLen=%d", id.Path, id.Symbol, loadGen, err, proseLen(exp))
			return explanationMsg{id: id, gen: loadGen, exp: exp, err: err}
		case model.KindDir:
			exp, err := indexGen.ExplainDir(ctx, id.Path)
			debug.Logf("loadCmd: ExplainDir done path=%q gen=%d err=%v proseLen=%d", id.Path, loadGen, err, proseLen(exp))
			return explanationMsg{id: id, gen: loadGen, exp: exp, err: err}
		case model.KindRepo:
			exp, err := indexGen.ExplainRepo(ctx)
			debug.Logf("loadCmd: ExplainRepo done gen=%d err=%v proseLen=%d", loadGen, err, proseLen(exp))
			return explanationMsg{id: id, gen: loadGen, exp: exp, err: err}
		}
		return explanationMsg{id: id, gen: loadGen}
	}
}

func proseLen(e *model.Explanation) int {
	if e == nil {
		return 0
	}
	return len(e.Prose)
}

// --- Tree pane mutators ---

func (m *Model) applyTreeCursor() tea.Cmd {
	rows := m.tree.Rows()
	if m.cursor >= len(rows) {
		return nil
	}
	id := rows[m.cursor].ID
	switch id.Kind {
	case model.KindFile:
		m.setFile(id.Path, 1)
	case model.KindSymbol:
		m.ensureFileLoaded(id.Path)
		sym, found := m.lookupSymbol(id.Path, id.Symbol)
		line := 1
		if found {
			line = sym.StartLine
		}
		m.setFile(id.Path, line)
		if found {
			m.scrollSymbolIntoView(sym)
		}
	default:
		// Dir / repo: clear the source view; explanation pane shows dir info.
		m.currentFile = ""
	}
	return m.syncCurrent()
}

func (m Model) lookupSymbol(path, name string) (model.Symbol, bool) {
	pf := m.parsedCache[path]
	if pf == nil {
		return model.Symbol{}, false
	}
	for _, s := range pf.Symbols {
		if s.Name == name {
			return s, true
		}
	}
	return model.Symbol{}, false
}

// scrollSymbolIntoView adjusts srcScroll so the symbol's body is visible.
// If it fits in the source viewport, scroll the minimum needed; otherwise
// pin the symbol's first line to the top of the pane.
func (m *Model) scrollSymbolIntoView(s model.Symbol) {
	h := m.sourceViewportH()
	if h <= 0 {
		return
	}
	startIdx := s.StartLine - 1
	endIdx := s.EndLine - 1
	if endIdx-startIdx+1 > h {
		m.srcScroll = startIdx
	} else if startIdx < m.srcScroll {
		m.srcScroll = startIdx
	} else if endIdx >= m.srcScroll+h {
		m.srcScroll = endIdx - h + 1
	}
	if m.srcScroll < 0 {
		m.srcScroll = 0
	}
}

func (m *Model) setFile(path string, line int) {
	if m.currentFile != path {
		m.currentFile = path
		m.srcScroll = 0
	}
	m.ensureFileLoaded(path)
	if line < 1 {
		line = 1
	}
	m.sourceLine = line
}

func (m *Model) moveTreeCursor(delta int) tea.Cmd {
	rows := m.tree.Rows()
	if len(rows) == 0 {
		return nil
	}
	m.cursor = clamp(m.cursor+delta, 0, len(rows)-1)
	return m.applyTreeCursor()
}

// --- Source pane mutators ---

func (m *Model) moveSourceCursor(delta int) tea.Cmd {
	if m.currentFile == "" {
		return nil
	}
	src, ok := m.sourceCache[m.currentFile]
	if !ok {
		return nil
	}
	total := lineCount(src)
	m.sourceLine = clamp(m.sourceLine+delta, 1, total)
	return m.syncCurrent()
}

func (m *Model) jumpSource(line int) tea.Cmd {
	if m.currentFile == "" {
		return nil
	}
	src, ok := m.sourceCache[m.currentFile]
	if !ok {
		return nil
	}
	total := lineCount(src)
	m.sourceLine = clamp(line, 1, total)
	return m.syncCurrent()
}

// --- Update ---

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case explanationMsg:
		debug.Logf("explanationMsg: arrived gen=%d (cur=%d) kind=%v path=%q sym=%q err=%v stale=%v", msg.gen, m.loadGen, msg.id.Kind, msg.id.Path, msg.id.Symbol, msg.err, msg.gen != m.loadGen)
		// Successful results land in the cache even if the user navigated
		// away — the work is done, may as well keep it.
		if msg.err == nil && msg.exp != nil {
			m.expCache[msg.id] = msg.exp
		}
		// Only mutate UI state if this result corresponds to the current
		// navigation generation. Stale results (from a load whose nav has
		// since moved on) shouldn't clear a fresh "loading" or surface a
		// context-canceled error.
		if msg.gen == m.loadGen {
			if msg.err != nil {
				if !errors.Is(msg.err, context.Canceled) {
					m.loadErr = msg.err
				}
				m.loading = false
			} else if msg.id == m.currentID {
				m.loading = false
				m.loadErr = nil
			}
		}
		return m, nil

	case debouncedLoadMsg:
		// Drop the tick if navigation has moved on during the debounce
		// window — that's the whole point.
		if msg.gen != m.loadGen || msg.id != m.currentID {
			debug.Logf("debouncedLoadMsg: dropping stale tick gen=%d cur=%d match=%v", msg.gen, m.loadGen, msg.id == m.currentID)
			return m, nil
		}
		debug.Logf("debouncedLoadMsg: firing gen=%d kind=%v path=%q sym=%q", msg.gen, msg.id.Kind, msg.id.Path, msg.id.Symbol)
		ctx, cancel := context.WithCancel(context.Background())
		m.loadCancel = cancel
		return m, m.loadCmd(ctx, msg.id, msg.gen)

	case prefetchUpdateMsg:
		if msg.update.Err == nil && msg.update.Exp != nil {
			m.expCache[msg.update.ID] = msg.update.Exp
			debug.Logf("prefetchUpdateMsg: cached kind=%v path=%q sym=%q", msg.update.ID.Kind, msg.update.ID.Path, msg.update.ID.Symbol)
			// If this happens to be the node we're currently looking at
			// while waiting for a user-driven load, drop the spinner.
			if msg.update.ID == m.currentID && m.loading {
				m.loading = false
				if m.loadCancel != nil {
					m.loadCancel()
					m.loadCancel = nil
				}
			}
		} else if msg.update.Err != nil {
			debug.Logf("prefetchUpdateMsg: err kind=%v path=%q err=%v", msg.update.ID.Kind, msg.update.ID.Path, msg.update.Err)
		}
		if m.prefetcher == nil {
			return m, nil
		}
		return m, listenPrefetch(m.prefetcher.Updates())

	case prefetchClosedMsg:
		return m, nil

	case qaStreamStarted:
		debug.Logf("qaStreamStarted: ch=%v", msg.ch != nil)
		m.qa.streamCh = msg.ch
		m.qa.stream = ""
		return m, listenQA(msg.ch)

	case qaTokenMsg:
		if msg.err != nil {
			debug.Logf("qaTokenMsg: err=%v", msg.err)
			m.qa.stream += "\n[error: " + msg.err.Error() + "]"
			m.qa.streamCh = nil
			return m, nil
		}
		if msg.done {
			debug.Logf("qaTokenMsg: done streamLen=%d mode=%s", len(m.qa.stream), m.qa.streamMode)
			turn := llm.Turn{Role: "assistant", Content: m.qa.stream}
			if m.qa.streamMode == "session" {
				m.qa.sessionLog = append(m.qa.sessionLog, turn)
			} else {
				m.qa.nodeThreads[m.qa.streamFor] = append(m.qa.nodeThreads[m.qa.streamFor], turn)
			}
			m.qa.stream = ""
			m.qa.streamCh = nil
			return m, nil
		}
		m.qa.stream += msg.text
		return m, listenQA(m.qa.streamCh)

	case tea.KeyMsg:
		// These bindings must work even when the Q&A input is collecting
		// keystrokes — otherwise you'd be trapped in the input with no way
		// to switch panes or tabs. Trade-off: literal '[' / ']' can't be
		// typed into a Q&A question.
		switch msg.String() {
		case "tab", "shift+tab", "alt+1", "alt+2", "alt+3", "[", "]":
			return m.updateNav(msg)
		}
		if m.activePane == paneExp && qaTabMode(m.expTab) != "" {
			return m.updateQA(msg)
		}
		return m.updateNav(msg)
	}
	return m, nil
}

func (m Model) updateNav(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := msg.String()

	// Vim-style count prefix: bare digits accumulate before a movement key.
	if len(s) == 1 && s[0] >= '0' && s[0] <= '9' {
		m.count = m.count*10 + int(s[0]-'0')
		if m.count > 9999 {
			m.count = 9999
		}
		m.pendingG = false
		return m, nil
	}

	// Global keys (work from any pane). These are not movement, so they clear
	// any pending count/g state.
	switch s {
	case "q":
		return m, tea.Quit
	case "ctrl+c":
		return m, tea.Quit
	case "alt+1":
		m.activePane = paneTree
		m.resetVim()
		return m, nil
	case "alt+2":
		m.activePane = paneExp
		m.resetVim()
		return m, nil
	case "alt+3":
		m.activePane = paneSrc
		m.resetVim()
		return m, nil
	case "tab":
		m.activePane = (m.activePane % paneCount) + 1
		m.resetVim()
		return m, nil
	case "shift+tab":
		m.activePane = ((m.activePane - 2 + paneCount) % paneCount) + 1
		m.resetVim()
		return m, nil
	case "]":
		m.cycleTab(1)
		m.resetVim()
		return m, nil
	case "[":
		m.cycleTab(-1)
		m.resetVim()
		return m, nil
	case "b", "ctrl+o":
		m.resetVim()
		if id, ok := m.stack.Back(); ok {
			return m, m.focusID(id)
		}
		return m, nil
	case "ctrl+i":
		m.resetVim()
		if id, ok := m.stack.Forward(); ok {
			return m, m.focusID(id)
		}
		return m, nil
	case "?":
		m.activePane = paneExp
		// Default to node-scoped on first open; [ / ] cycles to session.
		if qaTabMode(m.expTab) == "" {
			m.expTab = expTabNodeQA
		}
		m.resetVim()
		return m, nil
	case "r":
		m.resetVim()
		delete(m.expCache, m.currentID)
		return m, m.scheduleLoad(m.currentID)
	}

	// Pane-specific keys.
	switch m.activePane {
	case paneTree:
		return m.updateTreePane(s)
	case paneSrc:
		return m.updateSourcePane(s)
	case paneExp:
		m.proseScroll = m.scrollPane(s, m.proseScroll)
		return m, nil
	}
	return m, nil
}

// takeCount returns the pending vim count (or def if none) and clears it.
func (m *Model) takeCount(def int) int {
	n := def
	if m.count > 0 {
		n = m.count
	}
	m.count = 0
	return n
}

func (m *Model) resetVim() {
	m.count = 0
	m.pendingG = false
}

// cycleTab rotates the active tab of the focused pane by delta. Panes with a
// single tab are a no-op.
func (m *Model) cycleTab(delta int) {
	tabs := paneTabs[m.activePane]
	if len(tabs) <= 1 {
		return
	}
	switch m.activePane {
	case paneExp:
		m.expTab = (m.expTab + delta + len(tabs)) % len(tabs)
	}
}

func (m Model) updateTreePane(s string) (tea.Model, tea.Cmd) {
	rows := m.tree.Rows()
	switch s {
	case "j", "down":
		m.pendingG = false
		return m, m.moveTreeCursor(m.takeCount(1))
	case "k", "up":
		m.pendingG = false
		return m, m.moveTreeCursor(-m.takeCount(1))
	case "g":
		if m.pendingG {
			// gg: go to row N (1-indexed) or top if no count.
			n := m.takeCount(1) - 1
			m.pendingG = false
			if n < 0 {
				n = 0
			}
			if n >= len(rows) {
				n = len(rows) - 1
			}
			m.cursor = n
			return m, m.applyTreeCursor()
		}
		m.pendingG = true
		return m, nil
	case "G":
		m.resetVim()
		if n := len(rows); n > 0 {
			m.cursor = n - 1
		}
		return m, m.applyTreeCursor()
	case " ", "l", "right":
		m.resetVim()
		if err := m.tree.Toggle(context.Background(), m.cursor); err != nil {
			m.statusMsg = err.Error()
		}
		return m, nil
	case "enter":
		m.resetVim()
		if m.currentFile != "" {
			m.activePane = paneSrc
		}
		return m, nil
	case "h", "left":
		m.resetVim()
		rows = m.tree.Rows()
		if m.cursor < len(rows) && rows[m.cursor].Expanded {
			_ = m.tree.Toggle(context.Background(), m.cursor)
			return m, nil
		}
		if m.cursor < len(rows) {
			d := rows[m.cursor].Depth
			for i := m.cursor - 1; i >= 0; i-- {
				if rows[i].Depth < d {
					m.cursor = i
					return m, m.applyTreeCursor()
				}
			}
		}
		return m, nil
	}
	m.resetVim()
	return m, nil
}

func (m Model) updateSourcePane(s string) (tea.Model, tea.Cmd) {
	switch s {
	case "j", "down":
		m.pendingG = false
		return m, m.moveSourceCursor(m.takeCount(1))
	case "k", "up":
		m.pendingG = false
		return m, m.moveSourceCursor(-m.takeCount(1))
	case "J", "ctrl+d", "pgdown":
		m.pendingG = false
		return m, m.moveSourceCursor(m.sourceViewportH() * m.takeCount(1))
	case "K", "ctrl+u", "pgup":
		m.pendingG = false
		return m, m.moveSourceCursor(-m.sourceViewportH() * m.takeCount(1))
	case "g":
		if m.pendingG {
			// gg: jump to line N or line 1 if no count.
			line := m.takeCount(1)
			m.pendingG = false
			return m, m.jumpSource(line)
		}
		m.pendingG = true
		return m, nil
	case "G":
		m.resetVim()
		if src, ok := m.sourceCache[m.currentFile]; ok {
			return m, m.jumpSource(lineCount(src))
		}
		return m, nil
	}
	m.resetVim()
	return m, nil
}

// scrollPane is shared by the prose and metadata panes: j/k for line, J/K for page.
func (m *Model) scrollPane(s string, current int) int {
	switch s {
	case "j", "down":
		m.pendingG = false
		return current + m.takeCount(1)
	case "k", "up":
		m.pendingG = false
		return max(0, current-m.takeCount(1))
	case "J", "ctrl+d", "pgdown":
		m.pendingG = false
		return current + 10*m.takeCount(1)
	case "K", "ctrl+u", "pgup":
		m.pendingG = false
		return max(0, current-10*m.takeCount(1))
	case "g":
		if m.pendingG {
			m.resetVim()
			return 0
		}
		m.pendingG = true
		return current
	}
	m.resetVim()
	return current
}

func (m Model) sourceViewportH() int {
	_, srcH := m.paneHeights()
	h := srcH - 2 // border + title
	if h < 5 {
		h = 5
	}
	return h
}

// paneHeights returns the content heights of the explanation and source
// boxes so that the stacked center column matches the visual height of the
// single-box side panes. Each lipgloss box adds 2 rows of border, so two
// stacked boxes need their content heights to sum to (sideContentH - 2) for
// the total visual heights to match.
func (m Model) paneHeights() (expH, srcH int) {
	contentH := m.height - 3
	if contentH < 12 {
		contentH = 12
	}
	total := contentH - 2
	if total < 10 {
		total = 10
	}
	expH = total / 3
	if expH < 6 {
		expH = 6
	}
	if expH > total-6 {
		expH = total - 6
	}
	srcH = total - expH
	return
}

// focusID jumps to a NodeID (typically from the nav stack) without pushing.
func (m *Model) focusID(id model.NodeID) tea.Cmd {
	m.currentID = id
	switch id.Kind {
	case model.KindFile:
		m.setFile(id.Path, 1)
	case model.KindSymbol:
		m.ensureFileLoaded(id.Path)
		sym, found := m.lookupSymbol(id.Path, id.Symbol)
		line := 1
		if found {
			line = sym.StartLine
		}
		m.setFile(id.Path, line)
		if found {
			m.scrollSymbolIntoView(sym)
		}
	default:
		m.currentFile = ""
	}
	if r := m.tree.FindRow(id); r >= 0 {
		m.cursor = r
	}
	return m.scheduleLoad(id)
}

// --- Q&A ---

func (m Model) updateQA(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.expTab = expTabPlain
		if m.qa.cancel != nil {
			m.qa.cancel()
		}
		return m, nil
	case tea.KeyCtrlC:
		if m.qa.cancel != nil {
			m.qa.cancel()
			m.qa.stream += "\n[interrupted]"
			return m, nil
		}
		return m, tea.Quit
	case tea.KeyEnter:
		q := strings.TrimSpace(m.qa.input)
		if q == "" || m.qa.streamCh != nil {
			return m, nil
		}
		m.qa.input = ""

		// Snapshot mode + node so the assistant turn lands in the right thread
		// even if the user switches tabs or navigates mid-stream.
		mode := qaTabMode(m.expTab)
		id := m.currentID
		m.qa.streamMode = mode
		m.qa.streamFor = id

		// In session mode we store the user turn with a focus tag so past
		// turns in conversation history disambiguate which node they were
		// about. Source is not stored here (would balloon the log); it is
		// re-attached only to the *current* turn at send time.
		if mode == "session" {
			m.qa.sessionLog = append(m.qa.sessionLog, llm.Turn{Role: "user", Content: sessionTag(id) + "\n" + q})
		} else {
			m.qa.nodeThreads[id] = append(m.qa.nodeThreads[id], llm.Turn{Role: "user", Content: q})
		}

		ctx, cancel := context.WithCancel(context.Background())
		m.qa.cancel = cancel
		return m, m.askCmd(ctx, id, mode, q)
	case tea.KeyBackspace:
		if len(m.qa.input) > 0 {
			m.qa.input = m.qa.input[:len(m.qa.input)-1]
		}
		return m, nil
	default:
		if msg.Type == tea.KeyRunes {
			m.qa.input += string(msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.qa.input += " "
		}
		return m, nil
	}
}

func (m Model) askCmd(ctx context.Context, id model.NodeID, mode string, q string) tea.Cmd {
	gen := m.gen

	// History excludes the user turn we just appended (the provider takes the
	// new Question as a separate field).
	var history []llm.Turn
	parentSummary := ""
	if mode == "session" {
		history = append([]llm.Turn{}, m.qa.sessionLog...)
		history = history[:len(history)-1]
	} else {
		history = append([]llm.Turn{}, m.qa.nodeThreads[id]...)
		history = history[:len(history)-1]
		// Node mode: include parent file's prose as priming context per DESIGN.
		if id.Kind == model.KindSymbol {
			if pe, ok := m.expCache[model.NodeID{Kind: model.KindFile, Path: id.Path}]; ok && pe != nil {
				parentSummary = pe.Prose
			}
		}
	}

	debug.Logf("askCmd: mode=%s kind=%v path=%q sym=%q histLen=%d qLen=%d", mode, id.Kind, id.Path, id.Symbol, len(history), len(q))

	return func() tea.Msg {
		var source string
		switch id.Kind {
		case model.KindFile:
			_, src, err := gen.ParseFile(ctx, id.Path)
			if err == nil {
				source = string(src)
			} else {
				debug.Logf("askCmd: ParseFile err=%v", err)
			}
		case model.KindSymbol:
			s, _ := gen.SymbolSource(ctx, id.Path, id.Symbol)
			source = s
		}

		var req llm.AskRequest
		if mode == "session" {
			// Self-contained question: focus tag + this turn's source +
			// question. AskRequest.Source is empty so claude.Ask skips its
			// stable leading-focus turn (would be wrong with per-turn focus).
			var b strings.Builder
			b.WriteString(sessionTag(id))
			b.WriteString("\n\nSource:\n```\n")
			b.WriteString(source)
			b.WriteString("\n```\n\n")
			b.WriteString(q)
			req = llm.AskRequest{
				RepoPrimer: gen.RepoPrimer,
				History:    history,
				Question:   b.String(),
			}
		} else {
			req = llm.AskRequest{
				FocusPath:     id.Path,
				FocusSymbol:   id.Symbol,
				Source:        source,
				ParentSummary: parentSummary,
				RepoPrimer:    gen.RepoPrimer,
				History:       history,
				Question:      q,
			}
		}

		debug.Logf("askCmd: calling Provider.Ask sourceLen=%d", len(source))
		ch, err := gen.Provider.Ask(ctx, req)
		if err != nil {
			debug.Logf("askCmd: Provider.Ask err=%v", err)
			return qaTokenMsg{err: err, done: true}
		}
		return qaStreamStarted{ch: ch}
	}
}

func sessionTag(id model.NodeID) string {
	var b strings.Builder
	b.WriteString("[focus: ")
	b.WriteString(id.Path)
	if id.Symbol != "" {
		b.WriteString("::")
		b.WriteString(id.Symbol)
	}
	b.WriteString("]")
	return b.String()
}

func listenQA(ch <-chan llm.Token) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		tok, ok := <-ch
		if !ok {
			return qaTokenMsg{done: true}
		}
		if tok.Err != nil {
			return qaTokenMsg{err: tok.Err}
		}
		return qaTokenMsg{text: tok.Text}
	}
}

// --- Rendering ---

var (
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	titleInactive = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("237")).Bold(true)
	curLineStyle  = lipgloss.NewStyle().Background(lipgloss.Color("236"))

	paneBorder       = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("241"))
	activePaneBorder = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("12"))
)

func (m Model) View() string {
	if m.width == 0 {
		return "loading..."
	}
	treeW := m.width * 28 / 100
	if treeW < 24 {
		treeW = 24
	}
	centerW := m.width - treeW - 4
	if centerW < 24 {
		centerW = 24
	}

	contentH := m.height - 3
	if contentH < 8 {
		contentH = 8
	}
	expH, srcH := m.paneHeights()

	treeBox := m.boxPane(paneTree, 0, m.renderTree(treeW-2, contentH-2), treeW, contentH)
	expBox := m.boxPane(paneExp, m.expTab, m.renderExp(centerW-2, expH-2), centerW, expH)
	srcBox := m.boxPane(paneSrc, 0, m.renderSource(centerW-2, srcH-2), centerW, srcH)
	center := lipgloss.JoinVertical(lipgloss.Left, expBox, srcBox)

	row := lipgloss.JoinHorizontal(lipgloss.Top, treeBox, center)
	return lipgloss.JoinVertical(lipgloss.Left, row, m.renderStatus())
}

func (m Model) boxPane(num, activeTab int, body string, w, h int) string {
	style := paneBorder
	if m.activePane == num {
		style = activePaneBorder
	}
	header := m.renderTabStrip(num, activeTab)
	inner := lipgloss.JoinVertical(lipgloss.Left, header, body)
	// MaxHeight enforces clipping so a too-tall body can't push the pane
	// past its allotted rows and scroll the title bar off the top.
	return style.Width(w).MaxWidth(w + 2).Height(h).MaxHeight(h + 2).Render(inner)
}

// renderTabStrip returns the title bar for a pane: `[N] Tab1 | Tab2`, with
// the active tab highlighted and dim separators between tabs.
func (m Model) renderTabStrip(num, activeTab int) string {
	tabs := paneTabs[num]
	isActive := m.activePane == num
	parts := []string{dimStyle.Render(fmt.Sprintf("[%d]", num))}
	for i, name := range tabs {
		if i > 0 {
			parts = append(parts, dimStyle.Render("│"))
		}
		switch {
		case i == activeTab && isActive:
			parts = append(parts, titleStyle.Render(name))
		case i == activeTab:
			parts = append(parts, titleInactive.Render(name))
		default:
			parts = append(parts, dimStyle.Render(name))
		}
	}
	return strings.Join(parts, " ")
}

func (m Model) renderTree(w, h int) string {
	rows := m.tree.Rows()
	if h < 1 || len(rows) == 0 {
		return ""
	}
	start, end := windowAround(m.cursor, h, len(rows))
	var b strings.Builder
	for i := start; i < end; i++ {
		r := rows[i]
		indent := strings.Repeat("  ", r.Depth)
		marker := "  "
		if r.HasKids {
			if r.Expanded {
				marker = "▾ "
			} else {
				marker = "▸ "
			}
		}
		line := truncate(indent+marker+r.Label, w)
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderExp(w, h int) string {
	if mode := qaTabMode(m.expTab); mode != "" {
		return m.renderQA(mode, w, h)
	}
	crumbs := m.stack.Breadcrumbs()
	var head strings.Builder
	for i, c := range crumbs {
		if i > 0 {
			head.WriteString(" → ")
		}
		head.WriteString(c.String())
	}
	headLine := dimStyle.Render(truncate(head.String(), w))

	var prose string
	switch {
	case m.loadErr != nil:
		prose = "Error: " + m.loadErr.Error()
	case m.loading:
		prose = dimStyle.Render("Generating explanation…")
	default:
		if exp, ok := m.expCache[m.currentID]; ok && exp != nil {
			prose = wrap(exp.Prose, w)
		} else {
			prose = dimStyle.Render("(no explanation yet)")
		}
	}

	avail := h - 2 // headLine + blank
	if avail < 1 {
		avail = 1
	}
	lines := strings.Split(prose, "\n")
	if m.proseScroll >= len(lines) {
		m.proseScroll = max(0, len(lines)-1)
	}
	lines = lines[m.proseScroll:]
	if len(lines) > avail {
		lines = lines[:avail]
	}
	body := strings.Join(lines, "\n")
	return headLine + "\n\n" + body
}

func (m Model) renderSource(w, h int) string {
	if m.currentFile == "" {
		return dimStyle.Render("(no file focused — pick one in [1] Tree)")
	}
	src, ok := m.sourceCache[m.currentFile]
	if !ok {
		return dimStyle.Render("(loading source…)")
	}
	if h < 1 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	total := len(lines)

	// Clamp scroll to keep sourceLine visible.
	scroll := m.srcScroll
	if m.sourceLine-1 < scroll {
		scroll = m.sourceLine - 1
	}
	if m.sourceLine-1 >= scroll+h {
		scroll = m.sourceLine - h
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll > total-1 {
		scroll = max(0, total-1)
	}
	end := scroll + h
	if end > total {
		end = total
	}

	gutterW := len(fmt.Sprintf("%d", total))
	var b strings.Builder
	for i := scroll; i < end; i++ {
		ln := strings.ReplaceAll(lines[i], "\t", "    ")
		num := fmt.Sprintf("%*d", gutterW, i+1)
		content := truncate(ln, w-gutterW-1)
		row := dimStyle.Render(num) + " " + content
		if i+1 == m.sourceLine {
			// Highlight whole row including the line number.
			row = curLineStyle.Render(fmt.Sprintf("%s %s", num, padRight(content, w-gutterW-1)))
		}
		b.WriteString(row + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderQA(mode string, w, h int) string {
	var b strings.Builder
	// Tab title already says node vs session, so the header just shows the
	// current focus (which the node-mode thread is keyed on, and which gets
	// stamped onto each session-mode turn).
	if m.currentID.Path != "" {
		b.WriteString(dimStyle.Render(sessionTag(m.currentID)) + "\n")
	}
	b.WriteString(dimStyle.Render("(Esc to close, Ctrl-C to interrupt)") + "\n\n")

	for _, t := range m.qa.threadFor(mode, m.currentID) {
		who := "you"
		if t.Role == "assistant" {
			who = "claude"
		}
		fmt.Fprintf(&b, "%s: %s\n\n", titleStyle.Render(who), wrap(t.Content, w))
	}
	if m.qa.streamVisible(mode, m.currentID) && m.qa.stream != "" {
		fmt.Fprintf(&b, "%s: %s\n\n", titleStyle.Render("claude"), wrap(m.qa.stream, w))
	}
	b.WriteString(dimStyle.Render("> ") + m.qa.input + "_")
	return b.String()
}

func (m Model) renderStatus() string {
	var keys string
	if m.activePane == paneExp && qaTabMode(m.expTab) != "" {
		keys = "[Enter] send  [Esc] back to Explanation  [Ctrl-C] interrupt  " + dimStyle.Render("[ [ / ] ] tab  [Tab] pane")
	} else {
		paneKeys := ""
		switch m.activePane {
		case paneTree:
			paneKeys = "[j/k] nav  [Space] expand  [Enter] view  [h] collapse  [gg/G] top/bot"
		case paneExp:
			paneKeys = "[j/k] scroll prose  [gg] top"
		case paneSrc:
			paneKeys = "[j/k] line  [J/K] page  [gg/G] top/bot  [Ngg] line N"
		}
		if m.count > 0 {
			paneKeys = fmt.Sprintf("(%d) ", m.count) + paneKeys
		} else if m.pendingG {
			paneKeys = "(g) " + paneKeys
		}
		keys = paneKeys + dimStyle.Render("  •  [Alt+1-3/Tab] pane  [ [ / ] ] tab  [b] back  [?] ask  [r] regen  [q] quit")
	}
	return keys + "  " + m.statusMsg
}

// --- Util ---

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func padRight(s string, w int) string {
	if w <= len(s) {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

func wrap(s string, w int) string {
	if w <= 0 {
		return s
	}
	var out strings.Builder
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		line := ""
		for _, w0 := range words {
			if line == "" {
				line = w0
				continue
			}
			if len(line)+1+len(w0) > w {
				out.WriteString(line + "\n")
				line = w0
			} else {
				line += " " + w0
			}
		}
		out.WriteString(line + "\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

func windowAround(cursor, height, total int) (int, int) {
	if height <= 0 || total == 0 {
		return 0, 0
	}
	if total <= height {
		return 0, total
	}
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	end := start + height
	if end > total {
		end = total
		start = end - height
	}
	return start, end
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func lineCount(s string) int {
	if s == "" {
		return 1
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	if n < 1 {
		n = 1
	}
	return n
}
