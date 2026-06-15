package studio

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

func Run(ctx context.Context, startDir string) error {
	model := NewModel(ctx, startDir)
	finalModel, err := tea.NewProgram(model).Run()
	if final, ok := finalModel.(Model); ok && final.session != nil {
		_ = final.session.Close()
	}
	return err
}
