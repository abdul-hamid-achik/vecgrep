# Embedding Providers

vecgrep defaults to local Ollama embeddings and supports managed cloud providers when you choose them.

## Provider Matrix

| Provider | Default Model | Dimensions | Notes |
| --- | --- | ---: | --- |
| Ollama | `nomic-embed-text` | 768 | Local-first default |
| OpenAI | `text-embedding-3-small` | 1536 | Supports configurable dimensions for `text-embedding-3-*` models |
| Cohere | `embed-v4.0` | 1536 | Uses retrieval-specific document/query input types |
| Voyage AI | `voyage-code-3` | 1024 | Uses retrieval-specific document/query input types |

## Ollama

```bash
ollama pull nomic-embed-text
vecgrep config set embedding.provider ollama
vecgrep config set embedding.model nomic-embed-text
vecgrep config set embedding.dimensions 768
```

If your Ollama server uses a non-default URL:

```bash
vecgrep config set embedding.ollama_url http://localhost:11434
```

## OpenAI

```bash
export OPENAI_API_KEY=sk-your-key
vecgrep config set embedding.provider openai
vecgrep config set embedding.model text-embedding-3-small
vecgrep config set embedding.dimensions 1536
vecgrep index --full
```

For custom or compatible endpoints:

```bash
vecgrep config set embedding.openai_base_url https://example.test/v1
```

## Cohere

```bash
export COHERE_API_KEY=your-key
vecgrep config set embedding.provider cohere
vecgrep config set embedding.model embed-v4.0
vecgrep config set embedding.dimensions 1536
vecgrep index --full
```

Cohere indexing uses `search_document`; search uses `search_query`.

## Voyage AI

```bash
export VOYAGE_API_KEY=your-key
vecgrep config set embedding.provider voyage
vecgrep config set embedding.model voyage-code-3
vecgrep config set embedding.dimensions 1024
vecgrep index --full
```

Voyage indexing uses `document`; search uses `query`.

## Re-indexing Rules

Changing provider, model, dimensions, distance metric, or chunking profile changes vector meaning. Run a full rebuild after changing any of those settings:

```bash
vecgrep index --full
```

You can also clear the index:

```bash
vecgrep reset --force
```
