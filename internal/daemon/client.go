package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// reindexSyncReadTimeout caps how long the CLI waits for a daemon reindex when
// the caller's context has no deadline. A full reindex with embeddings on a
// large tree can take many minutes, so this is generous; a truly stuck daemon
// still returns control to the caller.

// reindexSyncResult is the JSON wire shape for a daemon.reindex_sync result.
// index.IndexResult.Errors is []error (not JSON-encodable), so errors are
// stringified on the daemon side and re-wrapped on the client side.
type reindexSyncResult struct {
	FilesProcessed int           `json:"files_processed"`
	FilesSkipped   int           `json:"files_skipped"`
	ChunksCreated  int           `json:"chunks_created"`
	Duration       time.Duration `json:"duration"`
	Errors         []string      `json:"errors"`
}

const reindexSyncReadTimeout = 30 * time.Minute

// reindexSyncClient wire types (kept local; the daemon's jsonRPCRequest/
// jsonRPCResponse in daemon.go are not exported).
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ReindexSync delegates an incremental (or full) reindex to a running daemon
// hub over its global unix socket and returns the index result, so a CLI
// `vecgrep index` that finds the daemon running can render the same summary as
// a local index instead of opening a second write handle (which would collide
// with the daemon's exclusive lock). globalDataDir is the hub's data dir
// (~/.vecgrep); projectRoot identifies the worker. Returns an error if no
// daemon is running, it doesn't respond in time, or the reindex failed.
func ReindexSync(ctx context.Context, globalDataDir, projectRoot string, full bool) (*index.IndexResult, error) {
	socketPath := filepath.Join(globalDataDir, "daemon.sock")
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect to daemon socket: %w", err)
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(reindexSyncReadTimeout))
	}

	params, err := json.Marshal(map[string]any{"project": projectRoot, "full": full})
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	req := rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "daemon.reindex_sync", Params: params}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send reindex: %w", err)
	}

	var resp rpcResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("daemon: %s", resp.Error.Message)
	}

	var wire reindexSyncResult
	if err := json.Unmarshal(resp.Result, &wire); err != nil {
		return nil, fmt.Errorf("decode index result: %w", err)
	}
	res := &index.IndexResult{
		FilesProcessed: wire.FilesProcessed,
		FilesSkipped:   wire.FilesSkipped,
		ChunksCreated:  wire.ChunksCreated,
		Duration:       wire.Duration,
	}
	for _, msg := range wire.Errors {
		res.Errors = append(res.Errors, errors.New(msg))
	}
	return res, nil
}
