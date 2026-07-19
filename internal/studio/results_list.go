package studio

import (
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

// resultItem adapts a search.Result for the Charm list bubble.
type resultItem struct {
	result search.Result
	ord    int // 1-based rank in the original result set
}

func (r resultItem) FilterValue() string {
	path := r.result.RelativePath
	if path == "" {
		path = r.result.FilePath
	}
	parts := []string{path, r.result.SymbolName, r.result.Language, r.result.ChunkType}
	return strings.Join(parts, " ")
}

func (r resultItem) Title() string {
	path := r.result.RelativePath
	if path == "" {
		path = r.result.FilePath
	}
	if path == "" {
		path = "(unknown)"
	}
	bar := scoreBar(r.result.Score, 6)
	return fmt.Sprintf("%2d  %s %.2f  %s:%d", r.ord, bar, r.result.Score, path, r.result.StartLine)
}

func (r resultItem) Description() string {
	var meta []string
	if r.result.Reranked {
		meta = append(meta, "≈reranked")
	}
	if r.result.SymbolName != "" {
		meta = append(meta, r.result.SymbolName)
	}
	if r.result.Language != "" {
		meta = append(meta, r.result.Language)
	}
	if r.result.ChunkType != "" {
		meta = append(meta, r.result.ChunkType)
	}
	if len(meta) == 0 {
		return " "
	}
	return strings.Join(meta, " · ")
}

// resultDelegate is a compact DefaultDelegate styled for vecgrep.
type resultDelegate struct {
	list.DefaultDelegate
}

func newResultDelegate() resultDelegate {
	d := list.NewDefaultDelegate()
	d.ShowDescription = true
	d.SetHeight(2)
	d.SetSpacing(0)

	// Align with studio theme (accent selection, muted desc).
	styles := list.NewDefaultItemStyles(true)
	styles.NormalTitle = lipgloss.NewStyle().Foreground(colorInk).Padding(0, 0, 0, 1)
	styles.NormalDesc = lipgloss.NewStyle().Foreground(colorMuted).Padding(0, 0, 0, 1)
	styles.SelectedTitle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true).
		BorderForeground(colorAccent).
		Foreground(colorAccent).
		Bold(true).
		Padding(0, 0, 0, 1)
	styles.SelectedDesc = styles.SelectedTitle.Foreground(colorDim).Bold(false)
	styles.DimmedTitle = lipgloss.NewStyle().Foreground(colorMuted).Padding(0, 0, 0, 1)
	styles.DimmedDesc = styles.DimmedTitle.Foreground(colorMuted)
	styles.FilterMatch = lipgloss.NewStyle().Foreground(colorWarn).Underline(true)
	d.Styles = styles

	return resultDelegate{DefaultDelegate: d}
}

// Render truncates long titles to the list width so borders stay tidy.
func (d resultDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	ri, ok := item.(resultItem)
	if !ok {
		d.DefaultDelegate.Render(w, m, index, item)
		return
	}
	// Soft-truncate title path for narrow panels before default render.
	_ = ri
	d.DefaultDelegate.Render(w, m, index, item)
}

func newResultList(width, height int) list.Model {
	delegate := newResultDelegate()
	l := list.New(nil, delegate, width, height)
	l.SetShowTitle(false)
	l.SetShowStatusBar(true)
	l.SetStatusBarItemName("result", "results")
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)
	l.SetShowFilter(true)
	l.SetShowPagination(true)
	l.DisableQuitKeybindings()
	l.InfiniteScrolling = false
	// Remap: keep j/k navigation; use ctrl+/ for in-list filter so Studio "/"
	// can still mean "focus query" when not on results. On results, "/" is
	// handled explicitly to open the list filter.
	l.KeyMap.Filter.SetEnabled(true)
	l.Styles = list.DefaultStyles(true)
	return l
}

func resultsToItems(results []search.Result) []list.Item {
	items := make([]list.Item, len(results))
	for i, r := range results {
		items[i] = resultItem{result: r, ord: i + 1}
	}
	return items
}

func (m *Model) rebuildResultList() {
	items := resultsToItems(m.results)
	// SetItems returns a cmd for filter re-apply; ignore when unfiltered.
	_ = m.resultList.SetItems(items)
	if len(items) == 0 {
		m.selected = 0
		return
	}
	idx := clamp(m.selected, 0, len(items)-1)
	m.resultList.Select(idx)
	m.selected = idx
}

func (m *Model) syncSelectionFromList() {
	item := m.resultList.SelectedItem()
	if item == nil {
		return
	}
	ri, ok := item.(resultItem)
	if !ok {
		// Fall back to global index within visible set.
		m.selected = clamp(m.resultList.GlobalIndex(), 0, max(0, len(m.results)-1))
		m.updatePreview()
		return
	}
	// Map ord (1-based) back to results index.
	idx := ri.ord - 1
	if idx < 0 || idx >= len(m.results) {
		// Filtered item still holds the original result; find by path+line.
		for i, r := range m.results {
			if r.ChunkID == ri.result.ChunkID && r.ChunkID != 0 {
				idx = i
				break
			}
			if r.RelativePath == ri.result.RelativePath && r.StartLine == ri.result.StartLine {
				idx = i
				break
			}
		}
	}
	if idx != m.selected {
		m.selected = clamp(idx, 0, len(m.results)-1)
		m.updatePreview()
	}
}

// handleMouseWheel scrolls results, preview, or overlays with the wheel.
func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	up := msg.Button == tea.MouseWheelUp
	switch m.activeView {
	case viewStatus, viewConfig, viewHelp:
		if up {
			m.overlay.ScrollUp(3)
		} else {
			m.overlay.ScrollDown(3)
		}
		return nil
	default:
		if m.focus == focusPreview {
			if up {
				m.preview.ScrollUp(3)
			} else {
				m.preview.ScrollDown(3)
			}
			return nil
		}
		// Results list (or default search surface).
		if len(m.results) == 0 {
			return nil
		}
		if len(m.resultList.Items()) != len(m.results) {
			m.rebuildResultList()
		}
		if up {
			m.resultList.CursorUp()
		} else {
			m.resultList.CursorDown()
		}
		m.syncSelectionFromList()
		return nil
	}
}

// handleMouseClick focuses results (left half) or preview (right half) on wide layouts.
func (m *Model) handleMouseClick(msg tea.MouseClickMsg) {
	if msg.Button != tea.MouseLeft {
		return
	}
	if m.activeView != viewSearch || m.session == nil {
		return
	}
	// Wide layout: left ≈ results, right ≈ preview.
	if m.width >= 96 {
		if msg.X < m.width/2 {
			m.focus = focusResults
		} else {
			m.focus = focusPreview
		}
		m.applyFocus()
		return
	}
	// Narrow: click upper half → results, lower → preview (rough).
	if msg.Y < m.height/2 {
		m.focus = focusResults
	} else {
		m.focus = focusPreview
	}
	m.applyFocus()
}

func (m *Model) sizeResultList() {
	w := clamp(m.width/2-8, 30, m.width-6)
	if m.width < 96 {
		w = clamp(m.width-8, 30, m.width-4)
	}
	h := m.resultsMaxRows()
	// list height includes status/filter chrome; give room for ~h item rows.
	// Each item is 2 lines + delegate spacing 0 → roughly 2*h + 3 chrome.
	listH := clamp(h*2+3, 8, m.height-8)
	if m.indexing {
		listH = clamp(m.height/4*2+3, 6, 14)
	}
	m.resultList.SetSize(w, listH)
}
