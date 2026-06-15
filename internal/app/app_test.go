package app

import (
	"context"
	"os"
	"path/filepath"
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
