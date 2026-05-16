// Package nav implements the breadcrumb-aware navigation stack used by the TUI.
// The stack records every focus change so `b` / `Ctrl-o` and `Ctrl-i` traverse
// history identically to a browser. Each frame also carries the revision the
// node was viewed at ("" = live working tree, else a commit sha) so back /
// forward restore the historical-snapshot context they were captured in.
package nav

import "github.com/mikethicke/explore/internal/model"

type frame struct {
	id  model.NodeID
	rev string
}

type Stack struct {
	entries []frame
	cursor  int // index of the *current* entry, or -1 when empty
}

func New() *Stack {
	return &Stack{cursor: -1}
}

// Push records a new focus at rev. Any forward history past the cursor is
// dropped, matching browser back/forward semantics. Consecutive duplicates
// (same node *and* same revision) are coalesced.
func (s *Stack) Push(id model.NodeID, rev string) {
	if s.cursor >= 0 && s.cursor < len(s.entries)-1 {
		s.entries = s.entries[:s.cursor+1]
	}
	if len(s.entries) > 0 && s.entries[s.cursor].id == id && s.entries[s.cursor].rev == rev {
		return
	}
	s.entries = append(s.entries, frame{id: id, rev: rev})
	s.cursor = len(s.entries) - 1
}

func (s *Stack) Current() (model.NodeID, bool) {
	if s.cursor < 0 {
		return model.NodeID{}, false
	}
	return s.entries[s.cursor].id, true
}

// CurRev returns the revision of the current frame ("" when empty or live).
func (s *Stack) CurRev() string {
	if s.cursor < 0 {
		return ""
	}
	return s.entries[s.cursor].rev
}

func (s *Stack) Back() (model.NodeID, bool) {
	if s.cursor <= 0 {
		return model.NodeID{}, false
	}
	s.cursor--
	return s.entries[s.cursor].id, true
}

func (s *Stack) Forward() (model.NodeID, bool) {
	if s.cursor >= len(s.entries)-1 {
		return model.NodeID{}, false
	}
	s.cursor++
	return s.entries[s.cursor].id, true
}

// Breadcrumbs returns the node sequence from root to current.
func (s *Stack) Breadcrumbs() []model.NodeID {
	if s.cursor < 0 {
		return nil
	}
	out := make([]model.NodeID, s.cursor+1)
	for i := 0; i <= s.cursor; i++ {
		out[i] = s.entries[i].id
	}
	return out
}
