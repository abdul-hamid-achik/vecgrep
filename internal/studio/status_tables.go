package studio

import (
	"fmt"
	"strconv"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
)

// studioTableStyles matches the Monokai-ish Studio chrome.
func studioTableStyles() table.Styles {
	return table.Styles{
		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccent).
			Padding(0, 1).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(colorPanel).
			BorderBottom(true),
		Cell: lipgloss.NewStyle().
			Foreground(colorInk).
			Padding(0, 1),
		Selected: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorInk).
			Background(colorSelect).
			Padding(0, 1),
	}
}

// countTable builds a Charm table from name→count stats with share %.
func countTable(title string, counts map[string]int64, limit, width int) table.Model {
	items := sortedCounts(counts, limit)
	rows := make([]table.Row, 0, len(items))
	var total int64
	for _, it := range items {
		total += it.count
	}
	for _, it := range items {
		share := "—"
		if total > 0 {
			share = fmt.Sprintf("%.0f%%", 100*float64(it.count)/float64(total))
		}
		rows = append(rows, table.Row{it.name, strconv.FormatInt(it.count, 10), share})
	}

	nameW := clamp(width/2, 10, 22)
	countW := 8
	shareW := 7
	// Keep table within panel width.
	for nameW+countW+shareW+6 > width && nameW > 8 {
		nameW--
	}

	cols := []table.Column{
		{Title: title, Width: nameW},
		{Title: "count", Width: countW},
		{Title: "share", Width: shareW},
	}
	h := len(rows) + 1 // header + rows
	if h < 2 {
		h = 2
	}
	if h > 14 {
		h = 14
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithHeight(h),
		table.WithWidth(nameW+countW+shareW+4),
		table.WithFocused(false),
		table.WithStyles(studioTableStyles()),
	)
	return t
}

// pendingTable renders pending index changes as a compact Charm table.
func pendingTable(newFiles, modFiles, delFiles, width int) table.Model {
	rows := []table.Row{
		{"new", strconv.Itoa(newFiles)},
		{"modified", strconv.Itoa(modFiles)},
		{"deleted", strconv.Itoa(delFiles)},
	}
	nameW := clamp(width/2, 10, 16)
	countW := 10
	t := table.New(
		table.WithColumns([]table.Column{
			{Title: "pending", Width: nameW},
			{Title: "files", Width: countW},
		}),
		table.WithRows(rows),
		table.WithHeight(4),
		table.WithWidth(nameW+countW+4),
		table.WithFocused(false),
		table.WithStyles(studioTableStyles()),
	)
	return t
}

// renderStatsTables lays out language + chunk-type tables (side-by-side when wide).
func renderStatsTables(languages, chunkTypes map[string]int64, width int) string {
	if len(languages) == 0 && len(chunkTypes) == 0 {
		return ""
	}
	limit := 10
	half := clamp((width-6)/2, 28, 48)

	var left, right string
	if len(languages) > 0 {
		left = countTable("language", languages, limit, half).View()
	}
	if len(chunkTypes) > 0 {
		right = countTable("chunk type", chunkTypes, limit, half).View()
	}

	if left != "" && right != "" && width >= 72 {
		return lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	}
	if left != "" && right != "" {
		return left + "\n\n" + right
	}
	if left != "" {
		return left
	}
	return right
}
