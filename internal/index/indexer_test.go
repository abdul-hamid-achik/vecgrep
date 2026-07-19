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

	"golang.org/x/sync/semaphore"

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
	if cfg.SourceBufferBytes == 0 {
		t.Error("Expected non-zero SourceBufferBytes")
	}
	if cfg.SyncInterval == 0 {
		t.Error("Expected non-zero SyncInterval")
	}
	if cfg.SyncIntervalDuration == 0 {
		t.Error("Expected non-zero SyncIntervalDuration")
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

func TestIndexCountsActualChunkOrigins(t *testing.T) {
	counts := countChunkOrigins([]Chunk{
		{Content: "local"},
		{Content: "symbol", Origin: ChunkOriginStructural},
		{Content: "gap one", Origin: ChunkOriginGap},
		{Content: "gap two", Origin: ChunkOriginGap},
	})
	if counts.Files.Local != 1 || counts.Files.Structural != 1 || counts.Files.Gap != 1 {
		t.Fatalf("file origin counts = %+v", counts.Files)
	}
	if counts.Chunks.Local != 1 || counts.Chunks.Structural != 1 || counts.Chunks.Gap != 2 {
		t.Fatalf("chunk origin counts = %+v", counts.Chunks)
	}
}

func TestIndexRunObserverFailureIsHardAndPreservesResult(t *testing.T) {
	indexer, database, tmpDir := setupTestIndexer(t)
	defer database.Close()
	projectDir := filepath.Join(tmpDir, "observer-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexer.SetIndexRunObserver(func(report IndexRunReport) error {
		if report.Result == nil || report.Err != nil {
			t.Fatalf("observer report = %+v", report)
		}
		return errors.New("receipt write failed")
	})
	result, err := indexer.Index(context.Background(), projectDir)
	if err == nil || !strings.Contains(err.Error(), "record ingestion receipt") {
		t.Fatalf("Index error = %v, want hard receipt error", err)
	}
	if result == nil || result.ChunksCreated == 0 {
		t.Fatalf("Index result = %+v", result)
	}
	if len(result.Errors) == 0 || !strings.Contains(result.Errors[len(result.Errors)-1].Error(), "record ingestion receipt") {
		t.Fatalf("observer warning not visible: %+v", result.Errors)
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
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Index error = %v, want context canceled", err)
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

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
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
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Index error = %v, want context canceled", err)
		}
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

type gatedSourceBudget struct {
	started  chan struct{}
	grant    chan struct{}
	acquired chan struct{}
	once     sync.Once
	mu       sync.Mutex
	held     int64
}

func newGatedSourceBudget() *gatedSourceBudget {
	return &gatedSourceBudget{
		started:  make(chan struct{}),
		grant:    make(chan struct{}),
		acquired: make(chan struct{}),
	}
}

func (b *gatedSourceBudget) Acquire(ctx context.Context, n int64) error {
	b.once.Do(func() { close(b.started) })
	select {
	case <-b.grant:
	case <-ctx.Done():
		return ctx.Err()
	}
	b.mu.Lock()
	b.held += n
	b.mu.Unlock()
	select {
	case <-b.acquired:
	default:
		close(b.acquired)
	}
	return nil
}

func (b *gatedSourceBudget) Release(n int64) {
	b.mu.Lock()
	b.held -= n
	b.mu.Unlock()
}

func (b *gatedSourceBudget) heldBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held
}

func TestWalkAndFilter_ReservesBeforeReadingSource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source.txt")
	oldContent := []byte("aaaaaaaa")
	newContent := []byte("bbbbbbbbbbbb")
	if err := os.WriteFile(path, oldContent, 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	cfg := DefaultIndexerConfig()
	idx := NewIndexer(nil, nil, cfg)
	ignore, err := idx.buildIgnoreMatcher(root)
	if err != nil {
		t.Fatalf("build ignore matcher: %v", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}

	budget := newGatedSourceBudget()
	files := make(chan fileInfo, 1)
	var discovered, skipped int64
	done := make(chan error, 1)
	go func() {
		done <- idx.walkAndFilter(context.Background(), root, absRoot, nil, ignore, nil, budget, cfg.SourceBufferBytes, files, &discovered, &skipped)
	}()

	select {
	case <-budget.started:
	case <-time.After(5 * time.Second):
		t.Fatal("walker never requested source bytes")
	}
	if err := os.WriteFile(path, newContent, 0o644); err != nil {
		t.Fatalf("replace source while reservation blocked: %v", err)
	}
	close(budget.grant)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("walk: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("walker did not finish after reservation was granted")
	}
	file := <-files
	if got := string(file.content); got != string(newContent) {
		t.Fatalf("retained content = %q, want post-reservation content %q", got, newContent)
	}
	if file.queueBytes != int64(len(newContent)) {
		t.Fatalf("queued charge = %d, want actual bytes %d", file.queueBytes, len(newContent))
	}
	if file.size != int64(len(newContent)) {
		t.Fatalf("file size = %d, want actual bytes %d", file.size, len(newContent))
	}
	if held := budget.heldBytes(); held != file.queueBytes {
		t.Fatalf("held source bytes = %d, want queued charge %d", held, file.queueBytes)
	}
	budget.Release(file.queueBytes)
}

func TestWalkAndFilter_CancellationReleasesReadReservation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cfg := DefaultIndexerConfig()
	idx := NewIndexer(nil, nil, cfg)
	ignore, err := idx.buildIgnoreMatcher(root)
	if err != nil {
		t.Fatalf("build ignore matcher: %v", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}

	budget := newGatedSourceBudget()
	close(budget.grant)
	files := make(chan fileInfo)
	var discovered, skipped int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- idx.walkAndFilter(ctx, root, absRoot, nil, ignore, nil, budget, cfg.SourceBufferBytes, files, &discovered, &skipped)
	}()

	select {
	case <-budget.acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("walker never acquired source bytes")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("walk error = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("walker did not cancel while enqueue was blocked")
	}
	if held := budget.heldBytes(); held != 0 {
		t.Fatalf("held source bytes after cancellation = %d, want 0", held)
	}
}

func TestWalkAndFilter_SourceBufferBudgetCancelsWhileFull(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(strings.Repeat("x", 80)), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	cfg := DefaultIndexerConfig()
	cfg.SourceBufferBytes = 100
	idx := NewIndexer(nil, nil, cfg)
	ignore, err := idx.buildIgnoreMatcher(root)
	if err != nil {
		t.Fatalf("build ignore matcher: %v", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}

	budget := semaphore.NewWeighted(cfg.SourceBufferBytes)
	files := make(chan fileInfo, 10)
	var discovered, skipped int64
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- idx.walkAndFilter(ctx, root, absRoot, nil, ignore, nil, budget, cfg.SourceBufferBytes, files, &discovered, &skipped)
	}()

	deadline := time.After(5 * time.Second)
	for len(files) != 1 {
		select {
		case err := <-done:
			t.Fatalf("walk returned before filling budget: %v", err)
		case <-deadline:
			t.Fatal("walk did not enqueue the first source file")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	first := <-files
	if first.queueBytes != first.size {
		t.Fatalf("queued charge = %d, want file size %d", first.queueBytes, first.size)
	}
	// Do not release the first file's charge: the second acquire must remain
	// blocked even though the file channel has spare count capacity.
	time.Sleep(20 * time.Millisecond)
	if got := len(files); got != 0 {
		t.Fatalf("queued files after byte budget filled = %d, want 0", got)
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("walk error = %v, want context canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("walk did not cancel while waiting for source-buffer bytes")
	}
}

func TestWalkAndFilter_LargeFileConsumesWholeSourceBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", 200)), 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}

	cfg := DefaultIndexerConfig()
	cfg.SourceBufferBytes = 64
	idx := NewIndexer(nil, nil, cfg)
	ignore, err := idx.buildIgnoreMatcher(root)
	if err != nil {
		t.Fatalf("build ignore matcher: %v", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}

	budget := semaphore.NewWeighted(cfg.SourceBufferBytes)
	files := make(chan fileInfo, 1)
	var discovered, skipped int64
	if err := idx.walkAndFilter(context.Background(), root, absRoot, nil, ignore, nil, budget, cfg.SourceBufferBytes, files, &discovered, &skipped); err != nil {
		t.Fatalf("walk: %v", err)
	}
	file := <-files
	if file.queueBytes != cfg.SourceBufferBytes {
		t.Fatalf("large-file charge = %d, want full budget %d", file.queueBytes, cfg.SourceBufferBytes)
	}
	if file.size <= cfg.SourceBufferBytes {
		t.Fatalf("test file size = %d, want larger than budget %d", file.size, cfg.SourceBufferBytes)
	}
	budget.Release(file.queueBytes)
}

func TestReindexAll_SkipsPerFileDeleteAfterReset(t *testing.T) {
	database := openTestDB(t, 8)
	cfg := DefaultIndexerConfig()
	cfg.Workers = 1
	idx := NewIndexer(database, newMockEmbedProvider(8), cfg)
	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	deleteCalls := 0
	idx.deleteFileFn = func(context.Context, string, string) (int64, error) {
		deleteCalls++
		return 0, nil
	}
	result, err := idx.ReindexAll(context.Background(), projectDir)
	if err != nil {
		t.Fatalf("reindex all: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("reindex errors: %v", result.Errors)
	}
	if deleteCalls != 0 {
		t.Fatalf("per-file delete calls after reset = %d, want 0", deleteCalls)
	}
}

func TestIndex_DeleteExistingFailurePreventsReplacementInsert(t *testing.T) {
	database := openTestDB(t, 8)
	idx := NewIndexer(database, newMockEmbedProvider(8), DefaultIndexerConfig())
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package p\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx.deleteFileFn = func(context.Context, string, string) (int64, error) {
		return 0, errors.New("delete failed")
	}

	result, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.ChunksCreated != 0 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error(), "delete existing file chunks") {
		t.Fatalf("result = %+v, want visible delete failure and no replacement chunks", result)
	}
	stats, err := database.StatsForProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if stats["chunks"] != 0 {
		t.Fatalf("chunks = %d, want 0 after failed delete", stats["chunks"])
	}
}

func TestIndex_FinalSyncRunsForEmptyProject(t *testing.T) {
	database := openTestDB(t, 8)
	idx := NewIndexer(database, newMockEmbedProvider(8), DefaultIndexerConfig())
	syncCalls := 0
	idx.syncFn = func() error {
		syncCalls++
		return nil
	}

	result, err := idx.Index(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("index errors: %v", result.Errors)
	}
	if syncCalls != 1 {
		t.Fatalf("sync calls = %d, want one final sync", syncCalls)
	}
}

func TestIndex_FullScanPrunesDeletedFiles(t *testing.T) {
	database := openTestDB(t, 8)
	idx := NewIndexer(database, newMockEmbedProvider(8), DefaultIndexerConfig())
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep.go", "delete.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package p\nfunc F() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := idx.Index(context.Background(), root); err != nil || len(result.Errors) != 0 {
		t.Fatalf("initial index = %+v, %v", result, err)
	}
	if err := os.Remove(filepath.Join(root, "delete.go")); err != nil {
		t.Fatal(err)
	}

	result, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 0 || result.FilesDeleted != 1 {
		t.Fatalf("prune result = %+v", result)
	}
	hashes, err := database.GetFileHashes(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := hashes["delete.go"]; exists {
		t.Fatalf("deleted file hash remains: %v", hashes)
	}
	if _, exists := hashes["keep.go"]; !exists {
		t.Fatalf("live file hash missing: %v", hashes)
	}
}

func TestIndex_SkipsSymlinkDirectoriesAndIndexesSymlinkFiles(t *testing.T) {
	const dimensions = 8
	database := openTestDB(t, dimensions)
	cfg := DefaultIndexerConfig()
	cfg.Workers = 1
	idx := NewIndexer(database, newMockEmbedProvider(dimensions), cfg)

	root := t.TempDir()
	targetRoot := t.TempDir()
	targetDir := filepath.Join(targetRoot, "shared")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "ignored.go"), []byte("package ignored\n\nfunc Ignored() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(targetRoot, "shared.go")
	if err := os.WriteFile(targetFile, []byte("package shared\n\nfunc Shared() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, targetDir, filepath.Join(root, "linked-dir"))
	linkedFile := filepath.Join(root, "linked.go")
	symlinkOrSkip(t, targetFile, linkedFile)

	result, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatalf("index symlinks: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("index symlink errors: %v", result.Errors)
	}

	hashes, complete, err := database.GetSourceHashes(root)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatalf("source hashes incomplete: %v", hashes)
	}
	wantLinkedHash, _, err := hashFile(linkedFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := hashes["linked.go"]; got != wantLinkedHash {
		t.Fatalf("linked.go source hash = %q, want %q (all hashes: %v)", got, wantLinkedHash, hashes)
	}
	if _, ok := hashes["main.go"]; !ok {
		t.Fatalf("main.go missing from source hashes: %v", hashes)
	}
	if len(hashes) != 2 {
		t.Fatalf("indexed files = %v, want only main.go and linked.go", hashes)
	}
	for path := range hashes {
		if path == "linked-dir" || strings.HasPrefix(path, "linked-dir"+string(filepath.Separator)) {
			t.Fatalf("symlink directory was indexed: %v", hashes)
		}
	}
}

func TestIndex_PartialScanDoesNotPruneOutsideSelection(t *testing.T) {
	database := openTestDB(t, 8)
	idx := NewIndexer(database, newMockEmbedProvider(8), DefaultIndexerConfig())
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"keep.go", "outside.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package p\nfunc F() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "outside.go")); err != nil {
		t.Fatal(err)
	}

	result, err := idx.Index(context.Background(), root, "keep.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesDeleted != 0 {
		t.Fatalf("partial scan deleted %d files", result.FilesDeleted)
	}
	hashes, _ := database.GetFileHashes(root)
	if _, exists := hashes["outside.go"]; !exists {
		t.Fatalf("partial scan pruned outside selection: %v", hashes)
	}
}

func TestIndex_CanceledScanDoesNotPrune(t *testing.T) {
	database := openTestDB(t, 8)
	idx := NewIndexer(database, newMockEmbedProvider(8), DefaultIndexerConfig())
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "stale.go")
	if err := os.WriteFile(path, []byte("package p\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := idx.Index(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Index error = %v, want context canceled", err)
	}
	if result.FilesDeleted != 0 {
		t.Fatalf("canceled scan deleted %d files", result.FilesDeleted)
	}
	hashes, _ := database.GetFileHashes(root)
	if _, exists := hashes["stale.go"]; !exists {
		t.Fatalf("canceled scan pruned stale file: %v", hashes)
	}
}

func TestIndex_PruneFailureRemainsVisibleAndPending(t *testing.T) {
	database := openTestDB(t, 8)
	idx := NewIndexer(database, newMockEmbedProvider(8), DefaultIndexerConfig())
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "stale.go")
	if err := os.WriteFile(path, []byte("package p\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	idx.deleteFileFn = func(context.Context, string, string) (int64, error) {
		return 0, errors.New("delete failed")
	}

	result, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesDeleted != 0 || len(result.Errors) == 0 || !strings.Contains(result.Errors[0].Error(), "delete failed") {
		t.Fatalf("failed prune result = %+v", result)
	}
	hashes, _ := database.GetFileHashes(root)
	if _, exists := hashes["stale.go"]; !exists {
		t.Fatalf("failed prune removed hash: %v", hashes)
	}
}

func TestIndex_PeriodicSyncPolicy(t *testing.T) {
	database := openTestDB(t, 8)
	cfg := DefaultIndexerConfig()
	cfg.Workers = 1
	cfg.BatchSize = 1
	cfg.SyncInterval = 2
	cfg.SyncIntervalDuration = time.Hour
	idx := NewIndexer(database, newMockEmbedProvider(8), cfg)
	syncCalls := 0
	idx.syncFn = func() error {
		syncCalls++
		return nil
	}

	projectDir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for i := range 4 {
		src := fmt.Sprintf("package p\nfunc F%d() int { return %d }\n", i, i)
		if err := os.WriteFile(filepath.Join(projectDir, fmt.Sprintf("f%d.go", i)), []byte(src), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
	}

	result, err := idx.Index(context.Background(), projectDir)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("index errors: %v", result.Errors)
	}
	if result.FilesProcessed != 4 {
		t.Fatalf("files processed = %d, want 4", result.FilesProcessed)
	}
	// Incremental syncs at files 2 and 4. Because the latter already covers
	// all writes, the final durability check must not rewrite the snapshot.
	if syncCalls != 2 {
		t.Fatalf("sync calls = %d, want 2", syncCalls)
	}
}

func TestDryRunPreviewNeedsConfirm(t *testing.T) {
	if (DryRunPreview{FilesToEmbed: 100}).NeedsConfirm() {
		t.Fatal("small plan should not need confirm")
	}
	if !(DryRunPreview{FilesToEmbed: ConfirmScopeFiles}).NeedsConfirm() {
		t.Fatal("files threshold should need confirm")
	}
	if !(DryRunPreview{ScannedFiles: ConfirmScopeFiles}).NeedsConfirm() {
		t.Fatal("scanned files threshold should need confirm")
	}
	if !(DryRunPreview{BytesScanned: ConfirmScopeBytes}).NeedsConfirm() {
		t.Fatal("bytes threshold should need confirm")
	}
	if !(DryRunPreview{EstimatedChunks: ConfirmScopeChunks}).NeedsConfirm() {
		t.Fatal("chunks threshold should need confirm")
	}
}

func TestProgressLargeScopeUsesSharedConstants(t *testing.T) {
	if (Progress{WalkedFiles: LargeScopeFiles - 1}).LargeScope() {
		t.Fatal("below file threshold")
	}
	if !(Progress{WalkedFiles: LargeScopeFiles}).LargeScope() {
		t.Fatal("at file threshold")
	}
	if !(Progress{BytesWalked: LargeScopeBytes}).LargeScope() {
		t.Fatal("at byte threshold")
	}
}
