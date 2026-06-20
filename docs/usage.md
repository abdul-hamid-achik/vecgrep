# CLI Usage

vecgrep exposes command-line search, indexing, status, MCP, and maintenance commands.

## Initialize

```bash
vecgrep init [--local] [--force]
```

Default behavior:

- Registers the project in `~/.vecgrep/config.yaml`.
- Stores data under `~/.vecgrep/projects/<project>/`.
- Does not create a repo-local `.vecgrep/` directory.

Use `--local` to create project-local state intentionally.

## Index

```bash
vecgrep index [paths...] [--full] [--ignore pattern]
```

| Flag | Description |
| --- | --- |
| `--full` | Force a full re-index and ignore file hashes |
| `--ignore` | Add an ignore pattern for this run |
| `-v`, `--verbose` | Print detailed progress |

vecgrep writes `embedding_profile.json` next to `vectors.veclite`. If provider, model, dimensions, distance, or chunking profile changes, vector search and incremental indexing require a full rebuild.

## Search

```bash
vecgrep search <query> [options]
```

| Flag | Description |
| --- | --- |
| `-n`, `--limit` | Maximum result count |
| `-f`, `--format` | `default`, `json`, or `compact` |
| `-m`, `--mode` | `hybrid`, `semantic`, or `keyword` |
| `--explain` | Include search diagnostics |
| `-l`, `--lang` | Filter by one language |
| `--languages` | Filter by multiple languages |
| `-t`, `--type` | Filter by one chunk type |
| `--types` | Filter by multiple chunk types |
| `--file` | Filter by glob pattern |
| `--dir` | Filter by directory prefix |
| `--lines` | Filter by line range, such as `1-100` |

Examples:

```bash
vecgrep search "database connection pooling"
vecgrep search --mode=semantic "error handling patterns"
vecgrep search --mode=keyword "SELECT FROM users"
vecgrep search --explain "authentication middleware"
vecgrep search "test helpers" --file="**/*_test.go"
vecgrep search "handlers" --types=function,method
vecgrep search "API endpoints" --format=json
```

## Similar Code

```bash
vecgrep similar --chunk-id 42
vecgrep similar --file-location internal/search/search.go:50
vecgrep similar --text "func handleError(err error)"
```

Useful filters:

```bash
vecgrep similar --chunk-id 42 --lang go --exclude-same-file
vecgrep similar --text "config loading" --dir internal/
```

## Status and Maintenance

```bash
vecgrep status
vecgrep status --format json
vecgrep delete internal/old_file.go
vecgrep clean
vecgrep reset --force
```

## Shell Completion

```bash
vecgrep completion bash > /etc/bash_completion.d/vecgrep
vecgrep completion zsh > "${fpath[1]}/_vecgrep"
vecgrep completion fish > ~/.config/fish/completions/vecgrep.fish
```
