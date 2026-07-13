package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// IndexDBLease is one exclusive writable database lease. The coordinator
// borrows it for one run and always releases it; ownership remains with the
// source (a long-lived Session or MCP's on-demand write handle).
type IndexDBLease struct {
	DB      *db.DB
	Release func() error
}

// IndexDBSource supplies exclusive writable handles without coupling app to a
// transport. CLI/Studio/daemon use a fixed session DB; MCP opens one per call.
type IndexDBSource interface {
	AcquireIndexDB(context.Context) (IndexDBLease, error)
}

type fixedIndexDBSource struct{ database *db.DB }

func (s fixedIndexDBSource) AcquireIndexDB(context.Context) (IndexDBLease, error) {
	if s.database == nil {
		return IndexDBLease{}, fmt.Errorf("index database is nil")
	}
	return IndexDBLease{DB: s.database, Release: func() error { return nil }}, nil
}

// IndexCoordinator is the single application-owned indexing protocol shared
// by CLI, Studio, daemon, watcher, and MCP. It borrows DB/provider resources,
// serializes runs, and owns every readiness step around the run-scoped Indexer.
type IndexCoordinator struct {
	projectRoot string
	cfg         *config.Config
	provider    embed.Provider
	stores      IndexDBSource

	runMu       sync.Mutex
	restoreOnce sync.Once
}

func NewIndexCoordinator(projectRoot string, cfg *config.Config, provider embed.Provider, stores IndexDBSource) *IndexCoordinator {
	return &IndexCoordinator{
		projectRoot: projectRoot,
		cfg:         cfg,
		provider:    provider,
		stores:      stores,
	}
}

func newSessionIndexCoordinator(session *Session) *IndexCoordinator {
	if session == nil {
		return nil
	}
	return NewIndexCoordinator(session.ProjectRoot, session.Config, session.Provider, fixedIndexDBSource{database: session.DB})
}

// Index runs one full application lifecycle. The Indexer is intentionally
// constructed per run because progress, structural policy, and observers are
// mutable run state.
func (c *IndexCoordinator) Index(ctx context.Context, req IndexRequest, progress func(index.Progress)) (*index.IndexResult, error) {
	if c == nil {
		return nil, fmt.Errorf("index coordinator is nil")
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	return c.indexLocked(ctx, req, progress)
}

func (c *IndexCoordinator) indexLocked(ctx context.Context, req IndexRequest, progress func(index.Progress)) (result *index.IndexResult, retErr error) {
	if c.cfg == nil || c.stores == nil {
		return nil, fmt.Errorf("index coordinator is not configured")
	}
	if c.provider == nil {
		return nil, ErrProviderRequired
	}
	if err := c.provider.Ping(ctx); err != nil {
		return nil, fmt.Errorf("embedding provider unavailable: %w", err)
	}

	// Cache restoration is best-effort and only useful once per long-lived
	// runtime. Warmup is retried per run because a transient model unload should
	// not permanently disable it for a daemon.
	c.restoreOnce.Do(func() {
		borrowed := c.borrowedService(nil)
		borrowed.maybeRestoreEmbeddingCache(ctx)
	})
	log.Printf("warming up embedding model")
	if loadDur, err := c.provider.Warmup(ctx); err != nil {
		log.Printf("model warmup skipped: %v", err)
	} else {
		log.Printf("model warmup complete (load_duration: %dms)", loadDur.Milliseconds())
	}

	database, release, err := c.acquireIndexDB(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := release(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release index database: %w", err))
		}
	}()

	service := c.borrowedService(database)
	indexer, err := NewConfiguredIndexer(database, c.provider, c.cfg, req.AdditionalIgnores, req.StructuralChunks)
	if err != nil {
		return nil, err
	}
	requestedMode, err := requestedStructuralMode(c.cfg, req.StructuralChunks)
	if err != nil {
		return nil, err
	}
	// A project_dirty tombstone is durable evidence of an interrupted
	// multi-collection mutation. Do not let an incremental run appear to repair
	// it: only ReindexAll resets the project and can clear the marker. Requiring
	// FullReindex here also avoids silently widening a path-scoped request into a
	// destructive project rebuild.
	if !req.FullReindex {
		absRoot, absErr := filepath.Abs(c.projectRoot)
		if absErr != nil {
			return nil, fmt.Errorf("resolve project root for hash preflight: %w", absErr)
		}
		if _, hashErr := database.GetFileHashes(absRoot); errors.Is(hashErr, db.ErrProjectFileHashesDirty) {
			return nil, hashErr
		}
	}
	attemptID, err := newIngestionAttemptID()
	if err != nil {
		return nil, err
	}
	scopeComplete := req.FullReindex || len(req.Paths) == 0
	// Poison the previous success before ensureEmbeddingProfileForIndex or the
	// indexer can mutate collection metadata/chunks. Failure here is a hard
	// preflight error and leaves the old searchable index untouched.
	if err := beginIngestionReceiptAttempt(c.cfg.DataDir, c.projectRoot, attemptID, requestedMode, scopeComplete); err != nil {
		return nil, fmt.Errorf("begin ingestion receipt: %w", err)
	}
	indexer.SetIndexRunAttemptID(attemptID)
	if err := service.ensureEmbeddingProfileForIndex(req.FullReindex); err != nil {
		finalizeErr := finalizeIngestionReceiptAttempt(c.cfg.DataDir, c.projectRoot, attemptID, err)
		return nil, errors.Join(err, finalizeErr)
	}
	if progress != nil {
		indexer.SetProgressCallback(progress)
	}

	var indexErr error
	if req.FullReindex {
		result, indexErr = indexer.ReindexAll(ctx, c.projectRoot)
	} else {
		result, indexErr = indexer.Index(ctx, c.projectRoot, req.Paths...)
	}
	flushErr := flushProvider(c.provider)
	if indexErr != nil {
		if flushErr != nil {
			flushErr = fmt.Errorf("flush embedding provider: %w", flushErr)
		}
		runErr := errors.Join(indexErr, flushErr)
		finalizeErr := finalizeIngestionReceiptAttempt(c.cfg.DataDir, c.projectRoot, attemptID, runErr)
		if finalizeErr != nil {
			finalizeErr = errors.Join(finalizeErr, invalidateIngestionReceiptAttempt(c.cfg.DataDir, c.projectRoot, attemptID))
		}
		if releaseErr := release(); releaseErr != nil {
			releaseErr = fmt.Errorf("release index database: %w", releaseErr)
			return nil, errors.Join(runErr, finalizeErr, releaseErr)
		}
		return nil, errors.Join(runErr, finalizeErr)
	}

	var postErr error
	if flushErr != nil {
		postErr = fmt.Errorf("flush embedding provider: %w", flushErr)
	}
	if postErr == nil {
		if err := service.saveCurrentEmbeddingProfile(); err != nil {
			postErr = err
		}
	}
	// The indexer syncs chunk/hash mutations. This second sync publishes the
	// embedding profile before the receipt is allowed to advance last_success.
	if postErr == nil {
		if err := database.Sync(); err != nil {
			postErr = fmt.Errorf("sync index postflight: %w", err)
		}
	}
	// Finalize while the exclusive DB lease is still held. Another process
	// cannot publish a newer attempt between observer and finalization, and the
	// attempt token makes any unexpected replacement fail closed.
	finalizeErr := finalizeIngestionReceiptAttempt(c.cfg.DataDir, c.projectRoot, attemptID, postErr)
	var invalidateErr error
	if finalizeErr != nil {
		invalidateErr = invalidateIngestionReceiptAttempt(c.cfg.DataDir, c.projectRoot, attemptID)
	}
	releaseErr := release()
	if releaseErr != nil {
		releaseErr = fmt.Errorf("release index database: %w", releaseErr)
		// A close/release failure occurs after success could have been published.
		// Revoke that exact attempt; never touch a newer token.
		invalidateErr = errors.Join(invalidateErr, invalidateIngestionReceiptAttempt(c.cfg.DataDir, c.projectRoot, attemptID))
	}
	if postErr != nil {
		return nil, errors.Join(postErr, finalizeErr, releaseErr, invalidateErr)
	}
	if finalizeErr != nil || releaseErr != nil {
		return nil, errors.Join(finalizeErr, releaseErr, invalidateErr)
	}

	service.maybeStashEmbeddingCache(ctx)
	return result, nil
}

func (c *IndexCoordinator) acquireIndexDB(ctx context.Context) (*db.DB, func() error, error) {
	if c == nil || c.stores == nil {
		return nil, nil, fmt.Errorf("index coordinator is not configured")
	}
	lease, err := c.stores.AcquireIndexDB(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquire index database: %w", err)
	}

	var once sync.Once
	release := func() (releaseErr error) {
		once.Do(func() {
			if lease.Release != nil {
				releaseErr = lease.Release()
			}
		})
		return releaseErr
	}
	if lease.DB == nil {
		nilErr := fmt.Errorf("acquire index database: nil database")
		if err := release(); err != nil {
			nilErr = errors.Join(nilErr, fmt.Errorf("release index database: %w", err))
		}
		return nil, nil, nilErr
	}
	return lease.DB, release, nil
}

func flushProvider(provider embed.Provider) error {
	if flusher, ok := provider.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func requestedStructuralMode(cfg *config.Config, override string) (StructuralChunksMode, error) {
	configured := ""
	if cfg != nil {
		configured = cfg.Codemap.StructuralChunks
	}
	if override != "" {
		configured = override
	}
	return ParseStructuralChunksMode(configured)
}

func (c *IndexCoordinator) borrowedService(database *db.DB) *Service {
	return &Service{session: &Session{
		ProjectRoot: c.projectRoot,
		Config:      c.cfg,
		DB:          database,
		Provider:    c.provider,
	}}
}

// ApplyWatchEvents applies one debounced filesystem batch under the same run
// lock as explicit indexing. Writes/creates are indexed first; removals and
// renames are then deleted project-scoped and synced once.
func (c *IndexCoordinator) ApplyWatchEvents(ctx context.Context, events []index.WatchEvent) (*index.IndexResult, error) {
	if c == nil {
		return nil, fmt.Errorf("index coordinator is nil")
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	started := time.Now()

	toIndex := make(map[string]struct{})
	toRemove := make(map[string]struct{})
	for _, event := range events {
		switch event.Op {
		case index.OpCreate, index.OpWrite:
			toIndex[event.Path] = struct{}{}
			delete(toRemove, event.Path)
		case index.OpRemove, index.OpRename:
			toRemove[event.Path] = struct{}{}
			delete(toIndex, event.Path)
		}
	}

	result := &index.IndexResult{}
	var runErr error
	if len(toIndex) > 0 {
		paths := sortedSet(toIndex)
		indexed, err := c.indexLocked(ctx, IndexRequest{Paths: paths}, nil)
		if indexed != nil {
			result = indexed
		}
		runErr = errors.Join(runErr, err)
	}
	if len(toRemove) > 0 {
		deleted, err := c.deletePathsLocked(ctx, sortedSet(toRemove))
		result.FilesDeleted += deleted
		runErr = errors.Join(runErr, err)
	}
	result.Duration = time.Since(started)
	return result, runErr
}

func (c *IndexCoordinator) deletePathsLocked(ctx context.Context, paths []string) (deletedFiles int, retErr error) {
	database, release, err := c.acquireIndexDB(ctx)
	if err != nil {
		return 0, err
	}
	defer func() {
		if err := release(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("release index database: %w", err))
		}
	}()
	requestedMode, err := requestedStructuralMode(c.cfg, "")
	if err != nil {
		return 0, err
	}
	attemptID, err := newIngestionAttemptID()
	if err != nil {
		return 0, err
	}
	// A watch delete is path-scoped, so it invalidates the prior global proof
	// before touching hashes/chunks and intentionally remains incomplete until a
	// full-project scan can certify the resulting index.
	if err := beginIngestionReceiptAttempt(c.cfg.DataDir, c.projectRoot, attemptID, requestedMode, false); err != nil {
		return 0, fmt.Errorf("begin watched-delete receipt: %w", err)
	}

	var errs []error
	for _, path := range paths {
		rel := path
		if filepath.IsAbs(path) {
			if relative, relErr := filepath.Rel(c.projectRoot, path); relErr == nil {
				rel = relative
			}
		}
		rel = filepath.Clean(rel)
		if _, err := database.DeleteProjectFile(ctx, c.projectRoot, rel); err != nil {
			errs = append(errs, fmt.Errorf("delete watched file %s: %w", rel, err))
			continue
		}
		deletedFiles++
	}
	if err := database.Sync(); err != nil {
		errs = append(errs, fmt.Errorf("sync watched deletes: %w", err))
	}
	return deletedFiles, errors.Join(errs...)
}

func sortedSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}
