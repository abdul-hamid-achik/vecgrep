package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	maxBatchSize         = 64
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
}

// ollamaEmbeddingRequest is the request body for Ollama's legacy /api/embeddings
// endpoint (single prompt). Superseded by /api/embed which supports batch input.
type ollamaEmbeddingRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// ollamaEmbeddingResponse is the response from Ollama's legacy /api/embeddings
// endpoint.
type ollamaEmbeddingResponse struct {
	Embedding []float64 `json:"embedding"`
}

// ollamaBatchEmbedRequest is the request body for Ollama's /api/embed endpoint
// which accepts a string or a list of strings as input.
type ollamaBatchEmbedRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	Truncate bool     `json:"truncate"`
}

// ollamaBatchEmbedResponse is the response from Ollama's /api/embed endpoint.
type ollamaBatchEmbedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	TotalDuration   int64       `json:"total_duration,omitempty"`
	LoadDuration    int64       `json:"load_duration,omitempty"`
	PromptEvalCount int         `json:"prompt_eval_count,omitempty"`
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

// EmbedDocuments generates embeddings for multiple texts using Ollama's
// native batch endpoint (/api/embed). This is far more efficient than
// EmbedBatch because it sends a single HTTP request for up to maxBatchSize
// texts instead of one request per text.
func (p *OllamaProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Filter out empty texts and track their positions.
	type indexedText struct {
		idx  int
		text string
	}
	var nonEmpty []indexedText
	for i, t := range texts {
		if t == "" {
			return nil, NewProviderError("ollama", "embedDocuments", fmt.Errorf("text %d: %w", i, ErrEmptyText))
		}
		nonEmpty = append(nonEmpty, indexedText{idx: i, text: t})
	}

	results := make([][]float32, len(texts))

	// Process in sub-batches to keep HTTP request sizes reasonable.
	for start := 0; start < len(nonEmpty); start += maxBatchSize {
		end := start + maxBatchSize
		if end > len(nonEmpty) {
			end = len(nonEmpty)
		}

		batch := nonEmpty[start:end]
		input := make([]string, len(batch))
		for j, b := range batch {
			input[j] = b.text
		}

		embeddings, err := p.doEmbedBatch(ctx, input)
		if err != nil {
			return results, NewProviderError("ollama", "embedDocuments", err)
		}

		for j, emb := range embeddings {
			results[batch[j].idx] = emb
		}
	}

	return results, nil
}

// doEmbedBatch sends a single HTTP request to /api/embed with multiple texts
// and returns the embeddings in the same order.
func (p *OllamaProvider) doEmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := ollamaBatchEmbedRequest{
		Model:    p.config.Model,
		Input:    texts,
		Truncate: true,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ErrContextCanceled
			case <-time.After(p.config.RetryInterval * time.Duration(attempt)):
			}
		}

		embeddings, err := p.doEmbedBatchRequest(ctx, jsonBody)
		if err == nil {
			return embeddings, nil
		}

		lastErr = err
		if err == ErrContextCanceled || err == ErrModelNotFound {
			return nil, err
		}
	}

	return nil, lastErr
}

// doEmbedBatchRequest performs a single batch embedding HTTP request to /api/embed.
func (p *OllamaProvider) doEmbedBatchRequest(ctx context.Context, jsonBody []byte) ([][]float32, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", p.config.URL+"/api/embed", bytes.NewReader(jsonBody))
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
		// If /api/embed is not available (very old Ollama), fall back to
		// the legacy concurrent path.
		if resp.StatusCode == http.StatusNotFound {
			return nil, errBatchEndpointUnavailable
		}
		var errResp ollamaErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			if strings.Contains(errResp.Error, "model") && strings.Contains(errResp.Error, "not found") {
				return nil, ErrModelNotFound
			}
			if strings.Contains(errResp.Error, "not found") {
				return nil, errBatchEndpointUnavailable
			}
			return nil, fmt.Errorf("ollama error: %s", errResp.Error)
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var batchResp ollamaBatchEmbedResponse
	if err := json.Unmarshal(body, &batchResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(batchResp.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	// Convert and validate each embedding.
	results := make([][]float32, len(batchResp.Embeddings))
	for i, raw := range batchResp.Embeddings {
		if len(raw) == 0 {
			return nil, fmt.Errorf("empty embedding at index %d", i)
		}
		emb := float64sToFloat32s(raw)
		if len(emb) != p.config.Dimensions {
			return nil, fmt.Errorf("%w: expected %d, got %d", ErrDimensionMismatch, p.config.Dimensions, len(emb))
		}
		results[i] = emb
	}

	return results, nil
}

// EmbedBatch generates embeddings for multiple texts.
// It delegates to EmbedDocuments (native batch endpoint) and falls back to
// concurrent single requests if the batch endpoint is unavailable (very old Ollama).
func (p *OllamaProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Try the native batch endpoint first.
	embeddings, err := p.EmbedDocuments(ctx, texts)
	if err == nil {
		return embeddings, nil
	}

	// Fall back to concurrent single requests if the batch endpoint is unavailable.
	if errors.Is(err, errBatchEndpointUnavailable) {
		return p.embedBatchConcurrent(ctx, texts)
	}

	return embeddings, err
}

// errBatchEndpointUnavailable signals that /api/embed is not available on the
// Ollama server (very old versions) and the caller should fall back to the
// legacy concurrent single-request path.
var errBatchEndpointUnavailable = fmt.Errorf("batch embedding endpoint unavailable")

// embedBatchConcurrent is the legacy fallback: sends N concurrent single
// requests to /api/embeddings. Used only when /api/embed is unavailable.
func (p *OllamaProvider) embedBatchConcurrent(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	errors := make([]error, len(texts))

	batchSize := maxBatchSize
	if len(texts) < batchSize {
		batchSize = len(texts)
	}

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
