# vecgrep ⇄ codemap — Bidirectional Intelligence Sharing

> **Status:** validated design (2026-06-24, v2). Authored from a deep cross-repo investigation +
> adversarial review against the *actual* code of both tools. Supersedes the v1 survey (proposals A–E).
> **One line:** trade graph accuracy for semantic recall over **CLI `--json`**, each tool an optional
> accelerator that degrades to its own local capability — never a hard dependency.

## ⚠️ The integration that exists today is silently dead

vecgrep already ships a `codemap` client (`internal/mcp/codemap_client.go`) that shells the `codemap`
binary. **Three independent code paths parse field names codemap never emits**, so each fails
`json.Unmarshal` (or cobra arg parsing) and falls back without a trace:

| vecgrep expects | codemap actually emits | Result |
|---|---|---|
| `ImpactResult{Callers []string, Tests []string, BlastRadius int}` | `{direct_callers []SymbolRef, tests []ImpactNode, blast_radius []ImpactNode}` | `RelatedFiles` collects nothing → always regex |
| `HotspotResult{Refs}` (`refs`) | `HotspotRef{in_degree, shared_name}` | `Refs` always 0 → structural rerank blend is **inert** |
| `annotate --symbol <s>` | symbol is **positional** (`args[0]`); no `--symbol` flag | cobra rejects → annotate is a **no-op** |

So the integration *looks* shipped but does nothing, and nothing tells you. **Fixing this is the work** —
the new ideas below are extensions of one correctly-wired loop. The root cause is the absence of a
committed contract + a cross-repo test; both are first-class deliverables here.

## Clean boundary (no overlap)

| Concern | Owner | The other side must NOT |
|---|---|---|
| Resolved call/type/test/import graph, blast radius, hotspots, orphans, path | **codemap** | re-derive it from import-regex when codemap answers |
| Durable, reindex-proof annotations pinned to a symbol/path | **codemap** | keep its own; vecgrep writes *into* it (`source='vecgrep'`) |
| Semantic recall over chunks (hybrid) | **vecgrep** | stand up a second siloed embed index |
| Cross-project agent memory (importance/tags/TTL, `~/.vecai/memory`) | **vecgrep** | store memories; codemap only `memory_recall`s |
| `(relative_path, start_line)` join key (= vecgrep `chunk_key`) | **shared** | join on bare `symbol_name` (collides on `Foo`, may be `''`) |

Both embed the same repo with `nomic-embed-text` 768/cosine, but the stores stay separate:
codemap's is **structure-anchored** (node bodies + FQN), vecgrep's is **recall-anchored** (chunks,
cross-repo). They reconcile through the shared key, never by merging stores.

## Channel: CLI `--json` federation (one hop, CLI-only)

CLI `--json` is the only channel that bridges *different* data, and it's already how vecgrep shells
codemap. **Invariant (write it down, enforce it):** *neither tool's MCP server may call the other's
MCP server; cross-tool calls go CLI-only and exactly one hop deep.* That single rule eliminates the
circular-call / re-entrancy class outright.

- **Shared veclite vector reuse — CUT** (see below): feasible but the riskiest item for the smallest,
  most conditional payoff; the greenlit shared-throttling daemon is the right place to dedupe embedding
  work, not a cross-store reader.
- **Daemon socket transport — deferred:** codemap's daemon has a unix control socket but no public RPC
  (`app` can't import the daemon pkg). Revisit only after the per-call CLI cost is measured.

## Flows — KEEP

### F1 — `codemap related-files <file> --json`  ·  M · **first slice** · codemap builds, vecgrep repoints
One endpoint returning the ranked reverse-call + callee + covering-test file set for a file. **NEW**
`Service.RelatedFiles` (absent today), internally reusing `svc.Impact` + `heuristicTestCoverage` —
one process instead of vecgrep's N cold subprocess spawns (`symbols`→`impact` per symbol). vecgrep
replaces its `RelatedFiles` body with one exec parsing **C1 only**, and **deletes** the dead
`ImpactResult`/`HotspotResult` parse structs so they can't rot further. Highest value on TS/JS/Python,
where vecgrep's `strings.Contains` import heuristic is weakest and codemap resolves via LSP callHierarchy.

### F4 — `codemap symbol-at <file>:<line> --json` (+ `impact --at <file>:<line>`)  ·  M · **keystone** · codemap
The single biggest gap: codemap's outputs all carry `file+start_line+end_line`, but **no input accepts
a position** — "outputs richer than any input." Add `Store.NodeAtLine` (nodes-in-file → enclosing by
line range; absent today) + CLI/MCP plumbing. Unblocks F3 targeting, F5 scoping, and the whole
`file:line`-emitting ecosystem (EI.7/8/11). Do it immediately after the first slice.

### F3 — annotate fix (durable semantic pins)  ·  S · vecgrep, **gated behind F4**
Drop vecgrep's bogus `--symbol`; pass the symbol **positionally**, after resolving the hit's
`file:line` to the *correct* symbol via F4 (never annotate on a regex-extracted `symbol_name` that may
be `''` — it's a durable store). Closes the loop: semantic relevance becomes a reindex-proof,
symbol-pinned layer surfaced on every codemap query. **+ document the `source` enum** (EI.4).
**Rebind rule to specify first:** what happens to a `source='vecgrep'` annotation when codemap
reindexes/renames/moves its pinned node (`DeleteNodesInFile` mid-write).

### F2 — structural rerank, *actually* wired  ·  S · vecgrep
The rerank is **dead, not just imperfect**: `HotspotResult.Refs` (`refs`) ≠ codemap's `in_degree`, so
`hubScore` is uniformly 0 and the blend contributes nothing. Fix = rename `Refs`→`InDegree` (`in_degree`),
**plus** honor `shared_name>1` to down-weight name-inflated hubs, **plus** decide whether to score
blast-radius size or drop the unused parse. Fold the trivial field-rename into the first slice's golden
test (it catches this for free).

### G4 — registry / status cross-read  ·  S · both · *nice-to-have*
Filesystem stat of the peer's registry + `codemap status --json` (`{nodes,edges,vectors,stale}`, already
shelled). Each tool knows before delegating whether the peer can answer; vecgrep can reindex when
`codemap.stale` is non-zero. Cheap, symmetric, honest.

## G1 — semantic backfill — **SHIPPED** (codemap side, 2026-06-24)

Measurement settled the deferral: structure-only is the **norm** here (both codemap-indexed projects are
`vectors=0`; `graphite` is codemap-structure-only AND embedded in vecgrep), because codemap embedding is
slow and the user embeds in vecgrep. So codemap's `Service.Semantic`, in its `Mode="none"` (no local
embeddings) branch, now shells `vecgrep search <query> --format json` (config `vecgrep.enabled` default
true, `CODEMAP_VECGREP_BIN`/`$PATH`) and maps each chunk hit back onto the graph by `(relative_path,
start_line)` → `SemanticHit` carries codemap's FQN/kind/signature; `mode:"vecgrep"` marks provenance.
CLI-only one hop, degrades to the honest "no embeddings" note. (vecgrep already had `search --format
json` — a JSON array of `search.Result`.) Live-verified. **vecgrep does nothing here** — pure codemap-side.

## G2 — agent-memory governance — **SPEC (build next)**

vecgrep owns a **global** cross-project agent-memory store (`~/.vecai/memory/memory.veclite`;
`Memory{Content, Importance, Tags[], TTL, Score}`). codemap wants to surface relevant memories beside a
symbol in `codemap_context`/`codemap_impact` (Proposal E / EI.10). Because the store is global and tags
are free-form, naive recall (`tags:['codemap']`) would **leak memories across projects**. This is the
governance that prevents it.

**The scope key — codemap is the single authority (no independent re-derivation).**
- `<project_key>` = codemap's `git.RepoHash` (sha1 of the resolved absolute project root, first 12 hex —
  collision-resistant, stable per checkout, machine-local). codemap **exposes** it: `codemap status
  --json` → `project_key` and the `codemap_status` MCP tool (shipped 2026-06-24).
- **Rule:** nobody re-derives the key. Any tool/agent writing a codemap-scoped memory reads codemap's
  `project_key` and tags with that exact value — eliminating the "derive identically on both sides"
  failure mode. (A git-remote-derived key would be more portable across machines if the memory store is
  ever synced; out of scope for the local store.)

**Tag convention.**
- Write: `memory_remember(content, importance, tags=['codemap', <project_key>, …])`. The first two tags
  are mandatory for a codemap-scoped memory; extra tags (FQN, 'refactor', 'bug') are free.
- Read (codemap): `memory_recall(query=<FQN + nearby identifiers>, tags=['codemap', <project_key>],
  min_importance=0.3)`. BOTH tags required → only this project's codemap-scoped memories match.

**Read path (codemap build, after this spec).** `Service.Context`/`Service.Impact`, after assembling the
report, recall by the tags above and attach matches as **transient** entries (`source:"memory"`) under a
`memory` field — NOT codemap's durable symbol-pinned annotations (don't conflate the two). Gated on
`vecgrep.enabled`; needs a CLI recall path (`vecgrep memory recall --tags … --format json`, CLI-only one
hop — confirm/extend vecgrep's CLI).

**Safety — fails closed.** A wrong/absent `project_key` → recall returns nothing → no leakage, no false
memories. vecgrep drops expired (TTL) memories; codemap filters by `min_importance`. Memory is a shared
scratchpad surfaced by meaning, never authoritative.

**Build steps.** (1) codemap exposes `project_key` — **done**. (2) vecgrep memory recall CLI `--format
json` by tags (confirm/add). (3) codemap Context/Impact recall + attach. (4) document the convention in
`codemap docs` + vecgrep memory docs so agents tag correctly.

## Flows — CUT

- **G3 — shared veclite vector reuse.** Riskiest item, smallest conditional payoff (one `nomic`
  pass the shared throttling daemon already dedupes at the embedding layer). Profile-guard asymmetry,
  path-topology divergence (codemap one-DB-by-payload vs vecgrep one-DB-per-project-per-branch),
  node-body-vs-chunk granularity, HNSW-rebuild lock contention. Don't reintroduce until the daemon
  ships and there's a measured double-embed cost. (EI.15 embedding-profile key goes on the shelf with it.)
- **F5 — search-then-expand-blast-radius.** Speculative new vecgrep tool on F1+F4; no concrete workflow
  asks for it yet.
- **EI.14 — call graph → veclite KnowledgeGraph.** Dead-end; neither tool reads it.

## Data contracts (the committed shapes)

```jsonc
// C1 — codemap related-files <file> --json  (F1, the ONE stable contract vecgrep parses)
{ "project":"name", "file":"rel/path.go", "indexed":true,
  "related":[ {"relative_path":"x.go","reason":"caller|callee|test|import","confidence":0.0} ] }

// C2 — codemap symbol-at <file>:<line> --json  (F4)
{ "file":"x.go","line":42,"symbol":"Foo","fqn":"pkg.Foo","kind":"function",
  "start_line":40,"end_line":55,"resolution":"exact|enclosing|none" }

// C3 — annotate write (F3):  codemap annotate <symbol-positional> --source vecgrep --note <s> --data <json>
//      data is OPAQUE — vecgrep's finding JSON goes in verbatim.

// C4 — vecgrep search --json  (G1, NEW vecgrep path; emit the existing search.Result as JSON not Markdown)
{ "chunk_id":"…","relative_path":"x.go","symbol_name":"Foo","start_line":40,"end_line":55,
  "content":"…","score":0.0,"language":"go","chunk_type":"function" }
```

**Three typed peer states, never collapsed to nil:** `indexed:false` (peer ran, project not indexed)
vs a **non-zero exit** (real error) vs **empty result** (indexed, nothing matched). vecgrep must branch
on all three — today `cmd.Output()` error → `return nil,nil` collapses "absent", "crashed", and
"not found" into one indistinguishable nil, which makes provenance a lie.

**Provenance labeling (mandatory, both sides):** every degraded path sets a `note`/`source` marker
(e.g. `"codemap unavailable, used import-regex"`) so an agent can tell a graph answer from a heuristic
guess from a silent no-op.

**Conventions both must agree:** join key `(relative_path, start_line)`; annotation `source` enum
(`note`,`vecgrep`,`fcheap`,`vidtrace`,`cairntrace`,`glyphrun`,`mongosh`,`postgres`); (deferred) memory
tag `['codemap', <projectName>]`; (cut) embedding-profile key `provider:model:dims:distance:chunker`.

## Cross-repo golden contract test (prerequisite of the first slice)

The three silent no-ops are *exactly* what a cross-tool golden would have caught. Pin C1/C2/C4 as
committed JSON fixtures and assert, **in both repos' CI**, that the producer emits them and the consumer
parses them. Run real `codemap related-files|hotspots|impact --json` against a fixture project. Without
this in both pipelines, F1's "one stable contract" rots the same way the five-shape coupling did —
**version skew is the dominant failure mode** (vecgrep shells `codemap` by name; the current no-ops
*are* skew that already happened silently).

## Sequenced build order

**MINIMAL FIRST SLICE — make one contract real and prove no other path lies:**
1. codemap: `related-files <file> --json` → `Service.RelatedFiles` (reuse `svc.Impact`), emit C1 with explicit `indexed:bool` + the three typed states.
2. vecgrep: replace `RelatedFiles` body with one exec parsing C1; **delete** the dead `ImpactResult`/`HotspotResult` structs that path touched.
3. **Golden contract test in BOTH CIs** for `related-files`, `hotspots`, `impact` (this catches the `refs`→`in_degree` rerank no-op for free → fold F2's field-rename in here).
4. Provenance + typed `indexed/error/empty` on this one path, as the template every later flow copies.

**Then:** F4 (keystone) → F3 (annotate, after F4 + the rebind rule) + EI.4 → G4 + the rest of F2 →
*[defer]* G2 (after the tag convention) → *[defer]* G1 (after measuring the empty-embedding case).
**Never** widen to G1/G2 until the golden harness has caught at least one real skew in CI — that's the
proof the safety net works before the surface grows.

## Reconciliation with v1 (proposals A–E) + codemap EI.*

- **Keep, re-scoped:** A → F1 (shipped-but-dead, fix via one contract); B → F3 (the `--symbol` no-op);
  C → F2 (the `refs` no-op); E → G2 (defer); EI.3 → G4; EI.1 → F4 (the real prerequisite); EI.6 → F1.
- **Add:** the single stable `related-files` contract; the cross-tool golden test; CLI-only one-hop
  invariant; provenance labeling; three typed peer states.
- **Drop:** EI.14 (KnowledgeGraph). **Cut:** G3, F5. **Shelve:** EI.15/EI.16 with G3.
