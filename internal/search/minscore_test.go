package search

import (
	"context"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

// TestSearch_MinScoreFiltersBelowThreshold asserts the existing MinScore
// filter (honored in Search) actually drops sub-threshold hits. A threshold
// above the maximum achievable score (1.0) must yield zero results, while the
// default (0) keeps them.
func TestSearch_MinScoreFiltersBelowThreshold(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	high := DefaultSearchOptions()
	high.MinScore = 2.0 // impossible: scores are 1-distance, capped at 1.0
	results, err := searcher.Search(context.Background(), "error handling", high)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("MinScore=2.0 returned %d results, want 0 (all filtered)", len(results))
	}

	none := DefaultSearchOptions() // MinScore defaults to 0
	results, err = searcher.Search(context.Background(), "error handling", none)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("MinScore=0 returned 0 results, want at least one")
	}
}

// TestSearchSimilarByID_MinScoreFilters asserts the MinScore filter is honored
// on the similar-by-ID path (the path the `similar` CLI command uses).
func TestSearchSimilarByID_MinScoreFilters(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	// Two chunks in the same project so similar-by-ID has a non-source hit.
	emb := make([]float32, 768)
	for i := range emb {
		emb[i] = 0.5
	}
	chunk1 := db.NewChunkRecord("/tmp/test/a.go", "a.go", "h1", 10, "go",
		"func A() {}", 1, 1, 0, 11, "function", "A", "/tmp/test")
	chunk2 := db.NewChunkRecord("/tmp/test/b.go", "b.go", "h2", 10, "go",
		"func B() {}", 1, 1, 0, 11, "function", "B", "/tmp/test")
	id1, err := database.InsertChunk(chunk1, emb)
	if err != nil {
		t.Fatalf("InsertChunk 1: %v", err)
	}
	if _, err := database.InsertChunk(chunk2, emb); err != nil {
		t.Fatalf("InsertChunk 2: %v", err)
	}

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	high := SimilarOptions{SearchOptions: SearchOptions{Limit: 10, MinScore: 2.0}, ExcludeSourceID: true}
	results, err := searcher.SearchSimilarByID(context.Background(), int64(id1), high)
	if err != nil {
		t.Fatalf("SearchSimilarByID failed: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("MinScore=2.0 returned %d similar results, want 0", len(results))
	}

	none := SimilarOptions{SearchOptions: SearchOptions{Limit: 10}, ExcludeSourceID: true}
	results, err = searcher.SearchSimilarByID(context.Background(), int64(id1), none)
	if err != nil {
		t.Fatalf("SearchSimilarByID failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("MinScore=0 returned 0 similar results, want at least one non-source hit")
	}
	// Source chunk must be excluded.
	for _, r := range results {
		if r.RelativePath == "a.go" {
			t.Errorf("similar results included the source chunk a.go; expected it excluded")
		}
	}
}
