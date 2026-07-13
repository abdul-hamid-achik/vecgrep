package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type stubStructuralSource struct {
	set *StructuralChunkSet
	err error
}

func (s stubStructuralSource) LoadStructuralChunks(context.Context, string) (*StructuralChunkSet, error) {
	return s.set, s.err
}

func TestLoadStructuralChunksAutoKeepsValidFiles(t *testing.T) {
	set := &StructuralChunkSet{
		Complete: true,
		Files: map[string]StructuralFileChunks{
			"fresh.go": {FileHash: strings.Repeat("a", 64), ProfileHash: "profile", Chunks: []Chunk{{Content: "fresh"}}},
		},
		Issues: []error{errors.New("stale.go: stale index")},
	}
	idx := NewIndexer(nil, nil, DefaultIndexerConfig())
	idx.SetStructuralChunkSource(stubStructuralSource{set: set}, false)

	got, warning, err := idx.loadStructuralChunks(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("hard error = %v", err)
	}
	if got != set || warning == nil || !strings.Contains(warning.Error(), "affected files") {
		t.Fatalf("set/warning = %#v / %v", got, warning)
	}
}

func TestLoadStructuralChunksAcceptsCompleteEmptySnapshot(t *testing.T) {
	set := &StructuralChunkSet{Complete: true, Files: map[string]StructuralFileChunks{}}
	idx := NewIndexer(nil, nil, DefaultIndexerConfig())
	idx.SetStructuralChunkSource(stubStructuralSource{set: set}, true)

	got, warning, err := idx.loadStructuralChunks(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("hard error = %v", err)
	}
	if warning != nil || got != set {
		t.Fatalf("set/warning = %#v / %v, want valid empty snapshot", got, warning)
	}
}

func TestStructuralChunkSourceSnapshotRestoresExactConfiguration(t *testing.T) {
	first := stubStructuralSource{set: &StructuralChunkSet{Complete: true, ProjectKey: "first"}}
	second := stubStructuralSource{set: &StructuralChunkSet{Complete: true, ProjectKey: "second"}}
	idx := NewIndexer(nil, nil, DefaultIndexerConfig())
	idx.SetStructuralChunkSource(first, true)
	snapshot := idx.SnapshotStructuralChunkSource()

	idx.SetStructuralChunkSource(second, false)
	idx.RestoreStructuralChunkSource(snapshot)

	got, warning, err := idx.loadStructuralChunks(context.Background(), t.TempDir())
	if err != nil || warning != nil {
		t.Fatalf("load restored source: warning=%v err=%v", warning, err)
	}
	if got == nil || got.ProjectKey != "first" {
		t.Fatalf("restored source returned %#v, want first source", got)
	}

	// The required bit is part of the same atomic snapshot.
	idx.RestoreStructuralChunkSource(snapshot)
	idx.SetStructuralChunkSource(stubStructuralSource{err: errors.New("unavailable")}, false)
	idx.RestoreStructuralChunkSource(snapshot)
	first.err = errors.New("required failure")
	idx.SetStructuralChunkSource(first, true)
	requiredSnapshot := idx.SnapshotStructuralChunkSource()
	idx.SetStructuralChunkSource(second, false)
	idx.RestoreStructuralChunkSource(requiredSnapshot)
	_, _, err = idx.loadStructuralChunks(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "required failure") {
		t.Fatalf("restored required source error = %v", err)
	}
}

func TestRequiredStructuralChunksFailBeforeFullReset(t *testing.T) {
	idx := NewIndexer(nil, nil, DefaultIndexerConfig())
	idx.SetStructuralChunkSource(stubStructuralSource{set: &StructuralChunkSet{
		Complete: true,
		Files:    map[string]StructuralFileChunks{"fresh.go": {Chunks: []Chunk{{Content: "fresh"}}}},
		Issues:   []error{errors.New("stale.go: stale index")},
	}}, true)

	_, err := idx.ReindexAll(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "stale.go") {
		t.Fatalf("error = %v, want required validation failure before nil DB reset", err)
	}
}

func TestRequiredStructuralHashMismatchFailsBeforeFullReset(t *testing.T) {
	root := t.TempDir()
	content := []byte("package fixture\n")
	if err := os.WriteFile(filepath.Join(root, "fresh.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	idx := NewIndexer(nil, nil, DefaultIndexerConfig())
	idx.SetStructuralChunkSource(stubStructuralSource{set: &StructuralChunkSet{
		Complete: true,
		Files: map[string]StructuralFileChunks{
			"fresh.go": {
				FileHash:    strings.Repeat("a", 64),
				FileSize:    int64(len(content)),
				ProfileHash: "profile",
				Chunks:      []Chunk{{Content: "package fixture"}},
			},
		},
	}}, true)

	_, err := idx.ReindexAll(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "changed before indexing") {
		t.Fatalf("error = %v, want required preflight failure before nil DB reset", err)
	}
}

func TestRequiredStructuralPreflightMaterializesFilesystemSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fresh.go")
	original := []byte("package fixture\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	rawHash, _, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	set := &StructuralChunkSet{Complete: true, Files: map[string]StructuralFileChunks{
		"fresh.go": {
			FileHash:    rawHash,
			FileSize:    int64(len(original)),
			ProfileHash: "profile",
			Chunks:      []Chunk{{Content: "package fixture"}},
		},
	}}
	idx := NewIndexer(nil, nil, DefaultIndexerConfig())
	prepared, err := idx.prepareRequiredStructuralFiles(context.Background(), root, nil, set)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 1 || string(prepared[0].content) != string(original) || prepared[0].sourceHash != rawHash {
		t.Fatalf("prepared snapshot = %+v", prepared)
	}
	if prepared[0].hash != structuralIndexHash(rawHash, set.Files["fresh.go"]) {
		t.Fatalf("prepared hash = %s, want structural hash", prepared[0].hash)
	}
}

func TestChunkFileEmbedsEnrichmentButStoresCleanPreview(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fresh.go")
	clean := `func Fresh() string { return "fresh" }`
	if err := os.WriteFile(path, []byte(clean), 0o644); err != nil {
		t.Fatal(err)
	}
	rawHash, content, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	structural := &StructuralChunkSet{Complete: true, Files: map[string]StructuralFileChunks{
		"fresh.go": {
			FileHash:    rawHash,
			ProfileHash: "profile",
			Chunks: []Chunk{{
				Content:          clean,
				EmbeddingContent: "docs\n\nsignature\n\n" + clean,
				StartLine:        1,
				EndLine:          1,
				ChunkType:        ChunkTypeFunction,
				SymbolName:       "fixture.Fresh",
			}},
		},
	}}
	idx := NewIndexer(nil, nil, DefaultIndexerConfig())
	items := make(chan embedItem, 2)
	results := make(chan fileResult, 1)
	idx.chunkFile(context.Background(), root, false, structural, fileInfo{
		path:         path,
		relativePath: "fresh.go",
		hash:         structuralIndexHash(rawHash, structural.Files["fresh.go"]),
		sourceHash:   rawHash,
		size:         int64(len(content)),
		content:      content,
	}, items, results)

	item := <-items
	if item.text != "docs\n\nsignature\n\n"+clean {
		t.Fatalf("embedding text = %q", item.text)
	}
	if got := item.task.records[item.slot].Content; got != clean {
		t.Fatalf("stored preview = %q, want clean source", got)
	}
	if got := item.task.records[item.slot].SymbolName; got != "fixture.Fresh" {
		t.Fatalf("stored symbol = %q", got)
	}
}

func TestStructuralFileHashMismatchFallsBack(t *testing.T) {
	set := &StructuralChunkSet{Complete: true, Files: map[string]StructuralFileChunks{
		"changed.go": {
			FileHash:    strings.Repeat("a", 64),
			ProfileHash: "profile",
			Chunks:      []Chunk{{Content: "structural", EmbeddingContent: "enriched"}},
		},
	}}
	if _, ok := structuralFileForHash(set, "changed.go", strings.Repeat("b", 64)); ok {
		t.Fatal("changed file must fall back to the built-in chunker")
	}
}

func TestEmbeddingContentRemainsRuneSafeAndBounded(t *testing.T) {
	got := embeddingContent(Chunk{EmbeddingContent: strings.Repeat("é", 3000)})
	if len(got) > defaultMaxChunkChars || !utf8.ValidString(got) {
		t.Fatalf("embedding content bytes=%d valid_utf8=%t", len(got), utf8.ValidString(got))
	}
}
