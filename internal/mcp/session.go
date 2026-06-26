package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	vlsession "github.com/abdul-hamid-achik/veclite/session"
)

// mcpSession manages lazy dual-handle database access for the MCP server.
// Read tools use a cached read-only handle (shared flock, multiple readers OK).
// Write tools close the cached RO handle and open a fresh RW handle (exclusive
// flock) per call, returning it for the caller to close — so the exclusive lock
// is held only for the duration of the write operation.
//
// This resolves the multi-process lock contention: an idle MCP server never
// holds a lock, a searching MCP server holds only a shared lock (doesn't block
// other readers), and a writing MCP server holds an exclusive lock only briefly.
type mcpSession struct {
	cfg         *config.Config
	projectRoot string
	provider    embed.Provider

	dbOpts db.OpenOptions

	mu sync.Mutex
	ro *db.DB // cached read-only handle (shared lock), nil when not open

	reloadThreshold time.Duration
	daemonJSONPath  string // for mtime-based reload signal
	lastReload      time.Time
}

// newMCPSession creates a new MCP session. No database is opened until
// readOnlyDB() or readWriteDB() is called.
func newMCPSession(cfg *config.Config, projectRoot string, provider embed.Provider) *mcpSession {
	dbOpts := db.OpenOptions{
		Dimensions:         cfg.Embedding.Dimensions,
		DataDir:            cfg.DataDir,
		HNSWM:              cfg.Vector.VecLite.M,
		HNSWEfConstruction: cfg.Vector.VecLite.EfConstruction,
		HNSWEfSearch:       cfg.Vector.VecLite.EfSearch,
	}

	reloadThreshold := 5 * time.Second
	if cfg.Server.MCPReloadInterval != "" {
		if d, err := time.ParseDuration(cfg.Server.MCPReloadInterval); err == nil {
			reloadThreshold = d
		}
	}

	return &mcpSession{
		cfg:             cfg,
		projectRoot:     projectRoot,
		provider:        provider,
		dbOpts:          dbOpts,
		reloadThreshold: reloadThreshold,
		daemonJSONPath:  filepath.Join(cfg.DataDir, "daemon.json"),
	}
}

// readOnlyDB returns a *db.DB opened read-only with a shared flock. The handle
// is cached: the first call opens the database, subsequent calls return the
// same handle. If the database file doesn't exist yet (new project), returns
// an error — the caller should use readWriteDB() to create it first.
func (s *mcpSession) readOnlyDB() (*db.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ro != nil {
		return s.ro, nil
	}

	opts := s.dbOpts
	opts.ReadOnly = true
	opts.SharedRead = true

	database, err := db.OpenWithOptions(opts)
	if err != nil {
		return nil, err
	}
	s.ro = database
	s.lastReload = time.Now()
	return database, nil
}

// readWriteDB opens a *db.DB with an exclusive flock for writing. It closes
// any cached read-only handle first (LOCK_SH and LOCK_EX are mutually
// exclusive). The returned *db.DB is NOT cached — the caller must call
// database.Close() after use so the exclusive lock is released.
//
// On lock contention, returns a *vlsession.LockError with PID diagnostics.
func (s *mcpSession) readWriteDB() (*db.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Close cached RO so LOCK_EX can be acquired.
	if s.ro != nil {
		_ = s.ro.Close()
		s.ro = nil
	}

	database, err := db.OpenWithOptions(s.dbOpts)
	if err != nil {
		if errors.Is(err, vlsession.ErrFileLocked) {
			return nil, fmt.Errorf("%w (%s)", vlsession.ErrFileLocked, lockAgeDescription(s.dbOpts.DataDir))
		}
		return nil, err
	}
	return database, nil
}

// reloadIfStale reloads the cached read-only handle from disk if the reload
// threshold has elapsed or if the daemon.json file was modified since the
// last reload. This lets the RO MCP server pick up writes from the daemon or
// CLI index without closing and reopening.
func (s *mcpSession) reloadIfStale() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ro == nil {
		return nil // no RO handle to reload
	}

	stale := s.reloadThreshold > 0 && time.Since(s.lastReload) > s.reloadThreshold

	// Check daemon.json mtime as a cheaper "data changed" signal.
	if !stale && s.daemonJSONPath != "" {
		if info, err := os.Stat(s.daemonJSONPath); err == nil {
			if info.ModTime().After(s.lastReload) {
				stale = true
			}
		}
	}

	if !stale {
		return nil
	}

	if err := s.ro.Reload(); err != nil {
		return err
	}
	s.lastReload = time.Now()
	return nil
}

// close closes any cached handles and releases locks.
func (s *mcpSession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ro != nil {
		err := s.ro.Close()
		s.ro = nil
		return err
	}
	return nil
}

// hasDatabase returns true if the veclite database file exists.
func (s *mcpSession) hasDatabase() bool {
	vecPath := db.VecLitePath(s.cfg.DataDir)
	_, err := os.Stat(vecPath)
	return err == nil
}

// lockAgeForDB reads the lock file age for the database in the given data dir.
func lockAgeForDB(dataDir string) time.Duration {
	vecPath := db.VecLitePath(dataDir)
	lockPath := vecPath + ".lock"
	f, err := os.Open(lockPath)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, 128)
	n, _ := f.Read(buf)
	if n == 0 {
		return 0
	}
	// Parse "PID\nTIMESTAMP\n" format.
	lines := splitN(string(buf[:n]), '\n', 3)
	if len(lines) < 2 {
		return 0
	}
	var ts int64
	for i := 0; i < len(lines[1]); i++ {
		c := lines[1][i]
		if c < '0' || c > '9' {
			break
		}
		ts = ts*10 + int64(c-'0')
	}
	if ts == 0 {
		return 0
	}
	return time.Since(time.Unix(ts, 0))
}

func splitN(s string, sep byte, n int) []string {
	result := make([]string, 0, n)
	for i := 0; i < n; i++ {
		idx := -1
		for j := 0; j < len(s); j++ {
			if s[j] == sep {
				idx = j
				break
			}
		}
		if idx < 0 {
			result = append(result, s)
			break
		}
		result = append(result, s[:idx])
		s = s[idx+1:]
	}
	return result
}

// lockAgeDescription reads the lock file for the database in the given data dir
// and returns a human-readable description of the lock holder (PID + age).
func lockAgeDescription(dataDir string) string {
	age := lockAgeForDB(dataDir)
	if age > 0 {
		return fmt.Sprintf("locked %s ago", age.Truncate(time.Second))
	}
	return "lock file unreadable"
}

// formatLockError returns a user-friendly error message for lock contention.
func formatLockError(err error) string {
	if errors.Is(err, vlsession.ErrFileLocked) {
		return err.Error() + ". Search is available read-only. To update the index: " +
			"stop the other process or run 'vecgrep index' / 'vecgrep daemon reindex' from the CLI."
	}
	return err.Error()
}
