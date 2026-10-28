'use strict';
/* Search: vecgrep search with every filter, mode and diagnostics. */
Views.search = async function (view, params) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Search</h1>
    <p>Semantic, keyword or hybrid search over the index. Options of <code>vecgrep search</code>.</p></div>
    <div class="card"><div class="form-row">
      <div class="field" style="flex:3 1 320px"><label for="q">Query</label>
        <input type="search" id="q" class="mono" placeholder="e.g. where are http requests retried" value="${esc(params.get('q') || '')}"></div>
      <div class="field" style="flex:0 1 170px"><label>Mode</label><div class="seg" id="mode">
        <button data-m="hybrid" class="on">hybrid</button><button data-m="semantic">semantic</button><button data-m="keyword">keyword</button></div></div>
      <div class="field" style="flex:0 1 90px"><label for="limit">Limit</label><input type="number" id="limit" value="10" min="1" max="100"></div>
    </div>
    <div class="form-row">
      <div class="field"><label for="f-lang">Language</label><input type="text" id="f-lang" class="mono" placeholder="go"></div>
      <div class="field"><label for="f-langs">Languages (comma)</label><input type="text" id="f-langs" class="mono" placeholder="go,typescript"></div>
      <div class="field"><label for="f-type">Chunk type</label><input type="text" id="f-type" class="mono" placeholder="function"></div>
      <div class="field"><label for="f-types">Types (comma)</label><input type="text" id="f-types" class="mono" placeholder="function,class"></div>
    </div>
    <div class="form-row">
      <div class="field"><label for="f-file">File (glob)</label><input type="text" id="f-file" class="mono" placeholder="internal/search/*.go"></div>
      <div class="field"><label for="f-dir">Directory</label><input type="text" id="f-dir" class="mono" placeholder="internal/"></div>
      <div class="field"><label for="f-lines">Lines</label><input type="text" id="f-lines" class="mono" placeholder="1-100"></div>
      <div class="field"><label for="f-min">min-score (0–1)</label><input type="number" id="f-min" min="0" max="1" step="0.05" placeholder="0.3"></div>
    </div>
    <div class="form-row">
      <div class="field"><label for="f-scope">scope-files (comma)</label><input type="text" id="f-scope" class="mono" placeholder="relative paths, comma-separated"></div>
      <div class="field"><label for="f-symbol">Symbol (blast radius)</label><input type="text" id="f-symbol" class="mono" placeholder="pkg.Func via codemap"></div>
      <div class="field" style="flex:0 1 140px"><label>&nbsp;</label><label class="check"><input type="checkbox" id="f-explain"> explain</label></div>
    </div>
    <div class="btn-row"><button class="btn primary" id="go">Search</button>
      <button class="btn ghost" id="via-mcp" title="Run the same search via MCP">Via MCP</button>
      <button class="btn ghost" id="clear">Clear</button></div></div>
    <div id="out"></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  let mode = params.get('mode') || 'hybrid';
  const segBtns = $$sel('#mode button', wrap);
  const paintMode = () => segBtns.forEach((b) => b.classList.toggle('on', b.dataset.m === mode));
  segBtns.forEach((b) => { b.onclick = () => { mode = b.dataset.m; paintMode(); }; });
  paintMode();

  const val = (id) => ($('#' + id, wrap).value || '').trim();
  function buildArgs() {
    const q = val('q');
    if (!q) { toast('Type a query', 'err'); return null; }
    const a = ['search', q, '-f', 'json', '-m', mode, '-n', val('limit') || '10'];
    const push = (flag, v) => { if (v) a.push(flag, v); };
    push('-l', val('f-lang')); push('--languages', val('f-langs'));
    push('-t', val('f-type')); push('--types', val('f-types'));
    push('--file', val('f-file')); push('--dir', val('f-dir'));
    push('--lines', val('f-lines')); push('--min-score', val('f-min'));
    push('--scope-files', val('f-scope')); push('--symbol', val('f-symbol'));
    if ($('#f-explain', wrap).checked) a.push('--explain');
    return a;
  }
  function renderResults(container, results, extra) {
    if (!results.length) {
      container.innerHTML = '<div class="empty">No results on a ready index: the search worked, nothing matched.</div>';
      return;
    }
    container.innerHTML = `<p class="note">${results.length} result(s) · click <em>View context</em> to read the file.</p>` +
      results.map((r, i) => resultCard(r, i)).join('') +
      (extra || '');
    wireResults(container, results);
  }
  async function runSearch() {
    const args = buildArgs();
    if (!args) return;
    const out = $('#out', wrap);
    out.innerHTML = '<p class="note">Searching…</p>';
    const r = await cliJson(args, 'searching');
    if (r.code !== 0 || !Array.isArray(r.data)) {
      // The CLI may degrade hybrid→keyword with a stderr note: still shown.
      if (Array.isArray(r.data) && r.data.length) {
        renderResults(out, r.data, `<div class="callout warn">${esc(r.stderr.slice(0, 500))}</div>`);
      } else showCliError(out, r, 'Search');
      return;
    }
    let warn = '';
    if (r.stderr && /degrad|keyword-only/i.test(r.stderr)) warn = `<div class="callout warn">${esc(r.stderr.slice(0, 500))}</div>`;
    history.replaceState(null, '', '#/search?q=' + encodeURIComponent(val('q')) + '&mode=' + mode);
    renderResults(out, r.data, warn);
  }
  async function runMcp() {
    const q = val('q');
    if (!q) { toast('Type a query', 'err'); return; }
    const out = $('#out', wrap);
    out.innerHTML = '<p class="note">Searching via MCP…</p>';
    const num = (id) => { const v = Number(val(id)); return isNaN(v) ? undefined : v; };
    const args = { query: q, mode, limit: num('limit') || 10 };
    const s = (id) => val(id);
    if (s('f-lang')) args.language = s('f-lang');
    if (s('f-langs')) args.languages = s('f-langs').split(',').map((x) => x.trim()).filter(Boolean);
    if (s('f-type')) args.chunk_type = s('f-type');
    if (s('f-types')) args.chunk_types = s('f-types').split(',').map((x) => x.trim()).filter(Boolean);
    if (s('f-file')) args.file_pattern = s('f-file');
    if (s('f-dir')) args.directory = s('f-dir');
    if (s('f-scope')) args.file_paths = s('f-scope').split(',').map((x) => x.trim()).filter(Boolean);
    if (s('f-symbol')) args.symbol = s('f-symbol');
    if (s('f-lines')) {
      const m = s('f-lines').match(/(\d+)\s*-\s*(\d+)/);
      if (m) { args.min_line = Number(m[1]); args.max_line = Number(m[2]); }
    }
    if (s('f-min')) args.min_score = Number(s('f-min'));
    if ($('#f-explain', wrap).checked) args.explain = true;
    const r = await mcp('vecgrep_search', args, 'searching via MCP');
    if (!r.ok) {
      out.innerHTML = `<div class="callout err"><strong>MCP returned no results.</strong>
        <p class="note">If the index is empty or the profile mismatches, the server answers readiness instead of results: that is not “no results”.</p>
        <pre class="block">${esc((r.error || r.text || '').slice(0, 1500))}</pre></div>`;
      return;
    }
    out.innerHTML = `<div class="card"><h2>MCP response</h2><pre class="block">${esc(r.text.slice(0, 12000))}</pre></div>`;
  }

  $('#go', wrap).onclick = runSearch;
  $('#via-mcp', wrap).onclick = runMcp;
  $('#clear', wrap).onclick = () => { ['q','f-lang','f-langs','f-type','f-types','f-file','f-dir','f-lines','f-min','f-scope','f-symbol'].forEach((id) => { $('#'+id, wrap).value = ''; }); $('#out', wrap).innerHTML = ''; };
  $('#q', wrap).addEventListener('keydown', (e) => { if (e.key === 'Enter') runSearch(); });
  if (params.get('q')) runSearch();
};
