package config

import (
	"slices"
	"testing"
)

func TestDefaultConfigIgnoresLocalDataDirectory(t *testing.T) {
	patterns := DefaultConfig().Indexing.IgnorePatterns
	if !slices.Contains(patterns, ".vecgrep/**") {
		t.Fatalf("default ignore patterns = %v, want .vecgrep/**", patterns)
	}
}

func TestDefaultConfigUsesMemoryBoundedHNSWBuildProfile(t *testing.T) {
	cfg := DefaultConfig()
	if got, want := cfg.Vector.VecLite.EfConstruction, 100; got != want {
		t.Fatalf("default HNSW ef_construction = %d, want memory-bounded profile %d", got, want)
	}
	if got, want := cfg.Vector.VecLite.EfSearch, 100; got != want {
		t.Fatalf("default HNSW ef_search = %d, want %d", got, want)
	}
}
