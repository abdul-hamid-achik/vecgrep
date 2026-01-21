package db

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
)

// SqliteVecBackend implements VectorBackend using sqlite-vec.
type SqliteVecBackend struct {
	db         *sql.DB
	dimensions int
}

// NewSqliteVecBackend creates a new sqlite-vec backend.
// The sql.DB must already have sqlite-vec extension loaded.
func NewSqliteVecBackend(db *sql.DB) *SqliteVecBackend {
	return &SqliteVecBackend{
		db: db,
	}
}

// Init initializes the sqlite-vec backend with the given dimensions.
func (b *SqliteVecBackend) Init(dimensions int) error {
	b.dimensions = dimensions

	// Verify sqlite-vec is loaded
	var vecVersion string
	err := b.db.QueryRow("SELECT vec_version()").Scan(&vecVersion)
	if err != nil {
		return fmt.Errorf("sqlite-vec extension not loaded: %w", err)
	}

	// Create vector table if it doesn't exist
	createVecTable := fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
			chunk_id INTEGER PRIMARY KEY,
			embedding FLOAT[%d]
		)
	`, dimensions)

	if _, err := b.db.Exec(createVecTable); err != nil {
		return fmt.Errorf("failed to create vector table: %w", err)
	}

	return nil
}

// InsertEmbedding inserts an embedding for a chunk.
func (b *SqliteVecBackend) InsertEmbedding(chunkID int64, embedding []float32) error {
	if len(embedding) != b.dimensions {
		return fmt.Errorf("embedding dimension mismatch: got %d, expected %d", len(embedding), b.dimensions)
	}

	serialized, err := sqlite_vec.SerializeFloat32(embedding)
	if err != nil {
		return fmt.Errorf("failed to serialize embedding: %w", err)
	}

	_, err = b.db.Exec(
		"INSERT INTO vec_chunks(chunk_id, embedding) VALUES (?, ?)",
		chunkID,
		serialized,
	)
	return err
}

// DeleteEmbedding removes an embedding for a chunk.
func (b *SqliteVecBackend) DeleteEmbedding(chunkID int64) error {
	_, err := b.db.Exec("DELETE FROM vec_chunks WHERE chunk_id = ?", chunkID)
	return err
}

// SearchEmbeddings performs a vector similarity search.
func (b *SqliteVecBackend) SearchEmbeddings(queryEmbedding []float32, limit int) ([]SearchResult, error) {
	if len(queryEmbedding) != b.dimensions {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, expected %d", len(queryEmbedding), b.dimensions)
	}

	serialized, err := sqlite_vec.SerializeFloat32(queryEmbedding)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize query embedding: %w", err)
	}

	rows, err := b.db.Query(`
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

// GetEmbedding retrieves the embedding for a chunk by its ID.
func (b *SqliteVecBackend) GetEmbedding(chunkID int64) ([]float32, error) {
	var embeddingBytes []byte
	err := b.db.QueryRow("SELECT embedding FROM vec_chunks WHERE chunk_id = ?", chunkID).Scan(&embeddingBytes)
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

// Count returns the number of embeddings stored.
func (b *SqliteVecBackend) Count() (int64, error) {
	var count int64
	err := b.db.QueryRow("SELECT COUNT(*) FROM vec_chunks").Scan(&count)
	return count, err
}

// DeleteAll removes all embeddings.
func (b *SqliteVecBackend) DeleteAll() error {
	_, err := b.db.Exec("DELETE FROM vec_chunks")
	return err
}

// DeleteOrphaned removes embeddings that don't have corresponding chunks.
func (b *SqliteVecBackend) DeleteOrphaned(validChunkIDs []int64) (int64, error) {
	// For sqlite-vec, we need to query all chunk_ids and delete those not in the valid list
	rows, err := b.db.Query("SELECT chunk_id FROM vec_chunks")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	// Build map of valid IDs for fast lookup
	validMap := make(map[int64]bool, len(validChunkIDs))
	for _, id := range validChunkIDs {
		validMap[id] = true
	}

	var toDelete []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		if !validMap[id] {
			toDelete = append(toDelete, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Delete orphaned embeddings
	for _, id := range toDelete {
		if _, err := b.db.Exec("DELETE FROM vec_chunks WHERE chunk_id = ?", id); err != nil {
			return 0, err
		}
	}

	return int64(len(toDelete)), nil
}

// Sync is a no-op for sqlite-vec (uses SQLite's own sync).
func (b *SqliteVecBackend) Sync() error {
	return nil
}

// Close is a no-op for sqlite-vec (DB is managed externally).
func (b *SqliteVecBackend) Close() error {
	return nil
}

// Type returns "sqlite-vec".
func (b *SqliteVecBackend) Type() string {
	return string(VectorBackendSqliteVec)
}

// Ensure SqliteVecBackend implements VectorBackend.
var _ VectorBackend = (*SqliteVecBackend)(nil)
