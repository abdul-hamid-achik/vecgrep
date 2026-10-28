'use strict';
/* Dashboard: overall project status, index freshness and quick actions. */
Views.dashboard = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Dashboard</h1><p>Active project status with shortcuts to everything else.</p></div>
    <div id="p-stats" class="grid4"></div>
    <div class="grid2"><div id="p-left"></div><div id="p-right"></div></div>
    <div id="p-ready"></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  const [st, doc] = await Promise.all([
    cliJson(['status', '--format', 'json'], 'reading status'),
    cliJson(['doctor', '--no-ping', '-f', 'json'], 'diagnosing'),
  ]);

  if (st.code !== 0 || !st.data) {
    $('#p-stats', wrap).innerHTML = '';
    showCliError($('#p-left', wrap), st, 'Read status');
    $('#p-right', wrap).innerHTML = `<div class="card"><h2>Project not initialized?</h2>
      <p class="sub">If this folder is not a vecgrep project yet, initialize it.</p>
      <div class="btn-row"><button class="btn primary" id="p-init">Initialize here (vecgrep init)</button>
      <a class="btn ghost" href="#/projects">View projects</a></div></div>`;
    $('#p-init', wrap).onclick = async () => {
      const r = await cli(['init'], 'initializing');
      if (r.code === 0) { toast('Project initialized', 'ok'); refreshTopStatus(); Views.dashboard(view); }
      else showCliError($('#p-right', wrap), r, 'Initialize');
    };
    return;
  }
  const d = st.data;
  const fresh = d.index_fresh === true;
  const mismatch = d.profile_matches === false;

  $('#p-stats', wrap).innerHTML =
    statCard('Chunks', Number(d.stats.chunks).toLocaleString('en-US')) +
    statCard('Files', Number(d.stats.files).toLocaleString('en-US')) +
    statCard('Index size', fmtBytes(d.veclite_bytes), esc(d.vector_backend || '')) +
    statCard('Model', esc(d.embedding_model || '—'), esc(`${d.provider || '?'} · ${d.dimensions || '?'} dims`));

  const langRows = Object.entries(d.languages || {}).sort((a, b) => b[1] - a[1]).slice(0, 12);
  const maxLang = langRows.length ? langRows[0][1] : 1;
  $('#p-left', wrap).innerHTML = `<div class="card"><h2>Index</h2>
    <p class="sub">${esc(d.project_root || '')}</p>
    ${mismatch ? '<div class="callout err"><strong>The embedding profile changed.</strong> Reindex with <code>index --full</code> to search again.</div>'
      : fresh ? '<div class="callout ok">Index is fresh. You can search with confidence.</div>'
      : '<div class="callout warn"><strong>Stale index.</strong> There are unindexed changes since ' + esc(fmtDate(d.latest_indexed_at)) + '.</div>'}
    ${kvRows({
      'Last indexed': `${fmtDate(d.latest_indexed_at)} (${timeAgo(d.latest_indexed_at)})`,
      'Database': d.database || '—',
      'Profile': (d.current_profile && d.current_profile.profile_id) || '—',
      'Stored profile': d.profile_status || '—',
    })}
    <div class="btn-row">
      <button class="btn primary" id="p-index">Index now</button>
      <button class="btn" id="p-ensure">Ensure</button>
      <button class="btn ghost sm" id="p-light" title="Health without opening VecLite (status --lightweight)">Lightweight status</button>
      <a class="btn ghost" href="#/index">Indexing options</a>
    </div></div>
    <div class="card"><h2>Languages</h2><p class="sub">Chunks per language in the index.</p>
      ${langRows.map(([l, n]) => `<div class="bar-row"><span class="lbl">${langDot(l)}${esc(l)}</span>
        <span class="track"><i style="width:${Math.round((n / maxLang) * 100)}%"></i></span>
        <span class="num">${Number(n).toLocaleString('en-US')}</span></div>`).join('') || '<p class="note">No data.</p>'}
    </div>`;

  const checks = (doc.code === 0 && doc.data && doc.data.checks) || [];
  const failed = checks.filter((c) => c.status === 'error' || c.status === 'fail').length;
  const warned = checks.filter((c) => c.status === 'warn').length;
  $('#p-right', wrap).innerHTML = `<div class="card"><h2>Doctor</h2>
    <p class="sub">${failed ? `<span class="pill err">${failed} failures</span>` : '<span class="pill ok">no failures</span>'}
    ${warned ? ` <span class="pill warn">${warned} warnings</span>` : ''}</p>
    <table class="tbl"><tbody>${checks.map((c) =>
      `<tr><td class="mono">${esc(c.name)}</td><td><span class="pill ${pillFor(c.status)}">${esc(c.status)}</span></td>
      <td>${esc(c.detail || '')}${c.hint ? `<div class="note">${esc(c.hint)}</div>` : ''}</td></tr>`).join('') ||
      '<tr><td colspan="3">No diagnostics.</td></tr>'}</tbody></table>
    <div class="btn-row"><a class="btn ghost sm" href="#/doctor">Open doctor</a></div></div>
    <div class="card"><h2>Quick actions</h2><p class="sub">Most used, one click away.</p>
    <div class="btn-row">
      <a class="btn sm" href="#/search">Search</a>
      <a class="btn sm" href="#/batch">Investigate symbol</a>
      <a class="btn sm" href="#/structure">View structure</a>
      <a class="btn sm" href="#/memory">Memory</a>
      <a class="btn sm" href="#/benchmark">Benchmark</a>
      <a class="btn sm" href="#/mcp">MCP console</a>
    </div></div>`;

  $('#p-light', wrap).onclick = async () => {
    const r = await cliJson(['status', '--format', 'json', '--lightweight'], 'lightweight status');
    const m = modal(r.code === 0 ? `<pre class="block">${esc(JSON.stringify(r.data, null, 2))}</pre>`
      : `<div class="callout err">${esc((r.stderr || 'failed').slice(0, 800))}</div>`, { title: 'status --lightweight', footer: null });
    void m;
  };
  $('#p-index', wrap).onclick = async () => {
    const ok = await confirmModal({ title: 'Index project', okLabel: 'Index',
      body: 'Runs <code>vecgrep index --yes</code> on the active project. Only changed files get embedded.' });
    if (!ok) return;
    location.hash = '#/index?run=1';
  };
  $('#p-ensure', wrap).onclick = async () => {
    const r = await mcp('vecgrep_ensure', { mode: 'index_if_empty' }, 'ensuring index', 300000);
    toast(r.ok ? 'Ensure completed' : 'Ensure: ' + (r.error || r.text).slice(0, 160), r.ok ? 'ok' : 'err');
    refreshTopStatus();
    Views.dashboard(view);
  };

  // MCP readiness (state + recommended action)
  const er = await mcp('vecgrep_ensure', { mode: 'check' }, 'checking readiness');
  const box = $('#p-ready', wrap);
  if (er.ok && er.structured) {
    const s = er.structured;
    box.innerHTML = `<div class="card"><h2>Readiness</h2>
      <p class="sub">What an agent would see before searching.</p>
      ${kvRows({ state: s.state || '—', indexed: String(s.indexed), fresh: String(s.fresh),
        chunks: s.chunks, profile_ok: String(s.profile_matches), action: s.action || '—', reason: s.reason || '—' })}</div>`;
  }
};
