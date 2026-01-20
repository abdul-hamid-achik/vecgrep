package db

import (
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
