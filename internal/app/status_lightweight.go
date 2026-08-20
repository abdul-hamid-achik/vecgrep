package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// LightweightStatus resolves project metadata and verifies the latest health
// manifest without opening VecLite. It is intended for prompts, editor health
// panels, and polling agents where loading the HNSW graph would be wasteful.
// The result is deliberately fail-closed: a missing, stale, corrupt, or
// interrupted manifest never reports a ready index.
func LightweightStatus(ctx context.Context, startDir string) (*StatusResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	projectRoot, resolved, resolver, err := resolveProjectForStatus(startDir)
	if err != nil {
		return nil, err
	}
	cfg := resolved.Config
	stats := map[string]int64{}
	manifest, manifestErr := LoadIndexHealthManifest(cfg.DataDir, projectRoot)
	if manifestErr == nil && manifest != nil {
		stats = cloneHealthStats(manifest.Stats)
	}

	receipt, receiptErr := LoadIngestionReceipt(cfg.DataDir, projectRoot)
	profile, profileErr := LoadEmbeddingProfile(nil, cfg.DataDir)
	profileStatus := "ok"
	profileMatches := true
	if profileErr != nil {
		profileStatus = profileErr.Error()
		profileMatches = false
	} else if profile == nil {
		if stats["chunks"] > 0 {
			profileStatus = "missing"
			profileMatches = false
		} else {
			profileStatus = "not written yet"
		}
	} else if !profile.Matches(CurrentEmbeddingProfile(cfg)) {
		profileStatus = "mismatch"
		profileMatches = false
	}

	report := &IndexFreshnessReport{State: IndexFreshnessUnknown, Reason: "index_health_manifest_missing"}
	var pending *index.PendingChanges
	if manifestErr != nil {
		report.Reason = "index_health_manifest_invalid"
	} else if manifest == nil {
		report.Reason = "index_health_manifest_missing"
	} else if receiptErr != nil {
		report.Reason = "ingestion_receipt_invalid"
	} else if receipt == nil {
		report.Reason = "ingestion_receipt_missing"
	} else if !successfulIngestionReceipt(receipt) {
		report.Reason = "ingestion_receipt_incomplete"
	} else if manifest.AttemptID != receipt.AttemptID {
		report.Reason = "index_health_manifest_mismatch"
	} else {
		currentHashes, scanErr := index.ScanRawFileHashes(ctx, projectRoot, BuildIndexerConfig(cfg, nil))
		if scanErr != nil {
			report.Reason = "raw_source_check_failed"
		} else {
			report.RawSourceComplete = true
			pending = index.CompareSourceHashes(manifest.SourceHashes, currentHashes)
			if pending.TotalPending > 0 {
				report.State = IndexFreshnessStale
				report.Reason = "raw_source_drift"
			} else {
				report = evaluateReceiptFreshness(ctx, projectRoot, cfg, receipt, nil, nil, report)
			}
		}
	}

	receiptError := ""
	if receiptErr != nil {
		receiptError = receiptErr.Error()
	}
	return &StatusResponse{
		ProjectRoot:      projectRoot,
		ProjectName:      resolved.ProjectName,
		DataDir:          cfg.DataDir,
		DBPath:           cfg.DBPath,
		VecLitePath:      db.VecLitePath(cfg.DataDir),
		Provider:         cfg.Embedding.Provider,
		Model:            cfg.Embedding.Model,
		Dimensions:       cfg.Embedding.Dimensions,
		ProfilePath:      EmbeddingProfilePath(cfg.DataDir),
		CurrentProfile:   CurrentEmbeddingProfile(cfg),
		StoredProfile:    profile,
		ProfileStatus:    profileStatus,
		ProfileMatches:   profileMatches,
		VecLiteSizeBytes: fileSize(db.VecLitePath(cfg.DataDir)),
		IndexedBytes:     healthIndexedBytes(manifest),
		LatestIndexedAt:  healthLatestIndexedAt(manifest),
		IndexFresh:       report.IsFresh(),
		Stats:            stats,
		PendingChanges:   pending,
		ConfigSources:    resolver.FoundConfigFiles(),
		IngestionReceipt: receipt,
		ReceiptError:     receiptError,
		Freshness:        report,
		Lightweight:      true,
	}, nil
}

func resolveProjectForStatus(startDir string) (string, *config.ResolvedConfig, *config.ConfigResolution, error) {
	if startDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, nil, fmt.Errorf("get cwd: %w", err)
		}
		startDir = cwd
	}
	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve start dir: %w", err)
	}
	projectRoot, err := config.FindProjectRootFrom(absStart)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: run 'vecgrep init' first", err)
	}
	resolver := config.NewConfigResolution()
	resolved, err := resolver.Resolve(projectRoot)
	if err != nil {
		return "", nil, nil, fmt.Errorf("resolve config: %w", err)
	}
	return projectRoot, resolved, resolver, nil
}

func healthIndexedBytes(manifest *IndexHealthManifest) int64 {
	if manifest == nil {
		return 0
	}
	return manifest.IndexedBytes
}

func healthLatestIndexedAt(manifest *IndexHealthManifest) (result time.Time) {
	if manifest != nil {
		return manifest.LatestIndexedAt
	}
	return result
}
