package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	vlsession "github.com/abdul-hamid-achik/veclite/session"
)

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

func TestMCPSessionNewIsLazy(t *testing.T) {
	sess, dir := newTestMCPSession(t)
	defer func() { _ = sess.close() }()

	// No lock file should exist until readOnlyDB or readWriteDB is called.
	vecPath := db.VecLitePath(dir)
	if _, err := os.Stat(vecPath + ".lock"); err == nil {
		t.Fatal("lock file should not exist before first open")
	}
}

func TestMCPSessionReadOnlyDBOpensWithSharedLock(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	database, err := sess.readOnlyDB()
	if err != nil {
		t.Fatalf("readOnlyDB: %v", err)
	}
	if database == nil {
		t.Fatal("database is nil")
	}
}

func TestMCPSessionReadOnlyDBIsCached(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	db1, err := sess.readOnlyDB()
	if err != nil {
		t.Fatalf("readOnlyDB 1: %v", err)
	}
	db2, err := sess.readOnlyDB()
	if err != nil {
		t.Fatalf("readOnlyDB 2: %v", err)
	}
	if db1 != db2 {
		t.Fatal("readOnlyDB should return cached handle")
	}
}

func TestMCPSessionReadWriteDBClosesReadOnlyFirst(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	defer func() { _ = sess.close() }()

	// Open RO first.
	roDB, err := sess.readOnlyDB()
	if err != nil {
		t.Fatalf("readOnlyDB: %v", err)
	}
	if roDB == nil {
		t.Fatal("roDB is nil")
	}

	// Now open RW — should close RO first so LOCK_EX can be acquired.
	rwDB, err := sess.readWriteDB()
	if err != nil {
		t.Fatalf("readWriteDB after readOnlyDB: %v", err)
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

func TestMCPSessionReloadIfStaleAfterThreshold(t *testing.T) {
	sess, _ := newTestMCPSession(t, true)
	// Set a very short reload threshold for testing.
	sess.reloadThreshold = 50 * time.Millisecond
	defer func() { _ = sess.close() }()

	// Open RO.
	_, err := sess.readOnlyDB()
	if err != nil {
		t.Fatalf("readOnlyDB: %v", err)
	}

	// Immediately reload — should be no-op (not stale yet).
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("reloadIfStale (not stale): %v", err)
	}

	// Wait for stale threshold.
	time.Sleep(60 * time.Millisecond)

	// Now reload should happen.
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("reloadIfStale (stale): %v", err)
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
		Server: config.ServerConfig{MCPReloadInterval: "1h"}, // long threshold
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

	// Open RO.
	_, err = sess.readOnlyDB()
	if err != nil {
		t.Fatalf("readOnlyDB: %v", err)
	}

	// Simulate daemon writing daemon.json after the last reload.
	time.Sleep(10 * time.Millisecond)
	daemonJSON := filepath.Join(dir, "daemon.json")
	if err := os.WriteFile(daemonJSON, []byte(`{"pid":12345}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Reload should trigger because daemon.json was modified after lastReload.
	if err := sess.reloadIfStale(); err != nil {
		t.Fatalf("reloadIfStale on daemon.json change: %v", err)
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
	_, err = sess.readOnlyDB()
	if err != nil {
		t.Fatalf("readOnlyDB: %v", err)
	}

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
