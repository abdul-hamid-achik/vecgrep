// Package embed provides embedding generation for semantic search.
package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// EmbeddingCache provides an in-memory cache for embedding vectors.
// It uses a simple LRU-like eviction when the cache reaches maxSize.
type EmbeddingCache struct {
	mu      sync.RWMutex
	entries map[string]cachedEmbedding
	maxSize int
	ttl     time.Duration
}

// cachedEmbedding holds a cached embedding with creation time.
type cachedEmbedding struct {
	vector    []float32
	createdAt time.Time
}

// CacheStats holds cache statistics.
type CacheStats struct {
	Hits       int64
	Misses     int64
	Entries    int
	MaxSize    int
	HitRate    float64
	Evictions  int64
	ExpiredTTL int64
}

// NewEmbeddingCache creates a new EmbeddingCache.
// maxSize is the maximum number of entries to cache.
// ttl is the time-to-live for cache entries; zero means no expiration.
func NewEmbeddingCache(maxSize int, ttl time.Duration) *EmbeddingCache {
	if maxSize <= 0 {
		maxSize = 1000 // Default to 1000 entries
	}
	return &EmbeddingCache{
		entries: make(map[string]cachedEmbedding),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// Key generates a cache key for the given text using SHA256.
func (c *EmbeddingCache) Key(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

// Get retrieves an embedding from the cache.
// Returns the embedding and true if found and not expired, nil and false otherwise.
func (c *EmbeddingCache) Get(text string) ([]float32, bool) {
	key := c.Key(text)

	c.mu.RLock()
	entry, exists := c.entries[key]
	c.mu.RUnlock()

	if !exists {
		return nil, false
	}

	// Check TTL expiration
	if c.ttl > 0 && time.Since(entry.createdAt) > c.ttl {
		// Entry expired, remove it
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return nil, false
	}

	// Return a copy to prevent external modification
	result := make([]float32, len(entry.vector))
	copy(result, entry.vector)
	return result, true
}

// Set stores an embedding in the cache.
// If the cache is full, evicts old entries.
func (c *EmbeddingCache) Set(text string, vector []float32) {
	key := c.Key(text)

	// Make a copy of the vector
	vectorCopy := make([]float32, len(vector))
	copy(vectorCopy, vector)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict if at capacity
	if len(c.entries) >= c.maxSize {
		c.evictOldest()
	}

	c.entries[key] = cachedEmbedding{
		vector:    vectorCopy,
		createdAt: time.Now(),
	}
}

// evictOldest removes the oldest entries to make room.
// Must be called with lock held.
func (c *EmbeddingCache) evictOldest() {
	// Simple eviction: remove 10% of entries or at least 1
	toEvict := max(c.maxSize/10, 1)

	// Find oldest entries
	type keyTime struct {
		key       string
		createdAt time.Time
	}
	entries := make([]keyTime, 0, len(c.entries))
	for k, v := range c.entries {
		entries = append(entries, keyTime{k, v.createdAt})
	}

	// Sort by creation time (oldest first)
	for i := 0; i < len(entries)-1; i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].createdAt.Before(entries[i].createdAt) {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	// Remove oldest entries
	for i := 0; i < toEvict && i < len(entries); i++ {
		delete(c.entries, entries[i].key)
	}
}

// Size returns the current number of entries in the cache.
func (c *EmbeddingCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// Clear removes all entries from the cache.
func (c *EmbeddingCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cachedEmbedding)
}

// Cleanup removes expired entries from the cache.
// This is useful when TTL is set and you want to proactively clean up.
func (c *EmbeddingCache) Cleanup() int {
	if c.ttl <= 0 {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	removed := 0

	for key, entry := range c.entries {
		if now.Sub(entry.createdAt) > c.ttl {
			delete(c.entries, key)
			removed++
		}
	}

	return removed
}
