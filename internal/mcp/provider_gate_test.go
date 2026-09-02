package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/app"
	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

// keylessProvider simulates a cloud provider whose API key is missing: every
// embedding call fails with the sentinel, exactly like OpenAI without a key.
type keylessProvider struct{ mcpIndexProvider }

func (keylessProvider) Embed(context.Context, string) ([]float32, error) {
	return nil, embed.NewProviderError("openai", "embed", embed.ErrAPIKeyNotConfigured)
}

func (keylessProvider) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, embed.NewProviderError("openai", "embed", embed.ErrAPIKeyNotConfigured)
}

func (keylessProvider) Ping(context.Context) error {
	return embed.NewProviderError("openai", "ping", embed.ErrAPIKeyNotConfigured)
}

func clearCloudKeys(t *testing.T) {
	t.Helper()
	for _, p := range []embed.ProviderType{embed.ProviderOpenAI, embed.ProviderCohere, embed.ProviderVoyage} {
		for _, name := range embed.APIKeyEnvVars(p) {
			t.Setenv(name, "")
		}
	}
}

// newUnbuiltTestServer builds an initialized server for an openai project whose
// veclite store has never been written.
func newUnbuiltTestServer(t *testing.T) *SDKServer {
	t.Helper()
	clearCloudKeys(t)
	base := t.TempDir()
	root := filepath.Join(base, "proj")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.DataDir = filepath.Join(base, "data")
	cfg.Embedding.Provider = "openai"
	cfg.Embedding.Model = "text-embedding-3-small"
	cfg.Embedding.Dimensions = 8
	cfg.Codemap.Enabled = false
	session := newMCPSession(cfg, root, keylessProvider{mcpIndexProvider{dimensions: 8, model: "test"}})
	session.projectName = "proj"
	return &SDKServer{
		session:     session,
		projectRoot: root,
		initialized: true,
		codemapCfg:  config.CodemapConfig{Enabled: false},
	}
}

func TestHandleSearch_IndexNotBuiltReportsEmptyReadinessAndKeyNote(t *testing.T) {
	s := newUnbuiltTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, structured, err := s.handleSearch(ctx, nil, SearchInput{Query: "auth", Mode: "keyword", Limit: 5})
	text, isErr := toolText(t, result, err)
	if !isErr {
		t.Fatalf("IsError = false, want true; body:\n%s", text)
	}
	if strings.Contains(text, "collection not found") || strings.Contains(text, "Failed to open database") {
		t.Fatalf("veclite internals must not leak into the agent-facing error:\n%s", text)
	}
	for _, want := range []string{`"state":"empty"`, `"action":"vecgrep_index"`, "not built", "OPENAI_API_KEY"} {
		if !strings.Contains(text, want) {
			t.Fatalf("body missing %q:\n%s", want, text)
		}
	}
	if r, ok := structured.(app.Readiness); !ok || r.State != app.ReadinessEmpty {
		t.Fatalf("structured = %#v, want Readiness empty", structured)
	}
}

func TestHandleStatus_IndexNotBuiltIsReadinessNotError(t *testing.T) {
	s := newUnbuiltTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, structured, err := s.handleStatus(ctx, nil, StatusInput{})
	text, isErr := toolText(t, result, err)
	if isErr {
		t.Fatalf("status of a never-built index must not be a tool error:\n%s", text)
	}
	for _, want := range []string{"not built", `"state":"empty"`, "API key: missing", "OPENAI_API_KEY"} {
		if !strings.Contains(text, want) {
			t.Fatalf("body missing %q:\n%s", want, text)
		}
	}
	if r, ok := structured.(app.Readiness); !ok || r.State != app.ReadinessEmpty || r.Action != app.ActionIndex {
		t.Fatalf("structured = %#v", structured)
	}
}

func TestHandleEnsure_CheckOnIndexNotBuilt(t *testing.T) {
	s := newUnbuiltTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, _, err := s.handleEnsure(ctx, nil, EnsureInput{Mode: "check"})
	text, isErr := toolText(t, result, err)
	if !isErr {
		t.Fatalf("check on a missing store must be IsError:\n%s", text)
	}
	for _, want := range []string{"Not searchable. Next: call vecgrep_index", `"state":"empty"`, "OPENAI_API_KEY"} {
		if !strings.Contains(text, want) {
			t.Fatalf("body missing %q:\n%s", want, text)
		}
	}
}

func TestHandleSearch_KeywordModeDoesNotNeedProvider(t *testing.T) {
	clearCloudKeys(t)
	s := newReadinessTestServer(t, true, false)
	s.session.provider = keylessProvider{mcpIndexProvider{dimensions: 8, model: "test"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, _, err := s.handleSearch(ctx, nil, SearchInput{Query: "LoadConfig", Mode: "keyword", Limit: 5})
	text, isErr := toolText(t, result, err)
	if isErr {
		t.Fatalf("keyword search must not be gated on the embedder:\n%s", text)
	}
	if strings.Contains(text, "Embedding provider is not available") {
		t.Fatalf("keyword search must not probe the provider:\n%s", text)
	}
}

func TestHandleSearch_HybridDegradesToKeywordWithoutKey(t *testing.T) {
	clearCloudKeys(t)
	s := newReadinessTestServer(t, true, false)
	s.session.provider = keylessProvider{mcpIndexProvider{dimensions: 8, model: "test"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, _, err := s.handleSearch(ctx, nil, SearchInput{Query: "LoadConfig", Limit: 5})
	text, isErr := toolText(t, result, err)
	if isErr {
		t.Fatalf("hybrid must degrade, not fail:\n%s", text)
	}
	for _, want := range []string{"Warning", "keyword-only", "API key not configured"} {
		if !strings.Contains(text, want) {
			t.Fatalf("body missing %q:\n%s", want, text)
		}
	}
}

func TestHandleSearch_SemanticWithoutKeyExplainsRemedy(t *testing.T) {
	clearCloudKeys(t)
	s := newReadinessTestServer(t, true, false)
	s.session.provider = keylessProvider{mcpIndexProvider{dimensions: 8, model: "test"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, _, err := s.handleSearch(ctx, nil, SearchInput{Query: "LoadConfig", Mode: "semantic", Limit: 5})
	text, isErr := toolText(t, result, err)
	if !isErr {
		t.Fatalf("semantic search without an embedder must fail:\n%s", text)
	}
	if !strings.Contains(text, "Embedding provider is not available") || !strings.Contains(text, "launched") {
		t.Fatalf("remedy text missing:\n%s", text)
	}
}

func TestProviderUnavailableTextUnwrapsThrottledOpenAI(t *testing.T) {
	clearCloudKeys(t)
	inner := embed.NewOpenAIProvider(embed.OpenAIConfig{})
	throttled := embed.NewThrottledProvider(inner, embed.ThrottleConfig{})
	defer throttled.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := throttled.Ping(ctx)
	if err == nil {
		t.Fatal("ping without key must fail")
	}
	text := providerUnavailableText(ctx, throttled, err)
	for _, want := range []string{"To fix this (OpenAI)", "OPENAI_API_KEY", "embedding.openai_api_key"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Verify your embedding provider is configured correctly") {
		t.Fatalf("generic fallback used despite a known backend:\n%s", text)
	}
}
