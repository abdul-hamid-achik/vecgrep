package embed

import (
	"fmt"
	"os"
	"strings"
)

// LauncherEnvHint explains the most common reason a key that "is set" is still
// missing: vecgrep inherits its environment from whatever launched it. GUI
// agents and MCP gateways do not source an interactive shell, so an export in
// ~/.zshrc never reaches them.
const LauncherEnvHint = "vecgrep only sees the environment of the process that launched it. " +
	"MCP gateways and GUI-launched agents (mcphub, Claude Code, Codex, Cursor) do not source ~/.zshrc, " +
	"so an export in your interactive shell is not enough. Put the key where the launcher spawns vecgrep " +
	"(mcphub: `vault:` + `vault_only:` or `env:` on the vecgrep server; Claude Code / Codex: the server's `env` block) " +
	"and run `vecgrep doctor` from that launcher to confirm what it sees."

// apiKeyEnv lists, in precedence order, the environment variables a cloud
// provider consults for its API key, plus the vecgrep.yaml field that can hold
// it. It is the single source of truth for every "where do I put the key"
// message so the CLI, MCP, and docs never disagree.
var apiKeyEnv = map[ProviderType]struct {
	envVars     []string
	configField string
}{
	ProviderOpenAI: {[]string{"OPENAI_API_KEY", "VECGREP_OPENAI_API_KEY"}, "embedding.openai_api_key"},
	ProviderCohere: {[]string{"COHERE_API_KEY", "VECGREP_COHERE_API_KEY"}, "embedding.cohere_api_key"},
	ProviderVoyage: {[]string{"VOYAGE_API_KEY", "VECGREP_VOYAGE_API_KEY"}, "embedding.voyage_api_key"},
}

// RequiresAPIKey reports whether the provider is a cloud provider that needs
// an API key. Ollama and unknown providers return false.
func RequiresAPIKey(provider ProviderType) bool {
	_, ok := apiKeyEnv[provider]
	return ok
}

// APIKeyEnvVars returns the environment variables consulted for the provider's
// API key, highest precedence first. Nil for keyless providers.
func APIKeyEnvVars(provider ProviderType) []string {
	spec, ok := apiKeyEnv[provider]
	if !ok {
		return nil
	}
	out := make([]string, len(spec.envVars))
	copy(out, spec.envVars)
	return out
}

// APIKeyConfigField returns the vecgrep.yaml field that can hold the provider's
// API key, or "" for keyless providers.
func APIKeyConfigField(provider ProviderType) string {
	return apiKeyEnv[provider].configField
}

// APIKeySource reports where the provider's active API key comes from:
//
//	"env:OPENAI_API_KEY"               — an environment variable (named)
//	"config:embedding.openai_api_key"  — the resolved vecgrep configuration
//	""                                 — no key anywhere (or a keyless provider)
//
// configValue is the key as resolved from configuration. The value itself is
// never returned, only its origin, so the result is safe to print and log.
func APIKeySource(provider ProviderType, configValue string) string {
	spec, ok := apiKeyEnv[provider]
	if !ok {
		return ""
	}
	// Viper binds VECGREP_<PROVIDER>_API_KEY into the config value too, so an
	// env match must be checked first or it would be misattributed to config.
	for _, name := range spec.envVars {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return "env:" + name
		}
	}
	if strings.TrimSpace(configValue) != "" {
		return "config:" + spec.configField
	}
	return ""
}

// APIKeyHint is the one-line remedy for a missing key: which variables and
// which config field to set. "" for keyless providers.
func APIKeyHint(provider ProviderType) string {
	spec, ok := apiKeyEnv[provider]
	if !ok {
		return ""
	}
	return fmt.Sprintf("set %s (or %s in vecgrep.yaml)", strings.Join(spec.envVars, " or "), spec.configField)
}

// missingAPIKeyError builds the provider error for an absent key. The env
// variable names ride inside the message so a bare error string is already
// actionable; errors.Is(err, ErrAPIKeyNotConfigured) still holds.
func missingAPIKeyError(provider ProviderType, op string) error {
	spec, ok := apiKeyEnv[provider]
	if !ok {
		return NewProviderError(string(provider), op, ErrAPIKeyNotConfigured)
	}
	return NewProviderError(string(provider), op,
		fmt.Errorf("%w (set %s)", ErrAPIKeyNotConfigured, strings.Join(spec.envVars, " or ")))
}

// Unwrapper is implemented by providers that decorate another provider
// (throttling, caching). Underlying walks it.
type Unwrapper interface {
	Unwrap() Provider
}

// Underlying returns the innermost provider behind any decorator chain, so
// callers can type-switch on the concrete backend (OpenAI, Ollama, …) even when
// the active provider is throttled or cached.
func Underlying(p Provider) Provider {
	for p != nil {
		u, ok := p.(Unwrapper)
		if !ok {
			return p
		}
		inner := u.Unwrap()
		if inner == nil || inner == p {
			return p
		}
		p = inner
	}
	return nil
}
