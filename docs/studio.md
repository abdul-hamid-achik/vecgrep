# Studio

Studio is vecgrep's full-screen terminal workspace. It is built with Charm v2 Bubble Tea, Bubbles, and Lip Gloss.

## Open Studio

```bash
vecgrep studio
vecgrep browse
vecgrep studio /path/to/project
```

Running `vecgrep` without a subcommand also opens Studio in an interactive terminal.

## What You Can Do

- Search with semantic, keyword, or hybrid mode.
- Filter by language, chunk type, directory, file pattern, and line range.
- Preview selected results without leaving the TUI.
- Index or fully re-index the current project.
- Delete selected files from the index.
- Inspect project status, vector counts, and embedding profile state.
- Register a folder globally when no project is open.

## Key Bindings

| Key | Action |
| --- | --- |
| `/`, `ctrl+f` | Focus query |
| `enter` | Search from query/filter fields or open selected result |
| `tab` | Move focus across query, filters, results, and preview |
| `m` | Cycle search mode |
| `L` | Cycle language filter |
| `T` | Cycle chunk-type filter |
| `+`, `-` | Change result limit |
| `s` | Find code similar to the selected result |
| `r` | Incremental index |
| `R` | Full re-index |
| `x` | Delete selected file from the index |
| `?` | Toggle help |
| `ctrl+c` | Quit |

## First-run Flow

If Studio opens outside a vecgrep project, press `i` to register the folder under `~/.vecgrep/projects`. This mirrors the default `vecgrep init` behavior and avoids creating a repo-local `.vecgrep/` directory.

Use local storage manually when you need it:

```bash
vecgrep init --local
```
