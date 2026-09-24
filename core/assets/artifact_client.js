// Shared browser half of the artifact bundle format: download an export, and
// the preview-then-confirm import flow. One copy for every surface that
// exports or imports bundles (Admin, Account, ...); each passes its own
// endpoints, so the admin's deployment-wide importer and a user's own importer
// run the same flow against different doors.
//
// Lives in core (with the bundle format), NOT core/ui: it knows the bundle
// endpoints' request and response shapes. Served through ArtifactClientJS.
(function () {
  if (window.gohortArtifacts) return;

  function fail(prefix, e) {
    (window.uiAlert || window.alert)(prefix + (e && e.message || e));
  }

  function postJSON(url, payload) {
    return fetch(url, {
      method: 'POST', credentials: 'same-origin',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
    }).then(function (r) {
      return r.ok ? r.json() : r.text().then(function (t) { throw new Error(t || ('HTTP ' + r.status)); });
    });
  }

  // download navigates a hidden anchor at an export URL, so the browser saves
  // the bundle the server streams back.
  function download(href, filename) {
    var a = document.createElement('a');
    a.href = href;
    if (filename) a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
  }

  function row(it) {
    var r = document.createElement('div');
    r.style.cssText = 'display:flex;gap:0.6rem;align-items:baseline;font-size:0.85rem;border-bottom:1px solid var(--border);padding-bottom:0.35rem';
    var imp = it.action === 'import';
    var badge = document.createElement('span');
    badge.textContent = imp ? 'import' : 'skip';
    badge.style.cssText = 'flex:0 0 3.6rem;text-align:center;font-size:0.72rem;border-radius:4px;padding:0.1rem 0.3rem;' +
      (imp ? 'background:rgba(99,102,241,0.15);color:var(--accent,#6366f1)' : 'background:rgba(139,148,158,0.15);color:var(--text-mute)');
    var typ = document.createElement('span');
    typ.style.cssText = 'flex:0 0 6.5rem;color:var(--text-mute)';
    typ.textContent = it.type;
    var name = document.createElement('span');
    name.style.cssText = 'font-weight:600;overflow-wrap:anywhere';
    name.textContent = it.name || '(unnamed)';
    r.appendChild(badge);
    r.appendChild(typ);
    r.appendChild(name);
    if (it.detail) {
      var det = document.createElement('span');
      det.style.cssText = 'color:var(--text-mute);font-size:0.78rem';
      det.textContent = it.detail;
      r.appendChild(det);
    }
    return r;
  }

  function showPreview(opts, p, text, filename) {
    var n = p.would_import || 0, s = p.would_skip || 0;
    window.uiOpenModal({
      title: 'Import preview',
      subtitle: filename + ', nothing has been imported yet. ' +
        (opts.subtitle || 'Everything imported lands as a draft for review; a name that already exists is skipped.'),
      width: '760px',
      actions: [
        {label: 'Cancel'},
        {label: 'Import ' + n + (n === 1 ? ' item' : ' items'), primary: true, onClick: function (api, btn) {
          btn.disabled = true;
          btn.textContent = 'Importing…';
          postJSON(opts.importURL, {pack: text}).then(function (d) {
            api.close();
            if (opts.invalidate && window.uiInvalidate) window.uiInvalidate(opts.invalidate);
            if (opts.onDone) opts.onDone(d);
            (window.uiAlert || window.alert)(d.message || 'Import complete.');
          }).catch(function (e) {
            btn.disabled = false;
            btn.textContent = 'Import';
            fail('Import failed: ', e);
          });
        }},
      ],
      mount: function (body, api) {
        if (!n && api.primaryButton) api.primaryButton.disabled = true;
        var sum = document.createElement('div');
        sum.style.cssText = 'font-size:0.85rem;color:var(--text-mute)';
        sum.textContent = 'Would import ' + n + ', skip ' + s + '.';
        body.appendChild(sum);
        var list = document.createElement('div');
        list.style.cssText = 'display:flex;flex-direction:column;gap:0.35rem';
        (p.items || []).forEach(function (it) {
          list.appendChild(row(it));
          (it.warnings || []).forEach(function (wtext) {
            var w = document.createElement('div');
            w.style.cssText = 'font-size:0.78rem;color:var(--warn,#d97706);padding-left:4.2rem';
            w.textContent = 'Warning: ' + wtext;
            list.appendChild(w);
          });
        });
        body.appendChild(list);
      },
    });
  }

  // importFlow opens a file picker, dry-runs the chosen file against
  // opts.previewURL, and shows what would happen. Nothing is written until the
  // person confirms, which posts the SAME bytes to opts.importURL.
  //   opts: {previewURL, importURL, invalidate: [sources], subtitle, onDone(result)}
  function importFlow(opts) {
    var input = document.createElement('input');
    input.type = 'file';
    input.accept = '.json,application/json';
    input.style.display = 'none';
    document.body.appendChild(input);
    input.addEventListener('change', function () {
      var f = input.files && input.files[0];
      input.remove();
      if (!f) return;
      var reader = new FileReader();
      reader.onload = function () {
        var text = String(reader.result || '');
        postJSON(opts.previewURL, {pack: text})
          .then(function (p) { showPreview(opts, p, text, f.name); })
          .catch(function (e) { fail('Preview failed: ', e); });
      };
      reader.onerror = function () { fail('', 'Could not read the selected file.'); };
      reader.readAsText(f);
    });
    input.click();
  }

  window.gohortArtifacts = {download: download, importFlow: importFlow};
})();
