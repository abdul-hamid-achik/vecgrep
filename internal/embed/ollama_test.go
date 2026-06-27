package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOllamaProvider_EmbedDocuments(t *testing.T) {
	// Mock /api/embed server that returns one embedding per input text.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("expected /api/embed, got %s", r.URL.Path)
		}

		var req ollamaBatchEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if len(req.Input) != 3 {
			t.Errorf("expected 3 inputs, got %d", len(req.Input))
		}

		// Return one 3-dimensional embedding per input.
		embeddings := make([][]float64, len(req.Input))
		for i := range req.Input {
			embeddings[i] = []float64{float64(i), float64(i + 1), float64(i + 2)}
		}

		resp := ollamaBatchEmbedResponse{
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
		var req ollamaBatchEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		// Return one fewer embedding than requested.
		n := len(req.Input)
		if n > 0 {
			n--
		}
		embeddings := make([][]float64, n)
		for i := range embeddings {
			embeddings[i] = []float64{float64(i), float64(i + 1), float64(i + 2)}
		}
		resp := ollamaBatchEmbedResponse{Model: "nomic-embed-text", Embeddings: embeddings}
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
		var req ollamaBatchEmbedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode: %v", err)
		}

		callCount++

		embeddings := make([][]float64, len(req.Input))
		for i := range req.Input {
			embeddings[i] = []float64{0.1, 0.2, 0.3}
		}

		resp := ollamaBatchEmbedResponse{
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

		var req ollamaBatchEmbedRequest
		_ = json.NewDecoder(r.Body).Decode(&req)

		embeddings := make([][]float64, len(req.Input))
		for i := range req.Input {
			embeddings[i] = []float64{1.0, 2.0, 3.0}
		}

		resp := ollamaBatchEmbedResponse{
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

func TestOllamaProvider_EmbedBatchFallsBackOn404(t *testing.T) {
	// Simulate an old Ollama that doesn't have /api/embed (returns 404).
	// EmbedBatch should fall back to concurrent single requests.
	var mu sync.Mutex
	requestPaths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestPaths = append(requestPaths, r.URL.Path)
		mu.Unlock()

		if r.URL.Path == "/api/embed" {
			// Simulate old Ollama: /api/embed doesn't exist.
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Legacy /api/embeddings endpoint.
		if r.URL.Path == "/api/embeddings" {
			var req ollamaEmbeddingRequest
			_ = json.NewDecoder(r.Body).Decode(&req)

			resp := ollamaEmbeddingResponse{
				Embedding: []float64{1.0, 2.0, 3.0},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
			return
		}
	}))
	defer server.Close()

	provider := NewOllamaProvider(OllamaConfig{
		URL:           server.URL,
		Model:         "nomic-embed-text",
		Dimensions:    3,
		MaxRetries:    1,
		RetryInterval: 1, // fast retry for tests
	})

	results, err := provider.EmbedBatch(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Should have tried /api/embed once, then fell back to /api/embeddings.
	foundEmbed := false
	foundEmbeddings := false
	for _, p := range requestPaths {
		if p == "/api/embed" {
			foundEmbed = true
		}
		if p == "/api/embeddings" {
			foundEmbeddings = true
		}
	}
	if !foundEmbed {
		t.Error("expected /api/embed to be tried first")
	}
	if !foundEmbeddings {
		t.Error("expected fallback to /api/embeddings")
	}
}
