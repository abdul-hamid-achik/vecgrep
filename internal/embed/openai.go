package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultOpenAIURL        = "https://api.openai.com/v1"
	defaultOpenAIModel      = "text-embedding-3-small"
	defaultOpenAIDims       = 1536
	defaultOpenAITimeout    = 60 * time.Second
	defaultOpenAIMaxRetries = 3
	defaultOpenAIRetryDelay = 1 * time.Second
	openAIMaxBatchSize      = 2048 // OpenAI supports up to 2048 inputs per request
)

// OpenAIConfig holds configuration for the OpenAI embedding provider.
type OpenAIConfig struct {
	APIKey        string
	Model         string
	Dimensions    int
	BaseURL       string
	Timeout       time.Duration
	MaxRetries    int
	RetryInterval time.Duration
}

// DefaultOpenAIConfig returns a default configuration for OpenAI.
func DefaultOpenAIConfig() OpenAIConfig {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("VECGREP_OPENAI_API_KEY")
	}

	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("VECGREP_OPENAI_BASE_URL")
	}
	if baseURL == "" {
		baseURL = defaultOpenAIURL
	}

	return OpenAIConfig{
		APIKey:        apiKey,
		Model:         defaultOpenAIModel,
		Dimensions:    defaultOpenAIDims,
		BaseURL:       baseURL,
		Timeout:       defaultOpenAITimeout,
		MaxRetries:    defaultOpenAIMaxRetries,
		RetryInterval: defaultOpenAIRetryDelay,
	}
}

// OpenAIProvider implements the Provider interface using OpenAI's API.
type OpenAIProvider struct {
	config OpenAIConfig
	client *http.Client
}

// openaiEmbeddingRequest is the request body for OpenAI's embedding endpoint.
type openaiEmbeddingRequest struct {
	Model      string      `json:"model"`
	Input      interface{} `json:"input"` // string or []string
	Dimensions int         `json:"dimensions,omitempty"`
}

// openaiEmbeddingResponse is the response from OpenAI's embedding endpoint.
type openaiEmbeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// openaiErrorResponse represents an error response from OpenAI.
type openaiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Param   string `json:"param"`
		Code    string `json:"code"`
	} `json:"error"`
}

// NewOpenAIProvider creates a new OpenAI embedding provider.
func NewOpenAIProvider(cfg OpenAIConfig) *OpenAIProvider {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
		if cfg.APIKey == "" {
			cfg.APIKey = os.Getenv("VECGREP_OPENAI_API_KEY")
		}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
		if cfg.BaseURL == "" {
			cfg.BaseURL = os.Getenv("VECGREP_OPENAI_BASE_URL")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = defaultOpenAIURL
		}
	}
	if cfg.Model == "" {
		cfg.Model = defaultOpenAIModel
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = GetModelDimensions(cfg.Model)
		if cfg.Dimensions == 0 {
			cfg.Dimensions = defaultOpenAIDims
		}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultOpenAITimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultOpenAIMaxRetries
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = defaultOpenAIRetryDelay
	}

	// Ensure URL doesn't have trailing slash
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	return &OpenAIProvider{
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

// Embed generates an embedding for a single text.
func (p *OpenAIProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, ErrEmptyText
	}

	embeddings, err := p.embedBatchInternal(ctx, []string{text})
	if err != nil {
		return nil, err
	}

	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}

	return embeddings[0], nil
}

// EmbedBatch generates embeddings for multiple texts.
func (p *OpenAIProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Filter empty texts
	for i, text := range texts {
		if text == "" {
			return nil, NewProviderError("openai", "embedBatch", fmt.Errorf("text %d: %w", i, ErrEmptyText))
		}
	}

	// Process in batches if needed
	if len(texts) <= openAIMaxBatchSize {
		return p.embedBatchInternal(ctx, texts)
	}

	// Process large batches
	results := make([][]float32, len(texts))
	for i := 0; i < len(texts); i += openAIMaxBatchSize {
		end := i + openAIMaxBatchSize
		if end > len(texts) {
			end = len(texts)
		}

		batch := texts[i:end]
		embeddings, err := p.embedBatchInternal(ctx, batch)
		if err != nil {
			return results, err
		}

		for j, emb := range embeddings {
			results[i+j] = emb
		}
	}

	return results, nil
}

// embedBatchInternal performs a batch embedding request.
func (p *OpenAIProvider) embedBatchInternal(ctx context.Context, texts []string) ([][]float32, error) {
	if p.config.APIKey == "" {
		return nil, NewProviderError("openai", "embed", fmt.Errorf("API key not configured"))
	}

	var lastErr error
	for attempt := 0; attempt < p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, NewProviderError("openai", "embed", ErrContextCanceled)
			case <-time.After(p.config.RetryInterval * time.Duration(1<<uint(attempt-1))):
				// Exponential backoff
			}
		}

		embeddings, err := p.doEmbedBatch(ctx, texts)
		if err == nil {
			return embeddings, nil
		}

		lastErr = err
		// Don't retry on certain errors
		if err == ErrContextCanceled {
			return nil, NewProviderError("openai", "embed", err)
		}
		// Check for rate limit (429) - always retry
		if strings.Contains(err.Error(), "rate_limit") || strings.Contains(err.Error(), "429") {
			continue
		}
		// Check for authentication errors - don't retry
		if strings.Contains(err.Error(), "invalid_api_key") || strings.Contains(err.Error(), "401") {
			return nil, NewProviderError("openai", "embed", err)
		}
	}

	return nil, NewProviderError("openai", "embed", lastErr)
}

// doEmbedBatch performs a single batch embedding request.
func (p *OpenAIProvider) doEmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	var input interface{}
	if len(texts) == 1 {
		input = texts[0]
	} else {
		input = texts
	}

	reqBody := openaiEmbeddingRequest{
		Model: p.config.Model,
		Input: input,
	}

	// Only include dimensions for models that support it (text-embedding-3-*)
	if strings.HasPrefix(p.config.Model, "text-embedding-3") {
		reqBody.Dimensions = p.config.Dimensions
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.config.BaseURL+"/embeddings", bytes.NewReader(jsonBody))
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
		var errResp openaiErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error.Message != "" {
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, fmt.Errorf("rate_limit: %s", errResp.Error.Message)
			}
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, fmt.Errorf("invalid_api_key: %s", errResp.Error.Message)
			}
			return nil, fmt.Errorf("openai error (%s): %s", errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var embResp openaiEmbeddingResponse
	if err := json.Unmarshal(body, &embResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	// Sort by index to ensure correct order
	embeddings := make([][]float32, len(texts))
	for _, data := range embResp.Data {
		if data.Index < 0 || data.Index >= len(texts) {
			return nil, fmt.Errorf("invalid embedding index: %d", data.Index)
		}

		// Convert float64 to float32
		embedding := make([]float32, len(data.Embedding))
		for i, v := range data.Embedding {
			embedding[i] = float32(v)
		}

		embeddings[data.Index] = embedding
	}

	// Verify all embeddings are present
	for i, emb := range embeddings {
		if emb == nil {
			return nil, fmt.Errorf("missing embedding for index %d", i)
		}
	}

	return embeddings, nil
}

// Model returns the name of the embedding model.
func (p *OpenAIProvider) Model() string {
	return p.config.Model
}

// Dimensions returns the embedding vector dimensions.
func (p *OpenAIProvider) Dimensions() int {
	return p.config.Dimensions
}

// Ping checks if OpenAI is available and the API key is valid.
func (p *OpenAIProvider) Ping(ctx context.Context) error {
	if p.config.APIKey == "" {
		return NewProviderError("openai", "ping", fmt.Errorf("API key not configured"))
	}

	// Try to get a small embedding to verify the API key works
	_, err := p.Embed(ctx, "test")
	if err != nil {
		return NewProviderError("openai", "ping", err)
	}

	return nil
}
