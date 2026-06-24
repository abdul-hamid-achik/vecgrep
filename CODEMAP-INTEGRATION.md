# vecgrep ⇄ codemap integration

> **Status:** design / proposed (2026-06-24). Authored from a codemap-side ecosystem survey.
> **One line:** trade graph accuracy for semantic recall on a shared veclite substrate — vecgrep
> finds *where* code is by meaning; codemap knows *who calls whom* and *what breaks*.

## Why these two compose
vecgrep and codemap are the closest architectural siblings: both are local-first pure-Go tools with
CLI + MCP + a Bubble Tea studio, both store vectors in **veclite** on the **same default embedding
space** (Ollama `nomic-embed-text`, 768-dim, cosine), both use the XDG project-registry pattern
(`~/.vecgrep/config.yaml` ↔ codemap's `$XDG_DATA_HOME/codemap`), both speak newline-delimited MCP
stdio, and both address symbols by `path + start_line` (vecgrep `chunk_key = relative_path:start_line`).
Because the vectors are wire-compatible when the `EmbeddingProfile` matches, each tool can read the
other's index read-only with **no re-embedding**.

**Boundary.** vecgrep owns rich *chunk-level* semantic/keyword/hybrid search (function/class/block
chunks, BM25, multi-provider embeddings, plus the cross-project agent memory store). codemap owns the
*structural graph* (real call edges via go/types + LSP callHierarchy, impact/blast-radius, hotspots,
orphans, call paths, test coverage) and a durable annotation layer keyed to symbols & call paths.

## Integrations

### A — Delegate `vecgrep_related_files` to codemap's real graph  ·  M · **high**  ·  (vecgrep implements)
`vecgrep_related_files` (`findImports`/`findImportedBy`/`findTestFiles`) is **pure import-text regex** —
it misses cross-package and dynamic calls and over-matches on filename convention. codemap already
computes the exact answer. Delegate the `imports`/`imported_by`/`tests` relationships to codemap MCP
(`codemap_impact`), aggregate the reverse-call graph + covering tests by enclosing file, and return
`[]{relative_path, reason:'calls X'|'covers X', confidence}`. **Graceful fallback** to the existing
heuristics when codemap reports `{indexed:false}`. New optional codemap client in
`internal/mcp/overview_tools.go` behind a config flag `codemap.enabled`. Upgrades vecgrep's weakest
feature to graph-accurate with zero new index.

### B — Pin semantic hits as codemap annotations  ·  S · **medium**  ·  (vecgrep implements)
A vecgrep search/similar result (`relative_path, start_line, symbol_name, score, query`) → resolve to a
codemap FQN via `codemap_find`/`codemap_symbols` (path+line → symbol) → `codemap_annotate{source:'vecgrep',
note:'matched query "<q>" @<score>', data:{query, chunk_key, score}}`. codemap annotations survive reindex
and surface in `codemap_context`/studio everywhere. Turns ephemeral search into graph-anchored memory.

### C — codemap re-ranks vecgrep hybrid results by structural importance  ·  M · medium  ·  (vecgrep implements)
After vecgrep computes hybrid scores, fetch `codemap_hotspots` (fan-in hub score) and/or `codemap_impact`
blast-radius size per hit and blend: `final = vecScore·w_sem + normalize(hubScore)·w_struct`. Down-weight
hits codemap flags as name-ambiguous (`shared_name>1`). The bug/feature you want is usually where semantic
similarity AND high fan-in coincide.

### D — codemap reuses vecgrep's chunk vectors for finer semantic recall  ·  L · medium  ·  (codemap implements)
codemap embeds whole node bodies (coarse); vecgrep chunks by function/class/block (fine). With a matching
`EmbeddingProfile`, codemap reads vecgrep's `chunks` collection (`~/.vecgrep/projects/<name>/vectors.veclite`)
**read-only**, queries it, and joins each chunk back to a graph node by `(relative_path,start_line)` range
overlap → `SymbolRef + chunk score`. Better recall on long bodies, no second Ollama pass. Gated on the
profile-compatibility check both tools expose. *(codemap EI.15.)*

### E — codemap reads vecgrep's global agent memory as a recall source  ·  S · medium  ·  (codemap implements)
vecgrep owns the global cross-project memory (`~/.vecai/memory/memory.veclite`; `Importance`/`Tags`/`TTL`)
codemap lacks. When `codemap_context`/`codemap_impact` builds a bundle, also call
`memory_recall(query=FQN+nearby identifiers, tags:['codemap'])` and attach matching memories as "related
notes" beside codemap's annotations. Convention to agree: tag codemap-relevant memories `['codemap']`.
*(codemap EI.10.)*

### Registry cross-read  ·  S · low  ·  (both)
`vecgrep_status` reports `has_graph` by stat-ing codemap's XDG registry; on `vecgrep index`, emit an "also
registered in codemap" hint. Symmetric to codemap EI.3.

## Config (vecgrep side)
`codemap.enabled` (bool), `codemap.bin` (path), `codemap.mcp_endpoint`. Every call degrades gracefully when
codemap is off `$PATH` or the project isn't indexed.

## Suggested build order
A (highest ROI, no codemap change) → B → C → E → D. A/B/C/E are vecgrep-side; D depends on the shared
`EmbeddingProfile` agreement (see `veclite/CODEMAP-INTEGRATION.md`).
