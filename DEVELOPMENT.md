# Development Guide

## Quick Start

```bash
# Check your environment
task doctor

# Setup everything
task setup

# Start developing
task dev
```

## Daily Workflow

```bash
task dev          # Hot reload development
task check        # Run before committing (fmt, lint, test)
task ship         # Full CI pipeline locally
```

## Debugging

```bash
task wtf          # What's broken?
task doctor       # Environment check
```

## Ollama

```bash
task ollama       # Start Ollama with Metal GPU support
```

Requires `nomic-embed-text` model:
```bash
ollama pull nomic-embed-text
```

## Useful Commands

```bash
task              # List all available tasks
task build        # Build the binary
task test         # Run tests
task gen          # Generate code (sqlc, templ, css)
task clean        # Remove build artifacts
```

---

## Architecture Overview

vecgrep follows a layered architecture with clear separation of concerns:

```
┌─────────────────────────────────────────────────────────────┐
│                        CLI Layer                            │
│                    cmd/vecgrep/main.go                      │
│         (Cobra commands, user interaction, flags)           │
└─────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┼─────────────────────┐
        ▼                     ▼                     ▼
┌───────────────┐    ┌───────────────┐    ┌───────────────┐
│   MCP Server  │    │  Web Server   │    │    Search     │
│ internal/mcp  │    │ internal/web  │    │internal/search│
└───────────────┘    └───────────────┘    └───────────────┘
        │                     │                     │
        └─────────────────────┼─────────────────────┘
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                      Index Layer                            │
│                     internal/index                          │
│         (Chunker, file walking, language detection)         │
└─────────────────────────────────────────────────────────────┘
                              │
        ┌─────────────────────┴─────────────────────┐
        ▼                                           ▼
┌───────────────┐                          ┌───────────────┐
│   Embedding   │                          │   Database    │
│ internal/embed│                          │  internal/db  │
│(Ollama/OpenAI)│                          │ (SQLite+vec)  │
└───────────────┘                          └───────────────┘
```

### Data Flow

1. **Indexing Flow:**
   ```
   Files → Walker → Chunker → Embeddings → Database
   ```
   - File walker discovers files, respecting ignore patterns
   - Chunker splits code into semantic units (functions, classes, blocks)
   - Embedding provider generates vectors for each chunk
   - Database stores chunks and embeddings with file metadata

2. **Search Flow:**
   ```
   Query → Embedding → Vector Search → Ranked Results
   ```
   - Query text is embedded using the same provider
   - Vector similarity search finds closest chunks
   - Results are ranked by cosine similarity

---

## Package Responsibilities

### `cmd/vecgrep/`
CLI entry point using Cobra. Defines all commands (init, index, search, etc.) and handles user interaction, flags, and output formatting.

### `internal/config/`
Hierarchical configuration system with multiple sources:
- `config.go` - Core config types and loading
- `resolution.go` - Multi-level config resolution
- `global.go` - Global project registry (~/.vecgrep/)

**Resolution Order (highest to lowest priority):**
1. Environment variables (VECGREP_*)
2. Project root vecgrep.yaml
3. Project .config/vecgrep.yaml
4. Project .vecgrep/config.yaml (legacy)
5. Global project entry
6. Global defaults (~/.vecgrep/config.yaml)
7. Built-in defaults

### `internal/db/`
SQLite database layer with sqlc-generated code:
- `schema.sql` - Database schema definition
- `queries.sql` - SQL queries for sqlc
- `db.go` - Database operations and connection management
- `vector_backend.go` - Pluggable vector backend interface
- `sqlite_vec_backend.go` - sqlite-vec implementation
- `veclite_backend.go` - VecLite HNSW implementation
- `generated/` - sqlc-generated Go code

**Tables:**
- `files` - Indexed files with hash for change detection
- `chunks` - Code chunks with content, type, and language
- `embeddings` - Vector embeddings linked to chunks

### `internal/embed/`
Embedding provider implementations:
- `provider.go` - Provider interface definition
- `ollama.go` - Ollama API client (local)
- `openai.go` - OpenAI API client (cloud)

**Provider Interface:**
```go
type Provider interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    Dimensions() int
}
```

### `internal/index/`
File indexing and code chunking:
- `indexer.go` - Main indexer coordinating the process
- `chunker.go` - Language-aware code splitting
- `walker.go` - File discovery with ignore patterns
- `language.go` - Language detection

**Chunker Strategy:**
- Uses tree-sitter for language-aware parsing
- Identifies functions, methods, classes, and blocks
- Falls back to sliding window for unknown formats
- Respects chunk_size and chunk_overlap settings

### `internal/mcp/`
Model Context Protocol server:
- `server_sdk.go` - MCP server using official Go SDK

**Available Tools:**
- `vecgrep_init` - Initialize project
- `vecgrep_search` - Semantic search
- `vecgrep_index` - Index files
- `vecgrep_status` - Get statistics
- `vecgrep_similar` - Find similar code
- `vecgrep_delete` - Remove file from index
- `vecgrep_clean` - Optimize database
- `vecgrep_reset` - Clear database

### `internal/search/`
Search implementation:
- `search.go` - Query embedding and similarity search
- `results.go` - Result formatting and ranking

### `internal/version/`
Version information:
- Set via ldflags at build time
- Used by `vecgrep version` command

### `internal/web/`
Web server with HTMX:
- `server.go` - Chi router setup
- `handlers.go` - HTTP handlers
- `templates/` - Templ template files
- `static/` - Static assets (CSS)

---

## Adding New Features

### Adding an Embedding Provider

1. Create `internal/embed/newprovider.go`:
```go
type NewProvider struct {
    apiKey string
    model  string
}

func (p *NewProvider) Embed(ctx context.Context, text string) ([]float32, error) {
    // Implementation
}

func (p *NewProvider) Dimensions() int {
    return 768 // or whatever your model uses
}
```

2. Add config options to `internal/config/config.go`:
```go
type EmbeddingConfig struct {
    // ... existing fields
    NewProviderAPIKey string `mapstructure:"newprovider_api_key"`
}
```

3. Wire up in provider factory (in embed package)

4. Update README.md with usage instructions

### Adding a Language Chunker

1. Add language detection in `internal/index/language.go`

2. Implement tree-sitter parsing in `internal/index/chunker.go`:
```go
func (c *Chunker) chunkNewLang(content string) []Chunk {
    // Use tree-sitter parser for the language
    // Identify semantic units (functions, classes, etc.)
    // Return chunks with proper types
}
```

3. Add tests with sample code in your language

### Adding an MCP Tool

1. Add tool definition in `internal/mcp/server_sdk.go`:
```go
{
    Name:        "vecgrep_newtool",
    Description: "Description of the new tool",
    InputSchema: mcp.ToolInputSchema{
        Type: "object",
        Properties: map[string]any{
            "param1": map[string]any{
                "type":        "string",
                "description": "Parameter description",
            },
        },
        Required: []string{"param1"},
    },
}
```

2. Add handler in the tool dispatch:
```go
case "vecgrep_newtool":
    return s.handleNewTool(ctx, params)
```

3. Implement the handler method

4. Update README.md MCP section

### Modifying Database Schema

1. Edit `internal/db/schema.sql` for table changes

2. Edit `internal/db/queries.sql` for new queries

3. Run code generation:
```bash
task gen:sqlc
```

4. Update Go code using the new generated types

5. Add migration logic if needed for existing databases

---

## Testing Patterns

### Unit Tests

```go
func TestChunker_Go(t *testing.T) {
    chunker := NewChunker(512, 64)
    content := `func Hello() { return "world" }`

    chunks := chunker.Chunk(content, "go")

    assert.Len(t, chunks, 1)
    assert.Equal(t, "function", chunks[0].Type)
}
```

### Integration Tests

Tests requiring Ollama use a skip condition:
```go
func TestSearch_Integration(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }
    // ... test with real Ollama
}
```

Run integration tests:
```bash
task test        # All tests (Ollama required)
task test:short  # Skip integration tests
```

### Mock Providers

For testing search without Ollama:
```go
type MockProvider struct {
    embeddings map[string][]float32
}

func (m *MockProvider) Embed(ctx context.Context, text string) ([]float32, error) {
    if vec, ok := m.embeddings[text]; ok {
        return vec, nil
    }
    return make([]float32, 768), nil
}
```

---

## CI/CD

### GitHub Actions

- **ci.yml** - Runs on every push/PR: lint, test, build
- **release.yml** - Runs on tags: creates GitHub release with binaries

### Local CI Simulation

```bash
task ship  # Runs full CI pipeline locally
```

---

## Code Generation

| Command | Regenerates |
|---------|-------------|
| `task gen` | All generated code |
| `task gen:sqlc` | Database code from SQL |
| `task gen:templ` | Go code from .templ files |
| `task gen:css` | Tailwind CSS |

Always run `task gen` after modifying:
- `internal/db/*.sql`
- `internal/web/templates/*.templ`
- `assets/css/input.css`

---

## Troubleshooting

### Common Issues

**"not in a vecgrep project"**
- Run `vecgrep init` in your project directory
- Or add project to global registry

**"failed to connect to Ollama"**
- Ensure Ollama is running: `ollama serve`
- Check the URL in config (default: http://localhost:11434)

**"embedding dimensions mismatch"**
- Embeddings dimension changed (e.g., switched models)
- Run `vecgrep reset --force` and re-index

**CGO errors during build**
- Ensure CGO is enabled: `export CGO_ENABLED=1`
- On macOS, Xcode command line tools required

### Debug Mode

```bash
vecgrep --verbose <command>  # Enable verbose output
VECGREP_DEBUG=1 vecgrep ...  # Extra debug info
```
