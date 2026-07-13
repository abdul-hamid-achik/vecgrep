package index

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherStopBeforeStartIsNonBlockingAndIdempotent(t *testing.T) {
	cfg := DefaultWatcherConfig()
	cfg.Recursive = false
	watcher, err := NewWatcher(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- watcher.Stop() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop() blocked before Start()")
	}
	if err := watcher.Stop(); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
	if err := watcher.Start(context.Background()); err == nil {
		t.Fatal("Start() after Stop() succeeded")
	}
}

func TestWatcherStartFailureClosesResourcesWithoutBlockingStop(t *testing.T) {
	cfg := DefaultWatcherConfig()
	cfg.Recursive = false
	missing := filepath.Join(t.TempDir(), "missing")
	watcher, err := NewWatcher(missing, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := watcher.Start(context.Background()); err == nil {
		t.Fatal("Start() with a missing root succeeded")
	}

	done := make(chan error, 1)
	go func() { done <- watcher.Stop() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop() blocked after Start() failed")
	}
}

func TestWatcherStopAfterStartIsIdempotent(t *testing.T) {
	cfg := DefaultWatcherConfig()
	cfg.Recursive = false
	watcher, err := NewWatcher(t.TempDir(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := watcher.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := watcher.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := watcher.Stop(); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}
