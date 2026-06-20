package app

import (
	"fmt"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

func NewProvider(cfg *config.Config) (embed.Provider, error) {
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
			URL:        cfg.Embedding.OllamaURL,
			Model:      cfg.Embedding.Model,
			Dimensions: cfg.Embedding.Dimensions,
		}), nil
	default:
		return nil, fmt.Errorf("unknown embedding provider: %s", cfg.Embedding.Provider)
	}
}
