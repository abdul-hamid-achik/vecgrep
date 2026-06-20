package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

const (
	embeddingProfileFile          = "embedding_profile.json"
	embeddingProfileSchemaVersion = 1
	embeddingProfileDistance      = "cosine"
	embeddingProfileModality      = "text"
	embeddingProfilePreprocessor  = "code-chunker-v1"
)

type EmbeddingProfile struct {
	SchemaVersion int    `json:"schema_version"`
	ProfileID     string `json:"profile_id"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Dimensions    int    `json:"dimensions"`
	Distance      string `json:"distance"`
	Modality      string `json:"modality"`
	Preprocessor  string `json:"preprocessor"`
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

func EmbeddingProfilePath(dataDir string) string {
	return filepath.Join(dataDir, embeddingProfileFile)
}

func CurrentEmbeddingProfile(cfg *config.Config) EmbeddingProfile {
	profile := EmbeddingProfile{
		SchemaVersion: embeddingProfileSchemaVersion,
		Provider:      cfg.Embedding.Provider,
		Model:         cfg.Embedding.Model,
		Dimensions:    cfg.Embedding.Dimensions,
		Distance:      embeddingProfileDistance,
		Modality:      embeddingProfileModality,
		Preprocessor:  embeddingProfilePreprocessor,
	}
	profile.ProfileID = fmt.Sprintf("%s:%s:%d:%s:%s",
		profile.Provider,
		profile.Model,
		profile.Dimensions,
		profile.Distance,
		profile.Preprocessor,
	)
	return profile
}

func LoadEmbeddingProfile(dataDir string) (*EmbeddingProfile, error) {
	data, err := os.ReadFile(EmbeddingProfilePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read embedding profile: %w", err)
	}

	var profile EmbeddingProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("parse embedding profile: %w", err)
	}
	return &profile, nil
}

func SaveEmbeddingProfile(dataDir string, profile EmbeddingProfile) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("create profile directory: %w", err)
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal embedding profile: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(EmbeddingProfilePath(dataDir), data, 0644); err != nil {
		return fmt.Errorf("write embedding profile: %w", err)
	}
	return nil
}

func RemoveEmbeddingProfile(dataDir string) error {
	err := os.Remove(EmbeddingProfilePath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove embedding profile: %w", err)
	}
	return nil
}

func (p EmbeddingProfile) Matches(other EmbeddingProfile) bool {
	return p.SchemaVersion == other.SchemaVersion &&
		p.ProfileID == other.ProfileID &&
		p.Provider == other.Provider &&
		p.Model == other.Model &&
		p.Dimensions == other.Dimensions &&
		p.Distance == other.Distance &&
		p.Modality == other.Modality &&
		p.Preprocessor == other.Preprocessor
}

func (s *Service) ensureEmbeddingProfileMatches() error {
	current := CurrentEmbeddingProfile(s.session.Config)
	stored, err := LoadEmbeddingProfile(s.session.Config.DataDir)
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
	return SaveEmbeddingProfile(s.session.Config.DataDir, CurrentEmbeddingProfile(s.session.Config))
}

func (s *Service) hasIndexedChunks() bool {
	stats, err := s.session.DB.StatsForProject(s.session.ProjectRoot)
	if err != nil {
		return true
	}
	return stats["chunks"] > 0
}
