package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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

	// Add commands
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(indexCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(statusCmd)
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
	provider := embed.NewOllamaProvider(embed.OllamaConfig{
		URL:        cfg.Embedding.OllamaURL,
		Model:      cfg.Embedding.Model,
		Dimensions: cfg.Embedding.Dimensions,
	})

	// Ping provider to verify it's available
	ctx := context.Background()
	if err := provider.Ping(ctx); err != nil {
		return fmt.Errorf("embedding provider unavailable: %w\nMake sure Ollama is running and the model '%s' is available", err, cfg.Embedding.Model)
	}

	// Get flags
	fullReindex, _ := cmd.Flags().GetBool("full")
	additionalIgnores, _ := cmd.Flags().GetStringSlice("ignore")

	// Create indexer config
	indexerCfg := index.DefaultIndexerConfig()
	indexerCfg.ChunkSize = cfg.Indexing.ChunkSize * 4   // Convert tokens to chars (approx)
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
	provider := embed.NewOllamaProvider(embed.OllamaConfig{
		URL:        cfg.Embedding.OllamaURL,
		Model:      cfg.Embedding.Model,
		Dimensions: cfg.Embedding.Dimensions,
	})

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
	provider := embed.NewOllamaProvider(embed.OllamaConfig{
		URL:        cfg.Embedding.OllamaURL,
		Model:      cfg.Embedding.Model,
		Dimensions: cfg.Embedding.Dimensions,
	})

	// Get flags
	host, _ := cmd.Flags().GetString("host")
	port, _ := cmd.Flags().GetInt("port")
	mcpMode, _ := cmd.Flags().GetBool("mcp")
	webMode, _ := cmd.Flags().GetBool("web")

	// Default to web mode if neither is specified
	if !mcpMode && !webMode {
		webMode = true
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

	// Start MCP server (stdio)
	if mcpMode {
		fmt.Fprintln(os.Stderr, "Starting MCP server on stdio...")
		mcpServer := mcp.NewServer(mcp.ServerConfig{
			DB:          database,
			Provider:    provider,
			ProjectRoot: projectRoot,
		})
		return mcpServer.Run(ctx)
	}

	// Start web server
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

	return nil
}
