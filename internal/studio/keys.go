package studio

import (
	"fmt"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	key := msg.Keystroke()

	if m.confirm != confirmNone {
		switch key {
		case "y", "Y":
			action := m.confirm
			full := m.pendingFull
			m.confirm = confirmNone
			switch action {
			case confirmDelete:
				return m.deleteSelectedCmd(), true
			case confirmFullReindex:
				m.dryRun = nil
				return m.indexCmd(true), true
			case confirmIndex:
				m.dryRun = nil
				return m.indexCmd(full), true
			case confirmReset:
				return m.resetCmd(), true
			}
		case "n", "N", "esc":
			m.confirm = confirmNone
			m.dryRun = nil
			m.planningIndex = false
			m.statusMessage = "cancelled"
			return nil, true
		}
		return nil, true
	}

	if m.session == nil {
		switch key {
		case "ctrl+c", "q":
			return m.quitCmd(), true
		case "i":
			return m.initProjectCmd(), true
		case "?":
			// Only open help when not typing in the query box.
			if !m.isTextFocus() {
				m.activeView = viewHelp
				m.refreshOverlay()
				return nil, true
			}
		}
		// Unhandled keys fall through so text inputs still work in tests and
		// partial UI states; they are no-ops without a project.
	}

	// Overlay views (status/config/help): scroll + navigate.
	if m.activeView != viewSearch {
		switch key {
		case "ctrl+c", "q":
			return m.quitCmd(), true
		case "?", "esc":
			if m.activeView == viewHelp || key == "esc" {
				m.activeView = viewSearch
				return nil, true
			}
			m.activeView = viewHelp
			m.refreshOverlay()
			return nil, true
		case "v":
			if m.activeView == viewStatus {
				m.activeView = viewSearch
				return nil, true
			}
			m.activeView = viewStatus
			m.refreshOverlay()
			return m.reloadStatusCmd(), true
		case "c":
			if m.activeView == viewConfig {
				m.activeView = viewSearch
				return nil, true
			}
			m.activeView = viewConfig
			m.refreshOverlay()
			return nil, true
		case "j", "down":
			m.overlay.ScrollDown(1)
			return nil, true
		case "k", "up":
			m.overlay.ScrollUp(1)
			return nil, true
		case "pgdown", "d", "ctrl+d":
			m.overlay.PageDown()
			return nil, true
		case "pgup", "u", "ctrl+u":
			m.overlay.PageUp()
			return nil, true
		case "g", "home":
			m.overlay.GotoTop()
			return nil, true
		case "G", "end":
			m.overlay.GotoBottom()
			return nil, true
		}
		return nil, true
	}

	// Results list filtering: route keys to Charm list until filter closes.
	if m.focus == focusResults && m.resultList.SettingFilter() {
		if key == "ctrl+c" {
			return m.quitCmd(), true
		}
		var cmd tea.Cmd
		m.resultList, cmd = m.resultList.Update(msg)
		m.syncSelectionFromList()
		return cmd, true
	}

	// Text inputs: only non-printable / navigation shortcuts.
	if m.isTextFocus() {
		switch key {
		case "ctrl+c":
			// Always quit from text focus; cancel index first if needed.
			if m.indexCancel != nil {
				m.cancelIndex()
			}
			return m.quitCmd(), true
		case "esc":
			if m.indexing && m.indexCancel != nil {
				m.cancelIndex()
				return nil, true
			}
			// Collapse expanded filters first, then leave the text field.
			if m.filtersOpen && m.focus != focusQuery {
				m.filtersOpen = false
				m.focus = focusResults
				m.applyFocus()
				return nil, true
			}
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
		case "up":
			if m.focus == focusQuery {
				return m.historyBrowse(-1), true
			}
		case "down":
			if m.focus == focusQuery {
				return m.historyBrowse(1), true
			}
		}
		// Let textinput receive printable keys including "?".
		return nil, false
	}

	switch key {
	case "ctrl+c":
		// Always quit: cancel any index first so session.Close won't hang.
		if m.indexCancel != nil {
			m.cancelIndex()
		}
		return m.quitCmd(), true
	case "q":
		// Soft cancel: first q stops index; second quits.
		if m.indexing && m.indexCancel != nil {
			m.cancelIndex()
			return nil, true
		}
		return m.quitCmd(), true
	case "?":
		if m.activeView == viewHelp {
			m.activeView = viewSearch
			return nil, true
		}
		m.activeView = viewHelp
		m.refreshOverlay()
		return nil, true
	case "esc":
		if m.indexing {
			m.cancelIndex()
			return nil, true
		}
		if m.focus == focusResults && m.resultList.FilterState() != list.Unfiltered {
			var cmd tea.Cmd
			m.resultList, cmd = m.resultList.Update(msg)
			m.syncSelectionFromList()
			return cmd, true
		}
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
	case "ctrl+f":
		m.activeView = viewSearch
		m.focus = focusQuery
		m.applyFocus()
		return nil, true
	case "/":
		// On results: Charm list in-list fuzzy filter. Elsewhere: focus query.
		if m.focus == focusResults && len(m.results) > 0 {
			var cmd tea.Cmd
			m.resultList, cmd = m.resultList.Update(msg)
			return cmd, true
		}
		m.activeView = viewSearch
		m.focus = focusQuery
		m.applyFocus()
		return nil, true
	case "f":
		// Keep studio filter toggle; do not let list claim "f" for paging.
		m.filtersOpen = !m.filtersOpen
		if m.filtersOpen {
			m.focus = focusDirectory
			m.applyFocus()
		} else {
			m.focus = focusResults
			m.applyFocus()
		}
		m.resize()
		return nil, true
	case "enter":
		if m.focus == focusQuery {
			return m.searchCmd(), true
		}
		if m.focus == focusResults || m.focus == focusPreview {
			return m.openEditorCmd(), true
		}
	case "up", "k", "down", "j", "g", "G", "home", "end", "pgup", "pgdown", "u", "d", "ctrl+u", "ctrl+d", "left", "right", "h", "l", "b":
		if m.focus == focusResults && len(m.results) > 0 {
			if len(m.resultList.Items()) != len(m.results) {
				m.rebuildResultList()
			}
			// Page-up/down etc. go to Charm list (pagination + cursor).
			var cmd tea.Cmd
			m.resultList, cmd = m.resultList.Update(msg)
			m.syncSelectionFromList()
			return cmd, true
		}
		if m.focus == focusPreview {
			switch key {
			case "up", "k":
				m.preview.ScrollUp(1)
			case "down", "j":
				m.preview.ScrollDown(1)
			case "pgup", "u", "ctrl+u":
				m.preview.PageUp()
			case "pgdown", "d", "ctrl+d":
				m.preview.PageDown()
			case "g", "home":
				m.preview.GotoTop()
			case "G", "end":
				m.preview.GotoBottom()
			}
			return nil, true
		}
	case "m":
		m.cycleMode()
		m.markResultsDirty()
		if m.lastQuery != "" {
			return m.searchCmd(), true
		}
		return nil, true
	case "M":
		m.cycleModeReverse()
		m.markResultsDirty()
		if m.lastQuery != "" {
			return m.searchCmd(), true
		}
		return nil, true
	case "+", "=":
		m.limit = clamp(m.limit+5, 1, 100)
		m.markResultsDirty()
		return nil, true
	case "-":
		m.limit = clamp(m.limit-5, 1, 100)
		m.markResultsDirty()
		return nil, true
	case "[":
		m.minScore = clampFloat(m.minScore-minScoreStep, 0, 0.95)
		m.markResultsDirty()
		m.statusMessage = fmt.Sprintf("min-score %.2f", m.minScore)
		if m.lastQuery != "" {
			return m.searchCmd(), true
		}
		return nil, true
	case "]":
		m.minScore = clampFloat(m.minScore+minScoreStep, 0, 0.95)
		m.markResultsDirty()
		m.statusMessage = fmt.Sprintf("min-score %.2f", m.minScore)
		if m.lastQuery != "" {
			return m.searchCmd(), true
		}
		return nil, true
	case "L":
		m.langIdx = (m.langIdx + 1) % len(m.languages)
		m.markResultsDirty()
		if m.lastQuery != "" {
			return m.searchCmd(), true
		}
		return nil, true
	case "T":
		m.typeIdx = (m.typeIdx + 1) % len(m.types)
		m.markResultsDirty()
		if m.lastQuery != "" {
			return m.searchCmd(), true
		}
		return nil, true
	case "v":
		m.activeView = viewStatus
		// Prefer the already-loaded status immediately; async reload refreshes it.
		m.refreshOverlay()
		if m.status != nil {
			// Still revalidate in background, but content is already visible.
			return m.reloadStatusCmd(), true
		}
		return m.reloadStatusCmd(), true
	case "c":
		m.activeView = viewConfig
		m.refreshOverlay()
		return nil, true
	case "s":
		if len(m.results) == 0 {
			m.statusMessage = "select a result for similar"
			return nil, true
		}
		return m.similarSelectedCmd(), true
	case "y":
		return m.yankSelectedCmd(false), true
	case "Y":
		return m.yankSelectedCmd(true), true
	case "r":
		if m.indexing || m.planningIndex {
			m.statusMessage = "already indexing"
			return nil, true
		}
		// Empty / huge pending: plan first (wrong-folder gate). Else index now.
		if m.shouldPlanBeforeIndex() {
			return m.planIndexCmd(false), true
		}
		return m.indexCmd(false), true
	case "R":
		if m.indexing || m.planningIndex {
			m.statusMessage = "already indexing"
			return nil, true
		}
		if m.readOnly {
			m.pendingFull = true
			m.confirm = confirmFullReindex
			return nil, true
		}
		// Always plan + confirm before full reindex.
		return m.planIndexCmd(true), true
	case "x":
		if m.readOnly {
			m.statusMessage = "can't delete in read-only mode — stop the daemon (`vecgrep daemon stop`) first"
			return nil, true
		}
		if m.indexing {
			m.statusMessage = "can't delete while indexing"
			return nil, true
		}
		if len(m.results) > 0 {
			m.confirm = confirmDelete
			return nil, true
		}
		m.statusMessage = "select a result to delete its file"
		return nil, true
	case "!":
		if m.readOnly {
			m.statusMessage = "can't reset in read-only mode — stop the daemon (`vecgrep daemon stop`) first"
			return nil, true
		}
		if m.indexing {
			m.statusMessage = "can't reset while indexing"
			return nil, true
		}
		m.confirm = confirmReset
		return nil, true
	case "o":
		return m.openEditorCmd(), true
	}
	return nil, false
}

func (m *Model) historyBrowse(delta int) tea.Cmd {
	if len(m.queryHistory) == 0 {
		return nil
	}
	if m.historyIdx < 0 {
		if delta < 0 {
			m.historyIdx = len(m.queryHistory) - 1
		} else {
			return nil
		}
	} else {
		m.historyIdx += delta
		if m.historyIdx < 0 {
			m.historyIdx = 0
		}
		if m.historyIdx >= len(m.queryHistory) {
			m.historyIdx = -1
			m.query.SetValue("")
			return nil
		}
	}
	m.query.SetValue(m.queryHistory[m.historyIdx])
	m.query.CursorEnd()
	return nil
}

func (m *Model) nextFocus() {
	if !m.filtersOpen {
		// query → results → preview → query
		switch m.focus {
		case focusQuery:
			m.focus = focusResults
		case focusResults:
			m.focus = focusPreview
		default:
			m.focus = focusQuery
		}
		m.applyFocus()
		return
	}
	m.focus = (m.focus + 1) % focusCount
	m.applyFocus()
}

func (m *Model) prevFocus() {
	if !m.filtersOpen {
		switch m.focus {
		case focusQuery:
			m.focus = focusPreview
		case focusPreview:
			m.focus = focusResults
		default:
			m.focus = focusQuery
		}
		m.applyFocus()
		return
	}
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

	switch m.focus {
	case focusQuery:
		m.query.Focus()
	case focusDirectory:
		if m.filtersOpen {
			m.directory.Focus()
		}
	case focusFilePattern:
		if m.filtersOpen {
			m.filePattern.Focus()
		}
	case focusLineRange:
		if m.filtersOpen {
			m.lineRange.Focus()
		}
	}
}

func (m Model) isTextFocus() bool {
	if m.focus == focusQuery {
		return true
	}
	if !m.filtersOpen {
		return false
	}
	return m.focus == focusDirectory ||
		m.focus == focusFilePattern ||
		m.focus == focusLineRange
}

func (m *Model) moveSelection(delta int) {
	if len(m.results) == 0 {
		return
	}
	// Keep Charm list in sync when results were set outside searchLoadedMsg.
	if len(m.resultList.Items()) != len(m.results) {
		m.rebuildResultList()
	}
	if delta > 0 {
		for i := 0; i < delta; i++ {
			m.resultList.CursorDown()
		}
	} else if delta < 0 {
		for i := 0; i < -delta; i++ {
			m.resultList.CursorUp()
		}
	} else {
		m.resultList.Select(m.selected)
	}
	m.syncSelectionFromList()
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

func (m *Model) cycleModeReverse() {
	switch m.mode {
	case search.SearchModeHybrid:
		m.mode = search.SearchModeKeyword
	case search.SearchModeKeyword:
		m.mode = search.SearchModeSemantic
	default:
		m.mode = search.SearchModeHybrid
	}
}

func (m *Model) quitCmd() tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	return tea.Quit
}

func clampFloat(n, min, max float32) float32 {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// selectedPath returns the relative path of the current selection.
func (m Model) selectedPath() string {
	if len(m.results) == 0 || m.selected < 0 || m.selected >= len(m.results) {
		return ""
	}
	r := m.results[m.selected]
	if r.RelativePath != "" {
		return r.RelativePath
	}
	return r.FilePath
}

func (m Model) selectedPathLine() string {
	if len(m.results) == 0 || m.selected < 0 || m.selected >= len(m.results) {
		return ""
	}
	r := m.results[m.selected]
	path := r.RelativePath
	if path == "" {
		path = r.FilePath
	}
	return fmt.Sprintf("%s:%d", path, r.StartLine)
}
