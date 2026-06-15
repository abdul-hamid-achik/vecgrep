package app

import (
	"context"
	"fmt"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

type StatusResponse struct {
	ProjectRoot      string
	ProjectName      string
	DataDir          string
	DBPath           string
	VecLitePath      string
	VectorBackend    string
	Provider         string
	Model            string
	Dimensions       int
	Stats            map[string]int64
	DetailedStats    *db.Stats
	PendingChanges   *index.PendingChanges
	ConfigSources    []string
	MigrationWarning string
}

func (s *Service) Status(ctx context.Context) (*StatusResponse, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("service not initialized")
	}

	stats, err := s.session.DB.StatsForProject(s.session.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("get stats: %w", err)
	}

	detailed, err := s.session.DB.GetDetailedStats(s.session.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("get detailed stats: %w", err)
	}

	vecVersion, _ := s.session.DB.VecVersion()

	indexer := index.NewIndexer(s.session.DB, nil, s.indexerConfig(nil))
	pending, _ := indexer.GetPendingChanges(ctx, s.session.ProjectRoot)

	return &StatusResponse{
		ProjectRoot:      s.session.ProjectRoot,
		ProjectName:      s.session.ProjectName,
		DataDir:          s.session.Config.DataDir,
		DBPath:           s.session.Config.DBPath,
		VecLitePath:      s.session.VecLitePath,
		VectorBackend:    vecVersion,
		Provider:         s.session.Config.Embedding.Provider,
		Model:            s.session.Config.Embedding.Model,
		Dimensions:       s.session.Config.Embedding.Dimensions,
		Stats:            stats,
		DetailedStats:    detailed,
		PendingChanges:   pending,
		ConfigSources:    s.session.ConfigSources,
		MigrationWarning: s.session.MigrationWarning,
	}, nil
}
