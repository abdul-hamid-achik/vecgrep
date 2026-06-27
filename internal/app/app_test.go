package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	vlsession "github.com/abdul-hamid-achik/veclite/session"
)

func createTestSession(t *testing.T) (*Session, *Service) {
	t.Helper()

	projectRoot := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(projectRoot, config.DefaultDataDir)
	cfg.DBPath = filepath.Join(cfg.DataDir, config.DefaultDBFile)
	if err := cfg.EnsureDataDir(); err != nil {
		t.Fatalf("EnsureDataDir failed: %v", err)
	}
	if err := cfg.WriteDefaultConfig(); err != nil {
		t.Fatalf("WriteDefaultConfig failed: %v", err)
	}

	session, err := OpenSession(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return session, NewService(session)
}

func TestServiceKeywordSearchDoesNotRequireProvider(t *testing.T) {
	session, service := createTestSession(t)
	session.Provider = nil

	chunk := db.NewChunkRecord(
		filepath.Join(session.ProjectRoot, "main.go"),
		"main.go",
		"hash",
		64,
		"go",
		"func LoadConfig() error { return nil }",
		1, 1, 0, 36,
		"function",
		"LoadConfig",
		session.ProjectRoot,
	)
	if _, err := session.DB.InsertChunk(chunk, make([]float32, session.Config.Embedding.Dimensions)); err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	resp, err := service.Search(context.Background(), SearchRequest{
		Query: "LoadConfig",
		Mode:  search.SearchModeKeyword,
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if resp.Results[0].RelativePath != "main.go" {
		t.Fatalf("unexpected result path: %s", resp.Results[0].RelativePath)
	}
}

func TestServiceSemanticSearchRejectsExistingIndexWithoutProfile(t *testing.T) {
	session, service := createTestSession(t)
	session.Provider = fakeProvider{dimensions: session.Config.Embedding.Dimensions, model: session.Config.Embedding.Model}

	chunk := db.NewChunkRecord(
		filepath.Join(session.ProjectRoot, "main.go"),
		"main.go",
		"hash",
		64,
		"go",
		"func LoadConfig() error { return nil }",
		1, 1, 0, 36,
		"function",
		"LoadConfig",
		session.ProjectRoot,
	)
	if _, err := session.DB.InsertChunk(chunk, make([]float32, session.Config.Embedding.Dimensions)); err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	_, err := service.Search(context.Background(), SearchRequest{
		Query: "LoadConfig",
		Mode:  search.SearchModeSemantic,
		Limit: 5,
	})
	if !errors.Is(err, ErrEmbeddingProfileMismatch) {
		t.Fatalf("expected embedding profile mismatch, got %v", err)
	}
}

func TestServiceIndexRejectsMismatchedEmbeddingProfile(t *testing.T) {
	session, service := createTestSession(t)
	session.Provider = fakeProvider{dimensions: session.Config.Embedding.Dimensions, model: session.Config.Embedding.Model}

	stored := CurrentEmbeddingProfile(session.Config)
	stored.Model = "other-model"
	stored.ProfileID = "ollama:other-model:768:cosine:code-chunker-v1"
	if err := SaveEmbeddingProfile(session.DB, session.Config.DataDir, stored); err != nil {
		t.Fatalf("SaveEmbeddingProfile failed: %v", err)
	}

	_, err := service.Index(context.Background(), IndexRequest{}, nil)
	if !errors.Is(err, ErrEmbeddingProfileMismatch) {
		t.Fatalf("expected embedding profile mismatch, got %v", err)
	}
}

func TestServiceFullIndexWritesEmbeddingProfile(t *testing.T) {
	session, service := createTestSession(t)
	session.Provider = fakeProvider{dimensions: session.Config.Embedding.Dimensions, model: session.Config.Embedding.Model}
	if err := os.WriteFile(filepath.Join(session.ProjectRoot, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write source failed: %v", err)
	}

	if _, err := service.Index(context.Background(), IndexRequest{FullReindex: true}, nil); err != nil {
		t.Fatalf("full index failed: %v", err)
	}

	profile, err := LoadEmbeddingProfile(session.DB, session.Config.DataDir)
	if err != nil {
		t.Fatalf("LoadEmbeddingProfile failed: %v", err)
	}
	if profile == nil {
		t.Fatal("embedding profile was not written")
	}
	if !profile.Matches(CurrentEmbeddingProfile(session.Config)) {
		t.Fatalf("profile = %+v, want current %+v", profile, CurrentEmbeddingProfile(session.Config))
	}
}

func TestOpenSessionDetectsLegacySQLiteWithoutVecLite(t *testing.T) {
	projectRoot := t.TempDir()
	dataDir := filepath.Join(projectRoot, config.DefaultDataDir)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, config.DefaultConfigFile), []byte("embedding:\n  dimensions: 768\n"), 0644); err != nil {
		t.Fatalf("write config failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, config.DefaultDBFile), []byte("legacy sqlite sentinel"), 0644); err != nil {
		t.Fatalf("write legacy db failed: %v", err)
	}

	_, err := OpenSession(context.Background(), projectRoot)
	if err == nil {
		t.Fatal("expected migration error")
	}
	if !IsMigrationRequired(err) {
		t.Fatalf("expected ErrMigrationRequired, got %v", err)
	}
}

func TestResetIndexFilesRecreatesCorruptVecLiteIndex(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(projectRoot, config.DefaultDataDir)
	cfg.DBPath = filepath.Join(cfg.DataDir, config.DefaultDBFile)
	if err := cfg.EnsureDataDir(); err != nil {
		t.Fatalf("EnsureDataDir failed: %v", err)
	}
	if err := cfg.WriteDefaultConfig(); err != nil {
		t.Fatalf("WriteDefaultConfig failed: %v", err)
	}

	vecPath := db.VecLitePath(cfg.DataDir)
	if err := os.WriteFile(vecPath, []byte("not a veclite database"), 0644); err != nil {
		t.Fatalf("write corrupt veclite failed: %v", err)
	}

	result, err := ResetIndexFiles(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("ResetIndexFiles failed: %v", err)
	}
	if result.ProjectRoot != projectRoot {
		t.Fatalf("project root = %s, want %s", result.ProjectRoot, projectRoot)
	}
	if result.VecLitePath != vecPath {
		t.Fatalf("veclite path = %s, want %s", result.VecLitePath, vecPath)
	}

	session, err := OpenSession(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("OpenSession after reset failed: %v", err)
	}
	_ = session.Close()
}

func TestOpenSessionCorruptVecLiteIncludesRecoveryHint(t *testing.T) {
	projectRoot := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(projectRoot, config.DefaultDataDir)
	cfg.DBPath = filepath.Join(cfg.DataDir, config.DefaultDBFile)
	if err := cfg.EnsureDataDir(); err != nil {
		t.Fatalf("EnsureDataDir failed: %v", err)
	}
	if err := cfg.WriteDefaultConfig(); err != nil {
		t.Fatalf("WriteDefaultConfig failed: %v", err)
	}

	if err := os.WriteFile(db.VecLitePath(cfg.DataDir), []byte("not a veclite database"), 0644); err != nil {
		t.Fatalf("write corrupt veclite failed: %v", err)
	}

	_, err := OpenSession(context.Background(), projectRoot)
	if err == nil {
		t.Fatal("expected open error")
	}
	if !strings.Contains(err.Error(), "vecgrep reset --force") {
		t.Fatalf("recovery hint missing: %v", err)
	}
}

func TestOpenErrorHintDistinguishesLiveLockFromStaleIndex(t *testing.T) {
	// A live file-lock (another running vecgrep process) must NOT suggest the
	// destructive `reset --force` — that is only for an old-version/corrupt
	// index. veclite already auto-clears locks from dead processes, so a
	// surfaced ErrFileLocked always means a live holder.
	locked := openErrorHint(vlsession.ErrFileLocked)
	if !errors.Is(locked, vlsession.ErrFileLocked) {
		t.Fatalf("lock hint should still wrap ErrFileLocked: %v", locked)
	}
	if strings.Contains(locked.Error(), "reset --force") {
		t.Fatalf("live lock must not suggest reset --force: %v", locked)
	}
	if !strings.Contains(locked.Error(), "holds the index lock") {
		t.Fatalf("live lock hint should explain another process holds the lock: %v", locked)
	}

	// Any other open failure keeps the old-version recovery hint.
	other := openErrorHint(errors.New("not a veclite database"))
	if !strings.Contains(other.Error(), "reset --force") {
		t.Fatalf("non-lock open error should keep the recovery hint: %v", other)
	}
}

func TestInitGlobalProjectCreatesOpenableSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectRoot := t.TempDir()

	result, err := InitGlobalProject(context.Background(), projectRoot, false)
	if err != nil {
		t.Fatalf("InitGlobalProject failed: %v", err)
	}
	if result.ProjectRoot != projectRoot {
		t.Fatalf("project root = %s, want %s", result.ProjectRoot, projectRoot)
	}
	if !result.Global {
		t.Fatal("project should be registered in global storage")
	}
	wantDataPrefix := filepath.Join(home, ".vecgrep", "projects")
	if !strings.HasPrefix(result.DataDir, wantDataPrefix) {
		t.Fatalf("data dir = %s, want under %s", result.DataDir, wantDataPrefix)
	}
	if result.VectorBackend == "" {
		t.Fatal("vector backend should be reported")
	}
	if _, err := os.Stat(filepath.Join(home, ".vecgrep", "config.yaml")); err != nil {
		t.Fatalf("global config file was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, config.DefaultDataDir)); !os.IsNotExist(err) {
		t.Fatalf("local .vecgrep should not be created by global init, stat err: %v", err)
	}

	session, err := OpenSession(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("OpenSession after init failed: %v", err)
	}
	_ = session.Close()
}

func TestInitLocalProjectCreatesRepoLocalSession(t *testing.T) {
	projectRoot := t.TempDir()

	result, err := InitLocalProject(context.Background(), projectRoot, false)
	if err != nil {
		t.Fatalf("InitLocalProject failed: %v", err)
	}
	if result.Global {
		t.Fatal("local init should not be marked global")
	}
	if result.DataDir != filepath.Join(projectRoot, config.DefaultDataDir) {
		t.Fatalf("data dir = %s", result.DataDir)
	}
	if _, err := os.Stat(filepath.Join(result.DataDir, config.DefaultConfigFile)); err != nil {
		t.Fatalf("local config file was not created: %v", err)
	}
}

func TestEmbeddingProfileSidecarMigrationToMetadata(t *testing.T) {
	session, _ := createTestSession(t)

	// Simulate a pre-migration index by writing the legacy sidecar directly
	// and clearing any collection metadata that the session open may have set.
	profile := CurrentEmbeddingProfile(session.Config)
	if err := saveSidecarProfile(session.Config.DataDir, profile); err != nil {
		t.Fatalf("saveSidecarProfile failed: %v", err)
	}
	if _, ok := session.DB.CollectionMetadataValue(embeddingProfileMetaKey); ok {
		t.Fatalf("expected no metadata profile before migration, found one")
	}

	// Loading should transparently migrate the sidecar into collection metadata.
	loaded, err := LoadEmbeddingProfile(session.DB, session.Config.DataDir)
	if err != nil {
		t.Fatalf("LoadEmbeddingProfile failed: %v", err)
	}
	if loaded == nil || !loaded.Matches(profile) {
		t.Fatalf("loaded profile = %+v, want %+v", loaded, profile)
	}

	// The sidecar should be gone and the metadata key should now hold the profile.
	if _, err := os.Stat(EmbeddingProfilePath(session.Config.DataDir)); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar removed after migration, stat err: %v", err)
	}
	raw, ok := session.DB.CollectionMetadataValue(embeddingProfileMetaKey)
	if !ok {
		t.Fatalf("expected profile in collection metadata after migration")
	}
	parsed, err := decodeProfile(raw)
	if err != nil || parsed == nil || !parsed.Matches(profile) {
		t.Fatalf("metadata profile = %+v, err = %v, want %+v", parsed, err, profile)
	}
}

func TestEmbeddingProfileSurvivesReopen(t *testing.T) {
	session, _ := createTestSession(t)
	dataDir := session.Config.DataDir
	projectRoot := session.ProjectRoot

	// Save a profile via the service (as Index does).
	profile := CurrentEmbeddingProfile(session.Config)
	if err := SaveEmbeddingProfile(session.DB, dataDir, profile); err != nil {
		t.Fatalf("SaveEmbeddingProfile failed: %v", err)
	}
	// Sync so the metadata hits disk.
	if err := session.DB.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	// Close the session and reopen the same data dir.
	if err := session.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	session2, err := OpenSession(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("OpenSession on reopen failed: %v", err)
	}
	t.Cleanup(func() { _ = session2.Close() })

	// The profile must still be present in collection metadata after reload.
	loaded, err := LoadEmbeddingProfile(session2.DB, dataDir)
	if err != nil {
		t.Fatalf("LoadEmbeddingProfile after reopen: %v", err)
	}
	if loaded == nil {
		t.Fatal("profile missing from collection metadata after reopen (gob round-trip failed)")
	}
	if !loaded.Matches(profile) {
		t.Fatalf("profile mismatch after reopen: got %+v, want %+v", loaded, profile)
	}
}

type fakeProvider struct {
	dimensions int
	model      string
}

func (p fakeProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	embedding := make([]float32, p.dimensions)
	if len(embedding) > 0 {
		embedding[0] = 1
	}
	return embedding, nil
}

func (p fakeProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i], _ = p.Embed(ctx, texts[i])
	}
	return embeddings, nil
}

func (p fakeProvider) Model() string {
	return p.model
}

func (p fakeProvider) Dimensions() int {
	return p.dimensions
}

func (p fakeProvider) Ping(ctx context.Context) error {
	return nil
}

// Warmup implements the Provider interface for the test mock.
func (p fakeProvider) Warmup(ctx context.Context) (time.Duration, error) {
	return 0, nil
}
