package studio

import "charm.land/lipgloss/v2"

var (
	colorInk         = lipgloss.Color("#E6E6E6")
	colorDim         = lipgloss.Color("#B7BCC6")
	colorMuted       = lipgloss.Color("#8A8F98")
	colorAccent      = lipgloss.Color("#66D9EF")
	colorWarn        = lipgloss.Color("#E6DB74")
	colorBad         = lipgloss.Color("#F92672")
	colorPanel       = lipgloss.Color("#3A3F4B")
	colorPanelActive = lipgloss.Color("#66D9EF")
	colorSelect      = lipgloss.Color("#2E5E6E")

	titleStyle            = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	mutedStyle            = lipgloss.NewStyle().Foreground(colorMuted)
	errorStyle            = lipgloss.NewStyle().Foreground(colorBad)
	warnStyle             = lipgloss.NewStyle().Foreground(colorWarn)
	panelTitleStyle       = lipgloss.NewStyle().Bold(true).Foreground(colorDim)
	activePanelTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	chipStyle             = lipgloss.NewStyle().Foreground(colorDim).Background(lipgloss.Color("#252932")).Padding(0, 1)
	activeChipStyle       = lipgloss.NewStyle().Foreground(colorInk).Background(colorSelect).Padding(0, 1)
	panelStyle            = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorPanel).Padding(0, 1)
	focusedPanelStyle     = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(colorPanelActive).Padding(0, 1)
	activeStyle           = lipgloss.NewStyle().Foreground(colorInk).Background(colorSelect)
)

func clamp(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
