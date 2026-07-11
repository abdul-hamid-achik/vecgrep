package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
	// KeepAlive controls how long Ollama keeps the model loaded in memory.
	KeepAlive string
	// Context sets Ollama's num_ctx option when positive.
	Context int
	// Options are passed to Ollama's /api/embed options object. Context, when
	// set, takes precedence over an options entry named num_ctx.
	Options map[string]any
	// QueryTemplate and DocumentTemplate preprocess their respective inputs.
	// "{{text}}" is replaced with the input; when absent, the input is appended.
	QueryTemplate    string
	DocumentTemplate string
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

// ollamaEmbedRequest is the request body for Ollama's /api/embed endpoint.
// Input is always a list so query and document embedding share one wire path.
type ollamaEmbedRequest struct {
	Model      string         `json:"model"`
	Input      []string       `json:"input"`
	Truncate   bool           `json:"truncate"`
	Dimensions int            `json:"dimensions,omitempty"`
	KeepAlive  string         `json:"keep_alive,omitempty"`
	Options    map[string]any `json:"options,omitempty"`
}

// ollamaEmbedResponse is the response from Ollama's /api/embed endpoint.
type ollamaEmbedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float32 `json:"embeddings"`
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
	embedding, err := p.doEmbed(ctx, text)
	if err != nil {
		return nil, NewProviderError("ollama", "embed", err)
	}
	return embedding, nil
}

// doEmbed performs a single embedding request.
func (p *OllamaProvider) doEmbed(ctx context.Context, text string) ([]float32, error) {
	embeddings, err := p.doEmbedBatch(ctx, []string{p.applyTemplate(p.config.QueryTemplate, text)}, defaultKeepAlive)
	if err != nil {
		return nil, err
	}
	return embeddings[0], nil
}

// EmbedDocuments generates embeddings for multiple texts using Ollama's
// native batch endpoint (/api/embed). This is far more efficient than
// EmbedBatch because it sends a single HTTP request for up to MaxBatchSize
// texts instead of one request per text.
func (p *OllamaProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	for i, text := range texts {
		if text == "" {
			return nil, NewProviderError("ollama", "embedDocuments", fmt.Errorf("text %d: %w", i, ErrEmptyText))
		}
	}

	results := make([][]float32, len(texts))
	maxBatch := p.config.MaxBatchSize
	if maxBatch <= 0 {
		maxBatch = defaultMaxBatchSize
	}
	for start := 0; start < len(texts); start += maxBatch {
		end := min(start+maxBatch, len(texts))
		input := texts[start:end]
		if p.config.DocumentTemplate != "" {
			input = make([]string, end-start)
			for i, text := range texts[start:end] {
				input[i] = p.applyTemplate(p.config.DocumentTemplate, text)
			}
		}

		embeddings, err := p.doEmbedBatch(ctx, input, defaultBatchKeepAlive)
		if err != nil {
			return results, NewProviderError("ollama", "embedDocuments", err)
		}
		copy(results[start:end], embeddings)
	}
	return results, nil
}

// doEmbedBatch sends a single HTTP request to /api/embed with multiple texts
// and returns the embeddings in the same order.
func (p *OllamaProvider) doEmbedBatch(ctx context.Context, texts []string, defaultRequestKeepAlive string) ([][]float32, error) {
	reqBody := ollamaEmbedRequest{
		Model:      p.config.Model,
		Input:      texts,
		Truncate:   true,
		Dimensions: p.config.Dimensions,
		KeepAlive:  p.config.KeepAlive,
		Options:    p.requestOptions(),
	}
	if reqBody.KeepAlive == "" {
		reqBody.KeepAlive = defaultRequestKeepAlive
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

		embeddings, err := p.doEmbedRequest(ctx, jsonBody, texts)
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
func (p *OllamaProvider) doEmbedRequest(ctx context.Context, jsonBody []byte, texts []string) ([][]float32, error) {
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
		var errResp ollamaErrorResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			if strings.Contains(errResp.Error, "model") && strings.Contains(errResp.Error, "not found") {
				return nil, ErrModelNotFound
			}
			return nil, fmt.Errorf("ollama error: %s", errResp.Error)
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp ollamaEmbedResponse
	if err := json.Unmarshal(body, &embedResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(embedResp.Embeddings) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}
	if len(embedResp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embedding count mismatch: got %d for %d inputs", len(embedResp.Embeddings), len(texts))
	}
	for i, embedding := range embedResp.Embeddings {
		if len(embedding) == 0 {
			return nil, fmt.Errorf("empty embedding at index %d", i)
		}
		if len(embedding) != p.config.Dimensions {
			return nil, fmt.Errorf("%w: expected %d, got %d", ErrDimensionMismatch, p.config.Dimensions, len(embedding))
		}
	}

	if embedResp.PromptEvalCount > 0 && len(texts) > 0 {
		estimated := 0
		for _, text := range texts {
			estimated += len(text) / 4
		}
		actual := embedResp.PromptEvalCount
		if actual < int(float64(estimated)*0.9) {
			slog.Warn("embedding truncation detected",
				"input_texts", len(texts),
				"expected_tokens", estimated,
				"actual_tokens", actual,
			)
		}
	}
	return embedResp.Embeddings, nil
}

func (p *OllamaProvider) applyTemplate(template, text string) string {
	if template == "" {
		return text
	}
	if strings.Contains(template, "{{text}}") {
		return strings.ReplaceAll(template, "{{text}}", text)
	}
	return template + text
}

func (p *OllamaProvider) requestOptions() map[string]any {
	if len(p.config.Options) == 0 && p.config.Context <= 0 {
		return nil
	}
	options := make(map[string]any, len(p.config.Options)+1)
	for key, value := range p.config.Options {
		options[key] = value
	}
	if p.config.Context > 0 {
		options["num_ctx"] = p.config.Context
	}
	return options
}

// EmbedBatch generates embeddings for multiple texts through /api/embed.
func (p *OllamaProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return p.EmbedDocuments(ctx, texts)
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
	reqBody := ollamaEmbedRequest{
		Model:      p.config.Model,
		Input:      []string{"warmup"},
		Truncate:   true,
		Dimensions: p.config.Dimensions,
		KeepAlive:  defaultBatchKeepAlive,
		Options:    p.requestOptions(),
	}
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("marshal warmup request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", p.config.URL+"/api/embed", bytes.NewReader(jsonBody))
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

	var embedResp ollamaEmbedResponse
	if err := json.Unmarshal(body, &embedResp); err != nil {
		return 0, fmt.Errorf("unmarshal warmup response: %w", err)
	}
	return time.Duration(embedResp.LoadDuration), nil
}
