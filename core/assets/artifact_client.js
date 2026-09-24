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

  // What each dependency kind is called in the export dialog, in the order the
  // dialog lists them.
  var KINDS = [
    ['tool', 'Tools'],
    ['skill', 'Skills'],
    ['collection', 'Knowledge collections'],
    ['pipeline', 'Pipelines'],
    ['machine', 'Machines'],
    ['agent', 'Agents'],
    ['custom_app', 'Custom apps'],
    ['monitor', 'Monitors'],
  ];
  // A collection carries its documents' full text: it can be large and it is
  // the owner's data, so it goes only when asked for.
  var OFF_BY_DEFAULT = {collection: true};
  var KIND_NOTES = {
    collection: 'Carries the documents\' full text, and can be large.',
  };

  // exportFlow downloads one artifact, first letting the person choose which
  // kinds of dependency travel with it. It asks the export endpoint for the
  // plan (what the item depends on); with nothing to choose it downloads at
  // once, otherwise it opens a dialog with one checkbox per kind.
  //   opts: {base (export endpoint), type, name (name or id), label (filename), note}
  function exportFlow(opts) {
    var base = opts.base || '/account/api/artifacts/export';
    var q = '?type=' + encodeURIComponent(opts.type) + '&name=' + encodeURIComponent(opts.name) +
      (opts.label ? '&file=' + encodeURIComponent(opts.label) : '');
    fetch(base + q + '&plan=1', {credentials: 'same-origin'}).then(function (r) {
      return r.ok ? r.json() : r.text().then(function (t) { throw new Error(t || ('HTTP ' + r.status)); });
    }).then(function (plan) {
      var deps = plan.dependencies || [];
      if (!deps.length) { download(base + q + '&deps=0'); return; }
      var byKind = {};
      deps.forEach(function (d) { (byKind[d.type] = byKind[d.type] || []).push(d.name); });
      var boxes = {};
      window.uiOpenModal({
        title: 'Export ' + (opts.label || opts.name),
        subtitle: 'Choose what travels with it. Anything left out has to exist wherever the file is imported, or that part will not work there. No secrets are ever included.',
        width: '560px',
        actions: [
          {label: 'Cancel'},
          {label: 'Export', primary: true, onClick: function (api) {
            var chosen = [];
            Object.keys(boxes).forEach(function (k) { if (boxes[k].checked) chosen.push(k); });
            download(base + q + (chosen.length ? '&include=' + encodeURIComponent(chosen.join(',')) : '&deps=0'));
            api.close();
          }},
        ],
        mount: function (body) {
          if (opts.note) {
            var n = document.createElement('div');
            n.style.cssText = 'font-size:0.82rem;color:var(--text-mute)';
            n.textContent = opts.note;
            body.appendChild(n);
          }
          KINDS.forEach(function (kind) {
            var names = byKind[kind[0]];
            if (!names) return;
            delete byKind[kind[0]];
            body.appendChild(kindRow(kind[0], kind[1], names, boxes));
          });
          // A kind this list does not name yet still gets a checkbox.
          Object.keys(byKind).forEach(function (k) {
            body.appendChild(kindRow(k, k, byKind[k], boxes));
          });
        },
      });
    }).catch(function (e) { fail('Export failed: ', e); });
  }

  function kindRow(kind, label, names, boxes) {
    var wrap = document.createElement('label');
    wrap.style.cssText = 'display:flex;gap:0.6rem;align-items:flex-start;padding:0.45rem 0;border-bottom:1px solid var(--border);cursor:pointer';
    var cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !OFF_BY_DEFAULT[kind];
    cb.style.marginTop = '0.2rem';
    boxes[kind] = cb;
    var text = document.createElement('div');
    var head = document.createElement('div');
    head.style.cssText = 'font-weight:600;font-size:0.88rem';
    head.textContent = label + ' (' + names.length + ')';
    var list = document.createElement('div');
    list.style.cssText = 'font-size:0.8rem;color:var(--text-mute);overflow-wrap:anywhere';
    list.textContent = names.join(', ');
    text.appendChild(head);
    text.appendChild(list);
    if (KIND_NOTES[kind]) {
      var note = document.createElement('div');
      note.style.cssText = 'font-size:0.78rem;color:var(--warn,#d97706)';
      note.textContent = KIND_NOTES[kind];
      text.appendChild(note);
    }
    wrap.appendChild(cb);
    wrap.appendChild(text);
    return wrap;
  }

  // exportAction builds a client-action handler for an Export button. On a
  // table row it reads the record (nameField: the name or id to export,
  // labelField: the filename); on a toolbar it reads action.data, a JSON
  // object {name, label}.
  function exportAction(type, nameField, labelField) {
    return function (ctx) {
      var rec = ctx && ctx.record;
      var name = '', label = '';
      if (rec) {
        name = rec[nameField || 'name'];
        label = rec[labelField || 'name'] || name;
      } else if (ctx && ctx.action && ctx.action.data) {
        try {
          var d = JSON.parse(ctx.action.data);
          name = d.name;
          label = d.label || d.name;
        } catch (e) { name = ctx.action.data; }
      }
      if (!name) { fail('', 'Nothing to export.'); return; }
      exportFlow({type: type, name: String(name), label: String(label || name)});
    };
  }

  window.gohortArtifacts = {download: download, importFlow: importFlow, exportFlow: exportFlow, exportAction: exportAction};
})();
