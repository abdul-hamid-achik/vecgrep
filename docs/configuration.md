# Configuration

vecgrep resolves configuration from environment variables, project files, and global defaults.

## Resolution Order

Highest priority wins:

1. Environment variables
2. Project root `vecgrep.yaml` or `vecgrep.yml`
3. Project `.config/vecgrep.yaml`
4. Legacy project `.vecgrep/config.yaml`
5. Global project entry in `~/.vecgrep/config.yaml`
6. Global defaults in `~/.vecgrep/config.yaml`
7. Built-in defaults

## Default Storage

New projects use global storage by default:

```text
~/.vecgrep/
  config.yaml
  projects/
    <project-name>/
      vectors.veclite
      embedding_profile.json
```

This keeps generated vector indexes out of source repositories.

Use local storage only when required:

```bash
vecgrep init --local
```

## Project Config

Create `vecgrep.yaml` in your project root for checked-in settings:

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
    - ".git/**"
    - "node_modules/**"
    - "vendor/**"

search:
  default_mode: hybrid
  vector_weight: 0.7
  text_weight: 0.3

vector:
  veclite:
    m: 16
    ef_construction: 200
    ef_search: 100
```

## Configure From CLI

Set project-local config:

```bash
vecgrep config set search.default_mode keyword
vecgrep config set embedding.provider voyage
vecgrep config set embedding.model voyage-code-3
vecgrep config set embedding.dimensions 1024
```

Set global defaults:

```bash
vecgrep config set --global embedding.provider ollama
```

Inspect resolved config:

```bash
vecgrep config show
vecgrep config show --global
```

## Environment Variables

| Variable | Description |
| --- | --- |
| `VECGREP_EMBEDDING_PROVIDER` | `ollama`, `openai`, `cohere`, or `voyage` |
| `VECGREP_EMBEDDING_MODEL` | Embedding model name |
| `VECGREP_EMBEDDING_DIMENSIONS` | Embedding vector dimensions |
| `VECGREP_OLLAMA_URL` | Ollama API URL |
| `VECGREP_OPENAI_API_KEY` | OpenAI API key |
| `VECGREP_OPENAI_BASE_URL` | OpenAI-compatible base URL |
| `VECGREP_COHERE_API_KEY` | Cohere API key |
| `VECGREP_COHERE_BASE_URL` | Cohere-compatible base URL |
| `VECGREP_VOYAGE_API_KEY` | Voyage AI API key |
| `VECGREP_VOYAGE_BASE_URL` | Voyage-compatible base URL |

Provider-standard API key aliases are also supported: `OPENAI_API_KEY`, `COHERE_API_KEY`, and `VOYAGE_API_KEY`.
