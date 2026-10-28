'use strict';
// Vecgrep Studio — headless smoke test (node smoke.js).
// Exercises the bridge against a real vecgrep without opening windows:
// binary, status, doctor, keyword search (needs no provider),
// projects, config preset, MCP (status + overview + memory_stats).
// Exit code 0 = all good.

const bridge = require('./bridge');

const ROOT = process.env.VECGREP_SMOKE_ROOT || bridge.defaultProjectRoot();
let failures = 0;

function pass(name, detail) {
  console.log(`PASS  ${name}${detail ? ' — ' + detail : ''}`);
}
function fail(name, detail) {
  failures++;
  console.log(`FAIL  ${name}${detail ? ' — ' + detail : ''}`);
}

async function main() {
  console.log(`vecgrep-studio smoke — project: ${ROOT}`);

  // 1. Binary
  let bin;
  try {
    bin = bridge.resolveBinary();
    const info = bridge.binaryVersion(bin);
    if (info.ok) pass('binary', `${bin} (${info.version})`);
    else fail('binary', info.raw.slice(0, 200));
  } catch (e) {
    fail('binary', String(e.message));
    process.exitCode = 1;
    return;
  }

  // 2. status json
  {
    const r = await bridge.runCliJson(bin, ['status', '--format', 'json'], { cwd: ROOT, timeoutMs: 60000 });
    if (r.code === 0 && r.data && typeof r.data.stats === 'object') {
      pass('status', `${r.data.stats.chunks} chunks / ${r.data.stats.files} files`);
    } else {
      fail('status', (r.stderr || r.parseError || `exit ${r.code}`).slice(0, 300));
    }
  }

  // 3. doctor json without ping
  {
    const r = await bridge.runCliJson(bin, ['doctor', '--no-ping', '-f', 'json'], { cwd: ROOT, timeoutMs: 60000 });
    if (r.code === 0 && r.data && Array.isArray(r.data.checks)) {
      pass('doctor', `${r.data.checks.length} checks, ${r.data.failed} failures`);
    } else {
      fail('doctor', (r.stderr || r.parseError || `exit ${r.code}`).slice(0, 300));
    }
  }

  // 4. keyword search (needs no embedding provider)
  {
    const r = await bridge.runCliJson(bin, ['search', 'embedding provider', '-n', '2', '-f', 'json', '-m', 'keyword'], { cwd: ROOT, timeoutMs: 90000 });
    if (r.code === 0 && Array.isArray(r.data)) {
      pass('search keyword', `${r.data.length} results`);
    } else {
      fail('search keyword', (r.stderr || r.parseError || `exit ${r.code}`).slice(0, 300));
    }
  }

  // 5. projects list (text parsing)
  {
    const r = await bridge.runCli(bin, ['projects', 'list'], { cwd: ROOT, timeoutMs: 30000 });
    const list = bridge.parseProjectsList(r.stdout || '');
    if (r.code === 0 && list.length > 0) pass('projects', `${list.length} registered projects`);
    else fail('projects', (r.stderr || 'no projects').slice(0, 300));
  }

  // 6. config preset (list)
  {
    const r = await bridge.runCli(bin, ['config', 'preset'], { cwd: ROOT, timeoutMs: 30000 });
    if (r.code === 0 && /fast-local/.test(r.stdout)) pass('config preset', 'presets listed');
    else fail('config preset', (r.stderr || r.stdout || `exit ${r.code}`).slice(0, 300));
  }

  // 7. daemon status (may be off; answering is enough)
  {
    const r = await bridge.runCli(bin, ['daemon', 'status'], { cwd: ROOT, timeoutMs: 30000 });
    if (r.code === 0) pass('daemon status', r.stdout.trim().split(/\r?\n/)[0]);
    else fail('daemon status', (r.stderr || `exit ${r.code}`).slice(0, 300));
  }

  // 8. cache status
  {
    const r = await bridge.runCli(bin, ['cache', 'status'], { cwd: ROOT, timeoutMs: 30000 });
    if (r.code === 0) pass('cache status', 'ok');
    else fail('cache status', (r.stderr || `exit ${r.code}`).slice(0, 300));
  }

  // 9. MCP: tools/list + vecgrep_status + overview + memory_stats
  const pool = new bridge.McpPool(bin);
  try {
    const client = pool.forRoot(ROOT);
    const tools = await client.listTools();
    const names = tools.map((t) => t.name);
    const need = ['vecgrep_search', 'vecgrep_index', 'vecgrep_status', 'vecgrep_overview',
      'vecgrep_batch_search', 'vecgrep_related_files', 'vecgrep_investigate', 'vecgrep_ensure',
      'memory_remember', 'memory_recall', 'memory_forget', 'memory_stats'];
    const missing = need.filter((n) => !names.includes(n));
    if (missing.length === 0) pass('mcp tools/list', `${names.length} tools`);
    else fail('mcp tools/list', 'missing: ' + missing.join(', '));

    const st = await client.callTool('vecgrep_status', {});
    if (st.text && /Total chunks|Total files/.test(st.text)) pass('mcp vecgrep_status', 'ok');
    else fail('mcp vecgrep_status', (st.error || st.text || 'empty').slice(0, 300));

    const ov = await client.callTool('vecgrep_overview', {});
    if (ov.ok && ov.text) pass('mcp vecgrep_overview', `${ov.text.length} chars`);
    else fail('mcp vecgrep_overview', (ov.error || ov.text || 'empty').slice(0, 300));

    const ms = await client.callTool('memory_stats', {});
    if (ms.ok && ms.text) pass('mcp memory_stats', 'ok');
    else fail('mcp memory_stats', (ms.error || ms.text || 'empty').slice(0, 300));
  } catch (e) {
    fail('mcp', String(e && e.message || e).slice(0, 300));
  } finally {
    pool.stopAll();
  }

  console.log(failures === 0 ? '\nsmoke: ALL GOOD' : `\nsmoke: ${failures} FAILURES`);
  process.exitCode = failures === 0 ? 0 : 1;
}

main().catch((e) => {
  console.error('smoke error:', e);
  process.exitCode = 1;
});
