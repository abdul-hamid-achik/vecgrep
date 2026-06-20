package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultCohereURL        = "https://api.cohere.com/v2"
	defaultCohereModel      = "embed-v4.0"
	defaultCohereDims       = 1536
	defaultCohereTimeout    = 60 * time.Second
	defaultCohereMaxRetries = 3
	defaultCohereRetryDelay = 1 * time.Second
	cohereMaxBatchSize      = 96
	cohereInputDocument     = "search_document"
	cohereInputQuery        = "search_query"
)

// CohereConfig holds configuration for the Cohere embedding provider.
type CohereConfig struct {
	APIKey        string
	Model         string
	Dimensions    int
	BaseURL       string
	Timeout       time.Duration
	MaxRetries    int
	RetryInterval time.Duration
}

func DefaultCohereConfig() CohereConfig {
	apiKey := os.Getenv("COHERE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("VECGREP_COHERE_API_KEY")
	}

	baseURL := os.Getenv("COHERE_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("VECGREP_COHERE_BASE_URL")
	}
	if baseURL == "" {
		baseURL = defaultCohereURL
	}

	return CohereConfig{
		APIKey:        apiKey,
		Model:         defaultCohereModel,
		Dimensions:    defaultCohereDims,
		BaseURL:       baseURL,
		Timeout:       defaultCohereTimeout,
		MaxRetries:    defaultCohereMaxRetries,
		RetryInterval: defaultCohereRetryDelay,
	}
}

// CohereProvider implements Provider using Cohere Embed v2.
type CohereProvider struct {
	config CohereConfig
	client *http.Client
}

type cohereEmbeddingRequest struct {
	Model           string   `json:"model"`
	InputType       string   `json:"input_type"`
	Texts           []string `json:"texts"`
	EmbeddingTypes  []string `json:"embedding_types,omitempty"`
	OutputDimension int      `json:"output_dimension,omitempty"`
	Truncate        string   `json:"truncate,omitempty"`
}

type cohereEmbeddingResponse struct {
	ID         string `json:"id"`
	Embeddings struct {
		Float [][]float64 `json:"float"`
	} `json:"embeddings"`
	Texts []string `json:"texts"`
}

type cohereErrorResponse struct {
	Message string `json:"message"`
}

func NewCohereProvider(cfg CohereConfig) *CohereProvider {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("COHERE_API_KEY")
		if cfg.APIKey == "" {
			cfg.APIKey = os.Getenv("VECGREP_COHERE_API_KEY")
		}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("COHERE_BASE_URL")
		if cfg.BaseURL == "" {
			cfg.BaseURL = os.Getenv("VECGREP_COHERE_BASE_URL")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = defaultCohereURL
		}
	}
	if cfg.Model == "" {
		cfg.Model = defaultCohereModel
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = GetModelDimensions(cfg.Model)
		if cfg.Dimensions == 0 {
			cfg.Dimensions = defaultCohereDims
		}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultCohereTimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultCohereMaxRetries
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = defaultCohereRetryDelay
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	return &CohereProvider{
		config: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (p *CohereProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	return p.EmbedQuery(ctx, text)
}

func (p *CohereProvider) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, ErrEmptyText
	}
	embeddings, err := p.embedBatchInternal(ctx, []string{text}, cohereInputQuery)
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return embeddings[0], nil
}

func (p *CohereProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return p.embedBatchByInputType(ctx, texts, cohereInputQuery)
}

func (p *CohereProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return p.embedBatchByInputType(ctx, texts, cohereInputDocument)
}

func (p *CohereProvider) embedBatchByInputType(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	for i, text := range texts {
		if text == "" {
			return nil, NewProviderError("cohere", "embedBatch", fmt.Errorf("text %d: %w", i, ErrEmptyText))
		}
	}
	if len(texts) <= cohereMaxBatchSize {
		return p.embedBatchInternal(ctx, texts, inputType)
	}

	results := make([][]float32, len(texts))
	for i := 0; i < len(texts); i += cohereMaxBatchSize {
		end := i + cohereMaxBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		embeddings, err := p.embedBatchInternal(ctx, texts[i:end], inputType)
		if err != nil {
			return results, err
		}
		for j, embedding := range embeddings {
			results[i+j] = embedding
		}
	}
	return results, nil
}

func (p *CohereProvider) embedBatchInternal(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	if p.config.APIKey == "" {
		return nil, NewProviderError("cohere", "embed", fmt.Errorf("API key not configured"))
	}

	var lastErr error
	for attempt := 0; attempt < p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, NewProviderError("cohere", "embed", ErrContextCanceled)
			case <-time.After(p.config.RetryInterval * time.Duration(1<<uint(attempt-1))):
			}
		}

		embeddings, err := p.doEmbedBatch(ctx, texts, inputType)
		if err == nil {
			return embeddings, nil
		}
		lastErr = err
		if err == ErrContextCanceled {
			return nil, NewProviderError("cohere", "embed", err)
		}
		if errors.Is(err, ErrDimensionMismatch) {
			return nil, NewProviderError("cohere", "embed", err)
		}
		if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate_limit") {
			continue
		}
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "invalid_api_key") {
			return nil, NewProviderError("cohere", "embed", err)
		}
	}
	return nil, NewProviderError("cohere", "embed", lastErr)
}

func (p *CohereProvider) doEmbedBatch(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	reqBody := cohereEmbeddingRequest{
		Model:          p.config.Model,
		InputType:      inputType,
		Texts:          texts,
		EmbeddingTypes: []string{"float"},
		Truncate:       "END",
	}
	if cohereSupportsOutputDimension(p.config.Model) {
		reqBody.OutputDimension = p.config.Dimensions
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.config.BaseURL+"/embed", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.config.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ErrContextCanceled
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp cohereErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, fmt.Errorf("rate_limit: %s", errResp.Message)
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, fmt.Errorf("invalid_api_key: %s", errResp.Message)
			}
			return nil, fmt.Errorf("cohere error: %s", errResp.Message)
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var embResp cohereEmbeddingResponse
	if err := json.Unmarshal(body, &embResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(embResp.Embeddings.Float) != len(texts) {
		return nil, fmt.Errorf("embedding count mismatch: expected %d, got %d", len(texts), len(embResp.Embeddings.Float))
	}

	embeddings := make([][]float32, len(embResp.Embeddings.Float))
	for i, raw := range embResp.Embeddings.Float {
		embedding := float64sToFloat32s(raw)
		if err := validateEmbeddingDimensions("cohere", embedding, p.config.Dimensions); err != nil {
			return nil, err
		}
		embeddings[i] = embedding
	}
	return embeddings, nil
}

func (p *CohereProvider) Model() string {
	return p.config.Model
}

func (p *CohereProvider) Dimensions() int {
	return p.config.Dimensions
}

func (p *CohereProvider) Ping(ctx context.Context) error {
	if p.config.APIKey == "" {
		return NewProviderError("cohere", "ping", fmt.Errorf("API key not configured"))
	}
	if _, err := p.EmbedQuery(ctx, "test"); err != nil {
		return NewProviderError("cohere", "ping", err)
	}
	return nil
}

func cohereSupportsOutputDimension(model string) bool {
	return strings.Contains(strings.ToLower(model), "embed-v4")
}
