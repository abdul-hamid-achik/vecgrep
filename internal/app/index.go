package app

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

type IndexRequest struct {
	Paths             []string
	FullReindex       bool
	AdditionalIgnores []string
}

type ResetScope string

const (
	ResetProject ResetScope = "project"
	ResetAll     ResetScope = "all"
)

func (s *Service) Index(ctx context.Context, req IndexRequest, progress func(index.Progress)) (*index.IndexResult, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("service not initialized")
	}
	if s.session.Provider == nil {
		return nil, ErrProviderRequired
	}
	if err := s.ensureEmbeddingProfileForIndex(req.FullReindex); err != nil {
		return nil, err
	}
	if err := s.session.Provider.Ping(ctx); err != nil {
		return nil, fmt.Errorf("embedding provider unavailable: %w", err)
	}

	// Restore the embedding cache from fcheap before indexing so unchanged
	// chunks don't need to be re-embedded. Best-effort: if no stash is found
	// or fcheap is unavailable, indexing proceeds normally.
	s.maybeRestoreEmbeddingCache(ctx)

	// Warm up the embedding model so the first batch doesn't pay
	// cold-start latency.
	log.Printf("warming up embedding model")
	if loadDur, err := s.session.Provider.Warmup(ctx); err != nil {
		log.Printf("model warmup skipped: %v", err)
	} else {
		log.Printf("model warmup complete (load_duration: %dms)", loadDur.Milliseconds())
	}

	indexer := index.NewIndexer(s.session.DB, s.session.Provider, s.indexerConfig(req.AdditionalIgnores))
	if progress != nil {
		indexer.SetProgressCallback(progress)
	}

	var result *index.IndexResult
	var indexErr error
	if req.FullReindex {
		result, indexErr = indexer.ReindexAll(ctx, s.session.ProjectRoot)
	} else {
		result, indexErr = indexer.Index(ctx, s.session.ProjectRoot, req.Paths...)
	}

	// A persistent embedding cache may buffer writes behind its async writer.
	// Flush before saving/stashing cache metadata while keeping the provider
	// reusable by this session.
	var flushErr error
	if flusher, ok := s.session.Provider.(interface{ Flush() error }); ok {
		flushErr = flusher.Flush()
	}
	if indexErr != nil {
		return nil, indexErr
	}
	if flushErr != nil {
		return nil, fmt.Errorf("flush embedding provider: %w", flushErr)
	}
	if err := s.saveCurrentEmbeddingProfile(); err != nil {
		return nil, err
	}

	// Auto-snapshot the embedding cache to fcheap after a successful index.
	// Best-effort: failures are logged and swallowed so they never break
	// the index command.
	s.maybeStashEmbeddingCache(ctx)

	return result, nil
}

// maybeRestoreEmbeddingCache searches fcheap for a matching embedding-cache
// stash and merges it into the live DiskCache before indexing. Best-effort:
// if fcheap is unavailable or no stash is found, it is a silent no-op.
func (s *Service) maybeRestoreEmbeddingCache(ctx context.Context) {
	if !s.session.Config.Cache.FcheapStashEnabled() {
		return
	}
	modelName := s.session.Config.Embedding.Model
	restoredPath := restoreEmbeddingCache(ctx, s.session.ProjectRoot, modelName)
	if restoredPath != "" {
		loadRestoredCacheIntoProvider(s.session.Provider, restoredPath)
	}
}

// maybeStashEmbeddingCache stashes the disk cache file (if any) to fcheap
// after indexing completes. Best-effort: if fcheap is unavailable or the
// save fails, the error is logged and swallowed.
func (s *Service) maybeStashEmbeddingCache(ctx context.Context) {
	if !s.session.Config.Cache.FcheapStashEnabled() {
		return
	}
	modelName := s.session.Config.Embedding.Model
	ttl := s.session.Config.Cache.FcheapTTL
	if ttl == "" {
		ttl = "30d"
	}
	stashEmbeddingCache(ctx, s.session.Provider, s.session.ProjectRoot, modelName, ttl)
}

func (s *Service) DeleteFile(ctx context.Context, path string) (int64, error) {
	if s == nil || s.session == nil {
		return 0, fmt.Errorf("service not initialized")
	}
	return s.session.DB.DeleteFile(ctx, path)
}

// DryRunPreview returns counts of files needing reindexing and an estimated
// chunk count without calling the embedding provider. It is used by the
// --dry-run flag on the index command to preview what would change.
func (s *Service) DryRunPreview(ctx context.Context) (*index.DryRunPreview, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("service not initialized")
	}
	indexer := index.NewIndexer(s.session.DB, nil, s.indexerConfig(nil))
	return indexer.DryRunPreview(ctx, s.session.ProjectRoot)
}

func (s *Service) Clean(ctx context.Context) (*db.CleanStats, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("service not initialized")
	}
	return s.session.DB.Clean(ctx)
}

func (s *Service) Reset(ctx context.Context, scope ResetScope) error {
	if s == nil || s.session == nil {
		return fmt.Errorf("service not initialized")
	}
	switch scope {
	case ResetAll:
		if err := s.session.DB.ResetAll(ctx); err != nil {
			return err
		}
	default:
		if err := s.session.DB.Reset(ctx, s.session.ProjectRoot); err != nil {
			return err
		}
	}
	if err := RemoveEmbeddingProfileMeta(s.session.DB); err != nil {
		return fmt.Errorf("remove embedding profile metadata: %w", err)
	}
	return RemoveEmbeddingProfile(s.session.Config.DataDir)
}

func (s *Service) indexerConfig(additionalIgnores []string) index.IndexerConfig {
	cfg := index.DefaultIndexerConfig()
	// Config ChunkSize is in tokens; the chunker operates in characters.
	// Approximate conversion: ~4 chars per token for typical code.
	cfg.ChunkSize = s.session.Config.Indexing.ChunkSize * 4
	// Config ChunkOverlap is in tokens; the chunker operates in characters.
	// Approximate conversion: ~4 chars per token for typical code.
	cfg.ChunkOverlap = s.session.Config.Indexing.ChunkOverlap * 4
	cfg.MaxFileSize = s.session.Config.Indexing.MaxFileSize
	cfg.SourceBufferBytes = s.session.Config.Indexing.SourceBufferBytes
	cfg.SyncInterval = s.session.Config.Indexing.SyncInterval
	cfg.SyncIntervalDuration = s.session.Config.Indexing.SyncIntervalDuration
	cfg.IgnorePatterns = append(cfg.IgnorePatterns, s.session.Config.Indexing.IgnorePatterns...)
	cfg.IgnorePatterns = append(cfg.IgnorePatterns, additionalIgnores...)
	return cfg
}

func RoundDuration(d time.Duration) time.Duration {
	return d.Round(100 * time.Millisecond)
}
