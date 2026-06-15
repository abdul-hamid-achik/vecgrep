// Package source provides abstractions for different content sources.
package source

import (
	"context"
)

// Document represents a document from any source.
type Document struct {
	// ID is a unique identifier for the document within its source.
	ID string `json:"id"`

	// Path is the file path or URI for the document.
	Path string `json:"path"`

	// Content is the full text content of the document.
	Content string `json:"content"`

	// Language is the programming language or document type (e.g., "go", "markdown").
	Language string `json:"language"`

	// Title is an optional human-readable title for the document.
	Title string `json:"title,omitempty"`

	// Metadata holds additional source-specific information.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Source defines the interface for content sources.
// Sources can be files on disk, note-taking apps, databases, etc.
type Source interface {
	// Name returns the unique name of this source.
	Name() string

	// List returns all documents from this source.
	List(ctx context.Context) ([]Document, error)

	// Watch watches for changes and calls onChange when documents are added/modified/deleted.
	// Returns immediately; watching happens in a goroutine.
	// Returns nil if watching is not supported.
	Watch(ctx context.Context, onChange func([]Document)) error
}

// SourceRegistry holds registered sources.
type SourceRegistry struct {
	sources map[string]Source
}

// NewSourceRegistry creates a new source registry.
func NewSourceRegistry() *SourceRegistry {
	return &SourceRegistry{
		sources: make(map[string]Source),
	}
}

// Register adds a source to the registry.
func (r *SourceRegistry) Register(source Source) {
	r.sources[source.Name()] = source
}

// Get returns a source by name.
func (r *SourceRegistry) Get(name string) (Source, bool) {
	s, ok := r.sources[name]
	return s, ok
}

// List returns all registered source names.
func (r *SourceRegistry) List() []string {
	names := make([]string, 0, len(r.sources))
	for name := range r.sources {
		names = append(names, name)
	}
	return names
}

// All returns all registered sources.
func (r *SourceRegistry) All() []Source {
	sources := make([]Source, 0, len(r.sources))
	for _, s := range r.sources {
		sources = append(sources, s)
	}
	return sources
}
