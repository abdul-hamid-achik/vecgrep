package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

// hubDataDir returns the global data dir (~/.vecgrep) where the daemon hub's
// socket lives. On error it returns "" — daemonClient.available() then fails
// fast and callers fall back to a read-only session.
func hubDataDir() string {
	dir, err := config.GetGlobalConfigDir()
	if err != nil {
		return ""
	}
	return dir
}

// daemonClient talks to the vecgrep daemon hub over its global unix socket.
// It is used to route read operations (search, status, similar, batch_search)
// through the daemon's warm writable session, avoiding any file-lock contention.
// The hub serves many projects, so every request carries the project root; the
// hub opens that project lazily on first request. When the daemon is not
// running, the MCP server falls back to a read-only database session.
type daemonClient struct {
	socketPath  string
	projectRoot string
}

// newDaemonClient creates a daemon client for the hub at globalDataDir
// (~/.vecgrep), scoped to projectRoot. The socket path is
// globalDataDir/daemon.sock.
func newDaemonClient(globalDataDir, projectRoot string) *daemonClient {
	return &daemonClient{
		socketPath:  filepath.Join(globalDataDir, "daemon.sock"),
		projectRoot: projectRoot,
	}
}

// available returns true if the daemon socket is alive (dial succeeds within
// 200ms). Cheap and non-blocking: a missing daemon fails the dial well under
// 200ms, so callers can invoke this unconditionally before falling back to
// a read-only session.
func (c *daemonClient) available() bool {
	conn, err := net.DialTimeout("unix", c.socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// jsonRPCRequest is the wire format for the daemon's newline-delimited JSON-RPC.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse is the wire format for the daemon's response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call sends a single JSON-RPC request and returns the result.
func (c *daemonClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon socket: %w", err)
	}
	defer conn.Close()

	// Set a deadline from the context.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}

	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  method,
		Params:  paramsJSON,
	}
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	var resp jsonRPCResponse
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("daemon error: %s", resp.Error.Message)
	}

	return resp.Result, nil
}

// searchParams holds the parameters for a daemon.search request.
type daemonSearchParams struct {
	Project     string   `json:"project,omitempty"`
	Query       string   `json:"query"`
	Limit       int      `json:"limit"`
	Mode        string   `json:"mode"`
	Language    string   `json:"language,omitempty"`
	Languages   []string `json:"languages,omitempty"`
	ChunkTypes  []string `json:"chunk_types,omitempty"`
	ChunkType   string   `json:"chunk_type,omitempty"`
	FilePattern string   `json:"file_pattern,omitempty"`
	Directory   string   `json:"directory,omitempty"`
	MinLine     int      `json:"min_line,omitempty"`
	MaxLine     int      `json:"max_line,omitempty"`
	Explain     bool     `json:"explain,omitempty"`
	FilePaths   []string `json:"file_paths,omitempty"`
	Symbol      string   `json:"symbol,omitempty"`
}

// search sends a daemon.search request and returns the raw JSON result.
// The result contains {"results": [...], "mode": "..."}. The client's project
// root is injected so the hub routes to (and lazily opens) the right project.
func (c *daemonClient) search(ctx context.Context, params daemonSearchParams) (json.RawMessage, error) {
	params.Project = c.projectRoot
	return c.call(ctx, "daemon.search", params)
}

// reindex sends a daemon.reindex request (async — returns immediately).
func (c *daemonClient) reindex(ctx context.Context) error {
	_, err := c.call(ctx, "daemon.reindex", map[string]any{"project": c.projectRoot})
	return err
}

// stats sends a daemon.stats request and returns index statistics.
func (c *daemonClient) stats(ctx context.Context) (json.RawMessage, error) {
	return c.call(ctx, "daemon.stats", map[string]any{"project": c.projectRoot})
}
