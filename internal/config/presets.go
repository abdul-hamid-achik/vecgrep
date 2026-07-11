package config

import "fmt"

const qualityCodeQueryTemplate = "Instruct: Given a natural language query, retrieve relevant code snippets that answer the query\nQuery:{{text}}"

// EmbeddingPreset describes a named, self-contained semantic embedding profile.
// LookupEmbeddingPreset and ListEmbeddingPresets return independent copies.
type EmbeddingPreset struct {
	Name        string
	Description string
	Embedding   EmbeddingConfig
}

var embeddingPresets = []EmbeddingPreset{
	{
		Name:        "fast-local",
		Description: "Fast local search with the default Nomic embedding model",
		Embedding: EmbeddingConfig{
			Provider:      "ollama",
			Model:         "nomic-embed-text",
			Dimensions:    768,
			OllamaContext: 2048,
		},
	},
	{
		Name:        "quality-code",
		Description: "Higher-quality local embeddings tuned for code retrieval",
		Embedding: EmbeddingConfig{
			Provider:      "ollama",
			Model:         "qwen3-embedding:0.6b",
			Dimensions:    1024,
			OllamaContext: 1024,
			QueryTemplate: qualityCodeQueryTemplate,
		},
	},
}

// ListEmbeddingPresets returns all built-in presets in stable name order.
func ListEmbeddingPresets() []EmbeddingPreset {
	presets := make([]EmbeddingPreset, len(embeddingPresets))
	for i := range embeddingPresets {
		presets[i] = cloneEmbeddingPreset(embeddingPresets[i])
	}
	return presets
}

// LookupEmbeddingPreset returns an independent copy of a named preset.
func LookupEmbeddingPreset(name string) (EmbeddingPreset, bool) {
	for _, preset := range embeddingPresets {
		if preset.Name == name {
			return cloneEmbeddingPreset(preset), true
		}
	}
	return EmbeddingPreset{}, false
}

// ApplyEmbeddingPresetToFile atomically applies a named preset's semantic
// embedding fields to a project config, or to defaults.embedding in a global
// config when global is true. Provider endpoints, credentials, throttling,
// batching, keep-alive, and unrelated configuration are preserved.
func ApplyEmbeddingPresetToFile(path, name string, global bool) error {
	preset, ok := LookupEmbeddingPreset(name)
	if !ok {
		return fmt.Errorf("unknown embedding preset: %s", name)
	}

	prefix := "embedding."
	if global {
		prefix = "defaults.embedding."
	}
	embedding := preset.Embedding
	return SetConfigValuesInFile(path, map[string]any{
		prefix + "provider":          embedding.Provider,
		prefix + "model":             embedding.Model,
		prefix + "dimensions":        embedding.Dimensions,
		prefix + "ollama_context":    embedding.OllamaContext,
		prefix + "ollama_options":    cloneOptions(embedding.OllamaOptions),
		prefix + "query_template":    embedding.QueryTemplate,
		prefix + "document_template": embedding.DocumentTemplate,
	})
}

func cloneEmbeddingPreset(preset EmbeddingPreset) EmbeddingPreset {
	preset.Embedding.OllamaOptions = cloneOptions(preset.Embedding.OllamaOptions)
	return preset
}

func cloneOptions(options map[string]any) map[string]any {
	if len(options) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(options))
	for key, value := range options {
		cloned[key] = value
	}
	return cloned
}
