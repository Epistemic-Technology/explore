package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/mikethicke/explore/internal/highlight"
)

// syntaxStyles maps a highlight capture name to a lipgloss style. Unknown
// captures fall back to plain (no style). Loosely based on the gruvbox /
// one-dark palettes — bright enough to read on dark terminals, muted enough
// not to clash with the rest of the TUI chrome.
var syntaxStyles = map[highlight.Capture]lipgloss.Style{
	"comment":           lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true),
	"keyword":           lipgloss.NewStyle().Foreground(lipgloss.Color("141")),
	"keyword.function":  lipgloss.NewStyle().Foreground(lipgloss.Color("141")),
	"keyword.return":    lipgloss.NewStyle().Foreground(lipgloss.Color("141")),
	"string":            lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
	"string.escape":     lipgloss.NewStyle().Foreground(lipgloss.Color("180")),
	"number":            lipgloss.NewStyle().Foreground(lipgloss.Color("173")),
	"boolean":           lipgloss.NewStyle().Foreground(lipgloss.Color("167")),
	"function":          lipgloss.NewStyle().Foreground(lipgloss.Color("75")),
	"function.call":     lipgloss.NewStyle().Foreground(lipgloss.Color("75")),
	"function.method":   lipgloss.NewStyle().Foreground(lipgloss.Color("75")),
	"type":              lipgloss.NewStyle().Foreground(lipgloss.Color("180")),
	"type.builtin":      lipgloss.NewStyle().Foreground(lipgloss.Color("180")),
	"variable":          {},
	"variable.builtin":  lipgloss.NewStyle().Foreground(lipgloss.Color("167")),
	"constant":          lipgloss.NewStyle().Foreground(lipgloss.Color("173")),
	"constant.builtin":  lipgloss.NewStyle().Foreground(lipgloss.Color("167")),
	"property":          lipgloss.NewStyle().Foreground(lipgloss.Color("117")),
	"attribute":         lipgloss.NewStyle().Foreground(lipgloss.Color("141")),
	"tag":               lipgloss.NewStyle().Foreground(lipgloss.Color("167")),
}

// styleFor returns the style for a capture, or a zero style for unknowns.
// Zero style renders text unchanged.
func styleFor(c highlight.Capture) lipgloss.Style {
	return syntaxStyles[c]
}

// Git diff / change styles. Foreground colors for the colored tree and
// changed-file lists (green=added, yellow=modified, red=deleted); the *Bg
// styles tint whole diff lines in the source pane.
var (
	addStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	modifyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("180"))
	deleteStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))

	diffAddBg  = lipgloss.NewStyle().Foreground(lipgloss.Color("151")).Background(lipgloss.Color("22"))
	diffDelBg  = lipgloss.NewStyle().Foreground(lipgloss.Color("210")).Background(lipgloss.Color("52"))
	diffHunkBg = lipgloss.NewStyle().Foreground(lipgloss.Color("117")).Background(lipgloss.Color("236"))
)

// statusStyle maps a git name-status code (A/M/D/R/C, optionally with a
// similarity score like R100) to its tree/list color.
func statusStyle(status string) lipgloss.Style {
	if status == "" {
		return lipgloss.NewStyle()
	}
	switch status[0] {
	case 'A':
		return addStyle
	case 'D':
		return deleteStyle
	default: // M, R, C, T, …
		return modifyStyle
	}
}
