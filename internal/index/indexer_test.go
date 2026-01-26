package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

// mockEmbedProvider is a mock embedding provider for testing.
type mockEmbedProvider struct {
	model      string
	dimensions int
	embedCount int
}

func newMockEmbedProvider(dimensions int) *mockEmbedProvider {
	return &mockEmbedProvider{
		model:      "mock-embed",
		dimensions: dimensions,
	}
}

func (m *mockEmbedProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	m.embedCount++
	embedding := make([]float32, m.dimensions)
	for i := range embedding {
		embedding[i] = float32(len(text)%100) / 100.0
	}
	return embedding, nil
}

func (m *mockEmbedProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		emb, err := m.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		results[i] = emb
	}
	return results, nil
}

func (m *mockEmbedProvider) Model() string {
	return m.model
}

func (m *mockEmbedProvider) Dimensions() int {
	return m.dimensions
}

func (m *mockEmbedProvider) Ping(ctx context.Context) error {
	return nil
}

func setupTestIndexer(t *testing.T) (*Indexer, *db.DB, string) {
	t.Helper()

	// Create temp directory for database
	tmpDir := t.TempDir()

	// Open database
	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: 768,
		DataDir:    tmpDir,
	})
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	// Create mock provider
	provider := newMockEmbedProvider(768)

	// Create indexer
	cfg := DefaultIndexerConfig()
	cfg.Workers = 1 // Use single worker for deterministic tests
	indexer := NewIndexer(database, provider, cfg)

	return indexer, database, tmpDir
}

func setupTestFiles(t *testing.T, rootDir string) {
	t.Helper()

	// Create test files
	files := map[string]string{
		"main.go": `package main

func main() {
	println("Hello, World!")
}
`,
		"utils.go": `package main

func add(a, b int) int {
	return a + b
}

func subtract(a, b int) int {
	return a - b
}
`,
		"subdir/helper.go": `package subdir

func Helper() string {
	return "help"
}
`,
	}

	for relPath, content := range files {
		fullPath := filepath.Join(rootDir, relPath)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}
	}
}

func TestNewIndexer(t *testing.T) {
	indexer, database, _ := setupTestIndexer(t)
	defer database.Close()

	if indexer == nil {
		t.Fatal("NewIndexer returned nil")
	}
}

func TestDefaultIndexerConfig(t *testing.T) {
	cfg := DefaultIndexerConfig()

	if cfg.ChunkSize == 0 {
		t.Error("Expected non-zero ChunkSize")
	}
	if cfg.ChunkOverlap == 0 {
		t.Error("Expected non-zero ChunkOverlap")
	}
	if cfg.MaxFileSize == 0 {
		t.Error("Expected non-zero MaxFileSize")
	}
	if cfg.BatchSize == 0 {
		t.Error("Expected non-zero BatchSize")
	}
	if cfg.Workers == 0 {
		t.Error("Expected non-zero Workers")
	}
	if len(cfg.IgnorePatterns) == 0 {
		t.Error("Expected non-empty IgnorePatterns")
	}
}

func TestIndex_Basic(t *testing.T) {
	indexer, database, tmpDir := setupTestIndexer(t)
	defer database.Close()

	// Create test project directory
	projectDir := filepath.Join(tmpDir, "testproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	setupTestFiles(t, projectDir)

	// Index the project
	ctx := context.Background()
	result, err := indexer.Index(ctx, projectDir)
	if err != nil {
		t.Fatalf("Index failed: %v", err)
	}

	if result.FilesProcessed == 0 {
		t.Error("Expected some files to be processed")
	}
	if result.ChunksCreated == 0 {
		t.Error("Expected some chunks to be created")
	}
	if result.Duration == 0 {
		t.Error("Expected non-zero duration")
	}
}

func TestIndex_IncrementalSkipsUnchanged(t *testing.T) {
	indexer, database, tmpDir := setupTestIndexer(t)
	defer database.Close()

	// Create test project directory
	projectDir := filepath.Join(tmpDir, "testproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	setupTestFiles(t, projectDir)

	ctx := context.Background()

	// First index
	result1, err := indexer.Index(ctx, projectDir)
	if err != nil {
		t.Fatalf("First index failed: %v", err)
	}

	// Second index - should skip unchanged files
	result2, err := indexer.Index(ctx, projectDir)
	if err != nil {
		t.Fatalf("Second index failed: %v", err)
	}

	if result2.FilesProcessed > 0 {
		t.Errorf("Expected 0 files processed on second run, got %d", result2.FilesProcessed)
	}
	if result2.FilesSkipped != result1.FilesProcessed {
		t.Errorf("Expected %d files skipped, got %d", result1.FilesProcessed, result2.FilesSkipped)
	}
}

func TestIndex_ReindexesModifiedFiles(t *testing.T) {
	indexer, database, tmpDir := setupTestIndexer(t)
	defer database.Close()

	// Create test project directory
	projectDir := filepath.Join(tmpDir, "testproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	setupTestFiles(t, projectDir)

	ctx := context.Background()

	// First index
	_, err := indexer.Index(ctx, projectDir)
	if err != nil {
		t.Fatalf("First index failed: %v", err)
	}

	// Modify a file
	time.Sleep(10 * time.Millisecond) // Ensure modification time differs
	modifiedContent := `package main

func main() {
	println("Modified!")
}
`
	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte(modifiedContent), 0644); err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}

	// Second index - should reindex modified file
	result2, err := indexer.Index(ctx, projectDir)
	if err != nil {
		t.Fatalf("Second index failed: %v", err)
	}

	if result2.FilesProcessed != 1 {
		t.Errorf("Expected 1 file processed, got %d", result2.FilesProcessed)
	}
}

func TestIndex_RespectsIgnorePatterns(t *testing.T) {
	indexer, database, tmpDir := setupTestIndexer(t)
	defer database.Close()

	// Create test project directory
	projectDir := filepath.Join(tmpDir, "testproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	setupTestFiles(t, projectDir)

	// Create files that should be ignored
	ignoredFiles := []string{
		"node_modules/package/index.js",
		"vendor/github.com/pkg/mod.go",
		".git/config",
	}

	for _, relPath := range ignoredFiles {
		fullPath := filepath.Join(projectDir, relPath)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte("content"), 0644); err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}
	}

	ctx := context.Background()
	result, err := indexer.Index(ctx, projectDir)
	if err != nil {
		t.Fatalf("Index failed: %v", err)
	}

	// Verify only non-ignored files were processed
	// We created 3 test files + 3 ignored = 6 total
	// But only 3 should be processed
	if result.FilesProcessed != 3 {
		t.Errorf("Expected 3 files processed (ignoring vendor/node_modules/.git), got %d", result.FilesProcessed)
	}
}

func TestIndex_WithProgressCallback(t *testing.T) {
	indexer, database, tmpDir := setupTestIndexer(t)
	defer database.Close()

	// Create test project directory
	projectDir := filepath.Join(tmpDir, "testproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	setupTestFiles(t, projectDir)

	var progressCalled int
	indexer.SetProgressCallback(func(p Progress) {
		progressCalled++
	})

	ctx := context.Background()
	_, err := indexer.Index(ctx, projectDir)
	if err != nil {
		t.Fatalf("Index failed: %v", err)
	}

	if progressCalled == 0 {
		t.Error("Expected progress callback to be called")
	}
}

func TestReindexAll(t *testing.T) {
	indexer, database, tmpDir := setupTestIndexer(t)
	defer database.Close()

	// Create test project directory
	projectDir := filepath.Join(tmpDir, "testproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	setupTestFiles(t, projectDir)

	ctx := context.Background()

	// First index
	result1, err := indexer.Index(ctx, projectDir)
	if err != nil {
		t.Fatalf("First index failed: %v", err)
	}

	// ReindexAll should process all files again
	result2, err := indexer.ReindexAll(ctx, projectDir)
	if err != nil {
		t.Fatalf("ReindexAll failed: %v", err)
	}

	// Both should have processed the same number of files
	if result2.FilesProcessed != result1.FilesProcessed {
		t.Errorf("Expected %d files processed, got %d", result1.FilesProcessed, result2.FilesProcessed)
	}
}

func TestIndex_ContextCancellation(t *testing.T) {
	indexer, database, tmpDir := setupTestIndexer(t)
	defer database.Close()

	// Create test project directory
	projectDir := filepath.Join(tmpDir, "testproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("Failed to create project directory: %v", err)
	}

	setupTestFiles(t, projectDir)

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := indexer.Index(ctx, projectDir)
	// Should either return context error or complete with partial results
	if err != nil && err != context.Canceled {
		// Some errors are acceptable when context is canceled
		t.Logf("Index returned error (expected): %v", err)
	}
}

func TestHashFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := "Hello, World!"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	hash1, err := hashFile(testFile)
	if err != nil {
		t.Fatalf("hashFile failed: %v", err)
	}

	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same content should produce same hash
	hash2, err := hashFile(testFile)
	if err != nil {
		t.Fatalf("hashFile failed: %v", err)
	}

	if hash1 != hash2 {
		t.Error("Expected same hash for same file")
	}

	// Different content should produce different hash
	if err := os.WriteFile(testFile, []byte("Different content"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	hash3, err := hashFile(testFile)
	if err != nil {
		t.Fatalf("hashFile failed: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Expected different hash for different content")
	}
}
