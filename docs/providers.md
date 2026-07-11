# Embedding Providers

vecgrep defaults to local Ollama embeddings and supports managed cloud providers when you choose them.

## Provider Matrix

| Provider | Default Model | Dimensions | Notes |
| --- | --- | ---: | --- |
| Ollama | `nomic-embed-text` | 768 | Local-first default |
| OpenAI | `text-embedding-3-small` | 1536 | Supports configurable dimensions for `text-embedding-3-*` models |
| Cohere | `embed-v4.0` | 1536 | Uses retrieval-specific document/query input types |
| Voyage AI | `voyage-code-3` | 1024 | Uses retrieval-specific document/query input types |

## Local Embedding Presets

vecgrep keeps `nomic-embed-text` as the built-in default and provides two
explicit Ollama presets:

| Preset | Model | Dimensions | Context | Best for |
| --- | --- | ---: | ---: | --- |
| `fast-local` | `nomic-embed-text` | 768 | 2,048 | Lower memory, faster indexing, strong broad recall |
| `quality-code` | `qwen3-embedding:0.6b` | 1,024 | 1,024 | Better first-page code retrieval when extra latency and memory are acceptable |

List or apply them without manually coordinating model, dimensions, context,
options, and templates:

```bash
vecgrep config preset
vecgrep config preset quality-code
ollama pull qwen3-embedding:0.6b
vecgrep index --full
```

Use `--global` to apply a preset to global defaults. Applying a preset does not
download a model or rebuild an index; the command prints both required next
steps. It preserves provider endpoints, credentials, throttle/cache settings,
and unrelated indexing/search configuration.

To compare both profiles on the bundled labeled Go/polyglot corpus without
mutating project configuration or index data:

```bash
task bench:embeddings
# Or:
vecgrep benchmark embeddings --profiles fast-local,quality-code
```

The report includes Top-1, Recall@5, Recall@10, MRR, embedding throughput, and
corpus/query latency. Results are machine- and corpus-specific; use
`--dataset path/to/dataset.json` to supply your own labels.

## Ollama

```bash
ollama pull nomic-embed-text
vecgrep config set embedding.provider ollama
vecgrep config set embedding.model nomic-embed-text
vecgrep config set embedding.dimensions 768
```

### Qwen3 Embedding

Use an explicit Qwen size tag so Ollama does not resolve the bare model name to
the much larger 8B variant:

```bash
ollama pull qwen3-embedding:0.6b
vecgrep config set embedding.provider ollama
vecgrep config set embedding.model qwen3-embedding:0.6b
vecgrep config set embedding.dimensions 1024
vecgrep index --full
```

`qwen3-embedding:0.6b` is a balanced local code-search option with native
1,024-dimensional vectors. Model and dimension changes always require a full
rebuild.

### Ollama request options

Ollama query, document, and warmup requests use `/api/embed`. Configure its
context and options in `vecgrep.yaml`. For Qwen3 0.6B, start with a 1,024-token
context for vecgrep's default 512-token chunks; the model's 32K maximum consumes
substantially more unified memory and is unnecessary for the default chunk size.

```yaml
embedding:
  ollama_context: 1024
  ollama_options: {}
```

Optional query and document templates use `{{text}}` as the input placeholder:

```yaml
embedding:
  query_template: "search_query: {{text}}"
  document_template: "search_document: {{text}}"
```

Templates are part of the persisted embedding profile. Changing either
template requires `vecgrep index --full`.

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
