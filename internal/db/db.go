package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"fmt"
	"math"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps the database connection with vecgrep-specific functionality
type DB struct {
	*sql.DB
	dimensions int
}

// Open opens a database connection and initializes the schema
func Open(dbPath string, dimensions int) (*DB, error) {
	// Register sqlite-vec extension
	sqlite_vec.Auto()

	// Open database connection
	sqlDB, err := sql.Open("sqlite3", dbPath)
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

	db := &DB{
		DB:         sqlDB,
		dimensions: dimensions,
	}

	// Initialize schema
	if err := db.initSchema(); err != nil {
		sqlDB.Close()
		return nil, err
	}

	return db, nil
}

// initSchema creates the database tables and vector index
func (db *DB) initSchema() error {
	// Create regular tables
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}

	// Verify sqlite-vec is loaded
	var vecVersion string
	err := db.QueryRow("SELECT vec_version()").Scan(&vecVersion)
	if err != nil {
		return fmt.Errorf("sqlite-vec extension not loaded: %w", err)
	}

	// Create vector table if it doesn't exist
	// sqlite-vec uses vec0 virtual table
	createVecTable := fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
			chunk_id INTEGER PRIMARY KEY,
			embedding FLOAT[%d]
		)
	`, db.dimensions)

	if _, err := db.Exec(createVecTable); err != nil {
		return fmt.Errorf("failed to create vector table: %w", err)
	}

	return nil
}

// InsertEmbedding inserts an embedding for a chunk
func (db *DB) InsertEmbedding(chunkID int64, embedding []float32) error {
	if len(embedding) != db.dimensions {
		return fmt.Errorf("embedding dimension mismatch: got %d, expected %d", len(embedding), db.dimensions)
	}

	serialized, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("failed to serialize embedding: %w", err)
	}

	_, err = db.Exec(
		"INSERT INTO vec_chunks(chunk_id, embedding) VALUES (?, ?)",
		chunkID,
		serialized,
	)
	return err
}

// DeleteEmbedding removes an embedding for a chunk
func (db *DB) DeleteEmbedding(chunkID int64) error {
	_, err := db.Exec("DELETE FROM vec_chunks WHERE chunk_id = ?", chunkID)
	return err
}

// SearchResult represents a vector search result
type SearchResult struct {
	ChunkID  int64
	Distance float32
}

// SearchEmbeddings performs a vector similarity search
func (db *DB) SearchEmbeddings(queryEmbedding []float32, limit int) ([]SearchResult, error) {
	if len(queryEmbedding) != db.dimensions {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, expected %d", len(queryEmbedding), db.dimensions)
	}

	serialized, err := sqlite_vec.SerializeFloat32(queryEmbedding)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize query embedding: %w", err)
	}

	rows, err := db.Query(`
		SELECT chunk_id, distance
		FROM vec_chunks
		WHERE embedding MATCH ?
		ORDER BY distance
		LIMIT ?
	`, serialized, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ChunkID, &r.Distance); err != nil {
			return nil, err
		}
		results = append(results, r)
	}

	return results, rows.Err()
}

// VecVersion returns the sqlite-vec version
func (db *DB) VecVersion() (string, error) {
	var version string
	err := db.QueryRow("SELECT vec_version()").Scan(&version)
	return version, err
}

// GetEmbedding retrieves the embedding for a chunk by its ID.
func (db *DB) GetEmbedding(chunkID int64) ([]float32, error) {
	var embeddingBytes []byte
	err := db.QueryRow("SELECT embedding FROM vec_chunks WHERE chunk_id = ?", chunkID).Scan(&embeddingBytes)
	if err != nil {
		return nil, fmt.Errorf("get embedding for chunk %d: %w", chunkID, err)
	}

	// Deserialize bytes to []float32 (little-endian format)
	if len(embeddingBytes)%4 != 0 {
		return nil, fmt.Errorf("invalid embedding blob size: %d bytes", len(embeddingBytes))
	}

	embedding := make([]float32, len(embeddingBytes)/4)
	for i := range embedding {
		bits := binary.LittleEndian.Uint32(embeddingBytes[i*4 : (i+1)*4])
		embedding[i] = math.Float32frombits(bits)
	}

	return embedding, nil
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

	// Find orphaned embeddings (embeddings without chunks)
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM vec_chunks
		WHERE chunk_id NOT IN (SELECT id FROM chunks)
	`).Scan(&stats.OrphanedEmbeddings)
	if err != nil {
		return nil, fmt.Errorf("count orphaned embeddings: %w", err)
	}

	// Delete orphaned embeddings
	if stats.OrphanedEmbeddings > 0 {
		_, err = db.ExecContext(ctx, `
			DELETE FROM vec_chunks
			WHERE chunk_id NOT IN (SELECT id FROM chunks)
		`)
		if err != nil {
			return nil, fmt.Errorf("delete orphaned embeddings: %w", err)
		}
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
		// Delete all embeddings
		_, err := db.ExecContext(ctx, "DELETE FROM vec_chunks")
		if err != nil {
			return fmt.Errorf("delete embeddings: %w", err)
		}

		// Delete all chunks
		_, err = db.ExecContext(ctx, "DELETE FROM chunks")
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

	// Vector count
	var vecCount int64
	if err := db.QueryRow("SELECT COUNT(*) FROM vec_chunks").Scan(&vecCount); err != nil {
		return nil, err
	}
	stats["embeddings"] = vecCount

	return stats, nil
}
