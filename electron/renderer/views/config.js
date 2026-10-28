'use strict';
/* Settings: resolved show, set by key, presets and providers. */
const CONFIG_GROUPS = [
  { title: 'Embeddings', keys: [
    ['embedding.provider', 'Provider (ollama, openai, cohere, voyage)', 'openai'],
    ['embedding.model', 'Model', 'text-embedding-3-small'],
    ['embedding.dimensions', 'Dimensions', '1536'],
    ['embedding.ollama_url', 'Ollama URL', 'http://localhost:11434'],
    ['embedding.ollama_context', 'Ollama context', '2048'],
    ['embedding.keep_alive', 'Ollama keep alive', '5m'],
    ['embedding.max_batch_size', 'Batch size', '32'],
  ]},
  { title: 'Cloud keys & endpoints', keys: [
    ['embedding.openai_api_key', 'OpenAI API key', ''],
    ['embedding.openai_base_url', 'OpenAI base URL', 'https://api.openai.com/v1'],
    ['embedding.cohere_api_key', 'Cohere API key', ''],
    ['embedding.cohere_base_url', 'Cohere base URL', ''],
    ['embedding.voyage_api_key', 'Voyage API key', ''],
    ['embedding.voyage_base_url', 'Voyage base URL', ''],
  ]},
  { title: 'Search', keys: [
    ['search.default_mode', 'Default mode', 'hybrid'],
    ['search.vector_weight', 'Vector weight', '0.7'],
    ['search.text_weight', 'Text weight', '0.3'],
  ]},
  { title: 'Indexing', keys: [
    ['indexing.chunk_size', 'Chunk size', '512'],
    ['indexing.chunk_overlap', 'Overlap', '64'],
    ['indexing.max_file_size', 'Max file (bytes)', '1048576'],
    ['indexing.ignore_patterns', 'Ignore patterns (comma)', ''],
  ]},
  { title: 'Vector (veclite HNSW)', keys: [
    ['vector.veclite.m', 'M', '16'],
    ['vector.veclite.ef_construction', 'ef_construction', '200'],
    ['vector.veclite.ef_search', 'ef_search', '100'],
  ]},
  { title: 'Cache', keys: [
    ['cache.fcheap_stash', 'fcheap stash (true/false)', 'true'],
    ['cache.fcheap_ttl', 'fcheap TTL', '30d'],
  ]},
  { title: 'Codemap', keys: [
    ['codemap.enabled', 'Enabled (true/false)', 'true'],
    ['codemap.structural_chunks', 'structural_chunks (auto/off/required)', 'auto'],
    ['codemap.structural_weight', 'Structural weight', '0'],
    ['codemap.bin', 'Codemap binary', 'codemap'],
  ]},
  { title: 'Daemon', keys: [
    ['daemon.autostart', 'Autostart (true/false)', 'false'],
    ['daemon.idle_timeout', 'Idle timeout (s)', '30'],
    ['daemon.embed_workers', 'Embed workers', '4'],
    ['daemon.embed_rps', 'Embed RPS (0 = unlimited)', '0'],
    ['daemon.embed_max_in_flight', 'Embed max in-flight', '8'],
    ['daemon.debounce', 'Debounce (ms)', '500'],
    ['daemon.sweep_interval', 'Sweep interval', '24h'],
  ]},
  { title: 'Embedding throttle', keys: [
    ['embedding.throttle.enabled', 'Enabled (true/false)', ''],
    ['embedding.throttle.max_in_flight', 'Max in-flight', ''],
    ['embedding.throttle.rate_limit', 'Rate limit', ''],
  ]},
];

Views.settings = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Settings</h1>
    <p>Layered resolution (env → project → global → defaults). Commands <code>config show/set/preset</code>.</p></div>
    <div class="tabs" id="tabs">
      <button data-t="show" class="on">Resolved</button>
      <button data-t="set">Edit</button>
      <button data-t="preset">Presets</button>
    </div>
    <div id="t-show"><div class="card"><h2>Resolved configuration</h2>
      <p class="sub">Priority: <code>VECGREP_*</code> vars → <code>vecgrep.yaml</code> → <code>.config/vecgrep.yaml</code> → <code>.vecgrep/config.yaml</code> → global entry → global defaults → built-in.</p>
      <div class="btn-row"><button class="btn ghost sm" id="sh-proj">Project</button>
      <button class="btn ghost sm" id="sh-global">Global</button></div><div id="sh-out"></div></div></div>
    <div id="t-set" class="hidden">
      <div class="card"><h2>Destination</h2>
        <div class="seg" id="dest"><button data-d="project" class="on">Project</button><button data-d="global">Global (--global)</button></div>
        <p class="hint">Project writes to the project's <code>vecgrep.yaml</code>; global writes to <code>~/.vecgrep/config.yaml</code> (defaults).</p></div>
      <div id="groups"></div>
      <div class="card"><h2>Free-form key</h2>
        <p class="sub">Any <code>section.key</code> accepted by <code>config set</code>.</p>
        <div class="form-row"><div class="field"><label for="free-k">Key</label><input type="text" id="free-k" class="mono" placeholder="search.default_mode"></div>
        <div class="field" style="flex:2"><label for="free-v">Value</label><input type="text" id="free-v" class="mono"></div></div>
        <div class="btn-row"><button class="btn primary" id="free-go">Save</button></div></div>
      <div id="set-out"></div></div>
    <div id="t-preset" class="hidden"><div class="card"><h2>Embedding presets</h2>
      <p class="sub">Applying a preset changes the semantic profile: afterwards you must reindex with <code>index --full</code>.</p>
      <div class="seg" id="p-dest"><button data-d="project" class="on">Project</button><button data-d="global">Global</button></div>
      <div id="presets" style="margin-top:12px"></div><div id="preset-out"></div></div></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  $$sel('#tabs button', wrap).forEach((b) => {
    b.onclick = () => {
      $$sel('#tabs button', wrap).forEach((x) => x.classList.toggle('on', x === b));
      ['show', 'set', 'preset'].forEach((t) => $('#t-' + t, wrap).classList.toggle('hidden', t !== b.dataset.t));
    };
  });

  // --- show
  async function showConfig(global) {
    const out = $('#sh-out', wrap);
    out.innerHTML = '<p class="note">Loading…</p>';
    const r = await cli(global ? ['config', 'show', '--global'] : ['config', 'show'], 'config show');
    out.innerHTML = r.code === 0 ? `<pre class="block">${esc(r.stdout.slice(0, 20000))}</pre>` : '';
    if (r.code !== 0) showCliError(out, r, 'Config show');
  }
  $('#sh-proj', wrap).onclick = () => showConfig(false);
  $('#sh-global', wrap).onclick = () => showConfig(true);
  showConfig(false);

  // --- set
  let dest = 'project';
  $$sel('#dest button', wrap).forEach((b) => {
    b.onclick = () => { dest = b.dataset.d; $$sel('#dest button', wrap).forEach((x) => x.classList.toggle('on', x === b)); };
  });
  $('#groups', wrap).innerHTML = CONFIG_GROUPS.map((g, gi) =>
    `<div class="card"><h2>${esc(g.title)}</h2>` + g.keys.map(([k, label, ph], ki) =>
      `<div class="form-row" style="align-items:flex-end"><div class="field" style="flex:2"><label>${esc(label)} <span class="tag">${esc(k)}</span></label>
      <input type="text" class="mono" data-k="${esc(k)}" placeholder="${esc(ph)}"></div>
      <div class="field" style="flex:0"><label>&nbsp;</label><button class="btn sm" data-save="${esc(k)}">Save</button></div></div>`
    ).join('') + `</div>`).join('');
  async function setKey(key, value) {
    const out = $('#set-out', wrap);
    if (!value) { toast('Type a value', 'err'); return; }
    const args = dest === 'global' ? ['config', 'set', '--global', key, value] : ['config', 'set', key, value];
    const r = await cli(args, 'config set');
    const changedProfile = /^(embedding\.(provider|model|dimensions))/.test(key);
    out.innerHTML = r.code === 0
      ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 500))}${changedProfile ? '<br><strong>You changed the embedding profile: reindex with <code>index --full</code>.</strong>' : ''}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Config set');
    else refreshTopStatus();
  }
  $$sel('[data-save]', wrap).forEach((b) => {
    b.onclick = () => {
      const k = b.dataset.save;
      const inp = $(`input[data-k="${k}"]`, wrap);
      setKey(k, inp.value.trim());
    };
  });
  $('#free-go', wrap).onclick = () => setKey($('#free-k', wrap).value.trim(), $('#free-v', wrap).value.trim());

  // --- presets
  let pDest = 'project';
  $$sel('#p-dest button', wrap).forEach((b) => {
    b.onclick = () => { pDest = b.dataset.d; $$sel('#p-dest button', wrap).forEach((x) => x.classList.toggle('on', x === b)); };
  });
  async function loadPresets() {
    const box = $('#presets', wrap);
    const r = await cli(['config', 'preset'], 'presets');
    if (r.code !== 0) { showCliError(box, r, 'Presets'); return; }
    const rows = r.stdout.split('\n').map((l) => l.trim()).filter((l) => l && !/^Embedding presets/.test(l));
    box.innerHTML = `<table class="tbl"><thead><tr><th>Preset</th><th>Description</th><th></th></tr></thead><tbody>` +
      rows.map((row) => {
        const name = row.split(/\s+/)[0];
        const desc = row.slice(name.length).trim();
        return `<tr><td class="mono"><strong>${esc(name)}</strong></td><td>${esc(desc)}</td>
          <td><button class="btn sm" data-preset="${esc(name)}">Apply</button></td></tr>`;
      }).join('') + `</tbody></table>`;
    $$sel('[data-preset]', box).forEach((b) => {
      b.onclick = async () => {
        const ok = await confirmModal({ title: 'Apply preset', okLabel: 'Apply', danger: true,
          body: `Applies <code>${esc(b.dataset.preset)}</code> (${pDest === 'global' ? 'global' : 'project'}). This changes the semantic profile: afterwards you must run <code>index --full</code>.` });
        if (!ok) return;
        const out = $('#preset-out', wrap);
        const args = pDest === 'global' ? ['config', 'preset', '--global', b.dataset.preset] : ['config', 'preset', b.dataset.preset];
        const rr = await cli(args, 'applying preset');
        out.innerHTML = rr.code === 0 ? `<div class="callout ok">${esc((rr.stdout || 'Done.').slice(0, 1000))}<br><strong>Reindex with <code>index --full</code> to use the new profile.</strong></div>` : '';
        if (rr.code !== 0) showCliError(out, rr, 'Preset');
        else refreshTopStatus();
      };
    });
  }
  loadPresets();
};
