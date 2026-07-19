# Studio

Studio is vecgrep's full-screen terminal workspace. It is built with Charm v2 (**Bubble Tea**, **Bubbles**, **Lip Gloss**):

| Bubble | Where |
| --- | --- |
| `textinput` | Query + filters |
| `viewport` | Preview + status/config/help scroll |
| `spinner` | Searching, planning, indexing (discover) |
| `progress` | Honest gradient bar once walk completes |
| `help` | Short footer key hints + full `?` help |
| `list` | Results (pagination, selection chrome, fuzzy `/` filter) |
| `table` | Status breakdown (languages, chunk types, pending) |
| mouse | Wheel scroll + click to focus results/preview |

## Open Studio

```bash
vecgrep studio
vecgrep browse
vecgrep studio /path/to/project
```

Running `vecgrep` without a subcommand also opens Studio in an interactive terminal.

## What You Can Do

- Search with semantic, keyword, or hybrid mode (with automatic keyword fallback warnings).
- See **readiness** at a glance: empty, profile mismatch, stale, or ready — with one-key actions.
- Filter by language, chunk type, directory, file pattern, line range, and min-score.
- Preview selected results (soft-wrapped) without leaving the TUI.
- Index or fully re-index (full reindex shows a dry-run plan first).
- Delete selected files from the index or reset the project index (with path/count confirms).
- Yank `path:line` or snippet to the clipboard.
- Browse recent queries with ↑/↓ in the query field.
- Inspect project status, vector counts, branch, codemap, and embedding profile state.
- Register a folder globally when no project is open.

## Key Bindings

| Key | Action |
| --- | --- |
| `/` | Focus query — **or** fuzzy-filter results when results are focused |
| `ctrl+f` | Focus query |
| `enter` | Search from query/filter fields, or open selected result |
| `tab` / `shift+tab` | Move focus (query → results → preview; filters when expanded) |
| `f` | Expand/collapse directory · file glob · line range filters |
| `↑` / `↓` in query | Browse recent searches |
| `j`/`k`, arrows | Move result selection / scroll overlays |
| `g` / `G` | First / last result |
| `u`/`d`, pgup/pgdn | Page results (or preview when focused) |
| `m` / `M` | Cycle search mode forward / reverse (re-runs if a query is set) |
| `L` / `T` | Cycle language / type filter (re-runs if a query is set) |
| `+` / `-` | Change result limit |
| `[` / `]` | Decrease / increase min-score |
| `s` | Find code similar to the selected result |
| `y` / `Y` | Yank `path:line` / snippet to clipboard |
| `r` | Incremental index (phase-aware progress: walk vs embed, paths, bytes) |
| `R` | Full re-index (dry-run plan + confirm) |
| `esc` while indexing | Cancel the in-flight index |
| `x` | Delete selected file from the index |
| `o` | Open selected result in `$VISUAL` / `$EDITOR` |
| `i` | Register folder globally when none is open |
| `v` | Status view (j/k scroll, toggle) |
| `c` | Config view (j/k scroll, toggle) |
| `?` | Help (outside query input; typing `?` in the query is allowed) |
| `!` | Reset project index |
| `q` | Quit outside the query input |
| `ctrl+c` | Quit always |

## Readiness

Studio surfaces the same readiness vocabulary as CLI/MCP:

| Chip | Meaning | Action |
| --- | --- | --- |
| `empty` | No vectors yet | Press `r` |
| `profile≠` | Embedding profile mismatch | Press `R` |
| `stale` | Pending file changes | Press `r` |
| `fresh?` | Freshness unknown | Press `R` |
| `ready` | Searchable | — |

If hybrid search falls back to keyword (provider down), the footer shows a warning and the header can show `mode hybrid→keyword`.

## First-run Flow

If Studio opens outside a vecgrep project, press `i` to register the folder under `~/.vecgrep/projects`. This mirrors the default `vecgrep init` behavior and avoids creating a repo-local `.vecgrep/` directory.

After registration you will see an empty-index CTA — press `r` to index.

Use local storage manually when you need it:

```bash
vecgrep init --local
```

## Indexing progress

While indexing, Studio shows a dedicated panel (always visible, even if you still have search results):

- **Root path** in the panel title — check this first if you meant a smaller folder
- **Discover phase**: walked / queued / embedded / skipped + scanned bytes — **no %** (the queue still grows)
- **Embed phase** (after walk finishes): honest `N/M` bar + rate + ETA
- **Current file** + recent files
- **Large tree warning** when walked ≥ 5k files or ≥ ~500 MiB — cancel with `esc`

Percent never uses a growing denominator. That old “90/100 → 100/110” feeling was concurrent walk+embed; the UI no longer presents that as completion.

### Plan-first confirm (wrong-folder gate)

Before spending embeddings on a big tree, Studio may **plan** then ask for `y/n`:

| Trigger | Behavior |
| --- | --- |
| Empty index, small (&lt;100 files) | Plan printed, then **auto-start** (Ollama-friendly) |
| Empty index, larger | Dry-run → confirm with root / counts / size |
| Large plan | ≥2k files / ≥200 MiB / ≥10k chunks → confirm |
| Full reindex (`R`) | Always dry-run → confirm |
| Small incremental `r` | Indexes immediately |

Confirm footer example:

```text
first index @ /Users/me/projects? scan 12480 · embed 12480 · ~48200 chunks · 1.2 GiB  y/n
```

Press `n`/`esc` to cancel without embedding.

## Read-only / daemon

If the daemon holds the write lock, Studio opens **read-only**: search and preview still work; `r`/`R` reindex via the daemon (with elapsed time); delete and reset are blocked until the daemon releases the lock.
