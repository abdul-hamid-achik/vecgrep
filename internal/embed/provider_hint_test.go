package embed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCloudProvidersReportAPIKeySentinel(t *testing.T) {
	ctx := context.Background()
	openai := NewOpenAIProvider(OpenAIConfig{Model: "text-embedding-3-small", Dimensions: 1536})
	cohere := NewCohereProvider(CohereConfig{Model: "embed-v4.0", Dimensions: 1536})
	voyage := NewVoyageProvider(VoyageConfig{Model: "voyage-code-3", Dimensions: 1024})

	cases := []struct {
		name  string
		ping  func(context.Context) error
		embed func(context.Context, string) ([]float32, error)
	}{
		{"openai", openai.Ping, openai.Embed},
		{"cohere", cohere.Ping, cohere.Embed},
		{"voyage", voyage.Ping, voyage.Embed},
	}
	for _, tc := range cases {
		t.Run(tc.name+" ping", func(t *testing.T) {
			if err := tc.ping(ctx); !errors.Is(err, ErrAPIKeyNotConfigured) {
				t.Fatalf("ping error = %v, want ErrAPIKeyNotConfigured", err)
			}
		})
		t.Run(tc.name+" embed", func(t *testing.T) {
			if _, err := tc.embed(ctx, "x"); !errors.Is(err, ErrAPIKeyNotConfigured) {
				t.Fatalf("embed error = %v, want ErrAPIKeyNotConfigured", err)
			}
		})
	}
}

func TestLocalOllamaHintAt(t *testing.T) {
	serveTags := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tags" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
	}

	tests := []struct {
		name       string
		body       string
		wantHint   bool
		wantSubstr string
	}{
		{
			name:       "ollama with a known embedding model",
			body:       `{"models":[{"name":"nomic-embed-text:latest"},{"name":"llama3:8b"}]}`,
			wantHint:   true,
			wantSubstr: "config preset fast-local",
		},
		{
			// "all-minilm" contains no "embed", so this case passes only via
			// the known-model map — the substring heuristic alone would miss it.
			name:       "ollama with a known model whose name lacks embed",
			body:       `{"models":[{"name":"all-minilm:latest"}]}`,
			wantHint:   true,
			wantSubstr: "config preset fast-local",
		},
		{
			name:       "ollama with an embed-named model",
			body:       `{"models":[{"name":"qwen3-embedding:0.6b"}]}`,
			wantHint:   true,
			wantSubstr: "config preset fast-local",
		},
		{
			name:     "ollama without embedding models",
			body:     `{"models":[{"name":"llama3:8b"}]}`,
			wantHint: false,
		},
		{
			name:     "ollama with no models",
			body:     `{"models":[]}`,
			wantHint: false,
		},
		{
			name:     "malformed response",
			body:     `not json`,
			wantHint: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := serveTags(tc.body)
			defer srv.Close()

			hint := localOllamaHintAt(context.Background(), srv.URL)
			if tc.wantHint {
				if hint == "" {
					t.Fatal("expected a non-empty hint")
				}
				if !strings.Contains(hint, tc.wantSubstr) {
					t.Fatalf("hint %q should mention %q", hint, tc.wantSubstr)
				}
				if !strings.Contains(hint, "no API key") {
					t.Fatalf("hint %q should mention that no API key is needed", hint)
				}
			} else if hint != "" {
				t.Fatalf("hint = %q, want empty", hint)
			}
		})
	}
}

func TestLocalOllamaHintAtUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens anymore

	if hint := localOllamaHintAt(context.Background(), url); hint != "" {
		t.Fatalf("hint = %q, want empty for an unreachable ollama", hint)
	}
}

func TestLocalOllamaHintAtBoundedByShortTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)

	start := time.Now()
	hint := localOllamaHintAt(context.Background(), srv.URL)
	elapsed := time.Since(start)

	if hint != "" {
		t.Fatalf("hint = %q, want empty when ollama hangs", hint)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("detection took %v; it must stay bounded well below the hang", elapsed)
	}
}

func TestLocalOllamaHintAtRespectsCanceledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"nomic-embed-text:latest"}]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if hint := localOllamaHintAt(ctx, srv.URL); hint != "" {
		t.Fatalf("hint = %q, want empty for a canceled context", hint)
	}
}

func TestLocalOllamaHintURLRespectsOllamaHost(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	if url := localOllamaBaseURL(); url != "http://localhost:11434" {
		t.Fatalf("default base URL = %q", url)
	}
	t.Setenv("OLLAMA_HOST", "127.0.0.1:11434")
	if url := localOllamaBaseURL(); url != "http://127.0.0.1:11434" {
		t.Fatalf("scheme-less OLLAMA_HOST should gain http://, got %q", url)
	}
}
