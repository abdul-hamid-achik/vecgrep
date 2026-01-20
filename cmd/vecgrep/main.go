package main

import (
	"context"
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

	// Reset command flags
	resetCmd.Flags().Bool("force", false, "skip confirmation prompt")

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
}

func runInit(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}

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
	database, err := db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer database.Close()

	// Get and display sqlite-vec version
	vecVersion, err := database.VecVersion()
	if err != nil {
		return fmt.Errorf("failed to verify sqlite-vec: %w", err)
	}

	fmt.Printf("Initialized vecgrep in %s\n", dataDir)
	fmt.Printf("  Database: %s\n", cfg.DBPath)
	fmt.Printf("  sqlite-vec: %s\n", vecVersion)
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

	database, err := db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
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

	database, err := db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
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

		database, err = db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
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

func runStatus(cmd *cobra.Command, args []string) error {
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	database, err := db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	stats, err := database.Stats()
	if err != nil {
		return fmt.Errorf("failed to get stats: %w", err)
	}

	vecVersion, _ := database.VecVersion()

	fmt.Printf("vecgrep status\n")
	fmt.Printf("  Project root: %s\n", projectRoot)
	fmt.Printf("  Database: %s\n", cfg.DBPath)
	fmt.Printf("  sqlite-vec: %s\n", vecVersion)
	fmt.Printf("  Embedding model: %s (%s)\n", cfg.Embedding.Model, cfg.Embedding.Provider)
	fmt.Printf("\nIndex statistics:\n")
	fmt.Printf("  Projects:   %d\n", stats["projects"])
	fmt.Printf("  Files:      %d\n", stats["files"])
	fmt.Printf("  Chunks:     %d\n", stats["chunks"])
	fmt.Printf("  Embeddings: %d\n", stats["embeddings"])

	// Check for pending changes
	indexerCfg := index.DefaultIndexerConfig()
	indexerCfg.IgnorePatterns = append(cfg.Indexing.IgnorePatterns, indexerCfg.IgnorePatterns...)
	indexer := index.NewIndexer(database, nil, indexerCfg)

	ctx := context.Background()
	pending, err := indexer.GetPendingChanges(ctx, projectRoot)
	if err == nil {
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

	database, err := db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
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

	database, err := db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
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

	database, err := db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
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

	database, err := db.Open(cfg.DBPath, cfg.Embedding.Dimensions)
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
