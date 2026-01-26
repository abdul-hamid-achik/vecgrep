package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/index"
	"github.com/abdul-hamid-achik/vecgrep/internal/mcp"
	"github.com/abdul-hamid-achik/vecgrep/internal/search"
	"github.com/abdul-hamid-achik/vecgrep/internal/version"
	"github.com/abdul-hamid-achik/vecgrep/internal/web"
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
This creates a .vecgrep directory with the configuration and database.`,
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

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP and web server",
	Long: `Start the Model Context Protocol (MCP) server and optional web interface
for integration with AI assistants and browsers.`,
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
	Short: "Remove orphaned data and optimize database",
	Long: `Clean up orphaned data (chunks without files, embeddings without chunks)
and vacuum the database to reclaim space.`,
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

	// Search command flags
	searchCmd.Flags().IntP("limit", "n", 10, "maximum number of results")
	searchCmd.Flags().StringP("format", "f", "default", "output format (default, json, compact)")
	searchCmd.Flags().StringP("lang", "l", "", "filter by programming language")
	searchCmd.Flags().StringP("type", "t", "", "filter by chunk type (function, class, block)")
	searchCmd.Flags().String("file", "", "filter by file pattern (glob)")

	// Serve command flags
	serveCmd.Flags().IntP("port", "p", 8080, "server port")
	serveCmd.Flags().String("host", "localhost", "server host")
	serveCmd.Flags().Bool("mcp", false, "start MCP server (stdio)")
	serveCmd.Flags().Bool("web", false, "start web server")

	// Similar command flags
	similarCmd.Flags().IntP("limit", "n", 10, "maximum number of results")
	similarCmd.Flags().StringP("format", "f", "default", "output format (default, json, compact)")
	similarCmd.Flags().StringP("lang", "l", "", "filter by programming language")
	similarCmd.Flags().StringP("type", "t", "", "filter by chunk type (function, class, block)")
	similarCmd.Flags().String("file", "", "filter by file pattern (glob)")
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
	initCmd.Flags().Bool("global", false, "register project in ~/.vecgrep/ instead of creating local .vecgrep/")
	initCmd.Flags().Bool("local", false, "create local .vecgrep/ directory (default behavior)")
	initCmd.Flags().String("extension", "yaml", "preferred config file extension (yaml or yml)")

	// Add commands
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(indexCmd)
	rootCmd.AddCommand(searchCmd)
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

	// Global mode: register in ~/.vecgrep/projects/
	if globalMode {
		return runInitGlobal(cwd, force)
	}

	// Local mode (default): create .vecgrep/ directory
	return runInitLocal(cwd, force)
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
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	// Create embedding provider
	provider, err := createProvider(cfg)
	if err != nil {
		return fmt.Errorf("failed to create embedding provider: %w", err)
	}

	// Ping provider to verify it's available
	ctx := context.Background()
	if err := provider.Ping(ctx); err != nil {
		return fmt.Errorf("embedding provider unavailable: %w\nMake sure the provider is running and the model '%s' is available", err, cfg.Embedding.Model)
	}

	// Get flags
	fullReindex, _ := cmd.Flags().GetBool("full")
	additionalIgnores, _ := cmd.Flags().GetStringSlice("ignore")

	// Create indexer config
	indexerCfg := index.DefaultIndexerConfig()
	indexerCfg.ChunkSize = cfg.Indexing.ChunkSize * 4 // Convert tokens to chars (approx)
	indexerCfg.ChunkOverlap = cfg.Indexing.ChunkOverlap * 4
	indexerCfg.MaxFileSize = cfg.Indexing.MaxFileSize
	indexerCfg.IgnorePatterns = append(cfg.Indexing.IgnorePatterns, additionalIgnores...)

	// Create indexer
	indexer := index.NewIndexer(database, provider, indexerCfg)

	// Set up progress callback
	verbose, _ := rootCmd.PersistentFlags().GetBool("verbose")
	indexer.SetProgressCallback(func(p index.Progress) {
		if verbose {
			fmt.Printf("\r  %s (%d/%d files, %d chunks)",
				p.CurrentFile, p.ProcessedFiles, p.TotalFiles, p.TotalChunks)
		}
	})

	fmt.Printf("Indexing %s...\n", projectRoot)
	fmt.Printf("  Model: %s\n", cfg.Embedding.Model)

	// Perform indexing
	var result *index.IndexResult
	if fullReindex {
		fmt.Println("  Mode: full re-index")
		result, err = indexer.ReindexAll(ctx, projectRoot)
	} else {
		fmt.Println("  Mode: incremental")
		result, err = indexer.Index(ctx, projectRoot, args...)
	}

	if err != nil {
		return fmt.Errorf("indexing failed: %w", err)
	}

	if verbose {
		fmt.Println() // New line after progress
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
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	// Create embedding provider
	provider, err := createProvider(cfg)
	if err != nil {
		return fmt.Errorf("failed to create embedding provider: %w", err)
	}

	// Get flags
	query := strings.Join(args, " ")
	limit, _ := cmd.Flags().GetInt("limit")
	format, _ := cmd.Flags().GetString("format")
	lang, _ := cmd.Flags().GetString("lang")
	chunkType, _ := cmd.Flags().GetString("type")
	filePattern, _ := cmd.Flags().GetString("file")

	// Create searcher
	searcher := search.NewSearcher(database, provider)

	// Build search options
	opts := search.SearchOptions{
		Limit:       limit,
		Language:    lang,
		ChunkType:   chunkType,
		FilePattern: filePattern,
		ProjectRoot: projectRoot,
	}

	// Perform search
	ctx := context.Background()
	results, err := searcher.Search(ctx, query, opts)
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Format and print results
	var outputFormat search.OutputFormat
	switch format {
	case "json":
		outputFormat = search.FormatJSON
	case "compact":
		outputFormat = search.FormatCompact
	default:
		outputFormat = search.FormatDefault
	}

	fmt.Print(search.FormatResults(results, outputFormat))

	return nil
}

func runServe(cmd *cobra.Command, args []string) error {
	// Get flags
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")
	mcpMode, _ := cmd.Flags().GetBool("mcp")
	webMode, _ := cmd.Flags().GetBool("web")

	// Default to web mode if neither is specified
	if !mcpMode && !webMode {
		webMode = true
	}

	// Try to find project root - MCP mode can work without it
	projectRoot, projectErr := config.GetProjectRoot()

	var cfg *config.Config
	var database *db.DB
	var provider embed.Provider

	// If we have a project, load it fully
	if projectErr == nil {
		var err error
		cfg, err = config.Load(projectRoot)
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}

		database, err = db.OpenWithOptions(db.OpenOptions{
			Dimensions: cfg.Embedding.Dimensions,
			DataDir:    cfg.DataDir,
		})
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}
		defer database.Close()

		// Create embedding provider
		provider, err = createProvider(cfg)
		if err != nil {
			return fmt.Errorf("failed to create embedding provider: %w", err)
		}
	} else if !mcpMode {
		// Web mode requires an initialized project
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
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

	// Start MCP server (stdio) - works even without initialized project
	if mcpMode {
		// Use the official MCP SDK for reliable protocol handling
		mcpServer := mcp.NewSDKServer(mcp.SDKServerConfig{
			DB:          database,    // nil if not initialized
			Provider:    provider,    // nil if not initialized
			ProjectRoot: projectRoot, // empty if not initialized
		})
		return mcpServer.Run(ctx)
	}

	// Start web server (requires initialized project)
	if webMode {
		webServer := web.NewServer(web.ServerConfig{
			Host:        host,
			Port:        port,
			DB:          database,
			Provider:    provider,
			ProjectRoot: projectRoot,
		})

		fmt.Printf("Starting web server on http://%s:%d\n", host, port)
		fmt.Printf("  Project: %s\n", projectRoot)

		// Run until context is canceled
		errChan := make(chan error, 1)
		go func() {
			errChan <- webServer.ListenAndServe()
		}()

		select {
		case err := <-errChan:
			return err
		case <-ctx.Done():
			return nil
		}
	}

	return nil
}

// StatusOutput represents the JSON output for the status command
type StatusOutput struct {
	ProjectRoot    string           `json:"project_root"`
	Database       string           `json:"database"`
	VectorBackend  string           `json:"vector_backend"`
	EmbeddingModel string           `json:"embedding_model"`
	Provider       string           `json:"provider"`
	Stats          map[string]int64 `json:"stats"`
	PendingChanges *PendingChanges  `json:"pending_changes,omitempty"`
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

	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	stats, err := database.Stats()
	if err != nil {
		return fmt.Errorf("failed to get stats: %w", err)
	}

	vecVersion, _ := database.VecVersion()

	// Check for pending changes
	indexerCfg := index.DefaultIndexerConfig()
	indexerCfg.IgnorePatterns = append(cfg.Indexing.IgnorePatterns, indexerCfg.IgnorePatterns...)
	indexer := index.NewIndexer(database, nil, indexerCfg)

	ctx := context.Background()
	pending, pendingErr := indexer.GetPendingChanges(ctx, projectRoot)

	// JSON output
	if format == "json" {
		output := StatusOutput{
			ProjectRoot:    projectRoot,
			Database:       cfg.DBPath,
			VectorBackend:  vecVersion,
			EmbeddingModel: cfg.Embedding.Model,
			Provider:       cfg.Embedding.Provider,
			Stats:          stats,
		}

		if pendingErr == nil {
			output.PendingChanges = &PendingChanges{
				NewFiles:      pending.NewFiles,
				ModifiedFiles: pending.ModifiedFiles,
				DeletedFiles:  pending.DeletedFiles,
				TotalPending:  pending.TotalPending,
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
	fmt.Printf("  Project root: %s\n", projectRoot)
	fmt.Printf("  Database: %s\n", cfg.DBPath)
	fmt.Printf("  Vector backend: %s\n", vecVersion)
	fmt.Printf("  Embedding model: %s (%s)\n", cfg.Embedding.Model, cfg.Embedding.Provider)
	fmt.Printf("\nIndex statistics:\n")
	fmt.Printf("  Projects:   %d\n", stats["projects"])
	fmt.Printf("  Files:      %d\n", stats["files"])
	fmt.Printf("  Chunks:     %d\n", stats["chunks"])
	fmt.Printf("  Embeddings: %d\n", stats["embeddings"])

	if pendingErr == nil {
		fmt.Printf("\nReindex status:\n")
		fmt.Printf("  New files:      %d\n", pending.NewFiles)
		fmt.Printf("  Modified files: %d\n", pending.ModifiedFiles)
		fmt.Printf("  Deleted files:  %d\n", pending.DeletedFiles)
		if pending.TotalPending > 0 {
			fmt.Printf("\nRun 'vecgrep index' to update the index.\n")
		}
	}

	return nil
}

func runDelete(cmd *cobra.Command, args []string) error {
	filePath := args[0]

	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	ctx := context.Background()
	chunksDeleted, err := database.DeleteFile(ctx, filePath)
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	fmt.Printf("Deleted %s (%d chunks removed)\n", filePath, chunksDeleted)
	return nil
}

func runClean(cmd *cobra.Command, args []string) error {
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	fmt.Println("Cleaning database...")

	ctx := context.Background()
	stats, err := database.Clean(ctx)
	if err != nil {
		return fmt.Errorf("failed to clean database: %w", err)
	}

	fmt.Printf("Clean complete:\n")
	fmt.Printf("  Orphaned chunks removed: %d\n", stats.OrphanedChunks)
	fmt.Printf("  Orphaned embeddings removed: %d\n", stats.OrphanedEmbeddings)
	if stats.VacuumedBytes > 0 {
		fmt.Printf("  Space reclaimed: %d bytes\n", stats.VacuumedBytes)
	} else {
		fmt.Printf("  Database already optimized\n")
	}

	return nil
}

func runReset(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")

	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Confirmation prompt unless --force is used
	if !force {
		fmt.Printf("WARNING: This will delete ALL indexed data for %s\n", projectRoot)
		fmt.Printf("This action cannot be undone.\n\n")
		fmt.Printf("Type 'yes' to confirm: ")

		var confirmation string
		_, _ = fmt.Scanln(&confirmation)
		if confirmation != "yes" {
			fmt.Println("Reset cancelled.")
			return nil
		}
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	ctx := context.Background()
	if err := database.ResetAll(ctx); err != nil {
		return fmt.Errorf("failed to reset database: %w", err)
	}

	fmt.Println("Database reset complete. All indexed data has been cleared.")
	fmt.Println("Run 'vecgrep index' to re-index your codebase.")

	return nil
}

// createProvider creates an embedding provider based on config.
func createProvider(cfg *config.Config) (embed.Provider, error) {
	switch cfg.Embedding.Provider {
	case "openai":
		provider := embed.NewOpenAIProvider(embed.OpenAIConfig{
			APIKey:     cfg.Embedding.OpenAIAPIKey,
			BaseURL:    cfg.Embedding.OpenAIBaseURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
		})
		return provider, nil
	case "ollama", "":
		provider := embed.NewOllamaProvider(embed.OllamaConfig{
			URL:        cfg.Embedding.OllamaURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
		})
		return provider, nil
	default:
		return nil, fmt.Errorf("unknown embedding provider: %s", cfg.Embedding.Provider)
	}
}

func runSimilar(cmd *cobra.Command, args []string) error {
	// Get flags
	textSnippet, _ := cmd.Flags().GetString("text")
	limit, _ := cmd.Flags().GetInt("limit")
	format, _ := cmd.Flags().GetString("format")
	lang, _ := cmd.Flags().GetString("lang")
	chunkType, _ := cmd.Flags().GetString("type")
	filePattern, _ := cmd.Flags().GetString("file")
	excludeSameFile, _ := cmd.Flags().GetBool("exclude-same-file")

	// Validate input: either text flag or positional argument required
	if textSnippet == "" && len(args) == 0 {
		return fmt.Errorf("target required: provide a chunk ID, file:line location, or use --text")
	}
	if textSnippet != "" && len(args) > 0 {
		return fmt.Errorf("cannot specify both --text and a positional target")
	}

	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	// Create embedding provider
	provider, err := createProvider(cfg)
	if err != nil {
		return fmt.Errorf("failed to create embedding provider: %w", err)
	}

	// Create searcher
	searcher := search.NewSearcher(database, provider)

	// Build similar options
	opts := search.SimilarOptions{
		SearchOptions: search.SearchOptions{
			Limit:       limit,
			Language:    lang,
			ChunkType:   chunkType,
			FilePattern: filePattern,
			ProjectRoot: projectRoot,
		},
		ExcludeSameFile: excludeSameFile,
		ExcludeSourceID: true, // Default to excluding source
	}

	ctx := context.Background()
	var results []search.Result

	if textSnippet != "" {
		// Text-based search
		results, err = searcher.SearchSimilarByText(ctx, textSnippet, opts)
	} else {
		target := args[0]

		// Parse target: numeric → chunk ID, contains ":" → file:line
		if chunkID, parseErr := strconv.ParseInt(target, 10, 64); parseErr == nil {
			// Chunk ID
			results, err = searcher.SearchSimilarByID(ctx, chunkID, opts)
		} else if strings.Contains(target, ":") {
			// File:line location
			parts := strings.SplitN(target, ":", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid file:line format: %s", target)
			}
			line, lineErr := strconv.Atoi(parts[1])
			if lineErr != nil {
				return fmt.Errorf("invalid line number in %s: %w", target, lineErr)
			}
			results, err = searcher.SearchSimilarByLocation(ctx, parts[0], line, opts)
		} else {
			return fmt.Errorf("invalid target format: %s (expected chunk ID or file:line)", target)
		}
	}

	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Format and print results
	var outputFormat search.OutputFormat
	switch format {
	case "json":
		outputFormat = search.FormatJSON
	case "compact":
		outputFormat = search.FormatCompact
	default:
		outputFormat = search.FormatDefault
	}

	fmt.Print(search.FormatResults(results, outputFormat))

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
		// Set in global config
		globalCfg, err := config.LoadGlobalConfig()
		if err != nil {
			return fmt.Errorf("failed to load global config: %w", err)
		}

		// Parse dot-notation key
		if err := setConfigValue(&globalCfg.Defaults, key, value); err != nil {
			return err
		}

		if err := config.SaveGlobalConfig(globalCfg); err != nil {
			return fmt.Errorf("failed to save global config: %w", err)
		}

		fmt.Printf("Set %s = %s in global config\n", key, value)
		return nil
	}

	// Set in project config
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	// Determine config file path
	configPath := filepath.Join(projectRoot, "vecgrep.yaml")
	if _, err := os.Stat(filepath.Join(projectRoot, "vecgrep.yml")); err == nil {
		configPath = filepath.Join(projectRoot, "vecgrep.yml")
	}

	// Load or create project config
	cfg := &config.Config{}
	if data, err := os.ReadFile(configPath); err == nil {
		// TODO: parse existing yaml and merge
		_ = data
	}

	if err := setConfigValue(cfg, key, value); err != nil {
		return err
	}

	// Write config using viper
	v := viper.New()
	v.Set(key, value)
	if err := v.WriteConfigAs(configPath); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Printf("Set %s = %s in %s\n", key, value, configPath)
	return nil
}

// setConfigValue sets a value in a Config struct using dot notation
func setConfigValue(cfg *config.Config, key, value string) error {
	switch key {
	case "embedding.provider":
		cfg.Embedding.Provider = value
	case "embedding.model":
		cfg.Embedding.Model = value
	case "embedding.ollama_url":
		cfg.Embedding.OllamaURL = value
	case "embedding.openai_api_key":
		cfg.Embedding.OpenAIAPIKey = value
	case "embedding.openai_base_url":
		cfg.Embedding.OpenAIBaseURL = value
	case "embedding.dimensions":
		dim, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid dimensions value: %w", err)
		}
		cfg.Embedding.Dimensions = dim
	case "indexing.chunk_size":
		size, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid chunk_size value: %w", err)
		}
		cfg.Indexing.ChunkSize = size
	case "indexing.chunk_overlap":
		overlap, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid chunk_overlap value: %w", err)
		}
		cfg.Indexing.ChunkOverlap = overlap
	case "indexing.max_file_size":
		size, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid max_file_size value: %w", err)
		}
		cfg.Indexing.MaxFileSize = size
	case "server.host":
		cfg.Server.Host = value
	case "server.port":
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid port value: %w", err)
		}
		cfg.Server.Port = port
	case "data_dir":
		cfg.DataDir = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
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
