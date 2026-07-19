package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote. Used to assert report()'s gating behaviour.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestBarLogHandler_HandleCountsOnlyWarnAndAbove(t *testing.T) {
	h := &barLogHandler{}
	ctx := context.Background()
	for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		rec := slog.NewRecord(time.Time{}, lvl, "msg", 0)
		if err := h.Handle(ctx, rec); err != nil {
			t.Fatalf("Handle(%v) returned err: %v", lvl, err)
		}
	}
	if got := h.warnings.Load(); got != 2 {
		t.Errorf("warnings = %d, want 2 (only Warn+Error counted)", got)
	}
}

func TestBarLogHandler_EnabledThreshold(t *testing.T) {
	h := &barLogHandler{}
	ctx := context.Background()
	if h.Enabled(ctx, slog.LevelInfo) {
		t.Error("Enabled(Info) = true, want false (sub-Warn short-circuited)")
	}
	if !h.Enabled(ctx, slog.LevelWarn) {
		t.Error("Enabled(Warn) = false, want true")
	}
	if !h.Enabled(ctx, slog.LevelError) {
		t.Error("Enabled(Error) = false, want true")
	}
}

func TestBarLogHandler_WithAttrsGroupReturnSelf(t *testing.T) {
	h := &barLogHandler{}
	if h.WithAttrs(nil) == nil {
		t.Error("WithAttrs returned nil")
	}
	if h.WithGroup("g") == nil {
		t.Error("WithGroup returned nil")
	}
}

func TestBarLogHandler_ReportGating(t *testing.T) {
	// No warnings: report() must print nothing.
	h := &barLogHandler{}
	if out := captureStdout(t, h.report); out != "" {
		t.Errorf("report() with 0 warnings printed %q, want empty", out)
	}

	// With warnings: a single concise line mentioning the count and --verbose.
	h.warnings.Store(3)
	out := captureStdout(t, h.report)
	if !strings.Contains(out, "3") || !strings.Contains(out, "--verbose") {
		t.Errorf("report() output = %q, want it to mention count 3 and --verbose", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("report() should print exactly one line, got %q", out)
	}
}

// TestSuppressLogs_Restores locks in the fix for the log-restore ordering bug:
// after the returned restore runs, both slog.Default() and the std log writer
// must be byte-identical to their pre-call state (not left pointing at the
// swallowing handler), and during suppression the std log writer is discarded.
func TestSuppressLogs_Restores(t *testing.T) {
	beforeSlog := slog.Default()
	beforeOut := log.Writer()

	restore := suppressLogs(&barLogHandler{})

	if log.Writer() != io.Discard {
		t.Error("during suppression, std log writer should be io.Discard")
	}
	if slog.Default() == beforeSlog {
		t.Error("during suppression, slog.Default() should be swapped")
	}

	restore()

	if slog.Default() != beforeSlog {
		t.Error("slog.Default() not restored")
	}
	if log.Writer() != beforeOut {
		t.Error("std log writer not restored to its true original (the ordering bug)")
	}
}

func TestIndexProgressModel_ETA(t *testing.T) {
	tests := []struct {
		name     string
		model    indexProgressModel
		wantText bool // true => non-empty ETA expected
	}{
		{"zero start", indexProgressModel{progress: index.Progress{ProcessedFiles: 5, QueuedFiles: 100, WalkComplete: true}}, false},
		{"done<2", indexProgressModel{start: time.Now().Add(-2 * time.Second), progress: index.Progress{ProcessedFiles: 1, QueuedFiles: 100, WalkComplete: true}}, false},
		{"total<=done", indexProgressModel{start: time.Now().Add(-2 * time.Second), progress: index.Progress{ProcessedFiles: 10, QueuedFiles: 10, WalkComplete: true}}, false},
		{"walk open", indexProgressModel{start: time.Now().Add(-2 * time.Second), progress: index.Progress{ProcessedFiles: 10, QueuedFiles: 100, WalkComplete: false}}, false},
		{"elapsed<1s", indexProgressModel{start: time.Now(), progress: index.Progress{ProcessedFiles: 5, QueuedFiles: 100, WalkComplete: true}}, false},
		{"happy path", indexProgressModel{start: time.Now().Add(-2 * time.Second), progress: index.Progress{ProcessedFiles: 10, QueuedFiles: 100, WalkComplete: true}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.model.eta()
			if tc.wantText && got == "" {
				t.Errorf("eta() = empty, want a non-empty estimate")
			}
			if !tc.wantText && got != "" {
				t.Errorf("eta() = %q, want empty", got)
			}
		})
	}
}

func TestIndexProgressModel_View(t *testing.T) {
	// finished -> cleared (empty) so the summary prints on a clean line.
	finished := indexProgressModel{finished: true}
	if got := finished.View().Content; got != "" {
		t.Errorf("finished View = %q, want empty", got)
	}

	// walk open, no files yet -> discovering…
	warming := newIndexProgressModel()
	if got := warming.View().Content; !strings.Contains(got, "discovering") {
		t.Errorf("cold View = %q, want it to contain \"discovering\"", got)
	}

	// walk open with counters -> embed/queued, no percent.
	discovering := newIndexProgressModel()
	discovering.progress = index.Progress{
		ProcessedFiles: 3,
		QueuedFiles:    7,
		WalkedFiles:    20,
		SkippedFiles:   5,
		WalkComplete:   false,
		Phase:          index.PhaseDiscover,
	}
	got := discovering.View().Content
	// Discover shows walk/queue/embed counters — never a final N/M percent ratio.
	if !strings.Contains(got, "walk 20") || !strings.Contains(got, "queue 7") || !strings.Contains(got, "embed 3") {
		t.Errorf("discover View = %q, want walk/queue/embed counters", got)
	}
	if strings.Contains(got, "%") || strings.Contains(got, "3/7") {
		t.Errorf("discover View = %q, must not show percent or N/M ratio", got)
	}

	// walk complete -> shows percent / N/M.
	done := newIndexProgressModel()
	done.progress = index.Progress{
		ProcessedFiles: 3,
		QueuedFiles:    7,
		TotalFiles:     7,
		WalkComplete:   true,
		Phase:          index.PhaseEmbed,
	}
	if got := done.View().Content; !strings.Contains(got, "3/7") {
		t.Errorf("embed View = %q, want it to contain \"3/7\"", got)
	}
}
