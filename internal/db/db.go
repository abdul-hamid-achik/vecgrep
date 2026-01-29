package db

import (
	"context"
	"fmt"
	"time"
)

// DB wraps the veclite database with vecgrep-specific functionality.
// All data is stored in veclite - no SQLite needed.
type DB struct {
	backend    *VecLiteBackend
	dimensions int
	dataDir    string
}

// OpenOptions contains options for opening a database.
type OpenOptions struct {
	Dimensions int
	DataDir    string // Directory containing vectors.veclite
}

// Open opens a database connection and initializes the veclite backend.
func Open(dbPath string, dimensions int, dataDir string) (*DB, error) {
	return OpenWithOptions(OpenOptions{
		Dimensions: dimensions,
		DataDir:    dataDir,
	})
}

// OpenWithOptions opens a database connection with the specified options.
func OpenWithOptions(opts OpenOptions) (*DB, error) {
	if opts.DataDir == "" {
		return nil, fmt.Errorf("dataDir is required")
	}

	// Create veclite backend
	backend := NewVecLiteBackend(VecLitePath(opts.DataDir))

	// Initialize backend
	if err := backend.Init(opts.Dimensions); err != nil {
		return nil, fmt.Errorf("failed to initialize veclite: %w", err)
	}

	return &DB{
		backend:    backend,
		dimensions: opts.Dimensions,
		dataDir:    opts.DataDir,
	}, nil
}

// Backend returns the underlying VecLiteBackend for direct access.
func (db *DB) Backend() *VecLiteBackend {
	return db.backend
}

// InsertChunk inserts a chunk with all its metadata and embedding.
func (db *DB) InsertChunk(chunk ChunkRecord, embedding []float32) (uint64, error) {
	return db.backend.InsertChunk(chunk, embedding)
}

// InsertChunkBatch inserts multiple chunks in a single batch operation.
// This is more efficient than individual inserts for bulk indexing.
func (db *DB) InsertChunkBatch(chunks []ChunkRecord, embeddings [][]float32) ([]uint64, error) {
	return db.backend.InsertChunkBatch(chunks, embeddings)
}

// UpsertChunk inserts or updates a chunk using a unique key.
// Returns the ID and whether it was a new insert (true) or update (false).
func (db *DB) UpsertChunk(chunk ChunkRecord, embedding []float32) (uint64, bool, error) {
	return db.backend.UpsertChunk(chunk, embedding)
}

// InsertEmbedding inserts an embedding (legacy compatibility).
// Deprecated: Use InsertChunk for full metadata storage.
func (db *DB) InsertEmbedding(chunkID int64, embedding []float32) error {
	return db.backend.InsertEmbedding(chunkID, embedding)
}

// DeleteEmbedding removes an embedding for a chunk.
func (db *DB) DeleteEmbedding(chunkID int64) error {
	return db.backend.DeleteEmbedding(chunkID)
}

// SearchEmbeddings performs a vector similarity search.
func (db *DB) SearchEmbeddings(queryEmbedding []float32, limit int) ([]SearchResult, error) {
	return db.backend.SearchEmbeddings(queryEmbedding, limit)
}

// SearchWithFilter performs a filtered vector search using native veclite filters.
func (db *DB) SearchWithFilter(queryEmbedding []float32, limit int, opts FilterOptions) ([]SearchResult, error) {
	return db.backend.SearchWithFilter(queryEmbedding, limit, opts)
}

// SearchWithExplain performs a search and returns diagnostic information.
func (db *DB) SearchWithExplain(queryEmbedding []float32, limit int, opts FilterOptions) ([]SearchResult, *SearchExplanation, error) {
	return db.backend.SearchWithExplain(queryEmbedding, limit, opts)
}

// TextSearch performs a keyword-based search on content.
func (db *DB) TextSearch(query string, limit int, opts FilterOptions) ([]SearchResult, error) {
	return db.backend.TextSearch(query, limit, opts)
}

// HybridSearch combines vector search with text filtering.
// vectorWeight controls the influence of vector similarity (0-1).
func (db *DB) HybridSearch(queryEmbedding []float32, textQuery string, limit int, opts FilterOptions, vectorWeight float32) ([]SearchResult, error) {
	return db.backend.HybridSearch(queryEmbedding, textQuery, limit, opts, vectorWeight)
}

// VecVersion returns the vector backend version info.
func (db *DB) VecVersion() (string, error) {
	return db.backend.Type(), nil
}

// GetEmbedding retrieves the embedding for a chunk by its ID.
func (db *DB) GetEmbedding(chunkID int64) ([]float32, error) {
	return db.backend.GetEmbedding(chunkID)
}

// GetChunkByLocation finds a chunk containing the given file path and line number.
func (db *DB) GetChunkByLocation(filePath string, line int) (*ChunkRecord, error) {
	return db.backend.GetChunkByLocation(filePath, line)
}

// GetChunksByFile returns all chunks for a specific file.
func (db *DB) GetChunksByFile(filePath string) ([]ChunkRecord, error) {
	return db.backend.GetChunksByFile(filePath)
}

// DeleteFile removes a file and all its chunks from the index.
func (db *DB) DeleteFile(ctx context.Context, filePath string) (int64, error) {
	return db.backend.DeleteByFilePath(filePath)
}

// GetFileHashes returns file hashes for incremental indexing.
func (db *DB) GetFileHashes(projectRoot string) (map[string]string, error) {
	return db.backend.GetFileHashes(projectRoot)
}

// GetFileHash returns the hash of an indexed file.
func (db *DB) GetFileHash(relPath string) string {
	return db.backend.GetFileHash(relPath)
}

// HasFile checks if a file is indexed.
func (db *DB) HasFile(relPath string) bool {
	return db.backend.HasFile(relPath)
}

// ListFiles returns all unique files in the index.
func (db *DB) ListFiles(projectRoot string) ([]FileInfo, error) {
	return db.backend.ListFiles(projectRoot)
}

// CleanStats contains statistics from a clean operation.
type CleanStats struct {
	OrphanedChunks     int64
	OrphanedEmbeddings int64
	VacuumedBytes      int64
}

// Clean removes orphaned data.
// With veclite-only storage, this is largely a no-op since all data is self-contained.
func (db *DB) Clean(ctx context.Context) (*CleanStats, error) {
	// With veclite-only storage, there are no orphans to clean
	// Just sync to ensure consistency
	if err := db.backend.Sync(); err != nil {
		return nil, fmt.Errorf("sync failed: %w", err)
	}

	return &CleanStats{
		OrphanedChunks:     0,
		OrphanedEmbeddings: 0,
		VacuumedBytes:      0,
	}, nil
}

// Reset clears all data for a project.
func (db *DB) Reset(ctx context.Context, projectRoot string) error {
	if projectRoot == "" {
		return db.ResetAll(ctx)
	}

	_, err := db.backend.DeleteByProjectRoot(projectRoot)
	if err != nil {
		return fmt.Errorf("delete project data: %w", err)
	}

	return db.backend.Sync()
}

// ResetAll clears all data from the database.
func (db *DB) ResetAll(ctx context.Context) error {
	if err := db.backend.DeleteAll(); err != nil {
		return fmt.Errorf("delete all: %w", err)
	}

	return db.backend.Sync()
}

// Stats returns database statistics.
func (db *DB) Stats() (map[string]int64, error) {
	return db.StatsForProject("")
}

// StatsForProject returns database statistics for a specific project.
func (db *DB) StatsForProject(projectRoot string) (map[string]int64, error) {
	stats, err := db.backend.GetStats(projectRoot)
	if err != nil {
		return nil, err
	}

	return map[string]int64{
		"projects":   stats.TotalProjects,
		"files":      stats.TotalFiles,
		"chunks":     stats.TotalChunks,
		"embeddings": stats.TotalChunks, // Same as chunks with veclite-only
	}, nil
}

// GetDetailedStats returns detailed statistics including language/chunk type distribution.
func (db *DB) GetDetailedStats(projectRoot string) (*Stats, error) {
	return db.backend.GetStats(projectRoot)
}

// Close closes the database.
func (db *DB) Close() error {
	if db.backend != nil {
		if err := db.backend.Sync(); err != nil {
			// Log but continue closing
			_ = err
		}
		return db.backend.Close()
	}
	return nil
}

// Sync persists any pending changes.
func (db *DB) Sync() error {
	return db.backend.Sync()
}

// Dimensions returns the embedding dimensions.
func (db *DB) Dimensions() int {
	return db.dimensions
}

// DataDir returns the data directory path.
func (db *DB) DataDir() string {
	return db.dataDir
}

// NewChunkRecord creates a new ChunkRecord with the given parameters.
func NewChunkRecord(
	filePath, relativePath, fileHash string,
	fileSize int64,
	language string,
	content string,
	startLine, endLine, startByte, endByte int,
	chunkType, symbolName string,
	projectRoot string,
) ChunkRecord {
	return ChunkRecord{
		FilePath:     filePath,
		RelativePath: relativePath,
		FileHash:     fileHash,
		FileSize:     fileSize,
		Language:     language,
		Content:      content,
		StartLine:    startLine,
		EndLine:      endLine,
		StartByte:    startByte,
		EndByte:      endByte,
		ChunkType:    chunkType,
		SymbolName:   symbolName,
		ProjectRoot:  projectRoot,
		IndexedAt:    time.Now(),
	}
}
