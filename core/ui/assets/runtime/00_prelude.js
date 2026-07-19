(function() {
  'use strict';

  // --- helpers ----------------------------------------------------------
  // renderBulkBar adds a select-mode toggle pill above a side list.
  // When the pill is active:
  //   - Clicking a list item toggles its selection instead of opening it.
  //   - Selected items get the .selected highlight.
  //   - A bottom action bar shows Delete / Select all / Done buttons.
  // When inactive (default), items behave normally and the bar
  // collapses to just the "Select" pill.
  //
  // The caller drives it via:
  //   - state.mode (bool) — owned by the component, initially false
  //   - selectedMap (object) — mutated by item clicks
  //   - reload() — re-renders the list after any state change
  //   - onDelete() — runs the bulk action
  function renderBulkBar(items, listEl, state, selectedMap, idOf, reload, onDelete) {
    // The "Select" toggle now lives in the sidebar header (next to
    // "+ New") so users see it before scrolling. This bar only
    // surfaces while select mode is active and only renders the
    // contextual controls — Select all + Delete with a live count.
    if (!state.mode) return;
    var idKeys = Object.keys(selectedMap);
    var count = idKeys.length;

    var allActive = items.length > 0 && idKeys.length === items.length;
    var allBtn = el('button', {class: 'ui-row-btn', onclick: function() {
      if (allActive) {
        Object.keys(selectedMap).forEach(function(k){ delete selectedMap[k]; });
        reload();
        return;
      }
      // Scope Select-all to what the search currently SHOWS. Rows are appended
      // AFTER this bar is built, so read visibility at click time. When the list
      // tags its rows (data-bulk-id), select only the ones not hidden by the
      // active filter; otherwise fall back to every item (callers whose rows
      // aren't searchable behave exactly as before).
      var taggedRows = listEl.querySelectorAll('[data-bulk-id]');
      if (taggedRows.length) {
        taggedRows.forEach(function(row) {
          if (row.style.display === 'none') return;
          var k = row.getAttribute('data-bulk-id');
          if (k) selectedMap[k] = true;
        });
      } else {
        items.forEach(function(s){ var k = idOf(s); if (k) selectedMap[k] = true; });
      }
      reload();
    }}, [allActive ? 'Deselect all' : 'Select all']);
    listEl.appendChild(el('div', {class: 'ui-bulk-bar'}, [allBtn]));

    if (count > 0) {
      var delBtn = el('button', {class: 'ui-row-btn danger', onclick: onDelete}, ['Delete (' + count + ')']);
      listEl.appendChild(el('div', {class: 'ui-bulk-bar bottom'}, [delBtn]));
    }
  }

  function el(tag, opts, children) {
    var n = document.createElement(tag);
    if (opts) {
      for (var k in opts) {
        if (k === 'class') n.className = opts[k];
        else if (k === 'text') n.textContent = opts[k];
        else if (k === 'html') n.innerHTML = opts[k];
        else if (k.indexOf('on') === 0) n.addEventListener(k.slice(2), opts[k]);
        else n.setAttribute(k, opts[k]);
      }
    }
    if (children) {
      for (var i = 0; i < children.length; i++) {
        var c = children[i];
        if (c == null) continue;
        n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
      }
    }
    return n;
  }
  function fetchJSON(url, opts) {
    // Live dashboard data — never serve a stale HTTP-cached copy. Embedded
    // webviews (e.g. the gohort-desktop WKWebView behind its proxy) will
    // otherwise cache a list GET on first fetch and keep returning the old
    // body, so a list that was empty when first opened stays empty even
    // after the underlying data changes (the "Add conversation picker is
    // empty in desktop but populated in a browser" bug). no-store forces a
    // fresh fetch every time; callers can still override via opts.cache.
    opts = Object.assign({cache: 'no-store'}, opts || {});
    return fetch(url, opts).then(function(r) {
      if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
      var ct = r.headers.get('Content-Type') || '';
      return ct.indexOf('application/json') >= 0 ? r.json() : r.text();
    });
  }
  function relTime(iso) {
    if (!iso) return '';
    var t = new Date(iso).getTime();
    // Reject NaN, epoch, and Go's zero-value time.Time (year 0001,
    // serialized as "0001-01-01T00:00:00Z") which would otherwise
    // render as ~739763d ago. Any pre-epoch timestamp is treated as
    // "unknown" — we don't have legitimate pre-1970 records.
    if (!t || t <= 0) return '';
    var s = Math.round((Date.now() - t) / 1000);
    if (s < 60) return s + 's ago';
    if (s < 3600) return Math.round(s/60) + 'm ago';
    if (s < 86400) return Math.round(s/3600) + 'h ago';
    return Math.round(s/86400) + 'd ago';
  }
  // Shared minimal markdown renderer. Used by chat_panel for message
  // bubbles and pipeline_panel for transcript blocks. Top-level so
  // any component can call it without scope juggling.
  function mdToHTML(s) {
    s = String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    var fenceRe  = /```([\s\S]*?)```/g;
    var inlineRe = /`([^`\n]+)`/g;
    s = s.replace(fenceRe, function(_, body){ return '<pre><code>' + body.replace(/^\n/, '') + '</code></pre>'; });
    s = s.replace(inlineRe, '<code>$1</code>');
    s = s.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
    s = s.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>');
    // Side-coded headings — h3 lines starting with FOR or AGAINST
    // get rendered with side classes so side-coded sections
    // surface as visually distinct color-coded blocks. Verdict h2
    // gets an amber accent class for legacy parity. Generic
    // headings fall through to plain h1/h2/h3.
    //
    // Each replacement appends an extra \n so when the source has
    // a heading immediately followed by text on the next line
    // (single newline, no blank — common LLM output), the
    // paragraph splitter below sees a double newline between the
    // heading and the loose text and wraps that text in a p tag.
    // Without this, raw text after a heading bypasses the p
    // wrapper and any rule keyed off h2-plus-p never matches.
    s = s.replace(/^### (FOR)\b([^\n]*)$/gm,
      '<h3 class="ui-pl-side-h ui-pl-side-for">$1$2</h3>\n');
    s = s.replace(/^### (AGAINST)\b([^\n]*)$/gm,
      '<h3 class="ui-pl-side-h ui-pl-side-against">$1$2</h3>\n');
    s = s.replace(/^### (.+)$/gm, '<h3>$1</h3>\n');
    s = s.replace(/^## (.+)$/gm, '<h2>$1</h2>\n');
    s = s.replace(/^# (.+)$/gm, '<h1>$1</h1>\n');
    // Markdown links — accept absolute http(s) URLs AND relative
    // paths starting with ?, #, or / so internal cross-links
    // (e.g. ?id=<id> for cross-links) render as anchors.
    // Absolute URLs open in a new tab; relative ones navigate the
    // same window so the page deep-link logic can pick them up.
    s = s.replace(/\[([^\]]+)\]\((https?:[^)]+)\)/g,
      '<a href="$2" target="_blank" rel="noopener">$1</a>');
    s = s.replace(/\[([^\]]+)\]\(([?#/][^)]+)\)/g, '<a href="$2">$1</a>');
    // CommonMark angle-bracket autolinks — <https://url>. The escape
    // pass above turned the brackets into &lt; / &gt;, so match those.
    // This MUST run before the bare-URL pass: otherwise that pass's
    // [^\s<)] char class (which doesn't exclude &) swallows the
    // trailing &gt; into the href, producing a broken link ending in
    // a literal '>'. Non-greedy so the URL stops at its own &gt;
    // closer rather than a later one on the same line.
    s = s.replace(/&lt;(https?:\/\/[^\s<]+?)&gt;/g,
      '<a href="$1" target="_blank" rel="noopener">$1</a>');
    // Auto-link bare http/https URLs that didn't go through the
    // [text](url) replacement above. The (^|[^"'>=]) prefix avoids
    // matching URLs already inside href="..." attributes or as the
    // text content of a freshly-built <a> tag (>url</a>). Trailing
    // punctuation like ".," common at end of sentences gets pulled
    // out of the match.
    s = s.replace(/(^|[^"'>=])(https?:\/\/[^\s<)]+?)([.,;:!?]?(?=\s|$|<|\)))/g,
      '$1<a href="$2" target="_blank" rel="noopener">$2</a>$3');
    s = s.replace(/(^|\n)((?:[-*] .+(?:\n|$))+)/g, function(_, p, block) {
      var items = block.trim().split(/\n/).map(function(line){
        return '<li>' + line.replace(/^[-*]\s+/, '') + '</li>';
      }).join('');
      return p + '<ul>' + items + '</ul>';
    });
    s = s.split(/\n\n+/).map(function(block) {
      var t = block.trim();
      if (!t) return '';
      if (/^<(h[1-3]|pre|ul|ol|p|blockquote)/i.test(t)) return t;
      // Side-coded paragraphs — content starting with "For:" or
      // "Against:" (bolded by an earlier pass) gets a side class
      // so the whole paragraph renders green / red. Matches
      // legacy display where each round emitted colored
      // For:/Against: lines.
      var sideMatch = t.match(/^<strong>(For|Against):<\/strong>/i);
      if (sideMatch) {
        var cls = sideMatch[1].toLowerCase() === 'for'
          ? 'ui-pl-side-for-p' : 'ui-pl-side-against-p';
        return '<p class="' + cls + '">' + t.replace(/\n/g, '<br>') + '</p>';
      }
      return '<p>' + t.replace(/\n/g, '<br>') + '</p>';
    }).join('');
    // App-registered post-processors get a final crack at the
    // rendered HTML. Used for domain-specific markdown extensions
    // (a heading pattern unique to one app, etc.) that shouldn't
    // leak into the shared runtime.
    var exts = window.UIMarkdownExtensions || [];
    for (var ei = 0; ei < exts.length; ei++) {
      try { s = exts[ei](s) || s; } catch (_) {}
    }
    return s;
  }
  // Expose the DOM-constructor + markdown-renderer helpers so app
  // JS payloads (loaded via Page.ExtraHeadHTML) can build block
  // renderers without duplicating these. Per-renderer context (cfg
  // flags, etc.) is threaded through the renderer's second arg, so
  // these globals stay panel-agnostic.
  window.uiEl = el;
  window.uiMdToHTML = mdToHTML;
  // uiRenderMarkdown(target, raw) — the one call that renders markdown
  // into an element. Stamps the .ui-md prose class so the output gets
  // the canonical type scale instead of browser UA defaults, then sets
  // the HTML (mdToHTML has already run the extension post-processors).
  // Use this (or add the ui-md class yourself) for ANY element you fill
  // with rendered markdown — it is what keeps headings / code / lists
  // consistent across every surface.
  // uiStripMetaTags removes framework-internal markers from anything rendered
  // for the user — the reserved <gohort-meta>…</gohort-meta> convention plus
  // leaked delivery markers ([ATTACH:…], <<<ATTACH:…>>>…<<<END>>>). Mirrors core
  // StripMetaTags (Go) so the saved copy and the rendered copy agree.
  window.uiStripMetaTags = function(s) {
    if (!s || (s.indexOf('<gohort-meta') < 0 && s.indexOf('[ATTACH') < 0 && s.indexOf('<<<ATTACH') < 0)) return s;
    return s
      .replace(/<gohort-meta>[\s\S]*?<\/gohort-meta>/gi, '')
      .replace(/\[ATTACH:\s*[^\]]*\]/g, '')
      .replace(/<<<ATTACH:[^>]*>>>[\s\S]*?<<<END>>>/gi, '')
      .replace(/[ \t]+\n/g, '\n')
      .replace(/\n{3,}/g, '\n\n')
      .trim();
  };
  // uiStripEmDashes — deterministic house-style enforcement at the DISPLAY
  // boundary: replace em-dashes (U+2014) with a comma so the model's em-dash
  // tic never reaches the user, regardless of whether a system-prompt rule
  // suppressed it. Mirrors core/textutil StripEmDashes (server side handles
  // saved/exported copies; this handles the live-rendered view, streamed
  // included). Code is preserved: anything inside a fenced ``` block or inline
  // `code` span is left untouched. Fast no-op when no em-dash is present.
  window.uiStripEmDashes = function(s) {
    s = String(s == null ? '' : s);
    if (s.indexOf('—') === -1) return s;
    var codeRe = /```[\s\S]*?```|`[^`\n]*`/g, out = '', last = 0, m;
    function tx(seg) {
      if (seg.indexOf('—') === -1) return seg;
      seg = seg.replace(/[ \t]*—[ \t]*/g, ', ');
      seg = seg.replace(/^,[ \t]*/gm, '');       // line-leading dash artifact
      seg = seg.replace(/, \n/g, ',\n');          // trailing space before newline
      return seg.replace(/, ,/g, ',');
    }
    while ((m = codeRe.exec(s)) !== null) {
      out += tx(s.slice(last, m.index)) + m[0];   // prose transformed, code verbatim
      last = m.index + m[0].length;
    }
    return out + tx(s.slice(last));
  };
  window.uiRenderMarkdown = function(target, raw) {
    if (!target) return;
    target.classList.add('ui-md');
    target.innerHTML = mdToHTML(window.uiStripEmDashes(window.uiStripMetaTags(String(raw == null ? '' : raw))));
  };
  // Markdown extension registry — apps add post-processors that
  // run after base mdToHTML passes complete.
  if (!window.UIMarkdownExtensions) window.UIMarkdownExtensions = [];
  window.uiRegisterMarkdownExtension = function(fn) {
    if (typeof fn === 'function') window.UIMarkdownExtensions.push(fn);
  };
  // Block renderer registry — apps register types via JS shipped
  // through Page.ExtraHeadHTML. Hoisted to module scope (not buried
  // inside pipeline_panel's render function) so it exists at
  // DOMContentLoaded time, BEFORE the panel mounts. Without this,
  // an app's deferred DOMContentLoaded handler would find
  // uiRegisterBlockRenderer undefined and fail to register
  // anything.
  if (!window.UIBlockRenderers) window.UIBlockRenderers = {};
  window.uiRegisterBlockRenderer = function(name, fn) {
    if (typeof fn === 'function') window.UIBlockRenderers[name] = fn;
  };
  // Client-action registry — toolbar buttons with Method="client"
  // call into one of these handlers by name. Lets apps wire
  // browser-side actions (window.print, copy-to-clipboard with
  // custom shape, etc.) without needing a server round-trip.
  if (!window.UIClientActions) window.UIClientActions = {};
  window.uiRegisterClientAction = function(name, fn) {
    if (typeof fn === 'function') window.UIClientActions[name] = fn;
  };

  // uiConfirm / uiAlert / uiPrompt — always-async dialog wrappers. Use
  // these instead of native confirm() / alert() / prompt() anywhere in
  // the runtime or apps; callers await the result (uiPrompt resolves to
  // the entered string or null on cancel; uiConfirm to a bool).
  //
  // Native dialogs are inconsistent across hosts and broken in some:
  // Wails' WKWebView on macOS leaves runJavaScriptConfirmPanel /
  // AlertPanel / TextInputPanel unimplemented, so confirm() returns
  // false, alert() does nothing, prompt() returns null. Resolution
  // order:
  //   1. a host-injected impl (window.__uiConfirmImpl / __uiAlertImpl /
  //      __uiPromptImpl) — e.g. gohort-desktop's native-styled modal;
  //   2. otherwise uiDefaultModal — a themed in-page dialog that matches
  //      the rest of the UI.
  // We NO LONGER fall through to native confirm/alert/prompt, so every
  // host gets a real, consistent dialog instead of the browser's
  // default chrome.
  function uiDefaultModal(opts) {
    opts = opts || {};
    return new Promise(function(resolve) {
      var dlg = document.createElement('dialog');
      dlg.className = 'ui-modal-dialog';
      var msg = document.createElement('div');
      msg.className = 'ui-modal-msg';
      msg.textContent = opts.msg || '';
      dlg.appendChild(msg);
      var input = null;
      if (opts.kind === 'prompt') {
        input = document.createElement('input');
        input.type = 'text';
        input.className = 'ui-modal-input';
        input.value = (opts.def != null ? opts.def : '');
        dlg.appendChild(input);
      }
      var actions = document.createElement('div');
      actions.className = 'ui-modal-actions';
      function done(v) { try { dlg.close(); } catch (_) {} dlg.remove(); resolve(v); }
      function mkBtn(label, primary, val) {
        var b = document.createElement('button');
        b.type = 'button';
        b.className = 'ui-row-btn' + (primary ? ' primary' : '');
        b.textContent = label;
        b.addEventListener('click', function() {
          done(opts.kind === 'prompt' && primary ? (input ? input.value : null) : val);
        });
        return b;
      }
      if (opts.kind === 'confirm') {
        actions.appendChild(mkBtn('Cancel', false, false));
        actions.appendChild(mkBtn(opts.ok || 'OK', true, true));
      } else if (opts.kind === 'prompt') {
        actions.appendChild(mkBtn('Cancel', false, null));
        actions.appendChild(mkBtn(opts.ok || 'OK', true, null));
      } else {
        actions.appendChild(mkBtn(opts.ok || 'OK', true, undefined));
      }
      dlg.appendChild(actions);
      // Escape closes with the cancel value; Enter confirms / submits.
      dlg.addEventListener('cancel', function(e) {
        e.preventDefault();
        done(opts.kind === 'confirm' ? false : (opts.kind === 'prompt' ? null : undefined));
      });
      dlg.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' && opts.kind !== 'alert') {
          e.preventDefault();
          done(opts.kind === 'confirm' ? true : (input ? input.value : null));
        }
      });
      document.body.appendChild(dlg);
      if (typeof dlg.showModal === 'function') dlg.showModal(); else dlg.setAttribute('open', '');
      if (input) { input.focus(); try { input.select(); } catch (_) {} }
      else { var bs = actions.querySelectorAll('button'); if (bs.length) bs[bs.length - 1].focus(); }
    });
  }
  window.uiConfirm = function(msg) {
    if (typeof window.__uiConfirmImpl === 'function') return Promise.resolve(window.__uiConfirmImpl(msg));
    return uiDefaultModal({kind: 'confirm', msg: msg});
  };
  window.uiAlert = function(msg) {
    if (typeof window.__uiAlertImpl === 'function') return Promise.resolve(window.__uiAlertImpl(msg));
    return uiDefaultModal({kind: 'alert', msg: msg});
  };
  window.uiPrompt = function(msg, def) {
    if (typeof window.__uiPromptImpl === 'function') return Promise.resolve(window.__uiPromptImpl(msg, def));
    return uiDefaultModal({kind: 'prompt', msg: msg, def: def});
  };
  // Data-source invalidation. Apps and components fire this when a
  // write completes so any list/table fetched from the same source
  // can refetch. Pattern:
  //   window.uiInvalidate('api/queue')          // exact URL match
  //   window.uiInvalidate(['api/queue', ...])    // multiple sources
  // Listeners (Tables, etc.) compare their cfg.source to detail and
  // call their own reload() on match. Avoids polling and avoids
  // wiring every action handler to a specific list refresh.
  window.uiInvalidate = function(source) {
    var sources = Array.isArray(source) ? source : [source];
    try {
      window.dispatchEvent(new CustomEvent('ui-data-changed',
        {detail: {sources: sources}}));
    } catch (_) {}
  };
  // uiInvalidateSaved — fire invalidation after a FORM save: the form's declared
  // invalidate sources PLUS the endpoint it just wrote to (post_url, else source).
  // This makes a list reading the SAME endpoint refresh automatically — so "add a
  // record → it appears in the table" works even when the author didn't set an
  // explicit invalidate. The common form→list pattern stays live with no wiring.
  window.uiInvalidateSaved = function(cfg) {
    var inv = (cfg && Array.isArray(cfg.invalidate)) ? cfg.invalidate.slice() : [];
    var target = cfg && (cfg.post_url || cfg.source);
    if (target && inv.indexOf(target) < 0) inv.push(target);
    if (inv.length) window.uiInvalidate(inv);
  };
  // Message decorator registry — fires after a message is finalized
  // (markdown pass complete). Apps register a function that gets
  // {role, id, wrap, body, rawText} and can append affordances to
  // the wrap (e.g. "Save to TechWriter" / "Save to Workspace"
  // buttons under each assistant reply). One registry is shared
  // across panels so a single registration covers chat / agent
  // loop / pipeline reply rendering uniformly.
  if (!window.UIMessageDecorators) window.UIMessageDecorators = [];
  window.uiRegisterMessageDecorator = function(fn) {
    if (typeof fn === 'function') window.UIMessageDecorators.push(fn);
  };

  // window.uiOpenModal — THE shared modal primitive. A plain fixed-overlay
  // div, NOT native <dialog>: <dialog>+showModal renders blank on some iOS /
  // older-Android WebViews, so every gohort modal routes through this. It
  // owns the overlay, centered card, scrollable body, Escape-to-close, and
  // teardown. No backdrop-click-to-close — a text-selection drag that ends on
  // the backdrop would dismiss the modal mid-copy; dismiss via Escape or a
  // footer button.
  //
  // Options:
  //   title      string  — header text (omit to skip the header)
  //   subtitle   string  — secondary description under the header
  //   width      string  — CSS max-width (default "640px")
  //   closeLabel string  — label for the default Close button
  //   actions    array   — footer buttons [{label, primary?, onClick?(api, btn)}].
  //              Omit for a single Close button; pass [] for NO footer (the
  //              mount callback can build its own footer against api.close).
  //              A button with no onClick just closes.
  //   mount      fn(bodyEl, api) — called after the modal is shown; fill bodyEl.
  //
  // Returns { overlay, dialog, body, close, primaryButton }. `dialog` is the
  // card element; `close` tears the whole overlay down.
  window.uiOpenModal = function(opts) {
    opts = opts || {};
    var overlay = document.createElement('div');
    overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;padding:1rem;box-sizing:border-box';
    var dlg = document.createElement('div');
    dlg.style.cssText = 'box-sizing:border-box;background:var(--bg-1);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:1rem;width:100%;max-width:' + (opts.width || '640px') + ';max-height:88vh;display:flex;flex-direction:column';
    overlay.appendChild(dlg);
    function close() { overlay.remove(); document.removeEventListener('keydown', onKey); }
    function onKey(ev) { if (ev.key === 'Escape') close(); }
    document.addEventListener('keydown', onKey);
    if (opts.title) {
      var h = document.createElement('h3');
      h.style.cssText = 'margin:0 0 0.5rem';
      h.textContent = opts.title;
      dlg.appendChild(h);
    }
    if (opts.subtitle) {
      var sub = document.createElement('p');
      sub.style.cssText = 'margin:0 0 0.8rem;font-size:0.82rem;color:var(--text-mute);line-height:1.45';
      sub.textContent = opts.subtitle;
      dlg.appendChild(sub);
    }
    var body = document.createElement('div');
    // flex:1 1 auto (not flex:1) — flex-basis:0 collapses to zero height in
    // WKWebView inside an indefinite-height flex column (the card).
    body.style.cssText = 'overflow-y:auto;flex:1 1 auto;min-height:0;padding-right:0.3rem;display:flex;flex-direction:column;gap:1rem;-webkit-overflow-scrolling:touch';
    dlg.appendChild(body);
    var api = { overlay: overlay, dialog: dlg, body: body, close: close, primaryButton: null };
    // Footer: omit opts.actions -> single Close; [] -> no footer (mount builds
    // its own); otherwise one button per entry.
    var actions = opts.actions;
    if (actions === undefined) actions = [{label: opts.closeLabel || 'Close', primary: true}];
    if (actions && actions.length) {
      var bar = document.createElement('div');
      bar.style.cssText = 'display:flex;gap:0.5rem;justify-content:flex-end;margin-top:0.8rem;padding-top:0.6rem;border-top:1px solid var(--border)';
      actions.forEach(function(a) {
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'ui-row-btn' + (a.primary ? ' primary' : '');
        btn.textContent = a.label;
        btn.addEventListener('click', function() {
          if (typeof a.onClick === 'function') a.onClick(api, btn);
          else close();
        });
        if (a.primary && !api.primaryButton) api.primaryButton = btn;
        bar.appendChild(btn);
      });
      dlg.appendChild(bar);
    }
    document.body.appendChild(overlay);
    if (typeof opts.mount === 'function') {
      try { opts.mount(body, api); } catch (e) {
        console.error('uiOpenModal mount failed:', e);
      }
    }
    return api;
  };

  // window.uiOpenSimpleModal — backward-compatible wrapper over uiOpenModal.
  // Kept so existing callers keep working; the second mount arg is the card
  // element with .close()/.remove() wired to the full teardown, matching the
  // old <dialog> contract those callers relied on (some call dlg.close();
  // dlg.remove() to dismiss). Returns { dialog, body, close }.
  window.uiOpenSimpleModal = function(opts) {
    opts = opts || {};
    var userMount = opts.mount;
    var m = window.uiOpenModal({
      title: opts.title,
      subtitle: opts.subtitle,
      width: opts.width || '680px',
      closeLabel: opts.closeLabel || 'Close',
      mount: function(body, api) {
        // Preserve the legacy dlg.close()/dlg.remove() idiom: both tear the
        // whole overlay down, not just the inner card.
        api.dialog.close = api.close;
        api.dialog.remove = api.close;
        if (typeof userMount === 'function') userMount(body, api.dialog);
      },
    });
    return { dialog: m.dialog, body: m.body, close: m.close };
  };

  // window.uiOpenArtifactPane — THE shared viewer/previewer pane. A fixed
  // drawer that slides in from the right and renders an HTML surface
  // beside whatever page is showing. Two content modes with distinct
  // trust postures:
  //
  //   html — LLM/app-authored document, rendered via srcdoc in a
  //          SANDBOXED iframe (allow-scripts only): scripts run but the
  //          document has an opaque origin — no cookies, no parent DOM,
  //          no direct same-origin fetches. Dashboards, reports, mockups.
  //          Live data reaches it ONLY through the declared-allowlist
  //          bridge below (data_urls + gohort.fetch).
  //   url  — a SAME-ORIGIN relative path ("/custom/foo/"), rendered as a
  //          normal iframe WITHOUT sandbox: it's this app's own page,
  //          the same trust as the user opening it in a tab. Used to
  //          preview real served surfaces (a just-authored custom app).
  //          Anything not starting with a single "/" is refused.
  //
  // Singleton: opening while a pane is up replaces its content in place
  // (keyed by opts.id).
  //
  // Options:
  //   id        string — artifact identity; a repeat open with the same id
  //             updates the existing pane instead of flashing a new one
  //   title     string — header text
  //   html      string — a complete, self-contained HTML document (srcdoc)
  //   url       string — same-origin relative path to preview (wins over html)
  //   data_urls array  — html mode only: same-origin GET paths the document
  //             may fetch live through the postMessage bridge. The pane
  //             injects a gohort.fetch(path) helper into the document; a
  //             request for any path NOT on this list is refused up here
  //             in the privileged side, so the artifact can only see the
  //             endpoints it declared.
  //
  // Returns { update(opts), close, isOpen() }. Also exposed as
  // window.__uiArtifactPane while open so block renderers can live-update
  // a pane the user already has showing.
  window.uiOpenArtifactPane = function(opts) {
    opts = opts || {};
    var cur = window.__uiArtifactPane;
    if (cur && cur.isOpen()) { cur.update(opts); return cur; }
    var pane = document.createElement('div');
    pane.style.cssText = 'position:fixed;top:0;right:0;bottom:0;z-index:900;display:flex;flex-direction:column;' +
      'width:min(92vw, max(430px, 46vw));background:var(--bg-1);color:var(--text);' +
      'border-left:1px solid var(--border);box-shadow:-8px 0 24px rgba(0,0,0,0.25)';
    // Header: title + open-in-tab + close.
    var hdr = document.createElement('div');
    hdr.style.cssText = 'display:flex;align-items:center;gap:0.5rem;padding:0.5rem 0.8rem;border-bottom:1px solid var(--border);flex:0 0 auto';
    var ttl = document.createElement('div');
    ttl.style.cssText = 'flex:1 1 auto;min-width:0;font-size:0.85rem;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis';
    var popBtn = document.createElement('button');
    popBtn.type = 'button'; popBtn.className = 'ui-row-btn'; popBtn.textContent = '↗';
    popBtn.title = 'Open in a new tab';
    var closeBtn = document.createElement('button');
    closeBtn.type = 'button'; closeBtn.className = 'ui-row-btn'; closeBtn.textContent = '✕';
    closeBtn.title = 'Close';
    hdr.appendChild(ttl); hdr.appendChild(popBtn); hdr.appendChild(closeBtn);
    pane.appendChild(hdr);
    // Left-edge drag handle — resize by dragging toward/away from the chat.
    var grip = document.createElement('div');
    grip.style.cssText = 'position:absolute;left:-3px;top:0;bottom:0;width:7px;cursor:col-resize;z-index:1';
    pane.appendChild(grip);
    grip.addEventListener('mousedown', function(ev) {
      ev.preventDefault();
      var startX = ev.clientX, startW = pane.getBoundingClientRect().width;
      function move(e) {
        var w = Math.min(window.innerWidth * 0.92, Math.max(320, startW + (startX - e.clientX)));
        pane.style.width = w + 'px';
      }
      function up() { document.removeEventListener('mousemove', move); document.removeEventListener('mouseup', up); }
      document.addEventListener('mousemove', move);
      document.addEventListener('mouseup', up);
    });
    var frame = document.createElement('iframe');
    frame.style.cssText = 'flex:1 1 auto;min-height:0;width:100%;border:0;background:#fff';
    pane.appendChild(frame);
    // sameOriginPath accepts only a relative path on THIS origin: one
    // leading "/" (not "//host"), no scheme. Everything else is refused —
    // the url mode renders UNsandboxed, so it must never frame foreign
    // content.
    function sameOriginPath(u) {
      return typeof u === 'string' && u.charAt(0) === '/' && u.charAt(1) !== '/';
    }
    var state = {id: '', html: '', url: '', dataUrls: [], dataKey: ''};
    // gohort.fetch shim, injected into authored documents that declared
    // data_urls. Child side of the bridge: postMessage the request up,
    // resolve/reject on the reply. The parent side (onBridgeMsg below) is
    // the privileged half that enforces the allowlist and does the real
    // same-origin GET. The closing script tag is split so this string can
    // never terminate an enclosing <script> block.
    var BRIDGE_SHIM = '<script>(function(){var seq=0,pend={};' +
      'window.addEventListener("message",function(ev){if(ev.source!==window.parent)return;' +
      'var d=ev.data;if(!d||d.gohort_fetch_id==null||!pend[d.gohort_fetch_id])return;' +
      'var p=pend[d.gohort_fetch_id];delete pend[d.gohort_fetch_id];' +
      'if(d.ok)p.res({ok:true,status:d.status,body:d.body});' +
      'else p.rej(new Error(d.body||("HTTP "+d.status)));});' +
      'window.gohort={fetch:function(url){seq++;var id=seq;' +
      'return new Promise(function(res,rej){pend[id]={res:res,rej:rej};' +
      'window.parent.postMessage({gohort_fetch:url,gohort_fetch_id:id},"*");' +
      'setTimeout(function(){if(pend[id]){delete pend[id];rej(new Error("gohort.fetch timeout"));}},20000);});}};' +
      '})();<' + '/script>';
    function withBridge(html, dataUrls) {
      if (!dataUrls || !dataUrls.length) return html;
      var m = html.match(/<head[^>]*>/i);
      if (m) return html.replace(m[0], m[0] + BRIDGE_SHIM);
      return BRIDGE_SHIM + html;
    }
    function update(o) {
      o = o || {};
      state.id = o.id || state.id;
      if (o.title != null) ttl.textContent = o.title || 'Artifact';
      if (o.url != null && o.url !== '' && sameOriginPath(o.url)) {
        // Preview mode: our own served page, normal iframe (no sandbox —
        // the app's page needs its own cookies/scripts to function, and
        // it's the same trust as the user opening the path in a tab).
        if (o.url !== state.url) {
          state.url = o.url; state.html = '';
          state.dataUrls = []; state.dataKey = '';
          frame.removeAttribute('srcdoc');
          frame.removeAttribute('sandbox');
          frame.setAttribute('src', o.url);
        }
      } else if (o.html != null) {
        // Authored-document mode: sandboxed srcdoc. allow-scripts WITHOUT
        // allow-same-origin — opaque origin, no cookies/storage/parent
        // DOM. Do not widen this. Sandbox must be in place before the
        // content attribute so the new document loads under it.
        var dataUrls = Array.isArray(o.data_urls) ? o.data_urls.filter(sameOriginPath) : [];
        var dataKey = dataUrls.join('|');
        if (o.html !== state.html || dataKey !== state.dataKey) {
          state.html = o.html; state.url = '';
          state.dataUrls = dataUrls; state.dataKey = dataKey;
          frame.removeAttribute('src');
          frame.setAttribute('sandbox', 'allow-scripts');
          frame.setAttribute('srcdoc', withBridge(o.html, dataUrls));
        }
      }
    }
    // Parent half of the live-data bridge. Only answers the CURRENT
    // frame's document, and only for GET paths on the artifact's declared
    // allowlist (exact path match; query string free). The response text
    // goes back into the sandboxed page — nothing else crosses.
    function pathAllowed(u) {
      if (!sameOriginPath(u)) return false;
      var path = u.split('?')[0].split('#')[0];
      for (var i = 0; i < state.dataUrls.length; i++) {
        if (state.dataUrls[i] === path) return true;
      }
      return false;
    }
    function onBridgeMsg(ev) {
      if (ev.source !== frame.contentWindow) return;
      var d = ev.data;
      if (!d || typeof d.gohort_fetch !== 'string' || d.gohort_fetch_id == null) return;
      var url = d.gohort_fetch, id = d.gohort_fetch_id;
      function reply(ok, status, body) {
        try { frame.contentWindow.postMessage({gohort_fetch_id: id, ok: ok, status: status, body: body}, '*'); } catch (_) {}
      }
      if (!pathAllowed(url)) {
        reply(false, 0, 'path not in this artifact\'s data_urls allowlist');
        return;
      }
      fetch(url, {credentials: 'same-origin'})
        .then(function(r) { return r.text().then(function(t) { reply(r.ok, r.status, t); }); })
        .catch(function(e) { reply(false, 0, String(e && e.message || e)); });
    }
    window.addEventListener('message', onBridgeMsg);
    function close() {
      window.removeEventListener('message', onBridgeMsg);
      pane.remove();
      if (window.__uiArtifactPane === api) window.__uiArtifactPane = null;
    }
    popBtn.addEventListener('click', function() {
      try {
        if (state.url) { window.open(state.url, '_blank'); return; }
        var blob = new Blob([state.html || ''], {type: 'text/html'});
        window.open(URL.createObjectURL(blob), '_blank');
      } catch (_) {}
    });
    closeBtn.addEventListener('click', close);
    var api = {
      update: update,
      close: close,
      isOpen: function() { return document.body.contains(pane); },
      matches: function(id) { return !!id && id === state.id; },
    };
    document.body.appendChild(pane);
    update(opts);
    window.__uiArtifactPane = api;
    return api;
  };

  // Default renderer for the generic "html_artifact" block — the server
  // side of the viewer pane (e.g. a show_html tool call). Drops a compact
  // card into the conversation with an Open button, and auto-opens the
  // pane when the block arrives live with open:true (replayed blocks
  // omit the flag, so reloading a session never pops the pane unasked).
  // Re-emitting the same block id routes into onUpdate: the card retitles
  // and an open pane showing this artifact refreshes in place. Payload is
  // either {html} (authored document, sandboxed) or {url} (same-origin
  // page preview) — see uiOpenArtifactPane for the trust postures.
  window.uiRegisterBlockRenderer('html_artifact', function(d) {
    var state = {id: d.id || '', title: d.title || 'Artifact', html: d.html || '', url: d.url || '',
                 dataUrls: Array.isArray(d.data_urls) ? d.data_urls : []};
    function paneOpts() {
      return {id: state.id, title: state.title, html: state.html, url: state.url, data_urls: state.dataUrls};
    }
    var wrap = document.createElement('div');
    wrap.style.cssText = 'display:flex;align-items:center;gap:0.6rem;margin:0.5rem 0;padding:0.55rem 0.8rem;' +
      'border:1px solid var(--border);border-left:3px solid var(--accent, #6366f1);border-radius:6px;background:var(--bg-1)';
    var icon = document.createElement('span');
    var label = document.createElement('div');
    label.style.cssText = 'flex:1 1 auto;min-width:0;font-size:0.85rem;white-space:nowrap;overflow:hidden;text-overflow:ellipsis';
    var btn = document.createElement('button');
    btn.type = 'button'; btn.className = 'ui-row-btn'; btn.textContent = 'Open';
    btn.addEventListener('click', function() { window.uiOpenArtifactPane(paneOpts()); });
    wrap.appendChild(icon); wrap.appendChild(label); wrap.appendChild(btn);
    function refresh() {
      icon.textContent = state.url ? '🖥️' : '📊';
      label.textContent = state.title;
    }
    refresh();
    if (d.open) window.uiOpenArtifactPane(paneOpts());
    return {
      wrap: wrap,
      body: label,
      onUpdate: function(nd) {
        if (nd.title != null) state.title = nd.title;
        if (nd.html != null) { state.html = nd.html; state.url = ''; }
        if (nd.url != null && nd.url !== '') { state.url = nd.url; state.html = ''; }
        if (Array.isArray(nd.data_urls)) state.dataUrls = nd.data_urls;
        refresh();
        var pane = window.__uiArtifactPane;
        if (pane && pane.isOpen() && pane.matches(state.id)) {
          pane.update(paneOpts());
        } else if (nd.open) {
          window.uiOpenArtifactPane(paneOpts());
        }
      },
    };
  });

  // Default renderer for the generic "link_hint" block — a navigation
  // card an agent emits to point the user at a page (e.g. a show_link
  // tool call). Same trust rule as table link columns: same-origin
  // paths ("/...", but not protocol-relative "//...") and http(s) URLs
  // get a real anchor; anything else renders the card without one.
  // Always a new tab — the conversation stays put.
  window.uiRegisterBlockRenderer('link_hint', function(d) {
    var state = {title: d.title || 'Link', url: d.url || '', text: d.text || ''};
    var wrap = document.createElement('div');
    wrap.style.cssText = 'display:flex;align-items:center;gap:0.6rem;margin:0.5rem 0;padding:0.55rem 0.8rem;' +
      'border:1px solid var(--border);border-left:3px solid var(--accent, #6366f1);border-radius:6px;background:var(--bg-1)';
    var icon = document.createElement('span');
    icon.textContent = '🔗';
    var label = document.createElement('div');
    label.style.cssText = 'flex:1 1 auto;min-width:0;font-size:0.85rem;overflow:hidden';
    var title = document.createElement('div');
    title.style.cssText = 'white-space:nowrap;overflow:hidden;text-overflow:ellipsis';
    var note = document.createElement('div');
    note.style.cssText = 'font-size:0.78rem;color:var(--text-mute);white-space:nowrap;overflow:hidden;text-overflow:ellipsis';
    label.appendChild(title); label.appendChild(note);
    var a = document.createElement('a');
    a.className = 'ui-row-btn';
    a.style.cssText = 'text-decoration:none;display:inline-flex;align-items:center';
    a.textContent = 'Open';
    wrap.appendChild(icon); wrap.appendChild(label); wrap.appendChild(a);
    function safeHref(u) {
      u = String(u || '');
      if (/^https?:\/\//.test(u)) return u;
      if (u.charAt(0) === '/' && u.charAt(1) !== '/') return u;
      return '';
    }
    function refresh() {
      title.textContent = state.title;
      note.textContent = state.text;
      note.style.display = state.text ? '' : 'none';
      var href = safeHref(state.url);
      if (href) {
        a.setAttribute('href', href);
        a.setAttribute('target', '_blank');
        a.setAttribute('rel', 'noopener');
        a.style.display = 'inline-flex';
      } else {
        a.removeAttribute('href');
        a.style.display = 'none';
      }
    }
    refresh();
    return {
      wrap: wrap,
      body: title,
      onUpdate: function(nd) {
        if (nd.title != null) state.title = nd.title;
        if (nd.url != null) state.url = nd.url;
        if (nd.text != null) state.text = nd.text;
        refresh();
      },
    };
  });

  function fmt(value, format) {
    if (value == null) return '';
    if (format === 'reltime') return relTime(value);
    if (format === 'fromnow') return fromNow(value);
    if (format === 'bytes')   return fmtBytes(value);
    if (format === 'duration') return fmtDuration(value);
    if (format === 'thousands') return fmtThousands(value);
    return String(value);
  }
  // fromNow renders an ISO timestamp as a signed relative time —
  // future ("in 5m") or past ("5m ago"). Reltime only handles past.
  // Use for fields like "run_at" on scheduled tasks where the value
  // is almost always in the future.
  function fromNow(iso) {
    if (!iso) return '';
    var t = new Date(iso).getTime();
    if (!t) return '';
    var diff = t - Date.now(); // positive = future
    var future = diff > 0;
    var s = Math.abs(Math.round(diff / 1000));
    var label;
    if (s < 5)        label = 'now';
    else if (s < 60)  label = s + 's';
    else if (s < 3600) label = Math.round(s/60) + 'm';
    else if (s < 86400) label = Math.round(s/3600) + 'h';
    else label = Math.round(s/86400) + 'd';
    if (label === 'now') return 'now';
    return future ? ('in ' + label) : (label + ' ago');
  }
  function fmtThousands(n) {
    var v = Number(n);
    if (isNaN(v)) return String(n);
    return v.toLocaleString();
  }
  function fmtBytes(b) {
    var n = Number(b);
    if (isNaN(n)) return String(b);
    if (n < 1024) return n + ' B';
    if (n < 1024*1024) return (n/1024).toFixed(1) + ' KB';
    if (n < 1024*1024*1024) return (n/(1024*1024)).toFixed(1) + ' MB';
    return (n/(1024*1024*1024)).toFixed(2) + ' GB';
  }
  function fmtDuration(s) {
    var n = Number(s);
    if (isNaN(n)) return String(s);
    if (n < 60) return n.toFixed(0) + 's';
    if (n < 3600) return (n/60).toFixed(0) + 'm';
    if (n < 86400) return (n/3600).toFixed(1) + 'h';
    return (n/86400).toFixed(1) + 'd';
  }
  function substitute(template, record, keepUnknown) {
    // Allow lowercase, uppercase, digits, underscore, and dots so dotted
    // paths like "tool.name" work for endpoints with nested records.
    // keepUnknown=true leaves placeholders intact when the record has
    // no matching field — used by the Expand-time pre-substitution so
    // a nested component's later per-row substitution can still resolve
    // its own placeholders (e.g. inner Table's id-placeholder survives
    // outer expand's chat_id pass when the outer rec has no id field).
    return template.replace(/\{([A-Za-z0-9_.]+)\}/g, function(match, key) {
      var v = lookup(record, key);
      if (v == null) return keepUnknown ? match : '';
      return encodeURIComponent(v);
    });
  }
  // lookup walks a dotted path through an object: "tool.name" against
  // {tool:{name:"x"}} returns "x". Also handles plain field lookups
  // (no dot) — just returns obj[path]. Returns undefined for any
  // intermediate null/undefined, so callers can treat the result as
  // a single optional value without nested null checks.
  function lookup(obj, path) {
    if (obj == null) return undefined;
    if (path.indexOf('.') < 0) return obj[path];
    var parts = path.split('.');
    var cur = obj;
    for (var i = 0; i < parts.length; i++) {
      if (cur == null) return undefined;
      cur = cur[parts[i]];
    }
    return cur;
  }
  function showToast(msg) {
    var t = el('div', {class: 'ui-toast'}, [msg]);
    t.style.cssText = 'position:fixed;bottom:1.5rem;left:50%;transform:translateX(-50%);background:var(--bg-2);border:1px solid var(--border);color:var(--text);padding:0.6rem 1rem;border-radius:8px;z-index:50;box-shadow:0 4px 12px rgba(0,0,0,0.4);font-size:0.85rem;';
    document.body.appendChild(t);
    setTimeout(function(){ t.remove(); }, 2500);
  }

  // parseRules splits a free-form rules string into an array of
  // individual rule strings. Splits on newlines, strips common bullet
  // and number prefixes ("1. ", "2)", "- ", "* ") so existing rules
  // round-trip through the rules-list editor without doubling-up.
  function parseRules(s) {
    if (!s) return [];
    return s.split(/\r?\n/).map(function(line) {
      var t = line.trim();
      t = t.replace(/^\d+[.)]\s*/, '');
      t = t.replace(/^[-*]\s+/, '');
      return t;
    }).filter(function(t){ return t !== ''; });
  }

  // renderSideHeader builds the standard sidebar header used by chat,
  // pipeline, and article-editor panels. Layout is:
  //   [Label (flex:1), ...extras, + New, × close (mobile only)]
  // so + New always sits at the right edge on desktop and × close
  // takes that spot on mobile when the drawer is open. Returns the
  // built header element plus references to the close + new buttons
  // so callers can wire additional behavior (e.g. tw's collapse, or
  // Select toggle that lives between extras and + New).
  //
  // opts:
  //   label       — the header label text ("Sessions", "Articles")
  //   className   — header class ('ui-chat-side-h' or 'ui-tw-side-h')
  //   newLabel    — text for the + New button (default: '+ New')
  //   newTitle    — tooltip for the + New button
  //   onNew       — click handler for + New
  //   onClose     — click handler for × close (mobile)
  //   leftExtras  — components inserted between label and Select/New
  //   rightExtras — components inserted between Select/New and close
  function renderSideHeader(opts) {
    var sideClose = el('button', {
      class: 'ui-chat-side-close', title: 'Close',
      onclick: opts.onClose,
    }, ['×']);
    var sideNew = el('button', {
      class: 'ui-chat-new', title: opts.newTitle || '',
      onclick: opts.onNew,
    }, [opts.newLabel || '+ New']);
    var children = [el('span', {text: opts.label})];
    (opts.leftExtras || []).forEach(function(c) { children.push(c); });
    // Optional split control: a caret beside + New opens a menu of
    // alternate new-session modes (opts.newVariants — each {label, title,
    // onSelect}). Generic — core/ui knows nothing about what a variant
    // means; the caller wires onSelect. Absent variants → plain button.
    if (opts.noNew) {
      // Fixed-set list (a fixed catalog) — no create button.
    } else if (opts.newVariants && opts.newVariants.length) {
      var nvMenu = el('div', {class: 'ui-side-menu', style: 'display:none'});
      opts.newVariants.forEach(function(v) {
        nvMenu.appendChild(el('button', {
          class: 'ui-side-menu-item', title: v.title || '',
          onclick: function() {
            nvMenu.style.display = 'none';
            if (typeof v.onSelect === 'function') v.onSelect();
          },
        }, [v.label]));
      });
      var nvCaret = el('button', {
        class: 'ui-chat-new ui-chat-new-caret', title: 'New session options',
        onclick: function(ev) {
          ev.stopPropagation();
          nvMenu.style.display = (nvMenu.style.display === 'none') ? 'block' : 'none';
        },
      }, ['▾']);
      document.addEventListener('click', function() { nvMenu.style.display = 'none'; });
      children.push(el('div', {class: 'ui-side-menu-wrap ui-chat-new-wrap'}, [sideNew, nvCaret, nvMenu]));
    } else {
      children.push(sideNew);
    }
    (opts.rightExtras || []).forEach(function(c) { children.push(c); });
    children.push(sideClose);
    return {
      elt: el('div', {class: opts.className || 'ui-chat-side-h'}, children),
      closeBtn: sideClose,
      newBtn: sideNew,
    };
  }

  // makeDrawer wires the mobile sidebar drawer used by chat, pipeline,
  // and article-editor panels. Builds the mobile-only header (hamburger
  // + title + optional + button) and the backdrop, plus open/close
  // functions that toggle the side drawer's "open" class. Each panel
  // appends mobileHdr to its main column and backdrop to its wrap.
  //
  // opts:
  //   title           — initial mobile title text
  //   hamburgerTitle  — tooltip on the ☰ button
  //   newTitle, onNew — optional "+ N" button on the mobile header
  //   newLabel        — label for the new button (default '+')
  function makeDrawer(side, opts) {
    var backdrop    = el('div', {class: 'ui-chat-backdrop'});
    var mobileHdr   = el('div', {class: 'ui-chat-mobile-hdr'});
    var mobileTitle = el('div', {class: 'ui-chat-mobile-title'}, [opts.title || '']);
    function openDrawer()  { side.classList.add('open');    backdrop.classList.add('show'); }
    function closeDrawer() { side.classList.remove('open'); backdrop.classList.remove('show'); }
    backdrop.onclick = closeDrawer;
    mobileHdr.appendChild(el('button', {
      class: 'ui-chat-hamburger', title: opts.hamburgerTitle || 'Menu',
      onclick: openDrawer,
    }, ['☰']));
    mobileHdr.appendChild(mobileTitle);
    if (opts.onNew) {
      mobileHdr.appendChild(el('button', {
        class: 'ui-chat-hamburger', title: opts.newTitle || 'New',
        onclick: function() { opts.onNew(); closeDrawer(); },
      }, [opts.newLabel || '+']));
    }
    return {
      mobileHdr:   mobileHdr,
      backdrop:    backdrop,
      mobileTitle: mobileTitle,
      openDrawer:  openDrawer,
      closeDrawer: closeDrawer,
    };
  }

  // makeSideSearch builds the small search input rendered below the
  // sidebar header. Returns the input element. Filters elements
  // matching itemSelector (default '.ui-chat-side-item') under the
  // given list element by matching their textContent.
  function makeSideSearch(sideList, itemSelector) {
    var input = el('input', {
      type: 'search', class: 'ui-chat-side-search',
      placeholder: 'Search…',
      autocomplete: 'off', autocorrect: 'off', spellcheck: 'false',
    });
    var sel = itemSelector || '.ui-chat-side-item';
    // applyFilter re-runs the show/hide pass against the CURRENT rows. Exposed
    // on the input so a list that rebuilds itself (select-mode toggle, bulk
    // delete) can re-apply the active query — otherwise the rebuilt rows come
    // back all-visible and the filter is silently lost.
    function applyFilter() {
      var q = (input.value || '').trim().toLowerCase();
      sideList.querySelectorAll(sel).forEach(function(it) {
        if (!q) { it.style.display = ''; return; }
        var hay = (it.textContent || '').toLowerCase();
        it.style.display = hay.indexOf(q) >= 0 ? '' : 'none';
      });
    }
    input.addEventListener('input', applyFilter);
    input.applyFilter = applyFilter;
    return input;
  }

  // --- component renderers ---------------------------------------------
  var components = {};

