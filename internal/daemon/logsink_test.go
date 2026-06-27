package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotatingSinkWriteAndRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	s, err := newRotatingSink(path)
	if err != nil {
		t.Fatalf("newRotatingSink: %v", err)
	}
	defer s.Close()

	if _, err := s.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	now := time.Date(2026, 6, 27, 10, 30, 0, 0, time.UTC)
	rotated, err := s.Rotate(now)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated == "" {
		t.Fatal("expected a rotated path for a non-empty log")
	}

	// The rotated file holds the old content.
	if got, _ := os.ReadFile(rotated); string(got) != "hello\n" {
		t.Fatalf("rotated content = %q, want %q", got, "hello\n")
	}

	// The live path is a fresh, empty file.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat live log: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("live log size = %d, want 0 after rotate", info.Size())
	}

	// Writing after rotate goes to the fresh file only.
	if _, err := s.Write([]byte("world\n")); err != nil {
		t.Fatalf("write after rotate: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "world\n" {
		t.Fatalf("post-rotate content = %q, want %q", got, "world\n")
	}
}

func TestRotatingSinkRotateEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	s, err := newRotatingSink(path)
	if err != nil {
		t.Fatalf("newRotatingSink: %v", err)
	}
	defer s.Close()

	rotated, err := s.Rotate(time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated != "" {
		t.Fatalf("expected no rotation for an empty log, got %q", rotated)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 { // only the live daemon.log
		t.Fatalf("expected only the live log, got %d entries", len(entries))
	}
}

func TestRotatingSinkWriteAfterClose(t *testing.T) {
	dir := t.TempDir()
	s, err := newRotatingSink(filepath.Join(dir, "daemon.log"))
	if err != nil {
		t.Fatalf("newRotatingSink: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Writing after close must not panic and must report bytes consumed.
	n, err := s.Write([]byte("dropped"))
	if err != nil || n != len("dropped") {
		t.Fatalf("write after close = (%d, %v), want (%d, nil)", n, err, len("dropped"))
	}
	// Rotate after close is a no-op.
	if rotated, err := s.Rotate(time.Now()); err != nil || rotated != "" {
		t.Fatalf("rotate after close = (%q, %v), want (\"\", nil)", rotated, err)
	}
}
