package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

// Default incremental-sync thresholds: flush the vector DB to disk after
// processing this many files, or after this much elapsed time, whichever
// comes first. A final sync always runs at the end.
const (
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
}

// NewIndexer creates a new Indexer.
func NewIndexer(database *db.DB, provider embed.Provider, cfg IndexerConfig) *Indexer {
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultIndexerConfig().BatchSize
	}
	if cfg.Workers == 0 {
		cfg.Workers = DefaultIndexerConfig().Workers
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

// IndexResult contains the results of an indexing operation.
type IndexResult struct {
	FilesProcessed int
	FilesSkipped   int
	ChunksCreated  int
	Duration       time.Duration
	Errors         []error
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
	// filtering. If this fails, index everything (no filter).
	existingHashes, err := idx.db.GetFileHashes(absRoot)
	if err != nil {
		existingHashes = map[string]string{}
	}

	batchSize := idx.config.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultIndexerConfig().BatchSize
	}

	// Pipeline channels. itemChan/batchChan are sized so the stages stay busy
	// without unbounded buffering: backpressure from a slow embedder propagates
	// up through the batcher to the chunk workers and the walker.
	fileChan := make(chan fileInfo, 100)
	itemChan := make(chan embedItem, batchSize*2)
	batchChan := make(chan []embedItem, idx.config.Workers)
	resultsChan := make(chan fileResult, idx.config.Workers)

	// Shared counters for progress reporting, updated by the walker and
	// read by the results collector.
	var totalDiscovered int64 // files queued for indexing
	var skippedCount int64    // files skipped (unchanged)

	// Stage 1: chunk workers. Read + chunk each changed file and emit one item
	// per chunk; files needing no embedding (binary / empty) report immediately.
	var chunkWG sync.WaitGroup
	for i := 0; i < idx.config.Workers; i++ {
		chunkWG.Add(1)
		go func() {
			defer chunkWG.Done()
			idx.chunkWorker(ctx, absRoot, fileChan, itemChan, resultsChan)
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
		err := idx.walkAndFilter(ctx, projectRoot, absRoot, paths, ignoreMatcher, existingHashes, fileChan, &totalDiscovered, &skippedCount)
		close(fileChan)
		walkErrCh <- err
	}()

	result := &IndexResult{}
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

	for r := range resultsChan {
		result.FilesProcessed++
		result.ChunksCreated += r.chunksCreated

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
			if err := idx.db.Sync(); err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("incremental sync: %w", err))
			} else {
				lastSyncCount = result.FilesProcessed
				lastSyncTime = time.Now()
			}
		}
	}

	// Surface walk errors (context cancel is expected on cancellation).
	if walkErr := <-walkErrCh; walkErr != nil && walkErr != context.Canceled {
		result.Errors = append(result.Errors, fmt.Errorf("walk: %w", walkErr))
	}

	result.FilesSkipped = int(atomic.LoadInt64(&skippedCount))

	// Final sync to persist everything.
	if err := idx.db.Sync(); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("sync: %w", err))
	}

	result.Duration = time.Since(startTime)
	return result, nil
}

// fileInfo holds information about a file to be indexed. The content
// field caches the file bytes read during hashing so that chunkFile can
// reuse them without a second disk read. The content is nil'd after
// chunking to avoid holding all file contents in memory simultaneously.
type fileInfo struct {
	path         string
	relativePath string
	hash         string
	size         int64
	content      []byte
}

// fileResult holds the result of indexing a single file.
type fileResult struct {
	path          string
	chunksCreated int
	err           error
}

// fileTask tracks one file's chunks as they are embedded across (potentially
// several) shared batches. Its chunks may be scattered among batches that also
// carry other files' chunks, so completion is reference-counted: the goroutine
// that drives remaining to zero owns inserting the file and reporting it.
type fileTask struct {
	path    string
	records []db.ChunkRecord // one per chunk, in chunk order
	embeds  [][]float32      // filled in by slot as batches complete

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

// chunkWorker reads and chunks files from fileChan, emitting one embedItem per
// chunk. Files that need no embedding report completion directly.
func (idx *Indexer) chunkWorker(ctx context.Context, projectRoot string, files <-chan fileInfo, items chan<- embedItem, results chan<- fileResult) {
	for file := range files {
		select {
		case <-ctx.Done():
			results <- fileResult{path: file.path, err: ctx.Err()}
			return
		default:
		}
		idx.chunkFile(ctx, projectRoot, file, items, results)
	}
}

// chunkFile reads a file (reusing the content cached during hashing), drops its
// stale chunks, splits it, and hands each chunk to the embed pipeline. Binary
// and empty files report immediately; everything else completes asynchronously
// once its chunks are embedded and inserted.
func (idx *Indexer) chunkFile(ctx context.Context, projectRoot string, file fileInfo, items chan<- embedItem, results chan<- fileResult) {
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

	if !IsTextFile(content) {
		results <- fileResult{path: file.path} // binary: skip silently
		return
	}

	lang := DetectLanguage(file.path)

	// Drop existing chunks for this file before re-inserting (re-index).
	_, _ = idx.db.DeleteFile(ctx, file.relativePath)

	chunks := idx.chunker.ChunkFile(string(content), file.path)
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

	// Pre-build the records; embeddings are filled in as batches complete.
	records := make([]db.ChunkRecord, len(chunks))
	for i, chunk := range chunks {
		records[i] = db.NewChunkRecord(
			file.path, file.relativePath, file.hash, file.size, string(lang),
			chunk.Content, chunk.StartLine, chunk.EndLine, chunk.StartByte, chunk.EndByte,
			string(chunk.ChunkType), chunk.SymbolName, projectRoot,
		)
	}
	task := &fileTask{
		path:      file.path,
		records:   records,
		embeds:    make([][]float32, len(chunks)),
		remaining: len(chunks),
	}

	for i, chunk := range chunks {
		select {
		case items <- embedItem{task: task, slot: i, text: chunk.Content}:
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
				return nil // Skip files with errors
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

			// Check file size
			info, err := d.Info()
			if err != nil {
				return nil
			}

			if info.Size() > idx.config.MaxFileSize {
				return nil
			}

			// Calculate file hash and read content in one pass
			hash, content, err := hashFile(p)
			if err != nil {
				return nil
			}

			mu.Lock()
			files = append(files, fileInfo{
				path:         p,
				relativePath: relPath,
				hash:         hash,
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

// walkAndFilter walks the file tree and streams fileInfo structs that need
// indexing to fileChan. The incremental hash filter is applied inline (per
// file) so unchanged files are skipped without being sent to the worker
// pool. totalDiscovered and skippedCount are updated atomically for
// progress reporting. The caller is responsible for closing fileChan after
// this returns.
func (idx *Indexer) walkAndFilter(
	ctx context.Context,
	rootPath, absRoot string,
	paths []string,
	ignore *gitignore.GitIgnore,
	existingHashes map[string]string,
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
				return nil // Skip files with errors
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			relPath, err := filepath.Rel(absRoot, p)
			if err != nil {
				relPath = p
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

			info, err := d.Info()
			if err != nil {
				return nil
			}

			if info.Size() > idx.config.MaxFileSize {
				return nil
			}

			hash, content, err := hashFile(p)
			if err != nil {
				return nil
			}

			// Incremental filter: skip unchanged files inline so the
			// worker pool never sees them.
			if existingHash, exists := existingHashes[relPath]; exists && existingHash == hash {
				atomic.AddInt64(skippedCount, 1)
				return nil
			}

			fi := fileInfo{
				path:         p,
				relativePath: relPath,
				hash:         hash,
				size:         info.Size(),
				content:      content,
			}

			atomic.AddInt64(totalDiscovered, 1)

			select {
			case fileChan <- fi:
			case <-ctx.Done():
				return ctx.Err()
			}

			return nil
		})

		if err != nil && err != context.Canceled {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if err == context.Canceled {
			return err
		}
	}

	return nil
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
	NewFiles      int
	ModifiedFiles int
	DeletedFiles  int
	TotalPending  int
}

// GetPendingChanges scans the project and returns counts of files needing reindexing.
func (idx *Indexer) GetPendingChanges(ctx context.Context, projectRoot string) (*PendingChanges, error) {
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

	// Collect current files from filesystem
	currentFiles, err := idx.collectFiles(ctx, projectRoot, nil, ignoreMatcher)
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

	// Collect current files from filesystem
	currentFiles, err := idx.collectFiles(ctx, projectRoot, nil, ignoreMatcher)
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

// ReindexAll forces reindexing of all files in the project.
func (idx *Indexer) ReindexAll(ctx context.Context, projectRoot string) (*IndexResult, error) {
	absPath, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}

	// Delete all existing data for this project
	if err := idx.db.Reset(ctx, absPath); err != nil {
		return nil, fmt.Errorf("reset project: %w", err)
	}

	// Perform full index
	return idx.Index(ctx, projectRoot)
}
