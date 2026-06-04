// Web assets for the Knowledge app — embedded HTML + CSS + JS for
// the two pages: list (collections grid) and detail (per-collection
// upload + documents + search).
//
// CSS/DOM IDs intentionally keep their original "docs-" prefix so
// no styles need to be renamed during the Documents → Knowledge rename.
// The user-visible labels say "collection" / "knowledge"; the internals
// stay docs-prefixed for git-history sanity.

package knowledge

const documentsListBody = `
<div class="docs-page">
  <div class="docs-hdr">
    <p class="docs-intro">Reusable document collections. Create a named bundle of documents, then attach it to whichever agents or experts need it via their Knowledge picker. The chunks merge into RAG at consult / recall time. Use this for domain-scoped reference material that more than one agent might want; for files private to a single agent, use that agent's own Knowledge button instead.</p>
    <button id="docs-new" class="ui-row-btn primary">+ New collection</button>
  </div>
  <div id="docs-status" class="docs-status"></div>
  <div id="docs-list"></div>
</div>
`

const documentsListAssets = `<style>
.docs-page { padding: 0.5rem; }
.docs-hdr { display: flex; align-items: flex-start; gap: 1rem; margin-bottom: 1rem; flex-wrap: wrap; }
.docs-intro { flex: 1; min-width: 260px; margin: 0; color: var(--text-mute); font-size: 0.88rem; line-height: 1.45; }
.docs-status { font-size: 0.8rem; color: var(--text-mute); margin-bottom: 0.6rem; min-height: 1.2em; }
.docs-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 0.7rem; }
.docs-card { background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px; padding: 0.8rem; display: flex; flex-direction: column; gap: 0.4rem; cursor: pointer; transition: border-color 0.15s; }
.docs-card:hover { border-color: var(--accent, #56d364); }
.docs-card-name { font-weight: 600; color: var(--text); font-size: 1rem; }
.docs-card-desc { color: var(--text-mute); font-size: 0.78rem; line-height: 1.4; min-height: 1.2em; }
.docs-card-meta { display: flex; gap: 0.8rem; font-size: 0.72rem; color: var(--text-mute); margin-top: auto; padding-top: 0.4rem; border-top: 1px solid var(--border); }
.docs-card-meta span { display: inline-flex; align-items: center; gap: 0.2rem; }
.docs-empty { color: var(--text-mute); font-style: italic; padding: 2rem; text-align: center; border: 1px dashed var(--border); border-radius: 8px; }
.docs-modal-form { display: flex; flex-direction: column; gap: 0.5rem; }
.docs-modal-form label { font-size: 0.82rem; color: var(--text); font-weight: 600; }
.docs-modal-form input, .docs-modal-form textarea { background: var(--bg-0); color: var(--text); border: 1px solid var(--border); border-radius: 4px; padding: 0.4rem 0.6rem; font: inherit; }
.docs-modal-form textarea { min-height: 4rem; resize: vertical; }
</style>
<script>
(function() {
  function api(path) { return '/orchestrate' + path; }
  function $(sel) { return document.querySelector(sel); }
  var list = $('#docs-list');
  var status = $('#docs-status');
  var newBtn = $('#docs-new');

  function load() {
    if (status) status.textContent = 'Loading...';
    fetch(api('/api/collections'), {credentials: 'same-origin'}).then(function(r) {
      if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
      return r.json();
    }).then(function(d) {
      status.textContent = '';
      render(d && d.collections || []);
    }).catch(function(err) {
      status.style.color = 'var(--danger,#ff7b72)';
      status.textContent = 'Failed to load: ' + (err && err.message || err);
    });
  }

  function render(cols) {
    list.innerHTML = '';
    if (cols.length === 0) {
      var empty = document.createElement('div');
      empty.className = 'docs-empty';
      empty.textContent = 'No collections yet. Click "+ New collection" to create your first.';
      list.appendChild(empty);
      return;
    }
    var grid = document.createElement('div');
    grid.className = 'docs-list';
    list.appendChild(grid);
    cols.forEach(function(c) {
      var card = document.createElement('div');
      card.className = 'docs-card';
      card.addEventListener('click', function() {
        window.location.href = '/knowledge/c/' + encodeURIComponent(c.id);
      });
      var name = document.createElement('div');
      name.className = 'docs-card-name';
      name.textContent = c.name || '(unnamed)';
      card.appendChild(name);
      var desc = document.createElement('div');
      desc.className = 'docs-card-desc';
      desc.textContent = c.description || '';
      card.appendChild(desc);
      var meta = document.createElement('div');
      meta.className = 'docs-card-meta';
      meta.appendChild(spanIcon((c.documents || 0) + ' doc' + (c.documents === 1 ? '' : 's')));
      meta.appendChild(spanIcon((c.chunks || 0) + ' chunk' + (c.chunks === 1 ? '' : 's')));
      card.appendChild(meta);
      grid.appendChild(card);
    });
  }

  function spanIcon(text) {
    var s = document.createElement('span');
    s.textContent = text;
    return s;
  }

  newBtn.addEventListener('click', function() {
    // Div-overlay modal instead of a native <dialog>/showModal(): WKWebView
    // (gohort-desktop) renders dynamically-built <dialog> content
    // unreliably (the body doesn't expand), which is why the framework's
    // own modals are div overlays too. overlay = full-screen scrim; dlg =
    // the card.
    var overlay = document.createElement('div');
    overlay.style.cssText = 'position:fixed;top:0;left:0;right:0;bottom:0;z-index:2147483646;background:rgba(0,0,0,0.55);display:flex;align-items:center;justify-content:center';
    var dlg = document.createElement('div');
    dlg.style.cssText = 'background:var(--bg-1);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:1rem;max-width:560px;width:92%;max-height:88vh;overflow-y:auto;box-shadow:0 12px 40px rgba(0,0,0,0.6)';
    overlay.appendChild(dlg);
    var h = document.createElement('h3');
    h.textContent = 'New collection';
    h.style.cssText = 'margin:0 0 0.6rem';
    dlg.appendChild(h);
    var bodyWrap = document.createElement('div');
    bodyWrap.style.cssText = 'padding-right:0.3rem';
    dlg.appendChild(bodyWrap);
    var form = document.createElement('div');
    form.className = 'docs-modal-form';
    bodyWrap.appendChild(form);

    var lblN = document.createElement('label'); lblN.textContent = 'Name'; form.appendChild(lblN);
    var inpN = document.createElement('input'); inpN.type = 'text'; inpN.placeholder = 'e.g. Kubernetes Reference'; form.appendChild(inpN);

    var lblD = document.createElement('label'); lblD.textContent = 'Description'; form.appendChild(lblD);
    var inpD = document.createElement('textarea');
    inpD.placeholder = 'What documents should this collection contain? (Drives Auto-fill search queries.)';
    form.appendChild(inpD);
    var draftRow = document.createElement('div');
    draftRow.style.cssText = 'display:flex;align-items:center;gap:0.5rem;margin-top:0.3rem';
    var draftBtn = document.createElement('button');
    draftBtn.type = 'button'; draftBtn.className = 'ui-row-btn';
    draftBtn.textContent = 'Draft with AI';
    var draftStatus = document.createElement('span');
    draftStatus.style.cssText = 'font-size:0.72rem;color:var(--text-mute)';
    draftRow.appendChild(draftBtn); draftRow.appendChild(draftStatus);
    form.appendChild(draftRow);

    draftBtn.addEventListener('click', function() {
      var name = inpN.value.trim();
      if (!name) { window.uiAlert('Enter a name first — that\'s what the AI uses to draft.'); return; }
      draftBtn.disabled = true;
      draftStatus.style.color = 'var(--text-mute)';
      draftStatus.textContent = 'Drafting…';
      fetch(api('/api/collections/draft-description'), {
        method: 'POST', credentials: 'same-origin',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({name: name, description: inpD.value.trim()}),
      }).then(function(r){
        if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
        return r.json();
      }).then(function(out) {
        inpD.value = (out && out.description) || '';
        draftStatus.style.color = 'var(--text-mute)';
        draftStatus.textContent = 'Edit if needed, then Create.';
        draftBtn.disabled = false;
      }).catch(function(err) {
        draftStatus.style.color = 'var(--danger,#ff7b72)';
        draftStatus.textContent = 'Draft failed: ' + (err && err.message || err);
        draftBtn.disabled = false;
      });
    });

    var actions = document.createElement('div');
    actions.style.cssText = 'display:flex;gap:0.5rem;justify-content:flex-end;margin-top:0.8rem;padding-top:0.6rem;border-top:1px solid var(--border)';
    var cancel = document.createElement('button'); cancel.className = 'ui-row-btn'; cancel.textContent = 'Cancel';
    cancel.addEventListener('click', function(){ overlay.remove(); });
    var create = document.createElement('button'); create.className = 'ui-row-btn primary'; create.textContent = 'Create';
    create.addEventListener('click', function() {
      var name = inpN.value.trim();
      if (!name) { window.uiAlert('Name required'); return; }
      create.disabled = true;
      fetch(api('/api/collections'), {
        method: 'POST', credentials: 'same-origin',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
          name: name,
          description: inpD.value.trim(),
        }),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
        return r.json();
      }).then(function(c) {
        overlay.remove();
        window.location.href = '/knowledge/c/' + encodeURIComponent(c.id);
      }).catch(function(err) {
        create.disabled = false;
        window.uiAlert('Create failed: ' + (err && err.message || err));
      });
    });
    actions.appendChild(cancel); actions.appendChild(create);
    dlg.appendChild(actions);
    // Click the scrim (outside the card) to dismiss.
    overlay.addEventListener('click', function(e){ if (e.target === overlay) overlay.remove(); });
    document.body.appendChild(overlay);
    setTimeout(function(){ inpN.focus(); }, 0);
  });

  load();
})();
</script>`

const documentsDetailBody = `
<div class="docs-detail">
  <div class="docs-detail-hdr">
    <div class="docs-detail-name"><span id="docs-name">Loading...</span><button id="docs-rename" class="ui-row-btn">Rename</button><button id="docs-delete" class="ui-row-btn" style="color:var(--danger,#ff7b72)">Delete</button></div>
    <div class="docs-desc-wrap">
      <textarea id="docs-desc" class="docs-detail-desc-edit" placeholder="Describe what this collection should contain. The description steers Auto-fill's search queries — be specific (e.g. &quot;Official Kubernetes API reference, operator best practices, and our cluster runbook&quot;)."></textarea>
      <div class="docs-desc-actions">
        <button id="docs-desc-save" class="ui-row-btn" disabled>Save</button>
        <button id="docs-desc-suggest" class="ui-row-btn">Suggest with AI</button>
        <span id="docs-desc-status"></span>
      </div>
    </div>
    <div id="docs-meta" class="docs-detail-meta"></div>
  </div>

  <div class="docs-section">
    <div class="docs-section-title">Upload</div>
    <div class="docs-upload-row">
      <input id="docs-file" type="file" accept=".pdf,.docx,.txt,.md,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document,text/plain,text/markdown" />
      <button id="docs-upload" class="ui-row-btn primary" disabled>Upload</button>
      <span id="docs-upload-status"></span>
    </div>
  </div>

  <div class="docs-section">
    <div class="docs-section-title">Auto-fill from web</div>
    <div class="docs-section-help">Seeds this collection from the web using the name + description above. Skips URLs already pulled. The settings below shape which candidates get in.</div>
    <label style="display:block;font-size:0.82rem;font-weight:600;color:var(--text);margin-top:0.6rem">Filter rules / hints (optional)</label>
    <div class="docs-section-help" style="margin-top:0.15rem;font-size:0.74rem">Rules bias the generated search queries (e.g. "2026 edition", "official sources only") and, when the LLM judge is on, also drop candidates that don't match.</div>
    <div id="docs-filter-rules-list" style="display:flex;flex-direction:column;gap:0.3rem;margin:0.4rem 0"></div>
    <div style="display:flex;align-items:center;gap:0.4rem">
      <input id="docs-filter-rules-input" type="text" placeholder="Add a rule (e.g. &quot;Prefer 2026 edition&quot; or &quot;Skip blog posts&quot;)"
        style="flex:1;background:var(--bg-0);color:var(--text);border:1px solid var(--border);border-radius:4px;padding:0.35rem 0.55rem;font:inherit;font-size:0.85rem">
      <button id="docs-filter-rules-add" class="ui-row-btn" disabled>+ Add</button>
      <span id="docs-filter-rules-status" style="font-size:0.72rem;color:var(--text-mute)"></span>
    </div>
    <label style="display:flex;align-items:center;gap:0.5rem;font-size:0.85rem;color:var(--text);margin:0.6rem 0 0">
      <input id="docs-classify-toggle" type="checkbox">
      <span>Also run an LLM judge on each candidate (extra ~1 LLM call per doc)</span>
      <span id="docs-classify-status" style="font-size:0.72rem;color:var(--text-mute);margin-left:0.3rem"></span>
    </label>
    <div class="docs-upload-row" style="margin-top:0.8rem;align-items:center;gap:0.5rem;padding-top:0.6rem;border-top:1px solid var(--border)">
      <button id="docs-autofill" class="ui-row-btn primary">Auto-fill from web</button>
      <label style="display:flex;align-items:center;gap:0.35rem;font-size:0.82rem;color:var(--text-mute)">
        <span>up to</span>
        <input id="docs-autofill-max" type="number" min="1" max="50" value="10"
          style="width:4rem;background:var(--bg-0);color:var(--text);border:1px solid var(--border);border-radius:4px;padding:0.25rem 0.4rem;font:inherit;font-size:0.85rem">
        <span>documents</span>
      </label>
      <span id="docs-autofill-status"></span>
    </div>
  </div>

  <div class="docs-section">
    <div class="docs-section-title">Documents</div>
    <div id="docs-bulk-bar" style="display:none;align-items:center;gap:0.6rem;padding:0.4rem 0.6rem;background:var(--bg-2);border:1px solid var(--border);border-radius:4px;margin-bottom:0.4rem">
      <span id="docs-bulk-count" style="font-size:0.82rem;color:var(--text);font-weight:600"></span>
      <button id="docs-bulk-delete" class="ui-row-btn" style="color:var(--danger,#ff7b72);font-size:0.8rem;padding:0.25rem 0.6rem">Remove selected</button>
      <span id="docs-bulk-status" style="font-size:0.74rem;color:var(--text-mute);margin-left:auto"></span>
    </div>
    <div id="docs-sources"></div>
  </div>

  <div class="docs-section">
    <div class="docs-section-title">Search</div>
    <div class="docs-search-row">
      <input id="docs-q" type="text" placeholder="Search this collection for..." />
      <button id="docs-go" class="ui-row-btn">Search</button>
    </div>
    <div id="docs-hits"></div>
  </div>
</div>
`

const documentsDetailAssets = `<style>
.docs-detail { padding: 0.5rem; display: flex; flex-direction: column; gap: 1rem; }
.docs-detail-hdr { padding-bottom: 0.8rem; border-bottom: 1px solid var(--border); }
.docs-detail-name { display: flex; align-items: center; gap: 0.5rem; flex-wrap: wrap; margin-bottom: 0.3rem; }
.docs-detail-name span { font-size: 1.3rem; font-weight: 600; color: var(--text); flex: 1; min-width: 200px; }
.docs-detail-desc { color: var(--text-mute); font-size: 0.88rem; line-height: 1.45; margin-bottom: 0.5rem; }
.docs-desc-wrap { margin-bottom: 0.5rem; }
.docs-detail-desc-edit { width: 100%; min-height: 4.5rem; resize: vertical; background: var(--bg-0); color: var(--text); border: 1px solid var(--border); border-radius: 4px; padding: 0.5rem 0.6rem; font: inherit; font-size: 0.85rem; line-height: 1.45; }
.docs-detail-desc-edit:focus { border-color: var(--accent, #56d364); outline: none; }
.docs-desc-actions { display: flex; align-items: center; gap: 0.5rem; margin-top: 0.4rem; }
.docs-desc-actions span { font-size: 0.74rem; color: var(--text-mute); }
.docs-detail-meta { font-size: 0.74rem; color: var(--text-mute); display: flex; gap: 1rem; }
.docs-section { background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px; padding: 0.8rem; }
.docs-section-title { font-weight: 600; color: var(--text); margin-bottom: 0.4rem; }
.docs-section-help { font-size: 0.74rem; color: var(--text-mute); margin-bottom: 0.5rem; }
.docs-upload-row, .docs-search-row { display: flex; gap: 0.5rem; align-items: center; flex-wrap: wrap; }
.docs-upload-row input[type=file] { flex: 1; min-width: 0; font-size: 0.85rem; }
.docs-upload-row span { font-size: 0.74rem; color: var(--text-mute); }
.docs-search-row input { flex: 1; min-width: 0; background: var(--bg-0); color: var(--text); border: 1px solid var(--border); border-radius: 4px; padding: 0.4rem 0.6rem; font: inherit; }
#docs-sources, #docs-hits, #docs-agents { display: flex; flex-direction: column; gap: 0.3rem; margin-top: 0.4rem; }
.docs-row { display: flex; align-items: center; gap: 0.5rem; padding: 0.4rem 0.6rem; background: var(--bg-0); border: 1px solid var(--border); border-radius: 4px; font-size: 0.84rem; }
.docs-row-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.docs-row-meta { color: var(--text-mute); font-size: 0.72rem; }
.docs-hit { padding: 0.6rem; background: var(--bg-0); border: 1px solid var(--border); border-radius: 4px; }
.docs-hit-section { font-weight: 600; color: var(--text); margin-bottom: 0.3rem; font-size: 0.85rem; }
.docs-hit-text { color: var(--text-mute); font-size: 0.82rem; line-height: 1.45; white-space: pre-wrap; }
.docs-empty { color: var(--text-mute); font-style: italic; padding: 0.4rem 0; font-size: 0.84rem; }
.docs-agent-row { display: flex; align-items: center; gap: 0.6rem; padding: 0.3rem 0.5rem; }
.docs-agent-row label { flex: 1; cursor: pointer; }
</style>
<script>
(function() {
  function api(path) { return '/orchestrate' + path; }
  function $(sel) { return document.querySelector(sel); }
  function parseID() {
    var path = window.location.pathname;
    // Accept both /knowledge/c/<id> (current) and /documents/c/<id>
    // (legacy back-compat — the redirect normally handles this but
    // accept it directly too in case the redirect is bypassed).
    var m = path.match(/\/(?:knowledge|documents)\/c\/([^/]+)/);
    return m ? decodeURIComponent(m[1]) : '';
  }
  var cid = parseID();
  if (!cid) {
    document.querySelector('.docs-detail').innerHTML = '<div class="docs-empty">Collection ID missing from URL.</div>';
    return;
  }

  var loadedDescription = '';
  var loadedFilterRules = '';
  function loadDetail() {
    fetch(api('/api/collections/' + encodeURIComponent(cid)), {credentials: 'same-origin'})
      .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t); }); return r.json(); })
      .then(function(c) {
        document.title = (c.name || 'Collection') + ' — Documents';
        $('#docs-name').textContent = c.name || '(unnamed)';
        loadedDescription = c.description || '';
        // Don't blow away an in-progress edit — only refill the textarea
        // when its current value matches the previously-loaded value.
        var ta = $('#docs-desc');
        if (ta && (ta.value === '' || ta.value === ta.dataset.lastLoaded)) {
          ta.value = loadedDescription;
        }
        if (ta) {
          ta.dataset.lastLoaded = loadedDescription;
          $('#docs-desc-save').disabled = (ta.value === loadedDescription);
        }
        $('#docs-meta').textContent = (c.documents || 0) + ' documents - ' + (c.chunks || 0) + ' chunks';
        // Filter rules render as a discrete add/remove list.
        // Stored newline-joined server-side; split on load.
        loadedFilterRules = c.filter_rules || '';
        renderFilterRules(loadedFilterRules);
        var cb = $('#docs-classify-toggle');
        if (cb) cb.checked = !!c.classify_on_autofill;
      }).catch(function(err) {
        $('#docs-name').textContent = '(failed to load)';
        var ta = $('#docs-desc'); if (ta) ta.value = err && err.message || err;
      });
  }

  // Filter rules as add/remove list. Stored as a newline-joined
  // string server-side; the list is just the UI shape on top.
  // Each add or remove fires an immediate PATCH so there's no
  // "save" step to forget.
  function parseRules(s) {
    return (s || '').split('\n').map(function(x){ return x.replace(/^[-•*]\s*/, '').trim(); }).filter(function(x){ return x.length > 0; });
  }
  function joinRules(arr) { return (arr || []).join('\n'); }
  function renderFilterRules(raw) {
    var host = $('#docs-filter-rules-list');
    if (!host) return;
    host.innerHTML = '';
    var items = parseRules(raw);
    if (items.length === 0) {
      var emp = document.createElement('div');
      emp.style.cssText = 'font-size:0.78rem;color:var(--text-mute);font-style:italic';
      emp.textContent = '(no rules yet — judge will decide purely from the collection description)';
      host.appendChild(emp);
      return;
    }
    items.forEach(function(text, idx) {
      var row = document.createElement('div');
      row.style.cssText = 'display:flex;align-items:center;gap:0.4rem;padding:0.3rem 0.55rem;background:var(--bg-0);border:1px solid var(--border);border-radius:4px;font-size:0.82rem';
      var bullet = document.createElement('span');
      bullet.textContent = '•';
      bullet.style.cssText = 'color:var(--text-mute);min-width:0.7rem';
      var label = document.createElement('span');
      label.style.cssText = 'flex:1;min-width:0;word-break:break-word';
      label.textContent = text;
      var del = document.createElement('button');
      del.type = 'button';
      del.className = 'ui-row-btn';
      del.style.cssText = 'color:var(--danger,#ff7b72);font-size:0.72rem;padding:0.15rem 0.45rem';
      del.textContent = '✕';
      del.title = 'Remove this rule';
      del.addEventListener('click', function() {
        var next = items.slice();
        next.splice(idx, 1);
        saveFilterRules(joinRules(next));
      });
      row.appendChild(bullet); row.appendChild(label); row.appendChild(del);
      host.appendChild(row);
    });
  }
  function saveFilterRules(newRaw) {
    var st = $('#docs-filter-rules-status');
    st.style.color = 'var(--text-mute)';
    st.textContent = 'Saving…';
    fetch(api('/api/collections/' + encodeURIComponent(cid)), {
      method: 'PATCH', credentials: 'same-origin',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({filter_rules: newRaw}),
    }).then(function(r){
      if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
      loadedFilterRules = newRaw;
      renderFilterRules(newRaw);
      st.style.color = 'var(--accent,#56d364)';
      st.textContent = 'Saved';
      setTimeout(function(){ if (st.textContent === 'Saved') st.textContent = ''; }, 1800);
    }).catch(function(err){
      st.style.color = 'var(--danger,#ff7b72)';
      st.textContent = 'Save failed: ' + (err && err.message || err);
    });
  }
  // Add-rule wiring: enable button only when input has content.
  (function(){
    var inp = $('#docs-filter-rules-input');
    var btn = $('#docs-filter-rules-add');
    if (!inp || !btn) return;
    function refreshBtn(){ btn.disabled = inp.value.trim().length === 0; }
    inp.addEventListener('input', refreshBtn);
    inp.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !btn.disabled) { ev.preventDefault(); btn.click(); }
    });
    btn.addEventListener('click', function() {
      var v = inp.value.trim();
      if (!v) return;
      var current = parseRules(loadedFilterRules);
      current.push(v);
      saveFilterRules(joinRules(current));
      inp.value = '';
      refreshBtn();
      inp.focus();
    });
  })();

  // Classify-on-autofill toggle: saves immediately on change.
  $('#docs-classify-toggle') && $('#docs-classify-toggle').addEventListener('change', function() {
    var cb = $('#docs-classify-toggle');
    var st = $('#docs-classify-status');
    var v = !!cb.checked;
    cb.disabled = true;
    st.style.color = 'var(--text-mute)';
    st.textContent = 'Saving…';
    fetch(api('/api/collections/' + encodeURIComponent(cid)), {
      method: 'PATCH', credentials: 'same-origin',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({classify_on_autofill: v}),
    }).then(function(r){
      if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
      st.style.color = 'var(--accent,#56d364)';
      st.textContent = v ? 'Judge ON' : 'Judge OFF';
      cb.disabled = false;
      setTimeout(function(){ st.textContent = ''; }, 2500);
    }).catch(function(err){
      st.style.color = 'var(--danger,#ff7b72)';
      st.textContent = 'Save failed: ' + (err && err.message || err);
      cb.checked = !v;
      cb.disabled = false;
    });
  });

  // Dirty-tracking on the description textarea so Save only activates
  // when there's a real change.
  (function(){
    var ta = $('#docs-desc');
    if (!ta) return;
    ta.addEventListener('input', function(){
      $('#docs-desc-save').disabled = (ta.value === loadedDescription);
    });
  })();

  $('#docs-desc-save').addEventListener('click', function() {
    var ta = $('#docs-desc');
    var newDesc = (ta.value || '').trim();
    var btn = $('#docs-desc-save');
    var st = $('#docs-desc-status');
    btn.disabled = true;
    st.style.color = 'var(--text-mute)';
    st.textContent = 'Saving...';
    fetch(api('/api/collections/' + encodeURIComponent(cid)), {
      method: 'PATCH', credentials: 'same-origin',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({description: newDesc}),
    }).then(function(r){
      if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
      st.style.color = 'var(--accent,#56d364)';
      st.textContent = 'Saved';
      loadedDescription = newDesc;
      ta.dataset.lastLoaded = newDesc;
      setTimeout(function(){ if (st.textContent === 'Saved') st.textContent = ''; }, 2500);
    }).catch(function(err){
      st.style.color = 'var(--danger,#ff7b72)';
      st.textContent = 'Save failed: ' + (err && err.message || err);
      btn.disabled = false;
    });
  });

  // Suggest-with-AI: single click → vanilla worker LLM drafts a
  // description from the collection's name + existing docs.
  // Previously a modal let the user pick a skill voice; that
  // surface went with the skill/corpus split.
  $('#docs-desc-suggest').addEventListener('click', function() {
    var btn = $('#docs-desc-suggest');
    var st = $('#docs-desc-status');
    btn.disabled = true;
    st.style.color = 'var(--text-mute)';
    st.textContent = 'Drafting suggestion...';
    fetch(api('/api/collections/' + encodeURIComponent(cid) + '/suggest-description'), {
      method: 'POST', credentials: 'same-origin',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({mode: 'none'}),
    }).then(function(r){
      if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
      return r.json();
    }).then(function(out) {
      var ta = $('#docs-desc');
      ta.value = (out && out.description) || '';
      $('#docs-desc-save').disabled = (ta.value === loadedDescription);
      st.style.color = 'var(--text-mute)';
      st.textContent = 'Suggestion ready — edit and Save when you\'re happy.';
      btn.disabled = false;
    }).catch(function(err){
      st.style.color = 'var(--danger,#ff7b72)';
      st.textContent = 'Suggest failed: ' + (err && err.message || err);
      btn.disabled = false;
    });
  });

  // Selection state for the bulk-action toolbar. Cleared on
  // reload so newly-loaded sources start unselected.
  var selectedSources = {};
  function updateBulkBar() {
    var bar = $('#docs-bulk-bar');
    var count = Object.keys(selectedSources).length;
    if (!bar) return;
    if (count === 0) {
      bar.style.display = 'none';
      return;
    }
    bar.style.display = '';
    var label = $('#docs-bulk-count');
    if (label) label.textContent = count + ' selected';
  }
  function loadSources() {
    fetch(api('/api/collections/' + encodeURIComponent(cid) + '/sources'), {credentials: 'same-origin'})
      .then(function(r){ return r.ok ? r.json() : null; })
      .then(function(d) {
        var box = $('#docs-sources');
        box.innerHTML = '';
        selectedSources = {};
        updateBulkBar();
        var sources = (d && d.sources) || [];
        if (sources.length === 0) {
          var emp = document.createElement('div'); emp.className = 'docs-empty';
          emp.textContent = '(no documents yet — upload one above)';
          box.appendChild(emp);
          return;
        }
        // Select-all header — checkbox toggles every row's
        // checked state. Reflects partial selection by going
        // unchecked when any row is unchecked, fully checked
        // only when all rows are in selectedSources.
        var header = document.createElement('div');
        header.style.cssText = 'display:flex;align-items:center;gap:0.5rem;padding:0.2rem 0.55rem;font-size:0.78rem;color:var(--text-mute)';
        var allCb = document.createElement('input');
        allCb.type = 'checkbox';
        allCb.title = 'Select all';
        var allLbl = document.createElement('span');
        allLbl.textContent = 'Select all';
        header.appendChild(allCb); header.appendChild(allLbl);
        box.appendChild(header);
        allCb.addEventListener('change', function() {
          var rows = box.querySelectorAll('.docs-row-select');
          rows.forEach(function(cb){
            if (cb.checked !== allCb.checked) {
              cb.checked = allCb.checked;
              cb.dispatchEvent(new Event('change'));
            }
          });
        });

        sources.forEach(function(s) {
          var row = document.createElement('div'); row.className = 'docs-row';
          var sel = document.createElement('input');
          sel.type = 'checkbox'; sel.className = 'docs-row-select';
          sel.style.marginRight = '0.4rem';
          sel.addEventListener('change', function() {
            if (sel.checked) selectedSources[s.id] = s.name || s.id;
            else delete selectedSources[s.id];
            // Sync the select-all checkbox state.
            var rows = box.querySelectorAll('.docs-row-select');
            var anyUnchecked = false;
            rows.forEach(function(cb){ if (!cb.checked) anyUnchecked = true; });
            allCb.checked = !anyUnchecked;
            updateBulkBar();
          });
          var nm = document.createElement('span'); nm.className = 'docs-row-name';
          nm.textContent = (s.name || s.id || '(unnamed)').replace(/^#+\s*/, '');
          nm.title = nm.textContent;
          var meta = document.createElement('span'); meta.className = 'docs-row-meta';
          meta.textContent = (s.chunks || 0) + ' chunk' + (s.chunks === 1 ? '' : 's');
          var del = document.createElement('button'); del.className = 'ui-row-btn';
          del.style.cssText = 'color:var(--danger,#ff7b72);font-size:0.78rem;padding:0.2rem 0.5rem';
          del.textContent = 'Remove';
          del.onclick = async function() {
            if (!(await window.uiConfirm('Remove ' + nm.textContent + ' from this collection?'))) return;
            // Optimistic UI — hide the row immediately so the
            // click feels instant. Restore if the DELETE fails.
            var displayWas = row.style.display;
            row.style.display = 'none';
            delete selectedSources[s.id];
            updateBulkBar();
            fetch(api('/api/collections/' + encodeURIComponent(cid) + '/sources/' + encodeURIComponent(s.id)),
                  {method: 'DELETE', credentials: 'same-origin'})
              .then(function(r){
                if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
                // Refresh in the background to pick up chunk-count
                // changes on the metadata line — the row is already
                // gone, so this is just for the counts header.
                loadDetail();
              })
              .catch(function(err){
                row.style.display = displayWas;
                window.uiAlert('Remove failed: ' + (err && err.message || err));
              });
          };
          row.appendChild(sel); row.appendChild(nm); row.appendChild(meta); row.appendChild(del);
          box.appendChild(row);
        });
      });
  }

  // Bulk-delete handler: optimistically hide every selected row
  // immediately, then fire DELETEs in parallel (one per source).
  // Failed rows get restored + reported in the status line.
  // Single confirm covers the whole batch.
  document.addEventListener('click', async function(ev) {
    if (!ev.target || ev.target.id !== 'docs-bulk-delete') return;
    var ids = Object.keys(selectedSources);
    if (ids.length === 0) return;
    if (!(await window.uiConfirm('Remove ' + ids.length + ' document' + (ids.length === 1 ? '' : 's') + ' from this collection? This deletes their chunks permanently.'))) return;
    var btn = ev.target;
    var st = $('#docs-bulk-status');
    btn.disabled = true;
    st.style.color = 'var(--text-mute)';
    st.textContent = 'Removing ' + ids.length + '…';
    // Snapshot rows by ID and hide them up front for instant feedback.
    var rowByID = {};
    var box = $('#docs-sources');
    if (box) {
      box.querySelectorAll('.docs-row').forEach(function(row) {
        var sel = row.querySelector('.docs-row-select');
        // No id attribute on the row, but the checkbox owns the
        // truth: find which selected id this row belongs to by
        // matching the row's checkbox checked state at delete
        // time. Walk + map.
      });
    }
    // Easier path: iterate selectedSources, find each row by
    // checkbox-checked state, hide it.
    var rows = box ? box.querySelectorAll('.docs-row') : [];
    rows.forEach(function(row) {
      var sel = row.querySelector('.docs-row-select');
      if (sel && sel.checked) {
        // Find which id this checkbox represents by matching
        // selectedSources entries; safer than threading IDs
        // through the loadSources closure.
        for (var id in selectedSources) {
          // Pair the row's name span text with the stored label
          // for that id.
          var nm = row.querySelector('.docs-row-name');
          if (nm && nm.textContent === (selectedSources[id] || '').replace(/^#+\s*/, '')) {
            rowByID[id] = row;
            row.style.display = 'none';
            break;
          }
        }
      }
    });
    // Clear selection state immediately — the user committed.
    var capturedIds = ids.slice();
    var capturedLabels = {};
    capturedIds.forEach(function(id){ capturedLabels[id] = selectedSources[id]; });
    selectedSources = {};
    updateBulkBar();

    var done = 0, failed = 0;
    var failures = [];
    var pending = capturedIds.length;
    capturedIds.forEach(function(id) {
      fetch(api('/api/collections/' + encodeURIComponent(cid) + '/sources/' + encodeURIComponent(id)),
            {method: 'DELETE', credentials: 'same-origin'})
        .then(function(r){
          if (!r.ok) {
            failed++;
            failures.push(capturedLabels[id] || id);
            // Restore the row visually so the user sees what
            // didn't actually delete.
            if (rowByID[id]) rowByID[id].style.display = '';
          } else {
            done++;
          }
        })
        .catch(function(){
          failed++;
          failures.push(capturedLabels[id] || id);
          if (rowByID[id]) rowByID[id].style.display = '';
        })
        .finally(function(){
          pending--;
          if (pending === 0) {
            btn.disabled = false;
            st.style.color = failed === 0 ? 'var(--accent,#56d364)' : 'var(--danger,#ff7b72)';
            st.textContent = 'Removed ' + done + (failed > 0 ? ', failed ' + failed + ': ' + failures.join(', ') : '');
            loadDetail(); // refresh chunk count
            if (failed === 0) {
              setTimeout(function(){ if (st.textContent.indexOf('Removed') === 0) st.textContent = ''; }, 2500);
            }
          }
        });
    });
  });

  $('#docs-file').addEventListener('change', function() {
    $('#docs-upload').disabled = !($('#docs-file').files && $('#docs-file').files[0]);
  });
  $('#docs-upload').addEventListener('click', function() {
    var f = $('#docs-file').files && $('#docs-file').files[0];
    if (!f) return;
    $('#docs-upload').disabled = true;
    var st = $('#docs-upload-status');
    st.style.color = 'var(--text-mute)';
    st.textContent = 'Extracting + indexing...';
    var reader = new FileReader();
    reader.onload = function() {
      var s = reader.result || '';
      var comma = s.indexOf(',');
      var b64 = comma >= 0 ? s.substring(comma + 1) : s;
      fetch(api('/api/collections/' + encodeURIComponent(cid) + '/upload'), {
        method: 'POST', credentials: 'same-origin',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({name: f.name, mime_type: f.type || '', data: b64}),
      }).then(function(r){
        if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
        return r.json();
      }).then(function(out) {
        st.style.color = 'var(--accent,#56d364)';
        st.textContent = 'Added ' + (out.chunks || 0) + ' chunks';
        $('#docs-file').value = '';
        loadSources(); loadDetail();
      }).catch(function(err){
        st.style.color = 'var(--danger,#ff7b72)';
        st.textContent = 'Upload failed: ' + (err && err.message || err);
        $('#docs-upload').disabled = false;
      });
    };
    reader.readAsDataURL(f);
  });

  $('#docs-autofill').addEventListener('click', async function() {
    var maxInput = $('#docs-autofill-max');
    var maxDocs = parseInt(maxInput && maxInput.value, 10);
    if (isNaN(maxDocs) || maxDocs < 1) maxDocs = 10;
    if (maxDocs > 50) maxDocs = 50;
    if (!(await window.uiConfirm('Auto-fill this collection from the web?\n\nThe framework will generate search queries from the name + description, fetch up to ' + maxDocs + ' document' + (maxDocs === 1 ? '' : 's') + ', extract text, and ingest into this collection.\n\nLarger batches take longer (~30 sec per 10 docs). URLs already pulled previously will be skipped.'))) {
      return;
    }
    var btn = $('#docs-autofill');
    var st = $('#docs-autofill-status');
    var origLabel = btn.textContent;
    btn.disabled = true;
    // Braille-dot spinner — same pattern as the agent backfill button.
    // Visible cue that the multi-minute autofill is in flight, in
    // addition to the status-text update next to the button.
    var spinFrames = ['⠋','⠙','⠹','⠸','⠼','⠴','⠦','⠧','⠇','⠏'];
    var spinIdx = 0;
    var spinTimer = setInterval(function(){
      btn.textContent = spinFrames[spinIdx] + ' Auto-filling…';
      spinIdx = (spinIdx + 1) % spinFrames.length;
    }, 100);
    function stopSpinner(label) {
      clearInterval(spinTimer);
      btn.textContent = label;
    }
    st.style.color = 'var(--text-mute)';
    st.textContent = 'Searching + fetching...';
    fetch(api('/api/collections/' + encodeURIComponent(cid) + '/autofill'), {
      method: 'POST',
      credentials: 'same-origin',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({max_docs: maxDocs}),
    }).then(function(r){
      if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
      return r.json();
    }).then(function(out) {
      st.style.color = 'var(--accent,#56d364)';
      var parts = ['Added ' + (out.added || 0)];
      if (out.failed) parts.push((out.failed || 0) + ' failed');
      st.textContent = parts.join(' - ');
      loadSources(); loadDetail();
      stopSpinner(origLabel);
      btn.disabled = false;
    }).catch(function(err) {
      st.style.color = 'var(--danger,#ff7b72)';
      st.textContent = 'Failed: ' + (err && err.message || err);
      stopSpinner(origLabel);
      btn.disabled = false;
    });
  });

  function runSearch() {
    var q = $('#docs-q').value.trim();
    if (!q) return;
    var box = $('#docs-hits');
    box.innerHTML = '<div class="docs-empty">Searching...</div>';
    fetch(api('/api/collections/' + encodeURIComponent(cid) + '/search?q=' + encodeURIComponent(q)), {credentials: 'same-origin'})
      .then(function(r){ return r.ok ? r.json() : {hits: []}; })
      .then(function(d) {
        box.innerHTML = '';
        var hits = (d && d.hits) || [];
        if (hits.length === 0) {
          var emp = document.createElement('div'); emp.className = 'docs-empty';
          emp.textContent = '(no matches)';
          box.appendChild(emp);
          return;
        }
        hits.forEach(function(h) {
          var card = document.createElement('div'); card.className = 'docs-hit';
          var sec = document.createElement('div'); sec.className = 'docs-hit-section';
          sec.textContent = (h.section || '').replace(/^#+\s*/, '');
          card.appendChild(sec);
          var txt = document.createElement('div'); txt.className = 'docs-hit-text';
          txt.textContent = h.text || '';
          card.appendChild(txt);
          box.appendChild(card);
        });
      });
  }
  $('#docs-go').addEventListener('click', runSearch);
  $('#docs-q').addEventListener('keydown', function(ev) { if (ev.key === 'Enter') runSearch(); });

  $('#docs-rename').addEventListener('click', async function() {
    var newName = await window.uiPrompt('Rename collection to:', $('#docs-name').textContent || '');
    if (!newName || !newName.trim()) return;
    fetch(api('/api/collections/' + encodeURIComponent(cid)), {
      method: 'PATCH', credentials: 'same-origin',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({name: newName.trim()}),
    }).then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t); }); loadDetail(); })
      .catch(function(err){ window.uiAlert('Rename failed: ' + (err && err.message || err)); });
  });

  $('#docs-delete').addEventListener('click', async function() {
    if (!(await window.uiConfirm('Delete this collection? All documents in it will be removed and detached from any agents using it.'))) return;
    // Fire-and-forget the DELETE so navigation feels instant —
    // wiping a many-chunk collection can take several seconds and
    // the detail page is already useless. The list page re-fetches
    // on load, so a ghost row only shows briefly in the rare case
    // the metadata delete loses a race with the list fetch.
    fetch(api('/api/collections/' + encodeURIComponent(cid)),
          {method: 'DELETE', credentials: 'same-origin'})
      .catch(function(err){ console.warn('collection delete failed in background:', err); });
    window.location.href = '/knowledge/';
  });

  loadDetail();
  loadSources();
})();
</script>`
