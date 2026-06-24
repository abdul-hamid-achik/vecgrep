package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/config"
	"github.com/abdul-hamid-achik/vecgrep/internal/git"
	"github.com/abdul-hamid-achik/vecgrep/internal/snapshot"
)

// BranchIndexFile is the pointer/state file that tracks per-branch index
// metadata. Stored at ~/.vecgrep/projects/<name>/branches/index.json.
const BranchIndexFile = "index.json"

// BranchIndex tracks all branch indexes for a project.
type BranchIndex struct {
	RepoRoot      string                  `json:"repo_root"`
	RepoHash      string                  `json:"repo_hash"`
	DefaultBranch string                  `json:"default_branch,omitempty"`
	ActiveBranch  string                  `json:"active_branch"`
	Branches      map[string]*BranchEntry `json:"branches"`
}

// BranchEntry holds metadata for a single branch's index.
type BranchEntry struct {
	StashID          string           `json:"stash_id,omitempty"`
	BaseSHA          string           `json:"base_sha"`
	EmbeddingProfile EmbeddingProfile `json:"embedding_profile"`
	VectorCount      int64            `json:"vector_count"`
	LastSwitchedAt   time.Time        `json:"last_switched_at"`
}

// BranchSwitchResult holds the outcome of a branch switch operation.
type BranchSwitchResult struct {
	FromBranch  string
	ToBranch    string
	FromSHA     string
	ToSHA       string
	Restored    bool  // true if restored from snapshot, false if fresh-indexed
	VectorCount int64 // vectors in the target branch index
	Duration    time.Duration
	SnapshotID  string // fcheap stash ID if snapshot was taken
}

// BranchSnapshot creates a snapshot of the current branch's index and
// stores it in the fcheap vault. It also updates the branch index pointer
// file with the stash ID and base SHA.
func BranchSnapshot(ctx context.Context, projectRoot, projectName string) (*BranchSwitchResult, error) {
	start := time.Now()

	// Detect git state
	info, err := git.Detect(ctx, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("detect git: %w", err)
	}
	if info.Detached {
		return nil, fmt.Errorf("cannot snapshot in detached HEAD state")
	}

	// Resolve the branch data dir
	sanitized := git.SanitizeBranch(info.Branch)
	branchDir, err := config.GetProjectBranchDataDir(projectName, sanitized)
	if err != nil {
		return nil, fmt.Errorf("resolve branch data dir: %w", err)
	}

	// Read the current embedding profile from the branch's DB
	resolved, err := config.LoadResolved(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	currentProfile := CurrentEmbeddingProfile(resolved.Config)

	// Get vector count from the DB
	session, err := OpenReadOnlySession(ctx, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}
	defer session.Close()

	stats, err := session.DB.StatsForProject(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("get stats: %w", err)
	}
	vectorCount := stats["chunks"]

	// Snapshot via fcheap
	f := snapshot.NewFcheap()
	var stashID string
	if f.Available() {
		tags := []string{
			"vecgrep-index",
			"repo:" + git.RepoHash(info.Root),
			"branch:" + sanitized,
		}
		result, saveErr := f.Save(ctx, branchDir, "vecgrep-"+projectName+"-"+sanitized, "vecgrep", tags)
		if saveErr == nil && result != nil {
			stashID = result.StashID
		}
	}

	// Update the pointer file
	idx, err := loadBranchIndex(projectName)
	if err != nil {
		idx = &BranchIndex{Branches: make(map[string]*BranchEntry)}
	}
	idx.RepoRoot = info.Root
	idx.RepoHash = git.RepoHash(info.Root)
	idx.ActiveBranch = info.Branch
	if idx.Branches == nil {
		idx.Branches = make(map[string]*BranchEntry)
	}
	idx.Branches[sanitized] = &BranchEntry{
		StashID:          stashID,
		BaseSHA:          info.Head,
		EmbeddingProfile: currentProfile,
		VectorCount:      vectorCount,
		LastSwitchedAt:   time.Now(),
	}
	if err := saveBranchIndex(projectName, idx); err != nil {
		return nil, fmt.Errorf("save branch index: %w", err)
	}

	return &BranchSwitchResult{
		ToBranch:    info.Branch,
		ToSHA:       info.Head,
		SnapshotID:  stashID,
		VectorCount: vectorCount,
		Duration:    time.Since(start),
	}, nil
}

// BranchSwitch switches the active index to a target branch. If a valid
// fcheap snapshot exists for the target branch (matching base SHA and
// embedding profile), it restores the snapshot. Otherwise, it opens the
// branch directory and indexes the diff.
//
// The caller should close any existing session before calling this, as
// it needs exclusive access to the branch directory.
func BranchSwitch(ctx context.Context, projectRoot, projectName, targetBranch string) (*BranchSwitchResult, error) {
	start := time.Now()

	// Detect current git state
	info, err := git.Detect(ctx, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("detect git: %w", err)
	}

	fromBranch := info.Branch
	fromSHA := info.Head

	// Sanitize the target branch name
	sanitized := git.SanitizeBranch(targetBranch)
	branchDir, err := config.GetProjectBranchDataDir(projectName, sanitized)
	if err != nil {
		return nil, fmt.Errorf("resolve target branch dir: %w", err)
	}

	// Ensure the branch directory exists
	if mkErr := os.MkdirAll(branchDir, 0755); mkErr != nil {
		return nil, fmt.Errorf("create branch dir: %w", mkErr)
	}

	// Load the branch index
	idx, err := loadBranchIndex(projectName)
	if err != nil {
		idx = &BranchIndex{Branches: make(map[string]*BranchEntry)}
	}

	// Load current config to get the embedding profile
	resolved, err := config.LoadResolved(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	currentProfile := CurrentEmbeddingProfile(resolved.Config)

	// Try to restore from fcheap snapshot
	restored := false
	f := snapshot.NewFcheap()
	entry := idx.Branches[sanitized]

	if f.Available() && entry != nil && entry.StashID != "" {
		// Check embedding profile compatibility
		if entry.EmbeddingProfile.Matches(currentProfile) {
			// Check if the base SHA is still reachable (not rebased away)
			if entry.BaseSHA != "" && info.Head != "" {
				if git.IsAncestor(ctx, projectRoot, entry.BaseSHA, info.Head) {
					// Restore the snapshot
					if restoreErr := f.Restore(ctx, entry.StashID, branchDir); restoreErr == nil {
						restored = true
					}
				}
			}
		}
	}

	// Update the pointer file
	idx.RepoRoot = info.Root
	idx.RepoHash = git.RepoHash(info.Root)
	idx.ActiveBranch = targetBranch
	if idx.Branches == nil {
		idx.Branches = make(map[string]*BranchEntry)
	}
	if idx.Branches[sanitized] == nil {
		idx.Branches[sanitized] = &BranchEntry{}
	}
	idx.Branches[sanitized].BaseSHA = info.Head
	idx.Branches[sanitized].EmbeddingProfile = currentProfile
	idx.Branches[sanitized].LastSwitchedAt = time.Now()

	// Get vector count (best-effort — may not be available if not yet indexed)
	var vectorCount int64
	// We can't easily read the DB without opening it, so skip for now.
	// The status command will read this from the opened DB.
	idx.Branches[sanitized].VectorCount = vectorCount

	if err := saveBranchIndex(projectName, idx); err != nil {
		return nil, fmt.Errorf("save branch index: %w", err)
	}

	return &BranchSwitchResult{
		FromBranch:  fromBranch,
		ToBranch:    targetBranch,
		FromSHA:     fromSHA,
		ToSHA:       info.Head,
		Restored:    restored,
		VectorCount: vectorCount,
		Duration:    time.Since(start),
	}, nil
}

// BranchStatus returns information about the current branch and all known
// branch indexes for the project.
func BranchStatus(ctx context.Context, projectRoot, projectName string) (*BranchIndex, *git.BranchInfo, error) {
	info, err := git.Detect(ctx, projectRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("detect git: %w", err)
	}

	idx, err := loadBranchIndex(projectName)
	if err != nil {
		idx = &BranchIndex{Branches: make(map[string]*BranchEntry)}
	}

	return idx, info, nil
}

// loadBranchIndex reads the branch index pointer file.
func loadBranchIndex(projectName string) (*BranchIndex, error) {
	baseDir, err := config.GetProjectDataDir(projectName)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(baseDir, "branches", BranchIndexFile)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var idx BranchIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse branch index: %w", err)
	}
	if idx.Branches == nil {
		idx.Branches = make(map[string]*BranchEntry)
	}
	return &idx, nil
}

// saveBranchIndex writes the branch index pointer file atomically.
func saveBranchIndex(projectName string, idx *BranchIndex) error {
	baseDir, err := config.GetProjectDataDir(projectName)
	if err != nil {
		return err
	}
	dir := filepath.Join(baseDir, "branches")
	if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
		return fmt.Errorf("create branches dir: %w", mkErr)
	}

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal branch index: %w", err)
	}

	// Atomic write: temp file + rename
	path := filepath.Join(dir, BranchIndexFile)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
