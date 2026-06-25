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
	defaultVoyageURL        = "https://api.voyageai.com/v1"
	defaultVoyageModel      = "voyage-code-3"
	defaultVoyageDims       = 1024
	defaultVoyageTimeout    = 60 * time.Second
	defaultVoyageMaxRetries = 3
	defaultVoyageRetryDelay = 1 * time.Second
	voyageMaxBatchSize      = 128
	voyageInputDocument     = "document"
	voyageInputQuery        = "query"
)

// VoyageConfig holds configuration for the Voyage embedding provider.
type VoyageConfig struct {
	APIKey        string
	Model         string
	Dimensions    int
	BaseURL       string
	Timeout       time.Duration
	MaxRetries    int
	RetryInterval time.Duration
}

func DefaultVoyageConfig() VoyageConfig {
	apiKey := os.Getenv("VOYAGE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("VECGREP_VOYAGE_API_KEY")
	}

	baseURL := os.Getenv("VOYAGE_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("VECGREP_VOYAGE_BASE_URL")
	}
	if baseURL == "" {
		baseURL = defaultVoyageURL
	}

	return VoyageConfig{
		APIKey:        apiKey,
		Model:         defaultVoyageModel,
		Dimensions:    defaultVoyageDims,
		BaseURL:       baseURL,
		Timeout:       defaultVoyageTimeout,
		MaxRetries:    defaultVoyageMaxRetries,
		RetryInterval: defaultVoyageRetryDelay,
	}
}

// VoyageProvider implements Provider using Voyage AI embeddings.
type VoyageProvider struct {
	config VoyageConfig
	client *http.Client
}

type voyageEmbeddingRequest struct {
	Model           string   `json:"model"`
	Input           []string `json:"input"`
	InputType       string   `json:"input_type,omitempty"`
	OutputDimension int      `json:"output_dimension,omitempty"`
	OutputDType     string   `json:"output_dtype,omitempty"`
	Truncation      bool     `json:"truncation"`
}

type voyageEmbeddingResponse struct {
	Object string `json:"object"`
	Data   []struct {
		Object    string    `json:"object"`
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

type voyageErrorResponse struct {
	Message string `json:"message"`
	Detail  string `json:"detail"`
	Error   any    `json:"error"`
}

func NewVoyageProvider(cfg VoyageConfig) *VoyageProvider {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("VOYAGE_API_KEY")
		if cfg.APIKey == "" {
			cfg.APIKey = os.Getenv("VECGREP_VOYAGE_API_KEY")
		}
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("VOYAGE_BASE_URL")
		if cfg.BaseURL == "" {
			cfg.BaseURL = os.Getenv("VECGREP_VOYAGE_BASE_URL")
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = defaultVoyageURL
		}
	}
	if cfg.Model == "" {
		cfg.Model = defaultVoyageModel
	}
	if cfg.Dimensions == 0 {
		cfg.Dimensions = GetModelDimensions(cfg.Model)
		if cfg.Dimensions == 0 {
			cfg.Dimensions = defaultVoyageDims
		}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultVoyageTimeout
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultVoyageMaxRetries
	}
	if cfg.RetryInterval == 0 {
		cfg.RetryInterval = defaultVoyageRetryDelay
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")

	return &VoyageProvider{
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

func (p *VoyageProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	return p.EmbedQuery(ctx, text)
}

func (p *VoyageProvider) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if text == "" {
		return nil, ErrEmptyText
	}
	embeddings, err := p.embedBatchInternal(ctx, []string{text}, voyageInputQuery)
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return embeddings[0], nil
}

func (p *VoyageProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	return p.embedBatchByInputType(ctx, texts, voyageInputQuery)
}

func (p *VoyageProvider) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return p.embedBatchByInputType(ctx, texts, voyageInputDocument)
}

func (p *VoyageProvider) embedBatchByInputType(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	for i, text := range texts {
		if text == "" {
			return nil, NewProviderError("voyage", "embedBatch", fmt.Errorf("text %d: %w", i, ErrEmptyText))
		}
	}
	if len(texts) <= voyageMaxBatchSize {
		return p.embedBatchInternal(ctx, texts, inputType)
	}

	results := make([][]float32, len(texts))
	for i := 0; i < len(texts); i += voyageMaxBatchSize {
		end := i + voyageMaxBatchSize
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

func (p *VoyageProvider) embedBatchInternal(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	if p.config.APIKey == "" {
		return nil, NewProviderError("voyage", "embed", fmt.Errorf("API key not configured"))
	}

	var lastErr error
	for attempt := 0; attempt < p.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, NewProviderError("voyage", "embed", ErrContextCanceled)
			case <-time.After(p.config.RetryInterval * time.Duration(1<<uint(attempt-1))):
			}
		}

		embeddings, err := p.doEmbedBatch(ctx, texts, inputType)
		if err == nil {
			return embeddings, nil
		}
		lastErr = err
		if err == ErrContextCanceled {
			return nil, NewProviderError("voyage", "embed", err)
		}
		if errors.Is(err, ErrDimensionMismatch) {
			return nil, NewProviderError("voyage", "embed", err)
		}
		if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate_limit") {
			continue
		}
		if strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "invalid_api_key") {
			return nil, NewProviderError("voyage", "embed", err)
		}
	}
	return nil, NewProviderError("voyage", "embed", lastErr)
}

func (p *VoyageProvider) doEmbedBatch(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	reqBody := voyageEmbeddingRequest{
		Model:           p.config.Model,
		Input:           texts,
		InputType:       inputType,
		OutputDimension: p.config.Dimensions,
		OutputDType:     "float",
		Truncation:      true,
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
		message := voyageErrorMessage(body)
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("rate_limit: %s", message)
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("invalid_api_key: %s", message)
		}
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, message)
	}

	var embResp voyageEmbeddingResponse
	if err := json.Unmarshal(body, &embResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("no embeddings returned")
	}

	embeddings := make([][]float32, len(texts))
	for _, data := range embResp.Data {
		if data.Index < 0 || data.Index >= len(texts) {
			return nil, fmt.Errorf("invalid embedding index: %d", data.Index)
		}
		embedding := float64sToFloat32s(data.Embedding)
		if err := validateEmbeddingDimensions("voyage", embedding, p.config.Dimensions); err != nil {
			return nil, err
		}
		embeddings[data.Index] = embedding
	}
	for i, embedding := range embeddings {
		if embedding == nil {
			return nil, fmt.Errorf("missing embedding for index %d", i)
		}
	}
	return embeddings, nil
}

func (p *VoyageProvider) Model() string {
	return p.config.Model
}

func (p *VoyageProvider) Dimensions() int {
	return p.config.Dimensions
}

func (p *VoyageProvider) Ping(ctx context.Context) error {
	if p.config.APIKey == "" {
		return NewProviderError("voyage", "ping", fmt.Errorf("API key not configured"))
	}
	if _, err := p.EmbedQuery(ctx, "test"); err != nil {
		return NewProviderError("voyage", "ping", err)
	}
	return nil
}

// Warmup is a no-op for the Voyage provider. Cloud-hosted models are
// always loaded, so there is no cold-start penalty to avoid.
func (p *VoyageProvider) Warmup(ctx context.Context) (time.Duration, error) {
	return 0, nil
}

func voyageErrorMessage(body []byte) string {
	var errResp voyageErrorResponse
	if json.Unmarshal(body, &errResp) == nil {
		if errResp.Message != "" {
			return errResp.Message
		}
		if errResp.Detail != "" {
			return errResp.Detail
		}
		if errResp.Error != nil {
			if errText, ok := errResp.Error.(string); ok && errText != "" {
				return errText
			}
			if encoded, err := json.Marshal(errResp.Error); err == nil {
				return string(encoded)
			}
		}
	}
	return string(body)
}
