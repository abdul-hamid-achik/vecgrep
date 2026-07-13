package app

import (
	"slices"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

func TestBuildIndexerConfigCarriesAllResolvedSettings(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Indexing.ChunkSize = 333
	cfg.Indexing.ChunkOverlap = 44
	cfg.Indexing.MaxFileSize = 234567
	cfg.Indexing.SourceBufferBytes = 345678
	cfg.Indexing.SyncInterval = 17
	cfg.Indexing.SyncIntervalDuration = 9 * time.Second
	cfg.Indexing.IgnorePatterns = []string{"generated/**"}

	got := BuildIndexerConfig(cfg, []string{"scratch/**"})
	if got.ChunkSize != 1332 || got.ChunkOverlap != 176 {
		t.Fatalf("token conversion = (%d, %d), want (1332, 176)", got.ChunkSize, got.ChunkOverlap)
	}
	if got.MaxFileSize != cfg.Indexing.MaxFileSize || got.SourceBufferBytes != cfg.Indexing.SourceBufferBytes {
		t.Fatalf("size settings = (%d, %d)", got.MaxFileSize, got.SourceBufferBytes)
	}
	if got.SyncInterval != 17 || got.SyncIntervalDuration != 9*time.Second {
		t.Fatalf("sync settings = (%d, %s)", got.SyncInterval, got.SyncIntervalDuration)
	}
	for _, pattern := range []string{".vecgrep/**", "generated/**", "scratch/**"} {
		if !slices.Contains(got.IgnorePatterns, pattern) {
			t.Fatalf("ignore patterns %v omit %q", got.IgnorePatterns, pattern)
		}
	}
}

func TestBuildIndexerConfigNilUsesIndexerDefaults(t *testing.T) {
	got := BuildIndexerConfig(nil, nil)
	want := index.DefaultIndexerConfig()
	if got.ChunkSize != want.ChunkSize || got.ChunkOverlap != want.ChunkOverlap || got.MaxFileSize != want.MaxFileSize {
		t.Fatalf("nil config = %+v, want defaults %+v", got, want)
	}
}
