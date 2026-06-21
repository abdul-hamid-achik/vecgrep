package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

type ResetIndexFilesResult struct {
	ProjectRoot string
	VecLitePath string
}

type InitProjectResult struct {
	ProjectRoot   string
	ProjectName   string
	DataDir       string
	DBPath        string
	VecLitePath   string
	VectorBackend string
	Provider      string
	Model         string
	Global        bool
}

func InitGlobalProject(ctx context.Context, startDir string, force bool) (*InitProjectResult, error) {
	_ = ctx

	projectRoot, err := resolveProjectDir(startDir)
	if err != nil {
		return nil, err
	}

	existingName, existingEntry, _ := config.FindProjectByPath(projectRoot)
	if existingEntry != nil && !force {
		dataDir := config.ExpandPath(existingEntry.DataDir)
		if dataDir == "" {
			dataDir, err = config.GetProjectDataDir(existingName)
			if err != nil {
				return nil, fmt.Errorf("get project data dir: %w", err)
			}
		}
		return initProjectDatabase(projectRoot, existingName, dataDir, true)
	}

	if err := config.AddProjectToGlobal(projectRoot, ""); err != nil {
		return nil, fmt.Errorf("register global project: %w", err)
	}

	name, entry, _ := config.FindProjectByPath(projectRoot)
	if entry == nil {
		return nil, fmt.Errorf("global project registration was not found after save")
	}
	dataDir := config.ExpandPath(entry.DataDir)
	if dataDir == "" {
		dataDir, err = config.GetProjectDataDir(name)
		if err != nil {
			return nil, fmt.Errorf("get project data dir: %w", err)
		}
	}

	return initProjectDatabase(projectRoot, name, dataDir, true)
}

func InitLocalProject(ctx context.Context, startDir string, force bool) (*InitProjectResult, error) {
	_ = ctx

	projectRoot, err := resolveProjectDir(startDir)
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(projectRoot, config.DefaultDataDir)

	if _, err := os.Stat(dataDir); err == nil && !force {
		return nil, fmt.Errorf("vecgrep already initialized in %s", projectRoot)
	}

	result, err := initProjectDatabase(projectRoot, "", dataDir, false)
	if err != nil {
		return nil, err
	}

	cfg := config.DefaultConfig()
	cfg.DataDir = result.DataDir
	cfg.DBPath = result.DBPath
	if err := cfg.EnsureDataDir(); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	if err := cfg.WriteDefaultConfig(); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}

	return result, nil
}

func resolveProjectDir(startDir string) (string, error) {
	if startDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get cwd: %w", err)
		}
		startDir = cwd
	}

	projectRoot, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("resolve start dir: %w", err)
	}
	if info, err := os.Stat(projectRoot); err != nil {
		return "", fmt.Errorf("stat project root: %w", err)
	} else if !info.IsDir() {
		return "", fmt.Errorf("project root is not a directory: %s", projectRoot)
	}
	return projectRoot, nil
}

func initProjectDatabase(projectRoot, projectName, dataDir string, global bool) (*InitProjectResult, error) {
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir
	cfg.DBPath = filepath.Join(dataDir, config.DefaultDBFile)

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions:         cfg.Embedding.Dimensions,
		DataDir:            cfg.DataDir,
		HNSWM:              cfg.Vector.VecLite.M,
		HNSWEfConstruction: cfg.Vector.VecLite.EfConstruction,
		HNSWEfSearch:       cfg.Vector.VecLite.EfSearch,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize veclite index: %w", err)
	}
	defer database.Close()

	vecVersion, err := database.VecVersion()
	if err != nil {
		return nil, fmt.Errorf("verify vector backend: %w", err)
	}

	return &InitProjectResult{
		ProjectRoot:   projectRoot,
		ProjectName:   projectName,
		DataDir:       cfg.DataDir,
		DBPath:        cfg.DBPath,
		VecLitePath:   db.VecLitePath(cfg.DataDir),
		VectorBackend: vecVersion,
		Provider:      cfg.Embedding.Provider,
		Model:         cfg.Embedding.Model,
		Global:        global,
	}, nil
}

func ResetIndexFiles(ctx context.Context, startDir string) (*ResetIndexFilesResult, error) {
	_ = ctx

	if startDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get cwd: %w", err)
		}
		startDir = cwd
	}

	absStart, err := filepath.Abs(startDir)
	if err != nil {
		return nil, fmt.Errorf("resolve start dir: %w", err)
	}

	projectRoot, err := config.FindProjectRootFrom(absStart)
	if err != nil {
		return nil, fmt.Errorf("%w: run 'vecgrep init' first", ErrNoProject)
	}

	resolver := config.NewConfigResolution()
	resolved, err := resolver.Resolve(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}

	cfg := resolved.Config
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	vecPath := db.VecLitePath(cfg.DataDir)
	if err := os.RemoveAll(vecPath); err != nil {
		return nil, fmt.Errorf("remove veclite index: %w", err)
	}
	if err := RemoveEmbeddingProfile(cfg.DataDir); err != nil {
		return nil, err
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions:         cfg.Embedding.Dimensions,
		DataDir:            cfg.DataDir,
		HNSWM:              cfg.Vector.VecLite.M,
		HNSWEfConstruction: cfg.Vector.VecLite.EfConstruction,
		HNSWEfSearch:       cfg.Vector.VecLite.EfSearch,
	})
	if err != nil {
		return nil, fmt.Errorf("recreate veclite index: %w", err)
	}
	defer database.Close()

	return &ResetIndexFilesResult{
		ProjectRoot: projectRoot,
		VecLitePath: vecPath,
	}, nil
}
