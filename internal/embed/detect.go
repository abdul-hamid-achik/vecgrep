// Package embed provides embedding generation for semantic search.
package embed

import (
	"context"
	"encoding/json"
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
	// ProviderCohere is the Cohere embedding provider.
	ProviderCohere ProviderType = "cohere"
	// ProviderVoyage is the Voyage AI embedding provider.
	ProviderVoyage ProviderType = "voyage"
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
	if cohere := detectCohere(ctx); cohere != nil {
		providers = append(providers, *cohere)
	}
	if voyage := detectVoyage(ctx); voyage != nil {
		providers = append(providers, *voyage)
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

	if url := os.Getenv("VECGREP_OPENAI_BASE_URL"); url != "" {
		provider.URL = url
	} else if url := os.Getenv("OPENAI_BASE_URL"); url != "" {
		provider.URL = url
	}

	// Check for API key
	if os.Getenv("OPENAI_API_KEY") == "" && os.Getenv("VECGREP_OPENAI_API_KEY") == "" {
		provider.Available = false
		return provider
	}

	provider.Available = true
	return provider
}

// detectCohere checks if Cohere API credentials are available.
func detectCohere(_ context.Context) *DetectedProvider {
	provider := &DetectedProvider{
		Type:        ProviderCohere,
		URL:         "https://api.cohere.com/v2",
		Model:       "embed-v4.0",
		Dimensions:  1536,
		Description: "Cohere Embed API",
	}

	if url := os.Getenv("VECGREP_COHERE_BASE_URL"); url != "" {
		provider.URL = url
	} else if url := os.Getenv("COHERE_BASE_URL"); url != "" {
		provider.URL = url
	}

	if os.Getenv("COHERE_API_KEY") == "" && os.Getenv("VECGREP_COHERE_API_KEY") == "" {
		provider.Available = false
		return provider
	}

	provider.Available = true
	return provider
}

// detectVoyage checks if Voyage AI API credentials are available.
func detectVoyage(_ context.Context) *DetectedProvider {
	provider := &DetectedProvider{
		Type:        ProviderVoyage,
		URL:         "https://api.voyageai.com/v1",
		Model:       "voyage-code-3",
		Dimensions:  1024,
		Description: "Voyage AI embedding API",
	}

	if url := os.Getenv("VECGREP_VOYAGE_BASE_URL"); url != "" {
		provider.URL = url
	} else if url := os.Getenv("VOYAGE_BASE_URL"); url != "" {
		provider.URL = url
	}

	if os.Getenv("VOYAGE_API_KEY") == "" && os.Getenv("VECGREP_VOYAGE_API_KEY") == "" {
		provider.Available = false
		return provider
	}

	provider.Available = true
	return provider
}

// localOllamaHintTimeout bounds the onboarding-hint probe. It only matters
// when localhost connections hang (firewall rules); refusals return
// immediately. Keep it short — the hint rides on an error path.
const localOllamaHintTimeout = 750 * time.Millisecond

// localOllamaBaseURL resolves the local Ollama base URL from OLLAMA_HOST,
// defaulting to http://localhost:11434. A scheme-less host gains http:// so
// the URL can be used directly in requests.
func localOllamaBaseURL() string {
	url := os.Getenv("OLLAMA_HOST")
	if url == "" {
		return "http://localhost:11434"
	}
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	return strings.TrimSuffix(url, "/")
}

// LocalOllamaHint returns an onboarding tip when a local Ollama is reachable
// and has at least one embedding model pulled, or "" otherwise. It backs the
// auth-failure suggestion on the index path: a user whose cloud provider has
// no API key can be pointed at the keyless local setup directly. Strictly
// best-effort — bounded by a short timeout, never returns an error, and a
// detection failure never surfaces to the user.
func LocalOllamaHint(ctx context.Context) string {
	return localOllamaHintAt(ctx, localOllamaBaseURL())
}

// localOllamaHintAt is LocalOllamaHint with an explicit base URL, for tests.
func localOllamaHintAt(ctx context.Context, baseURL string) string {
	ctx, cancel := context.WithTimeout(ctx, localOllamaHintTimeout)
	defer cancel()

	models, ok := fetchOllamaModelNames(ctx, baseURL)
	if !ok || !hasEmbeddingModel(models) {
		return ""
	}
	return "a local ollama is running with embedding models pulled — `vecgrep config preset fast-local` needs no API key"
}

// fetchOllamaModelNames lists installed model names from Ollama's /api/tags.
// ok is false whenever the endpoint is unreachable, non-200, or malformed.
func fetchOllamaModelNames(ctx context.Context, baseURL string) (names []string, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/tags", nil)
	if err != nil {
		return nil, false
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, false
	}
	for _, m := range payload.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	return names, true
}

// hasEmbeddingModel reports whether any installed Ollama model looks like an
// embedding model: either a known vecgrep embedding model or a name carrying
// "embed" (covers e.g. qwen3-embedding:0.6b without hardcoding every tag).
func hasEmbeddingModel(models []string) bool {
	known := make(map[string]bool)
	for _, m := range GetSupportedModels() {
		if m.Provider == ProviderOllama {
			known[m.Name] = true
		}
	}
	for _, name := range models {
		base := name
		if i := strings.Index(base, ":"); i >= 0 {
			base = base[:i]
		}
		if known[base] || strings.Contains(strings.ToLower(name), "embed") {
			return true
		}
	}
	return false
}

// AutoDetect finds and returns the best available provider.
// It prefers local providers (Ollama) over cloud providers.
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

	// Fall back to cloud providers if available.
	for _, p := range providers {
		switch {
		case p.Type == ProviderOpenAI && p.Available:
			return NewOpenAIProvider(OpenAIConfig{
				BaseURL:    p.URL,
				Model:      p.Model,
				Dimensions: p.Dimensions,
			}), nil
		case p.Type == ProviderCohere && p.Available:
			return NewCohereProvider(CohereConfig{
				BaseURL:    p.URL,
				Model:      p.Model,
				Dimensions: p.Dimensions,
			}), nil
		case p.Type == ProviderVoyage && p.Available:
			return NewVoyageProvider(VoyageConfig{
				BaseURL:    p.URL,
				Model:      p.Model,
				Dimensions: p.Dimensions,
			}), nil
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
		fmt.Fprintf(&sb, "  - %s (%s)\n", p.Type, status)
		fmt.Fprintf(&sb, "    URL: %s\n", p.URL)
		fmt.Fprintf(&sb, "    Model: %s (%d dimensions)\n", p.Model, p.Dimensions)
		fmt.Fprintf(&sb, "    %s\n", p.Description)
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
				provider := NewOpenAIProvider(OpenAIConfig{
					BaseURL:    p.URL,
					Model:      p.Model,
					Dimensions: p.Dimensions,
				})
				return provider.Ping(ctx)
			case ProviderCohere:
				provider := NewCohereProvider(CohereConfig{
					BaseURL:    p.URL,
					Model:      p.Model,
					Dimensions: p.Dimensions,
				})
				return provider.Ping(ctx)
			case ProviderVoyage:
				provider := NewVoyageProvider(VoyageConfig{
					BaseURL:    p.URL,
					Model:      p.Model,
					Dimensions: p.Dimensions,
				})
				return provider.Ping(ctx)
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
		{
			Name:       "embed-v4.0",
			Provider:   ProviderCohere,
			Dimensions: 1536,
			MaxTokens:  128000,
		},
		{
			Name:       "embed-english-v3.0",
			Provider:   ProviderCohere,
			Dimensions: 1024,
			MaxTokens:  512,
		},
		{
			Name:       "embed-multilingual-v3.0",
			Provider:   ProviderCohere,
			Dimensions: 1024,
			MaxTokens:  512,
		},
		{
			Name:       "voyage-code-3",
			Provider:   ProviderVoyage,
			Dimensions: 1024,
			MaxTokens:  120000,
		},
		{
			Name:       "voyage-3.5",
			Provider:   ProviderVoyage,
			Dimensions: 1024,
			MaxTokens:  320000,
		},
		{
			Name:       "voyage-3.5-lite",
			Provider:   ProviderVoyage,
			Dimensions: 1024,
			MaxTokens:  1000000,
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
