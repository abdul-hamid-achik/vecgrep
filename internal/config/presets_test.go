package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestEmbeddingPresetsExactValuesAndStableListing(t *testing.T) {
	got := ListEmbeddingPresets()
	if len(got) != 2 {
		t.Fatalf("preset count = %d, want 2", len(got))
	}
	if names := []string{got[0].Name, got[1].Name}; !reflect.DeepEqual(names, []string{"fast-local", "quality-code"}) {
		t.Fatalf("preset names = %v", names)
	}

	fast := got[0]
	if fast.Description == "" {
		t.Fatal("fast-local description is empty")
	}
	if e := fast.Embedding; e.Provider != "ollama" || e.Model != "nomic-embed-text" || e.Dimensions != 768 || e.OllamaContext != 2048 || len(e.OllamaOptions) != 0 || e.QueryTemplate != "" || e.DocumentTemplate != "" {
		t.Fatalf("fast-local embedding = %+v", e)
	}

	quality := got[1]
	if quality.Description == "" {
		t.Fatal("quality-code description is empty")
	}
	if e := quality.Embedding; e.Provider != "ollama" || e.Model != "qwen3-embedding:0.6b" || e.Dimensions != 1024 || e.OllamaContext != 1024 || len(e.OllamaOptions) != 0 || e.QueryTemplate != qualityCodeQueryTemplate || e.DocumentTemplate != "" {
		t.Fatalf("quality-code embedding = %+v", e)
	}

	got[0].Name = "changed"
	got[0].Embedding.Model = "changed"
	got[0].Embedding.OllamaOptions["changed"] = true
	again := ListEmbeddingPresets()
	if again[0].Name != "fast-local" || again[0].Embedding.Model != "nomic-embed-text" || len(again[0].Embedding.OllamaOptions) != 0 {
		t.Fatalf("caller mutation changed registry: %+v", again[0])
	}
}

func TestLookupEmbeddingPreset(t *testing.T) {
	preset, ok := LookupEmbeddingPreset("quality-code")
	if !ok || preset.Name != "quality-code" || preset.Embedding.Model != "qwen3-embedding:0.6b" {
		t.Fatalf("lookup = (%+v, %v)", preset, ok)
	}
	preset.Embedding.OllamaOptions["num_batch"] = 1
	again, ok := LookupEmbeddingPreset("quality-code")
	if !ok || len(again.Embedding.OllamaOptions) != 0 {
		t.Fatalf("lookup did not return an independent copy: %+v", again)
	}
	if unknown, ok := LookupEmbeddingPreset("missing"); ok || !reflect.DeepEqual(unknown, EmbeddingPreset{}) {
		t.Fatalf("unknown lookup = (%+v, %v)", unknown, ok)
	}
}

func TestApplyEmbeddingPresetToProjectFilePreservesOperationalAndUnrelatedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vecgrep.yaml")
	initial := []byte(`embedding:
  provider: voyage
  model: old
  dimensions: 42
  ollama_url: http://custom-ollama.test
  ollama_context: 8192
  ollama_options:
    num_batch: 64
  query_template: old-query
  document_template: old-document
  openai_api_key: openai-secret
  openai_base_url: https://openai.test
  cohere_api_key: cohere-secret
  cohere_base_url: https://cohere.test
  voyage_api_key: voyage-secret
  voyage_base_url: https://voyage.test
  max_batch_size: 7
  keep_alive: 45m
  throttle:
    max_in_flight: 3
    rate_limit: 1.5
indexing:
  chunk_size: 99
search:
  default_mode: keyword
vector:
  veclite:
    m: 24
cache:
  path: /tmp/cache.db
custom:
  keep: yes
`)
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbeddingPresetToFile(path, "fast-local", false); err != nil {
		t.Fatalf("apply preset: %v", err)
	}

	raw := readPresetTestYAML(t, path)
	embedding := raw["embedding"].(map[string]any)
	assertPresetSemanticValues(t, embedding, "nomic-embed-text", 768, 2048, "")
	for key, want := range map[string]any{
		"ollama_url": "http://custom-ollama.test", "openai_api_key": "openai-secret", "openai_base_url": "https://openai.test",
		"cohere_api_key": "cohere-secret", "cohere_base_url": "https://cohere.test", "voyage_api_key": "voyage-secret", "voyage_base_url": "https://voyage.test",
		"max_batch_size": 7, "keep_alive": "45m",
	} {
		if embedding[key] != want {
			t.Errorf("embedding.%s = %v, want %v", key, embedding[key], want)
		}
	}
	if embedding["throttle"].(map[string]any)["max_in_flight"] != 3 || raw["indexing"].(map[string]any)["chunk_size"] != 99 || raw["search"].(map[string]any)["default_mode"] != "keyword" || raw["vector"].(map[string]any)["veclite"].(map[string]any)["m"] != 24 || raw["cache"].(map[string]any)["path"] != "/tmp/cache.db" || raw["custom"].(map[string]any)["keep"] != "yes" {
		t.Fatalf("unrelated configuration changed: %#v", raw)
	}
}

func TestApplyEmbeddingPresetToGlobalDefaultsPreservesProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte(`defaults:
  embedding:
    model: old
    ollama_options: {num_ctx: 4096}
    query_template: old-query
    document_template: old-document
    ollama_url: http://global-ollama.test
    throttle:
      max_in_flight: 9
projects:
  vecgrep:
    path: /work/vecgrep
    data_dir: /data/vecgrep
`)
	if err := os.WriteFile(path, initial, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbeddingPresetToFile(path, "quality-code", true); err != nil {
		t.Fatalf("apply global preset: %v", err)
	}
	raw := readPresetTestYAML(t, path)
	embedding := raw["defaults"].(map[string]any)["embedding"].(map[string]any)
	assertPresetSemanticValues(t, embedding, "qwen3-embedding:0.6b", 1024, 1024, qualityCodeQueryTemplate)
	if embedding["ollama_url"] != "http://global-ollama.test" || embedding["throttle"].(map[string]any)["max_in_flight"] != 9 {
		t.Fatalf("global operational settings changed: %#v", embedding)
	}
	project := raw["projects"].(map[string]any)["vecgrep"].(map[string]any)
	if project["path"] != "/work/vecgrep" || project["data_dir"] != "/data/vecgrep" {
		t.Fatalf("projects changed: %#v", project)
	}
}

func TestApplyEmbeddingPresetFailuresDoNotModifyFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial string
		preset  string
	}{
		{name: "unknown preset", initial: "embedding:\n  model: old\n", preset: "missing"},
		{name: "malformed YAML", initial: "embedding: [unterminated\n", preset: "fast-local"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "vecgrep.yaml")
			before := []byte(tc.initial)
			if err := os.WriteFile(path, before, 0644); err != nil {
				t.Fatal(err)
			}
			err := ApplyEmbeddingPresetToFile(path, tc.preset, false)
			if err == nil {
				t.Fatal("expected error")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("file changed on error:\n%s", after)
			}
			if tc.preset == "missing" && !strings.Contains(err.Error(), "unknown embedding preset") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestEmbeddingPresetsDoNotChangeDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Embedding.Provider != "ollama" || cfg.Embedding.Model != "nomic-embed-text" || cfg.Embedding.Dimensions != 768 || cfg.Embedding.OllamaContext != 0 || cfg.Embedding.QueryTemplate != "" || cfg.Embedding.DocumentTemplate != "" || cfg.Embedding.OllamaOptions != nil {
		t.Fatalf("default embedding changed: %+v", cfg.Embedding)
	}
}

func readPresetTestYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertPresetSemanticValues(t *testing.T, embedding map[string]any, model string, dimensions, context int, queryTemplate string) {
	t.Helper()
	if embedding["provider"] != "ollama" || embedding["model"] != model || embedding["dimensions"] != dimensions || embedding["ollama_context"] != context || embedding["query_template"] != queryTemplate || embedding["document_template"] != "" {
		t.Fatalf("semantic embedding fields = %#v", embedding)
	}
	options, ok := embedding["ollama_options"].(map[string]any)
	if !ok || len(options) != 0 {
		t.Fatalf("ollama_options = %#v, want empty mapping", embedding["ollama_options"])
	}
}
