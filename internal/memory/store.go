package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/veclite"
)

// MemoryStore manages persistent memory using veclite.
type MemoryStore struct {
	db       *veclite.DB
	coll     *veclite.Collection
	provider embed.Provider
	config   *Config
}

// Memory represents a stored memory with metadata.
type Memory struct {
	ID         uint64
	Content    string
	Importance float64
	Tags       []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	Score      float32 // Search relevance score
}

// RememberOptions contains options for storing a memory.
type RememberOptions struct {
	Importance float64  // 0.0-1.0, default 0.5
	Tags       []string // Categorization tags
	TTLHours   int      // Expiration in hours (0=never)
}

// RecallOptions contains options for searching memories.
type RecallOptions struct {
	Limit         int      // Max results, default 10
	Tags          []string // Filter by tags
	MinImportance float64  // Minimum importance threshold
}

// ForgetOptions contains options for deleting memories.
type ForgetOptions struct {
	ID             uint64   // Delete specific memory by ID
	Tags           []string // Delete by tags
	OlderThanHours int      // Delete memories older than this
}

// Stats contains memory store statistics.
type Stats struct {
	TotalMemories   int64
	TotalTags       int
	OldestMemory    *time.Time
	NewestMemory    *time.Time
	ExpiredMemories int64
	TagCounts       map[string]int64
}

// NewMemoryStore creates a new memory store.
func NewMemoryStore(cfg *Config, provider embed.Provider) (*MemoryStore, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	// Ensure the directory exists
	if err := cfg.EnsureDir(); err != nil {
		return nil, fmt.Errorf("failed to create memory directory: %w", err)
	}

	// Open veclite database
	db, err := veclite.Open(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open memory database: %w", err)
	}

	// Create or get the memories collection
	coll, err := db.CreateCollection("memories",
		veclite.WithDimension(cfg.EmbeddingDimensions),
		veclite.WithDistanceType(veclite.DistanceCosine),
		veclite.WithHNSW(16, 200),
	)
	if err != nil {
		// Collection might already exist
		coll, err = db.GetCollection("memories")
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to create/get memories collection: %w", err)
		}
	}

	return &MemoryStore{
		db:       db,
		coll:     coll,
		provider: provider,
		config:   cfg,
	}, nil
}

// Remember stores a memory with optional metadata.
func (s *MemoryStore) Remember(ctx context.Context, content string, opts RememberOptions) (uint64, error) {
	if content == "" {
		return 0, fmt.Errorf("content cannot be empty")
	}

	// Set default importance
	if opts.Importance <= 0 {
		opts.Importance = 0.5
	}
	if opts.Importance > 1.0 {
		opts.Importance = 1.0
	}

	// Generate embedding
	embedding, err := s.provider.Embed(ctx, content)
	if err != nil {
		return 0, fmt.Errorf("failed to generate embedding: %w", err)
	}

	// Calculate expiration time
	var expiresAt int64
	if opts.TTLHours > 0 {
		expiresAt = time.Now().Add(time.Duration(opts.TTLHours) * time.Hour).Unix()
	}

	// Build payload
	payload := map[string]any{
		"content":    content,
		"importance": opts.Importance,
		"tags":       strings.Join(opts.Tags, ","),
		"created_at": time.Now().Unix(),
		"expires_at": expiresAt,
	}

	// Insert into veclite
	id, err := s.coll.Insert(embedding, payload)
	if err != nil {
		return 0, fmt.Errorf("failed to store memory: %w", err)
	}

	return id, nil
}

// Recall searches memories semantically.
func (s *MemoryStore) Recall(ctx context.Context, query string, opts RecallOptions) ([]Memory, error) {
	if query == "" {
		return nil, fmt.Errorf("query cannot be empty")
	}

	// Set defaults
	if opts.Limit <= 0 {
		opts.Limit = 10
	}

	// Generate query embedding
	embedding, err := s.provider.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to generate query embedding: %w", err)
	}

	// Build filters
	var filters []veclite.Filter

	// Filter by tags if specified
	if len(opts.Tags) > 0 {
		// Use Contains filter for tag matching (tags stored as comma-separated)
		for _, tag := range opts.Tags {
			filters = append(filters, veclite.Contains("tags", tag))
		}
	}

	// Filter by minimum importance
	if opts.MinImportance > 0 {
		filters = append(filters, veclite.GTE("importance", opts.MinImportance))
	}

	// Build search options
	searchOpts := []veclite.SearchOption{veclite.TopK(opts.Limit * 2)} // Get more for filtering
	if len(filters) > 0 {
		searchOpts = append(searchOpts, veclite.WithFilters(filters...))
	}

	// Search
	results, err := s.coll.Search(embedding, searchOpts...)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Convert results, filtering expired memories
	now := time.Now().Unix()
	memories := make([]Memory, 0, opts.Limit)

	for _, r := range results {
		if len(memories) >= opts.Limit {
			break
		}

		// Check expiration
		expiresAt := getInt64Payload(r.Record.Payload, "expires_at")
		if expiresAt > 0 && expiresAt < now {
			continue // Skip expired memory
		}

		memory := recordToMemory(r.Record, r.Score)
		memories = append(memories, memory)
	}

	return memories, nil
}

// Forget deletes memories by criteria.
func (s *MemoryStore) Forget(ctx context.Context, opts ForgetOptions) (int, error) {
	var deleted int

	// Delete by specific ID
	if opts.ID > 0 {
		if err := s.coll.Delete(opts.ID); err != nil {
			return 0, fmt.Errorf("failed to delete memory %d: %w", opts.ID, err)
		}
		return 1, nil
	}

	// Get all records for bulk operations
	allRecords := s.coll.All()

	// Build list of IDs to delete
	var toDelete []uint64
	now := time.Now().Unix()

	for _, r := range allRecords {
		shouldDelete := false

		// Delete by tags
		if len(opts.Tags) > 0 {
			tagsStr := getStringPayload(r.Payload, "tags")
			for _, tag := range opts.Tags {
				if strings.Contains(tagsStr, tag) {
					shouldDelete = true
					break
				}
			}
		}

		// Delete by age
		if opts.OlderThanHours > 0 {
			createdAt := getInt64Payload(r.Payload, "created_at")
			cutoff := now - int64(opts.OlderThanHours*3600)
			if createdAt > 0 && createdAt < cutoff {
				shouldDelete = true
			}
		}

		if shouldDelete {
			toDelete = append(toDelete, r.ID)
		}
	}

	// Delete collected IDs
	for _, id := range toDelete {
		if err := s.coll.Delete(id); err == nil {
			deleted++
		}
	}

	return deleted, nil
}

// ForgetExpired removes all expired memories.
func (s *MemoryStore) ForgetExpired(ctx context.Context) (int, error) {
	allRecords := s.coll.All()
	now := time.Now().Unix()
	var deleted int

	for _, r := range allRecords {
		expiresAt := getInt64Payload(r.Payload, "expires_at")
		if expiresAt > 0 && expiresAt < now {
			if err := s.coll.Delete(r.ID); err == nil {
				deleted++
			}
		}
	}

	return deleted, nil
}

// Stats returns memory store statistics.
func (s *MemoryStore) Stats(ctx context.Context) (*Stats, error) {
	allRecords := s.coll.All()

	stats := &Stats{
		TagCounts: make(map[string]int64),
	}

	now := time.Now().Unix()
	var oldestTime, newestTime int64

	for _, r := range allRecords {
		// Check if expired
		expiresAt := getInt64Payload(r.Payload, "expires_at")
		if expiresAt > 0 && expiresAt < now {
			stats.ExpiredMemories++
			continue
		}

		stats.TotalMemories++

		// Track creation times
		createdAt := getInt64Payload(r.Payload, "created_at")
		if createdAt > 0 {
			if oldestTime == 0 || createdAt < oldestTime {
				oldestTime = createdAt
			}
			if createdAt > newestTime {
				newestTime = createdAt
			}
		}

		// Count tags
		tagsStr := getStringPayload(r.Payload, "tags")
		if tagsStr != "" {
			for _, tag := range strings.Split(tagsStr, ",") {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					stats.TagCounts[tag]++
				}
			}
		}
	}

	stats.TotalTags = len(stats.TagCounts)

	if oldestTime > 0 {
		t := time.Unix(oldestTime, 0)
		stats.OldestMemory = &t
	}
	if newestTime > 0 {
		t := time.Unix(newestTime, 0)
		stats.NewestMemory = &t
	}

	return stats, nil
}

// Close closes the memory store.
func (s *MemoryStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Helper functions

func recordToMemory(r *veclite.Record, score float32) Memory {
	createdAt := time.Now()
	if ts := getInt64Payload(r.Payload, "created_at"); ts > 0 {
		createdAt = time.Unix(ts, 0)
	}

	var expiresAt *time.Time
	if ts := getInt64Payload(r.Payload, "expires_at"); ts > 0 {
		t := time.Unix(ts, 0)
		expiresAt = &t
	}

	var tags []string
	if tagsStr := getStringPayload(r.Payload, "tags"); tagsStr != "" {
		for _, tag := range strings.Split(tagsStr, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				tags = append(tags, tag)
			}
		}
	}

	return Memory{
		ID:         r.ID,
		Content:    getStringPayload(r.Payload, "content"),
		Importance: getFloat64Payload(r.Payload, "importance"),
		Tags:       tags,
		CreatedAt:  createdAt,
		ExpiresAt:  expiresAt,
		Score:      score,
	}
}

func getStringPayload(payload map[string]any, key string) string {
	if v, ok := payload[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getInt64Payload(payload map[string]any, key string) int64 {
	if v, ok := payload[key]; ok {
		switch n := v.(type) {
		case int64:
			return n
		case int:
			return int64(n)
		case float64:
			return int64(n)
		}
	}
	return 0
}

func getFloat64Payload(payload map[string]any, key string) float64 {
	if v, ok := payload[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case float32:
			return float64(n)
		case int64:
			return float64(n)
		case int:
			return float64(n)
		}
	}
	return 0
}
