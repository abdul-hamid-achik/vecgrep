package db

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
)

// MigrationStats contains statistics about a migration operation.
type MigrationStats struct {
	TotalEmbeddings   int64
	MigratedOK        int64
	MigrationErrors   int64
	SkippedDuplicates int64
}

// MigrateToVecLite migrates embeddings from sqlite-vec to a VecLite backend.
// This reads all embeddings from the sqlite-vec virtual table and inserts them
// into the VecLite backend.
func MigrateToVecLite(ctx context.Context, sqlDB *sql.DB, vecliteBackend *VecLiteBackend) (*MigrationStats, error) {
	stats := &MigrationStats{}

	// Count total embeddings
	err := sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM vec_chunks").Scan(&stats.TotalEmbeddings)
	if err != nil {
		return nil, fmt.Errorf("count embeddings: %w", err)
	}

	if stats.TotalEmbeddings == 0 {
		return stats, nil
	}

	// Read all embeddings from sqlite-vec
	rows, err := sqlDB.QueryContext(ctx, "SELECT chunk_id, embedding FROM vec_chunks")
	if err != nil {
		return nil, fmt.Errorf("query embeddings: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var chunkID int64
		var embeddingBytes []byte

		if err := rows.Scan(&chunkID, &embeddingBytes); err != nil {
			stats.MigrationErrors++
			continue
		}

		// Deserialize bytes to []float32 (little-endian format)
		embedding, err := deserializeEmbedding(embeddingBytes)
		if err != nil {
			stats.MigrationErrors++
			continue
		}

		// Insert into VecLite backend
		if err := vecliteBackend.InsertEmbedding(chunkID, embedding); err != nil {
			// Check if it's a duplicate
			if isDuplicateError(err) {
				stats.SkippedDuplicates++
			} else {
				stats.MigrationErrors++
			}
			continue
		}

		stats.MigratedOK++
	}

	if err := rows.Err(); err != nil {
		return stats, fmt.Errorf("iterate embeddings: %w", err)
	}

	// Sync VecLite to persist changes
	if err := vecliteBackend.Sync(); err != nil {
		return stats, fmt.Errorf("sync veclite: %w", err)
	}

	return stats, nil
}

// MigrateFromVecLite migrates embeddings from VecLite back to sqlite-vec.
// This is useful for reverting to sqlite-vec if needed.
func MigrateFromVecLite(ctx context.Context, vecliteBackend *VecLiteBackend, sqliteBackend *SqliteVecBackend) (*MigrationStats, error) {
	stats := &MigrationStats{}

	// Count total embeddings
	count, err := vecliteBackend.Count()
	if err != nil {
		return nil, fmt.Errorf("count embeddings: %w", err)
	}
	stats.TotalEmbeddings = count

	if count == 0 {
		return stats, nil
	}

	// Get all chunk IDs from VecLite
	// We'll need to search with a dummy query to get all results
	// For a proper migration, we should iterate through all records
	// This is a simplified approach - in production, you'd iterate the collection directly

	// Note: This is a limitation - VecLite doesn't expose direct iteration
	// For now, we rely on the fact that vecgrep has chunk metadata in SQLite
	// and we can reconstruct by looking at chunk IDs

	return stats, fmt.Errorf("migration from VecLite to sqlite-vec is not yet implemented")
}

// deserializeEmbedding converts sqlite-vec binary format to []float32.
func deserializeEmbedding(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("invalid embedding blob size: %d bytes", len(data))
	}

	embedding := make([]float32, len(data)/4)
	for i := range embedding {
		bits := binary.LittleEndian.Uint32(data[i*4 : (i+1)*4])
		embedding[i] = math.Float32frombits(bits)
	}

	return embedding, nil
}

// isDuplicateError checks if an error indicates a duplicate entry.
func isDuplicateError(err error) bool {
	// VecLite returns a specific error for duplicates
	// This is a simple string check - could be improved with error types
	return err != nil && (err.Error() == "record already exists" ||
		err.Error() == "duplicate key")
}

// VerifyMigration compares embeddings between two backends to verify migration.
func VerifyMigration(ctx context.Context, source, target VectorBackend, chunkIDs []int64) (int, int, error) {
	matched := 0
	mismatched := 0

	for _, chunkID := range chunkIDs {
		sourceEmb, err := source.GetEmbedding(chunkID)
		if err != nil {
			mismatched++
			continue
		}

		targetEmb, err := target.GetEmbedding(chunkID)
		if err != nil {
			mismatched++
			continue
		}

		if embeddingsEqual(sourceEmb, targetEmb) {
			matched++
		} else {
			mismatched++
		}
	}

	return matched, mismatched, nil
}

// embeddingsEqual checks if two embeddings are approximately equal.
func embeddingsEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}

	const epsilon = 1e-6
	for i := range a {
		diff := a[i] - b[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > epsilon {
			return false
		}
	}

	return true
}
