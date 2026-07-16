package search

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// failingProvider always fails to embed, simulating an embedding provider
// (e.g. ollama) that went away between indexing and query time.
type failingProvider struct {
	dimensions int
	err        error
}

func (p *failingProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	return nil, p.err
}

func (p *failingProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, p.err
}

func (p *failingProvider) Model() string   { return "failing-embed" }
func (p *failingProvider) Dimensions() int { return p.dimensions }
func (p *failingProvider) Ping(ctx context.Context) error {
	return p.err
}
func (p *failingProvider) Warmup(ctx context.Context) (time.Duration, error) {
	return 0, p.err
}

// TestSearchWithOutcome_HybridDegradesToKeywordWithWarning is a regression
// test for silent embedder failures: when the provider fails at query time in
// hybrid mode, SearchWithOutcome must still return keyword results AND report
// the degradation as a warning — never silently.
func TestSearchWithOutcome_HybridDegradesToKeywordWithWarning(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := &failingProvider{dimensions: 768, err: errors.New("ollama connection refused")}
	searcher := NewSearcher(database, provider)

	opts := DefaultSearchOptions()
	opts.Mode = SearchModeHybrid

	outcome, err := searcher.SearchWithOutcome(context.Background(), "HandleError", opts)
	if err != nil {
		t.Fatalf("SearchWithOutcome should degrade, not fail: %v", err)
	}
	if len(outcome.Results) == 0 {
		t.Error("expected keyword fallback results for exact identifier match")
	}
	if outcome.Mode != SearchModeKeyword {
		t.Errorf("outcome.Mode = %q, want %q after degradation", outcome.Mode, SearchModeKeyword)
	}
	if len(outcome.Warnings) == 0 {
		t.Fatal("degraded search must carry a warning — silent fallback")
	}
	warning := outcome.Warnings[0]
	if !strings.Contains(warning, "keyword-only") || !strings.Contains(warning, "ollama connection refused") {
		t.Errorf("warning should explain the degradation and its cause, got: %q", warning)
	}
}

// TestSearch_HybridEmbedFailureIsFatal verifies the strict Search API still
// fails loudly on embedder errors (no silent fallback there either).
func TestSearch_HybridEmbedFailureIsFatal(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := &failingProvider{dimensions: 768, err: errors.New("ollama connection refused")}
	searcher := NewSearcher(database, provider)

	opts := DefaultSearchOptions()
	opts.Mode = SearchModeHybrid

	if _, err := searcher.Search(context.Background(), "HandleError", opts); err == nil {
		t.Error("Search should return an error when the embedder fails")
	}
}

// TestSearchWithOutcome_SemanticNeverDegrades verifies an explicit semantic
// request fails when embeddings are unavailable instead of silently changing
// search semantics.
func TestSearchWithOutcome_SemanticNeverDegrades(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := &failingProvider{dimensions: 768, err: errors.New("ollama connection refused")}
	searcher := NewSearcher(database, provider)

	opts := DefaultSearchOptions()
	opts.Mode = SearchModeSemantic

	if _, err := searcher.SearchWithOutcome(context.Background(), "HandleError", opts); err == nil {
		t.Error("semantic search should fail when the embedder fails, not degrade")
	}
}

// TestSearchWithOutcome_HealthyHybridHasNoWarnings verifies the happy path
// reports hybrid mode and no warnings.
func TestSearchWithOutcome_HealthyHybridHasNoWarnings(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	outcome, err := searcher.SearchWithOutcome(context.Background(), "HandleError", DefaultSearchOptions())
	if err != nil {
		t.Fatalf("SearchWithOutcome failed: %v", err)
	}
	if len(outcome.Warnings) != 0 {
		t.Errorf("healthy search should carry no warnings, got %v", outcome.Warnings)
	}
	if outcome.Mode != SearchModeHybrid {
		t.Errorf("outcome.Mode = %q, want %q", outcome.Mode, SearchModeHybrid)
	}
}
