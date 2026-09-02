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

// NewDaemonProvider constructs the daemon's provider with exactly one
// throttle/cache layer. OpenSession already returns a throttled provider, so
// wrapping it again in internal/daemon hides the disk cache and applies two
// independent queues. The daemon-specific limits belong here, beside the raw
// provider factory, while ownership and Close remain with app.Session.
func NewDaemonProvider(cfg *config.Config) (embed.Provider, error) {
	inner, err := newInnerProvider(cfg)
	if err != nil {
		return nil, err
	}

	throttleCfg := embed.ThrottleConfig{
		Workers:     cfg.Daemon.EmbedWorkers,
		RPS:         cfg.Daemon.EmbedRPS,
		MaxInFlight: cfg.Daemon.EmbedMaxInFlight,
		CacheSize:   1000,
		CachePath:   ResolvedCachePath(cfg),
	}
	if throttleCfg.Workers <= 0 {
		throttleCfg.Workers = config.DefaultDaemonEmbedWorkers
	}
	if throttleCfg.MaxInFlight <= 0 {
		throttleCfg.MaxInFlight = config.DefaultDaemonEmbedMaxInFlight
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
			URL:              cfg.Embedding.OllamaURL,
			Model:            cfg.Embedding.Model,
			Dimensions:       cfg.Embedding.Dimensions,
			MaxBatchSize:     cfg.Embedding.MaxBatchSize,
			KeepAlive:        cfg.Embedding.KeepAlive,
			Context:          cfg.Embedding.OllamaContext,
			Options:          cfg.Embedding.OllamaOptions,
			QueryTemplate:    cfg.Embedding.QueryTemplate,
			DocumentTemplate: cfg.Embedding.DocumentTemplate,
		}), nil
	default:
		return nil, fmt.Errorf("unknown embedding provider: %s", cfg.Embedding.Provider)
	}
}

// ProviderKeyStatus describes the API-key situation of the configured
// embedding provider without ever exposing the key itself.
type ProviderKeyStatus struct {
	// Provider is the configured provider type (ollama, openai, cohere, voyage).
	Provider embed.ProviderType
	// RequiresKey is true for cloud providers.
	RequiresKey bool
	// Source is where the active key comes from ("env:OPENAI_API_KEY",
	// "config:embedding.openai_api_key") or "" when no key is available.
	Source string
}

// Missing reports whether a required key is absent.
func (k ProviderKeyStatus) Missing() bool {
	return k.RequiresKey && k.Source == ""
}

// Label is a compact, printable summary: "env:OPENAI_API_KEY",
// "missing (set OPENAI_API_KEY or VECGREP_OPENAI_API_KEY ...)", or
// "not required" for keyless providers.
func (k ProviderKeyStatus) Label() string {
	switch {
	case !k.RequiresKey:
		return "not required"
	case k.Source == "":
		return "missing — " + embed.APIKeyHint(k.Provider)
	default:
		return k.Source
	}
}

// ResolveProviderKeyStatus inspects the resolved configuration and the current
// process environment for the configured provider's API key.
func ResolveProviderKeyStatus(cfg *config.Config) ProviderKeyStatus {
	if cfg == nil {
		return ProviderKeyStatus{}
	}
	provider := embed.ProviderType(cfg.Embedding.Provider)
	if provider == "" {
		provider = embed.ProviderOllama
	}
	status := ProviderKeyStatus{Provider: provider, RequiresKey: embed.RequiresAPIKey(provider)}
	if !status.RequiresKey {
		return status
	}
	var configValue string
	switch provider {
	case embed.ProviderOpenAI:
		configValue = cfg.Embedding.OpenAIAPIKey
	case embed.ProviderCohere:
		configValue = cfg.Embedding.CohereAPIKey
	case embed.ProviderVoyage:
		configValue = cfg.Embedding.VoyageAPIKey
	}
	status.Source = embed.APIKeySource(provider, configValue)
	return status
}
