package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps the database connection with vecgrep-specific functionality
type DB struct {
	*sql.DB
	dimensions    int
	vectorBackend VectorBackend
}

// OpenOptions contains options for opening a database with a specific vector backend.
type OpenOptions struct {
	DBPath      string
	Dimensions  int
	BackendType VectorBackendType
	DataDir     string // For veclite path construction
}

// Open opens a database connection and initializes the schema.
// This is a convenience function that uses sqlite-vec backend by default.
// Use OpenWithBackend for explicit backend selection.
func Open(dbPath string, dimensions int) (*DB, error) {
	return OpenWithBackend(OpenOptions{
		DBPath:      dbPath,
		Dimensions:  dimensions,
		BackendType: VectorBackendSqliteVec,
	})
}

// OpenWithBackend opens a database connection with the specified vector backend.
func OpenWithBackend(opts OpenOptions) (*DB, error) {
	// Register sqlite-vec extension (needed for sqlite-vec backend)
	if opts.BackendType == VectorBackendSqliteVec || opts.BackendType == "" {
		sqlite_vec.Auto()
	}

	// Open database connection
	sqlDB, err := sql.Open("sqlite3", opts.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Enable foreign keys
	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Set WAL mode for better concurrency
	if _, err := sqlDB.Exec("PRAGMA journal_mode = WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to set WAL mode: %w", err)
	}

	// Create vector backend
	backend, err := createVectorBackend(sqlDB, opts.BackendType, opts.DataDir)
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to create vector backend: %w", err)
	}

	db := &DB{
		DB:            sqlDB,
		dimensions:    opts.Dimensions,
		vectorBackend: backend,
	}

	// Initialize schema
	if err := db.initSchema(); err != nil {
		_ = backend.Close()
		_ = sqlDB.Close()
		return nil, err
	}

	// Initialize vector backend
	if err := backend.Init(opts.Dimensions); err != nil {
		_ = backend.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("failed to initialize vector backend: %w", err)
	}

	return db, nil
}

// createVectorBackend creates the appropriate vector backend based on the type.
func createVectorBackend(sqlDB *sql.DB, backendType VectorBackendType, dataDir string) (VectorBackend, error) {
	switch backendType {
	case VectorBackendVecLite:
		if dataDir == "" {
			return nil, fmt.Errorf("dataDir is required for veclite backend")
		}
		return NewVecLiteBackend(VecLitePath(dataDir)), nil
	case VectorBackendSqliteVec, "":
		return NewSqliteVecBackend(sqlDB), nil
	default:
		return nil, fmt.Errorf("unknown vector backend type: %s", backendType)
	}
}

// initSchema creates the database tables (vector index is handled by backend)
func (db *DB) initSchema() error {
	// Create regular tables
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	// Vector backend initialization is handled separately by backend.Init()
	return nil
}

// InsertEmbedding inserts an embedding for a chunk
func (db *DB) InsertEmbedding(chunkID int64, embedding []float32) error {
	return db.vectorBackend.InsertEmbedding(chunkID, embedding)
}

// DeleteEmbedding removes an embedding for a chunk
func (db *DB) DeleteEmbedding(chunkID int64) error {
	return db.vectorBackend.DeleteEmbedding(chunkID)
}

// SearchResult represents a vector search result
type SearchResult struct {
	ChunkID  int64
	Distance float32
}

// SearchEmbeddings performs a vector similarity search
func (db *DB) SearchEmbeddings(queryEmbedding []float32, limit int) ([]SearchResult, error) {
	return db.vectorBackend.SearchEmbeddings(queryEmbedding, limit)
}

// VecVersion returns the vector backend version info
func (db *DB) VecVersion() (string, error) {
	backendType := db.vectorBackend.Type()
	if backendType == string(VectorBackendSqliteVec) {
		var version string
		err := db.QueryRow("SELECT vec_version()").Scan(&version)
		return version, err
	}
	// For other backends, return the backend type
	return backendType, nil
}

// GetEmbedding retrieves the embedding for a chunk by its ID.
func (db *DB) GetEmbedding(chunkID int64) ([]float32, error) {
	return db.vectorBackend.GetEmbedding(chunkID)
}

// GetChunkByLocation finds a chunk containing the given file path and line number.
// Returns the chunk_id of the smallest (most specific) chunk containing the line.
func (db *DB) GetChunkByLocation(filePath string, line int) (int64, error) {
	// Query for chunks that contain the specified line, ordered by size (smallest first)
	// Join chunks with files to match on relative_path or path
	var chunkID int64
	err := db.QueryRow(`
		SELECT c.id
		FROM chunks c
		JOIN files f ON c.file_id = f.id
		WHERE (f.relative_path = ? OR f.path = ?)
		  AND c.start_line <= ?
		  AND c.end_line >= ?
		ORDER BY (c.end_line - c.start_line) ASC
		LIMIT 1
	`, filePath, filePath, line, line).Scan(&chunkID)
	if err != nil {
		return 0, fmt.Errorf("get chunk at %s:%d: %w", filePath, line, err)
	}

	return chunkID, nil
}

// DeleteFile removes a file and all its chunks/embeddings from the index.
func (db *DB) DeleteFile(ctx context.Context, filePath string) (int64, error) {
	// First, get the file ID and count of chunks to be deleted
	var fileID int64
	var chunkCount int64

	err := db.QueryRowContext(ctx, `
		SELECT f.id, COUNT(c.id)
		FROM files f
		LEFT JOIN chunks c ON c.file_id = f.id
		WHERE f.relative_path = ? OR f.path = ?
		GROUP BY f.id
	`, filePath, filePath).Scan(&fileID, &chunkCount)

	if err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("file not found: %s", filePath)
		}
		return 0, fmt.Errorf("find file: %w", err)
	}

	// Get all chunk IDs for this file to delete embeddings
	rows, err := db.QueryContext(ctx, "SELECT id FROM chunks WHERE file_id = ?", fileID)
	if err != nil {
		return 0, fmt.Errorf("get chunks: %w", err)
	}
	defer rows.Close()

	var chunkIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan chunk id: %w", err)
		}
		chunkIDs = append(chunkIDs, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate chunks: %w", err)
	}

	// Delete embeddings for all chunks
	for _, chunkID := range chunkIDs {
		_ = db.DeleteEmbedding(chunkID) // Ignore errors - embedding might not exist
	}

	// Delete chunks (will cascade from file deletion, but be explicit)
	_, err = db.ExecContext(ctx, "DELETE FROM chunks WHERE file_id = ?", fileID)
	if err != nil {
		return 0, fmt.Errorf("delete chunks: %w", err)
	}

	// Delete the file
	_, err = db.ExecContext(ctx, "DELETE FROM files WHERE id = ?", fileID)
	if err != nil {
		return 0, fmt.Errorf("delete file: %w", err)
	}

	return chunkCount, nil
}

// CleanStats contains statistics from a clean operation.
type CleanStats struct {
	OrphanedChunks     int64
	OrphanedEmbeddings int64
	VacuumedBytes      int64
}

// Clean removes orphaned data and vacuums the database.
func (db *DB) Clean(ctx context.Context) (*CleanStats, error) {
	stats := &CleanStats{}

	// Find orphaned chunks (chunks without files)
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM chunks
		WHERE file_id NOT IN (SELECT id FROM files)
	`).Scan(&stats.OrphanedChunks)
	if err != nil {
		return nil, fmt.Errorf("count orphaned chunks: %w", err)
	}

	// Delete orphaned chunks
	if stats.OrphanedChunks > 0 {
		_, err = db.ExecContext(ctx, `
			DELETE FROM chunks
			WHERE file_id NOT IN (SELECT id FROM files)
		`)
		if err != nil {
			return nil, fmt.Errorf("delete orphaned chunks: %w", err)
		}
	}

	// Get all valid chunk IDs
	rows, err := db.QueryContext(ctx, "SELECT id FROM chunks")
	if err != nil {
		return nil, fmt.Errorf("get chunk ids: %w", err)
	}
	defer rows.Close()

	var validChunkIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan chunk id: %w", err)
		}
		validChunkIDs = append(validChunkIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunks: %w", err)
	}

	// Delete orphaned embeddings using the backend
	stats.OrphanedEmbeddings, err = db.vectorBackend.DeleteOrphaned(validChunkIDs)
	if err != nil {
		return nil, fmt.Errorf("delete orphaned embeddings: %w", err)
	}

	// Sync backend if needed
	if err := db.vectorBackend.Sync(); err != nil {
		return nil, fmt.Errorf("sync vector backend: %w", err)
	}

	// Get database size before vacuum
	var sizeBefore int64
	err = db.QueryRowContext(ctx, "SELECT page_count * page_size FROM pragma_page_count, pragma_page_size").Scan(&sizeBefore)
	if err != nil {
		// Ignore error, just continue
		sizeBefore = 0
	}

	// Vacuum the database
	_, err = db.ExecContext(ctx, "VACUUM")
	if err != nil {
		return nil, fmt.Errorf("vacuum database: %w", err)
	}

	// Get database size after vacuum
	var sizeAfter int64
	err = db.QueryRowContext(ctx, "SELECT page_count * page_size FROM pragma_page_count, pragma_page_size").Scan(&sizeAfter)
	if err != nil {
		// Ignore error
		sizeAfter = sizeBefore
	}

	stats.VacuumedBytes = sizeBefore - sizeAfter
	if stats.VacuumedBytes < 0 {
		stats.VacuumedBytes = 0
	}

	return stats, nil
}

// Reset clears all data for a project.
func (db *DB) Reset(ctx context.Context, projectID int64) error {
	// If projectID is 0, reset all data
	if projectID == 0 {
		// Delete all embeddings using the backend
		if err := db.vectorBackend.DeleteAll(); err != nil {
			return fmt.Errorf("delete embeddings: %w", err)
		}

		// Sync backend
		if err := db.vectorBackend.Sync(); err != nil {
			return fmt.Errorf("sync vector backend: %w", err)
		}

		// Delete all chunks
		_, err := db.ExecContext(ctx, "DELETE FROM chunks")
		if err != nil {
			return fmt.Errorf("delete chunks: %w", err)
		}

		// Delete all files
		_, err = db.ExecContext(ctx, "DELETE FROM files")
		if err != nil {
			return fmt.Errorf("delete files: %w", err)
		}

		// Delete all projects
		_, err = db.ExecContext(ctx, "DELETE FROM projects")
		if err != nil {
			return fmt.Errorf("delete projects: %w", err)
		}

		// Vacuum
		_, err = db.ExecContext(ctx, "VACUUM")
		if err != nil {
			return fmt.Errorf("vacuum database: %w", err)
		}

		return nil
	}

	// Get all file IDs for this project
	rows, err := db.QueryContext(ctx, "SELECT id FROM files WHERE project_id = ?", projectID)
	if err != nil {
		return fmt.Errorf("get files: %w", err)
	}
	defer rows.Close()

	var fileIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan file id: %w", err)
		}
		fileIDs = append(fileIDs, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate files: %w", err)
	}

	// Delete embeddings for all chunks in project files
	for _, fileID := range fileIDs {
		chunkRows, err := db.QueryContext(ctx, "SELECT id FROM chunks WHERE file_id = ?", fileID)
		if err != nil {
			return fmt.Errorf("get chunks for file %d: %w", fileID, err)
		}

		for chunkRows.Next() {
			var chunkID int64
			if err := chunkRows.Scan(&chunkID); err != nil {
				_ = chunkRows.Close()
				return fmt.Errorf("scan chunk id: %w", err)
			}
			_ = db.DeleteEmbedding(chunkID) // Ignore errors
		}
		_ = chunkRows.Close()
	}

	// Sync backend after deletions
	if err := db.vectorBackend.Sync(); err != nil {
		return fmt.Errorf("sync vector backend: %w", err)
	}

	// Delete chunks for all files in this project
	_, err = db.ExecContext(ctx, `
		DELETE FROM chunks WHERE file_id IN (
			SELECT id FROM files WHERE project_id = ?
		)
	`, projectID)
	if err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}

	// Delete files for this project
	_, err = db.ExecContext(ctx, "DELETE FROM files WHERE project_id = ?", projectID)
	if err != nil {
		return fmt.Errorf("delete files: %w", err)
	}

	// Delete the project
	_, err = db.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", projectID)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}

	return nil
}

// ResetAll clears all data from the database.
func (db *DB) ResetAll(ctx context.Context) error {
	return db.Reset(ctx, 0)
}

// Stats returns database statistics
func (db *DB) Stats() (map[string]int64, error) {
	stats := make(map[string]int64)

	queries := map[string]string{
		"projects": "SELECT COUNT(*) FROM projects",
		"files":    "SELECT COUNT(*) FROM files",
		"chunks":   "SELECT COUNT(*) FROM chunks",
	}

	for name, query := range queries {
		var count int64
		if err := db.QueryRow(query).Scan(&count); err != nil {
			return nil, err
		}
		stats[name] = count
	}

	// Vector count using the backend
	vecCount, err := db.vectorBackend.Count()
	if err != nil {
		return nil, err
	}
	stats["embeddings"] = vecCount

	return stats, nil
}

// Close closes the database and vector backend.
func (db *DB) Close() error {
	if db.vectorBackend != nil {
		if err := db.vectorBackend.Sync(); err != nil {
			// Log but continue closing
			_ = err
		}
		if err := db.vectorBackend.Close(); err != nil {
			// Log but continue closing
			_ = err
		}
	}
	return db.DB.Close()
}
