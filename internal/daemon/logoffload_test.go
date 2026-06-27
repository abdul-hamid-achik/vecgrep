package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
)

func TestOffloadLogWithoutFcheapKeepsSegment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	sink, err := newRotatingSink(path)
	if err != nil {
		t.Fatalf("newRotatingSink: %v", err)
	}
	t.Cleanup(func() { _ = sink.Close() })

	if _, err := sink.Write([]byte("log line\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	d := &Daemon{logSink: sink}
	// A nil provider exercises the "fcheap unavailable" branch: the segment is
	// rotated and kept locally, never deleted.
	d.offloadLog(context.Background(), nil, time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC))

	rotated := findRotatedSegment(t, dir, path)
	if got, _ := os.ReadFile(rotated); string(got) != "log line\n" {
		t.Fatalf("rotated content = %q, want %q", got, "log line\n")
	}
}

func TestOffloadLogNilSinkIsSafe(t *testing.T) {
	// Offload disabled (no sink) must be a safe no-op, not a nil-deref panic.
	d := &Daemon{}
	d.offloadLog(context.Background(), nil, time.Now())
}

func TestProjectLabelFallsBackToRootBase(t *testing.T) {
	withName := &Daemon{session: &app.Session{ProjectName: "named", ProjectRoot: "/tmp/whatever"}}
	if got := withName.projectLabel(); got != "named" {
		t.Errorf("projectLabel() = %q, want %q", got, "named")
	}

	noName := &Daemon{session: &app.Session{ProjectName: "", ProjectRoot: "/tmp/projects/monitor"}}
	if got := noName.projectLabel(); got != "monitor" {
		t.Errorf("projectLabel() = %q, want %q", got, "monitor")
	}
}

func findRotatedSegment(t *testing.T, dir, livePath string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var rotated string
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if full == livePath {
			continue
		}
		if rotated != "" {
			t.Fatalf("expected exactly one rotated segment, found extra: %s", e.Name())
		}
		rotated = full
	}
	if rotated == "" {
		t.Fatal("no rotated segment found")
	}
	return rotated
}
