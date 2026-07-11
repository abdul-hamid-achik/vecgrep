package embed

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
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

func TestDiskCacheWriterQueueIsBounded(t *testing.T) {
	cache, err := NewPersistentCache(16, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	if capacity := cap(cache.writes); capacity != diskCacheWriteQueueSize {
		t.Fatalf("writer queue capacity = %d, want %d", capacity, diskCacheWriteQueueSize)
	}
	if diskCacheWriteBatchSize > diskCacheWriteQueueSize {
		t.Fatalf("writer batch size %d exceeds queue capacity %d", diskCacheWriteBatchSize, diskCacheWriteQueueSize)
	}
}

func TestDiskCacheFlushPersistsPendingWrites(t *testing.T) {
	cache, err := NewPersistentCache(2, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cache.SetModel("test-model")

	want := []float32{1.25, -2.5, 3.75}
	cache.Set("pending", want)
	if err := cache.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	cache.Clear()

	got, ok := cache.Get("pending")
	if !ok {
		t.Fatal("entry was not persisted by FlushToDisk")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get() = %v, want %v", got, want)
	}
}

func TestDiskCacheClosePersistsQueuedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	cache, err := NewPersistentCache(4, path)
	if err != nil {
		t.Fatal(err)
	}
	cache.SetModel("test-model")

	const entries = diskCacheWriteQueueSize + diskCacheWriteBatchSize
	for i := range entries {
		cache.Set(fmt.Sprintf("text-%d", i), []float32{float32(i), float32(-i)})
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewPersistentCache(4, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.SetModel("test-model")
	for _, i := range []int{0, entries / 2, entries - 1} {
		got, ok := reopened.Get(fmt.Sprintf("text-%d", i))
		want := []float32{float32(i), float32(-i)}
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("reopened Get(%d) = %v, %v, want %v, true", i, got, ok, want)
		}
	}
}

func TestDiskCacheReadsLegacyJSONEntries(t *testing.T) {
	cache, err := NewPersistentCache(4, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cache.SetModel("legacy-model")

	want := []float32{0.125, -4.5, 99}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(embedCacheBucket)).Put([]byte(cache.diskKey("legacy")), data)
	}); err != nil {
		t.Fatal(err)
	}

	got, ok := cache.Get("legacy")
	if !ok {
		t.Fatal("legacy JSON entry was not readable")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get() = %v, want %v", got, want)
	}
}

func TestDiskCacheWritesBinaryEntries(t *testing.T) {
	cache, err := NewPersistentCache(4, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cache.SetModel("binary-model")
	cache.Set("binary", []float32{1, 2, 3})
	if err := cache.FlushToDisk(); err != nil {
		t.Fatal(err)
	}

	if err := cache.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket([]byte(embedCacheBucket)).Get([]byte(cache.diskKey("binary")))
		if len(data) < len(diskCacheBinaryHeader) ||
			string(data[:len(diskCacheBinaryHeader)]) != diskCacheBinaryHeader {
			t.Fatalf("persisted entry does not have binary header: %q", data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDiskCacheConcurrentGetSetAndFlush(t *testing.T) {
	cache, err := newPersistentCache(64, time.Minute, filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cache.SetModel("concurrent-model")

	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range 100 {
				text := fmt.Sprintf("%d-%d", worker, i%20)
				cache.Set(text, []float32{float32(worker), float32(i)})
				cache.Get(text)
				if i%25 == 0 {
					if err := cache.FlushToDisk(); err != nil {
						t.Errorf("FlushToDisk() error = %v", err)
					}
				}
			}
		}(worker)
	}
	wg.Wait()
	if err := cache.FlushToDisk(); err != nil {
		t.Fatal(err)
	}
}
