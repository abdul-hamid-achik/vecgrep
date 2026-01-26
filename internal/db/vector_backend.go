package db

// VectorBackend is the interface for vector storage and search.
// With the veclite-only architecture, this interface is primarily
// for backwards compatibility and potential future backends.
type VectorBackend interface {
	// Init initializes the vector backend with the given dimensions.
	Init(dimensions int) error

	// InsertEmbedding inserts an embedding for a chunk (legacy).
	InsertEmbedding(chunkID int64, embedding []float32) error

	// DeleteEmbedding removes an embedding for a chunk.
	DeleteEmbedding(chunkID int64) error

	// SearchEmbeddings performs a vector similarity search.
	SearchEmbeddings(queryEmbedding []float32, limit int) ([]SearchResult, error)

	// GetEmbedding retrieves the embedding for a chunk by its ID.
	GetEmbedding(chunkID int64) ([]float32, error)

	// Count returns the number of embeddings stored.
	Count() (int64, error)

	// DeleteAll removes all embeddings.
	DeleteAll() error

	// DeleteOrphaned removes embeddings that don't have corresponding chunks.
	DeleteOrphaned(chunkIDs []int64) (int64, error)

	// Sync persists any pending changes (for backends that buffer writes).
	Sync() error

	// Close closes the backend and releases resources.
	Close() error

	// Type returns the backend type name.
	Type() string
}

// VectorBackendType represents the type of vector backend.
type VectorBackendType string

const (
	// VectorBackendVecLite uses VecLite as the vector backend.
	VectorBackendVecLite VectorBackendType = "veclite"
)
