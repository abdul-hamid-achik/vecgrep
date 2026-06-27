package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/git"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// codemapDetect reports whether the codemap CLI is installed (on PATH). It is a
// package var so tests can stub it for deterministic results regardless of the
// host. Used to default codemap.enabled to on when codemap is available.
var codemapDetect = func() bool {
	_, err := exec.LookPath("codemap")
	return err == nil
}

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

	// Branch is the detected git branch name (empty for non-git repos or
	// detached HEAD). When non-empty, DataDir is redirected to a
	// branch-specific subdirectory so each branch has its own index.
	Branch    string
	GitHead   string // short HEAD SHA
	Detached  bool   // true when HEAD is detached
	IsGitRepo bool   // true when the project root is inside a git repository
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

	// Auto-enable the codemap integration when the codemap CLI is installed, so
	// it works out of the box. This is only the resolution-base default: an
	// explicit `codemap.enabled` in any config file or VECGREP_CODEMAP_ENABLED
	// is merged on top below and still wins (including an explicit `false`).
	result.Config.Codemap.Enabled = codemapDetect()

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

	// Step 5b: Detect git branch and redirect DataDir to a branch-specific
	// subdirectory when in global mode. This ensures each branch has its
	// own index, so switching branches doesn't produce stale results.
	// Non-git repos and detached HEAD fall back to the legacy flat layout.
	if projectDir != "" && result.IsGlobalMode && result.ProjectName != "" {
		branchInfo, err := git.Detect(r.resolveCtx(), projectDir)
		if err == nil && branchInfo != nil && !branchInfo.Detached && branchInfo.Branch != "" {
			result.Branch = branchInfo.Branch
			result.GitHead = branchInfo.Head
			result.IsGitRepo = true

			// Redirect DataDir to the branch-specific subdirectory
			sanitized := git.SanitizeBranch(branchInfo.Branch)
			branchDir, dirErr := GetProjectBranchDataDir(result.ProjectName, sanitized)
			if dirErr == nil {
				result.Config.DataDir = branchDir
				result.Config.DBPath = filepath.Join(branchDir, DefaultDBFile)
			}
		} else if err == nil && branchInfo != nil {
			// Git repo but detached HEAD — use legacy flat dir
			result.GitHead = branchInfo.Head
			result.IsGitRepo = true
			result.Detached = branchInfo.Detached
		}
	}

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

// resolveCtx returns a context for git operations during config resolution.
// Uses a short timeout so git detection never blocks the caller for long.
func (r *ConfigResolution) resolveCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = cancel // The caller doesn't need to cancel; timeout handles it.
	return ctx
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
	cfg.present = collectConfigPresence(data, "")

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
	mergeSearchConfig(dst, src)
	mergeServerConfigWithPresence(dst, src)
	mergeVectorConfig(dst, src)
	mergeCodemapConfig(dst, src)
	mergeDaemonConfig(dst, src)
	mergeCacheConfig(dst, src)
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
	if src.CohereAPIKey != "" {
		dst.CohereAPIKey = src.CohereAPIKey
	}
	if src.CohereBaseURL != "" {
		dst.CohereBaseURL = src.CohereBaseURL
	}
	if src.VoyageAPIKey != "" {
		dst.VoyageAPIKey = src.VoyageAPIKey
	}
	if src.VoyageBaseURL != "" {
		dst.VoyageBaseURL = src.VoyageBaseURL
	}
	mergeThrottleConfig(&dst.Throttle, &src.Throttle)

	if src.MaxBatchSize != 0 {
		dst.MaxBatchSize = src.MaxBatchSize
	}
	if src.KeepAlive != "" {
		dst.KeepAlive = src.KeepAlive
	}
}

// mergeThrottleConfig merges non-zero throttle settings from src into dst.
// The Enabled pointer is copied when non-nil so callers can explicitly opt
// in or out of the throttled provider wrapper.
func mergeThrottleConfig(dst, src *ThrottleConfig) {
	if src.Enabled != nil {
		dst.Enabled = src.Enabled
	}
	if src.MaxInFlight != 0 {
		dst.MaxInFlight = src.MaxInFlight
	}
	if src.RateLimit > 0 {
		dst.RateLimit = src.RateLimit
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
	if src.MCPEnabled {
		dst.MCPEnabled = true
	}
}

func mergeServerConfigWithPresence(dst, src *Config) {
	if src.Server.MCPEnabled || src.has("server.mcp_enabled") {
		dst.Server.MCPEnabled = src.Server.MCPEnabled
	}
}

func mergeSearchConfig(dst, src *Config) {
	if src.Search.DefaultMode != "" || src.has("search.default_mode") {
		dst.Search.DefaultMode = src.Search.DefaultMode
	}
	if src.Search.VectorWeight != 0 || src.has("search.vector_weight") {
		dst.Search.VectorWeight = src.Search.VectorWeight
	}
	if src.Search.TextWeight != 0 || src.has("search.text_weight") {
		dst.Search.TextWeight = src.Search.TextWeight
	}
}

func mergeVectorConfig(dst, src *Config) {
	if src.Vector.VecLite.M != 0 || src.has("vector.veclite.m") {
		dst.Vector.VecLite.M = src.Vector.VecLite.M
	}
	if src.Vector.VecLite.EfConstruction != 0 || src.has("vector.veclite.ef_construction") {
		dst.Vector.VecLite.EfConstruction = src.Vector.VecLite.EfConstruction
	}
	if src.Vector.VecLite.EfSearch != 0 || src.has("vector.veclite.ef_search") {
		dst.Vector.VecLite.EfSearch = src.Vector.VecLite.EfSearch
	}
}

func mergeCodemapConfig(dst, src *Config) {
	if src.Codemap.Enabled || src.has("codemap.enabled") {
		dst.Codemap.Enabled = src.Codemap.Enabled
	}
	if src.Codemap.Bin != "" {
		dst.Codemap.Bin = src.Codemap.Bin
	}
	if src.Codemap.MCPEndpoint != "" {
		dst.Codemap.MCPEndpoint = src.Codemap.MCPEndpoint
	}
	if src.Codemap.StructuralWeight > 0 {
		dst.Codemap.StructuralWeight = src.Codemap.StructuralWeight
	}
}

// mergeCacheConfig merges non-zero cache settings from src into dst.
// The FcheapStash pointer is copied when non-nil so callers can explicitly
// enable or disable fcheap embedding-cache stashing.
func mergeCacheConfig(dst, src *Config) {
	if src.Cache.FcheapStash != nil || src.has("cache.fcheap_stash") {
		dst.Cache.FcheapStash = src.Cache.FcheapStash
	}
	if src.Cache.FcheapTTL != "" || src.has("cache.fcheap_ttl") {
		dst.Cache.FcheapTTL = src.Cache.FcheapTTL
	}
	if src.Cache.Path != "" || src.has("cache.path") {
		dst.Cache.Path = src.Cache.Path
	}
}

func mergeDaemonConfig(dst, src *Config) {
	if src.Daemon.Autostart || src.has("daemon.autostart") {
		dst.Daemon.Autostart = src.Daemon.Autostart
	}
	if src.Daemon.IdleTimeout != 0 || src.has("daemon.idle_timeout") {
		dst.Daemon.IdleTimeout = src.Daemon.IdleTimeout
	}
	if src.Daemon.EmbedWorkers != 0 || src.has("daemon.embed_workers") {
		dst.Daemon.EmbedWorkers = src.Daemon.EmbedWorkers
	}
	if src.Daemon.EmbedRPS > 0 || src.has("daemon.embed_rps") {
		dst.Daemon.EmbedRPS = src.Daemon.EmbedRPS
	}
	if src.Daemon.EmbedMaxInFlight != 0 || src.has("daemon.embed_max_in_flight") {
		dst.Daemon.EmbedMaxInFlight = src.Daemon.EmbedMaxInFlight
	}
	if src.Daemon.Debounce != 0 || src.has("daemon.debounce") {
		dst.Daemon.Debounce = src.Daemon.Debounce
	}
	if src.Daemon.SweepInterval != "" || src.has("daemon.sweep_interval") {
		dst.Daemon.SweepInterval = src.Daemon.SweepInterval
	}
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

	// Cohere settings - check both VECGREP_ and standard COHERE_ prefixes
	if val := os.Getenv("VECGREP_COHERE_API_KEY"); val != "" {
		cfg.Embedding.CohereAPIKey = val
	} else if val := os.Getenv("COHERE_API_KEY"); val != "" {
		cfg.Embedding.CohereAPIKey = val
	}
	if val := os.Getenv("VECGREP_COHERE_BASE_URL"); val != "" {
		cfg.Embedding.CohereBaseURL = val
	} else if val := os.Getenv("COHERE_BASE_URL"); val != "" {
		cfg.Embedding.CohereBaseURL = val
	}

	// Voyage settings - check both VECGREP_ and standard VOYAGE_ prefixes
	if val := os.Getenv("VECGREP_VOYAGE_API_KEY"); val != "" {
		cfg.Embedding.VoyageAPIKey = val
	} else if val := os.Getenv("VOYAGE_API_KEY"); val != "" {
		cfg.Embedding.VoyageAPIKey = val
	}
	if val := os.Getenv("VECGREP_VOYAGE_BASE_URL"); val != "" {
		cfg.Embedding.VoyageBaseURL = val
	} else if val := os.Getenv("VOYAGE_BASE_URL"); val != "" {
		cfg.Embedding.VoyageBaseURL = val
	}

	// Embedding throttle settings
	if val := os.Getenv("VECGREP_EMBEDDING_THROTTLE_ENABLED"); val != "" {
		if enabled, err := strconv.ParseBool(val); err == nil {
			cfg.Embedding.Throttle.Enabled = &enabled
		}
	}
	if val := os.Getenv("VECGREP_EMBEDDING_THROTTLE_MAX_IN_FLIGHT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			cfg.Embedding.Throttle.MaxInFlight = n
		}
	}
	if val := os.Getenv("VECGREP_EMBEDDING_THROTTLE_RATE_LIMIT"); val != "" {
		if r, err := strconv.ParseFloat(val, 64); err == nil && r >= 0 {
			cfg.Embedding.Throttle.RateLimit = r
		}
	}

	// Embedding extras
	if val := os.Getenv("VECGREP_EMBEDDING_MAX_BATCH_SIZE"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n >= 0 {
			cfg.Embedding.MaxBatchSize = n
		}
	}
	if val := os.Getenv("VECGREP_EMBEDDING_KEEP_ALIVE"); val != "" {
		cfg.Embedding.KeepAlive = val
	}

	// Data directory
	if val := os.Getenv("VECGREP_DATA_DIR"); val != "" {
		cfg.DataDir = val
		cfg.DBPath = filepath.Join(val, DefaultDBFile)
	}

	// VecLite HNSW tuning. These were previously collected by viper's
	// AutomaticEnv but never read into the Config struct, so env overrides
	// for veclite.m / ef_construction / ef_search were silently ignored.
	// Resolve them explicitly so the backend actually receives them.
	if val := os.Getenv("VECGREP_VECTOR_VECLITE_M"); val != "" {
		if m, err := strconv.Atoi(val); err == nil && m > 0 {
			cfg.Vector.VecLite.M = m
		}
	}
	if val := os.Getenv("VECGREP_VECTOR_VECLITE_EF_CONSTRUCTION"); val != "" {
		if ef, err := strconv.Atoi(val); err == nil && ef > 0 {
			cfg.Vector.VecLite.EfConstruction = ef
		}
	}
	if val := os.Getenv("VECGREP_VECTOR_VECLITE_EF_SEARCH"); val != "" {
		if ef, err := strconv.Atoi(val); err == nil && ef > 0 {
			cfg.Vector.VecLite.EfSearch = ef
		}
	}

	// Codemap integration settings
	if val := os.Getenv("VECGREP_CODEMAP_ENABLED"); val != "" {
		if enabled, err := strconv.ParseBool(val); err == nil {
			cfg.Codemap.Enabled = enabled
		}
	}
	if val := os.Getenv("VECGREP_CODEMAP_BIN"); val != "" {
		cfg.Codemap.Bin = val
	}
	if val := os.Getenv("VECGREP_CODEMAP_MCP_ENDPOINT"); val != "" {
		cfg.Codemap.MCPEndpoint = val
	}
	if val := os.Getenv("VECGREP_CODEMAP_STRUCTURAL_WEIGHT"); val != "" {
		if w, err := strconv.ParseFloat(val, 32); err == nil {
			cfg.Codemap.StructuralWeight = float32(w)
		}
	}

	// Daemon settings
	if val := os.Getenv("VECGREP_DAEMON_AUTOSTART"); val != "" {
		if enabled, err := strconv.ParseBool(val); err == nil {
			cfg.Daemon.Autostart = enabled
		}
	}
	if val := os.Getenv("VECGREP_DAEMON_IDLE_TIMEOUT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.Daemon.IdleTimeout = n
		}
	}
	if val := os.Getenv("VECGREP_DAEMON_EMBED_WORKERS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			cfg.Daemon.EmbedWorkers = n
		}
	}
	if val := os.Getenv("VECGREP_DAEMON_EMBED_RPS"); val != "" {
		if r, err := strconv.ParseFloat(val, 64); err == nil {
			cfg.Daemon.EmbedRPS = r
		}
	}
	if val := os.Getenv("VECGREP_DAEMON_EMBED_MAX_IN_FLIGHT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			cfg.Daemon.EmbedMaxInFlight = n
		}
	}
	if val := os.Getenv("VECGREP_DAEMON_DEBOUNCE"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n >= 0 {
			cfg.Daemon.Debounce = n
		}
	}
	if val := os.Getenv("VECGREP_DAEMON_SWEEP_INTERVAL"); val != "" {
		cfg.Daemon.SweepInterval = val
	}

	// Cache settings
	if val := os.Getenv("VECGREP_CACHE_FCHEAP_STASH"); val != "" {
		if enabled, err := strconv.ParseBool(val); err == nil {
			cfg.Cache.FcheapStash = &enabled
		}
	}
	if val := os.Getenv("VECGREP_CACHE_FCHEAP_TTL"); val != "" {
		cfg.Cache.FcheapTTL = val
	}
	if val := os.Getenv("VECGREP_CACHE_PATH"); val != "" {
		cfg.Cache.Path = val
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
			return "", fmt.Errorf("not in a vecgrep project (no config file or %s directory found). Run 'vecgrep init' to initialize", DefaultDataDir)
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
	fmt.Fprintf(&sb, "  data_dir: %s\n", cfg.DataDir)
	fmt.Fprintf(&sb, "  db_path: %s\n", cfg.DBPath)

	// Embedding settings
	sb.WriteString("\nEmbedding:\n")
	fmt.Fprintf(&sb, "  provider: %s\n", cfg.Embedding.Provider)
	fmt.Fprintf(&sb, "  model: %s\n", cfg.Embedding.Model)
	fmt.Fprintf(&sb, "  dimensions: %d\n", cfg.Embedding.Dimensions)
	if cfg.Embedding.Provider == "ollama" {
		fmt.Fprintf(&sb, "  ollama_url: %s\n", cfg.Embedding.OllamaURL)
	}
	if cfg.Embedding.Provider == "openai" {
		if cfg.Embedding.OpenAIAPIKey != "" {
			sb.WriteString("  openai_api_key: [set]\n")
		} else {
			sb.WriteString("  openai_api_key: [not set]\n")
		}
		if cfg.Embedding.OpenAIBaseURL != "" {
			fmt.Fprintf(&sb, "  openai_base_url: %s\n", cfg.Embedding.OpenAIBaseURL)
		}
	}
	if cfg.Embedding.Provider == "cohere" {
		if cfg.Embedding.CohereAPIKey != "" {
			sb.WriteString("  cohere_api_key: [set]\n")
		} else {
			sb.WriteString("  cohere_api_key: [not set]\n")
		}
		if cfg.Embedding.CohereBaseURL != "" {
			fmt.Fprintf(&sb, "  cohere_base_url: %s\n", cfg.Embedding.CohereBaseURL)
		}
	}
	if cfg.Embedding.Provider == "voyage" {
		if cfg.Embedding.VoyageAPIKey != "" {
			sb.WriteString("  voyage_api_key: [set]\n")
		} else {
			sb.WriteString("  voyage_api_key: [not set]\n")
		}
		if cfg.Embedding.VoyageBaseURL != "" {
			fmt.Fprintf(&sb, "  voyage_base_url: %s\n", cfg.Embedding.VoyageBaseURL)
		}
	}

	// Embedding throttle settings
	sb.WriteString("\nEmbedding throttle:\n")
	if cfg.Embedding.Throttle.Enabled != nil {
		fmt.Fprintf(&sb, "  throttle.enabled: %t\n", *cfg.Embedding.Throttle.Enabled)
	} else {
		sb.WriteString("  throttle.enabled: [unset - default wrap]\n")
	}
	fmt.Fprintf(&sb, "  throttle.max_in_flight: %d\n", cfg.Embedding.Throttle.MaxInFlight)
	fmt.Fprintf(&sb, "  throttle.rate_limit: %.1f\n", cfg.Embedding.Throttle.RateLimit)

	// Embedding extras
	if cfg.Embedding.MaxBatchSize > 0 {
		fmt.Fprintf(&sb, "  max_batch_size: %d\n", cfg.Embedding.MaxBatchSize)
	}
	if cfg.Embedding.KeepAlive != "" {
		fmt.Fprintf(&sb, "  keep_alive: %s\n", cfg.Embedding.KeepAlive)
	}

	// Cache settings
	sb.WriteString("\nCache:\n")
	if cfg.Cache.FcheapStash != nil {
		fmt.Fprintf(&sb, "  fcheap_stash: %t\n", *cfg.Cache.FcheapStash)
	} else {
		sb.WriteString("  fcheap_stash: true (default)\n")
	}
	if cfg.Cache.FcheapTTL != "" {
		fmt.Fprintf(&sb, "  fcheap_ttl: %s\n", cfg.Cache.FcheapTTL)
	}
	if cfg.Cache.Path != "" {
		fmt.Fprintf(&sb, "  path: %s\n", cfg.Cache.Path)
	}

	// Indexing settings
	sb.WriteString("\nIndexing:\n")
	fmt.Fprintf(&sb, "  chunk_size: %d\n", cfg.Indexing.ChunkSize)
	fmt.Fprintf(&sb, "  chunk_overlap: %d\n", cfg.Indexing.ChunkOverlap)
	fmt.Fprintf(&sb, "  max_file_size: %d\n", cfg.Indexing.MaxFileSize)
	fmt.Fprintf(&sb, "  ignore_patterns: %v\n", cfg.Indexing.IgnorePatterns)

	// Search settings
	sb.WriteString("\nSearch:\n")
	fmt.Fprintf(&sb, "  default_mode: %s\n", cfg.Search.DefaultMode)
	fmt.Fprintf(&sb, "  vector_weight: %.2f\n", cfg.Search.VectorWeight)
	fmt.Fprintf(&sb, "  text_weight: %.2f\n", cfg.Search.TextWeight)

	// Server settings
	sb.WriteString("\nServer:\n")
	fmt.Fprintf(&sb, "  mcp_enabled: %t\n", cfg.Server.MCPEnabled)

	// Vector settings
	sb.WriteString("\nVector:\n")
	fmt.Fprintf(&sb, "  veclite.m: %d\n", cfg.Vector.VecLite.M)
	fmt.Fprintf(&sb, "  veclite.ef_construction: %d\n", cfg.Vector.VecLite.EfConstruction)
	fmt.Fprintf(&sb, "  veclite.ef_search: %d\n", cfg.Vector.VecLite.EfSearch)

	// Codemap settings
	sb.WriteString("\nCodemap:\n")
	fmt.Fprintf(&sb, "  codemap.enabled: %t\n", cfg.Codemap.Enabled)
	if cfg.Codemap.Bin != "" {
		fmt.Fprintf(&sb, "  codemap.bin: %s\n", cfg.Codemap.Bin)
	}
	if cfg.Codemap.MCPEndpoint != "" {
		fmt.Fprintf(&sb, "  codemap.mcp_endpoint: %s\n", cfg.Codemap.MCPEndpoint)
	}
	fmt.Fprintf(&sb, "  codemap.structural_weight: %.2f\n", cfg.Codemap.StructuralWeight)

	// Daemon settings
	sb.WriteString("\nDaemon:\n")
	fmt.Fprintf(&sb, "  daemon.autostart: %t\n", cfg.Daemon.Autostart)
	fmt.Fprintf(&sb, "  daemon.idle_timeout: %d\n", cfg.Daemon.IdleTimeout)
	fmt.Fprintf(&sb, "  daemon.embed_workers: %d\n", cfg.Daemon.EmbedWorkers)
	fmt.Fprintf(&sb, "  daemon.embed_rps: %.1f\n", cfg.Daemon.EmbedRPS)
	fmt.Fprintf(&sb, "  daemon.embed_max_in_flight: %d\n", cfg.Daemon.EmbedMaxInFlight)
	fmt.Fprintf(&sb, "  daemon.debounce: %d\n", cfg.Daemon.Debounce)
	if cfg.Daemon.SweepInterval != "" {
		fmt.Fprintf(&sb, "  daemon.sweep_interval: %s\n", cfg.Daemon.SweepInterval)
	}

	// Sources
	if len(sources) > 0 {
		sb.WriteString("\nConfig sources (in order of loading):\n")
		for _, s := range sources {
			fmt.Fprintf(&sb, "  - %s\n", s)
		}
	}

	return sb.String()
}
