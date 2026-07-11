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

// defaultIdleEvictThreshold is how long the cached read-only handle may sit
// idle (no reads in flight, no recent access) before it is closed to release
// its shared file lock. Releasing it lets `vecgrep daemon start` acquire the
// exclusive lock, and lets later reads route through the daemon socket, instead
// of an idle MCP server pinning the shared lock for its whole lifetime.
const defaultIdleEvictThreshold = 30 * time.Second

// fileGeneration identifies one persisted database generation. SameFile
// detects atomic replacement even when size and timestamps happen to match;
// size and modification time detect ordinary in-place updates.
type fileGeneration struct {
	info   os.FileInfo
	exists bool
}

func statFileGeneration(path string) (fileGeneration, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fileGeneration{}, nil
		}
		return fileGeneration{}, err
	}
	return fileGeneration{info: info, exists: true}, nil
}

func (g fileGeneration) differs(next fileGeneration) bool {
	if g.exists != next.exists {
		return true
	}
	if !g.exists {
		return false
	}
	return !os.SameFile(g.info, next.info) ||
		g.info.Size() != next.info.Size() ||
		!g.info.ModTime().Equal(next.info.ModTime())
}

// mcpSession manages lazy dual-handle database access for the MCP server.
// Read tools borrow a cached read-only handle (shared flock, multiple readers
// OK) via a lease; write tools wait for outstanding leases to drain, then close
// the cached RO handle and open a fresh RW handle (exclusive flock) per call,
// returning it for the caller to close — so the exclusive lock is held only for
// the duration of the write operation.
//
// This resolves the multi-process lock contention: an idle MCP server releases
// its lock (see releaseIfIdle), a searching MCP server holds only a shared lock
// (doesn't block other readers), and a writing MCP server holds an exclusive
// lock only briefly. Leases (roLeases) guard against closing the RO handle out
// from under an in-flight reader — MCP tool handlers run concurrently.
type mcpSession struct {
	cfg         *config.Config
	projectRoot string
	provider    embed.Provider

	dbOpts db.OpenOptions

	mu       sync.Mutex
	cond     *sync.Cond // broadcast when roLeases reaches 0
	ro       *db.DB     // cached read-only handle (shared lock), nil when not open
	roLeases int        // in-flight borrowers of ro; ro must not be closed while > 0

	freshnessCheckInterval time.Duration
	idleThreshold          time.Duration // evict idle ro after this; 0 disables
	databasePath           string
	daemonJSONPath         string
	lastFreshnessCheck     time.Time
	databaseGeneration     fileGeneration
	daemonGeneration       fileGeneration
	lastAccess             time.Time

	reloadObserver func() // tests only; called after a successful reload
}

// newMCPSession creates a new MCP session. No database is opened until
// acquireRO() or readWriteDB() is called.
func newMCPSession(cfg *config.Config, projectRoot string, provider embed.Provider) *mcpSession {
	dbOpts := db.OpenOptions{
		Dimensions:         cfg.Embedding.Dimensions,
		DataDir:            cfg.DataDir,
		HNSWM:              cfg.Vector.VecLite.M,
		HNSWEfConstruction: cfg.Vector.VecLite.EfConstruction,
		HNSWEfSearch:       cfg.Vector.VecLite.EfSearch,
	}

	freshnessCheckInterval := 5 * time.Second
	if cfg.Server.MCPReloadInterval != "" {
		if d, err := time.ParseDuration(cfg.Server.MCPReloadInterval); err == nil {
			freshnessCheckInterval = d
		}
	}

	s := &mcpSession{
		cfg:                    cfg,
		projectRoot:            projectRoot,
		provider:               provider,
		dbOpts:                 dbOpts,
		freshnessCheckInterval: freshnessCheckInterval,
		idleThreshold:          defaultIdleEvictThreshold,
		databasePath:           db.VecLitePath(cfg.DataDir),
		daemonJSONPath:         filepath.Join(cfg.DataDir, "daemon.json"),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// acquireRO returns the cached read-only handle (opening it lazily with a
// shared flock) together with a release function the caller MUST invoke once it
// is done using the handle. While any lease is outstanding the handle will not
// be closed by readWriteDB, close(), or releaseIfIdle — this prevents a
// concurrent writer/evictor from closing the handle out from under an in-flight
// reader (use-after-close). If the database file doesn't exist yet (new
// project), returns an error — the caller should use readWriteDB() first.
func (s *mcpSession) acquireRO() (*db.DB, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ro == nil {
		opts := s.dbOpts
		opts.ReadOnly = true
		opts.SharedRead = true

		database, err := db.OpenWithOptions(opts)
		if err != nil {
			return nil, nil, err
		}
		s.ro = database
		s.observeLoadedGeneration(time.Now())
	}

	s.roLeases++
	s.lastAccess = time.Now()

	released := false
	release := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if released {
			return
		}
		released = true
		s.roLeases--
		if s.roLeases == 0 {
			s.cond.Broadcast()
		}
	}
	return s.ro, release, nil
}

// readWriteDB opens a *db.DB with an exclusive flock for writing. It first waits
// for any in-flight read leases to drain, then closes the cached read-only
// handle (LOCK_SH and LOCK_EX are mutually exclusive). The returned *db.DB is
// NOT cached — the caller must call database.Close() after use so the exclusive
// lock is released.
//
// On lock contention, returns a wrapped vlsession.ErrFileLocked with PID
// diagnostics.
func (s *mcpSession) readWriteDB() (*db.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Wait for in-flight readers to finish before dropping the shared handle.
	for s.roLeases > 0 {
		s.cond.Wait()
	}

	// Close cached RO so LOCK_EX can be acquired. We hold s.mu across the open
	// so a concurrent acquireRO can't slip a new shared lock in between.
	if s.ro != nil {
		_ = s.ro.Close()
		s.ro = nil
	}

	database, err := db.OpenWithOptions(s.dbOpts)
	if err != nil {
		// The exclusive open failed (we already dropped the cached RO handle to
		// attempt LOCK_EX). Best-effort restore the warm shared read handle so
		// reads don't cold-start after a failed write. This itself fails if
		// another process now holds the exclusive lock, in which case the next
		// read reopens lazily.
		roOpts := s.dbOpts
		roOpts.ReadOnly = true
		roOpts.SharedRead = true
		if roDB, roErr := db.OpenWithOptions(roOpts); roErr == nil {
			s.ro = roDB
			s.observeLoadedGeneration(time.Now())
		}
		if errors.Is(err, vlsession.ErrFileLocked) {
			return nil, fmt.Errorf("%w (%s)", vlsession.ErrFileLocked, lockAgeDescription(s.dbOpts.DataDir))
		}
		return nil, err
	}
	return database, nil
}

// releaseIfIdle closes the cached read-only handle (releasing its shared file
// lock) when no reads are in flight and it has been idle longer than the idle
// threshold. Returns true if it evicted the handle. The next acquireRO reopens
// it lazily — or, by then, reads route through a daemon socket instead.
func (s *mcpSession) releaseIfIdle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ro == nil || s.roLeases > 0 || s.idleThreshold <= 0 {
		return false
	}
	if time.Since(s.lastAccess) < s.idleThreshold {
		return false
	}
	_ = s.ro.Close()
	s.ro = nil
	return true
}

// reloadIfStale checks for a newer persisted database generation and reloads
// the cached read-only handle only when disk actually changed. The configured
// interval is a minimum between database metadata checks, not an elapsed-time
// staleness signal. daemon.json remains an immediate, independent signal.
func (s *mcpSession) reloadIfStale() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ro == nil {
		return nil
	}

	now := time.Now()
	daemonGeneration, daemonErr := statFileGeneration(s.daemonJSONPath)
	daemonChanged := daemonErr == nil &&
		daemonGeneration.exists &&
		s.daemonGeneration.differs(daemonGeneration)
	if daemonErr == nil && !daemonChanged {
		// A deletion is not a reload signal, but remembering it lets a later
		// recreation signal exactly once.
		s.daemonGeneration = daemonGeneration
	}

	checkDatabase := s.freshnessCheckInterval > 0 &&
		now.Sub(s.lastFreshnessCheck) >= s.freshnessCheckInterval
	var databaseGeneration fileGeneration
	databaseObserved := false
	databaseChanged := false
	if checkDatabase {
		s.lastFreshnessCheck = now
		if generation, err := statFileGeneration(s.databasePath); err == nil {
			databaseGeneration = generation
			databaseObserved = true
			databaseChanged = s.databaseGeneration.differs(generation)
		}
	} else if daemonChanged {
		// Capture the database generation before Reload. Recording a post-reload
		// stat could incorrectly mark a concurrent commit as already loaded.
		if generation, err := statFileGeneration(s.databasePath); err == nil {
			databaseGeneration = generation
			databaseObserved = true
		}
	}

	if !daemonChanged && !databaseChanged {
		return nil
	}
	if err := s.ro.Reload(); err != nil {
		return err
	}

	if databaseObserved {
		s.databaseGeneration = databaseGeneration
	}
	if daemonErr == nil {
		s.daemonGeneration = daemonGeneration
	}
	s.lastFreshnessCheck = now
	if s.reloadObserver != nil {
		s.reloadObserver()
	}
	return nil
}

// observeLoadedGeneration records the filesystem state represented by a newly
// opened read-only handle. The caller must hold s.mu.
func (s *mcpSession) observeLoadedGeneration(now time.Time) {
	if generation, err := statFileGeneration(s.databasePath); err == nil {
		s.databaseGeneration = generation
	}
	if generation, err := statFileGeneration(s.daemonJSONPath); err == nil {
		s.daemonGeneration = generation
	}
	s.lastFreshnessCheck = now
}

// close closes any cached handles and releases locks. It waits for in-flight
// read leases to drain first so it never closes the handle out from under a
// reader (close is called by activateProject when swapping sessions).
func (s *mcpSession) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.roLeases > 0 {
		s.cond.Wait()
	}
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
