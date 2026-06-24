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
		cfg:        appCfg,
		session:    session,
		indexer:    indexer,
		throttled:  throttled,
		socketPath: socketPath,
		statePath:  statePath,
		lockPath:   lockPath,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
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

	// Wait for stop
	<-d.stopCh
	_ = d.listener.Close()
	if d.watcher != nil {
		_ = d.watcher.Stop()
	}
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
		go d.reindex(ctx)
		return jsonRPCResponse{ID: req.ID, Result: map[string]any{"started": true}}
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
