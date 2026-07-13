package db

import (
	"testing"
	"time"
)

func TestOpen(t *testing.T) {
	tmpDir := t.TempDir()

	database, err := Open(tmpDir+"/test.db", 768, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Verify we can get the version
	version, err := database.VecVersion()
	if err != nil {
		t.Fatalf("Failed to get VecVersion: %v", err)
	}
	if version != "veclite" {
		t.Errorf("Expected veclite, got: %s", version)
	}
}

func TestVecVersion(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := Open(tmpDir+"/test.db", 768, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	version, err := db.VecVersion()
	if err != nil {
		t.Fatalf("VecVersion failed: %v", err)
	}

	if version != "veclite" {
		t.Errorf("VecVersion returned unexpected value: %s", version)
	}
}

// TestBackendReloadRefetchesCollection pins the fix for the silent-stale-read
// bug: db.Reload() rebuilds fresh *veclite.Collection objects and swaps them
// into the underlying *veclite.DB, but the backend's cached coll pointer must
// be re-fetched too — otherwise a read-only handle keeps serving the
// pre-reload snapshot forever.
func TestBackendReloadRefetchesCollection(t *testing.T) {
	tmpDir := t.TempDir()

	b := NewVecLiteBackend(VecLitePath(tmpDir))
	if err := b.Init(8, HNSWConfig{}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	defer b.Close()

	// Persist a snapshot so Reload has an on-disk source of truth to rebuild
	// from (Reload is a no-op when nothing was ever persisted).
	emb := make([]float32, 8)
	for i := range emb {
		emb[i] = 0.1
	}
	if _, err := b.InsertChunk(ChunkRecord{RelativePath: "a.go", StartLine: 1, IndexedAt: time.Now()}, emb); err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}
	if err := b.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	oldColl := b.collection()
	if oldColl == nil {
		t.Fatal("collection is nil after init")
	}

	if err := b.Reload(); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	if got := b.collection(); got == oldColl {
		t.Fatal("Reload did not re-fetch the collection (stale-snapshot bug): b.coll still points at the pre-reload object")
	}

	// The rebuilt collection must still expose the persisted data.
	if c, err := b.Count(); err != nil || c != 1 {
		t.Fatalf("expected 1 chunk after reload, got %d (err=%v)", c, err)
	}
}

func TestInsertAndSearchEmbedding(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Create a test embedding
	embedding := make([]float32, dimensions)
	for i := range embedding {
		embedding[i] = float32(i) / float32(dimensions)
	}

	// Create a chunk record
	chunk := NewChunkRecord(
		"/tmp/test/main.go",
		"main.go",
		"abc123",
		100,
		"go",
		"func main() {}",
		1, 1, 0, 14,
		"function",
		"main",
		"/tmp/test",
	)

	// Insert chunk with embedding
	chunkID, err := db.InsertChunk(chunk, embedding)
	if err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	// Search for similar embeddings
	results, err := db.SearchEmbeddings(embedding, 10)
	if err != nil {
		t.Fatalf("SearchEmbeddings failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("SearchEmbeddings returned no results")
	}

	if results[0].ChunkID != int64(chunkID) {
		t.Errorf("Expected chunk ID %d, got %d", chunkID, results[0].ChunkID)
	}

	// Verify chunk metadata is returned
	if results[0].Chunk == nil {
		t.Error("Expected chunk metadata in result")
	} else {
		if results[0].Chunk.Content != "func main() {}" {
			t.Errorf("Expected content 'func main() {}', got '%s'", results[0].Chunk.Content)
		}
		if results[0].Chunk.Language != "go" {
			t.Errorf("Expected language 'go', got '%s'", results[0].Chunk.Language)
		}
	}
}

func TestInsertEmbeddingDimensionMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Try to insert embedding with wrong dimensions
	wrongEmbedding := make([]float32, 512)
	err = db.InsertEmbedding(1, wrongEmbedding)
	if err == nil {
		t.Error("Expected error for dimension mismatch, got nil")
	}
}

func TestDeleteFile(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	database, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Create test chunk
	embedding := make([]float32, dimensions)
	for i := range embedding {
		embedding[i] = float32(i) / float32(dimensions)
	}

	chunk := NewChunkRecord(
		"/tmp/test/main.go",
		"main.go",
		"abc123",
		100,
		"go",
		"func main() {}",
		1, 1, 0, 14,
		"function",
		"main",
		"/tmp/test",
	)

	_, err = database.InsertChunk(chunk, embedding)
	if err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	// Delete by file path
	ctx := t.Context()
	deleted, err := database.DeleteFile(ctx, "main.go")
	if err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}

	if deleted != 1 {
		t.Errorf("Expected 1 deleted, got %d", deleted)
	}

	// Verify deletion
	stats, err := database.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats["chunks"] != 0 {
		t.Errorf("Expected 0 chunks after deletion, got %d", stats["chunks"])
	}
}

func TestStats(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Insert a test chunk
	embedding := make([]float32, dimensions)
	chunk := NewChunkRecord(
		"/tmp/test/main.go",
		"main.go",
		"abc123",
		100,
		"go",
		"func main() {}",
		1, 1, 0, 14,
		"function",
		"main",
		"/tmp/test",
	)

	_, err = db.InsertChunk(chunk, embedding)
	if err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}

	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats["chunks"] != 1 {
		t.Errorf("Expected 1 chunk, got %d", stats["chunks"])
	}

	if stats["files"] != 1 {
		t.Errorf("Expected 1 file, got %d", stats["files"])
	}
}

func TestGetFileHashes(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	projectRoot := "/tmp/test"

	// Insert test chunks from different files
	files := []struct {
		relPath string
		hash    string
	}{
		{"main.go", "hash1"},
		{"utils.go", "hash2"},
		{"lib/helper.go", "hash3"},
	}

	embedding := make([]float32, dimensions)
	for _, f := range files {
		chunk := NewChunkRecord(
			projectRoot+"/"+f.relPath,
			f.relPath,
			f.hash,
			100,
			"go",
			"content",
			1, 1, 0, 7,
			"generic",
			"",
			projectRoot,
		)
		_, err = db.InsertChunk(chunk, embedding)
		if err != nil {
			t.Fatalf("InsertChunk failed: %v", err)
		}
	}

	// Get file hashes
	hashes, err := db.GetFileHashes(projectRoot)
	if err != nil {
		t.Fatalf("GetFileHashes failed: %v", err)
	}

	if len(hashes) != 3 {
		t.Errorf("Expected 3 files, got %d", len(hashes))
	}

	for _, f := range files {
		if hashes[f.relPath] != f.hash {
			t.Errorf("Expected hash '%s' for '%s', got '%s'", f.hash, f.relPath, hashes[f.relPath])
		}
	}
}

func TestDeleteFileMultipleChunks(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	database, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	// Insert multiple chunks for the same file
	embedding := make([]float32, dimensions)
	for i := range 3 {
		chunk := NewChunkRecord(
			"/tmp/test/main.go",
			"main.go",
			"abc123",
			100,
			"go",
			"content",
			i*10, i*10+10, 0, 7,
			"generic",
			"",
			"/tmp/test",
		)
		_, err = database.InsertChunk(chunk, embedding)
		if err != nil {
			t.Fatalf("InsertChunk failed: %v", err)
		}
	}

	// Delete the file
	ctx := t.Context()
	deleted, err := database.DeleteFile(ctx, "main.go")
	if err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}

	if deleted != 3 {
		t.Errorf("Expected 3 deleted chunks, got %d", deleted)
	}

	// Verify stats
	stats, err := database.Stats()
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats["chunks"] != 0 {
		t.Errorf("Expected 0 chunks after deletion, got %d", stats["chunks"])
	}
}

func TestSearchWithFilter(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Insert chunks with different languages
	embedding := make([]float32, dimensions)
	langs := []string{"go", "python", "javascript", "go"}
	for i, lang := range langs {
		chunk := NewChunkRecord(
			"/tmp/test/file."+lang,
			"file."+lang,
			"hash"+string(rune('0'+i)),
			100,
			lang,
			"content for "+lang,
			1, 10, 0, 20,
			"generic",
			"",
			"/tmp/test",
		)
		_, err = db.InsertChunk(chunk, embedding)
		if err != nil {
			t.Fatalf("InsertChunk failed: %v", err)
		}
	}

	// Search with language filter
	results, err := db.SearchWithFilter(embedding, 10, FilterOptions{
		Language: "go",
	})
	if err != nil {
		t.Fatalf("SearchWithFilter failed: %v", err)
	}

	// Should return only Go files
	for _, r := range results {
		if r.Chunk != nil && r.Chunk.Language != "go" {
			t.Errorf("Expected language 'go', got '%s'", r.Chunk.Language)
		}
	}
}

func TestSearchWithFilePathsFilter(t *testing.T) {
	tmpDir := t.TempDir()
	dimensions := 768

	db, err := Open(tmpDir+"/test.db", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Insert chunks across multiple files
	embedding := make([]float32, dimensions)
	files := []string{"auth.go", "login.go", "models.go", "auth.go"}
	for i, rel := range files {
		chunk := NewChunkRecord(
			"/tmp/test/"+rel,
			rel,
			"hash"+string(rune('0'+i)),
			100,
			"go",
			"content for "+rel,
			i*10+1, i*10+10, 0, 20,
			"function",
			"",
			"/tmp/test",
		)
		_, err = db.InsertChunk(chunk, embedding)
		if err != nil {
			t.Fatalf("InsertChunk failed: %v", err)
		}
	}

	// Scope to a 2-file allow-list (auth.go + login.go)
	results, err := db.SearchWithFilter(embedding, 10, FilterOptions{
		FilePaths: []string{"auth.go", "login.go"},
	})
	if err != nil {
		t.Fatalf("SearchWithFilter failed: %v", err)
	}

	allowed := map[string]bool{"auth.go": true, "login.go": true}
	for _, r := range results {
		if r.Chunk == nil {
			continue
		}
		if !allowed[r.Chunk.RelativePath] {
			t.Errorf("expected result in {auth.go, login.go}, got %q", r.Chunk.RelativePath)
		}
	}

	// models.go should never appear
	for _, r := range results {
		if r.Chunk != nil && r.Chunk.RelativePath == "models.go" {
			t.Error("models.go should have been filtered out by FilePaths")
		}
	}
}

func TestNewChunkRecord(t *testing.T) {
	before := time.Now()
	chunk := NewChunkRecord(
		"/path/to/file.go",
		"file.go",
		"abc123",
		500,
		"go",
		"func test() {}",
		1, 5, 0, 14,
		"function",
		"test",
		"/path/to",
	)
	after := time.Now()

	if chunk.FilePath != "/path/to/file.go" {
		t.Errorf("Unexpected FilePath: %s", chunk.FilePath)
	}
	if chunk.RelativePath != "file.go" {
		t.Errorf("Unexpected RelativePath: %s", chunk.RelativePath)
	}
	if chunk.IndexedAt.Before(before) || chunk.IndexedAt.After(after) {
		t.Errorf("IndexedAt should be between test start and end")
	}
}

func TestFileHashesRemainCorrectAfterDeleteAndReopen(t *testing.T) {
	tmpDir := t.TempDir()
	const dimensions = 32
	const projectRoot = "/tmp/hash-project"
	embedding := make([]float32, dimensions)

	database, err := Open("", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	for _, file := range []struct {
		path string
		hash string
	}{
		{path: "main.go", hash: "hash-main"},
		{path: "pkg/helper.go", hash: "hash-helper"},
	} {
		chunk := NewChunkRecord(
			projectRoot+"/"+file.path,
			file.path,
			file.hash,
			100,
			"go",
			"content",
			1, 1, 0, 7,
			"generic",
			"",
			projectRoot,
		)
		if _, err := database.InsertChunk(chunk, embedding); err != nil {
			t.Fatalf("InsertChunk(%s) failed: %v", file.path, err)
		}
	}

	hashes, err := database.GetFileHashes(projectRoot)
	if err != nil {
		t.Fatalf("GetFileHashes before delete failed: %v", err)
	}
	if len(hashes) != 2 || hashes["main.go"] != "hash-main" || hashes["pkg/helper.go"] != "hash-helper" {
		t.Fatalf("unexpected hashes before delete: %#v", hashes)
	}

	records := database.backend.fileHashCollection().All()
	if len(records) != 2 {
		t.Fatalf("expected two vectorless file records, got %d", len(records))
	}
	for _, record := range records {
		if len(record.Vector) != 0 {
			t.Fatalf("file metadata record %d unexpectedly cloned/stored a vector of length %d", record.ID, len(record.Vector))
		}
	}

	if deleted, err := database.DeleteFile(t.Context(), "main.go"); err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	} else if deleted != 1 {
		t.Fatalf("DeleteFile deleted %d chunks, want 1", deleted)
	}
	hashes, err = database.GetFileHashes(projectRoot)
	if err != nil {
		t.Fatalf("GetFileHashes after delete failed: %v", err)
	}
	if len(hashes) != 1 || hashes["pkg/helper.go"] != "hash-helper" {
		t.Fatalf("unexpected hashes after delete: %#v", hashes)
	}

	if err := database.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	database, err = Open("", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer database.Close()

	hashes, err = database.GetFileHashes(projectRoot)
	if err != nil {
		t.Fatalf("GetFileHashes after reopen failed: %v", err)
	}
	if len(hashes) != 1 || hashes["pkg/helper.go"] != "hash-helper" {
		t.Fatalf("unexpected hashes after reopen: %#v", hashes)
	}
}

func TestDeleteFileUsesCanonicalPathBeforeLegacyFallback(t *testing.T) {
	tmpDir := t.TempDir()
	const dimensions = 16
	const projectRoot = "/tmp/delete-project"
	embedding := make([]float32, dimensions)

	database, err := Open("", dimensions, tmpDir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	for _, path := range []string{"main.go", "pkg/main.go"} {
		chunk := NewChunkRecord(
			projectRoot+"/"+path,
			path,
			"hash-"+path,
			100,
			"go",
			"content",
			1, 1, 0, 7,
			"generic",
			"",
			projectRoot,
		)
		if _, err := database.InsertChunk(chunk, embedding); err != nil {
			t.Fatalf("InsertChunk(%s) failed: %v", path, err)
		}
	}

	const legacyPath = "/legacy/project/legacy.go"
	if _, err := database.backend.collection().Insert(embedding, map[string]any{
		"file_path":    legacyPath,
		"file_hash":    "legacy-hash",
		"project_root": "/legacy/project",
	}); err != nil {
		t.Fatalf("insert legacy record failed: %v", err)
	}

	if deleted, err := database.DeleteFile(t.Context(), "main.go"); err != nil {
		t.Fatalf("canonical DeleteFile failed: %v", err)
	} else if deleted != 1 {
		t.Fatalf("canonical DeleteFile deleted %d chunks, want 1", deleted)
	}
	if chunks, err := database.GetChunksByFile("pkg/main.go"); err != nil {
		t.Fatalf("GetChunksByFile(pkg/main.go) failed: %v", err)
	} else if len(chunks) != 1 {
		t.Fatalf("canonical deletion removed sibling path: got %d chunks", len(chunks))
	}

	if deleted, err := database.DeleteFile(t.Context(), legacyPath); err != nil {
		t.Fatalf("legacy DeleteFile failed: %v", err)
	} else if deleted != 1 {
		t.Fatalf("legacy DeleteFile deleted %d chunks, want 1", deleted)
	}
	if chunks, err := database.GetChunksByFile(legacyPath); err != nil {
		t.Fatalf("GetChunksByFile(legacy) failed: %v", err)
	} else if len(chunks) != 0 {
		t.Fatalf("legacy record remains after fallback deletion: got %d chunks", len(chunks))
	}
}

func TestDeleteProjectFileDoesNotCrossProjectBoundary(t *testing.T) {
	tmpDir := t.TempDir()
	const dimensions = 16
	database, err := Open("", dimensions, tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, root := range []string{"/projects/alpha", "/projects/beta"} {
		chunk := NewChunkRecord(root+"/main.go", "main.go", "hash-"+root, 10, "go", "package p", 1, 1, 0, 9, "generic", "", root)
		if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
			t.Fatal(err)
		}
	}

	if deleted, err := database.DeleteProjectFile(t.Context(), "/projects/alpha", "main.go"); err != nil {
		t.Fatal(err)
	} else if deleted != 1 {
		t.Fatalf("deleted %d chunks, want 1", deleted)
	}
	alpha, _ := database.GetFileHashes("/projects/alpha")
	beta, _ := database.GetFileHashes("/projects/beta")
	if len(alpha) != 0 {
		t.Fatalf("alpha hashes remain: %v", alpha)
	}
	if len(beta) != 1 || beta["main.go"] == "" {
		t.Fatalf("beta was affected: %v", beta)
	}
}

func TestChunkIdentitySeparatesPartsAndProjects(t *testing.T) {
	tmpDir := t.TempDir()
	const dimensions = 8
	database, err := Open("", dimensions, tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	base := NewChunkRecord("/alpha/one.go", "one.go", "hash", 20, "go", "first", 1, 1, 0, 0, "function", "Long", "/alpha")
	second := base
	second.Content = "second"
	second.ChunkIndex = 1
	otherProject := base
	otherProject.FilePath = "/beta/one.go"
	otherProject.ProjectRoot = "/beta"
	if _, err := database.InsertChunkBatch([]ChunkRecord{base, second, otherProject}, [][]float32{
		make([]float32, dimensions), make([]float32, dimensions), make([]float32, dimensions),
	}); err != nil {
		t.Fatal(err)
	}

	records := database.backend.collection().All()
	keys := make(map[string]struct{}, len(records))
	for _, record := range records {
		key := getStringPayload(record.Payload, "chunk_key")
		if _, duplicate := keys[key]; duplicate {
			t.Fatalf("duplicate chunk key %q", key)
		}
		keys[key] = struct{}{}
	}
	if len(keys) != 3 {
		t.Fatalf("chunk keys = %v", keys)
	}

	base.Content = "updated"
	if _, isNew, err := database.UpsertChunk(base, make([]float32, dimensions)); err != nil {
		t.Fatal(err)
	} else if isNew {
		t.Fatal("upsert inserted a duplicate first part")
	}
	if got := database.backend.collection().Count(); got != 3 {
		t.Fatalf("chunk count after upsert = %d, want 3", got)
	}
}
