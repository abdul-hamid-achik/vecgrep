package studio

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

func Run(ctx context.Context, startDir string) error {
	model := NewModel(ctx, startDir)
	finalModel, err := tea.NewProgram(model, tea.WithContext(ctx)).Run()
	if final, ok := finalModel.(Model); ok {
		// Stop any in-flight index before closing the DB so Close cannot hang
		// waiting on a cancelled-but-still-draining embed run.
		if final.indexCancel != nil {
			final.indexCancel()
		}
		if final.cancel != nil {
			final.cancel()
		}
		if final.session != nil {
			done := make(chan struct{})
			go func() {
				_ = final.session.Close()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				// Best-effort: process can exit even if veclite close is slow.
			}
		}
	}
	return err
}
