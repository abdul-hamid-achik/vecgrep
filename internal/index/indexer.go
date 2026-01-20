package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
	"github.com/abdul-hamid-achik/vecgrep/internal/embed"
)

// IndexerConfig holds configuration for the indexer.
type IndexerConfig struct {
	ChunkSize      int
	ChunkOverlap   int
	IgnorePatterns []string
	MaxFileSize    int64
	BatchSize      int
	Workers        int
}

// DefaultIndexerConfig returns sensible defaults for indexing.
func DefaultIndexerConfig() IndexerConfig {
	return IndexerConfig{
		ChunkSize:    2048,
		ChunkOverlap: 256,
		IgnorePatterns: []string{
			".git/**",
			".vecgrep/**",
			"node_modules/**",
			"vendor/**",
			"__pycache__/**",
			"*.min.js",
			"*.min.css",
			"*.lock",
			"go.sum",
			"package-lock.json",
			"yarn.lock",
		},
		MaxFileSize: 1024 * 1024, // 1MB
		BatchSize:   32,
		Workers:     4,
	}
}

// Progress represents indexing progress information.
type Progress struct {
	TotalFiles    int
	ProcessedFiles int
	SkippedFiles  int
	TotalChunks   int
	CurrentFile   string
	StartTime     time.Time
	Errors        []error
}

// ProgressCallback is called during indexing to report progress.
type ProgressCallback func(Progress)

// Indexer orchestrates the file indexing process.
type Indexer struct {
	db       *db.DB
	provider embed.Provider
	chunker  *Chunker
	config   IndexerConfig
	progress ProgressCallback
	mu       sync.Mutex
}

// NewIndexer creates a new Indexer.
func NewIndexer(database *db.DB, provider embed.Provider, cfg IndexerConfig) *Indexer {
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultIndexerConfig().BatchSize
	}
	if cfg.Workers == 0 {
		cfg.Workers = DefaultIndexerConfig().Workers
	}

	chunkerCfg := ChunkerConfig{
		ChunkSize:    cfg.ChunkSize,
		ChunkOverlap: cfg.ChunkOverlap,
	}

	return &Indexer{
		db:       database,
		provider: provider,
		chunker:  NewChunker(chunkerCfg),
		config:   cfg,
	}
}

// SetProgressCallback sets a callback for progress updates.
func (idx *Indexer) SetProgressCallback(cb ProgressCallback) {
	idx.progress = cb
}

// IndexResult contains the results of an indexing operation.
type IndexResult struct {
	FilesProcessed int
	FilesSkipped   int
	ChunksCreated  int
	Duration       time.Duration
	Errors         []error
}

// Index indexes the given paths.
func (idx *Indexer) Index(ctx context.Context, projectRoot string, paths ...string) (*IndexResult, error) {
	startTime := time.Now()

	// Ensure project exists in database
	projectID, err := idx.ensureProject(ctx, projectRoot)
	if err != nil {
		return nil, fmt.Errorf("ensure project: %w", err)
	}

	// Build gitignore matcher
	ignoreMatcher, err := idx.buildIgnoreMatcher(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("build ignore matcher: %w", err)
	}

	// Collect all files to index
	files, err := idx.collectFiles(ctx, projectRoot, paths, ignoreMatcher)
	if err != nil {
		return nil, fmt.Errorf("collect files: %w", err)
	}

	// Filter files that need re-indexing
	filesToIndex, skipped := idx.filterUnchangedFiles(ctx, projectID, files)

	progress := Progress{
		TotalFiles:   len(filesToIndex),
		SkippedFiles: skipped,
		StartTime:    startTime,
	}

	// Index files
	result := &IndexResult{
		FilesSkipped: skipped,
	}

	// Process files with worker pool
	filesChan := make(chan fileInfo, len(filesToIndex))
	resultsChan := make(chan fileResult, len(filesToIndex))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < idx.config.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			idx.indexWorker(ctx, projectID, filesChan, resultsChan)
		}()
	}

	// Send files to workers
	go func() {
		for _, f := range filesToIndex {
			select {
			case filesChan <- f:
			case <-ctx.Done():
				break
			}
		}
		close(filesChan)
	}()

	// Collect results
	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	for r := range resultsChan {
		result.FilesProcessed++
		result.ChunksCreated += r.chunksCreated

		if r.err != nil {
			result.Errors = append(result.Errors, r.err)
		}

		progress.ProcessedFiles = result.FilesProcessed
		progress.TotalChunks = result.ChunksCreated
		progress.CurrentFile = r.path
		progress.Errors = result.Errors

		if idx.progress != nil {
			idx.progress(progress)
		}
	}

	// Update project stats
	if err := idx.updateProjectStats(ctx, projectID, result); err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("update stats: %w", err))
	}

	result.Duration = time.Since(startTime)
	return result, nil
}

// fileInfo holds information about a file to be indexed.
type fileInfo struct {
	path         string
	relativePath string
	hash         string
	size         int64
}

// fileResult holds the result of indexing a single file.
type fileResult struct {
	path          string
	chunksCreated int
	err           error
}

// indexWorker processes files from the channel.
func (idx *Indexer) indexWorker(ctx context.Context, projectID int64, files <-chan fileInfo, results chan<- fileResult) {
	for file := range files {
		select {
		case <-ctx.Done():
			results <- fileResult{path: file.path, err: ctx.Err()}
			return
		default:
		}

		chunks, err := idx.indexFile(ctx, projectID, file)
		results <- fileResult{
			path:          file.path,
			chunksCreated: chunks,
			err:           err,
		}
	}
}

// indexFile indexes a single file.
func (idx *Indexer) indexFile(ctx context.Context, projectID int64, file fileInfo) (int, error) {
	// Read file content
	content, err := os.ReadFile(file.path)
	if err != nil {
		return 0, fmt.Errorf("read file: %w", err)
	}

	// Check if text file
	if !IsTextFile(content) {
		return 0, nil // Skip binary files silently
	}

	// Detect language
	lang := DetectLanguage(file.path)

	// Delete existing file entry if present (for re-indexing)
	_, err = idx.db.Exec(`DELETE FROM files WHERE project_id = ? AND relative_path = ?`,
		projectID, file.relativePath)
	if err != nil {
		return 0, fmt.Errorf("delete existing file: %w", err)
	}

	// Insert file record
	result, err := idx.db.Exec(`
		INSERT INTO files (project_id, path, relative_path, hash, size, language, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		projectID, file.path, file.relativePath, file.hash, file.size, string(lang), time.Now())
	if err != nil {
		return 0, fmt.Errorf("insert file: %w", err)
	}

	fileID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get file ID: %w", err)
	}

	// Chunk the content
	chunks := idx.chunker.ChunkFile(string(content), file.path)
	if len(chunks) == 0 {
		return 0, nil
	}

	// Process chunks in batches for embedding
	var totalChunks int
	for i := 0; i < len(chunks); i += idx.config.BatchSize {
		end := i + idx.config.BatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[i:end]

		// Get texts for embedding
		texts := make([]string, len(batch))
		for j, chunk := range batch {
			texts[j] = chunk.Content
		}

		// Generate embeddings
		embeddings, err := idx.provider.EmbedBatch(ctx, texts)
		if err != nil {
			return totalChunks, fmt.Errorf("embed batch: %w", err)
		}

		// Insert chunks with embeddings
		for j, chunk := range batch {
			if j >= len(embeddings) || embeddings[j] == nil {
				continue
			}

			chunkID, err := idx.insertChunk(fileID, chunk)
			if err != nil {
				return totalChunks, fmt.Errorf("insert chunk: %w", err)
			}

			if err := idx.db.InsertEmbedding(chunkID, embeddings[j]); err != nil {
				return totalChunks, fmt.Errorf("insert embedding: %w", err)
			}

			totalChunks++
		}
	}

	return totalChunks, nil
}

// insertChunk inserts a chunk into the database.
func (idx *Indexer) insertChunk(fileID int64, chunk Chunk) (int64, error) {
	result, err := idx.db.Exec(`
		INSERT INTO chunks (file_id, content, start_line, end_line, start_byte, end_byte, chunk_type, symbol_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fileID, chunk.Content, chunk.StartLine, chunk.EndLine, chunk.StartByte, chunk.EndByte,
		string(chunk.ChunkType), chunk.SymbolName, time.Now())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ensureProject creates or returns the project ID for the given root path.
func (idx *Indexer) ensureProject(ctx context.Context, rootPath string) (int64, error) {
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return 0, fmt.Errorf("abs path: %w", err)
	}

	// Try to get existing project
	var projectID int64
	err = idx.db.QueryRow(`SELECT id FROM projects WHERE root_path = ?`, absPath).Scan(&projectID)
	if err == nil {
		// Update last_indexed_at
		_, err = idx.db.Exec(`UPDATE projects SET last_indexed_at = ? WHERE id = ?`, time.Now(), projectID)
		return projectID, err
	}

	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("query project: %w", err)
	}

	// Create new project
	projectName := filepath.Base(absPath)
	result, err := idx.db.Exec(`
		INSERT INTO projects (name, root_path, created_at, updated_at, last_indexed_at)
		VALUES (?, ?, ?, ?, ?)`,
		projectName, absPath, time.Now(), time.Now(), time.Now())
	if err != nil {
		return 0, fmt.Errorf("insert project: %w", err)
	}

	return result.LastInsertId()
}

// buildIgnoreMatcher builds a gitignore-style matcher for file filtering.
func (idx *Indexer) buildIgnoreMatcher(rootPath string) (*gitignore.GitIgnore, error) {
	// Start with configured ignore patterns
	patterns := make([]string, len(idx.config.IgnorePatterns))
	copy(patterns, idx.config.IgnorePatterns)

	// Try to read .gitignore
	gitignorePath := filepath.Join(rootPath, ".gitignore")
	if content, err := os.ReadFile(gitignorePath); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}

	// Try to read .vecgrepignore
	vecgrepignorePath := filepath.Join(rootPath, ".vecgrepignore")
	if content, err := os.ReadFile(vecgrepignorePath); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}

	matcher := gitignore.CompileIgnoreLines(patterns...)
	return matcher, nil
}

// collectFiles walks the file tree and collects files to index.
func (idx *Indexer) collectFiles(ctx context.Context, rootPath string, paths []string, ignore *gitignore.GitIgnore) ([]fileInfo, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("abs root: %w", err)
	}

	// If no paths specified, use root
	if len(paths) == 0 {
		paths = []string{absRoot}
	}

	var files []fileInfo
	var mu sync.Mutex

	for _, path := range paths {
		absPath := path
		if !filepath.IsAbs(path) {
			absPath = filepath.Join(absRoot, path)
		}

		err := filepath.WalkDir(absPath, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // Skip files with errors
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Get relative path for ignore matching
			relPath, err := filepath.Rel(absRoot, p)
			if err != nil {
				relPath = p
			}

			// Check if should be ignored
			if ignore.MatchesPath(relPath) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}

			if d.IsDir() {
				return nil
			}

			// Check file size
			info, err := d.Info()
			if err != nil {
				return nil
			}

			if info.Size() > idx.config.MaxFileSize {
				return nil
			}

			// Calculate file hash
			hash, err := hashFile(p)
			if err != nil {
				return nil
			}

			mu.Lock()
			files = append(files, fileInfo{
				path:         p,
				relativePath: relPath,
				hash:         hash,
				size:         info.Size(),
			})
			mu.Unlock()

			return nil
		})

		if err != nil && err != context.Canceled {
			return nil, fmt.Errorf("walk %s: %w", path, err)
		}
	}

	return files, nil
}

// filterUnchangedFiles filters out files that haven't changed since last indexing.
func (idx *Indexer) filterUnchangedFiles(ctx context.Context, projectID int64, files []fileInfo) ([]fileInfo, int) {
	var toIndex []fileInfo
	var skipped int

	for _, file := range files {
		var existingHash string
		err := idx.db.QueryRow(`
			SELECT hash FROM files
			WHERE project_id = ? AND relative_path = ?`,
			projectID, file.relativePath).Scan(&existingHash)

		if err == nil && existingHash == file.hash {
			skipped++
			continue
		}

		toIndex = append(toIndex, file)
	}

	return toIndex, skipped
}

// updateProjectStats updates statistics for the project.
func (idx *Indexer) updateProjectStats(ctx context.Context, projectID int64, result *IndexResult) error {
	now := time.Now()

	// Update or insert stats
	stats := map[string]int64{
		"files_indexed":  int64(result.FilesProcessed),
		"files_skipped":  int64(result.FilesSkipped),
		"chunks_created": int64(result.ChunksCreated),
		"errors":         int64(len(result.Errors)),
	}

	for key, value := range stats {
		_, err := idx.db.Exec(`
			INSERT INTO stats (project_id, stat_key, stat_value, recorded_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(project_id, stat_key) DO UPDATE SET stat_value = ?, recorded_at = ?`,
			projectID, key, value, now, value, now)
		if err != nil {
			return fmt.Errorf("update stat %s: %w", key, err)
		}
	}

	return nil
}

// hashFile calculates SHA256 hash of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReindexAll forces reindexing of all files in the project.
func (idx *Indexer) ReindexAll(ctx context.Context, projectRoot string) (*IndexResult, error) {
	// Get project ID
	absPath, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}

	var projectID int64
	err = idx.db.QueryRow(`SELECT id FROM projects WHERE root_path = ?`, absPath).Scan(&projectID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("query project: %w", err)
	}

	// Clear existing data for this project
	if projectID != 0 {
		if _, err := idx.db.Exec(`DELETE FROM files WHERE project_id = ?`, projectID); err != nil {
			return nil, fmt.Errorf("clear files: %w", err)
		}
	}

	// Perform full index
	return idx.Index(ctx, projectRoot)
}
