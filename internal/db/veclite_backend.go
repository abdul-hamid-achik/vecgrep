package db

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/veclite"
)

// SearchMode defines how search is performed.
type SearchMode string

const (
	// SearchModeSemantic uses pure vector similarity search.
	SearchModeSemantic SearchMode = "semantic"
	// SearchModeKeyword uses BM25 text search only.
	SearchModeKeyword SearchMode = "keyword"
	// SearchModeHybrid combines vector and text search.
	SearchModeHybrid SearchMode = "hybrid"
)

// SearchExplanation provides diagnostic info about a search.
type SearchExplanation struct {
	IndexType    string
	NodesVisited int
	Duration     time.Duration
	Mode         SearchMode
}

// ChunkRecord represents a chunk with all its metadata from veclite.
type ChunkRecord struct {
	ID           uint64
	FilePath     string
	RelativePath string
	FileHash     string
	FileSize     int64
	Language     string
	Content      string
	StartLine    int
	EndLine      int
	StartByte    int
	EndByte      int
	ChunkType    string
	SymbolName   string
	ProjectRoot  string
	IndexedAt    time.Time
	Vector       []float32
}

// FileInfo represents file information stored in veclite.
type FileInfo struct {
	Path         string
	RelativePath string
	Hash         string
	Size         int64
	Language     string
	IndexedAt    time.Time
}

// Stats contains database statistics.
type Stats struct {
	TotalChunks   int64
	TotalFiles    int64
	TotalProjects int64
	Languages     map[string]int64
	ChunkTypes    map[string]int64
}

// VecLiteBackend implements the database layer using VecLite with HNSW indexing.
// All metadata is stored in vector payload - no SQLite needed.
type VecLiteBackend struct {
	db         *veclite.DB
	coll       *veclite.Collection
	dbPath     string
	dimensions int
}

// NewVecLiteBackend creates a new VecLite backend.
// The dbPath should point to the VecLite database file.
func NewVecLiteBackend(dbPath string) *VecLiteBackend {
	return &VecLiteBackend{
		dbPath: dbPath,
	}
}

// Init initializes the VecLite backend with the given dimensions.
func (b *VecLiteBackend) Init(dimensions int) error {
	b.dimensions = dimensions

	// Open VecLite database
	db, err := veclite.Open(b.dbPath)
	if err != nil {
		return fmt.Errorf("failed to open veclite database: %w", err)
	}
	b.db = db

	// Create or get collection with HNSW index
	// Use cosine distance which is standard for normalized embeddings
	coll, err := db.CreateCollection("chunks",
		veclite.WithDimension(dimensions),
		veclite.WithDistanceType(veclite.DistanceCosine), // Use cosine for embeddings
		veclite.WithHNSW(16, 200),                        // M=16, efConstruction=200
	)
	if err != nil {
		// Collection might already exist, try to get it
		coll, err = db.GetCollection("chunks")
		if err != nil {
			return fmt.Errorf("failed to create/get collection: %w", err)
		}
	}
	b.coll = coll

	return nil
}

// InsertChunk inserts a chunk with all its metadata and embedding.
func (b *VecLiteBackend) InsertChunk(chunk ChunkRecord, embedding []float32) (uint64, error) {
	if len(embedding) != b.dimensions {
		return 0, fmt.Errorf("embedding dimension mismatch: got %d, expected %d", len(embedding), b.dimensions)
	}

	// Generate unique chunk key for upsert operations
	chunkKey := fmt.Sprintf("%s:%d", chunk.RelativePath, chunk.StartLine)

	payload := map[string]any{
		// File info (denormalized)
		"file_path":     chunk.FilePath,
		"relative_path": chunk.RelativePath,
		"file_hash":     chunk.FileHash,
		"file_size":     chunk.FileSize,
		"language":      chunk.Language,

		// Chunk info
		"content":     chunk.Content,
		"start_line":  chunk.StartLine,
		"end_line":    chunk.EndLine,
		"start_byte":  chunk.StartByte,
		"end_byte":    chunk.EndByte,
		"chunk_type":  chunk.ChunkType,
		"symbol_name": chunk.SymbolName,

		// Unique key for upsert
		"chunk_key": chunkKey,

		// Project info
		"project_root": chunk.ProjectRoot,
		"indexed_at":   chunk.IndexedAt.Format(time.RFC3339),
	}

	id, err := b.coll.Insert(embedding, payload)
	if err != nil {
		return 0, err
	}

	return id, nil
}

// InsertChunkBatch inserts multiple chunks in a single batch operation.
// Returns the IDs of the inserted chunks.
func (b *VecLiteBackend) InsertChunkBatch(chunks []ChunkRecord, embeddings [][]float32) ([]uint64, error) {
	if len(chunks) != len(embeddings) {
		return nil, fmt.Errorf("chunks and embeddings length mismatch: %d vs %d", len(chunks), len(embeddings))
	}

	if len(chunks) == 0 {
		return nil, nil
	}

	vectors := make([][]float32, len(chunks))
	payloads := make([]map[string]any, len(chunks))

	for i, chunk := range chunks {
		if len(embeddings[i]) != b.dimensions {
			return nil, fmt.Errorf("embedding %d dimension mismatch: got %d, expected %d", i, len(embeddings[i]), b.dimensions)
		}

		vectors[i] = embeddings[i]

		chunkKey := fmt.Sprintf("%s:%d", chunk.RelativePath, chunk.StartLine)
		payloads[i] = map[string]any{
			"file_path":     chunk.FilePath,
			"relative_path": chunk.RelativePath,
			"file_hash":     chunk.FileHash,
			"file_size":     chunk.FileSize,
			"language":      chunk.Language,
			"content":       chunk.Content,
			"start_line":    chunk.StartLine,
			"end_line":      chunk.EndLine,
			"start_byte":    chunk.StartByte,
			"end_byte":      chunk.EndByte,
			"chunk_type":    chunk.ChunkType,
			"symbol_name":   chunk.SymbolName,
			"chunk_key":     chunkKey,
			"project_root":  chunk.ProjectRoot,
			"indexed_at":    chunk.IndexedAt.Format(time.RFC3339),
		}
	}

	// Use InsertBatch for batch insert
	ids, err := b.coll.InsertBatch(vectors, payloads)
	if err != nil {
		return nil, fmt.Errorf("batch insert failed: %w", err)
	}

	return ids, nil
}

// UpsertChunk inserts or updates a chunk using a unique key.
// The key is based on relative_path:start_line for chunk identification.
// Returns the ID and whether it was a new insert (true) or update (false).
func (b *VecLiteBackend) UpsertChunk(chunk ChunkRecord, embedding []float32) (uint64, bool, error) {
	if len(embedding) != b.dimensions {
		return 0, false, fmt.Errorf("embedding dimension mismatch: got %d, expected %d", len(embedding), b.dimensions)
	}

	chunkKey := fmt.Sprintf("%s:%d", chunk.RelativePath, chunk.StartLine)

	payload := map[string]any{
		"file_path":     chunk.FilePath,
		"relative_path": chunk.RelativePath,
		"file_hash":     chunk.FileHash,
		"file_size":     chunk.FileSize,
		"language":      chunk.Language,
		"content":       chunk.Content,
		"start_line":    chunk.StartLine,
		"end_line":      chunk.EndLine,
		"start_byte":    chunk.StartByte,
		"end_byte":      chunk.EndByte,
		"chunk_type":    chunk.ChunkType,
		"symbol_name":   chunk.SymbolName,
		"chunk_key":     chunkKey,
		"project_root":  chunk.ProjectRoot,
		"indexed_at":    chunk.IndexedAt.Format(time.RFC3339),
	}

	id, isNew, err := b.coll.UpsertByKey("chunk_key", chunkKey, embedding, payload)
	if err != nil {
		return 0, false, fmt.Errorf("upsert failed: %w", err)
	}

	return id, isNew, nil
}

// InsertEmbedding inserts an embedding for a chunk (legacy compatibility).
// Deprecated: Use InsertChunk instead for full metadata storage.
func (b *VecLiteBackend) InsertEmbedding(chunkID int64, embedding []float32) error {
	if len(embedding) != b.dimensions {
		return fmt.Errorf("embedding dimension mismatch: got %d, expected %d", len(embedding), b.dimensions)
	}

	// Legacy mode: store with minimal payload
	_, err := b.coll.Insert(embedding, map[string]any{"chunk_id": chunkID})
	return err
}

// DeleteEmbedding removes an embedding for a chunk (legacy compatibility).
func (b *VecLiteBackend) DeleteEmbedding(chunkID int64) error {
	_, err := b.coll.DeleteWhere(veclite.Equal("chunk_id", chunkID))
	return err
}

// DeleteByFilePath removes all chunks for a given file path.
func (b *VecLiteBackend) DeleteByFilePath(filePath string) (int64, error) {
	// Try both relative and absolute path matching
	deleted1, err := b.coll.DeleteWhere(veclite.Equal("file_path", filePath))
	if err != nil {
		return 0, err
	}

	deleted2, err := b.coll.DeleteWhere(veclite.Equal("relative_path", filePath))
	if err != nil {
		return int64(deleted1), err
	}

	return int64(deleted1 + deleted2), nil
}

// DeleteByProjectRoot removes all chunks for a project.
// If all records are deleted, the collection is recreated to reset the HNSW index.
func (b *VecLiteBackend) DeleteByProjectRoot(projectRoot string) (int64, error) {
	deleted, err := b.coll.DeleteWhere(veclite.Equal("project_root", projectRoot))
	if err != nil {
		return int64(deleted), err
	}

	// If the collection is now empty, recreate it to reset the HNSW index
	// This works around a bug in veclite where the HNSW index becomes
	// corrupted after deleting all records
	if b.coll.Count() == 0 {
		if err := b.DeleteAll(); err != nil {
			return int64(deleted), err
		}
	}

	return int64(deleted), nil
}

// GetFileHashes returns a map of relative_path -> file_hash for a project.
// Used for incremental indexing to detect changed files.
func (b *VecLiteBackend) GetFileHashes(projectRoot string) (map[string]string, error) {
	allRecords := b.coll.All()

	fileHashes := make(map[string]string)
	for _, r := range allRecords {
		root := getStringPayload(r.Payload, "project_root")
		if root != projectRoot {
			continue
		}

		relPath := getStringPayload(r.Payload, "relative_path")
		hash := getStringPayload(r.Payload, "file_hash")

		if relPath != "" && hash != "" {
			fileHashes[relPath] = hash
		}
	}

	return fileHashes, nil
}

// GetChunksByFile returns all chunks for a specific file.
func (b *VecLiteBackend) GetChunksByFile(filePath string) ([]ChunkRecord, error) {
	// Search by relative_path first
	records, err := b.coll.Find(veclite.Equal("relative_path", filePath))
	if err != nil {
		return nil, err
	}

	// If no results, try absolute path
	if len(records) == 0 {
		records, err = b.coll.Find(veclite.Equal("file_path", filePath))
		if err != nil {
			return nil, err
		}
	}

	chunks := make([]ChunkRecord, 0, len(records))
	for _, r := range records {
		chunks = append(chunks, recordToChunk(r))
	}

	return chunks, nil
}

// GetChunkByLocation finds a chunk containing the given file path and line number.
func (b *VecLiteBackend) GetChunkByLocation(filePath string, line int) (*ChunkRecord, error) {
	// Get all chunks for the file
	chunks, err := b.GetChunksByFile(filePath)
	if err != nil {
		return nil, err
	}

	// Find the smallest chunk containing the line
	var bestChunk *ChunkRecord
	var bestSize int

	for i := range chunks {
		c := &chunks[i]
		if c.StartLine <= line && c.EndLine >= line {
			size := c.EndLine - c.StartLine
			if bestChunk == nil || size < bestSize {
				bestChunk = c
				bestSize = size
			}
		}
	}

	if bestChunk == nil {
		return nil, fmt.Errorf("no chunk found at %s:%d", filePath, line)
	}

	return bestChunk, nil
}

// SearchResult represents a vector search result with full metadata.
type SearchResult struct {
	ChunkID  int64
	Distance float32
	Chunk    *ChunkRecord // Full chunk data when available
}

// SearchEmbeddings performs a vector similarity search.
func (b *VecLiteBackend) SearchEmbeddings(queryEmbedding []float32, limit int) ([]SearchResult, error) {
	if len(queryEmbedding) != b.dimensions {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, expected %d", len(queryEmbedding), b.dimensions)
	}

	results, err := b.coll.Search(queryEmbedding, veclite.TopK(limit))
	if err != nil {
		return nil, err
	}

	searchResults := make([]SearchResult, 0, len(results))
	for _, r := range results {
		chunk := recordToChunk(r.Record)
		sr := SearchResult{
			ChunkID:  int64(r.Record.ID),
			Distance: r.Score,
			Chunk:    &chunk,
		}

		// Check for legacy chunk_id in payload
		if chunkID := getInt64Payload(r.Record.Payload, "chunk_id"); chunkID != 0 {
			sr.ChunkID = chunkID
			sr.Chunk = nil // Legacy record without full metadata
		}

		searchResults = append(searchResults, sr)
	}

	return searchResults, nil
}

// GetChunkByID retrieves a full chunk record by its vector ID.
func (b *VecLiteBackend) GetChunkByID(chunkID int64) (*ChunkRecord, error) {
	record, err := b.coll.Get(uint64(chunkID))
	if err != nil {
		return nil, fmt.Errorf("chunk not found for ID %d: %w", chunkID, err)
	}
	if record == nil {
		return nil, fmt.Errorf("chunk not found for ID %d", chunkID)
	}
	chunk := recordToChunk(record)
	return &chunk, nil
}

// GetEmbedding retrieves the embedding for a chunk by its ID.
func (b *VecLiteBackend) GetEmbedding(chunkID int64) ([]float32, error) {
	// First try by record ID
	record, err := b.coll.Get(uint64(chunkID))
	if err == nil && record != nil {
		return record.Vector, nil
	}

	// Fall back to legacy chunk_id lookup
	records, err := b.coll.Find(veclite.Equal("chunk_id", chunkID))
	if err != nil {
		return nil, err
	}

	if len(records) == 0 {
		return nil, fmt.Errorf("embedding not found for chunk %d", chunkID)
	}

	return records[0].Vector, nil
}

// Count returns the number of embeddings stored.
func (b *VecLiteBackend) Count() (int64, error) {
	return int64(b.coll.Count()), nil
}

// GetStats returns comprehensive statistics about the index.
func (b *VecLiteBackend) GetStats(projectRoot string) (*Stats, error) {
	allRecords := b.coll.All()

	stats := &Stats{
		Languages:  make(map[string]int64),
		ChunkTypes: make(map[string]int64),
	}

	filesSet := make(map[string]bool)
	projectsSet := make(map[string]bool)

	for _, r := range allRecords {
		root := getStringPayload(r.Payload, "project_root")

		// Filter by project if specified
		if projectRoot != "" && root != projectRoot {
			continue
		}

		stats.TotalChunks++

		relPath := getStringPayload(r.Payload, "relative_path")
		if relPath != "" {
			filesSet[root+":"+relPath] = true
		}

		if root != "" {
			projectsSet[root] = true
		}

		lang := getStringPayload(r.Payload, "language")
		if lang != "" {
			stats.Languages[lang]++
		}

		chunkType := getStringPayload(r.Payload, "chunk_type")
		if chunkType != "" {
			stats.ChunkTypes[chunkType]++
		}
	}

	stats.TotalFiles = int64(len(filesSet))
	stats.TotalProjects = int64(len(projectsSet))

	return stats, nil
}

// DeleteAll removes all embeddings by recreating the collection.
// This ensures the HNSW index is properly reset.
func (b *VecLiteBackend) DeleteAll() error {
	// Drop and recreate the collection to ensure HNSW index is properly reset
	if err := b.db.DropCollection("chunks"); err != nil {
		// Collection might not exist, ignore error
		_ = err
	}

	coll, err := b.db.CreateCollection("chunks",
		veclite.WithDimension(b.dimensions),
		veclite.WithDistanceType(veclite.DistanceCosine), // Use cosine for embeddings
		veclite.WithHNSW(16, 200),                        // M=16, efConstruction=200
	)
	if err != nil {
		return fmt.Errorf("failed to recreate collection: %w", err)
	}
	b.coll = coll

	return nil
}

// DeleteOrphaned removes embeddings that don't have corresponding chunks.
// With veclite-only storage, this cleans up any legacy chunk_id references.
func (b *VecLiteBackend) DeleteOrphaned(validChunkIDs []int64) (int64, error) {
	// Build map of valid IDs
	validMap := make(map[int64]bool, len(validChunkIDs))
	for _, id := range validChunkIDs {
		validMap[id] = true
	}

	// Get all records
	allRecords := b.coll.All()

	var deleted int64
	for _, r := range allRecords {
		// Check for legacy chunk_id
		chunkID := getInt64Payload(r.Payload, "chunk_id")
		if chunkID == 0 {
			continue // Not a legacy record
		}

		if !validMap[chunkID] {
			if err := b.coll.Delete(r.ID); err == nil {
				deleted++
			}
		}
	}

	return deleted, nil
}

// Sync persists any pending changes.
func (b *VecLiteBackend) Sync() error {
	return b.db.Sync()
}

// Close closes the VecLite database.
func (b *VecLiteBackend) Close() error {
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// Type returns "veclite".
func (b *VecLiteBackend) Type() string {
	return string(VectorBackendVecLite)
}

// Dimensions returns the embedding dimensions.
func (b *VecLiteBackend) Dimensions() int {
	return b.dimensions
}

// VecLitePath returns the path to the VecLite database file.
func VecLitePath(dataDir string) string {
	return filepath.Join(dataDir, "vectors.veclite")
}

// Helper functions for payload extraction

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

func getIntPayload(payload map[string]any, key string) int {
	return int(getInt64Payload(payload, key))
}

func recordToChunk(r *veclite.Record) ChunkRecord {
	indexedAt := time.Now()
	if ts := getStringPayload(r.Payload, "indexed_at"); ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			indexedAt = t
		}
	}

	return ChunkRecord{
		ID:           r.ID,
		FilePath:     getStringPayload(r.Payload, "file_path"),
		RelativePath: getStringPayload(r.Payload, "relative_path"),
		FileHash:     getStringPayload(r.Payload, "file_hash"),
		FileSize:     getInt64Payload(r.Payload, "file_size"),
		Language:     getStringPayload(r.Payload, "language"),
		Content:      getStringPayload(r.Payload, "content"),
		StartLine:    getIntPayload(r.Payload, "start_line"),
		EndLine:      getIntPayload(r.Payload, "end_line"),
		StartByte:    getIntPayload(r.Payload, "start_byte"),
		EndByte:      getIntPayload(r.Payload, "end_byte"),
		ChunkType:    getStringPayload(r.Payload, "chunk_type"),
		SymbolName:   getStringPayload(r.Payload, "symbol_name"),
		ProjectRoot:  getStringPayload(r.Payload, "project_root"),
		IndexedAt:    indexedAt,
		Vector:       r.Vector,
	}
}

// ListFiles returns all unique files in the index for a project.
func (b *VecLiteBackend) ListFiles(projectRoot string) ([]FileInfo, error) {
	allRecords := b.coll.All()

	filesMap := make(map[string]*FileInfo)
	for _, r := range allRecords {
		root := getStringPayload(r.Payload, "project_root")
		if projectRoot != "" && root != projectRoot {
			continue
		}

		relPath := getStringPayload(r.Payload, "relative_path")
		if relPath == "" {
			continue
		}

		// Use relative path as key to deduplicate
		key := root + ":" + relPath
		if _, exists := filesMap[key]; !exists {
			indexedAt := time.Now()
			if ts := getStringPayload(r.Payload, "indexed_at"); ts != "" {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					indexedAt = t
				}
			}

			filesMap[key] = &FileInfo{
				Path:         getStringPayload(r.Payload, "file_path"),
				RelativePath: relPath,
				Hash:         getStringPayload(r.Payload, "file_hash"),
				Size:         getInt64Payload(r.Payload, "file_size"),
				Language:     getStringPayload(r.Payload, "language"),
				IndexedAt:    indexedAt,
			}
		}
	}

	files := make([]FileInfo, 0, len(filesMap))
	for _, f := range filesMap {
		files = append(files, *f)
	}

	return files, nil
}

// HasFile checks if a file is indexed.
func (b *VecLiteBackend) HasFile(relPath string) bool {
	records, _ := b.coll.Find(veclite.Equal("relative_path", relPath))
	return len(records) > 0
}

// GetFileHash returns the hash of an indexed file.
func (b *VecLiteBackend) GetFileHash(relPath string) string {
	records, err := b.coll.Find(veclite.Equal("relative_path", relPath))
	if err != nil || len(records) == 0 {
		return ""
	}
	return getStringPayload(records[0].Payload, "file_hash")
}

// FilterOptions for search filtering.
type FilterOptions struct {
	Language    string   // Filter by single language
	Languages   []string // Filter by multiple languages (OR)
	ChunkType   string   // Filter by single chunk type
	ChunkTypes  []string // Filter by multiple chunk types (OR)
	FilePattern string   // Filter by file pattern (glob)
	Directory   string   // Filter by directory prefix
	MinLine     int      // Filter by minimum start line (0 = no filter)
	MaxLine     int      // Filter by maximum start line (0 = no filter)
}

// buildNativeFilters converts FilterOptions to veclite native filters.
func (b *VecLiteBackend) buildNativeFilters(opts FilterOptions) []veclite.Filter {
	var filters []veclite.Filter

	// Language filter
	if opts.Language != "" {
		filters = append(filters, veclite.Equal("language", strings.ToLower(opts.Language)))
	} else if len(opts.Languages) > 0 {
		// Convert to []any for In filter
		langs := make([]any, len(opts.Languages))
		for i, l := range opts.Languages {
			langs[i] = strings.ToLower(l)
		}
		filters = append(filters, veclite.In("language", langs...))
	}

	// Chunk type filter
	if opts.ChunkType != "" {
		filters = append(filters, veclite.Equal("chunk_type", strings.ToLower(opts.ChunkType)))
	} else if len(opts.ChunkTypes) > 0 {
		types := make([]any, len(opts.ChunkTypes))
		for i, t := range opts.ChunkTypes {
			types[i] = strings.ToLower(t)
		}
		filters = append(filters, veclite.In("chunk_type", types...))
	}

	// File pattern filter using Glob
	if opts.FilePattern != "" {
		filters = append(filters, veclite.Glob("relative_path", opts.FilePattern))
	}

	// Directory prefix filter
	if opts.Directory != "" {
		dir := opts.Directory
		if !strings.HasSuffix(dir, "/") {
			dir += "/"
		}
		filters = append(filters, veclite.Prefix("relative_path", dir))
	}

	// Line range filter
	if opts.MinLine > 0 && opts.MaxLine > 0 {
		filters = append(filters, veclite.Between("start_line", float64(opts.MinLine), float64(opts.MaxLine)))
	} else if opts.MinLine > 0 {
		filters = append(filters, veclite.GTE("start_line", float64(opts.MinLine)))
	} else if opts.MaxLine > 0 {
		filters = append(filters, veclite.LTE("start_line", float64(opts.MaxLine)))
	}

	return filters
}

// SearchWithFilter performs a filtered vector search using native veclite filters.
func (b *VecLiteBackend) SearchWithFilter(queryEmbedding []float32, limit int, opts FilterOptions) ([]SearchResult, error) {
	if len(queryEmbedding) != b.dimensions {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, expected %d", len(queryEmbedding), b.dimensions)
	}

	// Build native filters
	filters := b.buildNativeFilters(opts)

	// Build search options
	searchOpts := []veclite.SearchOption{veclite.TopK(limit)}
	if len(filters) > 0 {
		searchOpts = append(searchOpts, veclite.WithFilters(filters...))
	}

	// Perform search with native filtering
	results, err := b.coll.Search(queryEmbedding, searchOpts...)
	if err != nil {
		return nil, err
	}

	searchResults := make([]SearchResult, 0, len(results))
	for _, r := range results {
		chunk := recordToChunk(r.Record)
		sr := SearchResult{
			ChunkID:  int64(r.Record.ID),
			Distance: r.Score,
			Chunk:    &chunk,
		}

		// Check for legacy chunk_id in payload
		if chunkID := getInt64Payload(r.Record.Payload, "chunk_id"); chunkID != 0 {
			sr.ChunkID = chunkID
			sr.Chunk = nil // Legacy record without full metadata
		}

		searchResults = append(searchResults, sr)
	}

	return searchResults, nil
}

// SearchWithExplain performs a search and returns diagnostic information.
func (b *VecLiteBackend) SearchWithExplain(queryEmbedding []float32, limit int, opts FilterOptions) ([]SearchResult, *SearchExplanation, error) {
	if len(queryEmbedding) != b.dimensions {
		return nil, nil, fmt.Errorf("query embedding dimension mismatch: got %d, expected %d", len(queryEmbedding), b.dimensions)
	}

	// Build native filters
	filters := b.buildNativeFilters(opts)

	// Build search options
	searchOpts := []veclite.SearchOption{veclite.TopK(limit)}
	if len(filters) > 0 {
		searchOpts = append(searchOpts, veclite.WithFilters(filters...))
	}

	// Use SearchExplain for diagnostics
	explanation, err := b.coll.SearchExplain(queryEmbedding, searchOpts...)
	if err != nil {
		return nil, nil, err
	}

	// Also get the actual results
	results, err := b.coll.Search(queryEmbedding, searchOpts...)
	if err != nil {
		return nil, nil, err
	}

	searchResults := make([]SearchResult, 0, len(results))
	for _, r := range results {
		chunk := recordToChunk(r.Record)
		sr := SearchResult{
			ChunkID:  int64(r.Record.ID),
			Distance: r.Score,
			Chunk:    &chunk,
		}

		if chunkID := getInt64Payload(r.Record.Payload, "chunk_id"); chunkID != 0 {
			sr.ChunkID = chunkID
			sr.Chunk = nil
		}

		searchResults = append(searchResults, sr)
	}

	// Convert veclite explanation to our type
	explainResult := &SearchExplanation{
		IndexType:    string(explanation.IndexType),
		NodesVisited: explanation.NodesVisited,
		Duration:     explanation.Duration,
		Mode:         SearchModeSemantic, // Currently only semantic mode
	}

	return searchResults, explainResult, nil
}

// TextSearch performs a keyword-based search on content.
// Note: This is a simple Contains-based search since veclite doesn't have native BM25.
// For true BM25 text search, a future veclite version would be needed.
func (b *VecLiteBackend) TextSearch(query string, limit int, opts FilterOptions) ([]SearchResult, error) {
	// Build filters including text search
	filters := b.buildNativeFilters(opts)

	// Add text search filter using Contains
	if query != "" {
		// Search in content field
		filters = append(filters, veclite.Contains("content", query))
	}

	// Find matching records
	records, err := b.coll.Find(filters...)
	if err != nil {
		return nil, err
	}

	// Convert to SearchResults (no score since this is text-based)
	searchResults := make([]SearchResult, 0, limit)
	for _, r := range records {
		if len(searchResults) >= limit {
			break
		}

		chunk := recordToChunk(r)
		sr := SearchResult{
			ChunkID:  int64(r.ID),
			Distance: 0, // No distance for text search
			Chunk:    &chunk,
		}
		searchResults = append(searchResults, sr)
	}

	return searchResults, nil
}

// HybridSearch combines vector search with text filtering.
// The vector search provides ranking while text matching is used as a boost.
// vectorWeight controls the influence of vector similarity (0-1).
func (b *VecLiteBackend) HybridSearch(queryEmbedding []float32, textQuery string, limit int, opts FilterOptions, vectorWeight float32) ([]SearchResult, error) {
	if len(queryEmbedding) != b.dimensions {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, expected %d", len(queryEmbedding), b.dimensions)
	}

	// For hybrid search, we do vector search with filters but don't require text match
	// Text matching is used for re-ranking/boosting, not filtering
	filters := b.buildNativeFilters(opts)

	// Build search options with filters (not including text query as filter)
	searchOpts := []veclite.SearchOption{veclite.TopK(limit * 2)} // Get more for re-ranking
	if len(filters) > 0 {
		searchOpts = append(searchOpts, veclite.WithFilters(filters...))
	}

	results, err := b.coll.Search(queryEmbedding, searchOpts...)
	if err != nil {
		return nil, err
	}

	// Convert all results - apply text matching as a score boost
	searchResults := make([]SearchResult, 0, len(results))
	textQueryLower := strings.ToLower(textQuery)
	textQueryWords := strings.Fields(textQueryLower)

	for _, r := range results {
		chunk := recordToChunk(r.Record)

		// Calculate text match boost
		score := r.Score
		if textQuery != "" && chunk.Content != "" {
			contentLower := strings.ToLower(chunk.Content)
			textWeight := 1.0 - float64(vectorWeight)

			// Exact phrase match gets full boost
			if strings.Contains(contentLower, textQueryLower) {
				score = score * float32(1.0+textWeight*0.5) // Up to 50% boost for exact phrase match
			} else {
				// Token-level matching: boost proportionally to matched words
				matchedWords := 0
				for _, word := range textQueryWords {
					if strings.Contains(contentLower, word) {
						matchedWords++
					}
				}
				if matchedWords > 0 && len(textQueryWords) > 0 {
					matchRatio := float64(matchedWords) / float64(len(textQueryWords))
					score = score * float32(1.0+textWeight*0.3*matchRatio) // Partial boost for word matches
				}
			}

			// Symbol name match gets extra boost
			if chunk.SymbolName != "" {
				symbolLower := strings.ToLower(chunk.SymbolName)
				for _, word := range textQueryWords {
					if strings.Contains(symbolLower, word) {
						score = score * float32(1.0+textWeight*0.2) // Extra boost for symbol match
						break
					}
				}
			}
		}

		sr := SearchResult{
			ChunkID:  int64(r.Record.ID),
			Distance: score,
			Chunk:    &chunk,
		}

		if chunkID := getInt64Payload(r.Record.Payload, "chunk_id"); chunkID != 0 {
			sr.ChunkID = chunkID
			sr.Chunk = nil
		}

		searchResults = append(searchResults, sr)
	}

	// Re-rank by boosted score (higher is better for cosine similarity)
	sort.Slice(searchResults, func(i, j int) bool {
		return searchResults[i].Distance > searchResults[j].Distance
	})

	// Trim to requested limit
	if len(searchResults) > limit {
		searchResults = searchResults[:limit]
	}

	return searchResults, nil
}

// Ensure VecLiteBackend implements VectorBackend.
var _ VectorBackend = (*VecLiteBackend)(nil)
