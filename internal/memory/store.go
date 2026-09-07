package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
	"github.com/abdul-hamid-achik/veclite"
)

// Payload keys for the compiled-memory layer: usage tracking and the
// bidirectional related-links graph, both stored in the veclite payload so
// they survive crash recovery through the write-ahead log.
const (
	payloadAccessCount    = "access_count"
	payloadLastAccessedAt = "last_accessed_at"
	payloadRelated        = "related"
)

// maxRelatedLinks bounds how many semantic links a single memory carries, so
// hub memories cannot grow payloads without limit. When a memory's link list
// is full, new backlinks to it are skipped (never evicted).
const maxRelatedLinks = 8

// relatedPreviewLimit bounds how many related memories Recall resolves with
// content, so a 10-hit recall cannot return 80 embedded previews.
const relatedPreviewLimit = 3

// accessSaturation is the access count at which the usage lift reaches half
// of its cap: lift = UsageBoost * count/(count+accessSaturation) * recency.
const accessSaturation = 5.0

// MemoryStore manages persistent memory using veclite.
type MemoryStore struct {
	mu       sync.Mutex
	db       *veclite.DB
	coll     *veclite.Collection
	provider embed.Provider
	config   *Config
}

// Memory represents a stored memory with metadata.
type Memory struct {
	ID             uint64
	Content        string
	Importance     float64
	Tags           []string
	CreatedAt      time.Time
	ExpiresAt      *time.Time
	AccessCount    int64
	LastAccessedAt *time.Time
	Related        []RelatedMemory
	Score          float32 // Search relevance score

	// relatedIDs holds the raw payload link IDs, populated by recordToMemory
	// for the related-preview resolution in Recall.
	relatedIDs []uint64
}

// RelatedMemory is a resolved semantic link to another memory, surfaced by
// Recall so connections between memories are visible without a second query.
type RelatedMemory struct {
	ID      uint64
	Content string
}

// RememberResult reports what Remember stored.
type RememberResult struct {
	// ID is the new memory's ID.
	ID uint64
	// Related lists the existing memories the new one was linked to. Links
	// are bidirectional: the other side also records the new ID.
	Related []RelatedMemory
}

// DuplicateError is returned by Remember when new content is a near-duplicate
// of an existing memory and AllowDuplicate was not set. ExistingID points at
// the memory that already holds the information.
type DuplicateError struct {
	ExistingID uint64
	Score      float64
	Content    string
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("near-duplicate of memory %d (similarity %.2f): %s",
		e.ExistingID, e.Score, truncateRunes(e.Content, 80))
}

// RememberOptions contains options for storing a memory.
type RememberOptions struct {
	Importance float64  // 0.0-1.0, default 0.5
	Tags       []string // Categorization tags
	TTLHours   int      // Expiration in hours (0=never)
	// AllowDuplicate stores the content even when it is a near-duplicate of
	// an existing memory (similarity >= DedupThreshold). The duplicate is
	// stored but not linked to its twin.
	AllowDuplicate bool
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
	LinkedMemories  int64 // memories carrying at least one related-link
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

	// Open veclite database. The memory store is a long-lived writer (MCP
	// server / daemon), so keep a write-ahead log: remembered items survive a
	// crash between snapshot saves.
	db, err := veclite.Open(cfg.DBPath, veclite.WithWAL(true))
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
func (s *MemoryStore) Remember(ctx context.Context, content string, opts RememberOptions) (RememberResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if content == "" {
		return RememberResult{}, fmt.Errorf("content cannot be empty")
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
		return RememberResult{}, fmt.Errorf("failed to generate embedding: %w", err)
	}

	// Duplicate detection and related-link discovery in one similarity probe
	// against the whole store — no filters, because both semantics are global
	// by definition. Expired memories are neither duplicate nor link targets.
	var relatedIDs []uint64
	if s.config.DedupThreshold > 0 || s.config.RelatedThreshold > 0 {
		hits, err := s.coll.Search(embedding, veclite.TopK(maxRelatedLinks+1))
		if err != nil {
			return RememberResult{}, fmt.Errorf("similarity probe failed: %w", err)
		}
		for _, h := range hits {
			if memoryExpired(h.Record.Payload) {
				continue
			}
			sim := float64(h.Score)
			if s.config.DedupThreshold > 0 && sim >= s.config.DedupThreshold {
				if !opts.AllowDuplicate {
					return RememberResult{}, &DuplicateError{
						ExistingID: h.Record.ID,
						Score:      sim,
						Content:    getStringPayload(h.Record.Payload, "content"),
					}
				}
				// Forced duplicate: stored, but not linked to its twin —
				// linking identical content is noise, not connection.
				continue
			}
			if s.config.RelatedThreshold > 0 && sim >= s.config.RelatedThreshold && len(relatedIDs) < maxRelatedLinks {
				relatedIDs = append(relatedIDs, h.Record.ID)
			}
		}
	}

	// Calculate expiration time
	var expiresAt int64
	if opts.TTLHours > 0 {
		expiresAt = time.Now().Add(time.Duration(opts.TTLHours) * time.Hour).Unix()
	}

	// Build payload
	payload := map[string]any{
		"content":             content,
		"importance":          opts.Importance,
		"tags":                strings.Join(opts.Tags, ","),
		"created_at":          time.Now().Unix(),
		"expires_at":          expiresAt,
		payloadAccessCount:    int64(0),
		payloadLastAccessedAt: int64(0),
		payloadRelated:        joinIDs(relatedIDs),
	}

	// Insert into veclite. Importance is set both in the payload (for GTE
	// filtering) and on the Record itself (so WithImportanceBoost sees it
	// at recall time).
	id, err := s.coll.InsertWithOptions(embedding, payload,
		veclite.WithImportance(float32(opts.Importance)))
	if err != nil {
		return RememberResult{}, fmt.Errorf("failed to store memory: %w", err)
	}

	// Backlinks make the connection graph bidirectional. Best-effort: a
	// failed backlink leaves a one-way link, never breaks the store.
	for _, otherID := range relatedIDs {
		s.linkMemories(otherID, id)
	}

	// Resolve link previews for the immediate response.
	var related []RelatedMemory
	for _, otherID := range relatedIDs {
		rec, err := s.coll.Get(otherID)
		if err != nil || memoryExpired(rec.Payload) {
			continue
		}
		related = append(related, RelatedMemory{
			ID:      rec.ID,
			Content: getStringPayload(rec.Payload, "content"),
		})
	}

	return RememberResult{ID: id, Related: related}, nil
}

// linkMemories records a backlink from otherID to id in otherID's payload.
// Callers hold s.mu. A full link list or a missing record silently skips the
// backlink.
func (s *MemoryStore) linkMemories(otherID, id uint64) {
	rec, err := s.coll.Get(otherID)
	if err != nil {
		return
	}
	links := parseRelatedIDs(rec.Payload)
	for _, l := range links {
		if l == id {
			return // already linked
		}
	}
	if len(links) >= maxRelatedLinks {
		return
	}
	payload := clonePayload(rec.Payload)
	payload[payloadRelated] = joinIDs(append(links, id))
	_ = s.coll.Update(otherID, payload)
}

// Recall searches memories semantically.
func (s *MemoryStore) Recall(ctx context.Context, query string, opts RecallOptions) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	// Filter by tags if specified. veclite combines multiple filters with AND
	// logic, so one Contains filter per requested tag means a record must carry
	// ALL of them (AND semantics) — the scoping the G2 governance hinges on
	// (e.g. tags=["codemap", <project_key>] matches only that project's
	// codemap-scoped memories, never every "codemap" memory across projects).
	//
	// Contains is a SUBSTRING match against the comma-joined tag string, so it
	// can over-match ("codemap" ⊂ "codemapper", a 12-hex key ⊂ a longer token).
	// We therefore use it only as a cheap pre-filter and re-verify exact tag
	// membership in Go below, so the scope can never leak on a substring.
	if len(opts.Tags) > 0 {
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

	// Ranking intelligence: recent + important memories surface first.
	// Exponential decay halves a memory's score every half-life; the
	// importance boost lifts records the agent marked as significant.
	// Both are ranking modifiers only — no memory is ever excluded by them.
	if s.config.DecayHalfLifeHours > 0 {
		searchOpts = append(searchOpts, veclite.WithDecay(
			veclite.DecayExponential,
			time.Duration(s.config.DecayHalfLifeHours)*time.Hour,
		))
	}
	if s.config.ImportanceBoost > 0 {
		searchOpts = append(searchOpts, veclite.WithImportanceBoost(float32(s.config.ImportanceBoost)))
	}

	// Search
	results, err := s.coll.Search(embedding, searchOpts...)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	// Convert results, filtering expired memories and re-verifying tag scope.
	memories := make([]Memory, 0, len(results))
	for _, r := range results {
		expiresAt := getInt64Payload(r.Record.Payload, "expires_at")
		if expiresAt > 0 && expiresAt < time.Now().Unix() {
			continue // Skip expired memory
		}

		memory := recordToMemory(r.Record, r.Score)

		// Exact tag-AND re-check: the veclite Contains pre-filter is a
		// substring match, so re-verify the parsed tag set carries every
		// requested tag exactly. Prevents cross-project leakage on a
		// substring collision.
		if !hasAllTags(memory.Tags, opts.Tags) {
			continue
		}

		memories = append(memories, memory)
	}

	// Learning from use: memories the agent actually recalls get a
	// usage-lifted rank, so working knowledge resurface before stale knowledge
	// with similar similarity. Ranking modifier only — never excludes.
	if s.config.UsageBoost > 0 {
		sort.SliceStable(memories, func(i, j int) bool {
			li, lj := usageLift(s.config, &memories[i]), usageLift(s.config, &memories[j])
			si, sj := memories[i].Score*(1+float32(li)), memories[j].Score*(1+float32(lj))
			if si != sj {
				return si > sj
			}
			return memories[i].ID < memories[j].ID
		})
	}

	if len(memories) > opts.Limit {
		memories = memories[:opts.Limit]
	}

	// Persist this recall's usage so future recalls learn from it. Payload-side
	// (not veclite's in-memory access tracking) so counts survive crash
	// recovery through the WAL.
	if s.config.UsageBoost > 0 {
		now := time.Now().Unix()
		for i := range memories {
			s.recordAccess(&memories[i], now)
		}
	}

	// Resolve related-memory previews so connections surface without a second
	// query. The payload keeps up to maxRelatedLinks links; content previews
	// are capped at relatedPreviewLimit.
	for i := range memories {
		for _, linkID := range memories[i].relatedIDs {
			if len(memories[i].Related) >= relatedPreviewLimit {
				break
			}
			rec, err := s.coll.Get(linkID)
			if err != nil || memoryExpired(rec.Payload) {
				continue
			}
			memories[i].Related = append(memories[i].Related, RelatedMemory{
				ID:      rec.ID,
				Content: getStringPayload(rec.Payload, "content"),
			})
		}
	}

	return memories, nil
}

// usageLift is the learning-from-use ranking term: saturating in access count
// (half effect at accessSaturation accesses) and fading with the recency of
// the last access at the same half-life as creation decay, so a heavily used
// memory that goes stale loses its lift. Returns a fractional multiplier cap
// in [0, UsageBoost].
func usageLift(cfg *Config, m *Memory) float64 {
	if cfg.UsageBoost <= 0 {
		return 0
	}
	count := float64(m.AccessCount)
	if count == 0 {
		return 0
	}
	sat := count / (count + accessSaturation)
	recency := 1.0
	if m.LastAccessedAt != nil && cfg.DecayHalfLifeHours > 0 {
		hours := time.Since(*m.LastAccessedAt).Hours()
		recency = math.Pow(2, -hours/float64(cfg.DecayHalfLifeHours))
	}
	return cfg.UsageBoost * sat * recency
}

// recordAccess increments a recalled memory's access count and last-access
// timestamp in its payload. Callers hold s.mu. veclite's Update replaces the
// whole payload, so the record's current payload is cloned and rewritten.
// Best-effort: a lost access count only costs a little ranking signal.
func (s *MemoryStore) recordAccess(m *Memory, now int64) {
	rec, err := s.coll.Get(m.ID)
	if err != nil {
		return
	}
	payload := clonePayload(rec.Payload)
	count := getInt64Payload(payload, payloadAccessCount) + 1
	payload[payloadAccessCount] = count
	payload[payloadLastAccessedAt] = now
	if err := s.coll.Update(m.ID, payload); err != nil {
		return
	}
	m.AccessCount = count
	ts := time.Unix(now, 0)
	m.LastAccessedAt = &ts
}

// hasAllTags reports whether tags contains every tag in want (exact match,
// AND semantics). An empty want matches everything.
func hasAllTags(tags, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		set[t] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
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

		// Track the connection graph's footprint
		if len(parseRelatedIDs(r.Payload)) > 0 {
			stats.LinkedMemories++
		}

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

// Ping checks whether the embedding provider backing this store is reachable.
// Callers use it to classify a Recall/Remember failure as "provider
// unavailable" versus a genuine data error, so a consumer can distinguish
// "recall unavailable" from "no matching memory".
func (s *MemoryStore) Ping(ctx context.Context) error {
	if s.provider == nil {
		return fmt.Errorf("embedding provider not configured")
	}
	return s.provider.Ping(ctx)
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

	var lastAccessedAt *time.Time
	if ts := getInt64Payload(r.Payload, payloadLastAccessedAt); ts > 0 {
		t := time.Unix(ts, 0)
		lastAccessedAt = &t
	}

	return Memory{
		ID:             r.ID,
		Content:        getStringPayload(r.Payload, "content"),
		Importance:     getFloat64Payload(r.Payload, "importance"),
		Tags:           tags,
		CreatedAt:      createdAt,
		ExpiresAt:      expiresAt,
		AccessCount:    getInt64Payload(r.Payload, payloadAccessCount),
		LastAccessedAt: lastAccessedAt,
		Related:        nil, // resolved in Recall after the top-limit trim
		relatedIDs:     parseRelatedIDs(r.Payload),
		Score:          score,
	}
}

// memoryExpired reports whether a payload's TTL has lapsed. Memories that no
// longer exist semantically (expired) are neither duplicate nor link targets.
func memoryExpired(payload map[string]any) bool {
	expiresAt := getInt64Payload(payload, "expires_at")
	return expiresAt > 0 && expiresAt < time.Now().Unix()
}

// parseRelatedIDs extracts the related-link ID list from a payload.
func parseRelatedIDs(payload map[string]any) []uint64 {
	s := getStringPayload(payload, payloadRelated)
	if s == "" {
		return nil
	}
	var ids []uint64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, err := strconv.ParseUint(part, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// joinIDs serializes a related-link ID list for payload storage.
func joinIDs(ids []uint64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatUint(id, 10)
	}
	return strings.Join(parts, ",")
}

// clonePayload copies a record payload before mutating it for Update, which
// replaces the map wholesale.
func clonePayload(payload map[string]any) map[string]any {
	clone := make(map[string]any, len(payload))
	for k, v := range payload {
		clone[k] = v
	}
	return clone
}

// truncateRunes truncates s to maxRunes runes, adding "..." when truncated.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-3]) + "..."
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
