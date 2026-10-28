'use strict';
/* Vecgrep Studio — renderer core: state, router, topbar and components. */
const api = window.vecgrep;
const Views = {}; // views register here from views/*.js

const S = {
  root: null,
  binary: null,
  projects: [],
  route: 'dashboard',
  statusCache: null,
  statusAt: 0,
};

/* ---------------- icons (linear SVG, 15px) ---------------- */
const IC = {
  panel: '<rect x="2" y="2" width="5.5" height="11" rx="1.2"/><rect x="8.5" y="2" width="5.5" height="6.5" rx="1.2"/><rect x="8.5" y="9.5" width="5.5" height="3.5" rx="1.2"/>',
  search: '<circle cx="7" cy="7" r="4.2"/><line x1="10.4" y1="10.4" x2="14" y2="14"/>',
  similar: '<circle cx="5.5" cy="5.5" r="2.6"/><circle cx="10.5" cy="10.5" r="2.6"/><line x1="7.3" y1="7.3" x2="8.7" y2="8.7"/>',
  batch: '<rect x="2" y="2" width="12" height="3.4" rx="1"/><rect x="2" y="6.6" width="12" height="3.4" rx="1"/><rect x="2" y="11" width="7" height="3" rx="1"/>',
  index: '<ellipse cx="8" cy="3.6" rx="5.4" ry="1.9"/><path d="M2.6 3.6v8.8c0 1 2.4 1.9 5.4 1.9s5.4-.9 5.4-1.9V3.6"/><path d="M2.6 8c0 1 2.4 1.9 5.4 1.9s5.4-.9 5.4-1.9"/>',
  tree: '<circle cx="8" cy="3" r="1.6"/><circle cx="3.6" cy="12.4" r="1.6"/><circle cx="12.4" cy="12.4" r="1.6"/><path d="M8 4.6v3.2M8 7.8L4.4 11M8 7.8l3.6 3.2"/>',
  folder: '<path d="M2 4.2c0-.7.5-1.2 1.2-1.2h2.6l1.4 1.6h5.6c.7 0 1.2.5 1.2 1.2v4.8c0 .7-.5 1.2-1.2 1.2H3.2c-.7 0-1.2-.5-1.2-1.2z"/>',
  branch: '<circle cx="4.4" cy="3.4" r="1.7"/><circle cx="4.4" cy="12.6" r="1.7"/><circle cx="11.6" cy="8" r="1.7"/><path d="M4.4 5.1v5.8M6 8h3.9"/>',
  cache: '<rect x="2" y="2.4" width="12" height="4.4" rx="1"/><rect x="2" y="8" width="12" height="4.4" rx="1"/><circle cx="4.4" cy="4.6" r=".7" fill="currentColor"/><circle cx="4.4" cy="10.2" r=".7" fill="currentColor"/>',
  daemon: '<circle cx="8" cy="8" r="5.6"/><circle cx="8" cy="8" r="1.8"/><path d="M8 2.4v2M8 11.6v2M2.4 8h2M11.6 8h2"/>',
  memory: '<path d="M8 2.2l5 2.6v4.4L8 13.8l-5-4.6V4.8z"/><path d="M8 8V2.4M3.4 5L8 7.6l4.6-2.6"/>',
  config: '<circle cx="8" cy="8" r="2.2"/><path d="M8 1.8v1.8M8 12.4v1.8M1.8 8h1.8M12.4 8h1.8M3.6 3.6l1.3 1.3M11.1 11.1l1.3 1.3M12.4 3.6l-1.3 1.3M4.9 11.1l-1.3 1.3"/>',
  doctor: '<path d="M6.4 2h3.2v3.2h3.2v3.2h-3.2V12H6.4V8.4H3.2V5.2h3.2z"/><rect x="2.4" y="11.4" width="11.2" height="1.8" rx=".9"/>',
  bench: '<line x1="2.4" y1="13.4" x2="13.6" y2="13.4"/><rect x="3.4" y="8" width="2.2" height="5.4"/><rect x="6.9" y="5.4" width="2.2" height="8"/><rect x="10.4" y="3" width="2.2" height="10.4"/>',
  mcp: '<rect x="2" y="2" width="12" height="12" rx="2.5"/><path d="M5.4 6.2l-1.6 1.8 1.6 1.8M10.6 6.2l1.6 1.8-1.6 1.8M9 5.4l-2 5.2"/>',
  file: '<path d="M4 1.8h5.4L13 5.4V14H4z"/><path d="M9.2 2v3.6H13"/>',
};
function icon(name) {
  return `<svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${IC[name] || IC.file}</svg>`;
}

/* ---------------- utilities ---------------- */
function esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}
function fmtBytes(n) {
  if (n == null || isNaN(n)) return '—';
  const u = ['B', 'KiB', 'MiB', 'GiB'];
  let i = 0, v = Number(n);
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${u[i]}`;
}
function fmtDate(s) {
  if (!s) return '—';
  const d = new Date(s);
  return isNaN(d) ? String(s) : d.toLocaleString('en-US', { dateStyle: 'medium', timeStyle: 'short' });
}
function timeAgo(s) {
  if (!s) return '';
  const d = new Date(s).getTime();
  if (isNaN(d)) return '';
  const mins = Math.max(0, Math.round((Date.now() - d) / 60000));
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins} min ago`;
  const h = Math.floor(mins / 60);
  if (h < 24) return `${h} h ago`;
  return `${Math.floor(h / 24)} d ago`;
}
function $(sel, root) { return (root || document).querySelector(sel); }
function $$sel(sel, root) { return Array.from((root || document).querySelectorAll(sel)); }

const LANG_COLORS = {
  go: '#7cc7f5', typescript: '#6ea8fe', javascript: '#e8c84b', python: '#8fb8f5',
  rust: '#e09a6c', markdown: '#a7bcb1', json: '#b8a6e8', yaml: '#e8a6b8',
  css: '#8fd0c2', shell: '#9fe08f', toml: '#c9c9c9', unknown: '#6f8479',
};
function langDot(lang) {
  const c = LANG_COLORS[lang] || LANG_COLORS.unknown;
  return `<span class="langdot" style="background:${c}"></span>`;
}
function scoreHtml(score) {
  if (score == null || isNaN(score)) return '';
  const pct = Math.max(0, Math.min(100, Math.round(Number(score) * 100)));
  return `<span class="score" title="score ${Number(score).toFixed(3)}"><span class="bar"><i style="width:${pct}%"></i></span>${Number(score).toFixed(2)}</span>`;
}
function pillFor(status) {
  const map = { ok: 'ok', ready: 'ok', fresh: 'ok', warn: 'warn', stale: 'warn', error: 'err', err: 'err', info: '', degraded: 'warn' };
  return map[status] || '';
}

/* ---------------- toast / modal ---------------- */
function toast(msg, kind) {
  const root = $('#toast-root');
  const div = document.createElement('div');
  div.className = 'toast ' + (kind || '');
  div.textContent = msg;
  root.appendChild(div);
  setTimeout(() => { div.style.opacity = '0'; setTimeout(() => div.remove(), 300); }, 3600);
}
function copyText(s, label) {
  navigator.clipboard.writeText(String(s)).then(
    () => toast((label || 'Text') + ' copied', 'ok'),
    () => toast('Could not copy', 'err')
  );
}
function modal(html, opts = {}) {
  const root = $('#modal-root');
  root.innerHTML = `<div class="modal-veil"><div class="modal" role="dialog" aria-modal="true">
    <div class="modal-head"><h2>${opts.title || ''}</h2><button class="x" aria-label="Close">×</button></div>
    <div class="modal-body"></div>
    ${opts.footer === null ? '' : '<div class="modal-foot"></div>'}
  </div></div>`;
  const veil = root.firstElementChild;
  $('.modal-body', veil).innerHTML = html;
  const close = () => { root.innerHTML = ''; document.removeEventListener('keydown', onKey); };
  const onKey = (e) => { if (e.key === 'Escape') close(); };
  document.addEventListener('keydown', onKey);
  $('.x', veil).onclick = close;
  veil.addEventListener('mousedown', (e) => { if (e.target === veil) close(); });
  return { veil, close, foot: $('.modal-foot', veil) };
}
function confirmModal({ title, body, okLabel, danger }) {
  return new Promise((resolve) => {
    const m = modal(`<p class="note" style="margin-top:0">${body}</p>`, { title });
    m.foot.innerHTML = `<button class="btn ghost" data-x="no">Cancel</button>
      <button class="btn ${danger ? 'danger' : 'primary'}" data-x="yes">${esc(okLabel || 'Confirm')}</button>`;
    $('[data-x="no"]', m.foot).onclick = () => { m.close(); resolve(false); };
    $('[data-x="yes"]', m.foot).onclick = () => { m.close(); resolve(true); };
  });
}
async function codeModal(filePath, opts = {}) {
  const start = Math.max(1, (opts.start || 1) - (opts.context == null ? 8 : opts.context));
  const end = (opts.end || opts.start || 1) + (opts.context == null ? 8 : opts.context);
  const m = modal('<p class="note">Loading…</p>', { title: esc(filePath) });
  m.foot.innerHTML = `<button class="btn ghost sm" data-x="open">Open in editor</button>
    <button class="btn ghost sm" data-x="reveal">Show in folder</button>
    <button class="btn ghost sm" data-x="copy">Copy path</button>`;
  $('[data-x="open"]', m.foot).onclick = () => api.shellOpen(filePath);
  $('[data-x="reveal"]', m.foot).onclick = () => api.shellShow(filePath);
  $('[data-x="copy"]', m.foot).onclick = () => copyText(filePath, 'Path');
  try {
    const r = await api.fileRead(filePath, start, end);
    const hs = opts.start || 0, he = opts.end || 0;
    const rows = r.lines.map((ln, i) => {
      const n = r.start + i;
      const hit = hs && n >= hs && n <= he ? ' hit' : '';
      return `<div class="ln${hit}"><span>${n}</span><span>${esc(ln) || ' '}</span></div>`;
    }).join('');
    $('.modal-body', m.veil).innerHTML = `<div class="codeview">${rows}</div>
      <p class="note">Lines ${r.start}–${r.end} of ${r.total}${opts.start ? ` · match at ${opts.start}–${opts.end}` : ''}</p>`;
    const hit = $('.ln.hit', m.veil);
    if (hit) hit.scrollIntoView({ block: 'center' });
  } catch (e) {
    $('.modal-body', m.veil).innerHTML = `<div class="callout err">${esc(String(e.message || e))}</div>`;
  }
}

/* ---------------- backend calls ---------------- */
let busyCount = 0;
function setBusy(label) {
  busyCount++;
  $('#busy').classList.remove('hidden');
  if (label) $('#busy-label').textContent = label;
}
function clearBusy() {
  busyCount = Math.max(0, busyCount - 1);
  if (busyCount === 0) $('#busy').classList.add('hidden');
}
async function cli(args, label) {
  setBusy(label);
  try {
    return await api.cli(args);
  } finally { clearBusy(); }
}
async function cliJson(args, label) {
  setBusy(label);
  try {
    return await api.cliJson(args);
  } finally { clearBusy(); }
}
async function mcp(tool, mcpArgs, label, timeoutMs) {
  setBusy(label);
  try {
    return await api.mcpCall(tool, mcpArgs || {}, timeoutMs ? { timeoutMs } : {});
  } finally { clearBusy(); }
}
function showCliError(container, r, action) {
  const detail = esc((r.stderr || r.stdout || `exit ${r.code}`).trim().slice(0, 1200) || 'unknown error');
  container.innerHTML = `<div class="callout err"><strong>${esc(action)} failed</strong> (exit ${r.code}).<pre class="block">${detail}</pre></div>`;
}

/* ---------------- view components ---------------- */
function statCard(k, v, d) {
  return `<div class="stat"><div class="k">${esc(k)}</div><div class="v">${v}</div>${d ? `<div class="d">${d}</div>` : ''}</div>`;
}
function kvRows(obj) {
  return `<dl class="kv">` + Object.entries(obj).map(([k, v]) => `<dt>${esc(k)}</dt><dd>${esc(v)}</dd>`).join('') + `</dl>`;
}
function resultCard(r, idx) {
  const rel = r.relative_path || r.file_path || '';
  const abs = r.file_path || rel;
  const lang = r.language || 'unknown';
  const lines = (r.start_line != null) ? `${r.start_line}–${r.end_line}` : '';
  const sym = r.symbol_name ? `<span class="tag">${esc(r.symbol_name)}</span>` : '';
  const sel = r.selector && r.selector.kind ? `<span class="tag">${esc(r.selector.kind)}</span>` : '';
  return `<article class="result" data-i="${idx}">
    <div class="result-head">${langDot(lang)}<span class="path" title="${esc(abs)}">${esc(rel)}</span>
      <span class="meta">${esc([r.chunk_type, lines].filter(Boolean).join(' · '))}</span>
      <span class="sp"></span>${scoreHtml(r.score)}<span class="meta">#${r.chunk_id}</span></div>
    <pre>${esc(r.content || '')}</pre>
    <div class="result-foot">${sym}${sel}
      <span style="flex:1"></span>
      <button class="btn ghost sm" data-act="ctx">View context</button>
      <button class="btn ghost sm" data-act="open">Open</button>
      <button class="btn ghost sm" data-act="similar">Similar</button>
      <button class="btn ghost sm" data-act="copy">Copy path</button>
    </div></article>`;
}
function wireResults(container, results, opts = {}) {
  $$sel('.result', container).forEach((card) => {
    const r = results[Number(card.dataset.i)];
    if (!r) return;
    const abs = r.file_path || r.relative_path;
    $('[data-act="ctx"]', card).onclick = () => codeModal(abs, { start: r.start_line, end: r.end_line });
    $('[data-act="open"]', card).onclick = () => api.shellOpen(abs);
    $('[data-act="similar"]', card).onclick = () => {
      location.hash = '#/similar?chunk=' + encodeURIComponent(r.chunk_id);
    };
    $('[data-act="copy"]', card).onclick = () => copyText(abs, 'Path');
  });
}
function streamConsole(id) {
  return `<pre class="console" id="console-${id}"></pre>
    <div class="btn-row"><button class="btn danger sm" id="kill-${id}" disabled>Stop</button>
    <button class="btn ghost sm" id="clear-${id}">Clear</button>
    <button class="btn ghost sm" id="copy-${id}">Copy output</button></div>`;
}
function wireStream(id, args, opts = {}) {
  const con = $('#console-' + id);
  const killBtn = $('#kill-' + id);
  $('#clear-' + id).onclick = () => { con.textContent = ''; };
  $('#copy-' + id).onclick = () => copyText(con.textContent, 'Output');
  let running = false;
  const offData = api.onStreamData((p) => {
    if (p.id !== id) return;
    con.textContent += p.text;
    con.scrollTop = con.scrollHeight;
  });
  const offExit = api.onStreamExit((p) => {
    if (p.id !== id) return;
    running = false;
    killBtn.disabled = true;
    con.textContent += `\n— process exited (${p.code}) —\n`;
    con.scrollTop = con.scrollHeight;
    if (opts.onExit) opts.onExit(p);
    offData(); offExit();
  });
  return {
    async start() {
      if (running) return;
      running = true;
      killBtn.disabled = false;
      con.textContent += `$ vecgrep ${args.join(' ')}\n`;
      await api.streamStart(id, args);
      if (opts.onStart) opts.onStart();
    },
    kill() { api.streamKill(id); },
  };
}

/* ---------------- navigation ---------------- */
const NAV = [
  { section: 'Work', items: [
    ['dashboard', 'Dashboard', 'panel'],
    ['search', 'Search', 'search'],
    ['similar', 'Similar', 'similar'],
    ['batch', 'Batch & Investigate', 'batch'],
  ]},
  { section: 'Index', items: [
    ['index', 'Manage index', 'index'],
    ['structure', 'Structure', 'tree'],
    ['branches', 'Branches', 'branch'],
    ['cache', 'Cache', 'cache'],
    ['daemon', 'Daemon', 'daemon'],
  ]},
  { section: 'Project', items: [
    ['projects', 'Projects', 'folder'],
    ['memory', 'Memory', 'memory'],
    ['settings', 'Settings', 'config'],
    ['doctor', 'Doctor', 'doctor'],
    ['benchmark', 'Benchmark', 'bench'],
    ['mcp', 'MCP', 'mcp'],
  ]},
];
function buildNav() {
  $('#nav').innerHTML = NAV.map((s) =>
    `<div class="nav-section"><h3>${esc(s.section)}</h3>` +
    s.items.map(([route, label, ic]) =>
      `<button class="nav-item" data-route="${route}">${icon(ic)}<span>${esc(label)}</span></button>`
    ).join('') + `</div>`
  ).join('');
  $$sel('.nav-item').forEach((b) => { b.onclick = () => { location.hash = '#/' + b.dataset.route; }; });
}
function parseHash() {
  const h = (location.hash || '#/dashboard').replace(/^#\//, '');
  const [route, qs] = h.split('?');
  return { route: Views[route] ? route : 'dashboard', params: new URLSearchParams(qs || '') };
}
async function render() {
  const { route, params } = parseHash();
  S.route = route;
  $$sel('.nav-item').forEach((b) => b.classList.toggle('active', b.dataset.route === route));
  const view = $('#view');
  view.innerHTML = '<div class="wrap"><p class="note">Loading…</p></div>';
  try {
    await Views[route](view, params);
  } catch (e) {
    view.innerHTML = `<div class="wrap"><div class="callout err">Error loading view: ${esc(String(e.message || e))}</div></div>`;
  }
  view.scrollTop = 0;
}

/* ---------------- topbar: project + status ---------------- */
async function refreshProjects(selectRoot) {
  const r = await api.cli(['projects', 'list']);
  let list = [];
  if (r.code === 0) {
    // Same parsing as bridge.parseProjectsList
    let cur = null;
    for (const line of r.stdout.split(/\r?\n/)) {
      if (/^Registered projects/.test(line) || !line.trim()) continue;
      if (/^  \S/.test(line) && !line.startsWith('    ')) { cur = { name: line.trim(), path: '', data: '' }; list.push(cur); continue; }
      if (cur) {
        const m = line.match(/^\s*(Path|Data):\s*(.+)$/);
        if (m) { if (m[1] === 'Path') cur.path = m[2].trim(); else cur.data = m[2].trim(); }
      }
    }
  }
  S.projects = list;
  const sel = $('#proj-select');
  sel.innerHTML = '';
  const roots = new Set();
  for (const p of list) {
    if (!p.path || roots.has(p.path)) continue;
    roots.add(p.path);
    const o = document.createElement('option');
    o.value = p.path;
    o.textContent = `${p.name} — ${p.path}`;
    sel.appendChild(o);
  }
  const current = selectRoot || S.root;
  if (current && !roots.has(current)) {
    const o = document.createElement('option');
    o.value = current;
    o.textContent = `(unregistered) — ${current}`;
    sel.appendChild(o);
  }
  sel.value = current || '';
}
async function setProject(root, opts = {}) {
  await api.projectSet(root);
  S.root = root;
  S.statusCache = null;
  $('#proj-select').value = root;
  await refreshTopStatus();
  if (!opts.silent) toast('Active project: ' + root, 'ok');
  render();
}
async function refreshTopStatus() {
  const pp = $('#provider-pill'), hp = $('#health-pill');
  pp.className = 'pill'; pp.textContent = 'provider…';
  hp.className = 'pill'; hp.textContent = 'index…';
  // Lightweight on purpose: a full status can block ~8s while our own MCP
  // server holds the VecLite handle. Its profile fields are untrusted (it
  // cannot see the metadata-stored profile), so mismatch is only reported
  // by the Dashboard's full status.
  const r = await api.cliJson(['status', '--format', 'json', '--lightweight']);
  if (r.code !== 0 || !r.data) {
    hp.className = 'pill err'; hp.textContent = 'index: error';
    pp.className = 'pill warn'; pp.textContent = 'no status';
    return;
  }
  const d = r.data;
  S.statusCache = d; S.statusAt = Date.now();
  pp.className = 'pill live';
  pp.textContent = `${d.provider || '?'} / ${d.embedding_model || '?'}`;
  pp.title = `dimensions: ${d.dimensions} (lightweight)`;
  const fresh = d.index_fresh === true;
  if (fresh) { hp.className = 'pill ok'; hp.textContent = `fresh · ${d.stats.chunks} chunks`; }
  else { hp.className = 'pill warn'; hp.textContent = `stale · ${d.stats.chunks} chunks`; }
  hp.title = `files: ${d.stats.files} · last indexed: ${fmtDate(d.latest_indexed_at)} (lightweight)`;
  // binary + mcp in the footer
  $('#bin-dot').className = 'dot ok';
  $('#bin-label').textContent = S.binary ? `${S.binary.version}` : 'binary ok';
  $('#bin-label').title = S.binary ? S.binary.path : '';
  try {
    const ms = await api.mcpStatus();
    const mine = ms.find((x) => x.root === S.root);
    if (mine && mine.running) {
      $('#mcp-dot').className = 'dot live';
      $('#mcp-label').textContent = 'mcp running';
    } else {
      $('#mcp-dot').className = 'dot';
      $('#mcp-label').textContent = 'mcp idle';
    }
  } catch { $('#mcp-dot').className = 'dot err'; $('#mcp-label').textContent = 'mcp error'; }
}

/* ---------------- boot ---------------- */
window.addEventListener('hashchange', render);
document.addEventListener('DOMContentLoaded', async () => {
  buildNav();
  S.binary = await api.binary();
  $('#brand-version').textContent = S.binary.version || '';
  $('#bin-label').textContent = S.binary.version || 'binary';
  const p = await api.projectGet();
  S.root = p.root;
  await refreshProjects(S.root);
  $('#proj-select').onchange = (e) => setProject(e.target.value);
  $('#proj-browse').onclick = async () => {
    const r = await api.pickDirectory();
    if (!r.canceled && r.path) setProject(r.path);
  };
  $('#proj-refresh').onclick = () => { S.statusCache = null; refreshTopStatus(); render(); };
  await refreshTopStatus();
  // Gentle periodic status refresh (every 30 s)
  setInterval(() => { if (!document.hidden) refreshTopStatus(); }, 30000);
  render();
});
