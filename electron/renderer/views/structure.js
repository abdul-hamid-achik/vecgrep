'use strict';
/* Structure: codebase overview and related files (MCP). */
Views.structure = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Structure</h1>
    <p>Codebase overview and static file relationships. MCP tools: <code>vecgrep_overview</code>, <code>vecgrep_related_files</code>.</p></div>
    <div class="card"><h2>Overview</h2>
      <div class="form-row">
        <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="o-struct" checked> directory structure</label></div>
        <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="o-entry" checked> entry points</label></div>
        <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="o-key" checked> key files</label></div>
        <div class="field"><label for="o-depth">Max depth</label><input type="number" id="o-depth" value="3" min="1" max="10"></div>
      </div>
      <div class="btn-row"><button class="btn primary" id="o-go">Generate overview</button></div></div>
    <div id="o-out"></div>
    <div class="card"><h2>Related files</h2>
      <p class="sub">Static relationship: <code>imports</code> (what it imports), <code>imported_by</code> (who uses it), <code>tests</code> or <code>all</code>. Go resolves with <code>go/parser</code> (exact); JS/TS/Python use text matching.</p>
      <div class="form-row">
        <div class="field" style="flex:2"><label for="r-file">File</label><input type="text" id="r-file" class="mono" placeholder="internal/search/search.go"></div>
        <div class="field"><label for="r-rel">Relationship</label><select id="r-rel"><option>all</option><option>imports</option><option>imported_by</option><option>tests</option></select></div>
        <div class="field"><label for="r-limit">Limit</label><input type="number" id="r-limit" value="10" min="1" max="100"></div>
      </div>
      <div class="btn-row"><button class="btn primary" id="r-go">Find related</button></div></div>
    <div id="r-out"></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  $('#o-go', wrap).onclick = async () => {
    const out = $('#o-out', wrap);
    out.innerHTML = '<p class="note">Generating overview…</p>';
    const r = await mcp('vecgrep_overview', {
      include_structure: $('#o-struct', wrap).checked,
      include_entry_points: $('#o-entry', wrap).checked,
      include_key_files: $('#o-key', wrap).checked,
      max_directory_depth: Number($('#o-depth', wrap).value) || 3,
    }, 'overview via MCP');
    out.innerHTML = r.ok
      ? `<div class="card"><h2>Project overview</h2><pre class="block">${esc(r.text.slice(0, 30000))}</pre></div>`
      : `<div class="callout err"><strong>Overview failed.</strong><pre class="block">${esc((r.error || r.text).slice(0, 1500))}</pre></div>`;
  };
  $('#r-go', wrap).onclick = async () => {
    const file = $('#r-file', wrap).value.trim();
    if (!file) { toast('Type the file', 'err'); return; }
    const out = $('#r-out', wrap);
    out.innerHTML = '<p class="note">Finding related files…</p>';
    const r = await mcp('vecgrep_related_files', {
      file, relationship: $('#r-rel', wrap).value, limit: Number($('#r-limit', wrap).value) || 10,
    }, 'related files via MCP');
    out.innerHTML = r.ok
      ? `<div class="card"><h2>Files related to ${esc(file)}</h2><pre class="block">${esc(r.text.slice(0, 20000))}</pre></div>`
      : `<div class="callout err"><strong>Search failed.</strong><pre class="block">${esc((r.error || r.text).slice(0, 1500))}</pre></div>`;
  };
};
