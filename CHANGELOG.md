# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

- Resolve codemap from the project's fully resolved config in `NewSDKServer` when a project root is known up front. Previously `s.codemap` was built from the zero-value `SDKServerConfig.Codemap` (always `nil`), so `vecgrep serve` reported "Codemap integration: enabled / Status: codemap binary not found" regardless of whether codemap was installed, silently disabling structural re-ranking and impact-based search scoping. The up-front path now mirrors `activateProject`'s existing re-resolution.
- Add `config.ResolveBinary` and route codemap binary lookup through it (instead of a bare `exec.LookPath`) in `codemapDetect` and `NewCodemapClient`. Falls back to common install directories (`/opt/homebrew/bin`, `$HOME/go/bin`, etc.) when `$PATH` is a minimal subprocess PATH, and stores the resolved absolute path so subsequent `exec.Command` calls don't re-fail a `$PATH` lookup.

### Changed
- Hybrid fusion ownership moved from veclite to vecgrep; `docs/veclite-integration.md` documents
  the contract and rationale. `Score` semantics are documented in `internal/search`.

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

[2.2.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.0.1...v2.1.0
[2.0.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.3.1...v2.0.0
[0.3.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.2.10...v0.3.0
[0.2.10]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.2.0...v0.2.10
[0.2.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/abdul-hamid-achik/vecgrep/releases/tag/v0.1.0
