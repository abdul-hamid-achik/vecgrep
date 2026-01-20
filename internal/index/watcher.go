// Package index provides file indexing and watching for semantic search.
package index

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatcherConfig configures the file watcher behavior.
type WatcherConfig struct {
	// Debounce is the duration to wait before processing changes.
	// Multiple changes within this window are batched together.
	Debounce time.Duration

	// IgnorePatterns are glob patterns for files/directories to ignore.
	IgnorePatterns []string

	// MaxFileSize is the maximum file size to watch for changes.
	MaxFileSize int64

	// Recursive enables recursive directory watching.
	Recursive bool
}

// DefaultWatcherConfig returns sensible defaults for the watcher.
func DefaultWatcherConfig() WatcherConfig {
	return WatcherConfig{
		Debounce: 500 * time.Millisecond,
		IgnorePatterns: []string{
			".git/**",
			".vecgrep/**",
			"node_modules/**",
			"vendor/**",
			"__pycache__/**",
			"*.min.js",
			"*.min.css",
			"*.lock",
			"go.sum",
			"package-lock.json",
			"yarn.lock",
			"*.tmp",
			"*~",
			".#*",
		},
		MaxFileSize: 1024 * 1024, // 1MB
		Recursive:   true,
	}
}

// WatchEvent represents a file system change event.
type WatchEvent struct {
	Path      string
	Op        WatchOp
	Timestamp time.Time
}

// WatchOp represents the type of file system operation.
type WatchOp int

const (
	// OpCreate indicates a file was created.
	OpCreate WatchOp = iota
	// OpWrite indicates a file was modified.
	OpWrite
	// OpRemove indicates a file was removed.
	OpRemove
	// OpRename indicates a file was renamed.
	OpRename
)

func (op WatchOp) String() string {
	switch op {
	case OpCreate:
		return "create"
	case OpWrite:
		return "write"
	case OpRemove:
		return "remove"
	case OpRename:
		return "rename"
	default:
		return "unknown"
	}
}

// WatchCallback is called when file system changes are detected.
// The paths slice contains all changed files since the last callback.
type WatchCallback func(events []WatchEvent)

// Watcher monitors file system changes for auto-reindexing.
type Watcher struct {
	config   WatcherConfig
	watcher  *fsnotify.Watcher
	callback WatchCallback
	rootPath string

	pendingMu sync.Mutex
	pending   map[string]WatchEvent

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewWatcher creates a new file watcher for the given root path.
func NewWatcher(rootPath string, cfg WatcherConfig) (*Watcher, error) {
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		config:   cfg,
		watcher:  fsWatcher,
		rootPath: rootPath,
		pending:  make(map[string]WatchEvent),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}

	return w, nil
}

// SetCallback sets the callback function for file change events.
func (w *Watcher) SetCallback(cb WatchCallback) {
	w.callback = cb
}

// Start begins watching for file system changes.
func (w *Watcher) Start(ctx context.Context) error {
	// Add root path and optionally recurse
	if err := w.addPath(w.rootPath); err != nil {
		return err
	}

	if w.config.Recursive {
		if err := w.addRecursive(w.rootPath); err != nil {
			return err
		}
	}

	// Start event processing goroutine
	go w.processEvents(ctx)

	return nil
}

// Stop stops the watcher and releases resources.
func (w *Watcher) Stop() error {
	close(w.stopCh)
	<-w.doneCh
	return w.watcher.Close()
}

// addPath adds a single path to the watcher.
func (w *Watcher) addPath(path string) error {
	// Check if path should be ignored
	relPath, err := filepath.Rel(w.rootPath, path)
	if err != nil {
		relPath = path
	}

	if w.shouldIgnore(relPath) {
		return nil
	}

	return w.watcher.Add(path)
}

// addRecursive adds all subdirectories under path.
func (w *Watcher) addRecursive(path string) error {
	return filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip files we can't access
		}

		if !info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(w.rootPath, p)
		if err != nil {
			relPath = p
		}

		if w.shouldIgnore(relPath) {
			return filepath.SkipDir
		}

		return w.watcher.Add(p)
	})
}

// shouldIgnore checks if a path matches ignore patterns.
func (w *Watcher) shouldIgnore(relPath string) bool {
	for _, pattern := range w.config.IgnorePatterns {
		// Handle directory patterns (ending with /**)
		if strings.HasSuffix(pattern, "/**") {
			dirPattern := strings.TrimSuffix(pattern, "/**")
			if strings.HasPrefix(relPath, dirPattern+string(os.PathSeparator)) || relPath == dirPattern {
				return true
			}
		}

		// Handle glob patterns
		matched, err := filepath.Match(pattern, filepath.Base(relPath))
		if err == nil && matched {
			return true
		}

		// Also try matching the full relative path
		matched, err = filepath.Match(pattern, relPath)
		if err == nil && matched {
			return true
		}
	}

	return false
}

// processEvents processes file system events with debouncing.
func (w *Watcher) processEvents(ctx context.Context) {
	defer close(w.doneCh)

	ticker := time.NewTicker(w.config.Debounce)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("watcher error: %v", err)

		case <-ticker.C:
			w.flushPending()
		}
	}
}

// handleEvent processes a single fsnotify event.
func (w *Watcher) handleEvent(event fsnotify.Event) {
	// Get relative path
	relPath, err := filepath.Rel(w.rootPath, event.Name)
	if err != nil {
		relPath = event.Name
	}

	// Check if should be ignored
	if w.shouldIgnore(relPath) {
		return
	}

	// Check file size for non-delete operations
	if event.Op&fsnotify.Remove == 0 && event.Op&fsnotify.Rename == 0 {
		info, err := os.Stat(event.Name)
		if err == nil {
			if info.IsDir() {
				// Add new directories to watch list
				if event.Op&fsnotify.Create != 0 && w.config.Recursive {
					if err := w.addRecursive(event.Name); err != nil {
						log.Printf("failed to watch new directory %s: %v", event.Name, err)
					}
				}
				return
			}
			if info.Size() > w.config.MaxFileSize {
				return
			}
		}
	}

	// Convert fsnotify op to WatchOp
	var op WatchOp
	switch {
	case event.Op&fsnotify.Create != 0:
		op = OpCreate
	case event.Op&fsnotify.Write != 0:
		op = OpWrite
	case event.Op&fsnotify.Remove != 0:
		op = OpRemove
	case event.Op&fsnotify.Rename != 0:
		op = OpRename
	default:
		return
	}

	// Add to pending events
	w.pendingMu.Lock()
	w.pending[event.Name] = WatchEvent{
		Path:      event.Name,
		Op:        op,
		Timestamp: time.Now(),
	}
	w.pendingMu.Unlock()
}

// flushPending sends all pending events to the callback.
func (w *Watcher) flushPending() {
	w.pendingMu.Lock()
	if len(w.pending) == 0 {
		w.pendingMu.Unlock()
		return
	}

	events := make([]WatchEvent, 0, len(w.pending))
	for _, e := range w.pending {
		events = append(events, e)
	}
	w.pending = make(map[string]WatchEvent)
	w.pendingMu.Unlock()

	if w.callback != nil {
		w.callback(events)
	}
}

// WatchAndIndex creates a watcher that automatically triggers reindexing.
func WatchAndIndex(ctx context.Context, indexer *Indexer, rootPath string, cfg WatcherConfig) (*Watcher, error) {
	watcher, err := NewWatcher(rootPath, cfg)
	if err != nil {
		return nil, err
	}

	watcher.SetCallback(func(events []WatchEvent) {
		// Collect unique file paths that need reindexing
		toIndex := make(map[string]struct{})
		toRemove := make(map[string]struct{})

		for _, e := range events {
			switch e.Op {
			case OpCreate, OpWrite:
				toIndex[e.Path] = struct{}{}
				delete(toRemove, e.Path)
			case OpRemove:
				toRemove[e.Path] = struct{}{}
				delete(toIndex, e.Path)
			case OpRename:
				// Treat rename as remove + create
				toRemove[e.Path] = struct{}{}
			}
		}

		// Index changed files
		if len(toIndex) > 0 {
			paths := make([]string, 0, len(toIndex))
			for p := range toIndex {
				paths = append(paths, p)
			}

			_, err := indexer.Index(ctx, rootPath, paths...)
			if err != nil {
				log.Printf("auto-reindex failed: %v", err)
			}
		}

		// Handle removed files
		// Note: File removal from index is handled by the indexer
		// when it can't find the file during next index operation
	})

	if err := watcher.Start(ctx); err != nil {
		return nil, err
	}

	return watcher, nil
}
