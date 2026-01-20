// Package embed provides embedding generation for semantic search.
package embed

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ProviderType represents the type of embedding provider.
type ProviderType string

const (
	// ProviderOllama is the Ollama embedding provider.
	ProviderOllama ProviderType = "ollama"
	// ProviderOpenAI is the OpenAI embedding provider.
	ProviderOpenAI ProviderType = "openai"
	// ProviderUnknown is an unknown provider type.
	ProviderUnknown ProviderType = "unknown"
)

// DetectedProvider contains information about a detected embedding provider.
type DetectedProvider struct {
	Type        ProviderType
	Available   bool
	URL         string
	Model       string
	Dimensions  int
	Description string
}

// DetectConfig configures provider detection behavior.
type DetectConfig struct {
	// OllamaURL is the URL to check for Ollama.
	OllamaURL string
	// PreferredModel is the preferred embedding model.
	PreferredModel string
	// Timeout is the timeout for provider detection requests.
	Timeout time.Duration
}

// DefaultDetectConfig returns sensible defaults for provider detection.
func DefaultDetectConfig() DetectConfig {
	return DetectConfig{
		OllamaURL:      "http://localhost:11434",
		PreferredModel: "nomic-embed-text",
		Timeout:        5 * time.Second,
	}
}

// DetectProviders scans for available embedding providers.
func DetectProviders(ctx context.Context, cfg DetectConfig) []DetectedProvider {
	var providers []DetectedProvider

	// Check for Ollama
	if ollama := detectOllama(ctx, cfg); ollama != nil {
		providers = append(providers, *ollama)
	}

	// Check for OpenAI (via environment variable)
	if openai := detectOpenAI(ctx); openai != nil {
		providers = append(providers, *openai)
	}

	return providers
}

// detectOllama checks if Ollama is available.
func detectOllama(ctx context.Context, cfg DetectConfig) *DetectedProvider {
	provider := &DetectedProvider{
		Type:        ProviderOllama,
		URL:         cfg.OllamaURL,
		Model:       cfg.PreferredModel,
		Dimensions:  768, // Default for nomic-embed-text
		Description: "Local embedding provider using Ollama",
	}

	// Check if OLLAMA_HOST is set
	if host := os.Getenv("OLLAMA_HOST"); host != "" {
		provider.URL = host
	}

	// Try to connect to Ollama
	client := &http.Client{Timeout: cfg.Timeout}

	req, err := http.NewRequestWithContext(ctx, "GET", provider.URL+"/api/tags", nil)
	if err != nil {
		provider.Available = false
		return provider
	}

	resp, err := client.Do(req)
	if err != nil {
		provider.Available = false
		return provider
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		provider.Available = false
		return provider
	}

	provider.Available = true
	return provider
}

// detectOpenAI checks if OpenAI API credentials are available.
func detectOpenAI(_ context.Context) *DetectedProvider {
	provider := &DetectedProvider{
		Type:        ProviderOpenAI,
		URL:         "https://api.openai.com/v1",
		Model:       "text-embedding-3-small",
		Dimensions:  1536,
		Description: "OpenAI embedding API",
	}

	// Check for API key
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		provider.Available = false
		return provider
	}

	provider.Available = true
	return provider
}

// AutoDetect finds and returns the best available provider.
// It prefers local providers (Ollama) over cloud providers (OpenAI).
func AutoDetect(ctx context.Context) (Provider, error) {
	return AutoDetectWithConfig(ctx, DefaultDetectConfig())
}

// AutoDetectWithConfig finds and returns the best available provider with custom config.
func AutoDetectWithConfig(ctx context.Context, cfg DetectConfig) (Provider, error) {
	providers := DetectProviders(ctx, cfg)

	// Prefer Ollama (local-first)
	for _, p := range providers {
		if p.Type == ProviderOllama && p.Available {
			return NewOllamaProvider(OllamaConfig{
				URL:        p.URL,
				Model:      p.Model,
				Dimensions: p.Dimensions,
			}), nil
		}
	}

	// Fall back to OpenAI if available
	for _, p := range providers {
		if p.Type == ProviderOpenAI && p.Available {
			// Note: OpenAI provider would need to be implemented
			// For now, return an error indicating it's not yet supported
			return nil, fmt.Errorf("OpenAI provider detected but not yet implemented")
		}
	}

	return nil, ErrProviderUnavailable
}

// GetProviderInfo returns human-readable information about detected providers.
func GetProviderInfo(ctx context.Context) string {
	providers := DetectProviders(ctx, DefaultDetectConfig())

	var sb strings.Builder
	sb.WriteString("Detected Embedding Providers:\n")

	if len(providers) == 0 {
		sb.WriteString("  No providers detected\n")
		return sb.String()
	}

	for _, p := range providers {
		status := "unavailable"
		if p.Available {
			status = "available"
		}
		sb.WriteString(fmt.Sprintf("  - %s (%s)\n", p.Type, status))
		sb.WriteString(fmt.Sprintf("    URL: %s\n", p.URL))
		sb.WriteString(fmt.Sprintf("    Model: %s (%d dimensions)\n", p.Model, p.Dimensions))
		sb.WriteString(fmt.Sprintf("    %s\n", p.Description))
	}

	return sb.String()
}

// VerifyProvider checks if a specific provider is available and working.
func VerifyProvider(ctx context.Context, providerType ProviderType) error {
	cfg := DefaultDetectConfig()
	providers := DetectProviders(ctx, cfg)

	for _, p := range providers {
		if p.Type == providerType {
			if !p.Available {
				return fmt.Errorf("provider %s detected but not available", providerType)
			}

			// Try to create and ping the provider
			switch providerType {
			case ProviderOllama:
				provider := NewOllamaProvider(OllamaConfig{
					URL:        p.URL,
					Model:      p.Model,
					Dimensions: p.Dimensions,
				})
				return provider.Ping(ctx)
			case ProviderOpenAI:
				return fmt.Errorf("OpenAI provider verification not yet implemented")
			}
		}
	}

	return fmt.Errorf("provider %s not detected", providerType)
}

// ModelInfo contains information about an embedding model.
type ModelInfo struct {
	Name       string
	Provider   ProviderType
	Dimensions int
	MaxTokens  int
}

// GetSupportedModels returns a list of supported embedding models.
func GetSupportedModels() []ModelInfo {
	return []ModelInfo{
		{
			Name:       "nomic-embed-text",
			Provider:   ProviderOllama,
			Dimensions: 768,
			MaxTokens:  8192,
		},
		{
			Name:       "mxbai-embed-large",
			Provider:   ProviderOllama,
			Dimensions: 1024,
			MaxTokens:  512,
		},
		{
			Name:       "all-minilm",
			Provider:   ProviderOllama,
			Dimensions: 384,
			MaxTokens:  256,
		},
		{
			Name:       "text-embedding-3-small",
			Provider:   ProviderOpenAI,
			Dimensions: 1536,
			MaxTokens:  8191,
		},
		{
			Name:       "text-embedding-3-large",
			Provider:   ProviderOpenAI,
			Dimensions: 3072,
			MaxTokens:  8191,
		},
	}
}

// GetModelDimensions returns the embedding dimensions for a known model.
// Returns 0 if the model is unknown.
func GetModelDimensions(model string) int {
	for _, m := range GetSupportedModels() {
		if m.Name == model {
			return m.Dimensions
		}
	}
	return 0
}
