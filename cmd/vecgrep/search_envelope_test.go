package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

// createTestService builds a read-write app session backed by a temp project,
// mirroring internal/app's createTestSession so the main package can exercise
// service-level helpers (printSearchEnvelope) against a real veclite index.
func createTestService(t *testing.T) (*app.Service, *app.Session) {
	t.Helper()

	projectRoot := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(projectRoot, config.DefaultDataDir)
	cfg.DBPath = filepath.Join(cfg.DataDir, config.DefaultDBFile)
	if err := cfg.EnsureDataDir(); err != nil {
		t.Fatalf("EnsureDataDir failed: %v", err)
	}
	if err := cfg.WriteDefaultConfig(); err != nil {
		t.Fatalf("WriteDefaultConfig failed: %v", err)
	}

	session, err := app.OpenSession(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	return app.NewService(session), session
}

func TestIsMachineFormat(t *testing.T) {
	cases := map[string]bool{
		"":              false,
		"default":       false,
		"json":          true,
		"compact":       true,
		"json-envelope": true,
		"yaml":          false,
	}
	for in, want := range cases {
		if got := isMachineFormat(in); got != want {
			t.Errorf("isMachineFormat(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPrintSearchEnvelope_AbsentIndex(t *testing.T) {
	service, _ := createTestService(t)

	// A never-indexed project must report indexed=false, fresh=false,
	// chunks=0 so a consumer can distinguish "never indexed" from "indexed
	// but nothing matched" — the bare-array json format cannot.
	indexed, fresh, chunks, err := service.IndexMeta(context.Background())
	if err != nil {
		t.Fatalf("IndexMeta failed: %v", err)
	}
	if indexed || fresh || chunks != 0 {
		t.Fatalf("absent index = indexed=%v fresh=%v chunks=%d, want false/false/0", indexed, fresh, chunks)
	}
}

func TestPrintSearchEnvelope_IndexedWithHits(t *testing.T) {
	service, session := createTestService(t)

	// Insert one chunk so the index is non-empty.
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

	// Build the envelope exactly as printSearchEnvelope does, against a real
	// service, to pin the JSON contract shape.
	indexed, fresh, chunks, err := service.IndexMeta(context.Background())
	if err != nil {
		t.Fatalf("IndexMeta failed: %v", err)
	}
	if !indexed || chunks != 1 {
		t.Fatalf("indexed index = indexed=%v chunks=%d, want true/1", indexed, chunks)
	}
	// The source file isn't on disk in the temp project, so the indexer sees
	// a pending deletion → not fresh. Assert it's false (deterministic).
	if fresh {
		t.Fatalf("fresh = true, want false (source file absent from disk)")
	}

	envelope := searchEnvelope{
		SchemaVersion: searchEnvelopeSchemaVersion,
		Hits:          []search.Result{{RelativePath: "main.go", Score: 0.9}},
	}
	envelope.Index.Indexed = indexed
	envelope.Index.Fresh = fresh
	envelope.Index.Chunks = chunks

	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var got struct {
		SchemaVersion int `json:"schema_version"`
		Index         struct {
			Indexed bool `json:"indexed"`
			Fresh   bool `json:"fresh"`
			Chunks  int  `json:"chunks"`
		} `json:"index"`
		Hits []search.Result `json:"hits"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("envelope not parseable as a single JSON document: %v", err)
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", got.SchemaVersion)
	}
	if got.Index.Indexed != true || got.Index.Fresh != false || got.Index.Chunks != 1 {
		t.Errorf("index block = %+v, want {true false 1}", got.Index)
	}
	if len(got.Hits) != 1 || got.Hits[0].RelativePath != "main.go" {
		t.Errorf("hits = %+v, want one main.go hit", got.Hits)
	}
}

func TestSearchCommandExposesMinScoreAndEnvelopeFormat(t *testing.T) {
	if searchCmd.Flags().Lookup("min-score") == nil {
		t.Fatal("search command missing --min-score flag")
	}
	if similarCmd.Flags().Lookup("min-score") == nil {
		t.Fatal("similar command missing --min-score flag")
	}
	help := searchCmd.Flags().Lookup("format").Usage
	if !strings.Contains(help, "json-envelope") {
		t.Errorf("search --format help = %q, want it to advertise json-envelope", help)
	}
}

func TestWriteMemoryProviderUnavailable(t *testing.T) {
	var buf bytes.Buffer
	writeMemoryProviderUnavailable(&buf)
	var got map[string]string
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("envelope not parseable: %v (got %q)", err, buf.String())
	}
	if got["error"] != "provider_unavailable" {
		t.Errorf("error = %q, want %q", got["error"], "provider_unavailable")
	}
}
