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
recall-anchored (chunks, cross-repo). They reconcile through the shared join key —
`(relative_path, start_line)` — never by merging stores or joining on bare symbol
names (which collide).

## What's wired

- **Structural reranking** — `vecgrep_search` results are re-ranked by blending
  semantic similarity with codemap's fan-in hub score
  (`final = semantic × (1−w) + hub × w`, default weight 0.15). Reranked results
  show their structural score so agents can see *why* a hit ranked where it did.
- **Search-hit annotation** — top search hits are resolved to their enclosing
  symbol via `codemap symbol-at` and annotated, so vecgrep relevance signals
  survive codemap reindexes.
- **Blast-radius-scoped search** — `vecgrep_investigate` (and `symbol:` on
  `vecgrep_search`) calls `codemap impact` to compute a changed symbol's blast
  radius, then scopes the semantic search to that file allow-list.
- **Peer status** — `vecgrep_status` / `vecgrep_index` cross-read codemap's index
  freshness and hint when a reindex is needed.
- **Reverse direction** — codemap shells `vecgrep search --format json` as its
  semantic-search fallback and recalls vecgrep memories into its context reports.

## Invariants

- **CLI-only, one hop**: integration happens by shelling the peer binary with
  `--json`; neither tool links the other's packages or reads the other's store.
- **Best-effort**: if the peer binary is missing, stale, or errors, the caller
  falls back to its local capability and says so in a status note.
- Contract drift between the tools is guarded by golden tests
  (`internal/mcp/codemap_golden_test.go`).
