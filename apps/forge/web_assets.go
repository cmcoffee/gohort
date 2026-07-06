package forge

// forgeHeadHTML is injected into the page <head>. The page is
// terminal-first: this script mounts an interactive xterm wired to
// /forge/api/terminal and fills the viewport with it. Live status
// (session + working dir), the operator buttons (Rebuild + Restart, New
// session), and a ⚙ that folds the settings form into a drawer all live
// in the terminal's own title bar, so the visible page is mostly the
// terminal. The buttons call the app's endpoints directly and stream
// results into the terminal (DisplayPanel toolbar actions are
// server-fetch only, so they can't do this). Nothing here leaks into
// core/ui.
const forgeHeadHTML = `
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css">
<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
<style>
  #forge-term-wrap {
    margin-top: 12px;
    border: 1px solid var(--border, #30363d);
    border-radius: 8px;
    overflow: hidden;
    background: #0d1117;
  }
  #forge-term-bar {
    display: flex; align-items: center; gap: 10px;
    padding: 6px 12px; font: 12px ui-monospace, Menlo, Consolas, monospace;
    color: #8b949e; background: #161b22; border-bottom: 1px solid #30363d;
  }
  #forge-term-bar .dot { width: 8px; height: 8px; border-radius: 50%; background: #3fb950; flex: none; }
  #forge-term-bar.off .dot { background: #f85149; }
  #forge-term-bar .lbl { flex: none; }
  #forge-term-bar .status {
    color: #6e7681; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
    min-width: 0;
  }
  #forge-term-bar .spacer { margin-left: auto; flex: none; }
  #forge-term-bar .forge-btn {
    font: 11px ui-monospace, Menlo, Consolas, monospace; color: #c9d1d9;
    background: #21262d; border: 1px solid #30363d; border-radius: 5px;
    padding: 3px 10px; cursor: pointer; flex: none;
  }
  #forge-term-bar .forge-btn:hover { background: #30363d; }
  #forge-term-bar .forge-btn:disabled { opacity: 0.5; cursor: default; }
  #forge-term-bar .forge-btn.danger { color: #f85149; border-color: #5c2b29; }
  #forge-term-bar .forge-gear { padding: 3px 8px; font-size: 13px; }
  #forge-settings-drawer {
    display: none; padding: 12px 14px; background: #0d1117;
    border-bottom: 1px solid #30363d;
  }
  #forge-settings-drawer.open { display: block; }
  /* Let the reparented settings section sit flush inside the drawer. */
  #forge-settings-drawer .ui-section { margin: 0; border: 0; background: transparent; box-shadow: none; }
  #forge-term { height: 460px; padding: 6px 8px; }
</style>
<script>
(function () {
  function makeWsUrl(path) {
    var l = window.location;
    var proto = l.protocol === 'https:' ? 'wss:' : 'ws:';
    var base = l.pathname.replace(/[^/]*$/, ''); // page is /forge/ -> base /forge/
    return proto + '//' + l.host + base + path;
  }

  var term, fit, ws, host, reconnectTimer, reconnectDelay = 1500, autoReconnect = true;
  var statusEl;

  function setStatus(connected) {
    var bar = document.getElementById('forge-term-bar');
    if (!bar) return;
    bar.classList.toggle('off', !connected);
    var lbl = bar.querySelector('.lbl');
    if (lbl) lbl.textContent = connected ? 'attached' : 'disconnected';
  }

  function refreshStatus() {
    fetch('api/status').then(function (r) { return r.json(); }).then(function (d) {
      if (!statusEl || !d) return;
      var parts = [];
      if (d.session) parts.push(d.session);
      if (d.work_dir) parts.push(d.work_dir);
      statusEl.textContent = parts.join('  ·  ');
    }).catch(function () {});
  }

  function connect() {
    if (!term) return;
    autoReconnect = true;
    if (ws) { try { ws.onclose = null; ws.close(); } catch (_) {} }
    ws = new WebSocket(makeWsUrl('api/terminal'));
    ws.binaryType = 'arraybuffer';
    ws.onopen = function () {
      reconnectDelay = 1500;
      setStatus(true);
      try { fit.fit(); } catch (_) {}
      ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
    };
    ws.onmessage = function (e) {
      if (e.data instanceof ArrayBuffer) term.write(new Uint8Array(e.data));
      else term.write(e.data);
    };
    ws.onclose = function () {
      setStatus(false);
      if (autoReconnect) {
        clearTimeout(reconnectTimer);
        reconnectTimer = setTimeout(connect, reconnectDelay);
        reconnectDelay = Math.min(reconnectDelay * 1.5, 8000);
      }
    };
    ws.onerror = function () { try { ws.close(); } catch (_) {} };
  }

  window.__forgeReconnect = function () {
    if (term) term.write('\r\n\x1b[36m[forge] restarting session…\x1b[0m\r\n');
    connect();
  };

  // --- operator actions (bar buttons) --------------------------------
  function forgeRebuild(btn) {
    if (!window.confirm('Rebuild and restart the server?\n\nThe restart only runs if the build succeeds, and only if a restart command is configured.')) return;
    if (term) term.write('\r\n\x1b[36m[forge] building…\x1b[0m\r\n');
    if (btn) btn.disabled = true;
    fetch('api/rebuild', { method: 'POST', headers: { 'Content-Type': 'application/json' } })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (term) {
          if (d.log) term.write((d.log + '').replace(/\n/g, '\r\n') + '\r\n');
          term.write((d.ok ? '\x1b[32m' : '\x1b[31m') + '[forge] ' + (d.message || (d.ok ? 'ok' : 'failed')) + '\x1b[0m\r\n');
        }
      })
      .catch(function (e) { if (term) term.write('\r\n\x1b[31m[forge] ' + e + '\x1b[0m\r\n'); })
      .then(function () { if (btn) btn.disabled = false; });
  }

  function forgeNewSession(btn) {
    if (!window.confirm('Kill the current claude session and start a fresh one?')) return;
    if (btn) btn.disabled = true;
    fetch('api/session/new', { method: 'POST' })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        if (d && d.ok === false && term) term.write('\r\n\x1b[31m[forge] ' + (d.message || 'could not start session') + '\x1b[0m\r\n');
        window.__forgeReconnect();
      })
      .catch(function (e) { if (term) term.write('\r\n\x1b[31m[forge] ' + e + '\x1b[0m\r\n'); })
      .then(function () { if (btn) btn.disabled = false; });
  }

  function sizeTerm() {
    if (!host) return;
    var top = host.getBoundingClientRect().top;
    var h = Math.max(240, window.innerHeight - top - 18);
    host.style.height = h + 'px';
    try { fit.fit(); } catch (_) {}
  }

  function mount() {
    if (typeof Terminal === 'undefined' || typeof FitAddon === 'undefined') {
      return setTimeout(mount, 100); // CDN still loading
    }
    // The single ui.Section is the settings form; fold it into a drawer.
    var settings = document.querySelector('.ui-section');
    var container = settings ? settings.parentElement : document.body;

    var wrap = document.createElement('div');
    wrap.id = 'forge-term-wrap';

    // --- bar ---
    var bar = document.createElement('div');
    bar.id = 'forge-term-bar';
    var dot = document.createElement('span'); dot.className = 'dot';
    var lbl = document.createElement('span'); lbl.className = 'lbl'; lbl.textContent = 'connecting…';
    statusEl = document.createElement('span'); statusEl.className = 'status';
    var spacer = document.createElement('span'); spacer.className = 'spacer';
    var gearBtn = document.createElement('button');
    gearBtn.className = 'forge-btn forge-gear'; gearBtn.title = 'Settings'; gearBtn.textContent = '⚙';
    var newBtn = document.createElement('button');
    newBtn.className = 'forge-btn'; newBtn.textContent = 'New session';
    newBtn.onclick = function () { forgeNewSession(newBtn); };
    var rebuildBtn = document.createElement('button');
    rebuildBtn.className = 'forge-btn danger'; rebuildBtn.textContent = 'Rebuild + Restart';
    rebuildBtn.onclick = function () { forgeRebuild(rebuildBtn); };
    bar.appendChild(dot); bar.appendChild(lbl); bar.appendChild(statusEl);
    bar.appendChild(spacer);
    bar.appendChild(gearBtn); bar.appendChild(newBtn); bar.appendChild(rebuildBtn);

    // --- settings drawer (holds the reparented form) ---
    var drawer = document.createElement('div');
    drawer.id = 'forge-settings-drawer';
    gearBtn.onclick = function () {
      drawer.classList.toggle('open');
      sizeTerm();
    };

    host = document.createElement('div');
    host.id = 'forge-term';

    wrap.appendChild(bar);
    wrap.appendChild(drawer);
    wrap.appendChild(host);
    container.appendChild(wrap);

    // Move the settings form into the drawer (keeps its save wiring intact).
    if (settings) { drawer.appendChild(settings); }

    term = new Terminal({
      theme: { background: '#0d1117', foreground: '#e6edf3', cursor: '#58a6ff', selectionBackground: '#264f78' },
      fontSize: 13,
      fontFamily: 'ui-monospace, Menlo, Consolas, "Courier New", monospace',
      cursorBlink: true,
      scrollback: 5000,
    });
    fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(host);
    window.__forgeTerm = term;

    new ResizeObserver(function () { try { fit.fit(); } catch (_) {} }).observe(host);
    window.addEventListener('resize', sizeTerm);
    sizeTerm();

    term.onResize(function (sz) {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: sz.cols, rows: sz.rows }));
      }
    });
    term.onData(function (data) {
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(data);
    });

    connect();
    refreshStatus();
    setInterval(refreshStatus, 5000);
    term.focus();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', mount);
  } else {
    mount();
  }
})();
</script>
`
