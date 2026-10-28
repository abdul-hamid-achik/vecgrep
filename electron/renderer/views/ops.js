'use strict';
/* Cache: status, fcheap save/restore/sweep. */
Views.cache = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Cache</h1>
    <p>On-disk embedding cache and its fcheap snapshots. Command <code>cache</code>.</p></div>
    <div class="card"><h2>Status</h2><div id="st"></div></div>
    <div class="grid3">
      <div class="card"><h2>Save</h2><p class="sub">Manual snapshot to fcheap (<code>cache save</code>).</p>
        <div class="btn-row"><button class="btn" id="save">Save</button></div><div id="save-out"></div></div>
      <div class="card"><h2>Restore</h2><p class="sub">Recovers the latest snapshot (<code>cache restore</code>).</p>
        <div class="btn-row"><button class="btn" id="restore">Restore</button></div><div id="restore-out"></div></div>
      <div class="card"><h2>Sweep</h2><p class="sub">Deletes old snapshots (<code>cache sweep</code>).</p>
        <div class="btn-row"><button class="btn danger" id="sweep">Sweep…</button></div><div id="sweep-out"></div></div>
    </div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  async function load() {
    const box = $('#st', wrap);
    const r = await cli(['cache', 'status'], 'cache status');
    box.innerHTML = r.code === 0 ? `<pre class="block">${esc(r.stdout.slice(0, 4000))}</pre>` : '';
    if (r.code !== 0) showCliError(box, r, 'Cache status');
  }
  load();
  const run = (btn, out, args, confirm) => {
    $(btn, wrap).onclick = async () => {
      if (confirm) {
        const ok = await confirmModal({ title: confirm.t, okLabel: confirm.ok, danger: true, body: confirm.b });
        if (!ok) return;
      }
      const box = $(out, wrap);
      const r = await cli(args, args.join(' '));
      box.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
      if (r.code !== 0) showCliError(box, r, args.join(' '));
      else load();
    };
  };
  run('#save', '#save-out', ['cache', 'save']);
  run('#restore', '#restore-out', ['cache', 'restore']);
  run('#sweep', '#sweep-out', ['cache', 'sweep'],
    { t: 'Sweep cache', ok: 'Sweep', b: 'Deletes old or superseded cache snapshots in fcheap.' });
};

/* Daemon: background indexing hub. */
Views.daemon = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Daemon</h1>
    <p>Background hub: one process serves many projects over a global unix socket, watches files and is the sole index writer. Command <code>daemon</code>.</p></div>
    <div class="card"><h2>Status</h2><div id="st"></div>
      <div class="btn-row"><button class="btn ghost sm" id="reload">Refresh</button></div></div>
    <div class="grid2">
      <div class="card"><h2>Start</h2>
        <p class="sub"><code>daemon start</code> runs in the foreground; the app launches it in the background and shows live output.</p>
        <div class="btn-row"><button class="btn primary" id="start">Start daemon</button></div>
        <div style="margin-top:10px">${streamConsole('daemon')}</div></div>
      <div class="card"><h2>Stop</h2>
        <p class="sub">Stops the hub (<code>daemon stop</code>).</p>
        <div class="btn-row"><button class="btn danger" id="stop">Stop daemon…</button></div><div id="stop-out"></div></div>
    </div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  async function load() {
    const box = $('#st', wrap);
    const r = await cli(['daemon', 'status'], 'daemon status');
    box.innerHTML = r.code === 0 ? `<pre class="block">${esc(r.stdout.slice(0, 6000))}</pre>` : '';
    if (r.code !== 0) showCliError(box, r, 'Daemon status');
  }
  load();
  $('#reload', wrap).onclick = load;
  $('#start', wrap).onclick = async () => {
    const s = wireStream('daemon', ['daemon', 'start'], { onExit: () => load() });
    s.start();
    toast('Daemon launched in the background', 'ok');
    setTimeout(load, 2500);
  };
  $('#stop', wrap).onclick = async () => {
    const ok = await confirmModal({ title: 'Stop daemon', okLabel: 'Stop', danger: true,
      body: 'Stops the indexing hub. The CLI and MCP keep working in direct mode.' });
    if (!ok) return;
    const out = $('#stop-out', wrap);
    const r = await cli(['daemon', 'stop'], 'stopping daemon');
    out.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Daemon stop');
    else load();
  };
};
