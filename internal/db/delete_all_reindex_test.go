package db_test

import (
	"context"
	"os"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

// TestDeleteAllThenReindex exercises the exact scenario the workaround in
// DeleteByProjectRoot guards against: insert records, delete them all, then
// insert fresh records and confirm search still returns correct results.
//
// If veclite v0.16.0 handles HNSW index cleanup correctly on delete-all,
// this test passes WITHOUT the DeleteAll-on-empty workaround, which means the
// workaround can be dropped.
func TestDeleteAllThenReindex(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vecgrep-delete-all-reindex-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: 4,
		DataDir:    tmpDir,
	})
	if err != nil {
		t.Fatalf("Failed to open: %v", err)
	}
	defer database.Close()

	// Insert a batch of chunks for project /proj.
	chunks := []db.ChunkRecord{
		{
			FilePath:     "/proj/a.go",
			RelativePath: "a.go",
			FileHash:     "hash-a-1",
			Language:     "go",
			Content:      "package a",
			StartLine:    1,
			EndLine:      1,
			ProjectRoot:  "/proj",
		},
		{
			FilePath:     "/proj/b.go",
			RelativePath: "b.go",
			FileHash:     "hash-b-1",
			Language:     "go",
			Content:      "package b",
			StartLine:    1,
			EndLine:      1,
			ProjectRoot:  "/proj",
		},
	}
	vectors := [][]float32{
		{1, 0, 0, 0},
		{0, 1, 0, 0},
	}
	if _, err := database.InsertChunkBatch(chunks, vectors); err != nil {
		t.Fatalf("InsertChunkBatch failed: %v", err)
	}

	// Confirm search works before delete.
	results, err := database.SearchWithFilter([]float32{1, 0, 0, 0}, 10, db.FilterOptions{ProjectRoot: "/proj"})
	if err != nil {
		t.Fatalf("Search before delete failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("Expected results before delete, got 0")
	}

	// Delete all records for the project. Reset() routes through
	// DeleteByProjectRoot, which is where the workaround lives.
	if err := database.Reset(context.Background(), "/proj"); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	// Collection should be empty now.
	count, err := database.Backend().Count()
	if err != nil {
		t.Fatalf("Count failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("Expected 0 records after delete-all, got %d", count)
	}

	// Re-insert fresh records — this is the scenario the workaround guards.
	freshChunks := []db.ChunkRecord{
		{
			FilePath:     "/proj/c.go",
			RelativePath: "c.go",
			FileHash:     "hash-c-1",
			Language:     "go",
			Content:      "package c",
			StartLine:    1,
			EndLine:      1,
			ProjectRoot:  "/proj",
		},
	}
	freshVectors := [][]float32{
		{0, 0, 1, 0},
	}
	if _, err := database.InsertChunkBatch(freshChunks, freshVectors); err != nil {
		t.Fatalf("InsertChunkBatch after delete-all failed: %v", err)
	}

	// Search for the fresh record. If HNSW is corrupted, this returns 0
	// results or a wrong result.
	results, err = database.SearchWithFilter([]float32{0, 0, 1, 0}, 10, db.FilterOptions{ProjectRoot: "/proj"})
	if err != nil {
		t.Fatalf("Search after re-index failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("Expected results after re-index, got 0 — HNSW index may be corrupted after delete-all")
	}
	if results[0].Chunk == nil {
		t.Fatalf("Expected chunk data in search result, got nil")
	}
	if results[0].Chunk.RelativePath != "c.go" {
		t.Fatalf("Expected c.go, got %s", results[0].Chunk.RelativePath)
	}
}
