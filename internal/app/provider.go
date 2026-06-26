package app

import (
	"fmt"
	"path/filepath"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

// defaultEmbedCacheFilename is the bbolt file name used for the
// disk-persistent embedding cache when Cache.Path is not set explicitly.
const defaultEmbedCacheFilename = "embed-cache.db"

// ResolvedCachePath returns the bbolt path for the disk-persistent embedding
// cache. If cfg.Cache.Path is set, it is used as-is. Otherwise the path is
// derived from the project's base data directory (not the per-branch
// subdirectory) so the embedding cache is shared across branches.
func ResolvedCachePath(cfg *config.Config) string {
	if cfg.Cache.Path != "" {
		return cfg.Cache.Path
	}
	if cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(cfg.DataDir, defaultEmbedCacheFilename)
}

func NewProvider(cfg *config.Config) (embed.Provider, error) {
	inner, err := newInnerProvider(cfg)
	if err != nil {
		return nil, err
	}

	// Wrap the inner provider with a ThrottledProvider unless the user
	// has explicitly opted out via embedding.throttle.enabled = false.
	// The throttle layer adds content-hash dedup, an in-memory cache,
	// bounded concurrency, and optional rate limiting — all of which
	// benefit the CLI `vecgrep index` path just as much as the daemon.
	throttle := cfg.Embedding.Throttle
	if throttle.Enabled != nil && !*throttle.Enabled {
		return inner, nil
	}

	throttleCfg := embed.ThrottleConfig{
		MaxInFlight: throttle.MaxInFlight,
		RPS:         throttle.RateLimit,
		CacheSize:   1000,
		CachePath:   ResolvedCachePath(cfg),
	}
	if throttleCfg.MaxInFlight <= 0 {
		throttleCfg.MaxInFlight = embed.DefaultThrottleConfig().MaxInFlight
	}

	return embed.NewThrottledProvider(inner, throttleCfg), nil
}

// newInnerProvider constructs the raw embedding provider based on the
// configured provider type, without any throttle/cache wrapper.
func newInnerProvider(cfg *config.Config) (embed.Provider, error) {
	switch cfg.Embedding.Provider {
	case "openai":
		return embed.NewOpenAIProvider(embed.OpenAIConfig{
			APIKey:     cfg.Embedding.OpenAIAPIKey,
			BaseURL:    cfg.Embedding.OpenAIBaseURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
		}), nil
	case "cohere":
		return embed.NewCohereProvider(embed.CohereConfig{
			APIKey:     cfg.Embedding.CohereAPIKey,
			BaseURL:    cfg.Embedding.CohereBaseURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
		}), nil
	case "voyage":
		return embed.NewVoyageProvider(embed.VoyageConfig{
			APIKey:     cfg.Embedding.VoyageAPIKey,
			BaseURL:    cfg.Embedding.VoyageBaseURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
		}), nil
	case "ollama", "":
		return embed.NewOllamaProvider(embed.OllamaConfig{
			URL:          cfg.Embedding.OllamaURL,
			Model:        cfg.Embedding.Model,
			Dimensions:   cfg.Embedding.Dimensions,
			MaxBatchSize: cfg.Embedding.MaxBatchSize,
			KeepAlive:    cfg.Embedding.KeepAlive,
		}), nil
	default:
		return nil, fmt.Errorf("unknown embedding provider: %s", cfg.Embedding.Provider)
	}
}
