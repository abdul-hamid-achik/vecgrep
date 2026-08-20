package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexHealthManifestRoundTripIsProjectScoped(t *testing.T) {
	dataDir := t.TempDir()
	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	attemptID := "00112233445566778899aabbccddeeff"
	manifest := &IndexHealthManifest{
		SchemaVersion: indexHealthManifestSchemaVersion,
		AttemptID:     attemptID,
		Stats:         map[string]int64{"files": 2, "chunks": 9},
		SourceHashes:  map[string]string{"main.go": "sha256:one", "internal/app.go": "sha256:two"},
		UpdatedAt:     time.Now().UTC(),
	}
	key, err := ingestionReceiptProjectKey(firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest.ProjectKey = key

	if err := WriteIndexHealthManifest(dataDir, firstRoot, manifest); err != nil {
		t.Fatalf("WriteIndexHealthManifest() error = %v", err)
	}
	loaded, err := LoadIndexHealthManifest(dataDir, firstRoot)
	if err != nil {
		t.Fatalf("LoadIndexHealthManifest() error = %v", err)
	}
	if loaded == nil || loaded.AttemptID != attemptID || loaded.Stats["chunks"] != 9 || loaded.SourceHashes["main.go"] != "sha256:one" {
		t.Fatalf("loaded manifest = %#v", loaded)
	}
	other, err := LoadIndexHealthManifest(dataDir, secondRoot)
	if err != nil {
		t.Fatalf("LoadIndexHealthManifest(other) error = %v", err)
	}
	if other != nil {
		t.Fatalf("manifest leaked across projects: %#v", other)
	}
}

func TestLoadIndexHealthManifestRejectsCorruptAndUnsafeData(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	path, err := IndexHealthManifestPath(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIndexHealthManifest(dataDir, root); err == nil {
		t.Fatal("LoadIndexHealthManifest() accepted an incomplete manifest")
	}

	key, err := ingestionReceiptProjectKey(root)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := &IndexHealthManifest{
		SchemaVersion: indexHealthManifestSchemaVersion,
		AttemptID:     "00112233445566778899aabbccddeeff",
		ProjectKey:    key,
		Stats:         map[string]int64{"files": 1},
		SourceHashes:  map[string]string{"../secret": "sha256:bad"},
		UpdatedAt:     time.Now().UTC(),
	}
	if err := WriteIndexHealthManifest(dataDir, root, unsafe); err == nil {
		t.Fatal("WriteIndexHealthManifest() accepted path traversal")
	}
}

func TestLightweightStatusDoesNotNeedVecLite(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, ".vecgrep")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "vecgrep.yaml"), []byte("data_dir: .vecgrep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := LightweightStatus(t.Context(), root)
	if err != nil {
		t.Fatalf("LightweightStatus() error = %v", err)
	}
	if !status.Lightweight || status.IndexFresh || status.Freshness == nil || status.Freshness.Reason != "index_health_manifest_missing" {
		t.Fatalf("LightweightStatus() = %#v", status)
	}
}

func TestLightweightStatusVerifiesVectorFreeManifest(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, ".vecgrep")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const attemptID = "00112233445566778899aabbccddeeff"
	if err := beginIngestionReceiptAttempt(dataDir, root, attemptID, StructuralChunksOff, true); err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadIngestionReceipt(dataDir, root)
	if err != nil || receipt == nil {
		t.Fatalf("load receipt = %#v, error = %v", receipt, err)
	}
	receipt.IngestionComplete = true
	path, err := IngestionReceiptPath(dataDir, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIngestionReceiptAtomic(path, *receipt); err != nil {
		t.Fatal(err)
	}
	if err := finalizeIngestionReceiptAttempt(dataDir, root, attemptID, nil); err != nil {
		t.Fatal(err)
	}
	key, err := ingestionReceiptProjectKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteIndexHealthManifest(dataDir, root, &IndexHealthManifest{
		SchemaVersion: indexHealthManifestSchemaVersion,
		AttemptID:     attemptID,
		ProjectKey:    key,
		Stats:         map[string]int64{"files": 0, "chunks": 0},
		SourceHashes:  map[string]string{},
		UpdatedAt:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	status, err := LightweightStatus(t.Context(), root)
	if err != nil {
		t.Fatalf("LightweightStatus() error = %v", err)
	}
	if !status.IndexFresh || status.Freshness == nil || status.Freshness.State != IndexFreshnessFresh || status.PendingChanges == nil {
		t.Fatalf("LightweightStatus() = %#v", status)
	}
	if err := InvalidateIndexHealthEvidence(dataDir, root); err != nil {
		t.Fatal(err)
	}
	status, err = LightweightStatus(t.Context(), root)
	if err != nil {
		t.Fatalf("LightweightStatus() after invalidation error = %v", err)
	}
	if status.IndexFresh || status.Freshness == nil || status.Freshness.Reason != "index_health_manifest_missing" {
		t.Fatalf("status after invalidation = %#v", status)
	}
}
