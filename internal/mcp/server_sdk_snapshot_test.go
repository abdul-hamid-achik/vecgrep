package mcp

import (
	"context"
	"encoding/json"
	"fmt"
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

type snapshotSearchProvider struct {
	dimensions int
	model      string
}

func (p *snapshotSearchProvider) Embed(context.Context, string) ([]float32, error) {
	vector := make([]float32, p.dimensions)
	vector[0] = 1
	return vector, nil
}

func (p *snapshotSearchProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i := range texts {
		vector, err := p.Embed(ctx, texts[i])
		if err != nil {
			return nil, err
		}
		result[i] = vector
	}
	return result, nil
}

func (p *snapshotSearchProvider) Model() string                                 { return p.model }
func (p *snapshotSearchProvider) Dimensions() int                               { return p.dimensions }
func (p *snapshotSearchProvider) Ping(context.Context) error                    { return nil }
func (p *snapshotSearchProvider) Warmup(context.Context) (time.Duration, error) { return 0, nil }

func TestReadHandlersKeepOneProjectActivationAcrossSwitch(t *testing.T) {
	type handlerCase struct {
		name      string
		stateHook bool
		run       func(context.Context, *SDKServer) (*sdkmcp.CallToolResult, any, error)
		wantScope bool
	}
	cases := []handlerCase{
		{
			name:      "search",
			stateHook: true,
			wantScope: true,
			run: func(ctx context.Context, s *SDKServer) (*sdkmcp.CallToolResult, any, error) {
				return s.handleSearch(ctx, nil, SearchInput{
					Query: "A_MARKER", Symbol: "Target", Mode: "keyword", Limit: 10, ContextLines: 1,
				})
			},
		},
		{
			name: "similar",
			run: func(ctx context.Context, s *SDKServer) (*sdkmcp.CallToolResult, any, error) {
				return s.handleSimilar(ctx, nil, SimilarInput{Text: "A_MARKER", Limit: 10})
			},
		},
		{
			name:      "investigate",
			wantScope: true,
			run: func(ctx context.Context, s *SDKServer) (*sdkmcp.CallToolResult, any, error) {
				return s.handleInvestigate(ctx, nil, InvestigateInput{
					Symbol: "Target", Query: "A_MARKER", Mode: "keyword", Limit: 10, ContextLines: 1,
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionA, rootA, providerA := newSnapshotSearchSession(t, "a", "A_MARKER")
			sessionB, rootB, _ := newSnapshotSearchSession(t, "b", "B_MARKER")
			defer func() { _ = sessionA.close() }()
			defer func() { _ = sessionB.close() }()

			codemapLog := filepath.Join(t.TempDir(), "codemap-b.log")
			codemapB := &CodemapClient{bin: writeLoggingCodemap(t, codemapLog)}
			sessionB.cfg.Codemap = config.CodemapConfig{
				Enabled:          true,
				ImpactDepth:      7,
				StructuralWeight: 0.9,
			}
			daemonA := &daemonClient{socketPath: filepath.Join(t.TempDir(), "missing-a.sock"), projectRoot: rootA}
			daemonB := &daemonClient{socketPath: filepath.Join(t.TempDir(), "missing-b.sock"), projectRoot: rootB}
			s := &SDKServer{
				session:     sessionA,
				daemon:      daemonA,
				projectRoot: rootA,
				initialized: true,
			}

			snapshotReady := make(chan struct{})
			resume := make(chan struct{})
			var blockOnce sync.Once
			assertStateA := func(handler string, state projectStateSnapshot) {
				if handler != tc.name {
					return
				}
				if state.session != sessionA || state.projectRoot != rootA || state.cfg != sessionA.cfg ||
					state.provider != providerA || state.daemon != daemonA || state.codemap != nil {
					t.Errorf("%s captured torn project state: %+v", tc.name, state)
				}
				blockOnce.Do(func() {
					close(snapshotReady)
					<-resume
				})
			}
			if tc.stateHook {
				s.stateSnapshotHook = assertStateA
			} else {
				s.readSnapshotHook = func(handler string, state projectReadSnapshot) {
					assertStateA(handler, state.projectStateSnapshot)
					if handler == tc.name && (state.database == nil || state.searcher == nil) {
						t.Errorf("%s snapshot omitted database/searcher", tc.name)
					}
				}
			}

			type handlerResponse struct {
				result *sdkmcp.CallToolResult
				err    error
			}
			response := make(chan handlerResponse, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			go func() {
				result, _, err := tc.run(ctx, s)
				response <- handlerResponse{result: result, err: err}
			}()
			select {
			case <-snapshotReady:
			case <-time.After(time.Second):
				t.Fatal("handler never captured project A")
			}

			s.stateMu.Lock()
			s.session = sessionB
			s.daemon = daemonB
			s.projectRoot = rootB
			s.initialized = true
			s.codemap = codemapB
			s.codemapCfg = sessionB.cfg.Codemap
			s.stateMu.Unlock()
			closedA := make(chan error, 1)
			go func() { closedA <- sessionA.close() }()
			waitForMCPSessionClosing(t, sessionA)
			select {
			case err := <-closedA:
				t.Fatalf("project A closed while %s still held its snapshot: %v", tc.name, err)
			default:
			}
			close(resume)

			var got handlerResponse
			select {
			case got = <-response:
			case <-time.After(3 * time.Second):
				t.Fatalf("%s did not finish", tc.name)
			}
			text := callToolText(t, got.result, got.err)
			if !strings.Contains(text, "A_MARKER") || strings.Contains(text, "B_MARKER") || strings.Contains(text, rootB) {
				t.Fatalf("%s mixed project B into project A response:\n%s", tc.name, text)
			}
			if tc.wantScope && !strings.Contains(text, "codemap is unavailable") {
				t.Fatalf("%s did not preserve A's codemap scope config:\n%s", tc.name, text)
			}
			if data, err := os.ReadFile(codemapLog); err == nil && len(data) > 0 {
				t.Fatalf("%s invoked project B codemap during A request: %s", tc.name, data)
			} else if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			select {
			case err := <-closedA:
				if err != nil {
					t.Fatalf("close project A: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("project A did not close after handler released its snapshot")
			}
		})
	}
}

func TestHandleSearchUsesDaemonBeforeReadOnlyLease(t *testing.T) {
	session, root, _ := newSnapshotSearchSession(t, "daemon", "LOCAL_MARKER")
	defer func() { _ = session.close() }()
	socketPath, shutdown := startTestDaemon(t, func(method string, params json.RawMessage) (any, error) {
		if method != "daemon.search" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return map[string]any{
			"mode": "hybrid",
			"results": []map[string]any{{
				"relative_path": "daemon.go",
				"content":       "DAEMON_MARKER",
				"language":      "go",
				"start_line":    1,
				"end_line":      1,
				"score":         0.99,
			}},
		}, nil
	})
	defer shutdown()

	// Search now gates readiness via a short RO lease, then prefers the daemon
	// for the actual query and releases the RO handle before returning so a
	// long-lived exclusive writer (separate process / later reindex) is not
	// blocked by a leftover MCP RO cache.
	s := &SDKServer{
		session:     session,
		daemon:      &daemonClient{socketPath: socketPath, projectRoot: root},
		projectRoot: root,
		initialized: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, _, err := s.handleSearch(ctx, nil, SearchInput{Query: "daemon query"})
	text := callToolText(t, result, err)
	if !strings.Contains(text, "DAEMON_MARKER") {
		t.Fatalf("daemon result missing:\n%s", text)
	}
	// Readiness opens a short RO lease then prefers daemon for hits. The
	// session may keep a cached RO handle after the lease ends; a subsequent
	// exclusive write must still be acquirable without waiting for ctx.
	writeCtx, writeCancel := context.WithTimeout(context.Background(), time.Second)
	defer writeCancel()
	_, releaseWriter, err := session.acquireWriteDB(writeCtx)
	if err != nil {
		t.Fatalf("post-search write lease should be available after readiness RO release: %v", err)
	}
	_ = releaseWriter()
}

func TestOverviewAndBatchSearchKeepOneProjectActivationAcrossSwitch(t *testing.T) {
	type handlerCase struct {
		name string
		run  func(context.Context, *SDKServer) (*sdkmcp.CallToolResult, any, error)
		want string
	}
	cases := []handlerCase{
		{
			name: "overview",
			want: "a-only.txt",
			run: func(ctx context.Context, s *SDKServer) (*sdkmcp.CallToolResult, any, error) {
				return s.handleOverview(ctx, nil, OverviewInput{IncludeStructure: true})
			},
		},
		{
			name: "batch_search",
			want: "A_MARKER",
			run: func(ctx context.Context, s *SDKServer) (*sdkmcp.CallToolResult, any, error) {
				return s.handleBatchSearch(ctx, nil, BatchSearchInput{Queries: []string{"A_MARKER"}, LimitPerQuery: 10})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionA, rootA, providerA := newSnapshotSearchSession(t, "a", "A_MARKER")
			sessionB, rootB, _ := newSnapshotSearchSession(t, "b", "B_MARKER")
			defer func() { _ = sessionA.close() }()
			defer func() { _ = sessionB.close() }()
			if err := os.WriteFile(filepath.Join(rootA, "a-only.txt"), []byte("A"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(rootB, "b-only.txt"), []byte("B"), 0o644); err != nil {
				t.Fatal(err)
			}
			daemonA := &daemonClient{socketPath: filepath.Join(t.TempDir(), "missing-a.sock"), projectRoot: rootA}
			s := &SDKServer{session: sessionA, daemon: daemonA, projectRoot: rootA, initialized: true}
			ready := make(chan struct{})
			resume := make(chan struct{})
			s.readSnapshotHook = func(handler string, state projectReadSnapshot) {
				if handler != tc.name {
					return
				}
				if state.session != sessionA || state.projectRoot != rootA || state.projectName != "a" ||
					state.cfg != sessionA.cfg || state.provider != providerA || state.daemon != daemonA ||
					state.database == nil || state.searcher == nil {
					t.Errorf("%s captured torn project snapshot: %+v", tc.name, state.projectStateSnapshot)
				}
				close(ready)
				<-resume
			}

			type response struct {
				result *sdkmcp.CallToolResult
				err    error
			}
			resultCh := make(chan response, 1)
			go func() {
				result, _, err := tc.run(context.Background(), s)
				resultCh <- response{result: result, err: err}
			}()
			<-ready
			s.stateMu.Lock()
			s.session = sessionB
			s.daemon = &daemonClient{socketPath: filepath.Join(t.TempDir(), "missing-b.sock"), projectRoot: rootB}
			s.projectRoot = rootB
			s.initialized = true
			s.stateMu.Unlock()
			closedA := make(chan error, 1)
			go func() { closedA <- sessionA.close() }()
			waitForMCPSessionClosing(t, sessionA)
			select {
			case err := <-closedA:
				t.Fatalf("project A closed while %s held its read snapshot: %v", tc.name, err)
			default:
			}
			close(resume)

			got := <-resultCh
			text := callToolText(t, got.result, got.err)
			if !strings.Contains(text, tc.want) || strings.Contains(text, "b-only.txt") || strings.Contains(text, "B_MARKER") {
				t.Fatalf("%s mixed project B into A response:\n%s", tc.name, text)
			}
			if err := <-closedA; err != nil {
				t.Fatalf("close project A: %v", err)
			}
		})
	}
}

func TestRelatedFilesKeepsRootAndCodemapFromOneActivation(t *testing.T) {
	sessionA, rootA, _ := newSnapshotSearchSession(t, "a", "A_MARKER")
	sessionB, rootB, _ := newSnapshotSearchSession(t, "b", "B_MARKER")
	defer func() { _ = sessionA.close() }()
	defer func() { _ = sessionB.close() }()
	write := func(root, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(rootA, "target.ts", "export const target = 1\n")
	write(rootA, "a_importer.ts", "import { target } from './target'\n")
	write(rootB, "target.ts", "export const target = 2\n")
	write(rootB, "b_importer.ts", "import { target } from './target'\n")
	codemapLog := filepath.Join(t.TempDir(), "codemap-b.log")
	codemapB := &CodemapClient{bin: writeLoggingCodemap(t, codemapLog)}
	sessionB.cfg.Codemap = config.CodemapConfig{Enabled: true}
	s := &SDKServer{session: sessionA, projectRoot: rootA, initialized: true}
	ready := make(chan struct{})
	resume := make(chan struct{})
	s.stateSnapshotHook = func(handler string, state projectStateSnapshot) {
		if handler != "related_files" {
			return
		}
		if state.session != sessionA || state.projectRoot != rootA || state.cfg != sessionA.cfg || state.codemap != nil {
			t.Errorf("related_files captured torn project state: %+v", state)
		}
		close(ready)
		<-resume
	}
	type response struct {
		result *sdkmcp.CallToolResult
		err    error
	}
	resultCh := make(chan response, 1)
	go func() {
		result, _, err := s.handleRelatedFiles(context.Background(), nil, RelatedFilesInput{
			File: "target.ts", Relationship: "imported_by", Limit: 10,
		})
		resultCh <- response{result: result, err: err}
	}()
	<-ready
	s.stateMu.Lock()
	s.session = sessionB
	s.projectRoot = rootB
	s.initialized = true
	s.codemap = codemapB
	s.codemapCfg = sessionB.cfg.Codemap
	s.stateMu.Unlock()
	closedA := make(chan error, 1)
	go func() { closedA <- sessionA.close() }()
	waitForMCPSessionClosing(t, sessionA)
	close(resume)
	got := <-resultCh
	text := callToolText(t, got.result, got.err)
	if !strings.Contains(text, "a_importer.ts") || strings.Contains(text, "b_importer.ts") || strings.Contains(text, rootB) {
		t.Fatalf("related_files mixed project B into A response:\n%s", text)
	}
	if data, err := os.ReadFile(codemapLog); err == nil && len(data) > 0 {
		t.Fatalf("related_files invoked project B codemap: %s", data)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := <-closedA; err != nil {
		t.Fatalf("close project A: %v", err)
	}
}

func TestBranchStatusKeepsRootAndProjectNameFromOneActivation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	gitBin := filepath.Join(t.TempDir(), "git")
	gitScript := `#!/bin/sh
case "$2" in
  --show-toplevel) printf '%s\n' "$PWD" ;;
  --short) printf 'abc123\n' ;;
  --abbrev-ref) printf 'branch-%s\n' "${PWD##*/}" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(gitBin, []byte(gitScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(gitBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeBranchIndexFixture(t, home, "a", "A_ACTIVE")
	writeBranchIndexFixture(t, home, "b", "B_ACTIVE")

	sessionA, rootA, _ := newSnapshotSearchSession(t, "a", "A_MARKER")
	sessionB, rootB, _ := newSnapshotSearchSession(t, "b", "B_MARKER")
	defer func() { _ = sessionA.close() }()
	defer func() { _ = sessionB.close() }()
	s := &SDKServer{session: sessionA, projectRoot: rootA, initialized: true}
	ready := make(chan struct{})
	resume := make(chan struct{})
	s.stateSnapshotHook = func(handler string, state projectStateSnapshot) {
		if handler != "branch_status" {
			return
		}
		if state.session != sessionA || state.projectRoot != rootA || state.projectName != "a" || state.cfg != sessionA.cfg {
			t.Errorf("branch_status captured torn project state: %+v", state)
		}
		close(ready)
		<-resume
	}
	type response struct {
		result *sdkmcp.CallToolResult
		err    error
	}
	resultCh := make(chan response, 1)
	go func() {
		result, _, err := s.handleBranchStatus(context.Background(), nil, BranchStatusInput{})
		resultCh <- response{result: result, err: err}
	}()
	<-ready
	s.stateMu.Lock()
	s.session = sessionB
	s.projectRoot = rootB
	s.initialized = true
	s.stateMu.Unlock()
	closedA := make(chan error, 1)
	go func() { closedA <- sessionA.close() }()
	waitForMCPSessionClosing(t, sessionA)
	close(resume)
	got := <-resultCh
	text := callToolText(t, got.result, got.err)
	if !strings.Contains(text, rootA) || !strings.Contains(text, "A_ACTIVE") || strings.Contains(text, rootB) || strings.Contains(text, "B_ACTIVE") {
		t.Fatalf("branch_status mixed project B into A response:\n%s", text)
	}
	if err := <-closedA; err != nil {
		t.Fatalf("close project A: %v", err)
	}
}

func writeBranchIndexFixture(t *testing.T, home, projectName, active string) {
	t.Helper()
	dir := filepath.Join(home, ".vecgrep", "projects", projectName, "branches")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"active_branch":%q,"branches":{"main":{"base_sha":"abc123","vector_count":1}}}`, active)
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newSnapshotSearchSession(t *testing.T, name, marker string) (*mcpSession, string, *snapshotSearchProvider) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(base, "data")
	cfg.Embedding.Dimensions = 8
	cfg.Codemap.Enabled = false
	provider := &snapshotSearchProvider{dimensions: cfg.Embedding.Dimensions, model: name}
	session := newMCPSession(cfg, root, provider)
	session.projectName = name

	database, release, err := session.acquireWriteDB(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range []string{"main.go", "second.go"} {
		content := fmt.Sprintf("package sample\n\n// %s %s\nfunc Symbol%d() {}\n", marker, file, i)
		if err := os.WriteFile(filepath.Join(root, file), []byte(content), 0o644); err != nil {
			_ = release()
			t.Fatal(err)
		}
		chunk := db.NewChunkRecord(
			filepath.Join(root, file), file, "hash-"+name+file, int64(len(content)), "go", content,
			1, 4, 0, len(content), "function", fmt.Sprintf("Symbol%d", i), root,
		)
		vector := make([]float32, cfg.Embedding.Dimensions)
		vector[0] = 1
		if _, err := database.InsertChunk(chunk, vector); err != nil {
			_ = release()
			t.Fatal(err)
		}
	}
	// Persist a matching embedding profile so readiness does not block search.
	if err := app.SaveEmbeddingProfile(database, cfg.DataDir, app.CurrentEmbeddingProfile(cfg)); err != nil {
		_ = release()
		t.Fatal(err)
	}
	if err := database.Sync(); err != nil {
		_ = release()
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	return session, root, provider
}

func writeLoggingCodemap(t *testing.T, logPath string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "codemap")
	quotedLog := strings.ReplaceAll(logPath, "'", "'\"'\"'")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s|%%s\\n' \"$PWD\" \"$*\" >> '%s'\nprintf '{}\\n'\n", quotedLog)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func callToolText(t *testing.T, result *sdkmcp.CallToolResult, err error) string {
	t.Helper()
	if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("tool result = %#v, error = %v", result, err)
	}
	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("tool content = %#v", result.Content)
	}
	return text.Text
}
