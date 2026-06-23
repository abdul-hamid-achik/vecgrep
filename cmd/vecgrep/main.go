package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/mcp"
	"github.com/abdul-hamid-achik/vecgrep/internal/render"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	"github.com/abdul-hamid-achik/vecgrep/internal/studio"
	"github.com/abdul-hamid-achik/vecgrep/internal/version"
	"github.com/abdul-hamid-achik/veclite"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:     "vecgrep",
	Short:   "Local-first semantic code search",
	Version: version.Full(),
	Long: `vecgrep is a local-first semantic code search tool that uses
embeddings to find similar code across your codebase.

It supports Ollama for local embeddings, ensuring your code never
leaves your machine.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !isInteractiveTerminal() {
			return cmd.Help()
		}
		return runStudio(cmd, args)
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("vecgrep %s\n", version.Version)
		fmt.Printf("  commit:  %s\n", version.Commit)
		fmt.Printf("  built:   %s\n", version.Date)
	},
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize vecgrep in the current directory",
	Long: `Initialize a new vecgrep project in the current directory.
By default, project data is stored centrally in ~/.vecgrep/projects/{name}/.
Use --local to create a .vecgrep directory inside the project instead.`,
	RunE: runInit,
}

var indexCmd = &cobra.Command{
	Use:   "index [paths...]",
	Short: "Index files for semantic search",
	Long: `Index source files for semantic search. If no paths are specified,
indexes the current directory recursively.`,
	RunE: runIndex,
}

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search the codebase semantically",
	Long: `Search the indexed codebase using natural language queries.
Returns the most relevant code chunks ranked by similarity.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runSearch,
}

var studioCmd = &cobra.Command{
	Use:     "studio [path]",
	Aliases: []string{"browse"},
	Short:   "Open the interactive terminal search workspace",
	Long:    `Open vecgrep Studio for search, inspection, indexing, and project status.`,
	Args:    cobra.MaximumNArgs(1),
	RunE:    runStudio,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP stdio server",
	Long: `Start the Model Context Protocol (MCP) server over stdio
for integration with AI assistants.`,
	RunE: runServe,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show index status and statistics",
	RunE:  runStatus,
}

var similarCmd = &cobra.Command{
	Use:   "similar <target>",
	Short: "Find code similar to an existing chunk, file location, or text",
	Long: `Find code semantically similar to an existing chunk, file location, or text snippet.

Targets:
  42              # Chunk ID
  main.go:15      # File:line location

For text snippets, use the --text flag:
  vecgrep similar --text "func NewSearcher"

Examples:
  vecgrep similar 42
  vecgrep similar internal/search/search.go:50
  vecgrep similar --text "error handling" --lang go
  vecgrep similar 42 --exclude-same-file`,
	RunE: runSimilar,
}

var deleteCmd = &cobra.Command{
	Use:   "delete <file-path>",
	Short: "Delete a file from the index",
	Long:  `Remove a file and all its chunks from the search index.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runDelete,
}

var cleanCmd = &cobra.Command{
	Use:   "clean",
	Short: "Sync database to disk and report index stats",
	Long: `Sync the vector database to disk and report current index statistics.

With veclite-only storage all data is self-contained in collection records, so
there are no orphaned rows to reclaim. This command flushes pending writes and
prints record/file counts so you can confirm the index is consistent. If a
future veclite release exposes a collection-level Compact() API, HNSW tombstone
compaction will be wired in here.`,
	RunE: runClean,
}

var resetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Clear the entire project database",
	Long: `Reset the project by clearing all indexed files, chunks, and embeddings.
This is a destructive operation and cannot be undone.

Use --force to skip the confirmation prompt.`,
	RunE: runReset,
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage vecgrep configuration",
	Long: `View and manage vecgrep configuration.

Subcommands:
  show    Show the resolved configuration
  set     Set a configuration value`,
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the resolved configuration",
	Long: `Display the current resolved configuration from all sources.

Configuration is loaded in the following order (highest to lowest priority):
1. Environment variables (VECGREP_*)
2. Project root vecgrep.yaml or vecgrep.yml
3. Project .config/vecgrep.yaml (XDG-style)
4. Project .vecgrep/config.yaml (legacy)
5. Global project entry in ~/.vecgrep/config.yaml
6. Global defaults in ~/.vecgrep/config.yaml
7. Built-in defaults`,
	RunE: runConfigShow,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a configuration value in the project or global config.

Examples:
  vecgrep config set embedding.provider openai
  vecgrep config set embedding.provider cohere
  vecgrep config set embedding.provider voyage
  vecgrep config set --global embedding.provider openai`,
	Args: cobra.ExactArgs(2),
	RunE: runConfigSet,
}

var projectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "Manage globally registered projects",
	Long: `Manage projects registered in ~/.vecgrep/config.yaml.

Subcommands:
  list    List all registered projects
  add     Register the current project globally
  remove  Unregister a project`,
}

var projectsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all registered projects",
	RunE:  runProjectsList,
}

var projectsAddCmd = &cobra.Command{
	Use:   "add [name]",
	Short: "Register the current project globally",
	Long: `Register the current project in ~/.vecgrep/config.yaml.

The project name is automatically derived from the directory name if not provided.
Project data will be stored in ~/.vecgrep/projects/{name}/`,
	RunE: runProjectsAdd,
}

var projectsRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Unregister a project",
	Args:  cobra.ExactArgs(1),
	RunE:  runProjectsRemove,
}

func init() {
	// Set version template
	rootCmd.SetVersionTemplate("vecgrep version {{.Version}}\n")

	// Global flags
	rootCmd.PersistentFlags().StringP("config", "c", "", "config file path")
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "verbose output")

	// Bind flags to viper
	viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
	viper.BindPFlag("verbose", rootCmd.PersistentFlags().Lookup("verbose"))

	// Init command flags
	initCmd.Flags().Bool("force", false, "overwrite existing configuration")

	// Index command flags
	indexCmd.Flags().Bool("full", false, "force full re-index")
	indexCmd.Flags().StringSlice("ignore", nil, "additional patterns to ignore")
	indexCmd.Flags().Bool("no-progress", false, "disable the live progress bar (useful for scripts/CI)")

	// Search command flags
	searchCmd.Flags().IntP("limit", "n", 10, "maximum number of results")
	searchCmd.Flags().StringP("format", "f", "default", "output format (default, json, compact)")
	searchCmd.Flags().StringP("lang", "l", "", "filter by programming language")
	searchCmd.Flags().StringSlice("languages", nil, "filter by multiple languages (comma-separated)")
	searchCmd.Flags().StringP("type", "t", "", "filter by chunk type (function, class, block)")
	searchCmd.Flags().StringSlice("types", nil, "filter by multiple chunk types (comma-separated)")
	searchCmd.Flags().String("file", "", "filter by file pattern (glob)")
	searchCmd.Flags().String("dir", "", "filter by directory prefix")
	searchCmd.Flags().String("lines", "", "filter by line range (e.g., '1-100')")
	searchCmd.Flags().StringP("mode", "m", "hybrid", "search mode: semantic, keyword, or hybrid")
	searchCmd.Flags().Bool("explain", false, "show search diagnostics")

	// Serve command flags
	serveCmd.Flags().Bool("mcp", false, "start MCP server (stdio)")

	// Similar command flags
	similarCmd.Flags().IntP("limit", "n", 10, "maximum number of results")
	similarCmd.Flags().StringP("format", "f", "default", "output format (default, json, compact)")
	similarCmd.Flags().StringP("lang", "l", "", "filter by programming language")
	similarCmd.Flags().StringSlice("languages", nil, "filter by multiple languages (comma-separated)")
	similarCmd.Flags().StringP("type", "t", "", "filter by chunk type (function, class, block)")
	similarCmd.Flags().StringSlice("types", nil, "filter by multiple chunk types (comma-separated)")
	similarCmd.Flags().String("file", "", "filter by file pattern (glob)")
	similarCmd.Flags().String("dir", "", "filter by directory prefix")
	similarCmd.Flags().String("lines", "", "filter by line range (e.g., '1-100')")
	similarCmd.Flags().Bool("exclude-same-file", false, "exclude results from the same file as the source")
	similarCmd.Flags().StringP("text", "T", "", "find code similar to this text snippet")

	// Status command flags
	statusCmd.Flags().StringP("format", "f", "default", "output format (default, json)")

	// Reset command flags
	resetCmd.Flags().Bool("force", false, "skip confirmation prompt")

	// Config show command flags
	configShowCmd.Flags().Bool("global", false, "show global config only")

	// Config set command flags
	configSetCmd.Flags().Bool("global", false, "set value in global config")

	// Add config subcommands
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configSetCmd)

	// Add projects subcommands
	projectsCmd.AddCommand(projectsListCmd)
	projectsCmd.AddCommand(projectsAddCmd)
	projectsCmd.AddCommand(projectsRemoveCmd)

	// Init command flags for global/local mode
	initCmd.Flags().Bool("global", false, "register project in ~/.vecgrep/ (this is the default)")
	initCmd.Flags().Bool("local", false, "create local .vecgrep/ directory instead of centralized storage")
	initCmd.Flags().String("extension", "yaml", "preferred config file extension (yaml or yml)")

	// Add commands
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(indexCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(studioCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(similarCmd)
	rootCmd.AddCommand(deleteCmd)
	rootCmd.AddCommand(cleanCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(projectsCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")
	globalMode, _ := cmd.Flags().GetBool("global")
	localMode, _ := cmd.Flags().GetBool("local")

	// If both flags are specified, error
	if globalMode && localMode {
		return fmt.Errorf("cannot specify both --global and --local")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	// Local mode: create .vecgrep/ directory
	if localMode {
		return runInitLocal(cwd, force)
	}

	// Global mode (default): register in ~/.vecgrep/projects/
	return runInitGlobal(cwd, force)
}

// runInitGlobal initializes a project in global mode (~/.vecgrep/projects/)
func runInitGlobal(cwd string, force bool) error {
	// Check if already registered
	existingName, existingEntry, _ := config.FindProjectByPath(cwd)
	if existingEntry != nil && !force {
		return fmt.Errorf("project already registered as '%s' (use --force to reinitialize)", existingName)
	}

	// Add to global projects
	if err := config.AddProjectToGlobal(cwd, ""); err != nil {
		return fmt.Errorf("failed to register project: %w", err)
	}

	// Get the derived name
	name, _, _ := config.FindProjectByPath(cwd)

	// Get data directory
	dataDir, err := config.GetProjectDataDir(name)
	if err != nil {
		return fmt.Errorf("failed to get project data directory: %w", err)
	}

	// Create data directory
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Create config
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.DBPath = filepath.Join(dataDir, config.DefaultDBFile)

	// Initialize database
	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer database.Close()

	// Get vector backend info
	vecVersion, err := database.VecVersion()
	if err != nil {
		return fmt.Errorf("failed to verify vector backend: %w", err)
	}

	fmt.Printf("Initialized vecgrep (global mode)\n")
	fmt.Printf("  Project: %s\n", name)
	fmt.Printf("  Path: %s\n", cwd)
	fmt.Printf("  Data: %s\n", dataDir)
	fmt.Printf("  Database: %s\n", cfg.DBPath)
	fmt.Printf("  Vector backend: %s\n", vecVersion)
	fmt.Printf("  Embedding provider: %s (%s)\n", cfg.Embedding.Provider, cfg.Embedding.Model)
	fmt.Println()
	fmt.Println("Tip: Create a vecgrep.yaml in your project root for project-specific settings.")
	fmt.Printf("\nRun 'vecgrep index' to index your codebase.\n")

	return nil
}

// runInitLocal initializes a project in local mode (.vecgrep/ directory)
func runInitLocal(cwd string, force bool) error {
	dataDir := filepath.Join(cwd, config.DefaultDataDir)

	// Check if already initialized
	if _, err := os.Stat(dataDir); err == nil && !force {
		return fmt.Errorf("vecgrep already initialized in %s (use --force to reinitialize)", cwd)
	}

	// Create configuration
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.DBPath = filepath.Join(dataDir, config.DefaultDBFile)

	// Create data directory
	if err := cfg.EnsureDataDir(); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// Write default config
	if err := cfg.WriteDefaultConfig(); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	// Initialize database
	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer database.Close()

	// Get and display vector backend info
	vecVersion, err := database.VecVersion()
	if err != nil {
		return fmt.Errorf("failed to verify vector backend: %w", err)
	}

	fmt.Printf("Initialized vecgrep in %s\n", dataDir)
	fmt.Printf("  Database: %s\n", cfg.DBPath)
	fmt.Printf("  Vector backend: %s\n", vecVersion)
	fmt.Printf("  Embedding provider: %s (%s)\n", cfg.Embedding.Provider, cfg.Embedding.Model)
	fmt.Printf("\nIMPORTANT: Add .vecgrep to your .gitignore file.\n")
	fmt.Printf("\nRun 'vecgrep index' to index your codebase.\n")

	return nil
}

func runIndex(cmd *cobra.Command, args []string) error {
	session, err := app.OpenSession(cmd.Context(), "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	// Get flags
	fullReindex, _ := cmd.Flags().GetBool("full")
	additionalIgnores, _ := cmd.Flags().GetStringSlice("ignore")

	verbose, _ := rootCmd.PersistentFlags().GetBool("verbose")

	fmt.Printf("Indexing %s...\n", session.ProjectRoot)
	fmt.Printf("  Model: %s\n", session.Config.Embedding.Model)

	// Determine whether to show the live progress bar.
	// Show it by default in an interactive terminal; suppress when --no-progress
	// is set, when --verbose is set (verbose uses its own format), or when stdout
	// is not a TTY (piped/redirected output should stay line-oriented).
	showProgress := !verbose && isInteractiveTerminal()
	if noProgress, _ := cmd.Flags().GetBool("no-progress"); noProgress {
		showProgress = false
	}

	// Set up progress callback
	var progressCB index.ProgressCallback
	if verbose {
		progressCB = func(p index.Progress) {
			fmt.Printf("\r  %s (%d/%d files, %d chunks)",
				p.CurrentFile, p.ProcessedFiles, p.TotalFiles, p.TotalChunks)
		}
	} else if showProgress {
		progressCB = func(p index.Progress) {
			elapsed := time.Duration(0)
			if !p.StartTime.IsZero() {
				elapsed = time.Since(p.StartTime)
			}
			line := render.IndexProgressLine(p.ProcessedFiles, p.TotalFiles, p.TotalChunks, elapsed, render.ProgressBarWidth)
			fmt.Printf("\r%-80s", line)
		}
	}

	// Perform indexing
	req := app.IndexRequest{
		Paths:             args,
		FullReindex:       fullReindex,
		AdditionalIgnores: additionalIgnores,
	}
	var result *index.IndexResult
	if fullReindex {
		fmt.Println("  Mode: full re-index")
	} else {
		fmt.Println("  Mode: incremental")
	}
	result, err = service.Index(cmd.Context(), req, progressCB)

	if err != nil {
		if showProgress {
			fmt.Println() // newline before error so the bar isn't overwritten
		}
		return fmt.Errorf("indexing failed: %w", err)
	}

	if verbose || showProgress {
		fmt.Println() // new line after progress
	}
	fmt.Printf("\nIndexing complete:\n")
	fmt.Printf("  Files processed: %d\n", result.FilesProcessed)
	fmt.Printf("  Files skipped (unchanged): %d\n", result.FilesSkipped)
	fmt.Printf("  Chunks created: %d\n", result.ChunksCreated)
	fmt.Printf("  Duration: %s\n", result.Duration.Round(100*1000000))

	if len(result.Errors) > 0 {
		fmt.Printf("\nWarnings: %d\n", len(result.Errors))
		if verbose {
			for _, e := range result.Errors {
				fmt.Printf("  - %v\n", e)
			}
		}
	}

	return nil
}

func runSearch(cmd *cobra.Command, args []string) error {
	session, err := app.OpenReadOnlySession(cmd.Context(), "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	// Get flags
	query := strings.Join(args, " ")
	limit, _ := cmd.Flags().GetInt("limit")
	format, _ := cmd.Flags().GetString("format")
	lang, _ := cmd.Flags().GetString("lang")
	languages, _ := cmd.Flags().GetStringSlice("languages")
	chunkType, _ := cmd.Flags().GetString("type")
	chunkTypes, _ := cmd.Flags().GetStringSlice("types")
	filePattern, _ := cmd.Flags().GetString("file")
	directory, _ := cmd.Flags().GetString("dir")
	linesRange, _ := cmd.Flags().GetString("lines")
	modeStr, _ := cmd.Flags().GetString("mode")
	explain, _ := cmd.Flags().GetBool("explain")

	// Parse line range
	var minLine, maxLine int
	if linesRange != "" {
		minLine, maxLine = app.ParseLineRange(linesRange)
	}

	// Parse search mode
	mode := app.ParseSearchMode(modeStr, session.Config.Search.DefaultMode)

	resp, err := service.Search(cmd.Context(), app.SearchRequest{
		Query:       query,
		Limit:       limit,
		Language:    lang,
		Languages:   languages,
		ChunkType:   chunkType,
		ChunkTypes:  chunkTypes,
		FilePattern: filePattern,
		Directory:   directory,
		MinLine:     minLine,
		MaxLine:     maxLine,
		Mode:        mode,
		Explain:     explain,
	})
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	if explain && resp.Diagnostics != nil {
		// Print explanation
		fmt.Printf("Search Diagnostics:\n")
		fmt.Printf("  Index type: %s\n", resp.Diagnostics.IndexType)
		fmt.Printf("  Nodes visited: %d\n", resp.Diagnostics.NodesVisited)
		fmt.Printf("  Duration: %v\n", resp.Diagnostics.Duration)
		fmt.Printf("  Mode: %s\n", resp.Diagnostics.Mode)
		fmt.Println()
	}

	printSearchResults(resp.Results, format)
	return nil
}

func runStudio(cmd *cobra.Command, args []string) error {
	startDir := ""
	if len(args) > 0 {
		startDir = args[0]
	}
	return studio.Run(cmd.Context(), startDir)
}

func isInteractiveTerminal() bool {
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
}

func isCharDevice(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

// printSearchResults formats and prints search results.
func printSearchResults(results []search.Result, format string) {
	fmt.Print(render.Results(results, render.ParseOutputFormat(format)))
}

func runServe(cmd *cobra.Command, args []string) error {
	// The --mcp flag is retained for backward compatibility. The serve command
	// is MCP-only now that the browser UI has been removed.
	session, err := app.OpenSession(cmd.Context(), "")
	if err != nil && !app.IsNoProject(err) {
		return err
	}
	if session != nil {
		defer session.Close()
	}

	// Set up context with signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutting down...")
		cancel()
	}()

	mcpCfg := mcp.SDKServerConfig{}
	if session != nil {
		mcpCfg.DB = session.DB
		mcpCfg.Provider = session.Provider
		mcpCfg.ProjectRoot = session.ProjectRoot
	}

	mcpServer := mcp.NewSDKServer(mcpCfg)
	return mcpServer.Run(ctx)
}

// StatusOutput represents the JSON output for the status command
type StatusOutput struct {
	ProjectRoot    string                `json:"project_root"`
	DataDir        string                `json:"data_dir"`
	Database       string                `json:"database"`
	VectorBackend  string                `json:"vector_backend"`
	EmbeddingModel string                `json:"embedding_model"`
	Provider       string                `json:"provider"`
	Dimensions     int                   `json:"dimensions"`
	ProfilePath    string                `json:"profile_path"`
	ProfileStatus  string                `json:"profile_status"`
	ProfileMatches bool                  `json:"profile_matches"`
	CurrentProfile app.EmbeddingProfile  `json:"current_profile"`
	StoredProfile  *app.EmbeddingProfile `json:"stored_profile,omitempty"`
	VecLiteBytes   int64                 `json:"veclite_bytes"`
	IndexedBytes   int64                 `json:"indexed_bytes"`
	LatestIndexed  string                `json:"latest_indexed_at,omitempty"`
	IndexFresh     bool                  `json:"index_fresh"`
	Stats          map[string]int64      `json:"stats"`
	Languages      map[string]int64      `json:"languages,omitempty"`
	ChunkTypes     map[string]int64      `json:"chunk_types,omitempty"`
	PendingChanges *PendingChanges       `json:"pending_changes,omitempty"`
}

// PendingChanges represents pending reindex changes
type PendingChanges struct {
	NewFiles      int `json:"new_files"`
	ModifiedFiles int `json:"modified_files"`
	DeletedFiles  int `json:"deleted_files"`
	TotalPending  int `json:"total_pending"`
}

func runStatus(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")

	session, err := app.OpenReadOnlySession(cmd.Context(), "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	status, err := service.Status(cmd.Context())
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	// JSON output
	if format == "json" {
		output := StatusOutput{
			ProjectRoot:    status.ProjectRoot,
			DataDir:        status.DataDir,
			Database:       status.VecLitePath,
			VectorBackend:  status.VectorBackend,
			EmbeddingModel: status.Model,
			Provider:       status.Provider,
			Dimensions:     status.Dimensions,
			ProfilePath:    status.ProfilePath,
			ProfileStatus:  status.ProfileStatus,
			ProfileMatches: status.ProfileMatches,
			CurrentProfile: status.CurrentProfile,
			StoredProfile:  status.StoredProfile,
			VecLiteBytes:   status.VecLiteSizeBytes,
			IndexedBytes:   status.IndexedBytes,
			IndexFresh:     status.IndexFresh,
			Stats:          status.Stats,
		}
		if !status.LatestIndexedAt.IsZero() {
			output.LatestIndexed = status.LatestIndexedAt.Format(time.RFC3339)
		}
		if status.DetailedStats != nil {
			output.Languages = status.DetailedStats.Languages
			output.ChunkTypes = status.DetailedStats.ChunkTypes
		}

		if status.PendingChanges != nil {
			output.PendingChanges = &PendingChanges{
				NewFiles:      status.PendingChanges.NewFiles,
				ModifiedFiles: status.PendingChanges.ModifiedFiles,
				DeletedFiles:  status.PendingChanges.DeletedFiles,
				TotalPending:  status.PendingChanges.TotalPending,
			}
		}

		jsonBytes, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		fmt.Println(string(jsonBytes))
		return nil
	}

	// Default text output
	fmt.Printf("vecgrep status\n")
	fmt.Printf("  Project root: %s\n", status.ProjectRoot)
	fmt.Printf("  Data dir:     %s\n", status.DataDir)
	fmt.Printf("  VecLite index: %s\n", status.VecLitePath)
	fmt.Printf("  VecLite size: %s\n", formatBytes(status.VecLiteSizeBytes))
	fmt.Printf("  Vector backend: %s\n", status.VectorBackend)
	fmt.Printf("  Veclite version: %s\n", status.VecliteVersion)
	fmt.Printf("  Embedding model: %s (%s, %d dimensions)\n", status.Model, status.Provider, status.Dimensions)
	fmt.Printf("  Provider health: %s\n", providerHealthLabel(status.ProviderHealth))
	fmt.Printf("  HNSW:         M=%d  efConstruction=%d  efSearch=%d\n", status.HNSWM, status.HNSWEfConstruction, status.HNSWEfSearch)
	fmt.Printf("  Profile:     %s\n", status.ProfileStatus)
	fmt.Printf("  Profile path: %s\n", status.ProfilePath)
	fmt.Printf("\nIndex statistics:\n")
	fmt.Printf("  Projects:   %d\n", status.Stats["projects"])
	fmt.Printf("  Files:      %d\n", status.Stats["files"])
	fmt.Printf("  Chunks:     %d\n", status.Stats["chunks"])
	fmt.Printf("  Embeddings: %d\n", status.Stats["embeddings"])
	fmt.Printf("  Source size: %s\n", formatBytes(status.IndexedBytes))
	if !status.LatestIndexedAt.IsZero() {
		fmt.Printf("  Latest:     %s\n", status.LatestIndexedAt.Format(time.RFC3339))
	}
	if status.DetailedStats != nil {
		if languages := formatCountSummary(status.DetailedStats.Languages, 5); languages != "" {
			fmt.Printf("  Languages:  %s\n", languages)
		}
		if chunkTypes := formatCountSummary(status.DetailedStats.ChunkTypes, 5); chunkTypes != "" {
			fmt.Printf("  Types:      %s\n", chunkTypes)
		}
	}

	if status.PendingChanges != nil {
		fmt.Printf("\nReindex status:\n")
		if status.IndexFresh {
			fmt.Printf("  Fresh:        yes\n")
		} else {
			fmt.Printf("  Fresh:        no\n")
		}
		fmt.Printf("  New files:      %d\n", status.PendingChanges.NewFiles)
		fmt.Printf("  Modified files: %d\n", status.PendingChanges.ModifiedFiles)
		fmt.Printf("  Deleted files:  %d\n", status.PendingChanges.DeletedFiles)
		if status.PendingChanges.TotalPending > 0 {
			fmt.Printf("\nRun 'vecgrep index' to update the index.\n")
		}
	}

	return nil
}

type countItem struct {
	name  string
	count int64
}

func formatCountSummary(counts map[string]int64, limit int) string {
	if len(counts) == 0 {
		return ""
	}
	items := make([]countItem, 0, len(counts))
	for name, count := range counts {
		if name == "" || count == 0 {
			continue
		}
		items = append(items, countItem{name: name, count: count})
	}
	if len(items) == 0 {
		return ""
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].name < items[j].name
		}
		return items[i].count > items[j].count
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s %d", item.name, item.count))
	}
	return strings.Join(parts, ", ")
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}

// providerHealthLabel renders the ProviderHealth field for the CLI status
// output. Empty means "not checked", "ok" is shown verbatim, anything else
// is an error string and is truncated to keep the output tidy.
func providerHealthLabel(health string) string {
	if health == "" {
		return "not checked"
	}
	if health == "ok" {
		return "ok"
	}
	if len(health) > 60 {
		health = health[:60] + "..."
	}
	return "error: " + health
}

func runDelete(cmd *cobra.Command, args []string) error {
	filePath := args[0]

	session, err := app.OpenSession(cmd.Context(), "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	chunksDeleted, err := service.DeleteFile(cmd.Context(), filePath)
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	fmt.Printf("Deleted %s (%d chunks removed)\n", filePath, chunksDeleted)
	return nil
}

func runClean(cmd *cobra.Command, args []string) error {
	session, err := app.OpenSession(cmd.Context(), "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	fmt.Println("Syncing database...")

	stats, err := service.Clean(cmd.Context())
	if err != nil {
		return fmt.Errorf("failed to clean database: %w", err)
	}

	fmt.Printf("Sync complete:\n")
	if stats.Synced {
		fmt.Printf("  Database flushed to disk\n")
	}
	fmt.Printf("  Records: %d\n", stats.TotalRecords)
	fmt.Printf("  Files:   %d\n", stats.TotalFiles)
	// Legacy orphan fields are always zero with veclite-only storage; only
	// surface them if a future backend reports real reclamation work.
	if stats.OrphanedChunks > 0 || stats.VacuumedBytes > 0 {
		fmt.Printf("  Orphaned chunks removed: %d\n", stats.OrphanedChunks)
	}
	if stats.VacuumedBytes > 0 {
		fmt.Printf("  Space reclaimed: %d bytes\n", stats.VacuumedBytes)
	}

	return nil
}

func runReset(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")

	session, err := app.OpenSession(cmd.Context(), "")
	if err != nil {
		if errors.Is(err, veclite.ErrFileLocked) {
			// Another process (e.g. studio TUI, index, or MCP server) holds the lock.
			// With --force, delete the index files directly. Without --force, suggest it.
			if !force {
				fmt.Fprintln(os.Stderr, "Error: the database is locked by another process (e.g. studio, index, or MCP server).")
				fmt.Fprintln(os.Stderr, "To force-reset and delete the index files, run:")
				fmt.Fprintln(os.Stderr, "  vecgrep reset --force")
				fmt.Fprintln(os.Stderr, "")
				fmt.Fprintln(os.Stderr, "Note: this will delete all indexed data. The process holding the lock will need to re-index.")
				return err
			}
			result, resetErr := app.ResetIndexFiles(cmd.Context(), "")
			if resetErr != nil {
				return fmt.Errorf("failed to reset index files after open error %q: %w", err, resetErr)
			}
			fmt.Printf("Index files reset for %s\n", result.ProjectRoot)
			fmt.Printf("  VecLite index: %s\n", result.VecLitePath)
			fmt.Println("Run 'vecgrep index' to re-index your codebase.")
			return nil
		}
		if force {
			result, resetErr := app.ResetIndexFiles(cmd.Context(), "")
			if resetErr != nil {
				return fmt.Errorf("failed to reset index files after open error %q: %w", err, resetErr)
			}
			fmt.Printf("Index files reset for %s\n", result.ProjectRoot)
			fmt.Printf("  VecLite index: %s\n", result.VecLitePath)
			fmt.Println("Run 'vecgrep index' to re-index your codebase.")
			return nil
		}
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	// Confirmation prompt unless --force is used
	if !force {
		fmt.Printf("WARNING: This will delete ALL indexed data for %s\n", session.ProjectRoot)
		fmt.Printf("This action cannot be undone.\n\n")
		fmt.Printf("Type 'yes' to confirm: ")

		var confirmation string
		_, _ = fmt.Scanln(&confirmation)
		if confirmation != "yes" {
			fmt.Println("Reset cancelled.")
			return nil
		}
	}

	if err := service.Reset(cmd.Context(), app.ResetProject); err != nil {
		return fmt.Errorf("failed to reset database: %w", err)
	}

	fmt.Println("Database reset complete. All indexed data has been cleared.")
	fmt.Println("Run 'vecgrep index' to re-index your codebase.")

	return nil
}

func runSimilar(cmd *cobra.Command, args []string) error {
	// Get flags
	textSnippet, _ := cmd.Flags().GetString("text")
	limit, _ := cmd.Flags().GetInt("limit")
	format, _ := cmd.Flags().GetString("format")
	lang, _ := cmd.Flags().GetString("lang")
	languages, _ := cmd.Flags().GetStringSlice("languages")
	chunkType, _ := cmd.Flags().GetString("type")
	chunkTypes, _ := cmd.Flags().GetStringSlice("types")
	filePattern, _ := cmd.Flags().GetString("file")
	directory, _ := cmd.Flags().GetString("dir")
	linesRange, _ := cmd.Flags().GetString("lines")
	excludeSameFile, _ := cmd.Flags().GetBool("exclude-same-file")

	// Parse line range
	var minLine, maxLine int
	if linesRange != "" {
		minLine, maxLine = app.ParseLineRange(linesRange)
	}

	var positional string
	if len(args) > 0 {
		positional = args[0]
	}
	target, err := app.ParseSimilarTarget(positional, textSnippet)
	if err != nil {
		return err
	}

	session, err := app.OpenReadOnlySession(cmd.Context(), "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	resp, err := service.Similar(cmd.Context(), app.SimilarRequest{
		Target:          target,
		Limit:           limit,
		Language:        lang,
		Languages:       languages,
		ChunkType:       chunkType,
		ChunkTypes:      chunkTypes,
		FilePattern:     filePattern,
		Directory:       directory,
		MinLine:         minLine,
		MaxLine:         maxLine,
		ExcludeSameFile: excludeSameFile,
	})
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Format and print results
	printSearchResults(resp.Results, format)

	return nil
}

func runConfigShow(cmd *cobra.Command, args []string) error {
	globalOnly, _ := cmd.Flags().GetBool("global")

	if globalOnly {
		// Show global config only
		globalCfg, err := config.LoadGlobalConfig()
		if err != nil {
			return fmt.Errorf("failed to load global config: %w", err)
		}

		fmt.Println("Global configuration (~/.vecgrep/config.yaml):")
		fmt.Println()
		fmt.Println("Defaults:")
		fmt.Printf("  Embedding:\n")
		fmt.Printf("    provider: %s\n", globalCfg.Defaults.Embedding.Provider)
		fmt.Printf("    model: %s\n", globalCfg.Defaults.Embedding.Model)
		fmt.Printf("    dimensions: %d\n", globalCfg.Defaults.Embedding.Dimensions)
		fmt.Println()
		if len(globalCfg.Projects) > 0 {
			fmt.Printf("Projects: %d registered\n", len(globalCfg.Projects))
			for name := range globalCfg.Projects {
				fmt.Printf("  - %s\n", name)
			}
		} else {
			fmt.Println("Projects: none registered")
		}
		return nil
	}

	// Show resolved config for current project
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		// No project found - show defaults
		fmt.Println("No project found. Showing default configuration:")
		fmt.Println()
		cfg := config.DefaultConfig()
		fmt.Print(config.ShowResolvedConfig(cfg, nil))
		return nil
	}

	resolver := config.NewConfigResolution()
	resolved, err := resolver.Resolve(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to resolve config: %w", err)
	}

	fmt.Printf("Project: %s\n", projectRoot)
	if resolved.ProjectName != "" {
		fmt.Printf("Registered as: %s\n", resolved.ProjectName)
	}
	if resolved.IsGlobalMode {
		fmt.Println("Mode: global (data stored in ~/.vecgrep/projects/)")
	}
	fmt.Println()
	fmt.Print(config.ShowResolvedConfig(resolved.Config, resolver.FoundConfigFiles()))

	return nil
}

func runConfigSet(cmd *cobra.Command, args []string) error {
	key := args[0]
	value := args[1]
	isGlobal, _ := cmd.Flags().GetBool("global")

	if isGlobal {
		configPath, err := config.GetGlobalConfigPath()
		if err != nil {
			return err
		}
		if err := config.SetConfigValueInFile(configPath, "defaults."+key, value); err != nil {
			return err
		}

		fmt.Printf("Set %s = %s in global config\n", key, value)
		return nil
	}

	// Set in project config
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	configPath := projectConfigPath(projectRoot)

	if err := config.SetConfigValueInFile(configPath, key, value); err != nil {
		return err
	}

	fmt.Printf("Set %s = %s in %s\n", key, value, configPath)
	return nil
}

func projectConfigPath(projectRoot string) string {
	yamlPath := filepath.Join(projectRoot, "vecgrep.yaml")
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath
	}

	ymlPath := filepath.Join(projectRoot, "vecgrep.yml")
	if _, err := os.Stat(ymlPath); err == nil {
		return ymlPath
	}

	return yamlPath
}

func runProjectsList(cmd *cobra.Command, args []string) error {
	projects, err := config.ListGlobalProjects()
	if err != nil {
		return fmt.Errorf("failed to list projects: %w", err)
	}

	if len(projects) == 0 {
		fmt.Println("No projects registered globally.")
		fmt.Println("Use 'vecgrep projects add' or 'vecgrep init --global' to register a project.")
		return nil
	}

	fmt.Printf("Registered projects (%d):\n\n", len(projects))
	for name, entry := range projects {
		fmt.Printf("  %s\n", name)
		fmt.Printf("    Path: %s\n", entry.Path)
		if entry.DataDir != "" {
			fmt.Printf("    Data: %s\n", entry.DataDir)
		}
		if entry.Embedding != nil && entry.Embedding.Provider != "" {
			fmt.Printf("    Provider: %s\n", entry.Embedding.Provider)
		}
		fmt.Println()
	}

	return nil
}

func runProjectsAdd(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

	var name string
	if len(args) > 0 {
		name = args[0]
	}

	// Check if project is already registered
	existingName, existingEntry, _ := config.FindProjectByPath(cwd)
	if existingEntry != nil {
		fmt.Printf("Project already registered as '%s'\n", existingName)
		return nil
	}

	if err := config.AddProjectToGlobal(cwd, name); err != nil {
		return fmt.Errorf("failed to add project: %w", err)
	}

	// Get the derived name if not provided
	if name == "" {
		name, _, _ = config.FindProjectByPath(cwd)
	}

	// Ensure the project data directory exists
	dataDir, err := config.GetProjectDataDir(name)
	if err != nil {
		return fmt.Errorf("failed to get project data directory: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create project data directory: %w", err)
	}

	fmt.Printf("Project registered as '%s'\n", name)
	fmt.Printf("  Path: %s\n", cwd)
	fmt.Printf("  Data: %s\n", dataDir)
	fmt.Println()
	fmt.Println("Run 'vecgrep index' to index the project.")

	return nil
}

func runProjectsRemove(cmd *cobra.Command, args []string) error {
	name := args[0]

	if err := config.RemoveProjectFromGlobal(name); err != nil {
		return fmt.Errorf("failed to remove project: %w", err)
	}

	fmt.Printf("Project '%s' removed from global config.\n", name)
	fmt.Println("Note: Project data directory was not deleted. Remove it manually if needed.")

	return nil
}
