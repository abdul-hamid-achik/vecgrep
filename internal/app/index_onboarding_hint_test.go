package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

// failingPingProvider simulates a cloud provider whose credentials are missing.
type failingPingProvider struct {
	err error
}

func (p *failingPingProvider) Embed(context.Context, string) ([]float32, error) {
	return nil, p.err
}

func (p *failingPingProvider) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	return nil, p.err
}

func (p *failingPingProvider) Model() string              { return "text-embedding-3-small" }
func (p *failingPingProvider) Dimensions() int            { return 1536 }
func (p *failingPingProvider) Ping(context.Context) error { return p.err }
func (p *failingPingProvider) Warmup(context.Context) (time.Duration, error) {
	return 0, nil
}

func TestProviderUnavailableErrorAppendsLocalOllamaHint(t *testing.T) {
	orig := localOllamaHint
	t.Cleanup(func() { localOllamaHint = orig })

	pingErr := embed.NewProviderError("openai", "ping", embed.ErrAPIKeyNotConfigured)

	localOllamaHint = func(context.Context) string {
		return "a local ollama is running with embedding models pulled — `vecgrep config preset fast-local` needs no API key"
	}
	err := providerUnavailableError(context.Background(), pingErr)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "embedding provider unavailable") {
		t.Fatalf("error %q should keep the unavailable context", err)
	}
	if !strings.Contains(err.Error(), "config preset fast-local") {
		t.Fatalf("error %q should append the local-ollama tip", err)
	}
	if !errors.Is(err, embed.ErrAPIKeyNotConfigured) {
		t.Fatal("original error chain must be preserved")
	}
}

func TestProviderUnavailableErrorWithoutHintKeepsOriginalError(t *testing.T) {
	orig := localOllamaHint
	t.Cleanup(func() { localOllamaHint = orig })

	pingErr := embed.NewProviderError("openai", "ping", embed.ErrAPIKeyNotConfigured)

	localOllamaHint = func(context.Context) string { return "" }
	err := providerUnavailableError(context.Background(), pingErr)
	if !strings.Contains(err.Error(), "API key not configured") {
		t.Fatalf("error %q should keep the original message", err)
	}
	if strings.Contains(err.Error(), "preset") {
		t.Fatalf("error %q should not mention a preset when detection found nothing", err)
	}
	if !errors.Is(err, embed.ErrAPIKeyNotConfigured) {
		t.Fatal("original error chain must be preserved")
	}
}

func TestProviderUnavailableErrorIgnoresNonAuthFailures(t *testing.T) {
	orig := localOllamaHint
	t.Cleanup(func() { localOllamaHint = orig })

	hinted := false
	localOllamaHint = func(context.Context) string {
		hinted = true
		return "should not appear"
	}

	// A connectivity failure (ollama not running) must not suggest switching
	// presets: the user is already on a local setup.
	err := providerUnavailableError(context.Background(), errors.New("connection refused"))
	if strings.Contains(err.Error(), "preset") {
		t.Fatalf("error %q should not gain a preset tip for non-auth failures", err)
	}
	if hinted {
		t.Fatal("detection must not run for non-auth failures")
	}
}

func TestIndexCoordinatorSurfacesOllamaHintOnAuthFailure(t *testing.T) {
	orig := localOllamaHint
	t.Cleanup(func() { localOllamaHint = orig })
	localOllamaHint = func(context.Context) string {
		return "a local ollama is running with embedding models pulled — `vecgrep config preset fast-local` needs no API key"
	}

	root, cfg, _ := newCoordinatorFixture(t)
	provider := &failingPingProvider{err: embed.NewProviderError("openai", "ping", embed.ErrAPIKeyNotConfigured)}
	coordinator := NewIndexCoordinator(root, cfg, provider, &trackingIndexDBSource{})

	_, err := coordinator.Index(context.Background(), IndexRequest{StructuralChunks: string(StructuralChunksOff)}, nil)
	if err == nil {
		t.Fatal("expected the index run to fail")
	}
	if !strings.Contains(err.Error(), "config preset fast-local") {
		t.Fatalf("index error %q should carry the local-ollama tip", err)
	}
}
