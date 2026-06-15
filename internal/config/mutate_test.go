package config

import (
	"os"
	"path/filepath"
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

func TestParseConfigValueRejectsUnknownKeys(t *testing.T) {
	if _, err := ParseConfigValue("unknown.key", "value"); err == nil {
		t.Fatal("ParseConfigValue succeeded for an unknown key")
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
	t.Setenv("VECGREP_DATA_DIR", "")
	return home
}
