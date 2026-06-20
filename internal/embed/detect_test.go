package embed

import (
	"context"
	"testing"
	"time"
)

func TestDetectProvidersUsesCloudEnvAliases(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VECGREP_OPENAI_API_KEY", "openai-key")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("VECGREP_OPENAI_BASE_URL", "https://openai.example.test/v1")
	t.Setenv("COHERE_API_KEY", "cohere-key")
	t.Setenv("VECGREP_COHERE_API_KEY", "")
	t.Setenv("COHERE_BASE_URL", "https://cohere.example.test/v2")
	t.Setenv("VECGREP_COHERE_BASE_URL", "")
	t.Setenv("VOYAGE_API_KEY", "")
	t.Setenv("VECGREP_VOYAGE_API_KEY", "voyage-key")
	t.Setenv("VOYAGE_BASE_URL", "")
	t.Setenv("VECGREP_VOYAGE_BASE_URL", "https://voyage.example.test/v1")

	providers := DetectProviders(context.Background(), DetectConfig{
		OllamaURL:      "http://127.0.0.1:1",
		PreferredModel: "nomic-embed-text",
		Timeout:        10 * time.Millisecond,
	})

	openai := detectedProviderByType(providers, ProviderOpenAI)
	if openai == nil || !openai.Available {
		t.Fatalf("OpenAI provider = %+v, want available", openai)
	}
	if openai.URL != "https://openai.example.test/v1" {
		t.Fatalf("OpenAI URL = %q, want VECGREP override", openai.URL)
	}

	cohere := detectedProviderByType(providers, ProviderCohere)
	if cohere == nil || !cohere.Available {
		t.Fatalf("Cohere provider = %+v, want available", cohere)
	}
	if cohere.URL != "https://cohere.example.test/v2" {
		t.Fatalf("Cohere URL = %q, want standard env URL", cohere.URL)
	}

	voyage := detectedProviderByType(providers, ProviderVoyage)
	if voyage == nil || !voyage.Available {
		t.Fatalf("Voyage provider = %+v, want available", voyage)
	}
	if voyage.URL != "https://voyage.example.test/v1" {
		t.Fatalf("Voyage URL = %q, want VECGREP override", voyage.URL)
	}
}

func detectedProviderByType(providers []DetectedProvider, providerType ProviderType) *DetectedProvider {
	for i := range providers {
		if providers[i].Type == providerType {
			return &providers[i]
		}
	}
	return nil
}
