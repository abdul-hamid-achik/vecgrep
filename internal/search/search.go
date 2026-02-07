// Package search provides semantic search functionality.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
)

// SearchMode defines how search is performed.
type SearchMode = db.SearchMode

const (
	// SearchModeSemantic uses pure vector similarity search.
	SearchModeSemantic = db.SearchModeSemantic
	// SearchModeKeyword uses BM25 text search only.
	SearchModeKeyword = db.SearchModeKeyword
	// SearchModeHybrid combines vector and text search.
	SearchModeHybrid = db.SearchModeHybrid
)

// SearchExplanation provides diagnostic info about a search.
type SearchExplanation = db.SearchExplanation

// Result represents a search result with full metadata.
type Result struct {
	ChunkID      int64   `json:"chunk_id"`
	FileID       int64   `json:"file_id"`
	FilePath     string  `json:"file_path"`
	RelativePath string  `json:"relative_path"`
	Content      string  `json:"content"`
	StartLine    int     `json:"start_line"`
	EndLine      int     `json:"end_line"`
	ChunkType    string  `json:"chunk_type"`
	SymbolName   string  `json:"symbol_name,omitempty"`
	Language     string  `json:"language"`
	Distance     float32 `json:"distance"`
	Score        float32 `json:"score"` // 1 - distance (higher is better)
}

// SearchOptions configures search behavior.
type SearchOptions struct {
	Limit       int
	Language    string   // Filter by single language
	Languages   []string // Filter by multiple languages (OR)
	ChunkType   string   // Filter by single chunk type
	ChunkTypes  []string // Filter by multiple chunk types (OR)
	FilePattern string   // Filter by file path pattern (glob)
	Directory   string   // Filter by directory prefix
	MinLine     int      // Filter by minimum start line
	MaxLine     int      // Filter by maximum start line
	MinScore    float32  // Minimum similarity score (0-1)
	ProjectRoot string   // Project root for relative path filtering

	// Search mode and hybrid settings
	Mode         SearchMode // Search mode: semantic, keyword, or hybrid
	VectorWeight float32    // Weight for vector similarity in hybrid mode (0-1)
	TextWeight   float32    // Weight for text matching in hybrid mode (0-1)
	Explain      bool       // Return search explanation for debugging
}

// SimilarOptions configures similar code search behavior.
type SimilarOptions struct {
	SearchOptions          // Embed existing options: Limit, Language, ChunkType, FilePattern
	ExcludeSameFile bool   // Exclude results from same file as source
	ExcludeSourceID bool   // Exclude the source chunk itself (default: true when using By ID/Location)
	SourceFilePath  string // Internal: file path of source chunk for same-file exclusion
}

// DefaultSearchOptions returns sensible defaults.
func DefaultSearchOptions() SearchOptions {
	return SearchOptions{
		Limit:        10,
		MinScore:     0.0,
		Mode:         SearchModeHybrid, // Default to hybrid search
		VectorWeight: 0.7,              // 70% vector similarity
		TextWeight:   0.3,              // 30% text matching
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

// Search performs a search for the given query using the specified mode.
func (s *Searcher) Search(ctx context.Context, query string, opts SearchOptions) ([]Result, error) {
	if query == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}

	if opts.Limit == 0 {
		opts.Limit = DefaultSearchOptions().Limit
	}

	// Set default mode if not specified
	if opts.Mode == "" {
		opts.Mode = SearchModeHybrid
	}
	if opts.VectorWeight == 0 {
		opts.VectorWeight = DefaultSearchOptions().VectorWeight
	}

	// Build filter options with extended fields
	filterOpts := db.FilterOptions{
		Language:    opts.Language,
		Languages:   opts.Languages,
		ChunkType:   opts.ChunkType,
		ChunkTypes:  opts.ChunkTypes,
		FilePattern: opts.FilePattern,
		Directory:   opts.Directory,
		MinLine:     opts.MinLine,
		MaxLine:     opts.MaxLine,
	}

	var searchResults []db.SearchResult
	var err error

	switch opts.Mode {
	case SearchModeKeyword:
		// Pure text search (no embedding needed)
		searchResults, err = s.db.TextSearch(query, opts.Limit, filterOpts)
		if err != nil {
			return nil, fmt.Errorf("text search: %w", err)
		}

	case SearchModeSemantic:
		// Pure vector search
		queryEmbedding, embedErr := s.provider.Embed(ctx, query)
		if embedErr != nil {
			return nil, fmt.Errorf("embed query: %w", embedErr)
		}
		searchResults, err = s.db.SearchWithFilter(queryEmbedding, opts.Limit, filterOpts)
		if err != nil {
			return nil, fmt.Errorf("search embeddings: %w", err)
		}

	case SearchModeHybrid:
		fallthrough
	default:
		// Hybrid search: combine vector + text
		queryEmbedding, embedErr := s.provider.Embed(ctx, query)
		if embedErr != nil {
			return nil, fmt.Errorf("embed query: %w", embedErr)
		}
		searchResults, err = s.db.HybridSearch(queryEmbedding, query, opts.Limit, filterOpts, opts.VectorWeight)
		if err != nil {
			return nil, fmt.Errorf("hybrid search: %w", err)
		}
	}

	// Convert to Result format
	results := make([]Result, 0, len(searchResults))
	for _, sr := range searchResults {
		result := searchResultToResult(sr)

		// Apply minimum score filter
		if opts.MinScore > 0 && result.Score < opts.MinScore {
			continue
		}

		results = append(results, result)

		if len(results) >= opts.Limit {
			break
		}
	}

	return results, nil
}

// SearchWithExplain performs a search and returns diagnostic information.
func (s *Searcher) SearchWithExplain(ctx context.Context, query string, opts SearchOptions) ([]Result, *SearchExplanation, error) {
	if query == "" {
		return nil, nil, fmt.Errorf("query cannot be empty")
	}

	if opts.Limit == 0 {
		opts.Limit = DefaultSearchOptions().Limit
	}

	// Generate embedding for the query
	queryEmbedding, err := s.provider.Embed(ctx, query)
	if err != nil {
		return nil, nil, fmt.Errorf("embed query: %w", err)
	}

	// Build filter options
	filterOpts := db.FilterOptions{
		Language:    opts.Language,
		Languages:   opts.Languages,
		ChunkType:   opts.ChunkType,
		ChunkTypes:  opts.ChunkTypes,
		FilePattern: opts.FilePattern,
		Directory:   opts.Directory,
		MinLine:     opts.MinLine,
		MaxLine:     opts.MaxLine,
	}

	// Get results with explanation
	searchResults, explanation, err := s.db.SearchWithExplain(queryEmbedding, opts.Limit, filterOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("search with explain: %w", err)
	}

	// Convert to Result format
	results := make([]Result, 0, len(searchResults))
	for _, sr := range searchResults {
		result := searchResultToResult(sr)

		if opts.MinScore > 0 && result.Score < opts.MinScore {
			continue
		}

		results = append(results, result)

		if len(results) >= opts.Limit {
			break
		}
	}

	return results, explanation, nil
}

// SearchSimilarByID finds code similar to the chunk with the given ID.
func (s *Searcher) SearchSimilarByID(ctx context.Context, chunkID int64, opts SimilarOptions) ([]Result, error) {
	if opts.Limit == 0 {
		opts.Limit = DefaultSearchOptions().Limit
	}

	// Get the embedding for the source chunk
	embedding, err := s.db.GetEmbedding(chunkID)
	if err != nil {
		return nil, fmt.Errorf("get embedding for chunk %d: %w", chunkID, err)
	}

	// Get source chunk's file path for same-file exclusion
	if opts.ExcludeSameFile && opts.SourceFilePath == "" {
		chunk, err := s.db.Backend().GetChunkByID(chunkID)
		if err == nil && chunk != nil {
			opts.SourceFilePath = chunk.RelativePath
		}
	}

	// Build filter options with extended fields
	filterOpts := db.FilterOptions{
		Language:    opts.Language,
		Languages:   opts.Languages,
		ChunkType:   opts.ChunkType,
		ChunkTypes:  opts.ChunkTypes,
		FilePattern: opts.FilePattern,
		Directory:   opts.Directory,
		MinLine:     opts.MinLine,
		MaxLine:     opts.MaxLine,
	}

	// Request more results to account for filtering
	searchLimit := max(opts.Limit*3, 50)

	searchResults, err := s.db.SearchWithFilter(embedding, searchLimit, filterOpts)
	if err != nil {
		return nil, fmt.Errorf("search embeddings: %w", err)
	}

	// Convert to Result format
	results := make([]Result, 0, len(searchResults))
	for _, sr := range searchResults {
		// Exclude source chunk by default
		if opts.ExcludeSourceID && sr.ChunkID == chunkID {
			continue
		}

		result := searchResultToResult(sr)

		// Exclude same file if requested
		if opts.ExcludeSameFile && result.RelativePath == opts.SourceFilePath {
			continue
		}

		// Apply minimum score filter
		if opts.MinScore > 0 && result.Score < opts.MinScore {
			continue
		}

		results = append(results, result)

		if len(results) >= opts.Limit {
			break
		}
	}

	return results, nil
}

// SearchSimilarByLocation finds code similar to the chunk at the given file:line location.
func (s *Searcher) SearchSimilarByLocation(ctx context.Context, filePath string, line int, opts SimilarOptions) ([]Result, error) {
	// Resolve file:line to chunk
	chunk, err := s.db.GetChunkByLocation(filePath, line)
	if err != nil {
		return nil, fmt.Errorf("resolve location %s:%d: %w", filePath, line, err)
	}

	// Enable source exclusion by default for location-based search
	opts.ExcludeSourceID = true
	opts.SourceFilePath = chunk.RelativePath

	// Use the chunk's ID
	return s.SearchSimilarByID(ctx, int64(chunk.ID), opts)
}

// SearchSimilarByText finds code similar to the given text snippet.
func (s *Searcher) SearchSimilarByText(ctx context.Context, text string, opts SimilarOptions) ([]Result, error) {
	if text == "" {
		return nil, fmt.Errorf("text cannot be empty")
	}

	if opts.Limit == 0 {
		opts.Limit = DefaultSearchOptions().Limit
	}

	// Generate embedding for the text
	embedding, err := s.provider.Embed(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("embed text: %w", err)
	}

	// Build filter options with extended fields
	filterOpts := db.FilterOptions{
		Language:    opts.Language,
		Languages:   opts.Languages,
		ChunkType:   opts.ChunkType,
		ChunkTypes:  opts.ChunkTypes,
		FilePattern: opts.FilePattern,
		Directory:   opts.Directory,
		MinLine:     opts.MinLine,
		MaxLine:     opts.MaxLine,
	}

	searchResults, err := s.db.SearchWithFilter(embedding, opts.Limit, filterOpts)
	if err != nil {
		return nil, fmt.Errorf("search embeddings: %w", err)
	}

	// Convert to Result format
	results := make([]Result, 0, len(searchResults))
	for _, sr := range searchResults {
		result := searchResultToResult(sr)

		// Apply minimum score filter
		if opts.MinScore > 0 && result.Score < opts.MinScore {
			continue
		}

		results = append(results, result)

		if len(results) >= opts.Limit {
			break
		}
	}

	return results, nil
}

// searchResultToResult converts a db.SearchResult to search.Result.
func searchResultToResult(sr db.SearchResult) Result {
	result := Result{
		ChunkID:  sr.ChunkID,
		Distance: sr.Distance,
		Score:    sr.Distance, // For cosine, Distance is already the similarity score
	}

	// Extract metadata from chunk payload
	if sr.Chunk != nil {
		result.FilePath = sr.Chunk.FilePath
		result.RelativePath = sr.Chunk.RelativePath
		result.Content = sr.Chunk.Content
		result.StartLine = sr.Chunk.StartLine
		result.EndLine = sr.Chunk.EndLine
		result.ChunkType = sr.Chunk.ChunkType
		result.SymbolName = sr.Chunk.SymbolName
		result.Language = sr.Chunk.Language
	}

	return result
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

	// Get detailed stats from the database
	detailedStats, err := s.db.GetDetailedStats("")
	if err != nil {
		return nil, fmt.Errorf("get stats: %w", err)
	}

	stats["total_files"] = detailedStats.TotalFiles
	stats["total_chunks"] = detailedStats.TotalChunks
	stats["projects"] = detailedStats.TotalProjects
	stats["files"] = detailedStats.TotalFiles
	stats["chunks"] = detailedStats.TotalChunks
	stats["embeddings"] = detailedStats.TotalChunks

	// Language distribution
	stats["languages"] = detailedStats.Languages

	// Chunk type distribution
	stats["chunk_types"] = detailedStats.ChunkTypes

	// Get embedding model info
	stats["embedding_model"] = s.provider.Model()
	stats["embedding_dimensions"] = s.provider.Dimensions()

	return stats, nil
}

// SearchByFile returns all chunks for a specific file.
func (s *Searcher) SearchByFile(ctx context.Context, filePath string) ([]Result, error) {
	chunks, err := s.db.GetChunksByFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("get chunks: %w", err)
	}

	results := make([]Result, 0, len(chunks))
	for _, c := range chunks {
		results = append(results, Result{
			ChunkID:      int64(c.ID),
			FilePath:     c.FilePath,
			RelativePath: c.RelativePath,
			Content:      c.Content,
			StartLine:    c.StartLine,
			EndLine:      c.EndLine,
			ChunkType:    c.ChunkType,
			SymbolName:   c.SymbolName,
			Language:     c.Language,
			Score:        1.0, // Direct file match
			Distance:     0.0,
		})
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
