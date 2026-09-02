package embed

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func clearOpenAIKeyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VECGREP_OPENAI_API_KEY", "")
}

func TestAPIKeySourcePrecedence(t *testing.T) {
	clearOpenAIKeyEnv(t)

	if got := APIKeySource(ProviderOpenAI, ""); got != "" {
		t.Fatalf("no key anywhere: source = %q, want empty", got)
	}
	if got := APIKeySource(ProviderOpenAI, "sk-from-config"); got != "config:embedding.openai_api_key" {
		t.Fatalf("config key: source = %q", got)
	}
	t.Setenv("VECGREP_OPENAI_API_KEY", "sk-vecgrep")
	if got := APIKeySource(ProviderOpenAI, "sk-vecgrep"); got != "env:VECGREP_OPENAI_API_KEY" {
		t.Fatalf("vecgrep env var must win over config attribution: source = %q", got)
	}
	t.Setenv("OPENAI_API_KEY", "sk-plain")
	if got := APIKeySource(ProviderOpenAI, ""); got != "env:OPENAI_API_KEY" {
		t.Fatalf("OPENAI_API_KEY has highest precedence: source = %q", got)
	}
	if got := APIKeySource(ProviderOllama, "anything"); got != "" {
		t.Fatalf("keyless provider must report no source, got %q", got)
	}
	if RequiresAPIKey(ProviderOllama) || !RequiresAPIKey(ProviderOpenAI) {
		t.Fatal("RequiresAPIKey: ollama must be false, openai true")
	}
}

func TestAPIKeyHintNamesEveryLocation(t *testing.T) {
	hint := APIKeyHint(ProviderCohere)
	for _, want := range []string{"COHERE_API_KEY", "VECGREP_COHERE_API_KEY", "embedding.cohere_api_key"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q missing %q", hint, want)
		}
	}
	if APIKeyHint(ProviderOllama) != "" {
		t.Fatal("keyless provider must have no key hint")
	}
}

func TestMissingAPIKeyErrorNamesEnvVars(t *testing.T) {
	err := missingAPIKeyError(ProviderVoyage, "ping")
	if !errors.Is(err, ErrAPIKeyNotConfigured) {
		t.Fatalf("errors.Is sentinel lost: %v", err)
	}
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Provider != "voyage" || pe.Op != "ping" {
		t.Fatalf("provider error context lost: %#v", err)
	}
	if !strings.Contains(err.Error(), "VOYAGE_API_KEY") {
		t.Fatalf("message must name the env var: %v", err)
	}
}

func TestOpenAIPingWithoutKeyIsActionable(t *testing.T) {
	clearOpenAIKeyEnv(t)
	p := NewOpenAIProvider(OpenAIConfig{})
	err := p.Ping(context.Background())
	if !errors.Is(err, ErrAPIKeyNotConfigured) {
		t.Fatalf("want ErrAPIKeyNotConfigured, got %v", err)
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("message must say which variable to set: %v", err)
	}
}

func TestUnderlyingUnwrapsDecorators(t *testing.T) {
	inner := NewOpenAIProvider(OpenAIConfig{APIKey: "sk-test"})
	throttled := NewThrottledProvider(inner, ThrottleConfig{})
	defer throttled.Close()
	if Underlying(throttled) != inner {
		t.Fatal("Underlying must see through ThrottledProvider")
	}
	cached := WithCache(throttled, 4)
	if Underlying(cached) != inner {
		t.Fatal("Underlying must see through CachedProvider over ThrottledProvider")
	}
	if Underlying(inner) != inner {
		t.Fatal("Underlying of a plain provider is itself")
	}
	if Underlying(nil) != nil {
		t.Fatal("Underlying(nil) must be nil")
	}
}
