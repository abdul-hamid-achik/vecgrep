package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Warmup implements the Provider interface for the test mock.
func (m *mockEmbedProvider) Warmup(ctx context.Context) (time.Duration, error) {
	return 0, nil
}

type documentProviderMock struct {
	*mockEmbedProvider
	documentCalls int
}

func (m *documentProviderMock) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	m.documentCalls++
	results := make([][]float32, len(texts))
	for i, text := range texts {
		embedding := make([]float32, m.dimensions)
		for j := range embedding {
			embedding[j] = float32((len(text)+1)%100) / 100.0
		}
		results[i] = embedding
	}
	return results, nil
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

func TestEmbedDocumentsPrefersDocumentProvider(t *testing.T) {
	provider := &documentProviderMock{mockEmbedProvider: newMockEmbedProvider(4)}

	embeddings, err := embedDocuments(context.Background(), provider, []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("embedDocuments failed: %v", err)
	}
	if provider.documentCalls != 1 {
		t.Fatalf("documentCalls = %d, want 1", provider.documentCalls)
	}
	if provider.embedCount != 0 {
		t.Fatalf("embedCount = %d, want 0", provider.embedCount)
	}
	if len(embeddings) != 2 || len(embeddings[0]) != 4 || len(embeddings[1]) != 4 {
		t.Fatalf("embeddings shape = %d/%d/%d, want 2/4/4", len(embeddings), len(embeddings[0]), len(embeddings[1]))
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

// batchRecordingProvider records the size of every EmbedDocuments call so the
// packing test can verify chunks from many files coalesce into full batches.
type batchRecordingProvider struct {
	*mockEmbedProvider
	mu         sync.Mutex
	batchSizes []int
}

func (m *batchRecordingProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	m.mu.Lock()
	m.batchSizes = append(m.batchSizes, len(texts))
	m.mu.Unlock()
	results := make([][]float32, len(texts))
	for i := range texts {
		results[i] = make([]float32, m.dimensions)
	}
	return results, nil
}

// TestIndex_PacksBatchesAcrossFiles is the regression test for the throughput
// fix: with per-file embedding, N single-chunk files produced N tiny batches.
// The batcher must instead coalesce them into full BatchSize batches (only the
// trailing batch may be short) while losing no chunks.
func TestIndex_PacksBatchesAcrossFiles(t *testing.T) {
	tmpDir := t.TempDir()
	database, err := db.OpenWithOptions(db.OpenOptions{Dimensions: 8, DataDir: tmpDir})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	provider := &batchRecordingProvider{mockEmbedProvider: newMockEmbedProvider(8)}

	const batchSize = 8
	cfg := DefaultIndexerConfig()
	cfg.BatchSize = batchSize
	cfg.Workers = 4
	indexer := NewIndexer(database, provider, cfg)

	// 21 one-function Go files → 21 single-chunk files.
	projectDir := filepath.Join(tmpDir, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const numFiles = 21
	for i := 0; i < numFiles; i++ {
		src := fmt.Sprintf("package p\n\nfunc F%d() int { return %d }\n", i, i)
		if err := os.WriteFile(filepath.Join(projectDir, fmt.Sprintf("f%d.go", i)), []byte(src), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	result, err := indexer.Index(context.Background(), projectDir)
	if err != nil {
		t.Fatalf("index: %v", err)
	}

	total := result.ChunksCreated
	if total == 0 {
		t.Fatal("no chunks created")
	}

	// No chunk may be lost in the scatter/gather.
	sum := 0
	full := 0
	for _, s := range provider.batchSizes {
		sum += s
		if s == batchSize {
			full++
		} else if s > batchSize {
			t.Errorf("batch larger than BatchSize: %d > %d", s, batchSize)
		}
	}
	if sum != total {
		t.Errorf("embedded %d chunks across batches, want %d", sum, total)
	}

	// The batcher flushes only at BatchSize or at end-of-stream, so every batch
	// but the trailing remainder must be exactly full.
	wantFull := total / batchSize
	wantBatches := wantFull
	if total%batchSize != 0 {
		wantBatches++
	}
	if full != wantFull {
		t.Errorf("got %d full batches, want %d (sizes=%v)", full, wantFull, provider.batchSizes)
	}
	if len(provider.batchSizes) != wantBatches {
		t.Errorf("got %d batches, want %d (sizes=%v)", len(provider.batchSizes), wantBatches, provider.batchSizes)
	}
	// Packing must beat the old one-batch-per-file behavior.
	if len(provider.batchSizes) >= numFiles {
		t.Errorf("no packing: %d batches for %d files", len(provider.batchSizes), numFiles)
	}
}

// errEmbedProvider always fails embedding, to exercise the error path.
type errEmbedProvider struct {
	*mockEmbedProvider
}

func (m *errEmbedProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, errors.New("boom")
}

// blockingEmbedProvider parks in EmbedDocuments until the context is cancelled,
// to create in-flight backpressure for the mid-index cancellation test.
type blockingEmbedProvider struct {
	*mockEmbedProvider
	started chan struct{}
	once    sync.Once
}

func (m *blockingEmbedProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	m.once.Do(func() { close(m.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func openTestDB(t *testing.T, dims int) *db.DB {
	t.Helper()
	database, err := db.OpenWithOptions(db.OpenOptions{Dimensions: dims, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// TestNewIndexer_DefaultMaxChunkChars pins that the chunk cap actually engages
// through the production NewIndexer path (which does NOT set MaxChunkChars and
// relies on NewChunker's default). A regression that disabled the default here
// would silently re-introduce oversized chunks with every other test still green.
func TestNewIndexer_DefaultMaxChunkChars(t *testing.T) {
	idx := NewIndexer(openTestDB(t, 8), newMockEmbedProvider(8), DefaultIndexerConfig())
	if idx.chunker.config.MaxChunkChars != defaultMaxChunkChars {
		t.Errorf("NewIndexer chunker MaxChunkChars = %d, want default %d", idx.chunker.config.MaxChunkChars, defaultMaxChunkChars)
	}
}

// TestIndex_NoStoredChunkExceedsCap is the end-to-end original-problem guard: a
// file with one enormous line, indexed through the full pipeline, must store
// multiple chunks each within the byte cap (never one oversized blob).
func TestIndex_NoStoredChunkExceedsCap(t *testing.T) {
	database := openTestDB(t, 8)
	indexer := NewIndexer(database, newMockEmbedProvider(8), DefaultIndexerConfig())

	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bigPath := filepath.Join(projectDir, "big.txt")
	if err := os.WriteFile(bigPath, []byte(strings.Repeat("x", 50000)), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := indexer.Index(context.Background(), projectDir)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if result.ChunksCreated <= 1 {
		t.Fatalf("expected the giant line to be split into >1 chunk, got %d", result.ChunksCreated)
	}

	stored, err := database.GetChunksByFile(bigPath)
	if err != nil {
		t.Fatalf("GetChunksByFile: %v", err)
	}
	if len(stored) == 0 {
		t.Fatal("no chunks stored")
	}
	for i, ch := range stored {
		if len(ch.Content) > defaultMaxChunkChars {
			t.Errorf("stored chunk %d exceeds cap: %d > %d bytes", i, len(ch.Content), defaultMaxChunkChars)
		}
	}
}

// TestIndex_SingleFileSpansMultipleBatches exercises the hardest property of the
// rewrite: one file whose chunks are scattered across SEVERAL embedding batches
// must be gathered back and inserted exactly once, losing no chunk.
func TestIndex_SingleFileSpansMultipleBatches(t *testing.T) {
	database := openTestDB(t, 8)
	provider := &batchRecordingProvider{mockEmbedProvider: newMockEmbedProvider(8)}

	cfg := DefaultIndexerConfig()
	cfg.BatchSize = 2
	cfg.Workers = 4
	indexer := NewIndexer(database, provider, cfg)

	// One Go file with 12 functions -> 12 single chunks, far exceeding BatchSize.
	var b strings.Builder
	b.WriteString("package p\n\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&b, "func F%d() int { return %d }\n", i, i)
	}
	content := b.String()

	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fpath := filepath.Join(projectDir, "f.go")
	if err := os.WriteFile(fpath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	want := len(NewChunker(ChunkerConfig{ChunkSize: cfg.ChunkSize, ChunkOverlap: cfg.ChunkOverlap}).ChunkFile(content, fpath))
	if want <= cfg.BatchSize {
		t.Fatalf("test precondition: want %d chunks must exceed BatchSize %d", want, cfg.BatchSize)
	}

	result, err := indexer.Index(context.Background(), projectDir)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if result.FilesProcessed != 1 {
		t.Errorf("FilesProcessed = %d, want 1", result.FilesProcessed)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
	if result.ChunksCreated != want {
		t.Errorf("ChunksCreated = %d, want %d (chunk lost across batches)", result.ChunksCreated, want)
	}
	// Confirm the file's chunks genuinely spanned more than one batch.
	provider.mu.Lock()
	nbatches := len(provider.batchSizes)
	provider.mu.Unlock()
	if nbatches < 2 {
		t.Errorf("expected chunks to span >1 batch, got %d batches", nbatches)
	}
}

// TestIndex_ProviderErrorIsReported verifies an embedding failure is surfaced in
// result.Errors (and the file is not silently recorded), while Index itself
// still returns without dropping the whole run.
func TestIndex_ProviderErrorIsReported(t *testing.T) {
	database := openTestDB(t, 8)
	provider := &errEmbedProvider{mockEmbedProvider: newMockEmbedProvider(8)}
	indexer := NewIndexer(database, provider, DefaultIndexerConfig())

	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	setupTestFiles(t, projectDir)

	result, err := indexer.Index(context.Background(), projectDir)
	if err != nil {
		t.Fatalf("Index should not return a top-level error on per-file embed failure: %v", err)
	}
	if result == nil {
		t.Fatal("nil result")
	}
	if len(result.Errors) == 0 {
		t.Fatal("expected embed errors to be reported, got none")
	}
	if result.ChunksCreated != 0 {
		t.Errorf("ChunksCreated = %d, want 0 (failed files must not be persisted)", result.ChunksCreated)
	}
	foundEmbed := false
	for _, e := range result.Errors {
		if strings.Contains(e.Error(), "embed") {
			foundEmbed = true
		}
	}
	if !foundEmbed {
		t.Errorf("expected an error mentioning 'embed', got %v", result.Errors)
	}

	// The failed file must NOT have recorded a hash, so a re-run retries it.
	hashes, err := database.GetFileHashes(projectDir)
	if err == nil && len(hashes) != 0 {
		t.Errorf("failed files should leave no recorded hashes, got %d", len(hashes))
	}
}

// TestIndex_CancelMidIndexReturns proves the 3-stage pipeline drains without
// deadlock when the context is cancelled WHILE embedding is in flight (the
// fileTask.skip path and the channel-close chain under backpressure).
func TestIndex_CancelMidIndexReturns(t *testing.T) {
	database := openTestDB(t, 8)
	provider := &blockingEmbedProvider{mockEmbedProvider: newMockEmbedProvider(8), started: make(chan struct{})}

	cfg := DefaultIndexerConfig()
	cfg.BatchSize = 2 // small batches + small itemChan -> chunkFile hits the skip path
	cfg.Workers = 2
	indexer := NewIndexer(database, provider, cfg)

	projectDir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := 0; i < 60; i++ {
		src := fmt.Sprintf("package p\n\nfunc F%d() int { return %d }\n", i, i)
		if err := os.WriteFile(filepath.Join(projectDir, fmt.Sprintf("f%d.go", i)), []byte(src), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := indexer.Index(ctx, projectDir)
		done <- err
	}()

	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("embedding never started; cannot exercise mid-index cancellation")
	}

	cancel()

	select {
	case <-done: // returned without hanging — the property under test
	case <-time.After(10 * time.Second):
		t.Fatal("Index hung on mid-index cancellation (pipeline deadlock)")
	}
}

func TestHashFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	content := "Hello, World!"
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	hash1, _, err := hashFile(testFile)
	if err != nil {
		t.Fatalf("hashFile failed: %v", err)
	}

	if hash1 == "" {
		t.Error("Expected non-empty hash")
	}

	// Same content should produce same hash
	hash2, _, err := hashFile(testFile)
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

	hash3, _, err := hashFile(testFile)
	if err != nil {
		t.Fatalf("hashFile failed: %v", err)
	}

	if hash1 == hash3 {
		t.Error("Expected different hash for different content")
	}
}
