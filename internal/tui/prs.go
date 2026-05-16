package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mikethicke/explore/internal/ghsrc"
	"github.com/mikethicke/explore/internal/model"
)

// PR list caps. Open PRs are the working set; "recently merged" is a shallower
// catch-up window. Pagination is deferred, mirroring the History tab.
const (
	prOpenLimit   = 50
	prMergedLimit = 20
)

// prFetchTimeout bounds the PR-head fetch (network: two git fetches + a
// merge-base). Generous vs. gitOpTimeout because it actually hits the remote.
const prFetchTimeout = 60 * time.Second

// prUI holds the PRs-tab state: the PR list, the highlighted row, and per-PR
// caches for detail (body + changed files), the flat diff (fallback view +
// review input), and the AI review explanation. Loaded once and never reloaded
// on navigation, like historyUI.
type prUI struct {
	loaded  bool
	loading bool
	err     error
	prs     []ghsrc.PR
	cursor  int

	detail        map[int]*ghsrc.PRDetail
	detailErr     map[int]error
	loadingDetail map[int]bool

	diff        map[int]string
	diffErr     map[int]error
	loadingDiff map[int]bool

	explain        map[int]*model.Explanation
	explainErr     map[int]error
	loadingExplain map[int]bool

	// fetching is the PR number whose snapshot fetch is in flight (0 = none),
	// so the detail pane can show progress and Enter can't double-fire.
	fetching int
}

func newPRUI() prUI {
	return prUI{
		detail:         map[int]*ghsrc.PRDetail{},
		detailErr:      map[int]error{},
		loadingDetail:  map[int]bool{},
		diff:           map[int]string{},
		diffErr:        map[int]error{},
		loadingDiff:    map[int]bool{},
		explain:        map[int]*model.Explanation{},
		explainErr:     map[int]error{},
		loadingExplain: map[int]bool{},
	}
}

type prListMsg struct {
	prs []ghsrc.PR
	err error
}

type prDetailMsg struct {
	number int
	detail *ghsrc.PRDetail
	err    error
}

type prDiffMsg struct {
	number int
	text   string
	err    error
}

type prExplainMsg struct {
	number int
	exp    *model.Explanation
	err    error
}

// prSnapshotReadyMsg carries the result of the network fetch that prepares a
// PR for snapshot browsing. headRef is a local ref usable as a revision; base
// is the merge-base to diff against.
type prSnapshotReadyMsg struct {
	number  int
	headRef string
	base    string
	err     error
}

// openPRs focuses the tree pane's PRs tab and loads the PR list. No-op with a
// status hint when GitHub support is unavailable.
func (m *Model) openPRs() tea.Cmd {
	if m.gh == nil {
		m.statusMsg = "PRs: needs the gh CLI, a GitHub remote, and gh auth login"
		return nil
	}
	m.activePane = paneTree
	m.treeTab = treeTabPRs
	return m.ensurePRsLoaded()
}

// ensurePRsLoaded loads the PR list once. Cheap to call from the `[`/`]`
// tab-cycle and `P` paths.
func (m *Model) ensurePRsLoaded() tea.Cmd {
	if m.gh == nil || m.treeTab != treeTabPRs || m.prs.loading || m.prs.loaded {
		return nil
	}
	m.prs.loading = true
	m.prs.err = nil
	gh := m.gh
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		prs, err := gh.ListPRs(ctx, prOpenLimit, prMergedLimit)
		return prListMsg{prs: prs, err: err}
	}
}

func (m Model) handlePRListMsg(msg prListMsg) (tea.Model, tea.Cmd) {
	m.prs.loading = false
	m.prs.loaded = true
	m.prs.err = msg.err
	m.prs.prs = msg.prs
	m.prs.cursor = 0
	if msg.err != nil || len(msg.prs) == 0 {
		return m, nil
	}
	return m, m.onPRFocus(msg.prs[0])
}

func (m *Model) selectedPR() (ghsrc.PR, bool) {
	if m.prs.cursor < 0 || m.prs.cursor >= len(m.prs.prs) {
		return ghsrc.PR{}, false
	}
	return m.prs.prs[m.prs.cursor], true
}

// onPRFocus fires the lazy loads a highlighted PR needs: its detail (source
// pane) and its flat diff (review input + fallback view). The review
// explanation kicks off once both land. All number-cached, so j/k is cheap.
func (m *Model) onPRFocus(pr ghsrc.PR) tea.Cmd {
	return tea.Batch(m.ensurePRDetail(pr.Number), m.ensurePRDiff(pr.Number))
}

func (m *Model) ensurePRDetail(number int) tea.Cmd {
	if m.gh == nil || number == 0 {
		return nil
	}
	if _, ok := m.prs.detail[number]; ok {
		return nil
	}
	if m.prs.loadingDetail[number] {
		return nil
	}
	m.prs.loadingDetail[number] = true
	gh := m.gh
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		d, err := gh.PRDetail(ctx, number)
		if err != nil {
			return prDetailMsg{number: number, err: err}
		}
		return prDetailMsg{number: number, detail: &d}
	}
}

func (m *Model) ensurePRDiff(number int) tea.Cmd {
	if m.gh == nil || number == 0 {
		return nil
	}
	if _, ok := m.prs.diff[number]; ok {
		return nil
	}
	if m.prs.loadingDiff[number] {
		return nil
	}
	m.prs.loadingDiff[number] = true
	gh := m.gh
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitOpTimeout)
		defer cancel()
		b, err := gh.PRDiff(ctx, number)
		if err != nil {
			return prDiffMsg{number: number, err: err}
		}
		return prDiffMsg{number: number, text: string(b)}
	}
}

// ensurePRExplain requests the reviewer-focused explanation once both the
// detail (title/body) and the diff are in hand. Number-cached + in-flight
// guarded, so it's safe to call from both the detail and diff handlers.
func (m *Model) ensurePRExplain(number int) tea.Cmd {
	if m.gen == nil || number == 0 {
		return nil
	}
	d, okD := m.prs.detail[number]
	diff, okF := m.prs.diff[number]
	if !okD || !okF {
		return nil // wait for the other half
	}
	if _, done := m.prs.explain[number]; done {
		return nil
	}
	if m.prs.loadingExplain[number] {
		return nil
	}
	m.prs.loadingExplain[number] = true
	gen := m.gen
	title, body := d.Title, d.Body
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		exp, err := gen.ExplainPR(ctx, number, title, body, diff)
		return prExplainMsg{number: number, exp: exp, err: err}
	}
}

func (m Model) handlePRDetailMsg(msg prDetailMsg) (tea.Model, tea.Cmd) {
	delete(m.prs.loadingDetail, msg.number)
	if msg.err != nil {
		m.prs.detailErr[msg.number] = msg.err
		return m, nil
	}
	m.prs.detail[msg.number] = msg.detail
	return m, m.ensurePRExplain(msg.number)
}

func (m Model) handlePRDiffMsg(msg prDiffMsg) (tea.Model, tea.Cmd) {
	delete(m.prs.loadingDiff, msg.number)
	if msg.err != nil {
		m.prs.diffErr[msg.number] = msg.err
		return m, nil
	}
	m.prs.diff[msg.number] = msg.text
	return m, m.ensurePRExplain(msg.number)
}

func (m Model) handlePRExplainMsg(msg prExplainMsg) (tea.Model, tea.Cmd) {
	delete(m.prs.loadingExplain, msg.number)
	if msg.err != nil {
		m.prs.explainErr[msg.number] = msg.err
		return m, nil
	}
	m.prs.explain[msg.number] = msg.exp
	return m, nil
}

// updatePRsPane handles keys while the tree pane is on the PRs tab. j/k move
// the highlight (lazily loading the PR's detail/diff/review); Enter fetches the
// PR head and drops into snapshot mode.
func (m Model) updatePRsPane(s string) (tea.Model, tea.Cmd) {
	n := len(m.prs.prs)
	switch s {
	case "j", "down":
		m.pendingG = false
		if n > 0 {
			m.prs.cursor = min(m.prs.cursor+m.takeCount(1), n-1)
		}
		pr, _ := m.selectedPR()
		return m, m.onPRFocus(pr)
	case "k", "up":
		m.pendingG = false
		if n > 0 {
			m.prs.cursor = max(m.prs.cursor-m.takeCount(1), 0)
		}
		pr, _ := m.selectedPR()
		return m, m.onPRFocus(pr)
	case "g":
		if m.pendingG {
			m.pendingG = false
			m.prs.cursor = 0
			pr, _ := m.selectedPR()
			return m, m.onPRFocus(pr)
		}
		m.pendingG = true
		return m, nil
	case "G":
		m.resetVim()
		if n > 0 {
			m.prs.cursor = n - 1
		}
		pr, _ := m.selectedPR()
		return m, m.onPRFocus(pr)
	case "enter":
		m.resetVim()
		return m.enterPRSnapshot()
	}
	m.resetVim()
	return m, nil
}

// enterPRSnapshot fetches the highlighted PR's head + base (network) and, on
// success, switches the whole app into snapshot mode at the PR head with the
// merge-base as the diff base. The fetch is async; prSnapshotReadyMsg applies
// it. On failure the user stays on the PRs tab with the flat diff still shown.
func (m Model) enterPRSnapshot() (tea.Model, tea.Cmd) {
	if m.repo == nil {
		m.statusMsg = "PR snapshot: not a git repository"
		return m, nil
	}
	pr, ok := m.selectedPR()
	if !ok {
		return m, nil
	}
	if m.prs.fetching != 0 {
		return m, nil // a fetch is already in flight
	}
	if m.gh == nil {
		m.statusMsg = "PR snapshot: GitHub support unavailable"
		return m, nil
	}
	m.prs.fetching = pr.Number
	m.statusMsg = fmt.Sprintf("fetching PR #%d…", pr.Number)
	repo, gh := m.repo, m.gh
	num, base := pr.Number, pr.BaseRefName
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), prFetchTimeout)
		defer cancel()
		url, err := gh.HTTPSRemote(ctx)
		if err != nil {
			return prSnapshotReadyMsg{number: num, err: err}
		}
		headRef, mb, err := repo.PreparePR(ctx, num, base, url)
		return prSnapshotReadyMsg{number: num, headRef: headRef, base: mb, err: err}
	}
}

func (m Model) handlePRSnapshotReadyMsg(msg prSnapshotReadyMsg) (tea.Model, tea.Cmd) {
	m.prs.fetching = 0
	if msg.err != nil {
		m.statusMsg = warnStyle.Render("PR fetch failed: ") + msg.err.Error() +
			" — showing flat diff"
		return m, nil
	}
	// Record the merge-base so applyRevision (incl. back/forward into this
	// frame) diffs the PR head against it instead of head^.
	m.prBaseFor[msg.headRef] = msg.base
	if err := m.applyRevision(msg.headRef); err != nil {
		m.statusMsg = "PR snapshot: " + err.Error()
		return m, nil
	}
	m.treeTab = treeTabTree
	m.activePane = paneTree
	root := model.NodeID{Kind: model.KindRepo, Path: ""}
	m.currentID = root
	m.stack.Push(root, msg.headRef)
	title := ""
	if d := m.prs.detail[msg.number]; d != nil {
		title = d.Title
	}
	m.snapshotDesc = fmt.Sprintf("PR #%d %s", msg.number, title)
	m.statusMsg = fmt.Sprintf("snapshot @ PR #%d — Esc/b returns to working tree", msg.number)
	return m, tea.Batch(m.scheduleLoad(root), m.loadChangesCmd(msg.headRef))
}

// prStateStyle colors a PR row by its lifecycle state.
func prStateStyle(pr ghsrc.PR) lipgloss.Style {
	switch {
	case pr.Draft:
		return dimStyle
	case pr.State == "MERGED":
		return modifyStyle
	case pr.State == "CLOSED":
		return deleteStyle
	default: // OPEN
		return addStyle
	}
}

func prStateLabel(pr ghsrc.PR) string {
	if pr.Draft {
		return "DRAFT"
	}
	switch pr.State {
	case "MERGED":
		return "MERGED"
	case "CLOSED":
		return "CLOSED"
	default:
		return "OPEN"
	}
}

// renderPRList draws the PR list in the tree pane.
func (m Model) renderPRList(w, h int) string {
	if m.gh == nil {
		return dimStyle.Render("(GitHub PRs unavailable — needs gh + auth)")
	}
	if m.prs.loading {
		return dimStyle.Render("loading pull requests…")
	}
	if m.prs.err != nil {
		return warnStyle.Render("PR list error: ") + truncate(m.prs.err.Error(), w)
	}
	if len(m.prs.prs) == 0 {
		return dimStyle.Render("(no open or recently-merged PRs)")
	}
	if h < 1 {
		return ""
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render(truncate("⌥ pull requests", w)) + "\n")
	rowsH := h - 1
	start, end := windowAround(m.prs.cursor, rowsH, len(m.prs.prs))
	for i := start; i < end; i++ {
		pr := m.prs.prs[i]
		line := truncate(fmt.Sprintf("#%-5d %-6s %s", pr.Number, prStateLabel(pr), pr.Title), w)
		switch {
		case i == m.prs.cursor:
			// Cursor highlight wins; state color is dropped on the cursor row
			// so the selected background paints cleanly (same rule as the tree).
			line = selectedStyle.Render(line)
		default:
			line = prStateStyle(pr).Render(line)
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderPRDetail draws the highlighted PR's metadata, body, and changed-file
// list in the source pane.
func (m Model) renderPRDetail(w, h int) string {
	if m.gh == nil {
		return dimStyle.Render("(GitHub PRs unavailable)")
	}
	pr, ok := m.selectedPR()
	if !ok {
		return dimStyle.Render("(no PR selected)")
	}
	if err := m.prs.detailErr[pr.Number]; err != nil {
		return warnStyle.Render("detail error: ") + truncate(err.Error(), w)
	}
	d, ok := m.prs.detail[pr.Number]
	if !ok {
		return dimStyle.Render(fmt.Sprintf("loading PR #%d…", pr.Number))
	}

	var lines []string
	lines = append(lines, titleStyle.Render(truncate(fmt.Sprintf("#%d  %s", pr.Number, d.Title), w)))
	meta := fmt.Sprintf("%s · %s → %s · %s",
		prStateLabel(pr), d.HeadRefName, d.BaseRefName, d.Author)
	lines = append(lines, dimStyle.Render(truncate(meta, w)))
	if m.prs.fetching == pr.Number {
		lines = append(lines, warnStyle.Render("fetching head for snapshot…"))
	} else {
		lines = append(lines, dimStyle.Render("Enter: browse this PR as a snapshot"))
	}
	lines = append(lines, "")
	if body := strings.TrimSpace(d.Body); body != "" {
		for _, ln := range strings.Split(body, "\n") {
			lines = append(lines, truncate(ln, w))
		}
		lines = append(lines, "")
	}
	lines = append(lines, dimStyle.Render(fmt.Sprintf("%d changed file(s):", len(d.Files))))
	for _, f := range d.Files {
		stat := fmt.Sprintf("  +%d -%d  %s", f.Additions, f.Deletions, f.Path)
		lines = append(lines, truncate(stat, w))
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	return strings.Join(lines, "\n")
}

// renderPRExplain draws the AI review for the highlighted PR in the
// explanation pane.
func (m Model) renderPRExplain(w, h int) string {
	if m.gh == nil {
		return dimStyle.Render("(GitHub PRs unavailable)")
	}
	pr, ok := m.selectedPR()
	if !ok {
		return dimStyle.Render("(highlight a PR to review it)")
	}
	head := dimStyle.Render(truncate(fmt.Sprintf("⌥ review · #%d %s", pr.Number, pr.Title), w))
	var body string
	switch {
	case m.prs.explainErr[pr.Number] != nil:
		body = warnStyle.Render("review error: ") + m.prs.explainErr[pr.Number].Error()
	case m.prs.explain[pr.Number] != nil:
		body = wrap(m.prs.explain[pr.Number].Prose, w)
	default:
		body = dimStyle.Render("Reviewing this pull request…")
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
