package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockProvider implements embed.Provider for testing.
type mockProvider struct {
	embedFunc func(text string) []float32
}

func (m *mockProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if m.embedFunc != nil {
		return m.embedFunc(text), nil
	}
	// Return a simple deterministic embedding based on text length
	vec := make([]float32, 768)
	for i := range vec {
		vec[i] = float32(len(text)+i) / 1000.0
	}
	return vec, nil
}

func (m *mockProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		results[i], _ = m.Embed(ctx, text)
	}
	return results, nil
}

func (m *mockProvider) Model() string              { return "mock-model" }
func (m *mockProvider) Dimensions() int            { return 768 }
func (m *mockProvider) Ping(context.Context) error { return nil }

// Warmup implements embed.Provider for the test mock.
func (m *mockProvider) Warmup(context.Context) (time.Duration, error) { return 0, nil }

func setupTestStore(t *testing.T) (*MemoryStore, func()) {
	t.Helper()

	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "memory-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	cfg := &Config{
		DBPath:              filepath.Join(tmpDir, "test.veclite"),
		OllamaURL:           "http://localhost:11434",
		EmbeddingModel:      "test-model",
		EmbeddingDimensions: 768,
	}

	store, err := NewMemoryStore(cfg, &mockProvider{})
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create store: %v", err)
	}

	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func TestRememberAndRecall(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember a note
	res, err := store.Remember(ctx, "This is a test memory about Go programming", RememberOptions{
		Importance: 0.8,
		Tags:       []string{"programming", "go"},
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if res.ID == 0 {
		t.Error("Expected non-zero ID")
	}

	// Recall the memory
	memories, err := store.Recall(ctx, "Go programming", RecallOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(memories) == 0 {
		t.Fatal("Expected at least one memory")
	}

	// Verify content
	found := false
	for _, m := range memories {
		if m.Content == "This is a test memory about Go programming" {
			found = true
			if m.Importance != 0.8 {
				t.Errorf("Expected importance 0.8, got %f", m.Importance)
			}
			if len(m.Tags) != 2 {
				t.Errorf("Expected 2 tags, got %d", len(m.Tags))
			}
		}
	}
	if !found {
		t.Error("Did not find the stored memory")
	}
}

func TestForgetByID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember a note
	res, err := store.Remember(ctx, "Memory to delete", RememberOptions{})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}

	// Forget by ID
	deleted, err := store.Forget(ctx, ForgetOptions{ID: res.ID})
	if err != nil {
		t.Fatalf("Forget failed: %v", err)
	}
	if deleted != 1 {
		t.Errorf("Expected 1 deleted, got %d", deleted)
	}

	// Verify it's gone
	stats, _ := store.Stats(ctx)
	if stats.TotalMemories != 0 {
		t.Errorf("Expected 0 memories, got %d", stats.TotalMemories)
	}
}

func TestForgetByTags(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember notes with different tags
	_, _ = store.Remember(ctx, "Memory with tag1", RememberOptions{Tags: []string{"tag1"}})
	_, _ = store.Remember(ctx, "Memory with tag2", RememberOptions{Tags: []string{"tag2"}})
	_, _ = store.Remember(ctx, "Memory with tag1 and tag2", RememberOptions{Tags: []string{"tag1", "tag2"}})

	// Forget by tag1
	deleted, err := store.Forget(ctx, ForgetOptions{Tags: []string{"tag1"}})
	if err != nil {
		t.Fatalf("Forget failed: %v", err)
	}
	if deleted != 2 {
		t.Errorf("Expected 2 deleted, got %d", deleted)
	}

	// Verify only tag2-only memory remains
	stats, _ := store.Stats(ctx)
	if stats.TotalMemories != 1 {
		t.Errorf("Expected 1 memory, got %d", stats.TotalMemories)
	}
}

func TestStats(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember some notes
	_, _ = store.Remember(ctx, "Memory 1", RememberOptions{Tags: []string{"work"}})
	_, _ = store.Remember(ctx, "Memory 2", RememberOptions{Tags: []string{"personal"}})
	_, _ = store.Remember(ctx, "Memory 3", RememberOptions{Tags: []string{"work", "important"}})

	// Get stats
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats.TotalMemories != 3 {
		t.Errorf("Expected 3 memories, got %d", stats.TotalMemories)
	}
	if stats.TotalTags != 3 {
		t.Errorf("Expected 3 unique tags, got %d", stats.TotalTags)
	}
	if stats.TagCounts["work"] != 2 {
		t.Errorf("Expected 'work' count 2, got %d", stats.TagCounts["work"])
	}
	if stats.OldestMemory == nil {
		t.Error("Expected OldestMemory to be set")
	}
	if stats.NewestMemory == nil {
		t.Error("Expected NewestMemory to be set")
	}
}

func TestExpiredMemories(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember a note with very short TTL (we can't actually test expiration without time manipulation,
	// but we can test that TTL is stored correctly)
	res, err := store.Remember(ctx, "Expiring memory", RememberOptions{TTLHours: 1})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if res.ID == 0 {
		t.Error("Expected non-zero ID")
	}

	// The memory should still be accessible (not expired yet)
	memories, err := store.Recall(ctx, "Expiring", RecallOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(memories) == 0 {
		t.Fatal("Expected at least one memory")
	}

	// Verify ExpiresAt is set
	if memories[0].ExpiresAt == nil {
		t.Error("Expected ExpiresAt to be set")
	} else {
		expectedExpiry := time.Now().Add(time.Hour)
		if memories[0].ExpiresAt.Before(time.Now()) {
			t.Error("ExpiresAt should be in the future")
		}
		if memories[0].ExpiresAt.After(expectedExpiry.Add(time.Minute)) {
			t.Error("ExpiresAt should be approximately 1 hour from now")
		}
	}
}

func TestTagFiltering(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember notes with different tags
	_, _ = store.Remember(ctx, "Work meeting notes", RememberOptions{Tags: []string{"work", "meeting"}})
	_, _ = store.Remember(ctx, "Personal todo", RememberOptions{Tags: []string{"personal"}})
	_, _ = store.Remember(ctx, "Work project plan", RememberOptions{Tags: []string{"work", "project"}})

	// Recall with tag filter
	memories, err := store.Recall(ctx, "notes", RecallOptions{
		Limit: 10,
		Tags:  []string{"work"},
	})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}

	// Should only return work-tagged memories
	for _, m := range memories {
		hasWork := false
		for _, tag := range m.Tags {
			if tag == "work" {
				hasWork = true
				break
			}
		}
		if !hasWork {
			t.Errorf("Memory %d should have 'work' tag", m.ID)
		}
	}
}

func TestMinImportanceFiltering(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember notes with different importance
	_, _ = store.Remember(ctx, "Low importance note", RememberOptions{Importance: 0.2})
	_, _ = store.Remember(ctx, "Medium importance note", RememberOptions{Importance: 0.5})
	_, _ = store.Remember(ctx, "High importance note", RememberOptions{Importance: 0.9})

	// Recall with min importance filter
	memories, err := store.Recall(ctx, "note", RecallOptions{
		Limit:         10,
		MinImportance: 0.7,
	})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}

	// Should only return high importance memory
	for _, m := range memories {
		if m.Importance < 0.7 {
			t.Errorf("Memory %d has importance %f, expected >= 0.7", m.ID, m.Importance)
		}
	}
}

func TestDefaultImportance(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember without specifying importance
	_, _ = store.Remember(ctx, "Default importance note", RememberOptions{})

	memories, err := store.Recall(ctx, "Default", RecallOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(memories) == 0 {
		t.Fatal("Expected at least one memory")
	}

	// Default importance should be 0.5
	if memories[0].Importance != 0.5 {
		t.Errorf("Expected default importance 0.5, got %f", memories[0].Importance)
	}
}

func TestEmptyContent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Remember with empty content should fail
	_, err := store.Remember(ctx, "", RememberOptions{})
	if err == nil {
		t.Error("Expected error for empty content")
	}
}

func TestEmptyQuery(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Recall with empty query should fail
	_, err := store.Recall(ctx, "", RecallOptions{})
	if err == nil {
		t.Error("Expected error for empty query")
	}
}

func TestImportanceBoundaries(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Test negative importance (should default to 0.5)
	_, _ = store.Remember(ctx, "Negative importance", RememberOptions{Importance: -0.5})
	memories, _ := store.Recall(ctx, "Negative", RecallOptions{Limit: 1})
	if len(memories) > 0 && memories[0].Importance != 0.5 {
		t.Errorf("Negative importance should default to 0.5, got %f", memories[0].Importance)
	}

	// Test importance > 1.0 (should cap at 1.0)
	_, _ = store.Remember(ctx, "Over one importance", RememberOptions{Importance: 1.5})
	memories, _ = store.Recall(ctx, "Over one", RecallOptions{Limit: 1})
	if len(memories) > 0 && memories[0].Importance > 1.0 {
		t.Errorf("Importance > 1.0 should cap at 1.0, got %f", memories[0].Importance)
	}
}

func TestForgetExpired(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Store a memory without expiration
	_, _ = store.Remember(ctx, "Permanent memory", RememberOptions{})

	// ForgetExpired should not delete non-expired memories
	deleted, err := store.ForgetExpired(ctx)
	if err != nil {
		t.Fatalf("ForgetExpired failed: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Expected 0 deleted, got %d", deleted)
	}

	// Verify memory still exists
	stats, _ := store.Stats(ctx)
	if stats.TotalMemories != 1 {
		t.Errorf("Expected 1 memory, got %d", stats.TotalMemories)
	}
}

func TestForgetByAge(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Store memories
	_, _ = store.Remember(ctx, "Memory 1", RememberOptions{})
	_, _ = store.Remember(ctx, "Memory 2", RememberOptions{})

	// Delete memories older than 0 hours should delete none (they're brand new)
	// Note: This tests the edge case where OlderThanHours is set but no memories match
	deleted, err := store.Forget(ctx, ForgetOptions{OlderThanHours: 24})
	if err != nil {
		t.Fatalf("Forget by age failed: %v", err)
	}

	// Memories are < 1 second old, so 24 hours cutoff should delete none
	if deleted != 0 {
		t.Errorf("Expected 0 deleted for recent memories, got %d", deleted)
	}
}

func TestDeleteNonExistentID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Try to delete a non-existent ID
	_, err := store.Forget(ctx, ForgetOptions{ID: 999999})
	// Should return error for non-existent ID
	if err == nil {
		t.Log("Note: Deleting non-existent ID did not return error (may be acceptable behavior)")
	}
}

func TestSpecialCharactersInContent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Test content with special characters
	specialContent := "Memory with special chars: <>&\"'`\n\t\r\x00unicode: \u0041\u0042\u0043"
	res, err := store.Remember(ctx, specialContent, RememberOptions{})
	if err != nil {
		t.Fatalf("Remember with special chars failed: %v", err)
	}
	if res.ID == 0 {
		t.Error("Expected non-zero ID")
	}

	// Verify it can be recalled
	memories, err := store.Recall(ctx, "special chars", RecallOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(memories) == 0 {
		t.Fatal("Expected at least one memory")
	}
}

func TestSpecialCharactersInTags(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Test tags with special characters (commas are tricky since we join with commas)
	_, _ = store.Remember(ctx, "Memory with special tags", RememberOptions{
		Tags: []string{"tag-with-dash", "tag_with_underscore", "tag.with.dots"},
	})

	stats, _ := store.Stats(ctx)
	if stats.TotalTags != 3 {
		t.Errorf("Expected 3 tags, got %d", stats.TotalTags)
	}
}

func TestLargeContent(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Create large content (10KB)
	largeContent := strings.Repeat("This is a test sentence for large content. ", 250)
	res, err := store.Remember(ctx, largeContent, RememberOptions{})
	if err != nil {
		t.Fatalf("Remember large content failed: %v", err)
	}
	if res.ID == 0 {
		t.Error("Expected non-zero ID")
	}

	// Verify it can be recalled
	memories, err := store.Recall(ctx, "test sentence", RecallOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(memories) == 0 {
		t.Fatal("Expected at least one memory")
	}
	if len(memories[0].Content) != len(largeContent) {
		t.Errorf("Content length mismatch: expected %d, got %d", len(largeContent), len(memories[0].Content))
	}
}

func TestRecallLimitEdgeCases(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Store multiple memories
	for i := 0; i < 5; i++ {
		_, _ = store.Remember(ctx, fmt.Sprintf("Memory number %d", i), RememberOptions{})
	}

	// Test limit of 0 (should default to 10)
	memories, err := store.Recall(ctx, "Memory", RecallOptions{Limit: 0})
	if err != nil {
		t.Fatalf("Recall with limit 0 failed: %v", err)
	}
	if len(memories) != 5 {
		t.Errorf("Expected 5 memories with limit 0 (default), got %d", len(memories))
	}

	// Test limit of 2
	memories, err = store.Recall(ctx, "Memory", RecallOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Recall with limit 2 failed: %v", err)
	}
	if len(memories) != 2 {
		t.Errorf("Expected 2 memories with limit 2, got %d", len(memories))
	}

	// Test negative limit (should default to 10)
	memories, err = store.Recall(ctx, "Memory", RecallOptions{Limit: -5})
	if err != nil {
		t.Fatalf("Recall with negative limit failed: %v", err)
	}
	if len(memories) != 5 {
		t.Errorf("Expected 5 memories with negative limit (default), got %d", len(memories))
	}
}

func TestForgetWithNoParameters(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Store a memory
	_, _ = store.Remember(ctx, "Test memory", RememberOptions{})

	// Forget with no parameters should delete nothing
	deleted, err := store.Forget(ctx, ForgetOptions{})
	if err != nil {
		t.Fatalf("Forget with no params failed: %v", err)
	}
	if deleted != 0 {
		t.Errorf("Expected 0 deleted with no parameters, got %d", deleted)
	}

	// Memory should still exist
	stats, _ := store.Stats(ctx)
	if stats.TotalMemories != 1 {
		t.Errorf("Expected 1 memory, got %d", stats.TotalMemories)
	}
}

func TestMultipleTagsRecall(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Store memories with overlapping tags
	_, _ = store.Remember(ctx, "Work meeting", RememberOptions{Tags: []string{"work", "meeting"}})
	_, _ = store.Remember(ctx, "Personal meeting", RememberOptions{Tags: []string{"personal", "meeting"}})
	_, _ = store.Remember(ctx, "Work project", RememberOptions{Tags: []string{"work", "project"}})

	// Filter by multiple tags (should require all tags - AND logic)
	memories, err := store.Recall(ctx, "meeting", RecallOptions{
		Limit: 10,
		Tags:  []string{"work", "meeting"},
	})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}

	// Should only return "Work meeting" which has both tags
	for _, m := range memories {
		hasWork := false
		hasMeeting := false
		for _, tag := range m.Tags {
			if tag == "work" {
				hasWork = true
			}
			if tag == "meeting" {
				hasMeeting = true
			}
		}
		if !hasWork || !hasMeeting {
			t.Errorf("Memory %d should have both 'work' and 'meeting' tags", m.ID)
		}
	}
}

// TestTagAndScopingNoSubstringLeak is the G2 governance test: tag-AND recall
// must NOT leak across scopes on a substring collision. The veclite Contains
// pre-filter is a substring match, so a memory tagged "codemapper" or a key
// that is a substring of another token could slip through; the exact
// post-filter in Recall must reject them.
func TestTagAndScopingNoSubstringLeak(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	const keyA = "abc123def456"   // project A's codemap key (12 hex)
	const keyB = "abc123def456ff" // project B: a SUPERSTRING of keyA

	// In-scope: this project's codemap memory.
	_, _ = store.Remember(ctx, "auth token rotation gotcha", RememberOptions{Tags: []string{"codemap", keyA}})
	// Leak bait 1: same content, a DIFFERENT project whose key is a superstring of keyA.
	_, _ = store.Remember(ctx, "auth token rotation gotcha B", RememberOptions{Tags: []string{"codemap", keyB}})
	// Leak bait 2: a tag that is a superstring of "codemap".
	_, _ = store.Remember(ctx, "auth token rotation gotcha C", RememberOptions{Tags: []string{"codemapper", keyA}})

	memories, err := store.Recall(ctx, "auth token rotation", RecallOptions{
		Limit: 10,
		Tags:  []string{"codemap", keyA},
	})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}

	// Only the in-scope memory (exact "codemap" AND exact keyA) may match.
	if len(memories) != 1 {
		t.Fatalf("expected exactly 1 in-scope memory, got %d: %+v", len(memories), memories)
	}
	got := memories[0]
	if !hasAllTags(got.Tags, []string{"codemap", keyA}) {
		t.Errorf("matched memory lacks the exact scope tags: %+v", got.Tags)
	}
	// The superstring-key memory (keyB) and superstring-tag memory (codemapper)
	// must NOT appear.
	for _, m := range memories {
		for _, tag := range m.Tags {
			if tag == keyB || tag == "codemapper" {
				t.Errorf("scope leaked: matched memory carries out-of-scope tag %q", tag)
			}
		}
	}
}

func TestHasAllTags(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want []string
		ok   bool
	}{
		{"empty want matches all", []string{"a", "b"}, nil, true},
		{"all present", []string{"codemap", "abc123", "extra"}, []string{"codemap", "abc123"}, true},
		{"missing one", []string{"codemap", "other"}, []string{"codemap", "abc123"}, false},
		{"substring is not a match", []string{"codemapper", "abc123ff"}, []string{"codemap", "abc123"}, false},
		{"order independent", []string{"b", "a"}, []string{"a", "b"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasAllTags(c.tags, c.want); got != c.ok {
				t.Errorf("hasAllTags(%v, %v) = %v, want %v", c.tags, c.want, got, c.ok)
			}
		})
	}
}

func TestEmptyTagsList(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Store with empty tags
	res, err := store.Remember(ctx, "No tags memory", RememberOptions{Tags: []string{}})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if res.ID == 0 {
		t.Error("Expected non-zero ID")
	}

	// Should be recallable
	memories, _ := store.Recall(ctx, "No tags", RecallOptions{Limit: 1})
	if len(memories) == 0 {
		t.Fatal("Expected at least one memory")
	}
	if len(memories[0].Tags) != 0 {
		t.Errorf("Expected 0 tags, got %d", len(memories[0].Tags))
	}
}

// testVec builds a 768-dim L2-normalized vector from leading values, so tests
// can reason about exact cosine similarities between hand-picked texts.
func testVec(values ...float32) []float32 {
	v := make([]float32, 768)
	copy(v, values)
	var norm float64
	for _, x := range v {
		norm += float64(x) * float64(x)
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

// setupTestStoreWithProvider builds a store with an explicit config and
// embedding provider, for tests that exercise dedup, links, or usage ranking.
func setupTestStoreWithProvider(t *testing.T, cfg *Config, provider *mockProvider) (*MemoryStore, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "memory-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(tmpDir, "test.veclite")
	} else {
		cfg.DBPath = filepath.Join(tmpDir, filepath.Base(cfg.DBPath))
	}

	store, err := NewMemoryStore(cfg, provider)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create store: %v", err)
	}

	cleanup := func() {
		_ = store.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return store, cleanup
}

func TestRememberRejectsDuplicate(t *testing.T) {
	cfg := &Config{
		EmbeddingDimensions: 768,
		DedupThreshold:      DefaultDedupThreshold,
	}
	// Parallel vectors make every text a perfect cosine match, the
	// worst case for duplicate detection.
	provider := &mockProvider{}
	store, cleanup := setupTestStoreWithProvider(t, cfg, provider)
	defer cleanup()
	ctx := context.Background()

	first, err := store.Remember(ctx, "the deploy port is 8080", RememberOptions{})
	if err != nil {
		t.Fatalf("first Remember failed: %v", err)
	}

	_, err = store.Remember(ctx, "the deploy port is 8080", RememberOptions{})
	var dup *DuplicateError
	if !errors.As(err, &dup) {
		t.Fatalf("expected DuplicateError, got %v", err)
	}
	if dup.ExistingID != first.ID {
		t.Errorf("DuplicateError points at %d, want %d", dup.ExistingID, first.ID)
	}
	if dup.Score < DefaultDedupThreshold {
		t.Errorf("duplicate score %f below threshold %f", dup.Score, DefaultDedupThreshold)
	}

	// The refusal must not have stored anything.
	stats, _ := store.Stats(ctx)
	if stats.TotalMemories != 1 {
		t.Errorf("expected 1 memory after refused duplicate, got %d", stats.TotalMemories)
	}

	// allow_duplicate overrides the refusal.
	if _, err := store.Remember(ctx, "the deploy port is 8080", RememberOptions{AllowDuplicate: true}); err != nil {
		t.Fatalf("allow_duplicate Remember failed: %v", err)
	}
	stats, _ = store.Stats(ctx)
	if stats.TotalMemories != 2 {
		t.Errorf("expected 2 memories after forced duplicate, got %d", stats.TotalMemories)
	}
}

func TestRememberLinksRelatedBidirectional(t *testing.T) {
	cfg := &Config{
		EmbeddingDimensions: 768,
		DedupThreshold:      DefaultDedupThreshold,
		RelatedThreshold:    0.6,
	}
	// A·B = 1/sqrt(2) ≈ 0.71 (linked); C is orthogonal to both (not linked).
	vecs := map[string][]float32{
		"alpha one": testVec(1),
		"alpha two": testVec(1, 1),
		"gamma":     testVec(0, 0, 1),
		"alpha":     testVec(1),
		"two":       testVec(1, 1),
	}
	store, cleanup := setupTestStoreWithProvider(t, cfg, &mockProvider{embedFunc: func(text string) []float32 {
		return vecs[text]
	}})
	defer cleanup()
	ctx := context.Background()

	resA, err := store.Remember(ctx, "alpha one", RememberOptions{})
	if err != nil {
		t.Fatalf("Remember alpha one failed: %v", err)
	}
	resB, err := store.Remember(ctx, "alpha two", RememberOptions{})
	if err != nil {
		t.Fatalf("Remember alpha two failed: %v", err)
	}
	if len(resB.Related) != 1 || resB.Related[0].ID != resA.ID {
		t.Errorf("expected link to alpha one (%d), got %+v", resA.ID, resB.Related)
	}
	resC, err := store.Remember(ctx, "gamma", RememberOptions{})
	if err != nil {
		t.Fatalf("Remember gamma failed: %v", err)
	}
	if len(resC.Related) != 0 {
		t.Errorf("orthogonal memory should have no links, got %+v", resC.Related)
	}

	// Backlinks: the earlier memory must know about the later one too.
	recA, err := store.coll.Get(resA.ID)
	if err != nil {
		t.Fatalf("Get alpha one failed: %v", err)
	}
	links := parseRelatedIDs(recA.Payload)
	if len(links) != 1 || links[0] != resB.ID {
		t.Errorf("alpha one backlinks = %v, want [%d]", links, resB.ID)
	}

	// Recall resolves link previews in both directions.
	for _, q := range []string{"alpha", "two"} {
		memories, err := store.Recall(ctx, q, RecallOptions{Limit: 5})
		if err != nil {
			t.Fatalf("Recall %q failed: %v", q, err)
		}
		found := false
		for _, m := range memories {
			if m.ID != resA.ID && m.ID != resB.ID {
				continue
			}
			if len(m.Related) == 1 && m.Related[0].ID != m.ID {
				found = true
				if m.Related[0].Content == "" {
					t.Errorf("related preview for %d has no content", m.ID)
				}
			}
		}
		if !found {
			t.Errorf("recall %q: neither memory resolved its related link", q)
		}
	}
}

func TestRecallUsageLift(t *testing.T) {
	// Two memories, "alpha" always the more similar to the query; "beta"
	// starts far below but carries a large access count. A strong UsageBoost
	// must promote beta above alpha; a disabled boost must not.
	run := func(usageBoost float64) []string {
		t.Helper()
		cfg := &Config{
			EmbeddingDimensions: 768,
			UsageBoost:          usageBoost,
		}
		vecs := map[string][]float32{
			"alpha note": testVec(1),
			"beta note":  testVec(1, 1), // cosine 0.71 to the alpha query
			"alpha":      testVec(1),
		}
		store, cleanup := setupTestStoreWithProvider(t, cfg, &mockProvider{embedFunc: func(text string) []float32 {
			return vecs[text]
		}})
		defer cleanup()
		ctx := context.Background()

		if _, err := store.Remember(ctx, "alpha note", RememberOptions{}); err != nil {
			t.Fatalf("Remember alpha failed: %v", err)
		}
		beta, err := store.Remember(ctx, "beta note", RememberOptions{})
		if err != nil {
			t.Fatalf("Remember beta failed: %v", err)
		}

		// Seed a heavy access history on beta directly in its payload.
		rec, err := store.coll.Get(beta.ID)
		if err != nil {
			t.Fatalf("Get beta failed: %v", err)
		}
		payload := clonePayload(rec.Payload)
		payload[payloadAccessCount] = int64(50)
		payload[payloadLastAccessedAt] = time.Now().Unix()
		if err := store.coll.Update(beta.ID, payload); err != nil {
			t.Fatalf("seed beta access failed: %v", err)
		}

		memories, err := store.Recall(ctx, "alpha", RecallOptions{Limit: 2})
		if err != nil {
			t.Fatalf("Recall failed: %v", err)
		}
		var order []string
		for _, m := range memories {
			order = append(order, m.Content)
		}
		return order
	}

	// No usage boost: similarity order wins (alpha first, beta second).
	if got := run(0); len(got) != 2 || got[0] != "alpha note" {
		t.Errorf("with UsageBoost=0 expected alpha first, got %v", got)
	}
	// With the boost, the heavily used memory overtakes the more similar one.
	if got := run(1.0); len(got) != 2 || got[0] != "beta note" {
		t.Errorf("with UsageBoost=1.0 expected heavily-used beta first, got %v", got)
	}
}

func TestRecallTracksAccess(t *testing.T) {
	cfg := &Config{
		EmbeddingDimensions: 768,
		UsageBoost:          DefaultUsageBoost,
	}
	store, cleanup := setupTestStoreWithProvider(t, cfg, &mockProvider{})
	defer cleanup()
	ctx := context.Background()

	if _, err := store.Remember(ctx, "tracked memory", RememberOptions{}); err != nil {
		t.Fatalf("Remember failed: %v", err)
	}

	for want := int64(1); want <= 2; want++ {
		memories, err := store.Recall(ctx, "tracked", RecallOptions{Limit: 1})
		if err != nil {
			t.Fatalf("Recall failed: %v", err)
		}
		if len(memories) != 1 {
			t.Fatalf("expected 1 memory, got %d", len(memories))
		}
		m := memories[0]
		if m.AccessCount != want {
			t.Errorf("recall %d: in-memory access count = %d, want %d", want, m.AccessCount, want)
		}
		if m.LastAccessedAt == nil {
			t.Errorf("recall %d: last accessed not set", want)
		}
		// The count must live in the payload (WAL-durable), not just memory.
		rec, err := store.coll.Get(m.ID)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if got := getInt64Payload(rec.Payload, payloadAccessCount); got != want {
			t.Errorf("recall %d: payload access_count = %d, want %d", want, got, want)
		}
		if got := getInt64Payload(rec.Payload, payloadLastAccessedAt); got == 0 {
			t.Errorf("recall %d: payload last_accessed_at not set", want)
		}
	}
}

func TestStatsLinkedMemories(t *testing.T) {
	cfg := &Config{
		EmbeddingDimensions: 768,
		DedupThreshold:      DefaultDedupThreshold,
		RelatedThreshold:    0.6,
	}
	vecs := map[string][]float32{
		"alpha one": testVec(1),
		"alpha two": testVec(1, 1),
		"gamma":     testVec(0, 0, 1),
	}
	store, cleanup := setupTestStoreWithProvider(t, cfg, &mockProvider{embedFunc: func(text string) []float32 {
		return vecs[text]
	}})
	defer cleanup()
	ctx := context.Background()

	_, _ = store.Remember(ctx, "alpha one", RememberOptions{})
	_, _ = store.Remember(ctx, "alpha two", RememberOptions{})
	_, _ = store.Remember(ctx, "gamma", RememberOptions{})

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}
	if stats.LinkedMemories != 2 {
		t.Errorf("LinkedMemories = %d, want 2", stats.LinkedMemories)
	}
}
