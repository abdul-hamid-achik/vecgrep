// Package embed provides embedding generation for semantic search.
package embed

import (
	"context"
	"errors"
	"fmt"
)

// Common errors for embedding providers.
var (
	ErrProviderUnavailable = errors.New("embedding provider unavailable")
	ErrModelNotFound       = errors.New("embedding model not found")
	ErrInvalidInput        = errors.New("invalid input for embedding")
	ErrEmptyText           = errors.New("cannot embed empty text")
	ErrContextCanceled     = errors.New("embedding operation canceled")
	ErrRateLimited         = errors.New("rate limited by embedding provider")
	ErrDimensionMismatch   = errors.New("embedding dimension mismatch")
)

// Provider defines the interface for embedding backends.
type Provider interface {
	// Embed generates an embedding vector for a single text.
	Embed(ctx context.Context, text string) ([]float32, error)

	// EmbedBatch generates embedding vectors for multiple texts.
	// Returns embeddings in the same order as input texts.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)

	// Model returns the name of the embedding model being used.
	Model() string

	// Dimensions returns the dimensionality of the embedding vectors.
	Dimensions() int

	// Ping checks if the provider is available and the model is loaded.
	Ping(ctx context.Context) error
}

// ProviderError wraps errors with provider context.
type ProviderError struct {
	Provider string
	Op       string
	Err      error
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: %s: %v", e.Provider, e.Op, e.Err)
}

func (e *ProviderError) Unwrap() error {
	return e.Err
}

// NewProviderError creates a new ProviderError.
func NewProviderError(provider, op string, err error) error {
	return &ProviderError{
		Provider: provider,
		Op:       op,
		Err:      err,
	}
}

// EmbeddingResult holds an embedding with metadata.
type EmbeddingResult struct {
	Embedding  []float32
	TokenCount int
	Truncated  bool
}

// BatchResult holds results from a batch embedding operation.
type BatchResult struct {
	Embeddings [][]float32
	Errors     []error
	Successful int
	Failed     int
}
