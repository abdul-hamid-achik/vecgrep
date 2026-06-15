package search

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

func TestSearchFiltersByProjectRoot(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	dims := database.Dimensions()
	insert := func(root, rel, content string) {
		t.Helper()
		chunk := db.NewChunkRecord(
			filepath.Join(root, rel),
			rel,
			"hash",
			int64(len(content)),
			"go",
			content,
			1, 1, 0, len(content),
			"function",
			"LoadConfig",
			root,
		)
		if _, err := database.InsertChunk(chunk, make([]float32, dims)); err != nil {
			t.Fatalf("InsertChunk failed: %v", err)
		}
	}

	insert("/tmp/project-a", "main.go", "func LoadConfig() {}")
	insert("/tmp/project-b", "main.go", "func LoadConfig() {}")

	searcher := NewSearcher(database, nil)
	results, err := searcher.Search(context.Background(), "LoadConfig", SearchOptions{
		Mode:        SearchModeKeyword,
		Limit:       10,
		ProjectRoot: "/tmp/project-a",
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].FilePath != "/tmp/project-a/main.go" {
		t.Fatalf("unexpected project result: %s", results[0].FilePath)
	}
}
