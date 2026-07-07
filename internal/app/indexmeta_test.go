package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

// TestServiceIndexMeta_AbsentIndex asserts a fresh, never-indexed project
// reports indexed=false / fresh=false / chunks=0 — the signal the
// `search --format json-envelope` contract uses to distinguish "never indexed"
// from "indexed but nothing matched".
func TestServiceIndexMeta_AbsentIndex(t *testing.T) {
	_, service := createTestSession(t)

	indexed, fresh, chunks, err := service.IndexMeta(context.Background())
	if err != nil {
		t.Fatalf("IndexMeta failed: %v", err)
	}
	if indexed || fresh || chunks != 0 {
		t.Fatalf("absent index = indexed=%v fresh=%v chunks=%d, want false/false/0", indexed, fresh, chunks)
	}
}

// TestServiceIndexMeta_IndexedProject asserts an index with chunks reports
// indexed=true and the chunk count. The source file isn't on disk in the temp
// project, so the indexer sees a pending deletion → fresh=false (deterministic).
func TestServiceIndexMeta_IndexedProject(t *testing.T) {
	session, service := createTestSession(t)

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

	indexed, fresh, chunks, err := service.IndexMeta(context.Background())
	if err != nil {
		t.Fatalf("IndexMeta failed: %v", err)
	}
	if !indexed {
		t.Fatal("indexed = false after inserting a chunk, want true")
	}
	if chunks != 1 {
		t.Errorf("chunks = %d, want 1", chunks)
	}
	if fresh {
		t.Error("fresh = true, want false (source file absent from disk → pending deletion)")
	}
}
