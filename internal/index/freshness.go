package index

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"

	gitignore "github.com/sabhiram/go-gitignore"
)

// GetRawPendingChanges compares the current working tree with the raw source
// hashes persisted alongside vecgrep chunks. Unlike GetPendingChanges, this
// status-only path never asks a StructuralChunkSource to load the paginated
// codemap export. complete is false for legacy indexes that predate source
// hashes; callers must then report freshness as unknown.
func (idx *Indexer) GetRawPendingChanges(ctx context.Context, projectRoot string) (*PendingChanges, bool, error) {
	if idx == nil || idx.db == nil {
		return nil, false, fmt.Errorf("indexer database is nil")
	}
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, false, fmt.Errorf("abs path: %w", err)
	}

	indexedFiles, complete, err := idx.db.GetSourceHashes(absRoot)
	if err != nil {
		return nil, false, fmt.Errorf("get source hashes: %w", err)
	}
	if !complete {
		return nil, false, nil
	}

	ignoreMatcher, err := idx.buildIgnoreMatcher(projectRoot)
	if err != nil {
		return nil, false, fmt.Errorf("build ignore matcher: %w", err)
	}
	currentFileHashes, err := idx.scanRawFileHashes(ctx, absRoot, ignoreMatcher)
	if err != nil {
		return nil, false, fmt.Errorf("scan raw file hashes: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	pending := &PendingChanges{}
	for relativePath, sourceHash := range currentFileHashes {
		indexedHash, exists := indexedFiles[relativePath]
		switch {
		case !exists:
			pending.NewFiles++
		case indexedHash != sourceHash:
			pending.ModifiedFiles++
		}
	}
	for relativePath := range indexedFiles {
		if _, exists := currentFileHashes[relativePath]; !exists {
			pending.DeletedFiles++
		}
	}
	pending.TotalPending = pending.NewFiles + pending.ModifiedFiles + pending.DeletedFiles
	return pending, true, nil
}

// scanRawFileHashes walks the same full-project filesystem scope as Index but
// retains only relative path -> raw hash metadata. Each source body is read,
// classified, and released inside one WalkDir callback, so memory is O(largest
// candidate file) rather than O(total project bytes) as it was through
// collectFiles.
//
// Binary, empty, and whitespace-only files cannot produce a persisted chunk or
// file_hash in chunkFile, so they are absent from this filesystem projection.
// If one previously had metadata, the normal indexedFiles comparison reports
// it deleted until Index removes the stale record; after a successful index it
// no longer creates perpetual "new" drift.
func (idx *Indexer) scanRawFileHashes(ctx context.Context, absRoot string, ignore *gitignore.GitIgnore) (map[string]string, error) {
	hashes := make(map[string]string)
	err := filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relativePath, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		if ignore.MatchesPath(relativePath) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, skip, err := indexableFileInfo(path, entry)
		if err != nil {
			return err
		}
		if skip {
			return nil
		}
		if info.Size() > idx.config.MaxFileSize {
			return nil
		}
		hash, content, err := hashFile(path)
		if err != nil {
			return err
		}
		// Match the index walk's second size check: the file may grow between
		// DirEntry.Info and ReadFile.
		if int64(len(content)) > idx.config.MaxFileSize {
			return nil
		}
		if !isChunkEligibleContent(content) {
			return nil
		}
		hashes[relativePath] = hash
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hashes, nil
}
