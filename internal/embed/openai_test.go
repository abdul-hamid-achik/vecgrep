package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewOpenAIProvider(t *testing.T) {
	cfg := OpenAIConfig{
		APIKey: "test-key",
	}

	provider := NewOpenAIProvider(cfg)

	if provider.config.APIKey != "test-key" {
		t.Errorf("expected API key 'test-key', got %s", provider.config.APIKey)
	}
	if provider.config.Model != defaultOpenAIModel {
		t.Errorf("expected model %s, got %s", defaultOpenAIModel, provider.config.Model)
	}
	if provider.config.BaseURL != defaultOpenAIURL {
		t.Errorf("expected base URL %s, got %s", defaultOpenAIURL, provider.config.BaseURL)
	}
}

func TestOpenAIProvider_Embed(t *testing.T) {
	// Create a mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Errorf("expected /embeddings, got %s", r.URL.Path)
		}

		// Check authorization header
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("expected 'Bearer test-key', got %s", auth)
		}

		// Return mock response
		resp := openaiEmbeddingResponse{
			Object: "list",
			Data: []struct {
				Object    string    `json:"object"`
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			}{
				{
					Object:    "embedding",
					Index:     0,
					Embedding: make([]float64, 1536),
				},
			},
			Model: "text-embedding-3-small",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewOpenAIProvider(OpenAIConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Dimensions: 1536,
	})

	ctx := context.Background()
	embedding, err := provider.Embed(ctx, "test text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(embedding) != 1536 {
		t.Errorf("expected 1536 dimensions, got %d", len(embedding))
	}
}

func TestOpenAIProvider_EmbedEmpty(t *testing.T) {
	provider := NewOpenAIProvider(OpenAIConfig{
		APIKey: "test-key",
	})

	ctx := context.Background()
	_, err := provider.Embed(ctx, "")
	if err != ErrEmptyText {
		t.Errorf("expected ErrEmptyText, got %v", err)
	}
}

func TestOpenAIProvider_EmbedBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Decode request to check input
		var req openaiEmbeddingRequest
		json.NewDecoder(r.Body).Decode(&req)

		// Get input texts
		inputs, ok := req.Input.([]interface{})
		if !ok {
			t.Fatal("expected array input")
		}

		// Return mock response with embeddings for each input
		resp := openaiEmbeddingResponse{
			Object: "list",
			Data: make([]struct {
				Object    string    `json:"object"`
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			}, len(inputs)),
			Model: "text-embedding-3-small",
		}

		for i := range inputs {
			resp.Data[i] = struct {
				Object    string    `json:"object"`
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			}{
				Object:    "embedding",
				Index:     i,
				Embedding: make([]float64, 1536),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewOpenAIProvider(OpenAIConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Dimensions: 1536,
	})

	ctx := context.Background()
	texts := []string{"text 1", "text 2", "text 3"}
	embeddings, err := provider.EmbedBatch(ctx, texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(embeddings) != len(texts) {
		t.Errorf("expected %d embeddings, got %d", len(texts), len(embeddings))
	}

	for i, emb := range embeddings {
		if len(emb) != 1536 {
			t.Errorf("embedding %d: expected 1536 dimensions, got %d", i, len(emb))
		}
	}
}

func TestOpenAIProvider_RateLimitRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			// Return rate limit error on first attempt
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(openaiErrorResponse{
				Error: struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Param   string `json:"param"`
					Code    string `json:"code"`
				}{
					Message: "Rate limit exceeded",
					Type:    "rate_limit_error",
				},
			})
			return
		}

		// Return success on second attempt
		resp := openaiEmbeddingResponse{
			Object: "list",
			Data: []struct {
				Object    string    `json:"object"`
				Index     int       `json:"index"`
				Embedding []float64 `json:"embedding"`
			}{
				{
					Object:    "embedding",
					Index:     0,
					Embedding: make([]float64, 1536),
				},
			},
			Model: "text-embedding-3-small",
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewOpenAIProvider(OpenAIConfig{
		APIKey:        "test-key",
		BaseURL:       server.URL,
		Dimensions:    1536,
		RetryInterval: 10 * time.Millisecond, // Fast retry for tests
	})

	ctx := context.Background()
	embedding, err := provider.Embed(ctx, "test text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}

	if len(embedding) != 1536 {
		t.Errorf("expected 1536 dimensions, got %d", len(embedding))
	}
}

func TestOpenAIProvider_AuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(openaiErrorResponse{
			Error: struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Param   string `json:"param"`
				Code    string `json:"code"`
			}{
				Message: "Invalid API key",
				Type:    "invalid_request_error",
				Code:    "invalid_api_key",
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAIProvider(OpenAIConfig{
		APIKey:  "bad-key",
		BaseURL: server.URL,
	})

	ctx := context.Background()
	_, err := provider.Embed(ctx, "test text")
	if err == nil {
		t.Fatal("expected error for invalid API key")
	}
}

func TestOpenAIProvider_Model(t *testing.T) {
	provider := NewOpenAIProvider(OpenAIConfig{
		APIKey: "test-key",
		Model:  "text-embedding-3-large",
	})

	if provider.Model() != "text-embedding-3-large" {
		t.Errorf("expected 'text-embedding-3-large', got %s", provider.Model())
	}
}

func TestOpenAIProvider_Dimensions(t *testing.T) {
	provider := NewOpenAIProvider(OpenAIConfig{
		APIKey:     "test-key",
		Model:      "text-embedding-3-large",
		Dimensions: 3072,
	})

	if provider.Dimensions() != 3072 {
		t.Errorf("expected 3072, got %d", provider.Dimensions())
	}
}

func TestDefaultOpenAIConfig(t *testing.T) {
	cfg := DefaultOpenAIConfig()

	if cfg.Model != defaultOpenAIModel {
		t.Errorf("expected model %s, got %s", defaultOpenAIModel, cfg.Model)
	}
	if cfg.Dimensions != defaultOpenAIDims {
		t.Errorf("expected dimensions %d, got %d", defaultOpenAIDims, cfg.Dimensions)
	}
	if cfg.Timeout != defaultOpenAITimeout {
		t.Errorf("expected timeout %s, got %s", defaultOpenAITimeout, cfg.Timeout)
	}
}
