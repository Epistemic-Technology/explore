package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mikethicke/explore/internal/gitsrc"
	"github.com/mikethicke/explore/internal/model"
)

// Tabs within the tree pane.
const (
	treeTabTree    = 0
	treeTabHistory = 1
)

// gitOpTimeout bounds a single git invocation. Local repos answer in
// milliseconds; the ceiling just stops a pathological repo from wedging the UI.
const gitOpTimeout = 8 * time.Second

// historyMaxCommits caps the commit list. Pagination is deferred (see DESIGN
// v0.6 known limitations) — a few hundred entries covers the browse use case.
const historyMaxCommits = 300

// historyUI holds the History-tab state: the full branch commit list, the
// highlighted row, and a per-sha detail cache so arrow-key sweeps don't
// re-shell git for a commit already seen. The list is the whole branch
// history — it is not scoped to the focused node.
type historyUI struct {
	branch  string
	loaded  bool
	commits []gitsrc.Commit
	cursor  int
	loading bool
	err     error

	detail      map[string]*gitsrc.CommitDetail
	detailErr   map[string]error
	loadingShas map[string]bool

	// Commit-level AI change explanations, keyed by sha.
	explain        map[string]*model.Explanation
	explainErr     map[string]error
	loadingExplain map[string]bool
}

func newHistoryUI() historyUI {
	return historyUI{
		detail:         map[string]*gitsrc.CommitDetail{},
		detailErr:      map[string]error{},
		loadingShas:    map[string]bool{},
		explain:        map[string]*model.Explanation{},
		explainErr:     map[string]error{},
		loadingExplain: map[string]bool{},
	}
}

type gitLogMsg struct {
	branch  string
	commits []gitsrc.Commit
	err     error
}

type commitDetailMsg struct {
	sha    string
	detail *gitsrc.CommitDetail
	err    error
}

type commitExplainMsg struct {
	sha string
	exp *model.Explanation
	err error
}

// onCommitFocus fires the two lazy loads a highlighted commit needs: its
// changed-file detail (source pane) and its AI change explanation
// (explanation pane). Both are sha-cached, so sweeping j/k is cheap.
func (m *Model) onCommitFocus(c gitsrc.Commit) tea.Cmd {
	return tea.Batch(m.ensureDetail(c.SHA), m.ensureCommitExplain(c))
}

// ensureCommitExplain requests a change-focused explanation for c unless it's
// cached or already in flight. The diff is fetched inside the command (off the
// UI goroutine); the commit subject is used as the intent hint.
func (m *Model) ensureCommitExplain(c gitsrc.Commit) tea.Cmd {
	if c.SHA == "" || m.repo == nil || m.gen == nil {
		return nil
	}
	if _, ok := m.history.explain[c.SHA]; ok {
		return nil
	}
	if m.history.loadingExplain[c.SHA] {
		return nil
	}
	m.history.loadingExplain[c.SHA] = true
	repo := m.repo
	gen := m.gen
	sha, subject := c.SHA, c.Subject
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		var diff []byte
		var err error
		if sha == workingRef {
			subject = "uncommitted working changes"
			diff, err = repo.WorkingDiff(ctx)
		} else {
			diff, err = repo.CommitDiff(ctx, sha)
		}
		if err != nil {
			return commitExplainMsg{sha: sha, err: err}
		}
		// The explain call has its own (LLM) latency budget separate from git.
		ectx, ecancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer ecancel()
		exp, err := gen.ExplainCommit(ectx, sha, subject, string(diff))
		return commitExplainMsg{sha: sha, exp: exp, err: err}
	}
}

func (m Model) handleCommitExplainMsg(msg commitExplainMsg) (tea.Model, tea.Cmd) {
	delete(m.history.loadingExplain, msg.sha)
	if msg.err != nil {
		m.history.explainErr[msg.sha] = msg.err
		return m, nil
	}
	m.history.explain[msg.sha] = msg.exp
	return m, nil
}

// openHistory focuses the tree pane's History tab and loads the full branch
// commit history. No-op with a status hint when not a git repository.
func (m *Model) openHistory() tea.Cmd {
	if m.repo == nil {
		m.statusMsg = "history: not a git repository"
		return nil
	}
	m.activePane = paneTree
	m.treeTab = treeTabHistory
	return m.ensureHistoryLoaded()
}

// ensureHistoryLoaded loads the branch history once. It is NOT scoped to the
// focused node and never reloads on navigation — the list is the whole branch.
// Cheap to call from the `[`/`]` tab-cycle and `H` paths.
func (m *Model) ensureHistoryLoaded() tea.Cmd {
	if m.repo == nil || m.treeTab != treeTabHistory || m.history.loading {
		return nil
	}
	if m.history.loaded {
		return nil
	}
	m.history.loading = true
	m.history.err = nil
	return m.loadHistoryCmd()
}

// loadHistoryCmd fetches the full commit history of the current branch.
func (m *Model) loadHistoryCmd() tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		branch, _ := repo.CurrentBranch(ctx)
		cs, err := repo.Log(ctx, "", historyMaxCommits)
		return gitLogMsg{branch: branch, commits: cs, err: err}
	}
}

func (m Model) handleGitLogMsg(msg gitLogMsg) (tea.Model, tea.Cmd) {
	m.history.loading = false
	m.history.loaded = true
	m.history.err = msg.err
	m.history.branch = msg.branch
	m.history.cursor = 0
	// Synthetic "WORKING" row at the top: the uncommitted state vs HEAD,
	// behaving like a commit whose parent is HEAD.
	working := gitsrc.Commit{SHA: workingRef, ShortSHA: "WORKING", Subject: "uncommitted changes vs HEAD"}
	m.history.commits = append([]gitsrc.Commit{working}, msg.commits...)
	if msg.err != nil {
		return m, nil
	}
	return m, m.onCommitFocus(m.history.commits[0])
}

func (m Model) handleCommitDetailMsg(msg commitDetailMsg) (tea.Model, tea.Cmd) {
	delete(m.history.loadingShas, msg.sha)
	if msg.err != nil {
		m.history.detailErr[msg.sha] = msg.err
		return m, nil
	}
	m.history.detail[msg.sha] = msg.detail
	return m, nil
}

// ensureDetail fetches a commit's message + changed-file list unless it's
// already cached or in flight. Returns nil when nothing needs doing.
func (m *Model) ensureDetail(sha string) tea.Cmd {
	if sha == "" || m.repo == nil {
		return nil
	}
	if _, ok := m.history.detail[sha]; ok {
		return nil
	}
	if m.history.loadingShas[sha] {
		return nil
	}
	m.history.loadingShas[sha] = true
	repo := m.repo
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		if sha == workingRef {
			ch, err := repo.WorkingChanges(ctx)
			if err != nil {
				return commitDetailMsg{sha: sha, err: err}
			}
			d := gitsrc.CommitDetail{
				Commit:  gitsrc.Commit{SHA: workingRef, ShortSHA: "WORKING", Subject: "Uncommitted changes vs HEAD"},
				Changes: ch,
			}
			return commitDetailMsg{sha: sha, detail: &d}
		}
		d, err := repo.CommitMeta(ctx, sha)
		if err != nil {
			return commitDetailMsg{sha: sha, err: err}
		}
		return commitDetailMsg{sha: sha, detail: &d}
	}
}

func (m *Model) selectedCommit() (gitsrc.Commit, bool) {
	if m.history.cursor < 0 || m.history.cursor >= len(m.history.commits) {
		return gitsrc.Commit{}, false
	}
	return m.history.commits[m.history.cursor], true
}

// updateHistoryPane handles keys while the tree pane is on the History tab.
// j/k move the highlight (lazily loading the highlighted commit's detail);
// Enter is wired to snapshot mode in a later phase.
func (m Model) updateHistoryPane(s string) (tea.Model, tea.Cmd) {
	n := len(m.history.commits)
	switch s {
	case "j", "down":
		m.pendingG = false
		if n > 0 {
			m.history.cursor = min(m.history.cursor+m.takeCount(1), n-1)
		}
		c, _ := m.selectedCommit()
		return m, m.onCommitFocus(c)
	case "k", "up":
		m.pendingG = false
		if n > 0 {
			m.history.cursor = max(m.history.cursor-m.takeCount(1), 0)
		}
		c, _ := m.selectedCommit()
		return m, m.onCommitFocus(c)
	case "g":
		if m.pendingG {
			m.pendingG = false
			m.history.cursor = 0
			c, _ := m.selectedCommit()
			return m, m.onCommitFocus(c)
		}
		m.pendingG = true
		return m, nil
	case "G":
		m.resetVim()
		if n > 0 {
			m.history.cursor = n - 1
		}
		c, _ := m.selectedCommit()
		return m, m.onCommitFocus(c)
	case "enter":
		m.resetVim()
		return m.enterSnapshot()
	}
	m.resetVim()
	return m, nil
}

// renderHistory draws the commit list in the tree pane.
func (m Model) renderHistory(w, h int) string {
	if m.repo == nil {
		return dimStyle.Render("(not a git repository)")
	}
	if m.history.loading {
		return dimStyle.Render("loading history…")
	}
	if m.history.err != nil {
		return warnStyle.Render("history error: ") + truncate(m.history.err.Error(), w)
	}
	if len(m.history.commits) == 0 {
		return dimStyle.Render("(no commits)")
	}
	if h < 1 {
		return ""
	}
	hdr := "history"
	if m.history.branch != "" {
		hdr = "history · " + m.history.branch
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render(truncate("⌥ "+hdr, w)) + "\n")
	rowsH := h - 1
	start, end := windowAround(m.history.cursor, rowsH, len(m.history.commits))
	for i := start; i < end; i++ {
		c := m.history.commits[i]
		line := truncate(fmt.Sprintf("%s %s", c.ShortSHA, c.Subject), w)
		if i == m.history.cursor {
			line = selectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderCommitDetail draws the highlighted commit's message and changed-file
// list in the source pane (per the History UX).
func (m Model) renderCommitDetail(w, h int) string {
	if m.repo == nil {
		return dimStyle.Render("(not a git repository)")
	}
	c, ok := m.selectedCommit()
	if !ok {
		return dimStyle.Render("(no commit selected)")
	}
	if err := m.history.detailErr[c.SHA]; err != nil {
		return warnStyle.Render("detail error: ") + truncate(err.Error(), w)
	}
	d, ok := m.history.detail[c.SHA]
	if !ok {
		return dimStyle.Render("loading commit " + c.ShortSHA + "…")
	}

	var lines []string
	lines = append(lines, titleStyle.Render(truncate(c.ShortSHA+"  "+d.Subject, w)))
	if c.SHA != workingRef {
		meta := fmt.Sprintf("%s · %s", d.Author, d.Date.Format("2006-01-02 15:04"))
		lines = append(lines, dimStyle.Render(truncate(meta, w)))
	}
	lines = append(lines, "")
	if body := strings.TrimSpace(d.Body); body != "" {
		for _, ln := range strings.Split(body, "\n") {
			lines = append(lines, truncate(ln, w))
		}
		lines = append(lines, "")
	}
	lines = append(lines, dimStyle.Render(fmt.Sprintf("%d changed file(s):", len(d.Changes))))
	for _, ch := range d.Changes {
		lines = append(lines, truncate(statusStyle(ch.Status).Render(changeLabel(ch)), w))
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// renderCommitExplain draws the AI change-explanation for the highlighted
// commit in the explanation pane.
func (m Model) renderCommitExplain(w, h int) string {
	if m.repo == nil {
		return dimStyle.Render("(not a git repository)")
	}
	c, ok := m.selectedCommit()
	if !ok {
		return dimStyle.Render("(highlight a commit to explain its changes)")
	}
	head := dimStyle.Render(truncate("⌥ "+c.ShortSHA+"  "+c.Subject, w))
	var body string
	switch {
	case m.history.explainErr[c.SHA] != nil:
		body = warnStyle.Render("explain error: ") + m.history.explainErr[c.SHA].Error()
	case m.history.explain[c.SHA] != nil:
		body = wrap(m.history.explain[c.SHA].Prose, w)
	default:
		body = dimStyle.Render("Explaining what this commit changed…")
	}
	avail := h - 2
	if avail < 1 {
		avail = 1
	}
	lines := strings.Split(body, "\n")
	if len(lines) > avail {
		lines = lines[:avail]
	}
	return head + "\n\n" + strings.Join(lines, "\n")
}

func changeLabel(c gitsrc.FileChange) string {
	mark := c.Status
	if len(mark) > 1 {
		mark = mark[:1] // R100 → R
	}
	if c.OldPath != "" {
		return fmt.Sprintf("  %s  %s → %s", mark, c.OldPath, c.Path)
	}
	return fmt.Sprintf("  %s  %s", mark, c.Path)
}
