package mcp

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// startTestDaemon starts a minimal unix socket server that speaks the
// newline-delimited JSON-RPC protocol, for testing daemonClient.
// Returns the socket path and a shutdown function.
func startTestDaemon(t *testing.T, handler func(method string, params json.RawMessage) (any, error)) (string, func()) {
	t.Helper()
	// Use /tmp directly to avoid long temp paths that exceed the unix socket
	// path limit on macOS (104 chars).
	dir, err := os.MkdirTemp("/tmp", "vecgrep-test-*")
	if err != nil {
		t.Fatalf("mkdirtemp: %v", err)
	}
	socketPath := filepath.Join(dir, "daemon.sock")

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				dec := json.NewDecoder(conn)
				enc := json.NewEncoder(conn)
				for dec.More() {
					var req struct {
						Method string          `json:"method"`
						Params json.RawMessage `json:"params,omitempty"`
					}
					if err := dec.Decode(&req); err != nil {
						return
					}
					result, err := handler(req.Method, req.Params)
					if err != nil {
						_ = enc.Encode(map[string]any{
							"jsonrpc": "2.0",
							"id":      "1",
							"error":   map[string]any{"code": -32603, "message": err.Error()},
						})
						continue
					}
					_ = enc.Encode(map[string]any{
						"jsonrpc": "2.0",
						"id":      "1",
						"result":  result,
					})
				}
			}(conn)
		}
	}()

	shutdown := func() {
		_ = listener.Close()
		<-doneCh
		_ = os.RemoveAll(socketPath)
	}
	return socketPath, shutdown
}

func TestDaemonClientAvailableFalseForMissingSocket(t *testing.T) {
	c := newDaemonClient("/nonexistent/path", "")
	if c.available() {
		t.Fatal("available should be false for missing socket")
	}
}

func TestDaemonClientAvailableTrueForLiveSocket(t *testing.T) {
	socketPath, shutdown := startTestDaemon(t, func(method string, params json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	defer shutdown()

	c := newDaemonClient(filepath.Dir(socketPath), "")
	// The socket is at dir/daemon.sock
	c.socketPath = socketPath

	if !c.available() {
		t.Fatal("available should be true for live socket")
	}
}

func TestDaemonClientSearch(t *testing.T) {
	socketPath, shutdown := startTestDaemon(t, func(method string, params json.RawMessage) (any, error) {
		if method != "daemon.search" {
			return nil, &testError{"unexpected method: " + method}
		}
		return map[string]any{
			"results": []map[string]any{},
			"mode":    "hybrid",
		}, nil
	})
	defer shutdown()

	c := &daemonClient{socketPath: socketPath}
	raw, err := c.search(context.Background(), daemonSearchParams{
		Query: "test query",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	var result struct {
		Results []map[string]any `json:"results"`
		Mode    string           `json:"mode"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result.Mode != "hybrid" {
		t.Fatalf("expected mode 'hybrid', got '%s'", result.Mode)
	}
}

func TestDaemonClientReindex(t *testing.T) {
	socketPath, shutdown := startTestDaemon(t, func(method string, params json.RawMessage) (any, error) {
		if method != "daemon.reindex" {
			return nil, &testError{"unexpected method: " + method}
		}
		return map[string]any{"started": true}, nil
	})
	defer shutdown()

	c := &daemonClient{socketPath: socketPath}
	if err := c.reindex(context.Background()); err != nil {
		t.Fatalf("reindex: %v", err)
	}
}

func TestDaemonClientReindexSyncPreservesOptionsAndResult(t *testing.T) {
	socketPath, shutdown := startTestDaemon(t, func(method string, params json.RawMessage) (any, error) {
		if method != "daemon.reindex_sync" {
			return nil, &testError{"unexpected method: " + method}
		}
		var got struct {
			Project          string   `json:"project"`
			Full             bool     `json:"full"`
			StructuralChunks string   `json:"structural_chunks"`
			Paths            []string `json:"paths"`
		}
		if err := json.Unmarshal(params, &got); err != nil {
			return nil, err
		}
		if got.Project != "/project" || !got.Full || got.StructuralChunks != "required" || len(got.Paths) != 1 || got.Paths[0] != "internal/app" {
			return nil, &testError{"unexpected params"}
		}
		return daemonReindexSyncResult{
			FilesProcessed: 3,
			FilesSkipped:   2,
			ChunksCreated:  9,
			Duration:       42 * time.Millisecond,
			Errors:         []string{"warning"},
		}, nil
	})
	defer shutdown()

	c := &daemonClient{socketPath: socketPath, projectRoot: "/project"}
	result, err := c.reindexSync(context.Background(), true, "required", []string{"internal/app"})
	if err != nil {
		t.Fatalf("reindexSync: %v", err)
	}
	if result.FilesProcessed != 3 || result.FilesSkipped != 2 || result.ChunksCreated != 9 || result.Duration != 42*time.Millisecond {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Errors) != 1 || result.Errors[0].Error() != "warning" {
		t.Fatalf("result errors = %v", result.Errors)
	}
}

func TestDaemonClientStats(t *testing.T) {
	socketPath, shutdown := startTestDaemon(t, func(method string, params json.RawMessage) (any, error) {
		if method != "daemon.stats" {
			return nil, &testError{"unexpected method: " + method}
		}
		return map[string]int64{
			"total_files":  42,
			"total_chunks": 100,
		}, nil
	})
	defer shutdown()

	c := &daemonClient{socketPath: socketPath}
	raw, err := c.stats(context.Background())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	var stats map[string]int64
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stats["total_files"] != 42 {
		t.Fatalf("expected total_files=42, got %d", stats["total_files"])
	}
	if stats["total_chunks"] != 100 {
		t.Fatalf("expected total_chunks=100, got %d", stats["total_chunks"])
	}
}

func TestDaemonClientCallTimeout(t *testing.T) {
	// Start a daemon that never responds.
	dir, err := os.MkdirTemp("/tmp", "vecgrep-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socketPath := filepath.Join(dir, "daemon.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		// Block — never respond.
		<-time.After(10 * time.Second)
		_ = conn.Close()
	}()

	c := &daemonClient{socketPath: socketPath}

	// Use a short context timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = c.call(ctx, "daemon.search", daemonSearchParams{Query: "test"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestFormatDaemonSearchResult(t *testing.T) {
	// Simulate a daemon response with no results.
	resultJSON, _ := json.Marshal(map[string]any{
		"results": []map[string]any{},
		"mode":    "hybrid",
	})

	text := formatDaemonSearchResult(resultJSON, "")
	if text == "" {
		t.Fatal("formatDaemonSearchResult should return non-empty text")
	}
	// Empty results should say "No results found."
	if !contains(text, "No results found") {
		t.Fatalf("expected 'No results found' in output, got: %s", text)
	}
}

func TestFormatDaemonSearchResultWithScopeNote(t *testing.T) {
	resultJSON, _ := json.Marshal(map[string]any{
		"results": []map[string]any{},
		"mode":    "hybrid",
	})

	text := formatDaemonSearchResult(resultJSON, "Scoped: 5 files in blast radius")
	if !contains(text, "Scoped:") {
		t.Fatal("scope note should appear in output")
	}
}

func TestFormatStatsResult(t *testing.T) {
	statsJSON, _ := json.Marshal(map[string]any{
		"total_files":  10,
		"total_chunks": 50,
		"languages": map[string]int64{
			"go": 7,
		},
		"backend": "veclite",
		"freshness": map[string]any{
			"state":               "unknown",
			"reason":              "structural_manifest_mismatch",
			"raw_source_complete": true,
			"receipt_verified":    true,
			"manifest_required":   true,
			"manifest_verified":   false,
		},
		"pending_changes": map[string]int{
			"new_files":      0,
			"modified_files": 0,
			"deleted_files":  0,
			"total_pending":  0,
		},
	})

	text := formatStatsResult(statsJSON, "/test/project")
	if text == "" {
		t.Fatal("formatStatsResult should return non-empty text")
	}
	if !contains(text, "Total files: 10") {
		t.Fatalf("expected 'Total files: 10' in output, got: %s", text)
	}
	if !contains(text, "Total chunks: 50") {
		t.Fatalf("expected 'Total chunks: 50' in output, got: %s", text)
	}
	if !contains(text, "go: 7") || contains(text, "stats parse error") {
		t.Fatalf("heterogeneous stats should parse, got: %s", text)
	}
	if !contains(text, "Freshness: unknown (structural_manifest_mismatch)") || !contains(text, "force:true") {
		t.Fatalf("daemon MCP status omitted conservative freshness: %s", text)
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
