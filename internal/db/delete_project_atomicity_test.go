package db

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/veclite"
)

func TestDeleteProjectFileFailureKeepsHashAndDurableDirtyFreshness(t *testing.T) {
	const (
		dimensions  = 8
		projectRoot = "/projects/atomic-file"
	)
	dataDir := t.TempDir()
	database, err := Open("", dimensions, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	chunk := NewChunkRecord(projectRoot+"/main.go", "main.go", "file-hash", 12, "go", "package main", 1, 1, 0, 12, "generic", "", projectRoot)
	chunk.SourceHash = strings.Repeat("a", 64)
	if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Sync(); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}

	sentinel := errors.New("injected file-hash delete failure")
	database.backend.testHooks = &vecLiteBackendTestHooks{
		beforeProjectHashDelete: func() error { return sentinel },
	}
	deleted, err := database.DeleteProjectFile(t.Context(), projectRoot, "main.go")
	if !errors.Is(err, sentinel) || deleted != 1 {
		_ = database.Close()
		t.Fatalf("DeleteProjectFile = deleted:%d err:%v, want 1/sentinel", deleted, err)
	}
	database.backend.testHooks = nil

	chunks, err := database.GetChunksByFile("main.go")
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		_ = database.Close()
		t.Fatalf("chunks remain after chunk-first delete: %d", len(chunks))
	}
	hashRecords, err := database.backend.fileHashCollection().Find(
		veclite.Equal(fileHashRecordField, fileHashRecordType),
		veclite.Equal("project_root", projectRoot),
		veclite.Equal("relative_path", "main.go"),
	)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if len(hashRecords) != 1 {
		_ = database.Close()
		t.Fatalf("hash records = %d, want stale hash retained after injected failure", len(hashRecords))
	}
	assertProjectSourceHashesDirty(t, database, projectRoot, chunk.SourceHash)

	walInfo, err := os.Stat(VecLitePath(dataDir) + ".wal")
	if err != nil || walInfo.Size() == 0 {
		_ = database.Close()
		t.Fatalf("durable WAL tombstone missing: info=%v err=%v", walInfo, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open("", dimensions, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertProjectSourceHashesDirty(t, reopened, projectRoot, chunk.SourceHash)

	if err := reopened.Reset(t.Context(), projectRoot); err != nil {
		t.Fatalf("full project reset did not recover dirty tombstone: %v", err)
	}
	if hashes, complete, err := reopened.GetSourceHashes(projectRoot); err != nil || !complete || len(hashes) != 0 {
		t.Fatalf("source hashes after full reset = %v complete=%t err=%v", hashes, complete, err)
	}
}

func TestSuccessfulFileDeleteFromCleanProjectPreservesCompleteHashes(t *testing.T) {
	const (
		dimensions  = 8
		projectRoot = "/projects/clean-file-delete"
	)
	database, err := Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, file := range []string{"a.go", "b.go"} {
		chunk := NewChunkRecord(
			projectRoot+"/"+file, file, file+"-index-hash", 12, "go",
			"package main", 1, 1, 0, 12, "generic", "", projectRoot,
		)
		chunk.SourceHash = strings.Repeat(strings.TrimSuffix(file, ".go"), 64)
		if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
			t.Fatal(err)
		}
	}
	if hashes, complete, err := database.GetSourceHashes(projectRoot); err != nil || !complete || len(hashes) != 2 {
		t.Fatalf("source hashes before clean delete = %v complete=%t err=%v", hashes, complete, err)
	}
	if deleted, err := database.DeleteProjectFile(t.Context(), projectRoot, "a.go"); err != nil || deleted != 1 {
		t.Fatalf("clean delete = deleted:%d err:%v, want 1/nil", deleted, err)
	}
	hashes, complete, err := database.GetSourceHashes(projectRoot)
	if err != nil || !complete {
		t.Fatalf("clean delete left project dirty: hashes=%v complete=%t err=%v", hashes, complete, err)
	}
	if len(hashes) != 1 || hashes["b.go"] == "" {
		t.Fatalf("source hashes after clean delete = %v, want only b.go", hashes)
	}
}

func TestSuccessfulFileDeleteCannotClearPreexistingDirtyTombstone(t *testing.T) {
	const (
		dimensions  = 8
		projectRoot = "/projects/dirty-across-file-deletes"
	)
	dataDir := t.TempDir()
	database, err := Open("", dimensions, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	newChunk := func(file, sourceHash string) ChunkRecord {
		chunk := NewChunkRecord(
			projectRoot+"/"+file, file, file+"-index-hash", 12, "go",
			"package main", 1, 1, 0, 12, "generic", "", projectRoot,
		)
		chunk.SourceHash = sourceHash
		return chunk
	}
	a := newChunk("a.go", strings.Repeat("a", 64))
	b := newChunk("b.go", strings.Repeat("b", 64))
	for _, chunk := range []ChunkRecord{a, b} {
		if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Sync(); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}

	sentinel := errors.New("injected delete A hash failure")
	database.backend.testHooks = &vecLiteBackendTestHooks{
		beforeProjectHashDelete: func() error { return sentinel },
	}
	if deleted, err := database.DeleteProjectFile(t.Context(), projectRoot, "a.go"); !errors.Is(err, sentinel) || deleted != 1 {
		_ = database.Close()
		t.Fatalf("delete A = deleted:%d err:%v, want 1/sentinel", deleted, err)
	}
	database.backend.testHooks = nil
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open("", dimensions, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if deleted, err := reopened.DeleteProjectFile(t.Context(), projectRoot, "b.go"); err != nil || deleted != 1 {
		t.Fatalf("delete B = deleted:%d err:%v, want 1/nil", deleted, err)
	}
	hashes, complete, err := reopened.GetSourceHashes(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatalf("successful delete B cleared preexisting dirty tombstone: %v", hashes)
	}
	if got := hashes["a.go"]; got != a.SourceHash {
		t.Fatalf("retained stale A hash = %q, want %q", got, a.SourceHash)
	}
	if _, exists := hashes["b.go"]; exists {
		t.Fatalf("successfully deleted B hash remains: %v", hashes)
	}

	if err := reopened.Reset(t.Context(), projectRoot); err != nil {
		t.Fatalf("full reset recovery: %v", err)
	}
	if hashes, complete, err = reopened.GetSourceHashes(projectRoot); err != nil || !complete || len(hashes) != 0 {
		t.Fatalf("source hashes after reset = %v complete=%t err=%v", hashes, complete, err)
	}
	rebuilt := newChunk("rebuilt.go", strings.Repeat("c", 64))
	if _, err := reopened.InsertChunk(rebuilt, make([]float32, dimensions)); err != nil {
		t.Fatal(err)
	}
	hashes, complete, err = reopened.GetSourceHashes(projectRoot)
	if err != nil || !complete || hashes["rebuilt.go"] != rebuilt.SourceHash {
		t.Fatalf("source hashes after full rebuild = %v complete=%t err=%v", hashes, complete, err)
	}
}

func TestDeleteProjectRootFailureLeavesDurableDirtyFreshness(t *testing.T) {
	const (
		dimensions  = 8
		projectRoot = "/projects/atomic-reset"
	)
	database, err := Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	chunk := NewChunkRecord(projectRoot+"/main.go", "main.go", "file-hash", 12, "go", "package main", 1, 1, 0, 12, "generic", "", projectRoot)
	chunk.SourceHash = strings.Repeat("b", 64)
	if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("injected project-hash delete failure")
	database.backend.testHooks = &vecLiteBackendTestHooks{
		beforeProjectHashDelete: func() error { return sentinel },
	}
	if err := database.Reset(t.Context(), projectRoot); !errors.Is(err, sentinel) {
		t.Fatalf("Reset error = %v, want sentinel", err)
	}
	database.backend.testHooks = nil
	assertProjectSourceHashesDirty(t, database, projectRoot, chunk.SourceHash)

	if err := database.Reset(t.Context(), projectRoot); err != nil {
		t.Fatalf("retry full reset: %v", err)
	}
	if hashes, complete, err := database.GetSourceHashes(projectRoot); err != nil || !complete || len(hashes) != 0 {
		t.Fatalf("source hashes after reset retry = %v complete=%t err=%v", hashes, complete, err)
	}
}

func assertProjectSourceHashesDirty(t *testing.T, database *DB, projectRoot, wantHash string) {
	t.Helper()
	hashes, complete, err := database.GetSourceHashes(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatalf("dirty project reported complete source hashes: %v", hashes)
	}
	if got := hashes["main.go"]; got != wantHash {
		t.Fatalf("retained source hash = %q, want %q", got, wantHash)
	}
}
