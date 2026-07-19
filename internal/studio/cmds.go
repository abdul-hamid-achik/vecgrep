package studio

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/daemon"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

func (m *Model) searchCmd() tea.Cmd {
	if m.service == nil {
		return nil
	}
	query := strings.TrimSpace(m.query.Value())
	if query == "" {
		m.statusMessage = "type a query first"
		return nil
	}
	rawRange := strings.TrimSpace(m.lineRange.Value())
	minLine, maxLine := app.ParseLineRange(rawRange)
	if rawRange != "" && minLine == 0 && maxLine == 0 {
		m.errMessage = "invalid line range, use start-end (e.g. 1-120)"
		return nil
	}
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
		MinScore:    m.minScore,
	}
	m.searchGen++
	gen := m.searchGen
	m.searching = true
	m.statusMessage = "searching"
	m.errMessage = ""
	ctx := m.ctx
	return func() tea.Msg {
		resp, err := m.service.Search(ctx, req)
		return searchLoadedMsg{gen: gen, query: query, response: resp, err: err}
	}
}

func (m *Model) reloadStatusCmd() tea.Cmd {
	if m.service == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		status, err := m.service.Status(ctx)
		if err != nil {
			return statusLoadedMsg{err: err}
		}
		readiness, _ := m.service.Readiness(ctx)
		return statusLoadedMsg{status: status, readiness: readiness}
	}
}

func (m *Model) similarSelectedCmd() tea.Cmd {
	if m.service == nil || len(m.results) == 0 {
		m.statusMessage = "select a result for similar"
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
		MinScore:        m.minScore,
		ExcludeSameFile: true,
	}
	m.searchGen++
	gen := m.searchGen
	m.searching = true
	m.statusMessage = "finding similar"
	m.errMessage = ""
	ctx := m.ctx
	return func() tea.Msg {
		resp, err := m.service.Similar(ctx, req)
		return searchLoadedMsg{gen: gen, query: "similar " + selected.RelativePath, response: resp, err: err}
	}
}

func (m *Model) indexCmd(full bool) tea.Cmd {
	if m.service == nil {
		return nil
	}
	if m.indexing {
		m.statusMessage = "already indexing"
		return nil
	}
	if m.readOnly {
		return m.daemonReindexCmd(full)
	}
	m.indexGen++
	gen := m.indexGen
	m.indexing = true
	m.daemonIndex = false
	m.indexStarted = time.Now()
	m.indexProgress = &index.Progress{StartTime: m.indexStarted}
	m.indexRecent = nil
	m.errMessage = ""
	m.dryRun = nil
	if full {
		m.statusMessage = "full reindexing"
	} else {
		m.statusMessage = "indexing"
	}
	req := app.IndexRequest{FullReindex: full}
	// Coalesce channel of size 1: always keep the newest snapshot so the UI
	// never stalls on a flood of walk ticks while also never dropping the
	// latest path/bytes.
	progressCh := make(chan index.Progress, 1)
	ctx, cancel := context.WithCancel(m.ctx)
	m.indexCancel = cancel
	indexTask := func() tea.Msg {
		defer close(progressCh)
		defer cancel()
		result, err := m.service.Index(ctx, req, func(progress index.Progress) {
			// Non-blocking publish with replace-latest semantics.
			select {
			case progressCh <- progress:
			default:
				select {
				case <-progressCh:
				default:
				}
				select {
				case progressCh <- progress:
				default:
				}
			}
		})
		return indexDoneMsg{gen: gen, result: result, err: err, full: full}
	}
	return tea.Batch(
		waitForIndexProgressCmd(gen, progressCh),
		indexTickCmd(gen),
		indexTask,
	)
}

// cancelIndex stops an in-flight index without quitting Studio.
// Clears indexCancel so a subsequent ctrl+c/q can quit the app (otherwise
// every ctrl+c would only re-signal cancel while indexing stays true until
// indexDoneMsg arrives).
func (m *Model) cancelIndex() {
	if m.indexCancel != nil {
		m.indexCancel()
		m.indexCancel = nil
		m.statusMessage = "canceling index…"
	}
}

func (m *Model) daemonReindexCmd(full bool) tea.Cmd {
	if m.service == nil || m.session == nil {
		return nil
	}
	if m.indexing {
		m.statusMessage = "already indexing"
		return nil
	}
	m.indexGen++
	gen := m.indexGen
	m.indexing = true
	m.daemonIndex = true
	m.indexStarted = time.Now()
	m.indexRecent = nil
	m.errMessage = ""
	if full {
		m.statusMessage = "full reindexing via daemon"
	} else {
		m.statusMessage = "indexing via daemon"
	}
	root := m.session.ProjectRoot
	ctx := m.ctx
	return tea.Batch(
		indexTickCmd(gen),
		func() tea.Msg {
			dir, _ := config.GetGlobalConfigDir()
			result, err := daemon.ReindexSync(ctx, dir, root, app.IndexRequest{FullReindex: full})
			return daemonReindexDoneMsg{gen: gen, result: result, err: err, full: full}
		},
	)
}

func waitForIndexProgressCmd(gen int, ch <-chan index.Progress) tea.Cmd {
	return func() tea.Msg {
		progress, ok := <-ch
		if !ok {
			return indexProgressClosedMsg{gen: gen}
		}
		return indexProgressMsg{gen: gen, progress: progress, ch: ch}
	}
}

func indexTickCmd(gen int) tea.Cmd {
	return tea.Tick(indexTickEvery, func(t time.Time) tea.Msg {
		return indexTickMsg{gen: gen}
	})
}

// planIndexCmd runs DryRunPreview then either confirms (large/empty/full) or
// starts Index immediately when the plan is small.
func (m *Model) planIndexCmd(full bool) tea.Cmd {
	if m.service == nil {
		return nil
	}
	if m.indexing || m.planningIndex {
		m.statusMessage = "already indexing"
		return nil
	}
	m.planningIndex = true
	m.pendingFull = full
	m.statusMessage = "planning index…"
	m.errMessage = ""
	m.dryRun = nil
	ctx := m.ctx
	return func() tea.Msg {
		preview, err := m.service.DryRunPreview(ctx)
		return dryRunLoadedMsg{preview: preview, err: err, full: full}
	}
}

func (m *Model) addIndexRecent(path string) {
	if path == "" {
		return
	}
	displayPath := displayIndexPath(m.session, path)
	if len(m.indexRecent) > 0 && m.indexRecent[len(m.indexRecent)-1] == displayPath {
		return
	}
	m.indexRecent = append(m.indexRecent, displayPath)
	if len(m.indexRecent) > 5 {
		m.indexRecent = m.indexRecent[len(m.indexRecent)-5:]
	}
}

// Note: progress ticks also call addIndexRecent via Update(indexProgressMsg).

func (m *Model) initProjectCmd() tea.Cmd {
	if m.sessionLoading {
		return nil
	}
	m.sessionLoading = true
	m.errMessage = ""
	m.statusMessage = "registering project"
	ctx := m.ctx
	startDir := m.startDir
	return func() tea.Msg {
		if _, err := app.InitGlobalProject(ctx, startDir, false); err != nil {
			return sessionLoadedMsg{err: err}
		}
		return loadSessionCmd(ctx, startDir)()
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
	ctx := m.ctx
	return func() tea.Msg {
		deleted, err := m.service.DeleteFile(ctx, path)
		return deleteDoneMsg{path: path, deleted: deleted, err: err}
	}
}

func (m *Model) resetCmd() tea.Cmd {
	if m.service == nil {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		err := m.service.Reset(ctx, app.ResetProject)
		return resetDoneMsg{err: err}
	}
}

func (m *Model) openEditorCmd() tea.Cmd {
	if len(m.results) == 0 {
		m.statusMessage = "select a result to open"
		return nil
	}
	result := m.results[m.selected]
	path := result.FilePath
	if path == "" && m.session != nil {
		path = filepath.Join(m.session.ProjectRoot, result.RelativePath)
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	arg := fmt.Sprintf("+%d", result.StartLine)
	// Common GUI editors prefer path:line form when possible.
	base := filepath.Base(editor)
	var cmd *exec.Cmd
	switch base {
	case "code", "code-insiders", "cursor", "codium":
		cmd = exec.Command(editor, "--goto", fmt.Sprintf("%s:%d", path, result.StartLine))
	case "hx", "helix":
		cmd = exec.Command(editor, fmt.Sprintf("%s:%d", path, result.StartLine))
	default:
		cmd = exec.Command(editor, arg, path)
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{err: err}
	})
}

func (m *Model) yankSelectedCmd(snippet bool) tea.Cmd {
	if len(m.results) == 0 {
		m.statusMessage = "select a result to yank"
		return nil
	}
	r := m.results[m.selected]
	var text string
	if snippet {
		text = r.Content
		if text == "" {
			text = m.selectedPathLine()
		}
	} else {
		text = m.selectedPathLine()
	}
	return func() tea.Msg {
		err := copyToClipboard(text)
		return yankDoneMsg{text: text, err: err}
	}
}

func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else {
			return fmt.Errorf("no clipboard tool (wl-copy/xclip/xsel)")
		}
	default:
		return fmt.Errorf("clipboard unsupported on %s", runtime.GOOS)
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
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
	if result.Reranked {
		b.WriteString("  ≈reranked")
		if result.StructuralScore > 0 {
			fmt.Fprintf(&b, " hub=%.2f", result.StructuralScore)
		}
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

	// Budget: header(1) + query(~3) + filters? + chips(1) + footer(1) + borders
	chrome := 8
	if m.filtersOpen {
		if m.width < 80 {
			chrome += 12
		} else {
			chrome += 5
		}
	}
	body := clamp(m.height-chrome, 8, m.height)

	previewHeight := clamp(body-2, 6, 40)
	previewWidth := clamp(m.width/2-4, 30, m.width-4)
	if m.width < 96 {
		previewWidth = clamp(m.width-4, 30, m.width-4)
		previewHeight = clamp(body/2-1, 6, 24)
	}
	m.preview.SetWidth(previewWidth)
	m.preview.SetHeight(previewHeight)

	m.overlay.SetWidth(clamp(m.width-4, 40, m.width))
	m.overlay.SetHeight(clamp(m.height-4, 8, m.height-2))

	// Charm progress bar width tracks the index panel.
	barW := clamp(m.width-30, 16, 48)
	m.progBar.SetWidth(barW)
	m.help.SetWidth(clamp(m.width-4, 40, m.width))
	m.sizeResultList()
}

func (m *Model) refreshOverlay() {
	var content string
	switch m.activeView {
	case viewStatus:
		content = m.statusBody()
	case viewConfig:
		content = m.configBody()
	case viewHelp:
		content = m.helpBody()
	default:
		return
	}
	m.overlay.SetContent(content)
	m.overlay.GotoTop()
}

func (m *Model) resultsMaxRows() int {
	chrome := 10
	if m.filtersOpen {
		if m.width < 80 {
			chrome += 12
		} else {
			chrome += 5
		}
	}
	return clamp(m.height-chrome, 5, 40)
}
