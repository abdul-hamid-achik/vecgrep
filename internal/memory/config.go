// Package memory provides agent memory/note-taking functionality using veclite.
package memory

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultMemoryDir is the directory name for vecai memory storage.
	DefaultMemoryDir = ".vecai/memory"
	// DefaultDBFile is the default veclite database filename for memory.
	DefaultDBFile = "memory.veclite"
	// DefaultOllamaURL is the default Ollama API URL.
	DefaultOllamaURL = "http://localhost:11434"
	// DefaultEmbeddingModel is the default embedding model.
	DefaultEmbeddingModel = "nomic-embed-text"
	// DefaultEmbeddingDimensions is the default embedding dimensions for nomic-embed-text.
	DefaultEmbeddingDimensions = 768
)

// Config holds memory store configuration.
type Config struct {
	// DBPath is the path to the veclite database file.
	DBPath string
	// OllamaURL is the Ollama API URL.
	OllamaURL string
	// EmbeddingModel is the embedding model name.
	EmbeddingModel string
	// EmbeddingDimensions is the embedding vector dimensions.
	EmbeddingDimensions int
}

// DefaultConfig returns the default memory configuration.
// It reads from environment variables with fallback to defaults.
func DefaultConfig() *Config {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	cfg := &Config{
		DBPath:              filepath.Join(homeDir, DefaultMemoryDir, DefaultDBFile),
		OllamaURL:           DefaultOllamaURL,
		EmbeddingModel:      DefaultEmbeddingModel,
		EmbeddingDimensions: DefaultEmbeddingDimensions,
	}

	// Override from environment variables
	if url := os.Getenv("VECAI_OLLAMA_URL"); url != "" {
		cfg.OllamaURL = strings.TrimRight(url, "/")
	}
	if model := os.Getenv("VECAI_EMBEDDING_MODEL"); model != "" {
		cfg.EmbeddingModel = model
	}

	return cfg
}

// EnsureDir creates the memory directory if it doesn't exist.
func (c *Config) EnsureDir() error {
	dir := filepath.Dir(c.DBPath)
	return os.MkdirAll(dir, 0755)
}
