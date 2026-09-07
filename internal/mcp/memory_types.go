package mcp

// MemoryRememberInput is the input for memory_remember.
type MemoryRememberInput struct {
	Content    string   `json:"content" jsonschema:"The content to remember. This can be any text you want to store for later recall."`
	Importance float64  `json:"importance,omitempty" jsonschema:"Importance level from 0.0 to 1.0. Higher importance memories are prioritized in recall. Default is 0.5."`
	Tags       []string `json:"tags,omitempty" jsonschema:"Categorization tags for filtering and organizing memories."`
	TTLHours   int      `json:"ttl_hours,omitempty" jsonschema:"Time to live in hours. Memory expires after this duration. 0 means no expiration."`
	// AllowDuplicate overrides near-duplicate refusal. The stored duplicate is
	// not linked to its twin.
	AllowDuplicate bool `json:"allow_duplicate,omitempty" jsonschema:"Set true to store even when the content is a near-duplicate of an existing memory (the tool otherwise refuses and names the existing memory). Default false."`
}

// MemoryRecallInput is the input for memory_recall.
type MemoryRecallInput struct {
	Query         string   `json:"query" jsonschema:"Natural language search query to find relevant memories."`
	Limit         int      `json:"limit,omitempty" jsonschema:"Maximum number of results to return. Default is 10."`
	Tags          []string `json:"tags,omitempty" jsonschema:"Filter results to only include memories with these tags."`
	MinImportance float64  `json:"min_importance,omitempty" jsonschema:"Minimum importance threshold. Only return memories with importance >= this value."`
}

// MemoryForgetInput is the input for memory_forget.
type MemoryForgetInput struct {
	ID             uint64   `json:"id,omitempty" jsonschema:"Delete a specific memory by its ID."`
	Tags           []string `json:"tags,omitempty" jsonschema:"Delete all memories that have any of these tags."`
	OlderThanHours int      `json:"older_than_hours,omitempty" jsonschema:"Delete memories older than this many hours."`
	Confirm        string   `json:"confirm,omitempty" jsonschema:"Set to yes to confirm bulk deletion (required when deleting by tags or age)."`
}

// MemoryStatsInput is the input for memory_stats (no parameters).
type MemoryStatsInput struct{}

// MemoryRelatedResult is a resolved link to a semantically related memory.
type MemoryRelatedResult struct {
	ID      uint64 `json:"id"`
	Content string `json:"content"`
}

// MemoryResult represents a memory in recall results.
type MemoryResult struct {
	ID             uint64                `json:"id"`
	Content        string                `json:"content"`
	Importance     float64               `json:"importance"`
	Tags           []string              `json:"tags,omitempty"`
	CreatedAt      string                `json:"created_at"`
	ExpiresAt      string                `json:"expires_at,omitempty"`
	AccessCount    int64                 `json:"access_count,omitempty"`
	LastAccessedAt string                `json:"last_accessed_at,omitempty"`
	Related        []MemoryRelatedResult `json:"related,omitempty"`
	Score          float32               `json:"score"`
}

// MemoryStatsResult contains memory store statistics.
type MemoryStatsResult struct {
	TotalMemories   int64            `json:"total_memories"`
	TotalTags       int              `json:"total_tags"`
	LinkedMemories  int64            `json:"linked_memories"`
	OldestMemory    string           `json:"oldest_memory,omitempty"`
	NewestMemory    string           `json:"newest_memory,omitempty"`
	ExpiredMemories int64            `json:"expired_memories"`
	TagCounts       map[string]int64 `json:"tag_counts,omitempty"`
}
