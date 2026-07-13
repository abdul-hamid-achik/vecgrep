package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	vlsession "github.com/abdul-hamid-achik/veclite/session"
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
	session            *Session
	indexCoordinatorMu sync.Mutex
	indexCoordinator   *IndexCoordinator
	manifestSource     *codemapStructuralManifestSource
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
		Dimensions:         cfg.Embedding.Dimensions,
		DataDir:            cfg.DataDir,
		HNSWM:              cfg.Vector.VecLite.M,
		HNSWEfConstruction: cfg.Vector.VecLite.EfConstruction,
		HNSWEfSearch:       cfg.Vector.VecLite.EfSearch,
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", openErrorHint(err))
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

// OpenDaemonSession opens the same project/database session as OpenSession but
// replaces the general CLI provider with one daemon-configured throttle layer.
// This prevents the daemon from wrapping an already-throttled provider while
// keeping provider lifetime owned by Session.
func OpenDaemonSession(ctx context.Context, startDir string) (*Session, error) {
	session, err := OpenSession(ctx, startDir)
	if err != nil {
		return nil, err
	}

	// The provider has not served any request yet. Close it before opening the
	// daemon provider because both may own the same disk-cache file.
	if err := closeProvider(session.Provider); err != nil {
		_ = session.DB.Close()
		return nil, fmt.Errorf("close general provider: %w", err)
	}
	session.Provider = nil
	provider, err := NewDaemonProvider(session.Config)
	if err != nil {
		_ = session.DB.Close()
		return nil, fmt.Errorf("create daemon provider: %w", err)
	}
	session.Provider = provider
	return session, nil
}

// openErrorHint wraps a database-open error with actionable guidance. A live
// file-lock (another running vecgrep process) and a stale/old-version index
// need very different remedies, so we must not blanket-suggest
// 'vecgrep reset --force' — that is destructive and only appropriate for an
// index left by an older vecgrep/veclite version, never for a lock held by a
// process that is still alive (veclite already auto-clears locks from dead
// processes before surfacing ErrFileLocked).
func openErrorHint(err error) error {
	if errors.Is(err, vlsession.ErrFileLocked) {
		return fmt.Errorf("%w; another vecgrep process holds the index lock (a daemon, a `serve --mcp`, or another command). Stop it — e.g. `vecgrep daemon stop` — or wait for it to finish, then retry", err)
	}
	return fmt.Errorf("%w; if this index was created by an older vecgrep/veclite version, run 'vecgrep reset --force' and then 'vecgrep index'", err)
}

// OpenReadOnlySession opens a read-only session that uses a shared file lock,
// allowing multiple processes to read the same database simultaneously (e.g.
// running `vecgrep search` while the studio TUI is running). The session does
// not create an embedding provider since no writes are needed.
func OpenReadOnlySession(ctx context.Context, startDir string) (*Session, error) {
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
		Dimensions:         cfg.Embedding.Dimensions,
		DataDir:            cfg.DataDir,
		HNSWM:              cfg.Vector.VecLite.M,
		HNSWEfConstruction: cfg.Vector.VecLite.EfConstruction,
		HNSWEfSearch:       cfg.Vector.VecLite.EfSearch,
		ReadOnly:           true,
		SharedRead:         true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database (read-only): %w", openErrorHint(err))
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
	if s == nil {
		return nil
	}
	providerErr := closeProvider(s.Provider)
	var dbErr error
	if s.DB != nil {
		dbErr = s.DB.Close()
	}
	return errors.Join(providerErr, dbErr)
}

func closeProvider(provider embed.Provider) error {
	switch closer := provider.(type) {
	case interface{ Close() error }:
		return closer.Close()
	case interface{ Close() }:
		closer.Close()
	}
	return nil
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
