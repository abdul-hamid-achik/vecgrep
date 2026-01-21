# vecgrep

Local-first semantic code search powered by embeddings.

vecgrep indexes your codebase and enables natural language search using vector embeddings. All processing happens locally via Ollama, ensuring your code never leaves your machine.

## Features

- **Semantic Search** - Find code by meaning, not just keywords
- **Local-First** - All embeddings generated locally via Ollama
- **OpenAI Support** - Optional cloud embeddings via OpenAI API
- **Incremental Indexing** - Only re-index changed files
- **Language-Aware Chunking** - Intelligent code splitting by functions, classes, and blocks
- **MCP Support** - Model Context Protocol server for AI assistant integration
- **Web Interface** - Browser-based search UI with syntax highlighting
- **Similar Code Finder** - Find semantically similar code across your codebase

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

Options:
- `-n, --limit N` - Maximum results (default: 10)
- `-f, --format` - Output format: `default`, `json`, `compact`
- `-l, --lang` - Filter by language (e.g., `go`, `python`)
- `-t, --type` - Filter by chunk type: `function`, `class`, `block`
- `--file` - Filter by file pattern (glob)

Examples:
```bash
vecgrep search "database connection pooling"
vecgrep search "authentication middleware" -l go -n 5
vecgrep search "error handling" --file "**/*_test.go"
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

Targets:
- `42` - Chunk ID (numeric)
- `main.go:15` - File:line location
- `--text "code"` - Inline text snippet

Options:
- `-n, --limit N` - Maximum results (default: 10)
- `-f, --format` - Output format: `default`, `json`, `compact`
- `-l, --lang` - Filter by language
- `-t, --type` - Filter by chunk type
- `--file` - Filter by file pattern (glob)
- `--exclude-same-file` - Exclude results from the same file
- `-T, --text` - Find similar to text snippet

Examples:
```bash
# Find code similar to chunk ID 42
vecgrep similar 42

# Find code similar to line 50 in search.go
vecgrep similar internal/search/search.go:50

# Find code similar to a text snippet
vecgrep similar --text "func NewSearcher"

# Find similar Go code, excluding same file
vecgrep similar 42 --lang go --exclude-same-file
```

### Check Status

```bash
vecgrep status
```

Displays index statistics and configuration.

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

server:
  host: localhost
  port: 8080

vector:
  backend: sqlite-vec           # or "veclite" for HNSW-based search
  veclite:
    m: 16                       # HNSW max connections per node
    ef_construction: 200        # Build quality (higher = better quality, slower build)
    ef_search: 100              # Search quality (higher = better recall, slower search)
```

### Vector Backends

vecgrep supports two vector storage backends:

| Backend | Description | Best For |
|---------|-------------|----------|
| `sqlite-vec` | SQLite extension with exact cosine similarity | Smaller codebases, exact results |
| `veclite` | HNSW-based approximate nearest neighbor search | Larger codebases, faster queries |

To switch backends, update your config and re-index:
```bash
# Edit .vecgrep/config.yaml to set vector.backend
vecgrep reset --force
vecgrep index
```

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

### Available Tools

| Tool | Description |
|------|-------------|
| `vecgrep_init` | Initialize vecgrep in a directory (creates `.vecgrep` folder) |
| `vecgrep_search` | Semantic search across the indexed codebase |
| `vecgrep_index` | Index or re-index files in the project |
| `vecgrep_status` | Get index statistics (files, chunks, languages) |
| `vecgrep_similar` | Find code similar to a chunk ID, file:line location, or text snippet |
| `vecgrep_delete` | Delete a file and its chunks from the index |
| `vecgrep_clean` | Remove orphaned data and optimize the database |
| `vecgrep_reset` | Reset the project database (requires confirmation) |

**Note:** In uninitialized directories, only `vecgrep_init` is available. After initialization, all tools become available.

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
