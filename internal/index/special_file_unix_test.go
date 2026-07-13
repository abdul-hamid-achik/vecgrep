//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package index

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestIndexAndRawPendingSkipFIFOs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		viaLink bool
	}{
		{name: "direct"},
		{name: "symlink", viaLink: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const dimensions = 8
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			fifoPath := filepath.Join(root, "events.pipe")
			if tc.viaLink {
				targetPath := filepath.Join(t.TempDir(), "events.pipe")
				if err := syscall.Mkfifo(targetPath, 0o600); err != nil {
					t.Skipf("create FIFO: %v", err)
				}
				symlinkOrSkip(t, targetPath, fifoPath)
			} else if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
				t.Skipf("create FIFO: %v", err)
			}

			database := openTestDB(t, dimensions)
			cfg := DefaultIndexerConfig()
			cfg.Workers = 1
			idx := NewIndexer(database, newMockEmbedProvider(dimensions), cfg)

			type indexOutcome struct {
				result *IndexResult
				err    error
			}
			indexDone := make(chan indexOutcome, 1)
			go func() {
				result, err := idx.Index(context.Background(), root)
				indexDone <- indexOutcome{result: result, err: err}
			}()
			select {
			case outcome := <-indexDone:
				if outcome.err != nil {
					t.Fatalf("index with FIFO: %v", outcome.err)
				}
				if len(outcome.result.Errors) != 0 {
					t.Fatalf("index with FIFO errors: %v", outcome.result.Errors)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("index blocked while opening FIFO")
			}

			type freshnessOutcome struct {
				pending  *PendingChanges
				complete bool
				err      error
			}
			freshnessDone := make(chan freshnessOutcome, 1)
			go func() {
				pending, complete, err := idx.GetRawPendingChanges(context.Background(), root)
				freshnessDone <- freshnessOutcome{pending: pending, complete: complete, err: err}
			}()
			select {
			case outcome := <-freshnessDone:
				if outcome.err != nil || !outcome.complete || outcome.pending == nil || outcome.pending.TotalPending != 0 {
					t.Fatalf("freshness with FIFO = %+v complete=%t err=%v", outcome.pending, outcome.complete, outcome.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("raw freshness blocked while opening FIFO")
			}
		})
	}
}
