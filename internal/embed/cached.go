// Package embed provides embedding generation for semantic search.
package embed

import (
	"context"
	"time"
)

// CachedProvider wraps an embedding provider with caching.
type CachedProvider struct {
	inner Provider
	cache *EmbeddingCache
}

// WithCache wraps a Provider with an EmbeddingCache.
// cacheSize is the maximum number of embeddings to cache.
// Returns a Provider that caches embeddings.
func WithCache(p Provider, cacheSize int) Provider {
	return &CachedProvider{
		inner: p,
		cache: NewEmbeddingCache(cacheSize, 0), // No TTL by default
	}
}

// WithCacheAndTTL wraps a Provider with an EmbeddingCache that has TTL expiration.
// cacheSize is the maximum number of embeddings to cache.
// ttl is the time-to-live for cache entries.
func WithCacheAndTTL(p Provider, cacheSize int, ttl time.Duration) Provider {
	return &CachedProvider{
		inner: p,
		cache: NewEmbeddingCache(cacheSize, ttl),
	}
}

// NewCachedProvider creates a CachedProvider with an existing cache instance.
// This is useful when you want to share a cache between multiple providers.
func NewCachedProvider(p Provider, cache *EmbeddingCache) *CachedProvider {
	return &CachedProvider{
		inner: p,
		cache: cache,
	}
}

// Embed generates an embedding for the given text, using cache if available.
func (c *CachedProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	// Check cache first
	if cached, found := c.cache.Get(text); found {
		return cached, nil
	}

	// Generate embedding
	embedding, err := c.inner.Embed(ctx, text)
	if err != nil {
		return nil, err
	}

	// Cache the result
	c.cache.Set(text, embedding)

	return embedding, nil
}

// EmbedBatch generates embeddings for multiple texts, using cache where available.
func (c *CachedProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	uncachedIndices := make([]int, 0, len(texts))
	uncachedTexts := make([]string, 0, len(texts))

	// Check cache for each text
	for i, text := range texts {
		if cached, found := c.cache.Get(text); found {
			results[i] = cached
		} else {
			uncachedIndices = append(uncachedIndices, i)
			uncachedTexts = append(uncachedTexts, text)
		}
	}

	// If all were cached, return early
	if len(uncachedTexts) == 0 {
		return results, nil
	}

	// Generate embeddings for uncached texts
	newEmbeddings, err := c.inner.EmbedBatch(ctx, uncachedTexts)
	if err != nil {
		return nil, err
	}

	// Store new embeddings in cache and results
	for i, idx := range uncachedIndices {
		results[idx] = newEmbeddings[i]
		c.cache.Set(uncachedTexts[i], newEmbeddings[i])
	}

	return results, nil
}

// Model returns the name of the embedding model being used.
func (c *CachedProvider) Model() string {
	return c.inner.Model()
}

// Dimensions returns the dimensionality of the embedding vectors.
func (c *CachedProvider) Dimensions() int {
	return c.inner.Dimensions()
}

// Ping checks if the provider is available and the model is loaded.
func (c *CachedProvider) Ping(ctx context.Context) error {
	return c.inner.Ping(ctx)
}

// Cache returns the underlying cache for direct access.
func (c *CachedProvider) Cache() *EmbeddingCache {
	return c.cache
}

// CacheStats returns statistics about the cache.
func (c *CachedProvider) CacheSize() int {
	return c.cache.Size()
}

// ClearCache removes all entries from the cache.
func (c *CachedProvider) ClearCache() {
	c.cache.Clear()
}
