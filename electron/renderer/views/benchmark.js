'use strict';
/* Benchmark: embedding preset comparison (never touches the index). */
Views.benchmark = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Benchmark</h1>
    <p>Compares embedding presets on a labeled corpus, without reading or writing the index. Command <code>benchmark embeddings</code>.</p></div>
    <div class="card"><div class="form-row">
      <div class="field" style="flex:2"><label for="profiles">Profiles (comma)</label>
        <input type="text" id="profiles" class="mono" value="fast-local,quality-code"></div>
      <div class="field"><label for="batch">Batch size</label><input type="number" id="batch" value="32" min="1" max="256"></div>
    </div>
    <div class="form-row">
      <div class="field" style="flex:2"><label for="dataset">Dataset JSON (optional)</label>
        <input type="text" id="dataset" class="mono" placeholder="path to a custom dataset; empty = built-in corpus"></div>
      <div class="field" style="flex:2"><label for="root">Project root</label>
        <input type="text" id="root" class="mono" value="${esc(S.root)}"></div>
    </div>
    <div class="btn-row"><button class="btn primary" id="go">Run benchmark</button></div>
    <p class="hint">May take several minutes depending on the profiles (embeds the corpus with each preset).</p></div>
    <div class="card"><h2>Live output</h2>${streamConsole('bench')}<div id="rep"></div></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  $('#go', wrap).onclick = async () => {
    const args = ['benchmark', 'embeddings', '--json'];
    if ($('#profiles', wrap).value.trim()) args.push('--profiles', $('#profiles', wrap).value.trim());
    if ($('#batch', wrap).value.trim()) args.push('--batch-size', $('#batch', wrap).value.trim());
    if ($('#dataset', wrap).value.trim()) args.push('--dataset', $('#dataset', wrap).value.trim());
    if ($('#root', wrap).value.trim()) args.push('--root', $('#root', wrap).value.trim());
    $('#rep', wrap).innerHTML = '';
    let captured = '';
    const off1 = api.onStreamData((p) => { if (p.id === 'bench' && p.stream === 'stdout') captured += p.text; });
    const s = wireStream('bench', args, { onExit: (p) => {
      off1();
      if (p.code !== 0) {
        $('#rep', wrap).innerHTML = `<div class="callout err">Benchmark exited with code ${p.code}.</div>`;
        return;
      }
      try {
        const data = JSON.parse(captured.trim());
        $('#rep', wrap).innerHTML = `<div class="callout ok">Benchmark complete.</div>
          <div class="card"><h2>Report</h2><pre class="block">${esc(JSON.stringify(data, null, 2).slice(0, 20000))}</pre></div>`;
      } catch {
        $('#rep', wrap).innerHTML = '<div class="callout warn">Could not parse the report JSON; check the output above.</div>';
      }
    }});
    s.start();
  };
};
