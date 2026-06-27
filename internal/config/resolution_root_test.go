package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindProjectRootFrom_GlobalDirNotAProject is a regression test for the bug
// where the global store at ~/.vecgrep (which shares the ".vecgrep" name with the
// project-local marker) was mistaken for a project root. That made the home
// directory resolve as a project and caused `vecgrep index` to walk the entire
// home tree, including ~/.asdf libraries.
func TestFindProjectRootFrom_GlobalDirNotAProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Simulate the global store living at ~/.vecgrep.
	if err := os.MkdirAll(filepath.Join(home, DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}

	// A subdirectory of home with no project marker of its own.
	sub := filepath.Join(home, "some", "where")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if root, err := FindProjectRootFrom(sub); err == nil {
		t.Fatalf("global ~/.vecgrep must not count as a project root; got %q", root)
	}

	// The home directory itself must not resolve as a project either.
	if root, err := FindProjectRootFrom(home); err == nil {
		t.Fatalf("home dir must not resolve as a project; got %q", root)
	}
}

// TestFindProjectRootFrom_RealProjectStillDetected ensures a genuine project-local
// .vecgrep marker is still detected after the global-dir guard, even when nested
// under a home directory that also contains the global ~/.vecgrep store.
func TestFindProjectRootFrom_RealProjectStillDetected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}

	proj := filepath.Join(home, "projects", "real")
	if err := os.MkdirAll(filepath.Join(proj, DefaultDataDir), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(proj, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := FindProjectRootFrom(sub)
	if err != nil {
		t.Fatalf("expected to find project root, got error: %v", err)
	}
	if root != proj {
		t.Fatalf("expected root %q, got %q", proj, root)
	}
}
