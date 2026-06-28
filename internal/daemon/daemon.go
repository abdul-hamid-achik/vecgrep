// Package daemon provides a background indexing daemon that watches files,
// throttles Ollama embedding requests, and serves queries over a unix socket.
//
// The daemon is a multi-project hub: one process listens on a single global
// socket (~/.vecgrep/daemon.sock) and routes JSON-RPC requests to per-project
// workers, opening projects lazily on first request. It is the sole writer for
// every project it has open; all other surfaces (CLI, MCP server) connect over
// the socket or fall back to read-only sessions.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/snapshot"
)

// DaemonState is the per-project metadata written to each project's daemon.json
// for status inspection and as the MCP reload signal.
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

// HubState is the hub-level metadata written to ~/.vecgrep/daemon.json. It lets
// `vecgrep daemon stop`/`status` find the hub PID and the set of open projects
// without connecting to the socket.
type HubState struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Projects  []string  `json:"projects"`
}

// Daemon is the multi-project hub. It listens on one global unix socket and
// routes requests to per-project workers, opening projects lazily.
type Daemon struct {
	cfg     *config.Config // hub-level config (sweep, log offload, throttle defaults)
	dataDir string         // global data dir (~/.vecgrep)

	socketPath string
	statePath  string
	lockPath   string
	logPath    string

	listener net.Listener

	workersMu sync.Mutex
	workers   map[string]*projectWorker

	startedAt time.Time

	stopCh      chan struct{}
	doneCh      chan struct{}
	sweepDoneCh chan struct{}

	// logSink, when non-nil, is the managed log file the hub writes to and
	// periodically offloads to fcheap. logOffloadDoneCh signals that loop to exit.
	logSink          *rotatingSink
	logOffloadDoneCh chan struct{}
}

// Config holds the hub startup configuration.
type Config struct {
	// ResolvedConfig is the global resolved config (defaults + global + env)
	// used for hub-level settings: sweep interval, log offload, throttle
	// defaults. Per-project settings come from each project's own session.
	ResolvedConfig *config.Config
	// DataDir is the global data dir where the hub's socket, lock, state and
	// log live (~/.vecgrep). If empty it is resolved from GetGlobalConfigDir.
	DataDir string
}

// New creates a new hub daemon.
func New(cfg Config) (*Daemon, error) {
	appCfg := cfg.ResolvedConfig
	if appCfg == nil {
		appCfg = config.DefaultConfig()
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		gd, err := config.GetGlobalConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve global data dir: %w", err)
		}
		dataDir = gd
	}

	return &Daemon{
		cfg:              appCfg,
		dataDir:          dataDir,
		socketPath:       filepath.Join(dataDir, "daemon.sock"),
		statePath:        filepath.Join(dataDir, "daemon.json"),
		lockPath:         filepath.Join(dataDir, "daemon.lock"),
		logPath:          filepath.Join(dataDir, "daemon.log"),
		workers:          make(map[string]*projectWorker),
		stopCh:           make(chan struct{}),
		doneCh:           make(chan struct{}),
		sweepDoneCh:      make(chan struct{}),
		logOffloadDoneCh: make(chan struct{}),
	}, nil
}

// SocketPath returns the path to the hub's unix socket.
func (d *Daemon) SocketPath() string { return d.socketPath }

// Start launches the hub: raises the FD limit, acquires the global lock,
// pre-opens any given project roots, then listens and serves until Stop. It
// blocks until Stop is called or the context is canceled.
func (d *Daemon) Start(ctx context.Context, preopen ...string) error {
	// Raise the open-file limit before any watcher opens a descriptor per
	// directory (kqueue on macOS). Without this, large trees exhaust the
	// default soft limit and net.Listen fails with the misleading
	// "too many open files". Best-effort: it never aborts startup.
	raiseFDLimit()

	if err := d.acquireLock(); err != nil {
		return fmt.Errorf("acquire daemon lock: %w", err)
	}
	d.startedAt = time.Now()
	if err := d.writeState(); err != nil {
		_ = d.releaseLock()
		return fmt.Errorf("write daemon state: %w", err)
	}

	// Pre-open requested projects. Best-effort: a bad project is logged, not fatal.
	for _, root := range preopen {
		if _, err := d.getOrOpenWorker(ctx, root); err != nil {
			log.Printf("daemon: pre-open %s failed: %v", root, err)
		} else {
			log.Printf("daemon: opened project %s", root)
		}
	}

	var err error
	d.listener, err = net.Listen("unix", d.socketPath)
	if err != nil {
		d.closeAllWorkers()
		_ = d.cleanup()
		return fmt.Errorf("listen on socket: %w", err)
	}
	log.Printf("daemon hub listening on %s (%d project(s) open)", d.socketPath, len(d.listWorkers()))

	go d.acceptLoop(ctx)

	if sweepInterval := parseSweepInterval(d.cfg.Daemon.SweepInterval); sweepInterval > 0 {
		go d.sweepLoop(ctx, sweepInterval)
	}
	if d.cfg.Daemon.LogOffload {
		d.startLogOffload(ctx)
	}

	<-d.stopCh
	_ = d.listener.Close()
	d.closeAllWorkers()

	close(d.sweepDoneCh)
	close(d.logOffloadDoneCh)

	// Final best-effort offload of whatever the hub logged this session, then
	// restore plain stderr logging and close the managed log file.
	if d.logSink != nil {
		d.offloadLog(context.Background(), snapshot.NewFcheap(), time.Now())
		log.SetOutput(os.Stderr)
		_ = d.logSink.Close()
	}

	_ = d.cleanup()
	close(d.doneCh)
	return nil
}

// Stop signals the hub to shut down and waits for it to finish.
func (d *Daemon) Stop() {
	select {
	case <-d.stopCh:
		// already closed
	default:
		close(d.stopCh)
	}
	<-d.doneCh
}

// --- worker registry ---

// getOrOpenWorker returns the worker for root, opening it lazily if not already
// present. Opening happens outside the registry lock (OpenSession can be slow).
func (d *Daemon) getOrOpenWorker(ctx context.Context, root string) (*projectWorker, error) {
	canon := canonicalRoot(root)

	d.workersMu.Lock()
	if w, ok := d.workers[canon]; ok {
		d.workersMu.Unlock()
		return w, nil
	}
	d.workersMu.Unlock()

	w, err := newProjectWorker(ctx, canon)
	if err != nil {
		return nil, err
	}

	d.workersMu.Lock()
	if existing, ok := d.workers[canon]; ok {
		// A concurrent request opened it first; discard our duplicate.
		d.workersMu.Unlock()
		w.close()
		return existing, nil
	}
	d.workers[canon] = w
	d.workersMu.Unlock()

	_ = d.writeState()
	return w, nil
}

// removeWorker closes and unregisters a project. Returns false if not open.
func (d *Daemon) removeWorker(root string) bool {
	canon := canonicalRoot(root)
	d.workersMu.Lock()
	w, ok := d.workers[canon]
	if ok {
		delete(d.workers, canon)
	}
	d.workersMu.Unlock()
	if ok {
		w.close()
		_ = d.writeState()
	}
	return ok
}

func (d *Daemon) lookupWorker(root string) (*projectWorker, bool) {
	canon := canonicalRoot(root)
	d.workersMu.Lock()
	defer d.workersMu.Unlock()
	w, ok := d.workers[canon]
	return w, ok
}

func (d *Daemon) listWorkers() []*projectWorker {
	d.workersMu.Lock()
	defer d.workersMu.Unlock()
	out := make([]*projectWorker, 0, len(d.workers))
	for _, w := range d.workers {
		out = append(out, w)
	}
	return out
}

func (d *Daemon) closeAllWorkers() {
	d.workersMu.Lock()
	workers := make([]*projectWorker, 0, len(d.workers))
	for _, w := range d.workers {
		workers = append(workers, w)
	}
	d.workers = make(map[string]*projectWorker)
	d.workersMu.Unlock()
	for _, w := range workers {
		w.close()
	}
}

// canonicalRoot normalizes a project root for use as a registry key.
func canonicalRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// --- connection handling / routing ---

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

func (d *Daemon) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req jsonRPCRequest
		if err := dec.Decode(&req); err != nil {
			return // connection closed or malformed
		}
		resp := d.handleRequest(ctx, &req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (d *Daemon) handleRequest(ctx context.Context, req *jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "daemon.ping":
		return jsonRPCResponse{ID: req.ID, Result: map[string]any{"ok": true}}
	case "daemon.status":
		return d.handleStatus(req)
	case "daemon.listProjects":
		return d.handleListProjects(req)
	case "daemon.addProject":
		return d.handleAddProject(ctx, req)
	case "daemon.removeProject":
		return d.handleRemoveProject(req)
	case "daemon.reindex":
		return d.handleReindex(ctx, req)
	case "daemon.switchBranch":
		return d.handleSwitchBranch(ctx, req)
	case "daemon.search":
		return d.handleSearch(ctx, req)
	case "daemon.stats":
		return d.handleStats(ctx, req)
	default:
		return jsonRPCResponse{
			ID:    req.ID,
			Error: &jsonRPCError{Code: -32601, Message: fmt.Sprintf("unknown method: %s", req.Method)},
		}
	}
}

// projectParam is the routing field every project-scoped request carries.
type projectParam struct {
	Project string `json:"project"`
}

// workerForReq resolves (lazily opening) the worker named by the request's
// project field.
func (d *Daemon) workerForReq(ctx context.Context, req *jsonRPCRequest) (*projectWorker, *jsonRPCError) {
	var p projectParam
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	if p.Project == "" {
		return nil, &jsonRPCError{Code: -32602, Message: "project is required"}
	}
	w, err := d.getOrOpenWorker(ctx, p.Project)
	if err != nil {
		return nil, &jsonRPCError{Code: -32603, Message: fmt.Sprintf("open project %q: %v", p.Project, err)}
	}
	return w, nil
}

func (d *Daemon) handleSearch(ctx context.Context, req *jsonRPCRequest) jsonRPCResponse {
	var params searchParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResp(req, -32602, fmt.Sprintf("invalid params: %v", err))
		}
	}
	if params.Query == "" {
		return errResp(req, -32602, "query is required")
	}
	if params.Limit <= 0 {
		params.Limit = 10
	}
	w, rpcErr := d.workerForReq(ctx, req)
	if rpcErr != nil {
		return jsonRPCResponse{ID: req.ID, Error: rpcErr}
	}
	w.touchActivity()
	results, mode, err := w.search(ctx, params)
	if err != nil {
		return errResp(req, -32603, fmt.Sprintf("search failed: %v", err))
	}
	return jsonRPCResponse{ID: req.ID, Result: map[string]any{"results": results, "mode": mode}}
}

func (d *Daemon) handleStats(ctx context.Context, req *jsonRPCRequest) jsonRPCResponse {
	w, rpcErr := d.workerForReq(ctx, req)
	if rpcErr != nil {
		return jsonRPCResponse{ID: req.ID, Error: rpcErr}
	}
	w.touchActivity()
	stats, err := w.stats(ctx)
	if err != nil {
		return errResp(req, -32603, fmt.Sprintf("stats failed: %v", err))
	}
	return jsonRPCResponse{ID: req.ID, Result: stats}
}

func (d *Daemon) handleReindex(ctx context.Context, req *jsonRPCRequest) jsonRPCResponse {
	w, rpcErr := d.workerForReq(ctx, req)
	if rpcErr != nil {
		return jsonRPCResponse{ID: req.ID, Error: rpcErr}
	}
	w.reindexWg.Add(1)
	go func() {
		defer w.reindexWg.Done()
		w.reindex(ctx)
	}()
	return jsonRPCResponse{ID: req.ID, Result: map[string]any{"started": true}}
}

func (d *Daemon) handleAddProject(ctx context.Context, req *jsonRPCRequest) jsonRPCResponse {
	w, rpcErr := d.workerForReq(ctx, req)
	if rpcErr != nil {
		return jsonRPCResponse{ID: req.ID, Error: rpcErr}
	}
	return jsonRPCResponse{ID: req.ID, Result: map[string]any{"project": w.root(), "opened": true}}
}

func (d *Daemon) handleRemoveProject(req *jsonRPCRequest) jsonRPCResponse {
	var p projectParam
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	if p.Project == "" {
		return errResp(req, -32602, "project is required")
	}
	removed := d.removeWorker(p.Project)
	return jsonRPCResponse{ID: req.ID, Result: map[string]any{"project": p.Project, "removed": removed}}
}

func (d *Daemon) handleListProjects(req *jsonRPCRequest) jsonRPCResponse {
	workers := d.listWorkers()
	roots := make([]string, 0, len(workers))
	for _, w := range workers {
		roots = append(roots, w.root())
	}
	return jsonRPCResponse{ID: req.ID, Result: map[string]any{"projects": roots}}
}

func (d *Daemon) handleStatus(req *jsonRPCRequest) jsonRPCResponse {
	var p projectParam
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	if p.Project != "" {
		w, ok := d.lookupWorker(p.Project)
		if !ok {
			return errResp(req, -32603, fmt.Sprintf("project %q is not open", p.Project))
		}
		return jsonRPCResponse{ID: req.ID, Result: w.statusState()}
	}
	workers := d.listWorkers()
	states := make([]DaemonState, 0, len(workers))
	for _, w := range workers {
		states = append(states, w.statusState())
	}
	return jsonRPCResponse{ID: req.ID, Result: map[string]any{
		"pid":        os.Getpid(),
		"started_at": d.startedAt,
		"projects":   states,
	}}
}

// handleSwitchBranch rejects in-place branch switching. A worker holds an
// exclusive lock on a single branch's index directory for its whole lifetime,
// and its watcher, indexer, and serving paths are all bound to that one open
// handle. Branch switching is a CLI operation: remove the project from the
// daemon (or stop it), run `vecgrep branch switch`, then re-add/restart.
func (d *Daemon) handleSwitchBranch(_ context.Context, req *jsonRPCRequest) jsonRPCResponse {
	return jsonRPCResponse{
		ID: req.ID,
		Error: &jsonRPCError{
			Code:    -32601,
			Message: "branch switching is not supported while the daemon holds the project; run `vecgrep branch switch <branch>` (stop the daemon or remove the project first), then re-add it",
		},
	}
}

func errResp(req *jsonRPCRequest, code int, msg string) jsonRPCResponse {
	return jsonRPCResponse{ID: req.ID, Error: &jsonRPCError{Code: code, Message: msg}}
}

// --- lifecycle helpers ---

func (d *Daemon) acquireLock() error {
	if err := os.MkdirAll(filepath.Dir(d.lockPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(d.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("daemon already running (lock file exists: %s)", d.lockPath)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	return nil
}

func (d *Daemon) releaseLock() error { return os.Remove(d.lockPath) }

func (d *Daemon) cleanup() error {
	_ = os.Remove(d.socketPath)
	_ = os.Remove(d.statePath)
	return d.releaseLock()
}

func (d *Daemon) writeState() error {
	d.workersMu.Lock()
	roots := make([]string, 0, len(d.workers))
	for _, w := range d.workers {
		roots = append(roots, w.root())
	}
	d.workersMu.Unlock()

	data, err := json.MarshalIndent(HubState{
		PID:       os.Getpid(),
		StartedAt: d.startedAt,
		Projects:  roots,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.statePath, data, 0o644)
}

// ReadHubState reads the hub state file from the given global data dir.
func ReadHubState(dataDir string) (*HubState, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "daemon.json"))
	if err != nil {
		return nil, err
	}
	var st HubState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse daemon state: %w", err)
	}
	return &st, nil
}

// ReadState reads a per-project daemon.json (used by callers that inspect a
// single project's worker state).
func ReadState(dataDir string) (*DaemonState, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "daemon.json"))
	if err != nil {
		return nil, err
	}
	var state DaemonState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse daemon state: %w", err)
	}
	return &state, nil
}

// IsRunning checks if a daemon is listening on the given data directory's socket
// (the global data dir for the hub).
func IsRunning(dataDir string) bool {
	lockPath := filepath.Join(dataDir, "daemon.lock")
	if _, err := os.Stat(lockPath); err != nil {
		return false
	}
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
	Project     string   `json:"project"`
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

// --- periodic background loops (hub-level) ---

// sweepLoop periodically runs fcheap vacuum to clean up orphaned stash entries.
// Best-effort: if fcheap is not available it logs and returns.
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

// startLogOffload redirects the hub's log to a managed file (while keeping
// stderr) and launches the offload loop. Best-effort.
func (d *Daemon) startLogOffload(ctx context.Context) {
	interval := parseSweepInterval(d.cfg.Daemon.LogOffloadInterval)
	if interval <= 0 {
		log.Printf("daemon: log offload enabled but interval %q is invalid; disabling",
			d.cfg.Daemon.LogOffloadInterval)
		return
	}
	sink, err := newRotatingSink(d.logPath)
	if err != nil {
		log.Printf("daemon: log offload disabled: %v", err)
		return
	}
	d.logSink = sink
	log.SetOutput(io.MultiWriter(os.Stderr, sink))
	log.Printf("daemon: log offload enabled (every %s, ttl %q) → %s",
		interval, d.cfg.Daemon.LogOffloadTTL, d.logPath)

	go d.logOffloadLoop(ctx, interval)
}

func (d *Daemon) logOffloadLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	f := snapshot.NewFcheap()
	if !f.Available() {
		log.Printf("daemon: log offload: fcheap not available, rotating logs locally only")
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.logOffloadDoneCh:
			return
		case <-ticker.C:
			d.offloadLog(ctx, f, time.Now())
		}
	}
}

// offloadLog rotates the managed log and, when fcheap is available, stashes the
// rotated segment with the configured TTL, deleting the local copy on success.
func (d *Daemon) offloadLog(ctx context.Context, f *snapshot.Fcheap, now time.Time) {
	if d.logSink == nil {
		return
	}
	rotated, err := d.logSink.Rotate(now)
	if err != nil {
		log.Printf("daemon: log rotate failed: %v", err)
		return
	}
	if rotated == "" {
		return // nothing written since the last rotation
	}
	if f == nil || !f.Available() {
		return // keep the rotated segment locally; nothing else to do
	}
	name := fmt.Sprintf("vecgrep-hub-log-%s", filepath.Base(rotated))
	tags := []string{"daemon-log", "hub"}
	if _, err := f.SaveWithTTL(ctx, rotated, name, "vecgrep-daemon", d.cfg.Daemon.LogOffloadTTL, tags); err != nil {
		log.Printf("daemon: log offload to fcheap failed (kept %s): %v", rotated, err)
		return
	}
	if err := os.Remove(rotated); err != nil {
		log.Printf("daemon: removing offloaded log %s: %v", rotated, err)
	}
}

// parseSweepInterval parses a duration config string (e.g. "24h", "1h"). It
// returns 0 for empty or invalid values, which disables the associated loop.
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
