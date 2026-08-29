  // --- document core ----------------------------------------------------
  //
  // Shared machinery for the document workbenches (article_editor,
  // codewriter_panel). Those two components grew the same behaviors
  // independently: a list sidebar, revision navigation, a chat pane
  // beside the editor. This file is where those converge, one builder at
  // a time, so an improvement lands once instead of twice.
  //
  // Each builder owns its DOM and its fetch cycle, and takes callbacks
  // for everything app-shaped (what a record's fields are called, how a
  // loaded revision maps onto the editor). Class names are parameters
  // rather than constants: the two panels are styled differently and
  // converging their CSS would be churn with no functional gain.

  // buildDocList renders the sidebar record list: fetch, sort
  // newest-first, draw a row per record with a hover tooltip and an
  // optional × delete, and hand clicks back to the host. Optional
  // bulk-select mode layers checkboxes and a multi-delete bar on top.
  //
  // opts:
  //   host        — the element to fill (the panel's side list)
  //   listURL     — GET, returns the array of records
  //   idField / labelField / dateField — record field names
  //   metaOf(rec) — optional extra shown before the relative time in the
  //                 row tooltip (codewriter puts the language there)
  //   emptyText   — copy for an empty list
  //   currentID() — the open record's id, for the active-row highlight
  //   onOpen(id, rec)  — a row was clicked; rec is the whole record, so a
  //                     caller can read fields this list does not interpret
  //   deleteURL   — DELETE template with {id}. Absent = no × buttons.
  //   deleteConfirm(label) — confirmation copy for a single delete
  //   onDeleted(id)        — called after a successful delete, before reload
  //   bulk        — {state, selected, confirmMany(n)} to enable
  //                 select-mode; omit for a plain list
  //
  // Returns {reload, markActive}.
  function buildDocList(opts) {
    opts = opts || {};
    var host = opts.host;
    var idF = opts.idField, labelF = opts.labelField, dateF = opts.dateField;

    function rowsOnly() {
      return host.querySelectorAll('.ui-chat-side-item');
    }

    // markActive re-highlights without a refetch — used when the host
    // opens a record it already has.
    function markActive(id) {
      rowsOnly().forEach(function(row) {
        row.classList.toggle('active', row.dataset.id === String(id == null ? '' : id));
      });
    }

    function deleteOne(id, label) {
      var url = opts.deleteURL.replace('{id}', encodeURIComponent(id));
      return fetchJSON(url, {method: 'DELETE'}).then(function() {
        if (opts.onDeleted) opts.onDeleted(id);
        reload();
        showToast('Deleted');
      }).catch(function(err) {
        showToast('Delete failed: ' + err.message);
      });
    }

    function reload() {
      fetchJSON(opts.listURL).then(function(items) {
        items = items || [];
        items.sort(function(a, b) {
          return String(b[dateF] || '').localeCompare(String(a[dateF] || ''));
        });
        host.innerHTML = '';

        var bulk = opts.bulk;
        var inMode = !!(bulk && bulk.state && bulk.state.mode);
        if (bulk) {
          // Drop selections for records that no longer exist, so a
          // stale id can't inflate the delete count.
          var live = {};
          items.forEach(function(it) { live[it[idF]] = true; });
          Object.keys(bulk.selected).forEach(function(k) {
            if (!live[k]) delete bulk.selected[k];
          });
        }

        if (!items.length) {
          if (inMode) {
            renderBulkBar([], host, bulk.state, bulk.selected,
              function(it){ return it[idF]; }, reload, function(){});
          }
          host.appendChild(el('div', {class: 'ui-chat-empty', style: 'padding:0.5rem;text-align:left'},
            [opts.emptyText || 'Nothing here yet.']));
          return;
        }

        if (bulk) {
          renderBulkBar(items, host, bulk.state, bulk.selected,
            function(it){ return it[idF]; }, reload,
            async function() {
              var keys = Object.keys(bulk.selected);
              if (!keys.length) return;
              var msg = bulk.confirmMany
                ? bulk.confirmMany(keys.length)
                : 'Delete ' + keys.length + ' item(s) permanently?';
              if (!(await window.uiConfirm(msg))) return;
              Promise.all(keys.map(function(id) {
                var url = opts.deleteURL.replace('{id}', encodeURIComponent(id));
                return fetchJSON(url, {method: 'DELETE'}).catch(function(){});
              })).then(function() {
                if (opts.onDeleted && bulk.selected[opts.currentID && opts.currentID()]) {
                  opts.onDeleted(opts.currentID());
                }
                Object.keys(bulk.selected).forEach(function(k){ delete bulk.selected[k]; });
                bulk.state.mode = false;
                reload();
              });
            });
        }

        var cur = opts.currentID ? opts.currentID() : null;
        items.forEach(function(it) {
          var id = it[idF];
          var label = it[labelF] || '(untitled)';
          var extra = opts.metaOf ? opts.metaOf(it) : '';
          var meta = (extra ? extra + ' · ' : '') + relTime(it[dateF]);
          var selected = !!(opts.bulk && opts.bulk.selected[id]);
          var row = el('div', {
            class: 'ui-chat-side-item' +
              (id === cur ? ' active' : '') +
              (inMode ? ' selectable' : '') +
              (selected ? ' selected' : '') +
              // A row that RUNS something rather than opening a record behaves
              // differently from every other row in the list, so it is marked
              // and styled differently. Structural, not app-specific: the list
              // still does not know what the action does.
              ((it.Action || it.action) ? ' is-action' : ''),
            // Native tooltip carries the full label + metadata, so the
            // row can ellipsize at a narrow sidebar width without
            // hiding information.
            title: label + ' — ' + meta,
          }, [
            el('div', {class: 'ui-chat-side-text'}, [
              el('div', {class: 'ui-chat-side-title'}, [label]),
            ]),
          ]);
          row.dataset.id = String(id == null ? '' : id);
          // data-bulk-id lets renderBulkBar scope "Select all" to rows
          // the search filter is actually showing. Untagged rows make it
          // fall back to selecting EVERYTHING, which silently ignored
          // the filter — tagging here fixes that for every caller.
          if (opts.bulk) row.setAttribute('data-bulk-id', String(id == null ? '' : id));

          if (opts.deleteURL && !inMode) {
            row.appendChild(el('button', {
              class: 'ui-chat-side-del', title: 'Delete',
              onclick: async function(ev) {
                ev.stopPropagation();
                var msg = opts.deleteConfirm
                  ? opts.deleteConfirm(label)
                  : 'Delete "' + label + '" permanently?';
                if (!(await window.uiConfirm(msg))) return;
                deleteOne(id, label);
              },
            }, ['×']));
          }
          row.addEventListener('click', function(ev) {
            // The × is a button child with its own handler; don't also
            // open the record behind the confirm dialog.
            if (ev.target.classList.contains('ui-chat-side-del')) return;
            if (inMode) {
              if (opts.bulk.selected[id]) delete opts.bulk.selected[id];
              else opts.bulk.selected[id] = true;
              reload();
              return;
            }
            if (opts.onOpen) opts.onOpen(id, it);
          });
          host.appendChild(row);
        });
      }).catch(function(err) {
        host.innerHTML = '';
        host.appendChild(el('div', {class: 'ui-chat-empty'}, ['Failed to load: ' + err.message]));
      });
    }

    return {reload: reload, markActive: markActive};
  }

  // openRulesPanel edits the standing instructions the assistant must
  // follow in this app. One rule per line, appended to the system prompt
  // on every call.
  //
  // Built on uiOpenModal rather than a panel-local slide-in, so every
  // writer app offers rules the same way. This also retires one of the
  // two remaining "builtin" ToolbarAction methods.
  //
  // opts: url (GET → {rules}, POST {rules}), noun (what the rules govern).
  function openRulesPanel(opts) {
    opts = opts || {};
    if (!opts.url) return;
    var ta = el('textarea', {
      class: 'ui-doc-rules-ta',
      placeholder: 'One rule per line. Examples:\n  Never post API keys, passwords, or other secrets.\n  Match the existing tone — terse, factual.',
    });
    ta.value = '(loading…)';
    ta.disabled = true;
    var status = el('span', {class: 'ui-doc-rules-status'});

    window.uiOpenModal({
      title: 'Rules',
      subtitle: 'Each line is one rule, appended to the assistant’s system prompt as a constraint on every ' +
        (opts.noun || 'message') + '.',
      width: 'min(680px, 94vw)',
      actions: [
        {label: 'Close'},
        {label: 'Save rules', primary: true, onClick: function(api, btn) {
          btn.disabled = true;
          status.textContent = 'saving…';
          fetchJSON(opts.url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({rules: ta.value}),
          }).then(function() {
            showToast('Rules saved — applies to the next assistant message');
            api.close();
          }).catch(function(err) {
            btn.disabled = false;
            status.textContent = '';
            showToast('Save failed: ' + err.message);
          });
        }},
      ],
      mount: function(body) {
        // Rules inherited from the deployment, shown READ-ONLY above your own.
        // They already reach the model by another path, so this is not an input
        // and editing them here would be a lie; it exists because "which rules
        // apply to this?" was otherwise answerable only by knowing that a second
        // screen existed and going to look at it.
        var inherited = document.createElement('div');
        inherited.style.cssText = 'display:none;margin-bottom:0.7rem';
        body.appendChild(inherited);
        ta.style.cssText = 'width:100%;min-height:38vh;flex:1 1 auto;resize:vertical;box-sizing:border-box';
        body.appendChild(ta);
        body.appendChild(status);
        fetchJSON(opts.url).then(function(d) {
          ta.disabled = false;
          var inh = (d && d.inherited || '').trim();
          if (inh) {
            var h = document.createElement('div');
            // No pointer to where these are edited: naming an admin path here
            // would put one app's navigation into a shared component. The panel
            // says they exist and are not yours; finding them is the host's job.
            h.textContent = 'Also in force everywhere, set by this deployment';
            h.style.cssText = 'font-size:0.78rem;color:var(--text-mute);margin-bottom:0.25rem';
            var pre = document.createElement('div');
            pre.textContent = inh;
            pre.style.cssText = 'white-space:pre-wrap;font-size:0.82rem;line-height:1.5;color:var(--text-mute);background:var(--bg-2);border:1px solid var(--border);border-radius:6px;padding:0.5rem 0.7rem;max-height:22vh;overflow:auto';
            inherited.appendChild(h); inherited.appendChild(pre);
            inherited.style.display = 'block';
          }
          ta.value = (d && d.rules) || '';
          ta.focus();
        }).catch(function() {
          ta.disabled = false;
          ta.value = '';
          status.textContent = 'Could not load existing rules — saving will overwrite them.';
        });
      },
    });
  }

  // openTemplatePicker shows the document-skeleton picker: the user's
  // saved templates above the host's built-in ones, with a save-current
  // action when the host wired a store.
  //
  // Built on the shared uiOpenModal rather than a panel-local shell, so
  // every writer app gets the same picker instead of its own copy.
  //
  // opts:
  //   builtins     — [{name, description, body}] declared by the host
  //   listURL      — GET saved templates; POST to create. Omit for
  //                  built-ins only (no save, no delete).
  //   itemURL      — DELETE {id}
  //   currentBody() → the text "save current" captures
  //   currentName() → seeds the name prompt
  //   onApply(tpl)  — fill the editor from a template
  function openTemplatePicker(opts) {
    opts = opts || {};
    var builtins = opts.builtins || [];
    var canSave = !!opts.listURL;
    if (!builtins.length && !canSave) { showToast('No templates configured'); return; }

    function rows(host, items, deletable, api) {
      items.forEach(function(tpl) {
        var row = el('div', {class: 'ui-doc-tpl-row'});
        var info = el('div', {class: 'ui-doc-tpl-info'});
        info.appendChild(el('div', {class: 'ui-doc-tpl-name'}, [tpl.name || '(unnamed)']));
        if (tpl.description) {
          info.appendChild(el('div', {class: 'ui-doc-tpl-desc'}, [tpl.description]));
        }
        info.addEventListener('click', function() {
          if (opts.onApply) opts.onApply(tpl);
          api.close();
        });
        row.appendChild(info);
        // Built-ins carry no delete: load() re-offers them every time, so
        // the button would promise something it cannot do.
        if (deletable && opts.itemURL && tpl.id) {
          var del = el('button', {class: 'ui-row-btn danger compact', title: 'Delete this template'}, ['×']);
          del.addEventListener('click', async function(ev) {
            ev.stopPropagation();
            if (!(await window.uiConfirm('Delete the template "' + (tpl.name || 'this template') + '"?'))) return;
            fetchJSON(opts.itemURL.replace('{id}', encodeURIComponent(tpl.id)), {method: 'DELETE'})
              .then(function(){ api.close(); openTemplatePicker(opts); })
              .catch(function(err){ showToast('Delete failed: ' + err.message); });
          });
          row.appendChild(del);
        }
        host.appendChild(row);
      });
    }

    async function saveCurrent(api) {
      var body = opts.currentBody ? opts.currentBody() : '';
      if (!body.trim()) { showToast('Nothing to save — the document is empty'); return; }
      var name = await uiPrompt('Name this template:', (opts.currentName && opts.currentName()) || '');
      if (name === null) return;
      name = String(name).trim();
      if (!name) return;
      var desc = await uiPrompt('Short description (optional) — this is what tells rows apart in the picker:', '');
      if (desc === null) desc = '';
      fetchJSON(opts.listURL, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({name: name, description: String(desc).trim(), body: body}),
      }).then(function() {
        showToast('Saved template "' + name + '"');
        api.close();
        openTemplatePicker(opts);
      }).catch(function(err) {
        showToast('Save failed: ' + err.message);
      });
    }

    var actions = [{label: 'Close'}];
    if (canSave) {
      actions.unshift({label: 'Save current as template', onClick: function(api){ saveCurrent(api); }});
    }
    window.uiOpenModal({
      title: 'Templates',
      subtitle: 'Start from a skeleton. This replaces whatever is in the editor.',
      width: 'min(620px, 94vw)',
      actions: actions,
      mount: function(body, api) {
        if (canSave) {
          body.appendChild(el('div', {class: 'ui-doc-tpl-head'}, ['Your templates']));
          var saved = el('div', {class: 'ui-doc-tpl-list'}, ['Loading…']);
          body.appendChild(saved);
          fetchJSON(opts.listURL).then(function(items) {
            saved.innerHTML = '';
            items = items || [];
            if (!items.length) {
              saved.appendChild(el('div', {class: 'ui-doc-tpl-empty'},
                ['Nothing saved yet. Write a document, then use "Save current as template".']));
              return;
            }
            rows(saved, items, true, api);
          }).catch(function(err) {
            saved.innerHTML = '';
            saved.appendChild(el('div', {class: 'ui-doc-tpl-empty'}, ['Failed to load: ' + err.message]));
          });
        }
        if (builtins.length) {
          body.appendChild(el('div', {class: 'ui-doc-tpl-head'}, ['Built-in']));
          var bi = el('div', {class: 'ui-doc-tpl-list'});
          rows(bi, builtins, false, api);
          body.appendChild(bi);
        }
      },
    });
  }

  // buildDocChat owns the chat PROTOCOL beside a document editor: the
  // re-entrancy guard, the thinking placeholder, the history window, the
  // POST, and the branch between "the model proposed a rewrite" and "the
  // model said something".
  //
  // It deliberately does NOT own rendering. The two panels draw messages
  // differently (one escapes to textContent, the other formats fenced
  // code into HTML) and that is a real difference, not drift — so
  // appendMsg is a required callback rather than something to unify.
  //
  // opts:
  //   url          — chat endpoint
  //   appendMsg(role, text) → node — role is 'user' | 'assistant' |
  //                  'error'. Required; the app owns the markup.
  //   thinking()   → node — optional placeholder shown while in flight,
  //                  removed on reply. Defaults to an assistant bubble.
  //   buildBody(message, mode, history) → object — the POST payload.
  //                  Required: only the app knows its own field names.
  //   proposalOf(data, mode) → text | null — non-null means the reply is
  //                  a document rewrite to review rather than prose.
  //   onProposal(text, data) — apply/queue the rewrite (the diff pane).
  //   onReply(text, data)    — optional hook after a plain reply.
  //   setBusy(bool)          — optional; toggle the app's send controls.
  //   historyLimit — turns kept and resent (default 40).
  //
  // Returns {send(message, mode), clear(), history()}.
  function buildDocChat(opts) {
    opts = opts || {};
    var limit = opts.historyLimit || 40;
    var history = [];
    var sending = false;

    function remember(role, content) {
      history.push({role: role, content: content});
      // Cap the window: an unbounded transcript grows every request
      // until the model's context is spent on its own backlog.
      if (history.length > limit) history = history.slice(-limit);
    }

    function busy(on) {
      sending = on;
      if (opts.setBusy) opts.setBusy(on);
    }

    function send(message, mode) {
      // One request at a time. Without this, a double-click sends twice
      // and the two replies interleave into the same transcript.
      if (sending) return;
      var text = String(message == null ? '' : message).trim();
      if (!text) return;

      opts.appendMsg('user', text);
      // Snapshot BEFORE recording this turn — the message rides in its
      // own field, so including it in history too would double it.
      var priorHistory = history.slice();
      remember('user', text);

      busy(true);
      var placeholder = opts.thinking ? opts.thinking() : opts.appendMsg('assistant', '…');

      fetchJSON(opts.url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(opts.buildBody(text, mode, priorHistory)),
      }).then(function(data) {
        if (placeholder && placeholder.remove) placeholder.remove();
        if (!data) { opts.appendMsg('error', '(empty response)'); return; }
        // A 200 carrying an error field is still an error.
        if (data.error) { opts.appendMsg('error', String(data.error)); return; }

        var proposal = opts.proposalOf ? opts.proposalOf(data, mode) : null;
        if (proposal != null && proposal !== '') {
          if (opts.onProposal) opts.onProposal(proposal, data);
        } else if (opts.onReply) {
          opts.onReply(data.content || '', data);
        } else {
          opts.appendMsg('assistant', data.content || '');
        }
        // The prose reply is what the model should see next turn, on
        // both branches: a proposal's text already lives in the editor.
        remember('assistant', data.content || '');
      }).catch(function(err) {
        if (placeholder && placeholder.remove) placeholder.remove();
        opts.appendMsg('error', err.message);
      }).then(function() {
        busy(false);
      });
    }

    return {
      send: send,
      clear: function() { history = []; },
      history: function() { return history.slice(); },
    };
  }

  // buildRevisionNav builds the back / indicator / forward group that
  // walks a record's revision history, plus the "make current" button
  // that promotes an older revision by re-saving it.
  //
  // opts:
  //   listURL          — GET template, {id} substituted. Absent = no nav.
  //   loadURL          — GET template for one revision. Both {revid} and
  //                      {id} are substituted, because the two callers
  //                      spell the placeholder differently and neither
  //                      URL should have to change to share this code.
  //   onLoad(rev)      — apply a fetched revision to the editor. Required;
  //                      it is the only app-specific part of the walk.
  //   onMakeCurrent()  — invoked by the make-current button, normally the
  //                      app's save (saving an older revision's text
  //                      appends it as the newest one).
  //   makeLabel/makeTitle          — copy for that button.
  //   btnClass/indicatorClass/makeClass/groupClass — styling hooks.
  //
  // Returns {group, reload(recordID), clear()}. group is null when the
  // host configured no revision URLs, so callers can append it blindly.
  function buildRevisionNav(opts) {
    opts = opts || {};
    if (!opts.listURL) {
      return {group: null, reload: function(){}, clear: function(){}};
    }
    var revisions = [];
    var index = -1;

    var backBtn = el('button', {
      class: opts.btnClass || 'ui-row-btn compact', title: 'Previous revision',
      onclick: function(){ navigate(-1); },
    }, ['◀']);
    var fwdBtn = el('button', {
      class: opts.btnClass || 'ui-row-btn compact', title: 'Next revision',
      onclick: function(){ navigate(1); },
    }, ['▶']);
    var indicator = el('span', {class: opts.indicatorClass || 'ui-tw-rev-indicator'}, []);
    var makeBtn = el('button', {
      class: opts.makeClass || 'ui-row-btn',
      title: opts.makeTitle || 'Save the displayed revision as the latest version',
      onclick: function(){ if (opts.onMakeCurrent) opts.onMakeCurrent(); },
    }, [opts.makeLabel || 'Make current']);
    makeBtn.style.display = 'none';

    var group = el('span', {class: opts.groupClass || 'ui-cw-rev-group', style: 'display:none'},
      [makeBtn, backBtn, indicator, fwdBtn]);

    function update() {
      var n = revisions.length;
      backBtn.disabled = index <= 0;
      fwdBtn.disabled = index >= n - 1;
      var cur = (index >= 0) ? revisions[index] : null;
      indicator.textContent = n > 0
        ? 'rev ' + (index + 1) + '/' + n + (cur && cur.label ? ' · ' + cur.label : '')
        : '';
      // "Make current" only means something while looking at an older
      // revision; on the newest it would be a no-op save.
      makeBtn.style.display = (n > 0 && index < n - 1) ? 'inline-flex' : 'none';
      // Explicit inline-flex / none: the bordered-span CSS carries no
      // display of its own, so an empty string would leave it visible.
      group.style.display = n > 0 ? 'inline-flex' : 'none';
    }

    function clear() {
      revisions = [];
      index = -1;
      update();
    }

    function reload(recordID) {
      if (!recordID) { clear(); return; }
      var url = opts.listURL.replace('{id}', encodeURIComponent(recordID));
      fetchJSON(url).then(function(items) {
        revisions = items || [];
        // Sort oldest-first when the records carry a date, so "forward"
        // always means newer. Servers that already return them in order
        // are unaffected; ones that don't were relying on luck.
        revisions.sort(function(a, b) {
          return String(a.date || '').localeCompare(String(b.date || ''));
        });
        index = revisions.length - 1;
        update();
      }).catch(function() { clear(); });
    }

    function navigate(dir) {
      var idx = index + dir;
      if (idx < 0 || idx >= revisions.length) return;
      if (!opts.loadURL) { showToast('Revision load not configured'); return; }
      var rid = encodeURIComponent(revisions[idx].id);
      var url = opts.loadURL.replace('{revid}', rid).replace('{id}', rid);
      fetchJSON(url).then(function(rev) {
        if (opts.onLoad) opts.onLoad(rev);
        index = idx;
        update();
      }).catch(function(err) {
        showToast('Could not load revision: ' + err.message);
      });
    }

    update();
    return {group: group, reload: reload, clear: clear};
  }
