package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCohereProvider_EmbedDocumentsUsesSearchDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/embed" {
			t.Fatalf("path = %s, want /embed", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want Bearer test-key", got)
		}

		var req cohereEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "embed-v4.0" {
			t.Fatalf("model = %q, want embed-v4.0", req.Model)
		}
		if req.InputType != cohereInputDocument {
			t.Fatalf("input_type = %q, want %q", req.InputType, cohereInputDocument)
		}
		if req.OutputDimension != 3 {
			t.Fatalf("output_dimension = %d, want 3", req.OutputDimension)
		}
		if !reflect.DeepEqual(req.EmbeddingTypes, []string{"float"}) {
			t.Fatalf("embedding_types = %v, want [float]", req.EmbeddingTypes)
		}
		if !reflect.DeepEqual(req.Texts, []string{"first", "second"}) {
			t.Fatalf("texts = %v, want [first second]", req.Texts)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "response-id",
			"embeddings": map[string]any{
				"float": [][]float64{
					{0.1, 0.2, 0.3},
					{0.4, 0.5, 0.6},
				},
			},
		})
	}))
	defer server.Close()

	provider := NewCohereProvider(CohereConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "embed-v4.0",
		Dimensions: 3,
		MaxRetries: 1,
	})

	embeddings, err := provider.EmbedDocuments(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatalf("EmbedDocuments failed: %v", err)
	}
	if len(embeddings) != 2 {
		t.Fatalf("len(embeddings) = %d, want 2", len(embeddings))
	}
	if len(embeddings[0]) != 3 || len(embeddings[1]) != 3 {
		t.Fatalf("embedding dimensions = %d/%d, want 3/3", len(embeddings[0]), len(embeddings[1]))
	}
}

func TestCohereProvider_EmbedQueryUsesSearchQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req cohereEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.InputType != cohereInputQuery {
			t.Fatalf("input_type = %q, want %q", req.InputType, cohereInputQuery)
		}
		if !reflect.DeepEqual(req.Texts, []string{"find error handling"}) {
			t.Fatalf("texts = %v, want query text", req.Texts)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": map[string]any{
				"float": [][]float64{{0.1, 0.2, 0.3}},
			},
		})
	}))
	defer server.Close()

	provider := NewCohereProvider(CohereConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "embed-v4.0",
		Dimensions: 3,
		MaxRetries: 1,
	})

	embedding, err := provider.EmbedQuery(context.Background(), "find error handling")
	if err != nil {
		t.Fatalf("EmbedQuery failed: %v", err)
	}
	if len(embedding) != 3 {
		t.Fatalf("len(embedding) = %d, want 3", len(embedding))
	}
}

func TestCohereProvider_EmbedQueryRejectsEmptyText(t *testing.T) {
	provider := NewCohereProvider(CohereConfig{APIKey: "test-key"})
	if _, err := provider.EmbedQuery(context.Background(), ""); err != ErrEmptyText {
		t.Fatalf("err = %v, want ErrEmptyText", err)
	}
}
