  components.codewriter_panel = function(cfg) {
    var idF   = cfg.id_field   || 'id';
    var nameF = cfg.name_field || 'name';
    var langF = cfg.lang_field || 'lang';
    var codeF = cfg.code_field || 'code';
    var dateF = cfg.date_field || 'date';
    var languages = (cfg.languages && cfg.languages.length)
      ? cfg.languages
      : ['bash','sql','python','powershell','go','regex',''];

    var wrap = el('div', {class: 'ui-cw ui-tw'});
    var side = el('div', {class: 'ui-tw-side'});

    // Collapse state — desktop only. Mobile uses the slide-in drawer
    // mechanism inherited from ChatPanel and ignores this state.
    var sideCollapsed = false;
    try { sideCollapsed = localStorage.getItem('cw.sideCollapsed') === '1'; } catch (_) {}
    var collapseBtn = el('button', {
      class: 'ui-tw-collapse', title: 'Hide snippets list',
      onclick: function(){ toggleCollapse(); },
    }, ['‹']);
    function toggleCollapse() {
      sideCollapsed = !sideCollapsed;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      collapseBtn.title = sideCollapsed ? 'Show snippets list' : 'Hide snippets list';
      collapseBtn.textContent = sideCollapsed ? '›' : '‹';
      try { localStorage.setItem('cw.sideCollapsed', sideCollapsed ? '1' : '0'); } catch (_) {}
    }

    var sideHdrBuilt = renderSideHeader({
      label:     'Snippets',
      className: 'ui-tw-side-h',
      newTitle:  'New snippet',
      onNew:     function(){ openSnippet(null); },
      onClose:   function(){ closeDrawer(); },
      leftExtras: [collapseBtn],
    });
    var sideHdr  = sideHdrBuilt.elt;
    var sideList = el('div', {class: 'ui-tw-side-list'}, ['Loading…']);
    var sideSearch = makeSideSearch(sideList);
    side.appendChild(sideHdr);
    side.appendChild(sideSearch);
    side.appendChild(sideList);

    var drawer = makeDrawer(side, {
      title:          'New snippet',
      hamburgerTitle: 'Snippets',
      newTitle:       'New snippet',
      onNew:          function(){ openSnippet(null); },
    });
    var mobileTitle    = drawer.mobileTitle;
    var drawerBackdrop = drawer.backdrop;
    var openDrawer  = drawer.openDrawer;
    var closeDrawer = drawer.closeDrawer;

    var main = el('div', {class: 'ui-tw-main ui-cw-main'});
    main.appendChild(drawer.mobileHdr);

    // Toolbar — name + lang + Save / Copy / New. Mirrors the legacy
    // codewriter top bar but lives inside the framework page chrome.
    var nameInput = el('input', {
      type: 'text', class: 'ui-cw-name',
      placeholder: cfg.placeholder_name || 'Snippet name…',
    });
    var langSelect = el('select', {class: 'ui-cw-lang'});
    languages.forEach(function(l) {
      langSelect.appendChild(el('option', {value: l}, [l || 'other']));
    });

    var saveBtn = el('button', {class: 'ui-row-btn primary', onclick: function(){ saveSnippet(); }}, ['Save']);
    var copyBtn = el('button', {class: 'ui-row-btn', onclick: function(){ copyEditor(); }}, ['Copy']);
    var newBtn  = el('button', {class: 'ui-row-btn', onclick: function(){ openSnippet(null); }}, ['New']);
    var varsBtn = el('button', {class: 'ui-row-btn', onclick: function(){ openVarsModal('apply'); }}, ['Variables']);
    var valuesBtn = cfg.values_list_url
      ? el('button', {class: 'ui-row-btn', onclick: function(){ openValuesModal(); }}, ['Values'])
      : null;

    // Revision navigation controls — only built when the snippet
    // record has revision-list / revision-load URLs configured. Hidden
    // when no snippet is open (currentID === null) so the toolbar
    // doesn't show "rev 0/0" on a blank New buffer.
    var revGroup = null, revBackBtn = null, revFwdBtn = null, revIndicator = null, revMarkBtn = null;
    if (cfg.revisions_list_url && cfg.revision_load_url) {
      revBackBtn = el('button', {class: 'ui-row-btn ui-cw-rev-btn', title: 'Previous revision',
        onclick: function(){ navigateRevision(-1); }, disabled: 'true'}, ['◀']);
      revFwdBtn = el('button', {class: 'ui-row-btn ui-cw-rev-btn', title: 'Next revision',
        onclick: function(){ navigateRevision(1); }, disabled: 'true'}, ['▶']);
      revIndicator = el('span', {class: 'ui-cw-rev-ind'}, []);
      revMarkBtn = el('button', {class: 'ui-row-btn ui-cw-rev-mark', title: 'Save current editor content as a new (latest) revision',
        onclick: function(){ markAsLatest(); }, style: 'display:none'}, ['Make Latest']);
      revGroup = el('span', {class: 'ui-cw-rev-group', style: 'display:none'},
        [revMarkBtn, revBackBtn, revIndicator, revFwdBtn]);
    }

    var toolbarKids = [nameInput];
    if (revGroup) toolbarKids.push(revGroup);
    toolbarKids.push(langSelect);
    toolbarKids.push(varsBtn);
    if (valuesBtn) toolbarKids.push(valuesBtn);
    toolbarKids.push(saveBtn);
    toolbarKids.push(copyBtn);
    toolbarKids.push(newBtn);
    var toolbar = el('div', {class: 'ui-cw-toolbar'}, toolbarKids);
    main.appendChild(toolbar);

    // Body row — editor (flex:1) + chat pane (right, fixed-ish width).
    var bodyRow = el('div', {class: 'ui-cw-body'});

    // Editor pane — code textarea fills the available space, with an
    // optional collapsible Context section beneath it for reference
    // material the LLM should see alongside the code on every chat
    // turn.
    var editor = el('textarea', {
      class: 'ui-cw-editor',
      placeholder: cfg.placeholder_code || 'Write or paste code here. Save it for later, or chat with the LLM to generate one.',
      spellcheck: 'false',
    });

    var ctxOpen = true;
    var ctxArrow   = el('span', {class: 'ui-cw-ctx-arrow open'}, ['▸']);
    var ctxLabel   = el('span', {}, [' Context (table schemas, reference docs, notes)']);
    var ctxCurrent = el('span', {class: 'ui-cw-ctx-current'}, []);
    var ctxSaveBtn = el('button', {class: 'ui-cw-ctx-btn',
      onclick: function(ev){ ev.stopPropagation(); saveContext(); }}, ['Save']);
    var ctxLoadBtn = el('button', {class: 'ui-cw-ctx-btn',
      onclick: function(ev){ ev.stopPropagation(); openContextsModal(); }}, ['Load']);
    var ctxActions = el('span', {class: 'ui-cw-ctx-actions'}, [ctxSaveBtn, ctxLoadBtn, ctxCurrent]);
    var ctxToggle  = el('div', {class: 'ui-cw-ctx-toggle',
      onclick: function(){ toggleCtx(); }},
      [ctxArrow, ctxLabel, ctxActions]);
    var ctxEditor  = el('textarea', {
      class: 'ui-cw-ctx-editor',
      placeholder: cfg.placeholder_ctx || 'Paste table schemas, DDL, column descriptions, API docs, or any reference material here. The LLM reads this alongside the code on every chat turn.',
      spellcheck: 'false',
    });
    // Reference-collections picker. Rendered only when the host wires
    // cfg.collections_list_url. A compact "+ Add <noun>" button opens a
    // modal listing each collection by name + description + size; the chosen
    // stores show as removable chips here. The selected IDs ride every chat
    // POST as a "collections" array (pickedCollections). The user-facing noun
    // is host-supplied (cfg.collections_noun) so this stays domain-agnostic;
    // it defaults to a generic label when the host doesn't set one.
    var collNoun     = cfg.collections_noun || 'Reference';
    var collList     = [];   // [{id, name, description, documents, chunks}]
    var collSelected = {};   // id -> true
    var collBar      = el('div', {class: 'ui-cw-coll-bar'});
    if (!cfg.collections_list_url) collBar.style.display = 'none';
    function pickedCollections() {
      var out = [];
      for (var i = 0; i < collList.length; i++) {
        if (collSelected[collList[i].id]) out.push(collList[i].id);
      }
      return out;
    }
    function renderCollBar() {
      collBar.innerHTML = '';
      collBar.appendChild(el('span', {class: 'ui-cw-coll-lbl'}, [collNoun]));
      collBar.appendChild(el('button', {class: 'ui-cw-coll-add', type: 'button',
        onclick: function(){ openCollectionsModal(); }}, ['+ Add ' + collNoun]));
      collList.forEach(function(c){
        if (!collSelected[c.id]) return;
        var x = el('span', {class: 'ui-cw-coll-x', title: 'Remove'}, ['×']);
        x.addEventListener('click', function(){ delete collSelected[c.id]; renderCollBar(); });
        collBar.appendChild(el('span', {class: 'ui-cw-coll-chip'}, [(c.name || c.id) + ' ', x]));
      });
    }
    var ctxPane    = el('div', {class: 'ui-cw-ctx-pane open'}, [collBar, ctxEditor]);
    var ctxSection = el('div', {class: 'ui-cw-ctx-section'}, [ctxToggle, ctxPane]);

    // Load the collection list once for the picker. Best-effort: any
    // failure (no endpoint, error, empty list) just hides the bar so the
    // panel degrades to plain context-only.
    if (cfg.collections_list_url) {
      fetch(cfg.collections_list_url, {credentials: 'same-origin'})
        .then(function(r){ return r.ok ? r.json() : []; })
        .then(function(list){
          collList = list || [];
          if (!collList.length) { collBar.style.display = 'none'; return; }
          renderCollBar();
        })
        .catch(function(){ collBar.style.display = 'none'; });
    }

    // Horizontal drag handle between the code editor and the context
    // section. Dragging resizes the context section's height. Wired
    // to editor.UtilsJS()'s editorStartResize, loaded via the page's
    // ExtraHeadHTML.
    var ctxResizer = el('div', {class: 'ui-cw-ctx-resizer'});
    ctxResizer.addEventListener('mousedown', function(ev) {
      if (typeof window.editorStartResize !== 'function') return;
      window.editorStartResize(ev, 'row', {
        target:    ctxSection,
        container: editorWrap,
        resizer:   ctxResizer,
        min:       80,
        pad:       100,
      });
    });

    var editorWrap = el('div', {class: 'ui-cw-editor-wrap'}, [editor, ctxResizer, ctxSection]);

    function toggleCtx() {
      ctxOpen = !ctxOpen;
      ctxArrow.classList.toggle('open', ctxOpen);
      ctxPane.classList.toggle('open', ctxOpen);
    }

    var currentContextID   = null;
    var currentContextName = null;
    function setCurrentContext(id, name) {
      currentContextID = id || null;
      currentContextName = name || null;
      ctxCurrent.textContent = name ? '[' + name + ']' : '';
    }
    async function saveContext() {
      if (!cfg.contexts_list_url) {
        showToast('Saving contexts not configured');
        return;
      }
      var body = ctxEditor.value || '';
      if (!body.trim()) { showToast('Context is empty'); return; }
      var name = await uiPrompt('Name this context:', currentContextName || '');
      if (name == null) return;
      name = name.trim();
      if (!name) return;
      fetch(cfg.contexts_list_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: currentContextID, name: name, body: body}),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(rec) {
        setCurrentContext(rec.id, rec.name);
        showToast('Context saved');
      }).catch(function(err) {
        showToast('Save failed: ' + err.message);
      });
    }
    function loadContext(id) {
      if (!cfg.context_url) return;
      var url = cfg.context_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        ctxEditor.value = rec.body || '';
        setCurrentContext(rec.id, rec.name);
        if (!ctxOpen) toggleCtx();
        closeModal();
      }).catch(function(err) {
        showToast('Load failed: ' + err.message);
      });
    }
    async function deleteContext(id) {
      if (!cfg.context_url) return;
      if (!(await window.uiConfirm('Delete this saved context?'))) return;
      var url = cfg.context_url.replace('{id}', encodeURIComponent(id));
      fetch(url, {method: 'DELETE'}).then(function() {
        if (currentContextID === id) setCurrentContext(null, null);
        openContextsModal();
      });
    }

    // Chat pane — header + scrollable transcript + input area with
    // dual-send buttons (Chat = discuss only, Edit = propose code).
    var chatPane = el('div', {class: 'ui-cw-chat'});
    var chatHdr  = el('div', {class: 'ui-cw-chat-h'});
    chatHdr.appendChild(el('span', {}, ['Chat']));

    // Generic reference picker — when the host wires reference_sources_url,
    // surface every registered reference source in one dropdown. The chosen
    // item rides along with each chat POST as references ([{kind, item_id}]);
    // the app's handler injects that source's text / tools into the model
    // context. Domain-agnostic: core/ui knows only the shape. Same wire
    // format as the ArticleEditor picker.
    var selectedRef = null;
    if (cfg.reference_sources_url) {
      var refSelect = el('select', {class: 'ui-chat-mode', title: 'Ground replies in material gathered by another service',
        onchange: function() {
          var o = refSelect.options[refSelect.selectedIndex];
          selectedRef = (o && o.value) ? {kind: o.getAttribute('data-kind'), item_id: o.value} : null;
        }});
      refSelect.appendChild(el('option', {value: ''}, ['Reference…']));
      fetch(cfg.reference_sources_url, {credentials: 'same-origin'})
        .then(function(r){ return r.json(); })
        .then(function(groups) {
          if (!groups || !groups.length) { refSelect.style.display = 'none'; return; }
          groups.forEach(function(g) {
            var og = el('optgroup', {label: g.label});
            (g.items || []).forEach(function(it) {
              og.appendChild(el('option', {value: it.id, 'data-kind': g.kind, title: it.desc || ''}, [it.name]));
            });
            refSelect.appendChild(og);
          });
        }).catch(function() { refSelect.style.display = 'none'; });
      chatHdr.appendChild(refSelect);
    }

    var chatClearBtn = el('button', {class: 'ui-row-btn', onclick: function(){ clearChat(); }}, ['Clear']);
    chatHdr.appendChild(chatClearBtn);

    var chatMessages = el('div', {class: 'ui-cw-chat-msgs'});
    var chatInput = el('textarea', {
      class: 'ui-cw-chat-input', rows: '3',
      placeholder: cfg.placeholder_chat || 'Discuss with Chat, or click Edit to apply changes.',
    });
    var chatBtnTalk = el('button', {class: 'ui-row-btn', title: 'Discuss without changing the editor', onclick: function(){ sendChat('chat'); }}, ['Chat']);
    var chatBtnEdit = el('button', {class: 'ui-row-btn primary', title: 'Propose a change to apply to the editor', onclick: function(){ sendChat('edit'); }}, ['Edit']);
    chatInput.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) {
        ev.preventDefault();
        sendChat(ev.altKey ? 'chat' : 'edit');
      }
    });
    var chatInputArea = el('div', {class: 'ui-cw-chat-input-area'},
      [chatInput, chatBtnTalk, chatBtnEdit]);
    chatPane.appendChild(chatHdr);
    chatPane.appendChild(chatMessages);
    chatPane.appendChild(chatInputArea);

    // Vertical drag handle between editor wrap and chat pane. Width
    // changes are inline-styled on chatPane; persisted across the
    // session via localStorage so the user's preferred split survives
    // page reloads.
    var chatResizer = el('div', {class: 'ui-cw-chat-resizer'});
    chatResizer.addEventListener('mousedown', function(ev) {
      if (typeof window.editorStartResize !== 'function') return;
      window.editorStartResize(ev, 'col', {
        target:    chatPane,
        container: bodyRow,
        resizer:   chatResizer,
        min:       240,
        pad:       240,
        onEnd: function() {
          try { localStorage.setItem('cw.chatWidth', chatPane.style.width || ''); } catch (_) {}
        },
      });
    });
    try {
      var saved = localStorage.getItem('cw.chatWidth');
      if (saved) chatPane.style.width = saved;
    } catch (_) {}

    bodyRow.appendChild(editorWrap);
    bodyRow.appendChild(chatResizer);
    bodyRow.appendChild(chatPane);
    main.appendChild(bodyRow);

    // Floating expand-tab shown when the sidebar is collapsed. Pinned
    // to the left edge of the main pane so the user can always pop
    // the snippets list back without hunting through menus.
    var expandTab = el('button', {
      class: 'ui-tw-expand', title: 'Show snippets list',
      onclick: function(){ toggleCollapse(); },
    }, ['›']);

    wrap.appendChild(side);
    wrap.appendChild(main);
    wrap.appendChild(expandTab);
    wrap.appendChild(drawerBackdrop);

    // Apply persisted collapse state after the wrap is built.
    if (sideCollapsed) {
      wrap.classList.add('side-collapsed');
      collapseBtn.title = 'Show snippets list';
      collapseBtn.textContent = '›';
    }

    // --- Chat state ---
    var chatHistory = [];
    function appendHistory(role, content) {
      chatHistory.push({role: role, content: content});
      if (chatHistory.length > 40) chatHistory = chatHistory.slice(-40);
    }
    function clearChat() {
      chatMessages.innerHTML = '';
      chatHistory = [];
    }
    function addChatMsg(role, html, copyPayload) {
      var msg = el('div', {class: 'ui-cw-msg ' + role});
      msg.innerHTML = html;
      if (copyPayload != null) msg.dataset.copy = copyPayload;
      chatMessages.appendChild(msg);
      chatMessages.scrollTop = chatMessages.scrollHeight;
      return msg;
    }
    function escapeChat(s) {
      return String(s == null ? '' : s)
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    }
    function formatChatBody(s) {
      // Render fenced code blocks as <pre>; everything else as escaped
      // paragraphs with line breaks. Avoids embedding a literal backtick
      // (would close the Go raw-string holding this JS) by composing
      // the fence delimiter at runtime.
      var fence = String.fromCharCode(96, 96, 96);
      var out = '';
      var parts = String(s || '').split(fence);
      for (var i = 0; i < parts.length; i++) {
        if (i % 2 === 0) {
          var t = parts[i].replace(/^\s+|\s+$/g, '');
          if (t) out += '<p>' + escapeChat(t).replace(/\n/g, '<br>') + '</p>';
        } else {
          var body = parts[i];
          var nl = body.indexOf('\n');
          if (nl >= 0 && body.slice(0, nl).match(/^[a-zA-Z0-9_+-]*$/)) {
            body = body.slice(nl + 1);
          }
          out += '<pre>' + escapeChat(body) + '</pre>';
        }
      }
      return out;
    }
    function setChatBusy(busy) {
      chatBtnTalk.disabled = !!busy;
      chatBtnEdit.disabled = !!busy;
    }
    function sendChat(mode) {
      mode = mode === 'chat' ? 'chat' : 'edit';
      var text = (chatInput.value || '').trim();
      if (!text) return;
      chatInput.value = '';
      var prefix = mode === 'chat' ? '<span class="ui-cw-mode-tag">chat</span> ' : '';
      addChatMsg('user', prefix + escapeChat(text));
      appendHistory('user', text);

      var body = {
        name:    nameInput.value.trim(),
        lang:    langSelect.value,
        code:    editor.value || '',
        context: ctxEditor.value || '',
        collections: pickedCollections(),
        references: selectedRef ? [selectedRef] : [],
        message: text,
        mode:    mode,
        history: chatHistory.slice(0, -1),
      };
      setChatBusy(true);
      var thinking = addChatMsg('assistant', '<span class="ui-cw-spinner"></span> Thinking…');

      fetch(cfg.chat_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(data) {
        thinking.remove();
        // Edit-mode response with code → trigger the inline diff in
        // the editor pane and SUPPRESS the chat bubble entirely. The
        // editor's diff view already shows adds/removes visually with
        // its own +N / -M counters, so a parallel summary in chat is
        // redundant. The assistant text still goes into chatHistory
        // so the LLM has context for the next turn.
        var hasCodeProposal = mode !== 'chat' && data.type === 'code' && data.code && langSelect.value !== 'regex';
        if (hasCodeProposal) {
          if (typeof window.editorShowDiff === 'function') {
            window.editorShowDiff({
              newText: data.code,
              editorPane: editorWrap,
              editorTextarea: editor,
              onApply: function(text) { editor.value = text; },
            });
          } else {
            editor.value = data.code;
          }
        } else {
          // Chat mode, or Edit mode where the LLM didn't return code
          // (just prose) — render the response in the chat panel as
          // normal.
          addChatMsg('assistant', formatChatBody(data.content), data.content || '');
        }
        appendHistory('assistant', data.content || '');
      }).catch(function(err) {
        thinking.remove();
        addChatMsg('assistant', '<span class="ui-cw-err">Error: ' + escapeChat(err.message) + '</span>');
      }).then(function() {
        setChatBusy(false);
      });
    }

    // --- State ---
    var currentID    = null;
    var savedTagShown = false;

    // Revision-history state. Populated by loadRevisions() whenever
    // a snippet is opened or saved; navigateRevision() walks the
    // index and updates the editor / name / lang to match.
    var revisions     = [];
    var revisionIndex = -1;
    function updateRevNav() {
      if (!revGroup) return;
      var n = revisions.length;
      revBackBtn.disabled = revisionIndex <= 0;
      revFwdBtn.disabled  = revisionIndex >= n - 1;
      revIndicator.textContent = n > 0 ? 'rev ' + (revisionIndex + 1) + '/' + n : '';
      // Show "Make Latest" only when the user has scrolled back to
      // an earlier revision — clicking it saves the editor's current
      // contents as a new revision so it becomes the latest.
      revMarkBtn.style.display = (n > 0 && revisionIndex < n - 1) ? 'inline-flex' : 'none';
      revGroup.style.display = n > 0 ? 'inline-flex' : 'none';
    }
    function loadRevisions(snippetID) {
      if (!revGroup) return;
      if (!snippetID) {
        revisions = []; revisionIndex = -1;
        updateRevNav();
        return;
      }
      var url = cfg.revisions_list_url.replace('{id}', encodeURIComponent(snippetID));
      fetchJSON(url).then(function(data) {
        revisions = data || [];
        revisionIndex = revisions.length - 1;
        updateRevNav();
      }).catch(function() {
        revisions = []; revisionIndex = -1;
        updateRevNav();
      });
    }
    function navigateRevision(dir) {
      if (!revGroup) return;
      var idx = revisionIndex + dir;
      if (idx < 0 || idx >= revisions.length) return;
      var url = cfg.revision_load_url.replace('{id}', encodeURIComponent(revisions[idx].id));
      fetchJSON(url).then(function(rev) {
        editor.value = rev[codeF] || rev.code || '';
        if (rev[nameF] || rev.name) nameInput.value = rev[nameF] || rev.name || '';
        var l = rev[langF] || rev.lang || '';
        if (l) {
          for (var i = 0; i < langSelect.options.length; i++) {
            if (langSelect.options[i].value === l) { langSelect.selectedIndex = i; break; }
          }
        }
        revisionIndex = idx;
        updateRevNav();
      }).catch(function(err) {
        showToast('Could not load revision: ' + err.message);
      });
    }
    function markAsLatest() {
      // Saving the current editor content creates a new revision —
      // server-side it's appended to the list and becomes the latest,
      // exactly the behavior the legacy "Make Latest" button had.
      saveSnippet();
    }

    function setMobileTitle(t) { mobileTitle.textContent = t || 'New snippet'; }

    function openSnippet(id) {
      if (id == null) {
        currentID = null;
        nameInput.value = '';
        editor.value = '';
        // langSelect retains the last choice — usually convenient.
        setMobileTitle('New snippet');
        closeDrawer();
        markActive(null);
        loadRevisions(null);
        return;
      }
      var url = (cfg.load_url || (cfg.list_url + '/{id}')).replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        currentID = rec[idF] || id;
        nameInput.value = rec[nameF] || '';
        if (rec[langF]) langSelect.value = rec[langF];
        editor.value = rec[codeF] || '';
        setMobileTitle(rec[nameF] || 'Untitled');
        closeDrawer();
        markActive(currentID);
        loadRevisions(currentID);
      }).catch(function(err) {
        showToast('Load failed: ' + err.message);
      });
    }

    function saveSnippet() {
      var name = (nameInput.value || '').trim();
      var code = editor.value || '';
      if (!name) {
        showToast('Snippet name required');
        nameInput.focus();
        return;
      }
      if (!code) {
        showToast('No code to save');
        editor.focus();
        return;
      }
      var body = {};
      body[idF]   = currentID || '';
      body[nameF] = name;
      body[langF] = langSelect.value || '';
      body[codeF] = code;
      saveBtn.disabled = true;
      fetch(cfg.save_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(rec) {
        if (rec && rec[idF]) currentID = rec[idF];
        else if (rec && rec.id) currentID = rec.id;
        setMobileTitle(name);
        loadList();
        loadRevisions(currentID);
        flashSaved();
      }).catch(function(err) {
        showToast('Save failed: ' + err.message);
      }).then(function() {
        saveBtn.disabled = false;
      });
    }

    function copyEditor() {
      navigator.clipboard.writeText(editor.value || '').then(function() {
        var orig = copyBtn.textContent;
        copyBtn.textContent = 'Copied!';
        copyBtn.classList.add('copied');
        setTimeout(function() {
          copyBtn.textContent = orig;
          copyBtn.classList.remove('copied');
        }, 1200);
      });
    }

    function flashSaved() {
      if (savedTagShown) return;
      savedTagShown = true;
      var tag = el('span', {class: 'ui-cw-saved'}, ['Saved']);
      toolbar.appendChild(tag);
      setTimeout(function() {
        tag.remove();
        savedTagShown = false;
      }, 1500);
    }

    async function deleteSnippet(id) {
      if (!cfg.delete_url) return;
      if (!(await window.uiConfirm('Delete this snippet? This cannot be undone.'))) return;
      var url = cfg.delete_url.replace('{id}', encodeURIComponent(id));
      fetch(url, {method: 'DELETE'}).then(function(r) {
        if (!r.ok && r.status !== 204) {
          return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        }
        if (currentID === id) openSnippet(null);
        loadList();
      }).catch(function(err) {
        showToast('Delete failed: ' + err.message);
      });
    }

    function markActive(id) {
      sideList.querySelectorAll('.ui-chat-side-item').forEach(function(it) {
        it.classList.toggle('active', it.dataset.id === id);
      });
    }

    function loadList() {
      fetchJSON(cfg.list_url).then(function(items) {
        items = items || [];
        // Sort newest-first by date.
        items.sort(function(a, b) {
          return String(b[dateF] || '').localeCompare(String(a[dateF] || ''));
        });
        sideList.innerHTML = '';
        if (!items.length) {
          sideList.appendChild(el('div', {class: 'ui-chat-empty'}, [cfg.empty_text || 'No snippets yet. Click + New or chat with the LLM to generate one.']));
          return;
        }
        items.forEach(function(it) {
          var fullName = it[nameF] || '(untitled)';
          var meta = (it[langF] ? it[langF] + ' · ' : '') + relTime(it[dateF]);
          var row = el('div', {
            class: 'ui-chat-side-item' + (it[idF] === currentID ? ' active' : ''),
            // title is the native browser tooltip — shows full name on
            // hover even when the row's text is ellipsized at the
            // halved width. Includes lang+date so a cursor pause
            // surfaces the same metadata that the legacy meta line
            // used to display inline.
            title: fullName + ' — ' + meta,
            onclick: function(ev) {
              if (ev.target.classList.contains('ui-chat-side-del')) return;
              openSnippet(it[idF]);
            },
          });
          row.dataset.id = String(it[idF] || '');
          var textWrap = el('div', {class: 'ui-chat-side-text'});
          textWrap.appendChild(el('div', {class: 'ui-chat-side-title'}, [fullName]));
          row.appendChild(textWrap);
          if (cfg.delete_url) {
            row.appendChild(el('button', {
              class: 'ui-chat-side-del', title: 'Delete',
              onclick: function(ev) { ev.stopPropagation(); deleteSnippet(it[idF]); },
            }, ['×']));
          }
          sideList.appendChild(row);
        });
      }).catch(function(err) {
        sideList.innerHTML = '';
        sideList.appendChild(el('div', {class: 'ui-chat-empty'}, ['Failed to load: ' + err.message]));
      });
    }

    // --- Modal infrastructure (variables / values / contexts) ---
    // One overlay+container reused across modal types. closeModal()
    // collapses both. Each opener clears the container, fills it with
    // its own content, and shows the overlay.
    // No backdrop-click-to-close: a text-selection drag ending on the
    // backdrop would dismiss mid-copy. Close via button or Escape.
    var modalOverlay = el('div', {class: 'ui-cw-modal-overlay'});
    var modalBox = el('div', {class: 'ui-cw-modal-box'});
    modalOverlay.appendChild(modalBox);
    document.body.appendChild(modalOverlay);
    function openModal()  { modalOverlay.classList.add('open'); }
    function closeModal() {
      modalOverlay.classList.remove('open');
      modalBox.innerHTML = '';
    }
    document.addEventListener('keydown', function(ev) {
      if (ev.key === 'Escape' && modalOverlay.classList.contains('open')) closeModal();
    });

    // --- Reference-collections modal ---
    // Two sections: the Enabled stores as removable pills, and an Available
    // list of everything not yet added — each row carries a "+" that MOVES
    // it out of the list and into the pills. Removing a pill returns it to
    // the available list. Mirrors the inline chip bar (renderCollBar), which
    // is refreshed on every change so the panel stays in sync. The noun is
    // host-supplied (collNoun) so this surface names no specific app.
    function openCollectionsModal() {
      if (!collList.length) { showToast('No collections available'); return; }
      modalBox.innerHTML = '';
      modalBox.appendChild(el('h3', {}, ['Add ' + collNoun]));
      modalBox.appendChild(el('div', {class: 'ui-cw-modal-desc'},
        ['Add the collections this chat should draw on. Enabled stores are searched and the best-matching passages ride along with each message.']));

      modalBox.appendChild(el('div', {class: 'ui-cw-modal-sub'}, ['Enabled']));
      var pillsEl = el('div', {class: 'ui-cw-coll-pills'});
      modalBox.appendChild(pillsEl);
      modalBox.appendChild(el('div', {class: 'ui-cw-modal-sub'}, ['Available']));
      var availEl = el('div', {class: 'ui-cw-list'});
      modalBox.appendChild(availEl);

      function paint() {
        // Enabled pills
        pillsEl.innerHTML = '';
        var anyEnabled = false;
        collList.forEach(function(c){
          if (!collSelected[c.id]) return;
          anyEnabled = true;
          var x = el('span', {class: 'ui-cw-coll-x', title: 'Remove'}, ['×']);
          x.addEventListener('click', function(){ delete collSelected[c.id]; renderCollBar(); paint(); });
          pillsEl.appendChild(el('span', {class: 'ui-cw-coll-chip'}, [(c.name || c.id) + ' ', x]));
        });
        if (!anyEnabled) pillsEl.appendChild(el('span', {class: 'ui-cw-empty', style: 'padding:0.1rem 0;text-align:left'}, ['None enabled yet.']));
        // Available list (everything not enabled), each with a + to add
        availEl.innerHTML = '';
        var anyAvail = false;
        collList.forEach(function(c){
          if (collSelected[c.id]) return;
          anyAvail = true;
          var info = el('div', {class: 'ui-cw-list-info'});
          info.appendChild(el('div', {class: 'ui-cw-list-title'}, [c.name || c.id]));
          if (c.description) info.appendChild(el('div', {class: 'ui-cw-list-meta'}, [c.description]));
          var bits = [];
          if (c.documents != null) bits.push(c.documents + (c.documents === 1 ? ' doc' : ' docs'));
          if (c.chunks != null)    bits.push(c.chunks + ' chunks');
          if (bits.length) info.appendChild(el('div', {class: 'ui-cw-list-meta mono'}, [bits.join(' · ')]));
          var addBtn = el('button', {class: 'ui-cw-list-btn add', type: 'button', title: 'Add'}, ['+']);
          addBtn.addEventListener('click', function(){ collSelected[c.id] = true; renderCollBar(); paint(); });
          availEl.appendChild(el('div', {class: 'ui-cw-list-row'}, [info, addBtn]));
        });
        if (!anyAvail) availEl.appendChild(el('div', {class: 'ui-cw-empty'}, ['All collections added.']));
      }
      paint();

      var doneBtn = el('button', {class: 'ui-row-btn primary'}, ['Done']);
      doneBtn.addEventListener('click', function(){ closeModal(); });
      modalBox.appendChild(el('div', {class: 'ui-cw-modal-btns'}, [doneBtn]));
      openModal();
    }

    // --- Variables modal ---
    // Scans the editor for {{NAME}} placeholders. Two modes: "apply"
    // (substitutes into the editor in-place) and "copy" (substitutes
    // and copies to clipboard, leaving the editor untouched).
    function extractVars(code) {
      var re = /\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}/g;
      var out = [];
      var seen = {};
      var m;
      while ((m = re.exec(code)) !== null) {
        if (!seen[m[1]]) { out.push(m[1]); seen[m[1]] = true; }
      }
      return out;
    }
    var savedVarValues = {};
    function loadValuesForPicker() {
      if (!cfg.values_list_url) return Promise.resolve([]);
      return fetchJSON(cfg.values_list_url).catch(function(){ return []; });
    }
    function openVarsModal(action) {
      action = action === 'copy' ? 'copy' : 'apply';
      var vars = extractVars(editor.value || '');
      if (!vars.length) {
        showToast('No {{VARIABLE}} placeholders in this snippet');
        return;
      }
      modalBox.innerHTML = '';
      modalBox.appendChild(el('h3', {}, [action === 'copy' ? 'Fill variables for copy' : 'Set variables']));
      modalBox.appendChild(el('div', {class: 'ui-cw-modal-desc'},
        ['Each variable can be a static value or a saved value from your library.']));
      var fields = el('div', {class: 'ui-cw-var-inputs'});
      modalBox.appendChild(fields);

      loadValuesForPicker().then(function(values) {
        vars.forEach(function(v) {
          var row = el('div', {class: 'ui-cw-var-row'});
          row.appendChild(el('label', {}, [v]));
          var input = el('input', {type: 'text', value: savedVarValues[v] || ''});
          input.dataset.var = v;
          row.appendChild(input);
          if (values && values.length) {
            var picker = el('select', {class: 'ui-cw-var-picker'});
            picker.appendChild(el('option', {value: ''}, ['— pick from library —']));
            values.forEach(function(val) {
              var label = val.name || '';
              if (val.desc) label += ' (' + val.desc + ')';
              picker.appendChild(el('option', {value: val.value || ''}, [label]));
            });
            picker.addEventListener('change', function() {
              if (picker.value) input.value = picker.value;
            });
            row.appendChild(picker);
          }
          fields.appendChild(row);
        });
        var btns = el('div', {class: 'ui-cw-modal-btns'});
        var cancelBtn = el('button', {class: 'ui-row-btn'}, ['Cancel']);
        cancelBtn.addEventListener('click', closeModal);
        var goBtn = el('button', {class: 'ui-row-btn primary'}, [action === 'copy' ? 'Copy' : 'Apply']);
        goBtn.addEventListener('click', function() {
          var code = editor.value || '';
          var inputs = fields.querySelectorAll('input[data-var]');
          for (var i = 0; i < inputs.length; i++) {
            var n = inputs[i].dataset.var;
            var v = inputs[i].value;
            if (v) {
              savedVarValues[n] = v;
              code = code.split('{{' + n + '}}').join(v);
            }
          }
          if (action === 'copy') {
            navigator.clipboard.writeText(code).then(function() {
              showToast('Copied with substitutions');
            });
          } else {
            editor.value = code;
          }
          closeModal();
        });
        btns.appendChild(cancelBtn);
        btns.appendChild(goBtn);
        modalBox.appendChild(btns);
      });
      openModal();
    }

    // --- Copy editor (with optional variable substitution) ---
    function copyEditorWithVars() {
      // Replaces the simple copyEditor handler when {{NAME}} placeholders
      // are present so the user gets a chance to fill them in before
      // the copy lands on the clipboard.
      var code = editor.value || '';
      if (!code) { showToast('Editor is empty'); return; }
      if (extractVars(code).length > 0) {
        openVarsModal('copy');
        return;
      }
      navigator.clipboard.writeText(code).then(function() {
        var orig = copyBtn.textContent;
        copyBtn.textContent = 'Copied!';
        copyBtn.classList.add('copied');
        setTimeout(function() {
          copyBtn.textContent = orig;
          copyBtn.classList.remove('copied');
        }, 1200);
      });
    }
    // Override the placeholder copyEditor with the variable-aware one.
    copyBtn.onclick = copyEditorWithVars;

    // --- Values library modal ---
    // Saved {name, desc, value} records the user can paste into the
    // editor or pick from inside the variables modal. CRUD via
    // values_list_url + value_url.
    function openValuesModal() {
      if (!cfg.values_list_url) return;
      modalBox.innerHTML = '';
      var hdr = el('h3', {}, ['Values']);
      modalBox.appendChild(hdr);
      var listEl = el('div', {class: 'ui-cw-list'}, ['Loading…']);
      modalBox.appendChild(listEl);
      var btns = el('div', {class: 'ui-cw-modal-btns'});
      var closeBtn = el('button', {class: 'ui-row-btn'}, ['Close']);
      closeBtn.addEventListener('click', closeModal);
      var newBtn = el('button', {class: 'ui-row-btn primary'}, ['+ New']);
      newBtn.addEventListener('click', function(){ editValueModal(null); });
      btns.appendChild(closeBtn);
      btns.appendChild(newBtn);
      modalBox.appendChild(btns);
      openModal();

      fetchJSON(cfg.values_list_url).then(function(items) {
        items = items || [];
        items.sort(function(a, b){ return (a.name || '').localeCompare(b.name || ''); });
        listEl.innerHTML = '';
        if (!items.length) {
          listEl.appendChild(el('div', {class: 'ui-cw-empty'},
            ['No saved values yet. Click + New to add one.']));
          return;
        }
        items.forEach(function(it) {
          var row = el('div', {class: 'ui-cw-list-row'});
          var info = el('div', {class: 'ui-cw-list-info'});
          info.appendChild(el('div', {class: 'ui-cw-list-title'}, [it.name || '(unnamed)']));
          if (it.desc) info.appendChild(el('div', {class: 'ui-cw-list-meta'}, [it.desc]));
          var preview = String(it.value || '');
          if (preview.length > 80) preview = preview.slice(0, 80) + '…';
          info.appendChild(el('div', {class: 'ui-cw-list-meta mono'}, [preview]));
          var editBtn = el('button', {class: 'ui-cw-list-btn'}, ['Edit']);
          editBtn.addEventListener('click', function(){ editValueModal(it); });
          var del = el('button', {class: 'ui-cw-list-btn danger'}, ['×']);
          del.addEventListener('click', async function() {
            if (!(await window.uiConfirm('Delete value "' + (it.name || '') + '"?'))) return;
            var url = cfg.value_url.replace('{id}', encodeURIComponent(it.id));
            fetch(url, {method: 'DELETE'}).then(function(){ openValuesModal(); });
          });
          row.appendChild(info);
          row.appendChild(editBtn);
          row.appendChild(del);
          listEl.appendChild(row);
        });
      }).catch(function(err) {
        listEl.innerHTML = '';
        listEl.appendChild(el('div', {class: 'ui-cw-empty'}, ['Failed to load: ' + err.message]));
      });
    }

    function editValueModal(rec) {
      modalBox.innerHTML = '';
      modalBox.appendChild(el('h3', {}, [rec ? 'Edit value' : 'New value']));
      var nameI = el('input', {type: 'text', value: (rec && rec.name) || '', placeholder: 'Name (e.g. MySQL Prod Password)'});
      var descI = el('input', {type: 'text', value: (rec && rec.desc) || '', placeholder: 'Description (optional)'});
      var valueI = el('input', {type: 'text', class: 'mono', value: (rec && rec.value) || '', placeholder: 'Value'});
      modalBox.appendChild(el('label', {}, ['Name']));
      modalBox.appendChild(nameI);
      modalBox.appendChild(el('label', {}, ['Description']));
      modalBox.appendChild(descI);
      modalBox.appendChild(el('label', {}, ['Value']));
      modalBox.appendChild(valueI);
      var btns = el('div', {class: 'ui-cw-modal-btns'});
      var cancel = el('button', {class: 'ui-row-btn'}, ['Cancel']);
      cancel.addEventListener('click', function(){ openValuesModal(); });
      var save = el('button', {class: 'ui-row-btn primary'}, ['Save']);
      save.addEventListener('click', function() {
        var name = (nameI.value || '').trim();
        if (!name) { nameI.focus(); return; }
        var body = {
          id:    rec && rec.id ? rec.id : '',
          name:  name,
          desc:  (descI.value || '').trim(),
          value: valueI.value || '',
        };
        fetch(cfg.values_list_url, {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(body),
        }).then(function(r) {
          if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
          openValuesModal();
        }).catch(function(err){ showToast('Save failed: ' + err.message); });
      });
      btns.appendChild(cancel);
      btns.appendChild(save);
      modalBox.appendChild(btns);
      openModal();
    }

    // --- Contexts library modal ---
    function openContextsModal() {
      if (!cfg.contexts_list_url) return;
      modalBox.innerHTML = '';
      modalBox.appendChild(el('h3', {}, ['Saved contexts']));
      var listEl = el('div', {class: 'ui-cw-list'}, ['Loading…']);
      modalBox.appendChild(listEl);
      var btns = el('div', {class: 'ui-cw-modal-btns'});
      var closeBtn = el('button', {class: 'ui-row-btn'}, ['Close']);
      closeBtn.addEventListener('click', closeModal);
      btns.appendChild(closeBtn);
      modalBox.appendChild(btns);
      openModal();

      fetchJSON(cfg.contexts_list_url).then(function(items) {
        items = items || [];
        items.sort(function(a, b){ return (a.name || '').localeCompare(b.name || ''); });
        listEl.innerHTML = '';
        if (!items.length) {
          listEl.appendChild(el('div', {class: 'ui-cw-empty'},
            ['No saved contexts. Use Save in the Context section to add one.']));
          return;
        }
        items.forEach(function(it) {
          var row = el('div', {class: 'ui-cw-list-row'});
          var info = el('div', {class: 'ui-cw-list-info'});
          info.appendChild(el('div', {class: 'ui-cw-list-title'}, [it.name || '(unnamed)']));
          if (it.date) info.appendChild(el('div', {class: 'ui-cw-list-meta'}, [relTime(it.date)]));
          info.style.cursor = 'pointer';
          info.addEventListener('click', function(){ loadContext(it.id); });
          var del = el('button', {class: 'ui-cw-list-btn danger'}, ['×']);
          del.addEventListener('click', function(ev) {
            ev.stopPropagation();
            deleteContext(it.id);
          });
          row.appendChild(info);
          row.appendChild(del);
          listEl.appendChild(row);
        });
      }).catch(function(err) {
        listEl.innerHTML = '';
        listEl.appendChild(el('div', {class: 'ui-cw-empty'}, ['Failed to load: ' + err.message]));
      });
    }

    // Initial state.
    loadList();
    // ?snippet=<id> deep-link.
    try {
      var params = new URLSearchParams(window.location.search);
      var sid = params.get('snippet');
      if (sid) openSnippet(sid);
    } catch (_) {}

    return wrap;
  };

