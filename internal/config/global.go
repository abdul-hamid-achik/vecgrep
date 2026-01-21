package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// GlobalConfigDir is the directory for global vecgrep config
	GlobalConfigDir = ".vecgrep"
	// GlobalConfigFile is the global config filename
	GlobalConfigFile = "config.yaml"
	// GlobalProjectsDir is the directory for global project data
	GlobalProjectsDir = "projects"
)

// GlobalConfig represents the global ~/.vecgrep/config.yaml configuration
type GlobalConfig struct {
	// Defaults are the default settings applied to all projects
	Defaults Config `yaml:"defaults"`
	// Projects maps project names to their configurations
	Projects map[string]ProjectEntry `yaml:"projects"`
	// PreferredExtension is the preferred config file extension (yaml or yml)
	PreferredExtension string `yaml:"preferred_extension,omitempty"`
}

// ProjectEntry represents a project in the global config
type ProjectEntry struct {
	// Path is the absolute path to the project (supports ~ for home)
	Path string `yaml:"path"`
	// DataDir is where to store .vecgrep data (defaults to ~/.vecgrep/projects/{name}/)
	DataDir string `yaml:"data_dir,omitempty"`
	// Embedding overrides for this project
	Embedding *EmbeddingConfig `yaml:"embedding,omitempty"`
	// Indexing overrides for this project
	Indexing *IndexingConfig `yaml:"indexing,omitempty"`
	// Server overrides for this project
	Server *ServerConfig `yaml:"server,omitempty"`
}

// GetGlobalConfigDir returns the path to ~/.vecgrep/
func GetGlobalConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, GlobalConfigDir), nil
}

// GetGlobalConfigPath returns the path to ~/.vecgrep/config.yaml
func GetGlobalConfigPath() (string, error) {
	dir, err := GetGlobalConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, GlobalConfigFile), nil
}

// GetGlobalProjectsDir returns the path to ~/.vecgrep/projects/
func GetGlobalProjectsDir() (string, error) {
	dir, err := GetGlobalConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, GlobalProjectsDir), nil
}

// LoadGlobalConfig loads the global config from ~/.vecgrep/config.yaml
func LoadGlobalConfig() (*GlobalConfig, error) {
	configPath, err := GetGlobalConfigPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Return empty global config if file doesn't exist
			return &GlobalConfig{
				Defaults: *DefaultConfig(),
				Projects: make(map[string]ProjectEntry),
			}, nil
		}
		return nil, fmt.Errorf("failed to read global config: %w", err)
	}

	var globalCfg GlobalConfig
	if err := yaml.Unmarshal(data, &globalCfg); err != nil {
		return nil, fmt.Errorf("failed to parse global config: %w", err)
	}

	// Initialize maps if nil
	if globalCfg.Projects == nil {
		globalCfg.Projects = make(map[string]ProjectEntry)
	}

	return &globalCfg, nil
}

// SaveGlobalConfig saves the global config to ~/.vecgrep/config.yaml
func SaveGlobalConfig(cfg *GlobalConfig) error {
	configDir, err := GetGlobalConfigDir()
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create global config directory: %w", err)
	}

	configPath := filepath.Join(configDir, GlobalConfigFile)

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal global config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write global config: %w", err)
	}

	return nil
}

// EnsureGlobalConfigDir creates the global config directory if it doesn't exist
func EnsureGlobalConfigDir() error {
	dir, err := GetGlobalConfigDir()
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0755)
}

// GlobalConfigExists checks if the global config file exists
func GlobalConfigExists() bool {
	path, err := GetGlobalConfigPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// GetProjectDataDir returns the data directory for a project stored globally
func GetProjectDataDir(projectName string) (string, error) {
	projectsDir, err := GetGlobalProjectsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(projectsDir, projectName), nil
}

// DeriveProjectName derives a unique project name from a directory path.
// Uses the directory name, prepending parent if needed to avoid collisions.
func DeriveProjectName(dirPath string, existingNames map[string]bool) string {
	dirPath = ExpandPath(dirPath)
	dirName := filepath.Base(dirPath)

	// If no collision, use the directory name
	if existingNames == nil || !existingNames[dirName] {
		return dirName
	}

	// Prepend parent directory to make unique
	parent := filepath.Base(filepath.Dir(dirPath))
	uniqueName := fmt.Sprintf("%s-%s", parent, dirName)

	// If still collision, keep prepending
	fullPath := dirPath
	for existingNames[uniqueName] {
		fullPath = filepath.Dir(fullPath)
		parent = filepath.Base(fullPath)
		if parent == "/" || parent == "." {
			// Fallback: use hash of path
			uniqueName = fmt.Sprintf("%s-%d", dirName, len(dirPath))
			break
		}
		uniqueName = fmt.Sprintf("%s-%s", parent, uniqueName)
	}

	return uniqueName
}

// ExpandPath expands ~ to the user's home directory
func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	return path
}

// AddProjectToGlobal adds a project to the global config
func AddProjectToGlobal(projectPath string, name string) error {
	globalCfg, err := LoadGlobalConfig()
	if err != nil {
		return err
	}

	// Expand and make path absolute
	projectPath = ExpandPath(projectPath)
	if !filepath.IsAbs(projectPath) {
		absPath, err := filepath.Abs(projectPath)
		if err != nil {
			return fmt.Errorf("failed to get absolute path: %w", err)
		}
		projectPath = absPath
	}

	// Derive name if not provided
	if name == "" {
		existingNames := make(map[string]bool)
		for n := range globalCfg.Projects {
			existingNames[n] = true
		}
		name = DeriveProjectName(projectPath, existingNames)
	}

	// Get data directory
	dataDir, err := GetProjectDataDir(name)
	if err != nil {
		return err
	}

	// Create project entry
	globalCfg.Projects[name] = ProjectEntry{
		Path:    projectPath,
		DataDir: dataDir,
	}

	return SaveGlobalConfig(globalCfg)
}

// RemoveProjectFromGlobal removes a project from the global config
func RemoveProjectFromGlobal(name string) error {
	globalCfg, err := LoadGlobalConfig()
	if err != nil {
		return err
	}

	if _, exists := globalCfg.Projects[name]; !exists {
		return fmt.Errorf("project '%s' not found in global config", name)
	}

	delete(globalCfg.Projects, name)
	return SaveGlobalConfig(globalCfg)
}

// ListGlobalProjects returns all globally registered projects
func ListGlobalProjects() (map[string]ProjectEntry, error) {
	globalCfg, err := LoadGlobalConfig()
	if err != nil {
		return nil, err
	}
	return globalCfg.Projects, nil
}

// FindProjectByPath finds a project in global config by its path
func FindProjectByPath(projectPath string) (string, *ProjectEntry, error) {
	globalCfg, err := LoadGlobalConfig()
	if err != nil {
		return "", nil, err
	}

	projectPath = ExpandPath(projectPath)
	if !filepath.IsAbs(projectPath) {
		absPath, err := filepath.Abs(projectPath)
		if err != nil {
			return "", nil, err
		}
		projectPath = absPath
	}

	for name, entry := range globalCfg.Projects {
		entryPath := ExpandPath(entry.Path)
		if entryPath == projectPath {
			return name, &entry, nil
		}
	}

	return "", nil, nil
}
