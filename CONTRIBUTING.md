# Contributing to vecgrep

Thank you for your interest in contributing to vecgrep! This document provides guidelines and instructions for contributing.

## Code of Conduct

Be respectful and constructive in all interactions. We're building this together.

## How to Contribute

### Reporting Bugs

1. Check existing [GitHub Issues](https://github.com/abdul-hamid-achik/vecgrep/issues) to avoid duplicates
2. Create a new issue with:
   - Clear, descriptive title
   - Steps to reproduce
   - Expected vs actual behavior
   - Environment details (OS, Go version, Ollama version)
   - Relevant logs or error messages

### Suggesting Features

1. Open a [GitHub Issue](https://github.com/abdul-hamid-achik/vecgrep/issues/new) with the `enhancement` label
2. Describe the use case and why this would be valuable
3. Include any implementation ideas if you have them

### Pull Requests

1. Fork the repository
2. Create a feature branch from `main`
3. Make your changes following the code style guidelines
4. Ensure all tests pass
5. Submit a pull request

## Development Setup

See [DEVELOPMENT.md](DEVELOPMENT.md) for detailed setup instructions.

Quick start:
```bash
# Clone your fork
git clone https://github.com/YOUR_USERNAME/vecgrep.git
cd vecgrep

# Check environment
task doctor

# Install dependencies
task setup

# Start developing
task dev
```

## Code Style

### Go Standards

- Run `go fmt` on all code
- Use `golangci-lint` for static analysis (config in `.golangci.yml`)
- Follow [Effective Go](https://golang.org/doc/effective_go) guidelines

### Conventions

- Error messages should be lowercase without trailing punctuation
- Use structured logging where appropriate
- Keep functions focused and testable
- Prefer explicit error handling over panics

### Linting

Before submitting, ensure your code passes all checks:

```bash
task check  # Runs fmt, lint, and test
```

## Testing

### Running Tests

```bash
task test        # Run all tests
task test:v      # Verbose output
task test:short  # Skip integration tests
task cov         # Generate coverage report
```

### Writing Tests

- Tests that require Ollama are skipped if it's not running
- Use table-driven tests where appropriate
- Mock external dependencies for unit tests
- Integration tests should be clearly marked

## Commit Messages

Follow conventional commit style:

```
type: short description

Optional longer description explaining the change.
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `refactor`: Code refactoring
- `test`: Adding or updating tests
- `chore`: Maintenance tasks

Examples:
```
feat: add OpenAI embedding provider support
fix: handle empty file content in chunker
docs: update MCP integration guide
refactor: extract vector backend interface
```

## Pull Request Process

1. **Before submitting:**
   - Run `task check` to verify everything passes
   - Update documentation if adding/changing features
   - Add tests for new functionality

2. **PR description should include:**
   - Summary of changes
   - Related issue number (if applicable)
   - Testing done
   - Breaking changes (if any)

3. **Review process:**
   - PRs require review before merging
   - Address review feedback promptly
   - Squash commits if requested

## Project Structure

```
cmd/vecgrep/       # CLI entry point
internal/
  config/          # Configuration loading
  db/              # Database layer (SQLite + sqlite-vec)
  embed/           # Embedding providers (Ollama, OpenAI)
  index/           # File indexer and chunker
  mcp/             # MCP server implementation
  search/          # Search implementation
  version/         # Version info
  web/             # Web server and templates
```

## Adding New Features

### Adding an Embedding Provider

1. Implement the `embed.Provider` interface in `internal/embed/`
2. Add configuration options to `internal/config/config.go`
3. Wire up in the provider factory function
4. Document in README.md

### Adding a Language Chunker

1. Add language detection in `internal/index/chunker.go`
2. Implement tree-sitter parsing for the language
3. Add tests with sample code

### Adding an MCP Tool

1. Define the tool in `internal/mcp/server_sdk.go`
2. Implement the handler
3. Update README.md MCP section

### Modifying Database Schema

1. Edit `internal/db/schema.sql` and `internal/db/queries.sql`
2. Run `task gen:sqlc` to regenerate code
3. Update any affected code using the generated types

## Code Generation

This project uses code generation. After modifying source files, run:

```bash
task gen          # Generate all code
task gen:sqlc     # Regenerate database code
task gen:templ    # Regenerate templates
task gen:css      # Rebuild Tailwind CSS
```

## Questions?

Open an issue or start a discussion on GitHub.

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
