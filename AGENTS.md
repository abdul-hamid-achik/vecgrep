# AGENTS.md

Instructions for AI agents working on the vecgrep codebase.

## Project Overview

vecgrep is a local-first semantic code search tool powered by vector embeddings. It indexes codebases and enables natural language search using Ollama for embedding generation.

**Key features:**
- Semantic search with vector embeddings
- Local-first (Ollama integration)
- Incremental indexing with file hashing
- Language-aware code chunking
- MCP (Model Context Protocol) server for AI assistant integration
- Bubble Tea Studio terminal app

## Directory Structure

```
cmd/vecgrep/        # CLI entrypoint
internal/
  config/           # Hierarchical configuration system
    config.go       # Core config types and loading
    resolution.go   # Multi-level config resolution
    global.go       # Global project registry (~/.vecgrep/)
  db/               # Pure veclite vector database (no SQL)
    vector_backend.go      # Vector backend interface
    veclite_backend.go     # VecLite HNSW implementation
  embed/            # Embedding providers (Ollama, OpenAI)
  index/            # File indexer and chunker
  app/              # Shared CLI/Studio service layer
  mcp/              # Model Context Protocol server (server_sdk.go)
  render/           # CLI rendering adapters
  search/           # Search implementation
  studio/           # Bubble Tea Studio terminal app
  version/          # Version info (set via ldflags)
docs/               # VitePress documentation website (deployed to Vercel)
```

## Documentation Discipline

There are two distinct documentation surfaces — do not mix them:

1. **`docs/` — VitePress website (deployed to Vercel).** This is the public product
   documentation site (user-facing guides, MCP reference, provider config, integration
   contracts). Build with `task site` / `task site:build` / `task site:preview`. Only
   edit files here for user-facing product documentation. **Do not use `docs/` for
   scratch notes, session handoffs, TODO dumps, or agent working memory.**

2. **`~/notes` — Obsidian vault (project notes).** All working notes, session
   handoffs, release notes, design decisions, TODO tracking, and agent memory live
   here. The vecgrep project folder is `~/notes/projects/vecgrep/`. When you need to
   make a note, use the **obsidian-cli** skill (invoke `skill` with name
   `obsidian-cli`) to read/write/search the vault rather than dropping markdown files
   into the repo. The vault has sibling folders for related projects:
   `~/notes/projects/veclite/` and `~/notes/projects/vidtrace/`.

Never create `.md` scratch/handoff/notes files inside the vecgrep repo. Keep the
repo clean: code, in-repo product docs (`docs/`), and specs only.

## Development Commands

Use [Task](https://taskfile.dev) for all operations:

```bash
task doctor       # Check environment setup
task setup        # Install dependencies and tools
task dev          # Hot reload development (air)
task check        # Run fmt, lint, test (use before commits)
task build        # Build binary to ./bin/vecgrep
task test         # Run tests
task flows        # Run terminal Studio flows
```

## Prerequisites

1. **Go 1.25+**
2. **Ollama** running locally with `nomic-embed-text` model
3. **Dev tools**: air (installed via `task tools`)

## Testing

```bash
task test         # Run all tests
task verbose      # Verbose output
task short        # Skip integration tests
task flows        # Run all specs/flows/*.yml with Glyphrun
task cov          # Coverage report
```

Tests that require Ollama are skipped if it's not running.

## Architecture Notes

### codemap Integration

Follow [`docs/codemap-integration.md`](docs/codemap-integration.md) for the cross-tool
contract. codemap owns the resolved structural graph and impact analysis; vecgrep owns
semantic retrieval and memory. Integration is one hop through versioned CLI JSON: neither
tool links the other's packages nor reads or shares the other's database.

### Embedding Flow
1. Files are chunked by `internal/index/chunker.go` (language-aware)
2. Chunks are embedded via `internal/embed/ollama.go`
3. Embeddings and metadata stored in veclite via `internal/db/db.go`
4. Search uses vector similarity in `internal/search/search.go`

### MCP Server
The MCP implementation in `internal/mcp/server_sdk.go` provides:
- `vecgrep_init` - Initialize a project
- `vecgrep_search` - Semantic search
- `vecgrep_index` - Index files
- `vecgrep_status` - Index statistics
- `vecgrep_similar` - Find similar code by chunk ID, file:line, or text
- `vecgrep_delete` - Remove file from index
- `vecgrep_clean` - Sync database to disk and report stats
- `vecgrep_reset` - Clear database

### Configuration
Configuration uses a hierarchical resolution system (highest to lowest priority):
1. Environment variables (`VECGREP_*`)
2. Project root `vecgrep.yaml`
3. Project `.config/vecgrep.yaml`
4. Project `.vecgrep/config.yaml` (legacy)
5. Global project entry in `~/.vecgrep/config.yaml`
6. Global defaults
7. Built-in defaults

See `internal/config/resolution.go` for the full resolution logic.

## Common Tasks for Agents

### Adding a new CLI command
1. Add command in `cmd/vecgrep/main.go` using Cobra
2. Implement logic in appropriate `internal/` package
3. Update README.md with usage

### Adding a new MCP tool
1. Add tool definition and handler in `internal/mcp/server_sdk.go`
2. Update README.md MCP section

### Modifying the data model
1. Update the `ChunkRecord` struct in `internal/db/veclite_backend.go`
2. Update payload construction in `InsertChunk()` and extraction in `recordToChunk()`
3. Run tests to ensure compatibility
4. Note: Existing indexes may need to be rebuilt after schema changes

### Modifying Studio
1. Update Bubble Tea state and rendering in `internal/studio/`
2. Keep shared behavior in `internal/app/` when CLI and Studio need the same operation
3. Add unit tests for state transitions and Glyphrun specs under `specs/flows/`

## Code Style

- Use `go fmt` and `golangci-lint`
- Error messages should be lowercase, no trailing punctuation
- Use structured logging where available
- Keep functions focused and testable
- Prefer explicit error handling over panics

## Important Patterns

### Error Handling
Return errors up the call stack; let the CLI handle user-facing messages.

### Configuration
Access config via the `config.Load()` function. Don't hardcode paths.

### Database
Use the methods in `internal/db/db.go`. All data is stored in veclite vector payloads.

### Embedding Provider
The `embed.Provider` interface allows for multiple provider implementations:
- `internal/embed/ollama.go` - Ollama (local, default)
- `internal/embed/openai.go` - OpenAI (cloud, requires API key)

## Before Committing

1. Run `task check` (formats, lints, tests)
2. Run `task build` to verify compilation
3. Update `docs/` only if adding/changing user-facing product features. For session
   notes, handoffs, or design decisions, write to the Obsidian vault at
   `~/notes/projects/vecgrep/` via the obsidian-cli skill instead.
