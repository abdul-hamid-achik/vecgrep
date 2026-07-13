package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

// Default source-buffer and incremental-sync thresholds. Source files queued
// between the walker and chunk workers are bounded by bytes rather than count.
// The vector DB is flushed after this many files or this much elapsed time,
// whichever comes first. A final sync always runs at the end.
const (
	defaultSourceBufferBytes    = 8 * 1024 * 1024
	defaultSyncInterval         = 50
	defaultSyncIntervalDuration = 30 * time.Second
)

// IndexerConfig holds configuration for the indexer.
type IndexerConfig struct {
	ChunkSize      int
	ChunkOverlap   int
	IgnorePatterns []string
	MaxFileSize    int64
	BatchSize      int
	Workers        int
	// SourceBufferBytes bounds source content retained by the walker and queue.
	// Zero falls back to defaultSourceBufferBytes. A file larger than the budget
	// consumes the whole budget while queued. Since workers release that charge
	// when they receive a file, total retained source bytes are bounded by
	// max(SourceBufferBytes, MaxFileSize) + Workers*MaxFileSize: at most one
	// budget-sized queue (or one oversized file) plus one active file per worker.
	SourceBufferBytes int64
	// SyncInterval is the number of files to process between incremental
	// db.Sync() calls during indexing. Zero falls back to defaultSyncInterval.
	SyncInterval int
	// SyncIntervalDuration is the maximum elapsed time between incremental
	// db.Sync() calls. Zero falls back to defaultSyncIntervalDuration.
	SyncIntervalDuration time.Duration
}

// DefaultIndexerConfig returns sensible defaults for indexing.
func DefaultIndexerConfig() IndexerConfig {
	return IndexerConfig{
		ChunkSize:    2048,
		ChunkOverlap: 256,
		IgnorePatterns: []string{
			".git/**",
			".vecgrep/**",
			"node_modules/**",
			"vendor/**",
			"__pycache__/**",
			"*.min.js",
			"*.min.css",
			"*.lock",
			"go.sum",
			"package-lock.json",
			"yarn.lock",
		},
		MaxFileSize:          1024 * 1024, // 1MB
		BatchSize:            64,
		Workers:              4,
		SourceBufferBytes:    defaultSourceBufferBytes,
		SyncInterval:         defaultSyncInterval,
		SyncIntervalDuration: defaultSyncIntervalDuration,
	}
}

// Progress represents indexing progress information.
type Progress struct {
	TotalFiles     int
	ProcessedFiles int
	SkippedFiles   int
	TotalChunks    int
	CurrentFile    string
	StartTime      time.Time
	Errors         []error
}

// ProgressCallback is called during indexing to report progress.
type ProgressCallback func(Progress)

// Indexer orchestrates the file indexing process.
type Indexer struct {
	db       *db.DB
	provider embed.Provider
	chunker  *Chunker
	config   IndexerConfig
	progress ProgressCallback

	structuralMu       sync.RWMutex
	structuralSource   StructuralChunkSource
	structuralRequired bool

	observerMu sync.RWMutex
	observer   IndexRunObserver
	attemptID  string

	// Test seams for observing storage calls without widening the public DB
	// contract. Production leaves these nil and uses db directly.
	syncFn       func() error
	deleteFileFn func(context.Context, string, string) (int64, error)
}

// NewIndexer creates a new Indexer.
func NewIndexer(database *db.DB, provider embed.Provider, cfg IndexerConfig) *Indexer {
	defaults := DefaultIndexerConfig()
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaults.BatchSize
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaults.Workers
	}
	if cfg.SourceBufferBytes <= 0 {
		cfg.SourceBufferBytes = defaults.SourceBufferBytes
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = defaults.SyncInterval
	}
	if cfg.SyncIntervalDuration <= 0 {
		cfg.SyncIntervalDuration = defaults.SyncIntervalDuration
	}
	chunkerCfg := ChunkerConfig{
		ChunkSize:    cfg.ChunkSize,
		ChunkOverlap: cfg.ChunkOverlap,
	}

	return &Indexer{
		db:       database,
		provider: provider,
		chunker:  NewChunker(chunkerCfg),
		config:   cfg,
	}
}

// SetProgressCallback sets a callback for progress updates.
func (idx *Indexer) SetProgressCallback(cb ProgressCallback) {
	idx.progress = cb
}

func (idx *Indexer) sync() error {
	if idx.syncFn != nil {
		return idx.syncFn()
	}
	return idx.db.Sync()
}

func (idx *Indexer) deleteFile(ctx context.Context, projectRoot, path string) (int64, error) {
	if idx.deleteFileFn != nil {
		return idx.deleteFileFn(ctx, projectRoot, path)
	}
	return idx.db.DeleteProjectFile(ctx, projectRoot, path)
}

// IndexResult contains the results of an indexing operation.
type IndexResult struct {
	FilesProcessed int
	FilesSkipped   int
	FilesDeleted   int
	ChunksCreated  int
	Duration       time.Duration
	Errors         []error
	Ingestion      IngestionCounts
}

// OriginCounts are exact counts for chunks written by one indexing attempt.
// A file may appear in more than one origin bucket (for example, a structural
// file with generic gap chunks), while each chunk belongs to exactly one.
type OriginCounts struct {
	Structural int `json:"structural"`
	Gap        int `json:"gap"`
	Local      int `json:"local"`
}

// IngestionCounts separates persisted files and chunks by their actual
// producer path. It is run-scoped rather than a claim about the whole index.
type IngestionCounts struct {
	Files  OriginCounts `json:"files"`
	Chunks OriginCounts `json:"chunks"`
}

func (c *IngestionCounts) add(other IngestionCounts) {
	c.Files.Structural += other.Files.Structural
	c.Files.Gap += other.Files.Gap
	c.Files.Local += other.Files.Local
	c.Chunks.Structural += other.Chunks.Structural
	c.Chunks.Gap += other.Chunks.Gap
	c.Chunks.Local += other.Chunks.Local
}

// IndexRunReport is the storage-neutral event emitted after a public Index or
// ReindexAll attempt. App owns any durable receipt derived from it.
type IndexRunReport struct {
	AttemptID            string
	ScopeComplete        bool
	ProjectRoot          string
	StartedAt            time.Time
	FinishedAt           time.Time
	StructuralConfigured bool
	StructuralRequired   bool
	StructuralLoaded     bool
	StructuralComplete   bool
	StructuralProjectKey string
	IndexFingerprint     string
	StructuralFiles      int
	StructuralIssues     int
	StructuralWarning    bool
	FailureStage         string
	Result               *IndexResult
	Err                  error
}

// IndexRunObserver records an attempt outside the indexing package. Returning
// an error preserves any populated result for diagnostics but also fails the
// run, so receipt persistence cannot fail silently.
type IndexRunObserver func(IndexRunReport) error

// SetIndexRunObserver installs the app-owned durable receipt hook.
func (idx *Indexer) SetIndexRunObserver(observer IndexRunObserver) {
	idx.observerMu.Lock()
	defer idx.observerMu.Unlock()
	idx.observer = observer
}

// SetIndexRunAttemptID binds the next public Index/ReindexAll call to the
// durable app-owned ingestion attempt that was written before storage mutation.
// Indexers used outside app may leave it empty; receipt observers are expected
// to reject an unbound run rather than update unrelated durable evidence.
func (idx *Indexer) SetIndexRunAttemptID(attemptID string) {
	idx.observerMu.Lock()
	defer idx.observerMu.Unlock()
	idx.attemptID = attemptID
}

func (idx *Indexer) indexRunAttemptID() string {
	idx.observerMu.RLock()
	defer idx.observerMu.RUnlock()
	return idx.attemptID
}

func (idx *Indexer) observeIndexRun(report IndexRunReport) error {
	idx.observerMu.RLock()
	observer := idx.observer
	idx.observerMu.RUnlock()
	if observer == nil {
		return nil
	}
	return observer(report)
}

// Index indexes the given paths.
//
// The pipeline has three concurrent stages so the embedder is never starved
// and its batches stay well-packed (the dominant cost is the embedding calls):
//
//		walker → fileChan → [chunk workers] → itemChan → [batcher] → batchChan → [embed pool] → insert+report
//
//	  - chunk workers read + chunk each changed file (fast, CPU/IO bound) and emit
//	    one item per chunk instead of embedding inline;
//	  - the batcher coalesces items from *many* files into full BatchSize batches,
//	    so a 6-chunk file no longer wastes a request — chunks ride along with
//	    others. This is the key throughput win over per-file embedding;
//	  - the embed pool runs several batches concurrently to overlap HTTP/queue
//	    latency, scattering each result back to its file and inserting + reporting
//	    a file the moment its last chunk is embedded.
//
// The walk overlaps with embedding (the GPU starts while the walk continues) and
// incremental db.Sync() calls flush the vector store periodically so a long run
// stays crash-resilient.
func (idx *Indexer) Index(ctx context.Context, projectRoot string, paths ...string) (*IndexResult, error) {
	return idx.indexObserved(ctx, projectRoot, paths...)
}

func (idx *Indexer) indexObserved(ctx context.Context, projectRoot string, paths ...string) (result *IndexResult, runErr error) {
	startedAt := time.Now()
	structuralConfig := idx.SnapshotStructuralChunkSource()
	report := IndexRunReport{
		AttemptID:            idx.indexRunAttemptID(),
		ScopeComplete:        len(paths) == 0,
		ProjectRoot:          projectRoot,
		StartedAt:            startedAt,
		StructuralConfigured: structuralConfig.source != nil,
		StructuralRequired:   structuralConfig.required,
	}
	defer func() {
		report.FinishedAt = time.Now()
		report.Result = result
		report.Err = runErr
		if runErr != nil && report.FailureStage == "" {
			report.FailureStage = "index"
		}
		if err := idx.observeIndexRun(report); err != nil {
			receiptErr := fmt.Errorf("record ingestion receipt: %w", err)
			if result != nil {
				result.Errors = append(result.Errors, receiptErr)
			}
			runErr = errors.Join(runErr, receiptErr)
		}
	}()
	structural, warning, err := idx.loadStructuralChunksWithSnapshot(ctx, projectRoot, structuralConfig)
	if err != nil {
		report.FailureStage = "structural_load"
		return nil, err
	}
	populateStructuralRunReport(&report, structural, warning)
	if structuralConfig.required && structural != nil && len(structural.Files) > 0 {
		prepared, err := idx.prepareRequiredStructuralFiles(ctx, projectRoot, paths, structural)
		if err != nil {
			report.FailureStage = "structural_preflight"
			return nil, err
		}
		return idx.indexPrepared(ctx, projectRoot, true, structural, warning, &prepared, paths...)
	}
	return idx.index(ctx, projectRoot, true, structural, warning, paths...)
}

func populateStructuralRunReport(report *IndexRunReport, structural *StructuralChunkSet, warning error) {
	if report == nil {
		return
	}
	report.StructuralWarning = warning != nil
	if structural == nil {
		return
	}
	report.StructuralLoaded = true
	report.StructuralComplete = structural.Complete
	report.StructuralProjectKey = structural.ProjectKey
	report.IndexFingerprint = structural.IndexFingerprint
	report.StructuralFiles = len(structural.Files)
	report.StructuralIssues = len(structural.Issues)
}

// index runs the streaming pipeline. deleteExisting is false only after a
// project Reset, when per-file deletion would redundantly scan an empty store.
func (idx *Indexer) index(ctx context.Context, projectRoot string, deleteExisting bool, structural *StructuralChunkSet, structuralWarning error, paths ...string) (*IndexResult, error) {
	return idx.indexPrepared(ctx, projectRoot, deleteExisting, structural, structuralWarning, nil, paths...)
}

// indexPrepared runs the normal streaming pipeline. When prepared is non-nil,
// it is a preflighted immutable view of the requested files. Required
// structural mode uses this path so no filesystem race after validation can
// silently downgrade a symbol file to the built-in chunker.
func (idx *Indexer) indexPrepared(ctx context.Context, projectRoot string, deleteExisting bool, structural *StructuralChunkSet, structuralWarning error, prepared *[]fileInfo, paths ...string) (*IndexResult, error) {
	startTime := time.Now()

	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}

	// Build gitignore matcher
	ignoreMatcher, err := idx.buildIgnoreMatcher(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("build ignore matcher: %w", err)
	}

	// Get existing file hashes from veclite up front for incremental
	// filtering. A durable dirty marker must fail closed: indexing everything
	// without a project reset cannot clear that marker and would leave freshness
	// unknown forever. Other legacy read failures retain the existing no-filter
	// fallback.
	existingHashes, err := idx.db.GetFileHashes(absRoot)
	if err != nil {
		if errors.Is(err, db.ErrProjectFileHashesDirty) {
			return nil, err
		}
		existingHashes = map[string]string{}
	}

	batchSize := idx.config.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultIndexerConfig().BatchSize
	}

	// Pipeline channels. Source files are charged against sourceBudget before
	// the walker reads and retains their content, then released when a chunk
	// worker receives them. Chunk items and embedding batches retain their own
	// independent backpressure.
	sourceBudgetBytes := idx.config.SourceBufferBytes
	sourceBudget := semaphore.NewWeighted(sourceBudgetBytes)
	fileChan := make(chan fileInfo, idx.config.Workers)
	itemChan := make(chan embedItem, batchSize*2)
	batchChan := make(chan []embedItem, idx.config.Workers)
	resultsChan := make(chan fileResult, idx.config.Workers)

	// Shared counters for progress reporting, updated by the walker and
	// read by the results collector.
	var totalDiscovered int64 // files queued for indexing
	var skippedCount int64    // files skipped (unchanged)
	var scan *fullScanState
	if deleteExisting && len(paths) == 0 {
		scan = newFullScanState()
	}

	// Stage 1: chunk workers. Read + chunk each changed file and emit one item
	// per chunk; files needing no embedding (binary / empty) report immediately.
	var chunkWG sync.WaitGroup
	for i := 0; i < idx.config.Workers; i++ {
		chunkWG.Add(1)
		go func() {
			defer chunkWG.Done()
			idx.chunkWorker(ctx, absRoot, deleteExisting, structural, sourceBudget, fileChan, itemChan, resultsChan)
		}()
	}
	go func() {
		chunkWG.Wait()
		close(itemChan) // no more chunks → the batcher can flush and finish
	}()

	// Stage 2: batcher. Packs items from all files into full batches.
	go func() {
		idx.batchItems(itemChan, batchChan, batchSize)
		close(batchChan)
	}()

	// Stage 3: embed pool. Concurrent batches overlap embedding latency.
	var embedWG sync.WaitGroup
	for i := 0; i < idx.config.Workers; i++ {
		embedWG.Add(1)
		go func() {
			defer embedWG.Done()
			for batch := range batchChan {
				idx.embedBatch(ctx, batch, resultsChan)
			}
		}()
	}
	// resultsChan is fed by chunk workers (binary/empty files) and the embed
	// pool (finished files). The embed pool drains only after the chunk workers
	// close itemChan, so it always finishes last — closing here is safe for
	// both producers.
	go func() {
		embedWG.Wait()
		close(resultsChan)
	}()

	// Walker goroutine: walks the tree, hashes each file, applies the
	// incremental filter inline, and sends files needing indexing to
	// fileChan. Closes fileChan when the walk completes (or on
	// error/cancel) so the pipeline drains and exits.
	walkErrCh := make(chan error, 1)
	go func() {
		var err error
		if prepared != nil {
			err = feedPreparedFiles(ctx, *prepared, existingHashes, scan, fileChan, &totalDiscovered, &skippedCount)
		} else {
			err = idx.walkAndFilterStructural(ctx, projectRoot, absRoot, paths, ignoreMatcher, existingHashes, structural, scan, sourceBudget, sourceBudgetBytes, fileChan, &totalDiscovered, &skippedCount)
		}
		close(fileChan)
		walkErrCh <- err
	}()

	result := &IndexResult{}
	if structuralWarning != nil {
		result.Errors = append(result.Errors, structuralWarning)
	}
	progress := Progress{StartTime: startTime}

	// Incremental sync thresholds (fall back to defaults when unset).
	syncInterval := idx.config.SyncInterval
	if syncInterval <= 0 {
		syncInterval = defaultSyncInterval
	}
	syncDuration := idx.config.SyncIntervalDuration
	if syncDuration <= 0 {
		syncDuration = defaultSyncIntervalDuration
	}
	lastSyncCount := 0
	lastSyncTime := time.Now()
	synced := false

	for r := range resultsChan {
		result.FilesProcessed++
		result.ChunksCreated += r.chunksCreated
		result.Ingestion.add(r.ingestion)

		if r.err != nil {
			result.Errors = append(result.Errors, r.err)
		}

		progress.ProcessedFiles = result.FilesProcessed
		progress.TotalChunks = result.ChunksCreated
		progress.CurrentFile = r.path
		progress.Errors = result.Errors
		progress.TotalFiles = int(atomic.LoadInt64(&totalDiscovered))
		progress.SkippedFiles = int(atomic.LoadInt64(&skippedCount))

		if idx.progress != nil {
			idx.progress(progress)
		}

		// Incremental DB sync: flush periodically so a long indexing
		// run is crash-resilient and search can see partial progress.
		if result.FilesProcessed-lastSyncCount >= syncInterval || time.Since(lastSyncTime) > syncDuration {
			if err := idx.sync(); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("incremental sync: %w", err))
			} else {
				lastSyncCount = result.FilesProcessed
				lastSyncTime = time.Now()
				synced = true
			}
		}
	}

	// Surface walk errors. Cancellation is a hard run failure: treating a
	// partially observed tree as a completed ingestion could advance durable
	// freshness evidence for work that never ran.
	walkErr := <-walkErrCh
	var fatalErr error
	if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
		fatalErr = walkErr
	} else if walkErr != nil {
		result.Errors = append(result.Errors, fmt.Errorf("walk: %w", walkErr))
	}

	result.FilesSkipped = int(atomic.LoadInt64(&skippedCount))
	if scan != nil && scan.complete && walkErr == nil && ctx.Err() == nil {
		deleted, pruneErrors := idx.pruneDeletedFiles(ctx, absRoot, existingHashes, scan.seen)
		result.FilesDeleted = deleted
		result.Errors = append(result.Errors, pruneErrors...)
	}

	// Final sync persists any work not already covered by an incremental sync.
	// An empty/no-op run still syncs once, while a periodic sync on the final
	// result is not immediately duplicated.
	if !synced || lastSyncCount != result.FilesProcessed {
		if err := idx.sync(); err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("sync: %w", err))
		}
	}
	if fatalErr == nil && ctx.Err() != nil {
		fatalErr = ctx.Err()
	}

	result.Duration = time.Since(startTime)
	return result, fatalErr
}

// fileInfo holds information about a file to be indexed. The content
// field caches the file bytes read during hashing so that chunkFile can
// reuse them without a second disk read. The content is nil'd after
// chunking to avoid holding all file contents in memory simultaneously.
type fileInfo struct {
	path         string
	relativePath string
	hash         string
	// sourceHash is the SHA-256 of the raw file. hash may additionally include
	// the per-file structural profile for incremental invalidation.
	sourceHash string
	size       int64
	content    []byte
	queueBytes int64
}

// fileResult holds the result of indexing a single file.
type fileResult struct {
	path          string
	chunksCreated int
	ingestion     IngestionCounts
	err           error
}

// fileTask tracks one file's chunks as they are embedded across (potentially
// several) shared batches. Its chunks may be scattered among batches that also
// carry other files' chunks, so completion is reference-counted: the goroutine
// that drives remaining to zero owns inserting the file and reporting it.
type fileTask struct {
	path      string
	records   []db.ChunkRecord // one per chunk, in chunk order
	embeds    [][]float32      // filled in by slot as batches complete
	ingestion IngestionCounts

	mu        sync.Mutex
	remaining int  // chunks not yet accounted for
	failed    bool // at least one chunk failed to embed
}

// embedItem is a single chunk awaiting embedding, tagged with the file task and
// the slot to write its embedding back into.
type embedItem struct {
	task *fileTask
	slot int
	text string
}

// complete records a chunk's embedding (or its failure) and returns true when
// this was the file's last outstanding chunk.
func (t *fileTask) complete(slot int, emb []float32, failed bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if failed {
		t.failed = true
	} else {
		t.embeds[slot] = emb
	}
	t.remaining--
	return t.remaining == 0
}

// skip accounts for n chunks that will never be embedded (e.g. context cancelled
// before they were queued) and returns true if the file is now complete.
func (t *fileTask) skip(n int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failed = true
	t.remaining -= n
	return t.remaining == 0
}

type sourceByteBudget interface {
	Acquire(context.Context, int64) error
	Release(int64)
}

// chunkWorker reads and chunks files from fileChan, emitting one embedItem per
// chunk. It releases each file's source-buffer charge as soon as the file
// becomes active, keeping queued bytes separate from active-worker memory.
// Files that need no embedding report completion directly.
func (idx *Indexer) chunkWorker(ctx context.Context, projectRoot string, deleteExisting bool, structural *StructuralChunkSet, sourceBudget sourceByteBudget, files <-chan fileInfo, items chan<- embedItem, results chan<- fileResult) {
	for file := range files {
		sourceBudget.Release(file.queueBytes)
		select {
		case <-ctx.Done():
			results <- fileResult{path: file.path, err: ctx.Err()}
			return
		default:
		}
		idx.chunkFile(ctx, projectRoot, deleteExisting, structural, file, items, results)
	}
}

// chunkFile reads a file (reusing the content cached during hashing), drops its
// stale chunks, splits it, and hands each chunk to the embed pipeline. Binary
// and empty files report immediately; everything else completes asynchronously
// once its chunks are embedded and inserted.
func (idx *Indexer) chunkFile(ctx context.Context, projectRoot string, deleteExisting bool, structural *StructuralChunkSet, file fileInfo, items chan<- embedItem, results chan<- fileResult) {
	// Use cached content from the hash phase. If for some reason content is
	// nil (e.g. fileInfo was constructed directly), fall back to reading.
	content := file.content
	if content == nil {
		var err error
		content, err = os.ReadFile(file.path)
		if err != nil {
			results <- fileResult{path: file.path, err: fmt.Errorf("read file: %w", err)}
			return
		}
	}

	// Drop stale chunks during incremental re-indexing. ReindexAll has already
	// reset the project, so deleting every file again would be redundant.
	// This must happen before binary/empty eligibility checks: a file that was
	// previously text can become binary (or otherwise chunkless), and a
	// successful index must remove its old chunks/hash so raw freshness can
	// converge instead of reporting it modified forever.
	if deleteExisting {
		if _, err := idx.deleteFile(ctx, projectRoot, file.relativePath); err != nil {
			results <- fileResult{path: file.path, err: fmt.Errorf("delete existing file chunks: %w", err)}
			return
		}
	}

	if !isChunkEligibleContent(content) {
		results <- fileResult{path: file.path} // binary/empty/whitespace: skip silently
		return
	}

	lang := DetectLanguage(file.path)

	chunks := idx.chunker.ChunkFile(string(content), file.path)
	if structuralFile, ok := structuralFileForHash(structural, file.relativePath, file.sourceHash); ok {
		chunks = structuralFile.Chunks
	}
	// Release the content reference so it can be GC'd while chunks are embedded.
	file.content = nil

	// Defensively drop empty / whitespace-only chunks. They carry no semantic
	// signal, and an empty text makes the whole shared embed batch fail
	// (provider ErrEmptyText) — which would then fail every OTHER file packed
	// into that batch too. The chunker shouldn't emit these, but guarding here
	// keeps one bad chunk from poisoning unrelated files.
	if len(chunks) > 0 {
		kept := chunks[:0]
		for _, c := range chunks {
			if strings.TrimSpace(c.Content) != "" {
				kept = append(kept, c)
			}
		}
		chunks = kept
	}

	if len(chunks) == 0 {
		results <- fileResult{path: file.path}
		return
	}
	ingestion := countChunkOrigins(chunks)

	// Pre-build the records; embeddings are filled in as batches complete.
	records := make([]db.ChunkRecord, len(chunks))
	for i, chunk := range chunks {
		records[i] = db.NewChunkRecord(
			file.path, file.relativePath, file.hash, file.size, string(lang),
			chunk.Content, chunk.StartLine, chunk.EndLine, chunk.StartByte, chunk.EndByte,
			string(chunk.ChunkType), chunk.SymbolName, projectRoot,
		)
		records[i].ChunkIndex = i
		records[i].SourceHash = file.sourceHash
	}
	task := &fileTask{
		path:      file.path,
		records:   records,
		embeds:    make([][]float32, len(chunks)),
		remaining: len(chunks),
		ingestion: ingestion,
	}

	for i, chunk := range chunks {
		select {
		case items <- embedItem{task: task, slot: i, text: embeddingContent(chunk)}:
		case <-ctx.Done():
			// Stop feeding; let any already-queued chunks finish the file with
			// what was embedded so far.
			if task.skip(len(chunks) - i) {
				idx.finishFile(task, results)
			}
			return
		}
	}
}

func countChunkOrigins(chunks []Chunk) IngestionCounts {
	var counts IngestionCounts
	for _, chunk := range chunks {
		switch chunk.Origin {
		case ChunkOriginStructural:
			counts.Chunks.Structural++
		case ChunkOriginGap:
			counts.Chunks.Gap++
		default:
			counts.Chunks.Local++
		}
	}
	if counts.Chunks.Structural > 0 {
		counts.Files.Structural = 1
	}
	if counts.Chunks.Gap > 0 {
		counts.Files.Gap = 1
	}
	if counts.Chunks.Local > 0 {
		counts.Files.Local = 1
	}
	return counts
}

// batchItems coalesces per-chunk items from every file into batches of up to
// batchSize. A partial trailing batch is flushed when items closes. Because
// chunking far outpaces embedding, the buffer reaches batchSize and flushes a
// full batch on the steady path; only the final tail is ever short.
func (idx *Indexer) batchItems(items <-chan embedItem, batches chan<- []embedItem, batchSize int) {
	buf := make([]embedItem, 0, batchSize)
	for it := range items {
		buf = append(buf, it)
		if len(buf) >= batchSize {
			batches <- buf
			buf = make([]embedItem, 0, batchSize)
		}
	}
	if len(buf) > 0 {
		batches <- buf
	}
}

// embedBatch embeds one packed batch and scatters the results back to the files
// the chunks came from, inserting and reporting each file as it completes.
func (idx *Indexer) embedBatch(ctx context.Context, batch []embedItem, results chan<- fileResult) {
	texts := make([]string, len(batch))
	for i, it := range batch {
		texts[i] = it.text
	}
	embeddings, err := embedDocuments(ctx, idx.provider, texts)
	for i, it := range batch {
		var emb []float32
		if err == nil && i < len(embeddings) {
			emb = embeddings[i]
		}
		// A nil/missing embedding (a short or partial provider response that did
		// not itself error) is a failure, not a silently-dropped chunk: mark it
		// so the file is reported and retried rather than recorded as complete.
		failed := err != nil || emb == nil
		if it.task.complete(it.slot, emb, failed) {
			idx.finishFile(it.task, results)
		}
	}
}

// finishFile inserts a fully-embedded file and reports the result. It runs only
// after the file's last chunk is accounted for, so reading the task's slices
// without the lock is safe (the reference-count handoff happens-before via the
// mutex in complete/skip).
//
// If ANY of the file's chunks failed to embed, nothing is inserted: persisting
// only the good chunks would also persist the file's hash, so the next
// incremental run would see the hash match and permanently skip the missing
// chunks. Inserting nothing leaves the file un-hashed, so it is retried in full
// next time (the stale chunks were already dropped by chunkFile's DeleteFile).
func (idx *Indexer) finishFile(task *fileTask, results chan<- fileResult) {
	if task.failed {
		results <- fileResult{path: task.path, err: fmt.Errorf("embed: one or more chunks failed for %s", task.path)}
		return
	}

	ids, err := idx.db.InsertChunkBatch(task.records, task.embeds)
	res := fileResult{path: task.path}
	if err != nil {
		res.err = fmt.Errorf("batch insert: %w", err)
	} else {
		res.chunksCreated = len(ids)
		res.ingestion = task.ingestion
	}
	results <- res
}

func embedDocuments(ctx context.Context, provider embed.Provider, texts []string) ([][]float32, error) {
	if documentProvider, ok := provider.(embed.DocumentProvider); ok {
		return documentProvider.EmbedDocuments(ctx, texts)
	}
	return provider.EmbedBatch(ctx, texts)
}

// buildIgnoreMatcher builds a gitignore-style matcher for file filtering.
func (idx *Indexer) buildIgnoreMatcher(rootPath string) (*gitignore.GitIgnore, error) {
	// Start with configured ignore patterns
	patterns := make([]string, len(idx.config.IgnorePatterns))
	copy(patterns, idx.config.IgnorePatterns)

	// Try to read .gitignore
	gitignorePath := filepath.Join(rootPath, ".gitignore")
	if content, err := os.ReadFile(gitignorePath); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}

	// Try to read .vecgrepignore
	vecgrepignorePath := filepath.Join(rootPath, ".vecgrepignore")
	if content, err := os.ReadFile(vecgrepignorePath); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}

	matcher := gitignore.CompileIgnoreLines(patterns...)
	return matcher, nil
}

// collectFiles walks the file tree and collects files to index.
func (idx *Indexer) collectFiles(ctx context.Context, rootPath string, paths []string, ignore *gitignore.GitIgnore) ([]fileInfo, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("abs root: %w", err)
	}

	// If no paths specified, use root
	if len(paths) == 0 {
		paths = []string{absRoot}
	}

	var files []fileInfo
	var mu sync.Mutex

	for _, path := range paths {
		absPath := path
		if !filepath.IsAbs(path) {
			absPath = filepath.Join(absRoot, path)
		}

		err := filepath.WalkDir(absPath, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Get relative path for ignore matching
			relPath, err := filepath.Rel(absRoot, p)
			if err != nil {
				relPath = p
			}

			// Check if should be ignored
			if ignore.MatchesPath(relPath) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			if d.IsDir() {
				return nil
			}

			// Check file size. WalkDir does not follow symlinks: preserve
			// symlink-to-file indexing, but do not pass a symlink-to-directory
			// to ReadFile (which would fail with EISDIR).
			info, skip, err := indexableFileInfo(p, d)
			if err != nil {
				return err
			}
			if skip {
				return nil
			}

			if info.Size() > idx.config.MaxFileSize {
				return nil
			}

			// Calculate file hash and read content in one pass
			hash, content, err := hashFile(p)
			if err != nil {
				return err
			}

			mu.Lock()
			files = append(files, fileInfo{
				path:         p,
				relativePath: relPath,
				hash:         hash,
				sourceHash:   hash,
				size:         info.Size(),
				content:      content,
			})
			mu.Unlock()

			return nil
		})

		if err != nil && err != context.Canceled {
			return nil, fmt.Errorf("walk %s: %w", path, err)
		}
	}

	return files, nil
}

// indexableFileInfo returns the effective file information for one non-dir
// WalkDir entry. WalkDir deliberately does not follow symlinks, while vecgrep
// has historically indexed symlinks to regular files via os.ReadFile. Follow
// only for classification/size: non-regular entries (directories, FIFOs,
// sockets, and devices) plus dangling links are skipped, regular-file targets
// remain indexable, and other stat failures stay visible rather than silently
// certifying an incomplete scan.
func indexableFileInfo(path string, entry fs.DirEntry) (info fs.FileInfo, skip bool, err error) {
	info, err = entry.Info()
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		info, err = os.Stat(path)
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
	return info, !info.Mode().IsRegular(), nil
}

// prepareRequiredStructuralFiles materializes the requested filesystem view and
// validates every selected structural file before any index mutation. The
// returned content is reused by the pipeline, so a subsequent edit is reported
// as pending on the next scan rather than causing a silent local-chunker
// downgrade during this run.
func (idx *Indexer) prepareRequiredStructuralFiles(ctx context.Context, rootPath string, paths []string, structural *StructuralChunkSet) ([]fileInfo, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	ignoreMatcher, err := idx.buildIgnoreMatcher(rootPath)
	if err != nil {
		return nil, fmt.Errorf("build ignore matcher: %w", err)
	}
	files, err := idx.collectFiles(ctx, rootPath, paths, ignoreMatcher)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]int, len(files))
	for i := range files {
		byPath[files[i].relativePath] = i
	}
	for relPath, structuralFile := range structural.Files {
		if !structuralPathSelected(absRoot, relPath, paths) || ignoreMatcher.MatchesPath(relPath) || structuralFile.FileSize > idx.config.MaxFileSize {
			continue
		}
		position, ok := byPath[relPath]
		if !ok {
			return nil, fmt.Errorf("required codemap structural source disappeared before indexing: %s", relPath)
		}
		if files[position].sourceHash != structuralFile.FileHash {
			return nil, fmt.Errorf("required codemap structural source changed before indexing: %s", relPath)
		}
		files[position].hash = structuralIndexHash(files[position].sourceHash, structuralFile)
	}
	return files, nil
}

func structuralPathSelected(absRoot, relativePath string, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	filePath := filepath.Join(absRoot, relativePath)
	for _, selected := range paths {
		if !filepath.IsAbs(selected) {
			selected = filepath.Join(absRoot, selected)
		}
		selected, err := filepath.Abs(selected)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(selected, filePath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func feedPreparedFiles(ctx context.Context, files []fileInfo, existingHashes map[string]string, scan *fullScanState, fileChan chan<- fileInfo, totalDiscovered, skippedCount *int64) error {
	for _, file := range files {
		if scan != nil {
			scan.observe(file.relativePath)
		}
		if existingHash, exists := existingHashes[file.relativePath]; exists && existingHash == file.hash {
			atomic.AddInt64(skippedCount, 1)
			continue
		}
		file.queueBytes = 0 // content is already retained by the preflight slice
		select {
		case fileChan <- file:
			atomic.AddInt64(totalDiscovered, 1)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// walkAndFilter walks the file tree and streams fileInfo structs that need
// indexing to fileChan. It reserves source bytes from the file's stat size
// before reading, then reconciles the reservation with the actual read size.
// The incremental hash filter is applied inline (per file) so unchanged files
// are skipped without being sent to the worker pool. totalDiscovered and
// skippedCount are updated atomically for progress reporting. The caller is
// responsible for closing fileChan after this returns.
func (idx *Indexer) walkAndFilter(
	ctx context.Context,
	rootPath, absRoot string,
	paths []string,
	ignore *gitignore.GitIgnore,
	existingHashes map[string]string,
	sourceBudget sourceByteBudget,
	sourceBufferBytes int64,
	fileChan chan<- fileInfo,
	totalDiscovered *int64,
	skippedCount *int64,
) error {
	return idx.walkAndFilterStructural(ctx, rootPath, absRoot, paths, ignore, existingHashes, nil, nil, sourceBudget, sourceBufferBytes, fileChan, totalDiscovered, skippedCount)
}

func (idx *Indexer) walkAndFilterStructural(
	ctx context.Context,
	rootPath, absRoot string,
	paths []string,
	ignore *gitignore.GitIgnore,
	existingHashes map[string]string,
	structural *StructuralChunkSet,
	scan *fullScanState,
	sourceBudget sourceByteBudget,
	sourceBufferBytes int64,
	fileChan chan<- fileInfo,
	totalDiscovered *int64,
	skippedCount *int64,
) error {
	// If no paths specified, use root
	if len(paths) == 0 {
		paths = []string{absRoot}
	}

	for _, path := range paths {
		absPath := path
		if !filepath.IsAbs(path) {
			absPath = filepath.Join(absRoot, path)
		}

		err := filepath.WalkDir(absPath, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if scan != nil {
					scan.invalidate()
				}
				return err
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			relPath, err := filepath.Rel(absRoot, p)
			if err != nil {
				if scan != nil {
					scan.invalidate()
				}
				return err
			}

			if ignore.MatchesPath(relPath) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			if d.IsDir() {
				return nil
			}

			info, skip, err := indexableFileInfo(p, d)
			if err != nil {
				if scan != nil {
					scan.invalidate()
				}
				return err
			}
			if skip {
				return nil
			}

			if info.Size() > idx.config.MaxFileSize {
				return nil
			}
			if scan != nil {
				scan.observe(relPath)
			}

			// Reserve before reading so a walker blocked behind the queue cannot
			// retain an uncharged file. The stat is only an estimate: a file can
			// change between Info and ReadFile, so reconcile against actual bytes.
			reservedBytes := min(info.Size(), sourceBufferBytes)
			if err := sourceBudget.Acquire(ctx, reservedBytes); err != nil {
				return err
			}

			hash, content, err := hashFile(p)
			if err != nil {
				if scan != nil {
					scan.invalidate()
				}
				sourceBudget.Release(reservedBytes)
				return err
			}

			actualSize := int64(len(content))
			if actualSize > idx.config.MaxFileSize {
				if scan != nil {
					scan.forget(relPath)
				}
				sourceBudget.Release(reservedBytes)
				return nil
			}
			actualBytes := min(actualSize, sourceBufferBytes)
			switch {
			case actualBytes > reservedBytes:
				if err := sourceBudget.Acquire(ctx, actualBytes-reservedBytes); err != nil {
					sourceBudget.Release(reservedBytes)
					return err
				}
			case actualBytes < reservedBytes:
				sourceBudget.Release(reservedBytes - actualBytes)
			}
			reservedBytes = actualBytes
			sourceHash := hash
			if structuralFile, ok := structuralFileForHash(structural, relPath, sourceHash); ok {
				hash = structuralIndexHash(sourceHash, structuralFile)
			}

			// Incremental filter: skip unchanged files inline so the
			// worker pool never sees them.
			if existingHash, exists := existingHashes[relPath]; exists && existingHash == hash {
				sourceBudget.Release(reservedBytes)
				atomic.AddInt64(skippedCount, 1)
				return nil
			}

			fi := fileInfo{
				path:         p,
				relativePath: relPath,
				hash:         hash,
				sourceHash:   sourceHash,
				size:         actualSize,
				content:      content,
				queueBytes:   reservedBytes,
			}

			select {
			case fileChan <- fi:
				atomic.AddInt64(totalDiscovered, 1)
			case <-ctx.Done():
				sourceBudget.Release(reservedBytes)
				return ctx.Err()
			}

			return nil
		})

		if err != nil && err != context.Canceled {
			if scan != nil {
				scan.invalidate()
			}
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if err == context.Canceled {
			if scan != nil {
				scan.invalidate()
			}
			return err
		}
	}

	return nil
}

// fullScanState tracks whether a root-wide traversal observed a complete
// filesystem view. It is written only by the walker goroutine and read after
// that goroutine reports completion through walkErrCh.
type fullScanState struct {
	seen     map[string]struct{}
	complete bool
}

func newFullScanState() *fullScanState {
	return &fullScanState{seen: make(map[string]struct{}), complete: true}
}

func (s *fullScanState) observe(path string) {
	if s != nil {
		s.seen[path] = struct{}{}
	}
}

func (s *fullScanState) forget(path string) {
	if s != nil {
		delete(s.seen, path)
	}
}

func (s *fullScanState) invalidate() {
	if s != nil {
		s.complete = false
	}
}

func (idx *Indexer) pruneDeletedFiles(ctx context.Context, projectRoot string, existingHashes map[string]string, seen map[string]struct{}) (int, []error) {
	stale := make([]string, 0)
	for path := range existingHashes {
		if _, exists := seen[path]; !exists {
			stale = append(stale, path)
		}
	}
	slices.Sort(stale)

	deleted := 0
	var errs []error
	for _, path := range stale {
		if _, err := idx.deleteFile(ctx, projectRoot, path); err != nil {
			errs = append(errs, fmt.Errorf("prune deleted file %s: %w", path, err))
			continue
		}
		deleted++
	}
	return deleted, errs
}

// hashFile calculates the SHA256 hash of a file and returns both the
// hex-encoded hash and the file content. Reading the full content once
// avoids a second disk read in chunkFile.
func hashFile(path string) (string, []byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]), content, nil
}

// PendingChanges holds counts of files needing reindexing.
type PendingChanges struct {
	NewFiles      int `json:"new_files"`
	ModifiedFiles int `json:"modified_files"`
	DeletedFiles  int `json:"deleted_files"`
	TotalPending  int `json:"total_pending"`
}

// GetPendingChanges scans the project and returns counts of files needing reindexing.
func (idx *Indexer) GetPendingChanges(ctx context.Context, projectRoot string) (*PendingChanges, error) {
	structuralConfig := idx.SnapshotStructuralChunkSource()
	structural, _, err := idx.loadStructuralChunksWithSnapshot(ctx, projectRoot, structuralConfig)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}

	// Get existing file hashes from veclite
	indexedFiles, err := idx.db.GetFileHashes(absPath)
	if err != nil {
		// No indexed files yet
		return &PendingChanges{}, nil
	}

	// Build ignore matcher
	ignoreMatcher, err := idx.buildIgnoreMatcher(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("build ignore matcher: %w", err)
	}

	// Collect current files from filesystem. Required mode uses the same strict
	// preflight as indexing, so status never reports a local-chunker downgrade as
	// an acceptable structural snapshot.
	var currentFiles []fileInfo
	if structuralConfig.required && structural != nil && len(structural.Files) > 0 {
		currentFiles, err = idx.prepareRequiredStructuralFiles(ctx, projectRoot, nil, structural)
	} else {
		currentFiles, err = idx.collectFiles(ctx, projectRoot, nil, ignoreMatcher)
		applyStructuralFileHashes(currentFiles, structural)
	}
	if err != nil {
		return nil, fmt.Errorf("collect files: %w", err)
	}

	// Build a map of current files for quick lookup
	currentFileMap := make(map[string]fileInfo)
	for _, f := range currentFiles {
		currentFileMap[f.relativePath] = f
	}

	pending := &PendingChanges{}

	// Count new and modified files
	for relPath, currentFile := range currentFileMap {
		indexedHash, exists := indexedFiles[relPath]
		if !exists {
			pending.NewFiles++
		} else if indexedHash != currentFile.hash {
			pending.ModifiedFiles++
		}
	}

	// Count deleted files (in index but not on disk)
	for relPath := range indexedFiles {
		if _, exists := currentFileMap[relPath]; !exists {
			pending.DeletedFiles++
		}
	}

	pending.TotalPending = pending.NewFiles + pending.ModifiedFiles + pending.DeletedFiles

	return pending, nil
}

// DryRunPreview scans the project and returns counts of files needing
// reindexing plus an estimated chunk count, without calling the embedding
// provider. It reuses collectFiles and runs the chunker over each changed
// file to estimate how many chunks would be created.
type DryRunPreview struct {
	NewFiles        int
	ModifiedFiles   int
	DeletedFiles    int
	TotalPending    int
	EstimatedChunks int
	FilesToEmbed    int
}

// DryRunPreview scans the project and returns counts of files needing
// reindexing plus an estimated chunk count, without calling the embedding
// provider.
func (idx *Indexer) DryRunPreview(ctx context.Context, projectRoot string) (*DryRunPreview, error) {
	structuralConfig := idx.SnapshotStructuralChunkSource()
	structural, _, err := idx.loadStructuralChunksWithSnapshot(ctx, projectRoot, structuralConfig)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}

	// Get existing file hashes from veclite
	indexedFiles, err := idx.db.GetFileHashes(absPath)
	if err != nil {
		indexedFiles = map[string]string{}
	}

	// Build ignore matcher
	ignoreMatcher, err := idx.buildIgnoreMatcher(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("build ignore matcher: %w", err)
	}

	// Collect current files from filesystem, with the same no-downgrade
	// preflight used by a real required-mode index run.
	var currentFiles []fileInfo
	if structuralConfig.required && structural != nil && len(structural.Files) > 0 {
		currentFiles, err = idx.prepareRequiredStructuralFiles(ctx, projectRoot, nil, structural)
	} else {
		currentFiles, err = idx.collectFiles(ctx, projectRoot, nil, ignoreMatcher)
		applyStructuralFileHashes(currentFiles, structural)
	}
	if err != nil {
		return nil, fmt.Errorf("collect files: %w", err)
	}

	// Build a map of current files for quick lookup
	currentFileMap := make(map[string]fileInfo)
	for _, f := range currentFiles {
		currentFileMap[f.relativePath] = f
	}

	preview := &DryRunPreview{}

	// Count new and modified files, and estimate chunks for changed files
	for relPath, currentFile := range currentFileMap {
		indexedHash, exists := indexedFiles[relPath]
		if !exists {
			preview.NewFiles++
		} else if indexedHash != currentFile.hash {
			preview.ModifiedFiles++
		} else {
			continue // unchanged
		}

		// Estimate chunks for this file
		if currentFile.content != nil && IsTextFile(currentFile.content) {
			chunks := idx.chunker.ChunkFile(string(currentFile.content), currentFile.path)
			if structuralFile, ok := structuralFileForHash(structural, relPath, currentFile.sourceHash); ok {
				chunks = structuralFile.Chunks
			}
			preview.EstimatedChunks += len(chunks)
		}
	}

	// Count deleted files (in index but not on disk)
	for relPath := range indexedFiles {
		if _, exists := currentFileMap[relPath]; !exists {
			preview.DeletedFiles++
		}
	}

	preview.TotalPending = preview.NewFiles + preview.ModifiedFiles + preview.DeletedFiles
	preview.FilesToEmbed = preview.NewFiles + preview.ModifiedFiles
	return preview, nil
}

func applyStructuralFileHashes(files []fileInfo, structural *StructuralChunkSet) {
	for i := range files {
		if structuralFile, ok := structuralFileForHash(structural, files[i].relativePath, files[i].sourceHash); ok {
			files[i].hash = structuralIndexHash(files[i].sourceHash, structuralFile)
		}
	}
}

// ReindexAll forces reindexing of all files in the project.
func (idx *Indexer) ReindexAll(ctx context.Context, projectRoot string) (result *IndexResult, runErr error) {
	startedAt := time.Now()
	// Resolve the external snapshot before Reset so required mode can fail
	// without destroying the last usable vecgrep index.
	structuralConfig := idx.SnapshotStructuralChunkSource()
	report := IndexRunReport{
		AttemptID:            idx.indexRunAttemptID(),
		ScopeComplete:        true,
		ProjectRoot:          projectRoot,
		StartedAt:            startedAt,
		StructuralConfigured: structuralConfig.source != nil,
		StructuralRequired:   structuralConfig.required,
	}
	defer func() {
		report.FinishedAt = time.Now()
		report.Result = result
		report.Err = runErr
		if runErr != nil && report.FailureStage == "" {
			report.FailureStage = "index"
		}
		if err := idx.observeIndexRun(report); err != nil {
			receiptErr := fmt.Errorf("record ingestion receipt: %w", err)
			if result != nil {
				result.Errors = append(result.Errors, receiptErr)
			}
			runErr = errors.Join(runErr, receiptErr)
		}
	}()
	structural, warning, err := idx.loadStructuralChunksWithSnapshot(ctx, projectRoot, structuralConfig)
	if err != nil {
		report.FailureStage = "structural_load"
		return nil, err
	}
	populateStructuralRunReport(&report, structural, warning)
	var prepared *[]fileInfo
	if structuralConfig.required && structural != nil && len(structural.Files) > 0 {
		files, prepareErr := idx.prepareRequiredStructuralFiles(ctx, projectRoot, nil, structural)
		if prepareErr != nil {
			report.FailureStage = "structural_preflight"
			return nil, prepareErr
		}
		prepared = &files
	}
	absPath, err := filepath.Abs(projectRoot)
	if err != nil {
		report.FailureStage = "project_root"
		return nil, fmt.Errorf("abs path: %w", err)
	}

	// Delete all existing data for this project
	if err := idx.db.Reset(ctx, absPath); err != nil {
		report.FailureStage = "storage_reset"
		return nil, fmt.Errorf("reset project: %w", err)
	}

	// Perform full index without redundant per-file deletion after Reset.
	if prepared != nil {
		return idx.indexPrepared(ctx, projectRoot, false, structural, warning, prepared)
	}
	return idx.index(ctx, projectRoot, false, structural, warning)
}
