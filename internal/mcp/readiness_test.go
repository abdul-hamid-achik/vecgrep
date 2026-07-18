package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newReadinessTestServer builds an initialized SDKServer with optional chunks
// and an optional stored embedding profile mismatch.
func newReadinessTestServer(t *testing.T, withChunk bool, mismatchProfile bool) *SDKServer {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(base, "data")
	cfg.Embedding.Dimensions = 8
	cfg.Codemap.Enabled = false
	provider := mcpIndexProvider{dimensions: cfg.Embedding.Dimensions, model: "test"}
	session := newMCPSession(cfg, root, provider)
	session.projectName = "proj"

	// Always open once so the veclite collection exists (empty index is a real
	// registered project with zero chunks, not a missing store).
	database, release, err := session.acquireWriteDB(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if withChunk {
		content := "package main\n\nfunc LoadConfig() error { return nil }\n"
		chunk := db.NewChunkRecord(
			filepath.Join(root, "main.go"), "main.go", "hash", int64(len(content)), "go", content,
			1, 3, 0, len(content), "function", "LoadConfig", root,
		)
		vector := make([]float32, cfg.Embedding.Dimensions)
		vector[0] = 1
		if _, err := database.InsertChunk(chunk, vector); err != nil {
			_ = release()
			t.Fatal(err)
		}
		if mismatchProfile {
			stored := app.CurrentEmbeddingProfile(cfg)
			stored.Preprocessor = "code-chunker-v1"
			stored.ProfileID = "ollama:nomic-embed-text:768:cosine:code-chunker-v1"
			if err := app.SaveEmbeddingProfile(database, cfg.DataDir, stored); err != nil {
				_ = release()
				t.Fatal(err)
			}
		} else {
			if err := app.SaveEmbeddingProfile(database, cfg.DataDir, app.CurrentEmbeddingProfile(cfg)); err != nil {
				_ = release()
				t.Fatal(err)
			}
		}
	}
	if err := database.Sync(); err != nil {
		_ = release()
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	return &SDKServer{
		session:     session,
		projectRoot: root,
		initialized: true,
		codemapCfg:  config.CodemapConfig{Enabled: false},
	}
}

func toolText(t *testing.T, result *sdkmcp.CallToolResult, err error) (string, bool) {
	t.Helper()
	if err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("result = %#v", result)
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("content = %#v", result.Content)
	}
	return text.Text, result.IsError
}

func TestHandleSearch_EmptyIsErrorNotNoResults(t *testing.T) {
	s := newReadinessTestServer(t, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, structured, err := s.handleSearch(ctx, nil, SearchInput{Query: "auth", Mode: "keyword", Limit: 5})
	text, isErr := toolText(t, result, err)
	if !isErr {
		t.Fatalf("IsError = false, want true; body:\n%s", text)
	}
	if strings.Contains(text, "No results found") {
		t.Fatalf("empty index must not look like no-match:\n%s", text)
	}
	for _, want := range []string{`"state":"empty"`, `"action":"vecgrep_index"`, `"indexed":false`, "vecgrep_index"} {
		if !strings.Contains(text, want) {
			t.Fatalf("body missing %q:\n%s", want, text)
		}
	}
	r, ok := structured.(app.Readiness)
	if !ok || r.State != app.ReadinessEmpty {
		t.Fatalf("structured = %#v, want Readiness empty", structured)
	}
}

func TestHandleSearch_ProfileMismatchIsError(t *testing.T) {
	s := newReadinessTestServer(t, true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, structured, err := s.handleSearch(ctx, nil, SearchInput{Query: "LoadConfig", Mode: "keyword", Limit: 5})
	text, isErr := toolText(t, result, err)
	if !isErr {
		t.Fatalf("IsError = false, want true; body:\n%s", text)
	}
	if strings.Contains(text, "No results found") {
		t.Fatalf("profile mismatch must not look like no-match:\n%s", text)
	}
	for _, want := range []string{
		`"state":"profile_mismatch"`,
		`"action":"vecgrep_index force:true"`,
		`"profile_matches":false`,
		"code-chunker-v1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("body missing %q:\n%s", want, text)
		}
	}
	// Must not collapse to freshness-only guidance.
	if strings.Contains(text, "force for freshness") && !strings.Contains(text, "profile") {
		t.Fatalf("must mention profile, not only freshness:\n%s", text)
	}
	r, ok := structured.(app.Readiness)
	if !ok || r.State != app.ReadinessProfileMismatch {
		t.Fatalf("structured = %#v, want profile_mismatch", structured)
	}
	if r.StoredProfileID == "" || r.ActiveProfileID == "" || r.StoredProfileID == r.ActiveProfileID {
		t.Fatalf("profile IDs = stored %q active %q", r.StoredProfileID, r.ActiveProfileID)
	}
}

func TestHandleSearch_ReadyZeroHitsSuccess(t *testing.T) {
	s := newReadinessTestServer(t, true, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Nonsense query should miss keyword matches but remain success.
	result, structured, err := s.handleSearch(ctx, nil, SearchInput{
		Query: "zzzzzz_nonexistent_symbol_xyz_12345",
		Mode:  "keyword",
		Limit: 5,
	})
	text, isErr := toolText(t, result, err)
	if isErr {
		t.Fatalf("IsError = true, want false; body:\n%s", text)
	}
	if !strings.Contains(text, "No results found") {
		t.Fatalf("ready zero-hits should say No results found:\n%s", text)
	}
	if !strings.Contains(text, `"state":"stale"`) && !strings.Contains(text, `"state":"ready"`) && !strings.Contains(text, `"state":"unknown"`) {
		// Indexed with source file absent → typically stale; any non-blocking state is OK.
		t.Fatalf("expected searchable readiness state in body:\n%s", text)
	}
	if strings.Contains(text, `"state":"empty"`) {
		t.Fatalf("must not report empty when chunks exist:\n%s", text)
	}
	if r, ok := structured.(app.Readiness); ok && r.BlocksSearch() {
		t.Fatalf("structured readiness blocks search: %+v", r)
	}
}

func TestHandleStatus_ExposesReadiness(t *testing.T) {
	s := newReadinessTestServer(t, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, structured, err := s.handleStatus(ctx, nil, StatusInput{})
	text, isErr := toolText(t, result, err)
	if isErr {
		t.Fatalf("status IsError = true; body:\n%s", text)
	}
	for _, want := range []string{`"state":"empty"`, `"action":"vecgrep_index"`, "readiness:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status missing %q:\n%s", want, text)
		}
	}
	if r, ok := structured.(app.Readiness); !ok || r.State != app.ReadinessEmpty {
		t.Fatalf("structured = %#v, want empty readiness", structured)
	}
}

func TestHandleEnsure_CheckEmpty(t *testing.T) {
	s := newReadinessTestServer(t, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, structured, err := s.handleEnsure(ctx, nil, EnsureInput{Mode: "check"})
	text, isErr := toolText(t, result, err)
	if !isErr {
		t.Fatalf("check on empty should IsError; body:\n%s", text)
	}
	if !strings.Contains(text, `"state":"empty"`) {
		t.Fatalf("missing empty readiness:\n%s", text)
	}
	if r, ok := structured.(app.Readiness); !ok || r.State != app.ReadinessEmpty {
		t.Fatalf("structured = %#v", structured)
	}
}

func TestHandleSearch_DaemonPathGatesReadinessFirst(t *testing.T) {
	// Empty index with a "working" daemon that would return empty results.
	// Readiness must fail before the daemon search is used as success.
	socketPath, shutdown := startTestDaemon(t, func(method string, params json.RawMessage) (any, error) {
		// Should not be called for search when empty.
		t.Errorf("daemon method %s should not be called when readiness blocks", method)
		return map[string]any{"results": []any{}, "mode": "keyword"}, nil
	})
	defer shutdown()

	s := newReadinessTestServer(t, false, false)
	s.daemon = &daemonClient{socketPath: socketPath, projectRoot: s.projectRoot}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, _, err := s.handleSearch(ctx, nil, SearchInput{Query: "auth", Mode: "keyword", Limit: 5})
	text, isErr := toolText(t, result, err)
	if !isErr {
		t.Fatalf("want IsError for empty index even with daemon; body:\n%s", text)
	}
	if strings.Contains(text, "No results found") {
		t.Fatalf("daemon must not supply no-results for empty index:\n%s", text)
	}
}
