---
layout: home

hero:
  name: vecgrep
  text: Semantic code search that actually understands your codebase
  tagline: Index once, search with plain English. Local-first vector embeddings, hybrid keyword+semantic search, and an MCP server for your AI assistant — all powered by Ollama.
  image:
    src: /vecgrep-mark.svg
    alt: vecgrep mark
  actions:
    - theme: brand
      text: Get Started in 60 Seconds
      link: /quick-start
    - theme: alt
      text: Explore Studio
      link: /studio
    - theme: alt
      text: GitHub
      link: https://github.com/abdul-hamid-achik/vecgrep

features:
  - icon: 🧠
    title: Semantic Vector Search
    details: Describe what you're looking for in natural language. vecgrep embeds code chunks and finds semantically related results — not just text matches.
  - icon: 🔍
    title: Hybrid Search Mode
    details: Combine semantic vector similarity with VecLite BM25 keyword matching. Tune vector/text weights or switch modes per query.
  - icon: 🔒
    title: Local-First by Default
    details: Vectors stay on your machine. Ollama provides local embeddings with nomic-embed-text. No cloud account, no telemetry, no data leaving your laptop.
  - icon: 🤖
    title: MCP Server for AI Assistants
    details: Expose indexed code to Claude, Cursor, and any MCP-compatible client. Your AI assistant can search, index, and inspect your codebase semantically.
  - icon: 🎨
    title: Studio Terminal UI
    details: A full-screen Bubble Tea TUI for searching, previewing code, indexing projects, and inspecting vector status — without leaving the terminal.
  - icon: ⚡
    title: Incremental Indexing
    details: File-hash-based change detection means only modified files get re-indexed. Full rebuilds only when you change embedding model or dimensions.
  - icon: 🌐
    title: Cloud-Ready Providers
    details: Switch to OpenAI, Cohere, or Voyage AI when a managed provider fits. Same CLI, same search, same workflow — just a config change away.
  - icon: 🧩
    title: Lossless Language-Aware Chunking
    details: Structural boundaries are used when available; imports, globals, container regions, and recognized long-tail languages stay searchable through a bounded generic fallback.
---

<div class="vp-home-custom">

<!-- Stats Banner -->
<div class="stats-banner">
  <div class="stat-item"><span class="stat-icon">🧠</span><span class="stat-num">4</span><span class="stat-label">Embedding Providers</span></div>
  <div class="stat-item"><span class="stat-icon">🔌</span><span class="stat-num">11</span><span class="stat-label">MCP Tools</span></div>
  <div class="stat-item"><span class="stat-icon">🎯</span><span class="stat-num">3</span><span class="stat-label">Search Modes</span></div>
  <div class="stat-item"><span class="stat-icon">📦</span><span class="stat-num">0</span><span class="stat-label">Cloud Required</span></div>
</div>

<!-- How It Works -->
## How It Works

<div class="how-it-works">

<div class="step" markdown="1">

### 1. Index

```bash
vecgrep init
vecgrep index
```

vecgrep uses structural boundaries where available and a lossless generic
fallback everywhere else, embeds each chunk via Ollama, and stores vectors in VecLite — all locally under
`~/.vecgrep/projects/`.

</div>

<div class="step" markdown="1">

### 2. Search

```bash
vecgrep search "error handling in HTTP middleware"
```

Ask in plain English. Hybrid mode blends semantic vector similarity with
BM25 keyword matching. Filter by language, file pattern, directory, or line
range.

</div>

<div class="step" markdown="1">

### 3. Connect

```bash
claude mcp add vecgrep -- vecgrep serve --mcp
```

Expose your index to any MCP-compatible AI assistant. Claude, Cursor, and
others can search your codebase semantically — no copy-paste required.

</div>

</div>

<!-- Terminal Demo -->
## See It In Action

<div class="terminal-demo" markdown="1">

```bash
# Index the current project (incremental — only changed files)
$ vecgrep index
Indexing 247 files...
  ✓ 2,126 chunks embedded
  ✓ 1,638,752 vectors stored in VecLite
Done in 14.2s

# Search with natural language
$ vecgrep search "database connection pooling"
Results (hybrid mode, top 5):

  0.68  internal/db/pool.go:42-68
        func (p *ConnectionPool) acquire(ctx context.Context) (*Conn, error) {
          p.mu.Lock()
          defer p.mu.Unlock()
          select {
          case conn := <-p.free:
        ...

  0.61  internal/db/pool.go:112-135
        func (p *ConnectionPool) release(conn *Conn) {
          p.mu.Lock()
          defer p.mu.Unlock()
        ...

  0.53  internal/config/resolution.go:88-104
        // resolveDatabaseConfig merges project and global DB settings
        ...

# Find similar code by chunk ID, file:line, or raw text
$ vecgrep similar --file-location internal/search/search.go:50

# Open the full-screen terminal workspace
$ vecgrep studio
```

</div>

<!-- Why vecgrep -->
## Why vecgrep

<div class="why-vecgrep" markdown="1">

### Your code never leaves your machine

Traditional cloud-based code search tools require uploading your source code
to a server. vecgrep runs entirely locally — Ollama generates embeddings on
your laptop, vectors are stored under `~/.vecgrep/`, and no telemetry is
collected. For proprietary or sensitive codebases, this is the difference
between "can we use this?" and "legal will never approve this."

### Semantic search finds what keywords can't

`grep` finds exact strings. vecgrep finds *meaning*. Search for "database
connection pooling" and get results about `ConnectionPool.acquire()` — even
if those exact words never appear together. The hybrid mode blends both
approaches so you never miss a keyword match either.

### Built for AI-assisted development

The MCP server turns vecgrep into a semantic search backend for your AI
assistant. Instead of manually pasting files into a chat, Claude and Cursor
query your index directly — `vecgrep_search`, `vecgrep_similar`,
`vecgrep_overview`, and 8 more tools are available out of the box.

</div>

<!-- Comparison -->
## vecgrep vs Traditional Search

<div class="comparison-table" markdown="1">

| Feature | grep / ripgrep | vecgrep |
| --- | --- | --- |
| Match by meaning | ✗ text patterns only | ✓ semantic vectors |
| Natural language queries | ✗ | ✓ plain English |
| Keyword + semantic blend | ✗ | ✓ hybrid mode |
| Language-aware chunking | ✗ line-based | ✓ structural where available, lossless fallback otherwise |
| Filter by language/type/dir | limited | ✓ full metadata filters |
| Similar code discovery | ✗ | ✓ `vecgrep similar` |
| AI assistant integration | ✗ | ✓ MCP server |
| Incremental re-indexing | ✗ | ✓ file-hash detection |
| Local-first embeddings | N/A | ✓ Ollama default |
| Terminal UI | ✗ | ✓ Studio TUI |

</div>

<!-- MCP Section -->
## Bring Your AI Assistant Into Your Codebase

<div class="mcp-cta" markdown="1">

vecgrep ships a Model Context Protocol server that gives AI assistants
semantic access to your indexed code. Instead of pasting files into a chat
window, your assistant queries the index directly.

```bash
# Add vecgrep to Claude Code
claude mcp add vecgrep -- vecgrep serve --mcp

# Or configure manually in your MCP client
{
  "mcpServers": {
    "vecgrep": {
      "command": "vecgrep",
      "args": ["serve", "--mcp"]
    }
  }
}
```

**11 MCP tools available:** `vecgrep_search` · `vecgrep_index` ·
`vecgrep_init` · `vecgrep_status` · `vecgrep_similar` · `vecgrep_delete` ·
`vecgrep_clean` · `vecgrep_reset` · `vecgrep_overview` ·
`vecgrep_batch_search` · `vecgrep_related_files`

→ [Read the full MCP integration guide](/mcp)

</div>

<!-- Provider Matrix -->
## Choose Your Embedding Provider

<div class="provider-matrix" markdown="1">

| Provider | Default Model | Dimensions | Type |
| --- | --- | ---: | --- |
| **Ollama** (default) | `nomic-embed-text` | 768 | Local · free |
| OpenAI | `text-embedding-3-small` | 1536 | Cloud · API key |
| Cohere | `embed-v4.0` | 1536 | Cloud · API key |
| Voyage AI | `voyage-code-3` | 1024 | Cloud · API key |

Switch providers with a single config command — then run `vecgrep index --full`.
→ [Provider configuration details](/providers)

</div>

<!-- FAQ -->
## Frequently Asked Questions

<div class="faq-section" markdown="1">

<details>
<summary><strong>Do I need a GPU or special hardware to run vecgrep?</strong></summary>

No. vecgrep uses Ollama with `nomic-embed-text` by default, which runs
efficiently on CPU. Embedding generation is a one-time cost per indexing
run — search itself is pure vector similarity and is instant on any machine.

</details>

<details>
<summary><strong>How is vecgrep different from grep or ripgrep?</strong></summary>

grep and ripgrep find exact text patterns. vecgrep finds *semantically
related* code — you describe what you're looking for in natural language and
get results that match the meaning, not just the text. The hybrid mode also
blends BM25 keyword matching so you never lose exact-match capability.

</details>

<details>
<summary><strong>Does my source code get sent to the cloud?</strong></summary>

No. With the default Ollama provider, embeddings are generated locally and
vectors are stored under `~/.vecgrep/projects/`. Nothing leaves your machine.
Cloud providers (OpenAI, Cohere, Voyage AI) are optional and only activated
when you explicitly configure them.

</details>

<details>
<summary><strong>Which AI assistants work with the MCP server?</strong></summary>

Any client that supports the Model Context Protocol, including Claude Code,
Cursor, and custom MCP-compatible clients. The server exposes 11 tools for
searching, indexing, status inspection, similar-code discovery, and
codebase overview.

</details>

<details>
<summary><strong>What languages does vecgrep support?</strong></summary>

vecgrep recognizes Go, JavaScript/TypeScript, Python, Vue, Rust, Java/Kotlin/
Scala, C/C++/CUDA, C#/VB, Ruby, PHP, Dart, Swift, Lua, Elixir, common web
containers, Terraform/HCL, and text formats. Recognition provides stable
language metadata and filters. Built-in structural heuristics currently cover
Go, JavaScript/TypeScript, Python, and Rust; fresh codemap exports provide
stronger symbol boundaries. Every other language uses bounded generic chunks,
so recognition never overstates parser or call-graph support.

</details>

<details>
<summary><strong>How does incremental indexing work?</strong></summary>

vecgrep hashes every file on each `vecgrep index` run. Files whose hash
hasn't changed are skipped — only new or modified files get re-embedded.
A full rebuild (`vecgrep index --full`) is only needed when you change
embedding model, dimensions, or chunking profile.

</details>

<details>
<summary><strong>Is vecgrep free and open source?</strong></summary>

Yes. vecgrep is MIT-licensed and available on
[GitHub](https://github.com/abdul-hamid-achik/vecgrep). Contributions,
issues, and feature requests are welcome.

</details>

</div>

<!-- Final CTA -->
## Ready to Search Smarter?

<div class="final-cta" markdown="1">

```bash
# Homebrew (recommended)
brew install abdul-hamid-achik/tap/vecgrep

# Or build from source
git clone https://github.com/abdul-hamid-achik/vecgrep.git
cd vecgrep && task build

# Then index and search
cd /path/to/your/project
vecgrep init && vecgrep index
vecgrep search "what you're looking for"
```

[Quick Start Guide](/quick-start){.cta-button} · [CLI Reference](/usage){.cta-button} · [Studio TUI](/studio){.cta-button} · [GitHub](https://github.com/abdul-hamid-achik/vecgrep){.cta-button}

MIT-licensed · open source · contributions welcome

</div>

</div>
