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
	// DecayHalfLifeHours controls temporal decay of recall ranking:
	// a memory's score halves every this-many hours. 0 disables decay.
	DecayHalfLifeHours int
	// ImportanceBoost scales how strongly a memory's importance (0..1)
	// lifts its recall rank. 0 disables the boost.
	ImportanceBoost float64
	// UsageBoost caps how much repeated recall lifts a memory's rank
	// (score * (1 + UsageBoost * usageFactor), usageFactor in [0,1]). This is
	// the learning-from-use term: memories the agent actually retrieves
	// resurface more readily. 0 disables usage tracking and ranking.
	UsageBoost float64
	// DedupThreshold is the cosine similarity at or above which Remember
	// refuses new content as a near-duplicate of an existing memory. 0
	// disables duplicate detection.
	DedupThreshold float64
	// RelatedThreshold is the cosine similarity above which Remember links
	// the new memory to an existing one, in both directions. 0 disables
	// linking.
	RelatedThreshold float64
}

// DefaultDecayHalfLifeHours halves a memory's recall score every 30 days —
// old memories fade but never vanish (unlike TTL expiry).
const DefaultDecayHalfLifeHours = 24 * 30

// DefaultImportanceBoost gives importance a moderate say in ranking:
// boosted = score * (1 + factor*importance).
const DefaultImportanceBoost = 0.25

// DefaultUsageBoost caps the learning-from-use lift: a memory recalled often
// and recently gains up to this fractional rank boost. Saturating (half effect
// at accessSaturation accesses) and recency-weighted, so it compounds without
// running away.
const DefaultUsageBoost = 0.25

// DefaultDedupThreshold refuses re-storing content that is a near-identical
// re-statement of an existing memory (cosine similarity at or above this).
// High enough that paraphrases pass; only true re-stores trip it.
const DefaultDedupThreshold = 0.98

// DefaultRelatedThreshold links memories whose cosine similarity exceeds this,
// in both directions, building the connection graph Recall exposes.
const DefaultRelatedThreshold = 0.70

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
		DecayHalfLifeHours:  DefaultDecayHalfLifeHours,
		ImportanceBoost:     DefaultImportanceBoost,
		UsageBoost:          DefaultUsageBoost,
		DedupThreshold:      DefaultDedupThreshold,
		RelatedThreshold:    DefaultRelatedThreshold,
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
