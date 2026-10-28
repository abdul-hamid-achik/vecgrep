# Vecgrep Studio

Desktop app (Electron) to review and work with vecgrep: every CLI command and every MCP tool in one GUI, against the project you choose.

No build step: the renderer is plain HTML + CSS + vanilla JS. The only dependency is `electron`.

## Usage

```bash
# from the repo root
task desktop          # build vecgrep, install deps and open the app
task desktop:smoke    # headless CLI+MCP bridge self-test
task desktop:verify   # open the app, verify the rendered DOM, quit
```

Or directly:

```bash
cd electron
npm install
npm start
```

## Install (macOS)

```bash
task desktop:install   # builds bin/vecgrep, packages Vecgrep Studio.app, copies to /Applications
```

The bundle carries its own `vecgrep` binary (`Contents/Resources/vecgrep`), the app icon, and an ad-hoc signature. Launch it from Spotlight/Launchpad like any app. Your projects registry (`~/.vecgrep/`) is shared with the CLI, so all 22 registered projects appear in the project picker.

The app resolves the binary in this order: `VECGREP_BIN` → `../bin/vecgrep` → `vecgrep` on `PATH`. The default project is the repo checkout itself; switch it in the top-bar selector.

Useful variables:

- `VECGREP_BIN=/path/to/vecgrep` — binary to use.
- `VECGREP_STUDIO_DEBUG=1` — forward the renderer console to stdout.

## How it works

- **CLI**: the main process runs `vecgrep … --format json` with `cwd` = active project and parses the output. Indexing, benchmark and `daemon start` run with live streamed output (IPC streams).
- **MCP**: a pool keeps one `vecgrep serve --mcp` per project (line-delimited JSON-RPC over stdio) for tools with no CLI equivalent: `overview`, `batch_search`, `related_files`, `investigate`, `ensure`, `branch_status`, `memory_forget` and `memory_stats`. The MCP console can call all 18 tools.
- **Security**: `contextIsolation` + `sandbox`, no `nodeIntegration`; the renderer only sees the minimal `preload.js` API. CSP `default-src 'self'`.

## Icon

`assets/icon.svg` is full-bleed square artwork derived 1:1 from the brand mark in `docs/public/vecgrep-mark.svg` (no rounded corners baked in — macOS applies the squircle mask). `icon.icns` (macOS) and `icon.png` (512px, window/dock) are generated from it with `sips` + `iconutil`. Regenerate with:

```bash
cd electron && mkdir -p /tmp/vg-icon.iconset
for s in 16 32 128 256 512; do sips -s format png -z $s $s assets/icon.svg --out /tmp/vg-icon.iconset/icon_${s}x${s}.png; done
for s in 32 64 256 512 1024; do sips -s format png -z $s $s assets/icon.svg --out /tmp/vg-icon.iconset/icon_$((s/2))x$((s/2))@2x.png; done
iconutil -c icns /tmp/vg-icon.iconset -o assets/icon.icns
sips -s format png -z 512 512 assets/icon.svg --out assets/icon.png
```

## Feature → view map

| vecgrep feature | Studio view |
|---|---|
| `status`, `doctor` (summary), `ensure` | Dashboard |
| `search` (modes, 12 filters, explain) | Search |
| `similar` (chunk / file:line / text) | Similar |
| `batch_search`, `investigate` (MCP) | Batch & Investigate |
| `index` (incremental/full/dry-run/plan/streaming), `delete`, `clean`, `reset` | Manage index |
| `overview`, `related_files` (MCP) | Structure |
| `branch` (switch/snapshot/status/install-hook/prune) + `branch_status` | Branches |
| `cache` (status/save/restore/sweep) | Cache |
| `daemon` (start/stop/status) | Daemon |
| `projects` (list/add/remove/prune), `init` | Projects |
| `memory recall/remember` + `memory_forget/stats` (MCP) | Memory |
| `config show/set/preset` | Settings |
| `doctor` (checks + optional ping) | Doctor |
| `benchmark embeddings` (profiles, dataset, batch, root) | Benchmark |
| `serve --mcp` (catalog, console, snippets), `completion` | MCP |
| `version` | Sidebar brand + status bar |

## Files

```
electron/
  assets/            app icon (icon.svg source, icon.png, icon.icns)
  main.js            main process (window, IPC, streams, MCP pool)
  preload.js         safe API exposed to the renderer
  bridge.js          CLI + MCP client in pure Node (shared with smoke.js)
  smoke.js           headless self-test (12 checks)
  renderer/
    index.html       shell (sidebar, topbar, view)
    styles.css       theme
    app.js           state, router, topbar, components
    views/*.js       the 15 views (one per route)
```
