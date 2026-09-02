# MCP Integration

vecgrep exposes a Model Context Protocol server for AI assistants.

## Start the Server

```bash
vecgrep serve --mcp
```

The server communicates over stdio.

## Tools

| Tool | Purpose |
| --- | --- |
| `vecgrep_init` | Initialize or activate a project |
| `vecgrep_search` | Search indexed code |
| `vecgrep_index` | Index files |
| `vecgrep_status` | Inspect index and provider status |
| `vecgrep_similar` | Find similar code |
| `vecgrep_delete` | Remove a file from the index |
| `vecgrep_clean` | Sync database to disk and report stats |
| `vecgrep_reset` | Clear the index |
| `vecgrep_overview` | Summarize codebase structure |
| `vecgrep_batch_search` | Run multiple searches |
| `vecgrep_related_files` | Find related files |

## Scores and Degraded Mode

`vecgrep_search` scores are calibrated 0-1 similarities in hybrid mode (good
matches typically land around 0.45-0.69) and raw cosine similarities in
semantic mode; keyword mode normalizes BM25 to 0-1 within each result set
(top hit = 1.0). `min_score` expects the 0-1 scale, which every mode now
uses — keyword scores are only comparable within one result set, though.

If the embedding provider is unavailable at query time, hybrid search degrades
to keyword-only and the tool result includes an explicit warning carrying the
provider error. Degraded results carry the same per-result-set normalized
keyword scores, so `min_score` keeps working after degradation. Semantic mode
never degrades; it returns an error instead.

## Provider Credentials

The server inherits its environment from the MCP client that spawned it, not
from your interactive shell. When the configured cloud provider has no key in
that environment:

- `vecgrep serve --mcp` logs one stderr line at startup
  (`vecgrep serve: project=… provider=openai … api_key=missing — set OPENAI_API_KEY …`).
- `vecgrep_status` reports `API key: missing — set …` next to the readiness block.
- `vecgrep_search` with `mode: "keyword"` works normally; hybrid degrades to
  keyword-only with a `Warning:` line; `mode: "semantic"` returns `IsError` with
  the provider-specific remedy.
- A project that was never indexed reports `readiness.state = "empty"` with
  `action = "vecgrep_index"` (not a database error), plus the key warning so the
  agent knows indexing will fail until the key is fixed.

See [Embedding Providers → API Keys Under MCP Launchers](./providers.md#api-keys-under-mcp-launchers)
for where to put the key per launcher, and run `vecgrep doctor` from that
launcher to confirm what it sees.

## Claude Code

Add vecgrep globally:

```bash
claude mcp add vecgrep -- vecgrep serve --mcp
```

Or add it for the current project:

```bash
claude mcp add --scope project vecgrep -- vecgrep serve --mcp
```

## Manual Config

```json
{
  "mcpServers": {
    "vecgrep": {
      "command": "vecgrep",
      "args": ["serve", "--mcp"]
    }
  }
}
```

## Project Activation

`vecgrep_init` defaults to global storage under `~/.vecgrep/projects`. Set `local=true` only when you want a project-local `.vecgrep/` directory.
