package db_test

import (
	"os"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

func TestVecLiteBackend(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "vecgrep-backend-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: 384,
		DataDir:    tmpDir,
	})
	if err != nil {
		t.Fatalf("Failed to open: %v", err)
	}
	defer database.Close()

	// Test VecVersion
	ver, err := database.VecVersion()
	if err != nil {
		t.Fatalf("VecVersion failed: %v", err)
	}
	if ver != "veclite" {
		t.Errorf("Expected veclite backend, got: %s", ver)
	}
	t.Logf("Backend version: %s", ver)

	// Test Insert
	embedding := make([]float32, 384)
	for i := range embedding {
		embedding[i] = float32(i) / 384.0
	}
	if err := database.InsertEmbedding(1, embedding); err != nil {
		t.Fatalf("InsertEmbedding failed: %v", err)
	}

	// Test Search
	results, err := database.SearchEmbeddings(embedding, 10)
	if err != nil {
		t.Fatalf("SearchEmbeddings failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if results[0].ChunkID != 1 {
		t.Fatalf("Expected chunk ID 1, got %d", results[0].ChunkID)
	}

	// Test GetEmbedding
	got, err := database.GetEmbedding(1)
	if err != nil {
		t.Fatalf("GetEmbedding failed: %v", err)
	}
	if len(got) != 384 {
		t.Fatalf("Expected 384 dimensions, got %d", len(got))
	}

	// Test Stats
	stats, err := database.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats["embeddings"] != 1 {
		t.Fatalf("Expected 1 embedding, got %d", stats["embeddings"])
	}

	// Test Delete
	if err := database.DeleteEmbedding(1); err != nil {
		t.Fatalf("DeleteEmbedding failed: %v", err)
	}

	t.Log("All operations successful for veclite backend")
}
