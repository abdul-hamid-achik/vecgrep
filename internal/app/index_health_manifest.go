package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

const indexHealthManifestSchemaVersion = 1

// IndexHealthManifest is the small, vector-free projection of a successful
// full-project index. It exists so health checks can compare source hashes
// without opening VecLite and materializing its HNSW graph.
//
// The manifest is never authoritative by itself: status also requires a
// matching successful ingestion receipt and attempt ID. This makes a crash
// between manifest publication and receipt finalization fail closed.
type IndexHealthManifest struct {
	SchemaVersion   int               `json:"schema_version"`
	AttemptID       string            `json:"attempt_id"`
	ProjectKey      string            `json:"project_key"`
	Stats           map[string]int64  `json:"stats"`
	IndexedBytes    int64             `json:"indexed_bytes"`
	LatestIndexedAt time.Time         `json:"latest_indexed_at,omitempty"`
	SourceHashes    map[string]string `json:"source_hashes"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

// IndexHealthManifestPath returns the project-isolated health manifest path.
func IndexHealthManifestPath(dataDir, projectRoot string) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", fmt.Errorf("index health manifest data dir is empty")
	}
	key, err := ingestionReceiptProjectKey(projectRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(dataDir), "health", key, "manifest.v1.json"), nil
}

// LoadIndexHealthManifest reads and validates one project's health manifest.
// A missing manifest is normal for legacy indexes and returns (nil, nil).
func LoadIndexHealthManifest(dataDir, projectRoot string) (*IndexHealthManifest, error) {
	path, err := IndexHealthManifestPath(dataDir, projectRoot)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read index health manifest: %w", err)
	}
	var manifest IndexHealthManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode index health manifest: %w", err)
	}
	key, err := ingestionReceiptProjectKey(projectRoot)
	if err != nil {
		return nil, err
	}
	if err := validateIndexHealthManifest(manifest, key); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func validateIndexHealthManifest(manifest IndexHealthManifest, projectKey string) error {
	if manifest.SchemaVersion != indexHealthManifestSchemaVersion {
		return fmt.Errorf("unsupported index health manifest schema %d", manifest.SchemaVersion)
	}
	if manifest.ProjectKey != projectKey {
		return fmt.Errorf("index health manifest project key mismatch")
	}
	if !validIngestionAttemptID(manifest.AttemptID) {
		return fmt.Errorf("index health manifest attempt_id is invalid")
	}
	if manifest.UpdatedAt.IsZero() {
		return fmt.Errorf("index health manifest updated_at is missing")
	}
	if manifest.IndexedBytes < 0 {
		return fmt.Errorf("index health manifest indexed_bytes is negative")
	}
	for name, count := range manifest.Stats {
		if name == "" || count < 0 {
			return fmt.Errorf("index health manifest has invalid stat")
		}
	}
	if manifest.SourceHashes == nil {
		return fmt.Errorf("index health manifest source_hashes are missing")
	}
	for relativePath, hash := range manifest.SourceHashes {
		if !validRelativeHealthPath(relativePath) || strings.TrimSpace(hash) == "" {
			return fmt.Errorf("index health manifest has invalid source hash entry")
		}
	}
	return nil
}

func validRelativeHealthPath(path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(path)
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// BuildIndexHealthManifest snapshots vector-free index metadata while the
// database is already open after a successful indexing pass.
func BuildIndexHealthManifest(database *db.DB, projectRoot, attemptID string) (*IndexHealthManifest, error) {
	if database == nil {
		return nil, fmt.Errorf("index health manifest database is nil")
	}
	if !validIngestionAttemptID(attemptID) {
		return nil, fmt.Errorf("index health manifest attempt_id is invalid")
	}
	projectKey, err := ingestionReceiptProjectKey(projectRoot)
	if err != nil {
		return nil, err
	}
	stats, err := database.StatsForProject(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("get index health stats: %w", err)
	}
	sourceHashes, complete, err := database.GetSourceHashes(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("get index health source hashes: %w", err)
	}
	if !complete {
		return nil, fmt.Errorf("index health source hashes are incomplete")
	}
	files, err := database.ListFiles(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("get index health file metadata: %w", err)
	}
	var indexedBytes int64
	var latestIndexedAt time.Time
	for _, file := range files {
		indexedBytes += file.Size
		if file.IndexedAt.After(latestIndexedAt) {
			latestIndexedAt = file.IndexedAt
		}
	}
	if stats == nil {
		stats = map[string]int64{}
	}
	return &IndexHealthManifest{
		SchemaVersion:   indexHealthManifestSchemaVersion,
		AttemptID:       attemptID,
		ProjectKey:      projectKey,
		Stats:           cloneHealthStats(stats),
		IndexedBytes:    indexedBytes,
		LatestIndexedAt: latestIndexedAt,
		SourceHashes:    cloneSourceHashes(sourceHashes),
		UpdatedAt:       time.Now().UTC(),
	}, nil
}

// WriteIndexHealthManifest atomically publishes a validated manifest.
func WriteIndexHealthManifest(dataDir, projectRoot string, manifest *IndexHealthManifest) error {
	if manifest == nil {
		return fmt.Errorf("index health manifest is nil")
	}
	path, err := IndexHealthManifestPath(dataDir, projectRoot)
	if err != nil {
		return err
	}
	key, err := ingestionReceiptProjectKey(projectRoot)
	if err != nil {
		return err
	}
	if err := validateIndexHealthManifest(*manifest, key); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create index health manifest directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create index health manifest temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("secure index health manifest temp file: %w", err)
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode index health manifest: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync index health manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close index health manifest: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace index health manifest: %w", err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open index health manifest directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync index health manifest directory: %w", err)
	}
	return nil
}

// InvalidateIndexHealthEvidence removes the vector-free proof after a direct
// mutation (delete/reset) that is not followed by a complete indexing pass.
// Missing evidence is safer than allowing an old manifest to describe a
// database that has already changed.
func InvalidateIndexHealthEvidence(dataDir, projectRoot string) error {
	healthPath, err := IndexHealthManifestPath(dataDir, projectRoot)
	if err != nil {
		return err
	}
	receiptPath, err := IngestionReceiptPath(dataDir, projectRoot)
	if err != nil {
		return err
	}
	var errs []error
	for _, path := range []string{healthPath, receiptPath} {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("remove index health evidence %s: %w", path, removeErr))
		}
	}
	return errors.Join(errs...)
}

// InvalidateAllIndexHealthEvidence clears every project-scoped proof under a
// data directory after a reset-all operation. It intentionally removes only
// the health and ingestion evidence trees, never the vector database itself.
func InvalidateAllIndexHealthEvidence(dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return fmt.Errorf("index health evidence data dir is empty")
	}
	var errs []error
	for _, name := range []string{"health", "ingestion"} {
		if err := os.RemoveAll(filepath.Join(filepath.Clean(dataDir), name)); err != nil {
			errs = append(errs, fmt.Errorf("remove all index health evidence %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func cloneHealthStats(stats map[string]int64) map[string]int64 {
	copyOf := make(map[string]int64, len(stats))
	for name, count := range stats {
		copyOf[name] = count
	}
	return copyOf
}

func cloneSourceHashes(hashes map[string]string) map[string]string {
	copyOf := make(map[string]string, len(hashes))
	for path, hash := range hashes {
		copyOf[path] = hash
	}
	return copyOf
}
