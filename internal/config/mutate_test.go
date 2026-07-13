package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSetConfigValueInFilePreservesExistingYAML(t *testing.T) {
	isolateConfigTestEnv(t)

	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, "vecgrep.yaml")
	initial := []byte(`embedding:
  model: old-model
indexing:
  ignore_patterns:
    - old/**
custom:
  keep: true
`)
	if err := os.WriteFile(configPath, initial, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if err := SetConfigValueInFile(configPath, "embedding.provider", "openai"); err != nil {
		t.Fatalf("set config value: %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse config: %v", err)
	}

	embedding := raw["embedding"].(map[string]any)
	if embedding["model"] != "old-model" {
		t.Fatalf("embedding.model = %v, want old-model", embedding["model"])
	}
	if embedding["provider"] != "openai" {
		t.Fatalf("embedding.provider = %v, want openai", embedding["provider"])
	}

	custom := raw["custom"].(map[string]any)
	if custom["keep"] != true {
		t.Fatalf("custom.keep = %v, want true", custom["keep"])
	}

	indexing := raw["indexing"].(map[string]any)
	patterns := indexing["ignore_patterns"].([]any)
	if len(patterns) != 1 || patterns[0] != "old/**" {
		t.Fatalf("indexing.ignore_patterns = %v, want [old/**]", patterns)
	}
}

func TestSetConfigValueInFileTypedValuesResolve(t *testing.T) {
	isolateConfigTestEnv(t)

	projectRoot := t.TempDir()
	configPath := filepath.Join(projectRoot, "vecgrep.yaml")

	settings := map[string]string{
		"embedding.provider":             "voyage",
		"embedding.model":                "voyage-code-3",
		"embedding.dimensions":           "1024",
		"embedding.voyage_api_key":       "voyage-key",
		"embedding.voyage_base_url":      "https://example.test/voyage",
		"embedding.cohere_api_key":       "cohere-key",
		"embedding.cohere_base_url":      "https://example.test/cohere",
		"indexing.ignore_patterns":       ".git/**, dist/**",
		"indexing.max_file_size":         "2048",
		"search.default_mode":            "keyword",
		"search.vector_weight":           "0",
		"search.text_weight":             "1",
		"server.mcp_enabled":             "false",
		"vector.veclite.m":               "32",
		"vector.veclite.ef_construction": "320",
		"vector.veclite.ef_search":       "64",
	}

	for key, value := range settings {
		if err := SetConfigValueInFile(configPath, key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	cfg := resolved.Config

	if cfg.Embedding.Provider != "voyage" {
		t.Fatalf("embedding.provider = %q, want voyage", cfg.Embedding.Provider)
	}
	if cfg.Embedding.Model != "voyage-code-3" {
		t.Fatalf("embedding.model = %q, want voyage-code-3", cfg.Embedding.Model)
	}
	if cfg.Embedding.Dimensions != 1024 {
		t.Fatalf("embedding.dimensions = %d, want 1024", cfg.Embedding.Dimensions)
	}
	if cfg.Embedding.VoyageAPIKey != "voyage-key" {
		t.Fatalf("embedding.voyage_api_key = %q, want voyage-key", cfg.Embedding.VoyageAPIKey)
	}
	if cfg.Embedding.VoyageBaseURL != "https://example.test/voyage" {
		t.Fatalf("embedding.voyage_base_url = %q, want https://example.test/voyage", cfg.Embedding.VoyageBaseURL)
	}
	if cfg.Embedding.CohereAPIKey != "cohere-key" {
		t.Fatalf("embedding.cohere_api_key = %q, want cohere-key", cfg.Embedding.CohereAPIKey)
	}
	if cfg.Embedding.CohereBaseURL != "https://example.test/cohere" {
		t.Fatalf("embedding.cohere_base_url = %q, want https://example.test/cohere", cfg.Embedding.CohereBaseURL)
	}
	if got := cfg.Indexing.IgnorePatterns; len(got) != 2 || got[0] != ".git/**" || got[1] != "dist/**" {
		t.Fatalf("ignore_patterns = %v, want [.git/** dist/**]", got)
	}
	if cfg.Indexing.MaxFileSize != 2048 {
		t.Fatalf("max_file_size = %d, want 2048", cfg.Indexing.MaxFileSize)
	}
	if cfg.Search.DefaultMode != "keyword" {
		t.Fatalf("default_mode = %q, want keyword", cfg.Search.DefaultMode)
	}
	if cfg.Search.VectorWeight != 0 {
		t.Fatalf("vector_weight = %f, want 0", cfg.Search.VectorWeight)
	}
	if cfg.Search.TextWeight != 1 {
		t.Fatalf("text_weight = %f, want 1", cfg.Search.TextWeight)
	}
	if cfg.Server.MCPEnabled {
		t.Fatal("mcp_enabled = true, want false")
	}
	if cfg.Vector.VecLite.M != 32 {
		t.Fatalf("veclite.m = %d, want 32", cfg.Vector.VecLite.M)
	}
	if cfg.Vector.VecLite.EfConstruction != 320 {
		t.Fatalf("veclite.ef_construction = %d, want 320", cfg.Vector.VecLite.EfConstruction)
	}
	if cfg.Vector.VecLite.EfSearch != 64 {
		t.Fatalf("veclite.ef_search = %d, want 64", cfg.Vector.VecLite.EfSearch)
	}
}

func TestSetGlobalConfigValueInFilePreservesProjects(t *testing.T) {
	home := isolateConfigTestEnv(t)

	configPath := filepath.Join(home, ".vecgrep", "config.yaml")
	initial := []byte(`defaults:
  embedding:
    model: old-model
projects:
  vecgrep:
    path: /tmp/vecgrep
    data_dir: /tmp/data
`)
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir global config: %v", err)
	}
	if err := os.WriteFile(configPath, initial, 0644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	if err := SetConfigValueInFile(configPath, "defaults.server.mcp_enabled", "false"); err != nil {
		t.Fatalf("set global config value: %v", err)
	}

	global, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("load global config: %v", err)
	}
	if global.Defaults.Server.MCPEnabled {
		t.Fatal("global defaults server.mcp_enabled = true, want false")
	}
	if got := global.Projects["vecgrep"].DataDir; got != "/tmp/data" {
		t.Fatalf("project data_dir = %q, want /tmp/data", got)
	}
	if got := global.Defaults.Embedding.Model; got != "old-model" {
		t.Fatalf("default embedding.model = %q, want old-model", got)
	}
}

func TestLoadResolvedAppliesCloudProviderEnvironment(t *testing.T) {
	isolateConfigTestEnv(t)
	projectRoot := t.TempDir()

	t.Setenv("VECGREP_EMBEDDING_PROVIDER", "cohere")
	t.Setenv("VECGREP_EMBEDDING_MODEL", "embed-v4.0")
	t.Setenv("VECGREP_EMBEDDING_DIMENSIONS", "512")
	t.Setenv("COHERE_API_KEY", "standard-key")
	t.Setenv("VECGREP_COHERE_API_KEY", "vecgrep-key")
	t.Setenv("COHERE_BASE_URL", "https://standard.example.test/v2")
	t.Setenv("VECGREP_COHERE_BASE_URL", "https://vecgrep.example.test/v2")

	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("LoadResolved failed: %v", err)
	}

	cfg := resolved.Config
	if cfg.Embedding.Provider != "cohere" {
		t.Fatalf("provider = %q, want cohere", cfg.Embedding.Provider)
	}
	if cfg.Embedding.Model != "embed-v4.0" {
		t.Fatalf("model = %q, want embed-v4.0", cfg.Embedding.Model)
	}
	if cfg.Embedding.Dimensions != 512 {
		t.Fatalf("dimensions = %d, want 512", cfg.Embedding.Dimensions)
	}
	if cfg.Embedding.CohereAPIKey != "vecgrep-key" {
		t.Fatalf("cohere_api_key = %q, want vecgrep-key", cfg.Embedding.CohereAPIKey)
	}
	if cfg.Embedding.CohereBaseURL != "https://vecgrep.example.test/v2" {
		t.Fatalf("cohere_base_url = %q, want VECGREP override", cfg.Embedding.CohereBaseURL)
	}

	output := ShowResolvedConfig(cfg, nil)
	if !containsString(output, "cohere_api_key: [set]") {
		t.Fatalf("ShowResolvedConfig output missing cohere key status:\n%s", output)
	}
	if !containsString(output, "cohere_base_url: https://vecgrep.example.test/v2") {
		t.Fatalf("ShowResolvedConfig output missing cohere base URL:\n%s", output)
	}
}

// TestLoadResolvedAppliesHNSWEnv verifies that the VECGREP_VECTOR_VECLITE_*
// environment variables are resolved into the Config struct. Before the fix
// these were collected by viper's AutomaticEnv but never read into the struct,
// so HNSW tuning via env vars was silently ignored.
func TestLoadResolvedAppliesHNSWEnv(t *testing.T) {
	isolateConfigTestEnv(t)
	projectRoot := t.TempDir()

	t.Setenv("VECGREP_VECTOR_VECLITE_M", "8")
	t.Setenv("VECGREP_VECTOR_VECLITE_EF_CONSTRUCTION", "64")
	t.Setenv("VECGREP_VECTOR_VECLITE_EF_SEARCH", "32")

	resolved, err := LoadResolved(projectRoot)
	if err != nil {
		t.Fatalf("LoadResolved failed: %v", err)
	}

	cfg := resolved.Config
	if cfg.Vector.VecLite.M != 8 {
		t.Fatalf("veclite.m = %d, want 8", cfg.Vector.VecLite.M)
	}
	if cfg.Vector.VecLite.EfConstruction != 64 {
		t.Fatalf("veclite.ef_construction = %d, want 64", cfg.Vector.VecLite.EfConstruction)
	}
	if cfg.Vector.VecLite.EfSearch != 32 {
		t.Fatalf("veclite.ef_search = %d, want 32", cfg.Vector.VecLite.EfSearch)
	}
}

func TestAddProjectToGlobalReusesExistingPath(t *testing.T) {
	home := isolateConfigTestEnv(t)
	projectRoot := t.TempDir()

	if err := AddProjectToGlobal(projectRoot, ""); err != nil {
		t.Fatalf("first AddProjectToGlobal failed: %v", err)
	}
	if err := AddProjectToGlobal(projectRoot, ""); err != nil {
		t.Fatalf("second AddProjectToGlobal failed: %v", err)
	}

	global, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("LoadGlobalConfig failed: %v", err)
	}
	if len(global.Projects) != 1 {
		t.Fatalf("projects = %v, want one entry", global.Projects)
	}

	name, entry, err := FindProjectByPath(projectRoot)
	if err != nil {
		t.Fatalf("FindProjectByPath failed: %v", err)
	}
	if entry == nil {
		t.Fatal("project was not registered")
	}
	if got, want := entry.DataDir, filepath.Join(home, ".vecgrep", "projects", name); got != want {
		t.Fatalf("data_dir = %q, want %q", got, want)
	}
}

func TestParseConfigValueRejectsUnknownKeys(t *testing.T) {
	if _, err := ParseConfigValue("unknown.key", "value"); err == nil {
		t.Fatal("ParseConfigValue succeeded for an unknown key")
	}
}

func TestParseConfigValueRejectsUnknownProvider(t *testing.T) {
	if _, err := ParseConfigValue("embedding.provider", "not-a-provider"); err == nil {
		t.Fatal("ParseConfigValue succeeded for an unknown provider")
	}
}

func TestSetConfigValuesInFileAppliesBatchAndPreservesOtherValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vecgrep.yaml")
	initial := []byte("embedding:\n  model: old\n  ollama_options:\n    stale: true\ncustom:\n  keep: true\n")
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}

	err := SetConfigValuesInFile(path, map[string]any{
		"embedding.model":          "new",
		"embedding.dimensions":     1024,
		"embedding.ollama_options": map[string]any{},
	})
	if err != nil {
		t.Fatalf("SetConfigValuesInFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	embedding := raw["embedding"].(map[string]any)
	if embedding["model"] != "new" || embedding["dimensions"] != 1024 {
		t.Fatalf("embedding = %#v", embedding)
	}
	if options := embedding["ollama_options"].(map[string]any); len(options) != 0 {
		t.Fatalf("ollama_options = %#v, want empty", options)
	}
	if raw["custom"].(map[string]any)["keep"] != true {
		t.Fatalf("custom config changed: %#v", raw)
	}
}

func isolateConfigTestEnv(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VECGREP_EMBEDDING_PROVIDER", "")
	t.Setenv("VECGREP_EMBEDDING_MODEL", "")
	t.Setenv("VECGREP_OLLAMA_URL", "")
	t.Setenv("VECGREP_EMBEDDING_DIMENSIONS", "")
	t.Setenv("VECGREP_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VECGREP_OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("VECGREP_COHERE_API_KEY", "")
	t.Setenv("COHERE_API_KEY", "")
	t.Setenv("VECGREP_COHERE_BASE_URL", "")
	t.Setenv("COHERE_BASE_URL", "")
	t.Setenv("VECGREP_VOYAGE_API_KEY", "")
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("VECGREP_VOYAGE_BASE_URL", "")
	t.Setenv("VOYAGE_BASE_URL", "")
	t.Setenv("VECGREP_DATA_DIR", "")
	t.Setenv("VECGREP_VECTOR_VECLITE_M", "")
	t.Setenv("VECGREP_VECTOR_VECLITE_EF_CONSTRUCTION", "")
	t.Setenv("VECGREP_VECTOR_VECLITE_EF_SEARCH", "")
	t.Setenv("VECGREP_CODEMAP_ENABLED", "")
	t.Setenv("VECGREP_CODEMAP_BIN", "")
	t.Setenv("VECGREP_CODEMAP_MCP_ENDPOINT", "")
	t.Setenv("VECGREP_CODEMAP_STRUCTURAL_WEIGHT", "")
	t.Setenv("VECGREP_CODEMAP_STRUCTURAL_CHUNKS", "")
	t.Setenv("VECGREP_DAEMON_AUTOSTART", "")
	t.Setenv("VECGREP_DAEMON_IDLE_TIMEOUT", "")
	t.Setenv("VECGREP_DAEMON_EMBED_WORKERS", "")
	t.Setenv("VECGREP_DAEMON_EMBED_RPS", "")
	t.Setenv("VECGREP_DAEMON_EMBED_MAX_IN_FLIGHT", "")
	t.Setenv("VECGREP_DAEMON_DEBOUNCE", "")

	// Default codemap to "not installed" so config resolution is deterministic
	// regardless of whether the dev machine has codemap on PATH. Tests that
	// exercise the install-detected auto-enable stub it to true explicitly.
	prevCodemapDetect := codemapDetect
	codemapDetect = func() bool { return false }
	t.Cleanup(func() { codemapDetect = prevCodemapDetect })

	return home
}

func containsString(s, substr string) bool {
	return strings.Contains(s, substr)
}
