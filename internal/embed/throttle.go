package embed

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Priority classifies an embedding request as interactive (search query)
// or background (indexer). Interactive requests jump ahead of background
// requests in the throttle queue.
type Priority int

const (
	// PriorityBackground is the default priority for indexing work.
	PriorityBackground Priority = 0
	// PriorityInteractive is for user-facing queries that must return fast.
	PriorityInteractive Priority = 1
)

// priorityContextKey is the context key for embedding request priority.
type priorityContextKey struct{}

// WithPriority returns a context annotated with the given embedding
// priority. The throttled provider reads it to decide which queue lane
// to use.
func WithPriority(ctx context.Context, p Priority) context.Context {
	return context.WithValue(ctx, priorityContextKey{}, p)
}

// priorityFromContext extracts the embedding priority from the context,
// defaulting to PriorityBackground.
func priorityFromContext(ctx context.Context) Priority {
	if p, ok := ctx.Value(priorityContextKey{}).(Priority); ok {
		return p
	}
	return PriorityBackground
}

// ThrottleConfig configures the ThrottledProvider behaviour.
type ThrottleConfig struct {
	// Workers is the number of concurrent embedding workers (default 2).
	Workers int
	// RPS is the maximum embedding requests per second (token-bucket).
	// Zero means no rate limit.
	RPS float64
	// MaxInFlight is the maximum number of concurrent in-flight
	// embedding requests (default 4).
	MaxInFlight int
	// CacheSize is the size of the in-memory embedding cache.
	// Zero disables caching (use 1000 as a reasonable default).
	CacheSize int
	// CacheTTL is the time-to-live for cached embeddings. Zero means no expiry.
	CacheTTL time.Duration
	// CachePath, when non-empty, enables a disk-persistent embedding
	// cache backed by bbolt at the given path. When set, a DiskCache wraps
	// the in-memory cache so embeddings survive across runs. The in-memory
	// layer still serves hot reads.
	CachePath string
}

// DefaultThrottleConfig returns sensible defaults for the throttle layer.
func DefaultThrottleConfig() ThrottleConfig {
	return ThrottleConfig{
		Workers:     4,
		MaxInFlight: 8,
		CacheSize:   1000,
	}
}

// embeddingCache is the minimal interface satisfied by both the in-memory
// EmbeddingCache and the disk-backed DiskCache. ThrottledProvider uses it so
// it can transparently swap between the two implementations.
type embeddingCache interface {
	Get(text string) ([]float32, bool)
	Set(text string, vector []float32)
	Key(text string) string
}

// ThrottledProvider wraps an embedding Provider with content-hash dedup,
// a coalescing FIFO queue, a bounded worker pool, a token-bucket rate
// limiter, and two priority lanes (interactive vs background).
//
// It implements the Provider interface so it can be used anywhere a
// Provider is expected.
type ThrottledProvider struct {
	inner Provider
	cfg   ThrottleConfig

	cache embeddingCache

	// diskCache is non-nil when a persistent (bbolt) cache is configured.
	// It is closed on Close.
	diskCache *DiskCache

	// Rate limiter (nil when RPS is zero — no limit).
	limiter *rate.Limiter

	// In-flight semaphore.
	inFlight chan struct{}

	// Request queue with two priority lanes.
	mu          sync.Mutex
	bgQueue     []throttleRequest
	fgQueue     []throttleRequest
	notify      chan struct{}
	closed      bool
	workerCtx   context.Context
	cancel      context.CancelFunc
	workerWG    sync.WaitGroup
	operationWG sync.WaitGroup
	closeOnce   sync.Once

	// singleflight-style dedup: in-flight request keyed by cache key.
	dedupMu sync.Mutex
	dedup   map[string]*dedupEntry
}

// dedupEntry tracks an in-flight embedding request so concurrent callers
// for the same text share a single result.
type dedupEntry struct {
	done   chan struct{}
	result []float32
	err    error
}

type throttleRequest struct {
	text     string
	ctx      context.Context
	priority Priority
	result   chan<- throttleResult
}

type throttleResult struct {
	vector []float32
	err    error
}

// NewThrottledProvider wraps the given provider with the throttle layer.
func NewThrottledProvider(inner Provider, cfg ThrottleConfig) *ThrottledProvider {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 8
	}

	workerCtx, cancel := context.WithCancel(context.Background())
	p := &ThrottledProvider{
		inner:     inner,
		cfg:       cfg,
		inFlight:  make(chan struct{}, cfg.MaxInFlight),
		notify:    make(chan struct{}, 1),
		workerCtx: workerCtx,
		cancel:    cancel,
		dedup:     make(map[string]*dedupEntry),
	}

	if cfg.CacheSize > 0 {
		if cfg.CachePath != "" {
			dc, err := NewPersistentCache(cfg.CacheSize, cfg.CachePath)
			if err == nil {
				dc.SetModel(inner.Model())
				p.diskCache = dc
				p.cache = dc
			} else {
				// Fall back to in-memory only if the disk cache cannot be opened.
				p.cache = NewEmbeddingCache(cfg.CacheSize, cfg.CacheTTL)
			}
		} else {
			p.cache = NewEmbeddingCache(cfg.CacheSize, cfg.CacheTTL)
		}
	}

	if cfg.RPS > 0 {
		p.limiter = rate.NewLimiter(rate.Limit(cfg.RPS), 1)
	}

	// Start worker goroutines.
	p.workerWG.Add(cfg.Workers)
	for range cfg.Workers {
		go p.worker()
	}

	return p
}

// Embed generates an embedding for the given text with throttling.
func (p *ThrottledProvider) Embed(ctx context.Context, text string) (vec []float32, err error) {
	if !p.beginOperation() {
		return nil, ErrContextCanceled
	}
	defer p.operationWG.Done()

	if text == "" {
		return nil, ErrEmptyText
	}

	// Check cache first
	if p.cache != nil {
		if v, ok := p.cache.Get(text); ok {
			return v, nil
		}
	}

	// Dedup: atomically check if the same text is already in flight. If so,
	// wait for the leader's result. If not, we become the leader.
	key := ""
	if p.cache != nil {
		key = p.cache.Key(text)
	}
	if entry := p.joinOrRegisterInFlight(key); entry != nil {
		select {
		case <-entry.done:
			return entry.result, entry.err
		case <-ctx.Done():
			return nil, ErrContextCanceled
		}
	}
	// We are the leader. Signal waiters with our result when we return.
	defer p.leaveInFlight(key, vec, err)

	priority := priorityFromContext(ctx)

	resultCh := make(chan throttleResult, 1)
	req := throttleRequest{
		ctx:      ctx,
		text:     text,
		priority: priority,
		result:   resultCh,
	}

	if !p.enqueue(req) {
		return nil, ErrContextCanceled
	}

	select {
	case res := <-resultCh:
		if res.err == nil && p.cache != nil {
			p.cache.Set(text, res.vector)
		}
		return res.vector, res.err
	case <-ctx.Done():
		return nil, ErrContextCanceled
	}
}

// EmbedBatch generates embeddings for multiple texts with throttling.
// When the inner provider implements DocumentProvider, the entire batch
// is delegated to a single inner.EmbedDocuments call (one HTTP request for
// the Ollama /api/embed endpoint), bypassing the per-text worker queue.
// Otherwise it falls back to processing each text through Embed.
func (p *ThrottledProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Fast path: inner provider supports native batch embedding.
	if docProvider, ok := p.inner.(DocumentProvider); ok {
		return p.embedBatchDelegated(ctx, docProvider, texts)
	}

	// Fallback: process each text through the per-text throttle queue.
	results := make([][]float32, len(texts))
	var wg sync.WaitGroup
	errs := make([]error, len(texts))

	for i, text := range texts {
		if text == "" {
			errs[i] = ErrEmptyText
			continue
		}
		wg.Add(1)
		go func(idx int, t string) {
			defer wg.Done()
			v, err := p.Embed(ctx, t)
			if err != nil {
				errs[idx] = err
				return
			}
			results[idx] = v
		}(i, text)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return results, NewProviderError("throttle", "embedBatch", fmt.Errorf("text %d: %w", i, err))
		}
	}

	return results, nil
}

// EmbedDocuments implements the DocumentProvider interface.
// It delegates to the inner provider's EmbedDocuments when available,
// applying cache dedup as a pre-filter to skip texts we already have.
func (p *ThrottledProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	docProvider, ok := p.inner.(DocumentProvider)
	if !ok {
		// Inner doesn't support batch — fall back to EmbedBatch.
		return p.EmbedBatch(ctx, texts)
	}
	return p.embedBatchDelegated(ctx, docProvider, texts)
}

// embedBatchDelegated sends the batch to the inner DocumentProvider in a
// single call, using the cache to skip texts whose embeddings we already
// have. This bypasses the per-text worker queue — the batch endpoint
// handles concurrency internally.
func (p *ThrottledProvider) embedBatchDelegated(ctx context.Context, docProvider DocumentProvider, texts []string) ([][]float32, error) {
	if !p.beginOperation() {
		return nil, ErrContextCanceled
	}
	defer p.operationWG.Done()

	results := make([][]float32, len(texts))

	// Phase 1: serve everything we can from cache, collect misses.
	missIndices := make([]int, 0, len(texts))
	missTexts := make([]string, 0, len(texts))
	for i, text := range texts {
		if text == "" {
			return nil, NewProviderError("throttle", "embedDocuments", fmt.Errorf("text %d: %w", i, ErrEmptyText))
		}
		if p.cache != nil {
			if v, ok := p.cache.Get(text); ok {
				results[i] = v
				continue
			}
		}
		missIndices = append(missIndices, i)
		missTexts = append(missTexts, text)
	}

	if len(missTexts) == 0 {
		return results, nil
	}

	// Phase 2: acquire a single in-flight slot and delegate the entire
	// batch to the inner provider's native batch endpoint.
	opCtx, cancel := p.operationContext(ctx)
	defer cancel()
	select {
	case p.inFlight <- struct{}{}:
		defer func() { <-p.inFlight }()
	case <-opCtx.Done():
		return nil, NewProviderError("throttle", "embedDocuments", ErrContextCanceled)
	}

	if p.limiter != nil {
		if err := p.limiter.Wait(opCtx); err != nil {
			return nil, NewProviderError("throttle", "embedDocuments", ErrContextCanceled)
		}
	}

	missed, err := docProvider.EmbedDocuments(opCtx, missTexts)
	if err != nil {
		return nil, NewProviderError("throttle", "embedDocuments", err)
	}
	if len(missed) != len(missTexts) {
		return nil, NewProviderError("throttle", "embedDocuments",
			fmt.Errorf("expected %d embeddings, got %d", len(missTexts), len(missed)))
	}

	// Phase 3: scatter results back, populate cache.
	for j, idx := range missIndices {
		results[idx] = missed[j]
		if p.cache != nil && missed[j] != nil {
			p.cache.Set(missTexts[j], missed[j])
		}
	}

	return results, nil
}

// Model returns the name of the embedding model.
func (p *ThrottledProvider) Model() string {
	return p.inner.Model()
}

// Dimensions returns the embedding vector dimensions.
func (p *ThrottledProvider) Dimensions() int {
	return p.inner.Dimensions()
}

// Ping checks if the underlying provider is available.
func (p *ThrottledProvider) Ping(ctx context.Context) error {
	return p.inner.Ping(ctx)
}

// Warmup delegates to the inner provider's Warmup. This preloads the
// embedding model before a batch indexing run starts.
func (p *ThrottledProvider) Warmup(ctx context.Context) (time.Duration, error) {
	return p.inner.Warmup(ctx)
}

// Cache returns the underlying embedding cache, or nil if caching is disabled.
// When a disk cache is configured the returned value is the *DiskCache; callers
// can type-assert to access persistence-specific methods.
func (p *ThrottledProvider) Cache() embeddingCache {
	return p.cache
}

// Flush persists every disk-cache write queued before this call without
// shutting down the provider. In-memory caches require no flush.
func (p *ThrottledProvider) Flush() error {
	if p.diskCache == nil {
		return nil
	}
	return p.diskCache.Flush()
}

// Close cancels active work, rejects new requests, resolves queued callers,
// waits for all provider operations to finish, and then closes the disk cache.
func (p *ThrottledProvider) Close() {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		queued := append(p.fgQueue, p.bgQueue...)
		p.fgQueue = nil
		p.bgQueue = nil
		p.cancel()
		p.mu.Unlock()

		for _, req := range queued {
			req.result <- throttleResult{err: ErrContextCanceled}
		}

		p.workerWG.Wait()
		p.operationWG.Wait()
		if p.diskCache != nil {
			_ = p.diskCache.Close()
		}
	})
}

// beginOperation prevents Close from racing with a new operationWG.Add.
func (p *ThrottledProvider) beginOperation() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.operationWG.Add(1)
	return true
}

// enqueue adds a request to the appropriate priority lane unless shutdown has begun.
func (p *ThrottledProvider) enqueue(req throttleRequest) bool {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return false
	}
	if req.priority == PriorityInteractive {
		p.fgQueue = append(p.fgQueue, req)
	} else {
		p.bgQueue = append(p.bgQueue, req)
	}
	p.mu.Unlock()

	select {
	case p.notify <- struct{}{}:
	default:
	}
	return true
}

// dequeue removes the next request, preferring interactive over background.
func (p *ThrottledProvider) dequeue() (throttleRequest, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.fgQueue) > 0 {
		req := p.fgQueue[0]
		p.fgQueue = p.fgQueue[1:]
		return req, true
	}
	if len(p.bgQueue) > 0 {
		req := p.bgQueue[0]
		p.bgQueue = p.bgQueue[1:]
		return req, true
	}
	return throttleRequest{}, false
}

// worker processes requests from the queue.
func (p *ThrottledProvider) worker() {
	defer p.workerWG.Done()
	for {
		select {
		case <-p.workerCtx.Done():
			return
		case <-p.notify:
		case <-time.After(100 * time.Millisecond):
		}

		for {
			req, ok := p.dequeue()
			if !ok {
				break
			}

			opCtx, cancel := p.operationContext(req.ctx)
			select {
			case p.inFlight <- struct{}{}:
			case <-opCtx.Done():
				cancel()
				req.result <- throttleResult{err: ErrContextCanceled}
				continue
			}

			if p.limiter != nil {
				if err := p.limiter.Wait(opCtx); err != nil {
					<-p.inFlight
					cancel()
					req.result <- throttleResult{err: ErrContextCanceled}
					continue
				}
			}

			vec, err := p.inner.Embed(opCtx, req.text)
			if opCtx.Err() != nil {
				vec = nil
				err = ErrContextCanceled
			}
			<-p.inFlight
			cancel()
			req.result <- throttleResult{vector: vec, err: err}
		}
	}
}

func (p *ThrottledProvider) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	opCtx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(p.workerCtx, cancel)
	return opCtx, func() {
		stopCancel()
		cancel()
	}
}

// joinOrRegisterInFlight atomically checks if there's already an in-flight
// request for the given cache key. If so, it returns the existing dedup entry
// (the caller should wait on it). If not, it creates and registers a new entry
// and returns nil (the caller is the leader and should proceed to embed).
//
// This is a single atomic operation to avoid the race where two goroutines
// both see no entry and both proceed to embed.
func (p *ThrottledProvider) joinOrRegisterInFlight(key string) *dedupEntry {
	if key == "" {
		return nil
	}
	p.dedupMu.Lock()
	defer p.dedupMu.Unlock()
	if entry, ok := p.dedup[key]; ok {
		return entry // someone else is the leader
	}
	p.dedup[key] = &dedupEntry{done: make(chan struct{})}
	return nil // we are the leader
}

// leaveInFlight records the result, removes the in-flight dedup entry for
// the given key, and signals any waiters.
func (p *ThrottledProvider) leaveInFlight(key string, result []float32, err error) {
	if key == "" {
		return
	}
	p.dedupMu.Lock()
	if entry, ok := p.dedup[key]; ok {
		entry.result = result
		entry.err = err
		close(entry.done)
		delete(p.dedup, key)
	}
	p.dedupMu.Unlock()
}
