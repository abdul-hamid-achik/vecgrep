package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	vlsession "github.com/abdul-hamid-achik/veclite/session"
)

type lifecycleMCPProvider struct {
	dimensions int
	model      string
	pingStart  chan struct{}
	pingDone   chan struct{}
	closeCalls atomic.Int32
}

func (p *lifecycleMCPProvider) Embed(context.Context, string) ([]float32, error) {
	return make([]float32, p.dimensions), nil
}

func (p *lifecycleMCPProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = make([]float32, p.dimensions)
	}
	return result, nil
}

func (p *lifecycleMCPProvider) Model() string   { return p.model }
func (p *lifecycleMCPProvider) Dimensions() int { return p.dimensions }
func (p *lifecycleMCPProvider) Ping(ctx context.Context) error {
	close(p.pingStart)
	select {
	case <-p.pingDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *lifecycleMCPProvider) Warmup(context.Context) (time.Duration, error) { return 0, nil }
func (p *lifecycleMCPProvider) Close()                                        { p.closeCalls.Add(1) }

// newTestMCPSession creates an mcpSession backed by a temp directory with
// a minimal vecgrep config. No embedding provider is needed for DB-only tests.
// If initDB is true, the veclite file is created with the "chunks" collection
// so read-only opens succeed.
func newTestMCPSession(t *testing.T, initDB ...bool) (*mcpSession, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir,
		Embedding: config.EmbeddingConfig{
			Provider:   "ollama",
			Model:      "nomic-embed-text",
			OllamaURL:  "http://localhost:11434",
			Dimensions: 768,
		},
		Server: config.ServerConfig{
			MCPReloadInterval: "1s",
		},
	}
	// Ensure data dir exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := newMCPSession(cfg, dir, nil)

	// Optionally initialize the DB with the chunks collection.
	if len(initDB) > 0 && initDB[0] {
		rwDB, err := sess.readWriteDB()
		if err != nil {
			t.Fatalf("init readWriteDB: %v", err)
		}
		_ = rwDB.Close()
	}

	return sess, dir
}

// openRO acquires the cached read-only handle and immediately releases the
// lease (the handle stays open/cached). For tests that just need the handle and
// don't exercise concurrent close.
func openRO(t *testing.T, sess *mcpSession) *db.DB {
	t.Helper()
	database, release, err := sess.acquireRO()
	if err != nil {
		t.Fatalf("acquireRO: %v", err)
	}
	release()
	return database
}

func TestMCPSessionNewIsLazy(t *testing.T) {
	sess, dir := newTestMCPSession(t)
	defer func() { _ = sess.close() }()

	// No lock file should exist until acquireRO or readWriteDB is called.
	vecPath := db.VecLitePath(dir)
	if _, err := os.Stat(vecPath + ".lock"); err == nil {
		t.Fatal("lock file should not exist before first open")
	}
}

func TestMCPSessionLeaseBlocksWriteUntilReleased(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	// Hold a read lease without releasing it.
	_, release, err := sess.acquireRO()
	if err != nil {
		t.Fatalf("acquireRO: %v", err)
	}

	// readWriteDB must wait for the lease to drain before closing the RO handle
	// and acquiring LOCK_EX — otherwise it would close the handle out from
	// under the live reader.
	done := make(chan *db.DB, 1)
	go func() {
		rw, rwErr := sess.readWriteDB()
		if rwErr != nil {
			t.Errorf("readWriteDB: %v", rwErr)
		}
		done <- rw
	}()

	select {
	case <-done:
		t.Fatal("readWriteDB returned while a read lease was still held")
	case <-time.After(100 * time.Millisecond):
		// Expected: blocked on the outstanding lease.
	}

	release()

	select {
	case rw := <-done:
		if rw != nil {
			_ = rw.Close()
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readWriteDB did not proceed after the lease was released")
	}
}

func TestMCPSessionReleaseIfIdleEvictsHandle(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	sess.idleThreshold = 20 * time.Millisecond

	// Open and release a lease so the handle is cached but idle.
	openRO(t, sess)

	// Read-only opens are lock-free (veclite v0.22.0+), so no .lock file is
	// left on disk while the RO handle is cached — a writer is never blocked
	// by it. Eviction is now about memory hygiene, not releasing a shared lock.

	// Not idle long enough yet.
	if sess.releaseIfIdle() {
		t.Fatal("releaseIfIdle evicted before the idle threshold elapsed")
	}

	time.Sleep(40 * time.Millisecond)

	if !sess.releaseIfIdle() {
		t.Fatal("releaseIfIdle should have evicted the idle handle")
	}

	// After eviction the cached RO handle is gone; a writer opens cleanly.
	// (Lock-free reads mean the writer was never blocked by the RO handle in
	// the first place — this just confirms eviction + write still work.)
	rw, err := sess.readWriteDB()
	if err != nil {
		t.Fatalf("readWriteDB after idle eviction: %v", err)
	}
	_ = rw.Close()
}

// TestMCPSessionConcurrentReadersAndEvictor exercises the lease/evict/reload
// machinery under -race: many readers borrow the shared handle while an evictor
// repeatedly tries to close it. Leases must keep the handle alive against the
// evictor, and pointer swaps (reload/evict) must never race a live reader.
func TestMCPSessionConcurrentReadersAndEvictor(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()
	sess.idleThreshold = time.Millisecond // race eviction against active readers

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				database, release, err := sess.acquireRO()
				if err != nil {
					t.Errorf("acquireRO: %v", err)
					return
				}
				_ = sess.reloadIfStale()
				_, _ = database.Stats()
				release()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			sess.releaseIfIdle()
		}
	}()
	wg.Wait()
}

func TestMCPSessionReleaseIfIdleKeepsHandleWhileLeased(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	sess.idleThreshold = 1 * time.Millisecond

	_, release, err := sess.acquireRO()
	if err != nil {
		t.Fatalf("acquireRO: %v", err)
	}
	defer release()

	time.Sleep(5 * time.Millisecond)

	// An outstanding lease must prevent eviction even past the idle threshold.
	if sess.releaseIfIdle() {
		t.Fatal("releaseIfIdle evicted a handle that still had an outstanding lease")
	}
}

func TestMCPSessionReadOnlyDBOpensWithSharedLock(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	database := openRO(t, sess)
	if database == nil {
		t.Fatal("database is nil")
	}
}

func TestMCPSessionReadOnlyDBIsCached(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	db1 := openRO(t, sess)
	db2 := openRO(t, sess)
	if db1 != db2 {
		t.Fatal("acquireRO should return the cached handle")
	}
}

func TestMCPSessionReadWriteDBClosesReadOnlyFirst(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	// Open RO first (lease released by openRO).
	roDB := openRO(t, sess)
	if roDB == nil {
		t.Fatal("roDB is nil")
	}

	// Now open RW — should close RO first so LOCK_EX can be acquired.
	rwDB, err := sess.readWriteDB()
	if err != nil {
		t.Fatalf("readWriteDB after acquireRO: %v", err)
	}
	defer rwDB.Close()
}

func TestMCPSessionReadWriteDBReturnsLockErrorOnContenion(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir,
		Embedding: config.EmbeddingConfig{
			Provider:   "ollama",
			Dimensions: 768,
		},
		Server: config.ServerConfig{MCPReloadInterval: "1s"},
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	// Session 1: open RW and hold the lock.
	sess1 := newMCPSession(cfg, dir, nil)
	rwDB, err := sess1.readWriteDB()
	if err != nil {
		t.Fatalf("sess1 readWriteDB: %v", err)
	}
	defer rwDB.Close()

	// Session 2: try to open RW — should get an error wrapping ErrFileLocked.
	sess2 := newMCPSession(cfg, dir, nil)
	defer func() { _ = sess2.close() }()
	_, err = sess2.readWriteDB()
	if err == nil {
		t.Fatal("expected lock error, got nil")
	}
	if !errors.Is(err, vlsession.ErrFileLocked) {
		t.Fatalf("error should wrap ErrFileLocked, got: %v", err)
	}
}

func TestMCPSessionReadWriteDBNotCached(t *testing.T) {
	sess, _ := newTestMCPSession(t)
	defer func() { _ = sess.close() }()

	db1, err := sess.readWriteDB()
	if err != nil {
		t.Fatalf("readWriteDB 1: %v", err)
	}
	// Close the returned handle (as a real caller would).
	_ = db1.Close()

	// Opening again should work (the previous handle was closed by the caller).
	db2, err := sess.readWriteDB()
	if err != nil {
		t.Fatalf("readWriteDB 2: %v", err)
	}
	_ = db2.Close()
}

func TestMCPSessionHasDatabaseReturnsFalseForNewProject(t *testing.T) {
	sess, _ := newTestMCPSession(t)
	defer func() { _ = sess.close() }()

	if sess.hasDatabase() {
		t.Fatal("hasDatabase should be false for a new project with no veclite file")
	}
}

func TestMCPSessionHasDatabaseReturnsTrueAfterWrite(t *testing.T) {
	sess, _ := newTestMCPSession(t)
	defer func() { _ = sess.close() }()

	// Open RW to create the database file.
	rwDB, err := sess.readWriteDB()
	if err != nil {
		t.Fatalf("readWriteDB: %v", err)
	}
	_ = rwDB.Close()

	// Now hasDatabase should be true.
	if !sess.hasDatabase() {
		t.Fatal("hasDatabase should be true after creating the veclite file")
	}
}

func TestMCPSessionReloadIfStaleNoROHandle(t *testing.T) {
	sess, _ := newTestMCPSession(t)
	defer func() { _ = sess.close() }()

	// No RO handle open — should be no-op.
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("reloadIfStale with no RO: %v", err)
	}
}

func TestMCPSessionFreshnessCheckDoesNotReloadWithoutWrite(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()
	sess.freshnessCheckInterval = time.Hour

	openRO(t, sess)
	reloads := 0
	sess.reloadObserver = func() { reloads++ }

	// Make the next read eligible for a metadata check without sleeping.
	sess.lastFreshnessCheck = time.Now().Add(-2 * sess.freshnessCheckInterval)
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("reloadIfStale after interval: %v", err)
	}
	if reloads != 0 {
		t.Fatalf("elapsed interval caused %d reloads without a persisted write", reloads)
	}
}

func TestMCPSessionFreshnessCheckReloadsCommittedExternalWriteOnce(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()
	sess.freshnessCheckInterval = time.Hour

	reader := openRO(t, sess)
	if stats, err := reader.Stats(); err != nil || stats["embeddings"] != 0 {
		t.Fatalf("initial embeddings = %d, err = %v", stats["embeddings"], err)
	}

	writer, err := db.OpenWithOptions(sess.dbOpts)
	if err != nil {
		t.Fatalf("open external writer: %v", err)
	}
	embedding := make([]float32, sess.dbOpts.Dimensions)
	if _, err := writer.InsertChunk(db.ChunkRecord{
		RelativePath: "external.go",
		StartLine:    1,
		IndexedAt:    time.Now(),
	}, embedding); err != nil {
		_ = writer.Close()
		t.Fatalf("external insert: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close external writer: %v", err)
	}

	reloads := 0
	sess.reloadObserver = func() { reloads++ }
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("reloadIfStale before interval: %v", err)
	}
	if reloads != 0 {
		t.Fatalf("external write reloaded before freshness interval elapsed")
	}
	if stats, err := reader.Stats(); err != nil || stats["embeddings"] != 0 {
		t.Fatalf("embeddings before eligible reload = %d, err = %v", stats["embeddings"], err)
	}

	sess.lastFreshnessCheck = time.Now().Add(-2 * sess.freshnessCheckInterval)
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("reloadIfStale after external write: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("reloads after committed external write = %d, want 1", reloads)
	}
	if stats, err := reader.Stats(); err != nil || stats["embeddings"] != 1 {
		t.Fatalf("embeddings after reload = %d, err = %v", stats["embeddings"], err)
	}

	// The same persisted generation must not trigger another reload on the
	// next eligible check.
	sess.lastFreshnessCheck = time.Now().Add(-2 * sess.freshnessCheckInterval)
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("second reloadIfStale: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("same generation reloaded %d times, want exactly 1", reloads)
	}
}

func TestMCPSessionReloadIfStaleOnDaemonJSONChange(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir,
		Embedding: config.EmbeddingConfig{
			Provider:   "ollama",
			Dimensions: 768,
		},
		Server: config.ServerConfig{MCPReloadInterval: "1h"},
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	sess := newMCPSession(cfg, dir, nil)
	defer func() { _ = sess.close() }()

	rwDB, err := sess.readWriteDB()
	if err != nil {
		t.Fatalf("init readWriteDB: %v", err)
	}
	_ = rwDB.Close()
	openRO(t, sess)

	reloads := 0
	sess.reloadObserver = func() { reloads++ }
	daemonJSON := filepath.Join(dir, "daemon.json")
	if err := os.WriteFile(daemonJSON, []byte(`{"pid":12345}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("reloadIfStale on daemon.json change: %v", err)
	}
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("second reloadIfStale on daemon.json change: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("daemon signal caused %d reloads, want exactly 1", reloads)
	}
}

func TestMCPSessionCloseReleasesReadOnlyLock(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir,
		Embedding: config.EmbeddingConfig{
			Provider:   "ollama",
			Dimensions: 768,
		},
		Server: config.ServerConfig{MCPReloadInterval: "1s"},
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	sess := newMCPSession(cfg, dir, nil)

	// Initialize the DB with a RW open first.
	rwDB, err := sess.readWriteDB()
	if err != nil {
		t.Fatalf("init readWriteDB: %v", err)
	}
	_ = rwDB.Close()

	// Now open RO.
	openRO(t, sess)

	// Close should release the lock.
	if err := sess.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Another session should be able to open RW now.
	sess2 := newMCPSession(cfg, dir, nil)
	defer func() { _ = sess2.close() }()
	rwDB2, err := sess2.readWriteDB()
	if err != nil {
		t.Fatalf("sess2 readWriteDB after close: %v", err)
	}
	defer rwDB2.Close()
}

func TestMCPSessionCloseWaitsForIndexAndClosesProviderOnce(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Embedding.Dimensions = 8
	cfg.Codemap.Enabled = false
	cfg.Codemap.StructuralChunks = string(app.StructuralChunksOff)
	provider := &lifecycleMCPProvider{
		dimensions: cfg.Embedding.Dimensions,
		model:      cfg.Embedding.Model,
		pingStart:  make(chan struct{}),
		pingDone:   make(chan struct{}),
	}
	var releasePing sync.Once
	defer releasePing.Do(func() { close(provider.pingDone) })
	sess := newMCPSession(cfg, root, provider)

	indexErr := make(chan error, 1)
	go func() {
		_, err := sess.index(context.Background(), app.IndexRequest{StructuralChunks: string(app.StructuralChunksOff)})
		indexErr <- err
	}()
	select {
	case <-provider.pingStart:
	case <-time.After(time.Second):
		t.Fatal("index did not reach provider preflight")
	}

	closed := make(chan error, 1)
	go func() { closed <- sess.close() }()
	waitForMCPSessionClosing(t, sess)
	if _, err := sess.index(context.Background(), app.IndexRequest{}); !errors.Is(err, errMCPSessionClosing) {
		t.Fatalf("new index during close error = %v, want session closing", err)
	}
	select {
	case err := <-closed:
		t.Fatalf("close returned during provider preflight: %v", err)
	default:
	}
	if provider.closeCalls.Load() != 0 {
		t.Fatal("provider closed while an index operation was active")
	}

	releasePing.Do(func() { close(provider.pingDone) })
	if err := <-indexErr; !errors.Is(err, errMCPSessionClosing) {
		t.Fatalf("in-flight index error = %v, want nested DB lease rejected by close", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not drain the index operation")
	}
	if provider.closeCalls.Load() != 1 {
		t.Fatalf("provider close calls = %d, want 1", provider.closeCalls.Load())
	}
	if err := sess.close(); err != nil {
		t.Fatalf("second close error = %v", err)
	}
	if provider.closeCalls.Load() != 1 {
		t.Fatalf("provider close calls after second close = %d, want 1", provider.closeCalls.Load())
	}
}

func TestMCPSessionCloseWaitsForWriteLease(t *testing.T) {
	sess, _ := newTestMCPSession(t)
	_, release, err := sess.acquireWriteDB(context.Background())
	if err != nil {
		t.Fatalf("acquireWriteDB: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- sess.close() }()
	waitForMCPSessionClosing(t, sess)

	if _, _, err := sess.acquireWriteDB(context.Background()); !errors.Is(err, errMCPSessionClosing) {
		t.Fatalf("new write lease during close error = %v, want session closing", err)
	}
	if _, _, err := sess.acquireRO(); !errors.Is(err, errMCPSessionClosing) {
		t.Fatalf("new read lease during close error = %v, want session closing", err)
	}
	select {
	case err := <-closed:
		t.Fatalf("close returned before write release: %v", err)
	default:
	}
	if err := release(); err != nil {
		t.Fatalf("release write lease: %v", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not drain the write lease")
	}
}

func TestMCPSessionSerializesWriterLeasesUntilRelease(t *testing.T) {
	sess, _ := newTestMCPSession(t)
	defer func() { _ = sess.close() }()

	first, releaseFirst, err := sess.acquireWriteDB(context.Background())
	if err != nil || first == nil {
		t.Fatalf("first acquireWriteDB = %v, %v", first, err)
	}
	type writeLease struct {
		database *db.DB
		release  func() error
		err      error
	}
	second := make(chan writeLease, 1)
	go func() {
		database, release, err := sess.acquireWriteDB(context.Background())
		second <- writeLease{database: database, release: release, err: err}
	}()
	waitForMCPWriterWaiters(t, sess, 1)

	select {
	case got := <-second:
		t.Fatalf("second writer acquired before first release: %+v", got)
	default:
	}
	if err := releaseFirst(); err != nil {
		t.Fatalf("release first writer: %v", err)
	}

	select {
	case got := <-second:
		if got.err != nil || got.database == nil || got.release == nil {
			t.Fatalf("second writer lease = %+v", got)
		}
		if err := got.release(); err != nil {
			t.Fatalf("release second writer: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second writer did not acquire after first release")
	}
}

func TestMCPSessionWriterPreferenceDrainsReadersAndBlocksNewOnes(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	_, releaseInitialReader, err := sess.acquireRO()
	if err != nil {
		t.Fatalf("initial acquireRO: %v", err)
	}
	type writeLease struct {
		database *db.DB
		release  func() error
		err      error
	}
	writer := make(chan writeLease, 1)
	go func() {
		database, release, err := sess.acquireWriteDB(context.Background())
		writer <- writeLease{database: database, release: release, err: err}
	}()
	waitForMCPWriterActive(t, sess)

	reader := make(chan error, 1)
	go func() {
		_, release, err := sess.acquireROContext(context.Background())
		if release != nil {
			release()
		}
		reader <- err
	}()
	select {
	case err := <-reader:
		t.Fatalf("new reader bypassed waiting writer: %v", err)
	default:
	}

	releaseInitialReader()
	var activeWriter writeLease
	select {
	case activeWriter = <-writer:
		if activeWriter.err != nil || activeWriter.database == nil || activeWriter.release == nil {
			t.Fatalf("writer lease = %+v", activeWriter)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not progress after the initial reader drained")
	}
	select {
	case err := <-reader:
		t.Fatalf("new reader entered while writer handle was active: %v", err)
	default:
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	canceledReader := make(chan error, 1)
	go func() {
		_, _, err := sess.acquireROContext(cancelCtx)
		canceledReader <- err
	}()
	cancel()
	select {
	case err := <-canceledReader:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked reader error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked reader did not observe cancellation")
	}

	if err := activeWriter.release(); err != nil {
		t.Fatalf("release writer: %v", err)
	}
	select {
	case err := <-reader:
		if err != nil {
			t.Fatalf("reader after writer release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not proceed after writer release")
	}
}

func TestMCPSessionWriterProgressesAgainstContinuousReaders(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	readCtx, stopReaders := context.WithCancel(context.Background())
	var reads atomic.Int64
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				database, release, err := sess.acquireROContext(readCtx)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					t.Errorf("continuous acquireRO: %v", err)
					return
				}
				_, _ = database.Stats()
				reads.Add(1)
				release()
			}
		}()
	}
	deadline := time.Now().Add(time.Second)
	for reads.Load() < 16 {
		if time.Now().After(deadline) {
			stopReaders()
			readers.Wait()
			t.Fatal("continuous readers did not start")
		}
		time.Sleep(time.Millisecond)
	}

	writerCtx, cancelWriter := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelWriter()
	type writeLease struct {
		release func() error
		err     error
	}
	writer := make(chan writeLease, 1)
	go func() {
		_, release, err := sess.acquireWriteDB(writerCtx)
		writer <- writeLease{release: release, err: err}
	}()
	var got writeLease
	select {
	case got = <-writer:
		if got.err != nil || got.release == nil {
			stopReaders()
			readers.Wait()
			t.Fatalf("writer under reader load = %+v", got)
		}
	case <-time.After(2 * time.Second):
		stopReaders()
		readers.Wait()
		t.Fatal("continuous readers starved writer")
	}
	if err := got.release(); err != nil {
		t.Fatalf("release writer: %v", err)
	}
	stopReaders()
	readers.Wait()
}

func TestMCPSessionWriterCancelsWhileDrainingExistingReader(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()
	_, releaseReader, err := sess.acquireRO()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseReader()

	ctx, cancel := context.WithCancel(context.Background())
	writer := make(chan error, 1)
	go func() {
		_, _, err := sess.acquireWriteDB(ctx)
		writer <- err
	}()
	waitForMCPWriterActive(t, sess)
	cancel()
	select {
	case err := <-writer:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("writer drain error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not cancel while draining an existing reader")
	}

	// Canceling the preferred writer must reopen admission for ordinary reads
	// even though the original reader is still leased.
	_, releaseNext, err := sess.acquireROContext(context.Background())
	if err != nil {
		t.Fatalf("reader after canceled writer: %v", err)
	}
	releaseNext()
}

func TestMCPSessionQueuedWriterCancellationAndCloseDrain(t *testing.T) {
	sess, _ := newTestMCPSession(t)
	_, releaseFirst, err := sess.acquireWriteDB(context.Background())
	if err != nil {
		t.Fatalf("first acquireWriteDB: %v", err)
	}

	waitCtx, cancelWait := context.WithCancel(context.Background())
	waitResult := make(chan error, 1)
	go func() {
		_, _, err := sess.acquireWriteDB(waitCtx)
		waitResult <- err
	}()
	waitForMCPWriterWaiters(t, sess, 1)
	cancelWait()
	select {
	case err := <-waitResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued writer error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued writer did not observe cancellation")
	}

	closed := make(chan error, 1)
	go func() { closed <- sess.close() }()
	waitForMCPSessionClosing(t, sess)
	select {
	case err := <-closed:
		t.Fatalf("close returned before active writer release: %v", err)
	default:
	}
	if _, _, err := sess.acquireWriteDB(context.Background()); !errors.Is(err, errMCPSessionClosing) {
		t.Fatalf("writer accepted during close: %v", err)
	}
	if err := releaseFirst(); err != nil {
		t.Fatalf("release first writer: %v", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not drain the active writer")
	}
}

func waitForMCPWriterWaiters(t *testing.T, sess *mcpSession, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		sess.mu.Lock()
		waiters := sess.writerWaiters
		sess.mu.Unlock()
		if waiters >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("writer waiters = %d, want at least %d", waiters, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForMCPWriterActive(t *testing.T, sess *mcpSession) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		sess.mu.Lock()
		active := sess.writerActive
		sess.mu.Unlock()
		if active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("writer never became active")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForMCPSessionClosing(t *testing.T, sess *mcpSession) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		sess.mu.Lock()
		closing := sess.closing
		sess.mu.Unlock()
		if closing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session never entered closing state")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFormatLockErrorWithFileLocked(t *testing.T) {
	err := errors.New("veclite: database file is locked by another process (PID 12345, locked 30s ago)")
	wrapped := &wrappedLockError{err: err}
	msg := formatLockError(wrapped)
	if msg == "" {
		t.Fatal("formatLockError should return non-empty message")
	}
	// Should contain the helpful guidance about CLI/daemon.
	if !contains(msg, "read-only") {
		t.Fatal("formatLockError should mention read-only search is available")
	}
	if !contains(msg, "vecgrep index") {
		t.Fatal("formatLockError should suggest CLI index command")
	}
}

type wrappedLockError struct{ err error }

func (e *wrappedLockError) Error() string { return e.err.Error() }
func (e *wrappedLockError) Unwrap() error { return vlsession.ErrFileLocked }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Ensure embed.Provider is referenced (unused in tests but needed for compilation)
var _ embed.Provider = (embed.Provider)(nil)
