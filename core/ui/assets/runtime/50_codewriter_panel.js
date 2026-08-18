  components.codewriter_panel = function(cfg) {
    var idF   = cfg.id_field   || 'id';
    var nameF = cfg.name_field || 'name';
    var langF = cfg.lang_field || 'lang';
    var codeF = cfg.code_field || 'code';
    var dateF = cfg.date_field || 'date';
    var languages = (cfg.languages && cfg.languages.length)
      ? cfg.languages
      : ['bash','sql','python','powershell','go','markdown','regex',''];

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
    // Standing instructions for the assistant — same shared panel the
    // article editor uses.
    var rulesBtn = cfg.rules_url
      ? el('button', {class: 'ui-row-btn', onclick: function(){ openRulesPanel({url: cfg.rules_url, noun: 'reply'}); }}, ['Rules'])
      : null;
    var valuesBtn = cfg.values_list_url
      ? el('button', {class: 'ui-row-btn', onclick: function(){ openValuesModal(); }}, ['Values'])
      : null;

    // Revision navigation controls — only built when the snippet
    // record has revision-list / revision-load URLs configured. Hidden
    // when no snippet is open (currentID === null) so the toolbar
    // doesn't show "rev 0/0" on a blank New buffer.
    // Revision walk — shared with article_editor via buildRevisionNav
    // (45_document_core.js). Only onLoad is app-shaped: a revision here
    // carries code + name + lang.
    var revNav = buildRevisionNav({
      listURL:  cfg.revisions_list_url,
      loadURL:  cfg.revision_load_url,
      btnClass: 'ui-row-btn ui-cw-rev-btn',
      indicatorClass: 'ui-cw-rev-ind',
      makeClass: 'ui-row-btn ui-cw-rev-mark',
      makeLabel: 'Make Latest',
      makeTitle: 'Save current editor content as a new (latest) revision',
      onMakeCurrent: function(){ saveSnippet(); },
      onLoad: function(rev) {
        docSetValue(rev[codeF] || rev.code || '');
        if (rev[nameF] || rev.name) nameInput.value = rev[nameF] || rev.name || '';
        var l = rev[langF] || rev.lang || '';
        if (l) {
          for (var i = 0; i < langSelect.options.length; i++) {
            if (langSelect.options[i].value === l) { langSelect.selectedIndex = i; break; }
          }
          syncDocViewBtn();
        }
      },
    });
    var revGroup = revNav.group;

    var toolbarKids = [nameInput];
    if (revGroup) toolbarKids.push(revGroup);
    toolbarKids.push(langSelect);
    toolbarKids.push(varsBtn);
    if (valuesBtn) toolbarKids.push(valuesBtn);
    if (rulesBtn) toolbarKids.push(rulesBtn);
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

    // Outline view over the MAIN editor, offered only while the language
    // is markdown. A markdown snippet is a document, and the same
    // sectioned editor that serves other prose surfaces serves it too.
    // Code stays a code textarea: headings mean nothing in bash.
    //
    // As with Context, the textarea remains the SINGLE value carrier —
    // save, chat, variable extraction, copy, and revision navigation all
    // keep reading editor.value untouched, and the outline writes
    // through on every edit.
    var docOutline     = null;
    var docOutlineHost = el('div', {class: 'ui-cw-doc-outline'});
    docOutlineHost.style.display = 'none';
    function docOutlineOn() { return docOutlineHost.style.display !== 'none'; }
    function langIsMarkdown() {
      return String(langSelect.value || '').toLowerCase() === 'markdown';
    }
    var docViewBtn = el('button', {
      class: 'ui-cw-doc-view', type: 'button',
      title: 'Switch between the raw text and a section outline',
    }, ['Outline']);
    docViewBtn.style.display = 'none';
    // docSetValue writes the snippet body from OUTSIDE the editors —
    // loading a snippet or a revision, applying a diff, substituting
    // variables — and keeps whichever view is showing in sync.
    function docSetValue(text) {
      editor.value = text == null ? '' : String(text);
      if (docOutline && docOutlineOn()) docOutline.setValue(editor.value);
    }
    // editorValue reads the live body whichever view is up. The outline
    // writes through on every keystroke so editor.value is already
    // current, but reading the visible editor directly keeps that an
    // implementation detail rather than a thing callers must know.
    function editorValue() {
      if (docOutline && docOutlineOn()) return docOutline.getValue();
      return editor.value || '';
    }
    // docToRaw forces the raw textarea back into view. Called before
    // anything that manipulates the textarea's own visibility (the diff
    // pane hides it and inserts itself where it sat) and whenever the
    // language stops being markdown.
    function docToRaw() {
      if (!docOutlineOn()) return;
      editor.value = docOutline.getValue();
      docOutlineHost.style.display = 'none';
      editor.style.display = '';
      docViewBtn.textContent = 'Outline';
      docViewBtn.classList.remove('on');
      if (docAssistBtn && langIsMarkdown()) docAssistBtn.style.display = '';
    }
    function toggleDocOutline() {
      if (docOutlineOn()) { docToRaw(); return; }
      if (!docOutline) {
        docOutline = buildSectionsEditor({
          allowFree: true,
          initial: editor.value || '',
          onChange: function(text) { editor.value = text; },
          // Per-section drafting, same as the agent editor's prompt
          // outline: asking for one section keeps the rest of the
          // document exactly as written.
          onSuggest: cfg.assist_url ? function(title, body, apply) {
            openDocAssist(title, body, apply);
          } : null,
        });
        docOutlineHost.appendChild(docOutline.node);
      } else {
        docOutline.setValue(editor.value || '');
      }
      editor.style.display = 'none';
      docOutlineHost.style.display = '';
      docViewBtn.textContent = 'Raw';
      docViewBtn.classList.add('on');
      if (docAssistBtn) docAssistBtn.style.display = 'none';
    }
    docViewBtn.addEventListener('click', toggleDocOutline);
    // syncDocViewBtn shows the toggle only for markdown, and drops out of
    // outline mode when the language changes away from it — an outline
    // over a bash script is nonsense, and leaving it up would hide the
    // editor the user is trying to reach.
    // openDocAssist launches the shared draft-with-me workbench for the
    // whole document or one section of it. section === '' means whole.
    // The saved Context rides along as reference material, since it is
    // exactly the background the document is being written against.
    function openDocAssist(section, initial, apply) {
      window.uiOpenAssist({
        title: (nameInput.value || 'Document') + (section ? ' — ' + section : ''),
        subtitle: section
          ? 'Drafting one section. The rest of the document is untouched.'
          : 'Drafting the whole document.',
        initial: initial,
        send: function(req, done) {
          fetch(cfg.assist_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
              name: nameInput.value || '',
              section: section || '',
              message: req.message,
              draft: req.draft,
              context: ctxEditor.value || '',
              history: req.history,
            }),
          }).then(function(r) {
            if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
            return r.json();
          }).then(function(d) {
            done({reply: d && d.reply, value: d && d.value});
          }).catch(function(err) {
            done(null, (err && err.message) || String(err));
          });
        },
        onAccept: apply,
      });
    }

    // Whole-document draft button — RAW mode only, mirroring the agent
    // editor. In the outline the ✨ lives per section, where a targeted
    // revision is possible; raw has no sections to hang one on and the
    // user is working on the document as a whole anyway.
    var docAssistBtn = cfg.assist_url
      ? el('button', {class: 'ui-cw-doc-view', type: 'button',
          title: 'Draft the whole document with assistance'}, ['✨ Draft'])
      : null;
    if (docAssistBtn) {
      docAssistBtn.style.display = 'none';
      docAssistBtn.addEventListener('click', function() {
        openDocAssist('', editorValue(), function(text) { docSetValue(text); });
      });
    }

    // Templates button — same markdown-only visibility as the outline
    // toggle, and hidden entirely when the host declared no templates.
    var tplBtn = ((cfg.templates && cfg.templates.length) || cfg.templates_list_url)
      ? el('button', {class: 'ui-cw-doc-view', type: 'button',
          title: 'Start from a markdown template, or save this one'}, ['Templates'])
      : null;
    if (tplBtn) {
      tplBtn.style.display = 'none';
      tplBtn.addEventListener('click', function(){ openTemplatesModal(); });
    }
    function syncDocViewBtn() {
      if (langIsMarkdown()) {
        docViewBtn.style.display = '';
        if (tplBtn) tplBtn.style.display = '';
        // Outline is the default view for a markdown document: the
        // structure is the point, and someone who opens a runbook wants
        // its sections, not a wall of text with hashes in it. Raw stays
        // one click away and re-asserts itself per document, so a
        // deliberate switch to Raw lasts as long as that document.
        if (!docOutlineOn()) toggleDocOutline();
        // Whole-document draft only makes sense in raw; the outline
        // offers it per section instead.
        if (docAssistBtn) docAssistBtn.style.display = docOutlineOn() ? 'none' : '';
        return;
      }
      docToRaw();
      docViewBtn.style.display = 'none';
      if (tplBtn) tplBtn.style.display = 'none';
      if (docAssistBtn) docAssistBtn.style.display = 'none';
    }
    langSelect.addEventListener('change', syncDocViewBtn);
    // The toolbar was assembled above, before these buttons existed. Park
    // them right after the language select they belong to.
    var docAnchor = langSelect.nextSibling;
    if (docAnchor) toolbar.insertBefore(docViewBtn, docAnchor);
    else toolbar.appendChild(docViewBtn);
    if (tplBtn) {
      if (docAnchor) toolbar.insertBefore(tplBtn, docAnchor);
      else toolbar.appendChild(tplBtn);
    }
    if (docAssistBtn) {
      if (docAnchor) toolbar.insertBefore(docAssistBtn, docAnchor);
      else toolbar.appendChild(docAssistBtn);
    }
    syncDocViewBtn();

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

    // Outline view over the Context block — the same sectioned markdown
    // editor form fields use, reached through a toggle in the Context
    // header. A context that has grown to schemas + conventions + gotchas
    // is a document, and scrolling one textarea to find the API section
    // is the problem this fixes.
    //
    // The textarea stays the SINGLE value carrier. Every existing read
    // (the chat POST, Save, the resizer) keeps using ctxEditor.value
    // untouched, and the outline writes through to it on every edit.
    //
    // Raw is the default: a context usually starts life as a pasted
    // schema dump, and re-shaping someone's paste on arrival is not a
    // favor. No declared skeleton either — what belongs in a reference
    // context is the user's business, so sections here are all free-form.
    var ctxOutline     = null;
    var ctxOutlineHost = el('div', {class: 'ui-cw-ctx-outline'});
    ctxOutlineHost.style.display = 'none';
    function ctxOutlineOn() { return ctxOutlineHost.style.display !== 'none'; }
    // ctxSetValue writes the context from OUTSIDE the editors (loading a
    // saved context, mainly) and keeps whichever view is showing in sync.
    function ctxSetValue(text) {
      ctxEditor.value = text == null ? '' : String(text);
      if (ctxOutline && ctxOutlineOn()) ctxOutline.setValue(ctxEditor.value);
    }
    var ctxViewBtn = el('button', {class: 'ui-cw-ctx-btn', title: 'Switch between the raw text and a section outline',
      onclick: function(ev) { ev.stopPropagation(); toggleCtxOutline(); }}, ['Outline']);
    function toggleCtxOutline() {
      if (ctxOutlineOn()) {
        ctxEditor.value = ctxOutline.getValue();
        ctxOutlineHost.style.display = 'none';
        ctxEditor.style.display = '';
        ctxViewBtn.textContent = 'Outline';
        ctxViewBtn.classList.remove('on');
        return;
      }
      if (!ctxOutline) {
        ctxOutline = buildSectionsEditor({
          allowFree: true,
          initial: ctxEditor.value || '',
          onChange: function(text) { ctxEditor.value = text; },
        });
        ctxOutlineHost.appendChild(ctxOutline.node);
      } else {
        ctxOutline.setValue(ctxEditor.value || '');
      }
      ctxEditor.style.display = 'none';
      ctxOutlineHost.style.display = '';
      ctxViewBtn.textContent = 'Raw';
      ctxViewBtn.classList.add('on');
    }
    // ctxActions was assembled above, before this button existed; put the
    // view toggle at its head so it reads left-to-right as
    // [Outline] [Save] [Load] <current context name>.
    ctxActions.insertBefore(ctxViewBtn, ctxActions.firstChild);
    // Outline is the default here too. A context with no headings shows
    // as one block, which reads exactly like the textarea did, so the
    // flip costs nothing for a pasted schema and pays off the moment the
    // context grows sections. Content is never reshaped unless edited.
    toggleCtxOutline();
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
    var ctxPane    = el('div', {class: 'ui-cw-ctx-pane open'}, [collBar, ctxEditor, ctxOutlineHost]);
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

    var editorWrap = el('div', {class: 'ui-cw-editor-wrap'}, [editor, docOutlineHost, ctxResizer, ctxSection]);

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
        ctxSetValue(rec.body || '');
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

    // Profile picker — "which kind of writer am I talking to", chosen once and
    // carried on every turn. Unlike the reference and collection pickers below
    // it, this is not a per-question attachment: it persists across reloads
    // (localStorage, keyed by the endpoint so two panels on one deployment do
    // not share a selection) because a profile is a standing choice and having
    // to re-pick it every visit would make it feel like one of the checkboxes.
    //
    // core/ui knows only the shape: a list of {id, name, description}, one
    // selection, sent as `profile`. What it MEANS is entirely the host's.
    var profileNoun = cfg.profiles_noun || 'Profile';
    var profileKey  = 'ui-cw-profile:' + (cfg.profiles_list_url || '');
    var pickedProfile = '';
    try { pickedProfile = window.localStorage.getItem(profileKey) || ''; } catch (_) {}
    if (cfg.profiles_list_url) {
      var profSelect = el('select', {class: 'ui-chat-mode',
        title: profileNoun + ' — applies to everything written here',
        onchange: function() {
          pickedProfile = profSelect.value || '';
          try { window.localStorage.setItem(profileKey, pickedProfile); } catch (_) {}
        }});
      profSelect.appendChild(el('option', {value: ''}, ['No ' + profileNoun.toLowerCase()]));
      fetch(cfg.profiles_list_url, {credentials: 'same-origin'})
        .then(function(r){ return r.ok ? r.json() : []; })
        .then(function(list) {
          (list || []).forEach(function(p) {
            profSelect.appendChild(el('option', {value: p.id, title: p.description || ''}, [p.name || p.id]));
          });
          // Re-apply the remembered pick AFTER the options exist — assigning a
          // value to an empty select silently does nothing, which read as the
          // choice being forgotten on every reload.
          if (pickedProfile) {
            profSelect.value = pickedProfile;
            // Gone (deleted, or another user's): fall back to none rather than
            // leaving a blank select that still POSTs a dead id.
            if (profSelect.value !== pickedProfile) {
              pickedProfile = '';
              try { window.localStorage.removeItem(profileKey); } catch (_) {}
            }
          }
        })
        .catch(function(){ profSelect.style.display = 'none'; });
      chatHdr.appendChild(profSelect);
      if (cfg.profiles_manage_url) {
        chatHdr.appendChild(el('a', {class: 'ui-cw-prof-manage', href: cfg.profiles_manage_url,
          title: 'Create and edit ' + profileNoun.toLowerCase() + 's'}, ['Manage']));
      }
    }

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
    // History lives inside docChat (buildDocChat); this side owns only
    // the visible transcript.
    function clearChat() {
      chatMessages.innerHTML = '';
      docChat.clear();
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
    // Chat protocol — shared with article_editor via buildDocChat
    // (45_document_core.js). Rendering stays here: this panel formats
    // fenced code into HTML, which article_editor deliberately doesn't.
    var docChat = buildDocChat({
      url: cfg.chat_url,
      setBusy: setChatBusy,
      appendMsg: function(role, text) {
        if (role === 'user') {
          var prefix = chatLastMode === 'chat' ? '<span class="ui-cw-mode-tag">chat</span> ' : '';
          return addChatMsg('user', prefix + escapeChat(text));
        }
        if (role === 'error') {
          return addChatMsg('assistant', '<span class="ui-cw-err">Error: ' + escapeChat(text) + '</span>');
        }
        return addChatMsg('assistant', formatChatBody(text), text || '');
      },
      thinking: function() {
        return addChatMsg('assistant', '<span class="ui-cw-spinner"></span> Thinking…');
      },
      buildBody: function(message, mode, history) {
        return {
          name:    nameInput.value.trim(),
          lang:    langSelect.value,
          code:    editorValue(),
          context: ctxEditor.value || '',
          collections: pickedCollections(),
          references: selectedRef ? [selectedRef] : [],
          // The standing profile, sent on EVERY turn — including the first of a
          // session and every edit. A foundation that applied only to some turns
          // would be worse than none: the code would follow house conventions
          // intermittently, and nothing in the reply would say which turns had it.
          profile: pickedProfile || '',
          message: message,
          mode:    mode,
          history: history,
        };
      },
      // A regex snippet's "code" is a pattern the diff pane can't
      // usefully review, so those replies stay in the chat.
      proposalOf: function(data, mode) {
        if (mode === 'chat' || data.type !== 'code' || !data.code) return null;
        if (langSelect.value === 'regex') return null;
        return data.code;
      },
      onProposal: function(text) {
        // The diff pane hides the textarea and inserts itself where the
        // textarea sat. Drop out of outline mode first, or the outline
        // stays up covering a review the user can't see or resolve.
        docToRaw();
        if (typeof window.editorShowDiff === 'function') {
          window.editorShowDiff({
            newText: text,
            editorPane: editorWrap,
            editorTextarea: editor,
            onApply: function(t) { docSetValue(t); },
          });
        } else {
          docSetValue(text);
        }
        // No chat bubble: the diff pane already shows the change with
        // its own +N / -M counters, so a prose echo would duplicate it.
      },
    });
    // The user bubble is tagged with the mode that produced it, and
    // appendMsg fires before send() knows the mode, so stash it.
    var chatLastMode = 'edit';
    function sendChat(mode) {
      chatLastMode = mode === 'chat' ? 'chat' : 'edit';
      docChat.send(chatInput.value, chatLastMode);
      chatInput.value = '';
    }

    // --- State ---
    var currentID    = null;
    var savedTagShown = false;

    // Revision state now lives inside revNav (buildRevisionNav);
    // reload/clear on open + save are the whole interface.

    function setMobileTitle(t) { mobileTitle.textContent = t || 'New snippet'; }

    function openSnippet(id) {
      if (id == null) {
        currentID = null;
        nameInput.value = '';
        docSetValue('');
        // langSelect retains the last choice — usually convenient.
        setMobileTitle('New snippet');
        closeDrawer();
        markActive(null);
        revNav.clear();
        return;
      }
      var url = (cfg.load_url || (cfg.list_url + '/{id}')).replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        currentID = rec[idF] || id;
        nameInput.value = rec[nameF] || '';
        if (rec[langF]) langSelect.value = rec[langF];
        // Setting .value in code fires no 'change' event, so the toggle's
        // visibility has to be re-derived by hand after a load.
        syncDocViewBtn();
        docSetValue(rec[codeF] || '');
        setMobileTitle(rec[nameF] || 'Untitled');
        closeDrawer();
        markActive(currentID);
        revNav.reload(currentID);
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
        revNav.reload(currentID);
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


    // Record list — shared with article_editor via buildDocList
    // (45_document_core.js). This panel shows the language in the row
    // tooltip; everything else is the common shape.
    var docList = buildDocList({
      host:       sideList,
      listURL:    cfg.list_url,
      idField:    idF,
      labelField: nameF,
      dateField:  dateF,
      emptyText:  cfg.empty_text || 'No snippets yet. Click + New or chat with the LLM to generate one.',
      metaOf:     function(it){ return it[langF] || ''; },
      currentID:  function(){ return currentID; },
      onOpen:     function(id){ openSnippet(id); },
      deleteURL:  cfg.delete_url,
      deleteConfirm: function(){ return 'Delete this snippet? This cannot be undone.'; },
      onDeleted:  function(id){ if (currentID === id) openSnippet(null); },
    });
    function loadList() { docList.reload(); }
    function markActive(id) { docList.markActive(id); }

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
            docSetValue(code);
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
    // Template picker — shared with article_editor via
    // openTemplatePicker (45_document_core.js), which is built on the
    // standard uiOpenModal rather than this panel's bespoke shell.
    function openTemplatesModal() {
      openTemplatePicker({
        builtins:    cfg.templates || [],
        listURL:     cfg.templates_list_url,
        itemURL:     cfg.template_url,
        currentBody: function(){ return editorValue(); },
        currentName: function(){ return (nameInput.value || '').trim(); },
        onApply: async function(tpl) {
          if ((editorValue() || '').trim() !== '') {
            if (!(await window.uiConfirm('Replace the editor contents with the "' + (tpl.name || 'template') + '" template?'))) return;
          }
          docSetValue(tpl.body || '');
          // A template names the document; seed the snippet name too when
          // the user hasn't given it one, so it isn't born "Untitled".
          if ((nameInput.value || '').trim() === '' && tpl.name) nameInput.value = tpl.name;
        },
      });
    }

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

