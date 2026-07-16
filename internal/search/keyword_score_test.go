package search

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

func keywordSearchResult(chunkID int64, relPath string, bm25 float32) db.SearchResult {
	return db.SearchResult{
		ChunkID:  chunkID,
		Distance: bm25,
		Chunk: &db.ChunkRecord{
			FilePath:     "/tmp/test/" + relPath,
			RelativePath: relPath,
			Content:      "func Match() {}",
			Language:     "go",
		},
	}
}

// TestConvertOutcomeResults_KeywordScoresNormalized verifies keyword-mode BM25
// scores are normalized to 0-1 within the result set (top hit = 1.0) so
// MinScore is meaningful, while Distance keeps the raw BM25 value.
func TestConvertOutcomeResults_KeywordScoresNormalized(t *testing.T) {
	searchResults := []db.SearchResult{
		keywordSearchResult(1, "a.go", 8.0),
		keywordSearchResult(2, "b.go", 4.0),
		keywordSearchResult(3, "c.go", 1.0),
	}

	results := convertOutcomeResults(searchResults, SearchModeKeyword, 0.4, 10)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (MinScore=0.4 should drop the 1.0/8.0 hit)", len(results))
	}
	if results[0].Score != 1.0 {
		t.Errorf("top keyword Score = %v, want 1.0 (normalized by max BM25)", results[0].Score)
	}
	if results[1].Score != 0.5 {
		t.Errorf("second keyword Score = %v, want 0.5", results[1].Score)
	}
	// Distance must keep the raw BM25 value.
	if results[0].Distance != 8.0 || results[1].Distance != 4.0 {
		t.Errorf("Distances = %v, %v, want raw BM25 8.0, 4.0", results[0].Distance, results[1].Distance)
	}
}

// TestConvertOutcomeResults_NonKeywordModesUntouched verifies hybrid and
// semantic scores pass through unchanged — they are already calibrated 0-1
// values and must not be re-normalized within the result set.
func TestConvertOutcomeResults_NonKeywordModesUntouched(t *testing.T) {
	searchResults := []db.SearchResult{
		keywordSearchResult(1, "a.go", 0.8),
		keywordSearchResult(2, "b.go", 0.4),
	}

	for _, mode := range []SearchMode{SearchModeHybrid, SearchModeSemantic} {
		results := convertOutcomeResults(searchResults, mode, 0, 10)
		if len(results) != 2 {
			t.Fatalf("mode %s: got %d results, want 2", mode, len(results))
		}
		if results[0].Score != 0.8 || results[1].Score != 0.4 {
			t.Errorf("mode %s: Scores = %v, %v, want raw 0.8, 0.4", mode, results[0].Score, results[1].Score)
		}
	}
}

// TestSearchWithOutcome_DegradedKeywordScoresNormalized verifies the
// degraded-hybrid fallback normalizes exactly like an explicit keyword-mode
// search: the top hit scores 1.0, every score stays within 0-1, and the
// warning explains the score semantics.
func TestSearchWithOutcome_DegradedKeywordScoresNormalized(t *testing.T) {
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
		t.Fatal("expected keyword fallback results for exact identifier match")
	}
	if got := outcome.Results[0].Score; got != 1.0 {
		t.Errorf("degraded top Score = %v, want 1.0 (BM25 normalized within result set)", got)
	}
	for i, r := range outcome.Results {
		if r.Score < 0 || r.Score > 1.0001 {
			t.Errorf("degraded result %d Score %v outside [0,1]", i, r.Score)
		}
	}
	if len(outcome.Warnings) == 0 || !strings.Contains(outcome.Warnings[0], "normalized") {
		t.Errorf("degradation warning should explain BM25 normalization, got %v", outcome.Warnings)
	}

	// The degraded path must behave identically to an explicit keyword search.
	keywordOpts := DefaultSearchOptions()
	keywordOpts.Mode = SearchModeKeyword
	keywordResults, err := searcher.Search(context.Background(), "HandleError", keywordOpts)
	if err != nil {
		t.Fatalf("keyword Search failed: %v", err)
	}
	if len(keywordResults) != len(outcome.Results) {
		t.Fatalf("keyword mode returned %d results, degraded hybrid %d; want identical", len(keywordResults), len(outcome.Results))
	}
	for i := range keywordResults {
		if keywordResults[i].Score != outcome.Results[i].Score {
			t.Errorf("result %d: keyword Score %v != degraded Score %v", i, keywordResults[i].Score, outcome.Results[i].Score)
		}
	}
}

// TestSearchOptions_TextWeightReachesBackend verifies opts.TextWeight is
// plumbed through to the backend's hybrid fusion: a keyword-only match's
// fused score must grow with a larger explicit text weight.
func TestSearchOptions_TextWeightReachesBackend(t *testing.T) {
	tmpDir := t.TempDir()
	database, err := db.OpenWithOptions(db.OpenOptions{Dimensions: 8, DataDir: tmpDir})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	provider := newMockProvider(8)
	provider.embeddings["sentinel"] = []float32{1, 0, 0, 0, 0, 0, 0, 0}

	// Keyword match for "sentinel" that is vector-orthogonal to the query, so
	// its fused score is purely the text-weight contribution.
	content := "func sentinel() { return 42 }"
	chunk := db.NewChunkRecord(
		"/tmp/test/sentinel.go", "sentinel.go", "h1", 100, "go",
		content, 1, 1, 0, len(content), "function", "sentinel", "/tmp/test",
	)
	if _, err := database.InsertChunk(chunk, []float32{0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	searcher := NewSearcher(database, provider)

	score := func(textWeight float32) float32 {
		t.Helper()
		opts := DefaultSearchOptions()
		opts.VectorWeight = 0.7
		opts.TextWeight = textWeight
		results, err := searcher.Search(context.Background(), "sentinel", opts)
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}
		if len(results) == 0 {
			t.Fatal("expected the keyword match in hybrid results")
		}
		return results[0].Score
	}

	low := score(0.3)  // matches the derived default
	high := score(0.9) // normalized to 0.5625 alongside vectorWeight 0.7
	if high <= low {
		t.Errorf("Score with TextWeight=0.9 (%v) should exceed Score with TextWeight=0.3 (%v); TextWeight not reaching the backend?", high, low)
	}
}
