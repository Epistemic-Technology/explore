package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	helpOverlayW = 76
	helpOverlayH = 28
)

// helpEntry is one row of the cheat sheet.
type helpEntry struct {
	keys string
	desc string
}

// helpSection is a titled group of entries — shown together in the overlay.
type helpSection struct {
	title   string
	entries []helpEntry
}

var helpSections = []helpSection{
	{
		title: "Panes & tabs",
		entries: []helpEntry{
			{"Alt+1 / Alt+2 / Alt+3", "focus tree / explanation / source"},
			{"Tab / Shift+Tab", "next / previous pane"},
			{"[ / ]", "previous / next tab within pane"},
			{"b  or  Ctrl+O", "back in navigation stack"},
			{"Ctrl+I", "forward in navigation stack"},
		},
	},
	{
		title: "Tree pane",
		entries: []helpEntry{
			{"j / k  or  ↓ / ↑", "move cursor"},
			{"Space  or  l  or  →", "expand / collapse"},
			{"←", "collapse / climb to parent"},
			{"Enter", "open in source view"},
			{"gg / G", "top / bottom"},
			{"Ngg", "jump to row N"},
		},
	},
	{
		title: "Source pane",
		entries: []helpEntry{
			{"j / k", "down / up one line"},
			{"J / K  or  Ctrl+D / Ctrl+U", "page down / up"},
			{"gg / G", "first / last line"},
			{"Ngg", "jump to line N"},
		},
	},
	{
		title: "Explanation & Q&A",
		entries: []helpEntry{
			{"?", "ask a question about the focused node"},
			{"i  or  Enter", "start typing in the Q&A input"},
			{"Esc", "leave the input (shortcuts active); again to close Q&A"},
			{"Enter (in input)", "send the question"},
			{"Ctrl+C", "interrupt a streaming response"},
			{"j / k", "scroll prose (when input inactive)"},
		},
	},
	{
		title: "Actions",
		entries: []helpEntry{
			{"/", "fuzzy search files & symbols"},
			{"r", "regenerate the current explanation"},
			{"e", "open the focused file in $EDITOR"},
			{"u", "show callers of the focused symbol"},
			{"d", "show callees on the current source line"},
			{"y", "yank menu  (p path · e explanation · s source)"},
		},
	},
	{
		title: "Global",
		entries: []helpEntry{
			{"h", "show this help"},
			{"q  or  Ctrl+C", "quit"},
		},
	},
}

// updateHelp handles keys while the help overlay is on top. Ctrl+C still
// quits (it's the universal escape hatch); every other key just dismisses
// the overlay, leaving the user where they were.
func (m Model) updateHelp(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	m.helpOpen = false
	return m, nil
}

// renderHelp draws the cheat sheet overlay. The caller composes it with the
// underlying view via lipgloss.Place.
func (m Model) renderHelp(w, h int) string {
	header := titleStyle.Render("keyboard shortcuts ") + dimStyle.Render("(press any key to close)")
	footer := dimStyle.Render("h · esc · enter — close")

	bodyH := h - 4
	if bodyH < 1 {
		bodyH = 1
	}

	// Compute the widest "keys" column once so descriptions align across all
	// sections.
	keyW := 0
	for _, sec := range helpSections {
		for _, e := range sec.entries {
			if w := lipgloss.Width(e.keys); w > keyW {
				keyW = w
			}
		}
	}
	// Cap so very long key strings don't push descriptions off the right
	// edge on narrow terminals.
	if keyW > w/2 {
		keyW = w / 2
	}

	var b strings.Builder
	for i, sec := range helpSections {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(titleInactive.Render(sec.title) + "\n")
		for _, e := range sec.entries {
			line := lipgloss.NewStyle().Width(keyW).Render(e.keys) + "  " + dimStyle.Render(e.desc)
			b.WriteString(truncate(line, w) + "\n")
		}
	}
	bodyText := strings.TrimRight(b.String(), "\n")

	// Trim to fit the overlay height. Help is reference material — losing the
	// tail is better than crashing the layout on a tiny terminal.
	lines := strings.Split(bodyText, "\n")
	if len(lines) > bodyH {
		lines = lines[:bodyH]
		lines[len(lines)-1] = dimStyle.Render("(more — resize for full list)")
	}
	bodyText = strings.Join(lines, "\n")

	full := header + "\n\n" + bodyText + "\n" + footer
	return helpBoxStyle.Width(w).Height(h).Render(full)
}

// helpOverlayDims caps the overlay at sensible defaults but shrinks to fit
// small terminals — same pattern as the search / xref overlays.
func helpOverlayDims(termW, termH int) (int, int) {
	w := helpOverlayW
	if w > termW-4 {
		w = termW - 4
	}
	if w < 36 {
		w = 36
	}
	h := helpOverlayH
	if h > termH-2 {
		h = termH - 2
	}
	if h < 10 {
		h = 10
	}
	return w, h
}

var helpBoxStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("12")).
	Padding(0, 1)
