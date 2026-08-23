package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupHomeWithProjects(t *testing.T, home string, projects map[string]ProjectEntry) {
	t.Helper()
	t.Setenv("HOME", home)

	globalDir := filepath.Join(home, DefaultDataDir)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobalConfig(&GlobalConfig{Projects: projects}); err != nil {
		t.Fatal(err)
	}
}

func TestFindDeepestRegisteredProjectContaining_PrefersChild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setupHomeWithProjects(t, home, map[string]ProjectEntry{
		"projects":   {Path: filepath.Join(home, "projects")},
		"pasaprecio": {Path: filepath.Join(home, "projects", "pasaprecio")},
	})

	cwd := filepath.Join(home, "projects", "pasaprecio", "src")
	name, entry, ok := FindDeepestRegisteredProjectContaining(cwd)
	if !ok {
		t.Fatal("expected a registered project")
	}
	if name != "pasaprecio" {
		t.Fatalf("expected pasaprecio, got %q", name)
	}
	if entry.Path != filepath.Join(home, "projects", "pasaprecio") {
		t.Fatalf("unexpected entry path %q", entry.Path)
	}
}

func TestFindProjectRootFrom_AncestorShadowsNestedGitRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setupHomeWithProjects(t, home, map[string]ProjectEntry{
		"projects": {Path: filepath.Join(home, "projects")},
	})

	monorepo := filepath.Join(home, "projects")
	child := filepath.Join(monorepo, "pasaprecio")
	if err := os.MkdirAll(filepath.Join(child, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := FindProjectRootFrom(filepath.Join(child, "src"))
	if err == nil {
		t.Fatal("expected nested boundary error, got nil")
	}
	if !errors.Is(err, ErrNestedProjectBoundary) {
		t.Fatalf("expected ErrNestedProjectBoundary, got %v", err)
	}
	if !strings.Contains(err.Error(), "vecgrep init") {
		t.Fatalf("expected init hint, got %v", err)
	}
}

func TestFindProjectRootFrom_ChildRegistrationWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setupHomeWithProjects(t, home, map[string]ProjectEntry{
		"projects":   {Path: filepath.Join(home, "projects")},
		"pasaprecio": {Path: filepath.Join(home, "projects", "pasaprecio")},
	})

	child := filepath.Join(home, "projects", "pasaprecio")
	if err := os.MkdirAll(filepath.Join(child, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(child, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := FindProjectRootFrom(filepath.Join(child, "src"))
	if err != nil {
		t.Fatalf("expected child project root, got error: %v", err)
	}
	want := filepath.Join(home, "projects", "pasaprecio")
	if root != want {
		t.Fatalf("expected %q, got %q", want, root)
	}
}

func TestFindProjectRootFrom_MonorepoSubdirWithoutBoundaryUsesAncestor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setupHomeWithProjects(t, home, map[string]ProjectEntry{
		"projects": {Path: filepath.Join(home, "projects")},
	})

	monorepo := filepath.Join(home, "projects")
	sub := filepath.Join(monorepo, "shared", "lib")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := FindProjectRootFrom(sub)
	if err != nil {
		t.Fatalf("expected monorepo root, got error: %v", err)
	}
	if root != monorepo {
		t.Fatalf("expected %q, got %q", monorepo, root)
	}
}
