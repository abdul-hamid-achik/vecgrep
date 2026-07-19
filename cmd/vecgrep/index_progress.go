package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// fileMsg advances the bar; doneMsg tells it indexing finished and it should
// quit (the real IndexResult is delivered out-of-band, see runIndexWithBar).
type (
	fileMsg struct {
		progress index.Progress
	}
	doneMsg struct{}
)

// indexProgressModel is a minimal inline Bubble Tea program: a single animated
// gradient progress bar that tracks indexing. It mirrors the look of codemap's
// index bar — blended purple→pink — with phase-aware counters so discover
// never shows a lying percentage.
type indexProgressModel struct {
	prog     progress.Model
	spin     spinner.Model
	progress index.Progress
	start    time.Time // set on the first tick; anchors the ETA estimate
	finished bool      // indexing reported done
	canceled bool      // user pressed ctrl+c
}

func newIndexProgressModel() indexProgressModel {
	spin := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("#66D9EF"))),
	)
	return indexProgressModel{
		prog: progress.New(progress.WithDefaultBlend(), progress.WithWidth(30)),
		spin: spin,
	}
}

func (m indexProgressModel) Init() tea.Cmd {
	return m.spin.Tick
}

func (m indexProgressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case fileMsg:
		if m.start.IsZero() {
			m.start = time.Now()
		}
		m.progress = msg.progress
		// Only drive the gradient fill when the walk is complete (honest %).
		if msg.progress.WalkComplete {
			return m, m.prog.SetPercent(msg.progress.HonestPercent())
		}
		// Indeterminate: leave bar at low fill; don't set a shrinking percent.
		return m, m.prog.SetPercent(0.08)
	case doneMsg:
		m.finished = true
		return m, tea.Quit
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.canceled = true // runIndexWithBar cancels the index and reports it
			return m, tea.Quit
		}
	case progress.FrameMsg:
		pm, cmd := m.prog.Update(msg)
		m.prog = pm
		return m, cmd
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if !m.finished {
			return m, cmd
		}
		return m, nil
	}
	return m, nil
}

func (m indexProgressModel) View() tea.View {
	if m.finished {
		return tea.NewView("") // clear the bar; the summary prints after the program quits
	}
	p := m.progress

	spin := m.spin.View()

	// Cold start / still discovering with no signal yet.
	if p.WalkedFiles == 0 && p.QueuedFiles == 0 && p.ProcessedFiles == 0 && !p.WalkComplete {
		return tea.NewView(spin + "  discovering…")
	}

	file := p.DisplayFile()
	if file == "" {
		file = p.CurrentFile
	}

	if !p.WalkComplete {
		// No honest %: counters only (never show embed/queued as a final N/M —
		// queued grows while walking and would look like 90/100 → 100/110).
		line := fmt.Sprintf("%s  walk %d · queue %d · embed %d", spin, p.WalkedFiles, p.QueuedFiles, p.ProcessedFiles)
		if p.SkippedFiles > 0 {
			line += fmt.Sprintf(" · skip %d", p.SkippedFiles)
		}
		if p.BytesWalked > 0 {
			line += " · " + humanBytes(p.BytesWalked)
		}
		if p.LargeScope() {
			line += "  ⚠ large"
		}
		if file != "" {
			line += "  " + truncStr(file, 28)
		}
		return tea.NewView(line)
	}

	// Walk complete: gradient bar + honest percent + classic N/M + ETA.
	line := m.prog.View()
	queued := p.QueuedFiles
	if queued == 0 {
		queued = p.TotalFiles
	}
	if queued == 0 && p.ProcessedFiles == 0 {
		// All-skip / nothing to embed.
		line += "  100%  0 to embed"
		if p.SkippedFiles > 0 {
			line += fmt.Sprintf("  skipped %d", p.SkippedFiles)
		}
		return tea.NewView(line)
	}
	pct := int(p.HonestPercent() * 100)
	line += fmt.Sprintf("  %d%%  %d/%d", pct, p.ProcessedFiles, queued)
	if p.BytesProcessed > 0 {
		line += "  " + humanBytes(p.BytesProcessed)
	}
	if eta := m.eta(); eta != "" {
		line += "  ~" + eta + " left"
	}
	if file != "" {
		line += "  " + truncStr(file, 28)
	}
	return tea.NewView(line) // inline (AltScreen defaults false) — a transient one-liner
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// eta returns a compact remaining-time estimate only after the walk completes
// and enough files have finished (avoid wild early guesses).
func (m indexProgressModel) eta() string {
	p := m.progress
	if !p.WalkComplete || m.start.IsZero() || p.ProcessedFiles < 2 {
		return ""
	}
	queued := p.QueuedFiles
	if queued == 0 {
		queued = p.TotalFiles
	}
	if queued <= p.ProcessedFiles {
		return ""
	}
	elapsed := time.Since(m.start)
	if elapsed < time.Second {
		return ""
	}
	rate := float64(p.ProcessedFiles) / elapsed.Seconds() // files per second
	if rate <= 0 {
		return ""
	}
	remaining := time.Duration(float64(queued-p.ProcessedFiles) / rate * float64(time.Second))
	return formatETA(remaining)
}

// runIndexWithBar runs an index while showing a live gradient progress bar,
// then returns the real *index.IndexResult. service.Index blocks, so it runs on
// a goroutine and feeds the bar via prog.Send; the authoritative result travels
// back on a buffered channel (not the model) so it survives interruption.
//
// A TUI failure never fails the index: indexing runs on the parent context, so
// if prog.Run errors (e.g. no usable terminal) we still wait for the index to
// finish and return its real result. Only a genuine ctrl+c cancels indexing —
// via a child context cancelled solely on that path, so the goroutine can't
// outlive this call and touch the DB after the session closes.
func runIndexWithBar(ctx context.Context, service *app.Service, req app.IndexRequest) (*index.IndexResult, error) {
	idxCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The bar is an inline (non-AltScreen) one-liner, so any concurrent write to
	// stderr — slog warnings from the embedder, the standard logger's warm-up
	// lines — interleaves with it and garbles the display. Hold both back while
	// the bar is on screen; warnings are counted and surfaced once it clears.
	// (This is why codemap's identical bar looks clean: indexing stays silent.)
	logs := &barLogHandler{}
	defer suppressLogs(logs)()

	prog := tea.NewProgram(newIndexProgressModel(), tea.WithContext(ctx))
	progressCB := func(p index.Progress) {
		prog.Send(fileMsg{progress: p})
	}

	type indexOut struct {
		res *index.IndexResult
		err error
	}
	resCh := make(chan indexOut, 1) // buffered: the goroutine never blocks on send
	go func() {
		res, err := service.Index(idxCtx, req, progressCB)
		resCh <- indexOut{res, err}
		prog.Send(doneMsg{}) // no-op if the program already quit
	}()

	finalModel, runErr := prog.Run()
	if runErr != nil {
		// The bar couldn't run (not a user action) — don't kill indexing; let it
		// finish silently and return the real result.
		out := <-resCh
		logs.report()
		return out.res, out.err
	}
	if m, ok := finalModel.(indexProgressModel); ok && m.canceled {
		cancel()      // stop indexing
		<-resCh       // wait for the goroutine to unwind
		logs.report() // surface any held-back warnings before exiting
		return nil, fmt.Errorf("indexing canceled")
	}
	out := <-resCh // doneMsg path: indexing already finished; authoritative result
	logs.report()
	return out.res, out.err
}

// suppressLogs routes both slog and the standard log package to the given
// swallowing handler and returns a restore func (call via defer). It snapshots
// the original std-log writer BEFORE installing the slog handler: slog.SetDefault
// re-points the std log package at the new handler as a side effect, so reading
// log.Writer() afterwards would capture the swallowing writer, not the real one.
// Restoring slog first, then the original writer, returns both to their true
// pre-call state — making the helper safe even if reused in a long-lived process.
func suppressLogs(h slog.Handler) func() {
	prevSlog := slog.Default()
	prevLogOut := log.Writer()
	slog.SetDefault(slog.New(h))
	log.SetOutput(io.Discard)
	return func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevLogOut)
	}
}

// barLogHandler swallows log records emitted while the live progress bar owns
// the terminal and tallies warnings (e.g. embedding truncation) so report() can
// surface a single concise line once the bar is gone, instead of letting each
// one corrupt the bar. It deliberately discards lower-severity output (warm-up,
// info) — the bar already conveys progress; run with --verbose to see logs live.
type barLogHandler struct {
	warnings atomic.Int64
}

// Enabled short-circuits records below Warn: they are discarded anyway (Handle
// only counts Warn+), so this saves slog the cost of building those records.
func (h *barLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

func (h *barLogHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn {
		h.warnings.Add(1)
	}
	return nil // swallow: never write to the terminal while the bar is live
}

func (h *barLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *barLogHandler) WithGroup(string) slog.Handler      { return h }

// report prints a one-line note if any warnings were held back during indexing.
func (h *barLogHandler) report() {
	if n := h.warnings.Load(); n > 0 {
		fmt.Printf("  ⚠ %d embedding warning(s) during indexing (run with --verbose to see them)\n", n)
	}
}

// truncStr shortens s to at most n runes, appending an ellipsis when clipped.
func truncStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}
