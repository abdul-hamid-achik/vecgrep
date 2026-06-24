package main

import (
	"context"
	"fmt"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// fileMsg advances the bar to a processed file; doneMsg tells it indexing
// finished and it should quit (the real IndexResult is delivered out-of-band,
// see runIndexWithBar).
type (
	fileMsg struct {
		done, total int
		file        string
	}
	doneMsg struct{}
)

// indexProgressModel is a minimal inline Bubble Tea program: a single animated
// gradient progress bar that tracks indexing, file by file. It mirrors the
// look of codemap's index bar — a blended purple→pink bar with percentage,
// file count, and the current path.
type indexProgressModel struct {
	prog        progress.Model
	done, total int
	file        string
	finished    bool // indexing reported done
	canceled    bool // user pressed ctrl+c
}

func newIndexProgressModel() indexProgressModel {
	return indexProgressModel{prog: progress.New(progress.WithDefaultBlend(), progress.WithWidth(30))}
}

func (m indexProgressModel) Init() tea.Cmd { return nil }

func (m indexProgressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case fileMsg:
		m.done, m.total, m.file = msg.done, msg.total, msg.file
		var pct float64
		if msg.total > 0 {
			pct = float64(msg.done) / float64(msg.total)
		}
		return m, m.prog.SetPercent(pct)
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
	}
	return m, nil
}

func (m indexProgressModel) View() tea.View {
	if m.finished {
		return tea.NewView("") // clear the bar; the summary prints after the program quits
	}
	line := m.prog.View()
	if m.total > 0 {
		line += fmt.Sprintf("  %d/%d", m.done, m.total)
	}
	if m.file != "" {
		line += "  " + truncStr(m.file, 26)
	}
	return tea.NewView(line) // inline (AltScreen defaults false) — a transient one-liner
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

	prog := tea.NewProgram(newIndexProgressModel(), tea.WithContext(ctx))
	progressCB := func(p index.Progress) {
		prog.Send(fileMsg{done: p.ProcessedFiles, total: p.TotalFiles, file: p.CurrentFile})
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
		return out.res, out.err
	}
	if m, ok := finalModel.(indexProgressModel); ok && m.canceled {
		cancel() // stop indexing
		<-resCh  // wait for the goroutine to unwind
		return nil, fmt.Errorf("indexing canceled")
	}
	out := <-resCh // doneMsg path: indexing already finished; authoritative result
	return out.res, out.err
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
