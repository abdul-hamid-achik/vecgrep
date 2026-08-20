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
vecgrep index [paths...] [--full] [--ignore pattern] [--structural-chunks mode]
```

| Flag | Description |
| --- | --- |
| `--full` | Force a full re-index and ignore file hashes |
| `--ignore` | Add an ignore pattern for this run |
| `--structural-chunks` | Override codemap symbol chunks: `auto`, `off`, or `required` |
| `-v`, `--verbose` | Print detailed progress |

vecgrep writes `embedding_profile.json` next to `vectors.veclite`. If provider, model, dimensions, distance, or chunking profile changes, vector search and incremental indexing require a full rebuild.

## Search

```bash
vecgrep search <query> [options]
```

| Flag | Description |
| --- | --- |
| `-n`, `--limit` | Maximum result count |
| `-f`, `--format` | `default`, `json`, `compact`, or `json-envelope` |
| `-m`, `--mode` | `hybrid`, `semantic`, or `keyword` |
| `--explain` | Include search diagnostics (routed to stderr for machine formats) |
| `-l`, `--lang` | Filter by one language |
| `--languages` | Filter by multiple languages |
| `-t`, `--type` | Filter by one chunk type |
| `--types` | Filter by multiple chunk types |
| `--file` | Filter by glob pattern |
| `--dir` | Filter by directory prefix |
| `--lines` | Filter by line range, such as `1-100` |
| `--scope-files` | Restrict search to these relative paths (comma-separated) |
| `--symbol` | Scope search to a symbol's blast radius via codemap impact |
| `--min-score` | Drop results scoring below this threshold (0-1 in all modes; keyword scores are BM25 normalized per result set) |

### Scores

What the `score` field means depends on the mode:

- **hybrid** (default): a calibrated 0-1 similarity — `0.7·cosine + 0.3·normalized BM25`.
  The keyword contribution of chunks under 200 characters is damped toward a 0.3
  floor so import-only snippets don't outrank real code on BM25 length bias.
  Good matches typically land around 0.45-0.69.
- **semantic**: raw cosine similarity, 0-1.
- **keyword**: BM25 normalized to 0-1 within the result set — the top hit scores
  1.0, so `--min-score` applies, but scores are not comparable across queries.
  JSON output keeps the raw BM25 value in `distance`.

If the embedding provider is unreachable at query time, hybrid search degrades
to keyword-only instead of failing — never silently. A warning carrying the
provider error is printed with the results (on stderr for machine formats, so
JSON output stays parseable), and the degraded results carry the same
per-result-set normalized keyword scores, so `--min-score` keeps working after
degradation. Semantic mode never degrades: it errors when the provider is
unavailable.

`-f json` and `-f compact` emit a single machine-parseable document on stdout;
scope notes and `--explain` diagnostics are written to stderr so they never
corrupt the JSON. `-f json-envelope` emits an object carrying index state
alongside the hits so a consumer can distinguish "never indexed" from "indexed
but nothing matched":

```json
{ "schema_version": 1, "index": { "indexed": true, "fresh": false, "chunks": 2126 }, "hits": [ ... ] }
```

Examples:

```bash
vecgrep search "database connection pooling"
vecgrep search --mode=semantic "error handling patterns"
vecgrep search --mode=keyword "SELECT FROM users"
vecgrep search --explain "authentication middleware"
vecgrep search "test helpers" --file="**/*_test.go"
vecgrep search "handlers" --types=function,method
vecgrep search "API endpoints" --format=json
vecgrep search "config loading" --min-score=0.3 -f json
vecgrep search "auth" --scope-files internal/auth/auth.go -f json
vecgrep search "auth" -f json-envelope
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
vecgrep similar --text "func handleError(err error)" --min-score=0.25 -f json
```

`similar` also supports `--min-score` and the same `-f` formats as `search`
(the `json-envelope` index block reflects the whole project, not the similar
target's scope). `similar` scores are cosine similarities (0-1).

## Status and Maintenance

```bash
vecgrep status
vecgrep status --format json
vecgrep status --lightweight --format json
vecgrep delete internal/old_file.go
vecgrep clean
vecgrep reset --force
```

`status --format json` includes a `freshness` proof and a `profile_status`
field. `fresh` means raw source hashes match, the latest ingestion receipt
completed application postflight, and any structural snapshot still matches
codemap's lightweight manifest. `stale` is confirmed drift; `unknown` is
deliberately fail-closed evidence (for example a legacy index without raw
hashes, an interrupted delete, a path-scoped indexing attempt, or a manifest
mismatch). Run `vecgrep index --full` to rebuild trusted metadata when
freshness is unknown; from MCP, call `vecgrep_index` with `force:true`.

`status --lightweight` uses a vector-free health manifest written after a
complete index. It compares persisted source hashes with the current
filesystem and can report files, chunks, pending changes, and freshness
without loading the VecLite/HNSW snapshot. Missing or invalid health metadata
reports `freshness.state: "unknown"`; use the full status command when you
need detailed vector/provider diagnostics.

### `profile_status` values

The `profile_status` field tells a consumer whether the embedding profile
matches the current configuration, so it can decide whether to reindex
before searching:

| Value | Meaning | Consumer action |
|------|---------|----------------|
| `ok` | Profile matches; index is ready | Proceed with search |
| `missing` | Index has chunks but no `embedding_profile.json` | Run `vecgrep index --full` |
| `mismatch` | Stored profile ≠ current config (provider/model/dimensions/distance changed) | Run `vecgrep index --full` to rebuild with the new profile |
| `not written yet` | No chunks and no profile (fresh `init`, nothing indexed) | Run `vecgrep index` |
| `<error>` | Profile read/parse failed | Check the error string; likely a corrupt profile file — `vecgrep reset --force` then `vecgrep index` |

The `freshness.state` field (separate from `profile_status`) reports source
drift: `fresh` (hashes match), `stale` (files changed/added/removed),
`unknown` (fail-closed). A project can be `profile_status: "ok"` but
`freshness.state: "stale"` — search works but results may reference outdated
code.

### Readiness via MCP

The MCP `vecgrep_ensure` tool provides a higher-level readiness enum
(`empty`, `stale`, `profile_mismatch`, `unknown`, `ready`) that combines
`profile_status` + `freshness` into a single consumer-friendly signal. The
CLI `status --format json` exposes the raw fields; MCP consumers should
prefer `vecgrep_ensure` or `vecgrep_status` for the combined signal.

## Memory

```bash
vecgrep memory recall <query> [--tags a,b] [--min-importance 0.5] [-f json]
vecgrep memory remember <content> [--tags a,b] [--importance 0.7] [--ttl-hours 24]
```

`recall` is semantic and scoped by tags (AND semantics: a memory must carry
every requested tag). `--format json` emits a JSON array of
`{id,content,importance,tags,score}`.

### C5 JSON contract

`memory recall --format json` emits a stable JSON array (never `null`):

```json
[
  {
    "id": "42",
    "content": "node api had a memory leak in handleRequest at src/api.js:120",
    "importance": 0.8,
    "tags": ["monitor", "incident", "rss_growth"],
    "score": 0.87
  }
]
```

| Field | Type | Description |
|------|------|-------------|
| `id` | string | Unique memory ID (uint64 emitted as a string) |
| `content` | string | The stored memory text |
| `importance` | float64 | Caller-supplied weight (0.0–1.0, default 0.5) |
| `tags` | []string | Tags attached at `remember` time |
| `score` | float32 | Cosine similarity to the query (0–1) |

Empty recall emits `[]` on stdout with exit 0 — "ran, no matches."

### Exit codes

| Code | Meaning | stdout | stderr |
|------|---------|--------|--------|
| 0 | Success (array on stdout, `[]` when no matches) | JSON array | empty |
| 3 | Embedding provider unreachable | empty | `{"error":"provider_unavailable"}` |
| 1 | Generic cobra error | empty | error message |

A consumer can distinguish "recall unavailable" (exit 3, no stdout) from
"recall ran, no matches" (exit 0, `[]` on stdout).

### Tag convention for sibling tools

Tags use AND semantics: a memory matches only if it carries **every**
requested tag exactly. The recommended convention is
`--tags <tool>,<scope>[,extra...]`:

| Tool | Example tags |
|------|-------------|
| Monitor | `monitor,incident,<rule>` (e.g. `monitor,incident,rss_growth`) |
| Codemap | `codemap,<project_key>` (see G2 memory governance) |
| Cairntrace | `cairntrace,<run_id>` |

This scoping prevents cross-project leakage: `recall --tags monitor,incident`
returns only Monitor incidents, never codemap-scoped memories.

## Indexing non-code text (incident bundles, logs)

vecgrep is code-corpus-first (language-aware chunker via codemap structural
chunks), but the generic chunker automatically routes any text file (`.md`,
`.txt`, `.log`, `.json`) through line-based chunking with sliding-window
overlap. This means you can index **incident bundles or log excerpts** as a
separate searchable project today:

```bash
# Export incident bundles to a directory (e.g. from fcheap stash restore)
mkdir -p ~/incidents && cd ~/incidents
vecgrep init
vecgrep index .

# Search across incidents by meaning, not exact tag
vecgrep search "node memory leak gc pressure" -m hybrid -f json
```

Each incident's `manifest.json`, `correlations.json`, and `semantic.json`
are text files that the generic chunker indexes as `ChunkTypeGeneric` chunks.
Search results include file paths and line ranges, so the consumer can map
a hit back to the originating incident bundle.

### Limitations and future work

- **No `--project` CLI flag**: the project is auto-detected from `cwd`. To
  search incidents and code in one query, index both under the same project
  root, or use MCP `vecgrep_batch_search` across projects.
- **No fcheap stash-id metadata on chunks**: chunks carry file path + line
  range only. To map a hit back to an fcheap stash, encode the stash ID in
  the indexed file path or content (e.g. name files `<stash-id>-manifest.json`).
- **No `--kind` flag**: all text is treated the same way. A future
  `--kind text|code|incident` flag could enable modality-aware ranking.
## Shell Completion

```bash
vecgrep completion bash > /etc/bash_completion.d/vecgrep
vecgrep completion zsh > "${fpath[1]}/_vecgrep"
vecgrep completion fish > ~/.config/fish/completions/vecgrep.fish
```
