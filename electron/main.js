'use strict';
// Vecgrep Studio — Electron main process.
// Exposes the bridge (CLI + MCP) to the renderer via IPC and serves the window.

const { app, BrowserWindow, ipcMain, dialog, shell } = require('electron');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const bridge = require('./bridge');

const QUIT_AFTER_SEC = (() => {
  const i = process.argv.indexOf('--quit-after');
  const n = i >= 0 ? Number(process.argv[i + 1]) : NaN;
  return Number.isFinite(n) && n > 0 ? n : 0;
})();

let BIN = null;
let BIN_INFO = null;
const mcpPool = { current: null };
let settings = { projectRoot: null };
let mainWindow = null;

function userDataFile() {
  return path.join(app.getPath('userData'), 'settings.json');
}

function loadSettings() {
  try {
    const raw = fs.readFileSync(userDataFile(), 'utf8');
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object') settings = { ...settings, ...parsed };
  } catch { /* first run */ }
  if (!settings.projectRoot || !fs.existsSync(settings.projectRoot)) {
    settings.projectRoot = bridge.defaultProjectRoot();
  }
}

function saveSettings() {
  try {
    fs.writeFileSync(userDataFile(), JSON.stringify(settings, null, 2));
  } catch { /* noop */ }
}

function activeRoot() {
  return settings.projectRoot;
}

function pool() {
  if (!mcpPool.current) mcpPool.current = new bridge.McpPool(BIN);
  return mcpPool.current;
}

const APP_ICON_PNG = path.join(__dirname, 'assets', 'icon.png');

function createWindow() {
  mainWindow = new BrowserWindow({
    width: 1380,
    height: 900,
    minWidth: 1080,
    minHeight: 680,
    title: 'Vecgrep Studio',
    backgroundColor: '#0c1210',
    icon: APP_ICON_PNG,
    webPreferences: {
      preload: path.join(__dirname, 'preload.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  mainWindow.loadFile(path.join(__dirname, 'renderer', 'index.html'));
  mainWindow.webContents.on('console-message', (ev) => {
    if (process.env.VECGREP_STUDIO_DEBUG && ev && typeof ev.message === 'string') {
      console.log(`[renderer:${ev.level}:${ev.sourceId || ''}:${ev.lineNumber || ''}] ${ev.message}`);
    }
  });
  mainWindow.webContents.on('render-process-gone', (_e, details) => {
    console.error(`[main] renderer gone: ${details.reason}`);
  });
}

// --- live streams (index, benchmark, daemon start) ---------------------------
const streams = new Map();

function streamStart(id, args, cwd) {
  streamKill(id);
  const child = spawn(BIN, args, { cwd: cwd || activeRoot(), env: { ...process.env } });
  streams.set(id, child);
  const send = (kind, payload) => {
    if (mainWindow && !mainWindow.isDestroyed()) {
      mainWindow.webContents.send(`vg:stream:${kind}`, { id, ...payload });
    }
  };
  child.stdout.on('data', (d) => send('data', { stream: 'stdout', text: d.toString() }));
  child.stderr.on('data', (d) => send('data', { stream: 'stderr', text: d.toString() }));
  child.on('error', (err) => {
    streams.delete(id);
    send('exit', { code: 127, error: String(err && err.message || err) });
  });
  child.on('close', (code) => {
    streams.delete(id);
    send('exit', { code: code == null ? 1 : code });
  });
  return { started: true, pid: child.pid };
}

function streamKill(id) {
  const child = streams.get(id);
  if (child) {
    try { child.kill('SIGTERM'); } catch { /* noop */ }
    streams.delete(id);
    return true;
  }
  return false;
}

// --- IPC --------------------------------------------------------------------
function registerIpc() {
  ipcMain.handle('vg:binary', async () => ({ path: BIN, ...BIN_INFO }));

  ipcMain.handle('vg:project:get', async () => ({ root: activeRoot() }));
  ipcMain.handle('vg:project:set', async (_e, root) => {
    if (!root || !fs.existsSync(root) || !fs.statSync(root).isDirectory()) {
      throw new Error('invalid project path: ' + root);
    }
    settings.projectRoot = root;
    saveSettings();
    return { root };
  });

  ipcMain.handle('vg:cli', async (_e, args, opts = {}) => {
    const r = await bridge.runCli(BIN, args, {
      cwd: opts.cwd || activeRoot(),
      timeoutMs: opts.timeoutMs || 180000,
      input: opts.input,
    });
    return r;
  });

  ipcMain.handle('vg:cli:json', async (_e, args, opts = {}) => {
    const r = await bridge.runCliJson(BIN, args, {
      cwd: opts.cwd || activeRoot(),
      timeoutMs: opts.timeoutMs || 180000,
      input: opts.input,
    });
    return r;
  });

  ipcMain.handle('vg:stream:start', async (_e, id, args, opts = {}) => {
    return streamStart(String(id), args, opts.cwd || activeRoot());
  });
  ipcMain.handle('vg:stream:kill', async (_e, id) => ({ killed: streamKill(String(id)) }));

  ipcMain.handle('vg:mcp:call', async (_e, tool, mcpArgs, opts = {}) => {
    const client = pool().forRoot(opts.cwd || activeRoot());
    try {
      const r = await client.callTool(tool, mcpArgs || {}, opts.timeoutMs);
      return { ok: r.ok, isError: r.isError, text: r.text, structured: r.structured };
    } catch (err) {
      return { ok: false, isError: true, text: '', structured: null, error: String(err && err.message || err) };
    }
  });
  ipcMain.handle('vg:mcp:tools', async (_e, opts = {}) => {
    const client = pool().forRoot(opts.cwd || activeRoot());
    return client.listTools();
  });
  ipcMain.handle('vg:mcp:status', async () => pool().status());

  ipcMain.handle('vg:dialog:save', async (_e, opts = {}) => {
    const r = await dialog.showSaveDialog(mainWindow, {
      title: opts.title || 'Save file',
      defaultPath: opts.defaultPath || 'vecgrep-completion.sh',
      filters: opts.filters || [{ name: 'All', extensions: ['*'] }],
    });
    return { canceled: r.canceled, path: r.filePath || null };
  });

  ipcMain.handle('vg:file:write', async (_e, filePath, content) => {
    fs.writeFileSync(filePath, String(content), 'utf8');
    return { ok: true, path: filePath };
  });

  ipcMain.handle('vg:dialog:directory', async () => {
    const r = await dialog.showOpenDialog(mainWindow, {
      properties: ['openDirectory', 'createDirectory'],
      defaultPath: activeRoot(),
      title: 'Choose project',
    });
    return { canceled: r.canceled, path: (r.filePaths && r.filePaths[0]) || null };
  });

  ipcMain.handle('vg:shell:open', async (_e, target) => {
    await shell.openPath(target);
    return { ok: true };
  });
  ipcMain.handle('vg:shell:show', async (_e, target) => {
    shell.showItemInFolder(target);
    return { ok: true };
  });

  ipcMain.handle('vg:file:read', async (_e, filePath, startLine, endLine) => {
    const abs = path.isAbsolute(filePath) ? filePath : path.join(activeRoot(), filePath);
    const raw = fs.readFileSync(abs, 'utf8');
    const lines = raw.split(/\r?\n/);
    const s = Math.max(1, startLine || 1);
    const e = Math.min(lines.length, endLine || lines.length);
    return {
      path: abs,
      total: lines.length,
      start: s,
      end: e,
      lines: lines.slice(s - 1, e),
      truncated: raw.length > 4 * 1024 * 1024,
    };
  });

  ipcMain.handle('vg:file:exists', async (_e, filePath) => {
    const abs = path.isAbsolute(filePath) ? filePath : path.join(activeRoot(), filePath);
    return { exists: fs.existsSync(abs), path: abs };
  });
}

// --- boot -------------------------------------------------------------------
app.whenReady().then(() => {
  try {
    BIN = bridge.resolveBinary();
  } catch (e) {
    dialog.showErrorBox('Vecgrep Studio', String(e && e.message || e));
    app.quit();
    return;
  }
  BIN_INFO = bridge.binaryVersion(BIN);
  loadSettings();
  registerIpc();
  if (process.platform === 'darwin' && fs.existsSync(APP_ICON_PNG)) {
    try { app.dock.setIcon(APP_ICON_PNG); } catch { /* dev-only nicety */ }
  }
  createWindow();
  console.log(`[main] ready — binary=${BIN} project=${settings.projectRoot}`);
  if (QUIT_AFTER_SEC > 0) {
    console.log(`[main] auto-quit in ${QUIT_AFTER_SEC}s (--quit-after)`);
    setTimeout(() => app.quit(), QUIT_AFTER_SEC * 1000);
  }
  if (process.argv.includes('--verify-ui')) {
    mainWindow.webContents.on('did-finish-load', () => {
      (async () => {
        const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
        const routes = ['dashboard', 'search', 'similar', 'batch', 'index', 'structure',
          'branches', 'cache', 'daemon', 'projects', 'memory', 'settings', 'doctor', 'benchmark', 'mcp'];
        await sleep(5000); // let boot (binary/project/status) settle
        const nav = await mainWindow.webContents.executeJavaScript(
          `document.querySelectorAll('.nav-item').length`);
        console.log(`[verify-ui] nav items: ${nav}`);
        const sample = () => mainWindow.webContents.executeJavaScript(`(() => ({
          cards: document.querySelectorAll('#view .card, #view .stat').length,
          errors: document.querySelectorAll('#view .callout.err').length,
          h1: ((document.querySelector('#view h1') || {}).textContent || '').slice(0, 40),
        }))()`);
        const results = {};
        for (const r of routes) {
          await mainWindow.webContents.executeJavaScript(`location.hash = '#/${r}'`);
          await sleep(7000); // full status/doctor can take ~8s while MCP holds VecLite
          results[r] = await sample();
          console.log(`[verify-ui] ${r}: ${JSON.stringify(results[r])}`);
        }
        // Revisit slow routes once (lock contention can exceed one window)
        for (const r of routes.filter((x) => results[x].cards === 0 && results[x].errors === 0)) {
          await mainWindow.webContents.executeJavaScript(`location.hash = '#/search'; location.hash = '#/${r}'`);
          await sleep(20000);
          results[r] = await sample();
          console.log(`[verify-ui] ${r} (retry): ${JSON.stringify(results[r])}`);
        }
        const bad = Object.entries(results).filter(([, v]) => v.errors > 0 || v.cards === 0);
        if (nav === 15 && bad.length === 0) console.log('[verify-ui] PASS all 15 routes render with 0 errors');
        else console.log('[verify-ui] FAIL nav=' + nav + ' bad=' + bad.map(([k]) => k).join(','));
        app.quit();
      })().catch((e) => { console.log('[verify-ui] ERROR ' + (e.message || e)); app.quit(); });
    });
  }
});

app.on('window-all-closed', () => {
  if (mcpPool.current) mcpPool.current.stopAll();
  for (const [, child] of streams) {
    try { child.kill('SIGTERM'); } catch { /* noop */ }
  }
  app.quit();
});
