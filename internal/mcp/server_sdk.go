// Package mcp implements the MCP server using the official SDK.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
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
	Local bool   `json:"local,omitempty" jsonschema:"Create local .vecgrep/ directory instead of centralized storage in ~/.vecgrep/projects/."`
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
	FilePaths    []string `json:"file_paths,omitempty" jsonschema:"Restrict search to these relative paths (allow-list). Used for blast-radius scoping from codemap impact."`
	Symbol       string   `json:"symbol,omitempty" jsonschema:"When set, uses codemap impact to compute the blast radius of this symbol and scopes the search to affected files. Falls back to unscoped search if codemap is unavailable."`
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

// InvestigateInput is the input for vecgrep_investigate.
type InvestigateInput struct {
	Symbol       string `json:"symbol" jsonschema:"The symbol to compute the blast radius for (e.g., 'pkg.FuncName' or 'FuncName'). codemap impact finds all files transitively affected by a change to this symbol."`
	Query        string `json:"query" jsonschema:"The semantic search query to run within the scoped file set."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum number of results to return (default: 10)."`
	Mode         string `json:"mode,omitempty" jsonschema:"Search mode: 'semantic' (vector only), 'keyword' (text only), or 'hybrid' (combined, default)."`
	ContextLines int    `json:"context_lines,omitempty" jsonschema:"Number of lines to include before and after each result (default: 0)."`
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
	IncludeStructure   bool `json:"include_structure,omitempty" jsonschema:"Include directory structure in output (default: true)."`
	IncludeEntryPoints bool `json:"include_entry_points,omitempty" jsonschema:"Include entry point files in output (default: true)."`
	MaxDirectoryDepth  int  `json:"max_directory_depth,omitempty" jsonschema:"Maximum directory depth to show (default: 3)."`
	IncludeKeyFiles    bool `json:"include_key_files,omitempty" jsonschema:"Include key files like README, config (default: true)."`
}

// BatchSearchInput is the input for vecgrep_batch_search.
type BatchSearchInput struct {
	Queries       []string `json:"queries" jsonschema:"List of queries to search for."`
	LimitPerQuery int      `json:"limit_per_query,omitempty" jsonschema:"Maximum results per query (default: 3)."`
	Deduplicate   *bool    `json:"deduplicate,omitempty" jsonschema:"Remove duplicate results across queries (default: true)."`
	Language      string   `json:"language,omitempty" jsonschema:"Filter results by programming language."`
	ChunkType     string   `json:"chunk_type,omitempty" jsonschema:"Filter results by chunk type."`
}

// RelatedFilesInput is the input for vecgrep_related_files.
type RelatedFilesInput struct {
	File         string `json:"file" jsonschema:"Path to the file to find related files for."`
	Relationship string `json:"relationship,omitempty" jsonschema:"Type of relationship: 'imports', 'imported_by', 'tests', 'all' (default: 'all')."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum number of related files to return (default: 10)."`
}

// BranchStatusInput is the input for vecgrep_branch_status (empty).
type BranchStatusInput struct{}

// SDKServer wraps the official MCP SDK server.
type SDKServer struct {
	server      *sdkmcp.Server
	session     *mcpSession   // lazy dual-handle DB session (replaces s.db, s.provider, s.searcher)
	daemon      *daemonClient // daemon socket client (nil if no daemon)
	projectRoot string
	initialized bool

	// Codemap integration client (nil when codemap is disabled or unavailable)
	codemap    *CodemapClient
	codemapCfg config.CodemapConfig

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
	Codemap     config.CodemapConfig
}

// NewSDKServer creates a new MCP server using the official SDK.
func NewSDKServer(cfg SDKServerConfig) *SDKServer {
	s := &SDKServer{
		projectRoot: cfg.ProjectRoot,
		initialized: cfg.DB != nil && cfg.ProjectRoot != "",
		codemap:     NewCodemapClient(cfg.Codemap),
		codemapCfg:  cfg.Codemap,
	}

	// If a pre-opened DB is provided (from the CLI serve command), wrap it
	// in an mcpSession that returns it as the cached RO handle. This keeps
	// backward compatibility with the existing serve command which opens
	// the session before starting the MCP server.
	if cfg.DB != nil && cfg.Provider != nil && cfg.ProjectRoot != "" {
		if resolved, err := config.LoadResolved(cfg.ProjectRoot); err == nil {
			sess := newMCPSession(resolved.Config, cfg.ProjectRoot, cfg.Provider)
			sess.ro = cfg.DB // pre-populate the cached RO handle
			sess.lastReload = time.Now()
			s.session = sess
		}
	}

	// Create the MCP server
	s.server = sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "vecgrep",
		Version: version.Version,
	}, &sdkmcp.ServerOptions{
		Instructions: "vecgrep provides semantic code search using vector embeddings. " +
			"By default, project data is stored centrally in ~/.vecgrep/projects/ so no .vecgrep folder is created in your project. " +
			"For projects with existing index (local or global), just use vecgrep_search directly - it auto-detects the project. " +
			"For new projects, run vecgrep_init with the project path, then vecgrep_index to index the codebase.",
	})

	// Register tools using typed handlers
	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_init",
		Description: "Initialize or activate vecgrep for a project. By default, registers the project centrally in ~/.vecgrep/projects/ (no local .vecgrep/ created). Use local=true to create a local .vecgrep/ directory instead. If an existing index is found (local or global), activates it. Search/index/status commands auto-detect projects, so this is only needed for new projects or switching directories.",
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
		Description: "Sync the vector database to disk and report current index statistics (record/file counts). Pure veclite storage has no orphaned rows to vacuum; this is a flush-and-report operation.",
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

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_branch_status",
		Description: "Show per-branch index status: current git branch, HEAD SHA, and all known branch indexes with their vector counts and snapshot IDs.",
	}, s.handleBranchStatus)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_investigate",
		Description: "Investigate a changed symbol's blast radius: runs codemap impact to find all affected files, then scopes a semantic search to that file set. Falls back to unscoped search when codemap is unavailable or not indexed.",
	}, s.handleInvestigate)

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

// provider returns the embedding provider from the active session, or nil
// if no session is active.
func (s *SDKServer) provider() embed.Provider {
	if s.session == nil {
		return nil
	}
	return s.session.provider
}

// roSearcher returns a *search.Searcher built from the read-only database
// handle and the embedding provider. It opens the RO handle if needed and
// reloads if stale. The searcher is created per-call (cheap struct) so it
// always reflects the latest database state.
func (s *SDKServer) roSearcher() (*search.Searcher, error) {
	database, err := s.session.readOnlyDB()
	if err != nil {
		return nil, err
	}
	_ = s.session.reloadIfStale()
	return search.NewSearcher(database, s.session.provider), nil
}

// roDB returns the read-only database handle, opening it if needed and
// reloading if stale.
func (s *SDKServer) roDB() (*db.DB, error) {
	database, err := s.session.readOnlyDB()
	if err != nil {
		return nil, err
	}
	_ = s.session.reloadIfStale()
	return database, nil
}

// rwDB returns a read-write database handle for write operations. The caller
// must call database.Close() after use to release the exclusive lock.
func (s *SDKServer) rwDB() (*db.DB, error) {
	return s.session.readWriteDB()
}

// ensureInitialized attempts to auto-detect and activate a project if not already initialized.
func (s *SDKServer) ensureInitialized(ctx context.Context) error {
	if s.initialized {
		return nil
	}

	// Try to auto-detect project from current working directory
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		// Not found via local markers or global config - try to auto-register globally
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return fmt.Errorf("no vecgrep project found. Run vecgrep_init with the project path, or run 'vecgrep init' in the project directory first")
		}

		// Auto-register the cwd globally
		if regErr := config.AddProjectToGlobal(cwd, ""); regErr != nil {
			return fmt.Errorf("no vecgrep project found and auto-register failed: %w. Run vecgrep_init with the project path", regErr)
		}

		// Create the data directory
		name, _, _ := config.FindProjectByPath(cwd)
		dataDir, ddErr := config.GetProjectDataDir(name)
		if ddErr != nil {
			return fmt.Errorf("failed to get project data directory: %w", ddErr)
		}
		if mkErr := os.MkdirAll(dataDir, 0755); mkErr != nil {
			return fmt.Errorf("failed to create data directory: %w", mkErr)
		}

		projectRoot = cwd
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

	// Local mode: create .vecgrep/ directory inside the project
	if input.Local {
		return s.handleInitLocal(ctx, path, input.Force)
	}

	// Global mode (default): register in ~/.vecgrep/projects/
	return s.handleInitGlobal(ctx, path, input.Force)
}

// handleInitLocal creates a local .vecgrep/ directory inside the project.
func (s *SDKServer) handleInitLocal(ctx context.Context, path string, force bool) (*sdkmcp.CallToolResult, any, error) {
	dataDir := filepath.Join(path, config.DefaultDataDir)

	// Check if already initialized locally
	if _, err := os.Stat(dataDir); err == nil && !force {
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

// handleInitGlobal registers a project in ~/.vecgrep/projects/.
func (s *SDKServer) handleInitGlobal(ctx context.Context, path string, force bool) (*sdkmcp.CallToolResult, any, error) {
	// Check if already registered globally
	existingName, existingEntry, _ := config.FindProjectByPath(path)
	if existingEntry != nil && !force {
		// Already registered - just activate
		return s.activateProject(ctx, path)
	}
	_ = existingName

	// Register in global config
	if err := config.AddProjectToGlobal(path, ""); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to register project globally: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Get the derived name and data directory
	name, _, _ := config.FindProjectByPath(path)
	dataDir, err := config.GetProjectDataDir(name)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to get project data directory: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Create data directory
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to create data directory: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Activate the project
	return s.activateProject(ctx, path)
}

// activateProject configures the server for the given project. The database
// is NOT opened here — it opens lazily on the first read or write tool call.
func (s *SDKServer) activateProject(ctx context.Context, projectPath string) (*sdkmcp.CallToolResult, any, error) {
	// Load resolved config to know if we're in global mode
	resolved, err := config.LoadResolved(projectPath)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to load config: %v", err)}},
			IsError: true,
		}, nil, nil
	}
	cfg := resolved.Config

	// Close existing session if open
	if s.session != nil {
		_ = s.session.close()
	}

	// Create embedding provider based on config
	provider, err := app.NewProvider(cfg)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to create embedding provider: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Create the lazy MCP session (no DB opened yet)
	s.session = newMCPSession(cfg, projectPath, provider)
	s.daemon = newDaemonClient(cfg.DataDir)

	// Update server state
	s.projectRoot = projectPath
	s.initialized = true
	s.codemap = NewCodemapClient(cfg.Codemap)
	s.codemapCfg = cfg.Codemap

	var sb strings.Builder
	fmt.Fprintf(&sb, "Activated vecgrep project: %s\n\n", projectPath)
	// Only show .gitignore warning when using local mode
	if !resolved.IsGlobalMode {
		sb.WriteString("**IMPORTANT:** Add `.vecgrep` to your `.gitignore` file.\n\n")
	}
	fmt.Fprintf(&sb, "- Data dir: %s\n", cfg.DataDir)
	fmt.Fprintf(&sb, "- Embedding provider: %s (%s)\n", cfg.Embedding.Provider, cfg.Embedding.Model)

	// Try to get vector backend info and stats via a temporary RO open (if DB exists)
	if s.session.hasDatabase() {
		if database, err := s.session.readOnlyDB(); err == nil {
			vecVersion, _ := database.VecVersion()
			fmt.Fprintf(&sb, "- Vector backend: %s\n", vecVersion)

			searcher := search.NewSearcher(database, provider)
			stats, err := searcher.GetIndexStats(ctx)
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
		} else {
			sb.WriteString("\nNext step: Run vecgrep_index to index your codebase.")
		}
	} else {
		sb.WriteString("\nNext step: Run vecgrep_index to index your codebase.")
	}

	// Note: search uses a read-only shared lock; writes acquire an exclusive
	// lock on demand and release it when done.
	sb.WriteString("\nSearch: read-only (shared lock). Index/delete: write lock on demand.")

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// checkProvider verifies the embedding provider is available.
func (s *SDKServer) checkProvider(ctx context.Context) *sdkmcp.CallToolResult {
	p := s.provider()
	if p == nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Embedding provider not configured. Run vecgrep_init first."}},
			IsError: true,
		}
	}

	if err := p.Ping(ctx); err != nil {
		var sb strings.Builder
		sb.WriteString("Embedding provider is not available.\n\n")

		// Check if it's an Ollama provider based on error message or model
		if _, ok := p.(*embed.OllamaProvider); ok {
			sb.WriteString("To fix this (Ollama):\n")
			sb.WriteString("1. Install Ollama: https://ollama.ai\n")
			sb.WriteString("2. Start Ollama:\n")
			sb.WriteString("   OLLAMA_HOST=0.0.0.0 ollama serve\n")
			sb.WriteString("3. Pull the embedding model:\n")
			sb.WriteString("   ollama pull nomic-embed-text\n")
		} else if _, ok := p.(*embed.OpenAIProvider); ok {
			sb.WriteString("To fix this (OpenAI):\n")
			sb.WriteString("1. Ensure OPENAI_API_KEY or VECGREP_OPENAI_API_KEY is set\n")
			sb.WriteString("2. Verify your API key is valid\n")
			sb.WriteString("3. Check your OpenAI account has available credits\n")
		} else if _, ok := p.(*embed.CohereProvider); ok {
			sb.WriteString("To fix this (Cohere):\n")
			sb.WriteString("1. Ensure COHERE_API_KEY or VECGREP_COHERE_API_KEY is set\n")
			sb.WriteString("2. Verify your API key is valid\n")
			sb.WriteString("3. Check your Cohere account has available credits\n")
		} else if _, ok := p.(*embed.VoyageProvider); ok {
			sb.WriteString("To fix this (Voyage):\n")
			sb.WriteString("1. Ensure VOYAGE_API_KEY or VECGREP_VOYAGE_API_KEY is set\n")
			sb.WriteString("2. Verify your API key is valid\n")
			sb.WriteString("3. Check your Voyage account has available credits\n")
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

// formatDaemonSearchResult formats the JSON result from a daemon.search
// socket call into the same text format as the direct search path. The
// result JSON has the shape {"results": [...], "mode": "..."}.
func formatDaemonSearchResult(raw json.RawMessage, scopeNote string) string {
	var resp struct {
		Results []search.Result `json:"results"`
		Mode    string          `json:"mode"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Sprintf("daemon search result parse error: %v", err)
	}

	var sb strings.Builder
	if scopeNote != "" {
		sb.WriteString(scopeNote)
		sb.WriteString("\n\n")
	}
	formatSearchResults(&sb, resp.Results)
	return sb.String()
}

// formatStatsResult formats the JSON stats result from a daemon.stats socket
// call into the same text format as the direct status path.
func formatStatsResult(raw json.RawMessage, projectRoot string) string {
	var stats map[string]int64
	if err := json.Unmarshal(raw, &stats); err != nil {
		return fmt.Sprintf("stats parse error: %v", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Index status for: %s\n\n", projectRoot)

	if totalFiles, ok := stats["total_files"]; ok {
		fmt.Fprintf(&sb, "Total files: %d\n", totalFiles)
	}
	if totalChunks, ok := stats["total_chunks"]; ok {
		fmt.Fprintf(&sb, "Total chunks: %d\n", totalChunks)
	}
	return sb.String()
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

	// Apply file scoping. Direct file_paths take precedence; otherwise,
	// when symbol is set, resolve the blast radius via codemap impact.
	scopeFiles, scopeNote := s.resolveSearchScope(ctx, input)
	if len(scopeFiles) > 0 {
		opts.FilePaths = scopeFiles
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

	// Report scoping status so the caller knows whether codemap was used
	if scopeNote != "" {
		sb.WriteString(scopeNote)
		sb.WriteString("\n\n")
	}

	// Try daemon socket first (warm session, no lock needed)
	if s.daemon != nil && s.daemon.available() {
		params := daemonSearchParams{
			Query:       input.Query,
			Limit:       opts.Limit,
			Mode:        input.Mode,
			Language:    input.Language,
			Languages:   input.Languages,
			ChunkTypes:  input.ChunkTypes,
			ChunkType:   input.ChunkType,
			FilePattern: input.FilePattern,
			Directory:   input.Directory,
			MinLine:     input.MinLine,
			MaxLine:     input.MaxLine,
			Explain:     input.Explain,
			FilePaths:   opts.FilePaths,
		}
		rawResult, dErr := s.daemon.search(ctx, params)
		if dErr == nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: formatDaemonSearchResult(rawResult, scopeNote)}},
			}, nil, nil
		}
		// fall through to RO session on daemon error
	}

	// Get a read-only searcher (opens RO handle if needed, reloads if stale)
	searcher, err := s.roSearcher()
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Perform search with or without explanation
	if input.Explain {
		results, explanation, err := searcher.SearchWithExplain(ctx, input.Query, opts)
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

		s.rerankWithCodemap(ctx, results, s.codemapStructuralWeight())
		formatSearchResults(&sb, results)
		s.annotateSearchHits(ctx, results, input.Query)
	} else {
		results, err := searcher.Search(ctx, input.Query, opts)
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

		s.rerankWithCodemap(ctx, results, s.codemapStructuralWeight())
		formatSearchResults(&sb, results)
		s.annotateSearchHits(ctx, results, input.Query)
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

	// If the daemon is running, delegate reindexing to it (async).
	if s.daemon != nil && s.daemon.available() {
		if err := s.daemon.reindex(ctx); err == nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Reindexing in background via daemon. Run vecgrep_status to check progress."}},
			}, nil, nil
		}
		// fall through to direct RW open on error
	}

	// Check embedding provider
	if errResult := s.checkProvider(ctx); errResult != nil {
		return errResult, nil, nil
	}

	// Open RW database (exclusive lock, per-call — caller closes)
	database, err := s.rwDB()
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database for writing: %s", formatLockError(err))}},
			IsError: true,
		}, nil, nil
	}
	defer database.Close()

	// Create indexer
	cfg := index.DefaultIndexerConfig()
	indexer := index.NewIndexer(database, s.provider(), cfg)

	var result *index.IndexResult
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

	// G4: after reindexing vecgrep, surface whether codemap also has a graph
	// for this project and whether it's drifted — so the agent knows to keep
	// the peer graph in sync. Degrades silently when codemap is absent.
	if cfg, cfgErr := config.Load(s.projectRoot); cfgErr == nil && cfg.Codemap.Enabled {
		s.writeCodemapStatus(ctx, &sb)
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

	// Try daemon socket first for stats (warm session, no lock needed)
	if s.daemon != nil && s.daemon.available() {
		if rawStats, dErr := s.daemon.stats(ctx); dErr == nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: formatStatsResult(rawStats, s.projectRoot)}},
			}, nil, nil
		}
		// fall through to RO session
	}

	// Get stats via RO searcher
	searcher, err := s.roSearcher()
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	stats, err := searcher.GetIndexStats(ctx)
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
		// Use the RO database for pending changes check
		roDatabase, _ := s.roDB()
		if roDatabase != nil {
			indexer := index.NewIndexer(roDatabase, nil, indexerCfg)
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

		// Report codemap integration status (G4 cross-read).
		if cfg.Codemap.Enabled {
			s.writeCodemapStatus(ctx, &sb)
		}
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// writeCodemapStatus reports the peer codemap graph's state (G4 cross-read):
// whether codemap also has a graph for this project, its size, and — when the
// graph has drifted from the working tree — a hint to reindex it. It shells
// `codemap status --json` (one hop, CLI-only) and degrades silently: an absent
// or erroring codemap simply reports "not found" / "not indexed", never an
// error to the caller. Used by both vecgrep_status and vecgrep_index so an
// agent knows before delegating whether the peer can answer.
func (s *SDKServer) writeCodemapStatus(ctx context.Context, sb *strings.Builder) {
	sb.WriteString("\nCodemap integration: enabled\n")
	if s.codemap == nil || !s.codemap.Available() {
		sb.WriteString("  Status: codemap binary not found\n")
		return
	}
	sb.WriteString("  Status: connected\n")
	status, _ := s.codemap.Status(ctx, s.projectRoot)
	if !status.Indexed() {
		sb.WriteString("  Graph: not indexed (run 'codemap index' to build)\n")
		return
	}
	fmt.Fprintf(sb, "  Graph: %d nodes, %d edges", status.Nodes, status.Edges)
	if st := status.Stale; st.Any() {
		fmt.Fprintf(sb, " (stale: %d changed / %d new / %d deleted)\n", st.Changed, st.New, st.Deleted)
		sb.WriteString("  **Hint:** codemap's graph is stale — run 'codemap index' to refresh it before trusting graph answers.\n")
	} else {
		sb.WriteString(" (fresh)\n")
	}
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

	// Get RO searcher
	searcher, sErr := s.roSearcher()
	if sErr != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", sErr)}},
			IsError: true,
		}, nil, nil
	}

	if input.ChunkID != 0 {
		results, err = searcher.SearchSimilarByID(ctx, input.ChunkID, opts)
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
		results, err = searcher.SearchSimilarByLocation(ctx, parts[0], line, opts)
	} else if input.Text != "" {
		results, err = searcher.SearchSimilarByText(ctx, input.Text, opts)
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

	s.annotateSearchHits(ctx, results, "similar")

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

	// Open RW database (exclusive lock, per-call)
	database, err := s.rwDB()
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database for writing: %s", formatLockError(err))}},
			IsError: true,
		}, nil, nil
	}
	defer database.Close()

	chunksDeleted, err := database.DeleteFile(ctx, input.FilePath)
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

	// Open RW database (exclusive lock, per-call)
	database, err := s.rwDB()
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database for writing: %s", formatLockError(err))}},
			IsError: true,
		}, nil, nil
	}
	defer database.Close()

	stats, err := database.Clean(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to clean database: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	var sb strings.Builder
	sb.WriteString("Database sync complete:\n")
	if stats.Synced {
		sb.WriteString("- Database flushed to disk\n")
	}
	fmt.Fprintf(&sb, "- Records: %d\n", stats.TotalRecords)
	fmt.Fprintf(&sb, "- Files:   %d\n", stats.TotalFiles)
	// Legacy orphan fields are always zero with veclite-only storage; only
	// surface them if a future backend reports real reclamation work.
	if stats.OrphanedChunks > 0 || stats.VacuumedBytes > 0 {
		fmt.Fprintf(&sb, "- Orphaned chunks removed: %d\n", stats.OrphanedChunks)
	}
	if stats.VacuumedBytes > 0 {
		fmt.Fprintf(&sb, "- Space reclaimed: %d bytes\n", stats.VacuumedBytes)
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

	// Open RW database (exclusive lock, per-call)
	database, err := s.rwDB()
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database for writing: %s", formatLockError(err))}},
			IsError: true,
		}, nil, nil
	}
	defer database.Close()

	if err := database.ResetAll(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to reset database: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Database reset complete. All indexed data has been cleared.\nRun vecgrep_index to re-index your codebase."}},
	}, nil, nil
}

// handleBranchStatus handles the vecgrep_branch_status tool.
func (s *SDKServer) handleBranchStatus(ctx context.Context, req *sdkmcp.CallToolRequest, input BranchStatusInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	idx, info, err := app.BranchStatus(ctx, s.projectRoot, s.projectName())
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Branch status error: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	var sb strings.Builder
	sb.WriteString("Branch Index Status:\n\n")
	sb.WriteString("Git:\n")
	if info != nil {
		fmt.Fprintf(&sb, "  Repo root: %s\n", info.Root)
		if info.Detached {
			fmt.Fprintf(&sb, "  HEAD: detached (%s)\n", info.Head)
		} else {
			fmt.Fprintf(&sb, "  Branch: %s\n", info.Branch)
			fmt.Fprintf(&sb, "  HEAD: %s\n", info.Head)
		}
	} else {
		sb.WriteString("  Not a git repository\n")
	}

	sb.WriteString("\nBranch Indexes:\n")
	if idx == nil || len(idx.Branches) == 0 {
		sb.WriteString("  No branch indexes found.\n")
	} else {
		fmt.Fprintf(&sb, "  Active: %s\n", idx.ActiveBranch)
		for name, entry := range idx.Branches {
			fmt.Fprintf(&sb, "\n  %s:\n", name)
			fmt.Fprintf(&sb, "    Base SHA: %s\n", entry.BaseSHA)
			fmt.Fprintf(&sb, "    Vectors: %d\n", entry.VectorCount)
			if entry.StashID != "" {
				fmt.Fprintf(&sb, "    Stash ID: %s\n", entry.StashID)
			}
		}
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// projectName returns the current project name from the resolved config.
func (s *SDKServer) projectName() string {
	if s.session == nil {
		return ""
	}
	resolved, err := config.LoadResolved(s.projectRoot)
	if err != nil || resolved == nil {
		return ""
	}
	return resolved.ProjectName
}

// annotateSearchHits pins search/similar results as codemap annotations
// so they persist across reindex and surface in codemap's context views.
// This is best-effort: errors are silently ignored and never affect the
// search response returned to the caller.
//
// Targeting (F3): instead of trusting vecgrep's regex-extracted SymbolName —
// which can be empty or collide on a bare name — we resolve each hit's
// file:start_line to the *correct* enclosing graph symbol via codemap's
// symbol-at (C2). We annotate that resolved FQN-anchored symbol and skip the
// hit entirely when codemap can't place the position (resolution "none"), so
// we never write a durable garbage pin.
func (s *SDKServer) annotateSearchHits(ctx context.Context, results []search.Result, query string) {
	if s.codemap == nil || !s.codemap.Available() || len(results) == 0 {
		return
	}
	// Use a background context so annotation outlives the request
	annotateCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, r := range results {
		// Resolve the hit's position to the enclosing symbol. We pin the
		// graph-resolved symbol, not the regex-extracted name.
		sa, err := s.codemap.SymbolAt(annotateCtx, s.projectRoot, r.RelativePath, r.StartLine)
		if err != nil || !sa.Resolved() {
			// Project not indexed, position off any symbol, or codemap
			// unavailable → skip rather than pin a guess.
			continue
		}
		note := fmt.Sprintf("vecgrep semantic hit (score %.2f): %s", r.Score, truncate(query, 120))
		_ = s.codemap.Annotate(annotateCtx, s.projectRoot, sa.Symbol, note, "vecgrep", map[string]any{
			"score":      r.Score,
			"query":      query,
			"file":       r.RelativePath,
			"start_line": r.StartLine,
			"end_line":   r.EndLine,
			"language":   r.Language,
			"chunk_type": r.ChunkType,
			"resolution": sa.Resolution,
			"fqn":        sa.FQN,
		})
	}
}

// truncate clips a string to n characters, appending an ellipsis if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "..."
}

// codemapStructuralWeight returns the configured structural re-ranking
// weight, defaulting to 0.15 when not explicitly set.
func (s *SDKServer) codemapStructuralWeight() float32 {
	if s.codemapCfg.StructuralWeight > 0 {
		return s.codemapCfg.StructuralWeight
	}
	return 0.15
}

// rerankWithCodemap re-orders search results using codemap's structural
// importance data (fan-in hub scores). The re-ranked results are written
// back into the slice in-place. This is best-effort: if codemap is
// unavailable or returns no data, results are left in their original order.
func (s *SDKServer) rerankWithCodemap(ctx context.Context, results []search.Result, structuralWeight float32) {
	if s.codemap == nil || !s.codemap.Available() || structuralWeight <= 0 || len(results) <= 1 {
		return
	}

	rerankInput := make([]CodemapRerankResult, len(results))
	for i, r := range results {
		rerankInput[i] = CodemapRerankResult{
			Result: codemapSearchResult{
				RelativePath: r.RelativePath,
				SymbolName:   r.SymbolName,
				StartLine:    r.StartLine,
				Score:        r.Score,
			},
		}
	}

	reranked := s.codemap.Rerank(ctx, s.projectRoot, rerankInput, structuralWeight)

	// Reorder the original results slice to match reranked order.
	// We find each reranked item's original index by matching path+line.
	used := make([]bool, len(results))
	reordered := make([]search.Result, 0, len(results))
	for _, rr := range reranked {
		for j, orig := range results {
			if used[j] {
				continue
			}
			if orig.RelativePath == rr.Result.RelativePath && orig.StartLine == rr.Result.StartLine {
				reordered = append(reordered, orig)
				used[j] = true
				break
			}
		}
	}
	// Append any unmatched results at the end
	for j, u := range used {
		if !u {
			reordered = append(reordered, results[j])
		}
	}
	copy(results, reordered)
}

// resolveSearchScope determines the file allow-list for a search. When
// input.FilePaths is set, it is used directly. When input.Symbol is set,
// codemap impact is called to compute the blast radius and the affected file
// set becomes the scope. Returns (files, note) where note is a human-readable
// status line for the caller's output (empty when no scoping was requested).
// Degrades silently: if codemap is unavailable or unindexed, returns an empty
// file list and a note explaining the fallback.
func (s *SDKServer) resolveSearchScope(ctx context.Context, input SearchInput) (files []string, note string) {
	// Direct file_paths take precedence
	if len(input.FilePaths) > 0 {
		return input.FilePaths, fmt.Sprintf("**Scope:** restricted to %d file(s)", len(input.FilePaths))
	}

	if input.Symbol == "" {
		return nil, ""
	}

	if s.codemap == nil || !s.codemap.Available() {
		return nil, fmt.Sprintf("**Scope:** symbol %q requested but codemap is unavailable — searching unscoped", input.Symbol)
	}

	depth := int(s.codemapCfg.ImpactDepth)
	result, err := s.codemap.Impact(ctx, s.projectRoot, input.Symbol, depth)
	if err != nil {
		return nil, fmt.Sprintf("**Scope:** codemap impact failed for %q (%v) — searching unscoped", input.Symbol, err)
	}
	if !result.Indexed {
		return nil, fmt.Sprintf("**Scope:** codemap graph not indexed for %q — searching unscoped", input.Symbol)
	}

	// Extract relative paths from the blast radius
	for _, f := range result.Files {
		if f.RelativePath != "" {
			files = append(files, f.RelativePath)
		}
	}

	if len(files) == 0 {
		return nil, fmt.Sprintf("**Scope:** codemap impact for %q returned no affected files — searching unscoped", input.Symbol)
	}

	return files, fmt.Sprintf("**Scope:** codemap impact for %q — %d file(s) in blast radius (radius: %d)", input.Symbol, len(files), result.BlastRadius)
}

// handleInvestigate handles the vecgrep_investigate tool. It runs codemap
// impact to compute the blast radius of a symbol, then scopes a semantic
// search to the affected file set. When codemap is unavailable or not
// indexed, it falls back to an unscoped search with a note.
func (s *SDKServer) handleInvestigate(ctx context.Context, req *sdkmcp.CallToolRequest, input InvestigateInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	if errResult := s.checkProvider(ctx); errResult != nil {
		return errResult, nil, nil
	}

	if input.Symbol == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "symbol parameter is required"}},
			IsError: true,
		}, nil, nil
	}
	if input.Query == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "query parameter is required"}},
			IsError: true,
		}, nil, nil
	}

	// Build search options from the investigate input
	opts := search.DefaultSearchOptions()
	opts.ProjectRoot = s.projectRoot
	if input.Limit > 0 {
		opts.Limit = input.Limit
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

	// Resolve blast radius scope via codemap
	scopeInput := SearchInput{
		Symbol: input.Symbol,
	}
	scopeFiles, scopeNote := s.resolveSearchScope(ctx, scopeInput)
	if len(scopeFiles) > 0 {
		opts.FilePaths = scopeFiles
	}

	var sb strings.Builder

	// Report scoping status
	if scopeNote != "" {
		sb.WriteString(scopeNote)
		sb.WriteString("\n\n")
	}

	// Perform the scoped search via RO searcher
	searcher, sErr := s.roSearcher()
	if sErr != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", sErr)}},
			IsError: true,
		}, nil, nil
	}
	results, err := searcher.Search(ctx, input.Query, opts)
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

	s.rerankWithCodemap(ctx, results, s.codemapStructuralWeight())
	formatSearchResults(&sb, results)
	s.annotateSearchHits(ctx, results, input.Query)

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}
