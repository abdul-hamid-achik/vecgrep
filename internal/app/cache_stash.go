package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/vecgrep/internal/snapshot"
)

// NewFcheapWrapper returns a fcheap CLI wrapper. It is the public entry
// point for callers (e.g. CLI commands) that need to check fcheap
// availability via Available().
func NewFcheapWrapper() *snapshot.Fcheap {
	return snapshot.NewFcheap()
}

// repoHashShort returns the first 8 hex chars of sha256(projectPath), used
// as a stable identifier in fcheap tags so stashes are scoped per repo.
func repoHashShort(projectPath string) string {
	h := sha256.Sum256([]byte(projectPath))
	return hex.EncodeToString(h[:])[:8]
}

// embeddingCacheTags builds the fcheap tag set for an embedding-cache stash.
// Stashes are tagged with repo:<hash> and model:<name> so restore can find a
// compatible cache for the current repo + embedding model.
func embeddingCacheTags(repoHash, modelName string) []string {
	return []string{
		"vecgrep",
		"embedding-cache",
		"repo:" + repoHash,
		"model:" + modelName,
	}
}

// diskCacheFromProvider extracts the *embed.DiskCache backing the provider's
// throttle layer, if any. Returns nil when no disk cache is configured.
func diskCacheFromProvider(provider embed.Provider) *embed.DiskCache {
	tp, ok := provider.(*embed.ThrottledProvider)
	if !ok {
		return nil
	}
	c := tp.Cache()
	if c == nil {
		return nil
	}
	if dc, ok := c.(*embed.DiskCache); ok {
		return dc
	}
	return nil
}

// stashEmbeddingCache stashes the disk cache file (if any) to fcheap. It is
// best-effort: fcheap unavailable or save failures are logged and swallowed.
func stashEmbeddingCache(ctx context.Context, provider embed.Provider, projectPath, modelName, ttl string) {
	dc := diskCacheFromProvider(provider)
	if dc == nil {
		return
	}

	// Flush pending async writes so the stashed file is consistent.
	if err := dc.FlushToDisk(); err != nil {
		log.Printf("embedding cache stash: flush failed: %v", err)
		return
	}

	cachePath := resolvedCachePathForDiskCache(dc)
	if cachePath == "" {
		return
	}

	f := snapshot.NewFcheap()
	if !f.Available() {
		return
	}

	repoHash := repoHashShort(projectPath)
	tags := embeddingCacheTags(repoHash, modelName)
	if ttl != "" {
		tags = append(tags, "ttl:"+ttl)
	}

	name := "vecgrep-embed-cache-" + repoHash + "-" + modelName
	result, err := f.SaveFile(ctx, cachePath, name, "vecgrep", tags)
	if err != nil {
		log.Printf("embedding cache stash: fcheap save failed: %v", err)
		return
	}
	if result != nil {
		log.Printf("embedding cache stash: saved to fcheap stash %s", result.StashID)
	}
}

// resolvedCachePathForDiskCache returns the on-disk bbolt path backing the
// DiskCache, via the DiskCache's CachePath() accessor.
func resolvedCachePathForDiskCache(dc *embed.DiskCache) string {
	if dc == nil {
		return ""
	}
	return dc.CachePath()
}

// restoreEmbeddingCache searches fcheap for the most recent embedding-cache
// stash matching repo:<hash> and model:<modelName>, restores it to a temp
// dir, and returns the restored file path. Returns ("", nil) when no stash
// is found or fcheap is unavailable. Best-effort: errors are logged.
func restoreEmbeddingCache(ctx context.Context, projectPath, modelName string) string {
	f := snapshot.NewFcheap()
	if !f.Available() {
		return ""
	}

	repoHash := repoHashShort(projectPath)
	tags := embeddingCacheTags(repoHash, modelName)

	latest, err := f.LatestByTags(ctx, tags)
	if err != nil {
		log.Printf("embedding cache restore: fcheap list failed: %v", err)
		return ""
	}
	if latest == nil {
		return ""
	}

	// Restore into a temp directory, then return the restored file path.
	tmpDir, err := os.MkdirTemp("", "vecgrep-embed-cache-restore-*")
	if err != nil {
		log.Printf("embedding cache restore: create temp dir failed: %v", err)
		return ""
	}

	if err := f.Restore(ctx, latest.StashID, tmpDir); err != nil {
		log.Printf("embedding cache restore: fcheap restore failed: %v", err)
		_ = os.RemoveAll(tmpDir)
		return ""
	}

	// The stash was a single file; find it in the restored directory.
	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) == 0 {
		_ = os.RemoveAll(tmpDir)
		return ""
	}
	restoredPath := filepath.Join(tmpDir, entries[0].Name())
	log.Printf("restored embedding cache from fcheap stash %s", latest.StashID)
	return restoredPath
}

// loadRestoredCacheIntoProvider opens the restored bbolt file and merges its
// entries into the active DiskCache so subsequent Get() calls hit the cache.
// Best-effort: errors are logged and the temp file is cleaned up.
func loadRestoredCacheIntoProvider(provider embed.Provider, restoredPath string) {
	if restoredPath == "" {
		return
	}
	defer func() {
		if rerr := os.RemoveAll(filepath.Dir(restoredPath)); rerr != nil {
			log.Printf("embedding cache restore: temp cleanup failed: %v", rerr)
		}
	}()

	dc := diskCacheFromProvider(provider)
	if dc == nil {
		return
	}

	if err := dc.MergeFromDisk(restoredPath); err != nil {
		log.Printf("embedding cache restore: merge from %s failed: %v", restoredPath, err)
		return
	}
	log.Printf("embedding cache restore: merged %s into live cache", restoredPath)
}

// PruneBranchIndexes runs fcheap cleanup-smart to prune branch index
// snapshots whose git branches have been deleted. It shells out to
// `fcheap cleanup-smart --apply --categories branch-gone --include-tag branch:`
// via the fcheap wrapper. Returns the number of stashes pruned (parsed
// from the command output, best-effort) and an error if fcheap is not
// available or the command fails.
func PruneBranchIndexes(ctx context.Context) (int, error) {
	f := snapshot.NewFcheap()
	if !f.Available() {
		return 0, fmt.Errorf("fcheap not found, skipping prune")
	}

	out, err := f.ExecRaw(ctx,
		"cleanup-smart", "--apply", "--json",
		"--categories", "branch-gone",
		"--include-tag", "branch:",
	)
	if err != nil {
		return 0, fmt.Errorf("fcheap cleanup-smart: %w: %s", err, out)
	}

	// Best-effort parse of the count from JSON or text output.
	count := parsePrunedCount(out)
	return count, nil
}

// SweepEmbeddingCaches runs fcheap sweep/vacuum to clean up old and
// superseded embedding-cache stashes. It shells out to
// `fcheap cleanup-smart --apply --include-tag embedding-cache` via the
// fcheap wrapper. Returns the number of stashes swept (best-effort) and
// an error if fcheap is not available or the command fails.
func SweepEmbeddingCaches(ctx context.Context) (int, error) {
	f := snapshot.NewFcheap()
	if !f.Available() {
		return 0, fmt.Errorf("fcheap not found, skipping sweep")
	}

	out, err := f.ExecRaw(ctx,
		"cleanup-smart", "--apply", "--json",
		"--include-tag", "embedding-cache",
	)
	if err != nil {
		return 0, fmt.Errorf("fcheap sweep: %w: %s", err, out)
	}

	return parsePrunedCount(out), nil
}

// CountEmbeddingCacheStashes returns the number of fcheap stashes tagged
// with "embedding-cache". Best-effort: returns 0 if fcheap is unavailable
// or the list fails.
func CountEmbeddingCacheStashes(ctx context.Context) int {
	f := snapshot.NewFcheap()
	if !f.Available() {
		return 0
	}
	entries, err := f.List(ctx, []string{"embedding-cache"})
	if err != nil {
		return 0
	}
	return len(entries)
}

// parsePrunedCount extracts a count from fcheap cleanup-smart output. It
// looks for a JSON field like "pruned" or "swept", falling back to 0.
func parsePrunedCount(out string) int {
	out = strings.TrimSpace(out)
	if out == "" {
		return 0
	}
	// Try JSON parse for common field names.
	type cleanupResult struct {
		Pruned int `json:"pruned"`
		Swept  int `json:"swept"`
		Count  int `json:"count"`
		Total  int `json:"total"`
	}
	var res cleanupResult
	if err := json.Unmarshal([]byte(out), &res); err == nil {
		if res.Pruned > 0 {
			return res.Pruned
		}
		if res.Swept > 0 {
			return res.Swept
		}
		if res.Count > 0 {
			return res.Count
		}
		return res.Total
	}
	// Fallback: no reliable count, return 0 (the command still ran).
	return 0
}

// restoreBranchIndex searches fcheap for the most recent branch-index stash
// matching repo:<hash> and branch:<sanitizedBranch>, restores it to the
// target directory, and returns the stash ID. Returns ("", nil) when no
// stash is found or fcheap is unavailable. Best-effort.
func restoreBranchIndex(ctx context.Context, targetDir, repoHash, sanitizedBranch string) (string, error) {
	f := snapshot.NewFcheap()
	if !f.Available() {
		return "", nil
	}

	tags := []string{
		"vecgrep-index",
		"repo:" + repoHash,
		"branch:" + sanitizedBranch,
	}
	latest, err := f.LatestByTags(ctx, tags)
	if err != nil {
		return "", fmt.Errorf("fcheap list branch index: %w", err)
	}
	if latest == nil {
		return "", nil
	}

	if err := f.Restore(ctx, latest.StashID, targetDir); err != nil {
		return "", fmt.Errorf("fcheap restore branch index: %w", err)
	}
	log.Printf("restored branch index from fcheap stash %s, skipping reindex", latest.StashID)
	return latest.StashID, nil
}

// StashEmbeddingCacheManual manually stashes the current embedding cache to
// fcheap. It is the public entry point for the `vecgrep cache save` command.
// Returns true if a stash was created, false otherwise.
func (s *Service) StashEmbeddingCacheManual(ctx context.Context) bool {
	if s == nil || s.session == nil {
		return false
	}
	if !s.session.Config.Cache.FcheapStashEnabled() {
		return false
	}
	modelName := s.session.Config.Embedding.Model
	ttl := s.session.Config.Cache.FcheapTTL
	if ttl == "" {
		ttl = "30d"
	}
	dc := diskCacheFromProvider(s.session.Provider)
	if dc == nil {
		return false
	}
	if err := dc.FlushToDisk(); err != nil {
		log.Printf("embedding cache stash: flush failed: %v", err)
		return false
	}
	cachePath := ResolvedCachePath(s.session.Config)
	if cachePath == "" {
		return false
	}
	f := NewFcheapWrapper()
	if !f.Available() {
		return false
	}
	repoHash := repoHashShort(s.session.ProjectRoot)
	tags := embeddingCacheTags(repoHash, modelName)
	if ttl != "" {
		tags = append(tags, "ttl:"+ttl)
	}
	name := "vecgrep-embed-cache-" + repoHash + "-" + modelName
	result, err := f.SaveFile(ctx, cachePath, name, "vecgrep", tags)
	if err != nil {
		log.Printf("embedding cache stash: fcheap save failed: %v", err)
		return false
	}
	if result != nil {
		log.Printf("embedding cache stash: saved to fcheap stash %s", result.StashID)
		return true
	}
	return false
}

// RestoreEmbeddingCacheManual manually restores the most recent matching
// embedding cache from fcheap and merges it into the live DiskCache. It is
// the public entry point for the `vecgrep cache restore` command. Returns
// true if a cache was restored, false otherwise.
func (s *Service) RestoreEmbeddingCacheManual(ctx context.Context) bool {
	if s == nil || s.session == nil {
		return false
	}
	if !s.session.Config.Cache.FcheapStashEnabled() {
		return false
	}
	modelName := s.session.Config.Embedding.Model
	restoredPath := restoreEmbeddingCache(ctx, s.session.ProjectRoot, modelName)
	if restoredPath == "" {
		return false
	}
	loadRestoredCacheIntoProvider(s.session.Provider, restoredPath)
	return true
}
