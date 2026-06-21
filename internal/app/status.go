package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/veclite"
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

	// HNSWConfig reports the resolved HNSW index/search parameters actually
	// applied to the veclite collection (M, EfConstruction, EfSearch). These
	// are surfaced in Studio status so users can confirm their config tuning
	// (Phase 1 wiring) is taking effect.
	HNSWM              int
	HNSWEfConstruction int
	HNSWEfSearch       int

	// VecliteVersion reports the veclite dependency version in use.
	VecliteVersion string

	// ProviderHealth reports the result of pinging the embedding provider.
	// Empty means "not checked" (e.g. no provider configured), "ok" means the
	// provider responded to a Ping, otherwise it holds the error string. This
	// is surfaced in Studio status so users can see at a glance whether
	// semantic search will work before issuing a query.
	ProviderHealth string
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
	storedProfile, profileErr := LoadEmbeddingProfile(s.session.DB, s.session.Config.DataDir)
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

	// Resolve the effective HNSW parameters. The config layer defaults M to 0
	// (meaning "use veclite's default"), so surface the veclite default when
	// unset rather than a misleading 0.
	hnswM := s.session.Config.Vector.VecLite.M
	hnswEfConstruction := s.session.Config.Vector.VecLite.EfConstruction
	hnswEfSearch := s.session.Config.Vector.VecLite.EfSearch
	if hnswEfConstruction == 0 {
		hnswEfConstruction = config.DefaultVecLiteEfConstruction
	}
	if hnswEfSearch == 0 {
		hnswEfSearch = config.DefaultVecLiteEfSearch
	}
	if hnswM == 0 {
		hnswM = config.DefaultVecLiteM
	}

	// Probe the embedding provider so users can see whether semantic search
	// will work before issuing a query. Best-effort: a failure here does not
	// fail the whole status call (the index stats are still useful without it).
	providerHealth := ""
	if s.session.Provider != nil {
		if err := s.session.Provider.Ping(ctx); err != nil {
			providerHealth = err.Error()
		} else {
			providerHealth = "ok"
		}
	}

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
		// Surface the resolved HNSW parameters so users can confirm their
		// config tuning is actually applied (Phase 1 wiring). Defaults are
		// resolved above so a 0 in config shows as veclite's default, not 0.
		HNSWM:              hnswM,
		HNSWEfConstruction: hnswEfConstruction,
		HNSWEfSearch:       hnswEfSearch,
		VecliteVersion:     veclite.Version,
		ProviderHealth:     providerHealth,
	}, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}
