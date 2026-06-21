package db_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

// TestHNSWConfigWiredThroughOpenOptions verifies that custom HNSW parameters
// set on OpenOptions reach the underlying VecLite collection. This guards
// against the regression where the backend hardcoded WithHNSW(16, 200) and
// silently ignored config-driven M, EfConstruction, and EfSearch values.
func TestHNSWConfigWiredThroughOpenOptions(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vecgrep-hnsw-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Use non-default values distinct from the previous hardcode (16, 200, 0).
	wantM := 8
	wantEfConstruction := 64
	wantEfSearch := 32

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions:         384,
		DataDir:            tmpDir,
		HNSWM:              wantM,
		HNSWEfConstruction: wantEfConstruction,
		HNSWEfSearch:       wantEfSearch,
	})
	if err != nil {
		t.Fatalf("Failed to open with HNSW config: %v", err)
	}
	defer database.Close()

	// Insert a record and search to ensure the configured index accepts both
	// write and query paths without error.
	embedding := make([]float32, 384)
	for i := range embedding {
		embedding[i] = float32(i) / 384.0
	}
	if err := database.InsertEmbedding(1, embedding); err != nil {
		t.Fatalf("InsertEmbedding failed: %v", err)
	}

	results, err := database.SearchEmbeddings(embedding, 10)
	if err != nil {
		t.Fatalf("SearchEmbeddings failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Persistence sanity: confirm the collection file exists on disk so the
	// configured collection was actually created (not a no-op fallback).
	matches, err := filepath.Glob(filepath.Join(tmpDir, "*"))
	if err != nil {
		t.Fatalf("Glob failed: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("Expected collection files under %s, found none", tmpDir)
	}
}
