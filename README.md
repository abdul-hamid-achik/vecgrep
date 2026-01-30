# vecgrep

Local-first semantic code search powered by embeddings.

vecgrep indexes your codebase and enables natural language search using vector embeddings. All processing happens locally via Ollama, ensuring your code never leaves your machine.

## Features

- **Hybrid Search** - Combine semantic (vector) and keyword search for best results
- **Three Search Modes** - Choose between semantic, keyword, or hybrid search
- **Local-First** - All embeddings generated locally via Ollama
- **OpenAI Support** - Optional cloud embeddings via OpenAI API
- **Incremental Indexing** - Only re-index changed files
- **Batch Operations** - Efficient bulk indexing with batch inserts
- **Language-Aware Chunking** - Intelligent code splitting by functions, classes, and blocks
- **Rich Filtering** - Filter by language, chunk type, directory, file pattern, and line range
- **MCP Support** - Model Context Protocol server for AI assistant integration
- **Web Interface** - Browser-based search UI with syntax highlighting
- **Similar Code Finder** - Find semantically similar code across your codebase
- **Search Diagnostics** - Explain mode for debugging and optimizing searches
- **Embedding Cache** - Cache query embeddings for faster repeated searches
- **Multi-Profile Search** - Search across code, notes, and other content sources
- **Batch Search** - Search multiple queries in parallel via MCP
- **Codebase Overview** - Get high-level insights about your codebase structure
- **Related Files** - Find imports, tests, and files that depend on a given file
- **Context Lines** - Include surrounding code in search results

## Installation

### Prerequisites

- Go 1.25+
- [Ollama](https://ollama.ai) with an embedding model (default: `nomic-embed-text`)
- [Task](https://taskfile.dev) (optional, for development)

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

   **Important:** Add `.vecgrep` to your `.gitignore` file:
   ```bash
   echo ".vecgrep" >> .gitignore
   ```

3. **Index your codebase:**
   ```bash
   vecgrep index
   ```

4. **Search:**
   ```bash
   vecgrep search "error handling in HTTP requests"
   ```

### Using OpenAI (Alternative)

If you prefer cloud embeddings via OpenAI:

1. **Set your API key:**
   ```bash
   export OPENAI_API_KEY=sk-your-key-here
   ```

2. **Configure vecgrep to use OpenAI:**

   Edit `.vecgrep/config.yaml`:
   ```yaml
   embedding:
     provider: openai
     model: text-embedding-3-small
     dimensions: 1536
   ```

   Or set via environment:
   ```bash
   export VECGREP_EMBEDDING_PROVIDER=openai
   export VECGREP_EMBEDDING_MODEL=text-embedding-3-small
   ```

3. **Re-index your codebase:**
   ```bash
   vecgrep index --full
   ```

## Usage

### Initialize a Project

```bash
vecgrep init [--force]
```

Creates a `.vecgrep` directory with configuration and database.

### Index Files

```bash
vecgrep index [paths...] [--full] [--ignore pattern]
```

Options:
- `--full` - Force full re-index (ignores file hashes)
- `--ignore` - Additional patterns to ignore
- `-v, --verbose` - Show detailed progress

### Search

```bash
vecgrep search <query> [options]
```

**Search Modes:**

| Mode | Description |
|------|-------------|
| `hybrid` | Combines vector similarity with text matching (default) |
| `semantic` | Pure vector similarity search |
| `keyword` | Text-based search using pattern matching |

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
| `-P, --profile` | Search a specific profile |
| `--profiles` | Search multiple profiles (comma-separated) |
| `--all-profiles` | Search all configured profiles |

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

### Web Interface

Start the web server:

```bash
vecgrep serve --web
```

Open http://localhost:8080 in your browser to search with a visual interface.

Options:
- `-p, --port` - Server port (default: 8080)
- `--host` - Server host (default: localhost)

### MCP Server

Start the MCP server for AI assistant integration:

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

#### Clean Database

Remove orphaned data (chunks without files, embeddings without chunks) and optimize:

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

### Search Profiles

Profiles allow you to search across different content sources (code, notes, documentation) with separate indexes.

```bash
# List all configured profiles
vecgrep profile list

# Add a new profile
vecgrep profile add notes --source noted --description "Personal notes"

# Remove a profile
vecgrep profile remove notes

# Show profile details
vecgrep profile show notes
```

**Searching with profiles:**

```bash
# Search a specific profile
vecgrep search "meeting notes" --profile notes

# Search multiple profiles
vecgrep search "API design" --profiles code,notes

# Search all configured profiles
vecgrep search "authentication" --all-profiles
```

Profiles are configured in `~/.config/vecgrep/profiles.yaml`.

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

Configuration is stored in `.vecgrep/config.yaml`:

> **Note:** The `.vecgrep` directory contains your local index database and configuration.
> Add it to `.gitignore` to avoid committing it to version control.

```yaml
embedding:
  provider: ollama              # or "openai" for cloud embeddings
  model: nomic-embed-text       # or "text-embedding-3-small" for OpenAI
  dimensions: 768               # 1536 for text-embedding-3-small, 3072 for large
  ollama_url: http://localhost:11434
  openai_api_key: ""            # Set via env var OPENAI_API_KEY or VECGREP_OPENAI_API_KEY
  openai_base_url: ""           # Optional: for Azure OpenAI or custom endpoints

indexing:
  chunk_size: 512
  chunk_overlap: 64
  max_file_size: 1048576
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

server:
  host: localhost
  port: 8080

vector:
  veclite:
    m: 16                       # HNSW max connections per node
    ef_construction: 200        # Build quality (higher = better quality, slower build)
    ef_search: 100              # Search quality (higher = better recall, slower search)
```

### Vector Backend

vecgrep uses veclite as its vector storage backend with:
- **Cosine distance** for normalized embedding similarity
- **HNSW indexing** for fast approximate nearest neighbor search
- **Native filtering** with support for glob patterns, prefix matching, range queries
- **Batch operations** for efficient bulk indexing

### Configuration Sources

vecgrep loads configuration from multiple sources in priority order:

1. **Environment variables** (`VECGREP_*`) - Highest priority
2. **Project root** `vecgrep.yaml` or `vecgrep.yml`
3. **XDG-style** `.config/vecgrep.yaml`
4. **Legacy** `.vecgrep/config.yaml`
5. **Global defaults** `~/.vecgrep/config.yaml`
6. **Built-in defaults** - Lowest priority

This allows you to set global defaults while overriding per-project settings.

### Environment Variables

All environment variables use the `VECGREP_` prefix:

| Variable | Description |
|----------|-------------|
| `VECGREP_EMBEDDING_PROVIDER` | Embedding provider: `ollama` (default) or `openai` |
| `VECGREP_EMBEDDING_MODEL` | Embedding model name |
| `VECGREP_OLLAMA_URL` | Ollama API URL (default: `http://localhost:11434`) |
| `VECGREP_OPENAI_API_KEY` | OpenAI API key (or use `OPENAI_API_KEY`) |
| `VECGREP_OPENAI_BASE_URL` | OpenAI base URL (for Azure/custom endpoints) |
| `VECGREP_HOST` | Server bind address |
| `VECGREP_PORT` | Server port |

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
| `vecgrep_init` | Initialize vecgrep in a directory (creates `.vecgrep` folder) |
| `vecgrep_search` | Search with semantic, keyword, or hybrid mode. Supports rich filtering, explain mode, and context lines. |
| `vecgrep_index` | Index or re-index files in the project |
| `vecgrep_status` | Get index statistics (files, chunks, languages) |
| `vecgrep_similar` | Find code similar to a chunk ID, file:line location, or text snippet |
| `vecgrep_delete` | Delete a file and its chunks from the index |
| `vecgrep_clean` | Remove orphaned data and optimize the database |
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

The MCP server works in any directory. If `.vecgrep` doesn't exist, use `vecgrep_init` to initialize it first.

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

**Note:** The `cwd` should point to a directory with an initialized `.vecgrep` folder.

## Docker

Run vecgrep in a container while using Ollama on your host machine.

### Quick Start

```bash
# Start Ollama on host (with Metal GPU on macOS)
OLLAMA_METAL=1 OLLAMA_HOST=0.0.0.0 ollama serve

# Run vecgrep container
docker compose up -d
```

The web interface is available at http://localhost:8080

### Configuration

The container connects to Ollama on your host via `host.docker.internal:11434`.

Volumes:
- `./.vecgrep:/data/.vecgrep` - Persistent index database
- `./:/workspace:ro` - Your codebase (read-only)

### Index from Container

```bash
docker compose exec app vecgrep index /workspace
docker compose exec app vecgrep search "your query"
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

See [DEVELOPMENT.md](DEVELOPMENT.md) for detailed development workflow.

```bash
task doctor       # Check your environment
task setup        # Install dependencies
task dev          # Run with hot reload
task check        # Run fmt, lint, test
task build        # Build binary
```

## License

MIT License - see [LICENSE](LICENSE) for details.
