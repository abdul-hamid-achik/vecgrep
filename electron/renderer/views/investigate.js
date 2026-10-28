'use strict';
/* Batch & Investigate: batch_search and investigate via MCP (no CLI equivalent). */
Views.batch = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Batch &amp; Investigate</h1>
    <p>Parallel multi-query search and symbol blast-radius investigation. MCP tools: <code>vecgrep_batch_search</code>, <code>vecgrep_investigate</code>.</p></div>
    <div class="card"><h2>Batch search</h2>
      <p class="sub">Several queries in parallel, with optional deduplication.</p>
      <div class="field"><label for="qs">Queries (one per line)</label>
        <textarea id="qs" class="mono" placeholder="where is the database opened&#10;how are embeddings computed"></textarea></div>
      <div class="form-row">
        <div class="field"><label for="lpq">Limit per query</label><input type="number" id="lpq" value="3" min="1" max="20"></div>
        <div class="field"><label for="b-lang">Language</label><input type="text" id="b-lang" class="mono" placeholder="(optional)"></div>
        <div class="field"><label for="b-type">Chunk type</label><input type="text" id="b-type" class="mono" placeholder="(optional)"></div>
        <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="b-dedup" checked> deduplicate</label></div>
      </div>
      <div class="btn-row"><button class="btn primary" id="b-go">Search batch</button></div></div>
    <div id="b-out"></div>
    <div class="card"><h2>Investigate symbol</h2>
      <p class="sub">Computes a symbol's blast radius with codemap, then searches semantically inside that set. Falls back to unscoped search when codemap is unavailable.</p>
      <div class="form-row">
        <div class="field"><label for="i-sym">Symbol</label><input type="text" id="i-sym" class="mono" placeholder="pkg.FuncName"></div>
        <div class="field" style="flex:2"><label for="i-q">Query</label><input type="text" id="i-q" class="mono" placeholder="what breaks if this symbol changes"></div>
      </div>
      <div class="form-row">
        <div class="field"><label for="i-limit">Limit</label><input type="number" id="i-limit" value="10" min="1" max="50"></div>
        <div class="field"><label for="i-mode">Mode</label><select id="i-mode"><option>hybrid</option><option>semantic</option><option>keyword</option></select></div>
        <div class="field"><label for="i-ctx">Context lines</label><input type="number" id="i-ctx" value="0" min="0" max="20"></div>
      </div>
      <div class="btn-row"><button class="btn primary" id="i-go">Investigate</button></div></div>
    <div id="i-out"></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  $('#b-go', wrap).onclick = async () => {
    const queries = ($('#qs', wrap).value || '').split('\n').map((s) => s.trim()).filter(Boolean);
    if (!queries.length) { toast('Type at least one query', 'err'); return; }
    const out = $('#b-out', wrap);
    out.innerHTML = '<p class="note">Searching batch via MCP…</p>';
    const args = { queries, limit_per_query: Number($('#lpq', wrap).value) || 3, deduplicate: $('#b-dedup', wrap).checked };
    if ($('#b-lang', wrap).value.trim()) args.language = $('#b-lang', wrap).value.trim();
    if ($('#b-type', wrap).value.trim()) args.chunk_type = $('#b-type', wrap).value.trim();
    const r = await mcp('vecgrep_batch_search', args, 'batch via MCP');
    out.innerHTML = r.ok
      ? `<div class="card"><h2>Results per query</h2><pre class="block">${esc(r.text.slice(0, 20000))}</pre></div>`
      : `<div class="callout err"><strong>Batch failed.</strong><pre class="block">${esc((r.error || r.text).slice(0, 1500))}</pre></div>`;
  };
  $('#i-go', wrap).onclick = async () => {
    const symbol = $('#i-sym', wrap).value.trim();
    const query = $('#i-q', wrap).value.trim();
    if (!symbol || !query) { toast('Symbol and query are required', 'err'); return; }
    const out = $('#i-out', wrap);
    out.innerHTML = '<p class="note">Investigating via MCP…</p>';
    const r = await mcp('vecgrep_investigate', {
      symbol, query,
      limit: Number($('#i-limit', wrap).value) || 10,
      mode: $('#i-mode', wrap).value,
      context_lines: Number($('#i-ctx', wrap).value) || 0,
    }, 'investigating via MCP');
    out.innerHTML = r.ok
      ? `<div class="card"><h2>Blast radius + search</h2><pre class="block">${esc(r.text.slice(0, 20000))}</pre></div>`
      : `<div class="callout err"><strong>Investigation failed.</strong><pre class="block">${esc((r.error || r.text).slice(0, 1500))}</pre></div>`;
  };
};
