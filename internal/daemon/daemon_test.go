package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
)

func TestHandleSwitchBranchIsRejected(t *testing.T) {
	// The daemon cannot switch branches in place (it holds an exclusive lock on
	// one branch's index dir), so the handler must reject the request with
	// guidance rather than deadlock or corrupt state. A zero-value Daemon is
	// fine: the handler touches no fields.
	d := &Daemon{}
	resp := d.handleSwitchBranch(context.Background(), &jsonRPCRequest{
		ID:     json.RawMessage("1"),
		Method: "daemon.switchBranch",
		Params: json.RawMessage(`{"branch":"feature"}`),
	})
	if resp.Error == nil {
		t.Fatal("expected handleSwitchBranch to return an error")
	}
	if !strings.Contains(resp.Error.Message, "vecgrep branch switch") {
		t.Fatalf("error should point to the CLI branch-switch flow, got: %q", resp.Error.Message)
	}
}

func TestSearchParamsPreserveMinScore(t *testing.T) {
	want := searchParams{Project: "/project", Query: "needle", MinScore: 0.73}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got searchParams
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.MinScore != want.MinScore {
		t.Fatalf("min_score = %v, want %v", got.MinScore, want.MinScore)
	}
}

func TestProjectWorkerCloseDrainsOperationsAndRejectsNewWork(t *testing.T) {
	w := &projectWorker{}
	if !w.beginOperation() {
		t.Fatal("initial operation was rejected")
	}
	closed := make(chan struct{})
	go func() {
		w.close()
		close(closed)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		w.operationsMu.Lock()
		closing := w.closing
		w.operationsMu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never entered closing state")
		}
		runtime.Gosched()
	}
	if w.beginOperation() {
		w.endOperation()
		t.Fatal("worker accepted an operation after close began")
	}
	select {
	case <-closed:
		t.Fatal("close returned before the active operation drained")
	default:
	}

	w.endOperation()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not return after the active operation ended")
	}
}

func TestReadStateNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := ReadState(tmpDir)
	if err == nil {
		t.Fatal("expected error when state file does not exist")
	}
}

func TestReadStateValid(t *testing.T) {
	tmpDir := t.TempDir()
	state := DaemonState{
		ProjectRoot:  "/tmp/project",
		ProjectName:  "test-project",
		PID:          12345,
		StartedAt:    time.Now().Truncate(time.Second),
		LastActivity: time.Now().Truncate(time.Second),
		ActiveBranch: "main",
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.json"), data, 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	loaded, err := ReadState(tmpDir)
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	if loaded.ProjectName != state.ProjectName {
		t.Errorf("ProjectName = %q, want %q", loaded.ProjectName, state.ProjectName)
	}
	if loaded.PID != state.PID {
		t.Errorf("PID = %d, want %d", loaded.PID, state.PID)
	}
	if loaded.ActiveBranch != state.ActiveBranch {
		t.Errorf("ActiveBranch = %q, want %q", loaded.ActiveBranch, state.ActiveBranch)
	}
}

func TestReadStateCorrupt(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.json"), []byte("not json"), 0644); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	_, err := ReadState(tmpDir)
	if err == nil {
		t.Fatal("expected error for corrupt state file")
	}
}

func TestIsRunningNoLockFile(t *testing.T) {
	tmpDir := t.TempDir()
	if IsRunning(tmpDir) {
		t.Fatal("expected IsRunning=false when no lock file exists")
	}
}

func TestIsRunningStaleLock(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a lock file but no socket → stale lock
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.lock"), []byte("99999\n"), 0644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}

	if IsRunning(tmpDir) {
		t.Fatal("expected IsRunning=false when lock exists but socket is not responding")
	}
}

func TestIsRunningWedgedSocket(t *testing.T) {
	// A socket that accepts connections but never answers the ping (a wedged
	// daemon, or a foreign process squatting on the path) must report
	// not-running within the probe deadline instead of blocking forever —
	// IsRunning is on interactive paths (studio startup, CLI status).
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "daemon.lock"), []byte("99999\n"), 0644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
	ln, err := net.Listen("unix", filepath.Join(tmpDir, "daemon.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open without ever writing a response.
			go func(c net.Conn) {
				<-done
				c.Close()
			}(conn)
		}
	}()

	start := time.Now()
	if IsRunning(tmpDir) {
		t.Fatal("expected IsRunning=false against a socket that never responds")
	}
	if elapsed := time.Since(start); elapsed > isRunningTimeout+2*time.Second {
		t.Fatalf("IsRunning took %v, expected it to give up within ~%v", elapsed, isRunningTimeout)
	}
}

func TestDaemonStateJSONRoundTrip(t *testing.T) {
	original := DaemonState{
		ProjectRoot:  "/home/user/project",
		ProjectName:  "myproject",
		PID:          42,
		StartedAt:    time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		LastActivity: time.Date(2026, 1, 1, 12, 5, 0, 0, time.UTC),
		ActiveBranch: "feature/test",
		QueueDepth:   3,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var loaded DaemonState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if loaded.ProjectRoot != original.ProjectRoot {
		t.Errorf("ProjectRoot = %q, want %q", loaded.ProjectRoot, original.ProjectRoot)
	}
	if loaded.PID != original.PID {
		t.Errorf("PID = %d, want %d", loaded.PID, original.PID)
	}
	if loaded.QueueDepth != original.QueueDepth {
		t.Errorf("QueueDepth = %d, want %d", loaded.QueueDepth, original.QueueDepth)
	}
}

func TestParseSweepInterval(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"invalid", 0},
		{"-1h", 0},
		{"24h", 24 * time.Hour},
		{"6h", 6 * time.Hour},
		{"30m", 30 * time.Minute},
	}
	for _, tc := range tests {
		got := parseSweepInterval(tc.input)
		if got != tc.want {
			t.Errorf("parseSweepInterval(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// TestReindexSyncWireRoundTrip verifies the daemon→client wire shape for a
// daemon.reindex_sync result round-trips through JSON (errors stringified,
// duration preserved), so the CLI decodes a faithful IndexResult.
func TestReindexSyncWireRoundTrip(t *testing.T) {
	original := reindexSyncResult{
		FilesProcessed: 12,
		FilesSkipped:   3,
		FilesDeleted:   2,
		ChunksCreated:  40,
		Duration:       1500 * time.Millisecond,
		Errors:         []string{"boom", "warn"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var loaded reindexSyncResult
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.FilesProcessed != original.FilesProcessed || loaded.FilesDeleted != original.FilesDeleted || loaded.ChunksCreated != original.ChunksCreated ||
		loaded.Duration != original.Duration || len(loaded.Errors) != 2 {
		t.Fatalf("round-trip mismatch: %+v", loaded)
	}
}

// TestReindexSyncClientNoDaemon verifies the client errors clearly when no
// daemon is listening (the CLI falls back to a local index in that case, but
// if it ever calls this with a stale IsRunning, it must not hang).
func TestReindexSyncClientNoDaemon(t *testing.T) {

	tmp := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ReindexSync(ctx, tmp, "/some/project", app.IndexRequest{}); err == nil {
		t.Fatal("ReindexSync with no daemon should error")
	}
}

// TestReindexSyncClientDecodesResult starts a stub unix-socket server that
// answers daemon.reindex_sync with a canned result and an error envelope, and
// verifies the client decodes the IndexResult and surfaces the daemon error.
func TestReindexSyncClientDecodesResult(t *testing.T) {
	// A short /tmp-based dir keeps the unix socket path within the OS limit.
	base := "/tmp"
	if _, err := os.Stat(base); err != nil {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "vecgsync")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var callCount atomic.Int32
	requests := make(chan rpcRequestExternal, 2)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleStubReindex(c, &callCount, requests)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First call: canned success result.
	res, err := ReindexSync(ctx, dir, "/proj", app.IndexRequest{
		Paths:             []string{"pkg/only.go"},
		FullReindex:       true,
		AdditionalIgnores: []string{"generated/**"},
		StructuralChunks:  "required",
	})
	if err != nil {
		t.Fatalf("ReindexSync success: %v", err)
	}
	if res.FilesProcessed != 7 || res.ChunksCreated != 19 {
		t.Errorf("decoded result = %+v, want FilesProcessed=7 ChunksCreated=19", res)
	}
	if len(res.Errors) != 1 || res.Errors[0].Error() != "stub warning" {
		t.Errorf("decoded errors = %v, want [stub warning]", res.Errors)
	}
	firstReq := <-requests
	var params reindexSyncParams
	if err := json.Unmarshal(firstReq.Params, &params); err != nil {
		t.Fatalf("decode forwarded params: %v", err)
	}
	if !params.Full || params.StructuralChunks != "required" || len(params.Paths) != 1 || params.Paths[0] != "pkg/only.go" || len(params.AdditionalIgnores) != 1 {
		t.Fatalf("forwarded params = %+v", params)
	}

	// Second call: the stub returns a JSON-RPC error envelope.
	_, err = ReindexSync(ctx, dir, "/proj", app.IndexRequest{})
	if err == nil || !strings.Contains(err.Error(), "stub failure") {
		t.Fatalf("expected 'stub failure' error, got %v", err)
	}
}

// handleStubReindex answers one request per connection: the first connection
// gets a success result, the second gets a JSON-RPC error envelope.
func handleStubReindex(c net.Conn, count *atomic.Int32, requests chan<- rpcRequestExternal) {
	defer c.Close()
	dec := json.NewDecoder(c)
	enc := json.NewEncoder(c)
	var req rpcRequestExternal
	if err := dec.Decode(&req); err != nil {
		return
	}
	requests <- req
	if count.Add(1) == 1 {
		out := reindexSyncResult{FilesProcessed: 7, ChunksCreated: 19, Duration: 42 * time.Millisecond, Errors: []string{"stub warning"}}
		resultJSON, _ := json.Marshal(out)
		_ = enc.Encode(map[string]any{
			"jsonrpc": "2.0", "id": "1", "result": json.RawMessage(resultJSON),
		})
		return
	}
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": "1",
		"error": map[string]any{"code": -32000, "message": "stub failure"},
	})
}

// rpcRequestExternal mirrors the wire request shape for the stub server.
type rpcRequestExternal struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}
