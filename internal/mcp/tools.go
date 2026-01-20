package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
)

// ToolsHandler manages MCP tool implementations.
type ToolsHandler struct {
	db          *db.DB
	provider    embed.Provider
	projectRoot string
	searcher    *search.Searcher
}

// NewToolsHandler creates a new tools handler.
func NewToolsHandler(database *db.DB, provider embed.Provider, projectRoot string) *ToolsHandler {
	return &ToolsHandler{
		db:          database,
		provider:    provider,
		projectRoot: projectRoot,
		searcher:    search.NewSearcher(database, provider),
	}
}

// ListTools returns the list of available tools.
func (h *ToolsHandler) ListTools() ToolsListResult {
	return ToolsListResult{
		Tools: []Tool{
			{
				Name:        "vecgrep_search",
				Description: "Perform semantic search across the indexed codebase. Returns code chunks that are semantically similar to the query.",
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]PropertySchema{
						"query": {
							Type:        "string",
							Description: "The search query. Can be natural language description of what you're looking for.",
						},
						"limit": {
							Type:        "integer",
							Description: "Maximum number of results to return.",
							Default:     10,
						},
						"language": {
							Type:        "string",
							Description: "Filter results by programming language.",
							Enum:        []string{"go", "python", "javascript", "typescript", "rust", "java", "c", "cpp"},
						},
						"chunk_type": {
							Type:        "string",
							Description: "Filter results by chunk type.",
							Enum:        []string{"function", "class", "block", "comment", "generic"},
						},
						"file_pattern": {
							Type:        "string",
							Description: "Filter results by file path pattern (glob).",
						},
					},
					Required: []string{"query"},
				},
			},
			{
				Name:        "vecgrep_index",
				Description: "Index files in the project for semantic search. Only indexes files that have changed since the last index.",
				InputSchema: InputSchema{
					Type: "object",
					Properties: map[string]PropertySchema{
						"paths": {
							Type:        "array",
							Description: "Specific paths to index. If empty, indexes the entire project.",
						},
						"force": {
							Type:        "boolean",
							Description: "Force re-indexing of all files, even if unchanged.",
							Default:     false,
						},
					},
				},
			},
			{
				Name:        "vecgrep_status",
				Description: "Get statistics about the search index, including number of files, chunks, and language distribution.",
				InputSchema: InputSchema{
					Type:       "object",
					Properties: map[string]PropertySchema{},
				},
			},
		},
	}
}

// CallTool executes a tool and returns the result.
func (h *ToolsHandler) CallTool(ctx context.Context, name string, args map[string]interface{}) (CallToolResult, error) {
	switch name {
	case "vecgrep_search":
		return h.handleSearch(ctx, args)
	case "vecgrep_index":
		return h.handleIndex(ctx, args)
	case "vecgrep_status":
		return h.handleStatus(ctx, args)
	default:
		return CallToolResult{
			Content: []ContentBlock{TextContent(fmt.Sprintf("Unknown tool: %s", name))},
			IsError: true,
		}, nil
	}
}

// handleSearch performs a semantic search.
func (h *ToolsHandler) handleSearch(ctx context.Context, args map[string]interface{}) (CallToolResult, error) {
	// Parse arguments
	query, _ := args["query"].(string)
	if query == "" {
		return CallToolResult{
			Content: []ContentBlock{TextContent("query parameter is required")},
			IsError: true,
		}, nil
	}

	opts := search.DefaultSearchOptions()
	opts.ProjectRoot = h.projectRoot

	if limit, ok := args["limit"].(float64); ok {
		opts.Limit = int(limit)
	}
	if lang, ok := args["language"].(string); ok {
		opts.Language = lang
	}
	if chunkType, ok := args["chunk_type"].(string); ok {
		opts.ChunkType = chunkType
	}
	if filePattern, ok := args["file_pattern"].(string); ok {
		opts.FilePattern = filePattern
	}

	// Perform search
	results, err := h.searcher.Search(ctx, query, opts)
	if err != nil {
		return CallToolResult{
			Content: []ContentBlock{TextContent(fmt.Sprintf("Search error: %v", err))},
			IsError: true,
		}, nil
	}

	// Format results
	if len(results) == 0 {
		return CallToolResult{
			Content: []ContentBlock{TextContent("No results found.")},
		}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d results:\n\n", len(results)))

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("### Result %d (score: %.2f)\n", i+1, r.Score))
		sb.WriteString(fmt.Sprintf("**File:** %s (lines %d-%d)\n", r.RelativePath, r.StartLine, r.EndLine))
		if r.SymbolName != "" {
			sb.WriteString(fmt.Sprintf("**Symbol:** %s\n", r.SymbolName))
		}
		if r.Language != "" && r.Language != "unknown" {
			sb.WriteString(fmt.Sprintf("**Language:** %s\n", r.Language))
		}
		sb.WriteString("\n```")
		if r.Language != "" && r.Language != "unknown" {
			sb.WriteString(r.Language)
		}
		sb.WriteString("\n")
		sb.WriteString(r.Content)
		sb.WriteString("\n```\n\n")
	}

	return CallToolResult{
		Content: []ContentBlock{TextContent(sb.String())},
	}, nil
}

// handleIndex triggers indexing.
func (h *ToolsHandler) handleIndex(ctx context.Context, args map[string]interface{}) (CallToolResult, error) {
	// Parse arguments
	force, _ := args["force"].(bool)

	var paths []string
	if pathsArg, ok := args["paths"].([]interface{}); ok {
		for _, p := range pathsArg {
			if s, ok := p.(string); ok {
				paths = append(paths, s)
			}
		}
	}

	// Create indexer
	cfg := index.DefaultIndexerConfig()
	indexer := index.NewIndexer(h.db, h.provider, cfg)

	var result *index.IndexResult
	var err error

	if force {
		result, err = indexer.ReindexAll(ctx, h.projectRoot)
	} else {
		result, err = indexer.Index(ctx, h.projectRoot, paths...)
	}

	if err != nil {
		return CallToolResult{
			Content: []ContentBlock{TextContent(fmt.Sprintf("Indexing error: %v", err))},
			IsError: true,
		}, nil
	}

	// Format result
	var sb strings.Builder
	sb.WriteString("Indexing complete:\n")
	sb.WriteString(fmt.Sprintf("- Files processed: %d\n", result.FilesProcessed))
	sb.WriteString(fmt.Sprintf("- Files skipped (unchanged): %d\n", result.FilesSkipped))
	sb.WriteString(fmt.Sprintf("- Chunks created: %d\n", result.ChunksCreated))
	sb.WriteString(fmt.Sprintf("- Duration: %s\n", result.Duration))

	if len(result.Errors) > 0 {
		sb.WriteString(fmt.Sprintf("\nWarnings/Errors: %d\n", len(result.Errors)))
		for _, e := range result.Errors {
			sb.WriteString(fmt.Sprintf("  - %v\n", e))
		}
	}

	return CallToolResult{
		Content: []ContentBlock{TextContent(sb.String())},
	}, nil
}

// handleStatus returns index statistics.
func (h *ToolsHandler) handleStatus(ctx context.Context, args map[string]interface{}) (CallToolResult, error) {
	stats, err := h.searcher.GetIndexStats(ctx)
	if err != nil {
		return CallToolResult{
			Content: []ContentBlock{TextContent(fmt.Sprintf("Error getting stats: %v", err))},
			IsError: true,
		}, nil
	}

	// Format stats
	data, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		return CallToolResult{
			Content: []ContentBlock{TextContent(fmt.Sprintf("Error formatting stats: %v", err))},
			IsError: true,
		}, nil
	}

	var sb strings.Builder
	sb.WriteString("Index Statistics:\n\n")
	sb.WriteString("```json\n")
	sb.WriteString(string(data))
	sb.WriteString("\n```")

	return CallToolResult{
		Content: []ContentBlock{TextContent(sb.String())},
	}, nil
}
