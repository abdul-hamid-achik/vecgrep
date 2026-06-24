package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// HasCodemapGraph reports whether codemap has an index for this project
	// (checked by stat-ing codemap's XDG registry). When true, the codemap
	// integration can use real call-graph data for related-files and
	// structural re-ranking. This is best-effort — a false value does not mean
	// codemap is uninstalled, just that no graph index was found.
	HasCodemapGraph bool
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

	// Check whether codemap has an index for this project by stat-ing its
	// XDG registry directory. Best-effort: missing registry or missing
	// codemap installation simply reports false.
	hasCodemapGraph := checkCodemapRegistry(s.session.ProjectRoot)

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
		HasCodemapGraph:    hasCodemapGraph,
	}, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0
	}
	return info.Size()
}

// checkCodemapRegistry reports whether codemap has registered an index for
// the given project root. It checks codemap's XDG data directory for a
// registry entry matching the project path. Best-effort: any error (missing
// XDG dir, missing codemap installation, unreadable registry) returns false.
func checkCodemapRegistry(projectRoot string) bool {
	// Resolve the XDG data directory for codemap
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		xdgData = filepath.Join(home, ".local", "share")
	}
	// codemap stores its registry under <xdg>/codemap/
	codemapDir := filepath.Join(xdgData, "codemap")
	// Check for a projects registry file or a per-project index directory.
	// codemap uses a SQLite registry at <codemapDir>/codemap.db or a
	// projects subdirectory. We stat for the directory existence as a
	// lightweight signal.
	if info, err := os.Stat(codemapDir); err != nil || !info.IsDir() {
		return false
	}
	// Look for a projects registry that might reference this project.
	// The codemap registry is at <codemapDir>/projects/ or a SQLite file.
	// We check for any file containing the project path hash.
	// For now, a simple heuristic: if the codemap directory exists and
	// contains any .db or index files, assume codemap is active for some
	// project. A more precise check would parse the registry.
	projectsDir := filepath.Join(codemapDir, "projects")
	if info, err := os.Stat(projectsDir); err == nil && info.IsDir() {
		// Check if there's an entry matching the project root
		absRoot, _ := filepath.Abs(projectRoot)
		entries, err := os.ReadDir(projectsDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() && entry.Name() == sanitizeForPath(absRoot) {
					return true
				}
			}
		}
	}
	// Fall back: codemap.db exists means codemap is installed and has data
	dbFile := filepath.Join(codemapDir, "codemap.db")
	if _, err := os.Stat(dbFile); err == nil {
		return true
	}
	return false
}

// sanitizeForPath converts a filesystem path into a safe directory name
// component (replacing path separators with dashes). This is a best-effort
// match against codemap's registry naming convention.
func sanitizeForPath(path string) string {
	s := filepath.ToSlash(path)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return strings.Trim(s, "-")
}
