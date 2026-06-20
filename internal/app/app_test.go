package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
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
	if err := SaveEmbeddingProfile(session.Config.DataDir, stored); err != nil {
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

	profile, err := LoadEmbeddingProfile(session.Config.DataDir)
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
