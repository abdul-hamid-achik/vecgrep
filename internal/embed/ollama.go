package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	defaultMaxBatchSize  = 64
	// defaultKeepAlive is used for single (interactive) embed requests.
	defaultKeepAlive = "5m"
	// defaultBatchKeepAlive is used for batch (indexing) embed requests.
	defaultBatchKeepAlive = "30m"
)

// OllamaConfig holds configuration for the Ollama embedding provider.
type OllamaConfig struct {
	URL           string
	Model         string
	Dimensions    int
	Timeout       time.Duration
	MaxRetries    int
	RetryInterval time.Duration
	// MaxBatchSize is the maximum number of texts sent in a single /api/embed
	// request (default 64). Larger batches are split into sub-batches of
	// this size to keep HTTP request bodies reasonable.
	MaxBatchSize int
	// KeepAlive controls how long Ollama keeps the model loaded in memory
	// after a request. If empty, sensible defaults are applied: "5m" for
	// single (interactive) embeds and "30m" for batch (indexing) embeds.
	KeepAlive string
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
		MaxBatchSize:  defaultMaxBatchSize,
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
	Model     string `json:"model"`
	Prompt    string `json:"prompt"`
	KeepAlive string `json:"keep_alive,omitempty"`
}

// ollamaEmbeddingResponse is the response from Ollama's legacy /api/embeddings
// endpoint.
type ollamaEmbeddingResponse struct {
	Embedding    []float64 `json:"embedding"`
	LoadDuration int64     `json:"load_duration,omitempty"`
}

// ollamaBatchEmbedRequest is the request body for Ollama's /api/embed endpoint
// which accepts a string or a list of strings as input.
type ollamaBatchEmbedRequest struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	Truncate  bool     `json:"truncate"`
	KeepAlive string   `json:"keep_alive,omitempty"`
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
	if cfg.MaxBatchSize <= 0 {
		cfg.MaxBatchSize = defaultMaxBatchSize
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
	// Apply the configured keep_alive, defaulting to the interactive value
	// for single embeds so the model stays warm for follow-up queries.
	reqBody.KeepAlive = p.config.KeepAlive
	if reqBody.KeepAlive == "" {
		reqBody.KeepAlive = defaultKeepAlive
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
// EmbedBatch because it sends a single HTTP request for up to MaxBatchSize
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
	maxBatch := p.config.MaxBatchSize
	if maxBatch <= 0 {
		maxBatch = defaultMaxBatchSize
	}
	for start := 0; start < len(nonEmpty); start += maxBatch {
		end := start + maxBatch
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
	// Apply the configured keep_alive, defaulting to the longer indexing
	// value for batch embeds so the model stays loaded across sub-batches.
	reqBody.KeepAlive = p.config.KeepAlive
	if reqBody.KeepAlive == "" {
		reqBody.KeepAlive = defaultBatchKeepAlive
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

		embeddings, err := p.doEmbedBatchRequest(ctx, jsonBody, texts)
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
// The input texts are passed alongside the pre-marshaled body so the response
// can be checked for truncation: the expected token count is estimated from the
// input texts and compared against prompt_eval_count reported by Ollama.
func (p *OllamaProvider) doEmbedBatchRequest(ctx context.Context, jsonBody []byte, texts []string) ([][]float32, error) {
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

	// A short response would otherwise leave trailing input texts with no
	// embedding, which the indexer cannot distinguish from a successful empty
	// slot — error instead of silently dropping those chunks.
	if len(batchResp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embedding count mismatch: got %d for %d inputs", len(batchResp.Embeddings), len(texts))
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

	// Truncation detection: Ollama truncates input that exceeds the model's
	// context window when Truncate is true. prompt_eval_count reports how
	// many tokens were actually processed. If that is significantly less
	// than our rough estimate (len(text)/4 chars per token), warn so users
	// know embeddings may be incomplete.
	if batchResp.PromptEvalCount > 0 && len(texts) > 0 {
		estimated := 0
		for _, t := range texts {
			estimated += len(t) / 4 // rough chars-to-tokens ratio
		}
		actual := batchResp.PromptEvalCount
		if actual < int(float64(estimated)*0.9) {
			slog.Warn("embedding truncation detected",
				"input_texts", len(texts),
				"expected_tokens", estimated,
				"actual_tokens", actual,
			)
		}
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

	batchSize := p.config.MaxBatchSize
	if batchSize <= 0 {
		batchSize = defaultMaxBatchSize
	}
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

// Warmup preloads the embedding model into Ollama's memory by sending a
// throwaway single-text embed request with a long keep_alive. This avoids
// the first-request latency penalty when a batch indexing run starts. The
// load_duration from the response (nanoseconds Ollama spent loading the
// model) is returned so callers can log it.
func (p *OllamaProvider) Warmup(ctx context.Context) (time.Duration, error) {
	reqBody := ollamaEmbeddingRequest{
		Model:     p.config.Model,
		Prompt:    "warmup",
		KeepAlive: defaultBatchKeepAlive,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("marshal warmup request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.config.URL+"/api/embeddings", bytes.NewReader(jsonBody))
	if err != nil {
		return 0, fmt.Errorf("create warmup request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ErrContextCanceled
		}
		return 0, fmt.Errorf("warmup request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read warmup response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp ollamaErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return 0, fmt.Errorf("warmup ollama error: %s", errResp.Error)
		}
		return 0, fmt.Errorf("warmup unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var embResp ollamaEmbeddingResponse
	if err := json.Unmarshal(body, &embResp); err != nil {
		return 0, fmt.Errorf("unmarshal warmup response: %w", err)
	}

	return time.Duration(embResp.LoadDuration), nil
}
