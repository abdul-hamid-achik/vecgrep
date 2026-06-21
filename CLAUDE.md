# CLAUDE.md — Working notes for Claude on vecgrep

> **Source of truth:** [`AGENTS.md`](./AGENTS.md) holds the full architecture,
> conventions, and task recipes. Read it first. This file adds Claude-specific
> orientation and the few things easy to get wrong.

## What vecgrep is

A local-first semantic code search tool powered by vector embeddings. It indexes a
codebase and searches it with natural language via vector embeddings, defaulting to
local Ollama with optional cloud providers (OpenAI, Cohere, Voyage). Built on
**VecLite** (`~/projects/veclite`) as its embedded vector-search database.

Surfaces:
- `vecgrep` CLI (Cobra) — `cmd/vecgrep/main.go`
- `vecgrep studio` — canonical Bubble Tea v2 terminal app (`vecgrep browse` is the alias)
- MCP server — `internal/mcp/server_sdk.go` (used by the vidtrace bug-finding loop)

## Two documentation surfaces — do not mix them

This is the easiest thing to get wrong.

1. **`docs/` is the VitePress website (deployed to Vercel).** It holds user-facing
   product documentation only: guides, MCP reference, provider config, the VecLite
   integration contract (`docs/veclite-integration.md`). Build with `task site` /
   `task site:build` / `task site:preview`. **Never drop scratch notes, session
   handoffs, TODO dumps, or agent working memory into `docs/` (or anywhere in the
   repo).**

2. **`~/notes` is the Obsidian vault.** All working notes, session handoffs, release
   notes, design decisions, TODO tracking, and agent memory live here. The vecgrep
   project folder is `~/notes/projects/vecgrep/` (with sibling folders for
   `~/notes/projects/veclite/` and `~/notes/projects/vidtrace/`).

When you need to make a note, **use the `obsidian-cli` skill**: invoke the `skill`
tool with name `obsidian-cli` and follow its instructions to read/write/search the
vault. Do not hand-write markdown note files into the repo.

## Easy to get wrong

- **`docs/` is not a notes folder.** It is a deployed VitePress site. Product docs
  only. Notes go in the Obsidian vault via the `obsidian-cli` skill.
- **The embedding profile is collection metadata, not a sidecar.** As of the
  VecLite v0.17.0 bump, vecgrep stores the `EmbeddingProfile` in VecLite collection
  metadata (`internal/db/veclite_backend.go`). Legacy `embedding_profile.json`
  sidecars are migrated transparently on first open (read → write to metadata →
  delete sidecar). Do not reintroduce the sidecar.
- **HNSW config is wired end-to-end.** `VECGREP_VECTOR_VECLITE_M`,
  `VECGREP_VECTOR_VECLITE_EF_CONSTRUCTION`, and `VECGREP_VECTOR_VECLITE_EF_SEARCH`
  env vars resolve in `internal/config/resolution.go`, flow through `OpenOptions`,
  and reach VecLite's `WithHNSWConfig` (collection creation) and `WithEfSearch`
  (per-query). Do not re-add hardcoded `WithHNSW(16, 200)` call sites.
- **The `DeleteAll`-on-empty workaround is intentional.** `DeleteByProjectRoot`
  (`internal/db/veclite_backend.go`) still drops and recreates the collection after
  deleting all records because a VecLite HNSW-corruption-on-delete-all bug persists
  through v0.17.0. Re-test before removing it.
- **`vecgrep clean` is sync-and-report, not vacuum.** With pure VecLite storage
  there are no orphans to remove. Do not advertise it as "remove orphaned data and
  optimize."
- **`findImportedBy` for Go uses `go/parser`** (`internal/mcp/overview_tools.go`),
  not substring matching. JS/TS/Python still fall back to substring matching. This
  matters for the vidtrace bug-finding loop — keep the Go path accurate.

## Validate your work

```bash
task check                     # fmt + lint + test
go test -race ./...            # race detector across critical packages
task build                     # binary to ./bin/vecgrep
task flows                     # Glyphrun specs in specs/flows/*.yml
```

Run `task check` before every commit. Tests that require Ollama are skipped if it
is not running.

## Related projects

- VecLite (vector DB): `~/projects/veclite` — `[[../veclite/index|VecLite]]` in the vault
- vidtrace (video bug evidence → vecgrep handoff): `~/projects/vidtrace` — `[[../vidtrace/index|vidtrace]]`