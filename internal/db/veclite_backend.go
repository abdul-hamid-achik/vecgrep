package db

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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

// HNSWConfig holds HNSW index parameters. It mirrors veclite's HNSWConfig
// so callers in the db package do not need to import veclite directly for
// configuration values.
type HNSWConfig struct {
	M              int
	EfConstruction int
	EfSearch       int
}

// VecLiteBackend implements the database layer using VecLite with HNSW indexing.
// All metadata is stored in vector payload - no SQLite needed.
//
// The active collection pointer (coll) is swapped by Reload (read-only handles
// picking up another process's writes) and DeleteAll (collection recreation).
// Those swaps can race with concurrent readers/writers — MCP tool handlers run
// concurrently, and the daemon shares one backend across its watcher, reindex,
// and search goroutines. collMu guards the pointer itself; veclite's
// *Collection is internally synchronized, so callers only need the lock long
// enough to read the current pointer (see collection()).
type vecLiteBackendTestHooks struct {
	afterChunkInsert func()
	syncLocked       func()
}

type VecLiteBackend struct {
	collMu     sync.RWMutex
	storageMu  sync.Mutex
	db         *veclite.DB
	coll       *veclite.Collection
	fileHashes *veclite.Collection
	dbPath     string
	dimensions int
	hnsw       HNSWConfig
	readOnly   bool
	testHooks  *vecLiteBackendTestHooks
}

// Lock order is storageMu, then collMu, then any VecLite DB/Collection lock.
// collection and fileHashCollection release collMu before returning, so readers
// never hold collMu while entering VecLite or waiting for storageMu.

// collection returns the active collection pointer under the read lock. The
// lock only guards against Reload/DeleteAll swapping the pointer; veclite's
// *Collection is internally synchronized, so it is safe to operate on the
// returned pointer after the lock is released.
func (b *VecLiteBackend) collection() *veclite.Collection {
	b.collMu.RLock()
	defer b.collMu.RUnlock()
	return b.coll
}

// fileHashCollection returns the collection that stores vectorless per-file
// metadata. Legacy databases opened read-only may not have this collection.
func (b *VecLiteBackend) fileHashCollection() *veclite.Collection {
	b.collMu.RLock()
	defer b.collMu.RUnlock()
	return b.fileHashes
}

// setCollections publishes collection pointers together so Reload and
// DeleteAll cannot expose a mismatched pair.
func (b *VecLiteBackend) setCollections(coll, fileHashes *veclite.Collection) {
	b.collMu.Lock()
	b.coll = coll
	b.fileHashes = fileHashes
	b.collMu.Unlock()
}

// NewVecLiteBackend creates a new VecLite backend.
// The dbPath should point to the VecLite database file.
func NewVecLiteBackend(dbPath string) *VecLiteBackend {
	return &VecLiteBackend{
		dbPath: dbPath,
		hnsw: HNSWConfig{
			M:              DefaultHNSWM,
			EfConstruction: DefaultHNSWEfConstruction,
			EfSearch:       DefaultHNSWEfSearch,
		},
	}
}

// Init initializes the VecLite backend with the given dimensions and HNSW config.
// If hnsw is the zero value, defaults (M=16, EfConstruction=200, EfSearch=100)
// are applied.
func (b *VecLiteBackend) Init(dimensions int, hnsw HNSWConfig) error {
	return b.InitWithOptions(dimensions, hnsw, false, false)
}

// InitWithOptions initializes the VecLite backend with the given dimensions, HNSW
// config, and access mode options. readOnly prevents writes; sharedRead allows
// multiple processes to open the same file for read-only access (requires
// readOnly).
func (b *VecLiteBackend) InitWithOptions(dimensions int, hnsw HNSWConfig, readOnly, sharedRead bool) error {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	b.dimensions = dimensions
	b.readOnly = readOnly
	if hnsw.M <= 0 {
		hnsw.M = DefaultHNSWM
	}
	if hnsw.EfConstruction <= 0 {
		hnsw.EfConstruction = DefaultHNSWEfConstruction
	}
	if hnsw.EfSearch <= 0 {
		hnsw.EfSearch = DefaultHNSWEfSearch
	}
	b.hnsw = hnsw

	// Open VecLite database
	var openOpts []veclite.Option
	if readOnly {
		openOpts = append(openOpts, veclite.WithReadOnly(true))
		if sharedRead {
			openOpts = append(openOpts, veclite.WithSharedRead(true))
		}
	}
	db, err := veclite.Open(b.dbPath, openOpts...)
	if err != nil {
		return fmt.Errorf("failed to open veclite database: %w", err)
	}
	b.db = db

	// Create or get collection with HNSW index.
	// Use cosine distance which is standard for normalized embeddings.
	coll, err := db.CreateCollection("chunks",
		veclite.WithDimension(dimensions),
		veclite.WithDistanceType(veclite.DistanceCosine),
		veclite.WithHNSWConfig(veclite.HNSWConfig{
			M:              hnsw.M,
			EfConstruction: hnsw.EfConstruction,
			EfSearch:       hnsw.EfSearch,
			UseHeuristic:   true,
		}),
		veclite.WithTextIndex("content", "symbol_name", "relative_path", "language", "chunk_type"),
	)
	if err != nil {
		// Collection might already exist, try to get it
		coll, err = db.GetCollection("chunks")
		if err != nil {
			return fmt.Errorf("failed to create/get collection: %w", err)
		}
	}
	var fileHashes *veclite.Collection
	fileHashes, err = db.CreateCollection("file_hashes")
	createdFileHashes := err == nil
	if err != nil {
		fileHashes, err = db.GetCollection("file_hashes")
		if err != nil && !readOnly {
			return fmt.Errorf("failed to create/get file hashes collection: %w", err)
		}
		if err != nil {
			fileHashes = nil
		}
	}
	if createdFileHashes && coll.Count() == 0 {
		if err := fileHashes.SetMetadataValue(fileHashCompleteMetadata, true); err != nil {
			return fmt.Errorf("initialize file hashes collection: %w", err)
		}
	}
	b.setCollections(coll, fileHashes)

	return nil
}

// HNSWConfig returns the active HNSW configuration.
func (b *VecLiteBackend) HNSWConfig() HNSWConfig {
	return b.hnsw
}

// SetMetadataValue stores a single metadata value on the chunks collection.
func (b *VecLiteBackend) SetMetadataValue(key string, value any) error {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	coll := b.collection()
	if coll == nil {
		return fmt.Errorf("backend not initialized")
	}
	return coll.SetMetadataValue(key, value)
}

// MetadataValue retrieves a single metadata value from the chunks collection.
// It returns (nil, false) when the key is absent or the backend is unopened.
func (b *VecLiteBackend) MetadataValue(key string) (any, bool) {
	coll := b.collection()
	if coll == nil {
		return nil, false
	}
	v, ok := coll.Metadata()[key]
	return v, ok
}

// DeleteMetadataValue removes a single metadata value from the chunks collection.
func (b *VecLiteBackend) DeleteMetadataValue(key string) error {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	coll := b.collection()
	if coll == nil {
		return fmt.Errorf("backend not initialized")
	}
	return coll.DeleteMetadataValue(key)
}

// collectionHNSWOptions returns the veclite collection options used by this backend.
// Used by DeleteAll to recreate the collection with the same HNSW parameters.
func (b *VecLiteBackend) collectionOptions() []veclite.CollectionOption {
	return []veclite.CollectionOption{
		veclite.WithDimension(b.dimensions),
		veclite.WithDistanceType(veclite.DistanceCosine),
		veclite.WithHNSWConfig(veclite.HNSWConfig{
			M:              b.hnsw.M,
			EfConstruction: b.hnsw.EfConstruction,
			EfSearch:       b.hnsw.EfSearch,
			UseHeuristic:   true,
		}),
		veclite.WithTextIndex("content", "symbol_name", "relative_path", "language", "chunk_type"),
	}
}

// searchOptions builds the base search options (TopK + EfSearch) used by every
// search call. Additional options (filters, weights) can be appended by callers.
func (b *VecLiteBackend) searchOptions(limit int) []veclite.SearchOption {
	opts := []veclite.SearchOption{veclite.TopK(limit)}
	if b.hnsw.EfSearch > 0 {
		opts = append(opts, veclite.WithEfSearch(b.hnsw.EfSearch))
	}
	return opts
}

const (
	fileHashRecordType       = "file_hash"
	fileHashReadyType        = "project_ready"
	fileHashKeyField         = "_file_hash_key"
	fileHashRecordField      = "_record_type"
	fileHashCompleteMetadata = "_file_hash_index_complete"
)

func fileHashKey(projectRoot, relativePath string) string {
	return "file:" + projectRoot + "\x00" + relativePath
}

func fileHashReadyKey(projectRoot string) string {
	return "project:" + projectRoot
}

func (b *VecLiteBackend) upsertFileHash(chunk ChunkRecord) error {
	coll := b.fileHashCollection()
	if coll == nil {
		return nil
	}
	key := fileHashKey(chunk.ProjectRoot, chunk.RelativePath)
	_, _, err := coll.UpsertRecordByKey(fileHashKeyField, key, veclite.RecordInput{
		Payload: map[string]any{
			fileHashKeyField:    key,
			fileHashRecordField: fileHashRecordType,
			"file_path":         chunk.FilePath,
			"relative_path":     chunk.RelativePath,
			"file_hash":         chunk.FileHash,
			"project_root":      chunk.ProjectRoot,
		},
	})
	return err
}

func (b *VecLiteBackend) markFileHashesReady(projectRoot string) error {
	coll := b.fileHashCollection()
	if coll == nil {
		return nil
	}
	key := fileHashReadyKey(projectRoot)
	_, _, err := coll.UpsertRecordByKey(fileHashKeyField, key, veclite.RecordInput{
		Payload: map[string]any{
			fileHashKeyField:    key,
			fileHashRecordField: fileHashReadyType,
			"project_root":      projectRoot,
		},
	})
	return err
}

func (b *VecLiteBackend) fileHashesReady(projectRoot string) bool {
	coll := b.fileHashCollection()
	if coll == nil {
		return false
	}
	if complete, _ := coll.Metadata()[fileHashCompleteMetadata].(bool); complete {
		return true
	}
	record, err := coll.FindOne(
		veclite.Equal(fileHashKeyField, fileHashReadyKey(projectRoot)),
		veclite.Equal(fileHashRecordField, fileHashReadyType),
	)
	return err == nil && record != nil
}

func (b *VecLiteBackend) invalidateFileHashes(projectRoot string) {
	if coll := b.fileHashCollection(); coll != nil {
		_ = coll.DeleteMetadataValue(fileHashCompleteMetadata)
		_, _ = coll.DeleteWhere(
			veclite.Equal(fileHashKeyField, fileHashReadyKey(projectRoot)),
			veclite.Equal(fileHashRecordField, fileHashReadyType),
		)
	}
}

// InsertChunk inserts a chunk with all its metadata and embedding.
func (b *VecLiteBackend) InsertChunk(chunk ChunkRecord, embedding []float32) (uint64, error) {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
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

	id, err := b.collection().Insert(embedding, payload)
	if err != nil {
		return 0, err
	}
	if b.testHooks != nil && b.testHooks.afterChunkInsert != nil {
		b.testHooks.afterChunkInsert()
	}
	if err := b.upsertFileHash(chunk); err != nil {
		_ = b.collection().Delete(id)
		b.invalidateFileHashes(chunk.ProjectRoot)
		return 0, fmt.Errorf("store file hash: %w", err)
	}

	return id, nil
}

// InsertChunkBatch inserts multiple chunks in a single batch operation.
// Returns the IDs of the inserted chunks.
func (b *VecLiteBackend) InsertChunkBatch(chunks []ChunkRecord, embeddings [][]float32) ([]uint64, error) {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	if len(chunks) != len(embeddings) {
		return nil, fmt.Errorf("chunks and embeddings length mismatch: %d vs %d", len(chunks), len(embeddings))
	}

	if len(chunks) == 0 {
		return nil, nil
	}

	vectors := make([][]float32, len(chunks))
	payloads := make([]map[string]any, len(chunks))
	fileChunks := make(map[string]ChunkRecord)

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
		fileChunks[fileHashKey(chunk.ProjectRoot, chunk.RelativePath)] = chunk
	}

	// Use InsertBatch for batch insert
	ids, err := b.collection().InsertBatch(vectors, payloads)
	if err != nil {
		return nil, fmt.Errorf("batch insert failed: %w", err)
	}
	for _, chunk := range fileChunks {
		if err := b.upsertFileHash(chunk); err != nil {
			for _, id := range ids {
				_ = b.collection().Delete(id)
			}
			b.invalidateFileHashes(chunk.ProjectRoot)
			return nil, fmt.Errorf("store file hash: %w", err)
		}
	}

	return ids, nil
}

// UpsertChunk inserts or updates a chunk using a unique key.
// The key is based on relative_path:start_line for chunk identification.
// Returns the ID and whether it was a new insert (true) or update (false).
func (b *VecLiteBackend) UpsertChunk(chunk ChunkRecord, embedding []float32) (uint64, bool, error) {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
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

	id, isNew, err := b.collection().UpsertByKey("chunk_key", chunkKey, embedding, payload)
	if err != nil {
		return 0, false, fmt.Errorf("upsert failed: %w", err)
	}
	if err := b.upsertFileHash(chunk); err != nil {
		b.invalidateFileHashes(chunk.ProjectRoot)
		return 0, false, fmt.Errorf("store file hash: %w", err)
	}

	return id, isNew, nil
}

// InsertEmbedding inserts an embedding for a chunk (legacy compatibility).
// Deprecated: Use InsertChunk instead for full metadata storage.
func (b *VecLiteBackend) InsertEmbedding(chunkID int64, embedding []float32) error {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	if len(embedding) != b.dimensions {
		return fmt.Errorf("embedding dimension mismatch: got %d, expected %d", len(embedding), b.dimensions)
	}

	// Legacy mode: store with minimal payload
	_, err := b.collection().Insert(embedding, map[string]any{"chunk_id": chunkID})
	return err
}

// DeleteEmbedding removes an embedding for a chunk (legacy compatibility).
func (b *VecLiteBackend) DeleteEmbedding(chunkID int64) error {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	_, err := b.collection().DeleteWhere(veclite.Equal("chunk_id", chunkID))
	return err
}

// DeleteByFilePath removes all chunks for a given file path.
func (b *VecLiteBackend) DeleteByFilePath(filePath string) (int64, error) {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()

	// Modern records are keyed by canonical project-relative paths. Only scan
	// the legacy absolute-path field when no canonical record matched.
	deleted, err := b.collection().DeleteWhere(veclite.Equal("relative_path", filePath))
	if err != nil {
		return int64(deleted), err
	}
	if deleted > 0 {
		if coll := b.fileHashCollection(); coll != nil {
			if _, err := coll.DeleteWhere(veclite.Equal("relative_path", filePath)); err != nil {
				return int64(deleted), err
			}
		}
		return int64(deleted), nil
	}

	deleted, err = b.collection().DeleteWhere(veclite.Equal("file_path", filePath))
	if err != nil {
		return int64(deleted), err
	}
	if deleted > 0 {
		if coll := b.fileHashCollection(); coll != nil {
			if _, err := coll.DeleteWhere(veclite.Equal("file_path", filePath)); err != nil {
				return int64(deleted), err
			}
		}
	}
	return int64(deleted), nil
}

// DeleteByProjectRoot removes all chunks for a project.
// If all records are deleted, the collection is recreated to reset the HNSW index.
func (b *VecLiteBackend) DeleteByProjectRoot(projectRoot string) (int64, error) {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()

	deleted, err := b.collection().DeleteWhere(veclite.Equal("project_root", projectRoot))
	if err != nil {
		return int64(deleted), err
	}
	if coll := b.fileHashCollection(); coll != nil {
		if _, err := coll.DeleteWhere(veclite.Equal("project_root", projectRoot)); err != nil {
			return int64(deleted), err
		}
	}

	// If the collection is now empty, recreate it to reset the HNSW index.
	// This works around VecLite's empty-collection entry-point corruption path.
	if b.collection().Count() == 0 {
		if err := b.recreateCollections(); err != nil {
			return int64(deleted), err
		}
	}

	return int64(deleted), nil
}

// GetFileHashes returns a map of relative_path -> file_hash for a project.
// Used for incremental indexing to detect changed files.
func (b *VecLiteBackend) GetFileHashes(projectRoot string) (map[string]string, error) {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()

	if b.fileHashesReady(projectRoot) {
		records, err := b.fileHashCollection().Find(
			veclite.Equal(fileHashRecordField, fileHashRecordType),
			veclite.Equal("project_root", projectRoot),
		)
		if err != nil {
			return nil, fmt.Errorf("find file hash records: %w", err)
		}
		return fileHashesFromRecords(records), nil
	}

	// Legacy databases have no vectorless file metadata. Scan their chunk
	// records once, then backfill the lightweight collection for future calls.
	records, err := b.collection().Find(veclite.Equal("project_root", projectRoot))
	if err != nil {
		return nil, fmt.Errorf("find project records: %w", err)
	}
	fileMetadata := fileMetadataFromRecords(records)
	fileHashes := make(map[string]string, len(fileMetadata))
	for relPath, chunk := range fileMetadata {
		fileHashes[relPath] = chunk.FileHash
	}

	hashColl := b.fileHashCollection()
	if b.readOnly || hashColl == nil {
		return fileHashes, nil
	}
	if _, err := hashColl.DeleteWhere(
		veclite.Equal(fileHashRecordField, fileHashRecordType),
		veclite.Equal("project_root", projectRoot),
	); err != nil {
		return nil, fmt.Errorf("clear file hash records: %w", err)
	}
	for _, chunk := range fileMetadata {
		if err := b.upsertFileHash(chunk); err != nil {
			return nil, fmt.Errorf("backfill file hash: %w", err)
		}
	}
	if err := b.markFileHashesReady(projectRoot); err != nil {
		return nil, fmt.Errorf("mark file hashes ready: %w", err)
	}
	return fileHashes, nil
}

func fileHashesFromRecords(records []*veclite.Record) map[string]string {
	fileHashes := make(map[string]string)
	for _, r := range records {
		relPath := getStringPayload(r.Payload, "relative_path")
		hash := getStringPayload(r.Payload, "file_hash")
		if relPath != "" && hash != "" {
			fileHashes[relPath] = hash
		}
	}
	return fileHashes
}

func fileMetadataFromRecords(records []*veclite.Record) map[string]ChunkRecord {
	files := make(map[string]ChunkRecord)
	for _, r := range records {
		relPath := getStringPayload(r.Payload, "relative_path")
		hash := getStringPayload(r.Payload, "file_hash")
		if relPath == "" || hash == "" {
			continue
		}
		files[relPath] = ChunkRecord{
			FilePath:     getStringPayload(r.Payload, "file_path"),
			RelativePath: relPath,
			FileHash:     hash,
			ProjectRoot:  getStringPayload(r.Payload, "project_root"),
		}
	}
	return files
}

// GetChunksByFile returns all chunks for a specific file.
func (b *VecLiteBackend) GetChunksByFile(filePath string) ([]ChunkRecord, error) {
	// Search by relative_path first
	records, err := b.collection().Find(veclite.Equal("relative_path", filePath))
	if err != nil {
		return nil, err
	}

	// If no results, try absolute path
	if len(records) == 0 {
		records, err = b.collection().Find(veclite.Equal("file_path", filePath))
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

	results, err := b.collection().Search(queryEmbedding, b.searchOptions(limit)...)
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
	record, err := b.collection().Get(uint64(chunkID))
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
	record, err := b.collection().Get(uint64(chunkID))
	if err == nil && record != nil {
		return record.Vector, nil
	}

	// Fall back to legacy chunk_id lookup
	records, err := b.collection().Find(veclite.Equal("chunk_id", chunkID))
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
	return int64(b.collection().Count()), nil
}

// GetStats returns comprehensive statistics about the index.
func (b *VecLiteBackend) GetStats(projectRoot string) (*Stats, error) {
	stats := &Stats{
		Languages:  make(map[string]int64),
		ChunkTypes: make(map[string]int64),
	}

	filesSet := make(map[string]bool)
	projectsSet := make(map[string]bool)

	// Push the project_root filter down to veclite when a specific project is
	// requested; only the global-stats case scans every record.
	var records []*veclite.Record
	if projectRoot != "" {
		filtered, err := b.collection().Find(veclite.Equal("project_root", projectRoot))
		if err != nil {
			return nil, fmt.Errorf("find project records for stats: %w", err)
		}
		records = filtered
	} else {
		records = b.collection().All()
	}

	for _, r := range records {
		root := getStringPayload(r.Payload, "project_root")

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
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	return b.recreateCollections()
}

func (b *VecLiteBackend) recreateCollections() error {
	if err := b.db.DropCollection("chunks"); err != nil {
		_ = err
	}
	if err := b.db.DropCollection("file_hashes"); err != nil {
		_ = err
	}

	coll, err := b.db.CreateCollection("chunks", b.collectionOptions()...)
	if err != nil {
		return fmt.Errorf("failed to recreate collection: %w", err)
	}
	fileHashes, err := b.db.CreateCollection("file_hashes")
	if err != nil {
		return fmt.Errorf("failed to recreate file hashes collection: %w", err)
	}
	if err := fileHashes.SetMetadataValue(fileHashCompleteMetadata, true); err != nil {
		return fmt.Errorf("initialize file hashes collection: %w", err)
	}
	b.setCollections(coll, fileHashes)
	return nil
}

// DeleteOrphaned removes embeddings that don't have corresponding chunks.
// With veclite-only storage, this cleans up any legacy chunk_id references.
func (b *VecLiteBackend) DeleteOrphaned(validChunkIDs []int64) (int64, error) {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	// Build map of valid IDs
	validMap := make(map[int64]bool, len(validChunkIDs))
	for _, id := range validChunkIDs {
		validMap[id] = true
	}

	// Only legacy records carry a non-zero chunk_id. Push that filter down to
	// veclite so we don't scan modern records on the hot path.
	legacyRecords, err := b.collection().Find(veclite.GreaterThan("chunk_id", 0))
	if err != nil {
		return 0, fmt.Errorf("find legacy records for orphan cleanup: %w", err)
	}

	var deleted int64
	for _, r := range legacyRecords {
		chunkID := getInt64Payload(r.Payload, "chunk_id")
		if chunkID == 0 {
			continue // defensive: filter may include zero due to payload edge cases
		}

		if !validMap[chunkID] {
			if err := b.collection().Delete(r.ID); err == nil {
				deleted++
			}
		}
	}

	return deleted, nil
}

// Sync persists any pending changes.
func (b *VecLiteBackend) Sync() error {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	if b.testHooks != nil && b.testHooks.syncLocked != nil {
		b.testHooks.syncLocked()
	}
	return b.db.Sync()
}

// Close closes the VecLite database.
func (b *VecLiteBackend) Close() error {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// Reload re-reads the database from disk, rebuilding all in-memory state
// (collections, HNSW indexes, BM25 indexes). It is intended for read-only
// databases opened with SharedRead so they can pick up writes performed by
// another process (e.g. the daemon or CLI index) without closing and
// reopening. No-op if the backend is not initialized.
//
// db.Reload() builds brand-new *veclite.Collection objects and swaps them into
// the underlying *veclite.DB, but our cached b.coll still points at the old
// collection. We MUST re-fetch it afterwards, otherwise every search keeps
// serving the pre-reload snapshot forever (the reload would be a silent no-op
// for the caller). The re-fetch is published under the write lock so concurrent
// readers never observe a torn pointer.
func (b *VecLiteBackend) Reload() error {
	b.storageMu.Lock()
	defer b.storageMu.Unlock()
	if b.db == nil {
		return fmt.Errorf("backend not initialized")
	}
	if err := b.db.Reload(); err != nil {
		return err
	}
	coll, err := b.db.GetCollection("chunks")
	if err != nil {
		return fmt.Errorf("reload: re-fetch collection: %w", err)
	}
	fileHashes, err := b.db.GetCollection("file_hashes")
	if err != nil {
		fileHashes = nil
	}
	b.setCollections(coll, fileHashes)
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
	// Push the project_root filter down to veclite when a specific project is
	// requested; only the global case scans every record.
	var records []*veclite.Record
	if projectRoot != "" {
		filtered, err := b.collection().Find(veclite.Equal("project_root", projectRoot))
		if err != nil {
			return nil, fmt.Errorf("find project records for file list: %w", err)
		}
		records = filtered
	} else {
		records = b.collection().All()
	}

	filesMap := make(map[string]*FileInfo)
	for _, r := range records {
		root := getStringPayload(r.Payload, "project_root")
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
	records, _ := b.collection().Find(veclite.Equal("relative_path", relPath))
	return len(records) > 0
}

// GetFileHash returns the hash of an indexed file.
func (b *VecLiteBackend) GetFileHash(relPath string) string {
	records, err := b.collection().Find(veclite.Equal("relative_path", relPath))
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
	FilePaths   []string // Filter by an allow-list of relative paths (OR). Used for blast-radius scoping.
	MinLine     int      // Filter by minimum start line (0 = no filter)
	MaxLine     int      // Filter by maximum start line (0 = no filter)
	ProjectRoot string   // Filter by project root
}

// buildNativeFilters converts FilterOptions to veclite native filters.
func (b *VecLiteBackend) buildNativeFilters(opts FilterOptions) []veclite.Filter {
	var filters []veclite.Filter

	// Project filter
	if opts.ProjectRoot != "" {
		filters = append(filters, veclite.Equal("project_root", opts.ProjectRoot))
	}

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

	// File allow-list filter (blast-radius scoping). When FilePaths is
	// non-empty, restrict results to chunks whose relative_path is in the
	// set. This is an OR filter — a chunk matches if its path is any of
	// the listed files.
	if len(opts.FilePaths) > 0 {
		paths := make([]any, len(opts.FilePaths))
		for i, p := range opts.FilePaths {
			paths[i] = p
		}
		filters = append(filters, veclite.In("relative_path", paths...))
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

	// Build search options (TopK + EfSearch + filters)
	searchOpts := b.searchOptions(limit)
	if len(filters) > 0 {
		searchOpts = append(searchOpts, veclite.WithFilters(filters...))
	}

	// Perform search with native filtering
	results, err := b.collection().Search(queryEmbedding, searchOpts...)
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

	// Build search options (TopK + EfSearch + filters)
	searchOpts := b.searchOptions(limit)
	if len(filters) > 0 {
		searchOpts = append(searchOpts, veclite.WithFilters(filters...))
	}

	// Use SearchExplain for diagnostics. veclite's SearchExplanation carries
	// the actual Results alongside the diagnostics, so we no longer need to
	// run a second Search() call — halving the work for every --explain.
	explanation, err := b.collection().SearchExplain(queryEmbedding, searchOpts...)
	if err != nil {
		return nil, nil, err
	}

	results := explanation.Results

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

// TextSearch performs a keyword-based search on content using VecLite BM25.
func (b *VecLiteBackend) TextSearch(query string, limit int, opts FilterOptions) ([]SearchResult, error) {
	filters := b.buildNativeFilters(opts)

	searchOpts := b.searchOptions(limit)
	if len(filters) > 0 {
		searchOpts = append(searchOpts, veclite.WithFilters(filters...))
	}

	results, err := b.collection().TextSearch(query, searchOpts...)
	if err != nil {
		return nil, err
	}

	return b.resultsToSearchResults(results), nil
}

// HybridSearch combines vector search with VecLite BM25 text search.
// vectorWeight controls the influence of vector similarity (0-1).
func (b *VecLiteBackend) HybridSearch(queryEmbedding []float32, textQuery string, limit int, opts FilterOptions, vectorWeight float32) ([]SearchResult, error) {
	if len(queryEmbedding) != b.dimensions {
		return nil, fmt.Errorf("query embedding dimension mismatch: got %d, expected %d", len(queryEmbedding), b.dimensions)
	}

	filters := b.buildNativeFilters(opts)

	textWeight := 1.0 - float64(vectorWeight)
	if textWeight < 0 {
		textWeight = 0
	}

	searchOpts := append(b.searchOptions(limit),
		veclite.WithVectorWeight(float64(vectorWeight)),
		veclite.WithTextWeight(textWeight),
	)
	if len(filters) > 0 {
		searchOpts = append(searchOpts, veclite.WithFilters(filters...))
	}

	results, err := b.collection().HybridSearch(queryEmbedding, textQuery, searchOpts...)
	if err != nil {
		return nil, err
	}

	return b.resultsToSearchResults(results), nil
}

func (b *VecLiteBackend) resultsToSearchResults(results []veclite.Result) []SearchResult {
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
	return searchResults
}

// Ensure VecLiteBackend implements VectorBackend.
var _ VectorBackend = (*VecLiteBackend)(nil)
