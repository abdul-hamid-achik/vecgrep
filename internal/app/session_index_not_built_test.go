package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

// A registered project whose store was never written must be reported as
// "index not built" with the first `vecgrep index` as remedy — not as a
// veclite collection error that suggests a destructive reset.
func TestOpenReadOnlySessionReportsIndexNotBuilt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VECGREP_OPENAI_API_KEY", "")

	root := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".vecgrep"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "embedding:\n  provider: ollama\n  model: nomic-embed-text\n  dimensions: 8\n"
	if err := os.WriteFile(filepath.Join(root, ".vecgrep", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := OpenReadOnlySession(context.Background(), root)
	if err == nil {
		t.Fatal("expected an error for a project with no store")
	}
	if !errors.Is(err, ErrIndexNotBuilt) {
		t.Fatalf("want ErrIndexNotBuilt, got %v", err)
	}
	if !strings.Contains(err.Error(), "vecgrep index") {
		t.Fatalf("remedy must be the first index run: %v", err)
	}
	if strings.Contains(err.Error(), "reset") {
		t.Fatalf("must not suggest a destructive reset for a never-built index: %v", err)
	}
}

func TestIndexNotBuiltErrorFallsBackToDirectoryName(t *testing.T) {
	err := IndexNotBuiltError("", "/tmp/work/demo", "/tmp/data/vectors.veclite")
	if !strings.Contains(err.Error(), `"demo"`) {
		t.Fatalf("project name fallback missing: %v", err)
	}
}

func TestResolveProviderKeyStatusLabels(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("VECGREP_OPENAI_API_KEY", "")
	c := config.DefaultConfig()
	c.Embedding.Provider = "ollama"
	if k := ResolveProviderKeyStatus(c); k.RequiresKey || k.Missing() || k.Label() != "not required" {
		t.Fatalf("ollama key status = %+v", k)
	}

	c.Embedding.Provider = "openai"
	k := ResolveProviderKeyStatus(c)
	if !k.RequiresKey || !k.Missing() || !strings.Contains(k.Label(), "OPENAI_API_KEY") {
		t.Fatalf("openai without key = %+v (label %q)", k, k.Label())
	}

	t.Setenv("OPENAI_API_KEY", "sk-test")
	k = ResolveProviderKeyStatus(c)
	if k.Missing() || k.Source != "env:OPENAI_API_KEY" || k.Label() != "env:OPENAI_API_KEY" {
		t.Fatalf("openai with env key = %+v", k)
	}
}
