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
      getBody:  function()    { return editorValue(); },
      setBody:  function(s)   { docSetValue(s); },
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

    // Document-mode buttons: outline toggle, template picker, and the
    // raw-mode whole-document draft. Each appears only when the host
    // wired the matching config, so an app opts in per capability.
    var outlineBtn = cfg.outline ? el('button', {
      class: 'ui-row-btn compact', type: 'button',
      title: 'Switch between the raw text and a section outline',
      onclick: function(){ toggleOutline(); },
    }, ['Outline']) : null;
    var tplBtn = ((cfg.templates && cfg.templates.length) || cfg.templates_list_url) ? el('button', {
      class: 'ui-row-btn compact', type: 'button',
      title: 'Start from a template, or save this document as one',
      onclick: function(){ openTemplatesModal(); },
    }, ['Templates']) : null;
    var rulesBtn = cfg.rules_url ? el('button', {
      class: 'ui-row-btn compact', type: 'button',
      title: 'Standing instructions the assistant must follow',
      onclick: function(){ toggleRules(); },
    }, ['Rules']) : null;
    var assistBtn = cfg.assist_url ? el('button', {
      class: 'ui-row-btn compact', type: 'button',
      title: 'Draft the whole document with assistance',
      onclick: function(){ openDocAssist('', editorValue(), function(t){ docSetValue(t); }); },
    }, ['✨ Draft']) : null;

    var actionButtons = [];
    // Holdover dispatcher for the two slide-in panel flows that
    // still live in this file (rules, merge). Everything else is
    // a "client" action registered from the app's package via
    // window.uiRegisterClientAction. Rules and merge will move
    // out once a generic SlidePanel primitive exists.
    function builtinAction(name) {
      switch (name) {
        // 'rules' used to live here; RulesURL now renders its own button,
        // so a declared {Method:"builtin", URL:"rules"} action is dropped
        // rather than producing a second one.
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
    // Revision walk — shared with the code-document panel via buildRevisionNav
    // (45_document_core.js). Only onLoad is app-shaped: an article
    // revision carries a body and a subject.
    var revNav = buildRevisionNav({
      listURL: cfg.revisions_list_url,
      loadURL: cfg.revision_load_url,
      onMakeCurrent: function(){ saveArticle(); },
      onLoad: function(rev) {
        docSetValue(rev.body || rev[bodyF] || '');
        if (rev.subject || rev[subjectF]) titleInput.value = rev.subject || rev[subjectF];
      },
    });

    // The titlebar-level Delete button used to live here. Removed —
    // delete now happens per-article via the × button on each sidebar
    // row (matches the code-document panel's pattern). Saves toolbar real estate
    // and removes a destructive control from a high-traffic toolbar.
    var saveBtn = el('button', {class: 'ui-row-btn primary', onclick: function(){ saveArticle(); }}, ['Save']);

    var revGroup = revNav.group;


    titleBar.appendChild(titleInput);
    titleBar.appendChild(savedTag);
    // Revision group sits as the leftmost button cluster on the
    // titlebar (immediately after the title input and saved tag) —
    // matches the code-document panel, where rev navigation is the first button
    // group after the name input.
    if (revGroup) titleBar.appendChild(revGroup);
    if (rulesBtn) titleBar.appendChild(rulesBtn);
    if (outlineBtn) titleBar.appendChild(outlineBtn);
    if (tplBtn) titleBar.appendChild(tplBtn);
    if (assistBtn) titleBar.appendChild(assistBtn);
    actionButtons.forEach(function(btn){ titleBar.appendChild(btn); });
    if (extrasBtn) {
      var extrasWrap = el('span', {class: 'ui-tw-extras-wrap'}, [extrasBtn, extrasMenu]);
      titleBar.appendChild(extrasWrap);
    }
    titleBar.appendChild(saveBtn);
    main.appendChild(titleBar);

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

    // Outline view over the body — the same sectioned markdown editor
    // the agent prompt fields and the code-document panel use. An article IS
    // markdown, so there's no language to gate on here; the host opts
    // in with cfg.outline.
    //
    // The textarea stays the SINGLE value carrier: save, chat, merge,
    // revisions, and the client-action editor handle all keep reading it,
    // and the outline writes through on every edit.
    var docOutline = null;
    var docOutlineHost = el('div', {class: 'ui-tw-outline'});
    docOutlineHost.style.display = 'none';
    main.appendChild(docOutlineHost);
    function docOutlineOn() { return docOutlineHost.style.display !== 'none'; }
    function docSetValue(text) {
      bodyArea.value = text == null ? '' : String(text);
      if (docOutline && docOutlineOn()) docOutline.setValue(bodyArea.value);
    }
    function editorValue() {
      if (docOutline && docOutlineOn()) return docOutline.getValue();
      return bodyArea.value || '';
    }
    // docToRaw restores the textarea before anything that manipulates its
    // visibility — the diff pane hides it and inserts itself where it sat,
    // so an outline left up would cover a review nobody can resolve.
    function docToRaw() {
      if (!docOutlineOn()) return;
      bodyArea.value = docOutline.getValue();
      docOutlineHost.style.display = 'none';
      bodyArea.style.display = '';
      if (outlineBtn) {
        outlineBtn.textContent = 'Outline';
        outlineBtn.classList.remove('on');
      }
      if (assistBtn) assistBtn.style.display = '';
    }
    function toggleOutline() {
      if (docOutlineOn()) { docToRaw(); return; }
      if (!docOutline) {
        docOutline = buildSectionsEditor({
          allowFree: true,
          initial: bodyArea.value || '',
          onChange: function(text) { bodyArea.value = text; },
          onSuggest: cfg.assist_url ? function(title, body, apply) {
            openDocAssist(title, body, apply);
          } : null,
        });
        docOutlineHost.appendChild(docOutline.node);
      } else {
        docOutline.setValue(bodyArea.value || '');
      }
      bodyArea.style.display = 'none';
      docOutlineHost.style.display = '';
      if (outlineBtn) {
        outlineBtn.textContent = 'Raw';
        outlineBtn.classList.add('on');
      }
      // Whole-document drafting is a raw-mode affordance; the outline
      // offers it per section instead.
      if (assistBtn) assistBtn.style.display = 'none';
    }

    // Template picker — shared with the code-document panel.
    function openTemplatesModal() {
      openTemplatePicker({
        builtins:    cfg.templates || [],
        listURL:     cfg.templates_list_url,
        itemURL:     cfg.template_url,
        currentBody: function(){ return editorValue(); },
        currentName: function(){ return (titleInput.value || '').trim(); },
        onApply: async function(tpl) {
          if ((editorValue() || '').trim() !== '') {
            if (!(await window.uiConfirm('Replace the body with the "' + (tpl.name || 'template') + '" template?'))) return;
          }
          docSetValue(tpl.body || '');
          if ((titleInput.value || '').trim() === '' && tpl.name && !cfg.title_readonly) {
            titleInput.value = tpl.name;
          }
        },
      });
    }

    // openDocAssist launches the shared draft-with-me workbench for the
    // whole body or one section of it.
    function openDocAssist(section, initial, apply) {
      window.uiOpenAssist({
        title: (titleInput.value || 'Document') + (section ? ' — ' + section : ''),
        subtitle: section
          ? 'Drafting one section. The rest of the document is untouched.'
          : 'Drafting the whole document.',
        initial: initial,
        send: function(req, done) {
          fetchJSON(cfg.assist_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
              name: titleInput.value || '',
              section: section || '',
              message: req.message,
              draft: req.draft,
              history: req.history,
            }),
          }).then(function(d) {
            done({reply: d && d.reply, value: d && d.value});
          }).catch(function(err) {
            done(null, (err && err.message) || String(err));
          });
        },
        onAccept: apply,
      });
    }

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
    // Chat history lives inside docChat (buildDocChat); this side keeps
    // only the mode toggle and the in-flight flag the send button reads.
    var chatMode = 'edit'; // 'edit' or 'chat'
    var asstSending = false;
    // Revision state lives inside revNav (buildRevisionNav).

    var openDrawer  = drawer.openDrawer;
    var closeDrawer = drawer.closeDrawer;

    // --- Sidebar list ---
    var bulkSelected = {}; // article id -> true
    var bulkState    = {mode: false};
    // Record list — shared with the code-document panel via buildDocList
    // (45_document_core.js). The bulk-select block is article-only;
    // That panel passes no `bulk` and gets a plain list.
    var docList = buildDocList({
      host:       sideList,
      listURL:    cfg.list_url,
      idField:    idF,
      labelField: subjectF,
      dateField:  dateF,
      emptyText:  cfg.empty_text || 'No articles yet.',
      currentID:  function(){ return currentID; },
      onOpen:     function(id){ openArticle(id); closeDrawer(); },
      deleteURL:  cfg.delete_url,
      onDeleted:  function(id){ if (currentID === id) openArticle(null); },
      bulk: cfg.bulk_select ? {
        state:    bulkState,
        selected: bulkSelected,
        confirmMany: function(n){ return 'Delete ' + n + ' article(s) permanently?'; },
      } : null,
    });
    function loadList() { docList.reload(); }

    function openArticle(id) {
      currentID = id;
      currentImageURL = '';
      asstThread.innerHTML = '';
      asstThread.appendChild(el('div', {class: 'ui-tw-asst-empty'},
        [id ? 'Ask the assistant to discuss or rewrite this article.' : 'Start typing your article — the assistant can help once you have something to work with.']));
      docChat.clear();
      hideImage();
      if (!id) {
        titleInput.value = '';
        docSetValue('');
        lastSavedSubject = '';
        lastSavedBody = '';
        savedTag.textContent = '';
        mobileTitle.textContent = 'New article';
        revNav.clear();
        loadList();
        return;
      }
      var url = cfg.load_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        titleInput.value = rec[subjectF] || '';
        docSetValue(rec[bodyF] || '');
        lastSavedSubject = titleInput.value;
        lastSavedBody    = bodyArea.value;
        savedTag.textContent = 'saved ' + relTime(rec[dateF]);
        mobileTitle.textContent = rec[subjectF] || 'Untitled';
        if (rec[imageF]) showImage(rec[imageF]);
        loadList();
        revNav.reload(currentID);
      }).catch(function(err){ showToast('Load failed: ' + err.message); });
    }

    function saveArticle(extra) {
      var subject = titleInput.value.trim();
      var body    = editorValue();
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
        revNav.reload(currentID);
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

    // --- Assistant ---
    // Chat protocol — shared with the code-document panel via buildDocChat
    // (45_document_core.js). Rendering stays here: article messages are
    // plain text set via textContent, not formatted HTML.
    var docChat = buildDocChat({
      url: cfg.chat_url,
      setBusy: function(on) {
        asstSending = on;
        asstSend.disabled = on;
      },
      appendMsg: function(role, text) {
        if (role === 'error') return asstAppend('assistant', 'Error: ' + text);
        return asstAppend(role, text);
      },
      thinking: function() {
        var node = asstAppend('assistant', '');
        node.querySelector('.ui-chat-msg-body').innerHTML =
          '<span class="ui-chat-typing"><span></span><span></span><span></span></span>';
        return node;
      },
      buildBody: function(message, mode, history) {
        return {
          subject: titleInput.value,
          body:    editorValue(),
          message: message,
          mode:    mode,
          history: history,
          references: selectedRef ? [selectedRef] : [],
        };
      },
      proposalOf: function(data) {
        return (data.type === 'article' && data.content) ? data.content : null;
      },
      onProposal: function(text, data) {
        // The diff pane hides the textarea and inserts itself where the
        // textarea sat; an outline left up would cover the review.
        docToRaw();
        if (typeof window.editorShowDiff === 'function') {
          window.editorShowDiff({
            newText: text,
            editorPane: main,
            editorTextarea: bodyArea,
            onApply: function(t) {
              docSetValue(t);
              if (data.title) titleInput.value = data.title;
              showToast('Applied — remember to Save');
            },
          });
          asstAppend('assistant', 'Review proposed changes in editor window.');
          return;
        }
        // Diff helper not loaded. Every host mounts it via
        // ExtraHeadHTML, so this is a misconfiguration rather than a
        // supported mode: apply directly and say so.
        docSetValue(text);
        if (data.title) titleInput.value = data.title;
        asstAppend('assistant', 'Applied directly (diff viewer unavailable) - remember to Save.');
        showToast('Applied - remember to Save');
      },
    });
    function doAssist() {
      var msg = asstInput.value.trim();
      if (!msg) return;
      asstInput.value = '';
      autoresizeAsst();
      docChat.send(msg, chatMode);
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
      // Hide siblings — only one overlay at a time. (Rules is a modal
      // now, so it can't collide with this pane.)
      revsPanel.style.display = 'none';
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
          if (!editorValue().trim()) { showToast('Current article is empty — nothing to merge into'); return; }
          if (!(await window.uiConfirm('Merge the source into the current article? The body will be replaced with the merged result.'))) return;
          setBtnBusy(mergeRunBtn, 'Merging…');
          statusLine.textContent = '';
          fetchJSON(cfg.merge_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
              subject:  titleInput.value,
              body:     editorValue(),
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
              docSetValue(merged);
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

    // Rules — shared with the code-document panel via openRulesPanel
    // (45_document_core.js), on the standard modal shell.
    function toggleRules() {
      openRulesPanel({url: cfg.rules_url, noun: 'reply'});
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

