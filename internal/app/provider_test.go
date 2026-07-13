package app

import (
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

func TestNewProviderSupportsCloudProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		dims     int
		wantType any
	}{
		{
			name:     "openai",
			provider: "openai",
			model:    "text-embedding-3-small",
			dims:     1536,
			wantType: &embed.OpenAIProvider{},
		},
		{
			name:     "cohere",
			provider: "cohere",
			model:    "embed-v4.0",
			dims:     1536,
			wantType: &embed.CohereProvider{},
		},
		{
			name:     "voyage",
			provider: "voyage",
			model:    "voyage-code-3",
			dims:     1024,
			wantType: &embed.VoyageProvider{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Embedding.Provider = tt.provider
			cfg.Embedding.Model = tt.model
			cfg.Embedding.Dimensions = tt.dims

			// Use newInnerProvider to get the raw provider without the
			// throttle wrapper — NewProvider wraps everything in a
			// ThrottledProvider.
			provider, err := newInnerProvider(cfg)
			if err != nil {
				t.Fatalf("newInnerProvider failed: %v", err)
			}

			switch tt.wantType.(type) {
			case *embed.OpenAIProvider:
				if _, ok := provider.(*embed.OpenAIProvider); !ok {
					t.Fatalf("provider type = %T, want *embed.OpenAIProvider", provider)
				}
			case *embed.CohereProvider:
				if _, ok := provider.(*embed.CohereProvider); !ok {
					t.Fatalf("provider type = %T, want *embed.CohereProvider", provider)
				}
			case *embed.VoyageProvider:
				if _, ok := provider.(*embed.VoyageProvider); !ok {
					t.Fatalf("provider type = %T, want *embed.VoyageProvider", provider)
				}
			}
			if provider.Model() != tt.model {
				t.Fatalf("model = %q, want %q", provider.Model(), tt.model)
			}
			if provider.Dimensions() != tt.dims {
				t.Fatalf("dimensions = %d, want %d", provider.Dimensions(), tt.dims)
			}
		})
	}
}

func TestNewProviderRejectsUnknownProvider(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Embedding.Provider = "unknown"

	if _, err := NewProvider(cfg); err == nil {
		t.Fatal("NewProvider succeeded for unknown provider")
	}
}

func TestNewDaemonProviderOwnsSingleThrottleLayer(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Daemon.EmbedWorkers = 2
	cfg.Daemon.EmbedMaxInFlight = 3

	provider, err := NewDaemonProvider(cfg)
	if err != nil {
		t.Fatalf("NewDaemonProvider failed: %v", err)
	}
	throttled, ok := provider.(*embed.ThrottledProvider)
	if !ok {
		t.Fatalf("provider type = %T, want *embed.ThrottledProvider", provider)
	}
	throttled.Close()
}
