package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpen(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath, 768)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Verify database was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("Database file was not created")
	}
}

func TestVecVersion(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath, 768)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	version, err := db.VecVersion()
	if err != nil {
		t.Fatalf("VecVersion failed: %v", err)
	}

	if version == "" {
		t.Error("VecVersion returned empty string")
	}
}

func TestInsertAndSearchEmbedding(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dimensions := 768

	db, err := Open(dbPath, dimensions)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create a test project
	result, err := db.Exec(`INSERT INTO projects (name, root_path) VALUES (?, ?)`, "test", "/tmp/test")
	if err != nil {
		t.Fatalf("Insert project failed: %v", err)
	}
	projectID, _ := result.LastInsertId()

	// Create a test file
	result, err = db.Exec(`INSERT INTO files (project_id, path, relative_path, hash, size, language) VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, "/tmp/test/main.go", "main.go", "abc123", 100, "go")
	if err != nil {
		t.Fatalf("Insert file failed: %v", err)
	}
	fileID, _ := result.LastInsertId()

	// Create a test chunk
	result, err = db.Exec(`INSERT INTO chunks (file_id, content, start_line, end_line, start_byte, end_byte, chunk_type, symbol_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		fileID, "func main() {}", 1, 1, 0, 14, "function", "main")
	if err != nil {
		t.Fatalf("Insert chunk failed: %v", err)
	}
	chunkID, _ := result.LastInsertId()

	// Create a test embedding
	embedding := make([]float32, dimensions)
	for i := range embedding {
		embedding[i] = float32(i) / float32(dimensions)
	}

	err = db.InsertEmbedding(chunkID, embedding)
	if err != nil {
		t.Fatalf("InsertEmbedding failed: %v", err)
	}

	// Search for similar embeddings
	results, err := db.SearchEmbeddings(embedding, 10)
	if err != nil {
		t.Fatalf("SearchEmbeddings failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("SearchEmbeddings returned no results")
	}

	if results[0].ChunkID != chunkID {
		t.Errorf("Expected chunk ID %d, got %d", chunkID, results[0].ChunkID)
	}
}

func TestInsertEmbeddingDimensionMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dimensions := 768

	db, err := Open(dbPath, dimensions)
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

func TestDeleteEmbedding(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	dimensions := 768

	db, err := Open(dbPath, dimensions)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create test data
	result, err := db.Exec(`INSERT INTO projects (name, root_path) VALUES (?, ?)`, "test", "/tmp/test")
	if err != nil {
		t.Fatalf("Insert project failed: %v", err)
	}
	projectID, _ := result.LastInsertId()

	result, err = db.Exec(`INSERT INTO files (project_id, path, relative_path, hash, size) VALUES (?, ?, ?, ?, ?)`,
		projectID, "/tmp/test/main.go", "main.go", "abc123", 100)
	if err != nil {
		t.Fatalf("Insert file failed: %v", err)
	}
	fileID, _ := result.LastInsertId()

	result, err = db.Exec(`INSERT INTO chunks (file_id, content, start_line, end_line, start_byte, end_byte) VALUES (?, ?, ?, ?, ?, ?)`,
		fileID, "func main() {}", 1, 1, 0, 14)
	if err != nil {
		t.Fatalf("Insert chunk failed: %v", err)
	}
	chunkID, _ := result.LastInsertId()

	// Insert embedding
	embedding := make([]float32, dimensions)
	if err := db.InsertEmbedding(chunkID, embedding); err != nil {
		t.Fatalf("InsertEmbedding failed: %v", err)
	}

	// Delete embedding
	if err := db.DeleteEmbedding(chunkID); err != nil {
		t.Fatalf("DeleteEmbedding failed: %v", err)
	}

	// Verify deletion
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM vec_chunks WHERE chunk_id = ?", chunkID).Scan(&count)
	if err != nil {
		t.Fatalf("Query count failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 embeddings after deletion, got %d", count)
	}
}

func TestStats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	db, err := Open(dbPath, 768)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create test data
	_, err = db.Exec(`INSERT INTO projects (name, root_path) VALUES (?, ?)`, "test", "/tmp/test")
	if err != nil {
		t.Fatalf("Insert project failed: %v", err)
	}

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats["projects"] != 1 {
		t.Errorf("Expected 1 project, got %d", stats["projects"])
	}

	if stats["files"] != 0 {
		t.Errorf("Expected 0 files, got %d", stats["files"])
	}

	if stats["chunks"] != 0 {
		t.Errorf("Expected 0 chunks, got %d", stats["chunks"])
	}
}
