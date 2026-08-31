  components.empty_state = function(cfg) {
    var wrap = el('div', {class: 'ui-empty'});
    if (cfg.icon) wrap.appendChild(el('div', {class: 'ui-empty-icon'}, [cfg.icon]));
    if (cfg.title) wrap.appendChild(el('div', {class: 'ui-empty-title'}, [cfg.title]));
    if (cfg.hint) wrap.appendChild(el('div', {class: 'ui-empty-hint'}, [cfg.hint]));
    if (cfg.action_label && cfg.action_url) {
      var btn = el('button', {class: 'ui-empty-action'}, [cfg.action_label]);
      btn.addEventListener('click', function() {
        if ((cfg.action_method || 'GET').toUpperCase() === 'GET') { window.location.href = cfg.action_url; return; }
        fetch(cfg.action_url, {method: cfg.action_method}).then(function(){ location.reload(); });
      });
      wrap.appendChild(btn);
    }
    return wrap;
  };

  // workbench_panel — three columns: item list (left), markdown viewer of the
  // selected item (center), chat (right). Owns the shared selection state the
  // three sub-surfaces lack on their own: clicking a list row loads that record
  // into the viewer. New affordance + chat are mounted sub-components (reuse
  // modal_button/form_panel + agent_loop_panel). See ui.WorkbenchPanel.
  components.workbench_panel = function(cfg) {
    var itemKey   = cfg.item_key   || 'id';
    var itemLabel = cfg.item_label || 'title';
    var bodyField = cfg.body_field || 'content';
    var selectedId = null;

    var root = el('div', {class: 'ui-wb'});

    // --- LEFT: list -------------------------------------------------------
    var left = el('div', {class: 'ui-wb-col ui-wb-list'});
    var head = el('div', {class: 'ui-wb-head'}, [el('span', {class: 'ui-wb-head-t', text: cfg.list_title || 'Items'})]);
    var headActions = el('div', {class: 'ui-wb-head-actions'});
    // Record-scoped header actions (e.g. Edit) sit to the LEFT of the New button,
    // grouped on the right of the header. Enabled only when a record is selected,
    // dispatched through the same runViewerAction path as the viewer toolbar.
    if (cfg.list_actions && cfg.list_actions.length) {
      cfg.list_actions.forEach(function(a) {
        if (a.kind === 'menu') { headActions.appendChild(buildActionMenu(a)); return; }
        var b = el('button', {class: 'ui-wb-action-btn', text: a.label});
        b.disabled = true;
        b.addEventListener('click', function() { runViewerAction(a, b); });
        headActions.appendChild(b);
      });
    }
    if (cfg.new_button) {
      var nbWrap = el('div', {class: 'ui-wb-new'});
      mountComponent(cfg.new_button, nbWrap);
      headActions.appendChild(nbWrap);
    }
    head.appendChild(headActions);
    left.appendChild(head);
    var listBody = el('div', {class: 'ui-wb-list-body'});
    left.appendChild(listBody);

    // --- CENTER: viewer ---------------------------------------------------
    var center = el('div', {class: 'ui-wb-col ui-wb-viewer'});
    // Optional per-document action toolbar (export / history / audit). Buttons
    // act on the selected record; disabled until one is selected.
    var actionBar = null;
    if (cfg.viewer_actions && cfg.viewer_actions.length) {
      actionBar = el('div', {class: 'ui-wb-actions'});
      cfg.viewer_actions.forEach(function(a) {
        if (a.kind === 'menu') {
          actionBar.appendChild(buildActionMenu(a));
          return;
        }
        var b = el('button', {class: 'ui-wb-action-btn', text: a.label});
        b.disabled = true;
        b.addEventListener('click', function() { runViewerAction(a, b); });
        actionBar.appendChild(b);
      });
      center.appendChild(actionBar);
    }
    var viewerBody = el('div', {class: 'ui-wb-viewer-body'});
    center.appendChild(viewerBody);

    // --- RIGHT: chat ------------------------------------------------------
    // Build the chat in LIVE JS from the stored chat's ENDPOINTS, rather than
    // mounting whatever panel type the spec baked in. This forces an
    // agent_loop_panel (the panel whose SSE parser matches chat/send's wire
    // format — a single-mode ChatPanel silently drops those frames), so a
    // workbench authored before this fix renders correctly WITHOUT a rebuild.
    // No-list (no session URLs) + lock_activity = one clean chat window.
    var right = el('div', {class: 'ui-wb-col ui-wb-chat'});
    var chatCfg = cfg.chat || {};
    // Carry the app's OWN chat config through. This used to be rebuilt field by
    // field from a fixed list of six, which silently discarded everything else
    // an app declared — a mid-turn inject_url, a session rail, a truncate_url —
    // with no error anywhere: the field simply never reached the panel, and the
    // app author is left looking at their own correct-looking Go.
    //
    // What the workbench genuinely imposes is forced, and only that:
    //   type          — chat/send emits the agent_loop_panel SSE format
    //                   (sse.Send); a ChatPanel's parser ignores those frames,
    //                   so its replies never render. Forcing it also means a
    //                   workbench authored before that was understood renders
    //                   correctly with no rebuild.
    //   lock_activity — the chat is the third of three columns and has no room
    //                   for an activity pane beside it.
    // Everything else is the app's call, including whether to offer a session
    // rail at all: declare the three session URLs and it appears (collapsed,
    // behind a hamburger), omit them and the column stays one clean window.
    var chatMount = {};
    for (var ck in chatCfg) {
      if (Object.prototype.hasOwnProperty.call(chatCfg, ck)) chatMount[ck] = chatCfg[ck];
    }
    chatMount.type          = 'agent_loop_panel';
    chatMount.lock_activity = true;
    chatMount.send_url      = chatCfg.send_url   || 'chat/send';
    chatMount.cancel_url    = chatCfg.cancel_url || 'chat/cancel';
    chatMount.empty_text    = chatCfg.empty_text || 'Ask the assistant to draft or add a section.';
    chatMount.placeholder   = chatCfg.placeholder || 'Ask the assistant…';
    if (chatMount.markdown === undefined) chatMount.markdown = true;
    mountComponent(chatMount, right);

    // Mobile: the list column becomes a slide-in drawer, reusing the same
    // makeDrawer machinery (hamburger header + backdrop) as the chat/
    // pipeline/article sidebars. The header only renders <=700px via the
    // shared ui-chat-mobile-hdr rules; on desktop nothing changes. A ✕ in
    // the list header closes the drawer, and so does selecting an item —
    // the phone flow is hamburger → pick → read.
    var drawer = makeDrawer(left, {
      title: cfg.list_title || 'Items',
      hamburgerTitle: 'Show ' + (cfg.list_title || 'items'),
    });
    var wbClose = el('button', {class: 'ui-wb-close', title: 'Close', onclick: drawer.closeDrawer}, ['✕']);
    head.insertBefore(wbClose, head.firstChild);

    root.appendChild(drawer.mobileHdr);
    root.appendChild(left);
    root.appendChild(center);
    root.appendChild(right);
    root.appendChild(drawer.backdrop);

    function showEmpty() {
      setActionsEnabled(false);
      viewerBody.innerHTML = '';
      var e = el('div', {class: 'ui-empty'});
      if (cfg.empty_icon)  e.appendChild(el('div', {class: 'ui-empty-icon',  text: cfg.empty_icon}));
      e.appendChild(el('div', {class: 'ui-empty-title', text: cfg.empty_title || 'Nothing selected'}));
      if (cfg.empty_hint)  e.appendChild(el('div', {class: 'ui-empty-hint',  text: cfg.empty_hint}));
      viewerBody.appendChild(e);
    }

    function highlight() {
      var rows = listBody.querySelectorAll('.ui-wb-item');
      for (var i = 0; i < rows.length; i++) {
        rows[i].classList.toggle('active', rows[i].getAttribute('data-id') === selectedId);
      }
    }

    function setActionsEnabled(on) {
      // Record-scoped buttons live in BOTH the viewer toolbar and the list header
      // (e.g. Edit next to New) — toggle both so they enable/disable together.
      var scopes = [actionBar, headActions];
      for (var s = 0; s < scopes.length; s++) {
        if (!scopes[s]) continue;
        var btns = scopes[s].querySelectorAll('.ui-wb-action-btn');
        for (var i = 0; i < btns.length; i++) btns[i].disabled = !on;
      }
    }

    // buildActionMenu renders a Kind:"menu" toolbar action as a dropdown button
    // whose children dispatch through the normal runViewerAction path. Generic —
    // any app can collapse related actions into one button (e.g. Export → HTML /
    // PDF / Markdown). The trigger keeps the .ui-wb-action-btn class so it enables
    // and disables with the rest of the bar when a record is (de)selected.
    function buildActionMenu(a) {
      var wrap = el('div', {class: 'ui-wb-action-wrap'});
      var trigger = el('button', {class: 'ui-wb-action-btn', text: (a.label || 'More') + ' ▾'});
      trigger.disabled = true;
      var menu = el('div', {class: 'ui-wb-menu'});
      menu.style.display = 'none';
      function closeMenu() {
        menu.style.display = 'none';
        document.removeEventListener('click', onDocClick, true);
      }
      function onDocClick(ev) { if (!wrap.contains(ev.target)) closeMenu(); }
      (a.children || []).forEach(function(child) {
        var item = el('button', {class: 'ui-wb-menu-item', text: child.label});
        item.addEventListener('click', function() { closeMenu(); runViewerAction(child, trigger); });
        menu.appendChild(item);
      });
      trigger.addEventListener('click', function(ev) {
        ev.stopPropagation();
        if (menu.style.display === 'none') {
          menu.style.display = 'flex';
          document.addEventListener('click', onDocClick, true);
        } else {
          closeMenu();
        }
      });
      wrap.appendChild(trigger);
      wrap.appendChild(menu);
      return wrap;
    }

    // runViewerAction dispatches a viewer toolbar button against the open record.
    // An optional a.confirm gates the action behind the themed uiConfirm modal.
    function runViewerAction(a, btn) {
      if (!selectedId) return;
      if (a.confirm) {
        window.uiConfirm(a.confirm).then(function(ok) { if (ok) doViewerAction(a, btn); });
        return;
      }
      doViewerAction(a, btn);
    }
    function doViewerAction(a, btn) {
      var url = (a.url || '').replace('{id}', encodeURIComponent(selectedId));
      if (a.kind === 'client') {
        // Browser-side action — dispatch by name (a.url carries the action
        // name) to a handler registered via window.uiRegisterClientAction.
        // The handler gets the open record id + a refresh hook so app-specific
        // toolbar behavior (open a picker, copy, print, …) stays out of core/ui.
        var fn = window.UIClientActions && window.UIClientActions[a.url];
        if (typeof fn === 'function') {
          fn({recordId: selectedId, button: btn, action: a, refresh: function(){ loadViewer(selectedId); }});
        } else {
          showToast('No handler for client action: ' + a.url);
        }
        return;
      }
      if (a.kind === 'download') {
        window.open(url, '_blank');
        return;
      }
      if (a.kind === 'report') {
        var orig = btn.textContent;
        btn.disabled = true; btn.textContent = a.spinner || 'Working…';
        // Cancellable: the working modal's Cancel aborts the request, which
        // cancels the server-side run (the handler drives its agent loop off the
        // request context).
        var controller = (typeof AbortController !== 'undefined') ? new AbortController() : null;
        var cancelled = false, workDlg = null;
        function closeWork() { if (workDlg) { try { workDlg.close(); workDlg.remove(); } catch(e){} workDlg = null; } }
        function restore() { btn.disabled = false; btn.textContent = orig; }
        window.uiOpenSimpleModal({title: a.label, width: '420px', mount: function(body, dlg) {
          workDlg = dlg;
          body.appendChild(el('div', {class: 'ui-wb-working'}, [
            el('span', {class: 'ui-spinner'}), el('span', {text: a.spinner || 'Working…'}),
          ]));
          var cancel = el('button', {class: 'ui-row-btn', text: 'Cancel', onclick: function() {
            cancelled = true;
            if (controller) { try { controller.abort(); } catch(e){} }
            closeWork(); restore();
          }});
          body.appendChild(el('div', {class: 'ui-wb-working-actions'}, [cancel]));
        }});
        var fopts = {method: 'POST', credentials: 'same-origin'};
        if (controller) fopts.signal = controller.signal;
        fetch(url, fopts)
          .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t); }); })
          .then(function(d) {
            restore(); closeWork();
            if (a.invalidate && a.invalidate.length && window.uiInvalidate) window.uiInvalidate(a.invalidate);
            window.uiOpenSimpleModal({title: a.label, width: '720px', mount: function(body) {
              var md = el('div', {class: 'ui-wb-md'});
              body.appendChild(md);
              uiRenderMarkdown(md, (d && d.report) || '_(no report)_');
              // Optional follow-up action the report handler returned (d.apply):
              // a button that POSTs the report BACK to an endpoint so a read-only
              // report (e.g. an audit) can offer a one-click "apply" without
              // re-deriving the findings. The report markdown rides in the body as
              // {report}. On success we invalidate + replace the modal contents
              // with the returned summary. Kept generic — core/ui never knows what
              // "apply" means for a given app.
              var ap = d && d.apply;
              if (ap && ap.url) {
                var footer = el('div', {class: 'ui-wb-working-actions'});
                var applyBtn = el('button', {class: 'ui-wb-action-btn', text: ap.label || 'Apply'});
                applyBtn.addEventListener('click', function() {
                  function go() {
                    var aurl = (ap.url || '').replace('{id}', encodeURIComponent(selectedId));
                    applyBtn.disabled = true; applyBtn.textContent = ap.spinner || 'Applying…';
                    fetch(aurl, {method: 'POST', credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({report: (d && d.report) || ''})})
                      .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t); }); })
                      .then(function(res) {
                        if (ap.invalidate && ap.invalidate.length && window.uiInvalidate) window.uiInvalidate(ap.invalidate);
                        footer.remove();
                        uiRenderMarkdown(md, (res && res.report) || '_Applied._');
                      })
                      .catch(function(err) { applyBtn.disabled = false; applyBtn.textContent = ap.label || 'Apply'; alert((ap.label || 'Apply') + ' failed: ' + (err && err.message || err)); });
                  }
                  if (ap.confirm) { window.uiConfirm(ap.confirm).then(function(ok){ if (ok) go(); }); } else { go(); }
                });
                footer.appendChild(applyBtn);
                body.appendChild(footer);
              }
            }});
          })
          .catch(function(err) {
            restore(); closeWork();
            if (cancelled || (err && err.name === 'AbortError')) return; // user cancelled — silent
            alert((a.label || 'Action') + ' failed: ' + (err && err.message || err));
          });
        return;
      }
      if (a.kind === 'history') {
        fetch(url, {credentials: 'same-origin'})
          .then(function(r){ return r.ok ? r.json() : []; })
          .then(function(items) {
            window.uiOpenSimpleModal({title: a.label, width: '560px', mount: function(body, dlg) {
              if (!items || !items.length) {
                body.appendChild(el('div', {class: 'ui-wb-hist-empty', text: 'No history yet.'}));
                return;
              }
              items.forEach(function(it) {
                var row = el('div', {class: 'ui-wb-hist-row'});
                row.appendChild(el('div', {class: 'ui-wb-hist-meta'}, [
                  el('span', {class: 'ui-wb-hist-note', text: it.note || '(change)'}),
                  el('span', {class: 'ui-wb-hist-at', text: it.at || ''}),
                ]));
                // Read a version without taking it. The usual reason to open
                // history is that one section went missing and you want it
                // back — restoring to find out what it said would throw away
                // everything written since.
                if (a.preview_url) {
                  var vb = el('button', {class: 'ui-wb-action-btn', text: 'View'});
                  vb.addEventListener('click', function() {
                    var purl = a.preview_url.replace('{id}', encodeURIComponent(selectedId)).replace('{rev}', encodeURIComponent(it.id));
                    vb.disabled = true; vb.textContent = 'Opening…';
                    fetchJSON(purl)
                      .then(function(res) {
                        vb.disabled = false; vb.textContent = 'View';
                        window.uiOpenSimpleModal({title: (res && res.title) || it.note || 'Earlier version', width: '900px', mount: function(pbody) {
                          var wrap = el('div', {class: 'ui-wb-hist-preview'});
                          // Server-built and trusted, same posture as the
                          // viewer's own body_is_html.
                          if (res && res.html) { wrap.innerHTML = res.html; }
                          else { uiRenderMarkdown(wrap, (res && res.markdown) || '_This version recorded nothing._'); }
                          pbody.appendChild(wrap);
                        }});
                      })
                      .catch(function(err) {
                        vb.disabled = false; vb.textContent = 'View';
                        alert('Could not open that version: ' + (err && err.message || err));
                      });
                  });
                  row.appendChild(vb);
                }
                var rb = el('button', {class: 'ui-wb-action-btn', text: 'Restore'});
                rb.addEventListener('click', function() {
                  window.uiConfirm('Restore this version? The current state is saved to history first, so this is undoable.').then(function(ok) {
                    if (!ok) return;
                    var rurl = (a.restore_url || '').replace('{id}', encodeURIComponent(selectedId)).replace('{rev}', encodeURIComponent(it.id));
                    rb.disabled = true; rb.textContent = 'Restoring…';
                    fetch(rurl, {method: 'POST', credentials: 'same-origin'})
                      .then(function(r){ if (!r.ok) throw new Error('HTTP ' + r.status); })
                      .then(function() { try { dlg.close(); dlg.remove(); } catch(e){} loadList(); loadViewer(selectedId); })
                      .catch(function(err) { rb.disabled = false; rb.textContent = 'Restore'; alert('Restore failed: ' + (err && err.message || err)); });
                  });
                });
                row.appendChild(rb);
                body.appendChild(row);
              });
            }});
          });
        return;
      }
    }

    function loadViewer(id) {
      selectedId = id;
      highlight();
      setActionsEnabled(true);
      // Tell the server which document is open, so the chat agent's co-author
      // tool writes into THIS record.
      if (cfg.active_url) {
        try {
          fetch(cfg.active_url, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id: id}),
          });
        } catch (e) {}
      }
      if (!cfg.record_url) return;
      var url = cfg.record_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        if (selectedId !== id) return; // a newer click won
        viewerBody.innerHTML = '';
        if (cfg.viewer_title_field && rec[cfg.viewer_title_field]) {
          viewerBody.appendChild(el('h2', {class: 'ui-wb-viewer-title', text: rec[cfg.viewer_title_field]}));
        }
        var bodyVal = (rec[bodyField] || '').trim();
        if (bodyVal) {
          var md = el('div', {class: 'ui-wb-md'});
          viewerBody.appendChild(md);
          // body_is_html: trusted server-rendered document HTML (ToC + sections);
          // otherwise render the field as markdown.
          if (cfg.body_is_html) { md.innerHTML = bodyVal; }
          else { uiRenderMarkdown(md, bodyVal); }
        } else {
          // Empty doc — guide the user to the ACTUAL commit path (the chat reply's
          // "Add to document" button), not "ask the assistant" (which led people to
          // a separate agent tool that writes elsewhere).
          var hint = el('div', {class: 'ui-wb-md-empty'});
          hint.appendChild(el('div', {text: 'This is empty.'}));
          hint.appendChild(el('div', {text: 'Ask the assistant on the right for a section, then click "Add to document" under its reply to drop it in here.'}));
          viewerBody.appendChild(hint);
        }
      }).catch(function() {});
    }

    // Delete endpoint: explicit delete_url, else DELETE the same record endpoint
    // the viewer reads (record_url). Falling back to record_url means EXISTING
    // workbench specs (authored before delete_url existed) still get the affordance
    // from live JS — no recreate needed.
    var delURL = cfg.delete_url || cfg.record_url || '';
    function deleteItem(id, label) {
      if (!delURL) return;
      window.uiConfirm('Delete "' + (label || 'this item') + '"?').then(function(ok) {
        if (!ok) return;
        var url = delURL.replace('{id}', encodeURIComponent(id));
        fetch(url, {method: 'DELETE'}).then(function() {
          if (selectedId === id) { selectedId = null; showEmpty(); }
          loadList();
        }).catch(function() {});
      });
    }

    function loadList() {
      fetchJSON(cfg.list_url).then(function(items) {
        listBody.innerHTML = '';
        if (!items || !items.length) {
          listBody.appendChild(el('div', {class: 'ui-wb-list-empty', text: cfg.list_empty || 'No items yet.'}));
          return;
        }
        items.forEach(function(it) {
          var id = String(it[itemKey] != null ? it[itemKey] : '');
          var label = it[itemLabel] || '(untitled)';
          var row = el('div', {class: 'ui-wb-item', 'data-id': id});
          row.appendChild(el('span', {class: 'ui-wb-item-label', text: label}));
          row.addEventListener('click', function() {
            loadViewer(id);
            drawer.mobileTitle.textContent = label;
            drawer.closeDrawer();
          });
          if (delURL) {
            var del = el('button', {class: 'ui-wb-item-del', title: 'Delete', text: '×'});
            del.addEventListener('click', function(ev) { ev.stopPropagation(); deleteItem(id, label); });
            row.appendChild(del);
          }
          listBody.appendChild(row);
        });
        highlight();
      }).catch(function() {});
    }

    // Co-author: each assistant reply gets an "Add to <noun>" button that appends
    // that reply's markdown to the OPEN record's body field and saves it (an
    // upsert), then invalidates so the viewer shows the new section. One global
    // decorator — there is one workbench per page, and it captures selectedId
    // live. No-op until a record is selected.
    // Default-on when the pieces exist (record_url to read/write + a chat),
    // opt out with coauthor:false. Gating on EXISTING fields means workbench
    // specs authored before the coauthor flag still get the affordance live.
    // Not applicable when the body is server-rendered HTML (nothing to append
    // markdown to — the agent edits via its own tools in that case).
    var coauthorOn = (cfg.coauthor !== false) && !!cfg.record_url && !!cfg.chat && !cfg.body_is_html;
    if (coauthorOn && window.uiRegisterMessageDecorator) {
      window.uiRegisterMessageDecorator(function(msg) {
        if (!msg || msg.role !== 'assistant' || !msg.wrap) return;
        var raw = (msg.rawText || '').trim();
        if (!raw) return;
        var btn = el('button', {class: 'ui-wb-coauthor-btn', text: cfg.coauthor_verb || 'Add to document'});
        btn.addEventListener('click', function() {
          if (!selectedId) { alert('Select an item on the left first, then add this to it.'); return; }
          if (!cfg.record_url) return;
          btn.disabled = true; btn.textContent = 'Adding…';
          var getURL = cfg.record_url.replace('{id}', encodeURIComponent(selectedId));
          fetchJSON(getURL).then(function(rec) {
            rec[bodyField] = ((rec[bodyField] || '').trim() + '\n\n' + raw).trim();
            return fetch(cfg.save_url || cfg.list_url, {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body: JSON.stringify(rec),
            });
          }).then(function() {
            btn.textContent = 'Added ✓';
            if (window.uiInvalidate) window.uiInvalidate(cfg.list_url);
          }).catch(function() {
            btn.disabled = false; btn.textContent = cfg.coauthor_verb || 'Add to document';
          });
        });
        msg.wrap.appendChild(btn);
      });
    }

    // A create (the New modal's form posts to list_url + Invalidates it) or a
    // co-author write fires ui-data-changed; refresh the list, and re-fetch the
    // open record so an appended section appears without a manual reload.
    window.addEventListener('ui-data-changed', function(e) {
      var srcs = (e.detail && e.detail.sources) || [];
      var hitList = srcs.indexOf(cfg.list_url) >= 0;
      var hitRec = (cfg.refresh_on || []).some(function(s) { return srcs.indexOf(s) >= 0; });
      if (hitList) loadList();
      if ((hitList || hitRec) && selectedId) loadViewer(selectedId);
    });

    // The embedded chat fires this when a round completes; the agent's co-author
    // tool may have appended a section to the open record, so re-fetch it.
    window.addEventListener('ui-chat-round-done', function() {
      if (selectedId) loadViewer(selectedId);
    });

    showEmpty();
    loadList();
    return root;
  };

  // upload_panel — pick or drop files, upload each with its own progress bar,
  // retry the one that failed, then optionally finalize and poll a status
  // endpoint until server-side processing finishes. See ui.UploadPanel.
  //
  // XMLHttpRequest, not fetch: fetch gives no upload progress events, so a
  // large POST behind it is indistinguishable from a hang. One request per
  // file, so a failure is isolated to the file that failed.
  components.upload_panel = function(cfg) {
    var field    = cfg.field || 'file';
    var pollMs   = (cfg.poll_seconds || 3) * 1000;
    var multiple = cfg.multiple !== false;
    var items    = [];   // {file, row, bar, status, state}
    var started  = false;

    var root  = el('div', {class: 'ui-upload'});
    var drop  = el('div', {class: 'ui-upload-drop'}, ['Drop files here, or ']);
    var input = el('input', {type: 'file', style: 'display:none'});
    if (cfg.accept) input.accept = cfg.accept;
    if (multiple) input.multiple = true;
    var pick = el('button', {type: 'button', class: 'ui-row-btn'}, ['choose files']);
    pick.addEventListener('click', function(){ input.click(); });
    drop.appendChild(pick);
    drop.appendChild(input);

    var list   = el('div', {class: 'ui-upload-list'});
    var status = el('div', {class: 'ui-upload-status'});
    var goBtn  = el('button', {type: 'button', class: 'ui-row-btn primary'}, [cfg.button_label || 'Upload']);
    goBtn.disabled = true;

    root.appendChild(drop);
    if (cfg.note) root.appendChild(el('div', {class: 'ui-form-hint'}, [cfg.note]));
    root.appendChild(list);
    root.appendChild(goBtn);
    root.appendChild(status);

    function human(n) {
      if (n < 1024) return n + ' B';
      var u = ['KB','MB','GB','TB'], i = -1;
      do { n = n / 1024; i++; } while (n >= 1024 && i < u.length - 1);
      return n.toFixed(1) + ' ' + u[i];
    }

    function addFiles(files) {
      for (var i = 0; i < files.length; i++) {
        (function(f) {
          if (cfg.max_bytes && f.size > cfg.max_bytes) {
            // Refused here rather than after transferring it: the server
            // would reject it too, but only once the bytes had crossed the
            // wire, which for a file this size is the whole cost.
            status.textContent = f.name + ' is ' + human(f.size) + ', over the ' + human(cfg.max_bytes) + ' limit.';
            return;
          }
          var bar   = el('div', {class: 'ui-upload-bar-fill'});
          var state = el('span', {class: 'ui-upload-state'}, ['queued']);
          var row = el('div', {class: 'ui-upload-row'}, [
            el('div', {class: 'ui-upload-name'}, [f.name + '  (' + human(f.size) + ')']),
            el('div', {class: 'ui-upload-bar'}, [bar]),
            state,
          ]);
          list.appendChild(row);
          items.push({file: f, row: row, bar: bar, state: state, done: false});
        })(files[i]);
      }
      goBtn.disabled = items.length === 0 || started;
    }

    input.addEventListener('change', function(){ addFiles(input.files); input.value = ''; });
    drop.addEventListener('dragover', function(e) {
      e.preventDefault();
      drop.classList.add('over');
    });
    drop.addEventListener('dragleave', function(){ drop.classList.remove('over'); });
    drop.addEventListener('drop', function(e) {
      e.preventDefault();
      drop.classList.remove('over');
      if (e.dataTransfer && e.dataTransfer.files) addFiles(e.dataTransfer.files);
    });

    // uploadOne resolves true on success, false on failure. It never rejects:
    // the caller stops the batch on a false, and a rejected promise here would
    // just be an error nobody rendered.
    function uploadOne(item, isFirst) {
      return new Promise(function(resolve) {
        var url = cfg.url;
        if (isFirst && cfg.reset_param) {
          url += (url.indexOf('?') >= 0 ? '&' : '?') + encodeURIComponent(cfg.reset_param) + '=1';
        }
        var fd = new FormData();
        fd.append(field, item.file, item.file.name);
        var xhr = new XMLHttpRequest();
        xhr.open('POST', url, true);
        xhr.upload.addEventListener('progress', function(e) {
          if (!e.lengthComputable) return;
          var pct = Math.round(e.loaded * 100 / e.total);
          item.bar.style.width = pct + '%';
          item.state.textContent = pct + '%';
        });
        xhr.addEventListener('load', function() {
          if (xhr.status >= 200 && xhr.status < 300) {
            item.bar.style.width = '100%';
            item.state.textContent = 'staged';
            item.done = true;
            resolve(true);
            return;
          }
          item.state.textContent = 'failed';
          item.row.classList.add('failed');
          status.textContent = item.file.name + ': ' + (xhr.responseText || ('HTTP ' + xhr.status));
          addRetry(item);
          resolve(false);
        });
        xhr.addEventListener('error', function() {
          item.state.textContent = 'failed';
          item.row.classList.add('failed');
          status.textContent = item.file.name + ': the connection dropped.';
          addRetry(item);
          resolve(false);
        });
        xhr.send(fd);
      });
    }

    // addRetry puts a per-file retry button on the failed row. Retrying ONE
    // file is the point of uploading them separately — a batch that fails on
    // its last file should not re-send the ones that already landed.
    function addRetry(item) {
      if (item.retry) return;
      item.retry = el('button', {type: 'button', class: 'ui-row-btn'}, ['Retry']);
      item.retry.addEventListener('click', function() {
        item.retry.disabled = true;
        item.row.classList.remove('failed');
        item.state.textContent = 'queued';
        item.bar.style.width = '0%';
        uploadOne(item, false).then(function(ok) {
          if (item.retry) { item.retry.remove(); item.retry = null; }
          if (ok) runRemaining();
          else if (item.retry) item.retry.disabled = false;
        });
      });
      item.row.appendChild(item.retry);
    }

    // runRemaining uploads every not-yet-done file in order, then finalizes.
    function runRemaining() {
      var pending = items.filter(function(i){ return !i.done; });
      var anyDone = items.some(function(i){ return i.done; });
      var chain = Promise.resolve(true);
      pending.forEach(function(item, idx) {
        chain = chain.then(function(ok) {
          if (!ok) return false;
          return uploadOne(item, idx === 0 && !anyDone);
        });
      });
      return chain.then(function(ok) {
        if (!ok) return;
        if (!cfg.finalize_url) { finish(); return; }
        status.textContent = 'Starting…';
        return fetch(cfg.finalize_url, {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(cfg.finalize_body || {}),
        }).then(function(r) {
          if (!r.ok) return r.text().then(function(t) { status.textContent = 'Failed to start: ' + t; });
          poll();
        });
      });
    }

    goBtn.addEventListener('click', function() {
      if (!items.length) return;
      started = true;
      goBtn.disabled = true;
      pick.disabled = true;
      status.textContent = 'Uploading…';
      runRemaining();
    });

    function labelFor(state) {
      if (cfg.status_labels && cfg.status_labels[state]) return cfg.status_labels[state];
      return state;
    }

    function poll() {
      if (!cfg.status_url) { finish(); return; }
      fetch(cfg.status_url)
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(rec) {
          if (!rec) { status.textContent = 'Lost track of the status — reload to see where it got to.'; return; }
          var state = String(rec[cfg.status_field || 'state'] || '');
          var failed = (cfg.status_failed || []).indexOf(state) >= 0;
          var done   = (cfg.status_done || []).indexOf(state) >= 0;
          if (failed) {
            var why = cfg.status_error_field ? rec[cfg.status_error_field] : '';
            status.textContent = why ? ('Failed: ' + why) : 'Failed.';
            pick.disabled = false;
            return;
          }
          if (done) { finish(); return; }
          status.textContent = labelFor(state) || 'Working…';
          setTimeout(poll, pollMs);
        })
        .catch(function() {
          // A transient fetch failure while polling is not a failed job —
          // keep watching rather than reporting an outcome we do not have.
          setTimeout(poll, pollMs);
        });
    }

    function finish() {
      status.textContent = 'Done.';
      if (cfg.reload_on_done) setTimeout(function(){ window.location.reload(); }, 600);
    }

    return root;
  };

  components.card = function(cfg) {
    var wrap = el('div', {class: 'ui-card'});
    // Re-execute any inline <script> tags. innerHTML doesn't run them
    // (per HTML5), so we manually clone each script into a fresh
    // element the browser will execute. Keep this for the escape-hatch
    // case where the Card's body needs to fetch + render data.
    function paint(html) {
      wrap.innerHTML = html || '';
      wrap.querySelectorAll('script').forEach(function(old) {
        var s = document.createElement('script');
        for (var i = 0; i < old.attributes.length; i++) {
          s.setAttribute(old.attributes[i].name, old.attributes[i].value);
        }
        s.text = old.textContent;
        old.parentNode.replaceChild(s, old);
      });
    }
    paint(cfg.html);

    if (!cfg.source) return wrap;

    // A sourced card is server-rendered content that goes stale. The
    // fetch takes the fragment as TEXT: an endpoint that already serves
    // a picture (an SVG, a rendered list) can be pointed at directly,
    // and {"html": "..."} is accepted for the ones that answer in JSON.
    function reload() {
      return fetch(cfg.source, {credentials: 'same-origin', cache: 'no-store'})
        .then(function(r) { return r.ok ? r.text() : null; })
        .then(function(body) {
          if (body === null) return;   // a failed refresh keeps the last good paint
          var html = body;
          if (body.charAt(0) === '{') {
            try { var d = JSON.parse(body); if (d && typeof d.html === 'string') html = d.html; }
            catch (_) {}
          }
          paint(html);
          // The card's content is new ELEMENTS, so anything a page hung
          // on the old ones — a "you are here" mark, a measured height —
          // has to go on again. Bubbles, so one listener on the page can
          // serve every card in it.
          wrap.dispatchEvent(new CustomEvent('ui-card-refreshed',
            {bubbles: true, detail: {source: cfg.source}}));
        })
        .catch(function() {});
    }
    if (!cfg.html) reload();

    // What changed is rarely the card's own source: a diagram of the
    // stages is redrawn when a STAGE is saved. Prefix match, because the
    // write a form broadcasts carries the record it wrote in its query.
    var watch = (cfg.refresh_on || []).concat([cfg.source]);
    var pending = null;
    window.addEventListener('ui-data-changed', function(ev) {
      var sources = (ev.detail && ev.detail.sources) || [];
      var hit = sources.some(function(src) {
        src = String(src);
        return watch.some(function(w) { return w && src.indexOf(w) === 0; });
      });
      if (!hit) return;
      // One redraw per burst: a panel fires a save per field group, and
      // three of those should not be three fetches of the same block.
      clearTimeout(pending);
      pending = setTimeout(reload, 250);
    });
    uiAutoRefresh(cfg.auto_refresh_ms, reload);
    return wrap;
  };

  // frame — a complete HTML document in its own iframe (srcdoc), so its
  // reset/body CSS styles ITSELF instead of the page hosting it and its
  // 100vh means the frame box. No sandbox: same origin as the page that
  // served it, so relative fetches ('data/<source>'), cookies and storage
  // work exactly as they do in an inlined card.
  components.frame = function(cfg) {
    var wrap = el('div', {class: 'ui-frame'});
    var f = document.createElement('iframe');
    f.style.cssText = 'display:block;width:100%;border:0;background:#fff;border-radius:8px;' +
      'height:' + (cfg.height || 'min(80vh, 860px)');
    f.setAttribute('allow', 'autoplay; fullscreen');
    f.setAttribute('srcdoc', cfg.html || '');
    // Keyboard-driven documents (games, editors) are inert until the frame
    // holds focus. Take it on load unless the user is typing somewhere.
    f.addEventListener('load', function() {
      var a = document.activeElement;
      if (a && (a.isContentEditable || /^(input|textarea|select)$/i.test(a.tagName || ''))) return;
      try { f.contentWindow.focus(); } catch (_) {}
    });
    wrap.appendChild(f);
    return wrap;
  };

  components.error = function(cfg) {
    return el('div', {class: 'ui-card', text: 'UI error: ' + (cfg.message || 'unknown')});
  };
