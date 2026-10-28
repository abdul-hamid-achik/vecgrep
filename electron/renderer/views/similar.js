'use strict';
/* Similar: vecgrep similar by chunk_id, file:line or free text. */
Views.similar = async function (view, params) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Similar</h1>
    <p>Code similar to an existing chunk, a <code>file:line</code> location or a text snippet. Options of <code>vecgrep similar</code>.</p></div>
    <div class="card"><div class="field"><label>Target</label><div class="seg" id="kind">
      <button data-k="chunk" class="on">chunk id</button><button data-k="loc">file:line</button><button data-k="text">text</button></div></div>
      <div class="field"><label for="t">Value</label>
        <input type="text" id="t" class="mono" placeholder="42" value="${esc(params.get('chunk') || '')}"></div>
      <div class="form-row">
        <div class="field"><label for="limit">Limit</label><input type="number" id="limit" value="10" min="1" max="100"></div>
        <div class="field"><label for="f-lang">Language</label><input type="text" id="f-lang" class="mono" placeholder="go"></div>
        <div class="field"><label for="f-file">File (glob)</label><input type="text" id="f-file" class="mono"></div>
        <div class="field"><label for="f-min">min-score</label><input type="number" id="f-min" min="0" max="1" step="0.05" placeholder="0.3"></div>
      </div>
      <div class="form-row">
        <div class="field"><label for="f-langs">Languages (comma)</label><input type="text" id="f-langs" class="mono"></div>
        <div class="field"><label for="f-type">Chunk type</label><input type="text" id="f-type" class="mono"></div>
        <div class="field"><label for="f-dir">Directory</label><input type="text" id="f-dir" class="mono"></div>
        <div class="field"><label for="f-lines">Lines</label><input type="text" id="f-lines" class="mono" placeholder="1-100"></div>
      </div>
      <label class="check"><input type="checkbox" id="f-excl"> exclude the source file (--exclude-same-file)</label>
      <div class="btn-row"><button class="btn primary" id="go">Find similar</button></div></div>
    <div id="out"></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  let kind = params.get('chunk') ? 'chunk' : 'chunk';
  const segBtns = $$sel('#kind button', wrap);
  const paint = () => segBtns.forEach((b) => b.classList.toggle('on', b.dataset.k === kind));
  const placeholders = { chunk: '42', loc: 'internal/search/search.go:50', text: 'func NewSearcher' };
  segBtns.forEach((b) => { b.onclick = () => { kind = b.dataset.k; paint(); $('#t', wrap).placeholder = placeholders[kind]; }; });
  paint();
  $('#t', wrap).placeholder = placeholders[kind];

  const val = (id) => ($('#' + id, wrap).value || '').trim();
  $('#go', wrap).onclick = async () => {
    const target = val('t');
    if (!target) { toast('Type the target', 'err'); return; }
    const out = $('#out', wrap);
    out.innerHTML = '<p class="note">Searching…</p>';
    const a = ['similar', '-f', 'json', '-n', val('limit') || '10'];
    if (kind === 'text') a.push('--text', target);
    else a.push(target);
    const push = (flag, v) => { if (v) a.push(flag, v); };
    push('-l', val('f-lang')); push('--languages', val('f-langs'));
    push('-t', val('f-type')); push('--file', val('f-file'));
    push('--dir', val('f-dir')); push('--lines', val('f-lines'));
    push('--min-score', val('f-min'));
    if ($('#f-excl', wrap).checked) a.push('--exclude-same-file');
    const r = await cliJson(a, 'finding similar code');
    if (r.code !== 0 || !Array.isArray(r.data)) { showCliError(out, r, 'Similar'); return; }
    if (!r.data.length) { out.innerHTML = '<div class="empty">No results.</div>'; return; }
    out.innerHTML = `<p class="note">${r.data.length} result(s).</p>` + r.data.map((x, i) => resultCard(x, i)).join('');
    wireResults(out, r.data);
  };
  $('#t', wrap).addEventListener('keydown', (e) => { if (e.key === 'Enter') $('#go', wrap).click(); });
  if (params.get('chunk')) $('#go', wrap).click();
};
