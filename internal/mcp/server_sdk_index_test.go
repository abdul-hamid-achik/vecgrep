package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpIndexProvider struct {
	dimensions int
	model      string
}

func (p mcpIndexProvider) Embed(context.Context, string) ([]float32, error) {
	return make([]float32, p.dimensions), nil
}

func (p mcpIndexProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = make([]float32, p.dimensions)
	}
	return result, nil
}

func (p mcpIndexProvider) Model() string                                 { return p.model }
func (p mcpIndexProvider) Dimensions() int                               { return p.dimensions }
func (p mcpIndexProvider) Ping(context.Context) error                    { return nil }
func (p mcpIndexProvider) Warmup(context.Context) (time.Duration, error) { return 0, nil }

type mcpTrackingIndexSource struct {
	database *db.DB
	mu       sync.Mutex
	acquired int
	released int
}

func (s *mcpTrackingIndexSource) AcquireIndexDB(context.Context) (app.IndexDBLease, error) {
	s.mu.Lock()
	s.acquired++
	s.mu.Unlock()
	return app.IndexDBLease{
		DB: s.database,
		Release: func() error {
			s.mu.Lock()
			s.released++
			s.mu.Unlock()
			return nil
		},
	}, nil
}

func (s *mcpTrackingIndexSource) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquired, s.released
}

func TestHandleIndexRequiredSurfacesDaemonFailure(t *testing.T) {
	socketPath, shutdown := startTestDaemon(t, func(method string, params json.RawMessage) (any, error) {
		if method != "daemon.reindex_sync" {
			return nil, &testError{"unexpected method: " + method}
		}
		return nil, &testError{"required structural export failed"}
	})
	defer shutdown()

	s := &SDKServer{
		session:     &mcpSession{},
		daemon:      &daemonClient{socketPath: socketPath, projectRoot: "/project"},
		projectRoot: "/project",
		initialized: true,
		codemapCfg: config.CodemapConfig{
			StructuralChunks: "required",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, _, err := s.handleIndex(ctx, nil, IndexInput{})
	if err != nil {
		t.Fatalf("handleIndex transport error: %v", err)
	}
	if result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("result = %#v, want MCP tool error", result)
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.Contains(text.Text, "required structural export failed") {
		t.Fatalf("result content = %#v", result.Content)
	}
}

func TestHandleIndexDelegatesToAppCoordinator(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(root, config.DefaultDataDir)
	cfg.Embedding.Dimensions = 8
	cfg.Codemap.Enabled = false
	cfg.Codemap.StructuralChunks = string(app.StructuralChunksOff)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open("", cfg.Embedding.Dimensions, cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	provider := mcpIndexProvider{dimensions: cfg.Embedding.Dimensions, model: cfg.Embedding.Model}
	stores := &mcpTrackingIndexSource{database: database}
	coordinator := app.NewIndexCoordinator(root, cfg, provider, stores)
	session := &mcpSession{
		cfg:         cfg,
		projectRoot: root,
		provider:    provider,
		coordinator: coordinator,
	}
	s := &SDKServer{
		session:     session,
		projectRoot: root,
		initialized: true,
		codemapCfg:  cfg.Codemap,
	}

	result, _, err := s.handleIndex(context.Background(), nil, IndexInput{})
	if err != nil {
		t.Fatalf("handleIndex transport error: %v", err)
	}
	if result == nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("result = %#v, want successful index response", result)
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok || !strings.Contains(text.Text, "Indexing complete") {
		t.Fatalf("result content = %#v", result.Content)
	}
	acquired, released := stores.counts()
	if acquired != 1 || released != 1 {
		t.Fatalf("coordinator leases = acquired:%d released:%d, want 1/1", acquired, released)
	}
}

func TestSnapshotProjectStateIsCoherent(t *testing.T) {
	stateA := projectStateSnapshot{
		session:     &mcpSession{projectRoot: "/a"},
		daemon:      &daemonClient{projectRoot: "/a"},
		projectRoot: "/a",
		initialized: true,
		codemap:     &CodemapClient{bin: "a"},
		codemapCfg:  config.CodemapConfig{Bin: "a"},
	}
	stateB := projectStateSnapshot{
		session:     &mcpSession{projectRoot: "/b"},
		daemon:      &daemonClient{projectRoot: "/b"},
		projectRoot: "/b",
		initialized: true,
		codemap:     &CodemapClient{bin: "b"},
		codemapCfg:  config.CodemapConfig{Bin: "b"},
	}
	s := &SDKServer{}
	setState := func(state projectStateSnapshot) {
		s.stateMu.Lock()
		s.session = state.session
		s.daemon = state.daemon
		s.projectRoot = state.projectRoot
		s.initialized = state.initialized
		s.codemap = state.codemap
		s.codemapCfg = state.codemapCfg
		s.stateMu.Unlock()
	}
	setState(stateA)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10_000; i++ {
			if i%2 == 0 {
				setState(stateB)
			} else {
				setState(stateA)
			}
		}
	}()
	for i := 0; i < 10_000; i++ {
		got := s.snapshotProjectState()
		wantTag := strings.TrimPrefix(got.projectRoot, "/")
		if !got.initialized || got.session == nil || got.daemon == nil || got.codemap == nil ||
			got.session.projectRoot != got.projectRoot || got.daemon.projectRoot != got.projectRoot ||
			got.codemap.bin != wantTag || got.codemapCfg.Bin != wantTag {
			t.Fatalf("torn project snapshot: %+v", got)
		}
	}
	wg.Wait()
}

func TestHandleStatusKeepsOneActivationSnapshotDuringConcurrentSwitch(t *testing.T) {
	sessionA, rootA := newDeleteTestSession(t, "status-a")
	sessionB, rootB := newDeleteTestSession(t, "status-b")
	defer func() { _ = sessionA.close() }()
	defer func() { _ = sessionB.close() }()
	sessionA.provider = mcpIndexProvider{dimensions: 8, model: "status-a"}
	sessionB.provider = mcpIndexProvider{dimensions: 8, model: "status-b"}

	// If status re-resolves config after the switch, this B-only setting makes
	// the torn response observable as a codemap section appended to A's stats.
	if err := os.WriteFile(filepath.Join(rootB, "vecgrep.yaml"), []byte("codemap:\n  enabled: true\n  bin: definitely-missing-codemap\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sessionB.cfg.Codemap = config.CodemapConfig{Enabled: true, Bin: "definitely-missing-codemap"}

	s := &SDKServer{
		session:     sessionA,
		daemon:      &daemonClient{socketPath: filepath.Join(t.TempDir(), "missing.sock"), projectRoot: rootA},
		projectRoot: rootA,
		initialized: true,
	}
	snapshotReady := make(chan struct{})
	resumeStatus := make(chan struct{})
	s.statusSnapshotHook = func(state projectReadSnapshot) {
		if state.projectRoot != rootA || state.cfg != sessionA.cfg || state.database == nil || state.searcher == nil || state.daemon == nil || state.daemon.projectRoot != rootA {
			t.Errorf("status captured torn A snapshot: %+v", state.projectStateSnapshot)
		}
		close(snapshotReady)
		<-resumeStatus
	}

	type statusResponse struct {
		result *sdkmcp.CallToolResult
		err    error
	}
	response := make(chan statusResponse, 1)
	go func() {
		result, _, err := s.handleStatus(context.Background(), nil, StatusInput{})
		response <- statusResponse{result: result, err: err}
	}()
	<-snapshotReady

	// Mirror activateProject's atomic swap, then close A concurrently. The A
	// read lease must keep its DB alive and every remaining status dependency
	// must still come from A.
	s.stateMu.Lock()
	s.session = sessionB
	s.daemon = &daemonClient{socketPath: filepath.Join(t.TempDir(), "missing.sock"), projectRoot: rootB}
	s.projectRoot = rootB
	s.initialized = true
	s.codemap = &CodemapClient{bin: "definitely-missing-codemap"}
	s.codemapCfg = sessionB.cfg.Codemap
	s.stateMu.Unlock()
	closeDone := make(chan error, 1)
	go func() { closeDone <- sessionA.close() }()
	close(resumeStatus)

	var got statusResponse
	select {
	case got = <-response:
	case <-time.After(3 * time.Second):
		t.Fatal("handleStatus did not finish")
	}
	if got.err != nil || got.result == nil || got.result.IsError || len(got.result.Content) != 1 {
		t.Fatalf("handleStatus result = %#v, error = %v", got.result, got.err)
	}
	text, ok := got.result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("status content = %#v", got.result.Content)
	}
	if !strings.Contains(text.Text, "Total files: 1") || !strings.Contains(text.Text, "Total chunks: 1") {
		t.Fatalf("status lost A database/searcher snapshot:\n%s", text.Text)
	}
	if strings.Contains(text.Text, "Codemap integration: enabled") || strings.Contains(text.Text, rootB) {
		t.Fatalf("status mixed B config/root into A response:\n%s", text.Text)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close A: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("A session close did not wait for/recover after status lease")
	}
}

func TestHandleDeleteKeepsDatabaseAndProjectRootFromSameSnapshot(t *testing.T) {
	sessionA, rootA := newDeleteTestSession(t, "a")
	sessionB, rootB := newDeleteTestSession(t, "b")
	defer func() { _ = sessionA.close() }()
	defer func() { _ = sessionB.close() }()

	_, releaseRO, err := sessionA.acquireRO()
	if err != nil {
		t.Fatal(err)
	}
	s := &SDKServer{session: sessionA, projectRoot: rootA, initialized: true}
	type deleteResponse struct {
		result *sdkmcp.CallToolResult
		err    error
	}
	response := make(chan deleteResponse, 1)
	go func() {
		result, _, err := s.handleDelete(context.Background(), nil, DeleteInput{FilePath: "main.go"})
		response <- deleteResponse{result: result, err: err}
	}()
	waitForMCPSessionOperation(t, sessionA)

	// Switch the server while delete is waiting for A's read lease. The handler
	// must continue with both the DB and root from snapshot A.
	s.stateMu.Lock()
	s.session = sessionB
	s.projectRoot = rootB
	s.initialized = true
	s.stateMu.Unlock()
	releaseRO()

	select {
	case got := <-response:
		if got.err != nil || got.result == nil || got.result.IsError {
			t.Fatalf("handleDelete result = %#v, error = %v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handleDelete did not finish")
	}
	assertProjectChunkCount(t, sessionA, "main.go", 0)
	assertProjectChunkCount(t, sessionB, "main.go", 1)
}

func newDeleteTestSession(t *testing.T, name string) (*mcpSession, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, name)
	dataDir := filepath.Join(base, "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.Embedding.Dimensions = 8
	session := newMCPSession(cfg, root, nil)
	database, err := session.readWriteDB()
	if err != nil {
		t.Fatal(err)
	}
	chunk := db.NewChunkRecord(
		filepath.Join(root, "main.go"), "main.go", "hash-"+name, 12, "go",
		"package main", 1, 1, 0, 12, "generic", "", root,
	)
	if _, err := database.InsertChunk(chunk, make([]float32, cfg.Embedding.Dimensions)); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Sync(); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return session, root
}

func waitForMCPSessionOperation(t *testing.T, session *mcpSession) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		session.mu.Lock()
		operations := session.operations
		session.mu.Unlock()
		if operations > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session operation did not start")
		}
		time.Sleep(time.Millisecond)
	}
}

func assertProjectChunkCount(t *testing.T, session *mcpSession, file string, want int) {
	t.Helper()
	database, release, err := session.acquireRO()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	chunks, err := database.GetChunksByFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != want {
		t.Fatalf("chunk count for %s = %d, want %d", file, len(chunks), want)
	}
}
