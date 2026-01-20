package search

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

// mockProvider is a mock embedding provider for testing
type mockProvider struct {
	embeddings map[string][]float32
	model      string
	dimensions int
}

func newMockProvider(dimensions int) *mockProvider {
	return &mockProvider{
		embeddings: make(map[string][]float32),
		model:      "mock-embed",
		dimensions: dimensions,
	}
}

func (m *mockProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	if emb, ok := m.embeddings[text]; ok {
		return emb, nil
	}
	// Generate a simple embedding based on text length
	emb := make([]float32, m.dimensions)
	for i := range emb {
		emb[i] = float32(len(text)%100) / 100.0
	}
	return emb, nil
}

func (m *mockProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		emb, err := m.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		results[i] = emb
	}
	return results, nil
}

func (m *mockProvider) Model() string {
	return m.model
}

func (m *mockProvider) Dimensions() int {
	return m.dimensions
}

func (m *mockProvider) Ping(ctx context.Context) error {
	return nil
}

func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath, 768)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	return database
}

func setupTestData(t *testing.T, database *db.DB) int64 {
	t.Helper()

	// Create project
	result, err := database.Exec(`INSERT INTO projects (name, root_path) VALUES (?, ?)`, "test", "/tmp/test")
	if err != nil {
		t.Fatalf("Insert project failed: %v", err)
	}
	projectID, _ := result.LastInsertId()

	// Create file
	result, err = database.Exec(`INSERT INTO files (project_id, path, relative_path, hash, size, language) VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, "/tmp/test/main.go", "main.go", "abc123", 100, "go")
	if err != nil {
		t.Fatalf("Insert file failed: %v", err)
	}
	fileID, _ := result.LastInsertId()

	// Create chunks
	chunks := []struct {
		content    string
		startLine  int
		endLine    int
		chunkType  string
		symbolName string
	}{
		{"func HandleError(err error) {\n\tlog.Error(err)\n}", 1, 3, "function", "HandleError"},
		{"func ProcessData(data []byte) error {\n\treturn nil\n}", 5, 7, "function", "ProcessData"},
		{"type Config struct {\n\tHost string\n\tPort int\n}", 9, 12, "class", "Config"},
	}

	for _, c := range chunks {
		result, err := database.Exec(`INSERT INTO chunks (file_id, content, start_line, end_line, start_byte, end_byte, chunk_type, symbol_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fileID, c.content, c.startLine, c.endLine, 0, len(c.content), c.chunkType, c.symbolName)
		if err != nil {
			t.Fatalf("Insert chunk failed: %v", err)
		}
		chunkID, _ := result.LastInsertId()

		// Create embedding
		embedding := make([]float32, 768)
		for i := range embedding {
			embedding[i] = float32(len(c.content)%100) / 100.0
		}
		if err := database.InsertEmbedding(chunkID, embedding); err != nil {
			t.Fatalf("Insert embedding failed: %v", err)
		}
	}

	return projectID
}

func TestNewSearcher(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	if searcher == nil {
		t.Fatal("NewSearcher returned nil")
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	_, err := searcher.Search(context.Background(), "", DefaultSearchOptions())
	if err == nil {
		t.Error("Expected error for empty query")
	}
}

func TestSearch_Basic(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	results, err := searcher.Search(context.Background(), "error handling", DefaultSearchOptions())
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("Expected at least one result")
	}
}

func TestSearch_WithLimit(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	opts := DefaultSearchOptions()
	opts.Limit = 1

	results, err := searcher.Search(context.Background(), "function", opts)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) > 1 {
		t.Errorf("Expected at most 1 result, got %d", len(results))
	}
}

func TestSearch_FilterByLanguage(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	opts := DefaultSearchOptions()
	opts.Language = "go"

	results, err := searcher.Search(context.Background(), "function", opts)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for _, r := range results {
		if r.Language != "go" {
			t.Errorf("Expected language 'go', got '%s'", r.Language)
		}
	}
}

func TestSearch_FilterByChunkType(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	opts := DefaultSearchOptions()
	opts.ChunkType = "function"

	results, err := searcher.Search(context.Background(), "process", opts)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for _, r := range results {
		if r.ChunkType != "function" {
			t.Errorf("Expected chunk type 'function', got '%s'", r.ChunkType)
		}
	}
}

func TestFormatResults_Default(t *testing.T) {
	results := []Result{
		{
			ChunkID:      1,
			FilePath:     "/tmp/test/main.go",
			RelativePath: "main.go",
			Content:      "func main() {}",
			StartLine:    1,
			EndLine:      1,
			ChunkType:    "function",
			SymbolName:   "main",
			Language:     "go",
			Score:        0.95,
		},
	}

	output := FormatResults(results, FormatDefault)
	if output == "" {
		t.Error("Expected non-empty output")
	}
	if !contains(output, "main.go") {
		t.Error("Expected output to contain file path")
	}
	if !contains(output, "0.95") {
		t.Error("Expected output to contain score")
	}
}

func TestFormatResults_JSON(t *testing.T) {
	results := []Result{
		{
			ChunkID:      1,
			RelativePath: "main.go",
			Content:      "func main() {}",
			Score:        0.95,
		},
	}

	output := FormatResults(results, FormatJSON)
	if !contains(output, "\"chunk_id\"") {
		t.Error("Expected JSON output with chunk_id field")
	}
	if !contains(output, "\"main.go\"") {
		t.Error("Expected JSON output with file path")
	}
}

func TestFormatResults_Compact(t *testing.T) {
	results := []Result{
		{
			RelativePath: "main.go",
			StartLine:    1,
			EndLine:      3,
			Score:        0.95,
			SymbolName:   "main",
		},
	}

	output := FormatResults(results, FormatCompact)
	if !contains(output, "main.go:1-3") {
		t.Error("Expected compact format with file:lines")
	}
}

func TestFormatResults_Empty(t *testing.T) {
	output := FormatResults([]Result{}, FormatDefault)
	if output != "No results found." {
		t.Errorf("Expected 'No results found.', got '%s'", output)
	}

	output = FormatResults([]Result{}, FormatCompact)
	if output != "" {
		t.Errorf("Expected empty string for compact format, got '%s'", output)
	}
}

func TestDefaultSearchOptions(t *testing.T) {
	opts := DefaultSearchOptions()
	if opts.Limit != 10 {
		t.Errorf("Expected default limit 10, got %d", opts.Limit)
	}
	if opts.MinScore != 0.0 {
		t.Errorf("Expected default MinScore 0.0, got %f", opts.MinScore)
	}
}

func TestSearchByFile(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	results, err := searcher.SearchByFile(context.Background(), "main.go")
	if err != nil {
		t.Fatalf("SearchByFile failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("Expected at least one result")
	}

	for _, r := range results {
		if r.RelativePath != "main.go" {
			t.Errorf("Expected relative path 'main.go', got '%s'", r.RelativePath)
		}
	}
}

func TestGetIndexStats(t *testing.T) {
	database := setupTestDB(t)
	defer database.Close()
	setupTestData(t, database)

	provider := newMockProvider(768)
	searcher := NewSearcher(database, provider)

	stats, err := searcher.GetIndexStats(context.Background())
	if err != nil {
		t.Fatalf("GetIndexStats failed: %v", err)
	}

	if stats["embedding_model"] != "mock-embed" {
		t.Errorf("Expected embedding_model 'mock-embed', got '%v'", stats["embedding_model"])
	}

	if stats["embedding_dimensions"] != 768 {
		t.Errorf("Expected embedding_dimensions 768, got '%v'", stats["embedding_dimensions"])
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
