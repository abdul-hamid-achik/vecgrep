package db

import (
	"errors"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/veclite"
)

// openDriftedBackend creates a database whose chunks collection was built with
// oldDimensions, then reopens it with newDimensions — the state left behind by
// switching embedding provider or preset without a reset.
func openDriftedBackend(t *testing.T, oldDimensions, newDimensions int) *VecLiteBackend {
	t.Helper()
	dataDir := t.TempDir()
	old := NewVecLiteBackend(VecLitePath(dataDir))
	if err := old.Init(oldDimensions, HNSWConfig{}); err != nil {
		t.Fatalf("init old backend: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close old backend: %v", err)
	}
	b := NewVecLiteBackend(VecLitePath(dataDir))
	if err := b.Init(newDimensions, HNSWConfig{}); err != nil {
		t.Fatalf("init drifted backend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func driftChunk() ChunkRecord {
	return NewChunkRecord("main.go", "main.go", "h", 10, "go", "package main", 1, 1, 0, 10, "function", "main", "/repo")
}

func TestDimensionDriftErrorsSuggestReset(t *testing.T) {
	// Collection built at 16 dims; active profile produces 8.
	b := openDriftedBackend(t, 16, 8)
	vec8 := make([]float32, 8)

	tests := []struct {
		name string
		call func() error
	}{
		{"SearchEmbeddings", func() error {
			_, err := b.SearchEmbeddings(vec8, 5)
			return err
		}},
		{"SearchWithFilter", func() error {
			_, err := b.SearchWithFilter(vec8, 5, FilterOptions{})
			return err
		}},
		{"SearchWithExplain", func() error {
			_, _, err := b.SearchWithExplain(vec8, 5, FilterOptions{})
			return err
		}},
		{"HybridSearch", func() error {
			_, err := b.HybridSearch(vec8, "query", 5, FilterOptions{}, 0.5, 0.5)
			return err
		}},
		{"InsertChunk", func() error {
			_, err := b.InsertChunk(driftChunk(), vec8)
			return err
		}},
		{"InsertChunkBatch", func() error {
			_, err := b.InsertChunkBatch([]ChunkRecord{driftChunk()}, [][]float32{vec8})
			return err
		}},
		{"UpsertChunk", func() error {
			_, _, err := b.UpsertChunk(driftChunk(), vec8)
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("expected a dimension-mismatch error")
			}
			if !errors.Is(err, veclite.ErrDimensionMismatch) {
				t.Fatalf("error %v should match veclite.ErrDimensionMismatch", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "dimension mismatch") {
				t.Fatalf("error %q should keep veclite's dimension mismatch text", msg)
			}
			if !strings.Contains(msg, "reset --force") {
				t.Fatalf("error %q should suggest 'vecgrep reset --force'", msg)
			}
			if !strings.Contains(msg, "vecgrep index") {
				t.Fatalf("error %q should suggest reindexing", msg)
			}
		})
	}
}

// A vector that disagrees with the *configured* dimensions is a different
// failure (the provider returned the wrong width for the active config); it
// must not tell the user to reset a perfectly good index.
func TestConfigDimensionPrecheckErrorIsNotDriftAnnotated(t *testing.T) {
	b := openDriftedBackend(t, 8, 8)
	_, err := b.SearchEmbeddings(make([]float32, 4), 5)
	if err == nil {
		t.Fatal("expected a dimension-mismatch error")
	}
	if strings.Contains(err.Error(), "reset --force") {
		t.Fatalf("error %q should not suggest a reset for a config-level mismatch", err)
	}
}
