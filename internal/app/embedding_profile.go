package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

const (
	embeddingProfileFile          = "embedding_profile.json"
	embeddingProfileMetaKey       = "embedding_profile"
	embeddingProfileSchemaVersion = 1
	embeddingProfileDistance      = "cosine"
	embeddingProfileModality      = "text"
	embeddingProfilePreprocessor  = "code-chunker-v2-lossless"
)

type EmbeddingProfile struct {
	SchemaVersion    int    `json:"schema_version"`
	ProfileID        string `json:"profile_id"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	Dimensions       int    `json:"dimensions"`
	Distance         string `json:"distance"`
	Modality         string `json:"modality"`
	Preprocessor     string `json:"preprocessor"`
	QueryTemplate    string `json:"query_template,omitempty"`
	DocumentTemplate string `json:"document_template,omitempty"`
	OllamaContext    int    `json:"ollama_context,omitempty"`
	OllamaOptions    string `json:"ollama_options,omitempty"`
}

type EmbeddingProfileMismatchError struct {
	Reason  string
	Stored  *EmbeddingProfile
	Current EmbeddingProfile
}

func (e *EmbeddingProfileMismatchError) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = "stored embedding profile does not match active configuration"
	}
	if e.Stored == nil {
		return fmt.Sprintf("%s; run 'vecgrep index --full' or 'vecgrep reset --force' to rebuild", reason)
	}
	return fmt.Sprintf("%s: stored %q, active %q; run 'vecgrep index --full' or 'vecgrep reset --force' to rebuild",
		reason, e.Stored.ProfileID, e.Current.ProfileID)
}

func (e *EmbeddingProfileMismatchError) Unwrap() error {
	return ErrEmbeddingProfileMismatch
}

// EmbeddingProfilePath returns the path of the legacy embedding_profile.json
// sidecar. New projects store the profile in VecLite collection metadata
// instead; the sidecar is kept only as a migration source for existing indexes.
func EmbeddingProfilePath(dataDir string) string {
	return filepath.Join(dataDir, embeddingProfileFile)
}

func CurrentEmbeddingProfile(cfg *config.Config) EmbeddingProfile {
	profile := EmbeddingProfile{
		SchemaVersion:    embeddingProfileSchemaVersion,
		Provider:         cfg.Embedding.Provider,
		Model:            cfg.Embedding.Model,
		Dimensions:       cfg.Embedding.Dimensions,
		Distance:         embeddingProfileDistance,
		Modality:         embeddingProfileModality,
		Preprocessor:     embeddingProfilePreprocessor,
		QueryTemplate:    cfg.Embedding.QueryTemplate,
		DocumentTemplate: cfg.Embedding.DocumentTemplate,
	}
	profile.ProfileID = fmt.Sprintf("%s:%s:%d:%s:%s",
		profile.Provider,
		profile.Model,
		profile.Dimensions,
		profile.Distance,
		profile.Preprocessor,
	)
	if profile.QueryTemplate != "" || profile.DocumentTemplate != "" {
		templateHash := sha256.Sum256([]byte(profile.QueryTemplate + "\x00" + profile.DocumentTemplate))
		profile.ProfileID += fmt.Sprintf(":templates:%x", templateHash)
	}
	if profile.Provider == "ollama" {
		profile.OllamaContext = cfg.Embedding.OllamaContext
		if len(cfg.Embedding.OllamaOptions) > 0 {
			options, err := json.Marshal(cfg.Embedding.OllamaOptions)
			if err != nil {
				panic(fmt.Sprintf("canonicalize Ollama embedding options: %v", err))
			}
			profile.OllamaOptions = string(options)
		}
		if profile.OllamaContext != 0 || profile.OllamaOptions != "" {
			requestHash := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", profile.OllamaContext, profile.OllamaOptions)))
			profile.ProfileID += fmt.Sprintf(":ollama:%x", requestHash)
		}
	}
	return profile
}

// LoadEmbeddingProfile reads the stored embedding profile from VecLite
// collection metadata. As a transparent migration, if the collection metadata
// has no profile but a legacy embedding_profile.json sidecar exists on disk,
// the sidecar is read, written into collection metadata, and the sidecar is
// removed.
func LoadEmbeddingProfile(database *db.DB, dataDir string) (*EmbeddingProfile, error) {
	if database == nil {
		return loadSidecarProfile(dataDir)
	}

	if raw, ok := database.CollectionMetadataValue(embeddingProfileMetaKey); ok {
		return decodeProfile(raw)
	}

	// Migration path: no metadata yet, but a legacy sidecar exists.
	sidecar, err := loadSidecarProfile(dataDir)
	if err != nil {
		return nil, err
	}
	if sidecar == nil {
		return nil, nil
	}
	// Store as a gob-native map[string]any. json.RawMessage (a []byte) is
	// NOT gob-registered in veclite's storage layer, so it fails silently
	// on Sync. A plain map round-trips through gob correctly.
	if err := SaveEmbeddingProfile(database, dataDir, *sidecar); err != nil {
		return nil, fmt.Errorf("migrate embedding profile to collection metadata: %w", err)
	}
	if err := RemoveEmbeddingProfile(dataDir); err != nil {
		log.Printf("embedding profile: migrated to collection metadata but failed to remove sidecar: %v", err)
	}
	return sidecar, nil
}

// SaveEmbeddingProfile writes the embedding profile to VecLite collection
// metadata. The legacy sidecar is removed if present so the two storage
// locations never diverge.
func SaveEmbeddingProfile(database *db.DB, dataDir string, profile EmbeddingProfile) error {
	if database == nil {
		return saveSidecarProfile(dataDir, profile)
	}
	// Marshal to JSON then unmarshal into map[string]any. This gives a
	// gob-encodable value (veclite's storage uses gob, and json.RawMessage
	// / []byte are not gob-registered for the metadata map).
	data, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("marshal embedding profile: %w", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(data, &asMap); err != nil {
		return fmt.Errorf("normalize embedding profile for storage: %w", err)
	}
	if err := database.SetCollectionMetadataValue(embeddingProfileMetaKey, asMap); err != nil {
		return fmt.Errorf("write embedding profile to collection metadata: %w", err)
	}
	// Best-effort cleanup of any leftover legacy sidecar.
	if err := RemoveEmbeddingProfile(dataDir); err != nil {
		log.Printf("embedding profile: saved to collection metadata but failed to remove legacy sidecar: %v", err)
	}
	return nil
}

// RemoveEmbeddingProfile removes the legacy embedding_profile.json sidecar
// from disk. Collection metadata is cleared via RemoveEmbeddingProfileMeta.
func RemoveEmbeddingProfile(dataDir string) error {
	err := os.Remove(EmbeddingProfilePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove embedding profile sidecar: %w", err)
	}
	return nil
}

// RemoveEmbeddingProfileMeta removes the embedding profile from VecLite
// collection metadata.
func RemoveEmbeddingProfileMeta(database *db.DB) error {
	if database == nil {
		return nil
	}
	return database.DeleteCollectionMetadataValue(embeddingProfileMetaKey)
}

func (p EmbeddingProfile) Matches(other EmbeddingProfile) bool {
	return p.SchemaVersion == other.SchemaVersion &&
		p.ProfileID == other.ProfileID &&
		p.Provider == other.Provider &&
		p.Model == other.Model &&
		p.Dimensions == other.Dimensions &&
		p.Distance == other.Distance &&
		p.Modality == other.Modality &&
		p.Preprocessor == other.Preprocessor &&
		p.QueryTemplate == other.QueryTemplate &&
		p.DocumentTemplate == other.DocumentTemplate &&
		p.OllamaContext == other.OllamaContext &&
		p.OllamaOptions == other.OllamaOptions
}

func (s *Service) ensureEmbeddingProfileMatches() error {
	current := CurrentEmbeddingProfile(s.session.Config)
	stored, err := LoadEmbeddingProfile(s.session.DB, s.session.Config.DataDir)
	if err != nil {
		return err
	}
	if stored == nil {
		if s.hasIndexedChunks() {
			return &EmbeddingProfileMismatchError{
				Reason:  "embedding profile is missing for an existing index",
				Current: current,
			}
		}
		return nil
	}
	if !stored.Matches(current) {
		return &EmbeddingProfileMismatchError{Stored: stored, Current: current}
	}
	return nil
}

func (s *Service) ensureEmbeddingProfileForIndex(fullReindex bool) error {
	if fullReindex {
		return nil
	}
	return s.ensureEmbeddingProfileMatches()
}

func (s *Service) saveCurrentEmbeddingProfile() error {
	return SaveEmbeddingProfile(s.session.DB, s.session.Config.DataDir, CurrentEmbeddingProfile(s.session.Config))
}

func (s *Service) hasIndexedChunks() bool {
	stats, err := s.session.DB.StatsForProject(s.session.ProjectRoot)
	if err != nil {
		return true
	}
	return stats["chunks"] > 0
}

// loadSidecarProfile reads the legacy embedding_profile.json sidecar.
func loadSidecarProfile(dataDir string) (*EmbeddingProfile, error) {
	data, err := os.ReadFile(EmbeddingProfilePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read embedding profile sidecar: %w", err)
	}
	return decodeProfile(data)
}

// saveSidecarProfile writes the legacy embedding_profile.json sidecar. Used
// only when no DB handle is available (e.g. before the collection exists).
func saveSidecarProfile(dataDir string, profile EmbeddingProfile) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal embedding profile: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(EmbeddingProfilePath(dataDir), data, 0644); err != nil {
		return fmt.Errorf("write embedding profile sidecar: %w", err)
	}
	return nil
}

func decodeProfile(raw any) (*EmbeddingProfile, error) {
	data, err := toJSONBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("decode embedding profile: %w", err)
	}
	var profile EmbeddingProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("parse embedding profile: %w", err)
	}
	return &profile, nil
}

// toJSONBytes normalizes a metadata value (string, []byte, json.RawMessage,
// or an already-decoded map) into JSON bytes.
func toJSONBytes(raw any) ([]byte, error) {
	switch v := raw.(type) {
	case nil:
		return nil, fmt.Errorf("empty profile value")
	case json.RawMessage:
		return v, nil
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return json.Marshal(v)
	}
}
