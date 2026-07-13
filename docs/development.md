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

The docs site uses Bun. `task setup` installs docs dependencies with `bun install`, and `task doctor` checks that Bun is available.

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
task race         # Run tests with the race detector
task short        # Run short tests
task verbose      # Run verbose tests
task cov          # Generate coverage output
task flows        # Run all terminal Studio flows
task site         # Start the VitePress docs site
task site:build   # Build the VitePress docs site
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
│   MCP Server  │    │    Studio     │    │    Search     │
│ internal/mcp  │    │internal/studio│    │internal/search│
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
│Ollama/cloud   │                          │  (veclite)    │
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
   Query → Embedding → Vector/BM25 Search → Ranked Results
   ```
   - Query text is embedded using the same provider for semantic and hybrid modes
   - Keyword mode uses VecLite BM25 without generating a query embedding
   - Vector similarity and BM25 results are ranked or fused by the selected search mode

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

**Default Storage:**
New projects are registered in `~/.vecgrep/config.yaml` by default, with generated index data stored under `~/.vecgrep/projects/<project>/`. Repo-local `.vecgrep/` directories are legacy or explicit `vecgrep init --local` behavior only.

### `internal/db/`
Pure veclite database layer (no SQLite, no CGO):
- `db.go` - Database operations and wrapper
- `vector_backend.go` - Vector backend interface
- `veclite_backend.go` - VecLite HNSW implementation with full metadata storage

**Data Model:**
All data is stored in veclite vector payloads:
- File info: path, hash, size, language
- Chunk info: content, lines, type, symbol name
- Project info: root path, indexed timestamp

**Embedding Boundary:**
vecgrep owns provider selection, credentials, batching, retries, code chunking, and rebuild policy. VecLite owns vector storage, BM25, filtering, HNSW search, and persistence. See `docs/veclite-integration.md` for the integration contract.

**Embedding Profile Guard:**
Indexing persists `embedding_profile.json` next to `vectors.veclite` with provider, model, dimensions, distance, modality, and chunker version. Incremental indexing and vector-based search compare the stored profile with the active config and require a full re-index when vector meaning changes.

### `internal/embed/`
Embedding provider implementations:
- `provider.go` - Provider interface definition
- `ollama.go` - Ollama API client (local)
- `openai.go` - OpenAI API client (cloud)
- `cohere.go` - Cohere Embed v2 client (cloud)
- `voyage.go` - Voyage AI embeddings client (cloud)
- `detect.go` - Provider detection and model metadata

**Provider Interface:**
```go
type Provider interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
    Model() string
    Dimensions() int
    Ping(ctx context.Context) error
}
```

Providers that distinguish retrieval roles can also implement:

```go
type QueryProvider interface {
    EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

type DocumentProvider interface {
    EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}
```

The indexer prefers `DocumentProvider` for chunk embeddings. Search prefers `QueryProvider` for semantic, hybrid, and similar-by-text queries. Providers without those optional interfaces use the base `Provider` methods.

### `internal/index/`
File indexing and code chunking:
- `indexer.go` - Main indexer coordinating the process
- `chunker.go` - Language detection and heuristic code splitting
- `watcher.go` - Optional file system watching for changed files

**Chunker Strategy:**
- Uses language-specific pattern extraction where available
- Identifies functions, types, classes, and blocks
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
- `vecgrep_clean` - Sync database to disk and report stats
- `vecgrep_reset` - Clear database

### `internal/search/`
Search implementation:
- `search.go` - Query embedding and similarity search
- `warmup.go` - Search warmup helpers

### `internal/app/`
Shared application service layer used by CLI and Studio:
- `session.go` - Project/config/database/provider session setup
- `search.go` - Search and similar-code requests
- `index.go` - Index maintenance operations
- `status.go` - Project status aggregation

### `internal/studio/`
Bubble Tea v2 Studio terminal app:
- `model.go` - Update/view state machine
- `run.go` - Program bootstrap
- `theme.go` - Lip Gloss styles

### `internal/render/`
CLI rendering adapters for shared result formatting.

### `internal/version/`
Version information:
- Set via ldflags at build time
- Used by `vecgrep version` command

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

3. Wire up in `internal/app/provider.go` and MCP initialization if the shared factory cannot be used

4. Update README.md with usage instructions

### Adding a Language Chunker

1. Add recognition metadata in `internal/index/languages.go`. Recognition does
   not imply structural parsing; the generic chunker remains the safe fallback.

2. Only add structural extraction when fixtures demonstrate correct boundaries.
   Implement it in `internal/index/chunker.go`:
```go
func (c *Chunker) chunkNewLang(content string) []Chunk {
    // Identify semantic units (functions, classes, etc.)
    // Return chunks with proper types
}
```

3. Add detection and source-coverage tests. A parser must preserve uncovered
   imports, docs, globals, and trailing source, keep every chunk valid UTF-8 and
   at most 4096 bytes, and avoid claiming call-graph support.

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

### Modifying the Data Model

The database uses veclite with all metadata stored in vector payloads.

1. Update the `ChunkRecord` struct in `internal/db/veclite_backend.go`

2. Update the payload construction in `InsertChunk()`

3. Update the payload extraction in `recordToChunk()`

4. Run tests to ensure compatibility:
```bash
task test
```

5. Note: Existing indexes may need to be rebuilt after schema changes

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
task short       # Skip integration tests
```

### Terminal Flows

Glyphrun terminal flows live in `specs/flows/`, matching the flow/action layout used by the automation projects.

```bash
task flows
task flow FLOW=specs/flows/studio_launch_quit.yml
```

### Docs Site

The docs site is powered by VitePress and uses Markdown files in `docs/`.

```bash
task site
task site:build
task site:preview
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

- **ci.yml** - Runs on every push/PR: `task check`, `task race`, `task build`, and coverage upload
- **release.yml** - Runs `task check`, then creates tagged release binaries with GoReleaser

### Local CI Simulation

```bash
task ship  # Runs full CI pipeline locally
```

---

## Troubleshooting

### Common Issues

**"not in a vecgrep project"**
- Run `vecgrep init` in your project directory
- Or add project to global registry

**"failed to connect to Ollama"**
- Ensure Ollama is running: `ollama serve`
- Check the URL in config (default: `http://localhost:11434`)

**"embedding profile mismatch" or "embedding dimensions mismatch"**
- Embedding provider, model, dimensions, distance, or chunker profile changed
- Run `vecgrep index --full` or `vecgrep reset --force` and re-index

**Database migration warning**
- A legacy `.vecgrep/vecgrep.db` file without a veclite index is not used by the current build
- Run `vecgrep reset --force` and re-index, or keep a backup before deleting legacy data

### Debug Mode

```bash
vecgrep --verbose <command>  # Enable verbose output
VECGREP_DEBUG=1 vecgrep ...  # Extra debug info
```
