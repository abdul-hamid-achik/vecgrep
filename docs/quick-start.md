# Quick Start

Use this guide to install vecgrep, register a project, index it, and run your first search.

## Prerequisites

- [Ollama](https://ollama.ai) with `nomic-embed-text`

```bash
ollama pull nomic-embed-text
```

## Install vecgrep

### Homebrew (recommended)

```bash
brew install abdul-hamid-achik/tap/vecgrep
```

### From Source

```bash
git clone https://github.com/abdul-hamid-achik/vecgrep.git
cd vecgrep
task build
```

The binary is written to `bin/vecgrep`.

## Initialize a Project

From any source repository:

```bash
cd /path/to/project
vecgrep init
```

By default, vecgrep registers the project globally and stores generated data under `~/.vecgrep/projects/<project>/`. This avoids creating a repo-local `.vecgrep/` directory.

Use local storage only when you need project-local state:

```bash
vecgrep init --local
```

## Index Files

```bash
vecgrep index
```

For a full rebuild:

```bash
vecgrep index --full
```

## Search

```bash
vecgrep search "error handling in HTTP requests"
```

Open the full-screen terminal workspace:

```bash
vecgrep studio
```

You can also use:

```bash
vecgrep browse
```

## Verify Status

```bash
vecgrep status
vecgrep status --format json
```

Status includes the data directory, vector backend, provider, index counts, and embedding profile state.
