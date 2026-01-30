// Package mcp implements the MCP server using the official SDK.
package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/memory"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	"github.com/abdul-hamid-achik/vecgrep/internal/version"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Input/output types for tools

// InitInput is the input for vecgrep_init.
type InitInput struct {
	Path  string `json:"path" jsonschema:"Full absolute path to the project directory. Get this from the current working directory the user is in."`
	Force bool   `json:"force,omitempty" jsonschema:"Overwrite existing configuration if present."`
}

// SearchInput is the input for vecgrep_search.
type SearchInput struct {
	Query        string   `json:"query" jsonschema:"The search query. Can be natural language description of what you're looking for."`
	Limit        int      `json:"limit,omitempty" jsonschema:"Maximum number of results to return."`
	Language     string   `json:"language,omitempty" jsonschema:"Filter results by programming language."`
	Languages    []string `json:"languages,omitempty" jsonschema:"Filter results by multiple languages (OR)."`
	ChunkType    string   `json:"chunk_type,omitempty" jsonschema:"Filter results by chunk type."`
	ChunkTypes   []string `json:"chunk_types,omitempty" jsonschema:"Filter results by multiple chunk types (OR)."`
	FilePattern  string   `json:"file_pattern,omitempty" jsonschema:"Filter results by file path pattern (glob)."`
	Directory    string   `json:"directory,omitempty" jsonschema:"Filter results by directory prefix."`
	MinLine      int      `json:"min_line,omitempty" jsonschema:"Filter by minimum start line."`
	MaxLine      int      `json:"max_line,omitempty" jsonschema:"Filter by maximum start line."`
	Mode         string   `json:"mode,omitempty" jsonschema:"Search mode: 'semantic' (vector only), 'keyword' (text only), or 'hybrid' (combined, default)."`
	Explain      bool     `json:"explain,omitempty" jsonschema:"Return search diagnostics including timing and index info."`
	ContextLines int      `json:"context_lines,omitempty" jsonschema:"Number of lines to include before and after each result (default: 0)."`
}

// IndexInput is the input for vecgrep_index.
type IndexInput struct {
	Paths []string `json:"paths,omitempty" jsonschema:"Specific paths to index. If empty indexes the entire project."`
	Force bool     `json:"force,omitempty" jsonschema:"Force re-indexing of all files even if unchanged."`
}

// StatusInput is the input for vecgrep_status (empty).
type StatusInput struct{}

// SimilarInput is the input for vecgrep_similar.
type SimilarInput struct {
	ChunkID         int64    `json:"chunk_id,omitempty" jsonschema:"Find code similar to this chunk ID."`
	FileLocation    string   `json:"file_location,omitempty" jsonschema:"Find code similar to the chunk at this file:line location (e.g., 'search.go:50')."`
	Text            string   `json:"text,omitempty" jsonschema:"Find code similar to this text snippet."`
	Limit           int      `json:"limit,omitempty" jsonschema:"Maximum number of results to return."`
	Language        string   `json:"language,omitempty" jsonschema:"Filter results by programming language."`
	Languages       []string `json:"languages,omitempty" jsonschema:"Filter results by multiple languages (OR)."`
	ChunkType       string   `json:"chunk_type,omitempty" jsonschema:"Filter results by chunk type."`
	ChunkTypes      []string `json:"chunk_types,omitempty" jsonschema:"Filter results by multiple chunk types (OR)."`
	FilePattern     string   `json:"file_pattern,omitempty" jsonschema:"Filter results by file path pattern (glob)."`
	Directory       string   `json:"directory,omitempty" jsonschema:"Filter results by directory prefix."`
	MinLine         int      `json:"min_line,omitempty" jsonschema:"Filter by minimum start line."`
	MaxLine         int      `json:"max_line,omitempty" jsonschema:"Filter by maximum start line."`
	ExcludeSameFile bool     `json:"exclude_same_file,omitempty" jsonschema:"Exclude results from the same file as the source."`
}

// DeleteInput is the input for vecgrep_delete.
type DeleteInput struct {
	FilePath string `json:"file_path" jsonschema:"The file path to delete from the index (relative or absolute)."`
}

// CleanInput is the input for vecgrep_clean (empty).
type CleanInput struct{}

// ResetInput is the input for vecgrep_reset.
type ResetInput struct {
	Confirm string `json:"confirm" jsonschema:"Type 'yes' to confirm the reset operation."`
}

// OverviewInput is the input for vecgrep_overview.
type OverviewInput struct {
	IncludeStructure    bool `json:"include_structure,omitempty" jsonschema:"Include directory structure in output (default: true)."`
	IncludeEntryPoints  bool `json:"include_entry_points,omitempty" jsonschema:"Include entry point files in output (default: true)."`
	MaxDirectoryDepth   int  `json:"max_directory_depth,omitempty" jsonschema:"Maximum directory depth to show (default: 3)."`
	IncludeKeyFiles     bool `json:"include_key_files,omitempty" jsonschema:"Include key files like README, config (default: true)."`
}

// BatchSearchInput is the input for vecgrep_batch_search.
type BatchSearchInput struct {
	Queries       []string `json:"queries" jsonschema:"List of queries to search for."`
	LimitPerQuery int      `json:"limit_per_query,omitempty" jsonschema:"Maximum results per query (default: 3)."`
	Deduplicate   bool     `json:"deduplicate,omitempty" jsonschema:"Remove duplicate results across queries (default: true)."`
	Language      string   `json:"language,omitempty" jsonschema:"Filter results by programming language."`
	ChunkType     string   `json:"chunk_type,omitempty" jsonschema:"Filter results by chunk type."`
}

// RelatedFilesInput is the input for vecgrep_related_files.
type RelatedFilesInput struct {
	File         string `json:"file" jsonschema:"Path to the file to find related files for."`
	Relationship string `json:"relationship,omitempty" jsonschema:"Type of relationship: 'imports', 'imported_by', 'tests', 'all' (default: 'all')."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum number of related files to return (default: 10)."`
}

// SDKServer wraps the official MCP SDK server.
type SDKServer struct {
	server      *sdkmcp.Server
	db          *db.DB
	provider    embed.Provider
	projectRoot string
	searcher    *search.Searcher
	initialized bool

	// Memory store (lazy initialized)
	memoryStore   *memory.MemoryStore
	memoryInitMu  sync.Mutex
	memoryInitErr error
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
			"IMPORTANT: The .vecgrep folder should be added to .gitignore - it contains the local database and should not be committed. " +
			"For projects with existing .vecgrep folder, just use vecgrep_search directly - it auto-detects the project. " +
			"For new projects, run vecgrep_init with the project path, then vecgrep_index to index the codebase.",
	})

	// Register tools using typed handlers
	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_init",
		Description: "Initialize or activate vecgrep for a project. If .vecgrep folder exists, activates the existing index. If not, creates a new one. Note: Search/index/status commands auto-detect projects, so this is only needed for new projects or switching directories.",
	}, s.handleInit)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_search",
		Description: "Perform semantic search across the indexed codebase. Auto-detects the project from current directory. Supports three search modes: 'semantic' (vector similarity), 'keyword' (text matching), or 'hybrid' (combined, default). Returns code chunks ranked by relevance.",
	}, s.handleSearch)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_index",
		Description: "Index files in the project for semantic search. Only indexes files that have changed since the last index.",
	}, s.handleIndex)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_status",
		Description: "Get statistics about the search index, including number of files, chunks, and language distribution.",
	}, s.handleStatus)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_similar",
		Description: "Find code similar to an existing chunk, file location, or text snippet. Provide exactly one of: chunk_id, file_location (e.g., 'search.go:50'), or text.",
	}, s.handleSimilar)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_delete",
		Description: "Delete a file and all its chunks from the search index.",
	}, s.handleDelete)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_clean",
		Description: "Remove orphaned data (chunks without files, embeddings without chunks) and vacuum the database to reclaim space.",
	}, s.handleClean)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_reset",
		Description: "Reset the project database by clearing all indexed files, chunks, and embeddings. Requires confirmation.",
	}, s.handleReset)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_overview",
		Description: "Get high-level overview of the codebase structure including languages, directory structure, entry points, and key files.",
	}, s.handleOverview)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_batch_search",
		Description: "Search multiple queries in parallel. Returns results grouped by query with optional deduplication.",
	}, s.handleBatchSearch)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_related_files",
		Description: "Find files related to a given file (imports, tests, configs). Useful for understanding code dependencies.",
	}, s.handleRelatedFiles)

	// Memory tools (global, not project-specific)
	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "memory_remember",
		Description: "Store a memory with optional importance, tags, and TTL. Memories are stored globally and persist across sessions.",
	}, s.handleMemoryRemember)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "memory_recall",
		Description: "Search memories semantically. Returns memories ranked by relevance to your query.",
	}, s.handleMemoryRecall)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "memory_forget",
		Description: "Delete memories by ID, tags, or age. Bulk deletion requires confirmation.",
	}, s.handleMemoryForget)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "memory_stats",
		Description: "Get memory store statistics including total count, tags, and age distribution.",
	}, s.handleMemoryStats)

	return s
}

// Run starts the MCP server.
func (s *SDKServer) Run(ctx context.Context) error {
	return s.server.Run(ctx, &sdkmcp.StdioTransport{})
}

// ensureInitialized attempts to auto-detect and activate a project if not already initialized.
func (s *SDKServer) ensureInitialized(ctx context.Context) error {
	if s.initialized {
		return nil
	}

	// Try to auto-detect project from current working directory
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("no vecgrep project found. Run vecgrep_init with the project path, or run 'vecgrep init' in the project directory first")
	}

	// Auto-activate the detected project
	_, _, err = s.activateProject(ctx, projectRoot)
	return err
}

// ensureMemoryInitialized initializes the memory store on first use.
func (s *SDKServer) ensureMemoryInitialized(ctx context.Context) error {
	s.memoryInitMu.Lock()
	defer s.memoryInitMu.Unlock()

	// Already initialized
	if s.memoryStore != nil {
		return nil
	}

	// Previous initialization failed
	if s.memoryInitErr != nil {
		return s.memoryInitErr
	}

	// Load memory config
	cfg := memory.DefaultConfig()

	// Create embedding provider for memory
	provider := embed.NewOllamaProvider(embed.OllamaConfig{
		URL:        cfg.OllamaURL,
		Model:      cfg.EmbeddingModel,
		Dimensions: cfg.EmbeddingDimensions,
	})

	// Check if provider is available
	if err := provider.Ping(ctx); err != nil {
		s.memoryInitErr = fmt.Errorf("embedding provider not available: %w. Ensure Ollama is running with nomic-embed-text model", err)
		return s.memoryInitErr
	}

	// Create memory store
	store, err := memory.NewMemoryStore(cfg, provider)
	if err != nil {
		s.memoryInitErr = fmt.Errorf("failed to initialize memory store: %w", err)
		return s.memoryInitErr
	}

	s.memoryStore = store
	return nil
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
		_ = s.db.Close()
	}

	// Open database
	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Create embedding provider based on config
	var provider embed.Provider
	switch cfg.Embedding.Provider {
	case "openai":
		provider = embed.NewOpenAIProvider(embed.OpenAIConfig{
			APIKey:     cfg.Embedding.OpenAIAPIKey,
			BaseURL:    cfg.Embedding.OpenAIBaseURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
		})
	default:
		provider = embed.NewOllamaProvider(embed.OllamaConfig{
			URL:        cfg.Embedding.OllamaURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
		})
	}

	// Update server state
	s.db = database
	s.provider = provider
	s.projectRoot = projectPath
	s.searcher = search.NewSearcher(database, provider)
	s.initialized = true

	// Get vector backend info
	vecVersion, _ := database.VecVersion()

	var sb strings.Builder
	fmt.Fprintf(&sb, "Activated vecgrep project: %s\n\n", projectPath)
	sb.WriteString("**IMPORTANT:** Add `.vecgrep` to your `.gitignore` file.\n\n")
	fmt.Fprintf(&sb, "- Data dir: %s\n", cfg.DataDir)
	fmt.Fprintf(&sb, "- Vector backend: %s\n", vecVersion)
	fmt.Fprintf(&sb, "- Embedding provider: %s (%s)\n", cfg.Embedding.Provider, cfg.Embedding.Model)

	// Get stats
	stats, err := s.searcher.GetIndexStats(ctx)
	if err == nil {
		if totalFiles, ok := stats["total_files"].(int64); ok && totalFiles > 0 {
			totalChunks, _ := stats["total_chunks"].(int64)
			fmt.Fprintf(&sb, "\nIndex stats: %d files, %d chunks\n", totalFiles, totalChunks)
		} else {
			sb.WriteString("\nNext step: Run vecgrep_index to index your codebase.")
		}
	} else {
		sb.WriteString("\nNext step: Run vecgrep_index to index your codebase.")
	}

	// Check for pending changes
	indexerCfg := index.DefaultIndexerConfig()
	indexerCfg.IgnorePatterns = append(cfg.Indexing.IgnorePatterns, indexerCfg.IgnorePatterns...)
	indexer := index.NewIndexer(database, nil, indexerCfg)

	pending, pendingErr := indexer.GetPendingChanges(ctx, projectPath)
	if pendingErr == nil && pending.TotalPending > 0 {
		fmt.Fprintf(&sb, "\n**Reindex needed:** %d files changed (%d new, %d modified, %d deleted)\n",
			pending.TotalPending, pending.NewFiles, pending.ModifiedFiles, pending.DeletedFiles)
		sb.WriteString("Run vecgrep_index to update the index.\n")
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// checkProvider verifies the embedding provider is available.
func (s *SDKServer) checkProvider(ctx context.Context) *sdkmcp.CallToolResult {
	if s.provider == nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Embedding provider not configured. Run vecgrep_init first."}},
			IsError: true,
		}
	}

	if err := s.provider.Ping(ctx); err != nil {
		var sb strings.Builder
		sb.WriteString("Embedding provider is not available.\n\n")

		// Check if it's an Ollama provider based on error message or model
		if _, ok := s.provider.(*embed.OllamaProvider); ok {
			sb.WriteString("To fix this (Ollama):\n")
			sb.WriteString("1. Install Ollama: https://ollama.ai\n")
			sb.WriteString("2. Start Ollama:\n")
			sb.WriteString("   OLLAMA_HOST=0.0.0.0 ollama serve\n")
			sb.WriteString("3. Pull the embedding model:\n")
			sb.WriteString("   ollama pull nomic-embed-text\n")
		} else if _, ok := s.provider.(*embed.OpenAIProvider); ok {
			sb.WriteString("To fix this (OpenAI):\n")
			sb.WriteString("1. Ensure OPENAI_API_KEY or VECGREP_OPENAI_API_KEY is set\n")
			sb.WriteString("2. Verify your API key is valid\n")
			sb.WriteString("3. Check your OpenAI account has available credits\n")
		} else {
			sb.WriteString("Verify your embedding provider is configured correctly.\n")
		}
		fmt.Fprintf(&sb, "\nError: %v", err)
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
			IsError: true,
		}
	}
	return nil
}

// handleSearch handles the vecgrep_search tool.
func (s *SDKServer) handleSearch(ctx context.Context, req *sdkmcp.CallToolRequest, input SearchInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	// Check embedding provider
	if errResult := s.checkProvider(ctx); errResult != nil {
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

	// Apply input options
	if input.Limit > 0 {
		opts.Limit = input.Limit
	}
	if input.Language != "" {
		opts.Language = input.Language
	}
	if len(input.Languages) > 0 {
		opts.Languages = input.Languages
	}
	if input.ChunkType != "" {
		opts.ChunkType = input.ChunkType
	}
	if len(input.ChunkTypes) > 0 {
		opts.ChunkTypes = input.ChunkTypes
	}
	if input.FilePattern != "" {
		opts.FilePattern = input.FilePattern
	}
	if input.Directory != "" {
		opts.Directory = input.Directory
	}
	if input.MinLine > 0 {
		opts.MinLine = input.MinLine
	}
	if input.MaxLine > 0 {
		opts.MaxLine = input.MaxLine
	}

	// Parse search mode
	switch strings.ToLower(input.Mode) {
	case "semantic":
		opts.Mode = search.SearchModeSemantic
	case "keyword":
		opts.Mode = search.SearchModeKeyword
	case "hybrid", "":
		opts.Mode = search.SearchModeHybrid
	default:
		opts.Mode = search.SearchModeHybrid
	}

	var sb strings.Builder

	// Perform search with or without explanation
	if input.Explain {
		results, explanation, err := s.searcher.SearchWithExplain(ctx, input.Query, opts)
		if err != nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Search error: %v", err)}},
				IsError: true,
			}, nil, nil
		}

		// Add explanation to output
		sb.WriteString("**Search Diagnostics:**\n")
		fmt.Fprintf(&sb, "- Index type: %s\n", explanation.IndexType)
		fmt.Fprintf(&sb, "- Nodes visited: %d\n", explanation.NodesVisited)
		fmt.Fprintf(&sb, "- Duration: %v\n", explanation.Duration)
		fmt.Fprintf(&sb, "- Mode: %s\n\n", explanation.Mode)

		// Expand context lines if requested
		if input.ContextLines > 0 {
			for i := range results {
				results[i].Content = expandContextLines(s.projectRoot, results[i], input.ContextLines)
			}
		}

		formatSearchResults(&sb, results)
	} else {
		results, err := s.searcher.Search(ctx, input.Query, opts)
		if err != nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Search error: %v", err)}},
				IsError: true,
			}, nil, nil
		}

		// Expand context lines if requested
		if input.ContextLines > 0 {
			for i := range results {
				results[i].Content = expandContextLines(s.projectRoot, results[i], input.ContextLines)
			}
		}

		formatSearchResults(&sb, results)
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// formatSearchResults formats search results into markdown.
func formatSearchResults(sb *strings.Builder, results []search.Result) {
	if len(results) == 0 {
		sb.WriteString("No results found.")
		return
	}

	fmt.Fprintf(sb, "Found %d results:\n\n", len(results))

	for i, r := range results {
		fmt.Fprintf(sb, "### Result %d (score: %.2f)\n", i+1, r.Score)
		fmt.Fprintf(sb, "**File:** %s (lines %d-%d)\n", r.RelativePath, r.StartLine, r.EndLine)
		if r.SymbolName != "" {
			fmt.Fprintf(sb, "**Symbol:** %s\n", r.SymbolName)
		}
		if r.Language != "" && r.Language != "unknown" {
			fmt.Fprintf(sb, "**Language:** %s\n", r.Language)
		}
		sb.WriteString("\n```")
		if r.Language != "" && r.Language != "unknown" {
			sb.WriteString(r.Language)
		}
		sb.WriteString("\n")
		sb.WriteString(r.Content)
		sb.WriteString("\n```\n\n")
	}
}

// handleIndex handles the vecgrep_index tool.
func (s *SDKServer) handleIndex(ctx context.Context, req *sdkmcp.CallToolRequest, input IndexInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	// Check Ollama
	if errResult := s.checkProvider(ctx); errResult != nil {
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
	fmt.Fprintf(&sb, "- Files processed: %d\n", result.FilesProcessed)
	fmt.Fprintf(&sb, "- Files skipped (unchanged): %d\n", result.FilesSkipped)
	fmt.Fprintf(&sb, "- Chunks created: %d\n", result.ChunksCreated)
	fmt.Fprintf(&sb, "- Duration: %s\n", result.Duration)

	if len(result.Errors) > 0 {
		fmt.Fprintf(&sb, "\nWarnings/Errors: %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Fprintf(&sb, "  - %v\n", e)
		}
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// handleStatus handles the vecgrep_status tool.
func (s *SDKServer) handleStatus(ctx context.Context, req *sdkmcp.CallToolRequest, input StatusInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
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
		fmt.Fprintf(&sb, "Total files: %d\n", totalFiles)
	}
	if totalChunks, ok := stats["total_chunks"].(int64); ok {
		fmt.Fprintf(&sb, "Total chunks: %d\n", totalChunks)
	}
	if langStats, ok := stats["languages"].(map[string]int64); ok {
		sb.WriteString("\nBy language:\n")
		for lang, count := range langStats {
			fmt.Fprintf(&sb, "  %s: %d\n", lang, count)
		}
	}

	// Check for pending changes
	cfg, cfgErr := config.Load(s.projectRoot)
	if cfgErr == nil {
		indexerCfg := index.DefaultIndexerConfig()
		indexerCfg.IgnorePatterns = append(cfg.Indexing.IgnorePatterns, indexerCfg.IgnorePatterns...)
		indexer := index.NewIndexer(s.db, nil, indexerCfg)

		pending, pendingErr := indexer.GetPendingChanges(ctx, s.projectRoot)
		if pendingErr == nil {
			sb.WriteString("\nReindex status:\n")
			fmt.Fprintf(&sb, "  New files: %d\n", pending.NewFiles)
			fmt.Fprintf(&sb, "  Modified files: %d\n", pending.ModifiedFiles)
			fmt.Fprintf(&sb, "  Deleted files: %d\n", pending.DeletedFiles)
			if pending.TotalPending > 0 {
				sb.WriteString("\n**Action needed:** Run vecgrep_index to update the index.\n")
			}
		}
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// handleSimilar handles the vecgrep_similar tool.
func (s *SDKServer) handleSimilar(ctx context.Context, req *sdkmcp.CallToolRequest, input SimilarInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	// Check Ollama
	if errResult := s.checkProvider(ctx); errResult != nil {
		return errResult, nil, nil
	}

	// Validate: exactly one of chunk_id, file_location, or text must be provided
	specCount := 0
	if input.ChunkID != 0 {
		specCount++
	}
	if input.FileLocation != "" {
		specCount++
	}
	if input.Text != "" {
		specCount++
	}

	if specCount == 0 {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Error: Provide exactly one of chunk_id, file_location, or text."}},
			IsError: true,
		}, nil, nil
	}
	if specCount > 1 {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Error: Provide only one of chunk_id, file_location, or text (not multiple)."}},
			IsError: true,
		}, nil, nil
	}

	// Build similar options with extended fields
	opts := search.SimilarOptions{
		SearchOptions: search.SearchOptions{
			Limit:       input.Limit,
			Language:    input.Language,
			Languages:   input.Languages,
			ChunkType:   input.ChunkType,
			ChunkTypes:  input.ChunkTypes,
			FilePattern: input.FilePattern,
			Directory:   input.Directory,
			MinLine:     input.MinLine,
			MaxLine:     input.MaxLine,
			ProjectRoot: s.projectRoot,
		},
		ExcludeSameFile: input.ExcludeSameFile,
		ExcludeSourceID: true, // Default to excluding source
	}

	if opts.Limit == 0 {
		opts.Limit = 10
	}

	var results []search.Result
	var err error

	if input.ChunkID != 0 {
		results, err = s.searcher.SearchSimilarByID(ctx, input.ChunkID, opts)
	} else if input.FileLocation != "" {
		// Parse file:line
		parts := strings.SplitN(input.FileLocation, ":", 2)
		if len(parts) != 2 {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Invalid file_location format: %s (expected 'file:line')", input.FileLocation)}},
				IsError: true,
			}, nil, nil
		}
		line, lineErr := strconv.Atoi(parts[1])
		if lineErr != nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Invalid line number in file_location: %s", input.FileLocation)}},
				IsError: true,
			}, nil, nil
		}
		results, err = s.searcher.SearchSimilarByLocation(ctx, parts[0], line, opts)
	} else if input.Text != "" {
		results, err = s.searcher.SearchSimilarByText(ctx, input.Text, opts)
	}

	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Search error: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Format results
	if len(results) == 0 {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "No similar code found."}},
		}, nil, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d similar code chunks:\n\n", len(results))

	for i, r := range results {
		fmt.Fprintf(&sb, "### Result %d (score: %.2f)\n", i+1, r.Score)
		fmt.Fprintf(&sb, "**File:** %s (lines %d-%d)\n", r.RelativePath, r.StartLine, r.EndLine)
		if r.SymbolName != "" {
			fmt.Fprintf(&sb, "**Symbol:** %s\n", r.SymbolName)
		}
		if r.Language != "" && r.Language != "unknown" {
			fmt.Fprintf(&sb, "**Language:** %s\n", r.Language)
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

// handleDelete handles the vecgrep_delete tool.
func (s *SDKServer) handleDelete(ctx context.Context, req *sdkmcp.CallToolRequest, input DeleteInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	if input.FilePath == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "file_path parameter is required"}},
			IsError: true,
		}, nil, nil
	}

	chunksDeleted, err := s.db.DeleteFile(ctx, input.FilePath)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to delete file: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Deleted %s (%d chunks removed)", input.FilePath, chunksDeleted)}},
	}, nil, nil
}

// handleClean handles the vecgrep_clean tool.
func (s *SDKServer) handleClean(ctx context.Context, req *sdkmcp.CallToolRequest, input CleanInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	stats, err := s.db.Clean(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to clean database: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	var sb strings.Builder
	sb.WriteString("Database cleanup complete:\n")
	fmt.Fprintf(&sb, "- Orphaned chunks removed: %d\n", stats.OrphanedChunks)
	fmt.Fprintf(&sb, "- Orphaned embeddings removed: %d\n", stats.OrphanedEmbeddings)
	if stats.VacuumedBytes > 0 {
		fmt.Fprintf(&sb, "- Space reclaimed: %d bytes\n", stats.VacuumedBytes)
	} else {
		sb.WriteString("- Database already optimized\n")
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// handleReset handles the vecgrep_reset tool.
func (s *SDKServer) handleReset(ctx context.Context, req *sdkmcp.CallToolRequest, input ResetInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	// Require confirmation
	if input.Confirm != "yes" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "WARNING: This will delete ALL indexed data. Set confirm='yes' to proceed."}},
			IsError: true,
		}, nil, nil
	}

	if err := s.db.ResetAll(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to reset database: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Database reset complete. All indexed data has been cleared.\nRun vecgrep_index to re-index your codebase."}},
	}, nil, nil
}
