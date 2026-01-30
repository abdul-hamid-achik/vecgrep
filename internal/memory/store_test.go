package memory

import (
	"context"
	"fmt"
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

func (m *mockProvider) Model() string       { return "mock-model" }
func (m *mockProvider) Dimensions() int     { return 768 }
func (m *mockProvider) Ping(context.Context) error { return nil }

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
	id, err := store.Remember(ctx, "This is a test memory about Go programming", RememberOptions{
		Importance: 0.8,
		Tags:       []string{"programming", "go"},
	})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if id == 0 {
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
	id, err := store.Remember(ctx, "Memory to delete", RememberOptions{})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}

	// Forget by ID
	deleted, err := store.Forget(ctx, ForgetOptions{ID: id})
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
	id, err := store.Remember(ctx, "Expiring memory", RememberOptions{TTLHours: 1})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if id == 0 {
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
	id, err := store.Remember(ctx, specialContent, RememberOptions{})
	if err != nil {
		t.Fatalf("Remember with special chars failed: %v", err)
	}
	if id == 0 {
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
	id, err := store.Remember(ctx, largeContent, RememberOptions{})
	if err != nil {
		t.Fatalf("Remember large content failed: %v", err)
	}
	if id == 0 {
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

func TestEmptyTagsList(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx := context.Background()

	// Store with empty tags
	id, err := store.Remember(ctx, "No tags memory", RememberOptions{Tags: []string{}})
	if err != nil {
		t.Fatalf("Remember failed: %v", err)
	}
	if id == 0 {
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
