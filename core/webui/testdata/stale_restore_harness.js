// Drives StaleRestoreScript (core/webui/stale_restore.go) with a stubbed window.
// Run by TestStaleRestoreScript; argv[2] is the script body.
const fs = require('fs');
const src = fs.readFileSync(process.argv[2], 'utf8');
function page(store, t0) {
  const listeners = {}; let reloaded = 0; let now = t0;
  const w = {
    location: { href: 'https://gh.example/orchestrate/', origin: 'https://gh.example', reload() { reloaded++; } },
    fetch(input, init) { if (this !== w) throw new Error('Illegal invocation'); return Promise.resolve({ ok: !String(input).includes('fail') }); },
    addEventListener(n, f) { listeners[n] = f; },
  };
  const ctx = { window: w, location: w.location, localStorage: store, URL, Date: { now: () => now }, String, Number, Promise };
  new Function(...Object.keys(ctx), src)(...Object.values(ctx));
  return { w, show: (persisted) => listeners.pageshow({ persisted }), get reloaded() { return reloaded; }, tick(ms) { now += ms; ctx.Date.now = () => now; } };
}
const mem = {}; const store = { getItem: k => (k in mem ? mem[k] : null), setItem: (k, v) => { mem[k] = v; } };
(async () => {
  let fail = 0; const check = (c, m) => { if (!c) { fail++; console.log('FAIL', m); } };
  const dash = page(store, 1000);           // dashboard drawn at t=1000
  const editor = page(store, 2000);         // then the editor, t=2000
  editor.tick(500);
  await editor.w.fetch('/api/agents/x', { method: 'GET' });
  check(!mem.gohort_changed_at, 'a GET stamped');
  await editor.w.fetch('https://other.example/api', { method: 'POST' });
  check(!mem.gohort_changed_at, 'a cross-origin POST stamped');
  await editor.w.fetch('/api/fail', { method: 'POST' });
  check(!mem.gohort_changed_at, 'a failed POST stamped');
  await editor.w.fetch('../api/agents/x', { method: 'DELETE' });
  check(Number(mem.gohort_changed_at) === 2500, 'a successful DELETE did not stamp: ' + mem.gohort_changed_at);
  dash.show(false); check(dash.reloaded === 0, 'a fresh load reloaded');
  dash.show(true);  check(dash.reloaded === 1, 'a stale restore did not reload');
  const later = page(store, 3000);
  later.show(true); check(later.reloaded === 0, 'a page drawn after the save reloaded');
  // wrapping twice (two head tags on one page) must not double-wrap
  const w2 = page(store, 4000).w; const f1 = w2.fetch; new Function('window','location','localStorage','URL','Date','String','Number','Promise', src)(w2, w2.location, store, URL, Date, String, Number, Promise);
  check(w2.fetch === f1, 'fetch wrapped twice');
  console.log(fail ? 'FAILED' : 'ok'); process.exit(fail ? 1 : 0);
})();
