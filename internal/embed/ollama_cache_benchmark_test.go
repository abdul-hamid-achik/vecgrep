package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

type recordingOllamaServer struct {
	server     *httptest.Server
	mu         sync.Mutex
	batches    []int
	dimensions int
}

func newRecordingOllamaServer(tb testing.TB, dimensions int) *recordingOllamaServer {
	tb.Helper()
	recorder := &recordingOllamaServer{dimensions: dimensions}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
			return
		}
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		recorder.mu.Lock()
		recorder.batches = append(recorder.batches, len(request.Input))
		recorder.mu.Unlock()

		embeddings := make([][]float32, len(request.Input))
		for i, text := range request.Input {
			embeddings[i] = make([]float32, dimensions)
			embeddings[i][0] = float32(len(text))
		}
		w.Header().Set("Content-Type", "application/json")
		response := struct {
			Embeddings [][]float32 `json:"embeddings"`
		}{Embeddings: embeddings}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			tb.Errorf("encode response: %v", err)
		}
	}))
	tb.Cleanup(recorder.server.Close)
	return recorder
}

func (s *recordingOllamaServer) batchSizes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.batches...)
}

func TestOllamaBatchBoundaryAndCacheWritePressure(t *testing.T) {
	server := newRecordingOllamaServer(t, 8)
	provider := NewOllamaProvider(OllamaConfig{
		URL: server.server.URL, Model: "benchmark", Dimensions: 8,
		MaxBatchSize: 4, MaxRetries: 1,
	})
	texts := make([]string, 10)
	for i := range texts {
		texts[i] = "input-" + strconv.Itoa(i)
	}
	vectors, err := provider.EmbedDocuments(context.Background(), texts)
	if err != nil {
		t.Fatalf("embed documents: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("got %d vectors, want %d", len(vectors), len(texts))
	}
	gotBatches := server.batchSizes()
	wantBatches := []int{4, 4, 2}
	if fmt.Sprint(gotBatches) != fmt.Sprint(wantBatches) {
		t.Fatalf("request batch sizes = %v, want %v", gotBatches, wantBatches)
	}

	cache, err := NewPersistentCache(64, filepath.Join(t.TempDir(), "embeddings.db"))
	if err != nil {
		t.Fatalf("open persistent cache: %v", err)
	}
	defer cache.Close()
	cache.SetModel("benchmark")
	for i, text := range texts {
		cache.Set(text, vectors[i])
	}
	if err := cache.FlushToDisk(); err != nil {
		t.Fatalf("flush cache: %v", err)
	}
	cache.Clear()
	for _, text := range texts {
		if _, ok := cache.Get(text); !ok {
			t.Fatalf("cache miss after flush for %q", text)
		}
	}
}

func BenchmarkOllamaResponseConversion(b *testing.B) {
	for _, dimensions := range []int{8, 768, 1536} {
		b.Run(fmt.Sprintf("dimensions=%d", dimensions), func(b *testing.B) {
			server := newRecordingOllamaServer(b, dimensions)
			provider := NewOllamaProvider(OllamaConfig{
				URL: server.server.URL, Model: "benchmark", Dimensions: dimensions,
				MaxBatchSize: 1, MaxRetries: 1,
			})
			b.ReportAllocs()
			b.ReportMetric(float64(dimensions), "dimensions")
			b.ResetTimer()
			for range b.N {
				embedding, err := provider.Embed(context.Background(), "conversion benchmark")
				if err != nil {
					b.Fatal(err)
				}
				if len(embedding) != dimensions {
					b.Fatalf("converted %d dimensions, want %d", len(embedding), dimensions)
				}
			}
		})
	}
}

func BenchmarkOllamaRequestBatching(b *testing.B) {
	for _, size := range []int{1, 64, 129} {
		b.Run(fmt.Sprintf("texts=%d", size), func(b *testing.B) {
			server := newRecordingOllamaServer(b, 8)
			provider := NewOllamaProvider(OllamaConfig{
				URL: server.server.URL, Model: "benchmark", Dimensions: 8,
				MaxBatchSize: 64, MaxRetries: 1,
			})
			texts := make([]string, size)
			for i := range texts {
				texts[i] = "input-" + strconv.Itoa(i)
			}
			requestsPerOp := (size + 63) / 64
			b.ReportAllocs()
			b.ReportMetric(float64(requestsPerOp), "requests/op")
			b.ReportMetric(float64(size), "vectors/op")
			b.ResetTimer()
			for range b.N {
				vectors, err := provider.EmbedDocuments(context.Background(), texts)
				if err != nil {
					b.Fatal(err)
				}
				if len(vectors) != size {
					b.Fatalf("got %d vectors, want %d", len(vectors), size)
				}
			}
			if got := len(server.batchSizes()); got != requestsPerOp*b.N {
				b.Fatalf("made %d requests, want %d", got, requestsPerOp*b.N)
			}
		})
	}
}

func BenchmarkPersistentCacheAllMissWritePressure(b *testing.B) {
	cache, err := NewPersistentCache(b.N+1, filepath.Join(b.TempDir(), "embeddings.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer cache.Close()
	cache.SetModel("benchmark")

	texts := make([]string, b.N)
	for i := range texts {
		texts[i] = "unique-cache-miss-" + strconv.Itoa(i)
	}
	vector := make([]float32, 768)
	b.ReportAllocs()
	b.ReportMetric(float64(len(vector)*4), "vector-bytes/op")
	b.ResetTimer()
	for _, text := range texts {
		cache.Set(text, vector)
	}
	if err := cache.FlushToDisk(); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()

	cache.Clear()
	for _, text := range texts {
		if _, ok := cache.Get(text); !ok {
			b.Fatalf("cache miss after write pressure for %q", text)
		}
	}
}
