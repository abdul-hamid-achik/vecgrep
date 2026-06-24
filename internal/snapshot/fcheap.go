// Package snapshot provides content-addressed snapshot management via the
// fcheap vault. It wraps the fcheap CLI for save/restore/list operations,
// used by the per-branch index switching feature.
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Fcheap is a thin exec wrapper around the fcheap CLI. It resolves the
// fcheap binary from $PATH and provides Save/Restore/List operations.
type Fcheap struct {
	bin string
}

// NewFcheap creates a new fcheap wrapper. If the fcheap binary cannot be
// found on $PATH, the returned wrapper's Available() method returns false
// and all operations become no-ops.
func NewFcheap() *Fcheap {
	bin, err := exec.LookPath("fcheap")
	if err != nil {
		return &Fcheap{bin: ""}
	}
	return &Fcheap{bin: bin}
}

// Available reports whether the fcheap binary is usable.
func (f *Fcheap) Available() bool {
	return f != nil && f.bin != ""
}

// SaveResult holds the output of a fcheap save operation.
type SaveResult struct {
	StashID string `json:"stash_id"`
	Name    string `json:"name"`
	Tool    string `json:"tool"`
}

// Save stores a directory in the fcheap vault and returns the stash ID.
// tags are applied to the stash for later filtering.
func (f *Fcheap) Save(ctx context.Context, dir, name, tool string, tags []string) (*SaveResult, error) {
	if !f.Available() {
		return nil, fmt.Errorf("fcheap not available")
	}

	args := []string{"save", "--json", "--path", dir, "--name", name, "--tool", tool}
	for _, tag := range tags {
		args = append(args, "--tag", tag)
	}

	out, err := f.exec(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("fcheap save: %w", err)
	}

	// Try to parse JSON output; fcheap may output non-JSON on some flags
	var result SaveResult
	if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
		// If not JSON, try to extract the stash ID from plain text
		stashID := strings.TrimSpace(out)
		if stashID != "" {
			return &SaveResult{StashID: stashID, Name: name, Tool: tool}, nil
		}
		return nil, fmt.Errorf("fcheap save: failed to parse output: %w", jsonErr)
	}
	return &result, nil
}

// Restore extracts a stash to the target directory.
func (f *Fcheap) Restore(ctx context.Context, stashID, targetDir string) error {
	if !f.Available() {
		return fmt.Errorf("fcheap not available")
	}

	_, err := f.exec(ctx, "restore", "--json", stashID, "--target", targetDir)
	if err != nil {
		return fmt.Errorf("fcheap restore: %w", err)
	}
	return nil
}

// ListEntry holds a stashed item from fcheap list.
type ListEntry struct {
	StashID string   `json:"stash_id"`
	Name    string   `json:"name"`
	Tool    string   `json:"tool"`
	Tags    []string `json:"tags"`
}

// List returns stashes filtered by tags. Pass nil for all stashes.
func (f *Fcheap) List(ctx context.Context, tags []string) ([]ListEntry, error) {
	if !f.Available() {
		return nil, fmt.Errorf("fcheap not available")
	}

	args := []string{"list", "--json"}
	for _, tag := range tags {
		args = append(args, "--tag", tag)
	}

	out, err := f.exec(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("fcheap list: %w", err)
	}

	var entries []ListEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, fmt.Errorf("fcheap list: failed to parse output: %w", err)
	}
	return entries, nil
}

// exec runs a fcheap command and returns stdout.
func (f *Fcheap) exec(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, f.bin, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
