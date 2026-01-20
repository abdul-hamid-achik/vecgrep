package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultOllamaURL     = "http://localhost:11434"
	defaultOllamaModel   = "nomic-embed-text"
	defaultOllamaDims    = 768
	defaultTimeout       = 30 * time.Second
	defaultMaxRetries    = 3
	defaultRetryInterval = 500 * time.Millisecond
	maxBatchSize         = 32
)

// OllamaConfig holds configuration for the Ollama embedding provider.
type OllamaConfig struct {
	URL           string
	Model         string
	Dimensions    int
	Timeout       time.Duration
	MaxRetries    int
	RetryInterval time.Duration
}

// DefaultOllamaConfig returns a default configuration for Ollama.
func DefaultOllamaConfig() OllamaConfig {
	return OllamaConfig{
		URL:           defaultOllamaURL,
		Model:         defaultOllamaModel,
		Dimensions:    defaultOllamaDims,
		Timeout:       defaultTimeout,
		MaxRetries:    defaultMaxRetries,
		RetryInterval: defaultRetryInterval,
	}
}

// OllamaProvider implements the Provider interface using Ollama's API.
type OllamaProvider struct {
	config OllamaConfig
	client *http.Client
	mu     sync.RWMutex
}

// ollamaEmbeddingRequest is the request body for Ollama's embedding endpoint.
type ollamaEmbeddingRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// ollamaEmbeddingResponse is the response from Ollama's embedding endpoint.
type ollamaEmbeddingResponse struct {
	Embedding []float64 `json:"embedding"`
}

// ollamaModelResponse is the response from Ollama's model show endpoint.
type ollamaModelResponse struct {
	Name       string `json:"name"`
	ModifiedAt string `json:"modified_at"`
}

// ollamaErrorResponse represents an error response from Ollama.
type ollamaErrorResponse struct {
	Error string `json:"error"`
}

// NewOllamaProvider creates a new Ollama embedding provider.
func NewOllamaProvider(cfg OllamaConfig) *OllamaProvider {
	if cfg.URL == "" {
		cfg.URL = defaultOllamaURL
	}
	if cfg.Model == "" {
		cfg.Model = defaultOllamaModel
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = defaultOllamaDims
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultMaxRetries
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = defaultRetryInterval
	}

	// Ensure URL doesn't have trailing slash
	cfg.URL = strings.TrimRight(cfg.URL, "/")

	return &OllamaProvider{
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
func (p *OllamaProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, ErrEmptyText
	}

	var lastErr error
	for attempt := 0; attempt < p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, NewProviderError("ollama", "embed", ErrContextCanceled)
			case <-time.After(p.config.RetryInterval * time.Duration(attempt)):
			}
		}

		embedding, err := p.doEmbed(ctx, text)
		if err == nil {
			return embedding, nil
		}

		lastErr = err
		// Don't retry on certain errors
		if err == ErrContextCanceled || err == ErrModelNotFound {
			return nil, NewProviderError("ollama", "embed", err)
		}
	}

	return nil, NewProviderError("ollama", "embed", lastErr)
}

// doEmbed performs a single embedding request.
func (p *OllamaProvider) doEmbed(ctx context.Context, text string) ([]float32, error) {
	reqBody := ollamaEmbeddingRequest{
		Model:  p.config.Model,
		Prompt: text,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.config.URL+"/api/embeddings", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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
		var errResp ollamaErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			if strings.Contains(errResp.Error, "model") && strings.Contains(errResp.Error, "not found") {
				return nil, ErrModelNotFound
			}
			return nil, fmt.Errorf("ollama error: %s", errResp.Error)
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var embResp ollamaEmbeddingResponse
	if err := json.Unmarshal(body, &embResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(embResp.Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding returned")
	}

	// Convert float64 to float32
	embedding := make([]float32, len(embResp.Embedding))
	for i, v := range embResp.Embedding {
		embedding[i] = float32(v)
	}

	// Validate dimensions
	if len(embedding) != p.config.Dimensions {
		return nil, fmt.Errorf("%w: expected %d, got %d", ErrDimensionMismatch, p.config.Dimensions, len(embedding))
	}

	return embedding, nil
}

// EmbedBatch generates embeddings for multiple texts.
// Ollama doesn't support native batch embeddings, so we process them concurrently.
func (p *OllamaProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Process in batches to avoid overwhelming the server
	batchSize := maxBatchSize
	if len(texts) < batchSize {
		batchSize = len(texts)
	}

	results := make([][]float32, len(texts))
	errors := make([]error, len(texts))

	// Use a semaphore to limit concurrent requests
	sem := make(chan struct{}, batchSize)
	var wg sync.WaitGroup

	for i, text := range texts {
		if text == "" {
			errors[i] = ErrEmptyText
			continue
		}

		wg.Add(1)
		go func(idx int, t string) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errors[idx] = ErrContextCanceled
				return
			}

			embedding, err := p.Embed(ctx, t)
			if err != nil {
				errors[idx] = err
				return
			}
			results[idx] = embedding
		}(i, text)
	}

	wg.Wait()

	// Check for errors
	for i, err := range errors {
		if err != nil {
			return results, NewProviderError("ollama", "embedBatch", fmt.Errorf("text %d: %w", i, err))
		}
	}

	return results, nil
}

// Model returns the name of the embedding model.
func (p *OllamaProvider) Model() string {
	return p.config.Model
}

// Dimensions returns the embedding vector dimensions.
func (p *OllamaProvider) Dimensions() int {
	return p.config.Dimensions
}

// Ping checks if Ollama is available and the model is loaded.
func (p *OllamaProvider) Ping(ctx context.Context) error {
	// First check if Ollama is running
	req, err := http.NewRequestWithContext(ctx, "GET", p.config.URL+"/api/tags", nil)
	if err != nil {
		return NewProviderError("ollama", "ping", fmt.Errorf("create request: %w", err))
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return NewProviderError("ollama", "ping", ErrProviderUnavailable)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return NewProviderError("ollama", "ping", fmt.Errorf("unexpected status: %d", resp.StatusCode))
	}

	// Check if the model exists by doing a show request
	showReq, err := http.NewRequestWithContext(ctx, "POST", p.config.URL+"/api/show",
		strings.NewReader(fmt.Sprintf(`{"name":"%s"}`, p.config.Model)))
	if err != nil {
		return NewProviderError("ollama", "ping", fmt.Errorf("create show request: %w", err))
	}
	showReq.Header.Set("Content-Type", "application/json")

	showResp, err := p.client.Do(showReq)
	if err != nil {
		return NewProviderError("ollama", "ping", fmt.Errorf("model check failed: %w", err))
	}
	defer showResp.Body.Close()

	if showResp.StatusCode == http.StatusNotFound {
		return NewProviderError("ollama", "ping", ErrModelNotFound)
	}

	if showResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(showResp.Body)
		var errResp ollamaErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			if strings.Contains(errResp.Error, "not found") {
				return NewProviderError("ollama", "ping", ErrModelNotFound)
			}
		}
		return NewProviderError("ollama", "ping", fmt.Errorf("model check status: %d", showResp.StatusCode))
	}

	return nil
}
