package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

type freshnessStructuralSource struct{ calls int }

func (s *freshnessStructuralSource) LoadStructuralChunks(context.Context, string) (*StructuralChunkSet, error) {
	s.calls++
	return nil, os.ErrPermission
}

func TestGetRawPendingChangesIsExactWithoutLoadingStructuralExport(t *testing.T) {
	const dimensions = 8
	root := t.TempDir()
	database, err := db.Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	path := filepath.Join(root, "main.go")
	content := []byte("package main\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sourceHash, _, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := db.NewChunkRecord(path, "main.go", "profile-aware-hash", int64(len(content)), "go", string(content), 1, 1, 0, len(content), "generic", "", root)
	chunk.SourceHash = sourceHash
	if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
		t.Fatal(err)
	}

	idx := NewIndexer(database, nil, DefaultIndexerConfig())
	structural := &freshnessStructuralSource{}
	idx.SetStructuralChunkSource(structural, true)

	pending, complete, err := idx.GetRawPendingChanges(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || pending == nil || pending.TotalPending != 0 {
		t.Fatalf("fresh pending = %+v, complete = %v", pending, complete)
	}
	if structural.calls != 0 {
		t.Fatalf("raw freshness loaded structural export %d times", structural.calls)
	}

	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, complete, err = idx.GetRawPendingChanges(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || pending.ModifiedFiles != 1 || pending.NewFiles != 1 || pending.DeletedFiles != 0 || pending.TotalPending != 2 {
		t.Fatalf("drift pending = %+v, complete = %v", pending, complete)
	}
	if structural.calls != 0 {
		t.Fatalf("raw drift loaded structural export %d times", structural.calls)
	}
}

func TestGetRawPendingChangesLegacySourceHashesAreIncomplete(t *testing.T) {
	const dimensions = 8
	root := t.TempDir()
	database, err := db.Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	path := filepath.Join(root, "legacy.go")
	content := []byte("package legacy\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	chunk := db.NewChunkRecord(path, "legacy.go", "legacy-hash", int64(len(content)), "go", string(content), 1, 1, 0, len(content), "generic", "", root)
	if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
		t.Fatal(err)
	}

	pending, complete, err := NewIndexer(database, nil, DefaultIndexerConfig()).GetRawPendingChanges(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if complete || pending != nil {
		t.Fatalf("legacy pending = %+v, complete = %v; want nil/false", pending, complete)
	}
}

func TestIncrementalFileEditRestoresCompleteRawFreshness(t *testing.T) {
	const dimensions = 8
	root := t.TempDir()
	database, err := db.Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	path := filepath.Join(root, "main.go")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package main\n\nfunc main() {}\n")
	cfg := DefaultIndexerConfig()
	cfg.Workers = 1
	idx := NewIndexer(database, newMockEmbedProvider(dimensions), cfg)
	if result, err := idx.Index(context.Background(), root); err != nil || len(result.Errors) != 0 {
		t.Fatalf("initial index = %+v err=%v", result, err)
	}

	write("package main\n\nfunc main() { println(1) }\n")
	if result, err := idx.Index(context.Background(), root, path); err != nil || len(result.Errors) != 0 {
		t.Fatalf("incremental edit = %+v err=%v", result, err)
	}
	hashes, complete, err := database.GetSourceHashes(root)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, _, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || hashes["main.go"] != wantHash {
		t.Fatalf("source hashes after incremental edit = %v complete=%t, want main.go=%q", hashes, complete, wantHash)
	}
	pending, complete, err := idx.GetRawPendingChanges(context.Background(), root)
	if err != nil || !complete || pending == nil || pending.TotalPending != 0 {
		t.Fatalf("raw freshness after incremental edit = %+v complete=%t err=%v", pending, complete, err)
	}
}

func TestRawPendingIgnoresChunklessFilesAfterIndex(t *testing.T) {
	const dimensions = 8
	root := t.TempDir()
	database, err := db.Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	files := map[string][]byte{
		"empty.go":      {},
		"whitespace.go": []byte("  \n\t\r\n"),
		"binary.dat":    {0x00, 0x01, 0x02, 0xff},
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := DefaultIndexerConfig()
	cfg.Workers = 1
	idx := NewIndexer(database, newMockEmbedProvider(dimensions), cfg)
	result, err := idx.Index(context.Background(), root)
	if err != nil || len(result.Errors) != 0 || result.ChunksCreated != 0 {
		t.Fatalf("chunkless index = %+v err=%v", result, err)
	}
	pending, complete, err := idx.GetRawPendingChanges(context.Background(), root)
	if err != nil || !complete || pending == nil || pending.TotalPending != 0 {
		t.Fatalf("chunkless raw freshness = %+v complete=%t err=%v", pending, complete, err)
	}
	hashes, complete, err := database.GetSourceHashes(root)
	if err != nil || !complete || len(hashes) != 0 {
		t.Fatalf("chunkless source hashes = %v complete=%t err=%v", hashes, complete, err)
	}
}

func TestRawPendingConvergesAcrossBinaryTextTransitions(t *testing.T) {
	const dimensions = 8
	root := t.TempDir()
	database, err := db.Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	path := filepath.Join(root, "asset.dat")
	write := func(content []byte) {
		t.Helper()
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write([]byte{0x00, 0x01, 0x02})
	cfg := DefaultIndexerConfig()
	cfg.Workers = 1
	idx := NewIndexer(database, newMockEmbedProvider(dimensions), cfg)
	if result, err := idx.Index(context.Background(), root); err != nil || len(result.Errors) != 0 {
		t.Fatalf("initial binary index = %+v err=%v", result, err)
	}

	write([]byte("now searchable text\n"))
	pending, complete, err := idx.GetRawPendingChanges(context.Background(), root)
	if err != nil || !complete || pending == nil || pending.NewFiles != 1 || pending.TotalPending != 1 {
		t.Fatalf("binary-to-text pending = %+v complete=%t err=%v", pending, complete, err)
	}
	if result, err := idx.Index(context.Background(), root, path); err != nil || len(result.Errors) != 0 || result.ChunksCreated == 0 {
		t.Fatalf("binary-to-text index = %+v err=%v", result, err)
	}
	if pending, complete, err = idx.GetRawPendingChanges(context.Background(), root); err != nil || !complete || pending.TotalPending != 0 {
		t.Fatalf("text freshness = %+v complete=%t err=%v", pending, complete, err)
	}

	write([]byte{0x00, 0x03, 0x04})
	pending, complete, err = idx.GetRawPendingChanges(context.Background(), root)
	if err != nil || !complete || pending == nil || pending.DeletedFiles != 1 || pending.TotalPending != 1 {
		t.Fatalf("text-to-binary pending = %+v complete=%t err=%v", pending, complete, err)
	}
	if result, err := idx.Index(context.Background(), root, path); err != nil || len(result.Errors) != 0 || result.ChunksCreated != 0 {
		t.Fatalf("text-to-binary index = %+v err=%v", result, err)
	}
	if chunks, err := database.GetChunksByFile("asset.dat"); err != nil || len(chunks) != 0 {
		t.Fatalf("stale text chunks after binary transition = %v err=%v", chunks, err)
	}
	if pending, complete, err = idx.GetRawPendingChanges(context.Background(), root); err != nil || !complete || pending.TotalPending != 0 {
		t.Fatalf("binary freshness after cleanup = %+v complete=%t err=%v", pending, complete, err)
	}
}

func TestRawPendingSkipsSymlinkDirectoriesAndTracksSymlinkFiles(t *testing.T) {
	const dimensions = 8
	root := t.TempDir()
	targetRoot := t.TempDir()
	targetDir := filepath.Join(targetRoot, "shared")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	directoryTargetFile := filepath.Join(targetDir, "ignored.go")
	if err := os.WriteFile(directoryTargetFile, []byte("package ignored\n\nfunc One() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(targetRoot, "shared.go")
	if err := os.WriteFile(targetFile, []byte("package shared\n\nfunc One() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, targetDir, filepath.Join(root, "linked-dir"))
	linkedFile := filepath.Join(root, "linked.go")
	symlinkOrSkip(t, targetFile, linkedFile)

	database, err := db.Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cfg := DefaultIndexerConfig()
	cfg.Workers = 1
	idx := NewIndexer(database, newMockEmbedProvider(dimensions), cfg)
	if result, err := idx.Index(context.Background(), root); err != nil || len(result.Errors) != 0 {
		t.Fatalf("initial symlink index = %+v err=%v", result, err)
	}

	pending, complete, err := idx.GetRawPendingChanges(context.Background(), root)
	if err != nil || !complete || pending == nil || pending.TotalPending != 0 {
		t.Fatalf("initial symlink freshness = %+v complete=%t err=%v", pending, complete, err)
	}

	// Changes reachable only through a directory symlink remain outside the
	// non-following index scope and therefore must not create false drift.
	if err := os.WriteFile(directoryTargetFile, []byte("package ignored\n\nfunc Two() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, complete, err = idx.GetRawPendingChanges(context.Background(), root)
	if err != nil || !complete || pending == nil || pending.TotalPending != 0 {
		t.Fatalf("directory-link target drift = %+v complete=%t err=%v", pending, complete, err)
	}

	// A symlink to a regular file remains an indexed source path. Editing its
	// target must be reported against the link path as a modification.
	if err := os.WriteFile(targetFile, []byte("package shared\n\nfunc Two() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pending, complete, err = idx.GetRawPendingChanges(context.Background(), root)
	if err != nil || !complete || pending == nil || pending.ModifiedFiles != 1 || pending.NewFiles != 0 || pending.DeletedFiles != 0 || pending.TotalPending != 1 {
		t.Fatalf("file-link target drift = %+v complete=%t err=%v", pending, complete, err)
	}

	if result, err := idx.Index(context.Background(), root); err != nil || len(result.Errors) != 0 {
		t.Fatalf("updated symlink index = %+v err=%v", result, err)
	}
	if pending, complete, err = idx.GetRawPendingChanges(context.Background(), root); err != nil || !complete || pending == nil || pending.TotalPending != 0 {
		t.Fatalf("updated symlink freshness = %+v complete=%t err=%v", pending, complete, err)
	}
}
