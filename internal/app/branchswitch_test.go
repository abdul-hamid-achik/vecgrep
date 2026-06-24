package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
)

func TestBranchIndexSaveLoadRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectName := "test-branch-project"

	// Create the project in global config so loadBranchIndex can find it
	if err := config.AddProjectToGlobal("/tmp/"+projectName, ""); err != nil {
		t.Fatalf("AddProjectToGlobal: %v", err)
	}

	idx := &BranchIndex{
		RepoRoot:     "/tmp/repo",
		RepoHash:     "abc123",
		ActiveBranch: "main",
		Branches: map[string]*BranchEntry{
			"main": {
				BaseSHA:     "abc123",
				VectorCount: 42,
			},
		},
	}

	if err := saveBranchIndex(projectName, idx); err != nil {
		t.Fatalf("saveBranchIndex: %v", err)
	}

	loaded, err := loadBranchIndex(projectName)
	if err != nil {
		t.Fatalf("loadBranchIndex: %v", err)
	}

	if loaded.ActiveBranch != "main" {
		t.Errorf("active_branch = %q, want main", loaded.ActiveBranch)
	}
	if loaded.Branches["main"].BaseSHA != "abc123" {
		t.Errorf("base_sha = %q, want abc123", loaded.Branches["main"].BaseSHA)
	}
	if loaded.Branches["main"].VectorCount != 42 {
		t.Errorf("vector_count = %d, want 42", loaded.Branches["main"].VectorCount)
	}
}

func TestLoadBranchIndexMissingFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectName := "test-branch-project-missing"

	// Create the project in global config
	if err := config.AddProjectToGlobal("/tmp/"+projectName, ""); err != nil {
		t.Fatalf("AddProjectToGlobal: %v", err)
	}

	_, err := loadBranchIndex(projectName)
	if err == nil {
		t.Fatal("expected error when branch index file does not exist")
	}
}

func TestSaveBranchIndexCreatesDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectName := "test-branch-mkdir"

	if err := config.AddProjectToGlobal("/tmp/"+projectName, ""); err != nil {
		t.Fatalf("AddProjectToGlobal: %v", err)
	}

	idx := &BranchIndex{
		ActiveBranch: "feature",
		Branches: map[string]*BranchEntry{
			"feature": {
				BaseSHA: "def456",
			},
		},
	}

	if err := saveBranchIndex(projectName, idx); err != nil {
		t.Fatalf("saveBranchIndex: %v", err)
	}

	// Verify the branches directory was created
	dataDir, _ := config.GetProjectDataDir(projectName)
	_, err := os.Stat(filepath.Join(dataDir, "branches", "index.json"))
	if err != nil {
		t.Fatalf("branch index file was not created: %v", err)
	}
}

func TestBranchStatusNonGitReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	projectRoot := t.TempDir() // not a git repo

	_, _, err := BranchStatus(context.Background(), projectRoot, "test-project")
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
}
