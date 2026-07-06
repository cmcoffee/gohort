package forge

// forgeHeadHTML is injected into the page <head>. It pulls in xterm.js
// (the same CDN + versions servitor uses), mounts an interactive
// terminal wired to /forge/api/terminal, and puts the two operator
// buttons (Rebuild + Restart, New session) in the terminal's own title
// bar. The buttons call the app's endpoints directly and stream results
// into the terminal — they do NOT go through DisplayPanel toolbar
// actions, which are server-fetch only (a Method:"client" there would
// 404 on the action name). Nothing here leaks into core/ui.
const forgeHeadHTML = `
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css">
<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
<style>
  #forge-term-wrap {
    margin-top: 16px;
    border: 1px solid var(--border, #30363d);
    border-radius: 8px;
    overflow: hidden;
    background: #0d1117;
  }
  #forge-term-bar {
    display: flex; align-items: center; gap: 8px;
    padding: 6px 12px; font: 12px ui-monospace, Menlo, Consolas, monospace;
    color: #8b949e; background: #161b22; border-bottom: 1px solid #30363d;
  }
  #forge-term-bar .dot { width: 8px; height: 8px; border-radius: 50%; background: #3fb950; flex: none; }
  #forge-term-bar.off .dot { background: #f85149; }
  #forge-term-bar .forge-btn {
    font: 11px ui-monospace, Menlo, Consolas, monospace; color: #c9d1d9;
    background: #21262d; border: 1px solid #30363d; border-radius: 5px;
    padding: 3px 10px; cursor: pointer;
  }
  #forge-term-bar .forge-btn:hover { background: #30363d; }
  #forge-term-bar .forge-btn:disabled { opacity: 0.5; cursor: default; }
  #forge-term-bar .forge-btn.danger { color: #f85149; border-color: #5c2b29; }
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

  var term, fit, ws, reconnectTimer, reconnectDelay = 1500, autoReconnect = true;

  function setStatus(connected) {
    var bar = document.getElementById('forge-term-bar');
    if (!bar) return;
    bar.classList.toggle('off', !connected);
    var lbl = bar.querySelector('.lbl');
    if (lbl) lbl.textContent = connected ? 'attached' : 'disconnected';
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

  // Reconnect after "New session".
  window.__forgeReconnect = function () {
    if (term) term.write('\r\n\x1b[36m[forge] restarting session…\x1b[0m\r\n');
    connect();
  };

  // --- operator actions (called by the bar buttons) ------------------
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

  function mount() {
    if (typeof Terminal === 'undefined' || typeof FitAddon === 'undefined') {
      return setTimeout(mount, 100); // CDN still loading
    }
    var anchor = document.querySelector('.ui-section');
    var container = anchor ? anchor.parentElement : document.body;

    var wrap = document.createElement('div');
    wrap.id = 'forge-term-wrap';

    var bar = document.createElement('div');
    bar.id = 'forge-term-bar';
    var dot = document.createElement('span'); dot.className = 'dot';
    var lbl = document.createElement('span'); lbl.className = 'lbl'; lbl.textContent = 'connecting…';
    var right = document.createElement('span');
    right.style.cssText = 'margin-left:auto;display:flex;gap:8px;align-items:center';
    var rebuildBtn = document.createElement('button');
    rebuildBtn.className = 'forge-btn danger'; rebuildBtn.textContent = 'Rebuild + Restart';
    rebuildBtn.onclick = function () { forgeRebuild(rebuildBtn); };
    var newBtn = document.createElement('button');
    newBtn.className = 'forge-btn'; newBtn.textContent = 'New session';
    newBtn.onclick = function () { forgeNewSession(newBtn); };
    right.appendChild(rebuildBtn); right.appendChild(newBtn);
    bar.appendChild(dot); bar.appendChild(lbl); bar.appendChild(right);

    var host = document.createElement('div');
    host.id = 'forge-term';
    wrap.appendChild(bar);
    wrap.appendChild(host);
    container.appendChild(wrap);

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
    try { fit.fit(); } catch (_) {}
    window.__forgeTerm = term;

    new ResizeObserver(function () { try { fit.fit(); } catch (_) {} }).observe(host);
    term.onResize(function (sz) {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: sz.cols, rows: sz.rows }));
      }
    });
    term.onData(function (data) {
      if (ws && ws.readyState === WebSocket.OPEN) ws.send(data);
    });

    connect();
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
