package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mikethicke/explore/internal/debug"
	"github.com/mikethicke/explore/internal/model"
	"github.com/mikethicke/explore/internal/search"
)

// searchUI holds the `/` overlay's state. open inhibits all other key
// bindings so typing into the query box isn't interpreted as navigation.
// index is built lazily on first open and reused thereafter — repos rarely
// change shape mid-session, and a stale index is acceptable for v0.3.
type searchUI struct {
	open     bool
	query    string
	indexing bool
	indexErr error
	index    *search.Index
	results  []search.Result
	cursor   int
}

// searchIndexedMsg is delivered once BuildIndex finishes. err is non-nil if
// the walk failed; the overlay falls back to showing the error inline.
type searchIndexedMsg struct {
	index *search.Index
	err   error
}

const (
	searchResultLimit = 30
	searchOverlayW    = 70
	searchOverlayH    = 18
)

// openSearch opens the overlay, kicking off an index build if needed.
func (m *Model) openSearch() tea.Cmd {
	m.search.open = true
	m.search.cursor = 0
	if m.search.index == nil && !m.search.indexing {
		m.search.indexing = true
		m.search.indexErr = nil
		root := m.tree.Root()
		return func() tea.Msg {
			debug.Logf("openSearch: building index root=%q", root)
			idx, err := search.BuildIndex(context.Background(), root)
			return searchIndexedMsg{index: idx, err: err}
		}
	}
	m.refreshSearch()
	return nil
}

// refreshSearch re-runs the matcher against the current query. Called on
// every keystroke and after the index lands.
func (m *Model) refreshSearch() {
	if m.search.index == nil {
		m.search.results = nil
		return
	}
	m.search.results = m.search.index.Search(m.search.query, searchResultLimit)
	if m.search.cursor >= len(m.search.results) {
		m.search.cursor = 0
	}
}

// updateSearch handles keystrokes while the overlay is open. It swallows
// everything except the navigation/closing keys, so typing letters into the
// query box doesn't fire model.go's vim bindings.
func (m Model) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.search.open = false
		return m, nil
	case "enter":
		if m.search.cursor < len(m.search.results) {
			id := m.search.results[m.search.cursor].ID
			m.search.open = false
			return m, m.jumpToSearchResult(id)
		}
		return m, nil
	case "up", "ctrl+p":
		if m.search.cursor > 0 {
			m.search.cursor--
		}
		return m, nil
	case "down", "ctrl+n":
		if m.search.cursor+1 < len(m.search.results) {
			m.search.cursor++
		}
		return m, nil
	case "backspace":
		if len(m.search.query) > 0 {
			m.search.query = m.search.query[:len(m.search.query)-1]
			m.refreshSearch()
		}
		return m, nil
	case "ctrl+u":
		m.search.query = ""
		m.refreshSearch()
		return m, nil
	}
	// Any single printable rune extends the query.
	if r := msg.Runes; len(r) == 1 && r[0] >= 0x20 && r[0] < 0x7f {
		m.search.query += string(r)
		m.refreshSearch()
		return m, nil
	}
	return m, nil
}

// jumpToSearchResult reveals the chosen NodeID in the tree, moves the cursor
// onto it, and triggers a normal focus load. Falls back to a status message
// if the tree can't locate it (e.g. file deleted since indexing).
func (m *Model) jumpToSearchResult(id model.NodeID) tea.Cmd {
	row := m.tree.Reveal(context.Background(), id)
	if row < 0 {
		m.statusMsg = "search: target not found in tree (try rebuilding the index)"
		return nil
	}
	m.cursor = row
	m.activePane = paneTree
	m.stack.Push(id)
	return m.focusID(id)
}

// renderSearch draws the overlay panel. The caller is responsible for
// composing it with the underlying view via lipgloss.Place.
func (m Model) renderSearch(w, h int) string {
	var b strings.Builder

	header := titleStyle.Render("search ") + dimStyle.Render("(esc to close, ↑/↓ ⏎ to pick)")
	b.WriteString(header + "\n")

	prompt := "› " + m.search.query + "_"
	b.WriteString(prompt + "\n\n")

	switch {
	case m.search.indexErr != nil:
		b.WriteString(dimStyle.Render("index error: " + m.search.indexErr.Error()))
	case m.search.indexing:
		b.WriteString(dimStyle.Render("indexing…"))
	case m.search.index == nil:
		b.WriteString(dimStyle.Render("(no index yet)"))
	case len(m.search.results) == 0:
		b.WriteString(dimStyle.Render("(no matches)"))
	default:
		listH := h - 5 // header + prompt + blank + footer + breathing room
		if listH < 1 {
			listH = 1
		}
		start, end := windowAround(m.search.cursor, listH, len(m.search.results))
		for i := start; i < end; i++ {
			r := m.search.results[i]
			line := truncate(formatSearchResult(r), w-2)
			if i == m.search.cursor {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}

	footer := dimStyle.Render(searchFooter(m.search))
	b.WriteString("\n" + footer)

	return searchBoxStyle.Width(w).Height(h).Render(b.String())
}

func formatSearchResult(r search.Result) string {
	tag := "file"
	if r.ID.Kind == model.KindSymbol {
		tag = "sym "
	}
	return dimStyle.Render("["+tag+"] ") + r.Label
}

func searchFooter(s searchUI) string {
	if s.index == nil {
		return "↑/↓ navigate · ⏎ open · esc cancel"
	}
	return "↑/↓ navigate · ⏎ open · esc cancel · " + countSummary(len(s.results), s.index.Len())
}

func countSummary(shown, total int) string {
	if shown == total {
		return shown_(shown) + " entries"
	}
	return shown_(shown) + " / " + shown_(total) + " entries"
}

// shown_ renders an integer; tiny helper so countSummary stays readable.
func shown_(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

var searchBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("12")).
	Padding(0, 1)
