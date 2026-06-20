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

	indexer := index.NewIndexer(s.session.DB, s.session.Provider, s.indexerConfig(req.AdditionalIgnores))
	if progress != nil {
		indexer.SetProgressCallback(progress)
	}

	if req.FullReindex {
		result, err := indexer.ReindexAll(ctx, s.session.ProjectRoot)
		if err != nil {
			return nil, err
		}
		if err := s.saveCurrentEmbeddingProfile(); err != nil {
			return nil, err
		}
		return result, nil
	}
	result, err := indexer.Index(ctx, s.session.ProjectRoot, req.Paths...)
	if err != nil {
		return nil, err
	}
	if err := s.saveCurrentEmbeddingProfile(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) DeleteFile(ctx context.Context, path string) (int64, error) {
	if s == nil || s.session == nil {
		return 0, fmt.Errorf("service not initialized")
	}
	return s.session.DB.DeleteFile(ctx, path)
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
	return RemoveEmbeddingProfile(s.session.Config.DataDir)
}

func (s *Service) indexerConfig(additionalIgnores []string) index.IndexerConfig {
	cfg := index.DefaultIndexerConfig()
	cfg.ChunkSize = s.session.Config.Indexing.ChunkSize * 4
	cfg.ChunkOverlap = s.session.Config.Indexing.ChunkOverlap * 4
	cfg.MaxFileSize = s.session.Config.Indexing.MaxFileSize
	cfg.IgnorePatterns = append(cfg.IgnorePatterns, s.session.Config.Indexing.IgnorePatterns...)
	cfg.IgnorePatterns = append(cfg.IgnorePatterns, additionalIgnores...)
	return cfg
}

func RoundDuration(d time.Duration) time.Duration {
	return d.Round(100 * time.Millisecond)
}
