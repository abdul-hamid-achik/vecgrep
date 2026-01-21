package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// ConfigSource represents where a config setting came from
type ConfigSource int

const (
	SourceDefault ConfigSource = iota
	SourceGlobalConfig
	SourceGlobalProject
	SourceLegacyProjectConfig
	SourceXDGProjectConfig
	SourceProjectRootConfig
	SourceEnvironment
)

func (s ConfigSource) String() string {
	switch s {
	case SourceDefault:
		return "default"
	case SourceGlobalConfig:
		return "~/.vecgrep/config.yaml"
	case SourceGlobalProject:
		return "~/.vecgrep/config.yaml (projects)"
	case SourceLegacyProjectConfig:
		return ".vecgrep/config.yaml"
	case SourceXDGProjectConfig:
		return ".config/vecgrep.yaml"
	case SourceProjectRootConfig:
		return "vecgrep.yaml"
	case SourceEnvironment:
		return "environment"
	default:
		return "unknown"
	}
}

// ResolvedConfig contains the final config along with source information
type ResolvedConfig struct {
	Config       *Config
	ProjectRoot  string
	ProjectName  string
	Sources      map[string]ConfigSource // which source each key came from
	IsGlobalMode bool                    // whether using global project management
}

// ConfigResolution handles the multi-level config resolution
type ConfigResolution struct {
	// foundConfigFiles tracks which config files were found
	foundConfigFiles []string
}

// NewConfigResolution creates a new config resolution instance
func NewConfigResolution() *ConfigResolution {
	return &ConfigResolution{
		foundConfigFiles: make([]string, 0),
	}
}

// Resolve performs the full config resolution chain
// Resolution order (highest to lowest priority):
// 1. Environment variables (VECGREP_*)
// 2. Project root vecgrep.yaml or vecgrep.yml
// 3. Project .config/vecgrep.yaml (XDG-style)
// 4. Project .vecgrep/config.yaml (legacy)
// 5. Global project entry in ~/.vecgrep/config.yaml
// 6. Global defaults in ~/.vecgrep/config.yaml
// 7. Built-in defaults
func (r *ConfigResolution) Resolve(projectDir string) (*ResolvedConfig, error) {
	result := &ResolvedConfig{
		Config:  DefaultConfig(),
		Sources: make(map[string]ConfigSource),
	}

	// Step 1: Start with built-in defaults
	// (already done above)

	// Step 2: Load and merge global defaults
	globalCfg, err := LoadGlobalConfig()
	if err == nil && globalCfg != nil {
		r.mergeConfig(result.Config, &globalCfg.Defaults)
	}

	// Step 3: Check if this project is in global projects list
	if projectDir != "" {
		absProjectDir, _ := filepath.Abs(projectDir)
		projectName, projectEntry, _ := FindProjectByPath(absProjectDir)
		if projectEntry != nil {
			result.ProjectName = projectName
			result.IsGlobalMode = true

			// Use data directory from global project
			if projectEntry.DataDir != "" {
				result.Config.DataDir = ExpandPath(projectEntry.DataDir)
				result.Config.DBPath = filepath.Join(result.Config.DataDir, DefaultDBFile)
			}

			// Merge project-specific overrides
			if projectEntry.Embedding != nil {
				mergeEmbeddingConfig(&result.Config.Embedding, projectEntry.Embedding)
			}
			if projectEntry.Indexing != nil {
				mergeIndexingConfig(&result.Config.Indexing, projectEntry.Indexing)
			}
			if projectEntry.Server != nil {
				mergeServerConfig(&result.Config.Server, projectEntry.Server)
			}
		}
	}

	// Step 4: Load project-level configs (in order of increasing priority)
	if projectDir != "" {
		// 4a: Legacy .vecgrep/config.yaml
		legacyPath := filepath.Join(projectDir, DefaultDataDir, DefaultConfigFile)
		if cfg, err := r.loadYAMLConfig(legacyPath); err == nil {
			r.mergeConfig(result.Config, cfg)
			r.foundConfigFiles = append(r.foundConfigFiles, legacyPath)
		}

		// 4b: XDG-style .config/vecgrep.yaml
		xdgPath := filepath.Join(projectDir, ".config", "vecgrep.yaml")
		if cfg, err := r.loadYAMLConfig(xdgPath); err == nil {
			r.mergeConfig(result.Config, cfg)
			r.foundConfigFiles = append(r.foundConfigFiles, xdgPath)
		}

		// 4c: Project root vecgrep.yaml or vecgrep.yml
		yamlPath := filepath.Join(projectDir, "vecgrep.yaml")
		ymlPath := filepath.Join(projectDir, "vecgrep.yml")

		yamlExists := fileExists(yamlPath)
		ymlExists := fileExists(ymlPath)

		if yamlExists && ymlExists {
			// Both exist - warn and use .yaml
			fmt.Fprintf(os.Stderr, "Warning: both vecgrep.yaml and vecgrep.yml exist; using vecgrep.yaml\n")
		}

		if yamlExists {
			if cfg, err := r.loadYAMLConfig(yamlPath); err == nil {
				r.mergeConfig(result.Config, cfg)
				r.foundConfigFiles = append(r.foundConfigFiles, yamlPath)
			}
		} else if ymlExists {
			if cfg, err := r.loadYAMLConfig(ymlPath); err == nil {
				r.mergeConfig(result.Config, cfg)
				r.foundConfigFiles = append(r.foundConfigFiles, ymlPath)
			}
		}
	}

	// Step 5: Apply environment variables (highest priority)
	r.applyEnvironment(result.Config)

	// Update paths relative to project directory if not absolute
	if projectDir != "" {
		result.ProjectRoot = projectDir
		if !filepath.IsAbs(result.Config.DataDir) {
			result.Config.DataDir = filepath.Join(projectDir, result.Config.DataDir)
		}
		if !filepath.IsAbs(result.Config.DBPath) {
			result.Config.DBPath = filepath.Join(projectDir, result.Config.DBPath)
		}
	}

	return result, nil
}

// loadYAMLConfig loads a config from a YAML file
func (r *ConfigResolution) loadYAMLConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", path, err)
	}

	return &cfg, nil
}

// mergeConfig merges src into dst (non-zero values in src override dst)
func (r *ConfigResolution) mergeConfig(dst, src *Config) {
	if src.DataDir != "" {
		dst.DataDir = src.DataDir
	}
	if src.DBPath != "" {
		dst.DBPath = src.DBPath
	}

	mergeEmbeddingConfig(&dst.Embedding, &src.Embedding)
	mergeIndexingConfig(&dst.Indexing, &src.Indexing)
	mergeServerConfig(&dst.Server, &src.Server)
}

func mergeEmbeddingConfig(dst, src *EmbeddingConfig) {
	if src.Provider != "" {
		dst.Provider = src.Provider
	}
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.OllamaURL != "" {
		dst.OllamaURL = src.OllamaURL
	}
	if src.Dimensions != 0 {
		dst.Dimensions = src.Dimensions
	}
	if src.OpenAIAPIKey != "" {
		dst.OpenAIAPIKey = src.OpenAIAPIKey
	}
	if src.OpenAIBaseURL != "" {
		dst.OpenAIBaseURL = src.OpenAIBaseURL
	}
}

func mergeIndexingConfig(dst, src *IndexingConfig) {
	if src.ChunkSize != 0 {
		dst.ChunkSize = src.ChunkSize
	}
	if src.ChunkOverlap != 0 {
		dst.ChunkOverlap = src.ChunkOverlap
	}
	if len(src.IgnorePatterns) > 0 {
		dst.IgnorePatterns = src.IgnorePatterns
	}
	if src.MaxFileSize != 0 {
		dst.MaxFileSize = src.MaxFileSize
	}
}

func mergeServerConfig(dst, src *ServerConfig) {
	if src.Host != "" {
		dst.Host = src.Host
	}
	if src.Port != 0 {
		dst.Port = src.Port
	}
	// MCPEnabled is a bool, only override if explicitly set
	// We can't distinguish false from unset, so we don't merge it
}

// applyEnvironment applies VECGREP_* environment variables
func (r *ConfigResolution) applyEnvironment(cfg *Config) {
	// Use viper for environment variable binding
	v := viper.New()
	v.SetEnvPrefix("VECGREP")
	v.AutomaticEnv()

	// Embedding settings
	if val := os.Getenv("VECGREP_EMBEDDING_PROVIDER"); val != "" {
		cfg.Embedding.Provider = val
	}
	if val := os.Getenv("VECGREP_EMBEDDING_MODEL"); val != "" {
		cfg.Embedding.Model = val
	}
	if val := os.Getenv("VECGREP_OLLAMA_URL"); val != "" {
		cfg.Embedding.OllamaURL = val
	}
	if val := os.Getenv("VECGREP_EMBEDDING_DIMENSIONS"); val != "" {
		if dim, err := strconv.Atoi(val); err == nil {
			cfg.Embedding.Dimensions = dim
		}
	}

	// OpenAI settings - check both VECGREP_ and standard OPENAI_ prefixes
	if val := os.Getenv("VECGREP_OPENAI_API_KEY"); val != "" {
		cfg.Embedding.OpenAIAPIKey = val
	} else if val := os.Getenv("OPENAI_API_KEY"); val != "" {
		cfg.Embedding.OpenAIAPIKey = val
	}
	if val := os.Getenv("VECGREP_OPENAI_BASE_URL"); val != "" {
		cfg.Embedding.OpenAIBaseURL = val
	} else if val := os.Getenv("OPENAI_BASE_URL"); val != "" {
		cfg.Embedding.OpenAIBaseURL = val
	}

	// Server settings
	if val := os.Getenv("VECGREP_HOST"); val != "" {
		cfg.Server.Host = val
	}
	if val := os.Getenv("VECGREP_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.Server.Port = port
		}
	}

	// Data directory
	if val := os.Getenv("VECGREP_DATA_DIR"); val != "" {
		cfg.DataDir = val
		cfg.DBPath = filepath.Join(val, DefaultDBFile)
	}
}

// FoundConfigFiles returns the list of config files that were found and loaded
func (r *ConfigResolution) FoundConfigFiles() []string {
	return r.foundConfigFiles
}

// FindProjectRoot searches for a project root by looking for config files.
// Search order: vecgrep.yaml, vecgrep.yml, .config/vecgrep.yaml, .vecgrep/
func FindProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	return FindProjectRootFrom(dir)
}

// FindProjectRootFrom searches for a project root starting from the given directory
func FindProjectRootFrom(startDir string) (string, error) {
	dir := startDir

	for {
		// Check for project config files in order of preference
		configFiles := []string{
			filepath.Join(dir, "vecgrep.yaml"),
			filepath.Join(dir, "vecgrep.yml"),
			filepath.Join(dir, ".config", "vecgrep.yaml"),
			filepath.Join(dir, DefaultDataDir),
		}

		for _, cf := range configFiles {
			if info, err := os.Stat(cf); err == nil {
				// For .vecgrep, check if it's a directory
				if strings.HasSuffix(cf, DefaultDataDir) {
					if info.IsDir() {
						return dir, nil
					}
				} else if info.Mode().IsRegular() {
					return dir, nil
				}
			}
		}

		// Check if this project is registered globally
		if name, entry, _ := FindProjectByPath(dir); entry != nil {
			// Project found in global config
			_ = name
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			return "", fmt.Errorf("not in a vecgrep project (no config file or %s directory found)", DefaultDataDir)
		}
		dir = parent
	}
}

// fileExists checks if a file exists and is a regular file
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// ShowResolvedConfig returns a formatted string showing the resolved config
func ShowResolvedConfig(cfg *Config, sources []string) string {
	var sb strings.Builder

	sb.WriteString("Resolved configuration:\n\n")

	// Data settings
	sb.WriteString("Data:\n")
	sb.WriteString(fmt.Sprintf("  data_dir: %s\n", cfg.DataDir))
	sb.WriteString(fmt.Sprintf("  db_path: %s\n", cfg.DBPath))

	// Embedding settings
	sb.WriteString("\nEmbedding:\n")
	sb.WriteString(fmt.Sprintf("  provider: %s\n", cfg.Embedding.Provider))
	sb.WriteString(fmt.Sprintf("  model: %s\n", cfg.Embedding.Model))
	sb.WriteString(fmt.Sprintf("  dimensions: %d\n", cfg.Embedding.Dimensions))
	if cfg.Embedding.Provider == "ollama" {
		sb.WriteString(fmt.Sprintf("  ollama_url: %s\n", cfg.Embedding.OllamaURL))
	}
	if cfg.Embedding.Provider == "openai" {
		if cfg.Embedding.OpenAIAPIKey != "" {
			sb.WriteString("  openai_api_key: [set]\n")
		} else {
			sb.WriteString("  openai_api_key: [not set]\n")
		}
		if cfg.Embedding.OpenAIBaseURL != "" {
			sb.WriteString(fmt.Sprintf("  openai_base_url: %s\n", cfg.Embedding.OpenAIBaseURL))
		}
	}

	// Indexing settings
	sb.WriteString("\nIndexing:\n")
	sb.WriteString(fmt.Sprintf("  chunk_size: %d\n", cfg.Indexing.ChunkSize))
	sb.WriteString(fmt.Sprintf("  chunk_overlap: %d\n", cfg.Indexing.ChunkOverlap))
	sb.WriteString(fmt.Sprintf("  max_file_size: %d\n", cfg.Indexing.MaxFileSize))
	sb.WriteString(fmt.Sprintf("  ignore_patterns: %v\n", cfg.Indexing.IgnorePatterns))

	// Server settings
	sb.WriteString("\nServer:\n")
	sb.WriteString(fmt.Sprintf("  host: %s\n", cfg.Server.Host))
	sb.WriteString(fmt.Sprintf("  port: %d\n", cfg.Server.Port))
	sb.WriteString(fmt.Sprintf("  mcp_enabled: %t\n", cfg.Server.MCPEnabled))

	// Sources
	if len(sources) > 0 {
		sb.WriteString("\nConfig sources (in order of loading):\n")
		for _, s := range sources {
			sb.WriteString(fmt.Sprintf("  - %s\n", s))
		}
	}

	return sb.String()
}
