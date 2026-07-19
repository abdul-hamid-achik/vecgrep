package studio

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
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
	model.filtersOpen = true
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

func TestModelCtrlFFromFilterFocusesQuery(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.filtersOpen = true
	model.focus = focusDirectory
	model.applyFocus()

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'f', Mod: tea.ModCtrl}))
	m := updated.(Model)
	if m.focus != focusQuery {
		t.Fatalf("focus after ctrl+f = %v, want query", m.focus)
	}
	if !m.query.Focused() {
		t.Fatal("query input is not focused")
	}
	if m.directory.Focused() {
		t.Fatal("directory input should be blurred")
	}
}

func TestModelTabCyclesThroughFilterFields(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.filtersOpen = true
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

func TestModelTabWithoutFiltersCyclesQueryResultsPreview(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.filtersOpen = false
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m := updated.(Model)
	if m.focus != focusResults {
		t.Fatalf("focus after first tab = %v, want results", m.focus)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(Model)
	if m.focus != focusPreview {
		t.Fatalf("focus after second tab = %v, want preview", m.focus)
	}
}

func TestModelShiftTabFromQueryWrapsToPreview(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.filtersOpen = false

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	m := updated.(Model)
	if m.focus != focusPreview {
		t.Fatalf("focus after shift+tab from query = %v, want preview", m.focus)
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

func TestModelRenderFilterBarShowsSelectedState(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.mode = search.SearchModeSemantic
	m.langIdx = 1
	m.typeIdx = 1
	m.limit = 25

	got := m.renderFilterBar()
	for _, want := range []string{"mode semantic", "lang go", "type function", "limit 25"} {
		if !contains(got, want) {
			t.Fatalf("filter bar missing %q: %q", want, got)
		}
	}
}

func TestModelRenderResultRowIncludesMetadata(t *testing.T) {
	m := NewModel(context.Background(), "")
	row := m.renderResultRow(0, search.Result{
		RelativePath: "internal/search/search.go",
		StartLine:    42,
		Score:        0.91,
		Language:     "go",
		ChunkType:    "function",
		SymbolName:   "NewSearcher",
	}, 120)

	// Symbol is preferred over lang/type when present; score bar is shown.
	for _, want := range []string{"internal/search/search.go:42", "0.91", "NewSearcher"} {
		if !contains(row, want) {
			t.Fatalf("result row missing %q: %q", want, row)
		}
	}
}

func TestModelRenderResultRowTruncatesToWidth(t *testing.T) {
	m := NewModel(context.Background(), "")
	row := m.renderResultRow(0, search.Result{
		RelativePath: "internal/some/deeply/nested/package/with/a/very_long_file_name.go",
		StartLine:    128,
		Score:        0.77,
		Language:     "go",
		ChunkType:    "function",
		SymbolName:   "VeryLongSymbolName",
	}, 42)

	if got := lipgloss.Width(row); got > 42 {
		t.Fatalf("row width = %d, want <= 42: %q", got, row)
	}
	if !strings.HasSuffix(row, "...") {
		t.Fatalf("truncated row should end with ellipsis: %q", row)
	}
}

func TestModelRenderSearchShowsPolishedPanels(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.width = 120
	m.height = 32
	m.sessionLoading = false
	m.filtersOpen = true
	m.session = &app.Session{}
	m.results = []search.Result{{
		RelativePath: "internal/app/search.go",
		Content:      "func Search() {}",
		StartLine:    12,
		EndLine:      12,
		Score:        0.88,
		Language:     "go",
		ChunkType:    "function",
	}}
	m.updatePreview()

	got := m.renderSearch()
	for _, want := range []string{"Search", "Directory", "File glob", "Line range", "Results 1/1", "Preview internal/app/search.go:12"} {
		if !contains(got, want) {
			t.Fatalf("rendered search missing %q: %q", want, got)
		}
	}
}

func TestModelRenderPreviewEmptyState(t *testing.T) {
	m := NewModel(context.Background(), "")
	got := m.renderPreview()
	if !contains(got, "Select a result to preview.") {
		t.Fatalf("preview empty state missing: %q", got)
	}
}

func TestModelRenderUnavailableProjectDistinguishesOpenError(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.errMessage = "open database: failed to initialize veclite"

	got := m.renderUnavailableProject()
	if !contains(got, "Could not open this project.") {
		t.Fatalf("database error title missing: %q", got)
	}
	if !contains(got, "vecgrep reset --force") {
		t.Fatalf("rebuild guidance missing: %q", got)
	}
	if contains(got, "No vecgrep project found.") {
		t.Fatalf("database error should not use no-project copy: %q", got)
	}
}

func TestModelRenderUnavailableProjectOmitsResetHintForLiveLock(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.errMessage = "open database (read-only): veclite: database file is locked by PID 80994 (locked 2h ago)"

	got := m.renderUnavailableProject()
	if !contains(got, "Could not open this project.") {
		t.Fatalf("database error title missing: %q", got)
	}
	// A live lock must NOT advise the destructive reset --force.
	if contains(got, "vecgrep reset --force") {
		t.Fatalf("live-lock error must not suggest reset --force: %q", got)
	}
	// The lock message itself should still be shown.
	if !contains(got, "locked by PID") {
		t.Fatalf("lock detail missing: %q", got)
	}
}

func TestIsLockHeldByDaemon(t *testing.T) {
	// No daemon running in the test env, so even a genuine lock-error string
	// must report false (the studio only falls back to read-only when the
	// daemon is actually the holder).
	if isLockHeldByDaemon(nil) {
		t.Fatal("nil error should not be a daemon lock")
	}
	if isLockHeldByDaemon(errString("open database: not locked at all")) {
		t.Fatal("non-lock error should not be a daemon lock")
	}
	// A lock error with no daemon running → false (don't fall back to RO for
	// a non-daemon holder; surface the error instead).
	if isLockHeldByDaemon(errString("veclite: database file is locked by PID 123 (locked 1h ago)")) {
		t.Fatal("lock error with no daemon running should not trigger RO fallback")
	}
}

func TestReadOnlySetsStatusAndHeader(t *testing.T) {
	model := NewModel(context.Background(), "")
	updated, _ := model.Update(sessionLoadedMsg{
		session: &app.Session{
			Config: &config.Config{
				Search: config.SearchConfig{DefaultMode: "hybrid"},
			},
			ProjectRoot: "/tmp/proj",
			ProjectName: "proj",
		},
		status:   &app.StatusResponse{ProjectRoot: "/tmp/proj", Stats: map[string]int64{"files": 4, "chunks": 9}},
		readOnly: true,
	})
	m := updated.(Model)
	m.width = 120
	if !m.readOnly {
		t.Fatal("readOnly should be set after a readOnly session load")
	}
	if !contains(m.statusMessage, "read-only") {
		t.Fatalf("status should mention read-only, got %q", m.statusMessage)
	}
	if !contains(m.renderHeader(), "read-only") {
		t.Fatalf("header should show read-only indicator, got %q", m.renderHeader())
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestModelRenderUnavailableProjectOffersGlobalRegistration(t *testing.T) {
	m := NewModel(context.Background(), "")

	got := m.renderUnavailableProject()
	if !contains(got, "No vecgrep project found.") {
		t.Fatalf("no-project title missing: %q", got)
	}
	if !contains(got, "Press i to register") {
		t.Fatalf("global registration guidance missing: %q", got)
	}
	if !contains(got, "~/.vecgrep/projects") {
		t.Fatalf("global storage location missing: %q", got)
	}
}

func TestModelInitShortcutWorksWithoutProject(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.sessionLoading = false
	m.session = nil

	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "i", Code: 'i'}))
	next := updated.(Model)
	if cmd == nil {
		t.Fatal("expected init command")
	}
	if !next.sessionLoading {
		t.Fatal("model should enter loading state")
	}
	if next.statusMessage != "registering project" {
		t.Fatalf("status message = %q", next.statusMessage)
	}
}

func TestModelRenderIndexProgressShowsCountersWhenWalkComplete(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 100
	m.height = 30
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.indexing = true
	progress := index.Progress{
		TotalFiles:     10,
		QueuedFiles:    10,
		ProcessedFiles: 4,
		SkippedFiles:   2,
		TotalChunks:    18,
		WalkComplete:   true,
		Phase:          index.PhaseEmbed,
		CurrentFile:    "/repo/internal/search/search.go",
		BytesWalked:    1024 * 100,
		BytesProcessed: 1024 * 40,
		StartTime:      time.Now().Add(-2 * time.Second),
	}
	m.indexProgress = &progress

	got := m.renderIndexProgress(80)
	for _, want := range []string{"40%", "4/10 files", "skipped 2", "18", "internal/search/search.go", "Embedding"} {
		if !contains(got, want) {
			t.Fatalf("index progress missing %q: %q", want, got)
		}
	}
}

func TestModelRenderIndexProgressDiscoverPhaseNoPercent(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 100
	m.height = 30
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.indexing = true
	progress := index.Progress{
		QueuedFiles:    7,
		ProcessedFiles: 3,
		WalkedFiles:    20,
		SkippedFiles:   5,
		WalkComplete:   false,
		Phase:          index.PhaseDiscover,
		WalkingFile:    "internal/app/search.go",
		BytesWalked:    2048,
		BytesQueued:    1024,
		StartTime:      time.Now().Add(-2 * time.Second),
	}
	m.indexProgress = &progress

	got := m.renderIndexProgress(80)
	// Must not show a completion percent or final N/M ratio during discover.
	// (Copy may mention "no % yet" — that is allowed; reject numeric percents.)
	if contains(got, "3/7") || contains(got, " 3%") || contains(got, "40%") {
		t.Fatalf("discover phase must not show processed/queued ratio or fill percent: %q", got)
	}
	for _, want := range []string{"embedded 3", "queued 7", "walked 20", "internal/app/search.go", "not final"} {
		if !contains(got, want) {
			t.Fatalf("discover progress missing %q: %q", want, got)
		}
	}
}

func TestModelRenderIndexProgressShowsRateAndETA(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 100
	m.height = 30
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.indexing = true
	progress := index.Progress{
		TotalFiles:     100,
		QueuedFiles:    100,
		ProcessedFiles: 30,
		SkippedFiles:   0,
		TotalChunks:    45,
		WalkComplete:   true,
		Phase:          index.PhaseEmbed,
		CurrentFile:    "/repo/main.go",
		StartTime:      time.Now().Add(-10 * time.Second),
	}
	m.indexProgress = &progress

	got := m.renderIndexProgress(80)
	// With 30 files in 10s, rate should be ~3 files/s
	if !contains(got, "files/s") {
		t.Fatalf("index progress missing rate: %q", got)
	}
	if !contains(got, "ETA") {
		t.Fatalf("index progress missing ETA: %q", got)
	}
}

func TestModelRenderIndexProgressHidesRateWhenTooFewFiles(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 100
	m.height = 30
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.indexing = true
	progress := index.Progress{
		TotalFiles:     10,
		QueuedFiles:    10,
		ProcessedFiles: 0,
		SkippedFiles:   0,
		TotalChunks:    0,
		WalkComplete:   true,
		CurrentFile:    "",
		StartTime:      time.Now().Add(-500 * time.Millisecond),
	}
	m.indexProgress = &progress

	got := m.renderIndexProgress(80)
	// With 0 files processed and < 1s elapsed, rate should NOT appear
	if contains(got, "files/s") {
		t.Fatalf("index progress should not show rate with 0 files: %q", got)
	}
	if contains(got, "ETA") {
		t.Fatalf("index progress should not show ETA with 0 files: %q", got)
	}
}

func TestModelRenderIndexProgressLargeScopeWarning(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 100
	m.height = 30
	m.session = &app.Session{ProjectRoot: "/Users/me/projects"}
	m.indexing = true
	progress := index.Progress{
		WalkedFiles:    6000,
		QueuedFiles:    100,
		ProcessedFiles: 10,
		WalkComplete:   false,
		Phase:          index.PhaseDiscover,
		WalkingFile:    "other-repo/main.go",
		BytesWalked:    600 * 1024 * 1024,
	}
	m.indexProgress = &progress
	got := m.renderIndexProgress(80)
	if !contains(got, "large tree") {
		t.Fatalf("expected large-tree warning: %q", got)
	}
	if !contains(got, "esc cancel") {
		t.Fatalf("expected cancel hint: %q", got)
	}
}

func TestProgressLargeScope(t *testing.T) {
	if (index.Progress{WalkedFiles: 100}).LargeScope() {
		t.Fatal("small tree should not be large")
	}
	if !(index.Progress{WalkedFiles: 5000}).LargeScope() {
		t.Fatal("5000 walked files should be large")
	}
	if !(index.Progress{BytesWalked: 500 * 1024 * 1024}).LargeScope() {
		t.Fatal("500MiB should be large")
	}
}

func TestModelIndexPanelAlwaysShownWhileIndexing(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 120
	m.height = 40
	m.sessionLoading = false
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.indexing = true
	m.results = []search.Result{{RelativePath: "a.go", StartLine: 1, Content: "x", Score: 0.9}}
	m.rebuildResultList()
	m.sizeResultList()
	m.indexProgress = &index.Progress{
		WalkedFiles: 12, QueuedFiles: 4, ProcessedFiles: 1,
		WalkingFile: "cmd/main.go", WalkComplete: false, Phase: index.PhaseDiscover,
	}
	got := m.renderSearch()
	if !contains(got, "Indexing") {
		t.Fatalf("expected index panel title: %q", got)
	}
	if !contains(got, "cmd/main.go") {
		t.Fatalf("expected walking file in panel: %q", got)
	}
	if !contains(got, "a.go") {
		t.Fatalf("prior results should remain visible: %q", got)
	}
}

func TestModelResultListFuzzyFilterValue(t *testing.T) {
	ri := resultItem{
		result: search.Result{
			RelativePath: "internal/app/search.go",
			SymbolName:   "Search",
			Language:     "go",
			Score:        0.9,
			StartLine:    10,
		},
		ord: 1,
	}
	if !contains(ri.FilterValue(), "search.go") || !contains(ri.FilterValue(), "Search") {
		t.Fatalf("FilterValue = %q", ri.FilterValue())
	}
	if !contains(ri.Title(), "0.90") || !contains(ri.Title(), "search.go:10") {
		t.Fatalf("Title = %q", ri.Title())
	}
}

func TestRenderStatsTablesSideBySide(t *testing.T) {
	got := renderStatsTables(
		map[string]int64{"go": 10, "python": 3},
		map[string]int64{"function": 8, "block": 5},
		100,
	)
	if !contains(got, "language") || !contains(got, "go") || !contains(got, "chunk type") {
		t.Fatalf("tables missing content: %q", got)
	}
	if !contains(got, "count") || !contains(got, "share") {
		t.Fatalf("tables missing headers: %q", got)
	}
}

func TestPendingTableRenders(t *testing.T) {
	got := pendingTable(2, 3, 1, 36).View()
	if !contains(got, "new") || !contains(got, "modified") || !contains(got, "deleted") {
		t.Fatalf("pending table: %q", got)
	}
}

func TestModelStatusBodyIncludesTables(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.width = 120
	m.height = 40
	m.hasReadiness = true
	m.readiness = app.Readiness{State: app.ReadinessReady}
	m.status = &app.StatusResponse{
		ProjectRoot: "/repo",
		Provider:    "ollama",
		Model:       "nomic-embed-text",
		Stats:       map[string]int64{"files": 3, "chunks": 8, "projects": 1, "embeddings": 8},
		DetailedStats: &db.Stats{
			Languages:  map[string]int64{"go": 6, "markdown": 2},
			ChunkTypes: map[string]int64{"function": 5, "generic": 3},
		},
		PendingChanges: &index.PendingChanges{NewFiles: 1, ModifiedFiles: 2, DeletedFiles: 0},
	}
	got := m.statusBody()
	for _, want := range []string{"Index breakdown", "language", "go", "chunk type", "pending", "new"} {
		if !contains(got, want) {
			t.Fatalf("status body missing %q: %q", want, got)
		}
	}
}

func TestReadinessChipStyled(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.hasReadiness = true
	m.readiness = app.Readiness{State: app.ReadinessReady}
	if !contains(m.readinessChip(), "ready") {
		t.Fatalf("chip = %q", m.readinessChip())
	}
	m.readiness.State = app.ReadinessStale
	if !contains(m.readinessChip(), "stale") {
		t.Fatalf("chip = %q", m.readinessChip())
	}
}

func TestModelMouseWheelMovesSelection(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.width = 120
	m.height = 40
	m.session = &app.Session{}
	m.focus = focusResults
	m.results = []search.Result{
		{RelativePath: "a.go", StartLine: 1, Content: "a", Score: 1},
		{RelativePath: "b.go", StartLine: 2, Content: "b", Score: 1},
	}
	m.rebuildResultList()
	m.handleMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.selected != 1 {
		t.Fatalf("selected after wheel = %d", m.selected)
	}
}

func TestModelMouseClickFocusesPreview(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.width = 120
	m.height = 40
	m.session = &app.Session{}
	m.focus = focusResults
	m.handleMouseClick(tea.MouseClickMsg{Button: tea.MouseLeft, X: 100, Y: 10})
	if m.focus != focusPreview {
		t.Fatalf("focus = %v, want preview", m.focus)
	}
}

func TestModelRebuildResultListSelectsFirst(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.results = []search.Result{
		{RelativePath: "a.go", StartLine: 1, Score: 0.9, Content: "a"},
		{RelativePath: "b.go", StartLine: 2, Score: 0.8, Content: "b"},
	}
	m.rebuildResultList()
	if len(m.resultList.Items()) != 2 {
		t.Fatalf("items = %d", len(m.resultList.Items()))
	}
	m.moveSelection(1)
	if m.selected != 1 {
		t.Fatalf("selected = %d", m.selected)
	}
	if got := m.preview.View(); !contains(got, "b.go") {
		t.Fatalf("preview = %q", m.preview.View())
	}
}

func TestModelEmptyIndexCTA(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 100
	m.height = 30
	m.sessionLoading = false
	m.indexing = false
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.status = &app.StatusResponse{Stats: map[string]int64{"chunks": 0, "files": 0}}
	got := m.renderResults(80)
	if !contains(got, "No index yet") || !contains(got, "Press r") {
		t.Fatalf("empty index CTA missing: %q", got)
	}
}

func TestModelRenderConfigShowsCacheAndThrottle(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.width = 120
	m.height = 40
	enabled := true
	m.session = &app.Session{
		Config: &config.Config{
			Embedding: config.EmbeddingConfig{
				Provider:     "ollama",
				Model:        "nomic-embed-text",
				Dimensions:   768,
				MaxBatchSize: 32,
				KeepAlive:    "30m",
				Throttle:     config.ThrottleConfig{Enabled: &enabled, MaxInFlight: 4},
			},
		},
	}

	got := m.renderConfig()
	for _, want := range []string{
		"max_batch_size: 32",
		"keep_alive:     30m",
		"Throttle",
		"enabled:        true",
		"max_in_flight:  4",
		"Cache",
		"fcheap_stash:  true (default)",
	} {
		if !contains(got, want) {
			t.Fatalf("config render missing %q: %q", want, got)
		}
	}
}

func TestModelRenderHelpMentionsETAAndConfig(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.width = 120
	got := m.renderHelp()
	for _, want := range []string{
		"full reindex",
		"config",
		"status",
		"hybrid may fall back",
		"yank",
	} {
		if !contains(strings.ToLower(got), strings.ToLower(want)) {
			t.Fatalf("help render missing %q: %q", want, got)
		}
	}
}

func TestModelRenderStatusShowsVectorHealth(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.width = 120
	m.status = &app.StatusResponse{
		ProjectRoot:        "/repo",
		DataDir:            "/home/user/.vecgrep/projects/repo",
		VecLitePath:        "/home/user/.vecgrep/projects/repo/vectors.veclite",
		VectorBackend:      "veclite",
		VecliteVersion:     "0.17.0",
		Provider:           "ollama",
		Model:              "nomic-embed-text",
		Dimensions:         768,
		ProfilePath:        "/home/user/.vecgrep/projects/repo/embedding_profile.json",
		ProfileStatus:      "ok",
		ProfileMatches:     true,
		VecLiteSizeBytes:   4096,
		IndexedBytes:       2048,
		LatestIndexedAt:    time.Now().Add(-time.Minute),
		IndexFresh:         true,
		HNSWM:              16,
		HNSWEfConstruction: 200,
		HNSWEfSearch:       100,
		MigrationWarning:   "",
		Stats: map[string]int64{
			"projects":   1,
			"files":      3,
			"chunks":     8,
			"embeddings": 8,
		},
		DetailedStats: &db.Stats{
			Languages:  map[string]int64{"go": 6, "markdown": 2},
			ChunkTypes: map[string]int64{"function": 5, "generic": 3},
		},
		PendingChanges: &index.PendingChanges{},
	}

	got := m.renderStatus()
	for _, want := range []string{
		"VecLite size: 4.0 KiB",
		"Profile:      ok",
		"Fresh:",
		"Source bytes: 2.0 KiB",
		"Index breakdown",
		"language",
		"go",
		"chunk type",
		"function",
		"Veclite ver:  0.17.0",
		"HNSW:         M=16  efConstruction=200  efSearch=100",
	} {
		if !contains(got, want) {
			t.Fatalf("status render missing %q: %q", want, got)
		}
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

func TestModelQuestionMarkInQueryDoesNotOpenHelp(t *testing.T) {
	model := NewModel(context.Background(), "")
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "?", Code: '?'}))
	m := updated.(Model)
	if m.activeView != viewSearch {
		t.Fatalf("activeView = %v, want search (? should type into query)", m.activeView)
	}
	if m.query.Value() != "?" {
		t.Fatalf("query value = %q, want ?", m.query.Value())
	}
}

func TestModelQuestionMarkOutsideQueryOpensHelp(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.session = &app.Session{Config: &config.Config{}}
	model.focus = focusResults
	model.query.Blur()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "?", Code: '?'}))
	m := updated.(Model)
	if m.activeView != viewHelp {
		t.Fatalf("activeView = %v, want help", m.activeView)
	}
}

func TestModelSearchLoadedFocusesResultsAndSurfacesWarnings(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.session = &app.Session{Config: &config.Config{}}
	model.service = app.NewService(model.session)
	model.mode = search.SearchModeHybrid
	model.searchGen = 1
	updated, _ := model.Update(searchLoadedMsg{
		gen:   1,
		query: "auth",
		response: &app.SearchResponse{
			Results:  []search.Result{{RelativePath: "a.go", StartLine: 1, Score: 0.9, Content: "x"}},
			Mode:     search.SearchModeKeyword,
			Warnings: []string{"embedding provider unreachable; keyword-only"},
		},
	})
	m := updated.(Model)
	if m.focus != focusResults {
		t.Fatalf("focus = %v, want results", m.focus)
	}
	if m.effectiveMode != search.SearchModeKeyword {
		t.Fatalf("effectiveMode = %s", m.effectiveMode)
	}
	if len(m.warnings) != 1 {
		t.Fatalf("warnings = %v", m.warnings)
	}
	if !contains(m.statusMessage, "keyword") && !contains(m.statusMessage, "fell back") {
		t.Fatalf("status should mention fallback: %q", m.statusMessage)
	}
}

func TestModelIgnoresStaleSearchGen(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.searchGen = 2
	updated, _ := model.Update(searchLoadedMsg{
		gen:   1,
		query: "old",
		response: &app.SearchResponse{
			Results: []search.Result{{RelativePath: "old.go", StartLine: 1}},
		},
	})
	m := updated.(Model)
	if len(m.results) != 0 {
		t.Fatalf("stale search should be ignored, got %d results", len(m.results))
	}
}

func TestModelIndexReentryGuard(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.session = &app.Session{Config: &config.Config{}, ProjectRoot: "/repo"}
	model.service = app.NewService(model.session)
	model.indexing = true
	cmd := model.indexCmd(false)
	if cmd != nil {
		t.Fatal("indexCmd should no-op while already indexing")
	}
	if model.statusMessage != "already indexing" {
		t.Fatalf("status = %q", model.statusMessage)
	}
}

func TestModelConfirmDeleteShowsPath(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.results = []search.Result{{RelativePath: "internal/foo.go", StartLine: 3}}
	m.selected = 0
	m.confirm = confirmDelete
	got := m.confirmText()
	if !contains(got, "internal/foo.go") {
		t.Fatalf("confirm text missing path: %q", got)
	}
}

func TestModelReadinessBannerProfileMismatch(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.sessionLoading = false
	m.session = &app.Session{}
	m.hasReadiness = true
	m.readiness = app.Readiness{
		State:           app.ReadinessProfileMismatch,
		StoredProfileID: "stored:id",
		ActiveProfileID: "active:id",
	}
	got := m.readinessBanner()
	if !contains(got, "mismatch") && !contains(got, "Profile") {
		t.Fatalf("banner missing mismatch: %q", got)
	}
	if !contains(got, "R") {
		t.Fatalf("banner should mention R: %q", got)
	}
}

func TestModelNeutralFilterPlaceholders(t *testing.T) {
	m := NewModel(context.Background(), "")
	if m.directory.Placeholder != "directory prefix…" {
		t.Fatalf("directory placeholder = %q", m.directory.Placeholder)
	}
	if m.filePattern.Placeholder != "file glob…" {
		t.Fatalf("file placeholder = %q", m.filePattern.Placeholder)
	}
	if m.lineRange.Placeholder != "start-end…" {
		t.Fatalf("lines placeholder = %q", m.lineRange.Placeholder)
	}
}

func TestModelFiltersCollapsedByDefault(t *testing.T) {
	m := NewModel(context.Background(), "")
	if m.filtersOpen {
		t.Fatal("filters should start collapsed")
	}
	m.width = 120
	m.height = 32
	m.sessionLoading = false
	m.session = &app.Session{}
	got := m.renderSearch()
	if contains(got, "Directory") {
		t.Fatalf("collapsed filters should hide Directory panel: %q", got)
	}
	if !contains(got, "f filters") {
		t.Fatalf("should show filters chip: %q", got)
	}
}

func TestModelDeleteRemovesLocalResults(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.results = []search.Result{
		{RelativePath: "a.go", StartLine: 1, Content: "a"},
		{RelativePath: "b.go", StartLine: 2, Content: "b"},
	}
	m.selected = 0
	m.removeResultsForPath("a.go")
	if len(m.results) != 1 || m.results[0].RelativePath != "b.go" {
		t.Fatalf("results = %+v", m.results)
	}
}

func TestModelMoveSelectionGAndG(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.session = &app.Session{Config: &config.Config{}}
	model.focus = focusResults
	model.query.Blur()
	model.results = []search.Result{
		{RelativePath: "a.go", StartLine: 1, Content: "a"},
		{RelativePath: "b.go", StartLine: 2, Content: "b"},
		{RelativePath: "c.go", StartLine: 3, Content: "c"},
	}
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'}))
	m := updated.(Model)
	if m.selected != 2 {
		t.Fatalf("selected after G = %d", m.selected)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "g", Code: 'g'}))
	m = updated.(Model)
	if m.selected != 0 {
		t.Fatalf("selected after g = %d", m.selected)
	}
}

func TestModelHeaderShowsReadinessChip(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.width = 120
	m.hasReadiness = true
	m.readiness = app.Readiness{State: app.ReadinessStale}
	m.status = &app.StatusResponse{ProjectRoot: "/repo", Stats: map[string]int64{"files": 1, "chunks": 2}}
	got := m.renderHeader()
	if !contains(got, "stale") {
		t.Fatalf("header missing stale chip: %q", got)
	}
}

func TestModelIndexProgressShowsRecent(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 100
	m.height = 30
	m.sessionLoading = false
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.indexing = true
	progress := index.Progress{
		QueuedFiles: 10, ProcessedFiles: 4, WalkComplete: true,
		Phase: index.PhaseEmbed, StartTime: time.Now().Add(-2 * time.Second),
		CurrentFile: "/repo/a.go",
	}
	m.indexProgress = &progress
	m.indexRecent = []string{"a.go", "b.go", "c.go"}
	got := m.renderIndexProgress(80)
	if !contains(got, "recent") {
		t.Fatalf("progress missing recent files: %q", got)
	}
}

func TestModelPreviewSoftWrapEnabled(t *testing.T) {
	m := NewModel(context.Background(), "")
	if !m.preview.SoftWrap {
		t.Fatal("preview SoftWrap should be enabled")
	}
}

func TestModelEmptyIndexRPlansBeforeIndex(t *testing.T) {
	model := NewModel(context.Background(), "/repo")
	model.sessionLoading = false
	model.session = &app.Session{ProjectRoot: "/repo", Config: &config.Config{}}
	model.service = app.NewService(model.session)
	model.status = &app.StatusResponse{ProjectRoot: "/repo", Stats: map[string]int64{"files": 0, "chunks": 0}}
	model.focus = focusResults
	model.query.Blur()
	if !model.shouldPlanBeforeIndex() {
		t.Fatal("empty index should plan first")
	}
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
	m := updated.(Model)
	if cmd == nil {
		t.Fatal("expected plan command")
	}
	if !m.planningIndex {
		t.Fatal("planningIndex should be true")
	}
	if m.statusMessage != "planning index…" {
		t.Fatalf("status = %q", m.statusMessage)
	}
}

func TestModelDryRunLoadedEmptySmallAutoStarts(t *testing.T) {
	// Ollama-local: small empty projects skip y/n after plan.
	model := NewModel(context.Background(), "/repo")
	model.session = &app.Session{ProjectRoot: "/Users/me/small", Config: &config.Config{}}
	model.service = app.NewService(model.session)
	model.status = &app.StatusResponse{Stats: map[string]int64{"files": 0, "chunks": 0}}
	model.planningIndex = true
	updated, cmd := model.Update(dryRunLoadedMsg{
		preview: &index.DryRunPreview{
			ScannedFiles: 12, FilesToEmbed: 12, EstimatedChunks: 40, BytesScanned: 4096,
			NewFiles: 12,
		},
		full: false,
	})
	m := updated.(Model)
	if m.confirm != confirmNone {
		t.Fatalf("small empty should auto-start, confirm=%v", m.confirm)
	}
	if cmd == nil || !m.indexing {
		t.Fatal("expected index auto-start")
	}
}

func TestModelDryRunLoadedEmptyLargeSetsConfirmIndex(t *testing.T) {
	model := NewModel(context.Background(), "/repo")
	model.session = &app.Session{ProjectRoot: "/Users/me/projects"}
	model.status = &app.StatusResponse{Stats: map[string]int64{"files": 0, "chunks": 0}}
	model.planningIndex = true
	updated, cmd := model.Update(dryRunLoadedMsg{
		preview: &index.DryRunPreview{
			ScannedFiles: emptyIndexAutoStartMax, FilesToEmbed: emptyIndexAutoStartMax,
			EstimatedChunks: 400, BytesScanned: 4096, NewFiles: emptyIndexAutoStartMax,
		},
		full: false,
	})
	m := updated.(Model)
	if cmd != nil {
		t.Fatal("should not auto-start large empty without confirm")
	}
	if m.confirm != confirmIndex {
		t.Fatalf("confirm = %v, want confirmIndex", m.confirm)
	}
	got := m.confirmText()
	if !contains(got, "first index") || !contains(got, "projects") {
		t.Fatalf("confirm text = %q", got)
	}
}

func TestModelUsesCharmSpinnerAndProgress(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.sessionLoading = false
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.indexing = true
	m.indexProgress = &index.Progress{
		WalkedFiles: 10, QueuedFiles: 4, ProcessedFiles: 1,
		WalkComplete: true, Phase: index.PhaseEmbed,
		CurrentFile: "/repo/a.go",
	}
	// Advance spinner once so View is stable.
	m.spinner, _ = m.spinner.Update(m.spinner.Tick())
	got := m.renderIndexProgress(80)
	if !contains(got, "Embedding") {
		t.Fatalf("missing embedding phase: %q", got)
	}
	// Charm progress bar renders fill characters.
	if !contains(got, "files") {
		t.Fatalf("missing file counters: %q", got)
	}
}

func TestModelDryRunLoadedSmallIncrementalAutoStarts(t *testing.T) {
	model := NewModel(context.Background(), "/repo")
	model.session = &app.Session{ProjectRoot: "/repo", Config: &config.Config{}}
	model.service = app.NewService(model.session)
	model.status = &app.StatusResponse{Stats: map[string]int64{"files": 10, "chunks": 20}}
	model.planningIndex = true
	updated, cmd := model.Update(dryRunLoadedMsg{
		preview: &index.DryRunPreview{
			ScannedFiles: 10, FilesToEmbed: 2, EstimatedChunks: 5, BytesScanned: 100,
			ModifiedFiles: 2,
		},
		full: false,
	})
	m := updated.(Model)
	if m.confirm != confirmNone {
		t.Fatalf("small plan should not confirm, got %v", m.confirm)
	}
	if cmd == nil {
		t.Fatal("expected indexCmd auto-start")
	}
	if !m.indexing {
		t.Fatal("expected indexing flag after auto-start")
	}
}

func TestModelDryRunLoadedLargeNeedsConfirm(t *testing.T) {
	model := NewModel(context.Background(), "/repo")
	model.session = &app.Session{ProjectRoot: "/repo"}
	model.status = &app.StatusResponse{Stats: map[string]int64{"files": 100, "chunks": 200}}
	updated, cmd := model.Update(dryRunLoadedMsg{
		preview: &index.DryRunPreview{
			ScannedFiles: index.ConfirmScopeFiles, FilesToEmbed: index.ConfirmScopeFiles,
			EstimatedChunks: 50, BytesScanned: 1024,
		},
		full: false,
	})
	m := updated.(Model)
	if cmd != nil {
		t.Fatal("large plan must not auto-start")
	}
	if m.confirm != confirmIndex {
		t.Fatalf("confirm = %v", m.confirm)
	}
	if !contains(m.confirmText(), "large scope") {
		t.Fatalf("confirm text = %q", m.confirmText())
	}
}

func TestModelConfirmYOnIndexStartsIndex(t *testing.T) {
	model := NewModel(context.Background(), "/repo")
	model.session = &app.Session{ProjectRoot: "/repo", Config: &config.Config{}}
	model.service = app.NewService(model.session)
	model.confirm = confirmIndex
	model.pendingFull = false
	model.dryRun = &index.DryRunPreview{FilesToEmbed: 3}
	model.focus = focusResults
	model.query.Blur()
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	m := updated.(Model)
	if m.confirm != confirmNone {
		t.Fatal("confirm should clear")
	}
	if cmd == nil {
		t.Fatal("expected index command")
	}
	if !m.indexing {
		t.Fatal("indexing should start")
	}
}

func TestModelConfirmNClearsPlan(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.confirm = confirmFullReindex
	model.dryRun = &index.DryRunPreview{FilesToEmbed: 9}
	model.focus = focusResults
	model.query.Blur()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "n", Code: 'n'}))
	m := updated.(Model)
	if m.confirm != confirmNone || m.dryRun != nil {
		t.Fatalf("confirm=%v dryRun=%v", m.confirm, m.dryRun)
	}
	if m.statusMessage != "cancelled" {
		t.Fatalf("status = %q", m.statusMessage)
	}
}

func TestModelEscCancelsIndexWhileIndexing(t *testing.T) {
	canceled := false
	model := NewModel(context.Background(), "/repo")
	model.session = &app.Session{ProjectRoot: "/repo"}
	model.indexing = true
	model.indexCancel = func() { canceled = true }
	model.focus = focusResults
	model.query.Blur()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m := updated.(Model)
	if !canceled {
		t.Fatal("esc should cancel index")
	}
	if !contains(m.statusMessage, "canceling") {
		t.Fatalf("status = %q", m.statusMessage)
	}
}

func TestModelIndexDoneCanceledMessageIncludesRoot(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.session = &app.Session{ProjectRoot: "/Users/me/projects"}
	model.indexing = true
	model.indexGen = 1
	updated, _ := model.Update(indexDoneMsg{gen: 1, err: context.Canceled})
	m := updated.(Model)
	if m.indexing {
		t.Fatal("should clear indexing")
	}
	if !contains(m.statusMessage, "index canceled") || !contains(m.statusMessage, "projects") {
		t.Fatalf("status = %q", m.statusMessage)
	}
}

func TestModelConfirmTextFullReindexIncludesRoot(t *testing.T) {
	m := NewModel(context.Background(), "")
	m.session = &app.Session{ProjectRoot: "/repo/app"}
	m.confirm = confirmFullReindex
	m.dryRun = &index.DryRunPreview{ScannedFiles: 5, FilesToEmbed: 5, EstimatedChunks: 12, BytesScanned: 2048}
	got := m.confirmText()
	if !contains(got, "full reindex") || !contains(got, "/repo/app") || !contains(got, "scan 5") {
		t.Fatalf("confirm = %q", got)
	}
}

func TestModelFTogglesFilters(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.session = &app.Session{Config: &config.Config{}}
	model.focus = focusResults
	model.query.Blur()
	if model.filtersOpen {
		t.Fatal("start collapsed")
	}
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "f", Code: 'f'}))
	m := updated.(Model)
	if !m.filtersOpen || m.focus != focusDirectory {
		t.Fatalf("filtersOpen=%v focus=%v", m.filtersOpen, m.focus)
	}
	// From a filter field, esc collapses filters (f would type into the input).
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	m = updated.(Model)
	if m.filtersOpen {
		t.Fatal("esc should close filters")
	}
	if m.focus != focusResults {
		t.Fatalf("focus after close = %v", m.focus)
	}
	// f from results opens again; second f from results closes.
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "f", Code: 'f'}))
	m = updated.(Model)
	m.focus = focusResults
	m.applyFocus()
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "f", Code: 'f'}))
	m = updated.(Model)
	if m.filtersOpen {
		t.Fatal("f from results should close filters")
	}
}

func TestModelReadinessChipMatrix(t *testing.T) {
	cases := []struct {
		state app.ReadinessState
		want  string
	}{
		{app.ReadinessEmpty, "empty"},
		{app.ReadinessProfileMismatch, "profile≠"},
		{app.ReadinessStale, "stale"},
		{app.ReadinessUnknown, "fresh?"},
		{app.ReadinessReady, "ready"},
	}
	for _, tc := range cases {
		m := NewModel(context.Background(), "")
		m.hasReadiness = true
		m.readiness = app.Readiness{State: tc.state}
		if got := m.readinessChip(); !contains(got, tc.want) {
			t.Fatalf("state %s chip = %q want %q", tc.state, got, tc.want)
		}
	}
}

func TestModelConfirmBlocksModeCycle(t *testing.T) {
	model := NewModel(context.Background(), "")
	model.confirm = confirmDelete
	model.mode = search.SearchModeHybrid
	model.focus = focusResults
	model.query.Blur()
	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "m", Code: 'm'}))
	m := updated.(Model)
	if m.mode != search.SearchModeHybrid {
		t.Fatal("confirm should block mode cycle")
	}
	if m.confirm != confirmDelete {
		t.Fatal("confirm should remain")
	}
}

func TestModelRStartsPlan(t *testing.T) {
	model := NewModel(context.Background(), "/repo")
	model.session = &app.Session{ProjectRoot: "/repo", Config: &config.Config{}}
	model.service = app.NewService(model.session)
	model.focus = focusResults
	model.query.Blur()
	updated, cmd := model.Update(tea.KeyPressMsg(tea.Key{Text: "R", Code: 'R'}))
	m := updated.(Model)
	if cmd == nil || !m.planningIndex {
		t.Fatalf("cmd=%v planning=%v", cmd != nil, m.planningIndex)
	}
}
