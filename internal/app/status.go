package app

import (
	"context"
	"fmt"
	"os"
	"time"

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
	ProfilePath      string
	CurrentProfile   EmbeddingProfile
	StoredProfile    *EmbeddingProfile
	ProfileStatus    string
	ProfileMatches   bool
	VecLiteSizeBytes int64
	IndexedBytes     int64
	LatestIndexedAt  time.Time
	IndexFresh       bool
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

	files, err := s.session.DB.ListFiles(s.session.ProjectRoot)
	if err != nil {
		return nil, fmt.Errorf("list indexed files: %w", err)
	}

	var indexedBytes int64
	var latestIndexedAt time.Time
	for _, file := range files {
		indexedBytes += file.Size
		if file.IndexedAt.After(latestIndexedAt) {
			latestIndexedAt = file.IndexedAt
		}
	}

	vecVersion, _ := s.session.DB.VecVersion()
	vecLiteSize := fileSize(s.session.VecLitePath)
	currentProfile := CurrentEmbeddingProfile(s.session.Config)
	storedProfile, profileErr := LoadEmbeddingProfile(s.session.Config.DataDir)
	profileStatus := "ok"
	profileMatches := true
	if profileErr != nil {
		profileStatus = profileErr.Error()
		profileMatches = false
	} else if storedProfile == nil {
		if stats["chunks"] > 0 {
			profileStatus = "missing"
			profileMatches = false
		} else {
			profileStatus = "not written yet"
		}
	} else if !storedProfile.Matches(currentProfile) {
		profileStatus = "mismatch"
		profileMatches = false
	}

	indexer := index.NewIndexer(s.session.DB, nil, s.indexerConfig(nil))
	pending, _ := indexer.GetPendingChanges(ctx, s.session.ProjectRoot)
	indexFresh := pending != nil && pending.TotalPending == 0

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
		ProfilePath:      EmbeddingProfilePath(s.session.Config.DataDir),
		CurrentProfile:   currentProfile,
		StoredProfile:    storedProfile,
		ProfileStatus:    profileStatus,
		ProfileMatches:   profileMatches,
		VecLiteSizeBytes: vecLiteSize,
		IndexedBytes:     indexedBytes,
		LatestIndexedAt:  latestIndexedAt,
		IndexFresh:       indexFresh,
		Stats:            stats,
		DetailedStats:    detailed,
		PendingChanges:   pending,
		ConfigSources:    s.session.ConfigSources,
		MigrationWarning: s.session.MigrationWarning,
	}, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}
