// Package search provides semantic search functionality.
package search

import (
	"context"
	"fmt"
)

// commonQueries contains common search queries to pre-warm.
// These represent typical development-related searches.
var commonQueries = []string{
	// Common code concepts
	"error handling",
	"main function",
	"configuration",
	"database connection",
	"authentication",
	"API endpoint",
	"test",
	"import",
	"struct definition",
	"interface",

	// Error patterns
	"handle error",
	"return error",
	"error message",

	// Function patterns
	"function definition",
	"helper function",
	"utility function",

	// Configuration
	"config file",
	"environment variable",
	"settings",

	// Data patterns
	"parse JSON",
	"read file",
	"write file",

	// HTTP/API
	"HTTP handler",
	"REST API",
	"request handler",
	"response",

	// Testing
	"unit test",
	"test case",
	"mock",
}

// Warmup pre-generates embeddings for common queries.
// This should be called in the background during initialization
// to improve response time for common searches.
func (s *Searcher) Warmup(ctx context.Context) error {
	// Check if provider is available
	if s.provider == nil {
		return fmt.Errorf("embedding provider not initialized")
	}

	// Generate embeddings for common queries
	// We do this one at a time to avoid overwhelming the provider
	for _, query := range commonQueries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Generate embedding (this will be cached by CachedProvider)
			_, err := s.provider.Embed(ctx, query)
			if err != nil {
				// Log but continue with other queries
				continue
			}
		}
	}

	return nil
}

// WarmupBatch pre-generates embeddings for common queries using batch processing.
// This is more efficient than Warmup() when the provider supports batching.
func (s *Searcher) WarmupBatch(ctx context.Context) error {
	if s.provider == nil {
		return fmt.Errorf("embedding provider not initialized")
	}

	// Generate all embeddings in a single batch
	_, err := s.provider.EmbedBatch(ctx, commonQueries)
	return err
}

// WarmupCustom pre-generates embeddings for custom queries.
// Use this to warm up domain-specific queries.
func (s *Searcher) WarmupCustom(ctx context.Context, queries []string) error {
	if s.provider == nil {
		return fmt.Errorf("embedding provider not initialized")
	}

	if len(queries) == 0 {
		return nil
	}

	_, err := s.provider.EmbedBatch(ctx, queries)
	return err
}

// CommonQueries returns the list of common queries used for warmup.
// This can be useful for debugging or extending the warmup list.
func CommonQueries() []string {
	result := make([]string, len(commonQueries))
	copy(result, commonQueries)
	return result
}
