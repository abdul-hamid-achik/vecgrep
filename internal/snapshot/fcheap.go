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
	"time"
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

// SweepResult holds the output of a fcheap vacuum (sweep) operation.
type SweepResult struct {
	// Swept is the number of orphaned stashes removed.
	Swept int `json:"swept"`
}

// Sweep runs fcheap vacuum to remove orphaned metadata- and search-index
// entries for stashes whose directory no longer exists, then compacts the
// database. This is the periodic cleanup operation. It is best-effort: if
// fcheap is not available, it returns an error without side effects.
func (f *Fcheap) Sweep(ctx context.Context) (*SweepResult, error) {
	if !f.Available() {
		return nil, fmt.Errorf("fcheap not available")
	}

	out, err := f.exec(ctx, "vacuum", "--json")
	if err != nil {
		return nil, fmt.Errorf("fcheap vacuum: %w", err)
	}

	// Try to parse JSON output; fcheap vacuum --json may output a count
	// or a status object. We do a best-effort parse.
	var result SweepResult
	if jsonErr := json.Unmarshal([]byte(out), &result); jsonErr != nil {
		// If not JSON, return with swept=0 since we can't confirm the count.
		// The vacuum still ran successfully.
		return &SweepResult{Swept: 0}, nil
	}
	return &result, nil
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

// ExecRaw runs an arbitrary fcheap command and returns stdout/stderr.
// It is a low-level escape hatch used by callers that need fcheap
// subcommands not wrapped by this type (e.g. cleanup-smart, sweep).
func (f *Fcheap) ExecRaw(ctx context.Context, args ...string) (string, error) {
	if !f.Available() {
		return "", fmt.Errorf("fcheap not available")
	}
	cmd := exec.CommandContext(ctx, f.bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// ListEntryDetailed extends ListEntry with timestamp information so callers
// can pick the most recent stash matching a set of tags.
type ListEntryDetailed struct {
	ListEntry
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListDetailed returns stashes with timestamp metadata, filtered by tags.
// Pass nil for all stashes.
func (f *Fcheap) ListDetailed(ctx context.Context, tags []string) ([]ListEntryDetailed, error) {
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

	var entries []ListEntryDetailed
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, fmt.Errorf("fcheap list: failed to parse output: %w", err)
	}
	return entries, nil
}

// LatestByTags returns the most recently created stash matching all the
// given tags. Returns nil (no error) when no stash matches.
func (f *Fcheap) LatestByTags(ctx context.Context, tags []string) (*ListEntryDetailed, error) {
	entries, err := f.ListDetailed(ctx, tags)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	latest := &entries[0]
	for i := 1; i < len(entries); i++ {
		if entries[i].CreatedAt.After(latest.CreatedAt) {
			latest = &entries[i]
		}
	}
	return latest, nil
}

// SaveFile stashes a single file (not a directory) to fcheap. It is a thin
// wrapper around Save for the common embedding-cache case where the cache
// is a single bbolt file.
func (f *Fcheap) SaveFile(ctx context.Context, path, name, tool string, tags []string) (*SaveResult, error) {
	return f.Save(ctx, path, name, tool, tags)
}
