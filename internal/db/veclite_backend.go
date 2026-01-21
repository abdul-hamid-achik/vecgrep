package db

import (
	"fmt"
	"path/filepath"

	"github.com/abdul-hamid-achik/veclite"
)

// VecLiteBackend implements VectorBackend using VecLite with HNSW indexing.
type VecLiteBackend struct {
	db         *veclite.DB
	coll       *veclite.Collection
	dbPath     string
	dimensions int
}

// NewVecLiteBackend creates a new VecLite backend.
// The dbPath should point to the VecLite database file.
func NewVecLiteBackend(dbPath string) *VecLiteBackend {
	return &VecLiteBackend{
		dbPath: dbPath,
	}
}

// Init initializes the VecLite backend with the given dimensions.
func (b *VecLiteBackend) Init(dimensions int) error {
	b.dimensions = dimensions

	// Open VecLite database
	db, err := veclite.Open(b.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open veclite database: %w", err)
	}
	b.db = db

	// Create or get collection with HNSW index
	coll, err := db.CreateCollection("chunks",
		veclite.WithDimension(dimensions),
		veclite.WithDistanceType(veclite.DistanceEuclidean), // Match sqlite-vec behavior
		veclite.WithHNSW(16, 200),                           // M=16, efConstruction=200
	)
	if err != nil {
		// Collection might already exist, try to get it
		coll, err = db.GetCollection("chunks")
		if err != nil {
			return fmt.Errorf("failed to create/get collection: %w", err)
		}
	}
	b.coll = coll

	return nil
}

// InsertEmbedding inserts an embedding for a chunk.
func (b *VecLiteBackend) InsertEmbedding(chunkID int64, embedding []float32) error {
	if len(embedding) != b.dimensions {
		return fmt.Errorf("embedding dimension mismatch: got %d, expected %d", len(embedding), b.dimensions)
	}

	// Use chunk_id as the payload to track the association
	_, err := b.coll.Insert(embedding, map[string]any{"chunk_id": chunkID})
	return err
}

// DeleteEmbedding removes an embedding for a chunk.
func (b *VecLiteBackend) DeleteEmbedding(chunkID int64) error {
	_, err := b.coll.DeleteWhere(veclite.Equal("chunk_id", chunkID))
	return err
}

// SearchEmbeddings performs a vector similarity search.
func (b *VecLiteBackend) SearchEmbeddings(queryEmbedding []float32, limit int) ([]SearchResult, error) {
	if len(queryEmbedding) != b.dimensions {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, expected %d", len(queryEmbedding), b.dimensions)
	}

	results, err := b.coll.Search(queryEmbedding, veclite.TopK(limit))
	if err != nil {
		return nil, err
	}

	// Convert VecLite results to SearchResult
	searchResults := make([]SearchResult, len(results))
	for i, r := range results {
		chunkID, ok := r.Record.Payload["chunk_id"].(int64)
		if !ok {
			// Try other numeric types
			switch v := r.Record.Payload["chunk_id"].(type) {
			case int:
				chunkID = int64(v)
			case float64:
				chunkID = int64(v)
			default:
				continue
			}
		}
		searchResults[i] = SearchResult{
			ChunkID:  chunkID,
			Distance: r.Score,
		}
	}

	return searchResults, nil
}

// GetEmbedding retrieves the embedding for a chunk by its ID.
func (b *VecLiteBackend) GetEmbedding(chunkID int64) ([]float32, error) {
	records, err := b.coll.Find(veclite.Equal("chunk_id", chunkID))
	if err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("embedding not found for chunk %d", chunkID)
	}

	return records[0].Vector, nil
}

// Count returns the number of embeddings stored.
func (b *VecLiteBackend) Count() (int64, error) {
	return int64(b.coll.Count()), nil
}

// DeleteAll removes all embeddings.
func (b *VecLiteBackend) DeleteAll() error {
	b.coll.Clear()
	return nil
}

// DeleteOrphaned removes embeddings that don't have corresponding chunks.
func (b *VecLiteBackend) DeleteOrphaned(validChunkIDs []int64) (int64, error) {
	// Build map of valid IDs
	validMap := make(map[int64]bool, len(validChunkIDs))
	for _, id := range validChunkIDs {
		validMap[id] = true
	}

	// Get all records
	allRecords := b.coll.All()

	var deleted int64
	for _, r := range allRecords {
		chunkID, ok := r.Payload["chunk_id"].(int64)
		if !ok {
			switch v := r.Payload["chunk_id"].(type) {
			case int:
				chunkID = int64(v)
			case float64:
				chunkID = int64(v)
			default:
				continue
			}
		}

		if !validMap[chunkID] {
			if err := b.coll.Delete(r.ID); err == nil {
				deleted++
			}
		}
	}

	return deleted, nil
}

// Sync persists any pending changes.
func (b *VecLiteBackend) Sync() error {
	return b.db.Sync()
}

// Close closes the VecLite database.
func (b *VecLiteBackend) Close() error {
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// Type returns "veclite".
func (b *VecLiteBackend) Type() string {
	return string(VectorBackendVecLite)
}

// VecLitePath returns the path to the VecLite database file.
func VecLitePath(dataDir string) string {
	return filepath.Join(dataDir, "vectors.veclite")
}

// Ensure VecLiteBackend implements VectorBackend.
var _ VectorBackend = (*VecLiteBackend)(nil)
