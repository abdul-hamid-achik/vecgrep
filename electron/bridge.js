'use strict';
// Vecgrep Studio — bridge.js
// Pure Node (no Electron dependencies) speaking to vecgrep two ways:
//   1) CLI: runs the vecgrep binary with --format json, returns stdout/stderr.
//   2) MCP: keeps one `vecgrep serve --mcp` server per project (newline-delimited
//      JSON-RPC over stdio) for tools with no CLI equivalent (overview,
//      batch_search, related_files, investigate, ensure, memory_forget/stats).
// main.js (Electron) and smoke.js (headless check) share this module.

const { spawn, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

// ---------------------------------------------------------------------------
// Binary resolution
// ---------------------------------------------------------------------------

function candidateBinaries() {
  const out = [];
  if (process.env.VECGREP_BIN) out.push(process.env.VECGREP_BIN);
  // Packaged app: binary bundled via --extra-resource into Resources/vecgrep
  if (typeof process.resourcesPath === 'string' && process.resourcesPath) {
    out.push(path.join(process.resourcesPath, 'vecgrep'));
  }
  // Dev: sibling checkout <repo>/bin/vecgrep (the app lives in <repo>/electron/)
  out.push(path.resolve(__dirname, '..', 'bin', 'vecgrep'));
  out.push(path.resolve(__dirname, '..', 'vecgrep'));
  return out;
}

function isExecutable(p) {
  try {
    fs.accessSync(p, fs.constants.X_OK);
    return fs.statSync(p).isFile();
  } catch {
    return false;
  }
}

function whichOnPath(name) {
  const r = spawnSync(process.platform === 'win32' ? 'where' : 'which', [name], { encoding: 'utf8' });
  if (r.status === 0 && r.stdout) {
    const first = r.stdout.split(/\r?\n/).map((s) => s.trim()).find(Boolean);
    if (first && isExecutable(first)) return first;
  }
  return null;
}

/** Resolves the vecgrep binary. Throws when not found. */
function resolveBinary() {
  for (const c of candidateBinaries()) {
    if (isExecutable(c)) return c;
  }
  const onPath = whichOnPath('vecgrep');
  if (onPath) return onPath;
  throw new Error(
    'vecgrep binary not found (tried VECGREP_BIN, ../bin/vecgrep and PATH). Run `task build` in the repo.'
  );
}

function binaryVersion(bin) {
  try {
    const r = spawnSync(bin, ['version'], { encoding: 'utf8', timeout: 10000 });
    const text = ((r.stdout || '') + (r.stderr || '')).trim();
    const m = text.match(/v?\d+\.\d+\.\d+[^\s]*/);
    return { ok: r.status === 0, version: m ? m[0] : (text.split(/\r?\n/)[0] || 'unknown'), raw: text };
  } catch (e) {
    return { ok: false, version: 'error', raw: String(e && e.message || e) };
  }
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

/**
 * Runs the CLI. Returns {code, stdout, stderr}.
 * opts: {cwd, timeoutMs, env, input}
 */
function runCli(bin, args, opts = {}) {
  return new Promise((resolve) => {
    const child = spawn(bin, args, {
      cwd: opts.cwd || process.cwd(),
      env: { ...process.env, ...(opts.env || {}) },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    let timedOut = false;
    const timer = opts.timeoutMs
      ? setTimeout(() => { timedOut = true; child.kill('SIGKILL'); }, opts.timeoutMs)
      : null;
    child.stdout.on('data', (d) => { stdout += d.toString(); });
    child.stderr.on('data', (d) => { stderr += d.toString(); });
    child.on('error', (err) => {
      if (timer) clearTimeout(timer);
      resolve({ code: 127, stdout, stderr: stderr + String(err && err.message || err) });
    });
    child.on('close', (code) => {
      if (timer) clearTimeout(timer);
      if (timedOut) stderr += '\n(vecgrep-studio: timed out)';
      resolve({ code: code == null ? 1 : code, stdout, stderr });
    });
    if (opts.input != null) {
      child.stdin.write(opts.input);
      child.stdin.end();
    } else {
      child.stdin.end();
    }
  });
}

/**
 * Runs the CLI expecting JSON on stdout. Returns {code, stdout, stderr, data, parseError}.
 * When stdout is not valid JSON, data=null and parseError describes the failure.
 */
async function runCliJson(bin, args, opts = {}) {
  const r = await runCli(bin, args, opts);
  let data = null;
  let parseError = null;
  const trimmed = r.stdout.trim();
  if (trimmed) {
    try {
      data = JSON.parse(trimmed);
    } catch (e) {
      parseError = String(e && e.message || e);
    }
  } else if (r.code === 0) {
    parseError = 'empty output';
  }
  return { ...r, data, parseError };
}

// ---------------------------------------------------------------------------
// MCP client over stdio (newline-delimited JSON-RPC)
// ---------------------------------------------------------------------------

const MCP_PROTOCOL_VERSION = '2025-06-18';

class McpClient {
  constructor(bin, cwd) {
    this.bin = bin;
    this.cwd = cwd;
    this.child = null;
    this.nextId = 1;
    this.pending = new Map();
    this.buffer = '';
    this.ready = null;
    this.startedAt = null;
    this.lastError = null;
  }

  get running() {
    return !!this.child && this.child.exitCode == null;
  }

  start() {
    if (this.ready) return this.ready;
    this.ready = new Promise((resolve, reject) => {
      const child = spawn(this.bin, ['serve', '--mcp'], {
        cwd: this.cwd,
        env: { ...process.env },
        stdio: ['pipe', 'pipe', 'pipe'],
      });
      this.child = child;
      this.startedAt = new Date().toISOString();
      let stderrTail = '';
      child.stderr.on('data', (d) => {
        stderrTail += d.toString();
        if (stderrTail.length > 4000) stderrTail = stderrTail.slice(-4000);
      });
      child.stdout.on('data', (d) => this._onData(d));
      child.on('error', (err) => {
        this.lastError = String(err && err.message || err);
        for (const [, p] of this.pending) p.reject(new Error(this.lastError));
        this.pending.clear();
        if (!this._settled) { this._settled = true; reject(new Error(this.lastError)); }
      });
      child.on('close', (code) => {
        const msg = `MCP server exited (code ${code})` + (stderrTail ? `: ${stderrTail.trim().split(/\r?\n/).slice(-2).join(' | ')}` : '');
        this.lastError = msg;
        for (const [, p] of this.pending) p.reject(new Error(msg));
        this.pending.clear();
        this.child = null;
        this.ready = null;
        this._settled = true;
      });
      this._request('initialize', {
        protocolVersion: MCP_PROTOCOL_VERSION,
        capabilities: {},
        clientInfo: { name: 'vecgrep-studio', version: '0.1.0' },
      }, 15000).then((result) => {
        this._notify('notifications/initialized');
        this.serverInfo = result && result.serverInfo || null;
        this._settled = true;
        resolve(result);
      }).catch((err) => {
        this.lastError = String(err && err.message || err);
        try { child.kill(); } catch { /* noop */ }
        this.ready = null;
        if (!this._settled) { this._settled = true; reject(err); }
      });
    });
    return this.ready;
  }

  _onData(d) {
    this.buffer += d.toString();
    let idx;
    while ((idx = this.buffer.indexOf('\n')) >= 0) {
      const line = this.buffer.slice(0, idx).trim();
      this.buffer = this.buffer.slice(idx + 1);
      if (!line) continue;
      let msg;
      try {
        msg = JSON.parse(line);
      } catch {
        continue;
      }
      if (msg.id != null && this.pending.has(msg.id)) {
        const p = this.pending.get(msg.id);
        this.pending.delete(msg.id);
        clearTimeout(p.timer);
        if (msg.error) p.reject(new Error(msg.error.message || JSON.stringify(msg.error)));
        else p.resolve(msg.result);
      }
      // Server notifications are ignored.
    }
  }

  _request(method, params, timeoutMs = 120000) {
    return new Promise((resolve, reject) => {
      if (!this.running && method !== 'initialize') {
        reject(new Error('MCP server is not running'));
        return;
      }
      const id = this.nextId++;
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`MCP ${method} timed out`));
      }, timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
      try {
        this.child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
      } catch (e) {
        this.pending.delete(id);
        clearTimeout(timer);
        reject(e);
      }
    });
  }

  _notify(method, params) {
    try {
      if (this.running) this.child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method, params: params || {} }) + '\n');
    } catch { /* noop */ }
  }

  async ensureStarted() {
    if (!this.running) {
      this.ready = null;
      this._settled = false;
    }
    await this.start();
  }

  /** Calls an MCP tool. Returns {ok, text, structured, isError}. */
  async callTool(name, args = {}, timeoutMs) {
    await this.ensureStarted();
    const result = await this._request('tools/call', { name, arguments: args }, timeoutMs || 180000);
    const chunks = (result && result.content || []).filter((c) => c && c.type === 'text').map((c) => c.text);
    return {
      ok: !result.isError,
      isError: !!result.isError,
      text: chunks.join('\n'),
      structured: result.structuredContent != null ? result.structuredContent : null,
      raw: result,
    };
  }

  async listTools() {
    await this.ensureStarted();
    const result = await this._request('tools/list', {}, 15000);
    return (result && result.tools) || [];
  }

  stop() {
    try {
      if (this.child) this.child.kill();
    } catch { /* noop */ }
    this.child = null;
    this.ready = null;
  }
}

/** MCP client pool: one per project root. */
class McpPool {
  constructor(bin) {
    this.bin = bin;
    this.clients = new Map();
  }

  forRoot(root) {
    let c = this.clients.get(root);
    if (!c || !c.running && !c.ready) {
      if (c) c.stop();
      c = new McpClient(this.bin, root);
      this.clients.set(root, c);
    }
    return c;
  }

  status() {
    const out = [];
    for (const [root, c] of this.clients) {
      out.push({ root, running: c.running, startedAt: c.startedAt, lastError: c.lastError, server: c.serverInfo || null });
    }
    return out;
  }

  stopAll() {
    for (const [, c] of this.clients) c.stop();
    this.clients.clear();
  }
}

// ---------------------------------------------------------------------------
// Text-output parsers (for commands without --format json)
// ---------------------------------------------------------------------------

/** Turns `projects list` into [{name, path, data}]. */
function parseProjectsList(text) {
  const projects = [];
  const lines = text.split(/\r?\n/);
  let current = null;
  for (const line of lines) {
    if (/^Registered projects/.test(line) || !line.trim()) continue;
    const nameMatch = line.match(/^  (\S[^\s].*)$/);
    if (nameMatch && !line.startsWith('    ')) {
      current = { name: nameMatch[1].trim(), path: '', data: '' };
      projects.push(current);
      continue;
    }
    if (current) {
      const m = line.match(/^\s*(Path|Data):\s*(.+)$/);
      if (m) {
        if (m[1] === 'Path') current.path = m[2].trim();
        else current.data = m[2].trim();
      }
    }
  }
  return projects;
}

/** Extracts "  Key: value" pairs from a status output. */
function parseStatusPairs(text) {
  const pairs = [];
  for (const line of text.split(/\r?\n/)) {
    const m = line.match(/^\s{2,}([^:]+):\s*(.*)$/);
    if (m) pairs.push({ key: m[1].trim(), value: m[2].trim() });
    else if (line.trim() && !/^\s/.test(line)) pairs.push({ key: '', value: line.trim(), header: true });
  }
  return pairs;
}

function defaultProjectRoot() {
  // The app lives in <repo>/electron → the default project is the repo itself.
  const repoRoot = path.resolve(__dirname, '..');
  if (fs.existsSync(path.join(repoRoot, 'go.mod'))) return repoRoot;
  // Packaged app (or anywhere else): start at home; the user picks a project.
  try {
    return os.homedir();
  } catch {
    return process.cwd();
  }
}

module.exports = {
  resolveBinary,
  binaryVersion,
  runCli,
  runCliJson,
  McpClient,
  McpPool,
  parseProjectsList,
  parseStatusPairs,
  defaultProjectRoot,
  MCP_PROTOCOL_VERSION,
};
