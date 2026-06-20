package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

type Session struct {
	ProjectRoot      string
	ProjectName      string
	Config           *config.Config
	Resolved         *config.ResolvedConfig
	ConfigSources    []string
	DB               *db.DB
	Provider         embed.Provider
	VecLitePath      string
	LegacyDBPath     string
	MigrationWarning string
}

type Service struct {
	session *Session
}

func OpenSession(ctx context.Context, startDir string) (*Session, error) {
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
	vecPath := db.VecLitePath(cfg.DataDir)
	legacyPath := cfg.DBPath
	if legacyPath == "" {
		legacyPath = filepath.Join(cfg.DataDir, config.DefaultDBFile)
	}

	migrationWarning := detectMigrationWarning(legacyPath, vecPath)
	if migrationWarning != "" {
		return nil, fmt.Errorf("%w: %s", ErrMigrationRequired, migrationWarning)
	}

	database, err := db.OpenWithOptions(db.OpenOptions{
		Dimensions: cfg.Embedding.Dimensions,
		DataDir:    cfg.DataDir,
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w; if this index was created by an older vecgrep/veclite version, run 'vecgrep reset --force' and then 'vecgrep index'", err)
	}

	provider, err := NewProvider(cfg)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("create provider: %w", err)
	}

	return &Session{
		ProjectRoot:      projectRoot,
		ProjectName:      resolved.ProjectName,
		Config:           cfg,
		Resolved:         resolved,
		ConfigSources:    resolver.FoundConfigFiles(),
		DB:               database,
		Provider:         provider,
		VecLitePath:      vecPath,
		LegacyDBPath:     legacyPath,
		MigrationWarning: migrationWarning,
	}, nil
}

func detectMigrationWarning(legacyPath, vecPath string) string {
	if legacyPath == "" || vecPath == "" || legacyPath == vecPath {
		return ""
	}
	if !fileExists(legacyPath) || fileExists(vecPath) {
		return ""
	}
	return fmt.Sprintf("%s exists but %s does not; rebuild or migrate the index explicitly", legacyPath, vecPath)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (s *Session) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func NewService(session *Session) *Service {
	return &Service{session: session}
}

func (s *Service) Session() *Session {
	if s == nil {
		return nil
	}
	return s.session
}

func IsNoProject(err error) bool {
	return errors.Is(err, ErrNoProject)
}

func IsMigrationRequired(err error) bool {
	return errors.Is(err, ErrMigrationRequired)
}

func IsEmbeddingProfileMismatch(err error) bool {
	return errors.Is(err, ErrEmbeddingProfileMismatch)
}
