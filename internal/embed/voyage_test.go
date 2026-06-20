package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestVoyageProvider_EmbedDocumentsUsesDocumentInputType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/embeddings" {
			t.Fatalf("path = %s, want /embeddings", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("Authorization = %q, want Bearer test-key", got)
		}

		var req voyageEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "voyage-code-3" {
			t.Fatalf("model = %q, want voyage-code-3", req.Model)
		}
		if req.InputType != voyageInputDocument {
			t.Fatalf("input_type = %q, want %q", req.InputType, voyageInputDocument)
		}
		if req.OutputDimension != 3 {
			t.Fatalf("output_dimension = %d, want 3", req.OutputDimension)
		}
		if req.OutputDType != "float" {
			t.Fatalf("output_dtype = %q, want float", req.OutputDType)
		}
		if !req.Truncation {
			t.Fatal("truncation = false, want true")
		}
		if !reflect.DeepEqual(req.Input, []string{"first", "second"}) {
			t.Fatalf("input = %v, want [first second]", req.Input)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}},
				{"object": "embedding", "index": 1, "embedding": []float64{0.4, 0.5, 0.6}},
			},
			"model": "voyage-code-3",
		})
	}))
	defer server.Close()

	provider := NewVoyageProvider(VoyageConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "voyage-code-3",
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

func TestVoyageProvider_EmbedQueryUsesQueryInputType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req voyageEmbeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.InputType != voyageInputQuery {
			t.Fatalf("input_type = %q, want %q", req.InputType, voyageInputQuery)
		}
		if !reflect.DeepEqual(req.Input, []string{"find error handling"}) {
			t.Fatalf("input = %v, want query text", req.Input)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}},
			},
			"model": "voyage-code-3",
		})
	}))
	defer server.Close()

	provider := NewVoyageProvider(VoyageConfig{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "voyage-code-3",
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

func TestVoyageProvider_EmbedQueryRejectsEmptyText(t *testing.T) {
	provider := NewVoyageProvider(VoyageConfig{APIKey: "test-key"})
	if _, err := provider.EmbedQuery(context.Background(), ""); err != ErrEmptyText {
		t.Fatalf("err = %v, want ErrEmptyText", err)
	}
}
