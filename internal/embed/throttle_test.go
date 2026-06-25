package embed

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewThrottledProvider(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)

	if p == nil {
		t.Fatal("expected non-nil throttled provider")
	}
	defer p.Close()
}

func TestThrottledProviderEmbed(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	ctx := context.Background()
	result, err := p.Embed(ctx, "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) != 3 {
		t.Fatalf("result length = %d, want 3", len(result))
	}
}

func TestThrottledProviderEmbedCaches(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	cfg.CacheSize = 100
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	ctx := context.Background()

	// First call should go to inner provider
	_, err := p.Embed(ctx, "test query")
	if err != nil {
		t.Fatalf("first embed: %v", err)
	}
	calls1 := mock.embedCalls.Load()

	// Second call with same text should use cache
	_, err = p.Embed(ctx, "test query")
	if err != nil {
		t.Fatalf("second embed: %v", err)
	}
	if mock.embedCalls.Load() != calls1 {
		t.Errorf("expected %d embed calls (cached), got %d", calls1, mock.embedCalls.Load())
	}
}

func TestThrottledProviderEmbedEmptyText(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	_, err := p.Embed(context.Background(), "")
	if err != ErrEmptyText {
		t.Fatalf("expected ErrEmptyText, got %v", err)
	}
}

func TestThrottledProviderEmbedBatchEmpty(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	results, err := p.EmbedBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results for empty batch, got %v", results)
	}
}

func TestThrottledProviderEmbedBatch(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	cfg.Workers = 2
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	texts := []string{"query1", "query2", "query3"}
	results, err := p.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("results length = %d, want 3", len(results))
	}
	for i, r := range results {
		if r == nil {
			t.Fatalf("results[%d] is nil", i)
		}
	}
}

func TestThrottledProviderEmbedBatchDedup(t *testing.T) {
	var calls int32
	mock := &mockProvider{
		embedFunc: func(ctx context.Context, text string) ([]float32, error) {
			atomic.AddInt32(&calls, 1)
			return []float32{1.0, 2.0, 3.0}, nil
		},
	}
	cfg := DefaultThrottleConfig()
	cfg.CacheSize = 100
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	ctx := context.Background()

	// Batch with duplicate texts — dedup should reduce to 2 unique calls
	texts := []string{"dup", "dup", "unique"}
	_, err := p.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// "dup" should be called once (cached on second hit), "unique" once
	if atomic.LoadInt32(&calls) > 2 {
		t.Errorf("expected at most 2 unique calls, got %d", atomic.LoadInt32(&calls))
	}
}

func TestThrottledProviderModelAndDimensions(t *testing.T) {
	mock := &mockProvider{model: "test-model", dimensions: 512}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	if p.Model() != "test-model" {
		t.Errorf("Model() = %q, want test-model", p.Model())
	}
	if p.Dimensions() != 512 {
		t.Errorf("Dimensions() = %d, want 512", p.Dimensions())
	}
}

func TestThrottledProviderPing(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	if err := p.Ping(context.Background()); err != nil {
		t.Errorf("Ping() error = %v", err)
	}
}

func TestThrottledProviderPriorityContext(t *testing.T) {
	ctx := WithPriority(context.Background(), PriorityInteractive)
	p := priorityFromContext(ctx)
	if p != PriorityInteractive {
		t.Errorf("expected PriorityInteractive, got %d", p)
	}

	// Default should be PriorityBackground
	p = priorityFromContext(context.Background())
	if p != PriorityBackground {
		t.Errorf("expected PriorityBackground, got %d", p)
	}
}

func TestThrottledProviderRateLimit(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	cfg.RPS = 100 // 100 RPS — high enough for the test to complete quickly
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	ctx := context.Background()
	_, err := p.Embed(ctx, "rate-limited query")
	if err != nil {
		t.Fatalf("unexpected error with rate limiter: %v", err)
	}
}

func TestThrottledProviderClose(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)

	// Close should not panic
	p.Close()
}

func TestThrottledProviderEmbedContextCanceled(t *testing.T) {
	mock := &mockProvider{
		embedFunc: func(ctx context.Context, text string) ([]float32, error) {
			// Simulate a slow provider
			select {
			case <-time.After(2 * time.Second):
				return []float32{1.0}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	cfg := DefaultThrottleConfig()
	cfg.Workers = 1
	cfg.MaxInFlight = 1
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := p.Embed(ctx, "slow query")
	if err == nil {
		t.Fatal("expected context canceled error")
	}
}

func TestThrottledProviderCacheReturnsCache(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	cfg.CacheSize = 50
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	if p.Cache() == nil {
		t.Fatal("expected non-nil cache")
	}
}

func TestThrottledProviderNoCacheWhenSizeZero(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	cfg.CacheSize = 0
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	if p.Cache() != nil {
		t.Fatal("expected nil cache when CacheSize is 0")
	}
}

// --- Batch delegation tests (Phase 4) ---

func TestThrottledProviderEmbedBatchDelegatesToDocuments(t *testing.T) {
	// When the inner provider implements DocumentProvider, EmbedBatch should
	// delegate to EmbedDocuments (one call) instead of calling Embed per text.
	var docsCallCount int32
	mock := &mockProvider{
		embedDocsFunc: func(ctx context.Context, texts []string) ([][]float32, error) {
			atomic.AddInt32(&docsCallCount, 1)
			results := make([][]float32, len(texts))
			for i := range texts {
				results[i] = []float32{float32(i), float32(i + 1)}
			}
			return results, nil
		},
	}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	texts := []string{"alpha", "bravo", "charlie"}
	results, err := p.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// EmbedDocuments should have been called exactly once (single batch request).
	if atomic.LoadInt32(&docsCallCount) != 1 {
		t.Errorf("expected 1 EmbedDocuments call, got %d", atomic.LoadInt32(&docsCallCount))
	}
	// Embed (per-text) should NOT have been called.
	if mock.embedCalls.Load() != 0 {
		t.Errorf("expected 0 Embed calls, got %d", mock.embedCalls.Load())
	}
}

func TestThrottledProviderEmbedDocuments(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	texts := []string{"one", "two", "three"}
	results, err := p.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if r == nil {
			t.Fatalf("results[%d] is nil", i)
		}
	}
	// Should delegate to inner EmbedDocuments, not per-text Embed.
	if mock.docsCalls.Load() != 1 {
		t.Errorf("expected 1 inner EmbedDocuments call, got %d", mock.docsCalls.Load())
	}
}

func TestThrottledProviderEmbedDocumentsCacheHit(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	cfg.CacheSize = 100
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	ctx := context.Background()

	// Pre-populate cache by embedding one text.
	_, err := p.Embed(ctx, "cached-text")
	if err != nil {
		t.Fatalf("pre-populate cache: %v", err)
	}

	mock.docsCalls.Store(0)
	mock.embedCalls.Store(0)

	// Batch with one cached and one uncached text.
	results, err := p.EmbedDocuments(ctx, []string{"cached-text", "new-text"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// EmbedDocuments should be called once but only for the uncached text.
	// The inner provider receives only the misses.
	if mock.docsCalls.Load() != 1 {
		t.Errorf("expected 1 EmbedDocuments call, got %d", mock.docsCalls.Load())
	}
}

func TestThrottledProviderEmbedDocumentsEmptyText(t *testing.T) {
	mock := &mockProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	_, err := p.EmbedDocuments(context.Background(), []string{"ok", ""})
	if err == nil {
		t.Fatal("expected error for empty text in batch")
	}
}

func TestThrottledProviderEmbedBatchFallbackWithoutDocumentProvider(t *testing.T) {
	// When inner provider does NOT implement DocumentProvider, EmbedBatch
	// should fall back to per-text Embed calls.
	mock := &noDocsProvider{}
	cfg := DefaultThrottleConfig()
	p := NewThrottledProvider(mock, cfg)
	defer p.Close()

	texts := []string{"a", "b", "c"}
	results, err := p.EmbedBatch(context.Background(), texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// Should have used per-text Embed (3 calls), not EmbedDocuments.
	if mock.embedCalls.Load() != 3 {
		t.Errorf("expected 3 Embed calls (fallback), got %d", mock.embedCalls.Load())
	}
}

// noDocsProvider implements Provider but NOT DocumentProvider, to test the
// fallback path in ThrottledProvider.EmbedBatch.
type noDocsProvider struct {
	embedCalls atomic.Int32
}

func (m *noDocsProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	m.embedCalls.Add(1)
	return []float32{1.0, 2.0, 3.0}, nil
}

func (m *noDocsProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i := range texts {
		results[i] = []float32{1.0, 2.0, 3.0}
	}
	return results, nil
}

func (m *noDocsProvider) Model() string                  { return "no-docs-model" }
func (m *noDocsProvider) Dimensions() int                { return 768 }
func (m *noDocsProvider) Ping(ctx context.Context) error { return nil }
