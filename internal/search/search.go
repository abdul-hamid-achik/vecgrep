// Package search provides semantic search functionality.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// Result represents a search result with full metadata.
type Result struct {
	ChunkID      int64         `json:"chunk_id"`
	FileID       int64         `json:"file_id"`
	FilePath     string        `json:"file_path"`
	RelativePath string        `json:"relative_path"`
	Content      string        `json:"content"`
	StartLine    int           `json:"start_line"`
	EndLine      int           `json:"end_line"`
	ChunkType    string        `json:"chunk_type"`
	SymbolName   string        `json:"symbol_name,omitempty"`
	Language     string        `json:"language"`
	Distance     float32       `json:"distance"`
	Score        float32       `json:"score"` // 1 - distance (higher is better)
}

// SearchOptions configures search behavior.
type SearchOptions struct {
	Limit       int
	Language    string        // Filter by language
	ChunkType   string        // Filter by chunk type
	FilePattern string        // Filter by file path pattern
	MinScore    float32       // Minimum similarity score (0-1)
	ProjectRoot string        // Project root for relative path filtering
}

// DefaultSearchOptions returns sensible defaults.
func DefaultSearchOptions() SearchOptions {
	return SearchOptions{
		Limit:    10,
		MinScore: 0.0,
	}
}

// Searcher performs semantic searches against the indexed codebase.
type Searcher struct {
	db       *db.DB
	provider embed.Provider
}

// NewSearcher creates a new Searcher.
func NewSearcher(database *db.DB, provider embed.Provider) *Searcher {
	return &Searcher{
		db:       database,
		provider: provider,
	}
}

// Search performs a semantic search for the given query.
func (s *Searcher) Search(ctx context.Context, query string, opts SearchOptions) ([]Result, error) {
	if query == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}

	if opts.Limit == 0 {
		opts.Limit = DefaultSearchOptions().Limit
	}

	// Generate embedding for the query
	queryEmbedding, err := s.provider.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Search for similar vectors
	// Request more results than needed to account for filtering
	searchLimit := opts.Limit * 3
	if searchLimit < 50 {
		searchLimit = 50
	}

	searchResults, err := s.db.SearchEmbeddings(queryEmbedding, searchLimit)
	if err != nil {
		return nil, fmt.Errorf("search embeddings: %w", err)
	}

	// Hydrate results with chunk metadata
	results := make([]Result, 0, len(searchResults))
	for _, sr := range searchResults {
		result, err := s.hydrateResult(sr)
		if err != nil {
			continue // Skip results we can't hydrate
		}

		// Apply filters
		if !s.matchesFilters(result, opts) {
			continue
		}

		results = append(results, result)

		if len(results) >= opts.Limit {
			break
		}
	}

	return results, nil
}

// hydrateResult fetches full metadata for a search result.
func (s *Searcher) hydrateResult(sr db.SearchResult) (Result, error) {
	var result Result
	result.ChunkID = sr.ChunkID
	result.Distance = sr.Distance
	result.Score = 1 - sr.Distance // Convert distance to similarity score

	// Fetch chunk data
	err := s.db.QueryRow(`
		SELECT c.file_id, c.content, c.start_line, c.end_line, c.chunk_type, c.symbol_name,
		       f.path, f.relative_path, f.language
		FROM chunks c
		JOIN files f ON c.file_id = f.id
		WHERE c.id = ?`, sr.ChunkID).Scan(
		&result.FileID, &result.Content, &result.StartLine, &result.EndLine,
		&result.ChunkType, &result.SymbolName,
		&result.FilePath, &result.RelativePath, &result.Language,
	)
	if err != nil {
		return result, fmt.Errorf("fetch chunk metadata: %w", err)
	}

	return result, nil
}

// matchesFilters checks if a result matches the search options filters.
func (s *Searcher) matchesFilters(result Result, opts SearchOptions) bool {
	// Check minimum score
	if opts.MinScore > 0 && result.Score < opts.MinScore {
		return false
	}

	// Filter by language
	if opts.Language != "" && !strings.EqualFold(result.Language, opts.Language) {
		return false
	}

	// Filter by chunk type
	if opts.ChunkType != "" && !strings.EqualFold(result.ChunkType, opts.ChunkType) {
		return false
	}

	// Filter by file pattern
	if opts.FilePattern != "" {
		matched, err := filepath.Match(opts.FilePattern, result.RelativePath)
		if err != nil || !matched {
			// Also try matching against the base name
			matched, _ = filepath.Match(opts.FilePattern, filepath.Base(result.RelativePath))
			if !matched {
				return false
			}
		}
	}

	return true
}

// OutputFormat specifies the output format for search results.
type OutputFormat string

const (
	FormatDefault OutputFormat = "default"
	FormatJSON    OutputFormat = "json"
	FormatCompact OutputFormat = "compact"
)

// FormatResults formats search results according to the specified format.
func FormatResults(results []Result, format OutputFormat) string {
	switch format {
	case FormatJSON:
		return formatJSON(results)
	case FormatCompact:
		return formatCompact(results)
	default:
		return formatDefault(results)
	}
}

// formatDefault produces human-readable output.
func formatDefault(results []Result) string {
	if len(results) == 0 {
		return "No results found."
	}

	var sb strings.Builder

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("=== Result %d (score: %.2f) ===\n", i+1, r.Score))
		sb.WriteString(fmt.Sprintf("File: %s\n", r.RelativePath))
		sb.WriteString(fmt.Sprintf("Lines: %d-%d", r.StartLine, r.EndLine))

		if r.SymbolName != "" {
			sb.WriteString(fmt.Sprintf(" | Symbol: %s", r.SymbolName))
		}
		if r.ChunkType != "" && r.ChunkType != "generic" {
			sb.WriteString(fmt.Sprintf(" | Type: %s", r.ChunkType))
		}
		if r.Language != "" && r.Language != "unknown" {
			sb.WriteString(fmt.Sprintf(" | Lang: %s", r.Language))
		}
		sb.WriteString("\n\n")

		// Indent content
		lines := strings.Split(r.Content, "\n")
		for _, line := range lines {
			sb.WriteString("  ")
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// formatJSON produces JSON output.
func formatJSON(results []Result) string {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": "%s"}`, err.Error())
	}
	return string(data)
}

// formatCompact produces compact single-line-per-result output.
func formatCompact(results []Result) string {
	if len(results) == 0 {
		return ""
	}

	var sb strings.Builder

	for _, r := range results {
		// Format: file:startLine-endLine score symbol
		sb.WriteString(fmt.Sprintf("%s:%d-%d\t%.2f", r.RelativePath, r.StartLine, r.EndLine, r.Score))
		if r.SymbolName != "" {
			sb.WriteString(fmt.Sprintf("\t%s", r.SymbolName))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// GetIndexStats returns statistics about the search index.
func (s *Searcher) GetIndexStats(ctx context.Context) (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Get database stats
	dbStats, err := s.db.Stats()
	if err != nil {
		return nil, fmt.Errorf("get db stats: %w", err)
	}

	for k, v := range dbStats {
		stats[k] = v
	}

	// Get additional info
	var totalFiles int64
	var totalChunks int64

	s.db.QueryRow(`SELECT COUNT(*) FROM files`).Scan(&totalFiles)
	s.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&totalChunks)

	stats["total_files"] = totalFiles
	stats["total_chunks"] = totalChunks

	// Get languages distribution
	rows, err := s.db.Query(`SELECT language, COUNT(*) as count FROM files GROUP BY language ORDER BY count DESC`)
	if err == nil {
		defer rows.Close()
		langStats := make(map[string]int64)
		for rows.Next() {
			var lang string
			var count int64
			if rows.Scan(&lang, &count) == nil {
				langStats[lang] = count
			}
		}
		stats["languages"] = langStats
	}

	// Get chunk types distribution
	rows, err = s.db.Query(`SELECT chunk_type, COUNT(*) as count FROM chunks GROUP BY chunk_type ORDER BY count DESC`)
	if err == nil {
		defer rows.Close()
		typeStats := make(map[string]int64)
		for rows.Next() {
			var chunkType string
			var count int64
			if rows.Scan(&chunkType, &count) == nil {
				typeStats[chunkType] = count
			}
		}
		stats["chunk_types"] = typeStats
	}

	// Get embedding model info
	stats["embedding_model"] = s.provider.Model()
	stats["embedding_dimensions"] = s.provider.Dimensions()

	return stats, nil
}

// SearchByFile returns all chunks for a specific file.
func (s *Searcher) SearchByFile(ctx context.Context, filePath string) ([]Result, error) {
	rows, err := s.db.Query(`
		SELECT c.id, c.file_id, c.content, c.start_line, c.end_line, c.chunk_type, c.symbol_name,
		       f.path, f.relative_path, f.language
		FROM chunks c
		JOIN files f ON c.file_id = f.id
		WHERE f.relative_path = ? OR f.path = ?
		ORDER BY c.start_line`, filePath, filePath)
	if err != nil {
		return nil, fmt.Errorf("query chunks: %w", err)
	}
	defer rows.Close()

	var results []Result
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.ChunkID, &r.FileID, &r.Content, &r.StartLine, &r.EndLine,
			&r.ChunkType, &r.SymbolName, &r.FilePath, &r.RelativePath, &r.Language); err != nil {
			continue
		}
		r.Score = 1.0 // Direct file match
		r.Distance = 0.0
		results = append(results, r)
	}

	return results, nil
}

// LanguageFilter wraps Language type for external use.
type LanguageFilter = index.Language

// Language constants for filtering.
const (
	LangGo         = index.LangGo
	LangPython     = index.LangPython
	LangJavaScript = index.LangJavaScript
	LangTypeScript = index.LangTypeScript
	LangRust       = index.LangRust
)

// ChunkTypeFilter constants.
const (
	ChunkTypeFunction = string(index.ChunkTypeFunction)
	ChunkTypeClass    = string(index.ChunkTypeClass)
	ChunkTypeBlock    = string(index.ChunkTypeBlock)
	ChunkTypeComment  = string(index.ChunkTypeComment)
	ChunkTypeGeneric  = string(index.ChunkTypeGeneric)
)
