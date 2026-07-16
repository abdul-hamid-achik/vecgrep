# VecLite Integration

vecgrep uses VecLite as its local vector-search database. vecgrep owns codebase discovery, chunking, embedding generation, provider configuration, user workflows, and hybrid result fusion (weighted score fusion of cosine similarity and normalized BM25 in `internal/db/veclite_backend.go`, producing calibrated 0-1 scores — VecLite's built-in RRF hybrid fusion is intentionally not used because raw reciprocal-rank scores are bounded by ~1/61 and are not meaningful as user-facing relevance). VecLite owns durable vector storage, metadata filtering, BM25, and HNSW search.

## Current Storage Model

The current backend stores one record per indexed code chunk in the `chunks` collection.

Collection configuration:

- dimension: `embedding.dimensions`
- distance: cosine
- vector index: HNSW
- BM25 fields: `content`, `symbol_name`, `relative_path`, `language`, `chunk_type`

Each record stores the embedding vector plus payload fields for:

- file identity: `file_path`, `relative_path`, `file_hash`, `file_size`
- chunk identity: `chunk_key`, `start_line`, `end_line`, `start_byte`, `end_byte`
- chunk meaning: `content`, `language`, `chunk_type`, `symbol_name`
- project identity: `project_root`, `indexed_at`

This model matches VecLite's current one-vector-per-record API.

## Storage Layout

vecgrep stores project indexes outside the repository by default:

```text
~/.vecgrep/
  config.yaml
  projects/
    <project-name>/
      vectors.veclite
      embedding_profile.json
```

`~/.vecgrep/config.yaml` maps project names to absolute project paths and project data directories. This keeps generated vector data out of source repositories and avoids requiring every project to add `.vecgrep/` to `.gitignore`.

Repository-local configuration is still supported:

- `vecgrep.yaml` or `.config/vecgrep.yaml` can override settings for a project.
- `data_dir` can point to a custom location when a project intentionally wants local or shared storage.
- legacy `.vecgrep/config.yaml` is still read for compatibility, but new default flows should not create repo-local `.vecgrep/` directories.

The VecLite database path is always derived from the resolved vecgrep data directory as `vectors.veclite`.

## Embedding Boundary

vecgrep should continue to generate embeddings through `internal/embed.Provider`.

Some cloud providers expose different embedding modes for indexed documents and search queries. vecgrep models that with optional provider interfaces:

- `embed.DocumentProvider` for chunk embeddings written to VecLite
- `embed.QueryProvider` for semantic, hybrid, and similar-by-text query embeddings

Cohere uses `search_document` for indexed chunks and `search_query` for searches. Voyage uses `document` for indexed chunks and `query` for searches. Providers without retrieval-specific modes continue to use the base `EmbedBatch` and `Embed` methods.

VecLite should not own:

- code walking or ignore rules
- language-aware chunking
- provider credentials or remote API behavior
- provider detection or model selection
- provider-specific query/document input type selection
- chunker-version rebuild decisions

VecLite should own:

- vector dimension validation
- HNSW and brute-force search
- BM25 indexing and text search
- metadata filters for project, path, language, type, and line ranges
- persistence and sync

## VecLite Library Contract

vecgrep uses VecLite as an embedded Go library, not as an embedding framework. The expected library responsibilities are:

- open or create a database at a caller-provided path
- create or open the `chunks` collection with dimension, distance, HNSW, and BM25 configuration
- validate vector dimensions on insert and query
- store vectors and payload fields durably
- expose vector search, BM25 search, hybrid search, metadata filters, stats, delete, sync, and close operations

vecgrep should keep a thin adapter in `internal/db/veclite_backend.go` that converts between vecgrep's `ChunkRecord` model and VecLite records. Provider selection, embedding batching, chunking, ignore rules, rebuild policy, and CLI/Studio/MCP workflows should stay outside VecLite.

When VecLite adds collection metadata, vecgrep should move `embedding_profile.json` into collection metadata without changing the public CLI behavior. Until then, the sidecar remains the source of truth for vector compatibility.

## Embedding Profile Guard

Changing the embedding provider, model, dimensions, distance metric, or chunker version can invalidate existing vectors. Dimension checks alone are not enough because two models can share the same dimension while producing incompatible vector spaces.

vecgrep persists and validates an embedding profile guard before indexing and vector search:

```json
{
  "schema_version": 1,
  "profile_id": "ollama:nomic-embed-text:768:cosine:code-chunker-v2-lossless",
  "provider": "ollama",
  "model": "nomic-embed-text",
  "dimensions": 768,
  "distance": "cosine",
  "modality": "text",
  "preprocessor": "code-chunker-v2-lossless"
}
```

Current implementation:

- Persist the profile under the `embedding_profile` key in VecLite collection metadata.
- Compute `profile_id` from provider, model, dimensions, distance, modality, and chunker version.
- On `index`, compare the configured profile with the stored profile before writing chunks unless `--full` is used.
- On mismatch, fail with a clear message and require `vecgrep index --full` or `vecgrep reset`.
- On semantic, hybrid, and similar search, fail when the stored profile does not match the configured provider or is missing for an existing vector index.
- Keyword search remains available because it uses VecLite BM25 without generating a query embedding.
- Write the profile after a successful first index or full re-index.
- Report profile status through `vecgrep status` and Studio.

Legacy `embedding_profile.json` sidecars from pre-v0.16.0 vecgrep builds are migrated transparently: on the first open after the bump, if collection metadata has no profile but the sidecar exists, vecgrep reads the sidecar, writes it to collection metadata, and removes the sidecar. New projects never create the sidecar.

## Named Vector Spaces (Available)

VecLite v0.16.0 shipped named vector spaces. The vecgrep integration can now stay mostly unchanged when adopting them:

- keep the existing `chunks` logical collection
- map current embeddings to the default or `code_text` vector space
- optionally add other spaces later, such as documentation summaries or symbol-level embeddings
- keep chunking and provider orchestration in vecgrep

Adoption is incremental. The current vecgrep build pins veclite v0.24.0 (see `go.mod` for the authoritative pinned version) and, as of that version, still uses the single-vector default space; named spaces remain an opt-in capability for a future vecgrep release rather than a runtime requirement. For incompatible embedding profiles before that opt-in, vecgrep still uses a full re-index.

## Related Repos

- VecLite embedding guide: `~/projects/veclite/docs/embeddings.md`
- VecLite ADR: `~/projects/veclite/docs/adr/0001-embedding-boundary-and-named-vector-spaces.md`
- vidtrace evidence-search design: `~/projects/vidtrace/docs/EVIDENCE_SEARCH.md`
