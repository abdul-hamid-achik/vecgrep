package app

import (
	"errors"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

func TestEmbeddingProfileProtectsSemanticTemplates(t *testing.T) {
	cfg := config.DefaultConfig()
	baseline := CurrentEmbeddingProfile(cfg)

	cfg.Embedding.QueryTemplate = "query: {{text}}"
	queryProfile := CurrentEmbeddingProfile(cfg)
	if baseline.Matches(queryProfile) {
		t.Fatal("query template change must require an index rebuild")
	}
	if baseline.ProfileID == queryProfile.ProfileID {
		t.Fatal("query template change did not change profile ID")
	}

	cfg.Embedding.QueryTemplate = ""
	cfg.Embedding.DocumentTemplate = "document: {{text}}"
	documentProfile := CurrentEmbeddingProfile(cfg)
	if baseline.Matches(documentProfile) {
		t.Fatal("document template change must require an index rebuild")
	}
	if baseline.ProfileID == documentProfile.ProfileID {
		t.Fatal("document template change did not change profile ID")
	}
}

func TestEmbeddingProfileIdentityTracksLosslessChunker(t *testing.T) {
	cfg := config.DefaultConfig()
	profile := CurrentEmbeddingProfile(cfg)
	if profile.ProfileID != "ollama:nomic-embed-text:768:cosine:code-chunker-v2-lossless" {
		t.Fatalf("ProfileID = %q, want lossless chunker identity", profile.ProfileID)
	}
}

func TestEmbeddingProfileCanonicalizesOllamaOptions(t *testing.T) {
	first := config.DefaultConfig()
	first.Embedding.OllamaOptions = map[string]any{
		"temperature": 0.25,
		"nested": map[string]any{
			"enabled": true,
			"values":  []any{"one", float64(2), nil},
		},
	}
	second := config.DefaultConfig()
	second.Embedding.OllamaOptions = map[string]any{
		"nested": map[string]any{
			"values":  []any{"one", float64(2), nil},
			"enabled": true,
		},
		"temperature": 0.25,
	}

	firstProfile := CurrentEmbeddingProfile(first)
	secondProfile := CurrentEmbeddingProfile(second)
	if !firstProfile.Matches(secondProfile) {
		t.Fatalf("equivalent reordered options produced different profiles:\nfirst:  %+v\nsecond: %+v", firstProfile, secondProfile)
	}
	if firstProfile.OllamaOptions != `{"nested":{"enabled":true,"values":["one",2,null]},"temperature":0.25}` {
		t.Fatalf("OllamaOptions = %q, want canonical JSON", firstProfile.OllamaOptions)
	}
}

func TestEmbeddingProfileProtectsOllamaRequestSemantics(t *testing.T) {
	baseline := CurrentEmbeddingProfile(config.DefaultConfig())

	tests := []struct {
		name   string
		mutate func(*config.Config)
	}{
		{
			name: "context",
			mutate: func(cfg *config.Config) {
				cfg.Embedding.OllamaContext = 4096
			},
		},
		{
			name: "option",
			mutate: func(cfg *config.Config) {
				cfg.Embedding.OllamaOptions = map[string]any{"num_batch": 128}
			},
		},
		{
			name: "nested option",
			mutate: func(cfg *config.Config) {
				cfg.Embedding.OllamaOptions = map[string]any{
					"sampling": map[string]any{"temperature": 0.5},
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			tt.mutate(cfg)
			changed := CurrentEmbeddingProfile(cfg)
			if baseline.Matches(changed) {
				t.Fatal("Ollama request semantic change must require an index rebuild")
			}
			if baseline.ProfileID == changed.ProfileID {
				t.Fatal("Ollama request semantic change did not change profile ID")
			}
		})
	}
}

func TestEmbeddingProfileLegacyMigrationDefaults(t *testing.T) {
	legacy, err := decodeProfile([]byte(`{
		"schema_version": 1,
		"profile_id": "ollama:nomic-embed-text:768:cosine:code-chunker-v1",
		"provider": "ollama",
		"model": "nomic-embed-text",
		"dimensions": 768,
		"distance": "cosine",
		"modality": "text",
		"preprocessor": "code-chunker-v1"
	}`))
	if err != nil {
		t.Fatalf("decodeProfile() error = %v", err)
	}
	if current := CurrentEmbeddingProfile(config.DefaultConfig()); legacy.Matches(current) {
		t.Fatalf("legacy lossy profile must require a rebuild:\nlegacy:  %+v\ncurrent: %+v", legacy, current)
	}

	withContext := config.DefaultConfig()
	withContext.Embedding.OllamaContext = 4096
	if legacy.Matches(CurrentEmbeddingProfile(withContext)) {
		t.Fatal("legacy profile must mismatch a non-default Ollama context")
	}

	withOptions := config.DefaultConfig()
	withOptions.Embedding.OllamaOptions = map[string]any{"num_batch": 128}
	if legacy.Matches(CurrentEmbeddingProfile(withOptions)) {
		t.Fatal("legacy profile must mismatch non-default Ollama options")
	}
}

func TestEmbeddingProfilePersistsOllamaRequestIdentity(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Embedding.OllamaContext = 8192
	cfg.Embedding.OllamaOptions = map[string]any{
		"num_batch": 128,
		"nested":    map[string]any{"enabled": true},
	}
	want := CurrentEmbeddingProfile(cfg)

	dataDir := t.TempDir()
	if err := SaveEmbeddingProfile(nil, dataDir, want); err != nil {
		t.Fatalf("SaveEmbeddingProfile() error = %v", err)
	}
	got, err := LoadEmbeddingProfile(nil, dataDir)
	if err != nil {
		t.Fatalf("LoadEmbeddingProfile() error = %v", err)
	}
	if got == nil || !got.Matches(want) {
		t.Fatalf("persisted profile = %+v, want %+v", got, want)
	}
}

func TestServiceRejectsSemanticTemplateProfileMismatch(t *testing.T) {
	session, service := createTestSession(t)
	stored := CurrentEmbeddingProfile(session.Config)
	if err := SaveEmbeddingProfile(session.DB, session.Config.DataDir, stored); err != nil {
		t.Fatalf("SaveEmbeddingProfile() error = %v", err)
	}
	session.Config.Embedding.QueryTemplate = "query: {{text}}"

	err := service.ensureEmbeddingProfileMatches()
	if !errors.Is(err, ErrEmbeddingProfileMismatch) {
		t.Fatalf("ensureEmbeddingProfileMatches() error = %v, want profile mismatch", err)
	}
}
