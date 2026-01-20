// Package mcp implements the MCP server using the official SDK.
package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	"github.com/abdul-hamid-achik/vecgrep/internal/version"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Input/output types for tools

// InitInput is the input for vecgrep_init.
type InitInput struct {
	Path  string `json:"path" jsonschema:"REQUIRED - Full absolute path to the project directory. Get this from the current working directory the user is in."`
	Force bool   `json:"force,omitempty" jsonschema:"Overwrite existing configuration if present."`
}

// SearchInput is the input for vecgrep_search.
type SearchInput struct {
	Query       string `json:"query" jsonschema:"The search query. Can be natural language description of what you're looking for."`
	Limit       int    `json:"limit,omitempty" jsonschema:"Maximum number of results to return."`
	Language    string `json:"language,omitempty" jsonschema:"Filter results by programming language."`
	ChunkType   string `json:"chunk_type,omitempty" jsonschema:"Filter results by chunk type."`
	FilePattern string `json:"file_pattern,omitempty" jsonschema:"Filter results by file path pattern (glob)."`
}

// IndexInput is the input for vecgrep_index.
type IndexInput struct {
	Paths []string `json:"paths,omitempty" jsonschema:"Specific paths to index. If empty indexes the entire project."`
	Force bool     `json:"force,omitempty" jsonschema:"Force re-indexing of all files even if unchanged."`
}

// StatusInput is the input for vecgrep_status (empty).
type StatusInput struct{}

// SDKServer wraps the official MCP SDK server.
type SDKServer struct {
	server      *sdkmcp.Server
	db          *db.DB
	provider    embed.Provider
	projectRoot string
	searcher    *search.Searcher
	initialized bool
}

// SDKServerConfig contains configuration for the SDK-based MCP server.
type SDKServerConfig struct {
	DB          *db.DB
	Provider    embed.Provider
	ProjectRoot string
}

// NewSDKServer creates a new MCP server using the official SDK.
func NewSDKServer(cfg SDKServerConfig) *SDKServer {
	s := &SDKServer{
		db:          cfg.DB,
		provider:    cfg.Provider,
		projectRoot: cfg.ProjectRoot,
		initialized: cfg.DB != nil && cfg.ProjectRoot != "",
	}
	if s.initialized {
		s.searcher = search.NewSearcher(cfg.DB, cfg.Provider)
	}

	// Create the MCP server
	s.server = sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "vecgrep",
		Version: version.Version,
	}, &sdkmcp.ServerOptions{
		Instructions: "vecgrep provides semantic code search using vector embeddings. " +
			"IMPORTANT: You must run vecgrep_init first with the project path to activate the project. " +
			"Example: vecgrep_init with path=\"/path/to/project\". " +
			"This is required even if the project was previously initialized via CLI. " +
			"After activation, use vecgrep_search to find code, vecgrep_index to index files, " +
			"and vecgrep_status to check statistics.",
	})

	// Register tools using typed handlers
	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_init",
		Description: "REQUIRED FIRST STEP: Activate vecgrep for a project. Run this before using any other vecgrep tools. If .vecgrep folder exists, it activates the existing index. If not, it creates a new one. Must be run each session to tell vecgrep which project to use.",
	}, s.handleInit)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_search",
		Description: "Perform semantic search across the indexed codebase. Returns code chunks that are semantically similar to the query.",
	}, s.handleSearch)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_index",
		Description: "Index files in the project for semantic search. Only indexes files that have changed since the last index.",
	}, s.handleIndex)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_status",
		Description: "Get statistics about the search index, including number of files, chunks, and language distribution.",
	}, s.handleStatus)

	return s
}

// Run starts the MCP server.
func (s *SDKServer) Run(ctx context.Context) error {
	return s.server.Run(ctx, &sdkmcp.StdioTransport{})
}

// handleInit handles the vecgrep_init tool.
func (s *SDKServer) handleInit(ctx context.Context, req *sdkmcp.CallToolRequest, input InitInput) (*sdkmcp.CallToolResult, any, error) {
	path := input.Path
	if path == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "Error: 'path' parameter is required.\n\nPlease specify the full path to the project directory you want to initialize.\nExample: vecgrep_init with path=\"/Users/you/projects/myproject\""},
			},
			IsError: true,
		}, nil, nil
	}

	// Expand ~ to home directory if needed
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to resolve path: %v", err)}},
			IsError: true,
		}, nil, nil
	}
	path = absPath

	// Verify the directory exists
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Directory does not exist: %s", path)}},
			IsError: true,
		}, nil, nil
	}

	dataDir := filepath.Join(path, config.DefaultDataDir)

	// Check if already initialized
	if _, err := os.Stat(dataDir); err == nil && !input.Force {
		// Already initialized - just activate this project
		return s.activateProject(ctx, path)
	}

	// Create configuration
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.DBPath = filepath.Join(dataDir, config.DefaultDBFile)

	// Create data directory
	if err := cfg.EnsureDataDir(); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to create data directory: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Write default config
	if err := cfg.WriteDefaultConfig(); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to write config: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Initialize and activate the project
	return s.activateProject(ctx, path)
}

// activateProject opens the database and configures the server for the given project.
func (s *SDKServer) activateProject(ctx context.Context, projectPath string) (*sdkmcp.CallToolResult, any, error) {
	dataDir := filepath.Join(projectPath, config.DefaultDataDir)
	dbPath := filepath.Join(dataDir, config.DefaultDBFile)

	// Load config
	cfg, err := config.Load(projectPath)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to load config: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Close existing database if open
	if s.db != nil {
		s.db.Close()
	}

	// Open database
	database, err := db.Open(dbPath, cfg.Embedding.Dimensions)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Create embedding provider
	provider := embed.NewOllamaProvider(embed.OllamaConfig{
		URL:        cfg.Embedding.OllamaURL,
		Model:      cfg.Embedding.Model,
		Dimensions: cfg.Embedding.Dimensions,
	})

	// Update server state
	s.db = database
	s.provider = provider
	s.projectRoot = projectPath
	s.searcher = search.NewSearcher(database, provider)
	s.initialized = true

	// Get sqlite-vec version
	vecVersion, _ := database.VecVersion()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Activated vecgrep project: %s\n\n", projectPath))
	sb.WriteString(fmt.Sprintf("- Database: %s\n", dbPath))
	sb.WriteString(fmt.Sprintf("- sqlite-vec: %s\n", vecVersion))
	sb.WriteString(fmt.Sprintf("- Embedding provider: %s (%s)\n", cfg.Embedding.Provider, cfg.Embedding.Model))

	// Get stats
	stats, err := s.searcher.GetIndexStats(ctx)
	if err == nil {
		if totalFiles, ok := stats["total_files"].(int64); ok && totalFiles > 0 {
			totalChunks, _ := stats["total_chunks"].(int64)
			sb.WriteString(fmt.Sprintf("\nIndex stats: %d files, %d chunks\n", totalFiles, totalChunks))
		} else {
			sb.WriteString("\nNext step: Run vecgrep_index to index your codebase.")
		}
	} else {
		sb.WriteString("\nNext step: Run vecgrep_index to index your codebase.")
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// checkOllama verifies Ollama is running.
func (s *SDKServer) checkOllama(ctx context.Context) *sdkmcp.CallToolResult {
	if s.provider == nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Ollama provider not configured. Run vecgrep_init first."}},
			IsError: true,
		}
	}

	if err := s.provider.Ping(ctx); err != nil {
		var sb strings.Builder
		sb.WriteString("Ollama is not running or not reachable.\n\n")
		sb.WriteString("To fix this:\n")
		sb.WriteString("1. Install Ollama: https://ollama.ai\n")
		sb.WriteString("2. Start Ollama:\n")
		sb.WriteString("   OLLAMA_HOST=0.0.0.0 ollama serve\n")
		sb.WriteString("3. Pull the embedding model:\n")
		sb.WriteString("   ollama pull nomic-embed-text\n")
		sb.WriteString(fmt.Sprintf("\nError: %v", err))
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
			IsError: true,
		}
	}
	return nil
}

// handleSearch handles the vecgrep_search tool.
func (s *SDKServer) handleSearch(ctx context.Context, req *sdkmcp.CallToolRequest, input SearchInput) (*sdkmcp.CallToolResult, any, error) {
	if !s.initialized {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "vecgrep is not initialized in this directory. Run vecgrep_init first."}},
			IsError: true,
		}, nil, nil
	}

	// Check Ollama
	if errResult := s.checkOllama(ctx); errResult != nil {
		return errResult, nil, nil
	}

	if input.Query == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "query parameter is required"}},
			IsError: true,
		}, nil, nil
	}

	opts := search.DefaultSearchOptions()
	opts.ProjectRoot = s.projectRoot

	if input.Limit > 0 {
		opts.Limit = input.Limit
	}
	if input.Language != "" {
		opts.Language = input.Language
	}
	if input.ChunkType != "" {
		opts.ChunkType = input.ChunkType
	}
	if input.FilePattern != "" {
		opts.FilePattern = input.FilePattern
	}

	// Perform search
	results, err := s.searcher.Search(ctx, input.Query, opts)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Search error: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Format results
	if len(results) == 0 {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "No results found."}},
		}, nil, nil
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

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// handleIndex handles the vecgrep_index tool.
func (s *SDKServer) handleIndex(ctx context.Context, req *sdkmcp.CallToolRequest, input IndexInput) (*sdkmcp.CallToolResult, any, error) {
	if !s.initialized {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "vecgrep is not initialized in this directory. Run vecgrep_init first."}},
			IsError: true,
		}, nil, nil
	}

	// Check Ollama
	if errResult := s.checkOllama(ctx); errResult != nil {
		return errResult, nil, nil
	}

	// Create indexer
	cfg := index.DefaultIndexerConfig()
	indexer := index.NewIndexer(s.db, s.provider, cfg)

	var result *index.IndexResult
	var err error

	if input.Force {
		result, err = indexer.ReindexAll(ctx, s.projectRoot)
	} else {
		result, err = indexer.Index(ctx, s.projectRoot, input.Paths...)
	}

	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Indexing error: %v", err)}},
			IsError: true,
		}, nil, nil
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

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// handleStatus handles the vecgrep_status tool.
func (s *SDKServer) handleStatus(ctx context.Context, req *sdkmcp.CallToolRequest, input StatusInput) (*sdkmcp.CallToolResult, any, error) {
	if !s.initialized {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "vecgrep is not initialized in this directory. Run vecgrep_init first."}},
			IsError: true,
		}, nil, nil
	}

	stats, err := s.searcher.GetIndexStats(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Error getting stats: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Format stats as text
	var sb strings.Builder
	sb.WriteString("Index Statistics:\n\n")

	if totalFiles, ok := stats["total_files"].(int64); ok {
		sb.WriteString(fmt.Sprintf("Total files: %d\n", totalFiles))
	}
	if totalChunks, ok := stats["total_chunks"].(int64); ok {
		sb.WriteString(fmt.Sprintf("Total chunks: %d\n", totalChunks))
	}
	if langStats, ok := stats["languages"].(map[string]int64); ok {
		sb.WriteString("\nBy language:\n")
		for lang, count := range langStats {
			sb.WriteString(fmt.Sprintf("  %s: %d\n", lang, count))
		}
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}
