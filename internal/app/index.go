package app

import (
	"context"
	"fmt"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

type IndexRequest struct {
	Paths             []string
	FullReindex       bool
	AdditionalIgnores []string
	// StructuralChunks overrides codemap.structural_chunks for this run when
	// non-empty (auto, off, or required).
	StructuralChunks string
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
	return s.coordinator().Index(ctx, req, progress)
}

// ApplyWatchEvents routes one debounced watcher batch through the same
// coordinator lifecycle and serialization as explicit CLI/MCP indexing.
func (s *Service) ApplyWatchEvents(ctx context.Context, events []index.WatchEvent) (*index.IndexResult, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("service not initialized")
	}
	return s.coordinator().ApplyWatchEvents(ctx, events)
}

func (s *Service) coordinator() *IndexCoordinator {
	s.indexCoordinatorMu.Lock()
	defer s.indexCoordinatorMu.Unlock()
	if s.indexCoordinator == nil {
		s.indexCoordinator = newSessionIndexCoordinator(s.session)
	}
	return s.indexCoordinator
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
	return s.session.DB.DeleteProjectFile(ctx, s.session.ProjectRoot, path)
}

// DryRunPreview returns counts of files needing reindexing and an estimated
// chunk count without calling the embedding provider. It is used by the
// --dry-run flag on the index command to preview what would change.
func (s *Service) DryRunPreview(ctx context.Context) (*index.DryRunPreview, error) {
	return s.DryRunPreviewWithStructuralMode(ctx, "")
}

// DryRunPreviewWithStructuralMode is DryRunPreview with the same optional
// structural-mode override accepted by Index.
func (s *Service) DryRunPreviewWithStructuralMode(ctx context.Context, structuralMode string) (*index.DryRunPreview, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("service not initialized")
	}
	indexer, err := NewConfiguredIndexer(s.session.DB, nil, s.session.Config, nil, structuralMode)
	if err != nil {
		return nil, err
	}
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

func RoundDuration(d time.Duration) time.Duration {
	return d.Round(100 * time.Millisecond)
}
