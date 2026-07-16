# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.19.1] - 2026-07-16

### Fixed

- **Daemon liveness probe no longer hangs on a wedged socket.** `IsRunning`
  dialed the daemon socket with no deadline, so a wedged daemon (or a foreign
  process squatting on the path) blocked interactive callers — studio startup
  and test runs — indefinitely. The whole probe (dial + ping + response) is
  now bounded at 2 seconds and reports not-running on expiry.
- **`go install ...@latest` no longer resolves to the year-old v0.31.0
  snapshot.** All stale 0.x/1.x tags are retracted in go.mod (carried by a
  v1.4.0 tombstone tag), so Go tooling falls back to the current default
  branch; Homebrew remains the supported channel for versioned builds.

## [2.19.0] - 2026-07-16

### Fixed
- Keyword-mode scores (including hybrid searches degraded to keyword-only by an unavailable
  embedder) are now BM25 normalized to 0–1 within each result set (top hit = 1.0), mirroring the
  `bm25/maxBM25` normalization hybrid fusion already applied, so `min_score` works in keyword
  mode instead of silently filtering nothing. The raw BM25 value is still reported as `distance`.
- Honor `search.text_weight`: hybrid search previously derived the keyword weight as
  `1 - vector_weight` and silently ignored the configured text weight. An unset (zero) text
  weight keeps the historical derivation; explicit weights that don't sum to 1 are normalized by
  their sum so fused scores stay a calibrated 0–1 value. The daemon worker and MCP handlers now
  pass the project's configured `vector_weight`/`text_weight` too, instead of always searching
  with the built-in defaults.

## [2.18.0] - 2026-07-15

### Fixed
- **Hybrid-mode scores were raw Reciprocal Rank Fusion values, not similarities.** With RRF k=60
  and 0.7/0.3 weights, the maximum reportable score was ~0.0164, so every hybrid result showed
  0.01–0.02, tiny import-only chunks that top BM25 length normalization outranked substantive
  code, and `min_score` above 0.02 silently wiped all results. Hybrid search now runs vector and
  BM25 retrieval separately (3× over-fetch per modality) and fuses in vecgrep with calibrated
  weighted score fusion: `0.7·clamp01(cosine) + 0.3·(bm25/maxBM25)·substanceFactor`, where chunks
  under 200 chars have their keyword contribution damped to a 0.3 floor. Scores are now
  discriminative (real-index spread 0.45–0.69 on near-exact matches) and `min_score` works.
- Embedder failures in hybrid mode now degrade to keyword-only search **with an explicit
  warning** carrying the provider error, rendered on every surface — CLI (stderr for machine
  formats), MCP handlers, daemon RPC, and the CLI daemon fast-path, which previously dropped
  warnings entirely. Strict `Search` still errors; semantic mode never silently degrades.

### Changed
- Hybrid fusion ownership moved from veclite to vecgrep; `docs/veclite-integration.md` documents
  the contract and rationale. `Score` semantics are documented in `internal/search`.

## [2.17.0] - 2026-07-13

### Added
- Trusted structural ingestion and freshness tracking for codemap-provided symbol chunks.
- `vecgrep projects prune` — clean stale entries from the global project registry.
- Homebrew install method documented on the docs landing page, quick-start, and README.

### Changed
- Adopt veclite v0.24.0 and enable the write-ahead log for writers.
- Optimize indexing and embedding workflows.
- Expand the embedding relevance corpus to 78 documents / 66 queries.
- Overhaul the docs landing page and add SEO infrastructure.

### Fixed
- Make Ollama truncation warnings truthful.

## [2.16.0] - 2026-07-09

### Added
- Memory ranking intelligence and search affordances.

### Changed
- Bump veclite dependency to v0.23.0.

## [2.15.0] - 2026-07-06

### Added
- Machine-consumer contracts for search and memory recall.

## [2.14.1] - 2026-06-30

### Fixed
- Resolve codemap from the project's fully resolved config in `NewSDKServer` when a project root is known up front. Previously `s.codemap` was built from the zero-value `SDKServerConfig.Codemap` (always `nil`), so `vecgrep serve` reported "Codemap integration: enabled / Status: codemap binary not found" regardless of whether codemap was installed, silently disabling structural re-ranking and impact-based search scoping. The up-front path now mirrors `activateProject`'s existing re-resolution.
- Add `config.ResolveBinary` and route codemap binary lookup through it (instead of a bare `exec.LookPath`) in `codemapDetect` and `NewCodemapClient`. Falls back to common install directories (`/opt/homebrew/bin`, `$HOME/go/bin`, etc.) when `$PATH` is a minimal subprocess PATH, and stores the resolved absolute path so subsequent `exec.Command` calls don't re-fail a `$PATH` lookup.

## [2.14.0] - 2026-06-29

### Added
- Studio read-only fallback with daemon-delegated reindex.
- `vecgrep index` delegates to a running daemon hub instead of opening a second write handle.

### Changed
- Bump veclite dependency to v0.22.0 (lock-free read-only opens).
- `go fmt` client/Studio files so GoReleaser no longer fails on a dirty tree.

## [2.13.0] - 2026-06-28

### Added
- Multi-project daemon hub — one socket with lazy per-project workers.

## [2.12.0] - 2026-06-27

### Fixed
- Don't treat the global `~/.vecgrep` directory as a project-root marker.

## [2.11.0] - 2026-06-27

### Changed
- Pipelined indexing with all-or-nothing per-file inserts and a live progress bar.

## [2.10.2] - 2026-06-27

### Fixed
- Parse codemap v0.17.0 impact output and auto-enable codemap when it is installed.

## [2.10.1] - 2026-06-27

### Fixed
- Resolve MCP/daemon index lock contention and concurrency races.

## [2.10.0] - 2026-06-26

### Added
- Dedicated tests for `mcpSession` and `daemonClient`.

### Changed
- Lock-free MCP initialization with a lazy dual-handle session.

## [2.9.0] - 2026-06-25

### Added
- codemap blast-radius investigation flow for scoped search.

## [2.8.0] - 2026-06-25

### Added
- fcheap integration, plus indexing speed, UX, and DX improvements.

### Fixed
- Data race in the `EmbedBatchFallsBackOn404` test.

## [2.7.1] - 2026-06-24

### Fixed
- Clear the line on verbose index progress so leftover characters no longer linger.

## [2.7.0] - 2026-06-24

### Added
- Ollama native batch embedding endpoint for 8–16× faster indexing.
- `vecgrep memory recall` / `vecgrep memory remember` CLI (C5 contract with exact tag-AND matching).
- codemap integration milestones wired against the real CLI contract: annotate targeting (F3), blast-radius decision (F2), and status cross-read (G4).

## [2.6.0] - 2026-06-24

### Added
- codemap integration, per-branch index switching, and a background daemon.

### Fixed
- Data race in throttle dedup; the test mockProvider is now thread-safe.

## [2.5.0] - 2026-06-24

### Changed
- Gradient progress bar for the `vecgrep index` CLI.

## [2.4.0] - 2026-06-23

### Added
- Live progress bar for the `vecgrep index` CLI.

## [2.3.1] - 2026-06-23

### Fixed
- `vecgrep reset --force` handles a locked database gracefully.

## [2.3.0] - 2026-06-23

### Added
- Use veclite `WithSharedRead` so CLI reads work while Studio holds the database open.

## [2.2.0] - 2026-06-21

### Added
- Expose resolved HNSW parameters (M, efConstruction, efSearch) and veclite version in `vecgrep status` and Studio status view.
- Expose default HNSW constants (`config.DefaultVecLiteM`, `DefaultVecLiteEfConstruction`, `DefaultVecLiteEfSearch`) so callers can distinguish user-tuned values from defaults.
- Go-aware reverse-import lookup in the `vecgrep_related_files` MCP tool via `go/parser`, reducing false positives for same-package substring matches. Non-Go files keep the existing substring fallback.
- Migration path from the legacy `embedding_profile.json` sidecar into veclite collection metadata (transparent on first open).
- Provider health probe surfaced in `vecgrep status` and Studio status via `Provider.Ping`.
- `CLAUDE.md` with Claude-specific orientation and documentation discipline (docs/ vs ~/notes vault).

### Changed
- Bump veclite dependency from v0.14.0 to v0.17.0 (storage format v4, first-class `EmbeddingProfile`, named vector spaces, per-space index cleanup on delete, `UpsertRecordByKey` and `HybridSearchSpace` APIs, errcheck lint clean).
- Honor `vector.veclite.m`, `vector.veclite.ef_construction`, and `vector.veclite.ef_search` from config instead of silently hardcoding `WithHNSW(16, 200)`. `ef_search` is applied as a per-query `veclite.WithEfSearch` search option.
- Store the embedding profile in veclite collection metadata instead of the `embedding_profile.json` sidecar. Existing sidecars are migrated transparently on first open and the sidecar file is removed.
- Push `GetFileHashes`, `GetStats`, `ListFiles`, and `DeleteOrphaned` down to native veclite filters instead of full-table scans with manual Go loops. Largest incremental-index startup win.
- Deduplicate `SearchWithExplain`: use the results returned by `SearchExplain` instead of running a second `Search` call.
- Replace the O(n²) bubble sort in the embedding cache eviction with `slices.SortFunc` (O(n log n)).
- Reframe `vecgrep clean` as "sync database to disk and report index stats" — the previous "remove orphaned data and optimize" framing was misleading because pure veclite storage has no orphans.
- Document the split between `docs/` (VitePress website deployed to Vercel) and the `~/notes` Obsidian vault for session notes, handoffs, and agent memory in `AGENTS.md`.

### Fixed
- HNSW config parsed in the config layer is now actually applied to the veclite collection at creation and to every search call site. Previously the config was collected and tested but never reached the backend.
- Resolve `VECGREP_VECTOR_VECLITE_M`, `VECGREP_VECTOR_VECLITE_EF_CONSTRUCTION`, and `VECGREP_VECTOR_VECLITE_EF_SEARCH` environment variables in `internal/config/resolution.go` so they flow into the resolved config and reach the backend.
- Documented the retained `DeleteAll`-on-empty workaround in `DeleteByProjectRoot`: veclite v0.17.0 still exhibits empty-collection entry-point corruption (`hnsw.go` index out of range) when re-indexing after a delete-all under concurrent workers with 384-dim embeddings, so the workaround stays.

## [2.1.0] - 2026-06-20

### Added
- VitePress documentation site powered by Bun, with `task site`, `task site:build`, and `task site:preview`.
- OpenAI, Cohere, and Voyage embedding provider support alongside Ollama.
- Query/document embedding role support for providers that distinguish retrieval modes.
- Embedding profile guard that detects provider, model, dimensions, distance, modality, and chunker drift before incremental indexing or vector search.
- Studio flows for first-run project registration, browse alias launch, cloud provider config, and removed command behavior.

### Changed
- Store generated project data under `~/.vecgrep/projects/<project>` by default instead of creating repo-local `.vecgrep/` directories.
- Make `vecgrep studio` the canonical Bubble Tea terminal workspace command and keep `vecgrep browse` as the alias.
- Use Bun for docs dependencies and scripts instead of npm lockfile workflows.
- Route document indexing through document embeddings and semantic search through query embeddings when the provider supports it.
- Polish Studio status, filtering, search, indexing, and first-run behavior.

### Removed
- Removed the old `vecgrep tui` command surface.
- Removed stale profile/source code paths that were not wired into the current product.
- Removed `package-lock.json` from the docs site setup.

## [2.0.1] - 2026-06-15

### Fixed
- Enable CGO for `task race` so the Go race detector works in CI.

## [2.0.0] - 2026-06-15

### Changed
- **BREAKING**: Migrated to pure veclite storage - all data (files, chunks, metadata) now stored in veclite
- **BREAKING**: Replaced the HTMX web interface with vecgrep Studio (`vecgrep studio`)
- Removed SQLite entirely - no CGO required for any operations
- Simplified database architecture - single data store instead of SQLite + veclite hybrid
- Shared CLI and Studio behavior now routes through an internal application service layer
- Cross-compilation now works for all platforms (linux/darwin/windows on amd64/arm64)
- Simplified release workflow - single GoReleaser job for all platforms

### Added
- Interactive Studio terminal app for search, preview, indexing, status, config inspection, similar-code lookup, and index maintenance

### Removed
- SQLite dependency and all related code
- sqlite-vec backend (replaced by pure veclite)
- sqlc code generation (no SQL needed)
- CGO requirement from build process
- Web UI, templ/Tailwind/npm build tooling, and web server Docker/CI steps

### Migration
- **Existing users must re-index after upgrading:**
  ```bash
  vecgrep reset --force
  vecgrep index
  ```
  The database format has changed and old indexes are not compatible.

## [0.3.1] - 2025-01-21

### Added
- Hierarchical configuration system with multiple config sources
- Global project registry for managing multiple projects
- VecLite backend with HNSW indexing
- Database migration support for schema upgrades
- CHANGELOG.md with version history
- CONTRIBUTING.md with contribution guidelines
- Comprehensive `docs/development.md` with architecture documentation

### Changed
- Configuration now resolves from multiple sources in priority order:
  1. Environment variables (VECGREP_*)
  2. Project root vecgrep.yaml
  3. Project .config/vecgrep.yaml
  4. Project .vecgrep/config.yaml (legacy)
  5. Global project entry
  6. Global defaults
  7. Built-in defaults

## [0.3.0] - 2025-01-20

### Added
- OpenAI embedding provider support (`text-embedding-3-small`, `text-embedding-3-large`)
- Similar code finder (`vecgrep similar`) - find semantically similar code by chunk ID, file:line, or text snippet
- `vecgrep delete` - remove specific files from the index
- `vecgrep reset` - clear the entire project database
- `vecgrep clean` - remove orphaned data and optimize database
- Web interface for similar code search

### Changed
- Updated MCP protocol version to 2025-06-18

## [0.2.0] - 2025-01-15

### Added
- Auto-detection for vecgrep projects without requiring manual init
- Official MCP Go SDK integration
- `vecgrep_init` MCP tool for initializing projects from AI assistants
- Graceful handling of uninitialized directories in MCP mode
- Claude Code CLI installation instructions

### Changed
- Switched from custom MCP implementation to official Go SDK
- MCP tools now work with global user scope configuration

### Fixed
- MCP capabilities declaration
- Version ldflags in Docker builds
- Removed stderr output in MCP mode to prevent protocol interference

## [0.1.0] - 2025-01-10

### Added
- Initial release with semantic code search
- Ollama embedding provider (nomic-embed-text model)
- SQLite database with sqlite-vec extension for vector storage
- Incremental indexing with file hash tracking
- Language-aware code chunking (Go, Python, JavaScript, TypeScript, Rust, and more)
- MCP server integration for AI assistant connectivity
- Web interface with HTMX and syntax highlighting
- Docker support with host Ollama connection
- Shell completion scripts (bash, zsh, fish)

### Features
- Semantic search using vector embeddings
- Local-first architecture - code never leaves your machine
- Multiple output formats (default, json, compact)
- Filter by language, chunk type, and file pattern
- Configurable chunk size and overlap
- Ignore patterns for excluding files from indexing

[2.18.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.17.0...v2.18.0
[2.17.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.16.0...v2.17.0
[2.16.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.15.0...v2.16.0
[2.15.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.14.1...v2.15.0
[2.14.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.14.0...v2.14.1
[2.14.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.13.0...v2.14.0
[2.13.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.12.0...v2.13.0
[2.12.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.11.0...v2.12.0
[2.11.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.10.2...v2.11.0
[2.10.2]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.10.1...v2.10.2
[2.10.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.10.0...v2.10.1
[2.10.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.9.0...v2.10.0
[2.9.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.8.0...v2.9.0
[2.8.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.7.1...v2.8.0
[2.7.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.7.0...v2.7.1
[2.7.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.6.0...v2.7.0
[2.6.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.5.0...v2.6.0
[2.5.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.4.0...v2.5.0
[2.4.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.3.1...v2.4.0
[2.3.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.3.0...v2.3.1
[2.3.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.2.0...v2.3.0
[2.2.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.0.1...v2.1.0
[2.0.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.3.1...v2.0.0
[0.3.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.2.10...v0.3.0
[0.2.10]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.2.0...v0.2.10
[0.2.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/abdul-hamid-achik/vecgrep/releases/tag/v0.1.0
