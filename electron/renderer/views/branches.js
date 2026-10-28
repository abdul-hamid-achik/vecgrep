'use strict';
/* Branches: per-branch git indexes + snapshot + hook. */
Views.branches = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Branches</h1>
    <p>Each git branch keeps its own index so results never mix. Command <code>branch</code> (+ <code>vecgrep_branch_status</code> via MCP).</p></div>
    <div class="card"><h2>Status</h2><div id="st"></div>
      <div class="btn-row"><button class="btn ghost sm" id="via-mcp">View via MCP</button></div><div id="mcp-out"></div></div>
    <div class="grid2">
      <div class="card"><h2>Switch branch</h2>
        <p class="sub">Activates the index of the current branch, or the given one (<code>branch switch</code>).</p>
        <div class="field"><label for="sw">Branch (empty = current)</label><input type="text" id="sw" class="mono" placeholder="feature/x"></div>
        <div class="btn-row"><button class="btn primary" id="sw-go">Switch</button></div><div id="sw-out"></div></div>
      <div class="card"><h2>Snapshot</h2>
        <p class="sub">Saves the current branch index to fcheap (<code>branch snapshot</code>).</p>
        <div class="btn-row"><button class="btn" id="snap-go">Create snapshot</button></div><div id="snap-out"></div></div>
    </div>
    <div class="grid2">
      <div class="card"><h2>post-checkout hook</h2>
        <p class="sub">Switches the index automatically when you switch git branches.</p>
        <div class="btn-row"><button class="btn" id="hook-go">Install hook</button></div><div id="hook-out"></div></div>
      <div class="card"><h2>Prune</h2>
        <p class="sub">Deletes index snapshots of branches that no longer exist (<code>branch prune</code>).</p>
        <div class="btn-row"><button class="btn danger" id="prune-go">Prune branches…</button></div><div id="prune-out"></div></div>
    </div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  async function loadStatus() {
    const box = $('#st', wrap);
    const r = await cli(['branch', 'status'], 'branch status');
    box.innerHTML = r.code === 0
      ? `<pre class="block">${esc(r.stdout.slice(0, 6000))}</pre>` : '';
    if (r.code !== 0) showCliError(box, r, 'Branch status');
  }
  loadStatus();

  $('#via-mcp', wrap).onclick = async () => {
    const out = $('#mcp-out', wrap);
    const r = await mcp('vecgrep_branch_status', {}, 'branch status via MCP');
    out.innerHTML = r.ok
      ? `<pre class="block">${esc(r.text.slice(0, 8000))}</pre>`
      : `<div class="callout err">${esc((r.error || r.text).slice(0, 800))}</div>`;
  };
  $('#sw-go', wrap).onclick = async () => {
    const out = $('#sw-out', wrap);
    const b = $('#sw', wrap).value.trim();
    const r = await cli(b ? ['branch', 'switch', b] : ['branch', 'switch'], 'switching branch');
    out.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Branch switch');
    else { loadStatus(); refreshTopStatus(); }
  };
  $('#snap-go', wrap).onclick = async () => {
    const out = $('#snap-out', wrap);
    const r = await cli(['branch', 'snapshot'], 'branch snapshot');
    out.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Branch snapshot');
  };
  $('#hook-go', wrap).onclick = async () => {
    const out = $('#hook-out', wrap);
    const r = await cli(['branch', 'install-hook'], 'installing hook');
    out.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Install hook');
  };
  $('#prune-go', wrap).onclick = async () => {
    const ok = await confirmModal({ title: 'Prune branches', okLabel: 'Prune', danger: true,
      body: 'Deletes index snapshots of branches already deleted in git.' });
    if (!ok) return;
    const out = $('#prune-out', wrap);
    const r = await cli(['branch', 'prune'], 'pruning branches');
    out.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Branch prune');
    else loadStatus();
  };
};
