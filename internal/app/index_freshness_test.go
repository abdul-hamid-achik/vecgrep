package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

func TestIndexFreshnessRequiresMatchingFreshManifestForStructuralReceipt(t *testing.T) {
	session, service := createTestSession(t)
	projectKey, fingerprint := seedFreshnessIndex(t, session, true)

	manifest := validStructuralManifest(projectKey, fingerprint)
	service.manifestSource = manifestSource(t, manifest, nil)
	report, pending, err := service.IndexFreshness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.IsFresh() || !report.RawSourceComplete || !report.ReceiptVerified || !report.ManifestRequired || !report.ManifestVerified {
		t.Fatalf("freshness report = %+v", report)
	}
	if pending == nil || pending.TotalPending != 0 {
		t.Fatalf("pending = %+v", pending)
	}

	manifest.IndexFingerprint = strings.Repeat("c", 64)
	service.manifestSource = manifestSource(t, manifest, nil)
	report, _, err = service.IndexFreshness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != IndexFreshnessUnknown || report.Reason != "structural_manifest_mismatch" || report.ManifestVerified {
		t.Fatalf("mismatched manifest report = %+v", report)
	}

	// A global codemap.db from another project must not override the scoped
	// manifest mismatch and claim this project has a usable graph.
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "codemap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "codemap", "codemap.db"), []byte("other project"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", xdg)
	session.Provider = nil
	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.HasCodemapGraph {
		t.Fatalf("global registry heuristic overrode manifest mismatch: %+v", status.Freshness)
	}
}

func TestIndexFreshnessReportsManifestAndRawDriftAsStale(t *testing.T) {
	session, service := createTestSession(t)
	projectKey, fingerprint := seedFreshnessIndex(t, session, true)
	manifest := validStructuralManifest(projectKey, fingerprint)
	manifest.Freshness = StructuralManifestFreshness{Checked: true, Fresh: false, Changed: 1}
	service.manifestSource = manifestSource(t, manifest, nil)

	report, pending, err := service.IndexFreshness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != IndexFreshnessStale || report.Reason != "structural_manifest_stale" || !report.ManifestVerified {
		t.Fatalf("stale manifest report = %+v", report)
	}
	if pending == nil || pending.TotalPending != 0 {
		t.Fatalf("raw pending = %+v", pending)
	}

	if err := os.WriteFile(filepath.Join(session.ProjectRoot, "main.go"), []byte("package main\n\nfunc changed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.manifestSource = &codemapStructuralManifestSource{
		bin: "fake", timeout: time.Second, maxOutput: 4096,
		run: func(context.Context, string, string, time.Duration, int) ([]byte, error) {
			calls++
			return nil, errors.New("manifest should not run after raw drift")
		},
	}
	report, pending, err = service.IndexFreshness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != IndexFreshnessStale || report.Reason != "raw_source_drift" || pending == nil || pending.ModifiedFiles != 1 {
		t.Fatalf("raw drift report/pending = %+v / %+v", report, pending)
	}
	if calls != 0 {
		t.Fatalf("manifest called %d times despite exact raw drift", calls)
	}
}

func TestIndexFreshnessLegacyHashesAndCorruptReceiptAreUnknown(t *testing.T) {
	session, service := createTestSession(t)
	path := filepath.Join(session.ProjectRoot, "legacy.go")
	content := []byte("package legacy\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	chunk := db.NewChunkRecord(path, "legacy.go", "legacy-hash", int64(len(content)), "go", string(content), 1, 1, 0, len(content), "generic", "", session.ProjectRoot)
	if _, err := session.DB.InsertChunk(chunk, make([]float32, session.Config.Embedding.Dimensions)); err != nil {
		t.Fatal(err)
	}

	report, pending, err := service.IndexFreshness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != IndexFreshnessUnknown || report.Reason != "raw_source_hashes_incomplete" || pending != nil {
		t.Fatalf("legacy report/pending = %+v / %+v", report, pending)
	}

	// A complete raw hash must still not bypass a corrupt receipt.
	sourceHash := sha256HexBytes(content)
	chunk.SourceHash = sourceHash
	if _, _, err := session.DB.UpsertChunk(chunk, make([]float32, session.Config.Embedding.Dimensions)); err != nil {
		t.Fatal(err)
	}
	receiptPath, err := IngestionReceiptPath(session.Config.DataDir, session.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(receiptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, pending, err = service.IndexFreshness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.State != IndexFreshnessUnknown || report.Reason != "ingestion_receipt_invalid" || pending == nil || pending.TotalPending != 0 {
		t.Fatalf("corrupt receipt report/pending = %+v / %+v", report, pending)
	}
}

func TestValidateStructuralManifestStrictV1Contract(t *testing.T) {
	projectKey := "0123456789ab"
	fingerprint := strings.Repeat("a", 64)
	base := validStructuralManifest(projectKey, fingerprint)

	tests := []struct {
		name       string
		mutate     func(*StructuralManifestReport)
		wantTarget error
	}{
		{name: "schema", mutate: func(r *StructuralManifestReport) { r.SchemaVersion = 2 }, wantTarget: errStructuralManifestInvalid},
		{name: "export schema", mutate: func(r *StructuralManifestReport) { r.ExportSchemaVersion = 2 }, wantTarget: errStructuralManifestInvalid},
		{name: "project key", mutate: func(r *StructuralManifestReport) { r.ProjectKey = "abcdef012345" }, wantTarget: errStructuralManifestMismatch},
		{name: "fingerprint", mutate: func(r *StructuralManifestReport) { r.IndexFingerprint = strings.Repeat("b", 64) }, wantTarget: errStructuralManifestMismatch},
		{name: "complete", mutate: func(r *StructuralManifestReport) { r.Complete = false }, wantTarget: errStructuralManifestInvalid},
		{name: "freshness unchecked", mutate: func(r *StructuralManifestReport) { r.Freshness.Checked = false }, wantTarget: errStructuralManifestInvalid},
		{name: "freshness inconsistent", mutate: func(r *StructuralManifestReport) { r.Freshness.Fresh = false }, wantTarget: errStructuralManifestInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := base
			test.mutate(&report)
			err := validateStructuralManifest(report, projectKey, fingerprint)
			if !errors.Is(err, test.wantTarget) {
				t.Fatalf("validate error = %v, want %v", err, test.wantTarget)
			}
		})
	}

	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "total_records")
	missing, _ := json.Marshal(document)
	source := &codemapStructuralManifestSource{
		bin: "fake", timeout: time.Second, maxOutput: 4096,
		run: func(context.Context, string, string, time.Duration, int) ([]byte, error) { return missing, nil },
	}
	if _, err := source.load(context.Background(), t.TempDir(), projectKey, fingerprint); !errors.Is(err, errStructuralManifestInvalid) {
		t.Fatalf("missing required field error = %v", err)
	}
}

func TestRunCodemapStructuralManifestBoundsTimeoutAndOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses POSIX executable semantics")
	}
	root := t.TempDir()
	overflow := filepath.Join(root, "overflow")
	if err := os.WriteFile(overflow, []byte("#!/bin/sh\nprintf 'abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runCodemapStructuralManifest(context.Background(), overflow, root, time.Second, 16); err == nil || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("overflow error = %v", err)
	}

	slow := filepath.Join(root, "slow")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runCodemapStructuralManifest(context.Background(), slow, root, 20*time.Millisecond, 1024); err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("timeout error = %v", err)
	}
}

func seedFreshnessIndex(t *testing.T, session *Session, structural bool) (string, string) {
	t.Helper()
	path := filepath.Join(session.ProjectRoot, "main.go")
	content := []byte("package main\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	chunk := db.NewChunkRecord(path, "main.go", "profile-aware-hash", int64(len(content)), "go", string(content), 1, 1, 0, len(content), "generic", "", session.ProjectRoot)
	chunk.SourceHash = sha256HexBytes(content)
	if _, err := session.DB.InsertChunk(chunk, make([]float32, session.Config.Embedding.Dimensions)); err != nil {
		t.Fatal(err)
	}

	projectKey, err := structuralProjectKey(session.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := strings.Repeat("a", 64)
	lastSuccess := time.Now().UTC()
	receipt := IngestionReceipt{
		SchemaVersion:     ingestionReceiptSchemaVersion,
		AttemptID:         strings.Repeat("b", ingestionAttemptBytes*2),
		ScopeComplete:     true,
		RequestedMode:     StructuralChunksOff,
		EffectiveMode:     "off",
		LastAttempt:       lastSuccess,
		LastSuccess:       &lastSuccess,
		IngestionComplete: true,
		Complete:          true,
		Success:           true,
	}
	receipt.ProjectKey, err = ingestionReceiptProjectKey(session.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if structural {
		receipt.RequestedMode = StructuralChunksRequired
		receipt.EffectiveMode = "structural"
		receipt.Producer = "codemap"
		receipt.Contract = "codemap.structural-export"
		receipt.ContractVersion = structuralExportSchemaVersion
		receipt.ProducerProjectKey = projectKey
		receipt.IndexFingerprint = fingerprint
	}
	receiptPath, err := IngestionReceiptPath(session.Config.DataDir, session.ProjectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIngestionReceiptAtomic(receiptPath, receipt); err != nil {
		t.Fatal(err)
	}
	return projectKey, fingerprint
}

func validStructuralManifest(projectKey, fingerprint string) StructuralManifestReport {
	return StructuralManifestReport{
		SchemaVersion:       structuralManifestSchemaVersion,
		ExportSchemaVersion: structuralExportSchemaVersion,
		Project:             "fixture",
		ProjectKey:          projectKey,
		IndexFingerprint:    fingerprint,
		TotalRecords:        1,
		Complete:            true,
		Freshness:           StructuralManifestFreshness{Checked: true, Fresh: true},
	}
}

func manifestSource(t *testing.T, report StructuralManifestReport, runErr error) *codemapStructuralManifestSource {
	t.Helper()
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return &codemapStructuralManifestSource{
		bin: "fake", timeout: time.Second, maxOutput: 4096,
		run: func(context.Context, string, string, time.Duration, int) ([]byte, error) {
			return data, runErr
		},
	}
}
