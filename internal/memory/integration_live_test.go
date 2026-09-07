package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

// TestLiveOllamaCompiledMemory exercises dedup, related links, and usage
// tracking against the real local Ollama nomic-embed-text embedder. Skipped
// when Ollama is not running, like the other provider-dependent tests.
func TestLiveOllamaCompiledMemory(t *testing.T) {
	provider := embed.NewOllamaProvider(embed.OllamaConfig{
		URL:        DefaultOllamaURL,
		Model:      DefaultEmbeddingModel,
		Dimensions: DefaultEmbeddingDimensions,
	})
	if err := provider.Ping(context.Background()); err != nil {
		t.Skip("local Ollama not running, skipping live memory test")
	}

	tmpDir, err := os.MkdirTemp("", "memory-live-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	cfg := DefaultConfig()
	cfg.DBPath = filepath.Join(tmpDir, "live.veclite")
	store, err := NewMemoryStore(cfg, provider)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	// 1. Store and then refuse a near-duplicate re-store.
	first, err := store.Remember(ctx, "The staging deploy port is 8080 and requires the VPN", RememberOptions{})
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	_, err = store.Remember(ctx, "The staging deploy port is 8080 and requires the VPN", RememberOptions{})
	var dup *DuplicateError
	if !errors.As(err, &dup) {
		t.Errorf("expected duplicate refusal, got %v", err)
	} else if dup.ExistingID != first.ID {
		t.Errorf("duplicate points at %d, want %d", dup.ExistingID, first.ID)
	}

	// 2. Related paraphrase gets linked, not refused.
	linked, err := store.Remember(ctx, "Staging deployments go through port 8080 behind the VPN", RememberOptions{})
	if err != nil {
		t.Fatalf("remember paraphrase: %v", err)
	}
	if len(linked.Related) == 0 {
		t.Errorf("paraphrase should link to the original, got no related memories")
	}

	// 3. An unrelated memory stays unlinked.
	unrelated, err := store.Remember(ctx, "Frontend likes tabs over spaces in the design system", RememberOptions{})
	if err != nil {
		t.Fatalf("remember unrelated: %v", err)
	}
	if len(unrelated.Related) != 0 {
		t.Errorf("unrelated memory should not link, got %+v", unrelated.Related)
	}

	// 4. Recall tracks access and resolves link previews.
	memories, err := store.Recall(ctx, "how do deploys to staging work", RecallOptions{Limit: 3})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(memories) == 0 {
		t.Fatalf("expected recall hits")
	}
	sawLink := false
	for _, m := range memories {
		if m.AccessCount < 1 {
			t.Errorf("memory %d recalled without access tracking", m.ID)
		}
		if len(m.Related) > 0 {
			sawLink = true
		}
	}
	if !sawLink {
		t.Errorf("deploy memories recalled without their related links")
	}

	// 5. Usage lift: after many accesses, the accessed memory keeps ranking.
	for i := 0; i < 3; i++ {
		if _, err := store.Recall(ctx, "staging deploy port", RecallOptions{Limit: 1}); err != nil {
			t.Fatalf("recall %d: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	top, err := store.Recall(ctx, "staging deploy port", RecallOptions{Limit: 1})
	if err != nil {
		t.Fatalf("final recall: %v", err)
	}
	if len(top) == 0 || (top[0].ID != first.ID && top[0].ID != linked.ID) {
		t.Errorf("expected a deploy memory on top, got %+v", top)
	}
}
