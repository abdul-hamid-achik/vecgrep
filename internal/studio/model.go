package studio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/app"
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
	statusMessage string
	errMessage    string
	confirm       confirmAction
}

type sessionLoadedMsg struct {
	session *app.Session
	status  *app.StatusResponse
	err     error
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
	ti := textinput.New()
	ti.Placeholder = "Search code by meaning..."
	ti.Prompt = "query "
	ti.SetWidth(80)
	ti.Focus()

	dir := textinput.New()
	dir.Placeholder = "internal/"
	dir.Prompt = "dir "
	dir.SetWidth(24)

	file := textinput.New()
	file.Placeholder = "**/*_test.go"
	file.Prompt = "file "
	file.SetWidth(28)

	lines := textinput.New()
	lines.Placeholder = "1-120"
	lines.Prompt = "lines "
	lines.SetWidth(18)

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

func (m Model) Init() tea.Cmd {
	return loadSessionCmd(m.ctx, m.startDir)
}

func loadSessionCmd(ctx context.Context, startDir string) tea.Cmd {
	return func() tea.Msg {
		session, err := app.OpenSession(ctx, startDir)
		if err != nil {
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
		m.statusMessage = "ready"
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
			m.query.Focus()
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
		if len(m.results) > 0 {
			m.confirm = confirmDelete
			return nil, true
		}
	case "!":
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
	m.indexing = true
	m.errMessage = ""
	if full {
		m.statusMessage = "full reindexing"
	} else {
		m.statusMessage = "indexing"
	}
	req := app.IndexRequest{FullReindex: full}
	return func() tea.Msg {
		result, err := m.service.Index(m.ctx, req, nil)
		return indexDoneMsg{result: result, err: err, full: full}
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
	m.query.SetWidth(clamp(m.width-12, 20, 120))
	filterWidth := clamp((m.width-12)/3, 16, 34)
	if m.width < 80 {
		filterWidth = clamp(m.width-12, 20, 80)
	}
	m.directory.SetWidth(filterWidth)
	m.filePattern.SetWidth(filterWidth)
	m.lineRange.SetWidth(filterWidth)

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
	left := titleStyle.Render("vecgrep") + " " + mutedStyle.Render(project)
	right := mutedStyle.Render(fmt.Sprintf("mode %s  limit %d", m.mode, m.limit))
	if m.width > lipgloss.Width(left)+lipgloss.Width(right)+2 {
		return left + strings.Repeat(" ", m.width-lipgloss.Width(left)-lipgloss.Width(right)) + right
	}
	return left
}

func (m Model) renderSearch() string {
	if m.loading && m.session == nil {
		return panelStyle.Width(m.width - 2).Render("loading project...")
	}
	if m.session == nil {
		msg := "No vecgrep project found.\n\nRun `vecgrep init --local` in this directory, or `vecgrep projects add`, then reopen Studio.\n\nctrl+c quits"
		if m.errMessage != "" {
			msg = m.errMessage + "\n\n" + msg
		}
		return panelStyle.Width(m.width - 2).Render(msg)
	}

	query := m.query.View()
	filterInputs := m.renderFilterInputs()
	filters := fmt.Sprintf("m mode: %s   L lang: %s   T type: %s   +/- limit: %d",
		m.mode, displayValue(m.languages[m.langIdx], "all"), displayValue(m.types[m.typeIdx], "all"), m.limit)

	results := m.renderResults()
	preview := panelStyle.Width(m.preview.Width()).Render(m.preview.View())
	if m.width >= 96 {
		listWidth := clamp(m.width-m.preview.Width()-6, 35, m.width-4)
		results = panelStyle.Width(listWidth).Render(results)
		return lipgloss.JoinVertical(lipgloss.Left,
			query,
			filterInputs,
			mutedStyle.Render(filters),
			lipgloss.JoinHorizontal(lipgloss.Top, results, preview),
		)
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		query,
		filterInputs,
		mutedStyle.Render(filters),
		panelStyle.Width(m.width-2).Render(results),
		preview,
	)
}

func (m Model) renderFilterInputs() string {
	if m.width < 80 {
		return lipgloss.JoinVertical(lipgloss.Left,
			m.directory.View(),
			m.filePattern.View(),
			m.lineRange.View(),
		)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top,
		m.directory.View(),
		"  ",
		m.filePattern.View(),
		"  ",
		m.lineRange.View(),
	)
}

func (m Model) renderResults() string {
	if m.indexing {
		return "indexing..."
	}
	if m.loading {
		return "searching..."
	}
	if len(m.results) == 0 {
		if m.lastQuery == "" {
			return "Type a query and press enter."
		}
		return "No results."
	}

	maxRows := clamp(m.height-14, 5, 24)
	start := 0
	if m.selected >= maxRows {
		start = m.selected - maxRows + 1
	}
	end := clamp(start+maxRows, 0, len(m.results))

	var b strings.Builder
	for i := start; i < end; i++ {
		result := m.results[i]
		line := fmt.Sprintf("%2d %.2f %s:%d", i+1, result.Score, result.RelativePath, result.StartLine)
		if result.SymbolName != "" {
			line += " " + result.SymbolName
		}
		if i == m.selected {
			b.WriteString(activeStyle.Width(max(20, m.width/3)).Render(line))
		} else {
			b.WriteString(line)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m Model) renderStatus() string {
	if m.status == nil {
		return panelStyle.Width(m.width - 2).Render("status unavailable")
	}
	lines := []string{
		"Status",
		"",
		fmt.Sprintf("Project:    %s", m.status.ProjectRoot),
		fmt.Sprintf("Database:   %s", m.status.VecLitePath),
		fmt.Sprintf("Provider:   %s (%s)", m.status.Model, m.status.Provider),
		fmt.Sprintf("Dimensions: %d", m.status.Dimensions),
		"",
		fmt.Sprintf("Projects:   %d", m.status.Stats["projects"]),
		fmt.Sprintf("Files:      %d", m.status.Stats["files"]),
		fmt.Sprintf("Chunks:     %d", m.status.Stats["chunks"]),
		fmt.Sprintf("Embeddings: %d", m.status.Stats["embeddings"]),
	}
	if m.status.PendingChanges != nil {
		lines = append(lines, "",
			"Pending changes",
			fmt.Sprintf("New:        %d", m.status.PendingChanges.NewFiles),
			fmt.Sprintf("Modified:   %d", m.status.PendingChanges.ModifiedFiles),
			fmt.Sprintf("Deleted:    %d", m.status.PendingChanges.DeletedFiles),
		)
	}
	return panelStyle.Width(m.width - 2).Render(strings.Join(lines, "\n"))
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
		fmt.Sprintf("embedding.ollama_url: %s", cfg.Embedding.OllamaURL),
		"",
		fmt.Sprintf("indexing.chunk_size:     %d", cfg.Indexing.ChunkSize),
		fmt.Sprintf("indexing.chunk_overlap:  %d", cfg.Indexing.ChunkOverlap),
		fmt.Sprintf("indexing.max_file_size:  %d", cfg.Indexing.MaxFileSize),
	}
	if len(m.session.ConfigSources) > 0 {
		lines = append(lines, "", "Sources")
		for _, source := range m.session.ConfigSources {
			lines = append(lines, "  "+source)
		}
	}
	return panelStyle.Width(m.width - 2).Render(strings.Join(lines, "\n"))
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
		"m            cycle mode",
		"L / T        cycle language / type filters",
		"+ / -        change limit",
		"s            find similar to selected result",
		"r / R        index / full reindex",
		"x            delete selected file",
		"o            open selected result in EDITOR",
		"v / c / ?    status / config / help",
		"!            reset project index",
		"q            quit outside the query input",
		"ctrl+c       quit",
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
