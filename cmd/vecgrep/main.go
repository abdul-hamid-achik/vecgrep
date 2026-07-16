package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	embeddingbench "github.com/abdul-hamid-achik/vecgrep/internal/benchmark"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/daemon"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/git"
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
  set     Set a configuration value
  preset  List or apply an embedding preset`,
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

var configPresetCmd = &cobra.Command{
	Use:   "preset [name]",
	Short: "List or apply an embedding preset",
	Long: `List the supported embedding presets or apply one to project/global configuration.

Applying a preset changes the semantic embedding profile. Pull the selected
Ollama model if needed, then rebuild the index with 'vecgrep index --full'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runConfigPreset,
}

var benchmarkCmd = &cobra.Command{
	Use:   "benchmark",
	Short: "Run reproducible vecgrep benchmarks",
}

var benchmarkEmbeddingsCmd = &cobra.Command{
	Use:   "embeddings",
	Short: "Compare embedding presets on a labeled code-retrieval corpus",
	Long: `Embed a deterministic labeled corpus without reading or writing the vecgrep index.

The default corpus combines anchored vecgrep source symbols with inline Go,
TypeScript, Python, Rust, YAML, Markdown, and shell examples.`,
	RunE: runBenchmarkEmbeddings,
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

var projectsPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Remove registry entries whose project path no longer exists",
	Long: `Remove stale entries from ~/.vecgrep/config.yaml.

An entry is stale when its project path no longer exists on disk (deleted
checkouts, temp directories from old runs). Entries whose path merely cannot
be checked are kept. With --purge-data, each pruned entry's index data under
~/.vecgrep/projects/ is deleted as well; data directories outside that
location are never touched.`,
	Args: cobra.NoArgs,
	RunE: runProjectsPrune,
}

// --- branch commands ---

var branchCmd = &cobra.Command{
	Use:   "branch",
	Short: "Manage per-branch indexes",
	Long: `Manage per-branch vector indexes.

When inside a git repository, vecgrep keeps a separate index per branch
so switching branches doesn't produce stale results. Branch indexes are
stored under ~/.vecgrep/projects/{name}/branches/{branch}/.

Subcommands:
  switch       Switch the active index to the current (or specified) branch
  snapshot     Snapshot the current branch's index to fcheap
  status       Show branch index status
  install-hook Install a post-checkout git hook for automatic branch switching`,
}

var branchSwitchCmd = &cobra.Command{
	Use:   "switch [branch]",
	Short: "Switch the active index to the current (or specified) branch",
	RunE:  runBranchSwitch,
}

var branchSnapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Snapshot the current branch's index to fcheap",
	RunE:  runBranchSnapshot,
}

var branchStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show branch index status",
	RunE:  runBranchStatus,
}

var branchInstallHookCmd = &cobra.Command{
	Use:   "install-hook",
	Short: "Install a post-checkout git hook for automatic branch switching",
	RunE:  runBranchInstallHook,
}

var branchPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Prune branch index snapshots for deleted branches",
	Long: `Prune fcheap branch index snapshots whose git branches have been deleted.

This runs fcheap cleanup-smart with the branch-gone category, filtered to
stashes tagged with "branch:". Best-effort: if fcheap is not available,
prints a notice and exits.

Requires fcheap to be installed and on $PATH.`,
	RunE: runBranchPrune,
}

// --- cache commands ---

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage the embedding cache and fcheap stash/restore",
	Long: `Manage the disk-persistent embedding cache and its fcheap snapshot/restore
integration.

Subcommands:
  status   Show embedding cache state (disk path, size, fcheap stashes count)
  save     Manually stash the current cache to fcheap
  restore  Manually restore the latest cache from fcheap
  sweep    Clean up old/superseded cache stashes in fcheap`,
}

var cacheStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show embedding cache state",
	RunE:  runCacheStatus,
}

var cacheSaveCmd = &cobra.Command{
	Use:   "save",
	Short: "Manually stash the current cache to fcheap",
	RunE:  runCacheSave,
}

var cacheRestoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Manually restore from the latest fcheap stash",
	RunE:  runCacheRestore,
}

var cacheSweepCmd = &cobra.Command{
	Use:   "sweep",
	Short: "Clean up old/superseded cache stashes",
	RunE:  runCacheSweep,
}

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the background indexing daemon",
	Long: `Manage the background indexing daemon.

The daemon is a multi-project hub: one process listens on a single global
unix socket (~/.vecgrep/daemon.sock) and serves many projects, opening each
lazily on first request. It watches files for changes, throttles Ollama
embedding requests, and is the sole writer to each project's index — the CLI
and MCP server connect over the socket or fall back to read-only sessions.

Subcommands:
  start    Start the daemon hub (foreground)
  stop     Stop the running daemon hub
  status   Show daemon hub status and open projects`,
}

var daemonStartCmd = &cobra.Command{
	Use:   "start [project-root...]",
	Short: "Start the background indexing daemon hub",
	Long: `Start the background indexing daemon hub.

The hub serves all projects over one socket. Any project roots given as
arguments are pre-opened (warmed) at startup; others open lazily on first
request. With no arguments, the current project (if cwd is inside one) is
pre-opened.`,
	RunE: runDaemonStart,
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running daemon",
	RunE:  runDaemonStop,
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show daemon status",
	RunE:  runDaemonStatus,
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
	indexCmd.Flags().Bool("dry-run", false, "preview changes without calling the embedding provider")
	indexCmd.Flags().String("structural-chunks", "", "codemap symbol chunks: auto, off, or required (overrides config)")

	// Search command flags
	searchCmd.Flags().IntP("limit", "n", 10, "maximum number of results")
	searchCmd.Flags().StringP("format", "f", "default", "output format (default, json, compact, json-envelope)")
	searchCmd.Flags().StringP("lang", "l", "", "filter by programming language")
	searchCmd.Flags().StringSlice("languages", nil, "filter by multiple languages (comma-separated)")
	searchCmd.Flags().StringP("type", "t", "", "filter by chunk type (function, class, block)")
	searchCmd.Flags().StringSlice("types", nil, "filter by multiple chunk types (comma-separated)")
	searchCmd.Flags().String("file", "", "filter by file pattern (glob)")
	searchCmd.Flags().String("dir", "", "filter by directory prefix")
	searchCmd.Flags().String("lines", "", "filter by line range (e.g., '1-100')")
	searchCmd.Flags().StringP("mode", "m", "hybrid", "search mode: semantic, keyword, or hybrid")
	searchCmd.Flags().Bool("explain", false, "show search diagnostics")
	searchCmd.Flags().StringSlice("scope-files", nil, "restrict search to these relative paths (comma-separated)")
	searchCmd.Flags().String("symbol", "", "scope search to a symbol's blast radius via codemap impact")
	searchCmd.Flags().Float32("min-score", 0, "drop results with score below this threshold (0-1)")

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
	similarCmd.Flags().Float32("min-score", 0, "drop results with score below this threshold (0-1)")

	// Status command flags
	statusCmd.Flags().StringP("format", "f", "default", "output format (default, json)")

	// Reset command flags
	resetCmd.Flags().Bool("force", false, "skip confirmation prompt")

	// Config show command flags
	configShowCmd.Flags().Bool("global", false, "show global config only")

	// Config set command flags
	configSetCmd.Flags().Bool("global", false, "set value in global config")

	// Config preset command flags
	configPresetCmd.Flags().Bool("global", false, "apply to global defaults")
	configCmd.AddCommand(configPresetCmd)

	// Embedding benchmark flags
	benchmarkEmbeddingsCmd.Flags().String("root", ".", "project root used to resolve source-backed documents")
	benchmarkEmbeddingsCmd.Flags().String("dataset", "", "path to a custom benchmark dataset JSON")
	benchmarkEmbeddingsCmd.Flags().StringSlice("profiles", []string{"fast-local", "quality-code"}, "embedding presets to compare")
	benchmarkEmbeddingsCmd.Flags().Int("batch-size", 32, "documents per embedding request")
	benchmarkEmbeddingsCmd.Flags().Bool("json", false, "emit the complete benchmark report as JSON")
	benchmarkCmd.AddCommand(benchmarkEmbeddingsCmd)

	// Add config subcommands
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configSetCmd)

	// Add projects subcommands
	projectsCmd.AddCommand(projectsListCmd)
	projectsCmd.AddCommand(projectsAddCmd)
	projectsCmd.AddCommand(projectsRemoveCmd)
	projectsCmd.AddCommand(projectsPruneCmd)
	projectsPruneCmd.Flags().Bool("dry-run", false, "list stale entries without removing anything")
	projectsPruneCmd.Flags().Bool("purge-data", false, "also delete pruned entries' data under ~/.vecgrep/projects/")

	// Memory command flags
	memoryRecallCmd.Flags().String("tags", "", "comma-separated tags; a memory must carry ALL of them (AND)")
	memoryRecallCmd.Flags().Float64("min-importance", 0, "minimum importance threshold (0-1)")
	memoryRecallCmd.Flags().IntP("limit", "n", 10, "maximum number of results")
	memoryRecallCmd.Flags().StringP("format", "f", "default", "output format (default, json)")
	memoryRememberCmd.Flags().String("tags", "", "comma-separated tags (e.g. codemap,<project_key>)")
	memoryRememberCmd.Flags().Float64("importance", 0.5, "importance (0-1)")
	memoryRememberCmd.Flags().Int("ttl-hours", 0, "expiration in hours (0 = never)")

	// Add memory subcommands
	memoryCmd.AddCommand(memoryRecallCmd)
	memoryCmd.AddCommand(memoryRememberCmd)

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
	rootCmd.AddCommand(branchCmd)
	rootCmd.AddCommand(daemonCmd)
	rootCmd.AddCommand(memoryCmd)
	rootCmd.AddCommand(benchmarkCmd)

	// Daemon subcommands
	daemonCmd.AddCommand(daemonStartCmd)
	daemonCmd.AddCommand(daemonStopCmd)
	daemonCmd.AddCommand(daemonStatusCmd)

	// Branch subcommands
	branchCmd.AddCommand(branchSwitchCmd)
	branchCmd.AddCommand(branchSnapshotCmd)
	branchCmd.AddCommand(branchStatusCmd)
	branchCmd.AddCommand(branchInstallHookCmd)
	branchCmd.AddCommand(branchPruneCmd)

	// Cache subcommands
	cacheCmd.AddCommand(cacheStatusCmd)
	cacheCmd.AddCommand(cacheSaveCmd)
	cacheCmd.AddCommand(cacheRestoreCmd)
	cacheCmd.AddCommand(cacheSweepCmd)
	rootCmd.AddCommand(cacheCmd)
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
	// If the daemon hub is running, it owns the exclusive write lock for every
	// open project. Delegate the reindex to it over the socket instead of
	// opening a second write session (which would collide with the daemon's
	// lock — the "database file is locked by another process" error). --dry-run
	// is a read-only preview, so it uses a read-only session instead.
	if gdir, err := config.GetGlobalConfigDir(); err == nil && daemon.IsRunning(gdir) {
		return indexViaDaemon(cmd, args, gdir)
	}
	structuralMode, _ := cmd.Flags().GetString("structural-chunks")
	if _, err := app.ParseStructuralChunksMode(structuralMode); err != nil {
		return err
	}
	session, err := app.OpenSession(cmd.Context(), "")
	if err != nil {
		if errors.Is(err, veclite.ErrFileLocked) || strings.Contains(strings.ToLower(err.Error()), "locked") {
			fmt.Fprintln(os.Stderr, "\nError: the database is locked. The daemon may be running.")
			fmt.Fprintln(os.Stderr, "  Use 'vecgrep daemon reindex' or stop the daemon first.")
			os.Exit(1)
		}
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	// --dry-run: preview changes without calling the embedding provider.
	if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
		preview, err := service.DryRunPreviewWithStructuralMode(cmd.Context(), structuralMode)
		if err != nil {
			return fmt.Errorf("dry-run failed: %w", err)
		}
		fmt.Printf("Dry run for %s\n", session.ProjectRoot)
		fmt.Printf("  New files:       %d\n", preview.NewFiles)
		fmt.Printf("  Modified files:  %d\n", preview.ModifiedFiles)
		fmt.Printf("  Deleted files:   %d\n", preview.DeletedFiles)
		fmt.Printf("  Files to embed:  %d\n", preview.FilesToEmbed)
		fmt.Printf("  Estimated chunks: %d\n", preview.EstimatedChunks)
		if preview.TotalPending == 0 {
			fmt.Println("\nIndex is up to date — nothing to do.")
		} else {
			fmt.Printf("\nRun 'vecgrep index' to update the index.\n")
		}
		return nil
	}

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

	// Perform indexing
	req := app.IndexRequest{
		Paths:             args,
		FullReindex:       fullReindex,
		AdditionalIgnores: additionalIgnores,
		StructuralChunks:  structuralMode,
	}
	if fullReindex {
		fmt.Println("  Mode: full re-index")
	} else {
		fmt.Println("  Mode: incremental")
	}

	var result *index.IndexResult
	if showProgress {
		// Live gradient progress bar (Bubble Tea), matching codemap's index UX.
		result, err = runIndexWithBar(cmd.Context(), service, req)
	} else {
		var progressCB index.ProgressCallback
		if verbose {
			progressCB = func(p index.Progress) {
				// \033[K erases from the cursor to end of line, so a shorter
				// line (shorter filename or smaller counts) doesn't leave
				// trailing characters from the previous, longer line.
				//
				// Only show rate/ETA after at least 1 file is processed and
				// elapsed > 1 second to avoid division by zero and misleading
				// early rates.
				elapsed := time.Since(p.StartTime)
				if p.ProcessedFiles > 0 && elapsed > time.Second {
					rate := float64(p.ProcessedFiles) / elapsed.Seconds()
					remaining := p.TotalFiles - p.ProcessedFiles
					eta := time.Duration(float64(remaining) / rate * float64(time.Second))
					fmt.Printf("\r  %s (%d/%d files, %d chunks, %.1f files/s, ETA %s)\033[K",
						p.CurrentFile, p.ProcessedFiles, p.TotalFiles, p.TotalChunks,
						rate, formatETA(eta))
				} else {
					fmt.Printf("\r  %s (%d/%d files, %d chunks)\033[K",
						p.CurrentFile, p.ProcessedFiles, p.TotalFiles, p.TotalChunks)
				}
			}
		}
		result, err = service.Index(cmd.Context(), req, progressCB)
	}

	if err != nil {
		if verbose {
			fmt.Println() // newline before error so the bar isn't overwritten
		}
		return fmt.Errorf("indexing failed: %w", err)
	}

	if verbose {
		fmt.Println() // new line after the verbose \r line
	}
	fmt.Printf("\nIndexing complete:\n")
	fmt.Printf("  Files processed: %d\n", result.FilesProcessed)
	fmt.Printf("  Files skipped (unchanged): %d\n", result.FilesSkipped)
	fmt.Printf("  Files deleted: %d\n", result.FilesDeleted)
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

// indexViaDaemon handles `vecgrep index` when the daemon hub is running. The
// real reindex is delegated to the daemon over its control socket (so the CLI
// never opens a second write handle that would collide with the daemon's
// exclusive lock); --dry-run uses a read-only session for the preview. It
// forwards --full, selected paths, structural mode, and one-run ignores.
func indexViaDaemon(cmd *cobra.Command, args []string, globalDataDir string) error {
	projectRoot, err := config.GetProjectRoot()
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	verbose, _ := rootCmd.PersistentFlags().GetBool("verbose")
	structuralMode, _ := cmd.Flags().GetString("structural-chunks")
	if _, err := app.ParseStructuralChunksMode(structuralMode); err != nil {
		return err
	}

	if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
		session, err := app.OpenReadOnlySession(cmd.Context(), "")
		if err != nil {
			return err
		}
		defer session.Close()
		service := app.NewService(session)
		preview, err := service.DryRunPreviewWithStructuralMode(cmd.Context(), structuralMode)
		if err != nil {
			return fmt.Errorf("dry-run failed: %w", err)
		}
		fmt.Printf("Dry run for %s\n", session.ProjectRoot)
		fmt.Printf("  New files:       %d\n", preview.NewFiles)
		fmt.Printf("  Modified files:  %d\n", preview.ModifiedFiles)
		fmt.Printf("  Deleted files:   %d\n", preview.DeletedFiles)
		fmt.Printf("  Files to embed:  %d\n", preview.FilesToEmbed)
		fmt.Printf("  Estimated chunks: %d\n", preview.EstimatedChunks)
		if preview.TotalPending == 0 {
			fmt.Println("\nIndex is up to date — nothing to do.")
		} else {
			fmt.Printf("\nRun 'vecgrep index' to update the index.\n")
		}
		return nil
	}

	fullReindex, _ := cmd.Flags().GetBool("full")
	additionalIgnores, _ := cmd.Flags().GetStringSlice("ignore")
	fmt.Printf("Indexing %s (via daemon)...\n", projectRoot)
	if fullReindex {
		fmt.Println("  Mode: full re-index")
	} else {
		fmt.Println("  Mode: incremental")
	}

	result, err := daemon.ReindexSync(cmd.Context(), globalDataDir, projectRoot, app.IndexRequest{
		Paths:             args,
		FullReindex:       fullReindex,
		AdditionalIgnores: additionalIgnores,
		StructuralChunks:  structuralMode,
	})
	if err != nil {
		return fmt.Errorf("delegate to daemon: %w", err)
	}

	fmt.Printf("\nIndexing complete (via daemon):\n")
	fmt.Printf("  Files processed: %d\n", result.FilesProcessed)
	fmt.Printf("  Files skipped (unchanged): %d\n", result.FilesSkipped)
	fmt.Printf("  Files deleted: %d\n", result.FilesDeleted)
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
	scopeFiles, _ := cmd.Flags().GetStringSlice("scope-files")
	symbol, _ := cmd.Flags().GetString("symbol")
	minScore, _ := cmd.Flags().GetFloat32("min-score")

	// Parse line range
	var minLine, maxLine int
	if linesRange != "" {
		minLine, maxLine = app.ParseLineRange(linesRange)
	}

	// Try searching via the daemon socket first. If the daemon is running,
	// this avoids opening a separate read-only session and re-initializing
	// the embedding provider. Falls back transparently if the socket is
	// unavailable or the request fails. The json-envelope format needs
	// index metadata from a session, so it always takes the session path.
	if format != "json-envelope" {
		if ok := tryDaemonSearch(cmd.Context(), query, limit, modeStr, lang, languages, chunkTypes, chunkType, filePattern, directory, minLine, maxLine, minScore, explain, format, scopeFiles, symbol); ok {
			return nil
		}
	}

	// Fallback: open a read-only session and search directly.
	session, err := app.OpenReadOnlySession(cmd.Context(), "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	// Parse search mode
	mode := app.ParseSearchMode(modeStr, session.Config.Search.DefaultMode)

	// Machine formats (json/compact/json-envelope) must keep stdout a single
	// parseable JSON document, so human-facing scope/diagnostic notes go to
	// stderr when those formats are in use.
	machine := isMachineFormat(format)
	noteOut := func(format string, args ...any) {
		if machine {
			fmt.Fprintf(os.Stderr, format, args...)
		} else {
			fmt.Printf(format, args...)
		}
	}

	// Resolve file scoping. When --symbol is set, use codemap impact to
	// compute the blast radius. When --scope-files is set, use it directly.
	var filePaths []string
	if len(scopeFiles) > 0 {
		filePaths = scopeFiles
		noteOut("Scope: restricted to %d file(s)\n", len(filePaths))
	} else if symbol != "" {
		resolvedPaths, scopeNote := resolveSymbolScope(cmd.Context(), session, symbol)
		if scopeNote != "" {
			noteOut("%s\n", scopeNote)
		}
		filePaths = resolvedPaths
	}

	resp, err := service.Search(cmd.Context(), app.SearchRequest{
		Query:       query,
		Limit:       limit,
		Language:    lang,
		Languages:   languages,
		ChunkType:   chunkType,
		ChunkTypes:  chunkTypes,
		FilePattern: filePattern,
		Directory:   directory,
		FilePaths:   filePaths,
		MinLine:     minLine,
		MaxLine:     maxLine,
		MinScore:    minScore,
		Mode:        mode,
		Explain:     explain,
	})
	if err != nil {
		return fmt.Errorf("search failed: %w", err)
	}

	// Surface degraded-mode diagnostics (e.g. embedder unavailable →
	// keyword-only results) so a fallback is never silent. Warnings go to
	// stderr for machine formats so stdout stays a single JSON document.
	for _, w := range resp.Warnings {
		noteOut("Warning: %s\n", w)
	}

	if explain && resp.Diagnostics != nil {
		// Print explanation. Routed to stderr for machine formats so stdout
		// stays a single JSON document.
		noteOut("Search Diagnostics:\n")
		noteOut("  Index type: %s\n", resp.Diagnostics.IndexType)
		noteOut("  Nodes visited: %d\n", resp.Diagnostics.NodesVisited)
		noteOut("  Duration: %v\n", resp.Diagnostics.Duration)
		noteOut("  Mode: %s\n", resp.Diagnostics.Mode)
		noteOut("\n")
	}

	if format == "json-envelope" {
		return printSearchEnvelope(cmd.Context(), service, resp.Results)
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

// isMachineFormat reports whether format emits machine-parseable output on
// stdout, where any leading non-JSON text (scope notes, diagnostics) would
// corrupt a single-document decode. json/compact/json-envelope all qualify.
func isMachineFormat(format string) bool {
	switch format {
	case "json", "compact", "json-envelope":
		return true
	}
	return false
}

const searchEnvelopeSchemaVersion = 1

type searchEnvelope struct {
	SchemaVersion int `json:"schema_version"`
	Index         struct {
		Indexed bool `json:"indexed"`
		Fresh   bool `json:"fresh"`
		Chunks  int  `json:"chunks"`
	} `json:"index"`
	Hits []search.Result `json:"hits"`
}

// printSearchEnvelope emits the json-envelope contract: a single JSON object
// carrying index state alongside the hits, so a consumer can distinguish
// "never indexed" (indexed=false) from "indexed but nothing matched"
// (indexed=true, hits=[]). The bare-array `json` format is unchanged.
func printSearchEnvelope(ctx context.Context, service *app.Service, results []search.Result) error {
	indexed, fresh, chunks, err := service.IndexMeta(ctx)
	if err != nil {
		return fmt.Errorf("index metadata: %w", err)
	}
	envelope := searchEnvelope{
		SchemaVersion: searchEnvelopeSchemaVersion,
		Hits:          results,
	}
	envelope.Index.Indexed = indexed
	envelope.Index.Fresh = fresh
	envelope.Index.Chunks = chunks
	if envelope.Hits == nil {
		envelope.Hits = []search.Result{}
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// tryDaemonSearch attempts to run a search through the daemon's unix socket.
// It returns (true) if the search was performed and results were rendered,
// or (false) if the daemon socket is unavailable, the request failed, or
// the query uses filters the daemon protocol does not yet support (in which
// case the caller falls back to a read-only session).
func tryDaemonSearch(
	ctx context.Context,
	query string,
	limit int,
	modeStr, lang string,
	languages, chunkTypes []string,
	chunkType, filePattern, directory string,
	minLine, maxLine int,
	minScore float32,
	explain bool,
	format string,
	scopeFiles []string,
	symbol string,
) bool {
	_ = ctx // reserved for future context-aware socket dial

	// The daemon search protocol currently supports query, limit, mode, and
	// language. If more complex filters are requested, fall back to the
	// read-only session which has full filter support.
	if len(languages) > 0 || len(chunkTypes) > 0 || chunkType != "" || filePattern != "" || directory != "" || minLine != 0 || maxLine != 0 || explain || len(scopeFiles) > 0 || symbol != "" {
		return false
	}

	// Find the project root and data dir to locate the daemon socket.
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	projectRoot, err := config.FindProjectRootFrom(cwd)
	if err != nil {
		return false
	}
	// The hub listens on one global socket and routes by project root.
	globalDir, err := config.GetGlobalConfigDir()
	if err != nil {
		return false
	}
	socketPath := filepath.Join(globalDir, "daemon.sock")

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return false // daemon not running
	}
	defer conn.Close()

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	params := struct {
		Project  string  `json:"project"`
		Query    string  `json:"query"`
		Limit    int     `json:"limit"`
		Mode     string  `json:"mode"`
		Language string  `json:"language,omitempty"`
		MinScore float32 `json:"min_score,omitempty"`
	}{
		Project:  projectRoot,
		Query:    query,
		Limit:    limit,
		Mode:     modeStr,
		Language: lang,
		MinScore: minScore,
	}
	paramsJSON, _ := json.Marshal(params)

	if err := enc.Encode(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Method:  "daemon.search",
		Params:  paramsJSON,
	}); err != nil {
		return false
	}

	var resp struct {
		Result struct {
			Results  []search.Result `json:"results"`
			Mode     string          `json:"mode"`
			Warnings []string        `json:"warnings"`
		} `json:"result,omitempty"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := dec.Decode(&resp); err != nil {
		return false
	}
	if resp.Error != nil {
		return false // let the fallback handle the real error
	}

	// Surface degraded-mode diagnostics (e.g. embedder unavailable →
	// keyword-only results) so a daemon-served fallback is never silent.
	// Warnings go to stderr for machine formats so stdout stays a single
	// JSON document, matching the session search path.
	for _, w := range resp.Result.Warnings {
		if isMachineFormat(format) {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		} else {
			fmt.Printf("Warning: %s\n", w)
		}
	}

	printSearchResults(resp.Result.Results, format)
	return true
}

// resolveSymbolScope uses codemap impact to compute the blast radius of a
// symbol and returns the affected file set. When codemap is unavailable or
// not indexed, it returns nil files and a human-readable note so the caller
// can fall back to unscoped search.
func resolveSymbolScope(ctx context.Context, session *app.Session, symbol string) ([]string, string) {
	cfg := session.Config.Codemap
	if !cfg.Enabled {
		return nil, fmt.Sprintf("Scope: codemap not enabled — searching unscoped for symbol %q", symbol)
	}

	client := mcp.NewCodemapClient(cfg)
	if !client.Available() {
		return nil, fmt.Sprintf("Scope: codemap binary not found — searching unscoped for symbol %q", symbol)
	}

	depth := int(cfg.ImpactDepth)
	result, err := client.Impact(ctx, session.ProjectRoot, symbol, depth)
	if err != nil {
		return nil, fmt.Sprintf("Scope: codemap impact failed for %q (%v) — searching unscoped", symbol, err)
	}
	if !result.Indexed {
		return nil, fmt.Sprintf("Scope: codemap graph not indexed for %q — searching unscoped", symbol)
	}

	var files []string
	for _, f := range result.Files {
		if f.RelativePath != "" {
			files = append(files, f.RelativePath)
		}
	}

	if len(files) == 0 {
		return nil, fmt.Sprintf("Scope: codemap impact for %q returned no affected files — searching unscoped", symbol)
	}

	return files, fmt.Sprintf("Scope: codemap impact for %q — %d file(s) in blast radius (radius: %d)", symbol, len(files), result.BlastRadius)
}

func runServe(cmd *cobra.Command, args []string) error {
	// The --mcp flag is retained for backward compatibility. The serve command
	// is MCP-only now that the browser UI has been removed.
	//
	// We deliberately do NOT open a session here. The MCP server opens the
	// database lazily on the first tool call — or routes reads/writes through a
	// running daemon over its socket — so it never holds a file lock while
	// idle. Opening a writable session up front would hold an exclusive lock
	// for the entire lifetime of the server, blocking `vecgrep daemon start`
	// (a second writer) with "database file is locked by another process".
	// (Read-only opens are lock-free since veclite v0.22.0, so an idle server
	// no longer blocks readers either.)
	projectRoot := ""
	if root, rootErr := config.GetProjectRoot(); rootErr == nil {
		projectRoot = root
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

	mcpServer := mcp.NewSDKServer(mcp.SDKServerConfig{ProjectRoot: projectRoot})
	return mcpServer.Run(ctx)
}

// StatusOutput represents the JSON output for the status command
type StatusOutput struct {
	ProjectRoot      string                    `json:"project_root"`
	DataDir          string                    `json:"data_dir"`
	Database         string                    `json:"database"`
	VectorBackend    string                    `json:"vector_backend"`
	EmbeddingModel   string                    `json:"embedding_model"`
	Provider         string                    `json:"provider"`
	Dimensions       int                       `json:"dimensions"`
	ProfilePath      string                    `json:"profile_path"`
	ProfileStatus    string                    `json:"profile_status"`
	ProfileMatches   bool                      `json:"profile_matches"`
	CurrentProfile   app.EmbeddingProfile      `json:"current_profile"`
	StoredProfile    *app.EmbeddingProfile     `json:"stored_profile,omitempty"`
	VecLiteBytes     int64                     `json:"veclite_bytes"`
	IndexedBytes     int64                     `json:"indexed_bytes"`
	LatestIndexed    string                    `json:"latest_indexed_at,omitempty"`
	IndexFresh       bool                      `json:"index_fresh"`
	Stats            map[string]int64          `json:"stats"`
	Languages        map[string]int64          `json:"languages,omitempty"`
	ChunkTypes       map[string]int64          `json:"chunk_types,omitempty"`
	PendingChanges   *PendingChanges           `json:"pending_changes,omitempty"`
	IngestionReceipt *app.IngestionReceipt     `json:"ingestion_receipt,omitempty"`
	ReceiptError     string                    `json:"ingestion_receipt_error,omitempty"`
	Freshness        *app.IndexFreshnessReport `json:"freshness,omitempty"`
}

// PendingChanges represents pending reindex changes
type PendingChanges struct {
	NewFiles      int `json:"new_files"`
	ModifiedFiles int `json:"modified_files"`
	DeletedFiles  int `json:"deleted_files"`
	TotalPending  int `json:"total_pending"`
}

func statusOutputFromResponse(status *app.StatusResponse) StatusOutput {
	output := StatusOutput{
		ProjectRoot:      status.ProjectRoot,
		DataDir:          status.DataDir,
		Database:         status.VecLitePath,
		VectorBackend:    status.VectorBackend,
		EmbeddingModel:   status.Model,
		Provider:         status.Provider,
		Dimensions:       status.Dimensions,
		ProfilePath:      status.ProfilePath,
		ProfileStatus:    status.ProfileStatus,
		ProfileMatches:   status.ProfileMatches,
		CurrentProfile:   status.CurrentProfile,
		StoredProfile:    status.StoredProfile,
		VecLiteBytes:     status.VecLiteSizeBytes,
		IndexedBytes:     status.IndexedBytes,
		IndexFresh:       status.IndexFresh,
		Stats:            status.Stats,
		IngestionReceipt: status.IngestionReceipt,
		ReceiptError:     status.ReceiptError,
		Freshness:        status.Freshness,
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
	return output
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
		output := statusOutputFromResponse(status)

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
	if status.IngestionReceipt != nil {
		receipt := status.IngestionReceipt
		fmt.Printf("  Structural ingestion: %s (requested %s, success=%t, complete=%t)\n", receipt.EffectiveMode, receipt.RequestedMode, receipt.Success, receipt.Complete)
	} else if status.ReceiptError != "" {
		fmt.Printf("  Structural ingestion: unknown (%s)\n", status.ReceiptError)
	}

	if status.Freshness != nil || status.PendingChanges != nil {
		fmt.Printf("\nReindex status:\n")
		if status.Freshness != nil {
			fmt.Printf("  Freshness:    %s (%s)\n", status.Freshness.State, status.Freshness.Reason)
		} else if status.IndexFresh {
			fmt.Printf("  Freshness:    fresh\n")
		} else {
			fmt.Printf("  Freshness:    unknown\n")
		}
		if status.PendingChanges != nil {
			fmt.Printf("  New files:      %d\n", status.PendingChanges.NewFiles)
			fmt.Printf("  Modified files: %d\n", status.PendingChanges.ModifiedFiles)
			fmt.Printf("  Deleted files:  %d\n", status.PendingChanges.DeletedFiles)
		}
		if status.PendingChanges != nil && status.PendingChanges.TotalPending > 0 {
			fmt.Printf("\nRun 'vecgrep index' to update the index.\n")
		} else if status.Freshness != nil && status.Freshness.State == app.IndexFreshnessUnknown {
			fmt.Printf("\nFreshness could not be proven; run 'vecgrep index --full' to rebuild trusted metadata.\n")
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

// formatETA formats a duration as a human-readable ETA string:
// < 60s = "Xs", < 60m = "Xm Ys", else = "Xh Ym".
func formatETA(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	remainingSeconds := seconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, remainingSeconds)
	}
	hours := minutes / 60
	remainingMinutes := minutes % 60
	return fmt.Sprintf("%dh %dm", hours, remainingMinutes)
}

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
	minScore, _ := cmd.Flags().GetFloat32("min-score")

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
		MinScore:        minScore,
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

func runConfigPreset(cmd *cobra.Command, args []string) error {
	out := cmd.OutOrStdout()
	if len(args) == 0 {
		fmt.Fprintln(out, "Embedding presets:")
		for _, preset := range config.ListEmbeddingPresets() {
			fmt.Fprintf(out, "  %-12s %s (%s, %d dimensions, context %d)\n",
				preset.Name,
				preset.Description,
				preset.Embedding.Model,
				preset.Embedding.Dimensions,
				preset.Embedding.OllamaContext,
			)
		}
		return nil
	}

	name := args[0]
	preset, ok := config.LookupEmbeddingPreset(name)
	if !ok {
		return fmt.Errorf("unknown embedding preset %q", name)
	}
	isGlobal, _ := cmd.Flags().GetBool("global")

	var configPath string
	if isGlobal {
		var err error
		configPath, err = config.GetGlobalConfigPath()
		if err != nil {
			return err
		}
	} else {
		projectRoot, err := config.GetProjectRoot()
		if err != nil {
			return fmt.Errorf("not in a vecgrep project: run 'vecgrep init' first")
		}
		configPath = projectConfigPath(projectRoot)
	}
	if err := config.ApplyEmbeddingPresetToFile(configPath, name, isGlobal); err != nil {
		return err
	}

	fmt.Fprintf(out, "Applied embedding preset %q to %s\n", name, configPath)
	fmt.Fprintf(out, "Model: %s (%d dimensions, context %d)\n",
		preset.Embedding.Model,
		preset.Embedding.Dimensions,
		preset.Embedding.OllamaContext,
	)
	fmt.Fprintf(out, "Next: ollama pull %s\n", preset.Embedding.Model)
	fmt.Fprintln(out, "Then: vecgrep index --full")
	return nil
}

func runBenchmarkEmbeddings(cmd *cobra.Command, _ []string) error {
	projectRoot, _ := cmd.Flags().GetString("root")
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return fmt.Errorf("resolve benchmark root: %w", err)
	}

	datasetPath, _ := cmd.Flags().GetString("dataset")
	var dataset embeddingbench.Dataset
	if datasetPath == "" {
		dataset, err = embeddingbench.LoadDefaultDataset(absRoot)
	} else {
		file, openErr := os.Open(datasetPath)
		if openErr != nil {
			return fmt.Errorf("open benchmark dataset: %w", openErr)
		}
		dataset, err = embeddingbench.LoadDataset(file, absRoot)
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}
	if err != nil {
		return err
	}

	names, _ := cmd.Flags().GetStringSlice("profiles")
	if len(names) == 0 {
		return errors.New("at least one embedding profile is required")
	}
	profiles := make([]embeddingbench.EmbeddingProfile, 0, len(names))
	for _, name := range names {
		preset, ok := config.LookupEmbeddingPreset(name)
		if !ok {
			return fmt.Errorf("unknown embedding preset %q", name)
		}
		profiles = append(profiles, embeddingbench.EmbeddingProfile{
			Name:             preset.Name,
			Provider:         preset.Embedding.Provider,
			Model:            preset.Embedding.Model,
			Dimensions:       preset.Embedding.Dimensions,
			OllamaContext:    preset.Embedding.OllamaContext,
			OllamaOptions:    preset.Embedding.OllamaOptions,
			QueryTemplate:    preset.Embedding.QueryTemplate,
			DocumentTemplate: preset.Embedding.DocumentTemplate,
		})
	}

	resolvedConfig, err := config.Load(absRoot)
	if err != nil {
		return fmt.Errorf("load benchmark configuration: %w", err)
	}
	factory := func(_ context.Context, profile embeddingbench.EmbeddingProfile) (embed.Provider, error) {
		if profile.Provider != "ollama" {
			return nil, fmt.Errorf("benchmark profile %q uses unsupported provider %q", profile.Name, profile.Provider)
		}
		return embed.NewOllamaProvider(embed.OllamaConfig{
			URL:        resolvedConfig.Embedding.OllamaURL,
			Model:      profile.Model,
			Dimensions: profile.Dimensions,
			Context:    profile.OllamaContext,
			Options:    profile.OllamaOptions,
		}), nil
	}

	batchSize, _ := cmd.Flags().GetInt("batch-size")
	report, err := (embeddingbench.Runner{
		Dataset:   dataset,
		BatchSize: batchSize,
	}).Run(cmd.Context(), profiles, factory)
	if err != nil {
		return err
	}

	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Embedding benchmark: %d documents, %d queries\n\n", len(dataset.Documents), len(dataset.Queries))
	fmt.Fprintln(out, "PROFILE       TOP-1   RECALL@5  RECALL@10  MRR     DOCS/S  QUERIES/S  CORPUS     QUERIES")
	for _, result := range report {
		fmt.Fprintf(out, "%-13s %6.1f%% %8.1f%% %9.1f%% %6.3f %7.1f %10.1f %10s %10s\n",
			result.Profile.Name,
			result.Metrics.Top1*100,
			result.Metrics.RecallAt5*100,
			result.Metrics.RecallAt10*100,
			result.Metrics.MRR,
			result.DocumentsPerSecond,
			result.QueriesPerSecond,
			result.CorpusLatency.Round(time.Millisecond),
			result.QueryLatency.Round(time.Millisecond),
		)
	}
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

func runProjectsPrune(cmd *cobra.Command, args []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	purgeData, _ := cmd.Flags().GetBool("purge-data")

	pruned, err := config.PruneGlobalProjects(dryRun, purgeData)
	if err != nil {
		return fmt.Errorf("failed to prune projects: %w", err)
	}

	if len(pruned) == 0 {
		fmt.Println("No stale projects.")
		return nil
	}

	verb := "Pruned"
	if dryRun {
		verb = "Would prune"
	}
	fmt.Printf("%s %d stale project(s):\n\n", verb, len(pruned))
	var reclaimed int64
	for _, p := range pruned {
		fmt.Printf("  %s\n", p.Name)
		fmt.Printf("    Path: %s (missing)\n", p.Path)
		if p.DataDir != "" && p.DataBytes > 0 {
			switch {
			case p.DataPurged:
				fmt.Printf("    Data: %s (%d bytes deleted)\n", p.DataDir, p.DataBytes)
				reclaimed += p.DataBytes
			case dryRun && purgeData:
				fmt.Printf("    Data: %s (%d bytes would be deleted)\n", p.DataDir, p.DataBytes)
			default:
				fmt.Printf("    Data: %s (%d bytes, kept; use --purge-data)\n", p.DataDir, p.DataBytes)
			}
		}
		fmt.Println()
	}
	if reclaimed > 0 {
		fmt.Printf("Reclaimed %d bytes of index data.\n", reclaimed)
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

// --- branch command runners ---

func runBranchSwitch(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	projectRoot, err := config.FindProjectRootFrom(cwd)
	if err != nil {
		return fmt.Errorf("no vecgrep project found: %w; run 'vecgrep init' first", err)
	}

	projectName, _, _ := config.FindProjectByPath(projectRoot)
	if projectName == "" {
		return fmt.Errorf("project not registered globally; branch switching requires global mode")
	}

	// Determine target branch
	targetBranch := ""
	if len(args) > 0 {
		targetBranch = args[0]
	} else {
		// Use current branch
		info, err := git.Detect(ctx, projectRoot)
		if err != nil {
			return fmt.Errorf("not a git repository: %w", err)
		}
		if info.Detached {
			return fmt.Errorf("detached HEAD — specify a branch name explicitly")
		}
		targetBranch = info.Branch
	}

	result, err := app.BranchSwitch(ctx, projectRoot, projectName, targetBranch)
	if err != nil {
		return fmt.Errorf("branch switch failed: %w", err)
	}

	fmt.Printf("Switched index from '%s' to '%s'\n", result.FromBranch, result.ToBranch)
	if result.Restored {
		fmt.Printf("  Restored from fcheap snapshot (stash ID: %s)\n", result.SnapshotID)
		fmt.Println("  Note: index was restored from a snapshot — no reindex needed.")
	} else {
		// No snapshot to restore — the caller needs to index the branch.
		// We auto-snapshot after indexing completes so the next switch is fast.
		fmt.Println("  Fresh index — indexing now...")

		// Open a session for the target branch and index it, then snapshot.
		session, sessErr := app.OpenSession(ctx, projectRoot)
		if sessErr != nil {
			fmt.Printf("  Warning: could not open session for indexing: %v\n", sessErr)
			fmt.Println("  Run 'vecgrep index' to build the index for this branch.")
		} else {
			service := app.NewService(session)
			_, idxErr := service.Index(ctx, app.IndexRequest{}, nil)
			if cerr := session.Close(); cerr != nil {
				fmt.Printf("  Warning: session close failed: %v\n", cerr)
			}
			if idxErr != nil {
				fmt.Printf("  Warning: indexing failed: %v\n", idxErr)
				fmt.Println("  Run 'vecgrep index' to build the index for this branch.")
			} else {
				// Auto-snapshot the freshly indexed branch directory.
				snapResult, snapErr := app.BranchSnapshot(ctx, projectRoot, projectName)
				if snapErr != nil {
					fmt.Printf("  Warning: auto-snapshot failed: %v\n", snapErr)
				} else if snapResult != nil && snapResult.SnapshotID != "" {
					fmt.Printf("  Auto-snapshotted to fcheap stash %s\n", snapResult.SnapshotID)
				}
			}
		}
	}
	fmt.Printf("  Duration: %s\n", result.Duration)
	fmt.Println("\nNote: The index will use the branch-specific directory on next search/index/status.")

	return nil
}

func runBranchSnapshot(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	projectRoot, err := config.FindProjectRootFrom(cwd)
	if err != nil {
		return fmt.Errorf("no vecgrep project found: %w; run 'vecgrep init' first", err)
	}

	projectName, _, _ := config.FindProjectByPath(projectRoot)
	if projectName == "" {
		return fmt.Errorf("project not registered globally; branch snapshotting requires global mode")
	}

	result, err := app.BranchSnapshot(ctx, projectRoot, projectName)
	if err != nil {
		return fmt.Errorf("branch snapshot failed: %w", err)
	}

	fmt.Printf("Snapshotted branch '%s' (SHA: %s)\n", result.ToBranch, result.ToSHA)
	if result.SnapshotID != "" {
		fmt.Printf("  fcheap stash ID: %s\n", result.SnapshotID)
	} else {
		fmt.Println("  fcheap not available — snapshot stored as branch index pointer only")
	}
	fmt.Printf("  Vectors: %d\n", result.VectorCount)
	fmt.Printf("  Duration: %s\n", result.Duration)

	return nil
}

func runBranchStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	projectRoot, err := config.FindProjectRootFrom(cwd)
	if err != nil {
		return fmt.Errorf("no vecgrep project found: %w; run 'vecgrep init' first", err)
	}

	projectName, _, _ := config.FindProjectByPath(projectRoot)
	if projectName == "" {
		return fmt.Errorf("project not registered globally")
	}

	idx, info, err := app.BranchStatus(ctx, projectRoot, projectName)
	if err != nil {
		return fmt.Errorf("branch status failed: %w", err)
	}

	fmt.Printf("Branch Index Status for: %s\n\n", projectName)
	fmt.Printf("Git:\n")
	fmt.Printf("  Repo root: %s\n", info.Root)
	if info.Detached {
		fmt.Printf("  HEAD: detached (%s)\n", info.Head)
	} else {
		fmt.Printf("  Branch: %s\n", info.Branch)
		fmt.Printf("  HEAD: %s\n", info.Head)
	}

	fmt.Printf("\nBranch Indexes:\n")
	if idx == nil || len(idx.Branches) == 0 {
		fmt.Println("  No branch indexes found. Run 'vecgrep branch switch' to create one.")
	} else {
		fmt.Printf("  Active branch: %s\n", idx.ActiveBranch)
		for name, entry := range idx.Branches {
			fmt.Printf("\n  %s:\n", name)
			fmt.Printf("    Base SHA: %s\n", entry.BaseSHA)
			fmt.Printf("    Vectors: %d\n", entry.VectorCount)
			if entry.StashID != "" {
				fmt.Printf("    Stash ID: %s\n", entry.StashID)
			}
			fmt.Printf("    Profile: %s/%s (%dd)\n", entry.EmbeddingProfile.Provider, entry.EmbeddingProfile.Model, entry.EmbeddingProfile.Dimensions)
			fmt.Printf("    Last switched: %s\n", entry.LastSwitchedAt.Format(time.RFC3339))
		}
	}

	return nil
}

func runBranchPrune(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	count, err := app.PruneBranchIndexes(ctx)
	if err != nil {
		// fcheap not available is a soft error, not a hard failure.
		if strings.Contains(err.Error(), "fcheap not found") {
			fmt.Println("fcheap not found, skipping prune")
			return nil
		}
		return fmt.Errorf("branch prune failed: %w", err)
	}

	fmt.Printf("Pruned %d branch index snapshots for deleted branches\n", count)
	return nil
}

// --- cache command runners ---

func runCacheStatus(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	session, err := app.OpenReadOnlySession(ctx, "")
	if err != nil {
		return err
	}
	defer session.Close()

	cachePath := app.ResolvedCachePath(session.Config)
	model := session.Config.Embedding.Model

	fmt.Printf("Embedding cache status\n")
	fmt.Printf("  Model: %s\n", model)
	fmt.Printf("  Disk cache path: %s\n", cachePath)

	if cachePath != "" {
		if info, statErr := os.Stat(cachePath); statErr == nil {
			fmt.Printf("  Disk cache size: %s\n", formatBytes(info.Size()))
		} else {
			fmt.Printf("  Disk cache size: (not yet created)\n")
		}
	}

	stashes := app.CountEmbeddingCacheStashes(ctx)
	fmt.Printf("  fcheap stashes: %d (tagged embedding-cache)\n", stashes)

	if session.Config.Cache.FcheapStashEnabled() {
		fmt.Printf("  fcheap stash: enabled (ttl: %s)\n", session.Config.Cache.FcheapTTL)
	} else {
		fmt.Printf("  fcheap stash: disabled\n")
	}

	return nil
}

func runCacheSave(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	session, err := app.OpenSession(ctx, "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	f := app.NewFcheapWrapper()
	if !f.Available() {
		fmt.Println("fcheap not found, skipping cache save")
		return nil
	}

	service.StashEmbeddingCacheManual(ctx)
	fmt.Println("Embedding cache stashed to fcheap")
	return nil
}

func runCacheRestore(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	session, err := app.OpenSession(ctx, "")
	if err != nil {
		return err
	}
	defer session.Close()
	service := app.NewService(session)

	f := app.NewFcheapWrapper()
	if !f.Available() {
		fmt.Println("fcheap not found, skipping cache restore")
		return nil
	}

	restored := service.RestoreEmbeddingCacheManual(ctx)
	if restored {
		fmt.Println("Embedding cache restored from fcheap")
	} else {
		fmt.Println("No matching embedding cache stash found in fcheap")
	}
	return nil
}

func runCacheSweep(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	f := app.NewFcheapWrapper()
	if !f.Available() {
		fmt.Println("fcheap not found, skipping sweep")
		return nil
	}

	count, err := app.SweepEmbeddingCaches(ctx)
	if err != nil {
		return fmt.Errorf("cache sweep failed: %w", err)
	}
	fmt.Printf("Swept %d embedding cache stashes from fcheap\n", count)
	return nil
}

func runBranchInstallHook(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get cwd: %w", err)
	}

	info, err := git.Detect(ctx, cwd)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}

	hookPath := filepath.Join(info.Root, ".git", "hooks", "post-checkout")

	// Check if hook already exists
	if _, err := os.Stat(hookPath); err == nil {
		// Check if it's already a vecgrep hook
		data, _ := os.ReadFile(hookPath)
		if strings.Contains(string(data), "vecgrep branch switch") {
			fmt.Println("post-checkout hook already installed for vecgrep.")
			return nil
		}
		return fmt.Errorf("post-checkout hook already exists (not a vecgrep hook); remove it first or merge manually")
	}

	// Write the hook
	hookContent := `#!/bin/sh
# vecgrep post-checkout hook — auto-switches the branch index
# arg1 = previous HEAD, arg2 = new HEAD, arg3 = flag (1=branch checkout, 0=file checkout)
prev="$1"
new="$2"
flag="$3"

# Only fire on branch checkout (flag=1), not file checkout (flag=0)
if [ "$flag" = "1" ]; then
    vecgrep branch switch >/dev/null 2>&1 || true
fi
`

	// Ensure hooks directory exists
	hooksDir := filepath.Dir(hookPath)
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("create hooks directory: %w", err)
	}

	if err := os.WriteFile(hookPath, []byte(hookContent), 0755); err != nil {
		return fmt.Errorf("write hook: %w", err)
	}

	fmt.Printf("Installed post-checkout hook at: %s\n", hookPath)
	fmt.Println("The hook will auto-switch the vecgrep index on branch checkout.")
	fmt.Println("To remove: delete the .git/hooks/post-checkout file.")

	return nil
}

// --- daemon command runners ---

func runDaemonStart(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Resolve the global data dir and hub-level config (defaults + global + env).
	globalDir, err := config.GetGlobalConfigDir()
	if err != nil {
		return fmt.Errorf("resolve global data dir: %w", err)
	}
	resolved, err := config.LoadResolved("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if daemon.IsRunning(globalDir) {
		return fmt.Errorf("daemon hub already running")
	}

	// Decide which projects to pre-open: explicit args, else the current
	// project if cwd is inside one. Others open lazily on first request.
	var preopen []string
	if len(args) > 0 {
		preopen = args
	} else if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		if root, rErr := config.FindProjectRootFrom(cwd); rErr == nil {
			preopen = []string{root}
		}
	}

	d, err := daemon.New(daemon.Config{
		ResolvedConfig: resolved.Config,
		DataDir:        globalDir,
	})
	if err != nil {
		return fmt.Errorf("create daemon: %w", err)
	}

	fmt.Println("Starting vecgrep daemon hub")
	fmt.Printf("  Socket: %s\n", d.SocketPath())
	fmt.Printf("  PID: %d\n", os.Getpid())
	if len(preopen) > 0 {
		fmt.Printf("  Pre-opening: %s\n", strings.Join(preopen, ", "))
	}

	// Handle signals for graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		d.Stop()
	}()

	return d.Start(ctx, preopen...)
}

func runDaemonStop(cmd *cobra.Command, args []string) error {
	globalDir, err := config.GetGlobalConfigDir()
	if err != nil {
		return fmt.Errorf("resolve global data dir: %w", err)
	}

	if !daemon.IsRunning(globalDir) {
		fmt.Println("No daemon hub running.")
		return nil
	}

	// The hub process handles SIGTERM for graceful shutdown. Read its PID from
	// the hub state file and signal it.
	state, err := daemon.ReadHubState(globalDir)
	if err != nil {
		return fmt.Errorf("read daemon state: %w", err)
	}
	if state.PID > 0 {
		p, err := os.FindProcess(state.PID)
		if err != nil {
			return fmt.Errorf("find process: %w", err)
		}
		if err := p.Signal(syscall.SIGTERM); err != nil {
			return fmt.Errorf("send SIGTERM: %w", err)
		}
	}

	fmt.Println("Daemon hub stopped.")
	return nil
}

func runDaemonStatus(cmd *cobra.Command, args []string) error {
	globalDir, err := config.GetGlobalConfigDir()
	if err != nil {
		return fmt.Errorf("resolve global data dir: %w", err)
	}

	if !daemon.IsRunning(globalDir) {
		fmt.Println("Daemon hub: not running")
		return nil
	}

	fmt.Println("Daemon hub: running")
	state, err := daemon.ReadHubState(globalDir)
	if err == nil && state != nil {
		fmt.Printf("  PID: %d\n", state.PID)
		fmt.Printf("  Started: %s\n", state.StartedAt.Format(time.RFC3339))
		fmt.Printf("  Socket: %s\n", filepath.Join(globalDir, "daemon.sock"))
		if len(state.Projects) == 0 {
			fmt.Println("  Projects: (none open — they open lazily on first request)")
		} else {
			fmt.Printf("  Projects (%d):\n", len(state.Projects))
			for _, p := range state.Projects {
				fmt.Printf("    - %s\n", p)
			}
		}
	}

	return nil
}
