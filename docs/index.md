---
layout: home

hero:
  name: vecgrep
  text: Local-first semantic code search
  tagline: Index codebases, search with natural language, and keep vectors on your machine by default.
  image:
    src: /vecgrep-mark.svg
    alt: vecgrep mark
  actions:
    - theme: brand
      text: Quick Start
      link: /quick-start
    - theme: alt
      text: Open Studio
      link: /studio

features:
  - title: Local-first indexing
    details: Store generated indexes under ~/.vecgrep/projects by default and use Ollama for local embeddings.
  - title: Hybrid search
    details: Combine semantic vector search with VecLite BM25 keyword matching and metadata filters.
  - title: Studio TUI
    details: Browse results, preview code, index projects, and inspect vector status from a Charm v2 terminal UI.
  - title: Cloud-ready providers
    details: Use OpenAI, Cohere, or Voyage AI when a managed embedding provider is the right fit.
---

## What vecgrep Does

vecgrep indexes a source tree into code chunks, embeds those chunks, and stores them in VecLite. You can search with natural language, exact keywords, or a hybrid of both.

The default setup is local-first:

- `vecgrep init` registers projects in `~/.vecgrep/config.yaml`.
- Vector data is stored under `~/.vecgrep/projects/<project>/`.
- Ollama provides local embeddings with `nomic-embed-text`.
- Repository-local `.vecgrep/` directories are only created when you use `vecgrep init --local`.

## Common Workflows

| Goal | Start Here |
| --- | --- |
| Install and run your first search | [Quick Start](/quick-start) |
| Learn the CLI commands | [CLI Usage](/usage) |
| Configure storage and providers | [Configuration](/configuration) |
| Use the terminal workspace | [Studio](/studio) |
| Connect an AI assistant | [MCP Integration](/mcp) |
| Understand VecLite storage | [VecLite Integration](/veclite-integration) |
| Understand codemap intelligence sharing | [codemap Integration](/codemap-integration) |
