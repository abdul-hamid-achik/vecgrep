package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

const (
	structuralManifestSchemaVersion = 1
	structuralManifestTimeout       = 10 * time.Second
	structuralManifestMaxOutput     = 1024 * 1024
	structuralManifestMaxStderr     = 64 * 1024
)

var (
	errStructuralManifestInvalid  = errors.New("invalid structural manifest")
	errStructuralManifestMismatch = errors.New("structural manifest mismatch")
)

type IndexFreshnessState string

const (
	IndexFreshnessFresh   IndexFreshnessState = "fresh"
	IndexFreshnessStale   IndexFreshnessState = "stale"
	IndexFreshnessUnknown IndexFreshnessState = "unknown"
)

// StructuralManifestFreshness is codemap's source-free working-tree drift
// summary. The fields mirror codemap.structural-manifest.v1.
type StructuralManifestFreshness struct {
	Checked bool `json:"checked"`
	Fresh   bool `json:"fresh"`
	Changed int  `json:"changed"`
	New     int  `json:"new"`
	Deleted int  `json:"deleted"`
}

// StructuralManifestReport is the validated identity preflight for the
// structural snapshot consumed by vecgrep. It contains no source bodies.
type StructuralManifestReport struct {
	SchemaVersion       int                         `json:"schema_version"`
	ExportSchemaVersion int                         `json:"export_schema_version"`
	Project             string                      `json:"project"`
	ProjectKey          string                      `json:"project_key"`
	IndexFingerprint    string                      `json:"index_fingerprint"`
	TotalRecords        int                         `json:"total_records"`
	Complete            bool                        `json:"complete"`
	Freshness           StructuralManifestFreshness `json:"freshness"`
}

// IndexFreshnessReport explains the conservative freshness decision. State is
// fresh only after raw source hashes, the last successful ingestion receipt,
// and (when structural data was consumed) codemap's manifest all agree.
type IndexFreshnessReport struct {
	State              IndexFreshnessState       `json:"state"`
	Reason             string                    `json:"reason"`
	RawSourceComplete  bool                      `json:"raw_source_complete"`
	ReceiptVerified    bool                      `json:"receipt_verified"`
	ReceiptLastSuccess *time.Time                `json:"receipt_last_success,omitempty"`
	ManifestRequired   bool                      `json:"manifest_required"`
	ManifestVerified   bool                      `json:"manifest_verified"`
	StructuralManifest *StructuralManifestReport `json:"structural_manifest,omitempty"`
}

func (r *IndexFreshnessReport) IsFresh() bool {
	return r != nil && r.State == IndexFreshnessFresh
}

type structuralManifestRunner func(context.Context, string, string, time.Duration, int) ([]byte, error)

type codemapStructuralManifestSource struct {
	bin       string
	timeout   time.Duration
	maxOutput int
	run       structuralManifestRunner
}

func newCodemapStructuralManifestSource(bin string) *codemapStructuralManifestSource {
	return &codemapStructuralManifestSource{
		bin:       bin,
		timeout:   structuralManifestTimeout,
		maxOutput: structuralManifestMaxOutput,
		run:       runCodemapStructuralManifest,
	}
}

func runCodemapStructuralManifest(ctx context.Context, bin, projectRoot string, timeout time.Duration, maxOutput int) ([]byte, error) {
	if timeout <= 0 {
		timeout = structuralManifestTimeout
	}
	if maxOutput <= 0 {
		maxOutput = structuralManifestMaxOutput
	}
	manifestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(manifestCtx, bin, "structural-manifest", "--json")
	cmd.Dir = projectRoot
	stdout := newCappedCommandOutput(maxOutput)
	stderr := newCappedCommandOutput(structuralManifestMaxStderr)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if manifestCtx.Err() != nil {
			return nil, fmt.Errorf("codemap structural-manifest: %w", manifestCtx.Err())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = codemapFailureMessage(stdout.Bytes())
		}
		if message != "" {
			return nil, fmt.Errorf("codemap structural-manifest: %s", message)
		}
		return nil, fmt.Errorf("codemap structural-manifest: %w", err)
	}
	if stdout.Overflowed() {
		return nil, fmt.Errorf("codemap structural-manifest exceeded the %d-byte output limit", maxOutput)
	}
	return stdout.Bytes(), nil
}

func (s *codemapStructuralManifestSource) load(ctx context.Context, projectRoot, projectKey, fingerprint string) (*StructuralManifestReport, error) {
	if s == nil || s.bin == "" || s.run == nil {
		return nil, fmt.Errorf("codemap structural manifest adapter unavailable")
	}
	out, err := s.run(ctx, s.bin, projectRoot, s.timeout, s.maxOutput)
	if err != nil {
		return nil, err
	}

	var report StructuralManifestReport
	decoder := json.NewDecoder(bytes.NewReader(out))
	if err := decoder.Decode(&report); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", errStructuralManifestInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing JSON value", errStructuralManifestInvalid)
		}
		return nil, fmt.Errorf("%w: trailing data: %v", errStructuralManifestInvalid, err)
	}
	if err := validateStructuralManifestShape(out); err != nil {
		return nil, err
	}
	if err := validateStructuralManifest(report, projectKey, fingerprint); err != nil {
		return nil, err
	}
	return &report, nil
}

func validateStructuralManifestShape(out []byte) error {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(out, &document); err != nil || document == nil {
		return fmt.Errorf("%w: expected JSON object", errStructuralManifestInvalid)
	}
	for _, field := range []string{"schema_version", "export_schema_version", "project", "project_key", "index_fingerprint", "total_records", "complete", "freshness"} {
		value, ok := document[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%w: missing %s", errStructuralManifestInvalid, field)
		}
	}
	var freshness map[string]json.RawMessage
	if err := json.Unmarshal(document["freshness"], &freshness); err != nil || freshness == nil {
		return fmt.Errorf("%w: freshness must be an object", errStructuralManifestInvalid)
	}
	for _, field := range []string{"checked", "fresh", "changed", "new", "deleted"} {
		value, ok := freshness[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%w: missing freshness.%s", errStructuralManifestInvalid, field)
		}
	}
	return nil
}

func validateStructuralManifest(report StructuralManifestReport, projectKey, fingerprint string) error {
	if report.SchemaVersion != structuralManifestSchemaVersion {
		return fmt.Errorf("%w: unsupported schema_version %d", errStructuralManifestInvalid, report.SchemaVersion)
	}
	if report.ExportSchemaVersion != structuralExportSchemaVersion {
		return fmt.Errorf("%w: unsupported export_schema_version %d", errStructuralManifestInvalid, report.ExportSchemaVersion)
	}
	if report.Project == "" || !validLowerHex(report.ProjectKey, 12) {
		return fmt.Errorf("%w: invalid project identity", errStructuralManifestInvalid)
	}
	if report.ProjectKey != projectKey {
		return fmt.Errorf("%w: project_key", errStructuralManifestMismatch)
	}
	if !validLowerHex(report.IndexFingerprint, 64) {
		return fmt.Errorf("%w: invalid index_fingerprint", errStructuralManifestInvalid)
	}
	if report.IndexFingerprint != fingerprint {
		return fmt.Errorf("%w: index_fingerprint", errStructuralManifestMismatch)
	}
	if report.TotalRecords < 0 || !report.Complete {
		return fmt.Errorf("%w: incomplete structural snapshot", errStructuralManifestInvalid)
	}
	freshness := report.Freshness
	if !freshness.Checked || freshness.Changed < 0 || freshness.New < 0 || freshness.Deleted < 0 {
		return fmt.Errorf("%w: invalid freshness", errStructuralManifestInvalid)
	}
	hasDrift := freshness.Changed > 0 || freshness.New > 0 || freshness.Deleted > 0
	if freshness.Fresh == hasDrift {
		return fmt.Errorf("%w: inconsistent freshness", errStructuralManifestInvalid)
	}
	return nil
}

// IndexFreshness evaluates status without loading codemap's structural export.
// It is shared by CLI, IndexMeta, daemon and MCP status surfaces.
func (s *Service) IndexFreshness(ctx context.Context) (*IndexFreshnessReport, *index.PendingChanges, error) {
	if s == nil || s.session == nil || s.session.DB == nil || s.session.Config == nil {
		return nil, nil, fmt.Errorf("service not initialized")
	}
	receipt, receiptErr := LoadIngestionReceipt(s.session.Config.DataDir, s.session.ProjectRoot)
	return s.indexFreshness(ctx, receipt, receiptErr)
}

func (s *Service) rawPendingChanges(ctx context.Context) (*index.PendingChanges, bool, error) {
	idx := index.NewIndexer(s.session.DB, nil, BuildIndexerConfig(s.session.Config, nil))
	return idx.GetRawPendingChanges(ctx, s.session.ProjectRoot)
}

func (s *Service) indexFreshness(ctx context.Context, receipt *IngestionReceipt, receiptErr error) (*IndexFreshnessReport, *index.PendingChanges, error) {
	report := &IndexFreshnessReport{State: IndexFreshnessUnknown, Reason: "raw_source_check_failed"}
	pending, rawComplete, err := s.rawPendingChanges(ctx)
	if err != nil {
		return report, nil, nil
	}
	report.RawSourceComplete = rawComplete
	if !rawComplete {
		report.Reason = "raw_source_hashes_incomplete"
		return report, nil, nil
	}
	if pending == nil {
		return report, nil, nil
	}
	if pending.TotalPending > 0 {
		report.State = IndexFreshnessStale
		report.Reason = "raw_source_drift"
		return report, pending, nil
	}

	if receiptErr != nil {
		report.Reason = "ingestion_receipt_invalid"
		return report, pending, nil
	}
	if receipt == nil {
		report.Reason = "ingestion_receipt_missing"
		return report, pending, nil
	}
	if !successfulIngestionReceipt(receipt) {
		report.Reason = "ingestion_receipt_incomplete"
		return report, pending, nil
	}
	report.ReceiptVerified = true
	lastSuccess := receipt.LastSuccess.UTC()
	report.ReceiptLastSuccess = &lastSuccess

	report.ManifestRequired = receiptUsesStructuralSnapshot(receipt)
	if !report.ManifestRequired {
		report.State = IndexFreshnessFresh
		report.Reason = "fresh"
		return report, pending, nil
	}

	expectedProjectKey, err := structuralProjectKey(s.session.ProjectRoot)
	if err != nil || receipt.Producer != "codemap" || receipt.Contract != "codemap.structural-export" || receipt.ContractVersion != structuralExportSchemaVersion || receipt.ProducerProjectKey != expectedProjectKey || !validLowerHex(receipt.IndexFingerprint, 64) {
		report.Reason = "ingestion_receipt_structural_contract_invalid"
		return report, pending, nil
	}

	source := s.manifestSource
	if source == nil {
		bin := s.session.Config.Codemap.Bin
		if bin == "" {
			bin = "codemap"
		}
		resolved, resolveErr := config.ResolveBinary(bin)
		if resolveErr != nil {
			report.Reason = "structural_manifest_unavailable"
			return report, pending, nil
		}
		source = newCodemapStructuralManifestSource(resolved)
	}
	manifest, manifestErr := source.load(ctx, s.session.ProjectRoot, expectedProjectKey, receipt.IndexFingerprint)
	if manifestErr != nil {
		switch {
		case errors.Is(manifestErr, errStructuralManifestMismatch):
			report.Reason = "structural_manifest_mismatch"
		case errors.Is(manifestErr, errStructuralManifestInvalid):
			report.Reason = "structural_manifest_invalid"
		default:
			report.Reason = "structural_manifest_unavailable"
		}
		return report, pending, nil
	}
	report.ManifestVerified = true
	report.StructuralManifest = manifest
	if !manifest.Freshness.Fresh {
		report.State = IndexFreshnessStale
		report.Reason = "structural_manifest_stale"
		return report, pending, nil
	}
	report.State = IndexFreshnessFresh
	report.Reason = "fresh"
	return report, pending, nil
}

func successfulIngestionReceipt(receipt *IngestionReceipt) bool {
	if receipt == nil || !receipt.Success || !receipt.Complete || !receipt.IngestionComplete || receipt.LastSuccess == nil {
		return false
	}
	return receipt.LastAttempt.UTC().Equal(receipt.LastSuccess.UTC())
}

func receiptUsesStructuralSnapshot(receipt *IngestionReceipt) bool {
	if receipt == nil {
		return false
	}
	return receipt.EffectiveMode == "structural" || receipt.EffectiveMode == "mixed" || receipt.IndexFingerprint != "" || receipt.ProducerProjectKey != ""
}
