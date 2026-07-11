package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefaultIndexingFlowControl(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Indexing.SourceBufferBytes != 8*1024*1024 {
		t.Errorf("SourceBufferBytes = %d, want 8 MiB", cfg.Indexing.SourceBufferBytes)
	}
	if cfg.Indexing.SyncInterval != 50 {
		t.Errorf("SyncInterval = %d, want 50", cfg.Indexing.SyncInterval)
	}
	if cfg.Indexing.SyncIntervalDuration != 30*time.Second {
		t.Errorf("SyncIntervalDuration = %v, want 30s", cfg.Indexing.SyncIntervalDuration)
	}
}

func TestEmbeddingAndIndexingConfigYAML(t *testing.T) {
	var cfg Config
	err := yaml.Unmarshal([]byte(`embedding:
  model: qwen3-embedding:0.6b
  dimensions: 1024
  ollama_context: 4096
  ollama_options:
    num_batch: 128
  query_template: "query: {{text}}"
  document_template: "document: {{text}}"
indexing:
  source_buffer_bytes: 4194304
  sync_interval: 25
  sync_interval_duration: 15s
`), &cfg)
	if err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	if cfg.Embedding.Model != "qwen3-embedding:0.6b" || cfg.Embedding.Dimensions != 1024 {
		t.Fatalf("embedding profile = %q/%d", cfg.Embedding.Model, cfg.Embedding.Dimensions)
	}
	if cfg.Embedding.OllamaContext != 4096 || cfg.Embedding.OllamaOptions["num_batch"] != 128 {
		t.Fatalf("ollama config = context %d, options %#v", cfg.Embedding.OllamaContext, cfg.Embedding.OllamaOptions)
	}
	if cfg.Embedding.QueryTemplate == "" || cfg.Embedding.DocumentTemplate == "" {
		t.Fatal("semantic templates were not decoded")
	}
	if cfg.Indexing.SourceBufferBytes != 4194304 || cfg.Indexing.SyncInterval != 25 || cfg.Indexing.SyncIntervalDuration != 15*time.Second {
		t.Fatalf("indexing config = %+v", cfg.Indexing)
	}
}

func TestApplyOllamaAndIndexingConfigValues(t *testing.T) {
	cfg := DefaultConfig()
	settings := map[string]string{
		"embedding.ollama_context":        "8192",
		"embedding.ollama_options":        "{num_batch: 256}",
		"embedding.query_template":        "query: {{text}}",
		"embedding.document_template":     "document: {{text}}",
		"indexing.source_buffer_bytes":    "2097152",
		"indexing.sync_interval":          "10",
		"indexing.sync_interval_duration": "5s",
	}
	for key, value := range settings {
		if err := ApplyConfigValue(cfg, key, value); err != nil {
			t.Fatalf("ApplyConfigValue(%q) error = %v", key, err)
		}
	}
	if cfg.Embedding.OllamaContext != 8192 || cfg.Embedding.OllamaOptions["num_batch"] != 256 {
		t.Fatalf("ollama config = %+v", cfg.Embedding)
	}
	if cfg.Indexing.SourceBufferBytes != 2097152 || cfg.Indexing.SyncInterval != 10 || cfg.Indexing.SyncIntervalDuration != 5*time.Second {
		t.Fatalf("indexing config = %+v", cfg.Indexing)
	}
}

func TestEmbeddingAndIndexingEnvironmentOverrides(t *testing.T) {
	t.Setenv("VECGREP_OLLAMA_CONTEXT", "6144")
	t.Setenv("VECGREP_OLLAMA_OPTIONS", "{num_batch: 96}")
	t.Setenv("VECGREP_EMBEDDING_QUERY_TEMPLATE", "query: {{text}}")
	t.Setenv("VECGREP_EMBEDDING_DOCUMENT_TEMPLATE", "document: {{text}}")
	t.Setenv("VECGREP_INDEXING_SOURCE_BUFFER_BYTES", "3145728")
	t.Setenv("VECGREP_INDEXING_SYNC_INTERVAL", "12")
	t.Setenv("VECGREP_INDEXING_SYNC_INTERVAL_DURATION", "7s")

	cfg := DefaultConfig()
	(&ConfigResolution{}).applyEnvironment(cfg)
	if cfg.Embedding.OllamaContext != 6144 || cfg.Embedding.OllamaOptions["num_batch"] != 96 {
		t.Fatalf("ollama environment config = %+v", cfg.Embedding)
	}
	if cfg.Embedding.QueryTemplate == "" || cfg.Embedding.DocumentTemplate == "" {
		t.Fatal("semantic template environment overrides were not applied")
	}
	if cfg.Indexing.SourceBufferBytes != 3145728 || cfg.Indexing.SyncInterval != 12 || cfg.Indexing.SyncIntervalDuration != 7*time.Second {
		t.Fatalf("indexing environment config = %+v", cfg.Indexing)
	}
}
