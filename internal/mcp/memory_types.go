package mcp

// MemoryRememberInput is the input for memory_remember.
type MemoryRememberInput struct {
	Content    string   `json:"content" jsonschema:"required,description=The content to remember. This can be any text you want to store for later recall."`
	Importance float64  `json:"importance,omitempty" jsonschema:"description=Importance level from 0.0 to 1.0. Higher importance memories are prioritized in recall. Default is 0.5."`
	Tags       []string `json:"tags,omitempty" jsonschema:"description=Categorization tags for filtering and organizing memories."`
	TTLHours   int      `json:"ttl_hours,omitempty" jsonschema:"description=Time to live in hours. Memory expires after this duration. 0 means no expiration."`
}

// MemoryRecallInput is the input for memory_recall.
type MemoryRecallInput struct {
	Query         string   `json:"query" jsonschema:"required,description=Natural language search query to find relevant memories."`
	Limit         int      `json:"limit,omitempty" jsonschema:"description=Maximum number of results to return. Default is 10."`
	Tags          []string `json:"tags,omitempty" jsonschema:"description=Filter results to only include memories with these tags."`
	MinImportance float64  `json:"min_importance,omitempty" jsonschema:"description=Minimum importance threshold. Only return memories with importance >= this value."`
}

// MemoryForgetInput is the input for memory_forget.
type MemoryForgetInput struct {
	ID             uint64   `json:"id,omitempty" jsonschema:"description=Delete a specific memory by its ID."`
	Tags           []string `json:"tags,omitempty" jsonschema:"description=Delete all memories that have any of these tags."`
	OlderThanHours int      `json:"older_than_hours,omitempty" jsonschema:"description=Delete memories older than this many hours."`
	Confirm        string   `json:"confirm,omitempty" jsonschema:"description=Set to 'yes' to confirm bulk deletion (required when deleting by tags or age)."`
}

// MemoryStatsInput is the input for memory_stats (no parameters).
type MemoryStatsInput struct{}

// MemoryResult represents a memory in recall results.
type MemoryResult struct {
	ID         uint64   `json:"id"`
	Content    string   `json:"content"`
	Importance float64  `json:"importance"`
	Tags       []string `json:"tags,omitempty"`
	CreatedAt  string   `json:"created_at"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	Score      float32  `json:"score"`
}

// MemoryStatsResult contains memory store statistics.
type MemoryStatsResult struct {
	TotalMemories   int64            `json:"total_memories"`
	TotalTags       int              `json:"total_tags"`
	OldestMemory    string           `json:"oldest_memory,omitempty"`
	NewestMemory    string           `json:"newest_memory,omitempty"`
	ExpiredMemories int64            `json:"expired_memories"`
	TagCounts       map[string]int64 `json:"tag_counts,omitempty"`
}
