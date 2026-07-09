  components.chat_panel = function(cfg) {
    var idF    = cfg.session_id_field     || 'ID';
    var titleF = cfg.session_title_field  || 'Title';
    var lastF  = cfg.session_last_at_field || 'LastAt';
    var msgF   = cfg.session_messages_field || 'Messages';
    // single = one pinned, ongoing conversation: no sessions sidebar,
    // no New / picker / delete. The init path opens the first (and only)
    // session the list returns. Generic — any "one room" surface (an
    // Operator console, an always-on assistant) uses it.
    var single = !!cfg.single;

    var wrap     = el('div', {class: 'ui-chat'});
    var side     = el('div', {class: 'ui-chat-side'});

    // Sidebar collapse — desktop only. Mobile uses the slide-in
    // drawer mechanism and ignores this state. Persisted in
    // localStorage so the operator's preference survives reloads.
    var sideCollapsed = false;
    try { sideCollapsed = localStorage.getItem('chat.sideCollapsed') === '1'; } catch (_) {}
    var collapseBtn = el('button', {
      class: 'ui-tw-collapse', title: 'Hide sessions list',
      onclick: function(){ toggleCollapse(); },
    }, ['‹']);
    function toggleCollapse() {
      sideCollapsed = !sideCollapsed;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      collapseBtn.title = sideCollapsed ? 'Show sessions list' : 'Hide sessions list';
      collapseBtn.textContent = sideCollapsed ? '›' : '‹';
      try { localStorage.setItem('chat.sideCollapsed', sideCollapsed ? '1' : '0'); } catch (_) {}
    }

    var sideSelectBtn = null;
    if (cfg.bulk_select) {
      sideSelectBtn = el('button', {
        class: 'ui-chat-side-btn',
        title: 'Tap items to select multiple',
        onclick: function() {
          bulkState.mode = !bulkState.mode;
          if (!bulkState.mode) {
            Object.keys(bulkSelected).forEach(function(k){ delete bulkSelected[k]; });
          }
          sideSelectBtn.classList.toggle('active', bulkState.mode);
          sideSelectBtn.textContent = bulkState.mode ? '✓ Selecting' : 'Select';
          loadSessions();
        },
      }, ['Select']);
    }
    var leftExtras = [collapseBtn];
    if (sideSelectBtn) leftExtras.push(sideSelectBtn);
    var sideHdrBuilt = renderSideHeader({
      label: 'Sessions',
      newTitle: 'Start a new session',
      onNew:    function(){ openSession(null); },
      onClose:  function(){ closeDrawer(); },
      leftExtras: leftExtras,
    });
    var sideHdr  = sideHdrBuilt.elt;
    var sideList = el('div', {class: 'ui-chat-side-list'}, ['Loading…']);
    var sideSearch = makeSideSearch(sideList);
    side.appendChild(sideHdr);
    side.appendChild(sideSearch);
    side.appendChild(sideList);

    var main = el('div', {class: 'ui-chat-main'});

    var drawer = makeDrawer(side, {
      title:          'New chat',
      hamburgerTitle: 'Sessions',
      newTitle:       'New session',
      onNew:          function(){ openSession(null); },
    });
    var mobileTitle    = drawer.mobileTitle;
    var drawerBackdrop = drawer.backdrop;
    if (!single) main.appendChild(drawer.mobileHdr);

    // Mode-toggles row above the thread (Private, Explorer, etc.).
    // Each pill's state is server-persisted
    // and also rides along on outgoing send bodies so the server
    // honors the active modes.
    // Top bar holds the mode toggles (modesRow) on the left and a
    // helpful "pick a session" hint (emptyHint) pinned to the right.
    // Two children inside one flex container — modesRow's
    // innerHTML='' rebuilds don't touch emptyHint because it's a
    // sibling, not a descendant.
    var modesBar = el('div', {class: 'ui-chat-modesbar'});
    var modesRow = el('div', {class: 'ui-chat-modes'});
    modesBar.appendChild(modesRow);
    main.appendChild(modesBar);

    // Empty-state hint pinned to the right side of the modes bar.
    // Hidden once a session is active or any message lands so it
    // doesn't compete with the toggles for attention mid-conversation.
    var emptyHint = el('div', {class: 'ui-chat-empty-hint'},
      [cfg.empty_text || 'Pick a session from the sidebar or start a new one.']);
    modesBar.appendChild(emptyHint);

    // Tools badge — expandable "N tools" pill that lists every tool
    // the LLM can call. Lazy-loaded on first click; cached for the
    // session so toggling open/closed doesn't re-fetch.
    var toolsBadge = null;
    var toolsPopover = null;
    var toolsLoaded = false;
    var toolsItems = [];
    // fetchTools is declared at the chat-panel function scope (NOT
    // inside the if-block) so toggleMode can call it directly. Under
    // strict mode (top of the runtime), function declarations inside
    // blocks are block-scoped — putting fetchTools inside the
    // tools-url conditional hid it from toggleMode below. No-ops
    // when ToolsURL isn't configured.
    function setBadgeLabel(n) {
      if (toolsBadge) toolsBadge.textContent = '🔧 ' + n + ' tool' + (n === 1 ? '' : 's');
    }
    function renderToolsPopover() {
      if (!toolsPopover) return;
      toolsPopover.innerHTML = '';
      if (!toolsItems.length) {
        toolsPopover.appendChild(el('div', {class: 'ui-chat-tools-loading'}, ['No tools available.']));
        return;
      }
      toolsItems.forEach(function(t) {
        var row = el('div', {class: 'ui-chat-tools-row'});
        row.appendChild(el('div', {class: 'ui-chat-tools-name'}, [t.name || t.Name || '?']));
        var desc = t.desc || t.Desc || t.description || '';
        if (desc) row.appendChild(el('div', {class: 'ui-chat-tools-desc'}, [desc]));
        toolsPopover.appendChild(row);
      });
    }
    // withAgentParam appends agent_id=<id> to a URL when the host
    // page has set window.GOHORT_AGENT_ID. Used by Per-(user, agent)
    // settings so toggle GETs read per-agent overrides. No-op when
    // the global isn't set (single-app pages, surfaces that don't
    // care about agent scoping).
    function withAgentParam(url) {
      var aid = window.GOHORT_AGENT_ID;
      if (!aid || !url) return url;
      var sep = url.indexOf('?') >= 0 ? '&' : '?';
      return url + sep + 'agent_id=' + encodeURIComponent(aid);
    }
    function fetchTools() {
      if (!cfg.tools_url) return Promise.resolve();
      var url = cfg.tools_url;
      var qs = [];
      Object.keys(modeState || {}).forEach(function(k) {
        if (modeState[k]) qs.push(encodeURIComponent(k) + '=true');
      });
      if (qs.length) {
        url += (url.indexOf('?') >= 0 ? '&' : '?') + qs.join('&');
      }
      return fetchJSON(url).then(function(list) {
        toolsLoaded = true;
        toolsItems = Array.isArray(list) ? list : [];
        setBadgeLabel(toolsItems.length);
        if (toolsPopover && toolsPopover.style.display !== 'none') renderToolsPopover();
      }).catch(function(err) {
        if (toolsPopover && toolsPopover.style.display !== 'none') {
          toolsPopover.innerHTML = '<div class="ui-chat-tools-loading">Load failed: ' + err.message + '</div>';
        }
      });
    }
    if (cfg.tools_url) {
      toolsBadge = el('button', {
        class: 'ui-chat-tools-badge',
        title: 'Tools the LLM can use',
        onclick: function() {
          if (toolsPopover.style.display !== 'none') {
            toolsPopover.style.display = 'none';
            return;
          }
          toolsPopover.style.display = '';
          if (toolsLoaded) {
            renderToolsPopover();
            return;
          }
          toolsPopover.innerHTML = '<div class="ui-chat-tools-loading">Loading…</div>';
          fetchTools();
        },
      }, ['🔧 Tools']);
      toolsPopover = el('div', {class: 'ui-chat-tools-popover', style: 'display:none'});
      // Prefetch so the badge shows the count immediately. fetchTools
      // is defined above this block at the chat-panel function scope.
      fetchTools();
      var toolsWrap = el('div', {class: 'ui-chat-tools-wrap'}, [toolsBadge, toolsPopover]);
      modesBar.appendChild(toolsWrap);
      // Outside-click dismiss.
      document.addEventListener('click', function(ev) {
        if (toolsPopover.style.display === 'none') return;
        if (!toolsWrap.contains(ev.target)) toolsPopover.style.display = 'none';
      });
    }

    var thread   = el('div', {class: 'ui-chat-thread'});

    // Cumulative session stats line — declared here, appended to the
    // main column between the thread and input area below.
    var statsBar = el('div', {class: 'ui-chat-stats-bar'});
    statsBar.style.display = 'none';

    var inputArea = el('div', {class: 'ui-chat-input-area'});
    var attachInput = null;
    var attachBtn = null;
    if (cfg.attachments) {
      attachInput = el('input', {type: 'file', accept: '.txt,.log,.json,.yaml,.yml,.md,.conf,text/*', style: 'display:none'});
      attachInput.addEventListener('change', handleAttach);
      attachBtn = el('button', {class: 'ui-chat-iconbtn', title: 'Attach a text file', onclick: function(){ attachInput.click(); }}, ['📎']);
      inputArea.appendChild(attachInput);
      inputArea.appendChild(attachBtn);
    }
    var input  = el('textarea', {class: 'ui-chat-input', rows: '1', placeholder: 'Message…'});
    var prefillBtn = null;
    if (cfg.prefill_url) {
      prefillBtn = el('button', {
        class: 'ui-chat-iconbtn',
        title: cfg.prefill_label || 'Suggest',
        onclick: function() {
          var orig = prefillBtn.textContent;
          prefillBtn.textContent = '…';
          prefillBtn.disabled = true;
          // Default GET; apps that need a POST body declare
          // prefill_method + prefill_body so the runtime doesn't
          // need an app-specific wrapper endpoint.
          var fetchOpts = {};
          if ((cfg.prefill_method || 'GET').toUpperCase() === 'POST') {
            fetchOpts.method = 'POST';
            fetchOpts.headers = {'Content-Type': 'application/json'};
            fetchOpts.body = cfg.prefill_body || '{}';
          }
          fetch(cfg.prefill_url, fetchOpts).then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.text();
          }).then(function(text) {
            // Endpoint may return JSON {topic|text|suggestion} or plain text.
            var t = String(text || '').trim();
            try {
              var j = JSON.parse(t);
              if (j && typeof j === 'object') t = String(j.topic || j.text || j.suggestion || j.message || '').trim();
            } catch (_) {}
            if (t) input.value = t;
          }).catch(function(err) {
            showToast('Suggest failed: ' + err.message);
          }).then(function() {
            prefillBtn.textContent = orig;
            prefillBtn.disabled = false;
          });
        },
      }, [cfg.prefill_label || '✨']);
      inputArea.appendChild(prefillBtn);
    }
    var sendBtn = el('button', {class: 'ui-chat-send', onclick: function(){ doSend(); }}, ['Send']);
    var cancelBtn = el('button', {class: 'ui-chat-cancel', onclick: function(){ doCancel(); }}, ['Cancel']);
    cancelBtn.style.display = 'none';
    inputArea.appendChild(input);
    inputArea.appendChild(cancelBtn);
    inputArea.appendChild(sendBtn);

    main.appendChild(thread);
    main.appendChild(statsBar);
    main.appendChild(inputArea);

    // Floating expand-tab pinned to the left edge of main while the
    // sidebar is collapsed — same pattern techwriter / codewriter
    // use. Single tap re-opens the sessions list.
    var expandTab = el('button', {
      class: 'ui-tw-expand', title: 'Show sessions list',
      onclick: function(){ toggleCollapse(); },
    }, ['›']);

    if (!single) wrap.appendChild(side);
    wrap.appendChild(main);
    if (!single) wrap.appendChild(expandTab);
    if (!single) wrap.appendChild(drawerBackdrop);
    if (single) wrap.classList.add('ui-chat-single');

    // Apply persisted collapse state after the wrap is built.
    if (sideCollapsed) {
      wrap.classList.add('side-collapsed');
      collapseBtn.title = 'Show sessions list';
      collapseBtn.textContent = '›';
    }

    var openDrawer  = drawer.openDrawer;
    var closeDrawer = drawer.closeDrawer;

    // State.
    var currentSessionId = null;
    var history          = [];   // [{role, content}, ...] for /api/send
    var sending          = false;
    var sendController   = null; // AbortController for in-flight SSE
    var modeState        = {};   // bool flags per mode field
    var pendingAttachment = null; // {filename, text}
    // Cumulative session stats — totals across every round in the
    // currently-open session. Reset on session switch / new session.
    var sessionStats = {rounds: 0, in: 0, out: 0, think: 0, ms: 0, cost: 0};
    // --- Mode toggles ----------------------------------------------------
    // Per-button refreshers — collected so we can re-fire all of them
    // when window.GOHORT_AGENT_ID changes (e.g. Agency agent dropdown
    // switches to a different agent and the per-(user, agent) override
    // for the new agent needs to load).
    var modeRefreshers = [];
    function buildModes() {
      modesRow.innerHTML = '';
      modeRefreshers = [];
      (cfg.modes || []).forEach(function(m) {
        var btn = el('button', {
          class: 'ui-chat-mode',
          title: m.title || m.label,
          onclick: function() { toggleMode(m, btn); },
        }, [m.label]);
        modesRow.appendChild(btn);
        var refresh = function() {
          if (!m.get_url) return; // client-only mode (no persisted server state)
          fetchJSON(withAgentParam(m.get_url)).then(function(d) {
            var v = !!(d && d[m.field]);
            modeState[m.send_field || m.field] = v;
            btn.classList.toggle('active', v);
            if (v) fetchTools();
          }).catch(function(){});
        };
        modeRefreshers.push(refresh);
        // Initial state — same fetcher, fired now. Per-(user, agent)
        // scoping: when window.GOHORT_AGENT_ID is set by the host page,
        // withAgentParam appends it so the server returns the per-agent
        // override (falls back to global user-level when no override
        // exists). Lets each agent remember its own Private/Clean stance.
        refresh();
      });
      // ExtraFields are arbitrary form fields (number, select, text)
      // rendered next to the toggles. Their values ride along on
      // every send body so the server sees them as session params.
      (cfg.extra_fields || []).forEach(function(f) {
        var wrap = el('label', {class: 'ui-chat-field'},
          [el('span', {class: 'ui-chat-field-label'}, [f.label || f.name])]);
        var input;
        if (f.type === 'select') {
          input = el('select', {class: 'ui-chat-field-input'});
          (f.options || []).forEach(function(opt) {
            input.appendChild(el('option', {value: opt}, [opt]));
          });
          if (f.default) input.value = f.default;
        } else if (f.type === 'number') {
          input = el('input', {
            type: 'number', class: 'ui-chat-field-input',
            value: f.default || '',
            min: typeof f.min !== 'undefined' ? String(f.min) : '',
            max: typeof f.max !== 'undefined' ? String(f.max) : '',
          });
        } else {
          input = el('input', {type: 'text', class: 'ui-chat-field-input', value: f.default || ''});
        }
        input.dataset.field = f.name;
        wrap.appendChild(input);
        modesRow.appendChild(wrap);
      });
    }

    function toggleMode(m, btn) {
      var key = m.send_field || m.field;
      var next = !modeState[key];
      modeState[key] = next;
      btn.classList.toggle('active', next);
      // Refetch tools immediately off the new local modeState. fetchTools
      // is declared at the chat-panel function scope (above) and is a
      // no-op when cfg.tools_url isn't set — direct call, no indirection.
      fetchTools();
      // Broadcast so app-side decorators (e.g. a toolbar button
      // label) can re-render when a mode flips state. Generic
      // event payload — key is the mode's send_field/field, value is
      // the new bool. Apps that don't care about modes ignore the
      // event entirely.
      window.dispatchEvent(new CustomEvent('ui-agent-mode-change',
        {detail: {key: key, value: next}}));
      if (!m.post_url) return; // client-only mode — local state only, nothing to persist server-side
      var body = {}; body[m.field] = next;
      // Per-(user, agent) scoping — always include the field even
      // when empty so the server-side diagnostic log can distinguish
      // "JS didn't send it" (would be missing) from "JS sent it but
      // the global was empty at click time" (sent as "").
      body.agent_id = window.GOHORT_AGENT_ID || '';
      try {
        console.log('[gohort/mode-toggle] sending', m.field, '=', next,
          'agent_id=', JSON.stringify(window.GOHORT_AGENT_ID || ''));
      } catch (_) {}
      fetchJSON(m.post_url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).catch(function(err) {
        // POST failed — roll back local state and refetch again so the
        // badge re-syncs to the actual server-side flag.
        modeState[key] = !next;
        btn.classList.toggle('active', !next);
        fetchTools();
        showToast('Save failed: ' + err.message);
      });
    }

    // --- Attachments -----------------------------------------------------
    function handleAttach(ev) {
      var f = ev.target.files && ev.target.files[0];
      if (!f) return;
      if (f.size > 1024 * 1024) { showToast('File too large (1MB max)'); return; }
      var reader = new FileReader();
      reader.onload = function() {
        pendingAttachment = {filename: f.name, text: String(reader.result)};
        if (attachBtn) attachBtn.classList.add('active');
        attachBtn.title = 'Attached: ' + f.name + ' (click to remove)';
        attachBtn.onclick = function() {
          pendingAttachment = null;
          attachBtn.classList.remove('active');
          attachBtn.title = 'Attach a text file';
          attachBtn.onclick = function(){ attachInput.click(); };
        };
      };
      reader.readAsText(f);
      ev.target.value = '';
    }

    // --- Sessions sidebar -------------------------------------------------
    var bulkSelected = {}; // id -> true
    var bulkState    = {mode: false};
    // sessionsByID caches the most recent sidebar fetch keyed by id
    // so renderActions can read per-session fields (HasDescendants
    // etc.) without a second round-trip. Populated by every
    // loadSessions() call; renderActions reads it on demand.
    var sessionsByID = {};
    function loadSessions() {
      fetchJSON(cfg.sessions_list_url).then(function(list) {
        sideList.innerHTML = '';
        sessionsByID = {};
        (list || []).forEach(function(s){ if (s && s[idF]) sessionsByID[s[idF]] = s; });
        if (!list || !list.length) {
          if (cfg.bulk_select && bulkState.mode) {
            renderBulkBar([], sideList, bulkState, bulkSelected,
              function(s){ return s[idF]; }, loadSessions, function(){});
          }
          sideList.appendChild(el('div', {class: 'ui-chat-empty', style: 'padding:0.5rem'}, ['No sessions yet.']));
          return;
        }
        list.sort(function(a, b){ return String(b[lastF] || '').localeCompare(String(a[lastF] || '')); });
        var ids = {}; list.forEach(function(s){ ids[s[idF]] = true; });
        Object.keys(bulkSelected).forEach(function(k){ if (!ids[k]) delete bulkSelected[k]; });

        if (cfg.bulk_select) {
          renderBulkBar(list, sideList, bulkState, bulkSelected,
            function(s){ return s[idF]; },
            loadSessions,
            async function() {
              var ids = Object.keys(bulkSelected);
              if (!ids.length) return;
              if (!(await window.uiConfirm('Delete ' + ids.length + ' session(s) permanently?'))) return;
              Promise.all(ids.map(function(id) {
                var url = cfg.session_delete_url.replace('{id}', encodeURIComponent(id));
                return fetchJSON(url, {method: 'DELETE'}).catch(function(){});
              })).then(function() {
                if (bulkSelected[currentSessionId]) openSession(null);
                bulkSelected = {};
                bulkState.mode = false;
                if (sideSelectBtn) {
                  sideSelectBtn.classList.remove('active');
                  sideSelectBtn.textContent = 'Select';
                }
                loadSessions();
              });
            });
        }
        list.forEach(function(s) {
          var inMode = cfg.bulk_select && bulkState.mode;
          var selected = !!bulkSelected[s[idF]];
          var item = el('div', {class:
            'ui-chat-side-item' +
            (s[idF] === currentSessionId ? ' active' : '') +
            (inMode ? ' selectable' : '') +
            (selected ? ' selected' : '')
          }, [
            el('div', {class: 'ui-chat-side-text'}, [
              el('div', {class: 'ui-chat-side-title'}, [
                // Running indicator — pulses while an agent loop is
                // mid-turn for this session. Survives client
                // disconnect (the run keeps running detached), so a
                // user reopening the chat sees at a glance which
                // sessions are still working. Populated by the server
                // from the runs registry; absent / falsy = idle.
                (s.running ? el('span', {
                  class: 'ui-chat-side-running-dot',
                  title: 'This session has an agent run in progress.',
                }) : null),
                s[titleF] || '(untitled)',
              ]),
              el('div', {class: 'ui-chat-side-meta'}, [
                relTime(s[lastF]),
                // Source badge — when the server tags a row with
                // source="<name>" — when an external source contributes the row,
                // surface a small pill with the chat label so the
                // user can tell externally-sourced sessions apart
                // from their own.
                (s.source ? el('span', {class: 'ui-chat-side-source ui-chat-side-source-' + s.source},
                  [s.source + (s.chat_id ? ' · ' + s.chat_id : '')]) : null),
              ]),
            ]),
            inMode ? null : el('button', {
              class: 'ui-chat-side-del', title: 'Delete session',
              onclick: async function(ev){
                ev.stopPropagation();
                if (!(await window.uiConfirm('Delete this session permanently?'))) return;
                var url = cfg.session_delete_url.replace('{id}', encodeURIComponent(s[idF]));
                fetchJSON(url, {method: 'DELETE'}).then(function() {
                  if (currentSessionId === s[idF]) openSession(null);
                  loadSessions();
                }).catch(function(err){ showToast('Delete failed: ' + err.message); });
              },
            }, ['×']),
          ]);
          item.addEventListener('click', function() {
            if (inMode) {
              if (bulkSelected[s[idF]]) delete bulkSelected[s[idF]];
              else bulkSelected[s[idF]] = true;
              loadSessions();
            } else {
              openSession(s[idF]);
              closeDrawer();
            }
          });
          sideList.appendChild(item);
        });
      }).catch(function(err){
        sideList.textContent = 'Failed to load: ' + err.message;
      });
    }

    function openSession(id) {
      currentSessionId = id;
      // Record the surface for this agent's next open (cortex hero vs a session).
      var landAg = window.GOHORT_AGENT_ID || '';
      setLanding(landAg, (id && id === altPinnedSession(landAg)) ? 'cortex' : 'session');
      thread.innerHTML = '';
      history = [];
      // Reset cumulative stats — historical rounds aren't summed
      // (the server doesn't replay token counts on session GET).
      sessionStats = {rounds: 0, in: 0, out: 0, think: 0, ms: 0, cost: 0};
      renderStatsBar();
      if (!id) {
        mobileTitle.textContent = 'New chat';
        emptyHint.style.display = '';
        loadSessions();
        return;
      }
      emptyHint.style.display = 'none';
      var url = cfg.session_load_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(s) {
        var msgs = (s && s[msgF]) || [];
        msgs.forEach(function(m) {
          var role = m.role || m.Role;
          var content = m.content || m.Content;
          var msgEl = appendMessage(role, content);
          history.push({role: role, content: content});
          // Loaded messages are final — render markdown immediately
          // instead of leaving them as plain text. (Streaming chunks
          // can't do this because incomplete markdown corrupts the
          // render; session loads have the full text already.)
          if (role === 'assistant' && msgEl) {
            var body = msgEl.querySelector('.ui-chat-msg-body');
            if (body) renderMessageBody(body, content);
          }
        });
        // Update the mobile header's title to the session's title (or
        // "New chat" when blank). Desktop ignores it via CSS.
        if (s && s[titleF]) mobileTitle.textContent = s[titleF];
        else mobileTitle.textContent = 'Untitled';
        scrollToBottom();
        loadSessions();
      }).catch(function(err){
        thread.appendChild(el('div', {class: 'ui-chat-error'}, ['Failed to load: ' + err.message]));
      });
    }

    // --- Thread rendering -------------------------------------------------
    function appendMessage(role, content) {
      // Any message in the thread means the user is no longer at the
      // empty state — hide the "pick a session" hint.
      if (emptyHint) emptyHint.style.display = 'none';
      var msg = el('div', {class: 'ui-chat-msg ' + (role === 'assistant' ? 'assistant' : 'user')});
      var body = el('div', {class: 'ui-chat-msg-body'});
      body.textContent = content || '';
      msg.appendChild(body);
      msg.dataset.role = role;
      msg.dataset.raw  = content || '';
      thread.appendChild(msg);
      // User messages get Edit + Retry actions once they're committed.
      // Skip during streaming (the assistant placeholder uses
      // appendMessage too but for role="assistant" — it gets its
      // actions later in the done handler).
      if (role === 'user' && content) addUserActions(msg);
      scrollToBottom();
      return msg;
    }

    // Edit + Retry on the LAST user message in the thread. Older user
    // messages are read-only — editing mid-history would require
    // re-running every assistant turn after it, which is closer to
    // "fork session" than "edit". Retry on a non-final user message
    // would also need to drop everything after it, which the Retry
    // helper already does.
    function addUserActions(msgEl) {
      var bar = el('div', {class: 'ui-chat-actions'});
      bar.appendChild(el('button', {class: 'ui-chat-act', onclick: function() {
        editUserMessage(msgEl);
      }}, ['Edit']));
      bar.appendChild(el('button', {class: 'ui-chat-act', title: 'Re-send this message and drop the response below', onclick: function() {
        retryUserMessage(msgEl);
      }}, ['Retry']));
      msgEl.appendChild(bar);
      // After appending a user msg with actions, prune actions from
      // any earlier user messages — only the most recent one should
      // be editable. Older ones become read-only.
      var msgs = thread.querySelectorAll('.ui-chat-msg.user');
      for (var i = 0; i < msgs.length - 1; i++) {
        var older = msgs[i].querySelector('.ui-chat-actions');
        if (older) older.remove();
      }
    }

    function editUserMessage(msgEl) {
      var raw = msgEl.dataset.raw || msgEl.querySelector('.ui-chat-msg-body').textContent;
      var body = msgEl.querySelector('.ui-chat-msg-body');
      var actions = msgEl.querySelector('.ui-chat-actions');
      // Replace body with textarea + Save/Cancel.
      body.style.display = 'none';
      if (actions) actions.style.display = 'none';
      var ta = el('textarea', {class: 'ui-chat-edit-ta', rows: '3'});
      ta.value = raw;
      var save = el('button', {class: 'ui-chat-act', onclick: function() {
        var newText = ta.value.trim();
        if (!newText) return;
        // Replace the message text in DOM + history, then drop every
        // turn after this one and re-send. The send will append a
        // fresh assistant placeholder.
        msgEl.dataset.raw = newText;
        body.textContent = newText;
        truncateAfter(msgEl, /*inclusive*/ true);
        // history: drop entries from this user message onward.
        for (var i = history.length - 1; i >= 0; i--) {
          if (history[i].role === 'user' && history[i].content === raw) {
            history.length = i;
            break;
          }
        }
        input.value = newText;
        doSend();
      }}, ['Save & resend']);
      var cancel = el('button', {class: 'ui-chat-act', onclick: function() {
        editBar.remove();
        body.style.display = '';
        if (actions) actions.style.display = '';
      }}, ['Cancel']);
      var editBar = el('div', {class: 'ui-chat-edit-bar'}, [ta, el('div', {class: 'ui-chat-edit-actions'}, [save, cancel])]);
      msgEl.appendChild(editBar);
      ta.focus();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    }

    function retryUserMessage(msgEl) {
      var raw = msgEl.dataset.raw || msgEl.querySelector('.ui-chat-msg-body').textContent;
      truncateAfter(msgEl, /*inclusive*/ true);
      for (var i = history.length - 1; i >= 0; i--) {
        if (history[i].role === 'user' && history[i].content === raw) {
          history.length = i;
          break;
        }
      }
      input.value = raw;
      doSend();
    }

    // Remove every DOM sibling after msgEl. Inclusive removes msgEl
    // itself too. Used by edit/retry to wipe the current turn before
    // re-running.
    function truncateAfter(msgEl, inclusive) {
      var n = inclusive ? msgEl : msgEl.nextElementSibling;
      while (n) {
        var next = n.nextElementSibling;
        if (n.parentNode === thread) n.remove();
        n = next;
      }
    }
    // Tool calls don't render inline anymore — instead the runtime
    // attaches an expandable "🔧 N tools" button to the assistant
    // bubble that fired them. Click to reveal a panel with every
    // tool call + result for that round. The bubble-attaching work
    // happens inline in the SSE switch (case 'tool_call'/'tool_result')
    // because it needs assistantMsg, which is var-declared inside
    // doSend's closure. The helpers below only manipulate detached
    // pending arrays + render the panel from a tools[] reference
    // already attached to a message element.
    function renderToolPanel(panel) {
      var tools = panel.parentNode && panel.parentNode.tools || [];
      panel.innerHTML = '';
      tools.forEach(function(t) {
        var summaryChildren = [el('span', {class: 'ui-chat-tool-name'}, ['→ ' + t.name])];
        if (t.args) summaryChildren.push(el('span', {class: 'ui-chat-tool-args'}, [t.args]));
        var summary = el('summary', {class: 'ui-chat-tool-summary'}, summaryChildren);
        var det = el('details', {class: 'ui-chat-tool'});
        det.appendChild(summary);
        var body = el('div', {class: 'ui-chat-tool-body'});
        // Full structured arguments — server-sent unclipped so the
        // user can see the actual command / script / parameters when
        // expanded, not just the 60-char-per-value summary chip. One
        // labeled <pre> per key, pretty-printed JSON for objects /
        // arrays. Only shown when argsFull is present (older bubbles
        // without it just fall through to the output block).
        if (t.argsFull && typeof t.argsFull === 'object') {
          var keys = Object.keys(t.argsFull);
          if (keys.length > 0) {
            keys.sort();
            var argBox = el('div', {class: 'ui-chat-tool-argblock'});
            keys.forEach(function(k) {
              var v = t.argsFull[k];
              var rendered;
              if (typeof v === 'string') {
                rendered = v;
              } else {
                try { rendered = JSON.stringify(v, null, 2); }
                catch (e) { rendered = String(v); }
              }
              var row = el('div', {class: 'ui-chat-tool-argrow'});
              row.appendChild(el('span', {class: 'ui-chat-tool-argkey'}, [k]));
              row.appendChild(el('pre', {class: 'ui-chat-tool-argval'}, [rendered]));
              argBox.appendChild(row);
            });
            body.appendChild(argBox);
          }
        }
        var trimmed = String(t.output || '').trim();
        if (t.output === null) {
          body.appendChild(el('div', {class: 'ui-chat-tool-empty'}, ['(running…)']));
        } else if (!trimmed) {
          body.appendChild(el('div', {class: 'ui-chat-tool-empty'}, ['(no output)']));
        } else {
          var pre = el('pre', {class: 'ui-chat-tool-result'});
          pre.textContent = t.output;
          body.appendChild(pre);
        }
        det.appendChild(body);
        panel.appendChild(det);
      });
    }
    // attachToolToggle creates the toggle+panel button on a given
    // assistant message element and points it at that bubble's
    // tools[] array. Called from inside the SSE switch where the
    // bubble is in scope. Idempotent — reuses the existing toggle
    // when called again to refresh the count.
    // chatAttachmentBox returns (creating on first call) the per-
    // message block-level container that holds inline file + image
    // attachments produced by tools (attach_file / generate_image /
    // view_image). The container is appended LAST so attachments
    // always sit below the assistant text body and the tool toggle —
    // without it, the inline-block file link can land next to the
    // collapsed tool toggle on the same line because there's no
    // intervening block element when the panel is display:none.
    function chatAttachmentBox(msgEl) {
      var box = msgEl.querySelector(':scope > .ui-chat-attachments');
      if (!box) {
        box = el('div', {class: 'ui-chat-attachments'});
        msgEl.appendChild(box);
      } else {
        // Re-append so it stays at the BOTTOM when a tool toggle is
        // added after the first attachment arrived (the tool toggle
        // is appended via msgEl.appendChild and would otherwise jump
        // ahead of the box).
        msgEl.appendChild(box);
      }
      return box;
    }
    function attachToolToggle(msgEl) {
      if (!msgEl || !msgEl.tools) return;
      var count = msgEl.tools.length;
      var label = '🔧 ' + count + ' tool' + (count === 1 ? '' : 's');
      var toggle = msgEl.querySelector(':scope > .ui-chat-tools-toggle');
      var panel  = msgEl.querySelector(':scope > .ui-chat-tools-panel');
      if (toggle) {
        toggle.textContent = label;
        // Re-render an open panel so newly-arrived tool_result data
        // replaces the "(running…)" placeholders for finished tools.
        if (panel && panel.style.display !== 'none') renderToolPanel(panel);
        return;
      }
      panel = el('div', {class: 'ui-chat-tools-panel', style: 'display:none'});
      toggle = el('button', {
        class: 'ui-chat-tools-toggle',
        onclick: function() {
          var open = panel.style.display !== 'none';
          panel.style.display = open ? 'none' : '';
          toggle.classList.toggle('open', !open);
          if (!open) renderToolPanel(panel);
        },
      }, [label]);
      msgEl.appendChild(toggle);
      msgEl.appendChild(panel);
      // Keep any pre-existing attachment box at the very bottom of
      // the message so it visually trails the tool toggle/panel
      // rather than jumping above them.
      var attachBox = msgEl.querySelector(':scope > .ui-chat-attachments');
      if (attachBox) msgEl.appendChild(attachBox);
    }
    function appendError(msg) {
      thread.appendChild(el('div', {class: 'ui-chat-error'}, [msg]));
      scrollToBottom();
    }

    // Inject / remove a three-dot typing indicator in an assistant
    // bubble's body. Used while the LLM is thinking + prefilling
    // before the first chunk arrives, so the empty bubble doesn't
    // sit there looking dead. Cleared by clearTyping(body) on the
    // first chunk OR by direct replacement when content lands.
    function showTyping(body) {
      if (!body || body.querySelector('.ui-chat-typing')) return;
      body.innerHTML = '<span class="ui-chat-typing" aria-label="Thinking"><span></span><span></span><span></span></span>';
    }
    function clearTyping(body) {
      if (!body) return;
      var t = body.querySelector('.ui-chat-typing');
      if (t) body.innerHTML = '';
    }
    function scrollToBottom() {
      // Defer to the next frame so layout has settled — scrollTop set
      // synchronously right after appendChild lands at the OLD
      // scrollHeight, before the browser has measured the new node.
      // Run twice (rAF + 100ms timeout) to also catch the case where a
      // markdown re-render or <details> expansion changes height after
      // the first frame.
      var pin = function() { thread.scrollTop = thread.scrollHeight; };
      pin();
      requestAnimationFrame(pin);
      setTimeout(pin, 100);
    }

    // Per-round stats footer, attached to the assistant bubble that
    // just finished. Mirrors legacy chat's appendStatsFooter.
    function renderRoundStats(msgEl, stats) {
      if (!msgEl || !stats) return;
      if (!stats.output_tokens && !stats.input_tokens && !stats.elapsed_ms) return;
      var parts = [];
      if (stats.tokens_per_sec) parts.push(stats.tokens_per_sec.toFixed(1) + ' tk/s');
      if (stats.prompt_per_sec) parts.push(Math.round(stats.prompt_per_sec) + ' prefill');
      if (stats.elapsed_ms)     parts.push((stats.elapsed_ms / 1000).toFixed(1) + 's');
      if (stats.input_tokens)   parts.push(stats.input_tokens.toLocaleString() + ' in');
      if (stats.output_tokens)  parts.push(stats.output_tokens.toLocaleString() + ' out');
      if (stats.reasoning_tokens) parts.push(stats.reasoning_tokens.toLocaleString() + ' think');
      if (stats.est_cost && stats.est_cost > 0) parts.push('$' + Number(stats.est_cost).toFixed(4));
      if (!parts.length) return;
      var bar = el('div', {class: 'ui-chat-round-stats'}, [parts.join(' · ')]);
      msgEl.appendChild(bar);
    }

    // Cumulative session totals, redrawn after every round so the
    // operator sees running spend without visiting the admin cost page.
    function accumulateStats(stats) {
      if (!stats) return;
      sessionStats.rounds += 1;
      sessionStats.in    += Number(stats.input_tokens || 0);
      sessionStats.out   += Number(stats.output_tokens || 0);
      sessionStats.think += Number(stats.reasoning_tokens || 0);
      sessionStats.ms    += Number(stats.elapsed_ms || 0);
      sessionStats.cost  += Number(stats.est_cost || 0);
      renderStatsBar();
    }
    function renderStatsBar() {
      if (sessionStats.rounds === 0) {
        statsBar.style.display = 'none';
        return;
      }
      var parts = [];
      parts.push(sessionStats.rounds + (sessionStats.rounds === 1 ? ' round' : ' rounds'));
      if (sessionStats.in)    parts.push(sessionStats.in.toLocaleString()    + ' in');
      if (sessionStats.out)   parts.push(sessionStats.out.toLocaleString()   + ' out');
      if (sessionStats.think) parts.push(sessionStats.think.toLocaleString() + ' think');
      if (sessionStats.ms)    parts.push((sessionStats.ms / 1000).toFixed(1) + 's');
      if (sessionStats.cost > 0) parts.push('$' + sessionStats.cost.toFixed(4));
      statsBar.textContent = 'session: ' + parts.join(' · ');
      statsBar.style.display = '';
    }

    // --- Send + SSE handling ---------------------------------------------
    function doSend() {
      if (sending) return;
      var text = input.value.trim();
      if (!text) return;
      input.value = '';
      autoresize();
      sending = true;
      sendBtn.disabled = true;
      cancelBtn.style.display = '';

      // Append user msg + assistant placeholder immediately.
      appendMessage('user', text);
      history.push({role: 'user', content: text});
      var assistantMsg = appendMessage('assistant', '');
      var assistantBody = assistantMsg.querySelector('.ui-chat-msg-body');
      // Show a typing indicator until the first chunk arrives. This is
      // the thinking-then-prefill window where the user is otherwise
      // staring at an empty bubble. The indicator gets replaced in
      // place when the first chunk (or tool_call event) lands.
      showTyping(assistantBody);
      var fullReply = '';

      sendController = new AbortController();
      // Build the send body — base fields plus active mode flags
      // (private_mode etc) plus an attached-file fence if pending.
      var bodyMessage = text;
      if (pendingAttachment) {
        var fence = '```';
        bodyMessage = text + '\n\n' +
          fence + '\n# attached: ' + pendingAttachment.filename + '\n' +
          pendingAttachment.text + '\n' + fence;
        pendingAttachment = null;
        if (attachBtn) {
          attachBtn.classList.remove('active');
          attachBtn.title = 'Attach a text file';
          attachBtn.onclick = function(){ attachInput.click(); };
        }
      }
      var sendBody = {session_id: currentSessionId, history: history.slice(0, -1), message: bodyMessage};
      Object.keys(modeState).forEach(function(k){ sendBody[k] = modeState[k]; });
      // Snapshot ExtraFields values into the send body. Numbers
      // get coerced; selects/text stay strings. Empty values are
      // dropped so the server sees absent rather than "".
      modesRow.querySelectorAll('[data-field]').forEach(function(inp) {
        var name = inp.dataset.field;
        if (!name) return;
        var v = inp.value;
        if (v === '' || v == null) return;
        if (inp.type === 'number') {
          var n = Number(v);
          if (!isNaN(n)) v = n;
        }
        sendBody[name] = v;
      });

      fetch(cfg.send_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        signal: sendController.signal,
        body: JSON.stringify(sendBody),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        var reader = r.body.getReader();
        var decoder = new TextDecoder();
        var buffer = '';
        function pump() {
          return reader.read().then(function(out) {
            if (out.done) { finish(); return; }
            buffer += decoder.decode(out.value, {stream: true});
            // Parse SSE: each event terminated by a blank line.
            var i;
            while ((i = buffer.indexOf('\n\n')) >= 0) {
              var raw = buffer.slice(0, i);
              buffer = buffer.slice(i + 2);
              processEvent(raw);
            }
            return pump();
          });
        }
        return pump();
      }).catch(function(err) {
        if (err.name === 'AbortError') {
          appendError('Cancelled.');
        } else {
          appendError('Error: ' + err.message);
        }
        finish();
      });

      function processEvent(raw) {
        var lines = raw.split('\n');
        var ev = '', dataStr = '';
        for (var li = 0; li < lines.length; li++) {
          var l = lines[li];
          if (l.indexOf('event:') === 0) ev = l.slice(6).trim();
          else if (l.indexOf('data:') === 0) dataStr += l.slice(5).trim();
        }
        if (!ev) return;
        var data = {};
        if (dataStr) { try { data = JSON.parse(dataStr); } catch(e) {} }
        switch (ev) {
          case 'chunk':
            // If the previous round was tool-only, its placeholder
            // got dropped on the prior done event. Recreate one at
            // the bottom of the thread so this round's text lands
            // BELOW any tool pills/results — not above them where
            // the original placeholder used to sit.
            if (!assistantMsg || !assistantMsg.parentNode) {
              assistantMsg = appendMessage('assistant', '');
              assistantBody = assistantMsg.querySelector('.ui-chat-msg-body');
              showTyping(assistantBody);
            }
            // First chunk replaces the typing indicator. Subsequent
            // chunks just append to the running reply text.
            if (fullReply === '') clearTyping(assistantBody);
            fullReply += data.text || '';
            assistantBody.textContent = fullReply;
            scrollToBottom();
            break;
          case 'thinking_chunk':
            // Stage 1: ignore. Stage 2 will surface in a collapsible block.
            break;
          case 'tool_call':
            // Round boundary detection: if the current bubble has
            // finalized text content from a PREVIOUS round, this
            // tool_call is the start of a NEW round. Mint a fresh
            // bubble for it (with a spinner) instead of stacking the
            // new tool onto the prior round's settled card. Without
            // this, tools accumulate on the last-text-bearing card
            // forever, even across distinct rounds — confusing,
            // because the chips visually attach to text they have
            // nothing to do with.
            if (assistantMsg && assistantMsg.parentNode && fullReply !== '') {
              assistantMsg = null;
              fullReply = '';
            }
            // Tool calls can fire BEFORE the first chunk. Mint a host
            // assistant bubble on demand so the toggle has a parent.
            if (!assistantMsg || !assistantMsg.parentNode) {
              assistantMsg = appendMessage('assistant', '');
              assistantBody = assistantMsg.querySelector('.ui-chat-msg-body');
              showTyping(assistantBody);
            }
            if (!assistantMsg.tools) assistantMsg.tools = [];
            assistantMsg.tools.push({
              name: data.name || 'tool',
              args: data.args || '',
              // Unclipped structured args, shown in the expanded
              // details body. Server sends as a map; we keep it raw
              // and let renderToolPanel format on display so a long
              // command_template value (e.g. "python3
              // /opt/gohort/data/workspaces/foo/script.py …") is
              // fully visible instead of clipped to 60 chars.
              argsFull: data.args_full || null,
              output: null,
            });
            attachToolToggle(assistantMsg);
            // Auto-expand on the first tool call of this bubble so
            // the user sees what the agent is doing without having to
            // hunt for the toggle. Once the user collapses it manually
            // we leave it alone — only the initial state is opinionated.
            if (assistantMsg.tools.length === 1) {
              var toolsPanel = assistantMsg.querySelector(':scope > .ui-chat-tools-panel');
              var toolsToggle = assistantMsg.querySelector(':scope > .ui-chat-tools-toggle');
              if (toolsPanel && toolsToggle && toolsPanel.style.display === 'none') {
                toolsPanel.style.display = '';
                toolsToggle.classList.add('open');
                renderToolPanel(toolsPanel);
              }
            }
            // Tool toggle changes layout — keep the bottom visible
            // so the user can see the latest activity in flight.
            scrollToBottom();
            break;
          case 'tool_result':
            // Match against the last entry without an output — tools
            // run sequentially within a round, so last-unmatched is
            // the right pair. Chat emits the body under "result";
            // other apps may use "output". Accept either.
            if (assistantMsg && assistantMsg.tools) {
              var resultText = data.result;
              if (resultText == null) resultText = data.output;
              for (var ti = assistantMsg.tools.length - 1; ti >= 0; ti--) {
                if (assistantMsg.tools[ti].output === null) {
                  assistantMsg.tools[ti].output = String(resultText || '');
                  break;
                }
              }
              attachToolToggle(assistantMsg); // refresh count + label
              scrollToBottom();
            }
            break;
          case 'session':
            if (data.id) currentSessionId = data.id;
            break;
          case 'status':
            // Light status line (e.g. "Investigating…"). Append as a
            // mute system-style line.
            thread.appendChild(el('div', {class: 'ui-chat-status'}, [data.text || '']));
            scrollToBottom();
            break;
          case 'image':
            // Inline image from a tool (generate_image, view_image, or
            // attach_file routing an image-MIME). Rendered into the
            // current assistant bubble; lifetime is tied to the
            // streaming response (no persistence today).
            if (data.data) {
              if (!assistantMsg || !assistantMsg.parentNode) {
                assistantMsg = appendMessage('assistant', '');
                assistantBody = assistantMsg.querySelector('.ui-chat-msg-body');
              }
              var img = el('img', {
                src: 'data:image/png;base64,' + data.data,
                class: 'ui-chat-attached-image',
              });
              chatAttachmentBox(assistantMsg).appendChild(img);
              scrollToBottom();
            }
            break;
          case 'file':
            // Generic file from attach_file. Rendered as an inline
            // download link in the assistant bubble; data URI lets the
            // browser download without a round-trip. As with images,
            // not persisted today — the link disappears on session
            // reload.
            if (data.data && data.name) {
              if (!assistantMsg || !assistantMsg.parentNode) {
                assistantMsg = appendMessage('assistant', '');
                assistantBody = assistantMsg.querySelector('.ui-chat-msg-body');
              }
              var mt = data.mime_type || 'application/octet-stream';
              var sizeStr = '';
              if (data.size) {
                var n = data.size;
                if (n >= 1048576) sizeStr = (n/1048576).toFixed(1) + ' MB';
                else if (n >= 1024) sizeStr = (n/1024).toFixed(1) + ' KB';
                else sizeStr = n + ' B';
              }
              var fileLink = el('a', {
                class: 'ui-chat-attached-file',
                href: 'data:' + mt + ';base64,' + data.data,
                download: data.name,
              }, ['📎 ' + data.name + (sizeStr ? ' (' + sizeStr + ')' : '')]);
              chatAttachmentBox(assistantMsg).appendChild(fileLink);
              scrollToBottom();
            }
            break;
          case 'error':
            appendError(data.message || 'Error');
            break;
          case 'done':
            // Stats arrive even on tool-only rounds where the LLM
            // produced no visible content. Always update cumulative
            // totals; only attach the per-round footer when there's
            // actually a finalized message.
            accumulateStats(data);
            if (fullReply.trim()) {
              history.push({role: 'assistant', content: fullReply});
              assistantMsg.dataset.raw = fullReply;
              renderMessageBody(assistantBody, fullReply);
              renderRoundStats(assistantMsg, data);
              addAssistantActions(assistantMsg);
              // Jump to the TOP of the just-finalized assistant
              // message so the user can read from the start instead
              // of landing at the bottom (where the streaming had
              // pinned the scroll). Use rect deltas because the
              // thread is the scroll container, not the document.
              // Defer past finalize's own scrollTranscript pins.
              var msgEl = assistantMsg;
              var jumpToTop = function() {
                if (!msgEl || !msgEl.parentNode) return;
                var mRect = msgEl.getBoundingClientRect();
                var tRect = thread.getBoundingClientRect();
                thread.scrollTop += (mRect.top - tRect.top);
              };
              setTimeout(jumpToTop, 50);
              setTimeout(jumpToTop, 200);
              fullReply = '';
              assistantMsg = null;
              assistantBody = null;
            } else if (assistantMsg && assistantBody && assistantBody.textContent === '' && (!assistantMsg.tools || !assistantMsg.tools.length)) {
              // Tool-only round with no tool toggle either — drop the
              // empty placeholder so the user doesn't see a blank
              // assistant bubble between rounds. Keep it if tools fired
              // (rare: tool_call without a paired result yet) so the
              // toggle stays attached to its bubble.
              assistantMsg.remove();
              assistantMsg = null;
              assistantBody = null;
            } else if (assistantMsg && assistantBody) {
              // Tool-only round but the bubble HAS a tools toggle —
              // clear the typing indicator so the bubble shows just
              // the toggle and any later text from the next round
              // continues into a fresh placeholder.
              clearTyping(assistantBody);
              assistantMsg = null;
              assistantBody = null;
            }
            break;
        }
      }

      function finish() {
        // Final cleanup. The done-handler now drops empty placeholders
        // proactively, so by the time we get here assistantMsg should
        // either be null or have real content. Belt-and-suspenders:
        // remove anything still empty, finalize anything with text.
        if (assistantMsg && assistantBody) {
          if (assistantBody.textContent === '' && fullReply === '') {
            assistantMsg.remove();
          } else if (fullReply.trim() && !assistantMsg.dataset.raw) {
            history.push({role: 'assistant', content: fullReply});
            assistantMsg.dataset.raw = fullReply;
            renderMessageBody(assistantBody, fullReply);
            addAssistantActions(assistantMsg);
          }
        }
        sending = false;
        sendBtn.disabled = false;
        cancelBtn.style.display = 'none';
        sendController = null;
        loadSessions();
        // Let a host surface (e.g. a WorkbenchPanel) know a round finished — its
        // co-author tool may have written into the open document.
        try { window.dispatchEvent(new CustomEvent('ui-chat-round-done')); } catch (e) {}
      }
    }

    function doCancel() {
      if (sendController) sendController.abort();
    }

    function addAssistantActions(msgEl) {
      var bar = el('div', {class: 'ui-chat-actions'});
      bar.appendChild(el('button', {class: 'ui-chat-act', onclick: function() {
        var t = msgEl.dataset.raw || '';
        if (navigator.clipboard) navigator.clipboard.writeText(t);
        showToast('Copied');
      }}, ['Copy']));
      bar.appendChild(el('button', {class: 'ui-chat-act', title: 'Drop this reply and re-run from the prior user message', onclick: function() {
        retryFromMessage(msgEl);
      }}, ['Retry']));
      msgEl.appendChild(bar);
      // Fire the SHARED message-decorator registry so app-side affordances
      // (e.g. the workbench's "Add to document" co-author button) attach to
      // chat_panel replies too — agent_loop_panel already fires these; the
      // registry is meant to cover both panels uniformly.
      var decorators = window.UIMessageDecorators || [];
      for (var di = 0; di < decorators.length; di++) {
        try {
          decorators[di]({
            role:    'assistant',
            id:      msgEl.dataset.id || '',
            wrap:    msgEl,
            body:    msgEl.querySelector('.ui-chat-msg-body'),
            rawText: msgEl.dataset.raw || '',
          });
        } catch (_) {}
      }
    }

    // Walk back from msgEl to the most recent user message, drop everything
    // after it, then re-send that user text. Useful when an assistant reply
    // missed the mark and you want a fresh try.
    function retryFromMessage(msgEl) {
      // Find the user message immediately before this assistant turn.
      var prev = msgEl.previousElementSibling;
      while (prev && !(prev.classList && prev.classList.contains('ui-chat-msg') && prev.dataset.role === 'user')) {
        prev = prev.previousElementSibling;
      }
      if (!prev) { showToast('No prior user message to retry'); return; }
      var userText = prev.dataset.raw || prev.querySelector('.ui-chat-msg-body').textContent;
      // Remove everything from prev (inclusive) onward in the DOM.
      var n = prev;
      while (n) { var next = n.nextElementSibling; n.remove(); n = next; }
      // Trim history back to before that user turn.
      while (history.length > 0 && !(history[history.length-1].role === 'user' && history[history.length-1].content === userText)) {
        history.pop();
      }
      if (history.length > 0) history.pop(); // drop the matching user entry — doSend will re-add
      input.value = userText;
      doSend();
    }

    // Minimal markdown renderer for completed assistant messages.
    // Streaming chunks render as plain text (textContent) until done,
    // then this re-renders the bubble with formatting. Handles
    // headings, code fences, inline code, bold, italic, links, lists.
    // mdToHTML is a top-level helper shared with pipeline_panel.
    function renderMessageBody(target, raw) {
      if (!cfg.markdown) { target.textContent = raw; return; }
      uiRenderMarkdown(target, raw);
    }

    // --- Input ergonomics -------------------------------------------------
    function autoresize() {
      input.style.height = 'auto';
      input.style.height = Math.min(input.scrollHeight, 200) + 'px';
    }
    input.addEventListener('input', autoresize);
    input.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) {
        ev.preventDefault();
        doSend();
      }
    });

    buildModes();
    if (single) {
      // One pinned thread: open the first (and only) session the list
      // returns. No New / picker / delete chrome to load.
      fetchJSON(cfg.sessions_list_url).then(function(list) {
        var first = (list && list.length) ? list[0] : null;
        openSession(first ? first[idF] : null);
      }).catch(function(){ openSession(null); });
    } else {
      loadSessions();
    }

    // When the host page swaps the active agent (Agency dropdown,
    // or any surface that updates window.GOHORT_AGENT_ID + dispatches
    // 'gohort-agent-id-changed'), refresh every mode button's state
    // so toggles reflect the new agent's per-agent override (not the
    // stale previous agent's value).
    window.addEventListener('gohort-agent-id-changed', function() {
      modeRefreshers.forEach(function(fn) { fn(); });
    });

    return wrap;
  };

  // Agent-loop panel — sessions sidebar (left), conversation
  // pane (center), live activity pane (right). The conversation
  // and activity panes are resizable. Built for any app that
  // drives an LLM agent through multi-turn tool use with
  // operator-in-the-loop confirmation. See AgentLoopPanel doc
  // comment in components.go for the SSE protocol.
