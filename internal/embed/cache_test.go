package embed

import (
	"sync"
	"testing"
	"time"
)

func TestNewEmbeddingCache(t *testing.T) {
	tests := []struct {
		name        string
		maxSize     int
		ttl         time.Duration
		wantMaxSize int
	}{
		{
			name:        "default size when zero",
			maxSize:     0,
			ttl:         0,
			wantMaxSize: 1000,
		},
		{
			name:        "default size when negative",
			maxSize:     -1,
			ttl:         0,
			wantMaxSize: 1000,
		},
		{
			name:        "custom size",
			maxSize:     500,
			ttl:         time.Hour,
			wantMaxSize: 500,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewEmbeddingCache(tt.maxSize, tt.ttl)
			if cache == nil {
				t.Fatal("expected non-nil cache")
			}
			if cache.maxSize != tt.wantMaxSize {
				t.Errorf("maxSize = %d, want %d", cache.maxSize, tt.wantMaxSize)
			}
			if cache.ttl != tt.ttl {
				t.Errorf("ttl = %v, want %v", cache.ttl, tt.ttl)
			}
		})
	}
}

func TestEmbeddingCache_Key(t *testing.T) {
	cache := NewEmbeddingCache(100, 0)

	// Same text should produce same key
	key1 := cache.Key("hello world")
	key2 := cache.Key("hello world")
	if key1 != key2 {
		t.Error("same text should produce same key")
	}

	// Different text should produce different key
	key3 := cache.Key("different text")
	if key1 == key3 {
		t.Error("different text should produce different key")
	}

	// Key should be hex encoded SHA256 (64 chars)
	if len(key1) != 64 {
		t.Errorf("key length = %d, want 64", len(key1))
	}
}

func TestEmbeddingCache_GetSet(t *testing.T) {
	cache := NewEmbeddingCache(100, 0)

	// Get non-existent entry
	_, found := cache.Get("test")
	if found {
		t.Error("expected not found for non-existent entry")
	}

	// Set and get
	vector := []float32{1.0, 2.0, 3.0}
	cache.Set("test", vector)

	result, found := cache.Get("test")
	if !found {
		t.Error("expected found after set")
	}
	if len(result) != len(vector) {
		t.Errorf("result length = %d, want %d", len(result), len(vector))
	}
	for i, v := range result {
		if v != vector[i] {
			t.Errorf("result[%d] = %f, want %f", i, v, vector[i])
		}
	}

	// Verify returned value is a copy
	result[0] = 999.0
	result2, _ := cache.Get("test")
	if result2[0] == 999.0 {
		t.Error("cache should return a copy, not reference")
	}
}

func TestEmbeddingCache_TTLExpiration(t *testing.T) {
	ttl := 50 * time.Millisecond
	cache := NewEmbeddingCache(100, ttl)

	cache.Set("test", []float32{1.0, 2.0})

	// Should be found immediately
	_, found := cache.Get("test")
	if !found {
		t.Error("expected found immediately after set")
	}

	// Wait for expiration
	time.Sleep(ttl + 10*time.Millisecond)

	// Should be expired
	_, found = cache.Get("test")
	if found {
		t.Error("expected not found after TTL expiration")
	}
}

func TestEmbeddingCache_Eviction(t *testing.T) {
	maxSize := 10
	cache := NewEmbeddingCache(maxSize, 0)

	// Fill cache beyond capacity
	for i := 0; i < maxSize+5; i++ {
		cache.Set(string(rune('a'+i)), []float32{float32(i)})
	}

	// Cache size should not exceed maxSize
	if cache.Size() > maxSize {
		t.Errorf("cache size = %d, want <= %d", cache.Size(), maxSize)
	}
}

func TestEmbeddingCache_Clear(t *testing.T) {
	cache := NewEmbeddingCache(100, 0)

	cache.Set("test1", []float32{1.0})
	cache.Set("test2", []float32{2.0})

	if cache.Size() != 2 {
		t.Errorf("size before clear = %d, want 2", cache.Size())
	}

	cache.Clear()

	if cache.Size() != 0 {
		t.Errorf("size after clear = %d, want 0", cache.Size())
	}
}

func TestEmbeddingCache_Cleanup(t *testing.T) {
	ttl := 50 * time.Millisecond
	cache := NewEmbeddingCache(100, ttl)

	cache.Set("test1", []float32{1.0})
	cache.Set("test2", []float32{2.0})

	// Cleanup should do nothing if entries haven't expired
	removed := cache.Cleanup()
	if removed != 0 {
		t.Errorf("cleanup removed = %d, want 0 (entries not expired)", removed)
	}

	// Wait for expiration
	time.Sleep(ttl + 10*time.Millisecond)

	// Now cleanup should remove entries
	removed = cache.Cleanup()
	if removed != 2 {
		t.Errorf("cleanup removed = %d, want 2", removed)
	}

	if cache.Size() != 0 {
		t.Errorf("size after cleanup = %d, want 0", cache.Size())
	}
}

func TestEmbeddingCache_CleanupNoTTL(t *testing.T) {
	cache := NewEmbeddingCache(100, 0) // No TTL

	cache.Set("test", []float32{1.0})

	removed := cache.Cleanup()
	if removed != 0 {
		t.Errorf("cleanup with no TTL removed = %d, want 0", removed)
	}
}

func TestEmbeddingCache_Concurrent(t *testing.T) {
	cache := NewEmbeddingCache(100, 0)

	var wg sync.WaitGroup
	numGoroutines := 10
	numOps := 100

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				key := string(rune('a' + (id*numOps+j)%26))
				cache.Set(key, []float32{float32(id), float32(j)})
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				key := string(rune('a' + j%26))
				cache.Get(key)
			}
		}(i)
	}

	wg.Wait()

	// Should not panic and cache should be in valid state
	size := cache.Size()
	if size < 0 || size > 100 {
		t.Errorf("invalid cache size after concurrent ops: %d", size)
	}
}
