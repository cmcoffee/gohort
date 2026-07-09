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
    mountComponent({
      type:          'agent_loop_panel',
      send_url:      chatCfg.send_url   || 'chat/send',
      cancel_url:    chatCfg.cancel_url || 'chat/cancel',
      lock_activity: true,
      markdown:      true,
      empty_text:    chatCfg.empty_text || 'Ask the assistant to draft or add a section.',
      placeholder:   chatCfg.placeholder || 'Ask the assistant…',
    }, right);

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

  components.card = function(cfg) {
    var wrap = el('div', {class: 'ui-card'});
    wrap.innerHTML = cfg.html || '';
    // Re-execute any inline <script> tags. innerHTML doesn't run them
    // (per HTML5), so we manually clone each script into a fresh
    // element the browser will execute. Keep this for the escape-hatch
    // case where the Card's body needs to fetch + render data.
    wrap.querySelectorAll('script').forEach(function(old) {
      var s = document.createElement('script');
      for (var i = 0; i < old.attributes.length; i++) {
        s.setAttribute(old.attributes[i].name, old.attributes[i].value);
      }
      s.text = old.textContent;
      old.parentNode.replaceChild(s, old);
    });
    return wrap;
  };

  components.error = function(cfg) {
    return el('div', {class: 'ui-card', text: 'UI error: ' + (cfg.message || 'unknown')});
  };
