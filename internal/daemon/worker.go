package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

// projectWorker owns one project's writable session, indexer, file watcher and
// throttled embedding provider. The hub daemon holds one worker per open
// project and routes requests to it. A worker holds the project's VecLite write
// lock for its whole lifetime — the daemon is the sole writer for that project.
type projectWorker struct {
	cfg     *config.Config // the project's own resolved config
	session *app.Session
	service *app.Service
	watcher *index.Watcher

	// statePath is <project data dir>/daemon.json. It carries per-project
	// status and doubles as the MCP read-only-session reload signal (its mtime
	// bumps on every reindex).
	statePath string

	stateMu sync.Mutex
	state   DaemonState

	operationsMu sync.Mutex
	closing      bool
	operations   sync.WaitGroup
}

var errWorkerClosing = fmt.Errorf("project worker is closing")

// newProjectWorker opens a writable session for root and starts its file
// watcher. ctx governs the watcher lifetime. The returned worker holds the
// project's write lock until close() is called.
func newProjectWorker(ctx context.Context, root string) (*projectWorker, error) {
	session, err := app.OpenDaemonSession(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("open session for %s: %w", root, err)
	}
	cfg := session.Config

	w := &projectWorker{
		cfg:       cfg,
		session:   session,
		service:   app.NewService(session),
		statePath: filepath.Join(cfg.DataDir, "daemon.json"),
		state: DaemonState{
			ProjectRoot:  session.ProjectRoot,
			ProjectName:  session.ProjectName,
			PID:          os.Getpid(),
			StartedAt:    time.Now(),
			LastActivity: time.Now(),
		},
	}
	if session.Resolved != nil {
		w.state.ActiveBranch = session.Resolved.Branch
	}

	// Start the watcher (auto-reindex on file changes).
	if cfg.Daemon.Debounce > 0 {
		watcherCfg := index.DefaultWatcherConfig()
		watcherCfg.Debounce = time.Duration(cfg.Daemon.Debounce) * time.Millisecond
		indexCfg := app.BuildIndexerConfig(cfg, nil)
		watcherCfg.IgnorePatterns = append([]string(nil), indexCfg.IgnorePatterns...)
		watcherCfg.MaxFileSize = indexCfg.MaxFileSize
		watcher, werr := index.NewWatcher(session.ProjectRoot, watcherCfg)
		if werr != nil {
			_ = session.Close()
			return nil, fmt.Errorf("start watcher for %s: %w", root, werr)
		}
		watcher.SetCallback(func(events []index.WatchEvent) {
			if !w.beginOperation() {
				return
			}
			defer w.endOperation()
			if _, err := w.service.ApplyWatchEvents(ctx, events); err != nil {
				log.Printf("daemon: auto-reindex %s failed: %v", w.root(), err)
				return
			}
			w.markReindexed()
		})
		if werr := watcher.Start(ctx); werr != nil {
			_ = session.Close()
			return nil, fmt.Errorf("start watcher for %s: %w", root, werr)
		}
		w.watcher = watcher
	}

	_ = w.writeState()
	return w, nil
}

// root returns the worker's project root.
func (w *projectWorker) root() string { return w.session.ProjectRoot }

func (w *projectWorker) touchActivity() {
	w.stateMu.Lock()
	w.state.LastActivity = time.Now()
	w.stateMu.Unlock()
}

func (w *projectWorker) statusState() DaemonState {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	return w.state
}

func (w *projectWorker) writeState() error {
	w.stateMu.Lock()
	state := w.state
	w.stateMu.Unlock()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(w.statePath, data, 0o644)
}

func (w *projectWorker) beginOperation() bool {
	w.operationsMu.Lock()
	defer w.operationsMu.Unlock()
	if w.closing {
		return false
	}
	w.operations.Add(1)
	return true
}

func (w *projectWorker) endOperation() { w.operations.Done() }

func (w *projectWorker) markReindexed() {
	w.stateMu.Lock()
	w.state.LastReindex = time.Now()
	w.stateMu.Unlock()
	_ = w.writeState()
}

// reindex runs an incremental project reindex. The operation lease lets
// close() wait for it before tearing the worker down.
func (w *projectWorker) reindex(ctx context.Context) {
	if !w.beginOperation() {
		return
	}
	defer w.endOperation()
	_, err := w.service.Index(ctx, app.IndexRequest{}, nil)
	if err != nil {
		log.Printf("daemon: reindex %s failed: %v", w.root(), err)
		return
	}
	w.markReindexed()
}

// reindexSync runs an incremental or full reindex synchronously and returns
// the result, so a CLI `vecgrep index` that finds the daemon running can
// delegate and render the same summary as a local index (instead of opening a
// second write handle that would collide with the daemon's exclusive lock).
// app.IndexCoordinator serializes it against watcher batches; the worker's
// operation lease ensures close() drains it.
func (w *projectWorker) reindexSync(ctx context.Context, req app.IndexRequest) (*index.IndexResult, error) {
	if !w.beginOperation() {
		return nil, errWorkerClosing
	}
	defer w.endOperation()
	result, err := w.service.Index(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	w.markReindexed()
	return result, nil
}

// search runs a search against the worker's warm session and returns the
// results, the resolved mode string, and any degraded-mode warnings.
func (w *projectWorker) search(ctx context.Context, params searchParams) (any, string, []string, error) {
	if !w.beginOperation() {
		return nil, "", nil, errWorkerClosing
	}
	defer w.endOperation()
	mode := app.ParseSearchMode(params.Mode, w.cfg.Search.DefaultMode)
	searcher := search.NewSearcher(w.session.DB, w.session.Provider)
	outcome, err := searcher.SearchWithOutcome(ctx, params.Query, search.SearchOptions{
		Limit:        params.Limit,
		Language:     params.Language,
		Languages:    params.Languages,
		ChunkType:    params.ChunkType,
		ChunkTypes:   params.ChunkTypes,
		FilePattern:  params.FilePattern,
		Directory:    params.Directory,
		MinLine:      params.MinLine,
		MaxLine:      params.MaxLine,
		MinScore:     params.MinScore,
		FilePaths:    params.FilePaths,
		ProjectRoot:  w.session.ProjectRoot,
		Mode:         mode,
		VectorWeight: w.cfg.Search.VectorWeight,
		TextWeight:   w.cfg.Search.TextWeight,
	})
	if err != nil {
		return nil, "", nil, err
	}
	return outcome.Results, string(outcome.Mode), outcome.Warnings, nil
}

// stats returns index statistics for the worker's project.
func (w *projectWorker) stats(ctx context.Context) (any, error) {
	if !w.beginOperation() {
		return nil, errWorkerClosing
	}
	defer w.endOperation()
	searcher := search.NewSearcher(w.session.DB, w.session.Provider)
	stats, err := searcher.GetIndexStats(ctx)
	if err != nil {
		return nil, err
	}
	freshness, pending, freshnessErr := w.service.IndexFreshness(ctx)
	if freshnessErr == nil {
		stats["freshness"] = freshness
		stats["index_fresh"] = freshness.IsFresh()
		if pending != nil {
			stats["pending_changes"] = pending
		}
	}
	return stats, nil
}

// close stops the watcher, drains in-flight reindexes, closes the throttled
// provider and session (releasing the project's write lock), and removes the
// per-project state file.
func (w *projectWorker) close() {
	w.operationsMu.Lock()
	w.closing = true
	w.operationsMu.Unlock()
	if w.watcher != nil {
		_ = w.watcher.Stop()
	}
	w.operations.Wait()
	if w.session != nil {
		_ = w.session.Close()
	}
	_ = os.Remove(w.statePath)
}
