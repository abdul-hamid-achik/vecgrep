package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/abdul-hamid-achik/vecgrep/internal/db"
)

// mockEmbedProvider is a mock embedding provider for testing.
type mockEmbedProvider struct {
	model      string
	dimensions int
}

func newMockEmbedProvider(dimensions int) *mockEmbedProvider {
	return &mockEmbedProvider{
		model:      "mock-embed",
		dimensions: dimensions,
	}
}

func (m *mockEmbedProvider) Embed(ctx context.Context, text string) ([]float32, error) {
	embedding := make([]float32, m.dimensions)
	for i := range embedding {
		embedding[i] = float32(len(text)%100) / 100.0
	}
	return embedding, nil
}

func (m *mockEmbedProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
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

func (m *mockEmbedProvider) Model() string {
	return m.model
}

func (m *mockEmbedProvider) Dimensions() int {
	return m.dimensions
}

func (m *mockEmbedProvider) Ping(ctx context.Context) error {
	return nil
}

func setupTestServer(t *testing.T) (*Server, *db.DB, *bytes.Buffer) {
	t.Helper()

	// Create temp directory and database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath, 768)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	provider := newMockEmbedProvider(768)

	server := NewServer(ServerConfig{
		DB:          database,
		Provider:    provider,
		ProjectRoot: tmpDir,
	})

	// Replace writer with buffer for testing
	var output bytes.Buffer
	server.writer = &output

	return server, database, &output
}

func TestNewServer(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	if server == nil {
		t.Fatal("NewServer returned nil")
	}

	if server.db == nil {
		t.Error("Server db should not be nil")
	}

	if server.provider == nil {
		t.Error("Server provider should not be nil")
	}

	if len(server.handlers) == 0 {
		t.Error("Server should have registered handlers")
	}
}

func TestHandleInitialize(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	ctx := context.Background()
	params := json.RawMessage(`{"protocolVersion": "2024-11-05"}`)

	result, err := server.handleInitialize(ctx, params)
	if err != nil {
		t.Fatalf("handleInitialize failed: %v", err)
	}

	initResult, ok := result.(InitializeResult)
	if !ok {
		t.Fatalf("Expected InitializeResult, got %T", result)
	}

	if initResult.ProtocolVersion == "" {
		t.Error("Expected non-empty protocol version")
	}

	if initResult.ServerInfo.Name != "vecgrep" {
		t.Errorf("Expected server name 'vecgrep', got '%s'", initResult.ServerInfo.Name)
	}

	if initResult.Capabilities.Tools == nil {
		t.Error("Expected Tools capability to be set")
	}
}

func TestHandleInitialized(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	// Initially not initialized
	if server.initialized {
		t.Error("Server should not be initialized initially")
	}

	ctx := context.Background()
	_, err := server.handleInitialized(ctx, nil)
	if err != nil {
		t.Fatalf("handleInitialized failed: %v", err)
	}

	if !server.initialized {
		t.Error("Server should be initialized after handleInitialized")
	}
}

func TestHandlePing(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	ctx := context.Background()
	result, err := server.handlePing(ctx, nil)
	if err != nil {
		t.Fatalf("handlePing failed: %v", err)
	}

	if result == nil {
		t.Error("Expected non-nil result")
	}
}

func TestHandleToolsList(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	ctx := context.Background()
	result, err := server.handleToolsList(ctx, nil)
	if err != nil {
		t.Fatalf("handleToolsList failed: %v", err)
	}

	toolsResult, ok := result.(ToolsListResult)
	if !ok {
		t.Fatalf("Expected ToolsListResult, got %T", result)
	}

	if len(toolsResult.Tools) == 0 {
		t.Error("Expected at least one tool")
	}

	// Check for expected tools
	toolNames := make(map[string]bool)
	for _, tool := range toolsResult.Tools {
		toolNames[tool.Name] = true
	}

	expectedTools := []string{"vecgrep_search", "vecgrep_status"}
	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("Expected tool '%s' not found", name)
		}
	}
}

func TestHandleResourcesList(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	ctx := context.Background()
	result, err := server.handleResourcesList(ctx, nil)
	if err != nil {
		t.Fatalf("handleResourcesList failed: %v", err)
	}

	resourcesResult, ok := result.(ResourcesListResult)
	if !ok {
		t.Fatalf("Expected ResourcesListResult, got %T", result)
	}

	// Currently no resources are exposed
	if len(resourcesResult.Resources) != 0 {
		t.Errorf("Expected 0 resources, got %d", len(resourcesResult.Resources))
	}
}

func TestHandlePromptsList(t *testing.T) {
	server, database, _ := setupTestServer(t)
	defer database.Close()

	ctx := context.Background()
	result, err := server.handlePromptsList(ctx, nil)
	if err != nil {
		t.Fatalf("handlePromptsList failed: %v", err)
	}

	promptsResult, ok := result.(PromptsListResult)
	if !ok {
		t.Fatalf("Expected PromptsListResult, got %T", result)
	}

	// Currently no prompts are exposed
	if len(promptsResult.Prompts) != 0 {
		t.Errorf("Expected 0 prompts, got %d", len(promptsResult.Prompts))
	}
}

func TestHandleRequest_InvalidJSONRPC(t *testing.T) {
	server, database, output := setupTestServer(t)
	defer database.Close()

	// Initialize server first
	server.initialized = true

	req := Request{
		JSONRPC: "1.0", // Wrong version
		ID:      1,
		Method:  "ping",
	}

	ctx := context.Background()
	server.handleRequest(ctx, req)

	// Check that an error response was sent
	var resp Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.Error == nil {
		t.Error("Expected error response for invalid JSON-RPC version")
	}
}

func TestHandleRequest_MethodNotFound(t *testing.T) {
	server, database, output := setupTestServer(t)
	defer database.Close()

	// Initialize server first
	server.initialized = true

	req := Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "nonexistent_method",
	}

	ctx := context.Background()
	server.handleRequest(ctx, req)

	// Check that an error response was sent
	var resp Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.Error == nil {
		t.Error("Expected error response for unknown method")
	}

	if resp.Error.Code != MethodNotFound {
		t.Errorf("Expected MethodNotFound error code, got %d", resp.Error.Code)
	}
}

func TestHandleRequest_NotInitialized(t *testing.T) {
	server, database, output := setupTestServer(t)
	defer database.Close()

	// Don't initialize server
	req := Request{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/list",
	}

	ctx := context.Background()
	server.handleRequest(ctx, req)

	// Check that an error response was sent
	var resp Response
	if err := json.Unmarshal(output.Bytes(), &resp); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if resp.Error == nil {
		t.Error("Expected error response for uninitialized server")
	}
}

func TestSendResponse(t *testing.T) {
	server, database, output := setupTestServer(t)
	defer database.Close()

	resp := NewResponse(1, map[string]string{"test": "value"})
	server.sendResponse(resp)

	if output.Len() == 0 {
		t.Error("Expected output to be written")
	}

	// Verify it's valid JSON
	var parsed Response
	if err := json.Unmarshal(output.Bytes(), &parsed); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	if parsed.JSONRPC != "2.0" {
		t.Errorf("Expected jsonrpc '2.0', got '%s'", parsed.JSONRPC)
	}
}

func TestSendError(t *testing.T) {
	server, database, output := setupTestServer(t)
	defer database.Close()

	server.sendError(1, InternalError, "Test error", "additional data")

	if output.Len() == 0 {
		t.Error("Expected output to be written")
	}

	var parsed Response
	if err := json.Unmarshal(output.Bytes(), &parsed); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	if parsed.Error == nil {
		t.Error("Expected error in response")
	}

	if parsed.Error.Code != InternalError {
		t.Errorf("Expected error code %d, got %d", InternalError, parsed.Error.Code)
	}

	if parsed.Error.Message != "Test error" {
		t.Errorf("Expected error message 'Test error', got '%s'", parsed.Error.Message)
	}
}

func TestNewResponse(t *testing.T) {
	resp := NewResponse(42, "test result")

	if resp.JSONRPC != "2.0" {
		t.Errorf("Expected jsonrpc '2.0', got '%s'", resp.JSONRPC)
	}

	if resp.ID != 42 {
		t.Errorf("Expected ID 42, got %v", resp.ID)
	}

	if resp.Error != nil {
		t.Error("Expected nil error")
	}
}

func TestNewErrorResponse(t *testing.T) {
	resp := NewErrorResponse(1, ParseError, "Parse error", nil)

	if resp.JSONRPC != "2.0" {
		t.Errorf("Expected jsonrpc '2.0', got '%s'", resp.JSONRPC)
	}

	if resp.Error == nil {
		t.Fatal("Expected non-nil error")
	}

	if resp.Error.Code != ParseError {
		t.Errorf("Expected error code %d, got %d", ParseError, resp.Error.Code)
	}
}
