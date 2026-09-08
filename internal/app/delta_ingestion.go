package app

import (
	"context"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

// Certified selective re-ingestion: when codemap's manifest attests a reindex
// delta whose from_fingerprint is exactly the fingerprint our ingestion
// receipt certified, this run can ingest only the delta files through the v2
// filtered export. Records for every other file are identical between the two
// exports — unchanged files keep their certified chunks and the run's receipt
// advances to the attested to_fingerprint without a full re-pagination.

// deltaIngestionPlan is the certified scope of a selective index run.
type deltaIngestionPlan struct {
	// ChangedNew are canonical project-relative files to (re)index: codemap's
	// changed and newly indexed files.
	ChangedNew []string
	// Deleted are files codemap pruned; their chunks must be dropped.
	Deleted []string
}

// planDeltaIngestion is the pure decision: given the last certified receipt
// and the freshly loaded codemap manifest, does an attested delta apply?
// Every bail-out falls back to today's full-path behavior — a nil plan is the
// conservative answer, never an error.
func planDeltaIngestion(receipt *IngestionReceipt, manifest *StructuralManifestReport) *deltaIngestionPlan {
	if receipt == nil || manifest == nil || manifest.ReindexDelta == nil {
		return nil
	}
	d := manifest.ReindexDelta
	// Only a complete, error-free, scope-complete receipt is a certification
	// of FromFingerprint; anything else means "we do not know what the old
	// export looked like".
	if !receipt.ScopeComplete || !receipt.IngestionComplete || receipt.ErrorCount > 0 {
		return nil
	}
	if receipt.IndexFingerprint == "" || receipt.IndexFingerprint != d.FromFingerprint {
		return nil
	}
	// The manifest itself must be the to-side of the attestation, so the
	// filtered export this run will consume is exactly the attested new state.
	if manifest.IndexFingerprint != d.ToFingerprint {
		return nil
	}
	if len(d.ChangedFiles)+len(d.NewFiles)+len(d.DeletedFiles) == 0 {
		return nil
	}
	changedNew := make([]string, 0, len(d.ChangedFiles)+len(d.NewFiles))
	changedNew = append(changedNew, d.ChangedFiles...)
	changedNew = append(changedNew, d.NewFiles...)
	return &deltaIngestionPlan{
		ChangedNew: changedNew,
		Deleted:    d.DeletedFiles,
	}
}

// loadCodemapDeltaManifest fetches and shape-validates the current codemap
// manifest without pinning a fingerprint. Best-effort: any failure returns nil
// so the caller falls back to the full ingestion path.
func loadCodemapDeltaManifest(ctx context.Context, cfg *config.Config, projectRoot string) *StructuralManifestReport {
	if cfg == nil || projectRoot == "" {
		return nil
	}
	bin := cfg.Codemap.Bin
	if bin == "" {
		bin = "codemap"
	}
	resolved, err := config.ResolveBinary(bin)
	if err != nil {
		return nil
	}
	source := newCodemapStructuralManifestSource(resolved)
	projectKey, err := ingestionReceiptProjectKey(projectRoot)
	if err != nil {
		return nil
	}
	manifest, err := source.loadUnpinned(ctx, projectRoot, projectKey)
	if err != nil {
		return nil
	}
	return manifest
}
