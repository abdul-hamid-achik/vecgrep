package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

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
	// DBPath is the path to the SQLite database file
	DBPath string `mapstructure:"db_path" yaml:"db_path,omitempty"`

	// Embedding configuration
	Embedding EmbeddingConfig `mapstructure:"embedding" yaml:"embedding,omitempty"`

	// Indexing configuration
	Indexing IndexingConfig `mapstructure:"indexing" yaml:"indexing,omitempty"`

	// Server configuration
	Server ServerConfig `mapstructure:"server" yaml:"server,omitempty"`
}

// EmbeddingConfig holds embedding provider settings
type EmbeddingConfig struct {
	// Provider is the embedding provider: "ollama", "openai", "local"
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
}

// IndexingConfig holds indexing settings
type IndexingConfig struct {
	// ChunkSize is the target chunk size in tokens
	ChunkSize int `mapstructure:"chunk_size" yaml:"chunk_size,omitempty"`
	// ChunkOverlap is the overlap between chunks in tokens
	ChunkOverlap int `mapstructure:"chunk_overlap" yaml:"chunk_overlap,omitempty"`
	// IgnorePatterns are glob patterns to ignore during indexing
	IgnorePatterns []string `mapstructure:"ignore_patterns" yaml:"ignore_patterns,omitempty"`
	// MaxFileSize is the maximum file size to index in bytes
	MaxFileSize int64 `mapstructure:"max_file_size" yaml:"max_file_size,omitempty"`
}

// ServerConfig holds server settings
type ServerConfig struct {
	// Host is the server bind address
	Host string `mapstructure:"host" yaml:"host,omitempty"`
	// Port is the server port
	Port int `mapstructure:"port" yaml:"port,omitempty"`
	// MCPEnabled enables the MCP server
	MCPEnabled bool `mapstructure:"mcp_enabled" yaml:"mcp_enabled,omitempty"`
}

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
		Server: ServerConfig{
			Host:       "localhost",
			Port:       8080,
			MCPEnabled: true,
		},
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
	_ = v.BindEnv("server.host", "VECGREP_HOST")
	_ = v.BindEnv("server.port", "VECGREP_PORT")

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
	v.Set("server.host", c.Server.Host)
	v.Set("server.port", c.Server.Port)
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
