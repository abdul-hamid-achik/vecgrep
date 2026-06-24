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
}

// DefaultThrottleConfig returns sensible defaults for the throttle layer.
func DefaultThrottleConfig() ThrottleConfig {
	return ThrottleConfig{
		Workers:     2,
		MaxInFlight: 4,
		CacheSize:   1000,
	}
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

	cache *EmbeddingCache

	// Rate limiter (nil when RPS is zero — no limit).
	limiter *rate.Limiter

	// In-flight semaphore.
	inFlight chan struct{}

	// Request queue with two priority lanes.
	mu      sync.Mutex
	bgQueue []throttleRequest
	fgQueue []throttleRequest
	notify  chan struct{}
	closed  bool

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
		cfg.Workers = 2
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 4
	}

	p := &ThrottledProvider{
		inner:    inner,
		cfg:      cfg,
		inFlight: make(chan struct{}, cfg.MaxInFlight),
		notify:   make(chan struct{}, 1),
		dedup:    make(map[string]*dedupEntry),
	}

	if cfg.CacheSize > 0 {
		p.cache = NewEmbeddingCache(cfg.CacheSize, cfg.CacheTTL)
	}

	if cfg.RPS > 0 {
		p.limiter = rate.NewLimiter(rate.Limit(cfg.RPS), 1)
	}

	// Start worker goroutines
	for i := 0; i < cfg.Workers; i++ {
		go p.worker()
	}

	return p
}

// Embed generates an embedding for the given text with throttling.
func (p *ThrottledProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, ErrEmptyText
	}

	// Check cache first
	if p.cache != nil {
		if v, ok := p.cache.Get(text); ok {
			return v, nil
		}
	}

	// Dedup: if the same text is already in flight, wait for it
	key := ""
	if p.cache != nil {
		key = p.cache.Key(text)
	}
	if entry := p.joinInFlight(key); entry != nil {
		select {
		case <-entry.done:
			return entry.result, entry.err
		case <-ctx.Done():
			return nil, ErrContextCanceled
		}
	}

	// Register ourselves as the in-flight request for this key
	p.registerInFlight(key)
	defer p.leaveInFlight(key)

	priority := priorityFromContext(ctx)

	resultCh := make(chan throttleResult, 1)
	req := throttleRequest{
		text:     text,
		priority: priority,
		result:   resultCh,
	}

	p.enqueue(req)

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
// It deduplicates texts within the batch and reuses cached results.
func (p *ThrottledProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	results := make([][]float32, len(texts))

	// Process each text through the throttle. We use Embed (which handles
	// cache + dedup) so identical texts in the batch share a single request.
	// For better throughput we could batch at the inner provider level, but
	// the primary goal of the throttle is to limit concurrency, not to
	// batch at the protocol level.
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

// Cache returns the underlying embedding cache, or nil if caching is disabled.
func (p *ThrottledProvider) Cache() *EmbeddingCache {
	return p.cache
}

// Close shuts down the worker pool. After Close, Embed calls will block
// until the context is canceled.
func (p *ThrottledProvider) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.notify <- struct{}{}
}

// enqueue adds a request to the appropriate priority lane.
func (p *ThrottledProvider) enqueue(req throttleRequest) {
	p.mu.Lock()
	if req.priority == PriorityInteractive {
		p.fgQueue = append(p.fgQueue, req)
	} else {
		p.bgQueue = append(p.bgQueue, req)
	}
	p.mu.Unlock()

	// Notify a worker that there's work available
	select {
	case p.notify <- struct{}{}:
	default:
	}
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
	for {
		// Wait for work notification
		<-p.notify

		for {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed {
				return
			}

			req, ok := p.dequeue()
			if !ok {
				break
			}

			// Acquire in-flight semaphore
			p.inFlight <- struct{}{}

			// Rate limit
			if p.limiter != nil {
				_ = p.limiter.Wait(context.Background())
			}

			// Generate embedding
			vec, err := p.inner.Embed(context.Background(), req.text)

			// Release in-flight slot
			<-p.inFlight

			// Send result
			req.result <- throttleResult{vector: vec, err: err}
		}
	}
}

// joinInFlight checks if there's already an in-flight request for the given
// cache key. If so, it returns the dedup entry so the caller can wait.
// If not, the caller should call registerInFlight to claim the slot.
func (p *ThrottledProvider) joinInFlight(key string) *dedupEntry {
	if key == "" {
		return nil
	}
	p.dedupMu.Lock()
	defer p.dedupMu.Unlock()
	return p.dedup[key]
}

// registerInFlight claims the in-flight slot for the given cache key.
// If the key is empty, this is a no-op.
func (p *ThrottledProvider) registerInFlight(key string) {
	if key == "" {
		return
	}
	p.dedupMu.Lock()
	if _, exists := p.dedup[key]; !exists {
		p.dedup[key] = &dedupEntry{done: make(chan struct{})}
	}
	p.dedupMu.Unlock()
}

// leaveInFlight removes the in-flight dedup entry for the given key and
// signals any waiters.
func (p *ThrottledProvider) leaveInFlight(key string) {
	if key == "" {
		return
	}
	p.dedupMu.Lock()
	if entry, ok := p.dedup[key]; ok {
		close(entry.done)
		delete(p.dedup, key)
	}
	p.dedupMu.Unlock()
}
