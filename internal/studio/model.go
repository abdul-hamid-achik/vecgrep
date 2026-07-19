package studio

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/daemon"
	"github.com/abdul-hamid-achik/vecgrep/internal/git"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

// emptyIndexAutoStartMax is the max scanned files for an empty project to
// start indexing without a y/n confirm (Ollama-local friendly). Larger first
// indexes still plan-first for wrong-folder protection.
const emptyIndexAutoStartMax = 100

type viewName int

const (
	viewSearch viewName = iota
	viewStatus
	viewConfig
	viewHelp
)

type focusArea int

const (
	focusQuery focusArea = iota
	focusDirectory
	focusFilePattern
	focusLineRange
	focusResults
	focusPreview
	focusCount
)

type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmDelete
	confirmFullReindex
	confirmIndex // plan-first incremental (empty index and/or large scope)
	confirmReset
)

const (
	maxQueryHistory = 20
	minScoreStep    = float32(0.05)
	indexTickEvery  = 250 * time.Millisecond
)

type Model struct {
	ctx      context.Context
	cancel   context.CancelFunc
	startDir string

	width  int
	height int

	session *app.Session
	service *app.Service
	status  *app.StatusResponse

	query       textinput.Model
	directory   textinput.Model
	filePattern textinput.Model
	lineRange   textinput.Model
	preview     viewport.Model
	overlay     viewport.Model
	spinner     spinner.Model
	progBar     progress.Model
	help        help.Model
	keys        studioKeyMap
	resultList  list.Model

	activeView viewName
	focus      focusArea

	mode          search.SearchMode
	effectiveMode search.SearchMode // mode of last successful result set
	limit         int
	minScore      float32
	languages     []string
	langIdx       int
	types         []string
	typeIdx       int

	results        []search.Result
	selected       int
	lastQuery      string
	lastDuration   time.Duration
	warnings       []string
	resultsDirty   bool
	queryHistory   []string
	historyIdx     int // -1 = not browsing history
	filtersOpen    bool
	sessionLoading bool
	searching      bool
	searchGen      int
	indexing       bool
	indexGen       int
	indexProgress  *index.Progress
	indexRecent    []string
	indexStarted   time.Time
	indexCancel    context.CancelFunc
	daemonIndex    bool
	statusMessage  string
	errMessage     string
	confirm        confirmAction
	dryRun         *index.DryRunPreview
	pendingFull    bool // which indexCmd(full) to run after plan confirm
	planningIndex  bool // dry-run in flight
	readiness      app.Readiness
	hasReadiness   bool
	branchName     string
	readOnly       bool
}

type sessionLoadedMsg struct {
	session    *app.Session
	status     *app.StatusResponse
	readiness  app.Readiness
	branchName string
	err        error
	readOnly   bool
}

type statusLoadedMsg struct {
	status    *app.StatusResponse
	readiness app.Readiness
	err       error
}

type searchLoadedMsg struct {
	gen      int
	query    string
	response *app.SearchResponse
	err      error
}

type indexDoneMsg struct {
	gen    int
	result *index.IndexResult
	err    error
	full   bool
}

type indexProgressMsg struct {
	gen      int
	progress index.Progress
	ch       <-chan index.Progress
}

type indexProgressClosedMsg struct {
	gen int
}

type indexTickMsg struct {
	gen int
}

type deleteDoneMsg struct {
	path    string
	deleted int64
	err     error
}

type resetDoneMsg struct {
	err error
}

type editorDoneMsg struct {
	err error
}

type dryRunLoadedMsg struct {
	preview *index.DryRunPreview
	err     error
	full    bool // true → confirm full reindex; false → first-index style
}

type daemonReindexDoneMsg struct {
	gen    int
	result *index.IndexResult
	err    error
	full   bool
}

type yankDoneMsg struct {
	text string
	err  error
}

func NewModel(ctx context.Context, startDir string) Model {
	opCtx, cancel := context.WithCancel(ctx)

	ti := newTextInput("query", "Search code by meaning…", 80)
	ti.Focus()

	dir := newTextInput("dir", "directory prefix…", 24)
	file := newTextInput("file", "file glob…", 28)
	lines := newTextInput("lines", "start-end…", 18)

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(12))
	vp.SoftWrap = true

	ov := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
	ov.SoftWrap = true

	spin := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(colorAccent)),
	)
	bar := progress.New(
		progress.WithDefaultBlend(),
		progress.WithWidth(36),
		progress.WithoutPercentage(), // we render N/M + % ourselves
	)
	helpModel := help.New()
	resultList := newResultList(40, 12)

	return Model{
		ctx:            opCtx,
		cancel:         cancel,
		startDir:       startDir,
		query:          ti,
		directory:      dir,
		filePattern:    file,
		lineRange:      lines,
		preview:        vp,
		overlay:        ov,
		spinner:        spin,
		progBar:        bar,
		help:           helpModel,
		keys:           defaultStudioKeys(),
		resultList:     resultList,
		activeView:     viewSearch,
		focus:          focusQuery,
		mode:           search.SearchModeHybrid,
		effectiveMode:  search.SearchModeHybrid,
		limit:          10,
		languages:      []string{"", "go", "python", "javascript", "typescript", "rust", "markdown"},
		types:          []string{"", "function", "class", "block", "comment", "generic"},
		sessionLoading: true,
		historyIdx:     -1,
	}
}

func newTextInput(prompt, placeholder string, width int) textinput.Model {
	input := textinput.New()
	input.Placeholder = placeholder
	input.Prompt = prompt + " "
	input.SetWidth(width)

	styles := textinput.DefaultDarkStyles()
	styles.Focused.Prompt = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styles.Focused.Placeholder = lipgloss.NewStyle().Foreground(colorMuted)
	styles.Focused.Text = lipgloss.NewStyle().Foreground(colorInk)
	styles.Blurred.Prompt = lipgloss.NewStyle().Foreground(colorMuted)
	styles.Blurred.Placeholder = lipgloss.NewStyle().Foreground(colorMuted)
	styles.Blurred.Text = lipgloss.NewStyle().Foreground(colorDim)
	input.SetStyles(styles)

	return input
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(loadSessionCmd(m.ctx, m.startDir), m.spinner.Tick)
}

// needsSpinner is true when a Charm spinner frame should keep animating.
func (m Model) needsSpinner() bool {
	return m.sessionLoading || m.searching || m.planningIndex || m.indexing
}

func loadSessionCmd(ctx context.Context, startDir string) tea.Cmd {
	return func() tea.Msg {
		session, err := app.OpenSession(ctx, startDir)
		if err != nil {
			if isLockHeldByDaemon(err) {
				if roSession, roErr := app.OpenReadOnlySession(ctx, startDir); roErr == nil {
					return finishSessionLoad(ctx, roSession, true)
				}
			}
			return sessionLoadedMsg{err: err}
		}
		return finishSessionLoad(ctx, session, false)
	}
}

func finishSessionLoad(ctx context.Context, session *app.Session, readOnly bool) sessionLoadedMsg {
	service := app.NewService(session)
	status, statusErr := service.Status(ctx)
	if statusErr != nil {
		_ = session.Close()
		return sessionLoadedMsg{err: statusErr}
	}
	readiness, _ := service.Readiness(ctx)
	branchName := ""
	if info, err := git.Detect(ctx, session.ProjectRoot); err == nil && info != nil {
		if info.Detached {
			branchName = "detached@" + info.Head
		} else {
			branchName = info.Branch
		}
	}
	return sessionLoadedMsg{
		session:    session,
		status:     status,
		readiness:  readiness,
		branchName: branchName,
		readOnly:   readOnly,
	}
}

func isLockHeldByDaemon(err error) bool {
	if err == nil {
		return false
	}
	if !strings.Contains(strings.ToLower(err.Error()), "locked") {
		return false
	}
	if dir, derr := config.GetGlobalConfigDir(); derr == nil {
		return daemon.IsRunning(dir)
	}
	return false
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Charm bubbles: keep spinner/progress animating independently of business msgs.
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.needsSpinner() {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)
	case progress.FrameMsg:
		var cmd tea.Cmd
		m.progBar, cmd = m.progBar.Update(msg)
		return m, tea.Batch(cmd)
	}

	switch msg := msg.(type) {
	case sessionLoadedMsg:
		m.sessionLoading = false
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.session = msg.session
		m.service = app.NewService(msg.session)
		m.status = msg.status
		m.readiness = msg.readiness
		m.hasReadiness = true
		m.branchName = msg.branchName
		m.mode = app.ParseSearchMode("", msg.session.Config.Search.DefaultMode)
		m.effectiveMode = m.mode
		m.readOnly = msg.readOnly
		m.applyLanguagesFromStatus()
		m.applyReadinessStatusMessage()
		return m, nil

	case statusLoadedMsg:
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.status = msg.status
		m.readiness = msg.readiness
		m.hasReadiness = true
		m.applyLanguagesFromStatus()
		if m.activeView == viewStatus {
			m.refreshOverlay()
		}
		return m, nil

	case searchLoadedMsg:
		if msg.gen != m.searchGen {
			return m, nil // stale
		}
		m.searching = false
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.results = msg.response.Results
		m.selected = 0
		m.lastQuery = msg.query
		m.lastDuration = msg.response.Duration
		m.warnings = msg.response.Warnings
		m.resultsDirty = false
		if msg.response.Mode != "" {
			m.effectiveMode = msg.response.Mode
		}
		m.pushQueryHistory(msg.query)
		m.statusMessage = fmt.Sprintf("%d results in %s", len(m.results), msg.response.Duration.Round(time.Millisecond))
		if len(m.warnings) > 0 {
			m.statusMessage += "  " + m.warnings[0]
		}
		if m.effectiveMode != "" && m.effectiveMode != m.mode {
			m.statusMessage += fmt.Sprintf("  (fell back to %s)", m.effectiveMode)
		}
		m.errMessage = ""
		m.rebuildResultList()
		m.sizeResultList()
		m.focus = focusResults
		m.applyFocus()
		m.updatePreview()
		return m, nil

	case indexDoneMsg:
		if msg.gen != m.indexGen {
			return m, nil
		}
		m.indexing = false
		m.daemonIndex = false
		m.indexProgress = nil
		m.indexRecent = nil
		m.indexCancel = nil
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) || strings.Contains(msg.err.Error(), "context canceled") {
				m.statusMessage = m.indexCanceledMessage()
				m.errMessage = ""
				return m, m.reloadStatusCmd()
			}
			m.errMessage = msg.err.Error()
			return m, nil
		}
		mode := "indexed"
		if msg.full {
			mode = "reindexed"
		}
		m.statusMessage = fmt.Sprintf("%s %d files, %d skipped, %d chunks in %s", mode, msg.result.FilesProcessed, msg.result.FilesSkipped, msg.result.ChunksCreated, msg.result.Duration.Round(time.Millisecond))
		m.errMessage = ""
		if m.focus == focusQuery || len(m.results) == 0 {
			m.focus = focusQuery
			m.applyFocus()
		}
		return m, m.reloadStatusCmd()

	case daemonReindexDoneMsg:
		if msg.gen != m.indexGen {
			return m, nil
		}
		m.indexing = false
		m.daemonIndex = false
		m.indexCancel = nil
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) || strings.Contains(msg.err.Error(), "context canceled") {
				m.statusMessage = m.indexCanceledMessage()
				m.errMessage = ""
				return m, m.reloadStatusCmd()
			}
			m.errMessage = fmt.Sprintf("daemon reindex failed: %v", msg.err)
			return m, nil
		}
		mode := "indexed"
		if msg.full {
			mode = "reindexed"
		}
		m.statusMessage = fmt.Sprintf("%s via daemon: %d files, %d skipped, %d chunks in %s", mode, msg.result.FilesProcessed, msg.result.FilesSkipped, msg.result.ChunksCreated, msg.result.Duration.Round(time.Millisecond))
		m.errMessage = ""
		return m, m.reloadStatusCmd()

	case indexProgressMsg:
		if msg.gen != m.indexGen {
			return m, nil
		}
		p := msg.progress
		m.indexProgress = &p
		// Prefer live walk path while discovering so recent files move.
		path := p.DisplayFile()
		if path == "" {
			path = p.CurrentFile
		}
		m.addIndexRecent(path)
		var barCmd tea.Cmd
		if p.WalkComplete {
			barCmd = m.progBar.SetPercent(p.HonestPercent())
		} else {
			// Indeterminate fill while discovering (does not imply completion).
			barCmd = m.progBar.SetPercent(0.08)
		}
		if m.indexing {
			return m, tea.Batch(waitForIndexProgressCmd(msg.gen, msg.ch), barCmd)
		}
		return m, barCmd

	case indexProgressClosedMsg:
		return m, nil

	case indexTickMsg:
		if msg.gen != m.indexGen || !m.indexing {
			return m, nil
		}
		// Force re-render elapsed time while indexing (esp. daemon path).
		return m, indexTickCmd(msg.gen)

	case dryRunLoadedMsg:
		m.planningIndex = false
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			m.statusMessage = "plan failed"
			return m, nil
		}
		m.dryRun = msg.preview
		m.pendingFull = msg.full
		// Confirm gates (Ollama-local friendly for small first indexes):
		// - full reindex always
		// - large plans (NeedsConfirm)
		// - empty projects with many files (wrong-folder protection)
		// Small empty projects auto-start after the plan.
		needConfirm := msg.full
		if msg.preview != nil {
			if msg.preview.NeedsConfirm() {
				needConfirm = true
			} else if m.isIndexEmpty() && msg.preview.ScannedFiles >= emptyIndexAutoStartMax {
				needConfirm = true
			}
		} else if m.isIndexEmpty() {
			needConfirm = true
		}
		if !needConfirm {
			m.dryRun = nil
			m.statusMessage = "plan ok — indexing"
			return m, m.indexCmd(msg.full)
		}
		if msg.full {
			m.confirm = confirmFullReindex
		} else {
			m.confirm = confirmIndex
		}
		m.statusMessage = "plan ready — confirm to index"
		return m, nil

	case deleteDoneMsg:
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.statusMessage = fmt.Sprintf("deleted %s (%d chunks)", msg.path, msg.deleted)
		m.errMessage = ""
		// Remove matching rows locally so empty-query delete stays consistent.
		m.removeResultsForPath(msg.path)
		if strings.TrimSpace(m.query.Value()) != "" {
			return m, tea.Batch(m.searchCmd(), m.reloadStatusCmd())
		}
		return m, m.reloadStatusCmd()

	case resetDoneMsg:
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.results = nil
		m.selected = 0
		m.rebuildResultList()
		m.preview.SetContent("")
		m.statusMessage = "index reset"
		m.errMessage = ""
		return m, m.reloadStatusCmd()

	case editorDoneMsg:
		if msg.err != nil {
			m.errMessage = msg.err.Error()
		}
		return m, nil

	case yankDoneMsg:
		if msg.err != nil {
			m.errMessage = "clipboard: " + msg.err.Error()
			return m, nil
		}
		m.statusMessage = "yanked " + truncateDisplay(msg.text, 48)
		m.errMessage = ""
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		if m.activeView != viewSearch {
			m.refreshOverlay()
		}
		if m.needsSpinner() {
			cmds = append(cmds, m.spinner.Tick)
		}
		return m, tea.Batch(cmds...)

	case tea.KeyPressMsg:
		cmd, handled := m.handleKey(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if handled {
			if m.needsSpinner() {
				cmds = append(cmds, m.spinner.Tick)
			}
			return m, tea.Batch(cmds...)
		}

	case tea.MouseWheelMsg:
		cmd := m.handleMouseWheel(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		return m, tea.Batch(cmds...)

	case tea.MouseClickMsg:
		m.handleMouseClick(msg)
		return m, nil
	}

	if m.activeView == viewSearch && m.confirm == confirmNone {
		var cmd tea.Cmd
		switch m.focus {
		case focusQuery:
			m.query, cmd = m.query.Update(msg)
			// History browse with empty query is handled in keys; while typing reset idx.
			if key, ok := msg.(tea.KeyPressMsg); ok {
				ks := key.Keystroke()
				if ks != "up" && ks != "down" {
					m.historyIdx = -1
				}
			}
		case focusDirectory:
			m.directory, cmd = m.directory.Update(msg)
			m.markResultsDirty()
		case focusFilePattern:
			m.filePattern, cmd = m.filePattern.Update(msg)
			m.markResultsDirty()
		case focusLineRange:
			m.lineRange, cmd = m.lineRange.Update(msg)
			m.markResultsDirty()
		case focusPreview:
			m.preview, cmd = m.preview.Update(msg)
		}
		cmds = append(cmds, cmd)
	} else if m.activeView != viewSearch && m.confirm == confirmNone {
		var cmd tea.Cmd
		m.overlay, cmd = m.overlay.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) applyReadinessStatusMessage() {
	if m.readOnly {
		m.statusMessage = "ready (read-only — daemon owns the index)"
		return
	}
	if !m.hasReadiness {
		if m.isIndexEmpty() {
			m.statusMessage = "empty index — press r to index"
		} else {
			m.statusMessage = "ready"
		}
		return
	}
	switch m.readiness.State {
	case app.ReadinessEmpty:
		m.statusMessage = "empty index — press r to plan & index"
	case app.ReadinessProfileMismatch:
		m.statusMessage = "profile mismatch — press R to full reindex"
	case app.ReadinessStale:
		m.statusMessage = "index stale — press r to update"
	case app.ReadinessUnknown:
		m.statusMessage = "freshness unknown — press R to rebuild"
	default:
		m.statusMessage = "ready"
	}
}

func (m *Model) applyLanguagesFromStatus() {
	if m.status == nil || m.status.DetailedStats == nil || len(m.status.DetailedStats.Languages) == 0 {
		return
	}
	current := ""
	if m.langIdx >= 0 && m.langIdx < len(m.languages) {
		current = m.languages[m.langIdx]
	}
	langs := make([]string, 0, len(m.status.DetailedStats.Languages)+1)
	langs = append(langs, "")
	for name := range m.status.DetailedStats.Languages {
		if name != "" {
			langs = append(langs, name)
		}
	}
	sort.Strings(langs[1:])
	m.languages = langs
	m.langIdx = 0
	for i, l := range m.languages {
		if l == current {
			m.langIdx = i
			break
		}
	}

	if len(m.status.DetailedStats.ChunkTypes) > 0 {
		curType := ""
		if m.typeIdx >= 0 && m.typeIdx < len(m.types) {
			curType = m.types[m.typeIdx]
		}
		types := make([]string, 0, len(m.status.DetailedStats.ChunkTypes)+1)
		types = append(types, "")
		for name := range m.status.DetailedStats.ChunkTypes {
			if name != "" {
				types = append(types, name)
			}
		}
		sort.Strings(types[1:])
		m.types = types
		m.typeIdx = 0
		for i, t := range m.types {
			if t == curType {
				m.typeIdx = i
				break
			}
		}
	}
}

func (m *Model) markResultsDirty() {
	if m.lastQuery != "" && len(m.results) > 0 {
		m.resultsDirty = true
	}
}

func (m *Model) pushQueryHistory(q string) {
	q = strings.TrimSpace(q)
	if q == "" {
		return
	}
	// De-dupe consecutive.
	if len(m.queryHistory) > 0 && m.queryHistory[len(m.queryHistory)-1] == q {
		m.historyIdx = -1
		return
	}
	m.queryHistory = append(m.queryHistory, q)
	if len(m.queryHistory) > maxQueryHistory {
		m.queryHistory = m.queryHistory[len(m.queryHistory)-maxQueryHistory:]
	}
	m.historyIdx = -1
}

func (m Model) indexCanceledMessage() string {
	root := ""
	if m.session != nil && m.session.ProjectRoot != "" {
		root = m.session.ProjectRoot
	} else if m.status != nil {
		root = m.status.ProjectRoot
	}
	if root == "" {
		return "index canceled"
	}
	return "index canceled — root was " + truncateDisplay(root, 48)
}

func (m Model) projectRootDisplay() string {
	if m.session != nil && m.session.ProjectRoot != "" {
		return m.session.ProjectRoot
	}
	if m.status != nil && m.status.ProjectRoot != "" {
		return m.status.ProjectRoot
	}
	return ""
}

// shouldPlanBeforeIndex decides whether r needs dry-run before embed.
func (m Model) shouldPlanBeforeIndex() bool {
	if m.isIndexEmpty() {
		return true
	}
	if m.status != nil && m.status.PendingChanges != nil {
		p := m.status.PendingChanges
		if p.NewFiles+p.ModifiedFiles >= index.ConfirmScopeFiles {
			return true
		}
	}
	return false
}

func (m *Model) removeResultsForPath(path string) {
	if path == "" || len(m.results) == 0 {
		return
	}
	out := make([]search.Result, 0, len(m.results))
	for _, r := range m.results {
		if r.RelativePath == path || r.FilePath == path {
			continue
		}
		out = append(out, r)
	}
	m.results = out
	m.rebuildResultList()
	if len(m.results) == 0 {
		m.selected = 0
		m.preview.SetContent("")
		return
	}
	m.selected = clamp(m.selected, 0, len(m.results)-1)
	m.resultList.Select(m.selected)
	m.updatePreview()
}
