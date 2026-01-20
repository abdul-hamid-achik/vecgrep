# vecgrep

Local-first semantic code search powered by embeddings.

vecgrep indexes your codebase and enables natural language search using vector embeddings. All processing happens locally via Ollama, ensuring your code never leaves your machine.

## Features

- **Semantic Search** - Find code by meaning, not just keywords
- **Local-First** - All embeddings generated locally via Ollama
- **Incremental Indexing** - Only re-index changed files
- **Language-Aware Chunking** - Intelligent code splitting by functions, classes, and blocks
- **MCP Support** - Model Context Protocol server for AI assistant integration
- **Web Interface** - Browser-based search UI with syntax highlighting

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

3. **Index your codebase:**
   ```bash
   vecgrep index
   ```

4. **Search:**
   ```bash
   vecgrep search "error handling in HTTP requests"
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

### Start Server

```bash
vecgrep serve [--web] [--mcp] [--port 8080] [--host localhost]
```

Options:
- `--web` - Start web interface (default if no flags)
- `--mcp` - Start MCP server on stdio
- `-p, --port` - Server port (default: 8080)
- `--host` - Server host (default: localhost)

### Check Status

```bash
vecgrep status
```

Displays index statistics and configuration.

## Configuration

Configuration is stored in `.vecgrep/config.yaml`:

```yaml
embedding:
  provider: ollama
  model: nomic-embed-text
  dimensions: 768
  ollama_url: http://localhost:11434

indexing:
  chunk_size: 512
  chunk_overlap: 64
  max_file_size: 1048576
  ignore_patterns:
    - ".git"
    - "node_modules"
    - "vendor"
    - "*.min.js"
```

### Environment Variables

- `OLLAMA_HOST` - Override Ollama URL
- `VECGREP_VERBOSE` - Enable verbose output

## MCP Integration

vecgrep implements the [Model Context Protocol](https://modelcontextprotocol.io/) for AI assistant integration.

### Claude Desktop

Add to your Claude configuration:

```json
{
  "mcpServers": {
    "vecgrep": {
      "command": "vecgrep",
      "args": ["serve", "--mcp"]
    }
  }
}
```

## Development

```bash
# Install dependencies
task setup

# Run with hot reload
task dev

# Run tests
task test

# Build
task build

# Generate code (sqlc, templ, css)
task generate
```

## License

MIT License - see [LICENSE](LICENSE) for details.
