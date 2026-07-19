  components.article_editor = function(cfg) {
    var idF      = cfg.id_field      || 'ID';
    var subjectF = cfg.subject_field || 'Subject';
    var bodyF    = cfg.body_field    || 'Body';
    var dateF    = cfg.date_field    || 'Date';
    var imageF   = cfg.image_field   || 'ImageURL';

    var wrap = el('div', {class: 'ui-tw'});

    // --- Sidebar (articles list) ---
    var side = el('div', {class: 'ui-tw-side'});
    // Collapse state — desktop only. Mobile uses the slide-in drawer
    // mechanism inherited from ChatPanel and ignores this state.
    var sideCollapsed = false;
    try { sideCollapsed = localStorage.getItem('tw.sideCollapsed') === '1'; } catch(e) {}
    var collapseBtn = el('button', {
      class: 'ui-tw-collapse', title: 'Hide articles list',
      onclick: function(){ toggleCollapse(); },
    }, ['‹']);
    var sideHdrBuilt = renderSideHeader({
      label:     cfg.list_label || 'Articles',
      className: 'ui-tw-side-h',
      newTitle:  'New article',
      noNew:     cfg.no_new,
      onNew:     function(){ openArticle(null); },
      onClose:   function(){ closeDrawer(); },
      leftExtras: [collapseBtn],
      // List-scoped actions (e.g. "Optimize all") live with the list, built via
      // the hoisted buildActionBtn so they dispatch like the toolbar's.
      rightExtras: (cfg.list_actions || []).map(buildActionBtn),
    });
    var sideHdr  = sideHdrBuilt.elt;
    var sideList = el('div', {class: 'ui-tw-side-list'}, ['Loading…']);
    var sideSearch = cfg.no_search ? null : makeSideSearch(sideList);
    side.appendChild(sideHdr);
    if (sideSearch) side.appendChild(sideSearch);
    side.appendChild(sideList);

    // Floating expand-tab shown when the sidebar is collapsed. Sits
    // pinned to the left edge of the main pane so the user can always
    // bring the list back without hunting for a menu.
    var expandTab = el('button', {
      class: 'ui-tw-expand', title: 'Show articles list',
      onclick: function(){ toggleCollapse(); },
    }, ['›']);

    function toggleCollapse() {
      sideCollapsed = !sideCollapsed;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      collapseBtn.title = sideCollapsed ? 'Show articles list' : 'Hide articles list';
      collapseBtn.textContent = sideCollapsed ? '›' : '‹';
      try { localStorage.setItem('tw.sideCollapsed', sideCollapsed ? '1' : '0'); } catch(e) {}
    }

    var drawer = makeDrawer(side, {
      title:          'New article',
      hamburgerTitle: cfg.list_label || 'Articles',
      newTitle:       'New article',
      onNew:          cfg.no_new ? null : function(){ openArticle(null); },
    });
    var mobileTitle    = drawer.mobileTitle;
    var drawerBackdrop = drawer.backdrop;

    // --- Main pane (editor + assistant) ---
    var main = el('div', {class: 'ui-tw-main'});
    main.appendChild(drawer.mobileHdr);

    var titleBar = el('div', {class: 'ui-tw-titlebar'});
    var titleInput = el('input', {type: 'text', class: 'ui-tw-title',
      placeholder: cfg.placeholder_title || 'Article title…'});
    // Fixed-name records edit the body, not the title.
    if (cfg.title_readonly) { titleInput.readOnly = true; titleInput.title = 'Name is fixed'; }
    var savedTag = el('span', {class: 'ui-tw-saved'}, []);

    // Declarative toolbar — apps populate cfg.actions with the
    // buttons they want. The runtime maps each entry to a generic
    // handler based on Method:
    //   "client"   → call into window.UIClientActions[<url>] with
    //                an editor handle. The app registered the
    //                handler from its own package (ExtraHeadHTML)
    //                — this is the supported path for any
    //                app-specific flow.
    //   "post"     → POST to URL with {id} substituted
    //   "open"     → window.open(URL, _blank)
    //   "redirect" → set window.location.href
    //   "builtin"  → legacy: invokes a hard-coded named flow that
    //                lives in this file. New code should use
    //                "client" instead; "builtin" is preserved for
    //                in-flight ports.
    //
    // editorAPI is the handle passed to client actions. Forward-
    // declared here because the closure variables it reads
    // (titleInput, bodyArea, currentID, …) are var-hoisted and
    // get their real values further down in the mount function.
    // At click time they're set; at the time we build editorAPI,
    // they're undefined but unreferenced. The methods read them
    // lazily so this works.
    var editorAPI = {
      getBody:  function()    { return bodyArea.value; },
      setBody:  function(s)   { bodyArea.value = s == null ? '' : String(s); },
      getTitle: function()    { return titleInput.value; },
      setTitle: function(s)   { titleInput.value = s == null ? '' : String(s); },
      getID:    function()    { return currentID; },
      getImage: function()    { return currentImageURL; },
      setImage: function(url) { showImage(url); },
      save:     function(extra) { saveArticle(extra); },
      toast:    function(msg) { showToast(msg); },
      busy:     function(btn, label) { setBtnBusy(btn, label); },
      restore:  function(btn) { restoreBtn(btn); },
      confirm:  function(msg) { return window.uiConfirm(msg); },
      appendAssistant: function(role, content) { asstAppend(role, content); },
      // Re-fetch and re-render the left list — e.g. after an app action changes a
      // row's summary or badge. Preserves the current editor content + selection.
      reloadList: function() { loadList(); },
    };

    var actionButtons = [];
    // Holdover dispatcher for the two slide-in panel flows that
    // still live in this file (rules, merge). Everything else is
    // a "client" action registered from the app's package via
    // window.uiRegisterClientAction. Rules and merge will move
    // out once a generic SlidePanel primitive exists.
    function builtinAction(name) {
      switch (name) {
        case 'rules': return toggleRules;
        case 'merge': return toggleMerge;
      }
      return null;
    }
    // buildActionBtn turns a declarative ToolbarAction into a wired button.
    // Shared by the editor toolbar (cfg.actions) and the sidebar list header
    // (cfg.list_actions) so both dispatch identically. Hoisted, so the header
    // (built earlier) can call it; editorAPI/currentID are read at click time.
    function buildActionBtn(action) {
      var classes = 'ui-row-btn';
      if (action.variant) classes += ' ' + action.variant;
      var btn = el('button', {class: classes, title: action.title || ''},
        [action.label || '(action)']);
      btn.addEventListener('click', async function() {
        if (action.confirm && !(await window.uiConfirm(action.confirm))) return;
        var method = action.method || 'post';
        if (method === 'client') {
          var name = action.url || '';
          var fn = window.UIClientActions && window.UIClientActions[name];
          if (typeof fn === 'function') {
            fn({editor: editorAPI, button: btn, action: action});
          } else {
            showToast('No handler for client action: ' + name);
          }
          return;
        }
        if (method === 'builtin') {
          var bfn = builtinAction(action.url || '');
          // Pass the clicked button so the handler can drive its own
          // busy/spinner state without a globally named button variable.
          if (bfn) bfn(btn);
          else showToast('Unknown built-in action: ' + (action.url || ''));
          return;
        }
        var url = (action.url || '').replace('{id}', encodeURIComponent(currentID || ''));
        if (method === 'open')          { window.open(url, '_blank', 'noopener'); }
        else if (method === 'redirect') { window.location.href = url; }
        else {
          fetchJSON(url, {method: 'POST'}).catch(function(err){
            showToast('Failed: ' + err.message);
          });
        }
      });
      return btn;
    }
    (cfg.actions || []).forEach(function(action) { actionButtons.push(buildActionBtn(action)); });

    // Less-frequent actions tucked under a "More" button so the
    // titlebar stays readable. The CONTENTS are driven entirely by
    // cfg.extra_actions — the framework just renders the popover and
    // wires generic POST / open / redirect / builtin handling. Apps
    // declare what they want from page.go; nothing here is
    // app-specific. The two built-in handlers (suggest_title,
    // generate_image) exist because their UX has side-effects beyond
    // a plain POST; new built-ins can be added the same way.
    var extras = (cfg.extra_actions || []).slice();
    var extrasBtn = null, extrasMenu = null;
    if (extras.length) {
      extrasBtn = el('button', {class: 'ui-row-btn', title: 'More actions'}, ['More ▾']);
      extrasMenu = el('div', {class: 'ui-tw-extras-menu', style: 'display:none'});
      extras.forEach(function(action) {
        var entry = el('button', {class: 'ui-tw-extras-item', title: action.title || ''},
          [action.label || '(action)']);
        entry.addEventListener('click', async function() {
          extrasMenu.style.display = 'none';
          if (action.confirm && !(await window.uiConfirm(action.confirm))) return;
          var method = action.method || 'post';
          if (method === 'client') {
            var name = action.url || '';
            var fn = window.UIClientActions && window.UIClientActions[name];
            if (typeof fn === 'function') {
              fn({editor: editorAPI, button: entry, action: action});
            } else {
              showToast('No handler for client action: ' + name);
            }
            return;
          }
          var url = (action.url || '').replace('{id}', encodeURIComponent(currentID || ''));
          if (method === 'open') {
            window.open(url, '_blank', 'noopener');
          } else if (method === 'redirect') {
            window.location.href = url;
          } else {
            // POST (default). No payload — the action URL itself
            // encodes whatever the server needs.
            fetchJSON(url, {method: 'POST'}).catch(function(err) {
              showToast('Failed: ' + err.message);
            });
          }
        });
        extrasMenu.appendChild(entry);
      });
      extrasBtn.addEventListener('click', function(ev) {
        ev.stopPropagation();
        extrasMenu.style.display = extrasMenu.style.display === 'none' ? 'block' : 'none';
      });
      document.addEventListener('click', function(ev) {
        if (extrasMenu.style.display === 'none') return;
        if (extrasMenu.contains(ev.target) || extrasBtn.contains(ev.target)) return;
        extrasMenu.style.display = 'none';
      });
    }
    // Inline revision navigation — back/forward arrows + indicator +
    // "Make current" button instead of a slide-in panel. Hidden when
    // there's only one revision (or the article hasn't been saved yet).
    var revBackBtn = cfg.revisions_list_url ? el('button', {
      class: 'ui-row-btn compact', title: 'Previous revision',
      onclick: function(){ navigateRevision(-1); },
    }, ['◀']) : null;
    var revFwdBtn = cfg.revisions_list_url ? el('button', {
      class: 'ui-row-btn compact', title: 'Next revision',
      onclick: function(){ navigateRevision(1); },
    }, ['▶']) : null;
    var revIndicator = cfg.revisions_list_url ? el('span', {class: 'ui-tw-rev-indicator'}, []) : null;
    var revMakeCurrentBtn = cfg.revisions_list_url ? el('button', {
      class: 'ui-row-btn', title: 'Save the displayed revision as the latest version',
      onclick: function(){ saveArticle(); },
    }, ['Make current']) : null;
    if (revMakeCurrentBtn) revMakeCurrentBtn.style.display = 'none';
    // The titlebar-level Delete button used to live here. Removed —
    // delete now happens per-article via the × button on each sidebar
    // row (matches codewriter's pattern). Saves toolbar real estate
    // and removes a destructive control from a high-traffic toolbar.
    var saveBtn = el('button', {class: 'ui-row-btn primary', onclick: function(){ saveArticle(); }}, ['Save']);

    // Group the revision controls inside a bordered span so they read
    // as a single visual unit — matches the codewriter toolbar's
    // .ui-cw-rev-group treatment. Hidden when no article is open or
    // there's only one revision.
    var revGroup = null;
    if (revBackBtn || revFwdBtn || revIndicator || revMakeCurrentBtn) {
      var revKids = [];
      if (revMakeCurrentBtn) revKids.push(revMakeCurrentBtn);
      if (revBackBtn)        revKids.push(revBackBtn);
      if (revIndicator)      revKids.push(revIndicator);
      if (revFwdBtn)         revKids.push(revFwdBtn);
      revGroup = el('span', {class: 'ui-cw-rev-group', style: 'display:none'}, revKids);
    }

    titleBar.appendChild(titleInput);
    titleBar.appendChild(savedTag);
    // Revision group sits as the leftmost button cluster on the
    // titlebar (immediately after the title input and saved tag) —
    // matches codewriter where rev navigation is the first button
    // group after the name input.
    if (revGroup) titleBar.appendChild(revGroup);
    actionButtons.forEach(function(btn){ titleBar.appendChild(btn); });
    if (extrasBtn) {
      var extrasWrap = el('span', {class: 'ui-tw-extras-wrap'}, [extrasBtn, extrasMenu]);
      titleBar.appendChild(extrasWrap);
    }
    titleBar.appendChild(saveBtn);
    main.appendChild(titleBar);

    // Rules slide-in panel — keeps the slide-in pattern for editing
    // the rules text block; revisions are now inline arrows.
    var rulesPanel = el('div', {class: 'ui-tw-revs'});
    rulesPanel.style.display = 'none';
    main.appendChild(rulesPanel);

    // Merge slide-in panel — picks a saved merge source (or pastes
    // content) and combines it with the current article.
    var mergePanel = el('div', {class: 'ui-tw-revs ui-tw-merge-panel'});
    mergePanel.style.display = 'none';
    main.appendChild(mergePanel);

    // Optional image preview row (hidden until a generated image arrives).
    var imageRow = el('div', {class: 'ui-tw-image-row'});
    imageRow.style.display = 'none';
    main.appendChild(imageRow);

    // Revisions slide-in panel. Anchored over the editor; toggleable.
    var revsPanel = el('div', {class: 'ui-tw-revs'});
    revsPanel.style.display = 'none';
    main.appendChild(revsPanel);

    var bodyArea = el('textarea', {class: 'ui-tw-body',
      placeholder: cfg.placeholder_body || 'Article body in markdown…'});
    main.appendChild(bodyArea);

    // --- Assistant chat (below the editor) ---
    // Drag handle between the editor body and the assistant pane.
    // Drag up to expand the assistant; drag down to shrink. Saved
    // height persists per-user via localStorage so the layout sticks.
    var asstResizer = el('div', {class: 'ui-tw-resizer', title: 'Drag to resize the assistant pane'});
    var asstWrap = el('div', {class: 'ui-tw-asst'});
    var asstThread = el('div', {class: 'ui-tw-asst-thread'},
      [el('div', {class: 'ui-tw-asst-empty'}, ['Ask the assistant to discuss or rewrite this article.'])]);
    var asstInputRow = el('div', {class: 'ui-tw-asst-input-row'});
    var modeBtn = el('button', {class: 'ui-chat-mode active', title: 'Edit mode — assistant may rewrite the article',
      onclick: function() {
        chatMode = (chatMode === 'edit') ? 'chat' : 'edit';
        modeBtn.textContent = chatMode === 'edit' ? 'Edit' : 'Chat';
        modeBtn.classList.toggle('active', chatMode === 'edit');
        modeBtn.title = chatMode === 'edit'
          ? 'Edit mode — assistant may rewrite the article'
          : 'Chat mode — discussion only, never touches the article';
      }}, ['Edit']);
    var asstInput = el('textarea', {class: 'ui-chat-input', rows: '1',
      placeholder: 'Ask the assistant…'});
    var asstSend  = el('button', {class: 'ui-chat-send', onclick: function(){ doAssist(); }}, ['Send']);

    // Generic reference picker — when the app wires reference_sources_url,
    // surface every registered reference source in one dropdown. The chosen
    // item rides along with each request as references; the app's handler
    // injects that source's text into the model's context. Domain-agnostic:
    // core/ui knows nothing about what a source contains — only the shape.
    var selectedRef = null;
    var refSelect = null;
    if (cfg.reference_sources_url) {
      refSelect = el('select', {class: 'ui-chat-mode', title: 'Ground replies in material gathered by another service',
        onchange: function() {
          var o = refSelect.options[refSelect.selectedIndex];
          selectedRef = (o && o.value) ? {kind: o.getAttribute('data-kind'), item_id: o.value} : null;
        }});
      refSelect.appendChild(el('option', {value: ''}, ['Reference…']));
      fetch(cfg.reference_sources_url).then(function(r){ return r.json(); }).then(function(groups) {
        if (!groups || !groups.length) { refSelect.style.display = 'none'; return; }
        groups.forEach(function(g) {
          var og = el('optgroup', {label: g.label});
          (g.items || []).forEach(function(it) {
            og.appendChild(el('option', {value: it.id, 'data-kind': g.kind, title: it.desc || ''}, [it.name]));
          });
          refSelect.appendChild(og);
        });
      }).catch(function() { if (refSelect) refSelect.style.display = 'none'; });
    }

    asstInputRow.appendChild(modeBtn);
    if (refSelect) asstInputRow.appendChild(refSelect);
    asstInputRow.appendChild(asstInput);
    asstInputRow.appendChild(asstSend);
    asstWrap.appendChild(asstThread);
    asstWrap.appendChild(asstInputRow);
    main.appendChild(asstResizer);
    main.appendChild(asstWrap);

    // Restore saved height (if any). Override the default 35% cap so
    // the saved value sticks even when it's larger than 35%.
    try {
      var savedH = parseInt(localStorage.getItem('tw.asst.height') || '0', 10);
      if (savedH > 80) {
        asstWrap.style.height = savedH + 'px';
        asstWrap.style.maxHeight = 'none';
      }
    } catch(e) {}

    // Drag-to-resize. Tracking moves the boundary between editor
    // body (above) and the assistant pane (below). Clamp to
    // [80px, 80% of viewport] so the user can't accidentally
    // disappear either pane.
    asstResizer.addEventListener('mousedown', function(ev) {
      ev.preventDefault();
      var startY = ev.clientY;
      var startH = asstWrap.offsetHeight;
      document.body.style.cursor = 'ns-resize';
      document.body.style.userSelect = 'none';
      function move(e) {
        var dy = startY - e.clientY;
        var maxH = Math.floor(window.innerHeight * 0.8);
        var newH = Math.max(80, Math.min(maxH, startH + dy));
        asstWrap.style.height = newH + 'px';
        asstWrap.style.maxHeight = 'none';
      }
      function up() {
        document.removeEventListener('mousemove', move);
        document.removeEventListener('mouseup', up);
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        try { localStorage.setItem('tw.asst.height', asstWrap.offsetHeight); } catch(e) {}
      }
      document.addEventListener('mousemove', move);
      document.addEventListener('mouseup', up);
    });
    // Touch parity for mobile / iPad — same drag behavior.
    asstResizer.addEventListener('touchstart', function(ev) {
      var t = ev.touches[0]; if (!t) return;
      var startY = t.clientY;
      var startH = asstWrap.offsetHeight;
      function move(e) {
        var t2 = e.touches[0]; if (!t2) return;
        var dy = startY - t2.clientY;
        var maxH = Math.floor(window.innerHeight * 0.8);
        var newH = Math.max(80, Math.min(maxH, startH + dy));
        asstWrap.style.height = newH + 'px';
        asstWrap.style.maxHeight = 'none';
        e.preventDefault();
      }
      function up() {
        document.removeEventListener('touchmove', move);
        document.removeEventListener('touchend', up);
        try { localStorage.setItem('tw.asst.height', asstWrap.offsetHeight); } catch(e) {}
      }
      document.addEventListener('touchmove', move, {passive: false});
      document.addEventListener('touchend', up);
    });

    wrap.appendChild(side);
    wrap.appendChild(main);
    wrap.appendChild(expandTab);
    wrap.appendChild(drawerBackdrop);
    if (sideCollapsed) {
      wrap.classList.add('side-collapsed');
      collapseBtn.title = 'Show articles list';
      collapseBtn.textContent = '›';
    }

    // --- State ---
    var currentID = null;
    var currentImageURL = '';
    var lastSavedSubject = '';
    var lastSavedBody = '';
    var chatHistory = [];
    var chatMode = 'edit'; // 'edit' or 'chat'
    var asstSending = false;
    // Revision navigation state — populated by reloadRevisions() after
    // every successful save. revisionIndex points at the entry in
    // revisions[] currently displayed in the editor; the Make-current
    // button is visible when we're not at the latest entry.
    var revisions = [];
    var revisionIndex = -1;

    var openDrawer  = drawer.openDrawer;
    var closeDrawer = drawer.closeDrawer;

    // --- Sidebar list ---
    var bulkSelected = {}; // article id -> true
    var bulkState    = {mode: false};
    function loadList() {
      fetchJSON(cfg.list_url).then(function(items) {
        sideList.innerHTML = '';
        items = items || [];
        items.sort(function(a, b){ return String(b[dateF] || '').localeCompare(String(a[dateF] || '')); });
        var ids = {}; items.forEach(function(it){ ids[it[idF]] = true; });
        Object.keys(bulkSelected).forEach(function(k){ if (!ids[k]) delete bulkSelected[k]; });
        if (!items.length) {
          if (cfg.bulk_select && bulkState.mode) {
            renderBulkBar([], sideList, bulkState, bulkSelected,
              function(it){ return it[idF]; }, loadList, function(){});
          }
          sideList.appendChild(el('div', {class: 'ui-chat-empty', style: 'padding:0.5rem;text-align:left'}, ['No articles yet.']));
          return;
        }
        if (cfg.bulk_select) {
          renderBulkBar(items, sideList, bulkState, bulkSelected,
            function(it){ return it[idF]; },
            loadList,
            async function() {
              var keys = Object.keys(bulkSelected);
              if (!keys.length) return;
              if (!(await window.uiConfirm('Delete ' + keys.length + ' article(s) permanently?'))) return;
              Promise.all(keys.map(function(id) {
                var url = cfg.delete_url.replace('{id}', encodeURIComponent(id));
                return fetchJSON(url, {method: 'DELETE'}).catch(function(){});
              })).then(function() {
                if (bulkSelected[currentID]) openArticle(null);
                bulkSelected = {};
                bulkState.mode = false;
                loadList();
              });
            });
        }
        items.forEach(function(it) {
          var inMode = cfg.bulk_select && bulkState.mode;
          var selected = !!bulkSelected[it[idF]];
          var fullSubject = it[subjectF] || '(untitled)';
          var rowMeta = relTime(it[dateF]);
          var row = el('div', {
            class:
              'ui-chat-side-item' +
              (it[idF] === currentID ? ' active' : '') +
              (inMode ? ' selectable' : '') +
              (selected ? ' selected' : ''),
            // Hover tooltip with full title + date so the row can be
            // shorter without losing information when the title
            // ellipsizes at the narrower sidebar width.
            title: fullSubject + ' — ' + rowMeta,
          }, [
            el('div', {class: 'ui-chat-side-text'}, [
              el('div', {class: 'ui-chat-side-title'}, [fullSubject]),
            ]),
          ]);
          // Per-row delete button (× in the right corner) — matches
          // codewriter. Bulk-select mode hides it; checkboxes drive
          // multi-delete instead. The titlebar Delete button is kept
          // off the toolbar entirely since this gives one-click access
          // per article.
          if (cfg.delete_url && !inMode) {
            row.appendChild(el('button', {
              class: 'ui-chat-side-del', title: 'Delete article',
              onclick: async function(ev) {
                ev.stopPropagation();
                if (!(await window.uiConfirm('Delete "' + fullSubject + '" permanently?'))) return;
                var url = cfg.delete_url.replace('{id}', encodeURIComponent(it[idF]));
                fetchJSON(url, {method: 'DELETE'}).then(function() {
                  if (currentID === it[idF]) openArticle(null);
                  loadList();
                  showToast('Deleted');
                }).catch(function(err){ showToast('Delete failed: ' + err.message); });
              },
            }, ['×']));
          }
          row.addEventListener('click', function(ev) {
            // Per-row × delete is a button child — let its own click
            // handler fire and short-circuit the row open.
            if (ev.target.classList.contains('ui-chat-side-del')) return;
            if (inMode) {
              if (bulkSelected[it[idF]]) delete bulkSelected[it[idF]];
              else bulkSelected[it[idF]] = true;
              loadList();
            } else {
              openArticle(it[idF]); closeDrawer();
            }
          });
          sideList.appendChild(row);
        });
      }).catch(function(err){ sideList.textContent = 'Failed: ' + err.message; });
    }

    function openArticle(id) {
      currentID = id;
      currentImageURL = '';
      asstThread.innerHTML = '';
      asstThread.appendChild(el('div', {class: 'ui-tw-asst-empty'},
        [id ? 'Ask the assistant to discuss or rewrite this article.' : 'Start typing your article — the assistant can help once you have something to work with.']));
      chatHistory = [];
      hideImage();
      if (!id) {
        titleInput.value = '';
        bodyArea.value = '';
        lastSavedSubject = '';
        lastSavedBody = '';
        savedTag.textContent = '';
        mobileTitle.textContent = 'New article';
        revisions = []; revisionIndex = -1;
        updateRevNav();
        loadList();
        return;
      }
      var url = cfg.load_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        titleInput.value = rec[subjectF] || '';
        bodyArea.value   = rec[bodyF]    || '';
        lastSavedSubject = titleInput.value;
        lastSavedBody    = bodyArea.value;
        savedTag.textContent = 'saved ' + relTime(rec[dateF]);
        mobileTitle.textContent = rec[subjectF] || 'Untitled';
        if (rec[imageF]) showImage(rec[imageF]);
        loadList();
        reloadRevisions();
      }).catch(function(err){ showToast('Load failed: ' + err.message); });
    }

    function saveArticle(extra) {
      var subject = titleInput.value.trim();
      var body    = bodyArea.value;
      if (!subject && !body) { showToast('Nothing to save'); return; }
      // Server-side accepts lowercase keys; encoding/json case-folds
      // for the inbound decode. Image URL is the new persisted field.
      var record = {
        id:        currentID || '',
        subject:   subject,
        body:      body,
        image_url: currentImageURL || '',
      };
      // Optional caller-supplied fields (e.g. a client action tagging the save
      // so the server can record HOW it happened). Generic — the editor doesn't
      // interpret them.
      if (extra && typeof extra === 'object') {
        for (var ek in extra) record[ek] = extra[ek];
      }
      saveBtn.disabled = true;
      savedTag.textContent = 'saving…';
      fetchJSON(cfg.save_url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(record),
      }).then(function(saved) {
        saveBtn.disabled = false;
        if (saved && saved[idF]) currentID = saved[idF];
        lastSavedSubject = subject;
        lastSavedBody    = body;
        savedTag.textContent = 'saved just now';
        mobileTitle.textContent = subject || 'Untitled';
        loadList();
        reloadRevisions();
      }).catch(function(err) {
        saveBtn.disabled = false;
        savedTag.textContent = '';
        showToast('Save failed: ' + err.message);
      });
    }

    // Image preview + persistence helpers.
    function showImage(url) {
      currentImageURL = url || '';
      if (!url) { hideImage(); return; }
      imageRow.innerHTML = '';
      imageRow.style.display = '';
      imageRow.appendChild(el('img', {src: url, class: 'ui-tw-image'}));
      imageRow.appendChild(el('div', {class: 'ui-tw-image-actions'}, [
        el('button', {class: 'ui-row-btn', onclick: async function() {
          if (!(await window.uiConfirm('Remove the header image from this article?'))) return;
          hideImage();
          showToast('Image removed — Save to persist');
        }}, ['Remove']),
      ]));
    }
    function hideImage() {
      currentImageURL = '';
      imageRow.style.display = 'none';
      imageRow.innerHTML = '';
    }

    // Revision navigation — back/forward arrows + "Make current".
    function reloadRevisions() {
      if (!cfg.revisions_list_url || !currentID) {
        revisions = []; revisionIndex = -1; updateRevNav();
        return;
      }
      var url = cfg.revisions_list_url.replace('{id}', encodeURIComponent(currentID));
      fetchJSON(url).then(function(items) {
        revisions = items || [];
        revisions.sort(function(a, b){ return String(a.date || '').localeCompare(String(b.date || '')); });
        revisionIndex = revisions.length - 1;
        updateRevNav();
      }).catch(function() { revisions = []; revisionIndex = -1; updateRevNav(); });
    }
    function updateRevNav() {
      if (!revBackBtn) return;
      var n = revisions.length;
      revBackBtn.disabled = revisionIndex <= 0;
      revFwdBtn.disabled = revisionIndex >= n - 1;
      var curRev = (revisionIndex >= 0) ? revisions[revisionIndex] : null;
      revIndicator.textContent = n > 0
        ? 'rev ' + (revisionIndex + 1) + '/' + n + (curRev && curRev.label ? ' · ' + curRev.label : '')
        : '';
      revMakeCurrentBtn.style.display = (n > 0 && revisionIndex < n - 1) ? 'inline-flex' : 'none';
      // The whole rev group is hidden until there are revisions to
      // navigate. Use explicit inline-flex / none so the value beats
      // the bordered-span CSS rules that don't carry display:none.
      if (revGroup) revGroup.style.display = n > 0 ? 'inline-flex' : 'none';
    }
    function navigateRevision(dir) {
      var idx = revisionIndex + dir;
      if (idx < 0 || idx >= revisions.length) return;
      if (!cfg.revision_load_url) { showToast('Revision load not configured'); return; }
      var url = cfg.revision_load_url.replace('{revid}', encodeURIComponent(revisions[idx].id));
      fetchJSON(url).then(function(rev) {
        bodyArea.value = rev.body || rev[bodyF] || '';
        if (rev.subject || rev[subjectF]) titleInput.value = rev.subject || rev[subjectF];
        revisionIndex = idx;
        updateRevNav();
      }).catch(function(err){ showToast('Load failed: ' + err.message); });
    }

    // --- Assistant ---
    function doAssist() {
      if (asstSending) return;
      var msg = asstInput.value.trim();
      if (!msg) return;
      asstInput.value = '';
      autoresizeAsst();
      asstSending = true;
      asstSend.disabled = true;
      // Push user msg.
      asstAppend('user', msg);
      var thinking = asstAppend('assistant', '');
      thinking.querySelector('.ui-chat-msg-body').innerHTML =
        '<span class="ui-chat-typing"><span></span><span></span><span></span></span>';
      var historyForSend = chatHistory.slice();
      chatHistory.push({role: 'user', content: msg});
      fetchJSON(cfg.chat_url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
          subject: titleInput.value,
          body:    bodyArea.value,
          message: msg,
          mode:    chatMode,
          history: historyForSend,
          references: selectedRef ? [selectedRef] : [],
        }),
      }).then(function(d) {
        asstSending = false;
        asstSend.disabled = false;
        thinking.remove();
        if (!d) { asstAppend('assistant', '(empty response)'); return; }
        if (d.error) { asstAppend('assistant', 'Error: ' + d.error); return; }
        if (d.type === 'article' && d.content) {
          // Article rewrite — show the proposal as an inline diff in
          // the editor pane (matches codewriter's pattern). Apply
          // copies the new text into the body textarea + suggested
          // title; Reject restores the textarea. The chat bubble is
          // a brief pointer rather than a full duplicate of the
          // content — the visual diff in the editor is the primary
          // affordance.
          if (typeof window.editorShowDiff === 'function') {
            window.editorShowDiff({
              newText: d.content,
              editorPane: main,
              editorTextarea: bodyArea,
              onApply: function(text) {
                bodyArea.value = text;
                if (d.title) titleInput.value = d.title;
                showToast('Applied — remember to Save');
              },
            });
            asstAppend('assistant', 'Review proposed changes in editor window.');
          } else {
            // Diff helper not loaded — fall back to the legacy
            // in-chat Approve/Deny pattern so the proposal is still
            // actionable.
            var diffLines = computeLineDiff(bodyArea.value, d.content);
            var msgEl = asstAppend('assistant', '');
            msgEl.querySelector('.ui-chat-msg-body').innerHTML = renderLineDiff(diffLines);
            var bar = el('div', {class: 'ui-chat-actions'});
            var statusLine = function(text) {
              bar.innerHTML = '';
              bar.appendChild(el('span', {class: 'ui-chat-act-status'}, [text]));
            };
            var approveBtn = el('button', {class: 'ui-row-btn success', onclick: function(){
              bodyArea.value = d.content;
              if (d.title) titleInput.value = d.title;
              statusLine('✓ Applied — Save to keep');
              showToast('Applied — remember to Save');
            }}, ['Approve']);
            var denyBtn = el('button', {class: 'ui-row-btn danger', onclick: function(){
              statusLine('✗ Denied');
            }}, ['Deny']);
            var copyBtn = el('button', {class: 'ui-row-btn', onclick: function(){
              navigator.clipboard && navigator.clipboard.writeText(d.content);
              showToast('Copied');
            }}, ['Copy']);
            bar.appendChild(approveBtn);
            bar.appendChild(denyBtn);
            bar.appendChild(copyBtn);
            msgEl.appendChild(bar);
          }
          chatHistory.push({role: 'assistant', content: d.content});
        } else {
          var text = d.content || '';
          asstAppend('assistant', text);
          chatHistory.push({role: 'assistant', content: text});
        }
      }).catch(function(err) {
        asstSending = false;
        asstSend.disabled = false;
        thinking.remove();
        asstAppend('assistant', 'Error: ' + err.message);
      });
    }

    function asstAppend(role, content) {
      // Drop the empty-state placeholder once a real message appears.
      var empty = asstThread.querySelector('.ui-tw-asst-empty');
      if (empty) empty.remove();
      var msg = el('div', {class: 'ui-chat-msg ' + (role === 'assistant' ? 'assistant' : 'user')});
      var body = el('div', {class: 'ui-chat-msg-body'});
      body.textContent = content || '';
      msg.appendChild(body);
      asstThread.appendChild(msg);
      asstThread.scrollTop = asstThread.scrollHeight;
      return msg;
    }

    function diffStats(oldText, newText) {
      // Cheap line-count diff so the user has SOME signal about how
      // big the rewrite is before applying.
      var oldLines = (oldText || '').split('\n').length;
      var newLines = (newText || '').split('\n').length;
      return { add: Math.max(0, newLines - oldLines), remove: Math.max(0, oldLines - newLines) };
    }

    // computeLineDiff runs an LCS-based diff over two text blocks split
    // on newlines and returns an array of {type, text} records. Type is
    // '=' (unchanged), '-' (in old but not new), or '+' (in new but not
    // old). Used by the assistant proposal renderer to draw red/green
    // line markers like a github-style diff. O(m*n) memory — fine for
    // typical articles (a few hundred lines); a Myers-style algorithm
    // would be linear-ish in practice but adds code without measurable
    // wins at the article length we deal with.
    function computeLineDiff(a, b) {
      var oldL = (a || '').split('\n');
      var newL = (b || '').split('\n');
      var m = oldL.length, n = newL.length;
      // Build LCS DP table.
      var dp = new Array(m + 1);
      for (var i = 0; i <= m; i++) {
        dp[i] = new Array(n + 1);
        dp[i][0] = 0;
      }
      for (var j = 0; j <= n; j++) dp[0][j] = 0;
      for (var i2 = 1; i2 <= m; i2++) {
        for (var j2 = 1; j2 <= n; j2++) {
          if (oldL[i2-1] === newL[j2-1]) dp[i2][j2] = dp[i2-1][j2-1] + 1;
          else dp[i2][j2] = Math.max(dp[i2-1][j2], dp[i2][j2-1]);
        }
      }
      // Backtrack to construct the diff.
      var out = [];
      var i3 = m, j3 = n;
      while (i3 > 0 || j3 > 0) {
        if (i3 > 0 && j3 > 0 && oldL[i3-1] === newL[j3-1]) {
          out.unshift({type: '=', text: oldL[i3-1]});
          i3--; j3--;
        } else if (j3 > 0 && (i3 === 0 || dp[i3][j3-1] >= dp[i3-1][j3])) {
          out.unshift({type: '+', text: newL[j3-1]});
          j3--;
        } else if (i3 > 0) {
          out.unshift({type: '-', text: oldL[i3-1]});
          i3--;
        }
      }
      return out;
    }

    function renderLineDiff(lines) {
      var add = 0, rem = 0;
      lines.forEach(function(l){ if (l.type === '+') add++; else if (l.type === '-') rem++; });
      var summary = '<div class="ui-tw-diff-h">Proposed rewrite &middot; <span class="add">+' + add + '</span> / <span class="rem">&minus;' + rem + '</span></div>';
      var rows = lines.map(function(l) {
        var cls = l.type === '+' ? 'add' : (l.type === '-' ? 'rem' : 'same');
        var prefix = l.type === '+' ? '+ ' : (l.type === '-' ? '- ' : '  ');
        var text = (l.text || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
        return '<div class="ui-tw-diff-row ' + cls + '"><span class="prefix">' + prefix + '</span>' + text + '</div>';
      }).join('');
      return summary + '<div class="ui-tw-diff">' + rows + '</div>';
    }

    function autoresizeAsst() {
      asstInput.style.height = 'auto';
      asstInput.style.height = Math.min(asstInput.scrollHeight, 120) + 'px';
    }
    asstInput.addEventListener('input', autoresizeAsst);
    asstInput.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) { ev.preventDefault(); doAssist(); }
    });

    // --- Toolbar actions -------------------------------------------------
    // setBtnBusy marks a toolbar button as in-flight: disables it and
    // swaps the text for a spinner + loading label. restoreBtn() puts
    // the original label back. Common to all long-running toolbar
    // actions (reprocess, suggest title, image generate) so the user
    // always has a visible "still working" signal.
    function setBtnBusy(btn, label) {
      if (!btn) return;
      btn.disabled = true;
      btn.dataset.origLabel = btn.textContent;
      btn.innerHTML = '<span class="ui-spinner"></span>' + label;
    }
    function restoreBtn(btn) {
      if (!btn) return;
      btn.disabled = false;
      var orig = btn.dataset.origLabel;
      if (orig) {
        btn.textContent = orig;
        delete btn.dataset.origLabel;
      }
    }

    // toggleMerge opens a slide-in panel that lets the user choose a
    // saved merge source (or paste content directly), add optional
    // guidance, and fire a merge call. The result applies directly
    // to the editor (no Approve/Deny — user explicitly initiated).
    function toggleMerge() {
      if (mergePanel.style.display !== 'none') {
        mergePanel.style.display = 'none';
        return;
      }
      // Hide siblings — only one overlay at a time.
      revsPanel.style.display = 'none';
      rulesPanel.style.display = 'none';
      mergePanel.innerHTML = '';
      mergePanel.style.display = '';

      var header = el('div', {class: 'ui-tw-revs-h'}, [
        el('span', {text: 'Merge with another source'}),
        el('button', {class: 'ui-row-btn', onclick: function(){ mergePanel.style.display = 'none'; }}, ['Close']),
      ]);
      mergePanel.appendChild(header);

      var hint = el('div', {class: 'ui-tw-rules-hint'},
        ['Pick a saved source from the dropdown OR paste content into the textarea below. Optional guidance shapes the merge (e.g. "favor the saved source\'s wording", "strip code blocks").']);
      mergePanel.appendChild(hint);

      // Saved-sources picker (rendered when MergeSourcesURL is set).
      var sourceSelect = null;
      if (cfg.merge_sources_url) {
        sourceSelect = el('select', {class: 'ui-form-select', style: 'width:100%;margin-bottom:0.5rem'});
        sourceSelect.appendChild(el('option', {value: ''}, ['(paste content below or pick a saved source)']));
        sourceSelect.addEventListener('change', function() {
          if (!sourceSelect.value || !cfg.merge_source_url) return;
          var url = cfg.merge_source_url.replace('{id}', encodeURIComponent(sourceSelect.value));
          fetchJSON(url).then(function(rec) {
            if (rec && (rec.body || rec.Body)) {
              pasteArea.value = rec.body || rec.Body;
            }
          }).catch(function(err){ showToast('Load source failed: ' + err.message); });
        });
        fetchJSON(cfg.merge_sources_url).then(function(items) {
          (items || []).forEach(function(s) {
            var opt = el('option', {value: s.id || s.ID, title: relTime(s.date || s.Date)},
              [(s.name || s.Name) + ' — ' + relTime(s.date || s.Date)]);
            sourceSelect.appendChild(opt);
          });
        }).catch(function(){});
        mergePanel.appendChild(sourceSelect);
      }

      var pasteArea = el('textarea', {class: 'ui-tw-rules-ta',
        placeholder: 'Paste the content to merge in here. Or pick a saved source above to load it.',
        style: 'min-height:140px'});
      mergePanel.appendChild(pasteArea);

      var guidance = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'Optional guidance (how should the merge resolve conflicts?)',
        style: 'margin-top:0.5rem'});
      mergePanel.appendChild(guidance);

      var statusLine = el('div', {class: 'ui-tw-rules-saved'});
      var saveSourceBtn = (cfg.merge_sources_url) ? el('button', {class: 'ui-row-btn',
        title: 'Save the pasted content as a reusable merge source',
        onclick: async function() {
          var name = await uiPrompt('Name this merge source:', '');
          if (!name) return;
          if (!pasteArea.value.trim()) { showToast('Paste something first'); return; }
          fetchJSON(cfg.merge_sources_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name: name, body: pasteArea.value}),
          }).then(function() {
            showToast('Saved');
            // Refresh the dropdown.
            if (sourceSelect) {
              while (sourceSelect.children.length > 1) sourceSelect.removeChild(sourceSelect.lastChild);
              fetchJSON(cfg.merge_sources_url).then(function(items) {
                (items || []).forEach(function(s) {
                  var opt = el('option', {value: s.id || s.ID},
                    [(s.name || s.Name) + ' — ' + relTime(s.date || s.Date)]);
                  sourceSelect.appendChild(opt);
                });
              });
            }
          }).catch(function(err){ showToast('Save source failed: ' + err.message); });
        }}, ['Save as source']) : null;
      var mergeRunBtn = el('button', {class: 'ui-row-btn success',
        onclick: async function() {
          var other = pasteArea.value.trim();
          if (!other) { showToast('Need something to merge with'); return; }
          if (!bodyArea.value.trim()) { showToast('Current article is empty — nothing to merge into'); return; }
          if (!(await window.uiConfirm('Merge the source into the current article? The body will be replaced with the merged result.'))) return;
          setBtnBusy(mergeRunBtn, 'Merging…');
          statusLine.textContent = '';
          fetchJSON(cfg.merge_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
              subject:  titleInput.value,
              body:     bodyArea.value,
              other:    other,
              mode:     'edit',
              guidance: guidance.value,
            }),
          }).then(function(d) {
            restoreBtn(mergeRunBtn);
            if (!d) { showToast('Empty response'); return; }
            if (d.error) { showToast('Error: ' + d.error); return; }
            var merged = d.content || d.body || '';
            if (d.type === 'article' && merged) {
              bodyArea.value = merged;
              if (d.title) titleInput.value = d.title;
              // Auto-save so the merge produces a revision. ◀ reverts
              // if the merge result isn't what the user wanted.
              saveArticle();
              showToast('Merged and saved — use ◀ to revert if needed');
              mergePanel.style.display = 'none';
            } else if (merged) {
              // Server returned chat-style instead of article — show it.
              statusLine.textContent = 'Merge returned conversational text instead of an article body — see the assistant pane.';
              asstAppend('assistant', merged);
            } else {
              showToast('Merge produced no output');
            }
          }).catch(function(err) {
            restoreBtn(mergeRunBtn);
            showToast('Merge failed: ' + err.message);
          });
        }}, ['Merge into article']);
      var actions = el('div', {class: 'ui-tw-rules-actions'}, [
        statusLine,
        el('div', {style: 'display:flex;gap:0.4rem'}, [saveSourceBtn, mergeRunBtn].filter(Boolean)),
      ]);
      mergePanel.appendChild(actions);
      pasteArea.focus();
    }

    function toggleRules() {
      if (rulesPanel.style.display !== 'none') {
        rulesPanel.style.display = 'none';
        return;
      }
      // Hide the revisions panel if it's open — only one overlay at a time.
      revsPanel.style.display = 'none';
      rulesPanel.innerHTML = '';
      rulesPanel.style.display = '';
      var header = el('div', {class: 'ui-tw-revs-h'}, [
        el('span', {text: 'Rules'}),
        el('button', {class: 'ui-row-btn', onclick: function(){ rulesPanel.style.display = 'none'; }}, ['Close']),
      ]);
      var ta = el('textarea', {class: 'ui-tw-rules-ta',
        placeholder: 'One rule per line. Examples:\n  Strip mentions of sample system.\n  Never post API keys, passwords, or other secrets.\n  Match the existing article tone — terse, factual.'});
      ta.value = '(loading…)';
      ta.disabled = true;
      var savedHint = el('div', {class: 'ui-tw-rules-saved'});
      var saveBtn = el('button', {class: 'ui-row-btn', onclick: function() {
        saveBtn.disabled = true;
        savedHint.textContent = 'saving…';
        fetchJSON(cfg.rules_url, {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({rules: ta.value}),
        }).then(function() {
          saveBtn.disabled = false;
          savedHint.textContent = 'saved — applies to next assistant message';
          setTimeout(function(){ savedHint.textContent = ''; }, 3000);
        }).catch(function(err) {
          saveBtn.disabled = false;
          savedHint.textContent = '';
          showToast('Save failed: ' + err.message);
        });
      }}, ['Save rules']);
      var hint = el('div', {class: 'ui-tw-rules-hint'},
        ['Lines you write here are appended to the assistant\'s system prompt as constraints. Each line is one rule.']);
      rulesPanel.appendChild(header);
      rulesPanel.appendChild(hint);
      rulesPanel.appendChild(ta);
      rulesPanel.appendChild(el('div', {class: 'ui-tw-rules-actions'}, [savedHint, saveBtn]));
      fetchJSON(cfg.rules_url).then(function(d) {
        ta.disabled = false;
        ta.value = (d && d.rules) || '';
        ta.focus();
      }).catch(function(err) {
        ta.disabled = false;
        ta.value = '';
        savedHint.textContent = 'Failed to load existing rules';
      });
    }

    // toggleRevisions retained for backward compatibility — the inline
    // nav (back/forward arrows + Make current) is the primary UX now.
    function toggleRevisions() {
    }

    loadList();
    // Deep link: open the article named in the URL (?article=<id>) so a
    // handoff from another app that saves a doc then opens this editor with
    // ?article=<id> lands directly in the editor instead of a blank page.
    // The list still loads behind it; this just selects the handed-off
    // article on arrival.
    try {
      var deepLinkArticle = new URLSearchParams(window.location.search).get('article');
      if (deepLinkArticle) { openArticle(deepLinkArticle); }
    } catch (e) {}
    return wrap;
  };

