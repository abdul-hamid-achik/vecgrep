package db

import (
	"testing"

	"github.com/abdul-hamid-achik/veclite"
)

func TestSourceHashRoundTrip(t *testing.T) {
	const (
		dimensions  = 8
		projectRoot = "/projects/roundtrip"
		sourceHash  = "sha256:raw-source"
	)
	dataDir := t.TempDir()
	database, err := Open("", dimensions, dataDir)
	if err != nil {
		t.Fatal(err)
	}

	chunk := NewChunkRecord(
		projectRoot+"/main.go", "main.go", "chunk-hash", 32, "go",
		"package main", 1, 1, 0, 12, "generic", "", projectRoot,
	)
	chunk.SourceHash = sourceHash
	if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open("", dimensions, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	hashes, complete, err := database.GetSourceHashes(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("source hash index is incomplete after round trip")
	}
	if got := hashes["main.go"]; got != sourceHash {
		t.Fatalf("source hash = %q, want %q", got, sourceHash)
	}

	chunks, err := database.GetChunksByFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].SourceHash != sourceHash {
		t.Fatalf("chunk source hash round trip = %#v", chunks)
	}
	files, err := database.ListFiles(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].SourceHash != sourceHash {
		t.Fatalf("file source hash round trip = %#v", files)
	}
}

func TestSourceHashesAreProjectScoped(t *testing.T) {
	const dimensions = 8
	database, err := Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	for _, test := range []struct {
		root       string
		sourceHash string
	}{
		{root: "/projects/alpha", sourceHash: "alpha-source"},
		{root: "/projects/beta", sourceHash: "beta-source"},
	} {
		chunk := NewChunkRecord(
			test.root+"/main.go", "main.go", "chunk-"+test.sourceHash, 32, "go",
			"package main", 1, 1, 0, 12, "generic", "", test.root,
		)
		chunk.SourceHash = test.sourceHash
		if _, err := database.InsertChunk(chunk, make([]float32, dimensions)); err != nil {
			t.Fatal(err)
		}
	}

	alpha, complete, err := database.GetSourceHashes("/projects/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(alpha) != 1 || alpha["main.go"] != "alpha-source" {
		t.Fatalf("alpha source hashes = %v, complete = %v", alpha, complete)
	}
	beta, complete, err := database.GetSourceHashes("/projects/beta")
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(beta) != 1 || beta["main.go"] != "beta-source" {
		t.Fatalf("beta source hashes = %v, complete = %v", beta, complete)
	}
}

func TestSourceHashesReportLegacyRecordsAsIncomplete(t *testing.T) {
	const (
		dimensions  = 8
		projectRoot = "/projects/legacy"
	)
	database, err := Open("", dimensions, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	fresh := NewChunkRecord(
		projectRoot+"/fresh.go", "fresh.go", "fresh-chunk", 32, "go",
		"package fresh", 1, 1, 0, 13, "generic", "", projectRoot,
	)
	fresh.SourceHash = "fresh-source"
	if _, err := database.InsertChunk(fresh, make([]float32, dimensions)); err != nil {
		t.Fatal(err)
	}

	// Insert records in the pre-source_hash shape to exercise compatibility
	// with an index created by an older vecgrep binary.
	if _, err := database.backend.collection().Insert(make([]float32, dimensions), map[string]any{
		"file_path":     projectRoot + "/legacy.go",
		"relative_path": "legacy.go",
		"file_hash":     "legacy-chunk",
		"project_root":  projectRoot,
		"content":       "package legacy",
	}); err != nil {
		t.Fatal(err)
	}
	key := fileHashKey(projectRoot, "legacy.go")
	if _, _, err := database.backend.fileHashCollection().UpsertRecordByKey(fileHashKeyField, key, veclite.RecordInput{
		Payload: map[string]any{
			fileHashKeyField:    key,
			fileHashRecordField: fileHashRecordType,
			"file_path":         projectRoot + "/legacy.go",
			"relative_path":     "legacy.go",
			"file_hash":         "legacy-chunk",
			"project_root":      projectRoot,
		},
	}); err != nil {
		t.Fatal(err)
	}

	hashes, complete, err := database.GetSourceHashes(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatalf("legacy source hashes unexpectedly complete: %v", hashes)
	}
	if len(hashes) != 1 || hashes["fresh.go"] != "fresh-source" {
		t.Fatalf("known source hashes = %v", hashes)
	}
	if _, exists := hashes["legacy.go"]; exists {
		t.Fatalf("legacy file received a fabricated source hash: %v", hashes)
	}

	chunks, err := database.GetChunksByFile("legacy.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0].SourceHash != "" {
		t.Fatalf("legacy chunk source hash = %#v", chunks)
	}
	files, err := database.ListFiles(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.RelativePath == "legacy.go" && file.SourceHash != "" {
			t.Fatalf("legacy file source hash = %q", file.SourceHash)
		}
	}
}
