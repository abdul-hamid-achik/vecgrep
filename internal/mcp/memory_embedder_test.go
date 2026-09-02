package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Stats and forget only touch the local store; they must work while the memory
// embedder (local Ollama) is unreachable. Remember and recall embed text and
// must fail with the provider remedy instead.
func TestMemoryStatsWorksWithoutEmbedder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("VECAI_OLLAMA_URL", "http://127.0.0.1:1") // nothing listens here
	s := &SDKServer{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, _, err := s.handleMemoryStats(ctx, nil, MemoryStatsInput{})
	text, isErr := toolText(t, result, err)
	if isErr {
		t.Fatalf("memory_stats must not need the embedder:\n%s", text)
	}
	if !strings.Contains(text, "Total memories: 0") {
		t.Fatalf("unexpected stats body:\n%s", text)
	}

	result, _, err = s.handleMemoryRecall(ctx, nil, MemoryRecallInput{Query: "anything"})
	text, isErr = toolText(t, result, err)
	if !isErr || !strings.Contains(text, "embedding provider not available") || !strings.Contains(text, "VECAI_OLLAMA_URL") {
		t.Fatalf("recall without embedder must fail with the provider remedy:\n%s", text)
	}
}
