package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"unicode/utf8"
)

// StructuralChunkSource is the port used by the indexer to obtain optional
// symbol-bounded chunks. Implementations live outside this package; the
// indexer never opens codemap's database or imports its packages.
type StructuralChunkSource interface {
	LoadStructuralChunks(context.Context, string) (*StructuralChunkSet, error)
}

// StructuralChunkSet is one validated, internally-consistent structural
// snapshot. Files absent from Files continue through vecgrep's built-in
// chunker so unsupported languages and files without symbols are not lost.
type StructuralChunkSet struct {
	ProjectKey       string
	IndexFingerprint string
	// Complete distinguishes a valid snapshot containing zero symbols from an
	// unavailable/partially-decoded source. A complete empty snapshot is useful:
	// files without exported symbols must continue through the built-in chunker.
	Complete bool
	Files    map[string]StructuralFileChunks
	// Issues contains file-scoped validation failures. Files with issues are
	// intentionally absent from Files and use vecgrep's built-in chunker in
	// auto mode. Required mode promotes the issues to a hard failure.
	Issues []error
}

// StructuralFileChunks holds chunks for one source file. FileHash is the
// SHA-256 of the complete current file at export-consumption time. It protects
// against a file changing between the external export and vecgrep's own walk.
// ProfileHash changes when symbol boundaries or embedding enrichment change.
type StructuralFileChunks struct {
	FileHash    string
	FileSize    int64
	ProfileHash string
	Chunks      []Chunk
}

// StructuralChunkSourceSnapshot is an opaque, exact snapshot of an Indexer's
// structural source configuration. It lets callers apply a temporary override
// and restore the original source object without resolving or reconstructing
// external dependencies a second time.
type StructuralChunkSourceSnapshot struct {
	source   StructuralChunkSource
	required bool
}

// SetStructuralChunkSource configures the optional source used at the start of
// every index run. When required is false, source errors become warnings and
// the run falls back to vecgrep's built-in chunker. A required source fails
// before a full reindex resets existing data.
func (idx *Indexer) SetStructuralChunkSource(source StructuralChunkSource, required bool) {
	idx.structuralMu.Lock()
	defer idx.structuralMu.Unlock()
	idx.structuralSource = source
	idx.structuralRequired = required
}

// SnapshotStructuralChunkSource atomically captures the current structural
// source and required flag. The returned value is safe to retain while another
// goroutine updates the Indexer's configuration.
func (idx *Indexer) SnapshotStructuralChunkSource() StructuralChunkSourceSnapshot {
	idx.structuralMu.RLock()
	defer idx.structuralMu.RUnlock()
	return StructuralChunkSourceSnapshot{
		source:   idx.structuralSource,
		required: idx.structuralRequired,
	}
}

// RestoreStructuralChunkSource atomically restores an exact prior snapshot.
func (idx *Indexer) RestoreStructuralChunkSource(snapshot StructuralChunkSourceSnapshot) {
	idx.structuralMu.Lock()
	defer idx.structuralMu.Unlock()
	idx.structuralSource = snapshot.source
	idx.structuralRequired = snapshot.required
}

func (idx *Indexer) loadStructuralChunks(ctx context.Context, projectRoot string) (*StructuralChunkSet, error, error) {
	return idx.loadStructuralChunksWithSnapshot(ctx, projectRoot, idx.SnapshotStructuralChunkSource())
}

func (idx *Indexer) loadStructuralChunksWithSnapshot(ctx context.Context, projectRoot string, snapshot StructuralChunkSourceSnapshot) (*StructuralChunkSet, error, error) {
	if snapshot.source == nil {
		return nil, nil, nil
	}
	set, err := snapshot.source.LoadStructuralChunks(ctx, projectRoot)
	if err == nil && set == nil {
		err = fmt.Errorf("structural export returned no snapshot")
	}
	if err == nil && !set.Complete {
		err = fmt.Errorf("structural export snapshot is incomplete")
	}
	if err == nil && len(set.Issues) > 0 {
		err = errors.Join(set.Issues...)
		if snapshot.required {
			return nil, nil, fmt.Errorf("load codemap structural chunks: %w", err)
		}
		if len(set.Files) > 0 {
			return set, fmt.Errorf("codemap structural chunks partially unavailable: %w; using built-in chunker for affected files", err), nil
		}
	}
	if err == nil {
		return set, nil, nil
	}
	wrapped := fmt.Errorf("load codemap structural chunks: %w", err)
	if snapshot.required {
		return nil, nil, wrapped
	}
	return nil, fmt.Errorf("%w; using built-in chunker", wrapped), nil
}

func structuralIndexHash(fileHash string, file StructuralFileChunks) string {
	h := sha256.New()
	_, _ = h.Write([]byte("vecgrep-codemap-structural-v1\x00"))
	_, _ = h.Write([]byte(fileHash))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(file.ProfileHash))
	return hex.EncodeToString(h.Sum(nil))
}

func structuralFileForHash(set *StructuralChunkSet, relativePath, fileHash string) (StructuralFileChunks, bool) {
	if set == nil {
		return StructuralFileChunks{}, false
	}
	file, ok := set.Files[relativePath]
	if !ok || file.FileHash == "" || file.FileHash != fileHash || len(file.Chunks) == 0 {
		return StructuralFileChunks{}, false
	}
	return file, true
}

func embeddingContent(chunk Chunk) string {
	content := chunk.Content
	if chunk.EmbeddingContent != "" {
		content = chunk.EmbeddingContent
	}
	if len(content) <= defaultMaxChunkChars {
		return content
	}
	end := defaultMaxChunkChars
	for end > 0 && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[:end]
}
