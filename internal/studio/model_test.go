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

func TestModelShiftTabFromQueryWrapsToPreview(t *testing.T) {
	model := NewModel(context.Background(), "")

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

	for _, want := range []string{"internal/search/search.go:42", "0.91", "go function NewSearcher"} {
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
	m.loading = false
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
		status:  &app.StatusResponse{ProjectRoot: "/tmp/proj", Stats: map[string]int64{"files": 4, "chunks": 9}},
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
	m.loading = false
	m.session = nil

	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "i", Code: 'i'}))
	next := updated.(Model)
	if cmd == nil {
		t.Fatal("expected init command")
	}
	if !next.loading {
		t.Fatal("model should enter loading state")
	}
	if next.statusMessage != "registering project" {
		t.Fatalf("status message = %q", next.statusMessage)
	}
}

func TestModelRenderIndexProgressShowsCountersAndRecentFiles(t *testing.T) {
	m := NewModel(context.Background(), "/repo")
	m.width = 100
	m.height = 30
	m.session = &app.Session{ProjectRoot: "/repo"}
	m.indexing = true
	progress := index.Progress{
		TotalFiles:     10,
		ProcessedFiles: 4,
		SkippedFiles:   2,
		TotalChunks:    18,
		CurrentFile:    "/repo/internal/search/search.go",
		StartTime:      time.Now().Add(-2 * time.Second),
	}
	m.indexProgress = &progress
	m.addIndexRecent(progress.CurrentFile)

	got := m.renderResults(80)
	for _, want := range []string{"40%", "4/10 files", "2 skipped", "18 chunks", "internal/search/search.go"} {
		if !contains(got, want) {
			t.Fatalf("index progress missing %q: %q", want, got)
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
		ProcessedFiles: 30,
		SkippedFiles:   0,
		TotalChunks:    45,
		CurrentFile:    "/repo/main.go",
		StartTime:      time.Now().Add(-10 * time.Second),
	}
	m.indexProgress = &progress

	got := m.renderResults(80)
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
		ProcessedFiles: 0,
		SkippedFiles:   0,
		TotalChunks:    0,
		CurrentFile:    "",
		StartTime:      time.Now().Add(-500 * time.Millisecond),
	}
	m.indexProgress = &progress

	got := m.renderResults(80)
	// With 0 files processed and < 1s elapsed, rate should NOT appear
	if contains(got, "files/s") {
		t.Fatalf("index progress should not show rate with 0 files: %q", got)
	}
	if contains(got, "ETA") {
		t.Fatalf("index progress should not show ETA with 0 files: %q", got)
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
		"ETA + rate",
		"config view (c)",
		"status view (v)",
		"hybrid / semantic / keyword",
	} {
		if !contains(got, want) {
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
		"Languages",
		"go 6",
		"Chunk types",
		"function 5",
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
