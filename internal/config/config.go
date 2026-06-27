package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool { return &b }

const (
	// DefaultDataDir is the default directory name for vecgrep data
	DefaultDataDir = ".vecgrep"
	// DefaultDBFile is the default database filename
	DefaultDBFile = "vecgrep.db"
	// DefaultConfigFile is the default config filename
	DefaultConfigFile = "config.yaml"
)

// Config holds the application configuration
type Config struct {
	// DataDir is the directory where vecgrep stores its data
	DataDir string `mapstructure:"data_dir" yaml:"data_dir,omitempty"`
	// DBPath is the path to the database file (veclite)
	DBPath string `mapstructure:"db_path" yaml:"db_path,omitempty"`

	// Embedding configuration
	Embedding EmbeddingConfig `mapstructure:"embedding" yaml:"embedding,omitempty"`

	// Indexing configuration
	Indexing IndexingConfig `mapstructure:"indexing" yaml:"indexing,omitempty"`

	// Search configuration
	Search SearchConfig `mapstructure:"search" yaml:"search,omitempty"`

	// Server configuration
	Server ServerConfig `mapstructure:"server" yaml:"server,omitempty"`

	// Vector configuration
	Vector VectorConfig `mapstructure:"vector" yaml:"vector,omitempty"`

	// Codemap integration configuration
	Codemap CodemapConfig `mapstructure:"codemap" yaml:"codemap,omitempty"`

	// Daemon configuration for the background indexing daemon
	Daemon DaemonConfig `mapstructure:"daemon" yaml:"daemon,omitempty"`

	// Cache configuration for the embedding disk cache and fcheap
	// snapshot/restore integration.
	Cache CacheConfig `mapstructure:"cache" yaml:"cache,omitempty"`

	present map[string]bool `mapstructure:"-" yaml:"-"`
}

// CacheConfig holds settings for the embedding disk cache and its fcheap
// snapshot/restore integration. When FcheapStash is enabled (default), the
// embedding cache is auto-stashed to fcheap after indexing and restored
// before indexing to avoid re-embedding unchanged chunks across runs.
//
// Path is the bbolt file path for the disk-persistent embedding cache.
// When empty, a default path under the project's base data directory is
// used (computed by the session layer).
type CacheConfig struct {
	// FcheapStash controls whether the embedding cache is auto-stashed
	// to fcheap after indexing. Defaults to true when nil. Set to false to
	// disable fcheap integration for the embedding cache.
	FcheapStash *bool `mapstructure:"fcheap_stash" yaml:"fcheap_stash,omitempty"`
	// FcheapTTL is a informational TTL tag applied to embedding cache
	// stashes (e.g. "30d"). Used by sweep commands to expire old stashes.
	FcheapTTL string `mapstructure:"fcheap_ttl" yaml:"fcheap_ttl,omitempty"`
	// Path is the bbolt file path for the disk-persistent embedding cache.
	// When empty, a default path is computed by the session layer.
	Path string `mapstructure:"path" yaml:"path,omitempty"`
}

// FcheapStashEnabled reports whether fcheap stashing of the embedding
// cache is enabled. Defaults to true when FcheapStash is nil.
func (c *CacheConfig) FcheapStashEnabled() bool {
	if c == nil || c.FcheapStash == nil {
		return true
	}
	return *c.FcheapStash
}

// SearchConfig holds search-related settings
type SearchConfig struct {
	// DefaultMode is the default search mode: "semantic", "keyword", or "hybrid"
	DefaultMode string `mapstructure:"default_mode" yaml:"default_mode,omitempty"`
	// VectorWeight is the weight for vector similarity in hybrid search (0-1)
	VectorWeight float32 `mapstructure:"vector_weight" yaml:"vector_weight,omitempty"`
	// TextWeight is the weight for text matching in hybrid search (0-1)
	TextWeight float32 `mapstructure:"text_weight" yaml:"text_weight,omitempty"`
}

// VectorConfig holds vector backend settings
type VectorConfig struct {
	// VecLite holds VecLite-specific configuration (HNSW parameters)
	VecLite VecLiteConfig `mapstructure:"veclite" yaml:"veclite,omitempty"`
}

// VecLiteConfig holds VecLite backend settings
type VecLiteConfig struct {
	// M is the HNSW max connections per node (default: DefaultVecLiteM = 16)
	M int `mapstructure:"m" yaml:"m,omitempty"`
	// EfConstruction is the HNSW build quality parameter (default: DefaultVecLiteEfConstruction = 200)
	EfConstruction int `mapstructure:"ef_construction" yaml:"ef_construction,omitempty"`
	// EfSearch is the HNSW search quality parameter (default: DefaultVecLiteEfSearch = 100)
	EfSearch int `mapstructure:"ef_search" yaml:"ef_search,omitempty"`
}

// Default HNSW parameters for VecLite. Exposed so callers (status views,
// diagnostics) can distinguish user-tuned values from defaults.
const (
	DefaultVecLiteM              = 16
	DefaultVecLiteEfConstruction = 200
	DefaultVecLiteEfSearch       = 100
)

// ThrottleConfig configures the ThrottledProvider that wraps the raw
// embedding provider. The same struct is reused for both the CLI path
// (Config.Embedding.Throttle) and the daemon path (Config.Daemon). When
// Enabled is true (or MaxInFlight > 0), NewProvider wraps the inner
// provider with embed.NewThrottledProvider, adding caching, dedup, and
// bounded concurrency. Set Enabled explicitly to false to disable the
// wrapper for the CLI path.
type ThrottleConfig struct {
	// Enabled controls whether the CLI path wraps the provider with a
	// ThrottledProvider. When false and no MaxInFlight/Workers are set,
	// the default is to wrap (see provider.go). Set this to false only to
	// opt out of throttling explicitly.
	Enabled *bool `mapstructure:"enabled" yaml:"enabled,omitempty"`
	// MaxInFlight is the maximum number of concurrent in-flight embedding
	// requests. Zero means use the default (8).
	MaxInFlight int `mapstructure:"max_in_flight" yaml:"max_in_flight,omitempty"`
	// RateLimit is the maximum embedding requests per second
	// (token-bucket). Zero means no rate limit.
	RateLimit float64 `mapstructure:"rate_limit" yaml:"rate_limit,omitempty"`
}

// EmbeddingConfig holds embedding provider settings
type EmbeddingConfig struct {
	// Provider is the embedding provider: "ollama", "openai", "cohere", or "voyage"
	Provider string `mapstructure:"provider" yaml:"provider,omitempty"`
	// Model is the embedding model name
	Model string `mapstructure:"model" yaml:"model,omitempty"`
	// OllamaURL is the Ollama API URL
	OllamaURL string `mapstructure:"ollama_url" yaml:"ollama_url,omitempty"`
	// Dimensions is the embedding vector dimensions
	Dimensions int `mapstructure:"dimensions" yaml:"dimensions,omitempty"`
	// OpenAIAPIKey is the API key for OpenAI (can also be set via OPENAI_API_KEY or VECGREP_OPENAI_API_KEY env)
	OpenAIAPIKey string `mapstructure:"openai_api_key" yaml:"openai_api_key,omitempty"`
	// OpenAIBaseURL is the base URL for OpenAI API (can also be set via OPENAI_BASE_URL or VECGREP_OPENAI_BASE_URL env)
	OpenAIBaseURL string `mapstructure:"openai_base_url" yaml:"openai_base_url,omitempty"`
	// CohereAPIKey is the API key for Cohere (can also be set via COHERE_API_KEY or VECGREP_COHERE_API_KEY env)
	CohereAPIKey string `mapstructure:"cohere_api_key" yaml:"cohere_api_key,omitempty"`
	// CohereBaseURL is the base URL for Cohere API (can also be set via COHERE_BASE_URL or VECGREP_COHERE_BASE_URL env)
	CohereBaseURL string `mapstructure:"cohere_base_url" yaml:"cohere_base_url,omitempty"`
	// VoyageAPIKey is the API key for Voyage AI (can also be set via VOYAGE_API_KEY or VECGREP_VOYAGE_API_KEY env)
	VoyageAPIKey string `mapstructure:"voyage_api_key" yaml:"voyage_api_key,omitempty"`
	// VoyageBaseURL is the base URL for Voyage AI API (can also be set via VOYAGE_BASE_URL or VECGREP_VOYAGE_BASE_URL env)
	VoyageBaseURL string `mapstructure:"voyage_base_url" yaml:"voyage_base_url,omitempty"`
	// MaxBatchSize is the maximum number of texts sent in a single embedding
	// request to the provider (Ollama /api/embed). Default 64. Only used by
	// providers that support native batch embedding.
	MaxBatchSize int `mapstructure:"max_batch_size" yaml:"max_batch_size,omitempty"`
	// KeepAlive controls how long the provider keeps the model loaded in
	// memory after a request. Only used by Ollama. If empty, sensible defaults
	// are applied: "5m" for single embeds, "30m" for batch indexing.
	KeepAlive string `mapstructure:"keep_alive" yaml:"keep_alive,omitempty"`
	// Throttle holds optional throttling/caching settings for the CLI
	// provider path. When left empty, NewProvider wraps the inner provider
	// with a default ThrottledProvider. Set Throttle.Enabled to false to
	// opt out of the wrapper.
	Throttle ThrottleConfig `mapstructure:"throttle" yaml:"throttle,omitempty"`
}

// IndexingConfig holds indexing settings
type IndexingConfig struct {
	// ChunkSize is the target chunk size in tokens. The chunker operates in
	// characters, so the value is converted using ~4 chars per token for
	// typical code (see internal/app/index.go indexerConfig).
	ChunkSize int `mapstructure:"chunk_size" yaml:"chunk_size,omitempty"`
	// ChunkOverlap is the overlap between chunks in tokens. The chunker
	// operates in characters, so the value is converted using ~4 chars per
	// token for typical code (see internal/app/index.go indexerConfig).
	ChunkOverlap int `mapstructure:"chunk_overlap" yaml:"chunk_overlap,omitempty"`
	// IgnorePatterns are glob patterns to ignore during indexing
	IgnorePatterns []string `mapstructure:"ignore_patterns" yaml:"ignore_patterns,omitempty"`
	// MaxFileSize is the maximum file size to index in bytes
	MaxFileSize int64 `mapstructure:"max_file_size" yaml:"max_file_size,omitempty"`
}

// ServerConfig holds MCP server settings.
type ServerConfig struct {
	// MCPEnabled enables the MCP server
	MCPEnabled bool `mapstructure:"mcp_enabled" yaml:"mcp_enabled,omitempty"`
	// MCPReloadInterval is the maximum age of a read-only snapshot before
	// it is reloaded from disk to pick up writes from other processes
	// (the daemon, CLI index, etc.). Zero means reload on every read
	// call. Default: 5s. Set via vecgrep.yaml:
	//   server:
	//     mcp_reload_interval: "10s"
	MCPReloadInterval string `mapstructure:"mcp_reload_interval" yaml:"mcp_reload_interval,omitempty"`
}

// CodemapConfig holds settings for the codemap graph integration. When
// enabled, vecgrep delegates related-files lookups and structural
// re-ranking to codemap's real call graph via its MCP tools. When
// codemap is unavailable or not indexed, vecgrep falls back to its
// built-in text-based heuristics.
type CodemapConfig struct {
	// Enabled controls whether vecgrep attempts to use codemap.
	Enabled bool `mapstructure:"enabled" yaml:"enabled,omitempty"`
	// Bin is the path to the codemap CLI binary (resolved via $PATH if empty).
	Bin string `mapstructure:"bin" yaml:"bin,omitempty"`
	// MCPEndpoint is the codemap MCP server endpoint (stdio or HTTP). If
	// empty, vecgrep shells out to the codemap binary for each query.
	MCPEndpoint string `mapstructure:"mcp_endpoint" yaml:"mcp_endpoint,omitempty"`
	// StructuralWeight is the weight for codemap's structural importance
	// score when re-ranking hybrid search results (0-1). 0 disables
	// re-ranking. Default 0.15.
	StructuralWeight float32 `mapstructure:"structural_weight" yaml:"structural_weight,omitempty"`
	// ImpactDepth is the transitive traversal depth for codemap impact
	// (blast-radius) queries. 0 means use codemap's default (typically 3).
	// Higher values find more affected files at the cost of a larger scope.
	ImpactDepth int `mapstructure:"impact_depth" yaml:"impact_depth,omitempty"`
}

// DaemonConfig holds settings for the background indexing daemon. The
// daemon watches files, throttles Ollama embedding requests, and serves
// MCP queries over a unix socket so that the CLI and MCP server never
// contend for the exclusive write lock.
type DaemonConfig struct {
	// Autostart starts the daemon automatically on the first search or
	// index operation if it is not already running.
	Autostart bool `mapstructure:"autostart" yaml:"autostart,omitempty"`
	// IdleTimeout is how long the daemon stays alive without activity
	// before shutting down. Zero means no idle shutdown.
	IdleTimeout int `mapstructure:"idle_timeout" yaml:"idle_timeout,omitempty"`
	// EmbedWorkers is the number of concurrent embedding workers for
	// background indexing (default 2).
	EmbedWorkers int `mapstructure:"embed_workers" yaml:"embed_workers,omitempty"`
	// EmbedRPS is the maximum embedding requests per second to Ollama
	// (token-bucket rate limit). Zero means no limit.
	EmbedRPS float64 `mapstructure:"embed_rps" yaml:"embed_rps,omitempty"`
	// EmbedMaxInFlight is the maximum number of concurrent embedding
	// requests in flight to Ollama (default 4).
	EmbedMaxInFlight int `mapstructure:"embed_max_in_flight" yaml:"embed_max_in_flight,omitempty"`
	// Debounce is the watcher debounce duration in milliseconds (default 500).
	Debounce int `mapstructure:"debounce" yaml:"debounce,omitempty"`
	// SweepInterval is the interval between automatic fcheap vault
	// cleanup sweeps. When non-zero, the daemon starts a ticker that
	// runs fcheap vacuum every SweepInterval to remove orphaned stash
	// entries. Use a string duration (e.g. "24h", "6h"). Zero or empty
	// disables periodic sweeps (default: 24h).
	SweepInterval string `mapstructure:"sweep_interval" yaml:"sweep_interval,omitempty"`
	// LogOffload enables rotating the daemon's own log to a managed file and
	// periodically stashing rotated segments into the fcheap vault. Off by
	// default. When enabled the daemon also keeps logging to stderr.
	LogOffload bool `mapstructure:"log_offload" yaml:"log_offload,omitempty"`
	// LogOffloadInterval is how often the managed log is rotated and offloaded
	// (string duration, e.g. "1h"). Empty/invalid disables offload (default 1h).
	LogOffloadInterval string `mapstructure:"log_offload_interval" yaml:"log_offload_interval,omitempty"`
	// LogOffloadTTL is the fcheap TTL applied to stashed log segments so they
	// auto-expire (e.g. "30d"). Empty means the stash never expires.
	LogOffloadTTL string `mapstructure:"log_offload_ttl" yaml:"log_offload_ttl,omitempty"`
}

// Default daemon constants.
const (
	DefaultDaemonIdleTimeout      = 30 // minutes
	DefaultDaemonEmbedWorkers     = 4
	DefaultDaemonEmbedMaxInFlight = 8
	DefaultDaemonDebounceMs       = 500
	DefaultDaemonSweepInterval    = "24h"
	DefaultDaemonLogOffloadInt    = "1h"
	DefaultDaemonLogOffloadTTL    = "30d"
)

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	return &Config{
		DataDir: DefaultDataDir,
		DBPath:  filepath.Join(DefaultDataDir, DefaultDBFile),
		Embedding: EmbeddingConfig{
			Provider:   "ollama",
			Model:      "nomic-embed-text",
			OllamaURL:  "http://localhost:11434",
			Dimensions: 768,
		},
		Indexing: IndexingConfig{
			ChunkSize:    512,
			ChunkOverlap: 64,
			IgnorePatterns: []string{
				".git/**",
				"node_modules/**",
				"vendor/**",
				"*.min.js",
				"*.min.css",
				"*.lock",
				"go.sum",
				"package-lock.json",
				"yarn.lock",
			},
			MaxFileSize: 1024 * 1024, // 1MB
		},
		Search: SearchConfig{
			DefaultMode:  "hybrid", // Default to hybrid search
			VectorWeight: 0.7,      // 70% vector similarity
			TextWeight:   0.3,      // 30% text matching
		},
		Server: ServerConfig{
			MCPEnabled:        true,
			MCPReloadInterval: "5s",
		},
		Vector: VectorConfig{
			VecLite: VecLiteConfig{
				M:              DefaultVecLiteM,
				EfConstruction: DefaultVecLiteEfConstruction,
				EfSearch:       DefaultVecLiteEfSearch,
			},
		},
		Daemon: DaemonConfig{
			IdleTimeout:      DefaultDaemonIdleTimeout,
			EmbedWorkers:     DefaultDaemonEmbedWorkers,
			EmbedMaxInFlight: DefaultDaemonEmbedMaxInFlight,
			Debounce:         DefaultDaemonDebounceMs,
			SweepInterval:    DefaultDaemonSweepInterval,
			// LogOffload defaults to off; interval/TTL apply only once enabled.
			LogOffloadInterval: DefaultDaemonLogOffloadInt,
			LogOffloadTTL:      DefaultDaemonLogOffloadTTL,
		},
		Cache: CacheConfig{
			FcheapStash: boolPtr(true),
			FcheapTTL:   "30d",
		},
	}
}

func (c *Config) has(path string) bool {
	if c == nil || c.present == nil {
		return false
	}
	return c.present[path]
}

func (c *Config) markPresent(paths ...string) {
	if c.present == nil {
		c.present = make(map[string]bool, len(paths))
	}
	for _, path := range paths {
		c.present[path] = true
	}
}

// Load loads configuration from file, environment, and flags using the
// new hierarchical config resolution system.
func Load(projectDir string) (*Config, error) {
	resolver := NewConfigResolution()
	resolved, err := resolver.Resolve(projectDir)
	if err != nil {
		return nil, err
	}
	return resolved.Config, nil
}

// LoadResolved loads configuration and returns full resolution information
func LoadResolved(projectDir string) (*ResolvedConfig, error) {
	resolver := NewConfigResolution()
	return resolver.Resolve(projectDir)
}

// LoadLegacy loads configuration using the legacy viper-based system.
// This is kept for backward compatibility.
func LoadLegacy(projectDir string) (*Config, error) {
	cfg := DefaultConfig()

	v := viper.New()

	// Set config name and paths
	v.SetConfigName("config")
	v.SetConfigType("yaml")

	// Look for config in project's .vecgrep directory
	configDir := filepath.Join(projectDir, DefaultDataDir)
	v.AddConfigPath(configDir)

	// Also check current directory
	v.AddConfigPath(".")

	// Environment variables
	v.SetEnvPrefix("VECGREP")
	v.AutomaticEnv()

	// Bind environment variables
	_ = v.BindEnv("embedding.provider", "VECGREP_EMBEDDING_PROVIDER")
	_ = v.BindEnv("embedding.model", "VECGREP_EMBEDDING_MODEL")
	_ = v.BindEnv("embedding.ollama_url", "VECGREP_OLLAMA_URL")
	_ = v.BindEnv("embedding.openai_api_key", "VECGREP_OPENAI_API_KEY")
	_ = v.BindEnv("embedding.openai_base_url", "VECGREP_OPENAI_BASE_URL")
	_ = v.BindEnv("embedding.cohere_api_key", "VECGREP_COHERE_API_KEY")
	_ = v.BindEnv("embedding.cohere_base_url", "VECGREP_COHERE_BASE_URL")
	_ = v.BindEnv("embedding.voyage_api_key", "VECGREP_VOYAGE_API_KEY")
	_ = v.BindEnv("embedding.voyage_base_url", "VECGREP_VOYAGE_BASE_URL")

	// Read config file (ignore if not found)
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config: %w", err)
		}
	}

	// Unmarshal into struct
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("error parsing config: %w", err)
	}

	// Update paths relative to project directory
	if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir = filepath.Join(projectDir, cfg.DataDir)
	}
	if !filepath.IsAbs(cfg.DBPath) {
		cfg.DBPath = filepath.Join(projectDir, cfg.DBPath)
	}

	return cfg, nil
}

// EnsureDataDir creates the data directory if it doesn't exist
func (c *Config) EnsureDataDir() error {
	return os.MkdirAll(c.DataDir, 0755)
}

// WriteDefaultConfig writes the default config file to the data directory
func (c *Config) WriteDefaultConfig() error {
	configPath := filepath.Join(c.DataDir, DefaultConfigFile)

	// Don't overwrite existing config
	if _, err := os.Stat(configPath); err == nil {
		return nil
	}

	v := viper.New()
	v.Set("embedding.provider", c.Embedding.Provider)
	v.Set("embedding.model", c.Embedding.Model)
	v.Set("embedding.ollama_url", c.Embedding.OllamaURL)
	v.Set("embedding.dimensions", c.Embedding.Dimensions)
	v.Set("indexing.chunk_size", c.Indexing.ChunkSize)
	v.Set("indexing.chunk_overlap", c.Indexing.ChunkOverlap)
	v.Set("indexing.ignore_patterns", c.Indexing.IgnorePatterns)
	v.Set("indexing.max_file_size", c.Indexing.MaxFileSize)
	v.Set("server.mcp_enabled", c.Server.MCPEnabled)

	return v.WriteConfigAs(configPath)
}

// GetProjectRoot finds the project root by looking for vecgrep config files.
// Search order: vecgrep.yaml, vecgrep.yml, .config/vecgrep.yaml, .vecgrep/
// Also checks if the project is registered in the global config.
func GetProjectRoot() (string, error) {
	return FindProjectRoot()
}

// GetProjectRootLegacy finds the project root by looking for .vecgrep directory only.
// This is the original implementation kept for backward compatibility.
func GetProjectRootLegacy() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		vecgrepDir := filepath.Join(dir, DefaultDataDir)
		if info, err := os.Stat(vecgrepDir); err == nil && info.IsDir() {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			return "", fmt.Errorf("not in a vecgrep project (no %s directory found)", DefaultDataDir)
		}
		dir = parent
	}
}
