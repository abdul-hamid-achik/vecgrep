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
vecgrep delete internal/old_file.go
vecgrep clean
vecgrep reset --force
```

`status --format json` includes a `freshness` proof. `fresh` means raw source
hashes match, the latest ingestion receipt completed application postflight,
and any structural snapshot still matches codemap's lightweight manifest.
`stale` is confirmed drift; `unknown` is deliberately fail-closed evidence
(for example a legacy index without raw hashes, an interrupted delete, a
path-scoped indexing attempt, or a manifest mismatch). Run
`vecgrep index --full` to rebuild trusted metadata when freshness is unknown;
from MCP, call `vecgrep_index` with `force:true`.

## Memory

```bash
vecgrep memory recall <query> [--tags a,b] [--min-importance 0.5] [-f json]
vecgrep memory remember <content> [--tags a,b] [--importance 0.7] [--ttl-hours 24]
```

`recall` is semantic and scoped by tags (AND semantics: a memory must carry
every requested tag). `--format json` emits a JSON array of
`{id,content,importance,tags,score}`.

When the embedding provider is unreachable, `recall --format json` keeps
stdout empty and emits `{"error":"provider_unavailable"}` to stderr with
exit code `3` — so a consumer can distinguish "recall unavailable" from
"recall ran, no matches" (the latter is a normal `[]` on stdout with exit 0).

## Shell Completion

```bash
vecgrep completion bash > /etc/bash_completion.d/vecgrep
vecgrep completion zsh > "${fpath[1]}/_vecgrep"
vecgrep completion fish > ~/.config/fish/completions/vecgrep.fish
```
