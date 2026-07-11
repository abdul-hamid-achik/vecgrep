// Package embed provides embedding generation for semantic search.
package embed

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
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
// Whitespace is normalized (per-line TrimSpace, empty lines dropped) before
// hashing so that chunks differing only in trailing whitespace or
// tabs-vs-spaces produce the same key. Within-line whitespace is preserved
// to avoid changing code semantics.
func (c *EmbeddingCache) Key(text string) string {
	normalized := normalizeText(text)
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:])
}

// normalizeText returns a whitespace-normalized form of text suitable for
// stable cache keys. It splits by newline, trims leading/trailing whitespace
// from each line, drops completely empty lines, and rejoins with "\n".
// Within-line whitespace is intentionally left untouched — collapsing it
// could change the meaning of code (e.g. "a + b" vs "a+b").
func normalizeText(text string) string {
	lines := strings.Split(text, "\n")
	// Reuse the backing slice capacity by filtering in place.
	out := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return strings.Join(out, "\n")
}

// Get retrieves an embedding from the cache.
// Returns the embedding and true if found and not expired, nil and false otherwise.
func (c *EmbeddingCache) Get(text string) ([]float32, bool) {
	key := c.Key(text)

	c.mu.RLock()
	entry, exists := c.entries[key]
	if !exists {
		c.mu.RUnlock()
		return nil, false
	}
	if c.ttl > 0 && time.Since(entry.createdAt) > c.ttl {
		c.mu.RUnlock()
		c.mu.Lock()
		current, stillExists := c.entries[key]
		if stillExists && current.createdAt.Equal(entry.createdAt) &&
			time.Since(current.createdAt) > c.ttl {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, false
	}

	// Return a copy to prevent external modification.
	result := slices.Clone(entry.vector)
	c.mu.RUnlock()
	return result, true
}

// Set stores an embedding in the cache.
// If the cache is full, evicts old entries.
func (c *EmbeddingCache) Set(text string, vector []float32) {
	key := c.Key(text)

	vectorCopy := slices.Clone(vector)

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
	// Evict 10% of entries (or at least 1) to amortize eviction cost.
	toEvict := max(c.maxSize/10, 1)

	// Collect (key, createdAt) pairs and sort by creation time (oldest first).
	// Replaced the previous O(n²) bubble sort with an O(n log n) sort.
	type keyTime struct {
		key       string
		createdAt time.Time
	}
	entries := make([]keyTime, 0, len(c.entries))
	for k, v := range c.entries {
		entries = append(entries, keyTime{k, v.createdAt})
	}
	slices.SortFunc(entries, func(a, b keyTime) int {
		return a.createdAt.Compare(b.createdAt)
	})

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

// embedCacheBucket is the BoltDB bucket name used by DiskCache.
const embedCacheBucket = "embeddings"

const (
	diskCacheWriteQueueSize = 256
	diskCacheWriteBatchSize = 64
	diskCacheBinaryHeader   = "VGF1"
)

type diskCacheWrite struct {
	key   string
	data  []byte
	flush chan error
}

// DiskCache wraps an EmbeddingCache with BoltDB persistence. Embedding
// vectors are keyed by "sha256(text):model" so that different models do not
// collide. Reads check the in-memory cache first, then BoltDB, promoting
// disk hits back into memory. A single bounded writer batches disk updates.
type DiskCache struct {
	mem *EmbeddingCache
	db  *bolt.DB

	modelMu sync.RWMutex
	model   string

	lifecycleMu sync.RWMutex
	closed      bool
	writes      chan diskCacheWrite
	writerDone  chan error
	closeOnce   sync.Once
	closeErr    error
}

// NewPersistentCache creates a DiskCache backed by the BoltDB file at
// dbPath. The in-memory layer holds up to memSize entries.
func NewPersistentCache(memSize int, dbPath string) (*DiskCache, error) {
	return newPersistentCache(memSize, 0, dbPath)
}

func newPersistentCache(memSize int, ttl time.Duration, dbPath string) (*DiskCache, error) {
	if memSize <= 0 {
		memSize = 1000
	}

	// Ensure the parent directory exists so bbolt can create the file.
	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create cache dir: %w", err)
		}
	}

	bdb, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open embed cache db: %w", err)
	}

	// Ensure the bucket exists.
	if err := bdb.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(embedCacheBucket))
		return err
	}); err != nil {
		_ = bdb.Close()
		return nil, fmt.Errorf("create embed cache bucket: %w", err)
	}

	cache := &DiskCache{
		mem:        NewEmbeddingCache(memSize, ttl),
		db:         bdb,
		writes:     make(chan diskCacheWrite, diskCacheWriteQueueSize),
		writerDone: make(chan error, 1),
	}
	go cache.runWriter()
	return cache, nil
}

// SetModel records the embedding model name used to namespace disk keys.
// This must be set before Get/Set are called so the key includes the model.
func (d *DiskCache) SetModel(model string) {
	d.modelMu.Lock()
	d.model = model
	d.modelMu.Unlock()
}

// diskKey returns the BoltDB key for the given text and current model.
func (d *DiskCache) diskKey(text string) string {
	d.modelMu.RLock()
	model := d.model
	d.modelMu.RUnlock()
	return d.mem.Key(text) + ":" + model
}

// Get retrieves an embedding, checking memory first then BoltDB. Disk hits
// are promoted into the in-memory cache.
func (d *DiskCache) Get(text string) ([]float32, bool) {
	if v, ok := d.mem.Get(text); ok {
		return v, true
	}

	key := d.diskKey(text)
	var vec []float32
	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(embedCacheBucket))
		if b == nil {
			return nil
		}
		raw := b.Get([]byte(key))
		if len(raw) == 0 {
			return nil
		}
		var err error
		vec, err = decodeDiskVector(raw)
		return err
	})
	if err != nil || vec == nil {
		return nil, false
	}

	// Promote to memory.
	d.mem.Set(text, vec)
	return vec, true
}

// Set stores an embedding in memory and queues it for the bounded disk writer.
// Backpressure is applied when the queue is full, preventing unbounded memory
// and goroutine growth during sustained cache misses.
func (d *DiskCache) Set(text string, vector []float32) {
	d.mem.Set(text, vector)

	write := diskCacheWrite{
		key:  d.diskKey(text),
		data: encodeDiskVector(vector),
	}

	d.lifecycleMu.RLock()
	defer d.lifecycleMu.RUnlock()
	if !d.closed {
		d.writes <- write
	}
}

// Key returns the content-hash key (without model suffix) for the given text.
func (d *DiskCache) Key(text string) string {
	return d.mem.Key(text)
}

// Size returns the number of entries in the in-memory layer.
func (d *DiskCache) Size() int {
	return d.mem.Size()
}

// Clear removes all entries from the in-memory layer.
func (d *DiskCache) Clear() {
	d.mem.Clear()
}

// FlushToDisk waits until every write queued before the call has committed.
func (d *DiskCache) FlushToDisk() error {
	d.lifecycleMu.RLock()
	defer d.lifecycleMu.RUnlock()
	if d.closed {
		return errors.New("embedding cache is closed")
	}

	done := make(chan error, 1)
	d.writes <- diskCacheWrite{flush: done}
	return <-done
}

// Flush commits every disk write queued before this call. The cache remains
// open and reusable after the barrier completes.
func (d *DiskCache) Flush() error {
	return d.FlushToDisk()
}

// LoadFromDisk is a no-op at the API level: reads already lazy-load from
// BoltDB on cache miss. It is retained for interface symmetry and to
// validate the database is readable.
func (d *DiskCache) LoadFromDisk(path string) error {
	return d.db.View(func(tx *bolt.Tx) error {
		_ = tx.Bucket([]byte(embedCacheBucket))
		return nil
	})
}

// MergeFromDisk opens an external bbolt file read-only and copies every
// entry from the embeddings bucket into the live cache (both memory and
// the current bbolt file). This is used by the fcheap restore flow: the
// stashed cache file is restored to a temp path, then merged into the
// active DiskCache so subsequent Get() calls hit the cache without
// re-embedding. The source file is not modified. Best-effort: a failure
// to open or read the source is returned as an error, but the live cache
// is left in a usable state.
func (d *DiskCache) MergeFromDisk(srcPath string) error {
	src, err := bolt.Open(srcPath, 0600, &bolt.Options{ReadOnly: true, Timeout: 5 * time.Second})
	if err != nil {
		return fmt.Errorf("open source cache for merge: %w", err)
	}
	defer src.Close()

	type entry struct {
		key  string
		data []byte
	}
	var entries []entry

	err = src.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(embedCacheBucket))
		if b == nil {
			return nil // empty source — nothing to merge
		}
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if len(v) == 0 {
				continue
			}
			// Copy the value so it's safe to use after the transaction closes.
			cp := make([]byte, len(v))
			copy(cp, v)
			entries = append(entries, entry{key: string(k), data: cp})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("read source cache: %w", err)
	}

	if len(entries) == 0 {
		return nil
	}

	// Write all entries into the live bbolt file and promote to memory.
	err = d.db.Update(func(tx *bolt.Tx) error {
		b, bErr := tx.CreateBucketIfNotExists([]byte(embedCacheBucket))
		if bErr != nil {
			return bErr
		}
		for _, e := range entries {
			if err := b.Put([]byte(e.key), e.data); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("write merged entries: %w", err)
	}

	// Promote entries into the in-memory layer. The disk key is
	// "sha256(text):model"; we don't have the original text, so we only
	// populate the disk bucket — the memory layer will lazy-load on Get.
	// This keeps the merge cheap and avoids a reverse-hash lookup.
	return nil
}

// CachePath returns the bbolt file path backing this DiskCache. It is
// used by the fcheap stash flow to locate the file to snapshot. Returns
// empty when the path is not available (e.g. the cache was created in
// memory only).
func (d *DiskCache) CachePath() string {
	if d == nil || d.db == nil {
		return ""
	}
	return d.db.Path()
}

// Close closes the underlying BoltDB handle. After Close, further Get/Set
// calls will fail on the disk side (memory layer keeps working).
func (d *DiskCache) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.closeErr = d.Flush()

		d.lifecycleMu.Lock()
		d.closed = true
		close(d.writes)
		d.lifecycleMu.Unlock()

		if err := <-d.writerDone; d.closeErr == nil {
			d.closeErr = err
		}
		if err := d.db.Close(); d.closeErr == nil {
			d.closeErr = err
		}
	})
	return d.closeErr
}

func (d *DiskCache) runWriter() {
	batch := make([]diskCacheWrite, 0, diskCacheWriteBatchSize)
	var pendingErr error
	defer func() {
		d.writerDone <- pendingErr
		close(d.writerDone)
	}()
	for {
		write, ok := <-d.writes
		if !ok {
			if err := d.commitWrites(batch); pendingErr == nil {
				pendingErr = err
			}
			return
		}
		if write.flush != nil {
			if err := d.commitWrites(batch); pendingErr == nil {
				pendingErr = err
			}
			batch = batch[:0]
			write.flush <- pendingErr
			pendingErr = nil
			continue
		}

		batch = append(batch, write)
		for len(batch) < diskCacheWriteBatchSize {
			select {
			case next, open := <-d.writes:
				if !open {
					if err := d.commitWrites(batch); pendingErr == nil {
						pendingErr = err
					}
					return
				}
				if next.flush != nil {
					if err := d.commitWrites(batch); pendingErr == nil {
						pendingErr = err
					}
					batch = batch[:0]
					next.flush <- pendingErr
					pendingErr = nil
					continue
				}
				batch = append(batch, next)
			default:
				goto commit
			}
		}

	commit:
		if err := d.commitWrites(batch); pendingErr == nil {
			pendingErr = err
		}
		batch = batch[:0]
	}
}

func (d *DiskCache) commitWrites(batch []diskCacheWrite) error {
	if len(batch) == 0 {
		return nil
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(embedCacheBucket))
		if b == nil {
			return errors.New("embedding cache bucket is missing")
		}
		for _, write := range batch {
			if err := b.Put([]byte(write.key), write.data); err != nil {
				return err
			}
		}
		return nil
	})
}

func encodeDiskVector(vector []float32) []byte {
	data := make([]byte, 8+len(vector)*4)
	copy(data, diskCacheBinaryHeader)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(vector)))
	for i, value := range vector {
		binary.LittleEndian.PutUint32(data[8+i*4:], math.Float32bits(value))
	}
	return data
}

func decodeDiskVector(data []byte) ([]float32, error) {
	if len(data) < 4 || string(data[:4]) != diskCacheBinaryHeader {
		var vector []float32
		if err := json.Unmarshal(data, &vector); err != nil {
			return nil, err
		}
		return vector, nil
	}
	if len(data) < 8 {
		return nil, errors.New("invalid embedding cache entry")
	}
	count := int(binary.LittleEndian.Uint32(data[4:8]))
	if count > (len(data)-8)/4 || len(data) != 8+count*4 {
		return nil, errors.New("invalid embedding cache entry")
	}
	vector := make([]float32, count)
	for i := range vector {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[8+i*4:]))
	}
	return vector, nil
}
