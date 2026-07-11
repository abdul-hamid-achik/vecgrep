package db

import (
	"sync"
	"testing"
	"time"
)

func TestSyncWaitsForChunkAndFileHashUpdate(t *testing.T) {
	path := VecLitePath(t.TempDir())
	backend := NewVecLiteBackend(path)
	if err := backend.Init(4, HNSWConfig{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	insertBetweenCollections := make(chan struct{})
	releaseInsert := make(chan struct{})
	syncLocked := make(chan struct{}, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseInsert) }) }
	closed := false
	defer func() {
		release()
		backend.testHooks = nil
		if !closed {
			_ = backend.Close()
		}
	}()

	backend.testHooks = &vecLiteBackendTestHooks{
		afterChunkInsert: func() {
			close(insertBetweenCollections)
			<-releaseInsert
		},
		syncLocked: func() {
			syncLocked <- struct{}{}
		},
	}

	chunk := ChunkRecord{
		FilePath:     "/repo/atomic.go",
		RelativePath: "atomic.go",
		FileHash:     "hash-after-both-collections",
		ProjectRoot:  "/repo",
		Content:      "package atomic",
		StartLine:    1,
		EndLine:      1,
		IndexedAt:    time.Now(),
	}
	insertDone := make(chan error, 1)
	go func() {
		_, err := backend.InsertChunk(chunk, []float32{1, 0, 0, 0})
		insertDone <- err
	}()
	<-insertBetweenCollections

	syncDone := make(chan error, 1)
	go func() { syncDone <- backend.Sync() }()

	select {
	case <-syncLocked:
		t.Fatal("Sync acquired the persistence boundary between chunk and file-hash updates")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	if err := <-insertDone; err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}
	select {
	case <-syncLocked:
	case <-time.After(5 * time.Second):
		t.Fatal("Sync did not acquire the persistence boundary after InsertChunk completed")
	}
	if err := <-syncDone; err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	backend.testHooks = nil
	if err := backend.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	closed = true

	reopened := NewVecLiteBackend(path)
	if err := reopened.Init(4, HNSWConfig{}); err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer reopened.Close()

	chunks, err := reopened.GetChunksByFile("atomic.go")
	if err != nil {
		t.Fatalf("GetChunksByFile failed: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("persisted chunks = %d, want 1", len(chunks))
	}
	hashes, err := reopened.GetFileHashes("/repo")
	if err != nil {
		t.Fatalf("GetFileHashes failed: %v", err)
	}
	if got := hashes["atomic.go"]; got != chunk.FileHash {
		t.Fatalf("persisted file hash = %q, want %q", got, chunk.FileHash)
	}
}
