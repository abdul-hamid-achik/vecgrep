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
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

// projectWorker owns one project's writable session, indexer, file watcher and
// throttled embedding provider. The hub daemon holds one worker per open
// project and routes requests to it. A worker holds the project's VecLite write
// lock for its whole lifetime — the daemon is the sole writer for that project.
type projectWorker struct {
	cfg       *config.Config // the project's own resolved config
	session   *app.Session
	indexer   *index.Indexer
	watcher   *index.Watcher
	throttled *embed.ThrottledProvider

	// statePath is <project data dir>/daemon.json. It carries per-project
	// status and doubles as the MCP read-only-session reload signal (its mtime
	// bumps on every reindex).
	statePath string

	stateMu sync.Mutex
	state   DaemonState

	// reindexWg tracks in-flight reindexes so close() can drain them before
	// closing the DB and throttled provider.
	reindexWg sync.WaitGroup

	// reindexMu serializes explicit reindexes (async + sync) against the
	// watcher's auto-reindex on the same indexer, so concurrent runs don't
	// race the indexer / DB state. The watcher holds it via WatcherConfig.Locker.
	reindexMu sync.Mutex
}

// newProjectWorker opens a writable session for root and starts its file
// watcher. ctx governs the watcher lifetime. The returned worker holds the
// project's write lock until close() is called.
func newProjectWorker(ctx context.Context, root string) (*projectWorker, error) {
	session, err := app.OpenSession(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("open session for %s: %w", root, err)
	}
	cfg := session.Config

	throttleCfg := embed.ThrottleConfig{
		Workers:     cfg.Daemon.EmbedWorkers,
		RPS:         cfg.Daemon.EmbedRPS,
		MaxInFlight: cfg.Daemon.EmbedMaxInFlight,
		CacheSize:   1000,
	}
	if throttleCfg.Workers == 0 {
		throttleCfg.Workers = config.DefaultDaemonEmbedWorkers
	}
	if throttleCfg.MaxInFlight == 0 {
		throttleCfg.MaxInFlight = config.DefaultDaemonEmbedMaxInFlight
	}
	throttled := embed.NewThrottledProvider(session.Provider, throttleCfg)

	indexerCfg := index.DefaultIndexerConfig()
	if cfg.Indexing.ChunkSize > 0 {
		indexerCfg.ChunkSize = cfg.Indexing.ChunkSize
	}
	if cfg.Indexing.ChunkOverlap > 0 {
		indexerCfg.ChunkOverlap = cfg.Indexing.ChunkOverlap
	}
	if cfg.Indexing.MaxFileSize > 0 {
		indexerCfg.MaxFileSize = cfg.Indexing.MaxFileSize
	}
	if len(cfg.Indexing.IgnorePatterns) > 0 {
		indexerCfg.IgnorePatterns = cfg.Indexing.IgnorePatterns
	}
	indexer := index.NewIndexer(session.DB, throttled, indexerCfg)

	w := &projectWorker{
		cfg:       cfg,
		session:   session,
		indexer:   indexer,
		throttled: throttled,
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
		watcherCfg.Locker = &w.reindexMu // serialize watcher vs explicit reindex
		watcher, werr := index.WatchAndIndex(ctx, indexer, session.ProjectRoot, watcherCfg)
		if werr != nil {
			throttled.Close()
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

// reindex runs a full reindex of the project. It is tracked by reindexWg so
// close() can wait for it to finish before tearing the worker down.
func (w *projectWorker) reindex(ctx context.Context) {
	w.reindexMu.Lock()
	_, err := w.indexer.Index(ctx, w.session.ProjectRoot)
	w.reindexMu.Unlock()
	if err != nil {
		log.Printf("daemon: reindex %s failed: %v", w.root(), err)
		return
	}
	w.stateMu.Lock()
	w.state.LastReindex = time.Now()
	w.stateMu.Unlock()
	_ = w.writeState()
}

// reindexSync runs an incremental or full reindex synchronously and returns
// the result, so a CLI `vecgrep index` that finds the daemon running can
// delegate and render the same summary as a local index (instead of opening a
// second write handle that would collide with the daemon's exclusive lock).
// Holds reindexMu to serialize against the watcher's auto-reindex. The caller
// must track the run via reindexWg so close() drains it.
func (w *projectWorker) reindexSync(ctx context.Context, full bool) (*index.IndexResult, error) {
	w.reindexMu.Lock()
	defer w.reindexMu.Unlock()
	var result *index.IndexResult
	var err error
	if full {
		result, err = w.indexer.ReindexAll(ctx, w.session.ProjectRoot)
	} else {
		result, err = w.indexer.Index(ctx, w.session.ProjectRoot)
	}
	if err != nil {
		return nil, err
	}
	w.stateMu.Lock()
	w.state.LastReindex = time.Now()
	w.stateMu.Unlock()
	_ = w.writeState()
	return result, nil
}

// search runs a search against the worker's warm session and returns the
// results and the resolved mode string.
func (w *projectWorker) search(ctx context.Context, params searchParams) (any, string, error) {
	mode := app.ParseSearchMode(params.Mode, w.cfg.Search.DefaultMode)
	searcher := search.NewSearcher(w.session.DB, w.throttled)
	results, err := searcher.Search(ctx, params.Query, search.SearchOptions{
		Limit:       params.Limit,
		Language:    params.Language,
		Languages:   params.Languages,
		ChunkType:   params.ChunkType,
		ChunkTypes:  params.ChunkTypes,
		FilePattern: params.FilePattern,
		Directory:   params.Directory,
		MinLine:     params.MinLine,
		MaxLine:     params.MaxLine,
		FilePaths:   params.FilePaths,
		ProjectRoot: w.session.ProjectRoot,
		Mode:        mode,
	})
	if err != nil {
		return nil, "", err
	}
	return results, string(mode), nil
}

// stats returns index statistics for the worker's project.
func (w *projectWorker) stats(ctx context.Context) (any, error) {
	searcher := search.NewSearcher(w.session.DB, w.throttled)
	return searcher.GetIndexStats(ctx)
}

// close stops the watcher, drains in-flight reindexes, closes the throttled
// provider and session (releasing the project's write lock), and removes the
// per-project state file.
func (w *projectWorker) close() {
	if w.watcher != nil {
		_ = w.watcher.Stop()
	}
	w.reindexWg.Wait()
	w.throttled.Close()
	if w.session != nil {
		_ = w.session.Close()
	}
	_ = os.Remove(w.statePath)
}
