'use strict';
/* Memory: global agent-memory store (~/.vecai/memory). */
Views.memory = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Memory</h1>
    <p>Cross-project, cross-session memories with tags and importance. CLI <code>memory recall/remember</code> plus MCP <code>memory_*</code> (including <code>forget</code> and <code>stats</code>).</p></div>
    <div class="tabs" id="tabs">
      <button data-t="recall" class="on">Recall</button>
      <button data-t="remember">Remember</button>
      <button data-t="forget">Forget</button>
      <button data-t="stats">Stats</button>
    </div>
    <div id="t-recall"><div class="card">
      <div class="form-row">
        <div class="field" style="flex:3"><label for="q">Query</label><input type="search" id="q" class="mono" placeholder="what did we decide about auth"></div>
        <div class="field"><label for="limit">Limit</label><input type="number" id="limit" value="10" min="1" max="50"></div>
      </div>
      <div class="form-row">
        <div class="field"><label for="tags">Tags (comma, AND)</label><input type="text" id="tags" class="mono" placeholder="codemap,my-project"></div>
        <div class="field"><label for="minimp">Min importance (0–1)</label><input type="number" id="minimp" min="0" max="1" step="0.1" placeholder="0.3"></div>
      </div>
      <p class="hint">Tags filter with AND: only memories carrying <em>all</em> of them match. For project memories use <code>codemap,&lt;project_key&gt;</code> (G2 convention).</p>
      <div class="btn-row"><button class="btn primary" id="go">Recall</button>
        <button class="btn ghost" id="go-mcp" title="Via MCP (same ranking + links)">Via MCP</button></div></div>
      <div id="r-out"></div></div>
    <div id="t-remember" class="hidden"><div class="card">
      <div class="field"><label for="c">Content</label><textarea id="c" class="mono" placeholder="we decided to use X because Y"></textarea></div>
      <div class="form-row">
        <div class="field"><label for="imp">Importance (0–1)</label><input type="number" id="imp" value="0.5" min="0" max="1" step="0.1"></div>
        <div class="field"><label for="rtags">Tags (comma)</label><input type="text" id="rtags" class="mono" placeholder="codemap,my-project"></div>
        <div class="field"><label for="ttl">TTL hours (0 = never)</label><input type="number" id="ttl" value="0" min="0"></div>
      </div>
      <label class="check"><input type="checkbox" id="dup"> allow duplicate (--allow-duplicate)</label>
      <div class="btn-row"><button class="btn primary" id="s-go">Save memory</button></div></div>
      <div id="s-out"></div></div>
    <div id="t-forget" class="hidden"><div class="card">
      <p class="sub">Delete by ID, by tags (any) or by age. Tag/age deletion is bulk and requires <code>yes</code> confirmation. MCP only.</p>
      <div class="form-row">
        <div class="field"><label for="f-id">ID</label><input type="text" id="f-id" class="mono" placeholder="123"></div>
        <div class="field"><label for="f-tags">Tags (comma, OR)</label><input type="text" id="f-tags" class="mono"></div>
        <div class="field"><label for="f-age">Older than (hours)</label><input type="number" id="f-age" min="0" placeholder="720"></div>
      </div>
      <div class="btn-row"><button class="btn danger" id="f-go">Forget…</button></div></div>
      <div id="f-out"></div></div>
    <div id="t-stats" class="hidden"><div class="card">
      <p class="sub">Totals, links, per-tag counts and age. MCP only; works without Ollama.</p>
      <div class="btn-row"><button class="btn primary" id="st-go">Load stats</button></div></div>
      <div id="st-out"></div></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  $$sel('#tabs button', wrap).forEach((b) => {
    b.onclick = () => {
      $$sel('#tabs button', wrap).forEach((x) => x.classList.toggle('on', x === b));
      ['recall', 'remember', 'forget', 'stats'].forEach((t) => $('#t-' + t, wrap).classList.toggle('hidden', t !== b.dataset.t));
    };
  });

  function memCard(m) {
    return `<article class="result"><div class="result-head">
      <span class="meta">#${esc(m.id)}</span><span class="sp"></span>
      <span class="meta">imp ${Number(m.importance).toFixed(2)}</span>
      ${m.score != null ? scoreHtml(m.score) : ''}</div>
      <pre>${esc(m.content)}</pre>
      <div class="result-foot">${(m.tags || []).map((t) => `<span class="tag">${esc(t)}</span>`).join('')}
      <span style="flex:1"></span><button class="btn ghost sm" data-forget="${esc(m.id)}">Forget</button></div></article>`;
  }
  function wireForget(container) {
    $$sel('[data-forget]', container).forEach((b) => {
      b.onclick = async () => {
        const ok = await confirmModal({ title: 'Forget memory', okLabel: 'Forget', danger: true,
          body: `Deletes memory <code>#${esc(b.dataset.forget)}</code>. Permanent.` });
        if (!ok) return;
        const r = await mcp('memory_forget', { id: Number(b.dataset.forget) }, 'forgetting');
        toast(r.ok ? 'Memory forgotten' : 'Failed: ' + (r.error || r.text).slice(0, 160), r.ok ? 'ok' : 'err');
        if (r.ok) $('#go', wrap).click();
      };
    });
  }

  $('#go', wrap).onclick = async () => {
    const q = $('#q', wrap).value.trim();
    if (!q) { toast('Type a query', 'err'); return; }
    const out = $('#r-out', wrap);
    out.innerHTML = '<p class="note">Recalling…</p>';
    const a = ['memory', 'recall', q, '-f', 'json', '-n', $('#limit', wrap).value || '10'];
    if ($('#tags', wrap).value.trim()) a.push('--tags', $('#tags', wrap).value.trim());
    if ($('#minimp', wrap).value.trim()) a.push('--min-importance', $('#minimp', wrap).value.trim());
    const r = await cliJson(a, 'memory recall');
    if (r.code !== 0 || !Array.isArray(r.data)) { showCliError(out, r, 'Memory recall'); return; }
    if (!r.data.length) { out.innerHTML = '<div class="empty">No matching memories.</div>'; return; }
    out.innerHTML = `<p class="note">${r.data.length} ${r.data.length === 1 ? 'memory' : 'memories'}.</p>` + r.data.map(memCard).join('');
    wireForget(out);
  };
  $('#go-mcp', wrap).onclick = async () => {
    const q = $('#q', wrap).value.trim();
    if (!q) { toast('Type a query', 'err'); return; }
    const out = $('#r-out', wrap);
    out.innerHTML = '<p class="note">Recalling via MCP…</p>';
    const args = { query: q, limit: Number($('#limit', wrap).value) || 10 };
    if ($('#tags', wrap).value.trim()) args.tags = $('#tags', wrap).value.trim().split(',').map((s) => s.trim()).filter(Boolean);
    if ($('#minimp', wrap).value.trim()) args.min_importance = Number($('#minimp', wrap).value);
    const r = await mcp('memory_recall', args, 'memory recall via MCP');
    out.innerHTML = r.ok
      ? `<div class="card"><h2>MCP response</h2><pre class="block">${esc(r.text.slice(0, 15000))}</pre></div>`
      : `<div class="callout err">${esc((r.error || r.text).slice(0, 800))}</div>`;
  };
  $('#q', wrap).addEventListener('keydown', (e) => { if (e.key === 'Enter') $('#go', wrap).click(); });

  $('#s-go', wrap).onclick = async () => {
    const c = $('#c', wrap).value.trim();
    if (!c) { toast('Type the content', 'err'); return; }
    const out = $('#s-out', wrap);
    const a = ['memory', 'remember', c, '--importance', $('#imp', wrap).value || '0.5',
      '--ttl-hours', $('#ttl', wrap).value || '0'];
    if ($('#rtags', wrap).value.trim()) a.push('--tags', $('#rtags', wrap).value.trim());
    if ($('#dup', wrap).checked) a.push('--allow-duplicate');
    const r = await cli(a, 'saving memory');
    out.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Saved.').slice(0, 1000))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Memory remember');
    else $('#c', wrap).value = '';
  };

  $('#f-go', wrap).onclick = async () => {
    const args = {};
    if ($('#f-id', wrap).value.trim()) args.id = Number($('#f-id', wrap).value.trim());
    if ($('#f-tags', wrap).value.trim()) args.tags = $('#f-tags', wrap).value.trim().split(',').map((s) => s.trim()).filter(Boolean);
    if ($('#f-age', wrap).value.trim()) args.older_than_hours = Number($('#f-age', wrap).value);
    if (!args.id && !args.tags && !args.older_than_hours) { toast('Give an ID, tags or age', 'err'); return; }
    const bulk = !args.id;
    const ok = await confirmModal({ title: 'Forget memories', okLabel: bulk ? 'Yes, bulk delete' : 'Forget', danger: true,
      body: bulk ? 'Bulk, <strong>permanent</strong> deletion (requires confirm=yes). Continue?' : `Deletes memory <code>#${args.id}</code>. Permanent.` });
    if (!ok) return;
    if (bulk) args.confirm = 'yes';
    const out = $('#f-out', wrap);
    const r = await mcp('memory_forget', args, 'forgetting via MCP');
    out.innerHTML = r.ok
      ? `<div class="callout ok"><pre class="block">${esc(r.text.slice(0, 4000))}</pre></div>`
      : `<div class="callout err">${esc((r.error || r.text).slice(0, 800))}</div>`;
  };

  $('#st-go', wrap).onclick = async () => {
    const out = $('#st-out', wrap);
    out.innerHTML = '<p class="note">Loading…</p>';
    const r = await mcp('memory_stats', {}, 'memory stats');
    out.innerHTML = r.ok
      ? `<div class="card"><h2>Statistics</h2><pre class="block">${esc(r.text.slice(0, 8000))}</pre></div>`
      : `<div class="callout err">${esc((r.error || r.text).slice(0, 800))}</div>`;
  };
};
