package embed

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mockProvider is a test mock for the Provider interface
type mockProvider struct {
	embedFunc      func(ctx context.Context, text string) ([]float32, error)
	embedBatchFunc func(ctx context.Context, texts []string) ([][]float32, error)
	model          string
	dimensions     int
	embedCalls     int
	batchCalls     int
}

func (m *mockProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	m.embedCalls++
	if m.embedFunc != nil {
		return m.embedFunc(ctx, text)
	}
	return []float32{1.0, 2.0, 3.0}, nil
}

func (m *mockProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	m.batchCalls++
	if m.embedBatchFunc != nil {
		return m.embedBatchFunc(ctx, texts)
	}
	results := make([][]float32, len(texts))
	for i := range texts {
		results[i] = []float32{float32(i), float32(i + 1)}
	}
	return results, nil
}

func (m *mockProvider) Model() string {
	if m.model != "" {
		return m.model
	}
	return "test-model"
}

func (m *mockProvider) Dimensions() int {
	if m.dimensions != 0 {
		return m.dimensions
	}
	return 768
}

func (m *mockProvider) Ping(ctx context.Context) error {
	return nil
}

func TestWithCache(t *testing.T) {
	mock := &mockProvider{}
	cached := WithCache(mock, 100)

	if cached == nil {
		t.Fatal("expected non-nil cached provider")
	}

	// Should implement Provider interface
	_ = Provider(cached)
}

func TestWithCacheAndTTL(t *testing.T) {
	mock := &mockProvider{}
	cached := WithCacheAndTTL(mock, 100, time.Hour)

	if cached == nil {
		t.Fatal("expected non-nil cached provider")
	}
}

func TestCachedProvider_Embed(t *testing.T) {
	mock := &mockProvider{}
	cached := WithCache(mock, 100).(*CachedProvider)
	ctx := context.Background()

	// First call should go to provider
	result1, err := cached.Embed(ctx, "test query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.embedCalls != 1 {
		t.Errorf("embedCalls = %d, want 1", mock.embedCalls)
	}

	// Second call with same text should use cache
	result2, err := cached.Embed(ctx, "test query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.embedCalls != 1 {
		t.Errorf("embedCalls = %d, want 1 (should use cache)", mock.embedCalls)
	}

	// Results should be equal
	if len(result1) != len(result2) {
		t.Error("cached result should equal original")
	}

	// Different text should call provider again
	_, err = cached.Embed(ctx, "different query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.embedCalls != 2 {
		t.Errorf("embedCalls = %d, want 2", mock.embedCalls)
	}
}

func TestCachedProvider_EmbedError(t *testing.T) {
	expectedErr := errors.New("provider error")
	mock := &mockProvider{
		embedFunc: func(ctx context.Context, text string) ([]float32, error) {
			return nil, expectedErr
		},
	}
	cached := WithCache(mock, 100)
	ctx := context.Background()

	_, err := cached.Embed(ctx, "test")
	if !errors.Is(err, expectedErr) {
		t.Errorf("error = %v, want %v", err, expectedErr)
	}
}

func TestCachedProvider_EmbedBatch(t *testing.T) {
	mock := &mockProvider{}
	cached := WithCache(mock, 100).(*CachedProvider)
	ctx := context.Background()

	texts := []string{"query1", "query2", "query3"}

	// First batch call
	results1, err := cached.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results1) != 3 {
		t.Errorf("results length = %d, want 3", len(results1))
	}
	if mock.batchCalls != 1 {
		t.Errorf("batchCalls = %d, want 1", mock.batchCalls)
	}

	// Same batch should use cache entirely
	results2, err := cached.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mock.batchCalls != 1 {
		t.Errorf("batchCalls = %d, want 1 (all cached)", mock.batchCalls)
	}
	if len(results2) != 3 {
		t.Errorf("results length = %d, want 3", len(results2))
	}
}

func TestCachedProvider_EmbedBatchPartialCache(t *testing.T) {
	mock := &mockProvider{}
	cached := WithCache(mock, 100).(*CachedProvider)
	ctx := context.Background()

	// Cache one query
	_, _ = cached.Embed(ctx, "query1")
	mock.embedCalls = 0 // Reset

	// Batch with mix of cached and uncached
	texts := []string{"query1", "query2", "query3"}
	_, err := cached.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only call batch for uncached (2 items)
	if mock.batchCalls != 1 {
		t.Errorf("batchCalls = %d, want 1", mock.batchCalls)
	}
}

func TestCachedProvider_Model(t *testing.T) {
	mock := &mockProvider{model: "custom-model"}
	cached := WithCache(mock, 100)

	if cached.Model() != "custom-model" {
		t.Errorf("Model() = %s, want custom-model", cached.Model())
	}
}

func TestCachedProvider_Dimensions(t *testing.T) {
	mock := &mockProvider{dimensions: 512}
	cached := WithCache(mock, 100)

	if cached.Dimensions() != 512 {
		t.Errorf("Dimensions() = %d, want 512", cached.Dimensions())
	}
}

func TestCachedProvider_Ping(t *testing.T) {
	mock := &mockProvider{}
	cached := WithCache(mock, 100)
	ctx := context.Background()

	if err := cached.Ping(ctx); err != nil {
		t.Errorf("Ping() error = %v", err)
	}
}

func TestCachedProvider_CacheSize(t *testing.T) {
	mock := &mockProvider{}
	cached := WithCache(mock, 100).(*CachedProvider)
	ctx := context.Background()

	if cached.CacheSize() != 0 {
		t.Errorf("initial CacheSize() = %d, want 0", cached.CacheSize())
	}

	_, _ = cached.Embed(ctx, "test1")
	_, _ = cached.Embed(ctx, "test2")

	if cached.CacheSize() != 2 {
		t.Errorf("CacheSize() = %d, want 2", cached.CacheSize())
	}
}

func TestCachedProvider_ClearCache(t *testing.T) {
	mock := &mockProvider{}
	cached := WithCache(mock, 100).(*CachedProvider)
	ctx := context.Background()

	cached.Embed(ctx, "test1")
	cached.Embed(ctx, "test2")
	cached.ClearCache()

	if cached.CacheSize() != 0 {
		t.Errorf("CacheSize() after clear = %d, want 0", cached.CacheSize())
	}

	// After clear, should call provider again
	mock.embedCalls = 0
	cached.Embed(ctx, "test1")
	if mock.embedCalls != 1 {
		t.Error("should call provider after cache clear")
	}
}

func TestNewCachedProvider(t *testing.T) {
	mock := &mockProvider{}
	cache := NewEmbeddingCache(50, time.Hour)
	cached := NewCachedProvider(mock, cache)

	if cached.Cache() != cache {
		t.Error("should use provided cache instance")
	}
}
