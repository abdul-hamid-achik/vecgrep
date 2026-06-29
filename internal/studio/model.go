package studio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/daemon"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

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
	confirmReset
)

type Model struct {
	ctx      context.Context
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

	activeView viewName
	focus      focusArea

	mode      search.SearchMode
	limit     int
	languages []string
	langIdx   int
	types     []string
	typeIdx   int

	results       []search.Result
	selected      int
	lastQuery     string
	lastDuration  time.Duration
	loading       bool
	indexing      bool
	indexProgress *index.Progress
	indexRecent   []string
	statusMessage string
	errMessage    string
	confirm       confirmAction
	readOnly      bool // true when opened read-only because the daemon owns the write lock
}

type sessionLoadedMsg struct {
	session *app.Session
	status  *app.StatusResponse
	err     error
	readOnly bool
}

type statusLoadedMsg struct {
	status *app.StatusResponse
	err    error
}

type searchLoadedMsg struct {
	query    string
	response *app.SearchResponse
	err      error
}

type indexDoneMsg struct {
	result *index.IndexResult
	err    error
	full   bool
}

type indexProgressMsg struct {
	progress index.Progress
	ch       <-chan index.Progress
}

type indexProgressClosedMsg struct{}

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

func NewModel(ctx context.Context, startDir string) Model {
	ti := newTextInput("query", "Search code by meaning...", 80)
	ti.Focus()

	dir := newTextInput("dir", "internal/", 24)

	file := newTextInput("file", "**/*_test.go", 28)

	lines := newTextInput("lines", "1-120", 18)

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(12))
	vp.SoftWrap = false

	return Model{
		ctx:         ctx,
		startDir:    startDir,
		query:       ti,
		directory:   dir,
		filePattern: file,
		lineRange:   lines,
		preview:     vp,
		activeView:  viewSearch,
		focus:       focusQuery,
		mode:        search.SearchModeHybrid,
		limit:       10,
		languages:   []string{"", "go", "python", "javascript", "typescript", "rust", "markdown"},
		types:       []string{"", "function", "class", "block", "comment", "generic"},
		loading:     true,
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
	return loadSessionCmd(m.ctx, m.startDir)
}
func loadSessionCmd(ctx context.Context, startDir string) tea.Cmd {
	return func() tea.Msg {
		session, err := app.OpenSession(ctx, startDir)
		if err != nil {
			// If the write lock is held by a running daemon, fall back to a
			// read-only session so the studio can still browse/search. Writes
			// (reindex/reset/delete) are then routed to the daemon or blocked.
			if isLockHeldByDaemon(err) {
				if roSession, roErr := app.OpenReadOnlySession(ctx, startDir); roErr == nil {
					roService := app.NewService(roSession)
					status, statusErr := roService.Status(ctx)
					if statusErr != nil {
						_ = roSession.Close()
						return sessionLoadedMsg{err: statusErr}
					}
					return sessionLoadedMsg{session: roSession, status: status, readOnly: true}
				}
			}
			return sessionLoadedMsg{err: err}
		}
		service := app.NewService(session)
		status, statusErr := service.Status(ctx)
		if statusErr != nil {
			_ = session.Close()
			return sessionLoadedMsg{err: statusErr}
		}
		return sessionLoadedMsg{session: session, status: status}
	}
}

// isLockHeldByDaemon reports whether the open error is a veclite lock error
// AND a daemon hub is currently running (so a read-only fallback is safe and
// writes can be delegated to the daemon).
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

	switch msg := msg.(type) {
	case sessionLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.session = msg.session
		m.service = app.NewService(msg.session)
		m.status = msg.status
		m.mode = app.ParseSearchMode("", msg.session.Config.Search.DefaultMode)
		m.readOnly = msg.readOnly
		if msg.readOnly {
			m.statusMessage = "ready (read-only — daemon owns the index)"
		} else {
			m.statusMessage = "ready"
		}
		return m, nil

	case statusLoadedMsg:
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.status = msg.status
		return m, nil

	case searchLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.results = msg.response.Results
		m.selected = 0
		m.lastQuery = msg.query
		m.lastDuration = msg.response.Duration
		m.statusMessage = fmt.Sprintf("%d results in %s", len(m.results), msg.response.Duration.Round(time.Millisecond))
		m.errMessage = ""
		m.updatePreview()
		return m, nil

	case indexDoneMsg:
		m.indexing = false
		m.indexProgress = nil
		m.indexRecent = nil
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		mode := "indexed"
		if msg.full {
			mode = "reindexed"
		}
		m.statusMessage = fmt.Sprintf("%s %d files, %d skipped, %d chunks in %s", mode, msg.result.FilesProcessed, msg.result.FilesSkipped, msg.result.ChunksCreated, msg.result.Duration.Round(time.Millisecond))
		m.errMessage = ""
		return m, m.reloadStatusCmd()

	case daemonReindexDoneMsg:
		m.indexing = false
		if msg.err != nil {
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
		progress := msg.progress
		m.indexProgress = &progress
		m.addIndexRecent(progress.CurrentFile)
		if m.indexing {
			return m, waitForIndexProgressCmd(msg.ch)
		}
		return m, nil

	case indexProgressClosedMsg:
		return m, nil

	case deleteDoneMsg:
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.statusMessage = fmt.Sprintf("deleted %s (%d chunks)", msg.path, msg.deleted)
		m.errMessage = ""
		return m, tea.Batch(m.searchCmd(), m.reloadStatusCmd())

	case resetDoneMsg:
		if msg.err != nil {
			m.errMessage = msg.err.Error()
			return m, nil
		}
		m.results = nil
		m.selected = 0
		m.preview.SetContent("")
		m.statusMessage = "index reset"
		m.errMessage = ""
		return m, m.reloadStatusCmd()

	case editorDoneMsg:
		if msg.err != nil {
			m.errMessage = msg.err.Error()
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil

	case tea.KeyPressMsg:
		cmd, handled := m.handleKey(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if handled {
			return m, tea.Batch(cmds...)
		}
	}

	if m.activeView == viewSearch && m.confirm == confirmNone {
		var cmd tea.Cmd
		switch m.focus {
		case focusQuery:
			m.query, cmd = m.query.Update(msg)
		case focusDirectory:
			m.directory, cmd = m.directory.Update(msg)
		case focusFilePattern:
			m.filePattern, cmd = m.filePattern.Update(msg)
		case focusLineRange:
			m.lineRange, cmd = m.lineRange.Update(msg)
		case focusPreview:
			m.preview, cmd = m.preview.Update(msg)
		}
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	key := msg.Keystroke()

	if m.confirm != confirmNone {
		switch key {
		case "y", "Y":
			action := m.confirm
			m.confirm = confirmNone
			switch action {
			case confirmDelete:
				return m.deleteSelectedCmd(), true
			case confirmFullReindex:
				return m.indexCmd(true), true
			case confirmReset:
				return m.resetCmd(), true
			}
		case "n", "N", "esc":
			m.confirm = confirmNone
			m.statusMessage = "cancelled"
			return nil, true
		}
		return nil, true
	}

	if m.session == nil {
		switch key {
		case "ctrl+c", "q":
			return tea.Quit, true
		case "i":
			return m.initProjectCmd(), true
		}
	}

	if m.activeView == viewSearch && m.isTextFocus() {
		switch key {
		case "ctrl+c":
			return tea.Quit, true
		case "?":
			m.activeView = viewHelp
			return nil, true
		case "esc":
			m.activeView = viewSearch
			m.focus = focusResults
			m.applyFocus()
			return nil, true
		case "tab":
			m.nextFocus()
			return nil, true
		case "shift+tab":
			m.prevFocus()
			return nil, true
		case "ctrl+f":
			m.focus = focusQuery
			m.applyFocus()
			return nil, true
		case "enter":
			return m.searchCmd(), true
		}
		return nil, false
	}

	switch key {
	case "ctrl+c", "q":
		return tea.Quit, true
	case "?":
		m.activeView = viewHelp
		return nil, true
	case "esc":
		m.activeView = viewSearch
		m.focus = focusQuery
		m.applyFocus()
		return nil, true
	case "tab":
		m.nextFocus()
		return nil, true
	case "shift+tab":
		m.prevFocus()
		return nil, true
	case "ctrl+f", "/":
		m.activeView = viewSearch
		m.focus = focusQuery
		m.applyFocus()
		return nil, true
	case "enter":
		if m.activeView == viewSearch && m.focus == focusQuery {
			return m.searchCmd(), true
		}
		if m.activeView == viewSearch && m.focus == focusResults {
			return m.openEditorCmd(), true
		}
	case "up", "k":
		if m.activeView == viewSearch && m.focus == focusResults {
			m.moveSelection(-1)
			return nil, true
		}
	case "down", "j":
		if m.activeView == viewSearch && m.focus == focusResults {
			m.moveSelection(1)
			return nil, true
		}
	case "pgup", "u":
		if m.activeView == viewSearch {
			m.preview.PageUp()
			m.focus = focusPreview
			return nil, true
		}
	case "pgdown", "d":
		if m.activeView == viewSearch {
			m.preview.PageDown()
			m.focus = focusPreview
			return nil, true
		}
	case "m":
		m.cycleMode()
		return nil, true
	case "+", "=":
		m.limit = clamp(m.limit+5, 1, 100)
		return nil, true
	case "-":
		m.limit = clamp(m.limit-5, 1, 100)
		return nil, true
	case "L":
		m.langIdx = (m.langIdx + 1) % len(m.languages)
		return nil, true
	case "T":
		m.typeIdx = (m.typeIdx + 1) % len(m.types)
		return nil, true
	case "v":
		m.activeView = viewStatus
		return m.reloadStatusCmd(), true
	case "c":
		m.activeView = viewConfig
		return nil, true
	case "s":
		return m.similarSelectedCmd(), true
	case "r":
		return m.indexCmd(false), true
	case "R":
		m.confirm = confirmFullReindex
		return nil, true
	case "x":
		if m.readOnly {
			m.statusMessage = "can't delete in read-only mode — stop the daemon (`vecgrep daemon stop`) first"
			return nil, true
		}
		if len(m.results) > 0 {
			m.confirm = confirmDelete
			return nil, true
		}
	case "!":
		if m.readOnly {
			m.statusMessage = "can't reset in read-only mode — stop the daemon (`vecgrep daemon stop`) first"
			return nil, true
		}
		m.confirm = confirmReset
		return nil, true
	case "o":
		return m.openEditorCmd(), true
	}
	return nil, false
}

func (m *Model) nextFocus() {
	m.focus = (m.focus + 1) % focusCount
	m.applyFocus()
}

func (m *Model) prevFocus() {
	if m.focus == 0 {
		m.focus = focusPreview
	} else {
		m.focus--
	}
	m.applyFocus()
}

func (m *Model) applyFocus() {
	m.query.Blur()
	m.directory.Blur()
	m.filePattern.Blur()
	m.lineRange.Blur()

	if m.focus == focusQuery {
		m.query.Focus()
		return
	}
	if m.focus == focusDirectory {
		m.directory.Focus()
		return
	}
	if m.focus == focusFilePattern {
		m.filePattern.Focus()
		return
	}
	if m.focus == focusLineRange {
		m.lineRange.Focus()
		return
	}
}

func (m Model) isTextFocus() bool {
	return m.focus == focusQuery ||
		m.focus == focusDirectory ||
		m.focus == focusFilePattern ||
		m.focus == focusLineRange
}

func (m *Model) moveSelection(delta int) {
	if len(m.results) == 0 {
		return
	}
	m.selected = clamp(m.selected+delta, 0, len(m.results)-1)
	m.updatePreview()
}

func (m *Model) cycleMode() {
	switch m.mode {
	case search.SearchModeHybrid:
		m.mode = search.SearchModeSemantic
	case search.SearchModeSemantic:
		m.mode = search.SearchModeKeyword
	default:
		m.mode = search.SearchModeHybrid
	}
}

func (m *Model) searchCmd() tea.Cmd {
	if m.service == nil || strings.TrimSpace(m.query.Value()) == "" {
		return nil
	}
	query := strings.TrimSpace(m.query.Value())
	minLine, maxLine := app.ParseLineRange(strings.TrimSpace(m.lineRange.Value()))
	req := app.SearchRequest{
		Query:       query,
		Limit:       m.limit,
		Mode:        m.mode,
		Language:    m.languages[m.langIdx],
		ChunkType:   m.types[m.typeIdx],
		Directory:   strings.TrimSpace(m.directory.Value()),
		FilePattern: strings.TrimSpace(m.filePattern.Value()),
		MinLine:     minLine,
		MaxLine:     maxLine,
	}
	m.loading = true
	m.statusMessage = "searching"
	m.errMessage = ""
	return func() tea.Msg {
		resp, err := m.service.Search(m.ctx, req)
		return searchLoadedMsg{query: query, response: resp, err: err}
	}
}

func (m *Model) reloadStatusCmd() tea.Cmd {
	if m.service == nil {
		return nil
	}
	return func() tea.Msg {
		status, err := m.service.Status(m.ctx)
		return statusLoadedMsg{status: status, err: err}
	}
}

func (m *Model) similarSelectedCmd() tea.Cmd {
	if m.service == nil || len(m.results) == 0 {
		return nil
	}
	selected := m.results[m.selected]
	minLine, maxLine := app.ParseLineRange(strings.TrimSpace(m.lineRange.Value()))
	req := app.SimilarRequest{
		Target: app.SimilarTarget{
			Kind:    app.SimilarTargetID,
			ChunkID: selected.ChunkID,
		},
		Limit:           m.limit,
		Language:        m.languages[m.langIdx],
		ChunkType:       m.types[m.typeIdx],
		Directory:       strings.TrimSpace(m.directory.Value()),
		FilePattern:     strings.TrimSpace(m.filePattern.Value()),
		MinLine:         minLine,
		MaxLine:         maxLine,
		ExcludeSameFile: true,
	}
	m.loading = true
	m.statusMessage = "finding similar"
	return func() tea.Msg {
		resp, err := m.service.Similar(m.ctx, req)
		return searchLoadedMsg{query: "similar " + selected.RelativePath, response: resp, err: err}
	}
}

func (m *Model) indexCmd(full bool) tea.Cmd {
	if m.service == nil {
		return nil
	}
	// Read-only mode (daemon owns the write lock): delegate the reindex to the
	// daemon over its socket instead of calling service.Index on a read-only
	// session (which can't write).
	if m.readOnly {
		return m.daemonReindexCmd(full)
	}
	m.indexing = true
	start := time.Now()
	m.indexProgress = &index.Progress{StartTime: start}
	m.indexRecent = nil
	m.errMessage = ""
	if full {
		m.statusMessage = "full reindexing"
	} else {
		m.statusMessage = "indexing"
	}
	req := app.IndexRequest{FullReindex: full}
	progressCh := make(chan index.Progress, 32)
	indexTask := func() tea.Msg {
		defer close(progressCh)
		result, err := m.service.Index(m.ctx, req, func(progress index.Progress) {
			select {
			case progressCh <- progress:
			default:
			}
		})
		return indexDoneMsg{result: result, err: err, full: full}
	}
	return tea.Batch(waitForIndexProgressCmd(progressCh), indexTask)
}

// daemonReindexDoneMsg is returned by daemonReindexCmd when the daemon finishes
// the delegated reindex (or it fails / the daemon is unreachable).
type daemonReindexDoneMsg struct {
	result *index.IndexResult
	err    error
	full   bool
}

// daemonReindexCmd delegates a reindex to the running daemon hub and reports
// the result. Used in read-only mode, where the studio can't write directly.
// There's no live progress stream (the RPC is synchronous), so the status bar
// just shows "reindexing via daemon" until it completes.
func (m *Model) daemonReindexCmd(full bool) tea.Cmd {
	if m.service == nil || m.session == nil {
		return nil
	}
	m.indexing = true
	m.indexRecent = nil
	m.errMessage = ""
	if full {
		m.statusMessage = "full reindexing via daemon"
	} else {
		m.statusMessage = "indexing via daemon"
	}
	root := m.session.ProjectRoot
	return func() tea.Msg {
		dir, _ := config.GetGlobalConfigDir()
		result, err := daemon.ReindexSync(m.ctx, dir, root, full)
		return daemonReindexDoneMsg{result: result, err: err, full: full}
	}
}

func waitForIndexProgressCmd(ch <-chan index.Progress) tea.Cmd {
	return func() tea.Msg {
		progress, ok := <-ch
		if !ok {
			return indexProgressClosedMsg{}
		}
		return indexProgressMsg{progress: progress, ch: ch}
	}
}

func (m *Model) addIndexRecent(path string) {
	if path == "" {
		return
	}
	displayPath := path
	if m.session != nil && m.session.ProjectRoot != "" {
		if rel, err := filepath.Rel(m.session.ProjectRoot, path); err == nil && !strings.HasPrefix(rel, "..") {
			displayPath = rel
		}
	}
	displayPath = filepath.ToSlash(displayPath)
	if len(m.indexRecent) > 0 && m.indexRecent[len(m.indexRecent)-1] == displayPath {
		return
	}
	m.indexRecent = append(m.indexRecent, displayPath)
	if len(m.indexRecent) > 5 {
		m.indexRecent = m.indexRecent[len(m.indexRecent)-5:]
	}
}

func (m *Model) initProjectCmd() tea.Cmd {
	if m.loading {
		return nil
	}
	m.loading = true
	m.errMessage = ""
	m.statusMessage = "registering project"
	return func() tea.Msg {
		if _, err := app.InitGlobalProject(m.ctx, m.startDir, false); err != nil {
			return sessionLoadedMsg{err: err}
		}
		return loadSessionCmd(m.ctx, m.startDir)()
	}
}

func (m *Model) deleteSelectedCmd() tea.Cmd {
	if m.service == nil || len(m.results) == 0 {
		return nil
	}
	path := m.results[m.selected].RelativePath
	if path == "" {
		path = m.results[m.selected].FilePath
	}
	return func() tea.Msg {
		deleted, err := m.service.DeleteFile(m.ctx, path)
		return deleteDoneMsg{path: path, deleted: deleted, err: err}
	}
}

func (m *Model) resetCmd() tea.Cmd {
	if m.service == nil {
		return nil
	}
	return func() tea.Msg {
		err := m.service.Reset(m.ctx, app.ResetProject)
		return resetDoneMsg{err: err}
	}
}

func (m *Model) openEditorCmd() tea.Cmd {
	if len(m.results) == 0 {
		return nil
	}
	result := m.results[m.selected]
	path := result.FilePath
	if path == "" && m.session != nil {
		path = filepath.Join(m.session.ProjectRoot, result.RelativePath)
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	arg := fmt.Sprintf("+%d", result.StartLine)
	cmd := exec.Command(editor, arg, path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{err: err}
	})
}

func (m *Model) updatePreview() {
	if len(m.results) == 0 || m.selected < 0 || m.selected >= len(m.results) {
		m.preview.SetContent("")
		return
	}
	result := m.results[m.selected]
	var b strings.Builder
	fmt.Fprintf(&b, "%s:%d-%d", result.RelativePath, result.StartLine, result.EndLine)
	if result.SymbolName != "" {
		b.WriteString("  ")
		b.WriteString(result.SymbolName)
	}
	if result.Language != "" {
		b.WriteString("  ")
		b.WriteString(result.Language)
	}
	b.WriteString("\n\n")

	lines := strings.Split(result.Content, "\n")
	for i, line := range lines {
		lineNo := result.StartLine + i
		fmt.Fprintf(&b, "%5d │ %s\n", lineNo, line)
	}
	m.preview.SetContent(b.String())
	m.preview.GotoTop()
}

func (m *Model) resize() {
	if m.width <= 0 {
		m.width = 100
	}
	if m.height <= 0 {
		m.height = 30
	}
	m.query.SetWidth(clamp(m.width-20, 20, 120))
	filterWidth := clamp((m.width-12)/3, 18, 36)
	if m.width < 80 {
		filterWidth = clamp(m.width-14, 20, 80)
	}
	m.directory.SetWidth(clamp(filterWidth-8, 8, 64))
	m.filePattern.SetWidth(clamp(filterWidth-9, 8, 64))
	m.lineRange.SetWidth(clamp(filterWidth-10, 8, 64))

	previewHeight := clamp(m.height-12, 6, 30)
	previewWidth := clamp(m.width/2-4, 30, m.width-4)
	if m.width < 96 {
		previewWidth = clamp(m.width-4, 30, m.width-4)
		previewHeight = clamp(m.height/2-2, 6, 24)
	}
	m.preview.SetWidth(previewWidth)
	m.preview.SetHeight(previewHeight)
}

func (m Model) View() tea.View {
	content := m.render()
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m Model) render() string {
	if m.width == 0 {
		return "vecgrep\n\nloading..."
	}

	header := m.renderHeader()
	body := ""
	switch m.activeView {
	case viewStatus:
		body = m.renderStatus()
	case viewConfig:
		body = m.renderConfig()
	case viewHelp:
		body = m.renderHelp()
	default:
		body = m.renderSearch()
	}

	footer := m.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m Model) renderHeader() string {
	project := "no project"
	if m.status != nil {
		project = m.status.ProjectRoot
	}
	left := titleStyle.Render("vecgrep") + " " + mutedStyle.Render(truncateDisplay(project, clamp(m.width/2, 24, 80)))
	rightParts := []string{fmt.Sprintf("mode %s", m.mode), fmt.Sprintf("limit %d", m.limit)}
	if m.readOnly {
		rightParts = append([]string{"read-only"}, rightParts...)
	}
	if m.status != nil {
		rightParts = append(rightParts,
			fmt.Sprintf("files %d", m.status.Stats["files"]),
			fmt.Sprintf("chunks %d", m.status.Stats["chunks"]),
		)
	}
	right := mutedStyle.Render(strings.Join(rightParts, "  "))
	if m.width > lipgloss.Width(left)+lipgloss.Width(right)+2 {
		return left + strings.Repeat(" ", m.width-lipgloss.Width(left)-lipgloss.Width(right)) + right
	}
	return left
}

func (m Model) renderSearch() string {
	if m.loading && m.session == nil {
		return m.renderPanel("Project", "loading project...", m.width-2, false)
	}
	if m.session == nil {
		return m.renderPanel("Project", m.renderUnavailableProject(), m.width-2, false)
	}

	query := m.renderQuery()
	filterInputs := m.renderFilterInputs()
	filters := m.renderFilterBar()

	if m.width >= 96 {
		listWidth := clamp(m.width-m.preview.Width()-6, 35, m.width-4)
		results := m.renderPanel(m.resultsTitle(), m.renderResults(listWidth-4), listWidth, m.focus == focusResults)
		preview := m.renderPanel(m.previewTitle(), m.renderPreview(), m.preview.Width(), m.focus == focusPreview)
		return lipgloss.JoinVertical(lipgloss.Left,
			query,
			filterInputs,
			filters,
			lipgloss.JoinHorizontal(lipgloss.Top, results, "  ", preview),
		)
	}

	results := m.renderPanel(m.resultsTitle(), m.renderResults(m.width-6), m.width-2, m.focus == focusResults)
	preview := m.renderPanel(m.previewTitle(), m.renderPreview(), m.width-2, m.focus == focusPreview)
	return lipgloss.JoinVertical(lipgloss.Left,
		query,
		filterInputs,
		filters,
		results,
		preview,
	)
}

func (m Model) renderUnavailableProject() string {
	if m.errMessage != "" && !strings.Contains(m.errMessage, "not in a vecgrep project") {
		msg := "Could not open this project.\n\n" + m.errMessage
		// Only suggest the destructive `reset --force` for a genuine
		// old-version/corrupt index — never for a live lock held by another
		// running vecgrep process. The lock error already carries its own
		// "stop the other process" guidance.
		if !strings.Contains(m.errMessage, "locked") {
			msg += "\n\nIf the index was created by an older vecgrep/veclite version, run `vecgrep reset --force` and then `vecgrep index`."
		}
		return msg + "\n\nctrl+c quits"
	}
	return "No vecgrep project found.\n\nPress i to register this folder in ~/.vecgrep/projects, or run `vecgrep init --local` to keep state in the repo.\n\nctrl+c quits"
}

func (m Model) renderQuery() string {
	return m.renderPanel("Search", m.query.View(), m.width-2, m.focus == focusQuery)
}

func (m Model) renderFilterInputs() string {
	if m.width < 80 {
		return lipgloss.JoinVertical(lipgloss.Left,
			m.renderPanel("Directory", m.directory.View(), m.width-2, m.focus == focusDirectory),
			m.renderPanel("File glob", m.filePattern.View(), m.width-2, m.focus == focusFilePattern),
			m.renderPanel("Line range", m.lineRange.View(), m.width-2, m.focus == focusLineRange),
		)
	}
	filterWidth := clamp((m.width-12)/3, 18, 36)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderPanel("Directory", m.directory.View(), filterWidth, m.focus == focusDirectory),
		"  ",
		m.renderPanel("File glob", m.filePattern.View(), filterWidth, m.focus == focusFilePattern),
		"  ",
		m.renderPanel("Line range", m.lineRange.View(), filterWidth, m.focus == focusLineRange),
	)
}

func (m Model) renderFilterBar() string {
	chips := []string{
		m.renderChip("mode "+string(m.mode), false),
		m.renderChip("lang "+displayValue(m.languages[m.langIdx], "all"), m.languages[m.langIdx] != ""),
		m.renderChip("type "+displayValue(m.types[m.typeIdx], "all"), m.types[m.typeIdx] != ""),
		m.renderChip(fmt.Sprintf("limit %d", m.limit), false),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, chips[0], " ", chips[1], " ", chips[2], " ", chips[3])
}

func (m Model) renderChip(label string, active bool) string {
	if active {
		return activeChipStyle.Render(label)
	}
	return chipStyle.Render(label)
}

func (m Model) renderPanel(title, body string, width int, focused bool) string {
	if width < 20 {
		width = 20
	}
	titleText := truncateDisplay(title, width-2)
	titleStyle := panelTitleStyle
	style := panelStyle
	if focused {
		titleStyle = activePanelTitleStyle
		style = focusedPanelStyle
	}
	content := lipgloss.JoinVertical(lipgloss.Left, titleStyle.Render(titleText), body)
	return style.Width(width).Render(content)
}

func (m Model) resultsTitle() string {
	if len(m.results) == 0 {
		return "Results"
	}
	title := fmt.Sprintf("Results %d/%d", m.selected+1, len(m.results))
	if m.lastDuration > 0 {
		title += " in " + m.lastDuration.Round(time.Millisecond).String()
	}
	return title
}

func (m Model) previewTitle() string {
	if len(m.results) == 0 || m.selected < 0 || m.selected >= len(m.results) {
		return "Preview"
	}
	result := m.results[m.selected]
	path := result.RelativePath
	if path == "" {
		path = result.FilePath
	}
	return fmt.Sprintf("Preview %s:%d", path, result.StartLine)
}

func (m Model) renderPreview() string {
	if len(m.results) == 0 {
		return mutedStyle.Render("Select a result to preview.")
	}
	return m.preview.View()
}

func (m Model) renderResults(width int) string {
	if m.indexing {
		return m.renderIndexProgress(width)
	}
	if m.loading {
		return mutedStyle.Render("Searching...")
	}
	if len(m.results) == 0 {
		if m.lastQuery == "" {
			return mutedStyle.Render("Type a query and press enter.")
		}
		return mutedStyle.Render("No results.")
	}

	maxRows := clamp(m.height-14, 5, 24)
	start := 0
	if m.selected >= maxRows {
		start = m.selected - maxRows + 1
	}
	end := clamp(start+maxRows, 0, len(m.results))

	var b strings.Builder
	for i := start; i < end; i++ {
		line := m.renderResultRow(i, m.results[i], width)
		if i == m.selected {
			b.WriteString(activeStyle.Width(width).Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderResultRow(index int, result search.Result, width int) string {
	marker := " "
	if index == m.selected {
		marker = ">"
	}
	path := result.RelativePath
	if path == "" {
		path = result.FilePath
	}
	if path == "" {
		path = "(unknown)"
	}
	row := fmt.Sprintf("%s %2d  %.2f  %s:%d", marker, index+1, result.Score, path, result.StartLine)

	var meta []string
	if result.Language != "" {
		meta = append(meta, result.Language)
	}
	if result.ChunkType != "" {
		meta = append(meta, result.ChunkType)
	}
	if result.SymbolName != "" {
		meta = append(meta, result.SymbolName)
	}
	if len(meta) > 0 {
		row += "  " + strings.Join(meta, " ")
	}

	return truncateDisplay(row, width)
}

func (m Model) renderIndexProgress(width int) string {
	progress := m.indexProgress
	if progress == nil {
		return mutedStyle.Render("Indexing current project...")
	}

	total := progress.TotalFiles
	processed := progress.ProcessedFiles
	bar := progressBar(processed, total, clamp(width-22, 10, 36))
	elapsed := ""
	rateStr := ""
	etaStr := ""
	if !progress.StartTime.IsZero() {
		elapsedDur := time.Since(progress.StartTime)
		elapsed = "  " + elapsedDur.Round(time.Second).String()
		// Show rate and ETA after at least 1 file is processed and > 1s
		// elapsed to avoid misleading early rates.
		if processed > 0 && elapsedDur > time.Second {
			rate := float64(processed) / elapsedDur.Seconds()
			rateStr = fmt.Sprintf("%.1f files/s", rate)
			remaining := total - processed
			if remaining > 0 && rate > 0 {
				eta := time.Duration(float64(remaining) / rate * float64(time.Second))
				etaStr = "ETA " + formatETA(eta)
			}
		}
	}

	lines := []string{
		fmt.Sprintf("%s  %d/%d files%s", bar, processed, total, elapsed),
		fmt.Sprintf("%d skipped  %d chunks", progress.SkippedFiles, progress.TotalChunks),
	}
	if rateStr != "" {
		line := rateStr
		if etaStr != "" {
			line += "  " + etaStr
		}
		lines = append(lines, line)
	}
	if progress.CurrentFile != "" {
		lines = append(lines, "Current: "+truncateDisplay(displayIndexPath(m.session, progress.CurrentFile), width-9))
	}
	if len(m.indexRecent) > 0 {
		lines = append(lines, "", "Recent")
		for _, path := range m.indexRecent {
			lines = append(lines, "  "+truncateDisplay(path, width-4))
		}
	}
	if len(progress.Errors) > 0 {
		lines = append(lines, "", warnStyle.Render(fmt.Sprintf("%d warnings", len(progress.Errors))))
	}
	return strings.Join(lines, "\n")
}

// formatETA formats a duration as a human-readable ETA string:
// < 60s = "Xs", < 60m = "Xm Ys", else = "Xh Ym".
func formatETA(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	remainingSeconds := seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, remainingSeconds)
	}
	hours := minutes / 60
	remainingMinutes := minutes % 60
	return fmt.Sprintf("%dh %dm", hours, remainingMinutes)
}

func (m Model) renderStatus() string {
	if m.status == nil {
		return m.renderPanel("Status", "status unavailable", m.width-2, true)
	}

	fresh := "unknown"
	if m.status.PendingChanges != nil {
		fresh = "yes"
		if !m.status.IndexFresh {
			fresh = "no"
		}
	}

	lines := []string{
		fmt.Sprintf("Project:      %s", m.status.ProjectRoot),
		fmt.Sprintf("Data dir:     %s", m.status.DataDir),
		fmt.Sprintf("VecLite:      %s", m.status.VecLitePath),
		fmt.Sprintf("VecLite size: %s", formatBytes(m.status.VecLiteSizeBytes)),
		fmt.Sprintf("Backend:      %s", m.status.VectorBackend),
		fmt.Sprintf("Veclite ver:  %s", m.status.VecliteVersion),
		fmt.Sprintf("Embedding:    %s / %s / %d dims", m.status.Provider, m.status.Model, m.status.Dimensions),
		fmt.Sprintf("HNSW:         M=%d  efConstruction=%d  efSearch=%d", m.status.HNSWM, m.status.HNSWEfConstruction, m.status.HNSWEfSearch),
		fmt.Sprintf("Profile:      %s", m.status.ProfileStatus),
		fmt.Sprintf("Provider:     %s", providerHealthLabel(m.status.ProviderHealth)),
		"",
		fmt.Sprintf("Fresh:        %s", fresh),
		fmt.Sprintf("Projects:     %d", m.status.Stats["projects"]),
		fmt.Sprintf("Files:        %d", m.status.Stats["files"]),
		fmt.Sprintf("Chunks:       %d", m.status.Stats["chunks"]),
		fmt.Sprintf("Embeddings:   %d", m.status.Stats["embeddings"]),
		fmt.Sprintf("Source bytes: %s", formatBytes(m.status.IndexedBytes)),
	}
	if !m.status.LatestIndexedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("Latest:       %s", formatTimeAgo(m.status.LatestIndexedAt)))
	}
	if m.status.ProfilePath != "" {
		lines = append(lines, fmt.Sprintf("Profile path: %s", m.status.ProfilePath))
	}
	if m.status.PendingChanges != nil {
		lines = append(lines, "",
			"Pending changes",
			fmt.Sprintf("New:        %d", m.status.PendingChanges.NewFiles),
			fmt.Sprintf("Modified:   %d", m.status.PendingChanges.ModifiedFiles),
			fmt.Sprintf("Deleted:    %d", m.status.PendingChanges.DeletedFiles),
		)
	}
	if m.status.MigrationWarning != "" {
		lines = append(lines, "",
			fmt.Sprintf("Warning: %s", m.status.MigrationWarning),
		)
	}
	if m.status.DetailedStats != nil {
		if languages := formatCountLines("Languages", m.status.DetailedStats.Languages, 6); len(languages) > 0 {
			lines = append(lines, "")
			lines = append(lines, languages...)
		}
		if chunkTypes := formatCountLines("Chunk types", m.status.DetailedStats.ChunkTypes, 6); len(chunkTypes) > 0 {
			lines = append(lines, "")
			lines = append(lines, chunkTypes...)
		}
	}
	return m.renderPanel("Status", strings.Join(lines, "\n"), m.width-2, true)
}

func (m Model) renderConfig() string {
	if m.session == nil {
		return panelStyle.Width(m.width - 2).Render("config unavailable")
	}
	cfg := m.session.Config
	lines := []string{
		"Config",
		"",
		fmt.Sprintf("data_dir: %s", cfg.DataDir),
		fmt.Sprintf("db_path:  %s", cfg.DBPath),
		"",
		fmt.Sprintf("embedding.provider:   %s", cfg.Embedding.Provider),
		fmt.Sprintf("embedding.model:      %s", cfg.Embedding.Model),
		fmt.Sprintf("embedding.dimensions: %d", cfg.Embedding.Dimensions),
	}
	switch cfg.Embedding.Provider {
	case "ollama", "":
		lines = append(lines, fmt.Sprintf("embedding.ollama_url: %s", cfg.Embedding.OllamaURL))
	case "openai":
		lines = append(lines,
			fmt.Sprintf("embedding.openai_api_key: %s", secretStatus(cfg.Embedding.OpenAIAPIKey)),
			fmt.Sprintf("embedding.openai_base_url: %s", cfg.Embedding.OpenAIBaseURL),
		)
	case "cohere":
		lines = append(lines,
			fmt.Sprintf("embedding.cohere_api_key: %s", secretStatus(cfg.Embedding.CohereAPIKey)),
			fmt.Sprintf("embedding.cohere_base_url: %s", cfg.Embedding.CohereBaseURL),
		)
	case "voyage":
		lines = append(lines,
			fmt.Sprintf("embedding.voyage_api_key: %s", secretStatus(cfg.Embedding.VoyageAPIKey)),
			fmt.Sprintf("embedding.voyage_base_url: %s", cfg.Embedding.VoyageBaseURL),
		)
	}
	lines = append(lines,
		"",
		fmt.Sprintf("indexing.chunk_size:     %d", cfg.Indexing.ChunkSize),
		fmt.Sprintf("indexing.chunk_overlap:  %d", cfg.Indexing.ChunkOverlap),
		fmt.Sprintf("indexing.max_file_size:  %d", cfg.Indexing.MaxFileSize),
	)
	// Embedding extras
	if cfg.Embedding.MaxBatchSize > 0 {
		lines = append(lines, fmt.Sprintf("embedding.max_batch_size: %d", cfg.Embedding.MaxBatchSize))
	}
	if cfg.Embedding.KeepAlive != "" {
		lines = append(lines, fmt.Sprintf("embedding.keep_alive:     %s", cfg.Embedding.KeepAlive))
	}
	// Throttle
	if cfg.Embedding.Throttle.Enabled != nil || cfg.Embedding.Throttle.MaxInFlight > 0 || cfg.Embedding.Throttle.RateLimit > 0 {
		lines = append(lines, "", "Throttle")
		if cfg.Embedding.Throttle.Enabled != nil {
			lines = append(lines, fmt.Sprintf("  enabled:        %v", *cfg.Embedding.Throttle.Enabled))
		} else {
			lines = append(lines, "  enabled:        true (default)")
		}
		if cfg.Embedding.Throttle.MaxInFlight > 0 {
			lines = append(lines, fmt.Sprintf("  max_in_flight:  %d", cfg.Embedding.Throttle.MaxInFlight))
		}
		if cfg.Embedding.Throttle.RateLimit > 0 {
			lines = append(lines, fmt.Sprintf("  rate_limit:     %.1f", cfg.Embedding.Throttle.RateLimit))
		}
	}
	// Cache
	lines = append(lines, "", "Cache")
	if cfg.Cache.FcheapStash != nil {
		lines = append(lines, fmt.Sprintf("  fcheap_stash:  %v", *cfg.Cache.FcheapStash))
	} else {
		lines = append(lines, "  fcheap_stash:  true (default)")
	}
	if cfg.Cache.FcheapTTL != "" {
		lines = append(lines, fmt.Sprintf("  fcheap_ttl:    %s", cfg.Cache.FcheapTTL))
	}
	if cfg.Cache.Path != "" {
		lines = append(lines, fmt.Sprintf("  path:          %s", cfg.Cache.Path))
	}
	// Daemon
	if cfg.Daemon.Autostart || cfg.Daemon.IdleTimeout > 0 || cfg.Daemon.SweepInterval != "" {
		lines = append(lines, "", "Daemon")
		if cfg.Daemon.Autostart {
			lines = append(lines, "  autostart:     true")
		}
		if cfg.Daemon.IdleTimeout > 0 {
			lines = append(lines, fmt.Sprintf("  idle_timeout:  %d", cfg.Daemon.IdleTimeout))
		}
		if cfg.Daemon.SweepInterval != "" {
			lines = append(lines, fmt.Sprintf("  sweep_interval: %s", cfg.Daemon.SweepInterval))
		}
	}
	if len(m.session.ConfigSources) > 0 {
		lines = append(lines, "", "Sources")
		for _, source := range m.session.ConfigSources {
			lines = append(lines, "  "+source)
		}
	}
	return panelStyle.Width(m.width - 2).Render(strings.Join(lines, "\n"))
}

func secretStatus(value string) string {
	if strings.TrimSpace(value) == "" {
		return "[not set]"
	}
	return "[set]"
}

func (m Model) renderHelp() string {
	lines := []string{
		"Keys",
		"",
		"/ or ctrl+f  focus query",
		"enter        search from query/filter fields, open result from list",
		"tab          change focus across query, filters, results, preview",
		"dir/file     editable directory prefix and file glob filters",
		"lines        editable line range filter, for example 1-120",
		"j/k arrows   move result selection",
		"u/d pgup/dn  scroll preview",
		"m            cycle mode (hybrid / semantic / keyword)",
		"L / T        cycle language / type filters",
		"+ / -        change limit",
		"s            find similar to selected result",
		"r / R        index / full reindex (shows ETA + rate)",
		"x            delete selected file",
		"i            register this folder globally when none is open",
		"o            open selected result in EDITOR",
		"v / c / ?    status / config / help",
		"!            reset project index",
		"q            quit outside the query input",
		"ctrl+c       quit",
		"",
		"The config view (c) shows throttle, cache, daemon, and",
		"embedding settings including keep_alive and max_batch_size.",
		"The status view (v) shows index freshness and pending changes.",
	}
	return panelStyle.Width(m.width - 2).Render(strings.Join(lines, "\n"))
}

func (m Model) renderFooter() string {
	var parts []string
	if m.statusMessage != "" {
		parts = append(parts, m.statusMessage)
	}
	if m.errMessage != "" {
		parts = append(parts, errorStyle.Render(m.errMessage))
	}
	if m.confirm != confirmNone {
		parts = append(parts, warnStyle.Render(m.confirmText()))
	}
	parts = append(parts, mutedStyle.Render("? help  ctrl+c quit"))
	return strings.Join(parts, "  ")
}

func (m Model) confirmText() string {
	switch m.confirm {
	case confirmDelete:
		return "delete selected file? y/n"
	case confirmFullReindex:
		return "full reindex? y/n"
	case confirmReset:
		return "reset project index? y/n"
	default:
		return ""
	}
}

func displayValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func displayIndexPath(session *app.Session, path string) string {
	if session != nil && session.ProjectRoot != "" {
		if rel, err := filepath.Rel(session.ProjectRoot, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func progressBar(processed, total, width int) string {
	if width < 1 {
		width = 1
	}
	percent := 0
	if total > 0 {
		percent = clamp((processed*100)/total, 0, 100)
	}
	filled := 0
	if total > 0 {
		filled = clamp((processed*width)/total, 0, width)
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-filled) + fmt.Sprintf("] %3d%%", percent)
}

type countItem struct {
	name  string
	count int64
}

func formatCountLines(title string, counts map[string]int64, limit int) []string {
	items := sortedCounts(counts, limit)
	if len(items) == 0 {
		return nil
	}
	lines := []string{title}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("  %s %d", item.name, item.count))
	}
	return lines
}

func sortedCounts(counts map[string]int64, limit int) []countItem {
	if len(counts) == 0 {
		return nil
	}
	items := make([]countItem, 0, len(counts))
	for name, count := range counts {
		if name == "" || count == 0 {
			continue
		}
		items = append(items, countItem{name: name, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].name < items[j].name
		}
		return items[i].count > items[j].count
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}

// providerHealthLabel renders the ProviderHealth field for the status panel.
// Empty means "not checked", "ok" is shown verbatim, anything else is an error
// string and is truncated to keep the panel tidy.
func providerHealthLabel(health string) string {
	if health == "" {
		return "not checked"
	}
	if health == "ok" {
		return "ok"
	}
	return "error: " + truncateDisplay(health, 40)
}

func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	age := time.Since(t).Round(time.Second)
	if age < 0 {
		return t.Format(time.RFC3339)
	}
	return fmt.Sprintf("%s ago (%s)", age, t.Format(time.RFC3339))
}

func truncateDisplay(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width {
		return value
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}

	var b strings.Builder
	for _, r := range value {
		next := b.String() + string(r)
		if lipgloss.Width(next)+3 > width {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "..."
}
