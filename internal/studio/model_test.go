package studio

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

func TestModelCyclesSearchMode(t *testing.T) {
	m := NewModel(context.Background(), "")
	if m.mode != search.SearchModeHybrid {
		t.Fatalf("initial mode = %s", m.mode)
	}
	m.cycleMode()
	if m.mode != search.SearchModeSemantic {
		t.Fatalf("mode after first cycle = %s", m.mode)
	}
	m.cycleMode()
	if m.mode != search.SearchModeKeyword {
		t.Fatalf("mode after second cycle = %s", m.mode)
	}
}

func TestModelUsesConfiguredDefaultSearchModeAfterSessionLoad(t *testing.T) {
	model := NewModel(context.Background(), "")
	updated, _ := model.Update(sessionLoadedMsg{
		session: &app.Session{
			Config: &config.Config{
				Search: config.SearchConfig{DefaultMode: "keyword"},
			},
		},
	})
	m := updated.(Model)
	if m.mode != search.SearchModeKeyword {
		t.Fatalf("mode after session load = %s, want keyword", m.mode)
	}
}

func TestModelLetsQueryInputReceivePrintableShortcutKeys(t *testing.T) {
	model := NewModel(context.Background(), "")
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "m", Code: 'm'}))
	m := updated.(Model)
	if m.mode != search.SearchModeHybrid {
		t.Fatalf("mode changed while typing query: %s", m.mode)
	}
	if m.query.Value() != "m" {
		t.Fatalf("query value = %q", m.query.Value())
	}
}

func TestModelCyclesModeOutsideQueryInput(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.focus = focusResults
	model.query.Blur()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "m", Code: 'm'}))
	m := updated.(Model)
	if m.mode != search.SearchModeSemantic {
		t.Fatalf("mode after shortcut = %s", m.mode)
	}
	if m.query.Value() != "" {
		t.Fatalf("query changed outside input: %q", m.query.Value())
	}
}

func TestModelLetsFilterInputsReceivePrintableShortcutKeys(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.focus = focusDirectory
	model.applyFocus()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "m", Code: 'm'}))
	m := updated.(Model)
	if m.mode != search.SearchModeHybrid {
		t.Fatalf("mode changed while typing directory filter: %s", m.mode)
	}
	if m.directory.Value() != "m" {
		t.Fatalf("directory value = %q", m.directory.Value())
	}
}

func TestModelTabCyclesThroughFilterFields(t *testing.T) {
	model := NewModel(context.Background(), "")
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m := updated.(Model)
	if m.focus != focusDirectory {
		t.Fatalf("focus after first tab = %v", m.focus)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(Model)
	if m.focus != focusFilePattern {
		t.Fatalf("focus after second tab = %v", m.focus)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(Model)
	if m.focus != focusLineRange {
		t.Fatalf("focus after third tab = %v", m.focus)
	}
}

func TestModelSelectionUpdatesPreview(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.results = []search.Result{
		{RelativePath: "a.go", Content: "func A() {}", StartLine: 1, EndLine: 1, Score: 1},
		{RelativePath: "b.go", Content: "func B() {}", StartLine: 7, EndLine: 7, Score: 1},
	}

	m.moveSelection(1)
	if m.selected != 1 {
		t.Fatalf("selected = %d", m.selected)
	}
	if got := m.preview.View(); !contains(got, "b.go:7-7") {
		t.Fatalf("preview did not update: %q", got)
	}
}

func TestModelWindowResizeSetsInputWidth(t *testing.T) {
	model := NewModel(context.Background(), "")
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m := updated.(Model)
	if m.query.Width() <= 0 {
		t.Fatal("query width was not set")
	}
	if m.directory.Width() <= 0 {
		t.Fatal("directory width was not set")
	}
	if m.filePattern.Width() <= 0 {
		t.Fatal("file pattern width was not set")
	}
	if m.lineRange.Width() <= 0 {
		t.Fatal("line range width was not set")
	}
	if m.preview.Width() <= 0 {
		t.Fatal("preview width was not set")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
