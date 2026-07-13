package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

func startReceiptAttempt(t *testing.T, dataDir, projectRoot string, mode StructuralChunksMode, scopeComplete bool) string {
	t.Helper()
	attemptID, err := newIngestionAttemptID()
	if err != nil {
		t.Fatal(err)
	}
	if err := beginIngestionReceiptAttempt(dataDir, projectRoot, attemptID, mode, scopeComplete); err != nil {
		t.Fatalf("beginIngestionReceiptAttempt: %v", err)
	}
	return attemptID
}

func TestIngestionReceiptRecordsSuccessfulStructuralRun(t *testing.T) {
	dataDir := t.TempDir()
	projectRoot := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	attemptedAt := time.Date(2026, 7, 13, 10, 30, 0, 0, time.UTC)
	setup := structuralChunksSetup{RequestedMode: StructuralChunksRequired, SourceEnabled: true}
	report := index.IndexRunReport{
		AttemptID:            startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true),
		ScopeComplete:        true,
		ProjectRoot:          projectRoot,
		FinishedAt:           attemptedAt,
		StructuralConfigured: true,
		StructuralRequired:   true,
		StructuralLoaded:     true,
		StructuralComplete:   true,
		StructuralProjectKey: "producer-key",
		IndexFingerprint:     strings.Repeat("a", 64),
		Result: &index.IndexResult{Ingestion: index.IngestionCounts{
			Files:  index.OriginCounts{Structural: 2, Gap: 1},
			Chunks: index.OriginCounts{Structural: 5, Gap: 2},
		}},
	}

	if err := recordIngestionReceipt(dataDir, setup, report); err != nil {
		t.Fatalf("recordIngestionReceipt: %v", err)
	}
	preflight, err := LoadIngestionReceipt(dataDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !preflight.IngestionComplete || preflight.Success || preflight.Complete || preflight.LastSuccess != nil {
		t.Fatalf("pre-finalization receipt = %+v", preflight)
	}
	if err := FinalizeIngestionReceipt(dataDir, projectRoot, nil); err != nil {
		t.Fatalf("FinalizeIngestionReceipt: %v", err)
	}
	receipt, err := LoadIngestionReceipt(dataDir, projectRoot)
	if err != nil {
		t.Fatalf("LoadIngestionReceipt: %v", err)
	}
	if receipt == nil || receipt.SchemaVersion != 1 || receipt.RequestedMode != StructuralChunksRequired || receipt.EffectiveMode != "structural" {
		t.Fatalf("receipt envelope = %+v", receipt)
	}
	if !receipt.Success || !receipt.Complete || receipt.LastSuccess == nil || !receipt.LastSuccess.Equal(attemptedAt) {
		t.Fatalf("receipt success state = %+v", receipt)
	}
	if receipt.Producer != "codemap" || receipt.ContractVersion != 1 || receipt.IndexFingerprint != strings.Repeat("a", 64) {
		t.Fatalf("receipt producer contract = %+v", receipt)
	}
	if receipt.Counts.Chunks.Structural != 5 || receipt.Counts.Chunks.Gap != 2 || receipt.Counts.Chunks.Local != 0 {
		t.Fatalf("receipt counts = %+v", receipt.Counts)
	}
	path, err := IngestionReceiptPath(dataDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %o, want 600", info.Mode().Perm())
	}
}

func TestIngestionReceiptRecordsAutoFallbackHonestly(t *testing.T) {
	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	setup := structuralChunksSetup{RequestedMode: StructuralChunksAuto, SourceEnabled: true}
	report := index.IndexRunReport{
		AttemptID:            startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true),
		ScopeComplete:        true,
		ProjectRoot:          projectRoot,
		FinishedAt:           time.Now(),
		StructuralConfigured: true,
		StructuralWarning:    true,
		Result: &index.IndexResult{
			Errors: []error{errors.New("producer returned an error")},
			Ingestion: index.IngestionCounts{
				Files:  index.OriginCounts{Local: 3},
				Chunks: index.OriginCounts{Local: 8},
			},
		},
	}
	if err := recordIngestionReceipt(dataDir, setup, report); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeIngestionReceipt(dataDir, projectRoot, nil); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadIngestionReceipt(dataDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.EffectiveMode != "local_fallback" || !receipt.Success || !receipt.Complete || receipt.ErrorCount != 0 {
		t.Fatalf("fallback receipt = %+v", receipt)
	}
	if len(receipt.Fallbacks) != 1 || receipt.Fallbacks[0].Code != "producer_error" {
		t.Fatalf("fallbacks = %+v", receipt.Fallbacks)
	}
}

func TestFailedIngestionPreservesLastSuccessWithoutPersistingErrorText(t *testing.T) {
	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	setup := structuralChunksSetup{RequestedMode: StructuralChunksRequired, SourceEnabled: true}
	firstSuccess := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	if err := recordIngestionReceipt(dataDir, setup, index.IndexRunReport{
		AttemptID:          startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true),
		ScopeComplete:      true,
		ProjectRoot:        projectRoot,
		FinishedAt:         firstSuccess,
		StructuralLoaded:   true,
		StructuralComplete: true,
		Result:             &index.IndexResult{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeIngestionReceipt(dataDir, projectRoot, nil); err != nil {
		t.Fatal(err)
	}

	lastAttempt := firstSuccess.Add(time.Hour)
	secret := "provider failed with token=super-secret"
	if err := recordIngestionReceipt(dataDir, setup, index.IndexRunReport{
		AttemptID:     startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true),
		ScopeComplete: true,
		ProjectRoot:   projectRoot,
		FinishedAt:    lastAttempt,
		FailureStage:  "structural_load",
		Err:           errors.New(secret),
	}); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadIngestionReceipt(dataDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Success || receipt.Complete || receipt.LastSuccess == nil || !receipt.LastSuccess.Equal(firstSuccess) || !receipt.LastAttempt.Equal(lastAttempt) {
		t.Fatalf("failed receipt = %+v", receipt)
	}
	if receipt.EffectiveMode != "unavailable" || len(receipt.Fallbacks) != 1 || receipt.Fallbacks[0].Code != "structural_load_failed" {
		t.Fatalf("failure reason = %+v", receipt)
	}
	path, _ := IngestionReceiptPath(dataDir, projectRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "super-secret") {
		t.Fatal("receipt persisted arbitrary error text")
	}
}

func TestPostflightFailurePreservesLastSuccess(t *testing.T) {
	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	setup := structuralChunksSetup{RequestedMode: StructuralChunksRequired, SourceEnabled: true}
	firstSuccess := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	firstReport := index.IndexRunReport{
		AttemptID:          startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true),
		ScopeComplete:      true,
		ProjectRoot:        projectRoot,
		FinishedAt:         firstSuccess,
		StructuralLoaded:   true,
		StructuralComplete: true,
		Result:             &index.IndexResult{},
	}
	if err := recordIngestionReceipt(dataDir, setup, firstReport); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeIngestionReceipt(dataDir, projectRoot, nil); err != nil {
		t.Fatal(err)
	}

	secondAttempt := firstSuccess.Add(time.Hour)
	secondReport := firstReport
	secondReport.AttemptID = startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true)
	secondReport.FinishedAt = secondAttempt
	if err := recordIngestionReceipt(dataDir, setup, secondReport); err != nil {
		t.Fatal(err)
	}
	secret := errors.New("flush failed with token=do-not-store")
	if err := FinalizeIngestionReceipt(dataDir, projectRoot, secret); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadIngestionReceipt(dataDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Success || receipt.Complete || !receipt.IngestionComplete || receipt.LastSuccess == nil || !receipt.LastSuccess.Equal(firstSuccess) {
		t.Fatalf("postflight failure receipt = %+v", receipt)
	}
	if len(receipt.Fallbacks) != 1 || receipt.Fallbacks[0].Code != "postflight_failed" {
		t.Fatalf("postflight fallback = %+v", receipt.Fallbacks)
	}
	path, _ := IngestionReceiptPath(dataDir, projectRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "do-not-store") {
		t.Fatal("postflight receipt persisted arbitrary error text")
	}
}

func TestLoadIngestionReceiptRejectsCorruptData(t *testing.T) {
	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	path, err := IngestionReceiptPath(dataDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIngestionReceipt(dataDir, projectRoot); err == nil || !strings.Contains(err.Error(), "decode ingestion receipt") {
		t.Fatalf("LoadIngestionReceipt error = %v", err)
	}
}

func TestIngestionReceiptIsolatesProjectsSharingDataDir(t *testing.T) {
	dataDir := t.TempDir()
	firstRoot := filepath.Join(t.TempDir(), "first")
	secondRoot := filepath.Join(t.TempDir(), "second")
	for _, root := range []string{firstRoot, secondRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setup := structuralChunksSetup{RequestedMode: StructuralChunksAuto}
	for root, localChunks := range map[string]int{firstRoot: 2, secondRoot: 7} {
		if err := recordIngestionReceipt(dataDir, setup, index.IndexRunReport{
			AttemptID:     startReceiptAttempt(t, dataDir, root, setup.RequestedMode, true),
			ScopeComplete: true,
			ProjectRoot:   root,
			FinishedAt:    time.Now(),
			Result: &index.IndexResult{Ingestion: index.IngestionCounts{
				Files:  index.OriginCounts{Local: 1},
				Chunks: index.OriginCounts{Local: localChunks},
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	firstPath, _ := IngestionReceiptPath(dataDir, firstRoot)
	secondPath, _ := IngestionReceiptPath(dataDir, secondRoot)
	if firstPath == secondPath {
		t.Fatal("project receipts share a path")
	}
	first, err := LoadIngestionReceipt(dataDir, firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadIngestionReceipt(dataDir, secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	if first.Counts.Chunks.Local != 2 || second.Counts.Chunks.Local != 7 || first.ProjectKey == second.ProjectKey {
		t.Fatalf("isolated receipts = first %+v second %+v", first, second)
	}
}

func TestPartialAttemptCannotCertifyGlobalStructuralFingerprint(t *testing.T) {
	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	setup := structuralChunksSetup{RequestedMode: StructuralChunksRequired, SourceEnabled: true}
	attemptID := startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, false)
	report := index.IndexRunReport{
		AttemptID:            attemptID,
		ScopeComplete:        false,
		ProjectRoot:          projectRoot,
		FinishedAt:           time.Now(),
		StructuralLoaded:     true,
		StructuralComplete:   true,
		StructuralProjectKey: "global-project-key",
		IndexFingerprint:     strings.Repeat("c", 64),
		Result: &index.IndexResult{Ingestion: index.IngestionCounts{
			Files:  index.OriginCounts{Structural: 1},
			Chunks: index.OriginCounts{Structural: 2},
		}},
	}
	if err := recordIngestionReceipt(dataDir, setup, report); err != nil {
		t.Fatal(err)
	}
	if err := finalizeIngestionReceiptAttempt(dataDir, projectRoot, attemptID, nil); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadIngestionReceipt(dataDir, projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ScopeComplete || receipt.IngestionComplete || receipt.Success || receipt.Complete {
		t.Fatalf("partial receipt certified completion: %+v", receipt)
	}
	if receipt.ProducerProjectKey != "" || receipt.IndexFingerprint != "" {
		t.Fatalf("partial receipt leaked global identity: %+v", receipt)
	}
	if len(receipt.Fallbacks) != 1 || receipt.Fallbacks[0].Code != "partial_scope" {
		t.Fatalf("partial receipt fallbacks = %+v", receipt.Fallbacks)
	}
}

func TestEffectiveIngestionModeKeepsNoOpStructuralSnapshot(t *testing.T) {
	report := index.IndexRunReport{
		StructuralLoaded: true,
		StructuralFiles:  3,
		Result:           &index.IndexResult{},
	}
	setup := structuralChunksSetup{RequestedMode: StructuralChunksRequired, SourceEnabled: true}
	if got := effectiveIngestionMode(setup, report); got != "structural" {
		t.Fatalf("effective mode for no-op structural pass = %q, want structural", got)
	}
}

func TestFinalizeRejectsSupersededAttempt(t *testing.T) {
	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	setup := structuralChunksSetup{RequestedMode: StructuralChunksOff}
	firstID := startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true)
	if err := recordIngestionReceipt(dataDir, setup, index.IndexRunReport{
		AttemptID:     firstID,
		ScopeComplete: true,
		ProjectRoot:   projectRoot,
		FinishedAt:    time.Now(),
		Result:        &index.IndexResult{},
	}); err != nil {
		t.Fatal(err)
	}

	secondID := startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true)
	err := finalizeIngestionReceiptAttempt(dataDir, projectRoot, firstID, nil)
	if !errors.Is(err, errIngestionAttemptSuperseded) {
		t.Fatalf("finalize error = %v, want superseded", err)
	}
	receipt, loadErr := LoadIngestionReceipt(dataDir, projectRoot)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if receipt.AttemptID != secondID || receipt.Success || receipt.Complete {
		t.Fatalf("newer attempt was modified: %+v", receipt)
	}
}

func TestRecordRejectsSupersededAttempt(t *testing.T) {
	dataDir := t.TempDir()
	projectRoot := t.TempDir()
	setup := structuralChunksSetup{RequestedMode: StructuralChunksOff}
	firstID := startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true)
	secondID := startReceiptAttempt(t, dataDir, projectRoot, setup.RequestedMode, true)

	err := recordIngestionReceipt(dataDir, setup, index.IndexRunReport{
		AttemptID:     firstID,
		ScopeComplete: true,
		ProjectRoot:   projectRoot,
		FinishedAt:    time.Now(),
		Result:        &index.IndexResult{},
	})
	if !errors.Is(err, errIngestionAttemptSuperseded) {
		t.Fatalf("record error = %v, want superseded", err)
	}
	receipt, loadErr := LoadIngestionReceipt(dataDir, projectRoot)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if receipt.AttemptID != secondID || receipt.Success || receipt.Complete || receipt.IngestionComplete {
		t.Fatalf("newer attempt was modified: %+v", receipt)
	}
}
