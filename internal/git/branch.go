// Package git provides git branch detection via shell-out (no CGO).
package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// BranchInfo holds the git state of a working directory.
type BranchInfo struct {
	// Root is the repository root directory (absolute path).
	Root string
	// Branch is the current branch name (empty for detached HEAD).
	Branch string
	// HEAD is the current commit SHA (short).
	Head string
	// Detached reports whether HEAD is in detached state.
	Detached bool
}

// Detect returns the current branch info for the given directory.
// If the directory is not inside a git repository, it returns an error
// and the caller should treat the project as non-git (no branch switching).
func Detect(ctx context.Context, dir string) (*BranchInfo, error) {
	// Find the repo root
	root, err := runGit(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}
	root = strings.TrimSpace(root)

	// Get the HEAD SHA
	head, err := runGit(ctx, dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}
	head = strings.TrimSpace(head)

	// Get the current branch name (this fails on detached HEAD)
	branch, branchErr := runGit(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if branchErr != nil {
		// Likely detached HEAD
		return &BranchInfo{
			Root:     root,
			Branch:   "",
			Head:     head,
			Detached: true,
		}, nil
	}
	branch = strings.TrimSpace(branch)

	detached := branch == "HEAD"
	if detached {
		branch = ""
	}

	return &BranchInfo{
		Root:     root,
		Branch:   branch,
		Head:     head,
		Detached: detached,
	}, nil
}

// SanitizeBranch converts a branch name into a safe filesystem directory
// name by replacing path separators and other unsafe characters.
// Always returns a non-empty string.
func SanitizeBranch(name string) string {
	// Replace characters that are problematic in directory names
	replacer := strings.NewReplacer(
		"/", "-", // feature/foo -> feature-foo
		"\\", "-", // backslash
		":", "-", // windows drive colons
		" ", "-", // spaces
		"~", "-", // tilde
		"..", "--", // parent dir traversal
	)
	s := replacer.Replace(name)
	// Trim leading/trailing dashes
	s = strings.Trim(s, "-")
	if s == "" {
		return "unnamed"
	}
	return s
}

// RepoHash returns a short hash of the repository root path, useful for
// generating unique directory names when the repo root contains special
// characters.
func RepoHash(root string) string {
	// Use a simple FNV-1a hash for the path
	var h uint32 = 2166136261
	for _, c := range []byte(root) {
		h ^= uint32(c)
		h *= 16777619
	}
	return fmt.Sprintf("%x", h)
}

// IsAncestor reports whether ancestor is an ancestor of descendant.
// This is used to detect if a branch has been rebased since the last
// snapshot (if the base SHA is no longer an ancestor, the snapshot is stale).
func IsAncestor(ctx context.Context, dir, ancestor, descendant string) bool {
	_, err := runGit(ctx, dir, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

// runGit executes a git command in the given directory and returns stdout.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
