# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed
- **BREAKING**: Migrated to pure veclite storage - all data (files, chunks, metadata) now stored in veclite
- Removed SQLite entirely - no CGO required for any operations
- Simplified database architecture - single data store instead of SQLite + veclite hybrid
- Cross-compilation now works for all platforms (linux/darwin/windows on amd64/arm64)
- Simplified release workflow - single GoReleaser job for all platforms

### Removed
- SQLite dependency and all related code
- sqlite-vec backend (replaced by pure veclite)
- sqlc code generation (no SQL needed)
- CGO requirement from build process

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
- Comprehensive DEVELOPMENT.md with architecture documentation

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

[0.3.1]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.2.10...v0.3.0
[0.2.10]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.2.0...v0.2.10
[0.2.0]: https://github.com/abdul-hamid-achik/vecgrep/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/abdul-hamid-achik/vecgrep/releases/tag/v0.1.0
