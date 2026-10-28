'use strict';
/* Projects: global registry + init. */
Views.projects = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Projects</h1>
    <p>Global registry in <code>~/.vecgrep/config.yaml</code> and initialization. Commands <code>projects</code> and <code>init</code>.</p></div>
    <div class="card"><h2>Initialize project</h2>
      <p class="sub">Registers the current folder (<code id="init-root"></code>) with vecgrep.</p>
      <div class="form-row">
        <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="init-local"> --local (create <code>.vecgrep/</code> here instead of <code>~/.vecgrep/projects/</code>)</label></div>
        <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="init-force"> --force (overwrite config)</label></div>
        <div class="field"><label for="init-ext">Config extension</label><select id="init-ext"><option>yaml</option><option>yml</option></select></div>
      </div>
      <div class="btn-row"><button class="btn primary" id="init-go">Initialize here</button>
        <button class="btn" id="add-go">Register (projects add)</button></div><div id="init-out"></div></div>
    <div class="card"><h2>Registered</h2><p class="sub">Click a row to activate that project.</p>
      <div id="list"></div>
      <div class="btn-row"><button class="btn ghost sm" id="prune">Prune missing (prune)</button></div></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);
  $('#init-root', wrap).textContent = S.root;

  async function reload() {
    const box = $('#list', wrap);
    box.innerHTML = '<p class="note">Loading…</p>';
    const r = await cli(['projects', 'list'], 'listing projects');
    if (r.code !== 0) { showCliError(box, r, 'List projects'); return; }
    let cur = null;
    const list = [];
    for (const line of r.stdout.split(/\r?\n/)) {
      if (/^Registered projects/.test(line) || !line.trim()) continue;
      if (/^  \S/.test(line) && !line.startsWith('    ')) { cur = { name: line.trim(), path: '', data: '' }; list.push(cur); continue; }
      if (cur) {
        const m = line.match(/^\s*(Path|Data):\s*(.+)$/);
        if (m) { if (m[1] === 'Path') cur.path = m[2].trim(); else cur.data = m[2].trim(); }
      }
    }
    if (!list.length) { box.innerHTML = '<div class="empty">No registered projects.</div>'; return; }
    box.innerHTML = `<table class="tbl"><thead><tr><th>Name</th><th>Path</th><th>Data</th><th></th></tr></thead><tbody>` +
      list.map((p, i) => `<tr data-i="${i}" style="cursor:pointer" title="Activate this project">
        <td class="mono"><strong>${esc(p.name)}</strong>${p.path === S.root ? ' <span class="tag hot">active</span>' : ''}</td>
        <td class="mono">${esc(p.path)}</td><td class="mono">${esc(p.data)}</td>
        <td><button class="btn ghost sm" data-act="rm">Remove</button></td></tr>`).join('') + `</tbody></table>`;
    $$sel('tr[data-i]', box).forEach((tr) => {
      const p = list[Number(tr.dataset.i)];
      tr.onclick = (e) => {
        if (e.target.dataset.act === 'rm') return;
        setProject(p.path);
      };
      $('[data-act="rm"]', tr).onclick = async (e) => {
        e.stopPropagation();
        const ok = await confirmModal({ title: 'Remove project', okLabel: 'Remove', danger: true,
          body: `Removes <code>${esc(p.name)}</code> from the global registry. The on-disk index is kept.` });
        if (!ok) return;
        const rr = await cli(['projects', 'remove', p.name], 'removing project');
        if (rr.code === 0) { toast('Project removed from registry', 'ok'); refreshProjects(S.root); reload(); }
        else showCliError(box, rr, 'Remove project');
      };
    });
  }

  $('#init-go', wrap).onclick = async () => {
    const out = $('#init-out', wrap);
    const args = ['init'];
    if ($('#init-local', wrap).checked) args.push('--local');
    if ($('#init-force', wrap).checked) args.push('--force');
    args.push('--extension', $('#init-ext', wrap).value);
    const r = await cli(args, 'initializing');
    out.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Init');
    else { refreshProjects(S.root); reload(); refreshTopStatus(); }
  };
  $('#add-go', wrap).onclick = async () => {
    const out = $('#init-out', wrap);
    const r = await cli(['projects', 'add'], 'registering project');
    out.innerHTML = r.code === 0 ? `<div class="callout ok">${esc((r.stdout || 'Done.').slice(0, 800))}</div>` : '';
    if (r.code !== 0) showCliError(out, r, 'Projects add');
    else { refreshProjects(S.root); reload(); }
  };
  $('#prune', wrap).onclick = async () => {
    const r = await cli(['projects', 'prune'], 'pruning registry');
    if (r.code === 0) { toast(r.stdout.trim().split('\n')[0] || 'Registry pruned', 'ok'); refreshProjects(S.root); reload(); }
    else toast('Prune failed: ' + (r.stderr || '').slice(0, 200), 'err');
  };
  reload();
};
