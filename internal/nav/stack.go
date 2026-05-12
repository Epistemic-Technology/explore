// Package nav implements the breadcrumb-aware navigation stack used by the TUI.
// The stack records every focus change so `b` / `Ctrl-o` and `Ctrl-i` traverse
// history identically to a browser.
package nav

import "github.com/mikethicke/explore/internal/model"

type Stack struct {
	entries []model.NodeID
	cursor  int // index of the *current* entry, or -1 when empty
}

func New() *Stack {
	return &Stack{cursor: -1}
}

// Push records a new focus. Any forward history past the cursor is dropped,
// matching browser back/forward semantics.
func (s *Stack) Push(id model.NodeID) {
	if s.cursor >= 0 && s.cursor < len(s.entries)-1 {
		s.entries = s.entries[:s.cursor+1]
	}
	if len(s.entries) > 0 && s.entries[s.cursor] == id {
		return // dedupe consecutive duplicates
	}
	s.entries = append(s.entries, id)
	s.cursor = len(s.entries) - 1
}

func (s *Stack) Current() (model.NodeID, bool) {
	if s.cursor < 0 {
		return model.NodeID{}, false
	}
	return s.entries[s.cursor], true
}

func (s *Stack) Back() (model.NodeID, bool) {
	if s.cursor <= 0 {
		return model.NodeID{}, false
	}
	s.cursor--
	return s.entries[s.cursor], true
}

func (s *Stack) Forward() (model.NodeID, bool) {
	if s.cursor >= len(s.entries)-1 {
		return model.NodeID{}, false
	}
	s.cursor++
	return s.entries[s.cursor], true
}

// Breadcrumbs returns the sequence from root to current.
func (s *Stack) Breadcrumbs() []model.NodeID {
	if s.cursor < 0 {
		return nil
	}
	out := make([]model.NodeID, s.cursor+1)
	copy(out, s.entries[:s.cursor+1])
	return out
}
