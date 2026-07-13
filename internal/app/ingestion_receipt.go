package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

const (
	ingestionReceiptSchemaVersion = 1
	ingestionReceiptMaxFallbacks  = 16
	ingestionAttemptBytes         = 16
)

var errIngestionAttemptSuperseded = errors.New("ingestion receipt attempt superseded")

// IngestionFallback is a bounded, non-sensitive explanation of why an
// indexing attempt used less structural information than requested.
type IngestionFallback struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
	Count  int    `json:"count,omitempty"`
}

// IngestionReceipt is vecgrep's durable, run-scoped account of the latest
// structural ingestion attempt. Counts describe chunks written by that
// attempt, not the entire index. ProducerVersion is omitted because the v1
// codemap export does not provide it; ContractVersion is known and explicit.
type IngestionReceipt struct {
	SchemaVersion      int                   `json:"schema_version"`
	AttemptID          string                `json:"attempt_id"`
	ScopeComplete      bool                  `json:"scope_complete"`
	ProjectKey         string                `json:"project_key"`
	RequestedMode      StructuralChunksMode  `json:"requested_mode"`
	EffectiveMode      string                `json:"effective_mode"`
	Producer           string                `json:"producer,omitempty"`
	ProducerVersion    string                `json:"producer_version,omitempty"`
	Contract           string                `json:"contract,omitempty"`
	ContractVersion    int                   `json:"contract_version,omitempty"`
	ProducerProjectKey string                `json:"producer_project_key,omitempty"`
	IndexFingerprint   string                `json:"index_fingerprint,omitempty"`
	Counts             index.IngestionCounts `json:"counts"`
	Fallbacks          []IngestionFallback   `json:"fallbacks,omitempty"`
	ErrorCount         int                   `json:"error_count,omitempty"`
	LastAttempt        time.Time             `json:"last_attempt"`
	LastSuccess        *time.Time            `json:"last_success,omitempty"`
	IngestionComplete  bool                  `json:"ingestion_complete"`
	Complete           bool                  `json:"complete"`
	Success            bool                  `json:"success"`
}

var ingestionReceiptLocks sync.Map

// IngestionReceiptPath returns the project-isolated receipt path without
// reading the vector store or asking codemap for a structural export.
func IngestionReceiptPath(dataDir, projectRoot string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", fmt.Errorf("ingestion receipt data dir is empty")
	}
	key, err := ingestionReceiptProjectKey(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(dataDir), "ingestion", key, "receipt.v1.json"), nil
}

// LoadIngestionReceipt reads and validates one project's receipt. A missing
// receipt is (nil, nil); malformed, unsupported, or cross-project data is an
// error so status can report unknown rather than trusting it.
func LoadIngestionReceipt(dataDir, projectRoot string) (*IngestionReceipt, error) {
	path, err := IngestionReceiptPath(dataDir, projectRoot)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ingestion receipt: %w", err)
	}
	var receipt IngestionReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("decode ingestion receipt: %w", err)
	}
	expectedKey, err := ingestionReceiptProjectKey(projectRoot)
	if err != nil {
		return nil, err
	}
	if err := validateIngestionReceipt(receipt, expectedKey); err != nil {
		return nil, err
	}
	return &receipt, nil
}

func ingestionReceiptProjectKey(projectRoot string) (string, error) {
	if strings.TrimSpace(projectRoot) == "" {
		return "", fmt.Errorf("ingestion receipt project root is empty")
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", fmt.Errorf("resolve ingestion receipt project root: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:]), nil
}

func validateIngestionReceipt(receipt IngestionReceipt, expectedKey string) error {
	if receipt.SchemaVersion != ingestionReceiptSchemaVersion {
		return fmt.Errorf("unsupported ingestion receipt schema %d", receipt.SchemaVersion)
	}
	if receipt.ProjectKey != expectedKey {
		return fmt.Errorf("ingestion receipt project key mismatch")
	}
	if !validIngestionAttemptID(receipt.AttemptID) {
		return fmt.Errorf("ingestion receipt attempt_id is invalid")
	}
	if _, err := ParseStructuralChunksMode(string(receipt.RequestedMode)); err != nil {
		return fmt.Errorf("invalid ingestion receipt requested mode: %w", err)
	}
	switch receipt.EffectiveMode {
	case "off", "local", "local_fallback", "structural", "mixed", "unavailable":
	default:
		return fmt.Errorf("invalid ingestion receipt effective mode %q", receipt.EffectiveMode)
	}
	if receipt.LastAttempt.IsZero() {
		return fmt.Errorf("ingestion receipt last_attempt is missing")
	}
	if receipt.ErrorCount < 0 || !validIngestionCounts(receipt.Counts) {
		return fmt.Errorf("ingestion receipt has invalid counts")
	}
	if len(receipt.Fallbacks) > ingestionReceiptMaxFallbacks {
		return fmt.Errorf("ingestion receipt has too many fallbacks")
	}
	for _, fallback := range receipt.Fallbacks {
		if fallback.Code == "" || fallback.Reason == "" || fallback.Count < 0 || len(fallback.Code) > 64 || len(fallback.Reason) > 256 {
			return fmt.Errorf("ingestion receipt has invalid fallback")
		}
	}
	if !receipt.ScopeComplete && (receipt.IngestionComplete || receipt.Complete || receipt.Success) {
		return fmt.Errorf("partial ingestion receipt cannot be complete")
	}
	return nil
}

func newIngestionAttemptID() (string, error) {
	random := make([]byte, ingestionAttemptBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate ingestion attempt id: %w", err)
	}
	return hex.EncodeToString(random), nil
}

func validIngestionAttemptID(attemptID string) bool {
	if len(attemptID) != ingestionAttemptBytes*2 {
		return false
	}
	decoded, err := hex.DecodeString(attemptID)
	return err == nil && len(decoded) == ingestionAttemptBytes
}

func validIngestionCounts(counts index.IngestionCounts) bool {
	values := []int{
		counts.Files.Structural, counts.Files.Gap, counts.Files.Local,
		counts.Chunks.Structural, counts.Chunks.Gap, counts.Chunks.Local,
	}
	for _, value := range values {
		if value < 0 {
			return false
		}
	}
	return true
}

func newIngestionReceiptObserver(dataDir string, setup structuralChunksSetup) index.IndexRunObserver {
	return func(report index.IndexRunReport) error {
		return recordIngestionReceipt(dataDir, setup, report)
	}
}

// beginIngestionReceiptAttempt durably invalidates the previous success before
// an index mutation is allowed to start. If this write fails, the coordinator
// must abort while the old searchable index is still untouched.
func beginIngestionReceiptAttempt(dataDir, projectRoot, attemptID string, requestedMode StructuralChunksMode, scopeComplete bool) error {
	if !validIngestionAttemptID(attemptID) {
		return fmt.Errorf("invalid ingestion attempt id")
	}
	path, err := IngestionReceiptPath(dataDir, projectRoot)
	if err != nil {
		return err
	}
	lockValue, _ := ingestionReceiptLocks.LoadOrStore(path, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	// A corrupt older receipt is already untrusted and must not prevent repair.
	// Preserve last_success only when the prior evidence validates completely.
	previous, _ := LoadIngestionReceipt(dataDir, projectRoot)
	projectKey, err := ingestionReceiptProjectKey(projectRoot)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	receipt := IngestionReceipt{
		SchemaVersion: ingestionReceiptSchemaVersion,
		AttemptID:     attemptID,
		ScopeComplete: scopeComplete,
		ProjectKey:    projectKey,
		RequestedMode: requestedMode,
		EffectiveMode: "unavailable",
		LastAttempt:   now,
	}
	if requestedMode == StructuralChunksOff {
		receipt.EffectiveMode = "off"
	} else {
		receipt.Producer = "codemap"
		receipt.Contract = "codemap.structural-export"
		receipt.ContractVersion = structuralExportSchemaVersion
	}
	if previous != nil && previous.LastSuccess != nil {
		last := previous.LastSuccess.UTC()
		receipt.LastSuccess = &last
	}
	if !scopeComplete {
		receipt.addFallback(IngestionFallback{
			Code:   "partial_scope",
			Reason: "a path-scoped attempt cannot certify the complete project index",
		})
	}
	return writeIngestionReceiptAtomic(path, receipt)
}

func recordIngestionReceipt(dataDir string, setup structuralChunksSetup, report index.IndexRunReport) error {
	if !validIngestionAttemptID(report.AttemptID) {
		return fmt.Errorf("ingestion attempt was not durably started")
	}
	path, err := IngestionReceiptPath(dataDir, report.ProjectRoot)
	if err != nil {
		return err
	}
	lockValue, _ := ingestionReceiptLocks.LoadOrStore(path, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	previous, err := LoadIngestionReceipt(dataDir, report.ProjectRoot)
	if err != nil {
		return err
	}
	if previous == nil || previous.AttemptID != report.AttemptID {
		return fmt.Errorf("%w: expected %s", errIngestionAttemptSuperseded, report.AttemptID)
	}
	receipt, err := buildIngestionReceipt(setup, report, previous)
	if err != nil {
		return err
	}
	return writeIngestionReceiptAtomic(path, receipt)
}

func buildIngestionReceipt(setup structuralChunksSetup, report index.IndexRunReport, previous *IngestionReceipt) (IngestionReceipt, error) {
	projectKey, err := ingestionReceiptProjectKey(report.ProjectRoot)
	if err != nil {
		return IngestionReceipt{}, err
	}
	attemptedAt := report.FinishedAt.UTC()
	if attemptedAt.IsZero() {
		attemptedAt = time.Now().UTC()
	}
	receipt := IngestionReceipt{
		SchemaVersion: ingestionReceiptSchemaVersion,
		AttemptID:     report.AttemptID,
		ScopeComplete: report.ScopeComplete,
		ProjectKey:    projectKey,
		RequestedMode: setup.RequestedMode,
		EffectiveMode: effectiveIngestionMode(setup, report),
		LastAttempt:   attemptedAt,
	}
	// Project key/fingerprint identify a complete producer snapshot. A path-only
	// run may consume records from that snapshot, but it did not apply every
	// per-file structural profile and therefore must never certify it globally.
	if report.ScopeComplete {
		receipt.ProducerProjectKey = report.StructuralProjectKey
		receipt.IndexFingerprint = report.IndexFingerprint
	}
	if setup.RequestedMode != StructuralChunksOff {
		receipt.Producer = "codemap"
		receipt.Contract = "codemap.structural-export"
		receipt.ContractVersion = structuralExportSchemaVersion
	}
	if report.Result != nil {
		receipt.Counts = report.Result.Ingestion
		receipt.ErrorCount = len(report.Result.Errors)
		if report.StructuralWarning && receipt.ErrorCount > 0 {
			receipt.ErrorCount--
		}
	}
	// This observer runs when the indexing pipeline finishes, before app's
	// provider/profile postflight. Record the completed ingestion phase, but do
	// not advance success/last_success until FinalizeIngestionReceipt confirms
	// the searchable index is ready.
	receipt.IngestionComplete = report.ScopeComplete && report.Err == nil && receipt.ErrorCount == 0
	receipt.Success = false
	receipt.Complete = false
	if previous != nil && previous.LastSuccess != nil {
		last := previous.LastSuccess.UTC()
		receipt.LastSuccess = &last
	}
	if setup.FallbackCode != "" {
		receipt.addFallback(IngestionFallback{Code: setup.FallbackCode, Reason: setup.FallbackReason})
	}
	if report.StructuralWarning {
		if report.StructuralLoaded {
			receipt.addFallback(IngestionFallback{
				Code:   "partial_structural_snapshot",
				Reason: "some files used the local chunker after structural validation",
				Count:  report.StructuralIssues,
			})
		} else {
			receipt.addFallback(IngestionFallback{
				Code:   "producer_error",
				Reason: "the codemap export was unavailable and the local chunker was used",
			})
		}
	}
	if report.StructuralLoaded && receipt.Counts.Files.Local > 0 {
		receipt.addFallback(IngestionFallback{
			Code:   "local_chunker_used",
			Reason: "files without usable structural records used the local chunker",
			Count:  receipt.Counts.Files.Local,
		})
	}
	if !report.ScopeComplete {
		receipt.addFallback(IngestionFallback{
			Code:   "partial_scope",
			Reason: "a path-scoped attempt cannot certify the complete project index",
		})
	}
	if report.Err != nil {
		code, reason := ingestionFailure(report.FailureStage)
		receipt.addFallback(IngestionFallback{Code: code, Reason: reason})
		if receipt.ErrorCount == 0 {
			receipt.ErrorCount = 1
		}
	}
	return receipt, nil
}

// FinalizeIngestionReceipt completes the two-phase receipt after app has
// flushed the provider, persisted the embedding profile, and completed any
// final storage sync. Arbitrary postflight errors are never persisted; only a
// bounded reason code is recorded. A failed postflight preserves last_success.
func FinalizeIngestionReceipt(dataDir, projectRoot string, postErr error) error {
	receipt, err := LoadIngestionReceipt(dataDir, projectRoot)
	if err != nil {
		return err
	}
	if receipt == nil {
		return fmt.Errorf("ingestion receipt is missing")
	}
	return finalizeIngestionReceiptAttempt(dataDir, projectRoot, receipt.AttemptID, postErr)
}

func finalizeIngestionReceiptAttempt(dataDir, projectRoot, attemptID string, postErr error) error {
	if !validIngestionAttemptID(attemptID) {
		return fmt.Errorf("invalid ingestion attempt id")
	}
	path, err := IngestionReceiptPath(dataDir, projectRoot)
	if err != nil {
		return err
	}
	lockValue, _ := ingestionReceiptLocks.LoadOrStore(path, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	receipt, err := LoadIngestionReceipt(dataDir, projectRoot)
	if err != nil {
		return err
	}
	if receipt == nil {
		return fmt.Errorf("ingestion receipt is missing")
	}
	if receipt.AttemptID != attemptID {
		return fmt.Errorf("%w: expected %s, found %s", errIngestionAttemptSuperseded, attemptID, receipt.AttemptID)
	}
	if postErr != nil {
		receipt.Success = false
		receipt.Complete = false
		if receipt.ErrorCount == 0 {
			receipt.ErrorCount = 1
		}
		receipt.addFallback(IngestionFallback{
			Code:   "postflight_failed",
			Reason: "the indexed data did not complete application postflight",
		})
		return writeIngestionReceiptAtomic(path, *receipt)
	}
	if !receipt.ScopeComplete || !receipt.IngestionComplete {
		return writeIngestionReceiptAtomic(path, *receipt)
	}
	successAt := receipt.LastAttempt.UTC()
	receipt.LastSuccess = &successAt
	receipt.Success = true
	receipt.Complete = true
	return writeIngestionReceiptAtomic(path, *receipt)
}

// invalidateIngestionReceiptAttempt makes a failed attempt durably untrusted.
// It is used when receipt finalization or DB release fails after a successful
// pipeline. If rewriting the receipt itself fails, removing it is a safe final
// fallback: missing evidence degrades freshness to unknown.
func invalidateIngestionReceiptAttempt(dataDir, projectRoot, attemptID string) error {
	path, err := IngestionReceiptPath(dataDir, projectRoot)
	if err != nil {
		return err
	}
	lockValue, _ := ingestionReceiptLocks.LoadOrStore(path, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	receipt, loadErr := LoadIngestionReceipt(dataDir, projectRoot)
	if loadErr != nil {
		removeErr := os.Remove(path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(loadErr, removeErr)
	}
	if receipt == nil {
		return nil
	}
	if receipt.AttemptID != attemptID {
		return fmt.Errorf("%w: expected %s, found %s", errIngestionAttemptSuperseded, attemptID, receipt.AttemptID)
	}
	receipt.Success = false
	receipt.Complete = false
	receipt.IngestionComplete = false
	if receipt.ErrorCount == 0 {
		receipt.ErrorCount = 1
	}
	receipt.addFallback(IngestionFallback{
		Code:   "receipt_persistence_failed",
		Reason: "durable ingestion evidence did not complete",
	})
	if writeErr := writeIngestionReceiptAtomic(path, *receipt); writeErr != nil {
		removeErr := os.Remove(path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		return errors.Join(writeErr, removeErr)
	}
	return nil
}

func effectiveIngestionMode(setup structuralChunksSetup, report index.IndexRunReport) string {
	if setup.RequestedMode == StructuralChunksOff {
		return "off"
	}
	if report.Err != nil && report.Result == nil {
		return "unavailable"
	}
	if report.StructuralLoaded {
		if report.Result != nil && report.Result.Ingestion.Chunks.Local > 0 && (report.Result.Ingestion.Chunks.Structural > 0 || report.Result.Ingestion.Chunks.Gap > 0) {
			return "mixed"
		}
		if report.Result != nil && (report.Result.Ingestion.Chunks.Structural > 0 || report.Result.Ingestion.Chunks.Gap > 0) {
			return "structural"
		}
		// A full incremental pass may validate the complete structural snapshot
		// while every file is unchanged. Zero run-scoped writes do not turn the
		// already-structural index into a local one.
		if report.StructuralFiles > 0 && report.Result != nil && report.Result.Ingestion == (index.IngestionCounts{}) {
			return "structural"
		}
		return "local"
	}
	if setup.SourceEnabled && report.StructuralWarning {
		return "local_fallback"
	}
	return "local"
}

func ingestionFailure(stage string) (string, string) {
	switch stage {
	case "structural_load":
		return "structural_load_failed", "the structural producer could not be loaded"
	case "structural_preflight":
		return "structural_preflight_failed", "required structural validation failed before indexing"
	case "storage_reset":
		return "storage_reset_failed", "the project index could not be reset"
	case "project_root":
		return "project_root_failed", "the project root could not be resolved"
	default:
		return "index_failed", "the indexing attempt did not complete"
	}
}

func (r *IngestionReceipt) addFallback(fallback IngestionFallback) {
	if r == nil || fallback.Code == "" || len(r.Fallbacks) >= ingestionReceiptMaxFallbacks {
		return
	}
	for i := range r.Fallbacks {
		if r.Fallbacks[i].Code == fallback.Code {
			r.Fallbacks[i].Count += fallback.Count
			return
		}
	}
	r.Fallbacks = append(r.Fallbacks, fallback)
}

func writeIngestionReceiptAtomic(path string, receipt IngestionReceipt) (retErr error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create ingestion receipt directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("create ingestion receipt temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure ingestion receipt temp file: %w", err)
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(receipt); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode ingestion receipt: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync ingestion receipt: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close ingestion receipt: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace ingestion receipt: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open ingestion receipt directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync ingestion receipt directory: %w", err)
	}
	return nil
}
