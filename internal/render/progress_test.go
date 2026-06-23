package render

import (
	"strings"
	"testing"
	"time"
)

func TestProgressBar(t *testing.T) {
	tests := []struct {
		name      string
		processed int
		total     int
		width     int
		wantSub   string // substring expected in output
	}{
		{"zero total", 0, 0, 20, "]   0%"},
		{"half", 5, 10, 10, "]  50%"},
		{"full", 10, 10, 10, "] 100%"},
		{"quarter", 1, 4, 8, "]  25%"},
		{"clamped width", 5, 10, 0, "]  50%"}, // width clamped to 1
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ProgressBar(tc.processed, tc.total, tc.width)
			if !strings.Contains(got, tc.wantSub) {
				t.Errorf("ProgressBar(%d, %d, %d) = %q, want substring %q", tc.processed, tc.total, tc.width, got, tc.wantSub)
			}
		})
	}
}

func TestProgressBarHasEqualAndDashFill(t *testing.T) {
	got := ProgressBar(3, 4, 8)
	// 3/4 of 8 = 6 filled, 2 empty
	if !strings.Contains(got, "======") {
		t.Errorf("expected 6 '=' chars, got %q", got)
	}
	if !strings.Contains(got, "--") {
		t.Errorf("expected 2 '-' chars, got %q", got)
	}
}

func TestIndexProgressLine(t *testing.T) {
	got := IndexProgressLine(5, 10, 42, 90*time.Second, 20)
	if !strings.Contains(got, "5/10 files") {
		t.Errorf("missing file count, got %q", got)
	}
	if !strings.Contains(got, "42 chunks") {
		t.Errorf("missing chunk count, got %q", got)
	}
	if !strings.Contains(got, "1m30s") {
		t.Errorf("missing elapsed, got %q", got)
	}
}
