// Package templates provides HTML templates for the web UI.
// This file contains stub implementations that will be replaced
// by templ-generated code when running `templ generate`.
package templates

import (
	"context"
	"io"
)

// Component is the interface that templ components implement.
type Component interface {
	Render(ctx context.Context, w io.Writer) error
}

// IndexData contains data for the index page.
type IndexData struct {
	Query   string
	Results []SearchResult
}

// SearchResult represents a search result for display.
type SearchResult struct {
	ChunkID    int64
	FilePath   string
	Content    string
	StartLine  int
	EndLine    int
	ChunkType  string
	SymbolName string
	Language   string
	Score      float32
}

// stubComponent is a no-op component for compilation.
type stubComponent struct{}

func (s stubComponent) Render(ctx context.Context, w io.Writer) error {
	return nil
}

// Index renders the main search page.
// This is a stub - run `templ generate` to create the real implementation.
func Index(data IndexData) Component {
	return stubComponent{}
}

// SearchResults renders search results.
// This is a stub - run `templ generate` to create the real implementation.
func SearchResults(results []SearchResult) Component {
	return stubComponent{}
}

// Error renders an error message.
// This is a stub - run `templ generate` to create the real implementation.
func Error(message string) Component {
	return stubComponent{}
}

// StatusPage renders the status page.
// This is a stub - run `templ generate` to create the real implementation.
func StatusPage(stats map[string]interface{}) Component {
	return stubComponent{}
}
