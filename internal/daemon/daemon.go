// Package daemon provides a background indexing daemon that watches files,
// throttles Ollama embedding requests, and serves MCP queries over a unix
// socket. The daemon is the sole writer — all other surfaces (CLI, MCP
// server) connect to it over the socket or fall back to read-only sessions.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	"github.com/abdul-hamid-achik/vecgrep/internal/snapshot"
)

// DaemonState is the metadata written to daemon.json for status inspection.
type DaemonState struct {
	ProjectRoot  string    `json:"project_root"`
	ProjectName  string    `json:"project_name"`
	PID          int       `json:"pid"`
	StartedAt    time.Time `json:"started_at"`
	LastActivity time.Time `json:"last_activity"`
	ActiveBranch string    `json:"active_branch,omitempty"`
	LastReindex  time.Time `json:"last_reindex,omitempty"`
	QueueDepth   int       `json:"queue_depth"`
}

// Daemon is the background indexing process. It owns one writable session
// and serves requests over a unix socket using newline-delimited JSON-RPC.
type Daemon struct {
	cfg       *config.Config
	session   *app.Session
	indexer   *index.Indexer
	watcher   *index.Watcher
	throttled *embed.ThrottledProvider

	state   DaemonState
	stateMu sync.Mutex

	socketPath string
	statePath  string
	lockPath   string

	listener net.Listener
	stopCh   chan struct{}
	doneCh   chan struct{}

	// reindexWg tracks in-flight reindex and switchBranch operations so
	// that Stop() can wait for them to complete before closing the DB and
	// throttled provider.
	reindexWg sync.WaitGroup

	// sweepDoneCh signals the periodic sweep goroutine to exit.
	sweepDoneCh chan struct{}
}

// Config holds the daemon startup configuration.
type Config struct {
	// Session is the writable session opened by the daemon. The daemon
	// is the sole writer; all other surfaces must use read-only sessions
	// or connect to the daemon over its socket.
	Session *app.Session
	// ResolvedConfig is the full resolved config.
	ResolvedConfig *config.Config
}

// New creates a new daemon from a writable session.
func New(cfg Config) (*Daemon, error) {
	session := cfg.Session
	if session == nil {
		return nil, fmt.Errorf("daemon requires a writable session")
	}

	appCfg := cfg.ResolvedConfig
	if appCfg == nil {
		appCfg = session.Config
	}

	// Build the throttled provider
	throttleCfg := embed.ThrottleConfig{
		Workers:     appCfg.Daemon.EmbedWorkers,
		RPS:         appCfg.Daemon.EmbedRPS,
		MaxInFlight: appCfg.Daemon.EmbedMaxInFlight,
		CacheSize:   1000,
	}
	if throttleCfg.Workers == 0 {
		throttleCfg.Workers = config.DefaultDaemonEmbedWorkers
	}
	if throttleCfg.MaxInFlight == 0 {
		throttleCfg.MaxInFlight = config.DefaultDaemonEmbedMaxInFlight
	}

	throttled := embed.NewThrottledProvider(session.Provider, throttleCfg)

	// Build the indexer with the throttled provider
	indexerCfg := index.DefaultIndexerConfig()
	if appCfg.Indexing.ChunkSize > 0 {
		indexerCfg.ChunkSize = appCfg.Indexing.ChunkSize
	}
	if appCfg.Indexing.ChunkOverlap > 0 {
		indexerCfg.ChunkOverlap = appCfg.Indexing.ChunkOverlap
	}
	if appCfg.Indexing.MaxFileSize > 0 {
		indexerCfg.MaxFileSize = appCfg.Indexing.MaxFileSize
	}
	if len(appCfg.Indexing.IgnorePatterns) > 0 {
		indexerCfg.IgnorePatterns = appCfg.Indexing.IgnorePatterns
	}

	indexer := index.NewIndexer(session.DB, throttled, indexerCfg)

	// Determine socket and state paths
	dataDir := session.Config.DataDir
	socketPath := filepath.Join(dataDir, "daemon.sock")
	statePath := filepath.Join(dataDir, "daemon.json")
	lockPath := filepath.Join(dataDir, "daemon.lock")

	d := &Daemon{
		cfg:         appCfg,
		session:     session,
		indexer:     indexer,
		throttled:   throttled,
		socketPath:  socketPath,
		statePath:   statePath,
		lockPath:    lockPath,
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
		sweepDoneCh: make(chan struct{}),
		state: DaemonState{
			ProjectRoot:  session.ProjectRoot,
			ProjectName:  session.ProjectName,
			PID:          os.Getpid(),
			StartedAt:    time.Now(),
			LastActivity: time.Now(),
		},
	}

	if session.Resolved != nil {
		d.state.ActiveBranch = session.Resolved.Branch
	}

	return d, nil
}

// SocketPath returns the path to the daemon's unix socket.
func (d *Daemon) SocketPath() string {
	return d.socketPath
}

// Start begins the daemon: starts the watcher, writes the state file, and
// begins listening on the unix socket. It blocks until Stop is called or
// the context is canceled.
func (d *Daemon) Start(ctx context.Context) error {
	// Acquire the lock
	if err := d.acquireLock(); err != nil {
		return fmt.Errorf("acquire daemon lock: %w", err)
	}

	// Write the initial state file
	if err := d.writeState(); err != nil {
		_ = d.releaseLock()
		return fmt.Errorf("write daemon state: %w", err)
	}

	// Start the watcher
	if d.cfg.Daemon.Debounce > 0 {
		watcherCfg := index.DefaultWatcherConfig()
		watcherCfg.Debounce = time.Duration(d.cfg.Daemon.Debounce) * time.Millisecond
		w, err := index.WatchAndIndex(ctx, d.indexer, d.session.ProjectRoot, watcherCfg)
		if err != nil {
			_ = d.releaseLock()
			return fmt.Errorf("start watcher: %w", err)
		}
		d.watcher = w
	}

	// Listen on the unix socket
	var err error
	d.listener, err = net.Listen("unix", d.socketPath)
	if err != nil {
		_ = d.cleanup()
		return fmt.Errorf("listen on socket: %w", err)
	}

	log.Printf("daemon listening on %s", d.socketPath)

	// Accept loop
	go d.acceptLoop(ctx)

	// Idle timeout goroutine
	if d.cfg.Daemon.IdleTimeout > 0 {
		go d.idleWatcher(ctx)
	}

	// Periodic fcheap sweep goroutine
	if sweepInterval := parseSweepInterval(d.cfg.Daemon.SweepInterval); sweepInterval > 0 {
		go d.sweepLoop(ctx, sweepInterval)
	}

	// Wait for stop
	<-d.stopCh
	_ = d.listener.Close()
	if d.watcher != nil {
		_ = d.watcher.Stop()
	}

	// Signal the sweep goroutine to exit.
	close(d.sweepDoneCh)

	// Wait for in-flight reindex and switchBranch operations to complete
	// before closing the throttled provider and DB.
	d.reindexWg.Wait()

	d.throttled.Close()
	_ = d.cleanup()
	close(d.doneCh)

	return nil
}

// Stop signals the daemon to shut down.
func (d *Daemon) Stop() {
	select {
	case <-d.stopCh:
		// already closed
	default:
		close(d.stopCh)
	}
	<-d.doneCh
}

// acceptLoop accepts connections on the unix socket.
func (d *Daemon) acceptLoop(ctx context.Context) {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.stopCh:
				return
			default:
				log.Printf("daemon accept error: %v", err)
				return
			}
		}
		go d.handleConn(ctx, conn)
	}
}

// handleConn handles a single client connection using newline-delimited JSON.
func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	for {
		var req jsonRPCRequest
		if err := dec.Decode(&req); err != nil {
			return // connection closed or malformed
		}

		d.touchActivity()

		resp := d.handleRequest(ctx, &req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// handleRequest dispatches a single JSON-RPC request.
func (d *Daemon) handleRequest(ctx context.Context, req *jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "daemon.ping":
		return jsonRPCResponse{ID: req.ID, Result: map[string]any{"ok": true}}
	case "daemon.status":
		d.stateMu.Lock()
		state := d.state
		d.stateMu.Unlock()
		return jsonRPCResponse{ID: req.ID, Result: state}
	case "daemon.reindex":
		d.reindexWg.Add(1)
		go func() {
			defer d.reindexWg.Done()
			d.reindex(ctx)
		}()
		return jsonRPCResponse{ID: req.ID, Result: map[string]any{"started": true}}
	case "daemon.switchBranch":
		return d.handleSwitchBranch(ctx, req)
	case "daemon.search":
		return d.handleSearch(ctx, req)
	default:
		return jsonRPCResponse{
			ID:    req.ID,
			Error: &jsonRPCError{Code: -32601, Message: fmt.Sprintf("unknown method: %s", req.Method)},
		}
	}
}

// reindex triggers a full reindex of the project.
func (d *Daemon) reindex(ctx context.Context) {
	_, err := d.indexer.Index(ctx, d.session.ProjectRoot)
	if err != nil {
		log.Printf("daemon reindex failed: %v", err)
		return
	}

	d.stateMu.Lock()
	d.state.LastReindex = time.Now()
	d.stateMu.Unlock()
	_ = d.writeState()
}

// idleWatcher shuts down the daemon after the configured idle timeout.
func (d *Daemon) idleWatcher(ctx context.Context) {
	timeout := time.Duration(d.cfg.Daemon.IdleTimeout) * time.Minute
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.stateMu.Lock()
			lastActivity := d.state.LastActivity
			d.stateMu.Unlock()

			if time.Since(lastActivity) > timeout {
				log.Printf("daemon idle for %s, shutting down", time.Since(lastActivity))
				d.Stop()
				return
			}
		}
	}
}

// touchActivity updates the last activity timestamp.
func (d *Daemon) touchActivity() {
	d.stateMu.Lock()
	d.state.LastActivity = time.Now()
	d.stateMu.Unlock()
}

// acquireLock writes the lock file, failing if it already exists.
func (d *Daemon) acquireLock() error {
	if err := os.MkdirAll(filepath.Dir(d.lockPath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(d.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("daemon already running (lock file exists: %s)", d.lockPath)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	return nil
}

// releaseLock removes the lock file.
func (d *Daemon) releaseLock() error {
	return os.Remove(d.lockPath)
}

// cleanup removes the socket, state, and lock files.
func (d *Daemon) cleanup() error {
	_ = os.Remove(d.socketPath)
	_ = os.Remove(d.statePath)
	return d.releaseLock()
}

// writeState writes the daemon state to the state file.
func (d *Daemon) writeState() error {
	d.stateMu.Lock()
	state := d.state
	d.stateMu.Unlock()

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.statePath, data, 0644)
}

// ReadState reads the daemon state from the state file (without a running daemon).
func ReadState(dataDir string) (*DaemonState, error) {
	path := filepath.Join(dataDir, "daemon.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state DaemonState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse daemon state: %w", err)
	}
	return &state, nil
}

// IsRunning checks if a daemon is running for the given data directory.
func IsRunning(dataDir string) bool {
	lockPath := filepath.Join(dataDir, "daemon.lock")
	if _, err := os.Stat(lockPath); err != nil {
		return false
	}
	// Try to ping the socket to confirm it's actually alive
	socketPath := filepath.Join(dataDir, "daemon.sock")
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return false // socket doesn't respond → stale lock
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	if err := enc.Encode(jsonRPCRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "daemon.ping"}); err != nil {
		return false
	}
	var resp jsonRPCResponse
	if err := dec.Decode(&resp); err != nil {
		return false
	}
	return resp.Result != nil
}

// searchParams holds the parameters for a daemon.search request.
type searchParams struct {
	Query    string `json:"query"`
	Limit    int    `json:"limit"`
	Mode     string `json:"mode"`
	Language string `json:"language,omitempty"`
}

// handleSearch runs a semantic/keyword/hybrid search using the daemon's warm
// session and returns the results as JSON. This lets CLI clients search
// through the socket instead of opening their own read-only session.
func (d *Daemon) handleSearch(ctx context.Context, req *jsonRPCRequest) jsonRPCResponse {
	var params searchParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return jsonRPCResponse{ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: fmt.Sprintf("invalid params: %v", err)}}
		}
	}
	if params.Query == "" {
		return jsonRPCResponse{ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "query is required"}}
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}

	mode := app.ParseSearchMode(params.Mode, d.cfg.Search.DefaultMode)

	searcher := search.NewSearcher(d.session.DB, d.throttled)
	results, err := searcher.Search(ctx, params.Query, search.SearchOptions{
		Limit:       params.Limit,
		Language:    params.Language,
		ProjectRoot: d.session.ProjectRoot,
		Mode:        mode,
	})
	if err != nil {
		return jsonRPCResponse{ID: req.ID, Error: &jsonRPCError{Code: -32603, Message: fmt.Sprintf("search failed: %v", err)}}
	}

	return jsonRPCResponse{ID: req.ID, Result: map[string]any{"results": results, "mode": string(mode)}}
}

// switchBranchParams holds the parameters for a daemon.switchBranch request.
type switchBranchParams struct {
	Branch string `json:"branch"`
}

// handleSwitchBranch snapshots the current branch's index to fcheap, then
// restores or reindexes the target branch. It runs synchronously in the
// request handler goroutine but tracks the operation via reindexWg so
// Stop() can wait for it.
func (d *Daemon) handleSwitchBranch(ctx context.Context, req *jsonRPCRequest) jsonRPCResponse {
	var params switchBranchParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return jsonRPCResponse{ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: fmt.Sprintf("invalid params: %v", err)}}
		}
	}
	if params.Branch == "" {
		return jsonRPCResponse{ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "branch is required"}}
	}

	d.reindexWg.Add(1)
	go func() {
		defer d.reindexWg.Done()
		d.doSwitchBranch(ctx, params.Branch)
	}()

	return jsonRPCResponse{ID: req.ID, Result: map[string]any{"status": "switching", "branch": params.Branch}}
}

// doSwitchBranch performs the actual branch switch: snapshots the current
// branch, then restores or reindexes the target branch. It updates the
// daemon state on success.
func (d *Daemon) doSwitchBranch(ctx context.Context, targetBranch string) {
	projectRoot := d.session.ProjectRoot
	projectName := d.session.ProjectName

	// Step a: snapshot the current branch's index to fcheap
	_, err := app.BranchSnapshot(ctx, projectRoot, projectName)
	if err != nil {
		log.Printf("daemon switchBranch: snapshot current branch failed: %v", err)
		return
	}

	// Step b: restore the target branch from fcheap if a snapshot exists,
	// or c: run a full reindex if no snapshot exists.
	result, err := app.BranchSwitch(ctx, projectRoot, projectName, targetBranch)
	if err != nil {
		log.Printf("daemon switchBranch: switch to %q failed: %v", targetBranch, err)
		return
	}

	// If no snapshot was restored, run a full reindex for the new branch.
	if !result.Restored {
		log.Printf("daemon switchBranch: no snapshot for %q, running full reindex", targetBranch)
		if _, idxErr := d.indexer.Index(ctx, projectRoot); idxErr != nil {
			log.Printf("daemon switchBranch: reindex failed: %v", idxErr)
			return
		}
	}

	// Update daemon state with the new active branch
	d.stateMu.Lock()
	d.state.ActiveBranch = targetBranch
	d.state.LastReindex = time.Now()
	d.stateMu.Unlock()
	_ = d.writeState()

	log.Printf("daemon switchBranch: switched to %q (restored=%v)", targetBranch, result.Restored)
}

// sweepLoop periodically runs fcheap vacuum to clean up orphaned stash
// entries from the fcheap vault. It is best-effort: if fcheap is not
// available, it logs and continues.
func (d *Daemon) sweepLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	f := snapshot.NewFcheap()
	if !f.Available() {
		log.Printf("periodic fcheap sweep: fcheap not available, skipping")
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.sweepDoneCh:
			return
		case <-ticker.C:
			log.Printf("periodic fcheap sweep started")
			result, err := f.Sweep(ctx)
			if err != nil {
				log.Printf("periodic fcheap sweep failed: %v", err)
				continue
			}
			log.Printf("periodic fcheap sweep complete: swept %d stashes", result.Swept)
		}
	}
}

// parseSweepInterval parses the SweepInterval config string into a
// time.Duration. Returns 0 for empty or invalid values, which disables
// the periodic sweep.
func parseSweepInterval(s string) time.Duration {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// jsonRPCRequest is a minimal JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse is a minimal JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// jsonRPCError is a JSON-RPC 2.0 error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
