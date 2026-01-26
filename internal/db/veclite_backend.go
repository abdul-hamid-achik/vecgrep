package db

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/veclite"
)

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
	coll, err := db.CreateCollection("chunks",
		veclite.WithDimension(dimensions),
		veclite.WithDistanceType(veclite.DistanceEuclidean),
		veclite.WithHNSW(16, 200), // M=16, efConstruction=200
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
		veclite.WithDistanceType(veclite.DistanceEuclidean),
		veclite.WithHNSW(16, 200), // M=16, efConstruction=200
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
	Language    string
	ChunkType   string
	FilePattern string
}

// SearchWithFilter performs a filtered vector search.
func (b *VecLiteBackend) SearchWithFilter(queryEmbedding []float32, limit int, opts FilterOptions) ([]SearchResult, error) {
	// Get more results than needed to account for filtering
	searchLimit := limit * 3
	if searchLimit < 50 {
		searchLimit = 50
	}

	results, err := b.SearchEmbeddings(queryEmbedding, searchLimit)
	if err != nil {
		return nil, err
	}

	filtered := make([]SearchResult, 0, limit)
	for _, r := range results {
		if r.Chunk == nil {
			continue // Skip legacy records without full metadata
		}

		// Apply filters
		if opts.Language != "" && !strings.EqualFold(r.Chunk.Language, opts.Language) {
			continue
		}

		if opts.ChunkType != "" && !strings.EqualFold(r.Chunk.ChunkType, opts.ChunkType) {
			continue
		}

		if opts.FilePattern != "" {
			matched, _ := filepath.Match(opts.FilePattern, r.Chunk.RelativePath)
			if !matched {
				matched, _ = filepath.Match(opts.FilePattern, filepath.Base(r.Chunk.RelativePath))
			}
			if !matched {
				continue
			}
		}

		filtered = append(filtered, r)
		if len(filtered) >= limit {
			break
		}
	}

	return filtered, nil
}

// Ensure VecLiteBackend implements VectorBackend.
var _ VectorBackend = (*VecLiteBackend)(nil)
