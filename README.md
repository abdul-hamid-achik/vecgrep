# vecgrep

Local-first semantic code search powered by embeddings.

vecgrep indexes your codebase and enables natural language search using vector embeddings. It defaults to local Ollama embeddings so your code stays on your machine, with optional cloud providers when you choose them.

## Features

- **Hybrid Search** - Combine semantic (vector) and keyword search for best results
- **Three Search Modes** - Choose between semantic, keyword, or hybrid search
- **Local-First** - Embeddings generated locally via Ollama by default
- **Cloud Provider Support** - Optional OpenAI, Cohere, and Voyage AI embeddings
- **Incremental Indexing** - Only re-index changed files
- **Batch Operations** - Efficient bulk indexing with batch inserts
- **Language-Aware Chunking** - Intelligent code splitting by functions, classes, and blocks
- **Rich Filtering** - Filter by language, chunk type, directory, file pattern, and line range
- **MCP Support** - Model Context Protocol server for AI assistant integration
- **Studio** - Full-screen Bubble Tea workspace for search, preview, indexing, and status
- **Similar Code Finder** - Find semantically similar code across your codebase
- **Search Diagnostics** - Explain mode for debugging and optimizing searches
- **Embedding Cache** - Cache query embeddings for faster repeated searches
- **Batch Search** - Search multiple queries in parallel via MCP
- **Codebase Overview** - Get high-level insights about your codebase structure
- **Related Files** - Find imports, tests, and files that depend on a given file
- **Context Lines** - Include surrounding code in search results

## Installation

### Homebrew (recommended)

```bash
brew install abdul-hamid-achik/tap/vecgrep
```

### From Source

```bash
git clone https://github.com/abdul-hamid-achik/vecgrep.git
cd vecgrep
task build
# or: go build -o bin/vecgrep ./cmd/vecgrep
```

### Install to GOPATH

```bash
task install
# or: go install ./cmd/vecgrep
```

## Quick Start

1. **Start Ollama and pull the embedding model:**
   ```bash
   ollama pull nomic-embed-text
   ```

2. **Initialize vecgrep in your project:**
   ```bash
   cd /path/to/your/project
   vecgrep init
   ```

   `vecgrep init` registers the project globally by default and stores index data under `~/.vecgrep/projects/<project>/`, so no repo-local `.vecgrep/` directory is created. Use `vecgrep init --local` only when you intentionally want project-local state.

3. **Index your codebase:**
   ```bash
   vecgrep index
   ```

4. **Search:**
   ```bash
   vecgrep search "error handling in HTTP requests"
   ```

   Or open Studio:
   ```bash
   vecgrep studio
   # or: vecgrep browse
   ```

### Using Cloud Embeddings (Alternative)

If you prefer managed embeddings, vecgrep supports OpenAI, Cohere, and Voyage AI.

1. **Set your API key:**
   ```bash
   # OpenAI
   export OPENAI_API_KEY=sk-your-key-here

   # Cohere
   export COHERE_API_KEY=your-cohere-key

   # Voyage AI
   export VOYAGE_API_KEY=your-voyage-key
   ```

2. **Configure vecgrep to use a cloud provider:**

   ```bash
   # OpenAI
   vecgrep config set embedding.provider openai
   vecgrep config set embedding.model text-embedding-3-small
   vecgrep config set embedding.dimensions 1536

   # Cohere
   vecgrep config set embedding.provider cohere
   vecgrep config set embedding.model embed-v4.0
   vecgrep config set embedding.dimensions 1536

   # Voyage AI
   vecgrep config set embedding.provider voyage
   vecgrep config set embedding.model voyage-code-3
   vecgrep config set embedding.dimensions 1024
   ```

   Or set via environment:
   ```bash
   export VECGREP_EMBEDDING_PROVIDER=voyage
   export VECGREP_EMBEDDING_MODEL=voyage-code-3
   export VECGREP_EMBEDDING_DIMENSIONS=1024
   ```

3. **Re-index your codebase:**
   ```bash
   vecgrep index --full
   ```

## Usage

### Initialize a Project

```bash
vecgrep init [--local] [--force]
```

Registers the project globally by default and stores data under `~/.vecgrep/projects/<project>/`. Use `--local` when you want a project-local `.vecgrep/` directory.

### Index Files

```bash
vecgrep index [paths...] [--full] [--ignore pattern]
```

Options:
- `--full` - Force full re-index (ignores file hashes)
- `--ignore` - Additional patterns to ignore
- `-v, --verbose` - Show detailed progress
- `--no-progress` - Disable the live progress bar

When a background daemon hub is running, `vecgrep index` **delegates** the
reindex to it over the daemon's control socket instead of opening a second
write handle (which would collide with the daemon's exclusive lock). The
output is the normal "Indexing complete" summary, annotated `(via daemon)`,
and forwards `--full`. `--ignore` (additional ignores) is not forwarded — the
daemon's configured ignores apply; stop + restart the daemon to change them.
`--dry-run` uses a read-only session for the preview.

vecgrep records an embedding profile in VecLite collection metadata after a successful first index or full re-index. Existing projects with a legacy `embedding_profile.json` sidecar are migrated transparently on the next open: the sidecar is read, written into collection metadata, and removed. If the active embedding provider, model, dimensions, distance, or chunker profile no longer matches the indexed vectors, incremental indexing and vector search fail with rebuild guidance. Run `vecgrep index --full` or `vecgrep reset --force` to refresh stale vectors.

### Search

```bash
vecgrep search <query> [options]
```

**Search Modes:**

| Mode | Description |
|------|-------------|
| `hybrid` | Combines vector similarity with text matching (default) |
| `semantic` | Pure vector similarity search |
| `keyword` | Text-based search using VecLite BM25 |

**Options:**

| Flag | Description |
|------|-------------|
| `-n, --limit N` | Maximum results (default: 10) |
| `-f, --format` | Output format: `default`, `json`, `compact` |
| `-m, --mode` | Search mode: `hybrid`, `semantic`, `keyword` |
| `--explain` | Show search diagnostics (index type, nodes visited, duration) |
| `-l, --lang` | Filter by single language |
| `--languages` | Filter by multiple languages (comma-separated) |
| `-t, --type` | Filter by chunk type: `function`, `class`, `block` |
| `--types` | Filter by multiple chunk types (comma-separated) |
| `--file` | Filter by file pattern (glob) |
| `--dir` | Filter by directory prefix |
| `--lines` | Filter by line range (e.g., `1-100`) |

**Examples:**

```bash
# Default hybrid search
vecgrep search "database connection pooling"

# Semantic-only search (vector similarity)
vecgrep search --mode=semantic "error handling patterns"

# Keyword search (text matching)
vecgrep search --mode=keyword "SELECT FROM users"

# Search with diagnostics
vecgrep search --explain "authentication middleware"

# Filter by language
vecgrep search "error handling" -l go -n 5

# Filter by multiple languages
vecgrep search "memory management" --languages=go,rust

# Filter by directory
vecgrep search "config loading" --dir=internal/

# Filter by file pattern
vecgrep search "test helpers" --file="**/*_test.go"

# Filter by chunk types
vecgrep search "handlers" --types=function,method

# Filter by line range
vecgrep search "imports" --lines=1-50

# JSON output for scripting
vecgrep search "API endpoints" --format=json
```

### Studio

Open the full-screen terminal workspace:

```bash
vecgrep studio
# or: vecgrep browse
# open a specific project/folder:
vecgrep studio /path/to/project
```

In an interactive terminal, running `vecgrep` without a subcommand also opens Studio.

Studio is built on Charm v2 Bubble Tea/Bubbles/Lip Gloss libraries. It supports query search, result preview, directory/file/line filters, language and chunk-type filters, status/config views, inline global registration when no project is open, incremental indexing with progress, full re-index confirmation, file deletion from the index, and reset confirmation.

### MCP Server

Start the MCP stdio server for AI assistant integration:

```bash
vecgrep serve --mcp
```

This runs on stdio for integration with Claude Desktop, Claude Code, etc.

### Find Similar Code

```bash
vecgrep similar <target> [options]
```

Find code semantically similar to an existing chunk, file location, or text snippet.

**Targets:**
- `42` - Chunk ID (numeric)
- `main.go:15` - File:line location
- `--text "code"` - Inline text snippet

**Options:**

| Flag | Description |
|------|-------------|
| `-n, --limit N` | Maximum results (default: 10) |
| `-f, --format` | Output format: `default`, `json`, `compact` |
| `-l, --lang` | Filter by single language |
| `--languages` | Filter by multiple languages |
| `-t, --type` | Filter by chunk type |
| `--types` | Filter by multiple chunk types |
| `--file` | Filter by file pattern (glob) |
| `--dir` | Filter by directory prefix |
| `--lines` | Filter by line range |
| `--exclude-same-file` | Exclude results from the same file |
| `-T, --text` | Find similar to text snippet |

**Examples:**

```bash
# Find code similar to chunk ID 42
vecgrep similar 42

# Find code similar to line 50 in search.go
vecgrep similar internal/search/search.go:50

# Find code similar to a text snippet
vecgrep similar --text "func NewSearcher"

# Find similar Go code, excluding same file
vecgrep similar 42 --lang go --exclude-same-file

# Find similar code in specific directory
vecgrep similar --text "error handling" --dir=internal/
```

### Check Status

```bash
vecgrep status [options]
```

Displays index statistics, configuration, and pending changes.

Options:
- `-f, --format` - Output format: `default`, `json`

Examples:
```bash
vecgrep status                # Default text output
vecgrep status --format json  # JSON output for scripting
```

### Index Management

#### Delete a File

Remove a specific file and its chunks from the index:

```bash
vecgrep delete <file-path>
```

Example:
```bash
vecgrep delete internal/old_file.go
```

#### Sync Database

Sync the vector database to disk and report current index statistics. With
veclite-only storage all data is self-contained in collection records, so this
is a flush-and-report rather than a vacuum operation:

```bash
vecgrep clean
```

#### Reset Index

Clear all indexed data (destructive):

```bash
vecgrep reset [--force]
```

Options:
- `--force` - Skip confirmation prompt

### Shell Completion

Generate shell completion scripts:

```bash
# Bash
vecgrep completion bash > /etc/bash_completion.d/vecgrep

# Zsh
vecgrep completion zsh > "${fpath[1]}/_vecgrep"

# Fish
vecgrep completion fish > ~/.config/fish/completions/vecgrep.fish
```

## Configuration

Project configuration is usually stored in `vecgrep.yaml` at the project root. Generated index data lives under `~/.vecgrep/projects/<project>/` by default.

> **Note:** Legacy projects may still keep configuration at `.vecgrep/config.yaml`, and `vecgrep init --local` can intentionally create repo-local state. Add `.vecgrep/` to `.gitignore` only for those opt-in local setups.

Use a named local profile when you do not want to coordinate model-specific
dimensions, context, and query templates manually:

```bash
vecgrep config preset
vecgrep config preset quality-code
ollama pull qwen3-embedding:0.6b
vecgrep index --full

# Compare fast-local and quality-code without changing config or index data:
task bench:embeddings
```

`fast-local` keeps the default `nomic-embed-text` profile. `quality-code` uses
the explicit `qwen3-embedding:0.6b` tag with 1,024 dimensions and a 1,024-token
context. Use `vecgrep config preset --global <name>` for global defaults.

```yaml
embedding:
  provider: ollama              # ollama, openai, cohere, or voyage
  model: nomic-embed-text       # Or qwen3-embedding:0.6b with dimensions: 1024
  dimensions: 768               # Must match the selected model's output
  ollama_url: http://localhost:11434
  ollama_context: 0             # Optional num_ctx; 0 uses the Ollama/model default
  ollama_options: {}            # Optional values passed to /api/embed
  query_template: ""            # Optional template containing {{text}}
  document_template: ""         # Optional template containing {{text}}
  openai_api_key: ""            # Set via env var OPENAI_API_KEY or VECGREP_OPENAI_API_KEY
  openai_base_url: ""           # Optional: for Azure OpenAI or custom endpoints
  cohere_api_key: ""            # Set via COHERE_API_KEY or VECGREP_COHERE_API_KEY
  cohere_base_url: ""           # Optional: for custom Cohere-compatible endpoints
  voyage_api_key: ""            # Set via VOYAGE_API_KEY or VECGREP_VOYAGE_API_KEY
  voyage_base_url: ""           # Optional: for custom Voyage-compatible endpoints

indexing:
  chunk_size: 512
  chunk_overlap: 64
  max_file_size: 1048576
  source_buffer_bytes: 8388608  # Bound queued source memory before chunking
  sync_interval: 50             # Files between periodic database syncs
  sync_interval_duration: 30s   # Maximum time between periodic syncs
  ignore_patterns:
    - ".git/**"
    - "node_modules/**"
    - "vendor/**"
    - "*.min.js"
    - "*.min.css"
    - "*.lock"

search:
  default_mode: hybrid          # Default search mode: semantic, keyword, or hybrid
  vector_weight: 0.7            # Weight for vector similarity in hybrid mode (0-1)
  text_weight: 0.3              # Weight for text matching in hybrid mode (0-1)

vector:
  veclite:
    m: 16                       # HNSW max connections per node
    ef_construction: 200        # Build quality (higher = better quality, slower build)
    ef_search: 100              # Search quality (higher = better recall, slower search)
```

### Vector Backend

vecgrep uses [veclite](https://github.com/abdul-hamid-achik/veclite) v0.23.0 as its vector storage backend with:
- **Cosine similarity** - Returns scores from 0.0 (orthogonal) to 1.0 (identical)
- **HNSW indexing** - Fast approximate nearest neighbor search
- **Native filtering** - Glob patterns, prefix matching, range queries
- **Batch operations** - Efficient bulk indexing

vecgrep owns code chunking and embedding generation. VecLite owns storage, filtering, BM25, vector search, and hybrid fusion. Current VecLite collections store one vector per record, so changing embedding provider, model, dimensions, distance metric, or chunking strategy requires a full re-index. vecgrep enforces this with an embedding profile stored in VecLite collection metadata and reports profile status in `vecgrep status` and Studio. See `docs/veclite-integration.md` for the integration contract and named-vector compatibility.

### Configuration Sources

vecgrep loads configuration from multiple sources in priority order:

1. **Environment variables** (`VECGREP_*`) - Highest priority
2. **Project root** `vecgrep.yaml` or `vecgrep.yml`
3. **XDG-style** `.config/vecgrep.yaml`
4. **Legacy** `.vecgrep/config.yaml`
5. **Global project entry** in `~/.vecgrep/config.yaml`
6. **Global defaults** `~/.vecgrep/config.yaml`
7. **Built-in defaults** - Lowest priority

This allows you to set global defaults while overriding per-project settings.

### Environment Variables

vecgrep-specific environment variables use the `VECGREP_` prefix. Provider-standard API key aliases are also supported:

| Variable | Description |
|----------|-------------|
| `VECGREP_EMBEDDING_PROVIDER` | Embedding provider: `ollama` (default), `openai`, `cohere`, or `voyage` |
| `VECGREP_EMBEDDING_MODEL` | Embedding model name |
| `VECGREP_EMBEDDING_DIMENSIONS` | Embedding vector dimensions |
| `VECGREP_OLLAMA_URL` | Ollama API URL (default: `http://localhost:11434`) |
| `VECGREP_OPENAI_API_KEY` | OpenAI API key (or use `OPENAI_API_KEY`) |
| `VECGREP_OPENAI_BASE_URL` | OpenAI base URL (for Azure/custom endpoints) |
| `VECGREP_COHERE_API_KEY` | Cohere API key (or use `COHERE_API_KEY`) |
| `VECGREP_COHERE_BASE_URL` | Cohere base URL |
| `VECGREP_VOYAGE_API_KEY` | Voyage AI API key (or use `VOYAGE_API_KEY`) |
| `VECGREP_VOYAGE_BASE_URL` | Voyage AI base URL |

### Global Flags

These flags work with all commands:

- `-c, --config` - Custom config file path
- `-v, --verbose` - Enable verbose output
- `--version` - Show version information

## MCP Integration

vecgrep implements the [Model Context Protocol](https://modelcontextprotocol.io/) for AI assistant integration.

### Code Search Tools

| Tool | Description |
|------|-------------|
| `vecgrep_init` | Initialize or activate a project. Defaults to global storage; set `local=true` to create `.vecgrep/` in the project. |
| `vecgrep_search` | Search with semantic, keyword, or hybrid mode. Supports rich filtering, explain mode, and context lines. |
| `vecgrep_index` | Index or re-index files in the project |
| `vecgrep_status` | Get index statistics (files, chunks, languages) |
| `vecgrep_similar` | Find code similar to a chunk ID, file:line location, or text snippet |
| `vecgrep_delete` | Delete a file and its chunks from the index |
| `vecgrep_clean` | Sync database to disk and report index stats (no orphans with veclite storage) |
| `vecgrep_reset` | Reset the project database (requires confirmation) |
| `vecgrep_overview` | Get high-level codebase structure, languages, and entry points |
| `vecgrep_batch_search` | Search multiple queries in parallel with optional deduplication |
| `vecgrep_related_files` | Find related files (imports, tests, files that import a given file) |

### Memory Tools

Global agent memory for storing and recalling notes across sessions. Memory is stored at `~/.vecai/memory/memory.veclite`.

| Tool | Description |
|------|-------------|
| `memory_remember` | Store a memory with optional importance, tags, and TTL |
| `memory_recall` | Search memories semantically with filtering options |
| `memory_forget` | Delete memories by ID, tags, or age |
| `memory_stats` | Get memory store statistics |

**memory_remember Parameters:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `content` | string | Yes | The content to remember |
| `importance` | float | No | Priority level 0.0-1.0 (default: 0.5) |
| `tags` | array | No | Categorization tags for filtering |
| `ttl_hours` | int | No | Expiration in hours (0 = never expires) |

**memory_recall Parameters:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `query` | string | Yes | Natural language search query |
| `limit` | int | No | Maximum results (default: 10) |
| `tags` | array | No | Filter by tags |
| `min_importance` | float | No | Minimum importance threshold |

**memory_forget Parameters:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `id` | uint64 | No | Delete specific memory by ID |
| `tags` | array | No | Delete memories with these tags |
| `older_than_hours` | int | No | Delete memories older than this |
| `confirm` | string | No | Set to "yes" for bulk deletion |

**Memory Environment Variables:**

| Variable | Description |
|----------|-------------|
| `VECAI_OLLAMA_URL` | Ollama API URL for memory embeddings |
| `VECAI_EMBEDDING_MODEL` | Embedding model (default: nomic-embed-text) |

**Search Tool Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `query` | string | Search query (required) |
| `limit` | int | Maximum results |
| `mode` | string | Search mode: `semantic`, `keyword`, or `hybrid` |
| `explain` | bool | Return search diagnostics |
| `context_lines` | int | Lines to include before/after each result |
| `language` | string | Filter by single language |
| `languages` | array | Filter by multiple languages |
| `chunk_type` | string | Filter by single chunk type |
| `chunk_types` | array | Filter by multiple chunk types |
| `file_pattern` | string | Filter by file pattern (glob) |
| `directory` | string | Filter by directory prefix |
| `min_line` | int | Filter by minimum start line |
| `max_line` | int | Filter by maximum start line |

**Overview Tool Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `include_structure` | bool | Include directory structure (default: true) |
| `include_entry_points` | bool | Include main/index entry points (default: true) |

**Batch Search Tool Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `queries` | array | List of search queries (required) |
| `limit_per_query` | int | Maximum results per query (default: 3) |
| `deduplicate` | bool | Remove duplicate results across queries (default: true) |

**Related Files Tool Parameters:**

| Parameter | Type | Description |
|-----------|------|-------------|
| `file` | string | Path to the file (required) |
| `relationship` | string | Type: `imports`, `imported_by`, `tests`, or `all` (default: all) |
| `limit` | int | Maximum results (default: 10) |

**Note:** In uninitialized directories, only `vecgrep_init` is available. After initialization, all code search tools become available. Memory tools are always available (they use a global store).

### Claude Code (CLI)

Add vecgrep as an MCP server:

```bash
# Add for all your projects (recommended)
claude mcp add --scope user vecgrep -- vecgrep serve --mcp

# Or add for current project only
claude mcp add --scope local vecgrep -- vecgrep serve --mcp
```

The MCP server works in any directory. If the project has not been registered or initialized yet, use `vecgrep_init` first.

Manage your MCP servers:

```bash
claude mcp list              # List all servers
claude mcp get vecgrep       # Show vecgrep config
claude mcp remove vecgrep    # Remove vecgrep
```

### Claude Code (Manual Config)

Add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "vecgrep": {
      "command": "vecgrep",
      "args": ["serve", "--mcp"],
      "cwd": "/path/to/your/project"
    }
  }
}
```

### Claude Desktop

Add to your Claude Desktop configuration (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "vecgrep": {
      "command": "vecgrep",
      "args": ["serve", "--mcp"],
      "cwd": "/path/to/your/project"
    }
  }
}
```

**Note:** The `cwd` should point to the project directory. vecgrep can use either global project registration or a local `.vecgrep/` directory.

## Docker

Run vecgrep in a container while using Ollama on your host machine.

### Quick Start

```bash
# Start Ollama on host (with Metal GPU on macOS)
OLLAMA_METAL=1 OLLAMA_HOST=0.0.0.0 ollama serve

# Run the vecgrep container
docker compose run --rm vecgrep vecgrep --help
```

### Configuration

The container connects to Ollama on your host via `host.docker.internal:11434`.

Volumes:
- `vecgrep-home:/home/vecgrep/.vecgrep` - Persistent global vecgrep config and project indexes for the container
- `./:/workspace:ro` - Your codebase (read-only)

### Index from Container

```bash
docker compose run --rm vecgrep vecgrep index /workspace
docker compose run --rm vecgrep vecgrep search "your query"
```

## Upgrading

### Breaking Changes in v2.0

Version 2.0 includes significant improvements that require re-indexing:

- **Cosine distance** replaces Euclidean for better embedding similarity
- **Native filtering** during search instead of post-filtering
- **Batch operations** for faster indexing

After upgrading, re-index your codebase:

```bash
vecgrep reset --force
vecgrep index
```

## Development

See [docs/development.md](docs/development.md) for detailed development workflow.

```bash
task doctor       # Check your environment
task setup        # Install dependencies
task dev          # Run with hot reload
task check        # Run fmt, lint, test
task build        # Build binary
task flows        # Run terminal Studio flows
task site         # Start the VitePress docs site
task site:build   # Build the docs site
```

## License

MIT License - see [LICENSE](LICENSE) for details.
