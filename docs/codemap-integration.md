# codemap Integration

vecgrep and [codemap](https://codemap.tools) share intelligence bidirectionally over
CLI `--json` — each tool is an optional accelerator that degrades to its own local
capability, never a hard dependency.

## Ownership boundary

| Concern | Owner | The other side must NOT |
|---|---|---|
| Resolved call/type/test/import graph, blast radius, hotspots | **codemap** | re-derive it from import-regex when codemap answers |
| Durable, reindex-proof annotations pinned to a symbol/path | **codemap** | keep its own; vecgrep writes *into* it (`source='vecgrep'`) |
| Semantic recall over chunks (hybrid vector + BM25) | **vecgrep** | stand up a second siloed embed index |
| Cross-project agent memory (importance/tags/TTL) | **vecgrep** | store memories; codemap only recalls |

Both tools embed the same repo (`nomic-embed-text`, 768d, cosine) but the stores stay
separate: codemap's is structure-anchored (node bodies + FQN), vecgrep's is
recall-anchored (chunks, cross-repo). Structural exports reconcile through the
durable join key `(project_key, relative_path, start_line, fqn, kind)` — never by
merging stores or joining on bare symbol names (which collide).

## What's wired

- **Structural reranking** — `vecgrep_search` results are re-ranked by blending
  semantic similarity with codemap's fan-in hub score
  (`final = semantic × (1−w) + hub × w`, default weight 0.15). Reranked results
  show their structural score so agents can see *why* a hit ranked where it did.
- **Structural indexing** — when codemap is enabled, `vecgrep index` consumes the
  paginated `codemap export-symbols --json` v1 contract. Each symbol's available
  docstring, signature, and source are embedded together, while search previews
  keep only clean source. Long symbols are split from the full validated file,
  and uncovered imports, globals, template/style blocks, and other gaps remain
  searchable as generic chunks. Stale, omitted, or invalid files fall back
  individually to vecgrep's built-in chunker; fresh files in the same export
  stay structural.
- **Search-hit annotation** — top search hits are resolved to their enclosing
  symbol via `codemap symbol-at` and annotated, so vecgrep relevance signals
  survive codemap reindexes.
- **Blast-radius-scoped search** — `vecgrep_investigate` (and `symbol:` on
  `vecgrep_search`) calls `codemap impact` to compute a changed symbol's blast
  radius, then scopes the semantic search to that file allow-list.
- **Peer status** — `vecgrep_status` / `vecgrep_index` cross-read codemap's index
  freshness and hint when a reindex is needed.
- **Ingestion receipt** — every configured indexer writes a small project-scoped
  `receipt.v1.json` under vecgrep's data directory. `vecgrep status --format json`
  exposes it as `ingestion_receipt`, including requested/effective mode, the
  codemap contract fingerprint when available, actual structural/gap/local
  counts, bounded fallback reasons, a unique `attempt_id`, `scope_complete`, and
  separate ingestion/postflight success. A new attempt invalidates the previous
  proof before index mutation; path-scoped runs intentionally remain incomplete
  until a full-project pass can certify the whole index.
  The receipt never shells out to codemap when read and never stores arbitrary
  provider or command error text.
- **Bounded freshness proof** — status surfaces compare the working tree with
  vecgrep's persisted raw-source hashes, verify the receipt's `last_success`,
  and, when structural chunks were consumed, call only
  `codemap structural-manifest --json`. The manifest is timeout/output bounded
  and must match schema v1, export schema v1, project key, fingerprint,
  completeness, and freshness. Status never downloads `export-symbols`; legacy,
  corrupt, unavailable, or mismatched evidence reports `freshness.state:
  unknown` until a successful `vecgrep index --full` rebuilds the proof. A durable
  project tombstone also forces `unknown` if a multi-collection delete/reset is
  interrupted, so retained hashes can never certify missing or ghost chunks.
- **Reverse direction** — codemap shells `vecgrep search --format json` as its
  semantic-search fallback and recalls vecgrep memories into its context reports.

## Invariants

- **CLI-only, one hop**: integration happens by shelling the peer binary with
  `--json`; neither tool links the other's packages or reads the other's store.
- **Best-effort**: if the peer binary is missing, stale, or errors, the caller
  falls back to its local capability and says so in a status note. Set
  `codemap.structural_chunks: required` (or pass
  `--structural-chunks required`) when CI should fail instead.
- Structural export pages must agree on schema, project key, and index
  fingerprint. Record paths are project-relative and slash-canonical on every
  OS, and each record carries a contiguous one-based `ordinal` so pagination
  remains verifiable even when long signature/docstring sort fields are
  truncated for transport. vecgrep validates the public contract and current
  source; it never reads codemap's SQLite database.
- Contract drift between the tools is guarded by golden tests
  (`internal/mcp/codemap_golden_test.go`).
