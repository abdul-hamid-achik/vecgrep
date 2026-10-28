'use strict';
/* Manage index: index (incremental/full/dry-run/plan), delete, clean and reset. */
Views.index = async function (view, params) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Manage index</h1>
    <p>Index, preview plans, delete files from the index, sync and reset. Commands <code>index</code>, <code>delete</code>, <code>clean</code> and <code>reset</code>.</p></div>
    <div class="tabs" id="tabs">
      <button data-t="run" class="on">Index</button>
      <button data-t="plan">Plan (dry-run)</button>
      <button data-t="maint">Maintenance</button>
    </div>
    <div id="t-run"><div class="card">
      <div class="form-row">
        <div class="field" style="flex:2"><label for="paths">Paths (empty = whole project)</label>
          <input type="text" id="paths" class="mono" placeholder="internal/ cmd/"></div>
        <div class="field"><label for="ignore">Ignore (comma)</label><input type="text" id="ignore" class="mono" placeholder="*.gen.go"></div>
        <div class="field"><label for="struct">structural-chunks</label>
          <select id="struct"><option value="">(config)</option><option>auto</option><option>off</option><option>required</option></select></div>
      </div>
      <label class="check"><input type="checkbox" id="full"> full reindex (--full, re-embeds everything)</label>
      <label class="check"><input type="checkbox" id="noprogress"> no progress bar (--no-progress)</label>
      <div class="btn-row"><button class="btn primary" id="go">Index</button></div>
      <p class="hint"><code>--yes</code> is always used (no prompt) and output streams live below.</p></div>
      <div class="card"><h2>Live output</h2>${streamConsole('index')}<div id="idx-result"></div></div></div>
    <div id="t-plan" class="hidden"><div class="card">
      <p class="sub">Shows what indexing would do without calling the embedding provider (<code>--dry-run</code>).</p>
      <div class="form-row"><div class="field" style="flex:2"><label for="p-paths">Paths</label>
        <input type="text" id="p-paths" class="mono" placeholder="(empty = all)"></div>
        <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="p-full"> --full</label></div></div>
      <div class="btn-row"><button class="btn primary" id="p-go">Show plan</button></div></div>
      <div id="p-out"></div></div>
    <div id="t-maint" class="hidden">
      <div class="card"><h2>Delete file from index</h2>
        <p class="sub">Removes a file's chunks from the index (<code>vecgrep delete</code>). The file on disk is untouched and returns on the next index run.</p>
        <div class="form-row"><div class="field" style="flex:3"><label for="d-path">Path (relative or absolute)</label>
          <input type="text" id="d-path" class="mono" placeholder="internal/search/search.go"></div></div>
        <div class="btn-row"><button class="btn danger" id="d-go">Delete from index</button></div><div id="d-out"></div></div>
      <div class="card"><h2>Sync (clean)</h2>
        <p class="sub">Flushes pending writes to disk and reports statistics. With pure veclite storage there are no orphans to purge: it is sync-and-report.</p>
        <div class="btn-row"><button class="btn" id="c-go">Run clean</button></div><div id="c-out"></div></div>
      <div class="card"><h2>Reset</h2>
        <p class="sub">Deletes the project's files, chunks and embeddings. Destructive and irreversible.</p>
        <div class="btn-row"><button class="btn danger" id="r-go">Reset project…</button></div><div id="r-out"></div></div>
    </div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  $$sel('#tabs button', wrap).forEach((b) => {
    b.onclick = () => {
      $$sel('#tabs button', wrap).forEach((x) => x.classList.toggle('on', x === b));
      ['run', 'plan', 'maint'].forEach((t) => $('#t-' + t, wrap).classList.toggle('hidden', t !== b.dataset.t));
    };
  });

  // --- index with live output
  $('#go', wrap).onclick = async () => {
    const args = ['index', '--yes'];
    const paths = ($('#paths', wrap).value || '').trim().split(/\s+/).filter(Boolean);
    if ($('#full', wrap).checked) args.push('--full');
    if ($('#noprogress', wrap).checked) args.push('--no-progress');
    const ig = $('#ignore', wrap).value.trim();
    if (ig) args.push('--ignore', ig);
    const st = $('#struct', wrap).value;
    if (st) args.push('--structural-chunks', st);
    args.push(...paths);
    if ($('#full', wrap).checked) {
      const ok = await confirmModal({ title: 'Full reindex', okLabel: 'Reindex everything', danger: true,
        body: '<code>--full</code> re-embeds every file: slow and consumes provider quota. Continue?' });
      if (!ok) return;
    }
    $('#idx-result', wrap).innerHTML = '';
    const s = wireStream('index', args, { onExit: (p) => {
      $('#idx-result', wrap).innerHTML = p.code === 0
        ? '<div class="callout ok">Indexing finished.</div>'
        : `<div class="callout err">Indexing exited with code ${p.code}.</div>`;
      refreshTopStatus();
    }});
    s.start();
  };

  // --- dry-run plan
  $('#p-go', wrap).onclick = async () => {
    const args = ['index', '--dry-run'];
    if ($('#p-full', wrap).checked) args.push('--full');
    const paths = ($('#p-paths', wrap).value || '').trim().split(/\s+/).filter(Boolean);
    args.push(...paths);
    const out = $('#p-out', wrap);
    out.innerHTML = '<p class="note">Computing plan…</p>';
    const r = await cli(args, 'dry-run plan');
    out.innerHTML = r.code === 0
      ? `<div class="card"><h2>Plan</h2><pre class="block">${esc((r.stdout || '(no changes)').slice(0, 20000))}</pre></div>`
      : '';
    if (r.code !== 0) showCliError(out, r, 'Plan');
  };

  // --- maintenance
  $('#d-go', wrap).onclick = async () => {
    const p = $('#d-path', wrap).value.trim();
    if (!p) { toast('Type the file path', 'err'); return; }
    const ok = await confirmModal({ title: 'Delete from index', okLabel: 'Delete', danger: true,
      body: `Removes <code>${esc(p)}</code> from the index. The file on disk is not modified.` });
    if (!ok) return;
    const out = $('#d-out', wrap);
    const r = await cli(['delete', p], 'deleting from index');
    out.innerHTML = r.code === 0
      ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Delete');
    else refreshTopStatus();
  };
  $('#c-go', wrap).onclick = async () => {
    const out = $('#c-out', wrap);
    const r = await cli(['clean'], 'clean');
    out.innerHTML = r.code === 0
      ? `<div class="callout ok"><pre class="block">${esc((r.stdout || '').slice(0, 2000))}</pre></div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Clean');
  };
  $('#r-go', wrap).onclick = async () => {
    const ok = await confirmModal({ title: 'Reset project', okLabel: 'Yes, delete everything', danger: true,
      body: 'This deletes <strong>all</strong> files, chunks and embeddings of the active project. Cannot be undone.' });
    if (!ok) return;
    const out = $('#r-out', wrap);
    const r = await cli(['reset', '--force'], 'reset');
    out.innerHTML = r.code === 0
      ? `<div class="callout ok">${esc((r.stdout || 'Database reset.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Reset');
    else refreshTopStatus();
  };

  if (params.get('run')) $('#go', wrap).click();
  if (params.get('tab')) {
    const b = $(`#tabs button[data-t="${params.get('tab')}"]`, wrap);
    if (b) b.click();
  }
};
