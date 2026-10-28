'use strict';
/* MCP: tool catalog, live console and client snippets. */
const TOOL_GROUPS = [
  { title: 'Lifecycle', tools: ['vecgrep_init', 'vecgrep_index', 'vecgrep_ensure', 'vecgrep_status', 'vecgrep_reset', 'vecgrep_clean', 'vecgrep_branch_status'] },
  { title: 'Retrieval', tools: ['vecgrep_search', 'vecgrep_batch_search', 'vecgrep_similar', 'vecgrep_investigate'] },
  { title: 'Structure', tools: ['vecgrep_overview', 'vecgrep_related_files', 'vecgrep_delete'] },
  { title: 'Global memory', tools: ['memory_remember', 'memory_recall', 'memory_forget', 'memory_stats'] },
];

Views.mcp = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>MCP</h1>
    <p><code>vecgrep serve --mcp</code> (stdio) server: tool catalog, console to call tools, and client setup. The app already uses this server internally for overview, batch, related, investigate, ensure and advanced memory.</p></div>
    <div class="card"><h2>Server</h2><div id="srv"></div>
      <div class="btn-row"><button class="btn ghost sm" id="srv-reload">Refresh</button></div></div>
    <div class="card"><h2>Tool catalog</h2><p class="sub">The registered server list is the contract; each description is authoritative.</p><div id="cat"></div></div>
    <div class="card"><h2>Console</h2>
      <p class="sub">Call any tool against the active project.</p>
      <div class="form-row">
        <div class="field"><label for="tool">Tool</label><select id="tool"></select></div>
        <div class="field" style="flex:2"><label for="args">Arguments (JSON)</label><input type="text" id="args" class="mono" value="{}"></div>
      </div>
      <div class="btn-row"><button class="btn primary" id="call">Call</button></div><div id="call-out"></div></div>
    <div class="card"><h2>Shell completion</h2>
      <p class="sub">Generates the <code>vecgrep completion</code> script for your shell; install it where appropriate.</p>
      <div class="seg" id="shell"><button data-s="bash" class="on">bash</button><button data-s="zsh">zsh</button><button data-s="fish">fish</button><button data-s="powershell">powershell</button></div>
      <div class="btn-row"><button class="btn primary" id="comp-go">Generate</button>
        <button class="btn ghost" id="comp-copy" disabled>Copy</button>
        <button class="btn ghost" id="comp-save" disabled>Save…</button></div>
      <div id="comp-out"></div>
      <p class="hint">bash: <code>source &lt;(vecgrep completion bash)</code> · zsh: add to a dir on your <code>fpath</code> · fish: <code>vecgrep completion fish &gt; ~/.config/fish/completions/vecgrep.fish</code></p></div>
    <div class="card"><h2>Connect a client</h2>
      <p class="sub">Snippets to register vecgrep as an MCP server in your assistant. Adjust paths and API keys for your launcher (run <code>vecgrep doctor</code> from that same launcher to verify what it sees).</p>
      <div class="field"><label for="bin">Binary</label><input type="text" id="bin" class="mono" readonly></div>
      <div class="field"><label>Claude Desktop / Cursor / Windsurf (JSON)</label>
      <pre class="block" id="snip"></pre>
      <div class="btn-row"><button class="btn ghost sm" id="copy-snip">Copy</button></div></div></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);
  $('#bin', wrap).value = S.binary.path;
  const snippet = JSON.stringify({ mcpServers: { vecgrep: { command: S.binary.path, args: ['serve', '--mcp'], cwd: S.root } } }, null, 2);
  $('#snip', wrap).textContent = snippet;
  $('#copy-snip', wrap).onclick = () => copyText(snippet, 'Snippet');

  let shell = 'bash';
  $$sel('#shell button', wrap).forEach((b) => {
    b.onclick = () => { shell = b.dataset.s; $$sel('#shell button', wrap).forEach((x) => x.classList.toggle('on', x === b)); };
  });
  let compScript = '';
  $('#comp-go', wrap).onclick = async () => {
    const out = $('#comp-out', wrap);
    const r = await cli(['completion', shell], 'generating completion');
    if (r.code !== 0 || !r.stdout) { showCliError(out, r, 'Completion'); return; }
    compScript = r.stdout;
    out.innerHTML = `<pre class="block">${esc(r.stdout.slice(0, 4000))}${r.stdout.length > 4000 ? '\n… (trimmed; copy/save gets the full script)' : ''}</pre>`;
    $('#comp-copy', wrap).disabled = false;
    $('#comp-save', wrap).disabled = false;
  };
  $('#comp-copy', wrap).onclick = () => copyText(compScript, 'Script');
  $('#comp-save', wrap).onclick = async () => {
    const ext = { bash: 'bash', zsh: 'zsh', fish: 'fish', powershell: 'ps1' }[shell] || 'sh';
    const r = await api.pickSave({ title: 'Save completion script', defaultPath: `vecgrep-completion.${ext}` });
    if (!r.canceled && r.path) {
      await api.fileWrite(r.path, compScript);
      toast('Saved to ' + r.path, 'ok');
    }
  };

  async function loadSrv() {
    const box = $('#srv', wrap);
    try {
      const ms = await api.mcpStatus();
      const mine = ms.find((x) => x.root === S.root);
      box.innerHTML = kvRows({
        project: S.root,
        state: mine && mine.running ? `running (since ${mine.startedAt || '?'})` : 'idle (starts on demand)',
        server: mine && mine.server ? `${mine.server.name} ${mine.server.version || ''}` : '—',
        pools: String(ms.length),
      });
    } catch (e) {
      box.innerHTML = `<div class="callout err">${esc(String(e.message || e))}</div>`;
    }
  }
  loadSrv();
  $('#srv-reload', wrap).onclick = loadSrv;

  async function loadCat() {
    const box = $('#cat', wrap);
    box.innerHTML = '<p class="note">Listing tools…</p>';
    try {
      const tools = await api.mcpTools();
      const byName = Object.fromEntries(tools.map((t) => [t.name, t]));
      box.innerHTML = TOOL_GROUPS.map((g) =>
        `<h3 style="margin:12px 0 6px;font-size:13px">${esc(g.title)}</h3>
        <table class="tbl"><tbody>` + g.tools.map((n) => {
          const t = byName[n];
          if (!t) return `<tr><td class="mono"><strong>${esc(n)}</strong></td><td><span class="pill warn">not registered</span></td></tr>`;
          return `<tr><td class="mono" style="white-space:nowrap"><strong>${esc(n)}</strong></td><td>${esc(t.description || '')}</td></tr>`;
        }).join('') + `</tbody></table>`).join('');
      const sel = $('#tool', wrap);
      sel.innerHTML = tools.map((t) => `<option>${esc(t.name)}</option>`).join('');
      sel.onchange = () => { $('#args', wrap).value = '{}'; };
    } catch (e) {
      box.innerHTML = `<div class="callout err">${esc(String(e.message || e))}</div>`;
    }
  }
  loadCat();

  $('#call', wrap).onclick = async () => {
    const out = $('#call-out', wrap);
    let args;
    try {
      args = JSON.parse($('#args', wrap).value || '{}');
    } catch {
      out.innerHTML = '<div class="callout err">Arguments are not valid JSON.</div>';
      return;
    }
    const tool = $('#tool', wrap).value;
    out.innerHTML = '<p class="note">Calling…</p>';
    const r = await mcp(tool, args, 'MCP ' + tool, 300000);
    out.innerHTML = `<div class="card"><h2>${esc(tool)} ${r.ok ? '<span class="pill ok">ok</span>' : '<span class="pill err">error</span>'}</h2>
      <pre class="block">${esc((r.text || r.error || '(empty)').slice(0, 20000))}</pre>
      ${r.structured ? `<details class="raw"><summary>structuredContent</summary><pre class="block">${esc(JSON.stringify(r.structured, null, 2).slice(0, 8000))}</pre></details>` : ''}</div>`;
    loadSrv();
  };
};
