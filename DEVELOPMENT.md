# Development Guide

## Quick Start

```bash
# Check your environment
task doctor

# Setup everything
task setup

# Start developing
task dev
```

## Daily Workflow

```bash
task dev          # Hot reload development
task check        # Run before committing (fmt, lint, test)
task ship         # Full CI pipeline locally
```

## Debugging

```bash
task wtf          # What's broken?
task doctor       # Environment check
```

## Ollama

```bash
task ollama       # Start Ollama with Metal GPU support
```

Requires `nomic-embed-text` model:
```bash
ollama pull nomic-embed-text
```

## Useful Commands

```bash
task              # List all available tasks
task build        # Build the binary
task test         # Run tests
task gen          # Generate code (sqlc, templ, css)
task clean        # Remove build artifacts
```
