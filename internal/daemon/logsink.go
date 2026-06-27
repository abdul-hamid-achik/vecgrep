package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"time"
)

// rotatingSink is an io.Writer backed by a single append-only log file that can
// be rotated on demand: Rotate renames the current file aside and reopens a
// fresh one, returning the rotated path so the caller can offload it (e.g. to
// the fcheap vault). It is safe for concurrent use by the logger and the
// offload goroutine.
type rotatingSink struct {
	path string

	mu sync.Mutex
	f  *os.File
}

// newRotatingSink opens (creating as needed) the log file at path.
func newRotatingSink(path string) (*rotatingSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	return &rotatingSink{path: path, f: f}, nil
}

// Write implements io.Writer. After Close it silently drops writes so a late
// log line never panics on a nil file.
func (s *rotatingSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return len(p), nil
	}
	return s.f.Write(p)
}

// Rotate renames the current file to "<path>.<UTC timestamp>" and opens a fresh
// file at path, returning the rotated file's path. It returns "" (no error)
// when the current file is empty — there is nothing worth offloading.
func (s *rotatingSink) Rotate(now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return "", nil
	}
	if info, err := s.f.Stat(); err == nil && info.Size() == 0 {
		return "", nil
	}

	rotated := s.path + "." + now.UTC().Format("20060102T150405.000000000Z")
	if err := s.f.Close(); err != nil {
		s.f = nil
		return "", err
	}
	if err := os.Rename(s.path, rotated); err != nil {
		// Reopen the original so logging keeps working even if rename failed.
		s.f, _ = openAppend(s.path)
		return "", err
	}
	f, err := openAppend(s.path)
	if err != nil {
		s.f = nil
		return rotated, err
	}
	s.f = f
	return rotated, nil
}

// Close closes the underlying file. Subsequent writes are dropped.
func (s *rotatingSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}

func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}
