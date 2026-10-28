'use strict';
/* Doctor: setup diagnostics as seen by this process. */
Views.doctor = async function (view) {
  const wrap = document.createElement('div');
  wrap.className = 'wrap';
  wrap.innerHTML = `<div class="view-head"><h1>Doctor</h1>
    <p>Diagnostics for the project, provider (where the API key comes from, never its value), local Ollama, index and daemon. Command <code>doctor</code>.</p></div>
    <div class="card"><div class="form-row">
      <div class="field"><label>&nbsp;</label><label class="check"><input type="checkbox" id="ping" checked> live provider ping (one embedding request)</label></div>
    </div>
    <div class="btn-row"><button class="btn primary" id="go">Diagnose</button></div></div>
    <div id="out"></div>
    <div class="card"><h2>A note on launchers</h2>
      <p class="sub">Run diagnostics from the same launcher that reports a problem: an MCP gateway or GUI-launched agent does not inherit your shell, so a key visible in your terminal may be missing there. This app inherits the environment of the process that started it (<code>npm start</code>).</p></div>`;
  view.innerHTML = '';
  view.appendChild(wrap);

  $('#go', wrap).onclick = async () => {
    const out = $('#out', wrap);
    out.innerHTML = '<p class="note">Diagnosing…</p>';
    const args = ['doctor', '-f', 'json'];
    if (!$('#ping', wrap).checked) args.push('--no-ping');
    const r = await cliJson(args, 'doctor');
    if (r.code !== 0 || !r.data) { showCliError(out, r, 'Doctor'); return; }
    const d = r.data;
    const badge = d.failed ? `<span class="pill err">${d.failed} failures</span>`
      : d.warned ? `<span class="pill warn">${d.warned} warnings</span>` : '<span class="pill ok">all good</span>';
    out.innerHTML = `<div class="card"><h2>Result ${badge}</h2>
      <p class="sub">vecgrep ${esc(d.version || '')}</p>
      <table class="tbl"><thead><tr><th>Check</th><th>Status</th><th>Detail</th></tr></thead><tbody>
      ${d.checks.map((c) => `<tr><td class="mono"><strong>${esc(c.name)}</strong></td>
        <td><span class="pill ${pillFor(c.status)}">${esc(c.status)}</span></td>
        <td>${esc(c.detail || '')}${c.hint ? `<div class="note">→ ${esc(c.hint)}</div>` : ''}</td></tr>`).join('')}
      </tbody></table>
      <details class="raw"><summary>Raw JSON</summary><pre class="block">${esc(JSON.stringify(d, null, 2).slice(0, 12000))}</pre></details></div>`;
  };
  $('#go', wrap).click();
};
