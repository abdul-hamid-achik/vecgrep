package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOllamaProvider_EmbedDocuments(t *testing.T) {
	// Mock /api/embed server that returns one embedding per input text.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected /api/embed, got %s", r.URL.Path)
		}

		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if len(req.Input) != 3 {
			t.Errorf("expected 3 inputs, got %d", len(req.Input))
		}

		// Return one 3-dimensional embedding per input.
		embeddings := make([][]float32, len(req.Input))
		for i := range req.Input {
			embeddings[i] = []float32{float32(i), float32(i + 1), float32(i + 2)}
		}

		resp := ollamaEmbedResponse{
			Model:      "nomic-embed-text",
			Embeddings: embeddings,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewOllamaProvider(OllamaConfig{
		URL:        server.URL,
		Model:      "nomic-embed-text",
		Dimensions: 3,
	})

	ctx := context.Background()
	texts := []string{"hello", "world", "test"}
	results, err := provider.EmbedDocuments(ctx, texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if len(r) != 3 {
			t.Fatalf("result[%d] has %d dims, want 3", i, len(r))
		}
		if r[0] != float32(i) {
			t.Errorf("result[%d][0] = %v, want %v", i, r[0], float32(i))
		}
	}
}

func TestOllamaProvider_EmbedDocumentsEmpty(t *testing.T) {
	provider := NewOllamaProvider(DefaultOllamaConfig())
	results, err := provider.EmbedDocuments(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results for empty input, got %v", results)
	}
}

func TestOllamaProvider_EmbedDocumentsEmptyText(t *testing.T) {
	provider := NewOllamaProvider(OllamaConfig{
		URL:        "http://localhost",
		Model:      "nomic-embed-text",
		Dimensions: 3,
	})
	_, err := provider.EmbedDocuments(context.Background(), []string{"ok", ""})
	if err == nil {
		t.Fatal("expected error for empty text in batch")
	}
}

func TestOllamaProvider_EmbedDocumentsCountMismatch(t *testing.T) {
	// The provider returns FEWER embeddings than inputs. The count-mismatch
	// guard must surface an error rather than silently dropping/zero-filling
	// the trailing chunks (which would otherwise be recorded as embedded).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		// Return one fewer embedding than requested.
		n := len(req.Input)
		if n > 0 {
			n--
		}
		embeddings := make([][]float32, n)
		for i := range embeddings {
			embeddings[i] = []float32{float32(i), float32(i + 1), float32(i + 2)}
		}
		resp := ollamaEmbedResponse{Model: "nomic-embed-text", Embeddings: embeddings}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewOllamaProvider(OllamaConfig{
		URL:        server.URL,
		Model:      "nomic-embed-text",
		Dimensions: 3,
	})

	_, err := provider.EmbedDocuments(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("expected an error when the provider returns fewer embeddings than inputs")
	}
	if !strings.Contains(err.Error(), "count mismatch") {
		t.Fatalf("expected a count-mismatch error, got: %v", err)
	}
}

func TestOllamaProvider_EmbedDocumentsSubBatching(t *testing.T) {
	// Generate more texts than defaultMaxBatchSize to test sub-batching.
	numTexts := defaultMaxBatchSize + 10
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		callCount++

		embeddings := make([][]float32, len(req.Input))
		for i := range req.Input {
			embeddings[i] = []float32{0.1, 0.2, 0.3}
		}

		resp := ollamaEmbedResponse{
			Model:      "nomic-embed-text",
			Embeddings: embeddings,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewOllamaProvider(OllamaConfig{
		URL:        server.URL,
		Model:      "nomic-embed-text",
		Dimensions: 3,
	})

	texts := make([]string, numTexts)
	for i := range texts {
		texts[i] = "text"
	}

	results, err := provider.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != numTexts {
		t.Fatalf("expected %d results, got %d", numTexts, len(results))
	}

	// Should have been split into 2 sub-batches: defaultMaxBatchSize + 10.
	if callCount != 2 {
		t.Errorf("expected 2 HTTP calls for sub-batching, got %d", callCount)
	}
}

func TestOllamaProvider_EmbedBatchDelegatesToDocuments(t *testing.T) {
	// EmbedBatch should delegate to EmbedDocuments (the /api/embed path).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected /api/embed, got %s", r.URL.Path)
		}

		var req ollamaEmbedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		embeddings := make([][]float32, len(req.Input))
		for i := range req.Input {
			embeddings[i] = []float32{1.0, 2.0, 3.0}
		}

		resp := ollamaEmbedResponse{
			Model:      "nomic-embed-text",
			Embeddings: embeddings,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewOllamaProvider(OllamaConfig{
		URL:        server.URL,
		Model:      "nomic-embed-text",
		Dimensions: 3,
	})

	results, err := provider.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestOllamaProvider_EmbedUsesCurrentEndpointAndOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Fatalf("request path = %q, want /api/embed", r.URL.Path)
		}
		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "qwen3-embedding:0.6b" {
			t.Errorf("model = %q, want exact configured tag", req.Model)
		}
		if req.Dimensions != 1024 {
			t.Errorf("dimensions = %d, want 1024", req.Dimensions)
		}
		if got := req.Options["num_ctx"]; got != float64(4096) {
			t.Errorf("options.num_ctx = %#v, want 4096", got)
		}
		if got := req.Options["num_batch"]; got != float64(128) {
			t.Errorf("options.num_batch = %#v, want 128", got)
		}
		if len(req.Input) != 1 || req.Input[0] != "query: needle" {
			t.Errorf("input = %#v, want templated query", req.Input)
		}
		embedding := make([]float32, 1024)
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{Embeddings: [][]float32{embedding}})
	}))
	defer server.Close()

	provider := NewOllamaProvider(OllamaConfig{
		URL:           server.URL,
		Model:         "qwen3-embedding:0.6b",
		Dimensions:    1024,
		Context:       4096,
		Options:       map[string]any{"num_ctx": 2048, "num_batch": 128},
		QueryTemplate: "query: {{text}}",
	})
	embedding, err := provider.Embed(context.Background(), "needle")
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(embedding) != 1024 {
		t.Fatalf("embedding dimensions = %d, want 1024", len(embedding))
	}
}

func TestOllamaProvider_EmbedDocumentsAppliesDocumentTemplate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req.Input) != 2 || req.Input[0] != "document: alpha" || req.Input[1] != "document: beta" {
			t.Errorf("input = %#v, want document templates", req.Input)
		}
		if req.Dimensions != 3 || req.Options["num_batch"] != float64(64) {
			t.Errorf("dimensions/options = %d/%#v, want 3/64", req.Dimensions, req.Options)
		}
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Embeddings: [][]float32{{1, 2, 3}, {4, 5, 6}},
		})
	}))
	defer server.Close()

	provider := NewOllamaProvider(OllamaConfig{
		URL:              server.URL,
		Dimensions:       3,
		Options:          map[string]any{"num_batch": 64},
		DocumentTemplate: "document: {{text}}",
	})
	if _, err := provider.EmbedDocuments(context.Background(), []string{"alpha", "beta"}); err != nil {
		t.Fatalf("EmbedDocuments() error = %v", err)
	}
}

func TestOllamaProvider_EmbedValidatesResponseDimensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ollamaEmbedResponse{
			Embeddings: [][]float32{{1, 2}},
		})
	}))
	defer server.Close()

	provider := NewOllamaProvider(OllamaConfig{URL: server.URL, Dimensions: 3, MaxRetries: 1})
	_, err := provider.Embed(context.Background(), "query")
	if err == nil || !strings.Contains(err.Error(), ErrDimensionMismatch.Error()) {
		t.Fatalf("Embed() error = %v, want dimension mismatch", err)
	}
}
