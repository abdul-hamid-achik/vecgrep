package config

import (
	"os"
	"path/filepath"
	"testing"
)

func setupPruneFixture(t *testing.T) (livePath, staleDataDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	livePath = t.TempDir()

	if err := EnsureGlobalConfigDir(); err != nil {
		t.Fatalf("ensure config dir: %v", err)
	}
	if err := AddProjectToGlobal(livePath, "live"); err != nil {
		t.Fatalf("add live: %v", err)
	}
	if err := AddProjectToGlobal(filepath.Join(home, "gone-project"), "stale"); err != nil {
		t.Fatalf("add stale: %v", err)
	}

	// Give the stale project a data dir with real bytes in the managed area.
	projects, err := ListGlobalProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	staleDataDir = ExpandPath(projects["stale"].DataDir)
	if err := os.MkdirAll(staleDataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDataDir, "vectors.veclite"), make([]byte, 2048), 0o600); err != nil {
		t.Fatalf("write data: %v", err)
	}
	return livePath, staleDataDir
}

func TestPruneGlobalProjectsDryRun(t *testing.T) {
	_, staleDataDir := setupPruneFixture(t)

	pruned, err := PruneGlobalProjects(true, true)
	if err != nil {
		t.Fatalf("prune dry-run: %v", err)
	}
	if len(pruned) != 1 || pruned[0].Name != "stale" {
		t.Fatalf("dry-run pruned = %+v, want just 'stale'", pruned)
	}
	if pruned[0].DataBytes != 2048 {
		t.Fatalf("DataBytes = %d, want 2048", pruned[0].DataBytes)
	}
	if pruned[0].DataPurged {
		t.Fatal("dry-run must not purge data")
	}

	// Nothing actually changed.
	projects, err := ListGlobalProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("dry-run modified registry: %d entries, want 2", len(projects))
	}
	if _, err := os.Stat(staleDataDir); err != nil {
		t.Fatalf("dry-run deleted data dir: %v", err)
	}
}

func TestPruneGlobalProjectsPurge(t *testing.T) {
	_, staleDataDir := setupPruneFixture(t)

	pruned, err := PruneGlobalProjects(false, true)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0].Name != "stale" || !pruned[0].DataPurged {
		t.Fatalf("pruned = %+v, want stale with data purged", pruned)
	}

	projects, err := ListGlobalProjects()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, ok := projects["stale"]; ok {
		t.Fatal("stale entry survived prune")
	}
	if _, ok := projects["live"]; !ok {
		t.Fatal("live entry was pruned")
	}
	if _, err := os.Stat(staleDataDir); !os.IsNotExist(err) {
		t.Fatalf("stale data dir survived purge (err=%v)", err)
	}
}

func TestPruneGlobalProjectsKeepsDataWithoutPurge(t *testing.T) {
	_, staleDataDir := setupPruneFixture(t)

	pruned, err := PruneGlobalProjects(false, false)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0].DataPurged {
		t.Fatalf("pruned = %+v, want stale entry with data kept", pruned)
	}
	if _, err := os.Stat(staleDataDir); err != nil {
		t.Fatalf("data dir deleted without --purge-data: %v", err)
	}
}

func TestPruneRefusesUnmanagedDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "precious.txt"), []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureGlobalConfigDir(); err != nil {
		t.Fatalf("ensure config dir: %v", err)
	}
	cfg, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Projects["rogue"] = ProjectEntry{
		Path:    filepath.Join(home, "gone"),
		DataDir: outside, // hand-edited to point outside ~/.vecgrep/projects
	}
	if err := SaveGlobalConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	pruned, err := PruneGlobalProjects(false, true)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if len(pruned) != 1 || pruned[0].DataPurged || pruned[0].DataBytes != 0 {
		t.Fatalf("pruned = %+v, want entry removed but unmanaged data untouched", pruned)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious.txt")); err != nil {
		t.Fatalf("prune deleted data outside ~/.vecgrep/projects: %v", err)
	}
}
