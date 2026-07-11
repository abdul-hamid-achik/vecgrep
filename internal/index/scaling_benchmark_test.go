package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"golang.org/x/sync/semaphore"
)

type byteBudgetBenchmarkProvider struct {
	dimensions int
}

func (p *byteBudgetBenchmarkProvider) Embed(context.Context, string) ([]float32, error) {
	return make([]float32, p.dimensions), nil
}

func (p *byteBudgetBenchmarkProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return p.EmbedDocuments(context.Background(), texts)
}

func (p *byteBudgetBenchmarkProvider) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = make([]float32, p.dimensions)
	}
	return vectors, nil
}

func (p *byteBudgetBenchmarkProvider) Model() string              { return "benchmark" }
func (p *byteBudgetBenchmarkProvider) Dimensions() int            { return p.dimensions }
func (p *byteBudgetBenchmarkProvider) Ping(context.Context) error { return nil }
func (p *byteBudgetBenchmarkProvider) Warmup(context.Context) (time.Duration, error) {
	return 0, nil
}

func writeByteBudgetFiles(tb testing.TB, root string, count, size int) int64 {
	tb.Helper()
	content := strings.Repeat("x", size)
	for i := range count {
		path := filepath.Join(root, fmt.Sprintf("source-%03d.txt", i))
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			tb.Fatalf("write source file: %v", err)
		}
	}
	return int64(count * size)
}

func TestWalkAndFilterStopsAtSourceByteBudget(t *testing.T) {
	const (
		fileSize = 32 * 1024
		budget   = 2 * fileSize
	)
	root := t.TempDir()
	writeByteBudgetFiles(t, root, 6, fileSize)

	database, err := db.OpenWithOptions(db.OpenOptions{Dimensions: 8, DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	cfg := DefaultIndexerConfig()
	cfg.SourceBufferBytes = budget
	cfg.MaxFileSize = fileSize
	idx := NewIndexer(database, &byteBudgetBenchmarkProvider{dimensions: 8}, cfg)
	ignore, err := idx.buildIgnoreMatcher(root)
	if err != nil {
		t.Fatalf("build ignore matcher: %v", err)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatalf("absolute root: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fileChan := make(chan fileInfo, 10)
	walkErr := make(chan error, 1)
	var discovered, skipped int64
	go func() {
		walkErr <- idx.walkAndFilter(
			ctx, root, absRoot, nil, ignore, map[string]string{},
			semaphore.NewWeighted(budget), budget, fileChan, &discovered, &skipped,
		)
	}()

	var queuedBytes int64
	for range 2 {
		select {
		case file := <-fileChan:
			queuedBytes += file.queueBytes
		case <-time.After(2 * time.Second):
			t.Fatal("walker did not fill source byte budget")
		}
	}
	if queuedBytes != budget {
		t.Fatalf("queued bytes = %d, want budget %d", queuedBytes, budget)
	}
	select {
	case file := <-fileChan:
		t.Fatalf("walker exceeded byte budget with %s (%d additional bytes)", file.relativePath, file.queueBytes)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	if err := <-walkErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("walk cancellation error = %v, want context canceled", err)
	}
	if got := atomic.LoadInt64(&discovered); got != 2 {
		t.Fatalf("discovered files = %d, want 2 before budget release", got)
	}
}

func BenchmarkIndexerByteBoundedWalker(b *testing.B) {
	const (
		fileCount = 16
		fileSize  = 32 * 1024
		budget    = 2 * fileSize
	)
	root := b.TempDir()
	totalBytes := writeByteBudgetFiles(b, root, fileCount, fileSize)
	database, err := db.OpenWithOptions(db.OpenOptions{Dimensions: 8, DataDir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()

	cfg := DefaultIndexerConfig()
	cfg.ChunkSize = fileSize * 2
	cfg.ChunkOverlap = 0
	cfg.MaxFileSize = fileSize
	cfg.BatchSize = 4
	cfg.Workers = 1
	cfg.SourceBufferBytes = budget
	cfg.SyncInterval = fileCount + 1
	cfg.SyncIntervalDuration = time.Hour
	idx := NewIndexer(database, &byteBudgetBenchmarkProvider{dimensions: 8}, cfg)

	b.ReportAllocs()
	b.ReportMetric(float64(budget), "source-buffer-bytes")
	b.SetBytes(totalBytes)
	for range b.N {
		b.StopTimer()
		if err := database.ResetAll(context.Background()); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		result, err := idx.Index(context.Background(), root)
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Errors) != 0 {
			b.Fatalf("index errors: %v", result.Errors)
		}
		if result.FilesProcessed != fileCount {
			b.Fatalf("processed %d files, want %d", result.FilesProcessed, fileCount)
		}
	}
}
