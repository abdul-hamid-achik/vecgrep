package db

import (
	"testing"
	"time"
)

func TestOpen(t *testing.T) {
	tmpDir := t.TempDir()

	database, err := Open(tmpDir+"/test.db", 768, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Verify we can get the version
	version, err := database.VecVersion()
	if err != nil {
		t.Fatalf("Failed to get VecVersion: %v", err)
	}
	if version != "veclite" {
		t.Errorf("Expected veclite, got: %s", version)
	}
}

func TestVecVersion(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := Open(tmpDir+"/test.db", 768, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	version, err := db.VecVersion()
	if err != nil {
		t.Fatalf("VecVersion failed: %v", err)
	}

	if version != "veclite" {
		t.Errorf("VecVersion returned unexpected value: %s", version)
	}
}

func TestInsertAndSearchEmbedding(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create a test embedding
	embedding := make([]float32, dimensions)
	for i := range embedding {
		embedding[i] = float32(i) / float32(dimensions)
	}

	// Create a chunk record
	chunk := NewChunkRecord(
		"/tmp/test/main.go",
		"main.go",
		"abc123",
		100,
		"go",
		"func main() {}",
		1, 1, 0, 14,
		"function",
		"main",
		"/tmp/test",
	)

	// Insert chunk with embedding
	chunkID, err := db.InsertChunk(chunk, embedding)
	if err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	// Search for similar embeddings
	results, err := db.SearchEmbeddings(embedding, 10)
	if err != nil {
		t.Fatalf("SearchEmbeddings failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("SearchEmbeddings returned no results")
	}

	if results[0].ChunkID != int64(chunkID) {
		t.Errorf("Expected chunk ID %d, got %d", chunkID, results[0].ChunkID)
	}

	// Verify chunk metadata is returned
	if results[0].Chunk == nil {
		t.Error("Expected chunk metadata in result")
	} else {
		if results[0].Chunk.Content != "func main() {}" {
			t.Errorf("Expected content 'func main() {}', got '%s'", results[0].Chunk.Content)
		}
		if results[0].Chunk.Language != "go" {
			t.Errorf("Expected language 'go', got '%s'", results[0].Chunk.Language)
		}
	}
}

func TestInsertEmbeddingDimensionMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Try to insert embedding with wrong dimensions
	wrongEmbedding := make([]float32, 512)
	err = db.InsertEmbedding(1, wrongEmbedding)
	if err == nil {
		t.Error("Expected error for dimension mismatch, got nil")
	}
}

func TestDeleteFile(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	database, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Create test chunk
	embedding := make([]float32, dimensions)
	for i := range embedding {
		embedding[i] = float32(i) / float32(dimensions)
	}

	chunk := NewChunkRecord(
		"/tmp/test/main.go",
		"main.go",
		"abc123",
		100,
		"go",
		"func main() {}",
		1, 1, 0, 14,
		"function",
		"main",
		"/tmp/test",
	)

	_, err = database.InsertChunk(chunk, embedding)
	if err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	// Delete by file path
	ctx := t.Context()
	deleted, err := database.DeleteFile(ctx, "main.go")
	if err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}

	if deleted != 1 {
		t.Errorf("Expected 1 deleted, got %d", deleted)
	}

	// Verify deletion
	stats, err := database.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats["chunks"] != 0 {
		t.Errorf("Expected 0 chunks after deletion, got %d", stats["chunks"])
	}
}

func TestStats(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Insert a test chunk
	embedding := make([]float32, dimensions)
	chunk := NewChunkRecord(
		"/tmp/test/main.go",
		"main.go",
		"abc123",
		100,
		"go",
		"func main() {}",
		1, 1, 0, 14,
		"function",
		"main",
		"/tmp/test",
	)

	_, err = db.InsertChunk(chunk, embedding)
	if err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats["chunks"] != 1 {
		t.Errorf("Expected 1 chunk, got %d", stats["chunks"])
	}

	if stats["files"] != 1 {
		t.Errorf("Expected 1 file, got %d", stats["files"])
	}
}

func TestGetFileHashes(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	projectRoot := "/tmp/test"

	// Insert test chunks from different files
	files := []struct {
		relPath string
		hash    string
	}{
		{"main.go", "hash1"},
		{"utils.go", "hash2"},
		{"lib/helper.go", "hash3"},
	}

	embedding := make([]float32, dimensions)
	for _, f := range files {
		chunk := NewChunkRecord(
			projectRoot+"/"+f.relPath,
			f.relPath,
			f.hash,
			100,
			"go",
			"content",
			1, 1, 0, 7,
			"generic",
			"",
			projectRoot,
		)
		_, err = db.InsertChunk(chunk, embedding)
		if err != nil {
			t.Fatalf("InsertChunk failed: %v", err)
		}
	}

	// Get file hashes
	hashes, err := db.GetFileHashes(projectRoot)
	if err != nil {
		t.Fatalf("GetFileHashes failed: %v", err)
	}

	if len(hashes) != 3 {
		t.Errorf("Expected 3 files, got %d", len(hashes))
	}

	for _, f := range files {
		if hashes[f.relPath] != f.hash {
			t.Errorf("Expected hash '%s' for '%s', got '%s'", f.hash, f.relPath, hashes[f.relPath])
		}
	}
}

func TestDeleteFileMultipleChunks(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	database, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Insert multiple chunks for the same file
	embedding := make([]float32, dimensions)
	for i := range 3 {
		chunk := NewChunkRecord(
			"/tmp/test/main.go",
			"main.go",
			"abc123",
			100,
			"go",
			"content",
			i*10, i*10+10, 0, 7,
			"generic",
			"",
			"/tmp/test",
		)
		_, err = database.InsertChunk(chunk, embedding)
		if err != nil {
			t.Fatalf("InsertChunk failed: %v", err)
		}
	}

	// Delete the file
	ctx := t.Context()
	deleted, err := database.DeleteFile(ctx, "main.go")
	if err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}

	if deleted != 3 {
		t.Errorf("Expected 3 deleted chunks, got %d", deleted)
	}

	// Verify stats
	stats, err := database.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats["chunks"] != 0 {
		t.Errorf("Expected 0 chunks after deletion, got %d", stats["chunks"])
	}
}

func TestSearchWithFilter(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Insert chunks with different languages
	embedding := make([]float32, dimensions)
	langs := []string{"go", "python", "javascript", "go"}
	for i, lang := range langs {
		chunk := NewChunkRecord(
			"/tmp/test/file."+lang,
			"file."+lang,
			"hash"+string(rune('0'+i)),
			100,
			lang,
			"content for "+lang,
			1, 10, 0, 20,
			"generic",
			"",
			"/tmp/test",
		)
		_, err = db.InsertChunk(chunk, embedding)
		if err != nil {
			t.Fatalf("InsertChunk failed: %v", err)
		}
	}

	// Search with language filter
	results, err := db.SearchWithFilter(embedding, 10, FilterOptions{
		Language: "go",
	})
	if err != nil {
		t.Fatalf("SearchWithFilter failed: %v", err)
	}

	// Should return only Go files
	for _, r := range results {
		if r.Chunk != nil && r.Chunk.Language != "go" {
			t.Errorf("Expected language 'go', got '%s'", r.Chunk.Language)
		}
	}
}

func TestNewChunkRecord(t *testing.T) {
	before := time.Now()
	chunk := NewChunkRecord(
		"/path/to/file.go",
		"file.go",
		"abc123",
		500,
		"go",
		"func test() {}",
		1, 5, 0, 14,
		"function",
		"test",
		"/path/to",
	)
	after := time.Now()

	if chunk.FilePath != "/path/to/file.go" {
		t.Errorf("Unexpected FilePath: %s", chunk.FilePath)
	}
	if chunk.RelativePath != "file.go" {
		t.Errorf("Unexpected RelativePath: %s", chunk.RelativePath)
	}
	if chunk.IndexedAt.Before(before) || chunk.IndexedAt.After(after) {
		t.Errorf("IndexedAt should be between test start and end")
	}
}
