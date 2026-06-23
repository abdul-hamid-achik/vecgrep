package render

import (
	"fmt"
	"strings"
	"time"
)

// ProgressBarWidth is the default width (in characters) of the CLI progress bar.
const ProgressBarWidth = 28

// ProgressBar renders a single-line ASCII progress bar suitable for
// carriage-return-based CLI updates. The format is:
//
//	[####------] 42%  120/285 files  1.2s
//
// If total is zero the bar is rendered empty and the percentage is shown as 0%.
func ProgressBar(processed, total, width int) string {
	if width < 1 {
		width = 1
	}
	percent := 0
	filled := 0
	if total > 0 {
		percent = clampInt((processed*100)/total, 0, 100)
		filled = clampInt((processed*width)/total, 0, width)
	}
	bar := "[" + strings.Repeat("=", filled) + strings.Repeat("-", width-filled) + "]"
	return fmt.Sprintf("%s %3d%%", bar, percent)
}

// IndexProgressLine renders a full single-line index-progress string for the
// CLI, including the bar, counts, chunks and elapsed time. It is meant to be
// printed with a leading carriage return (\r) during indexing.
func IndexProgressLine(processed, total, chunks int, elapsed time.Duration, width int) string {
	bar := ProgressBar(processed, total, width)
	return fmt.Sprintf("%s  %d/%d files  %d chunks  %s", bar, processed, total, chunks, elapsed.Round(time.Second))
}

func clampInt(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
