# vecgrep: per-branch index switching + background daemon

> **Status:** design / proposed (2026-06-24). Authored from a codemap-side cross-ecosystem design pass.
> Mirrors the codemap design so the two stay symmetric. **gpeek is NOT involved** (it's a Swift macOS
> GUI git client — dropped). The git-side trigger is a plain **`post-checkout` hook**.

vecgrep is the **lighter lift** for both features: its index is a single per-project `vectors.veclite`
file (so a branch snapshot is a file copy, not a row-serialization like codemap), and it **already has a
dormant fsnotify watcher** at `internal/index/watcher.go` (`Watcher` + `WatchAndIndex`, 500ms debounce,
ignore globs) that is wired nowhere today — the daemon is its first real caller.

---

## Feature A — per-branch index switching (via fcheap)

**Goal:** keep a separate index per git branch and switch in O(1) on checkout, instead of one flat index
that's wrong after every branch change. fcheap is the content-addressed snapshot vault (dedup + compression
+ hash-verified restore) keyed by `repo+branch+base-sha`.

**The single lever:** index path derivation in `internal/config/resolution.go` `Resolve()` →
`internal/db/veclite_backend.go` `VecLitePath(dataDir)`. Today every branch shares
`~/.vecgrep/projects/<name>/vectors.veclite`. Give each branch its own file.

### Tasks
1. **`internal/config/global.go`** — add `ProjectBranchDataDir(name, branch) = ~/.vecgrep/projects/<name>/branches/<sanitized-branch>/`. **`resolution.go Resolve()`** sets `Config.DataDir` to it when a branch resolves; fall back to the legacy flat dir for non-git / detached HEAD / worktree-without-branch (treat as `_detached` or the default branch). **This is the only path-derivation change** — CLI + MCP + studio all switch through it.
2. **`internal/git/branch.go`** (new) — `CurrentBranch(root)` via `git -C <root> rev-parse --abbrev-ref HEAD`, `HeadSHA(root)`, `RepoRoot` via `--show-toplevel`, `IsDetached` via `symbolic-ref`, `SanitizeBranch(name)` (replace `/`,`\`,`:`,whitespace → `-`, strip leading dots, cap length, disambiguate collisions with a short raw-branch hash), `RepoHash(root)` (sha1[:12] of the symlink-resolved abs root). Pure shell-out — **no CGO git lib** (honors `CGO_ENABLED=0`).
3. **`internal/app/branchswitch.go`** (new) — `BranchSnapshot(root, branch)` copies `<branchDir>/vectors.veclite` into a snapshot dir + writes `snapshot.json` (embeddingProfile from `embedding_profile.go LoadEmbeddingProfile`, `base_sha`), then shells `fcheap save`. `BranchSwitch(root, from, to)`: close current `Session`; resolve target branch dir; if an fcheap snapshot exists **and** its `base_sha` == current HEAD **and** the embeddingProfile matches → `fcheap restore` into the branch dir + open it; else open/create the branch dir and run the indexer (`filterUnchangedFiles` re-embeds only the diff), then re-snapshot.
4. **`internal/snapshot/fcheap.go`** (new) — thin exec wrapper: `Save(dir, tool, name, tags, sourceSHA) -> stashID` (parse `--json`), `Restore(id, toDir)`, `List(tags)`. Resolve the `fcheap` binary like vecgrep's analyze resolves its subprocess.
5. **Pointer/state file** `~/.vecgrep/projects/<name>/branches/index.json` (atomic temp+rename) — `{repo_root, repo_hash, default_branch, active_branch, branches:{<b>:{stash_id, base_sha, embedding_profile, vector_count, last_switched_at}}}`. The fast lookup; rebuildable from `fcheap list --tag vecgrep-index --tag repo:<hash>` if lost.
6. **`cmd/vecgrep/main.go`** — add `branch-switch`, `branch-snapshot`, `branch-status`, `--branch` override on `index`/`search`, and `--install-hook` (writes `.git/hooks/post-checkout`, worktree/`core.hooksPath`-aware, checks the hook's `flag` arg so it doesn't fire on `git checkout -- file`).
7. **`internal/mcp/server_sdk.go`** — `vecgrep_branch_switch` / `vecgrep_branch_status`; reuse `activateProject`'s close+reopen DB primitive to repoint at the branch dir (it's already the hot-swap primitive).
8. **`internal/app/status.go` + `projects list`** — enumerate `branches/` and surface which branch indexes exist + which is active.

**Embedding profile** lives in veclite collection metadata per branch file → no cross-branch contamination. **Restore is gated** on `snapshot.json` profile == current profile (never mix models). **Optional dedup win:** promote `internal/embed/cache.go EmbeddingCache` to an on-disk shared cache so switching re-embeds only the branch diff.

---

## Feature B — background daemon (incremental sync + Ollama throttle)

**Goal:** one long-lived process owns the writable veclite handle, watches the FS, incrementally re-embeds,
serves all clients over a unix socket (so multiple MCP/CLI clients stop fighting the veclite lock —
`ErrFileLocked`), and **throttles Ollama** so background re-embeds never saturate it or starve interactive
search.

### Tasks
1. **Wire the dormant watcher** — `internal/index/watcher.go` already coalesces a debounce window into `toIndex`/`toRemove`. **Bug to fix:** `WatchAndIndex` computes `toRemove` but never applies it (explicit TODO in the code) → wire `toRemove → db.DeleteFile(ctx, relPath)`. Keep the 500ms debounce + coalescing.
2. **`internal/embed/throttle.go`** (new) — `ThrottledProvider` wrapping the Ollama provider, **replacing the role of `OllamaProvider.EmbedBatch`'s bare `semaphore(maxBatchSize=32)`** with: (a) content-hash **dedup** (sha256 of chunk text → vector / in-flight singleflight; promote `embed.EmbeddingCache` into this layer); (b) a **coalescing FIFO queue** + bounded worker pool (`EmbedWorkers`, default 2 — lower than the index pool so background work stays gentle); (c) a **token-bucket rate limit** (`golang.org/x/time/rate`, `EmbedRPS`) **and** a max-in-flight semaphore (`EmbedMaxInFlight`); (d) **two priority lanes** — interactive query-embeds jump ahead of background index-embeds so a reindex storm never delays a user's search; (e) **backpressure** — when the queue is full the watcher flush blocks/recoalesces rather than spawning unbounded goroutines.
3. **`internal/daemon/`** (new, or extend `internal/app`) — `Daemon` holds ONE `app.OpenSession` (write; the sole writer). All other surfaces use `app.OpenReadOnlySession` (already exists, `SharedRead:true`). Write `daemon.json`/`daemon.lock`/`daemon.sock` under `cfg.DataDir`; start the watcher (`indexer.Index` for `toIndex`, `db.DeleteFile` for `toRemove`); serve `mcp.NewSDKServer{DB,Provider,ProjectRoot}` frames + `daemon.*` control RPCs over the socket (**newline-delimited JSON-RPC — NEVER Content-Length**).
4. **`cmd/vecgrep/main.go`** — `vecgrep daemon start|stop|status`. `runServe` becomes a **stdio↔socket bridge** when a daemon is live (forward newline-JSON verbatim), else today's `StdioTransport SDKServer`. `runSearch`/`runStatus`/`runSimilar` dial the socket when present, else `OpenReadOnlySession`. `ErrFileLocked` (already handled in `runReset`) becomes the explicit "daemon owns the lock — use the daemon" signal.
5. **`internal/config/config.go`** — `DaemonConfig{Autostart, IdleTimeout(30m), EmbedWorkers(2), EmbedRPS, EmbedMaxInFlight, Debounce}` with `config show/set` + viper persistence; `VECGREP_DAEMON_*` env overrides.
6. **`runStatus` / `projects list`** — read `daemon.json`, show running/branch/last_reindex/queue_depth.

**Lifecycle:** `start [--foreground]`, `stop` (socket shutdown or SIGTERM), `status` (read `daemon.json` + socket ping). Optional autostart on first query (double-fork detached); idle shutdown after `IdleTimeout`.

---

## How A + B compose
The daemon performs the branch switch (`daemon.switchBranch{branch}` control RPC: re-point the watcher, reopen the branch's veclite) and re-embeds only the branch diff through the throttle. `branch-switch` detects a running daemon (`daemon.json` + dialable socket) and **delegates the swap to it** rather than opening a second writer.

## Build order (vecgrep)
0. `internal/git/branch.go` + `ProjectBranchDataDir` wiring + read-only `branch-status` (validate detection incl. detached HEAD/worktrees, no writes).
1. Feature A: `BranchSnapshot`/`BranchSwitch` (file copy + close/reopen) + pointer file + fcheap wrapper. Validate manually (checkout → snapshot → switch back → search still works).
2. Feature B: wire the watcher into a daemon, fix the delete gap, add the throttle, socket-serve.
3. `--install-hook` + daemon-lock arbitration + status surfacing. Add a glyphrun/e2e flow per feature.

## Risks
embeddingProfile drift (gate every restore; veclite dim-mismatch is a hard fail) · base-sha staleness (rebase/force-push makes base-sha unreachable → `git merge-base --is-ancestor`, treat as absent → reindex) · single-writer lock (only the daemon writes; `branch-switch` delegates) · detached HEAD/worktrees/submodules · post-checkout hook also fires on file checkout (check the `flag` arg) · snapshot dedup needs **deterministic** export ordering or fcheap can't hash-collide identical slices · socket framing must stay newline-JSON (the documented `glyph` Content-Length bug).
