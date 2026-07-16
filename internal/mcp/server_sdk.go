// Package mcp implements the MCP server using the official SDK.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
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
	MinScore     float32  `json:"min_score,omitempty" jsonschema:"Drop matches below this score. Hybrid and semantic scores are 0-1 similarities; keyword scores (including hybrid degraded by an unavailable embedder) are raw BM25 on a different scale, where a 0-1 threshold is not meaningful."`
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
	server *sdkmcp.Server

	// stateMu guards the activation state below. MCP tool handlers run
	// concurrently (the SDK dispatches every tools/call on its own goroutine),
	// and activateProject swaps the session/daemon/project on the fly, so these
	// fields must not be read and written without synchronization. Multi-field
	// handlers must use the project snapshot helpers below.
	stateMu     sync.RWMutex
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

	statusSnapshotHook func(projectReadSnapshot)          // tests only
	readSnapshotHook   func(string, projectReadSnapshot)  // tests only
	stateSnapshotHook  func(string, projectStateSnapshot) // tests only
}

// projectStateSnapshot is one coherent view of the active project. Tool calls
// that combine the root, session/DB, daemon, and codemap configuration must
// capture this once instead of using independent accessors while another MCP
// request may activate a different project.
type projectStateSnapshot struct {
	session     *mcpSession
	daemon      *daemonClient
	projectRoot string
	projectName string
	initialized bool
	cfg         *config.Config
	provider    embed.Provider
	codemap     *CodemapClient
	codemapCfg  config.CodemapConfig
}

type projectReadSnapshot struct {
	projectStateSnapshot
	database *db.DB
	searcher *search.Searcher
	release  func()
}

type projectOperationSnapshot struct {
	projectStateSnapshot
	release func()
}

func (s *SDKServer) snapshotProjectState() projectStateSnapshot {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return projectStateSnapshot{
		session:     s.session,
		daemon:      s.daemon,
		projectRoot: s.projectRoot,
		projectName: projectName(s.session),
		initialized: s.initialized,
		cfg:         projectConfig(s.session),
		provider:    projectProvider(s.session),
		codemap:     s.codemap,
		codemapCfg:  s.codemapCfg,
	}
}

func projectConfig(session *mcpSession) *config.Config {
	if session == nil {
		return nil
	}
	return session.cfg
}

func projectProvider(session *mcpSession) embed.Provider {
	if session == nil {
		return nil
	}
	return session.provider
}

func projectName(session *mcpSession) string {
	if session == nil {
		return ""
	}
	return session.projectName
}

// acquireProjectOperationSnapshot pins one activation without opening its
// database. This is the daemon-first path: it keeps the session/provider alive
// across scope resolution and the socket request, then can safely upgrade the
// same activation to a read lease if the daemon call fails.
func (s *SDKServer) acquireProjectOperationSnapshot() (projectOperationSnapshot, error) {
	s.stateMu.RLock()
	state := projectStateSnapshot{
		session:     s.session,
		daemon:      s.daemon,
		projectRoot: s.projectRoot,
		projectName: projectName(s.session),
		initialized: s.initialized,
		cfg:         projectConfig(s.session),
		provider:    projectProvider(s.session),
		codemap:     s.codemap,
		codemapCfg:  s.codemapCfg,
	}
	if !state.initialized || state.session == nil || state.projectRoot == "" || state.cfg == nil {
		s.stateMu.RUnlock()
		return projectOperationSnapshot{}, fmt.Errorf("no active session")
	}
	if err := state.session.beginOperation(); err != nil {
		s.stateMu.RUnlock()
		return projectOperationSnapshot{}, err
	}
	s.stateMu.RUnlock()
	var once sync.Once
	return projectOperationSnapshot{
		projectStateSnapshot: state,
		release: func() {
			once.Do(state.session.endOperation)
		},
	}, nil
}

func (state projectOperationSnapshot) acquireRead(ctx context.Context) (projectReadSnapshot, error) {
	database, release, err := state.session.acquireROContextForOperation(ctx)
	if err != nil {
		return projectReadSnapshot{}, err
	}
	if rErr := state.session.reloadIfStale(); rErr != nil {
		log.Printf("vecgrep: read-only index reload failed: %v (search may reflect stale persisted data)", rErr)
	}
	return projectReadSnapshot{
		projectStateSnapshot: state.projectStateSnapshot,
		database:             database,
		searcher:             search.NewSearcher(database, state.provider),
		release:              release,
	}, nil
}

// sess returns the active DB session under the state read lock (nil if none).
func (s *SDKServer) sess() *mcpSession {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.session
}

// isInitialized reports whether a project is active, under the read lock.
func (s *SDKServer) isInitialized() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.initialized
}

// SDKServerConfig contains configuration for the SDK-based MCP server.
type SDKServerConfig struct {
	DB *db.DB
	// Provider ownership transfers to the server and is released with the
	// active MCP session. Leave nil to construct one from project config.
	Provider    embed.Provider
	ProjectRoot string
	Codemap     config.CodemapConfig
}

// NewSDKServer creates a new MCP server using the official SDK.
func NewSDKServer(cfg SDKServerConfig) *SDKServer {
	s := &SDKServer{
		projectRoot: cfg.ProjectRoot,
		codemap:     NewCodemapClient(cfg.Codemap),
		codemapCfg:  cfg.Codemap,
	}

	// When the project is known up front, set up a lazy session and daemon
	// client WITHOUT opening the database. The DB opens lazily on the first
	// read/write tool call — or read/write routes through a running daemon over
	// its socket — so `vecgrep serve --mcp` never holds a file lock while idle.
	//
	// The previous behavior wrapped a pre-opened *writable* session as the
	// cached RO handle, which held an exclusive lock for the entire lifetime of
	// the server. That blocked `vecgrep daemon start` and every other reader
	// (and never wired up the daemon client, so it never used the socket).
	if cfg.ProjectRoot != "" {
		if resolved, err := config.LoadResolved(cfg.ProjectRoot); err == nil {
			provider := cfg.Provider
			if provider == nil {
				provider, _ = app.NewProvider(resolved.Config)
			}
			if provider != nil {
				s.session = newMCPSession(resolved.Config, cfg.ProjectRoot, provider)
				s.session.projectName = resolved.ProjectName
				s.daemon = newDaemonClient(hubDataDir(), cfg.ProjectRoot)
				s.initialized = true
			}

			// Re-resolve the codemap client from the project's fully resolved
			// config, not the (usually zero-value) cfg.Codemap passed into
			// SDKServerConfig. `vecgrep serve` always starts with just a
			// ProjectRoot (see cmd/vecgrep/main.go runServe) and leaves
			// SDKServerConfig.Codemap unset, so the codemap:NewCodemapClient(...)
			// call above always builds a nil client — even when codemap is
			// installed and codemapDetect() would have auto-enabled it in
			// resolved.Config.Codemap. Without this, every tool call reports
			// "Codemap integration: enabled / Status: codemap binary not
			// found" regardless of whether codemap is actually reachable,
			// because s.codemap never gets a second look. activateProject
			// (used when a project is attached after startup) already does
			// this same re-resolution; this mirrors it for the up-front path.
			s.codemap = NewCodemapClient(resolved.Config.Codemap)
			s.codemapCfg = resolved.Config.Codemap
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
		Description: "Perform semantic search across the indexed codebase. Auto-detects the project from current directory. Supports three search modes: 'semantic' (vector similarity), 'keyword' (text matching), or 'hybrid' (combined, default). Returns code chunks ranked by relevance. Scores are 0-1 similarities in hybrid mode (calibrated cosine+BM25 fusion; good matches typically 0.45-0.69) and semantic mode (raw cosine); keyword mode returns raw BM25 scores (unbounded, different scale). If the embedding provider is unavailable, hybrid degrades to keyword-only and the result starts with an explicit warning — degraded scores are raw BM25, so min_score is effectively a no-op there.",
	}, s.handleSearch)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_index",
		Description: "Index files in the project for semantic search. Only indexes files that have changed since the last index.",
	}, s.handleIndex)

	sdkmcp.AddTool(s.server, &sdkmcp.Tool{
		Name:        "vecgrep_status",
		Description: "Get index statistics and conservative freshness evidence. Freshness is proven from raw source hashes, the last successful ingestion receipt, and codemap's bounded structural manifest when applicable; legacy or mismatched evidence reports unknown.",
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

// idleEvictInterval is how often the background evictor checks whether the
// cached read-only handle has been idle long enough to release its shared lock.
const idleEvictInterval = 10 * time.Second

// Run starts the MCP server.
func (s *SDKServer) Run(ctx context.Context) error {
	stopEvictor := s.startIdleEvictor(ctx)
	runErr := s.server.Run(ctx, &sdkmcp.StdioTransport{})
	stopEvictor()
	state := s.snapshotProjectState()
	var closeErr error
	if state.session != nil {
		closeErr = state.session.close()
	}
	return errors.Join(runErr, closeErr)
}

// startIdleEvictor launches a goroutine that periodically releases the cached
// read-only DB handle once it has been idle, so an idle `vecgrep serve --mcp`
// does not pin a shared file lock for its whole lifetime (which would block
// `vecgrep daemon start` from acquiring its exclusive lock). It returns a stop
// function to shut the goroutine down. Eviction is a no-op while reads are in
// flight or the handle is already closed.
func (s *SDKServer) startIdleEvictor(ctx context.Context) func() {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(idleEvictInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				if sess := s.sess(); sess != nil {
					sess.releaseIfIdle()
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}

// acquireProjectReadSnapshot captures the active root/config/session/codemap
// state and leases the matching read-only database while stateMu still pins
// that activation. The returned searcher and database therefore cannot belong
// to a different project, and a concurrent activation cannot close the old
// session until release runs.
func (s *SDKServer) acquireProjectReadSnapshot(ctx context.Context) (projectReadSnapshot, error) {
	s.stateMu.RLock()
	state := projectStateSnapshot{
		session:     s.session,
		daemon:      s.daemon,
		projectRoot: s.projectRoot,
		projectName: projectName(s.session),
		initialized: s.initialized,
		cfg:         projectConfig(s.session),
		provider:    projectProvider(s.session),
		codemap:     s.codemap,
		codemapCfg:  s.codemapCfg,
	}
	if !state.initialized || state.session == nil || state.projectRoot == "" || state.cfg == nil {
		s.stateMu.RUnlock()
		return projectReadSnapshot{}, fmt.Errorf("no active session")
	}
	database, release, err := state.session.acquireROContext(ctx)
	s.stateMu.RUnlock()
	if err != nil {
		return projectReadSnapshot{}, err
	}
	if rErr := state.session.reloadIfStale(); rErr != nil {
		log.Printf("vecgrep: read-only index reload failed: %v (status may reflect stale persisted data)", rErr)
	}
	return projectReadSnapshot{
		projectStateSnapshot: state,
		database:             database,
		searcher:             search.NewSearcher(database, state.provider),
		release:              release,
	}, nil
}

func (s *SDKServer) observeReadSnapshot(handler string, state projectReadSnapshot) {
	if s.readSnapshotHook != nil {
		s.readSnapshotHook(handler, state)
	}
}

func (s *SDKServer) observeStateSnapshot(handler string, state projectStateSnapshot) {
	if s.stateSnapshotHook != nil {
		s.stateSnapshotHook(handler, state)
	}
}

// ensureInitialized attempts to auto-detect and activate a project if not already initialized.
func (s *SDKServer) ensureInitialized(ctx context.Context) error {
	if s.isInitialized() {
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

	// Create embedding provider based on config
	provider, err := app.NewProvider(cfg)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to create embedding provider: %v", err)}},
			IsError: true,
		}, nil, nil
	}

	// Swap in the new (lazy, no DB opened yet) session and daemon client under
	// the state lock so concurrent handlers never observe a torn pointer, then
	// close the previous session OUTSIDE the lock — close() waits for in-flight
	// read leases to drain and we must not hold stateMu (which those handlers
	// need) while it blocks.
	newSession := newMCPSession(cfg, projectPath, provider)
	newSession.projectName = resolved.ProjectName
	s.stateMu.Lock()
	oldSession := s.session
	s.session = newSession
	s.daemon = newDaemonClient(hubDataDir(), projectPath)
	s.projectRoot = projectPath
	s.initialized = true
	s.codemap = NewCodemapClient(cfg.Codemap)
	s.codemapCfg = cfg.Codemap
	s.stateMu.Unlock()

	if oldSession != nil {
		_ = oldSession.close()
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Activated vecgrep project: %s\n\n", projectPath)
	// Only show .gitignore warning when using local mode
	if !resolved.IsGlobalMode {
		sb.WriteString("**IMPORTANT:** Add `.vecgrep` to your `.gitignore` file.\n\n")
	}
	fmt.Fprintf(&sb, "- Data dir: %s\n", cfg.DataDir)
	fmt.Fprintf(&sb, "- Embedding provider: %s (%s)\n", cfg.Embedding.Provider, cfg.Embedding.Model)

	// Try to get vector backend info and stats via a temporary RO open (if DB exists)
	if newSession.hasDatabase() {
		if database, release, err := newSession.acquireRO(); err == nil {
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

			// Use the same bounded freshness proof as vecgrep_status. Activation
			// must not download codemap's structural export just to report drift.
			freshnessService := app.NewService(&app.Session{ProjectRoot: projectPath, Config: cfg, DB: database})
			freshness, pending, freshnessErr := freshnessService.IndexFreshness(ctx)
			if freshnessErr == nil {
				writeFreshnessStatus(&sb, freshness, pending)
			}
			release()
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

func checkEmbeddingProvider(ctx context.Context, p embed.Provider) *sdkmcp.CallToolResult {
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
// result JSON has the shape {"results": [...], "mode": "...", "warnings": [...]}.
func formatDaemonSearchResult(raw json.RawMessage, scopeNote string) string {
	var resp struct {
		Results  []search.Result `json:"results"`
		Mode     string          `json:"mode"`
		Warnings []string        `json:"warnings"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Sprintf("daemon search result parse error: %v", err)
	}

	var sb strings.Builder
	if scopeNote != "" {
		sb.WriteString(scopeNote)
		sb.WriteString("\n\n")
	}
	for _, w := range resp.Warnings {
		fmt.Fprintf(&sb, "> **Warning:** %s\n\n", w)
	}
	formatSearchResults(&sb, resp.Results)
	return sb.String()
}

// formatStatsResult formats the JSON stats result from a daemon.stats socket
// call into the same text format as the direct status path.
func formatStatsResult(raw json.RawMessage, projectRoot string) string {
	var stats struct {
		TotalFiles     int64                     `json:"total_files"`
		TotalChunks    int64                     `json:"total_chunks"`
		Languages      map[string]int64          `json:"languages"`
		Freshness      *app.IndexFreshnessReport `json:"freshness"`
		PendingChanges *index.PendingChanges     `json:"pending_changes"`
	}
	if err := json.Unmarshal(raw, &stats); err != nil {
		return fmt.Sprintf("stats parse error: %v", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Index status for: %s\n\n", projectRoot)

	fmt.Fprintf(&sb, "Total files: %d\n", stats.TotalFiles)
	fmt.Fprintf(&sb, "Total chunks: %d\n", stats.TotalChunks)
	if len(stats.Languages) > 0 {
		languages := make([]string, 0, len(stats.Languages))
		for language := range stats.Languages {
			languages = append(languages, language)
		}
		sort.Strings(languages)
		sb.WriteString("\nBy language:\n")
		for _, language := range languages {
			fmt.Fprintf(&sb, "  %s: %d\n", language, stats.Languages[language])
		}
	}
	writeFreshnessStatus(&sb, stats.Freshness, stats.PendingChanges)
	return sb.String()
}

func writeFreshnessStatus(sb *strings.Builder, freshness *app.IndexFreshnessReport, pending *index.PendingChanges) {
	if sb == nil || freshness == nil {
		return
	}
	sb.WriteString("\nReindex status:\n")
	fmt.Fprintf(sb, "  Freshness: %s (%s)\n", freshness.State, freshness.Reason)
	if pending != nil {
		fmt.Fprintf(sb, "  New files: %d\n", pending.NewFiles)
		fmt.Fprintf(sb, "  Modified files: %d\n", pending.ModifiedFiles)
		fmt.Fprintf(sb, "  Deleted files: %d\n", pending.DeletedFiles)
	}
	switch freshness.State {
	case app.IndexFreshnessStale:
		sb.WriteString("\n**Action needed:** Run vecgrep_index to update the index.\n")
	case app.IndexFreshnessUnknown:
		sb.WriteString("\n**Freshness unknown:** Call vecgrep_index with force:true to rebuild trusted source/receipt metadata.\n")
	}
}

// handleSearch handles the vecgrep_search tool.
func (s *SDKServer) handleSearch(ctx context.Context, req *sdkmcp.CallToolRequest, input SearchInput) (*sdkmcp.CallToolResult, any, error) {
	if err := s.ensureInitialized(ctx); err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: err.Error()}},
			IsError: true,
		}, nil, nil
	}

	if input.Query == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "query parameter is required"}},
			IsError: true,
		}, nil, nil
	}
	state, err := s.acquireProjectOperationSnapshot()
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to capture project session: %v", err)}},
			IsError: true,
		}, nil, nil
	}
	defer state.release()
	s.observeStateSnapshot("search", state.projectStateSnapshot)
	if errResult := checkEmbeddingProvider(ctx, state.provider); errResult != nil {
		return errResult, nil, nil
	}

	opts := search.DefaultSearchOptions()
	opts.ProjectRoot = state.projectRoot

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
	if input.MinScore > 0 {
		opts.MinScore = input.MinScore
	}

	// Apply file scoping. Direct file_paths take precedence; otherwise,
	// when symbol is set, resolve the blast radius via codemap impact.
	scopeFiles, scopeNote := state.resolveSearchScope(ctx, input)
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
	if dc := state.daemon; dc != nil && dc.available() {
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
			MinScore:    input.MinScore,
			Explain:     input.Explain,
			FilePaths:   opts.FilePaths,
		}
		rawResult, dErr := dc.search(ctx, params)
		if dErr == nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: formatDaemonSearchResult(rawResult, scopeNote)}},
			}, nil, nil
		}
		// fall through to RO session on daemon error
	}
	readState, err := state.acquireRead(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", err)}},
			IsError: true,
		}, nil, nil
	}
	defer readState.release()
	s.observeReadSnapshot("search", readState)

	// Perform search with or without explanation
	if input.Explain {
		results, explanation, err := readState.searcher.SearchWithExplain(ctx, input.Query, opts)
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
				results[i].Content = expandContextLines(state.projectRoot, results[i], input.ContextLines)
			}
		}

		state.rerankWithCodemap(ctx, results, state.codemapStructuralWeight())
		formatSearchResults(&sb, results)
		state.annotateSearchHits(ctx, results, input.Query)
	} else {
		outcome, err := readState.searcher.SearchWithOutcome(ctx, input.Query, opts)
		if err != nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Search error: %v", err)}},
				IsError: true,
			}, nil, nil
		}
		results := outcome.Results

		// Surface degraded-mode diagnostics (e.g. embedder unavailable →
		// keyword-only results) so a fallback is never silent.
		for _, w := range outcome.Warnings {
			fmt.Fprintf(&sb, "> **Warning:** %s\n\n", w)
		}

		// Expand context lines if requested
		if input.ContextLines > 0 {
			for i := range results {
				results[i].Content = expandContextLines(state.projectRoot, results[i], input.ContextLines)
			}
		}

		state.rerankWithCodemap(ctx, results, state.codemapStructuralWeight())
		formatSearchResults(&sb, results)
		state.annotateSearchHits(ctx, results, input.Query)
	}

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}

// formatSearchResults formats search results into markdown, including match
// provenance (semantic vs structural) and next-action affordances so a weak
// agent knows why each hit ranked where it did and what to do next.
func formatSearchResults(sb *strings.Builder, results []search.Result) {
	if len(results) == 0 {
		sb.WriteString("No results found.\n")
		sb.WriteString("\nNext steps: try mode:\"keyword\" for exact identifiers, broaden the query phrasing, or check vecgrep_status to confirm the index is fresh.\n")
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
		if r.Reranked {
			fmt.Fprintf(sb, "**Why ranked here:** semantic %.2f + structural hub score %.2f (this symbol has high fan-in — many callers depend on it)\n", r.Score, r.StructuralScore)
		}
		sb.WriteString("\n```")
		if r.Language != "" && r.Language != "unknown" {
			sb.WriteString(r.Language)
		}
		sb.WriteString("\n")
		sb.WriteString(r.Content)
		sb.WriteString("\n```\n\n")
	}

	// Footer affordances: concrete next moves keyed to result quality.
	top := results[0]
	sb.WriteString("---\n")
	if top.Score < 0.35 {
		sb.WriteString("Top score is low — these may be weak matches. Try mode:\"keyword\" for exact identifiers, or rephrase the query closer to how the code names things.\n")
	}
	if top.SymbolName != "" {
		fmt.Fprintf(sb, "Next steps: codemap_context symbol:%q for callers/callees/tests of the top hit; vecgrep_similar file_location:\"%s:%d\" for related code.\n",
			top.SymbolName, top.RelativePath, top.StartLine)
	} else {
		fmt.Fprintf(sb, "Next steps: vecgrep_similar file_location:\"%s:%d\" for related code; codemap_symbol_at to resolve the enclosing symbol.\n",
			top.RelativePath, top.StartLine)
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

	state := s.snapshotProjectState()
	if !state.initialized || state.session == nil || state.projectRoot == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "No active project session. Run vecgrep_init first."}},
			IsError: true,
		}, nil, nil
	}
	structuralMode, err := app.ParseStructuralChunksMode(state.codemapCfg.StructuralChunks)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to configure structural chunks: %s", err)}},
			IsError: true,
		}, nil, nil
	}

	var result *index.IndexResult
	// Required structural ingestion must wait for the daemon's validation and
	// index result. Force and path-scoped requests also need the synchronous RPC
	// so their semantics are not silently lost by the background endpoint.
	if dc := state.daemon; dc != nil && dc.available() {
		if structuralMode == app.StructuralChunksRequired || input.Force || len(input.Paths) > 0 {
			result, err = dc.reindexSync(ctx, input.Force, string(structuralMode), input.Paths)
			if err != nil {
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Indexing error: %v", err)}},
					IsError: true,
				}, nil, nil
			}
		} else if err := dc.reindex(ctx); err == nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "Reindexing in background via daemon. Run vecgrep_status to check progress."}},
			}, nil, nil
		}
		// Fall through to coordinated local indexing on daemon transport error.
	}

	if result == nil {
		// Use the coordinator captured by the same project snapshot even if
		// another request activates a new project mid-index. It owns the complete
		// provider/profile/DB lifecycle and closes MCP's on-demand write lease.
		result, err = state.session.index(ctx, app.IndexRequest{
			Paths:            input.Paths,
			FullReindex:      input.Force,
			StructuralChunks: string(structuralMode),
		})
		if err != nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Indexing error: %s", formatLockError(err))}},
				IsError: true,
			}, nil, nil
		}
	}

	// Format result
	var sb strings.Builder
	sb.WriteString("Indexing complete:\n")
	fmt.Fprintf(&sb, "- Files processed: %d\n", result.FilesProcessed)
	fmt.Fprintf(&sb, "- Files skipped (unchanged): %d\n", result.FilesSkipped)
	fmt.Fprintf(&sb, "- Files deleted: %d\n", result.FilesDeleted)
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
	if state.codemapCfg.Enabled {
		writeCodemapStatusFor(ctx, &sb, state.codemap, state.projectRoot)
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

	// The daemon branch needs only the immutable client/root pair. If it fails,
	// acquireProjectReadSnapshot below captures a fresh, coherent direct-read
	// activation instead of mixing this snapshot with independently read fields.
	state := s.snapshotProjectState()
	if dc := state.daemon; dc != nil && dc.available() {
		if rawStats, dErr := dc.stats(ctx); dErr == nil {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: formatStatsResult(rawStats, state.projectRoot)}},
			}, nil, nil
		}
		// fall through to RO session
	}

	readState, err := s.acquireProjectReadSnapshot(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", err)}},
			IsError: true,
		}, nil, nil
	}
	defer readState.release()
	if s.statusSnapshotHook != nil {
		s.statusSnapshotHook(readState)
	}

	stats, err := readState.searcher.GetIndexStats(ctx)
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

	// The same leased database and activation config drive freshness; opening a
	// second RO handle or reloading config by a newly activated root would tear
	// one status response across projects.
	freshnessService := app.NewService(&app.Session{
		ProjectRoot: readState.projectRoot,
		Config:      readState.cfg,
		DB:          readState.database,
	})
	freshness, pending, freshnessErr := freshnessService.IndexFreshness(ctx)
	if freshnessErr == nil {
		writeFreshnessStatus(&sb, freshness, pending)
	}

	// Report codemap integration from the same activation snapshot.
	if readState.codemapCfg.Enabled {
		writeCodemapStatusFor(ctx, &sb, readState.codemap, readState.projectRoot)
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
func writeCodemapStatusFor(ctx context.Context, sb *strings.Builder, codemap *CodemapClient, projectRoot string) {
	sb.WriteString("\nCodemap integration: enabled\n")
	if codemap == nil || !codemap.Available() {
		sb.WriteString("  Status: codemap binary not found\n")
		return
	}
	sb.WriteString("  Status: connected\n")
	status, _ := codemap.Status(ctx, projectRoot)
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
	state, err := s.acquireProjectReadSnapshot(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", err)}},
			IsError: true,
		}, nil, nil
	}
	defer state.release()
	s.observeReadSnapshot("similar", state)
	if errResult := checkEmbeddingProvider(ctx, state.provider); errResult != nil {
		return errResult, nil, nil
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
			ProjectRoot: state.projectRoot,
		},
		ExcludeSameFile: input.ExcludeSameFile,
		ExcludeSourceID: true, // Default to excluding source
	}

	if opts.Limit == 0 {
		opts.Limit = 10
	}

	var results []search.Result

	if input.ChunkID != 0 {
		results, err = state.searcher.SearchSimilarByID(ctx, input.ChunkID, opts)
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
		results, err = state.searcher.SearchSimilarByLocation(ctx, parts[0], line, opts)
	} else if input.Text != "" {
		results, err = state.searcher.SearchSimilarByText(ctx, input.Text, opts)
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

	state.annotateSearchHits(ctx, results, "similar")

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

	state := s.snapshotProjectState()
	if !state.initialized || state.session == nil || state.projectRoot == "" {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "No active project session. Run vecgrep_init first."}},
			IsError: true,
		}, nil, nil
	}
	// Acquire the DB and project root from one coherent activation snapshot.
	database, release, err := state.session.acquireWriteDB(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database for writing: %s", formatLockError(err))}},
			IsError: true,
		}, nil, nil
	}
	defer func() { _ = release() }()

	chunksDeleted, err := database.DeleteProjectFile(ctx, state.projectRoot, input.FilePath)
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

	state := s.snapshotProjectState()
	if !state.initialized || state.session == nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "No active project session. Run vecgrep_init first."}},
			IsError: true,
		}, nil, nil
	}
	// Keep the write lease tied to the session captured for this tool call.
	database, release, err := state.session.acquireWriteDB(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database for writing: %s", formatLockError(err))}},
			IsError: true,
		}, nil, nil
	}
	defer func() { _ = release() }()

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

	state := s.snapshotProjectState()
	if !state.initialized || state.session == nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "No active project session. Run vecgrep_init first."}},
			IsError: true,
		}, nil, nil
	}
	// Keep the write lease tied to the session captured for this tool call.
	database, release, err := state.session.acquireWriteDB(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database for writing: %s", formatLockError(err))}},
			IsError: true,
		}, nil, nil
	}
	defer func() { _ = release() }()

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
	state, stateErr := s.acquireProjectOperationSnapshot()
	if stateErr != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to capture project session: %v", stateErr)}},
			IsError: true,
		}, nil, nil
	}
	defer state.release()
	s.observeStateSnapshot("branch_status", state.projectStateSnapshot)

	idx, info, err := app.BranchStatus(ctx, state.projectRoot, state.projectName)
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
func (state projectStateSnapshot) annotateSearchHits(ctx context.Context, results []search.Result, query string) {
	if state.codemap == nil || !state.codemap.Available() || len(results) == 0 {
		return
	}
	// Use a background context so annotation outlives the request
	annotateCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, r := range results {
		// Resolve the hit's position to the enclosing symbol. We pin the
		// graph-resolved symbol, not the regex-extracted name.
		sa, err := state.codemap.SymbolAt(annotateCtx, state.projectRoot, r.RelativePath, r.StartLine)
		if err != nil || !sa.Resolved() {
			// Project not indexed, position off any symbol, or codemap
			// unavailable → skip rather than pin a guess.
			continue
		}
		note := fmt.Sprintf("vecgrep semantic hit (score %.2f): %s", r.Score, truncate(query, 120))
		_ = state.codemap.Annotate(annotateCtx, state.projectRoot, sa.Symbol, note, "vecgrep", map[string]any{
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
func (state projectStateSnapshot) codemapStructuralWeight() float32 {
	if state.codemapCfg.StructuralWeight > 0 {
		return state.codemapCfg.StructuralWeight
	}
	return 0.15
}

// rerankWithCodemap re-orders search results using codemap's structural
// importance data (fan-in hub scores). The re-ranked results are written
// back into the slice in-place. This is best-effort: if codemap is
// unavailable or returns no data, results are left in their original order.
func (state projectStateSnapshot) rerankWithCodemap(ctx context.Context, results []search.Result, structuralWeight float32) {
	if state.codemap == nil || !state.codemap.Available() || structuralWeight <= 0 || len(results) <= 1 {
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

	reranked := state.codemap.Rerank(ctx, state.projectRoot, rerankInput, structuralWeight)

	// Reorder the original results slice to match reranked order, carrying
	// the structural scores onto the results so downstream formatting can
	// explain WHY a hit ranked where it did.
	used := make([]bool, len(results))
	reordered := make([]search.Result, 0, len(results))
	for _, rr := range reranked {
		for j, orig := range results {
			if used[j] {
				continue
			}
			if orig.RelativePath == rr.Result.RelativePath && orig.StartLine == rr.Result.StartLine {
				orig.StructuralScore = rr.StructuralScore
				orig.Reranked = rr.StructuralScore > 0
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
func (state projectStateSnapshot) resolveSearchScope(ctx context.Context, input SearchInput) (files []string, note string) {
	// Direct file_paths take precedence
	if len(input.FilePaths) > 0 {
		return input.FilePaths, fmt.Sprintf("**Scope:** restricted to %d file(s)", len(input.FilePaths))
	}

	if input.Symbol == "" {
		return nil, ""
	}

	if state.codemap == nil || !state.codemap.Available() {
		return nil, fmt.Sprintf("**Scope:** symbol %q requested but codemap is unavailable — searching unscoped", input.Symbol)
	}

	depth := int(state.codemapCfg.ImpactDepth)
	result, err := state.codemap.Impact(ctx, state.projectRoot, input.Symbol, depth)
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
	state, err := s.acquireProjectReadSnapshot(ctx)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Failed to open database: %v", err)}},
			IsError: true,
		}, nil, nil
	}
	defer state.release()
	s.observeReadSnapshot("investigate", state)
	if errResult := checkEmbeddingProvider(ctx, state.provider); errResult != nil {
		return errResult, nil, nil
	}

	// Build search options from the investigate input
	opts := search.DefaultSearchOptions()
	opts.ProjectRoot = state.projectRoot
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
	scopeFiles, scopeNote := state.resolveSearchScope(ctx, scopeInput)
	if len(scopeFiles) > 0 {
		opts.FilePaths = scopeFiles
	}

	var sb strings.Builder

	// Report scoping status
	if scopeNote != "" {
		sb.WriteString(scopeNote)
		sb.WriteString("\n\n")
	}

	outcome, err := state.searcher.SearchWithOutcome(ctx, input.Query, opts)
	if err != nil {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: fmt.Sprintf("Search error: %v", err)}},
			IsError: true,
		}, nil, nil
	}
	results := outcome.Results

	// Surface degraded-mode diagnostics so a fallback is never silent.
	for _, w := range outcome.Warnings {
		fmt.Fprintf(&sb, "> **Warning:** %s\n\n", w)
	}

	// Expand context lines if requested
	if input.ContextLines > 0 {
		for i := range results {
			results[i].Content = expandContextLines(state.projectRoot, results[i], input.ContextLines)
		}
	}

	state.rerankWithCodemap(ctx, results, state.codemapStructuralWeight())
	formatSearchResults(&sb, results)
	state.annotateSearchHits(ctx, results, input.Query)

	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: sb.String()}},
	}, nil, nil
}
