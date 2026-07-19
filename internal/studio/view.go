package studio

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

func (m Model) View() tea.View {
	content := m.render()
	v := tea.NewView(content)
	v.AltScreen = true
	// Enable click + wheel without full motion spam.
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) render() string {
	if m.width == 0 {
		return "vecgrep\n\nloading..."
	}

	header := m.renderHeader()
	body := ""
	switch m.activeView {
	case viewStatus, viewConfig, viewHelp:
		title := "Status"
		if m.activeView == viewConfig {
			title = "Config"
		} else if m.activeView == viewHelp {
			title = "Help"
		}
		// Live overlay content (not a stale viewport buffer) so status never
		// sticks on "unavailable" after session load while still supporting
		// j/k scroll via the overlay YOffset.
		body = m.renderPanel(title+"  (j/k scroll · esc back)", m.liveOverlayView(), m.width-2, true)
	default:
		body = m.renderSearch()
	}

	footer := m.renderFooter()
	// Keep footer visible: clamp body height when possible.
	out := lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
	return out
}

func (m Model) renderHeader() string {
	project := "no project"
	if m.status != nil {
		project = m.status.ProjectRoot
	}
	left := titleStyle.Render("vecgrep") + " " + mutedStyle.Render(truncateDisplay(project, clamp(m.width/3, 16, 48)))

	var rightParts []string
	if m.branchName != "" {
		rightParts = append(rightParts, "⎇ "+m.branchName)
	}
	if m.readOnly {
		rightParts = append(rightParts, "read-only")
	}
	if chip := m.readinessChip(); chip != "" {
		rightParts = append(rightParts, chip)
	}
	modeLabel := string(m.mode)
	if m.effectiveMode != "" && m.effectiveMode != m.mode && len(m.results) > 0 {
		modeLabel = fmt.Sprintf("%s→%s", m.mode, m.effectiveMode)
	}
	rightParts = append(rightParts, fmt.Sprintf("mode %s", modeLabel), fmt.Sprintf("limit %d", m.limit))
	if m.minScore > 0 {
		rightParts = append(rightParts, fmt.Sprintf("min %.2f", m.minScore))
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

func (m Model) readinessChip() string {
	var label string
	var style lipgloss.Style
	if !m.hasReadiness {
		if !m.isIndexEmpty() {
			return ""
		}
		label, style = "empty", warnChipStyle
	} else {
		switch m.readiness.State {
		case app.ReadinessEmpty:
			label, style = "empty", warnChipStyle
		case app.ReadinessProfileMismatch:
			label, style = "profile≠", badChipStyle
		case app.ReadinessStale:
			label, style = "stale", warnChipStyle
		case app.ReadinessUnknown:
			label, style = "fresh?", warnChipStyle
		case app.ReadinessReady:
			label, style = "ready", okChipStyle
		default:
			return ""
		}
	}
	return style.Render(label)
}

func (m Model) renderSearch() string {
	if m.sessionLoading && m.session == nil {
		return m.renderPanel("Project", "loading project...", m.width-2, false)
	}
	if m.session == nil {
		return m.renderPanel("Project", m.renderUnavailableProject(), m.width-2, false)
	}

	query := m.renderQuery()
	parts := []string{query}
	if m.filtersOpen {
		parts = append(parts, m.renderFilterInputs())
	}
	parts = append(parts, m.renderFilterBar())

	if banner := m.readinessBanner(); banner != "" {
		parts = append(parts, banner)
	}

	// Always show a dedicated indexing panel so paths/bytes are visible even
	// when previous search results are still on screen.
	if m.indexing {
		parts = append(parts, m.renderPanel(m.indexPanelTitle(), m.renderIndexProgress(m.width-6), m.width-2, true))
		if len(m.results) > 0 {
			// Keep a compact results strip under progress (don't hide hits).
			list := m.renderPanel(m.resultsTitle(), m.renderResultsCompact(m.width-6), m.width-2, m.focus == focusResults)
			parts = append(parts, list)
		}
		return lipgloss.JoinVertical(lipgloss.Left, parts...)
	}

	if m.width >= 96 {
		listWidth := clamp(m.width-m.preview.Width()-6, 35, m.width-4)
		results := m.renderPanel(m.resultsTitle(), m.renderResults(listWidth-4), listWidth, m.focus == focusResults)
		preview := m.renderPanel(m.previewTitle(), m.renderPreview(), m.preview.Width(), m.focus == focusPreview)
		parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Top, results, "  ", preview))
	} else {
		results := m.renderPanel(m.resultsTitle(), m.renderResults(m.width-6), m.width-2, m.focus == focusResults)
		preview := m.renderPanel(m.previewTitle(), m.renderPreview(), m.width-2, m.focus == focusPreview)
		parts = append(parts, results, preview)
	}
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m Model) indexPanelTitle() string {
	root := "project"
	if m.session != nil && m.session.ProjectRoot != "" {
		root = m.session.ProjectRoot
	} else if m.status != nil && m.status.ProjectRoot != "" {
		root = m.status.ProjectRoot
	}
	return "Indexing  " + truncateDisplay(root, clamp(m.width-18, 20, 80))
}

func (m Model) readinessBanner() string {
	if m.sessionLoading || m.indexing {
		return ""
	}
	if !m.hasReadiness {
		if m.isIndexEmpty() {
			return warnStyle.Render("No index yet. Press r to index this project.")
		}
		return ""
	}
	switch m.readiness.State {
	case app.ReadinessEmpty:
		return warnStyle.Render("No index yet. Press r to plan & index (y confirms).")
	case app.ReadinessProfileMismatch:
		msg := "Embedding profile mismatch. Press R to full reindex."
		if m.readiness.StoredProfileID != "" && m.readiness.ActiveProfileID != "" {
			msg = fmt.Sprintf("Profile mismatch (stored %s ≠ active %s). Press R to full reindex.",
				truncateDisplay(m.readiness.StoredProfileID, 24),
				truncateDisplay(m.readiness.ActiveProfileID, 24))
		}
		return errorStyle.Render(msg)
	case app.ReadinessStale:
		reason := m.readiness.Reason
		if reason == "" {
			reason = "index is stale"
		}
		return warnStyle.Render(reason + " — press r to update")
	case app.ReadinessUnknown:
		return warnStyle.Render("Freshness unknown — press R to rebuild trusted metadata")
	default:
		return ""
	}
}

func (m Model) renderUnavailableProject() string {
	if m.errMessage != "" && !strings.Contains(m.errMessage, "not in a vecgrep project") {
		msg := "Could not open this project.\n\n" + m.errMessage
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
	dirActive := strings.TrimSpace(m.directory.Value()) != ""
	fileActive := strings.TrimSpace(m.filePattern.Value()) != ""
	lineActive := strings.TrimSpace(m.lineRange.Value()) != ""
	lang := ""
	if m.langIdx >= 0 && m.langIdx < len(m.languages) {
		lang = m.languages[m.langIdx]
	}
	typ := ""
	if m.typeIdx >= 0 && m.typeIdx < len(m.types) {
		typ = m.types[m.typeIdx]
	}
	chips := []string{
		m.renderChip("mode "+string(m.mode), false),
		m.renderChip("lang "+displayValue(lang, "all"), lang != ""),
		m.renderChip("type "+displayValue(typ, "all"), typ != ""),
		m.renderChip(fmt.Sprintf("limit %d", m.limit), false),
	}
	if m.minScore > 0 {
		chips = append(chips, m.renderChip(fmt.Sprintf("min %.2f", m.minScore), true))
	}
	if dirActive {
		chips = append(chips, m.renderChip("dir "+truncateDisplay(m.directory.Value(), 16), true))
	}
	if fileActive {
		chips = append(chips, m.renderChip("file "+truncateDisplay(m.filePattern.Value(), 16), true))
	}
	if lineActive {
		chips = append(chips, m.renderChip("lines "+m.lineRange.Value(), true))
	}
	if !m.filtersOpen {
		chips = append(chips, m.renderChip("f filters", false))
	} else {
		chips = append(chips, m.renderChip("f hide", true))
	}
	if m.resultsDirty {
		chips = append(chips, warnStyle.Render("stale — enter"))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, joinChips(chips)...)
}

func joinChips(chips []string) []string {
	out := make([]string, 0, len(chips)*2)
	for i, c := range chips {
		if i > 0 {
			out = append(out, " ")
		}
		out = append(out, c)
	}
	return out
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
	ts := panelTitleStyle
	style := panelStyle
	if focused {
		ts = activePanelTitleStyle
		style = focusedPanelStyle
		titleText = "▸ " + titleText
	}
	content := lipgloss.JoinVertical(lipgloss.Left, ts.Render(titleText), body)
	return style.Width(width).Render(content)
}

func (m Model) resultsTitle() string {
	if m.searching && len(m.results) > 0 {
		return "Results (searching…)"
	}
	if len(m.results) == 0 {
		return "Results"
	}
	title := fmt.Sprintf("Results %d/%d", m.selected+1, len(m.results))
	if m.lastDuration > 0 {
		title += " in " + m.lastDuration.Round(time.Millisecond).String()
	}
	if m.resultsDirty {
		title += " · stale"
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
	if m.sessionLoading {
		return mutedStyle.Render("Loading…")
	}
	if m.searching && len(m.results) == 0 {
		return m.spinner.View() + " " + mutedStyle.Render("Searching…")
	}
	if len(m.results) == 0 {
		if m.indexing {
			return mutedStyle.Render("Indexing in progress…")
		}
		if m.isIndexEmpty() || (m.hasReadiness && m.readiness.State == app.ReadinessEmpty) {
			return mutedStyle.Render("No index yet. Press r to plan & index (y confirms).")
		}
		if m.hasReadiness && m.readiness.State == app.ReadinessProfileMismatch {
			return errorStyle.Render("Profile mismatch — press R to full reindex.")
		}
		if m.lastQuery == "" {
			return mutedStyle.Render("Type a query and press enter.")
		}
		return mutedStyle.Render(m.emptyResultsCopy())
	}
	// Charm list bubble: pagination, fuzzy filter (/), selection chrome.
	return m.resultList.View()
}

// renderResultsCompact shows the Charm list under the indexing panel.
func (m Model) renderResultsCompact(width int) string {
	if len(m.results) == 0 {
		return mutedStyle.Render("No prior results.")
	}
	return m.resultList.View()
}

func (m Model) emptyResultsCopy() string {
	parts := []string{fmt.Sprintf("No results for %q", m.lastQuery)}
	var filters []string
	filters = append(filters, string(m.mode))
	if m.langIdx > 0 && m.langIdx < len(m.languages) {
		filters = append(filters, m.languages[m.langIdx])
	}
	if d := strings.TrimSpace(m.directory.Value()); d != "" {
		filters = append(filters, "dir="+d)
	}
	if f := strings.TrimSpace(m.filePattern.Value()); f != "" {
		filters = append(filters, "file="+f)
	}
	if m.minScore > 0 {
		filters = append(filters, fmt.Sprintf("min=%.2f", m.minScore))
	}
	if len(filters) > 0 {
		parts = append(parts, "["+strings.Join(filters, " · ")+"]")
	}
	return strings.Join(parts, " ")
}

// renderResultRow remains for unit tests that assert row formatting.
func (m Model) renderResultRow(index int, result search.Result, width int) string {
	ri := resultItem{result: result, ord: index + 1}
	title := ri.Title()
	if desc := ri.Description(); desc != "" && desc != " " {
		title += "  " + desc
	}
	if index == m.selected {
		title = "> " + title
	} else {
		title = "  " + title
	}
	return truncateDisplay(title, width)
}

func scoreBar(score float32, width int) string {
	if width < 2 {
		width = 2
	}
	filled := int(score * float32(width))
	if score > 0 && filled == 0 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func (m Model) isIndexEmpty() bool {
	if m.status == nil || m.status.Stats == nil {
		return false
	}
	return m.status.Stats["chunks"] == 0 && m.status.Stats["files"] == 0
}

func (m Model) renderIndexProgress(width int) string {
	spin := m.spinner.View()
	p := m.indexProgress
	if p == nil {
		if m.daemonIndex {
			elapsed := ""
			if !m.indexStarted.IsZero() {
				elapsed = "  " + time.Since(m.indexStarted).Round(time.Second).String()
			}
			return spin + " " + mutedStyle.Render("Reindexing via daemon…"+elapsed+"\nesc cancel")
		}
		return spin + " " + mutedStyle.Render("Starting… scanning project tree\nesc cancel")
	}

	processed := p.ProcessedFiles
	queued := p.QueuedFiles
	if queued == 0 {
		queued = p.TotalFiles
	}
	elapsed := ""
	rateStr := ""
	etaStr := ""
	start := p.StartTime
	if start.IsZero() {
		start = m.indexStarted
	}
	if !start.IsZero() {
		elapsedDur := time.Since(start)
		elapsed = elapsedDur.Round(time.Second).String()
		if p.WalkComplete && processed > 1 && elapsedDur > time.Second {
			rate := float64(processed) / elapsedDur.Seconds()
			rateStr = fmt.Sprintf("%.1f files/s", rate)
			remaining := queued - processed
			if remaining > 0 && rate > 0 {
				eta := time.Duration(float64(remaining) / rate * float64(time.Second))
				etaStr = "ETA " + formatETA(eta)
			}
		}
	}

	var lines []string
	if p.LargeScope() {
		lines = append(lines, errorStyle.Render(
			"⚠ large tree — is this the right folder? esc cancels"))
	}

	if !p.WalkComplete {
		// Never present embed/queued as N/M while the walk is open: queued grows
		// and the ratio backslides (90/100 → 100/110). Spinner = motion; no %.
		lines = append(lines,
			fmt.Sprintf("%s Discovering · embedding in parallel%s", spin, formatElapsedSuffix(elapsed)),
			fmt.Sprintf("walked %d   queued %d   embedded %d   skipped %d",
				p.WalkedFiles, queued, processed, p.SkippedFiles),
			fmt.Sprintf("scanned %s   queued %s   embedded %s   chunks %d",
				formatBytes(p.BytesWalked),
				formatBytes(p.BytesQueued),
				formatBytes(p.BytesProcessed),
				p.TotalChunks),
			// Indeterminate Charm progress (low fill) — not a completion claim.
			m.progBar.ViewAs(0.08),
			mutedStyle.Render("denominator not final until walk finishes — no % yet"),
		)
	} else {
		pct := int(p.HonestPercent() * 100)
		// Charm progress bubble (gradient) + honest N/M counters.
		lines = append(lines,
			fmt.Sprintf("%s Embedding%s", spin, formatElapsedSuffix(elapsed)),
			fmt.Sprintf("%s  %d/%d files  %d%%", m.progBar.View(), processed, queued, pct),
			fmt.Sprintf("skipped %d   chunks %d   scanned %s   embedded %s",
				p.SkippedFiles, p.TotalChunks,
				formatBytes(p.BytesWalked), formatBytes(p.BytesProcessed)),
		)
		if rateStr != "" {
			line := rateStr
			if etaStr != "" {
				line += "  " + etaStr
			}
			lines = append(lines, line)
		}
	}

	file := p.DisplayFile()
	if file == "" {
		file = p.CurrentFile
	}
	if file != "" {
		label := "→ "
		if !p.WalkComplete && p.WalkingFile != "" {
			label = "walking "
		} else if p.WalkComplete {
			label = "done "
		}
		lines = append(lines, label+truncateDisplay(displayIndexPath(m.session, file), width-len(label)-2))
	}
	if len(m.indexRecent) > 0 {
		recent := m.indexRecent
		if len(recent) > 4 {
			recent = recent[len(recent)-4:]
		}
		lines = append(lines, "recent "+truncateDisplay(strings.Join(recent, " · "), width-8))
	}
	if len(p.Errors) > 0 {
		lines = append(lines, warnStyle.Render(fmt.Sprintf("%d warnings", len(p.Errors))))
	}
	lines = append(lines, mutedStyle.Render("esc cancel index"))
	return strings.Join(lines, "\n")
}

func formatElapsedSuffix(elapsed string) string {
	if elapsed == "" {
		return ""
	}
	return "  " + elapsed
}

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

func (m Model) statusBody() string {
	if m.status == nil {
		if m.sessionLoading {
			return "status loading…"
		}
		return "status unavailable"
	}

	fresh := "unknown"
	if m.status.Freshness != nil {
		fresh = string(m.status.Freshness.State)
		if m.status.Freshness.Reason != "" {
			fresh += " — " + m.status.Freshness.Reason
		}
	} else if m.status.PendingChanges != nil {
		if m.status.IndexFresh {
			fresh = "yes"
		} else {
			fresh = "no"
		}
	} else if m.status.IndexFresh {
		fresh = "yes"
	}

	lines := []string{}
	if m.hasReadiness {
		lines = append(lines,
			fmt.Sprintf("Readiness:    %s", m.readiness.State),
		)
		if m.readiness.Reason != "" {
			lines = append(lines, fmt.Sprintf("Reason:       %s", m.readiness.Reason))
		}
		if m.readiness.Action != "" {
			lines = append(lines, fmt.Sprintf("Action:       %s", m.readiness.Action))
		}
		lines = append(lines, "")
	}
	if m.branchName != "" {
		lines = append(lines, fmt.Sprintf("Branch:       %s", m.branchName))
	}
	lines = append(lines,
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
		fmt.Sprintf("Codemap:      %v", m.status.HasCodemapGraph),
		"",
		fmt.Sprintf("Fresh:        %s", fresh),
		fmt.Sprintf("Projects:     %d", m.status.Stats["projects"]),
		fmt.Sprintf("Files:        %d", m.status.Stats["files"]),
		fmt.Sprintf("Chunks:       %d", m.status.Stats["chunks"]),
		fmt.Sprintf("Embeddings:   %d", m.status.Stats["embeddings"]),
		fmt.Sprintf("Source bytes: %s", formatBytes(m.status.IndexedBytes)),
	)
	if !m.status.LatestIndexedAt.IsZero() {
		lines = append(lines, fmt.Sprintf("Latest:       %s", formatTimeAgo(m.status.LatestIndexedAt)))
	}
	if m.status.ProfilePath != "" {
		lines = append(lines, fmt.Sprintf("Profile path: %s", m.status.ProfilePath))
	}
	if m.status.ReceiptError != "" {
		lines = append(lines, "", errorStyle.Render("Receipt: "+m.status.ReceiptError))
	}
	if m.status.PendingChanges != nil {
		lines = append(lines, "")
		tw := clamp(m.width-10, 28, 40)
		lines = append(lines, pendingTable(
			m.status.PendingChanges.NewFiles,
			m.status.PendingChanges.ModifiedFiles,
			m.status.PendingChanges.DeletedFiles,
			tw,
		).View())
	}
	if m.status.MigrationWarning != "" {
		lines = append(lines, "",
			warnStyle.Render(fmt.Sprintf("Warning: %s", m.status.MigrationWarning)),
		)
	}
	if m.status.DetailedStats != nil {
		tables := renderStatsTables(
			m.status.DetailedStats.Languages,
			m.status.DetailedStats.ChunkTypes,
			clamp(m.width-8, 40, m.width-4),
		)
		if tables != "" {
			lines = append(lines, "", titleStyle.Render("Index breakdown"), tables)
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderStatus() string {
	return m.renderPanel("Status", m.statusBody(), m.width-2, true)
}

// liveOverlayView returns the scroll window for status/config/help using the
// overlay's YOffset/height but always re-reads the source body (fresh status).
func (m Model) liveOverlayView() string {
	content := ""
	switch m.activeView {
	case viewStatus:
		content = m.statusBody()
	case viewConfig:
		content = m.configBody()
	case viewHelp:
		content = m.helpBody()
	default:
		return m.overlay.View()
	}
	h := m.overlay.Height()
	if h <= 0 {
		h = clamp(m.height-4, 8, m.height)
	}
	// Leave room for panel chrome when clipping.
	vis := clamp(h-1, 4, h)
	y := m.overlay.YOffset()
	lines := strings.Split(content, "\n")
	if y < 0 {
		y = 0
	}
	if y >= len(lines) {
		y = max(0, len(lines)-1)
	}
	end := y + vis
	if end > len(lines) {
		end = len(lines)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines[y:end], "\n")
}

func (m Model) configBody() string {
	if m.session == nil {
		return "config unavailable"
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
	if cfg.Embedding.MaxBatchSize > 0 {
		lines = append(lines, fmt.Sprintf("embedding.max_batch_size: %d", cfg.Embedding.MaxBatchSize))
	}
	if cfg.Embedding.KeepAlive != "" {
		lines = append(lines, fmt.Sprintf("embedding.keep_alive:     %s", cfg.Embedding.KeepAlive))
	}
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
	return strings.Join(lines, "\n")
}

func (m Model) renderConfig() string {
	return panelStyle.Width(m.width - 2).Render(m.configBody())
}

func secretStatus(value string) string {
	if strings.TrimSpace(value) == "" {
		return "[not set]"
	}
	return "[set]"
}

func (m Model) helpBody() string {
	// Full Charm help columns + longer prose for Studio-specific flows.
	full := m.help.FullHelpView(m.keys.FullHelp())
	extra := strings.Join([]string{
		"",
		"More",
		"  g/G first/last · [ ] min-score · L/T lang/type · ! reset",
		"  / on results = fuzzy filter hits (Charm list) · esc clears filter",
		"  r plans first index / large trees; R always plans full reindex",
		"  Index: Charm spinner + gradient progress (honest % after walk)",
		"  Ollama-local: small empty projects auto-start after plan (<100 files)",
		"",
		"Readiness: empty · profile≠ · stale · ready",
		"Hybrid may fall back to keyword — footer shows a warning when it does.",
	}, "\n")
	return full + extra
}

func (m Model) renderHelp() string {
	return panelStyle.Width(m.width - 2).Render(m.helpBody())
}

func (m Model) renderFooter() string {
	var parts []string
	if m.indexing {
		// Detail lives in the index panel; footer stays a one-line status + cancel.
		if m.daemonIndex {
			elapsed := ""
			if !m.indexStarted.IsZero() {
				elapsed = " " + time.Since(m.indexStarted).Round(time.Second).String()
			}
			parts = append(parts, fmt.Sprintf("daemon reindex…%s", elapsed))
		} else if m.indexProgress != nil {
			p := m.indexProgress
			file := p.DisplayFile()
			if !p.WalkComplete {
				msg := fmt.Sprintf("discover walk %d · queue %d · embed %d",
					p.WalkedFiles, p.QueuedFiles, p.ProcessedFiles)
				if file != "" {
					msg += "  " + truncateDisplay(displayIndexPath(m.session, file), 28)
				}
				parts = append(parts, msg)
			} else {
				queued := p.QueuedFiles
				if queued == 0 {
					queued = p.TotalFiles
				}
				msg := fmt.Sprintf("embed %d/%d %d%%",
					p.ProcessedFiles, queued, int(p.HonestPercent()*100))
				if file != "" {
					msg += "  " + truncateDisplay(displayIndexPath(m.session, file), 28)
				}
				parts = append(parts, msg)
			}
		} else if m.statusMessage != "" {
			parts = append(parts, m.statusMessage)
		}
	} else if m.statusMessage != "" {
		parts = append(parts, m.statusMessage)
	}
	if len(m.warnings) > 0 && !m.searching {
		parts = append(parts, warnStyle.Render(truncateDisplay(m.warnings[0], 60)))
	}
	if m.errMessage != "" {
		parts = append(parts, errorStyle.Render(truncateDisplay(m.errMessage, clamp(m.width/2, 40, 100))))
	}
	if m.confirm != confirmNone {
		parts = append(parts, warnStyle.Render(m.confirmText()))
	}
	parts = append(parts, mutedStyle.Render(m.contextHints()))
	return strings.Join(parts, "  ")
}

func (m Model) contextHints() string {
	// Contextual one-liners for special modes; default uses Charm help bubble.
	if m.session == nil {
		return "i register  ? help  ctrl+c quit"
	}
	if m.confirm != confirmNone {
		return "y confirm  n/esc cancel"
	}
	if m.planningIndex {
		return m.spinner.View() + " planning…  ctrl+c quit"
	}
	if m.indexing {
		return "esc cancel index  ctrl+c quit"
	}
	if m.activeView != viewSearch {
		return "esc back  j/k scroll  ctrl+c quit"
	}
	if m.isTextFocus() {
		return "enter search  tab focus  esc results  ctrl+c quit"
	}
	if m.focus == focusResults {
		if m.resultList.SettingFilter() {
			return "filter results  enter apply  esc cancel"
		}
		return "j/k move  / filter  enter open  y yank  s similar"
	}
	// Charm help bubble short view (auto-styled key hints).
	return m.help.View(m.keys)
}

func (m Model) confirmText() string {
	root := m.projectRootDisplay()
	rootBit := ""
	if root != "" {
		rootBit = " @ " + truncateDisplay(root, 36)
	}
	switch m.confirm {
	case confirmDelete:
		path := m.selectedPath()
		if path == "" {
			return "delete selected file? y/n"
		}
		return fmt.Sprintf("delete %s from index? y/n", truncateDisplay(path, 40))
	case confirmFullReindex:
		if m.dryRun != nil {
			p := m.dryRun
			return fmt.Sprintf("full reindex%s? scan %d · embed %d · ~%d chunks · %s  y/n",
				rootBit, p.ScannedFiles, p.FilesToEmbed, p.EstimatedChunks, formatBytes(p.BytesScanned))
		}
		if m.status != nil {
			return fmt.Sprintf("full reindex%s (%d files / %d chunks)? y/n",
				rootBit, m.status.Stats["files"], m.status.Stats["chunks"])
		}
		return fmt.Sprintf("full reindex%s? y/n", rootBit)
	case confirmIndex:
		if m.dryRun != nil {
			p := m.dryRun
			kind := "index"
			if m.isIndexEmpty() {
				kind = "first index"
			} else if p.NeedsConfirm() {
				kind = "large scope"
			}
			return fmt.Sprintf("%s%s? scan %d · embed %d · ~%d chunks · %s  y/n",
				kind, rootBit, p.ScannedFiles, p.FilesToEmbed, p.EstimatedChunks, formatBytes(p.BytesScanned))
		}
		return fmt.Sprintf("index%s? y/n", rootBit)
	case confirmReset:
		if m.status != nil {
			return fmt.Sprintf("reset project index (%d files / %d chunks)? y/n",
				m.status.Stats["files"], m.status.Stats["chunks"])
		}
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

type countItem struct {
	name  string
	count int64
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
	w := 0
	for _, r := range value {
		rw := lipgloss.Width(string(r))
		if w+rw+3 > width {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "..."
}
