// Package config provides configuration management for vecgrep.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Profile represents a search profile configuration.
// Profiles allow searching different content sources (code, notes, docs, etc.).
type Profile struct {
	// Name is the unique identifier for this profile.
	Name string `yaml:"name" mapstructure:"name"`

	// Description is a human-readable description of this profile.
	Description string `yaml:"description,omitempty" mapstructure:"description"`

	// Source configures the content source for this profile.
	Source SourceConfig `yaml:"source" mapstructure:"source"`

	// Indexing configures indexing behavior for this profile.
	Indexing ProfileIndexingConfig `yaml:"indexing,omitempty" mapstructure:"indexing"`

	// DataDir is the directory where this profile's data is stored.
	// If empty, defaults to ~/.vecgrep/profiles/{name}/
	DataDir string `yaml:"data_dir,omitempty" mapstructure:"data_dir"`
}

// SourceConfig configures a content source.
type SourceConfig struct {
	// Type is the source type: "files", "noted", "custom"
	Type string `yaml:"type" mapstructure:"type"`

	// Path is the root path for file-based sources.
	Path string `yaml:"path,omitempty" mapstructure:"path"`

	// Command is the command to run for custom sources (e.g., "noted export --format json").
	Command string `yaml:"command,omitempty" mapstructure:"command"`

	// BinaryPath is the path to the source binary (for noted, etc.).
	BinaryPath string `yaml:"binary_path,omitempty" mapstructure:"binary_path"`

	// PollInterval is the polling interval in seconds for watching changes.
	PollInterval int `yaml:"poll_interval,omitempty" mapstructure:"poll_interval"`
}

// ProfileIndexingConfig holds indexing settings specific to a profile.
type ProfileIndexingConfig struct {
	// ChunkSize is the target chunk size in characters.
	ChunkSize int `yaml:"chunk_size,omitempty" mapstructure:"chunk_size"`

	// ChunkOverlap is the overlap between chunks in characters.
	ChunkOverlap int `yaml:"chunk_overlap,omitempty" mapstructure:"chunk_overlap"`

	// IgnorePatterns are glob patterns to ignore during indexing.
	IgnorePatterns []string `yaml:"ignore_patterns,omitempty" mapstructure:"ignore_patterns"`
}

// ProfilesConfig holds all profile configurations.
type ProfilesConfig struct {
	// Profiles is a map of profile name to profile configuration.
	Profiles map[string]Profile `yaml:"profiles" mapstructure:"profiles"`

	// DefaultProfile is the name of the default profile to use.
	DefaultProfile string `yaml:"default_profile,omitempty" mapstructure:"default_profile"`
}

// GetProfilesDir returns the directory where profile configs are stored.
func GetProfilesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".vecgrep", "profiles"), nil
}

// LoadProfiles loads all profile configurations.
func LoadProfiles() (*ProfilesConfig, error) {
	dir, err := GetProfilesDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(dir, "profiles.yaml")

	// Return empty config if file doesn't exist
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return &ProfilesConfig{
			Profiles: make(map[string]Profile),
		}, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read profiles config: %w", err)
	}

	var cfg ProfilesConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse profiles config: %w", err)
	}

	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]Profile)
	}

	return &cfg, nil
}

// SaveProfiles saves the profiles configuration.
func SaveProfiles(cfg *ProfilesConfig) error {
	dir, err := GetProfilesDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create profiles directory: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal profiles config: %w", err)
	}

	configPath := filepath.Join(dir, "profiles.yaml")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write profiles config: %w", err)
	}

	return nil
}

// GetProfile returns a profile by name.
func GetProfile(name string) (*Profile, error) {
	cfg, err := LoadProfiles()
	if err != nil {
		return nil, err
	}

	profile, exists := cfg.Profiles[name]
	if !exists {
		return nil, fmt.Errorf("profile '%s' not found", name)
	}

	return &profile, nil
}

// AddProfile adds a new profile.
func AddProfile(profile Profile) error {
	cfg, err := LoadProfiles()
	if err != nil {
		return err
	}

	if _, exists := cfg.Profiles[profile.Name]; exists {
		return fmt.Errorf("profile '%s' already exists", profile.Name)
	}

	cfg.Profiles[profile.Name] = profile
	return SaveProfiles(cfg)
}

// UpdateProfile updates an existing profile.
func UpdateProfile(profile Profile) error {
	cfg, err := LoadProfiles()
	if err != nil {
		return err
	}

	if _, exists := cfg.Profiles[profile.Name]; !exists {
		return fmt.Errorf("profile '%s' not found", profile.Name)
	}

	cfg.Profiles[profile.Name] = profile
	return SaveProfiles(cfg)
}

// DeleteProfile removes a profile.
func DeleteProfile(name string) error {
	cfg, err := LoadProfiles()
	if err != nil {
		return err
	}

	if _, exists := cfg.Profiles[name]; !exists {
		return fmt.Errorf("profile '%s' not found", name)
	}

	delete(cfg.Profiles, name)
	return SaveProfiles(cfg)
}

// ListProfiles returns all profile names.
func ListProfiles() ([]string, error) {
	cfg, err := LoadProfiles()
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	return names, nil
}

// GetProfileDataDir returns the data directory for a profile.
func GetProfileDataDir(name string) (string, error) {
	profile, err := GetProfile(name)
	if err != nil {
		return "", err
	}

	if profile.DataDir != "" {
		return profile.DataDir, nil
	}

	// Default location
	dir, err := GetProfilesDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, name, "data"), nil
}

// DefaultFilesProfile returns a default profile for file-based code search.
func DefaultFilesProfile(name, path string) Profile {
	return Profile{
		Name:        name,
		Description: "Code files in " + path,
		Source: SourceConfig{
			Type: "files",
			Path: path,
		},
		Indexing: ProfileIndexingConfig{
			ChunkSize:    2048,
			ChunkOverlap: 256,
		},
	}
}

// DefaultNotedProfile returns a default profile for noted CLI integration.
func DefaultNotedProfile() Profile {
	return Profile{
		Name:        "notes",
		Description: "Notes from noted CLI",
		Source: SourceConfig{
			Type:         "noted",
			PollInterval: 30,
		},
		Indexing: ProfileIndexingConfig{
			ChunkSize:    1024,
			ChunkOverlap: 128,
		},
	}
}
