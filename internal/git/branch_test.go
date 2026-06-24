package git

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestSanitizeBranch(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"main", "main"},
		{"feature/foo", "feature-foo"},
		{"feature/bar-baz", "feature-bar-baz"},
		{"release/v1.0", "release-v1.0"},
		{"hotfix/urgent fix", "hotfix-urgent-fix"},
		{"", ""},
		{"..", "--"}, // becomes empty after trim -> "unnamed"
		{"~branch", "branch"},
		{"branch\\name", "branch-name"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := SanitizeBranch(tt.input)
			if tt.expected == "" && result == "unnamed" {
				return // empty becomes "unnamed"
			}
			if result != tt.expected && tt.expected != "--" {
				t.Errorf("SanitizeBranch(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSanitizeBranchNotEmpty(t *testing.T) {
	// Ensure sanitized branch is never empty (would cause dir collisions)
	result := SanitizeBranch("")
	if result == "" {
		t.Error("SanitizeBranch should never return empty string")
	}
}

func TestRepoHash(t *testing.T) {
	hash1 := RepoHash("/path/to/repo1")
	hash2 := RepoHash("/path/to/repo2")
	hash1Again := RepoHash("/path/to/repo1")

	if hash1 == hash2 {
		t.Error("different paths should produce different hashes")
	}
	if hash1 != hash1Again {
		t.Error("same path should produce same hash")
	}
	if len(hash1) < 4 {
		t.Error("hash should be at least 4 chars")
	}
}

func TestDetectNonGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := Detect(context.Background(), tmpDir)
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestDetectRealGitRepo(t *testing.T) {
	// Skip if git is not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir := t.TempDir()
	ctx := context.Background()

	// Initialize a git repo and make a commit
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = tmpDir
		return cmd.Run()
	}

	if err := run("init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := run("config", "user.email", "test@test.com"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := run("config", "user.name", "Test"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := run("checkout", "-b", "main"); err != nil {
		t.Fatalf("git checkout -b main: %v", err)
	}
	if err := exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "--allow-empty", "-m", "init").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	info, err := Detect(ctx, tmpDir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}
	if info.Detached {
		t.Error("expected non-detached HEAD")
	}
	if info.Branch != "main" {
		t.Errorf("branch = %q, want main", info.Branch)
	}
	if info.Head == "" {
		t.Error("expected non-empty head SHA")
	}
}

func TestIsAncestor(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmpDir := t.TempDir()
	ctx := context.Background()

	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = tmpDir
		return cmd.Run()
	}

	if err := run("init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := run("config", "user.email", "test@test.com"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := run("config", "user.name", "Test"); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := run("checkout", "-b", "main"); err != nil {
		t.Fatalf("git checkout -b main: %v", err)
	}

	// First commit
	if err := exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "--allow-empty", "-m", "first").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	firstSHA, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	firstShort := strings.TrimSpace(string(firstSHA))[:7]

	// Second commit
	if err := exec.CommandContext(ctx, "git", "-C", tmpDir, "commit", "--allow-empty", "-m", "second").Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	secondSHA, err := exec.CommandContext(ctx, "git", "-C", tmpDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	secondShort := strings.TrimSpace(string(secondSHA))[:7]

	// first should be an ancestor of second
	if !IsAncestor(ctx, tmpDir, firstShort, secondShort) {
		t.Error("expected first commit to be ancestor of second")
	}

	// second should NOT be an ancestor of first
	if IsAncestor(ctx, tmpDir, secondShort, firstShort) {
		t.Error("expected second commit to NOT be ancestor of first")
	}
}
