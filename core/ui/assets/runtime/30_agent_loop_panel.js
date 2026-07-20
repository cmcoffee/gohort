  components.agent_loop_panel = function(cfg) {
    var idF   = cfg.id_field       || 'ID';
    var ttlF  = cfg.title_field    || 'Title';
    var atF   = cfg.date_field     || 'LastAt';
    var msgsF = cfg.messages_field || 'Messages';

    // The left rail is opt-in. Apps that don't supply list/load/
    // delete URLs get a single-column panel (no sidebar).
    var hasList = !!(cfg.list_url && cfg.load_url && cfg.delete_url);

    // Alternate-nav mode (domain-agnostic): for designated agents the host
    // app can replace the session list with a fixed nav and pin the panel to
    // ONE ongoing thread per agent. The app names a JS global (alt_nav_flag)
    // that maps each opted-in agent id to its pinned session id. core/ui
    // hardcodes neither the global name nor the session-id scheme.
    var altNavFlag = cfg.alt_nav_flag || '';
    function altNavAgents() { return (altNavFlag && window[altNavFlag]) || null; }
    function isAltNavAgent(agentId) { var m = altNavAgents(); return !!(m && agentId && m[agentId]); }
    // The alt-nav global maps agentId -> pinned session id, so each alt-nav
    // agent resumes its OWN ongoing thread (core/ui stays agnostic of the
    // app's session-id scheme). Empty string for non-alt-nav agents.
    function altPinnedSession(agentId) { var m = altNavAgents(); return (m && agentId && m[agentId]) || ''; }
    // Last surface this agent was on — so opening it later lands the same way: its
    // standing thread (cortex/home) → the cortex; a session → a NEW session.
    // Per-agent, browser-local (a landing preference, not synced state).
    function landingKey(agentId) { return 'gohort_landing_' + (agentId || ''); }
    function getLanding(agentId) { try { return localStorage.getItem(landingKey(agentId)) || ''; } catch (e) { return ''; } }
    function setLanding(agentId, surface) { try { localStorage.setItem(landingKey(agentId), surface); } catch (e) {} }

    var activeSessionId = '';
    // Channel-thread live polling: a watched channel is fed server-side (from
    // the messaging surface), so we poll its session to append new inbound
    // messages + the agent's replies while it's open. Timer + the count of
    // messages already on screen.
    var channelPollTimer = null, channelPollCount = 0;
    // cortexObsSeen — keys of observation cards already on screen in the open
    // cortex home thread, so its live poll appends only NEW ones (channel
    // messages / scheduled reports / monitor wakes) without re-rendering.
    var cortexObsSeen = {};
    // channelTranscript — non-null while a CHANNEL ROOM session is open
    // (id "chan:<chatID>"). It holds the sender labels so the thread reads
    // as a messaging transcript (contact name + agent name above each line),
    // not the anonymous you/assistant bubbles a web session uses. Reset to
    // null on every session open so it never leaks into a plain session.
    // {contact: <conversation partner>, agent: <bound agent's name>}.
    var channelTranscript = null;
    // currentAgentLabel reads the agent picker's selected option text so a
    // channel transcript can label the assistant side by the bound agent's
    // actual name (the panel serves all agents via the picker, so there's no
    // static name). Falls back to a generic label.
    function currentAgentLabel() {
      var sel = document.querySelector('.ui-agent-extras select[name="agent_id"], .ui-agent-extras-label select');
      if (sel && sel.selectedIndex >= 0) {
        var t = (sel.options[sel.selectedIndex].text || '').trim();
        if (t) return t;
      }
      return 'Assistant';
    }
    // activeRunId — server-issued run identifier for the current
    // in-flight turn. Captured from the kind=run event the server
    // emits right after the session event. Used to (a) address
    // /api/runs/<id>/cancel from the cancel button when
    // cfg.runs_url_base is set, and (b) subscribe to the run's
    // stream after a reconnect.
    var activeRunId = '';
    // runSeqReceived — counter of real SSE events delivered to
    // handleEvent for the current run. Sent as ?since=<n> on
    // /api/runs/<id>/stream reconnect so the server replays only
    // what was missed during the gap. Reset on session change or
    // when a new run starts.
    var runSeqReceived = 0;
    // sessionSources — populated when the rail renders. Maps each
    // session ID to its {source, chat_id} when the row comes from an
    // external ExtraSessionsSource (see core/session_sources.go). openSession
    // appends those as query params so the server can route the
    // load to the right per-source scope.
    var sessionSources = {};
    // activeContextId — used in CONTEXT mode for the left-rail's
    // active record (workspace, project, etc.). Distinct from
    // activeSessionId, which still tracks the server-issued chat
    // session for cancel/confirm routing.
    var activeContextId = '';
    var msgEls = {};      // message id -> {bubble, body, role, rawText}
    var activityEls = {}; // activity id -> element
    var blockEls = {};    // app-block id -> {wrap, body}
    var pendingAttachments = []; // {name, dataURL} for next send
    var pendingMessageExtras = {}; // app-supplied fields to merge into the next send body (one-shot, cleared after send)
    var messageReplayHooks = []; // app-registered fn(bubble, msg) called after each replayed message
    var activeStream = null;     // AbortController for in-flight send
    // Bulk-select state — wired when cfg.bulk_select is true. Tracks
    // which session ids are checked and whether the "Select" toggle
    // is engaged. loadSessions re-reads both on every render.
    var bulkSelected = {};
    var bulkState    = {mode: false};

    var wrap = el('div', {class: 'ui-agent' + (hasList ? '' : ' ui-agent-no-list')});

    // --- Optional list sidebar -------------------------------------------
    var side = null, sideList = null, sideSearch = null, drawer = null, navEl = null, sideHdrEl = null, orchView = null, lastSessionTitle = '';
    // Channel/fleet "Manage ▾" control — built in the rail block (where the nav
    // machinery is in scope), shown in the topbar actions for fleet agents.
    var manageControl = null, manageBtn = null, manageDot = null, pinnedEl = null;
    // Only one top-bar dropdown (Manage ▾ / the grouped toolbar menus) is open
    // at a time. openTopbarMenu holds the close-fn of whatever is currently
    // open; opening another closes it first. Each menu registers its own
    // closer on open and clears it on close.
    var openTopbarMenu = null;
    function setOpenTopbarMenu(closeFn) {
      if (openTopbarMenu && openTopbarMenu !== closeFn) { try { openTopbarMenu(); } catch (_) {} }
      openTopbarMenu = closeFn;
    }
    function clearOpenTopbarMenu(closeFn) {
      if (openTopbarMenu === closeFn) openTopbarMenu = null;
    }
    function closeDrawer() {
      if (!side) return;
      side.classList.remove('open');
      if (drawer) drawer.backdrop.classList.remove('show');
    }
    if (hasList) {
      side = el('div', {class: 'ui-chat-side'});
      // Collapse button — desktop. Hamburger icon sits next to
      // the New button. Mobile uses the drawer mechanism (×).
      var collapseBtn = el('button', {
        class: 'ui-agent-collapse',
        title: 'Hide ' + (cfg.list_title || 'list'),
        onclick: function(){ toggleSideCollapse(); },
      }, ['☰']);
      // Secondary sidebar actions (Mark all read, Select) live behind ONE "⋯"
      // overflow so they don't crowd (and overlap) the "Sessions" title — the
      // header reads just "⋯ · + New". The menu is built whenever at least one
      // secondary action exists; each app opts into its members (mark_all_read_url
      // / bulk_select).
      var leftExtras = [collapseBtn];
      var moreMenu = el('div', {class: 'ui-side-menu', style: 'display:none'});
      function closeMoreMenu() { moreMenu.style.display = 'none'; }
      var moreItemCount = 0;
      if (cfg.mark_all_read_url) {
        moreMenu.appendChild(el('button', {class: 'ui-side-menu-item', onclick: function() {
          closeMoreMenu();
          fetch(substituteExtras(cfg.mark_all_read_url), {method: 'POST'})
            .then(function() { loadSessions(); })
            .catch(function(err) { console.error('mark all read failed: ' + err.message); });
        }}, ['Mark all read']));
        moreItemCount++;
      }
      // Select toggle — bulk-select entry point, now a "⋯" menu item (was a
      // standalone header pill). Toggling off clears any prior selection so the
      // next entry starts fresh. sideSelectBtn stays the element reference the
      // auto-exit reset (further below) updates. Only when the app opted in.
      var sideSelectBtn = null;
      if (cfg.bulk_select) {
        sideSelectBtn = el('button', {
          class: 'ui-side-menu-item', title: 'Tap items to select multiple',
          onclick: function() {
            closeMoreMenu();
            bulkState.mode = !bulkState.mode;
            if (!bulkState.mode) {
              Object.keys(bulkSelected).forEach(function(k){ delete bulkSelected[k]; });
            }
            sideSelectBtn.classList.toggle('active', bulkState.mode);
            sideSelectBtn.textContent = bulkState.mode ? '✓ Selecting' : 'Select';
            loadSessions();
          },
        }, ['Select']);
        moreMenu.appendChild(sideSelectBtn);
        moreItemCount++;
      }
      if (moreItemCount > 0) {
        var moreBtn = el('button', {class: 'ui-chat-side-btn', title: 'More actions',
          onclick: function(ev) {
            ev.stopPropagation();
            moreMenu.style.display = (moreMenu.style.display === 'none') ? 'block' : 'none';
          }}, ['⋯']);
        // Any click outside the menu closes it.
        document.addEventListener('click', closeMoreMenu);
        leftExtras.push(el('div', {class: 'ui-side-menu-wrap'}, [moreBtn, moreMenu]));
      }
      var sideHdrBuilt = renderSideHeader({
        label:    cfg.list_title || 'Sessions',
        className: 'ui-chat-side-h',
        newTitle: cfg.new_label || 'New',
        onNew:    function(){ openSession(null); },
        onClose:  function(){ closeDrawer(); },
        // Alternate new-session modes (cfg.new_variants) — each opens a
        // fresh session and arms its extras onto the FIRST send, so the
        // server stamps the choice at session creation (e.g. incognito).
        // pendingMessageExtras rides one send then clears, which is
        // exactly creation-time scope — later turns need no re-arming.
        newVariants: (cfg.new_variants || []).map(function(v) {
          return {
            label: v.label, title: v.title,
            onSelect: function() {
              openSession(null);
              if (v.extras) {
                Object.keys(v.extras).forEach(function(k) {
                  pendingMessageExtras[k] = v.extras[k];
                });
              }
            },
          };
        }),
        // Hamburger inserted BEFORE the New button (left of it)
        // via leftExtras — matches the user's "next to new" ask.
        leftExtras: leftExtras,
      });
      sideList = el('div', {class: 'ui-chat-side-list'}, ['Loading…']);
      sideSearch = makeSideSearch(sideList);
      side.appendChild(sideHdrBuilt.elt);
      side.appendChild(sideSearch);
      side.appendChild(sideList);

      // --- Orchestrator sidebar nav (operator-mode only) -----------------
      // For agents the host app opts in (via the alt_nav_flag global), swap
      // the session list for cfg.orchestrator_nav. Strictly gated — every
      // other agent is untouched and keeps its session list. Renders the nav
      // + hides sessions; non-chat items overlay the main pane with a table.
      sideHdrEl = sideHdrBuilt.elt;
      // primaryEl — the "Channel" hero row: the agent's main/home thread, pinned
      // at the very TOP of the rail (above Permissions and the session header) and
      // styled distinctly so it doesn't read as just another session. Content is
      // filled by loadSessions, which knows the home thread + its unread state.
      var primaryEl = el('div', {style: 'display:none;padding:0.5rem 0.5rem 0.35rem'});
      side.insertBefore(primaryEl, sideHdrEl);
      // channelsEl — the Channels rail SECTION: a distinct region with its own
      // header + Add control, listing the agent's messaging-channel bindings
      // ABOVE the session list (not mixed into it). Filled by loadChannels;
      // hidden when the app didn't opt in (no channels_url).
      var channelsEl = el('div', {class: 'ui-channels-rail', style: 'display:none'});
      side.insertBefore(channelsEl, sideHdrEl);
      // schedulesEl — the Schedules rail SECTION: the agent's own event monitors
      // + scheduled runs, listed above the session list. Filled by loadSchedules;
      // hidden when the app didn't opt in (no schedules_url) or the agent has none.
      var schedulesEl = el('div', {class: 'ui-channels-rail', style: 'display:none'});
      side.insertBefore(schedulesEl, sideHdrEl);
      var orchBtns = [];
      var orchBadges = [];
      function renderOrchTable(rows, item, reload) {
        orchView.innerHTML = '';
        if (!rows || !rows.length) {
          orchView.appendChild(el('div', {style: 'color:var(--text-mute, #999);padding:0.5rem'}, ['Nothing here yet.']));
          return;
        }
        // Card layout (item.layout === 'cards') — a COMPACT one-row-per-entry
        // list (like Claude Desktop's permission settings): title + inline muted
        // details + a Status pill on the left, the segmented state control and
        // action buttons on the right. Wraps to a second line only when narrow.
        // openRowPicker backs a row action with a picker_source: fetch a list of
        // {value,label} choices and show them in a modal; picking one POSTs the
        // action URL with the chosen value, then reloads. Shared by the cards +
        // table renderers below.
        function openRowPicker(a, row) {
          var agent = window.GOHORT_AGENT_ID || '';
          var src = a.picker_source + (a.picker_source.indexOf('?') >= 0 ? '&' : '?') + 'agent=' + encodeURIComponent(agent);
          window.uiOpenSimpleModal({title: a.picker_title || a.label, width: '420px', mount: function(body, dlg) {
            var status = el('div', {style: 'color:var(--text-mute,#999);font-size:0.85rem;padding:0.3rem 0'}, ['Loading…']);
            var list = el('div', {style: 'display:flex;flex-direction:column;gap:0.35rem;margin-top:0.4rem'});
            body.appendChild(status); body.appendChild(list);
            fetch(src, {credentials: 'same-origin'})
              .then(function(r) { return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t); }); })
              .then(function(opts) {
                status.remove();
                if (!opts || !opts.length) { list.appendChild(el('div', {style: 'color:var(--text-mute,#999)'}, ['No options available.'])); return; }
                opts.forEach(function(opt) {
                  var b = el('button', {type: 'button', class: 'ui-row-btn', style: 'text-align:left', onclick: function() {
                    var u = a.url + '?id=' + encodeURIComponent(row._id) + '&agent=' + encodeURIComponent(agent) + '&value=' + encodeURIComponent(opt.value);
                    b.disabled = true;
                    fetch(u, {method: a.method || 'POST', credentials: 'same-origin'})
                      .then(function(r) { if (!r.ok) return r.text().then(function(t){ throw new Error(t); }); })
                      .then(function() { try { dlg.close(); } catch(e){} if (reload) reload(); })
                      .catch(function(err) { b.disabled = false; list.appendChild(el('div', {style: 'color:var(--danger,#e5484d);font-size:0.8rem'}, ['Failed: ' + err.message])); });
                  }}, [opt.label || opt.value]);
                  list.appendChild(b);
                });
              })
              .catch(function(err) { status.textContent = 'Failed to load: ' + err.message; });
          }});
        }
        if (item && item.layout === 'cards') {
          var cactions = (item && item.row_actions) || [];
          var ckeys = Object.keys(rows[0]).filter(function(k) { return k.charAt(0) !== '_'; });
          rows.forEach(function(row) {
            var card = el('div', {style: 'display:flex;align-items:center;gap:0.6rem;border:1px solid var(--border, rgba(127,127,127,0.25));border-radius:7px;padding:0.45rem 0.7rem;margin-bottom:0.4rem;background:var(--bg-1, rgba(127,127,127,0.03));flex-wrap:wrap'});
            // Left: title + status pill + inline muted details, all on one line.
            var info = el('div', {style: 'flex:1 1 11rem;min-width:0;display:flex;align-items:baseline;gap:0.45rem;flex-wrap:wrap'});
            ckeys.forEach(function(k, ki) {
              var v = row[k];
              var s = (v == null) ? '' : String(v);
              if (!s) return;
              if (ki === 0) {
                info.appendChild(el('span', {style: 'font-weight:600;font-size:0.9rem'}, [s]));
              } else if (k === 'Status') {
                var pend = /pending/i.test(s);
                info.appendChild(el('span', {style: 'font-size:0.56rem;text-transform:uppercase;letter-spacing:0.04em;padding:0.05rem 0.42rem;border-radius:999px;font-weight:700;align-self:center;' +
                  (pend ? 'background:var(--accent, #4a9eff);color:#fff' : 'background:var(--bg-2, rgba(127,127,127,0.22));color:var(--text-mute, #999)')}, [s]));
              } else {
                info.appendChild(el('span', {style: 'color:var(--text-mute, #999);font-size:0.78rem;word-break:break-word'}, [s]));
              }
            });
            card.appendChild(info);
            // Right: segmented state control (rows that carry it) + actions.
            var controls = el('div', {style: 'display:flex;align-items:center;gap:0.4rem;flex:0 0 auto;flex-wrap:wrap'});
            if (item.state_field && (item.state_options || []).length && row[item.state_field] != null) {
              var seg = el('div', {style: 'display:inline-flex;border:1px solid var(--border, rgba(127,127,127,0.35));border-radius:6px;overflow:hidden'});
              (item.state_options || []).forEach(function(opt, oi) {
                var active = String(row[item.state_field]) === String(opt.value);
                var segBtn = el('button', {type: 'button',
                  style: 'padding:0.22rem 0.6rem;border:none;' + (oi ? 'border-left:1px solid var(--border, rgba(127,127,127,0.35));' : '') + 'cursor:pointer;font:inherit;font-size:0.73rem;white-space:nowrap;' +
                    (active ? 'background:var(--accent, #4a9eff);color:#fff;font-weight:600' : 'background:transparent;color:var(--text-mute, #999)'),
                  onclick: function(ev) {
                    if (ev) ev.stopPropagation();
                    if (active) return;
                    var u = opt.url + '?id=' + encodeURIComponent(row._id) + '&agent=' + encodeURIComponent(window.GOHORT_AGENT_ID || '') + '&value=' + encodeURIComponent(opt.value);
                    fetch(u, {method: opt.method || 'POST'}).then(function() { if (reload) reload(); }).catch(function(err) { console.error('state set failed: ' + err.message); });
                  }}, [opt.label]);
                seg.appendChild(segBtn);
              });
              controls.appendChild(seg);
            }
            cactions.forEach(function(a) {
              if (a.only_if && !row[a.only_if]) return;
              if (a.hide_if && row[a.hide_if]) return;
              var cls = 'ui-row-btn compact';
              if (a.variant) cls += ' ' + a.variant;
              var btn = el('button', {type: 'button', class: cls, onclick: async function(ev) {
                if (ev) ev.stopPropagation();
                if (a.confirm && window.uiConfirm && !(await window.uiConfirm(a.confirm))) return;
                if (a.picker_source) { openRowPicker(a, row); return; }
                var rowURL = a.url + '?id=' + encodeURIComponent(row._id) + '&agent=' + encodeURIComponent(window.GOHORT_AGENT_ID || '');
                fetch(rowURL, {method: a.method || 'POST'})
                  .then(function() { if (reload) reload(); })
                  .catch(function(err) { console.error('row action failed: ' + err.message); });
              }}, [a.label]);
              controls.appendChild(btn);
            });
            if (controls.childNodes.length) card.appendChild(controls);
            orchView.appendChild(card);
          });
          return;
        }
        // Columns = the row's keys minus any "_"-prefixed (hidden, e.g. _id).
        var cols = Object.keys(rows[0]).filter(function(k) { return k.charAt(0) !== '_'; });
        var actions = (item && item.row_actions) || [];
        var tbl = el('table', {style: 'width:100%;border-collapse:collapse;font-size:0.9rem'});
        var hr = el('tr');
        cols.forEach(function(c) {
          hr.appendChild(el('th', {style: 'text-align:left;padding:0.35rem 0.5rem;border-bottom:1px solid var(--border, rgba(127,127,127,0.3));color:var(--text-mute, #999)'}, [c]));
        });
        if (actions.length) hr.appendChild(el('th', {style: 'border-bottom:1px solid var(--border, rgba(127,127,127,0.3))'}, ['']));
        tbl.appendChild(hr);
        rows.forEach(function(row) {
          // Which columns hold long / multi-line content. If any, the whole ROW
          // is click-to-expand (one expander per line, not per field): clicking
          // it reveals a detail line below with the full content.
          var longCols = cols.filter(function(c) {
            var s = String(row[c] == null ? '' : row[c]);
            return s.length > 80 || s.indexOf('\n') >= 0;
          });
          var tr = el('tr', longCols.length ? {style: 'cursor:pointer'} : {});
          cols.forEach(function(c) {
            var v = row[c];
            var s = (v == null) ? '' : String(v);
            if (s.length > 80 || s.indexOf('\n') >= 0) {
              s = s.replace(/\n/g, ' ');
              if (s.length > 80) s = s.slice(0, 80) + '…';
            }
            tr.appendChild(el('td', {style: 'padding:0.35rem 0.5rem;border-bottom:1px solid var(--border, rgba(127,127,127,0.15));vertical-align:top'}, [s]));
          });
          if (actions.length) {
            var cell = el('td', {style: 'padding:0.35rem 0.5rem;border-bottom:1px solid var(--border, rgba(127,127,127,0.15));white-space:nowrap'});
            actions.forEach(function(a) {
              // Conditional row actions: skip when only_if field is falsy or
              // hide_if field is truthy (e.g. show Pause only when not paused,
              // Resume only when paused).
              if (a.only_if && !row[a.only_if]) return;
              if (a.hide_if && row[a.hide_if]) return;
              var cls = 'ui-row-btn compact';
              if (a.variant) cls += ' ' + a.variant;
              var btn = el('button', {type: 'button', class: cls, style: 'margin-right:0.3rem', onclick: async function(ev) {
                if (ev) ev.stopPropagation(); // don't toggle the row expand
                if (a.confirm && window.uiConfirm && !(await window.uiConfirm(a.confirm))) return;
                if (a.picker_source) { openRowPicker(a, row); return; }
                // Stamp the in-view agent so per-agent row actions (e.g. History
                // turn-scrub) target the right agent's thread, not the default.
                var rowURL = a.url + '?id=' + encodeURIComponent(row._id) + '&agent=' + encodeURIComponent(window.GOHORT_AGENT_ID || '');
                fetch(rowURL, {method: a.method || 'POST'})
                  .then(function() { if (reload) reload(); })
                  .catch(function(err) { console.error('row action failed: ' + err.message); });
              }}, [a.label]);
              cell.appendChild(btn);
            });
            tr.appendChild(cell);
          }
          tbl.appendChild(tr);
          if (longCols.length) {
            var dtr = el('tr', {style: 'display:none'});
            var dtd = el('td', {colspan: String(cols.length + (actions.length ? 1 : 0)), style: 'padding:0.3rem 0.6rem 0.7rem;border-bottom:1px solid var(--border, rgba(127,127,127,0.15));background:var(--bg-2, rgba(127,127,127,0.06))'});
            longCols.forEach(function(c) {
              dtd.appendChild(el('div', {style: 'color:var(--text-mute, #999);font-size:0.8rem;margin:0.4rem 0 0.15rem'}, [c]));
              dtd.appendChild(el('pre', {style: 'white-space:pre-wrap;margin:0;font-size:0.82rem;max-height:340px;overflow:auto;background:var(--bg-1, rgba(127,127,127,0.1));padding:0.45rem;border-radius:4px'}, [String(row[c] == null ? '' : row[c])]));
            });
            dtr.appendChild(dtd);
            tbl.appendChild(dtr);
            tr.onclick = function() {
              dtr.style.display = dtr.style.display === 'none' ? '' : 'none';
            };
          }
        });
        orchView.appendChild(tbl);
      }
      // orchSourceURL pins a nav source/action fetch to the agent currently
      // in view. Data views like History are per-agent on the server (they key
      // off ?agent=…); without this the fetch omits the param and the server
      // falls back to its default agent, so a non-default channel's History
      // always renders empty. Mirrors the action_url agent-stamping below.
      function orchSourceURL(src) {
        if (!src) return src;
        return src + (src.indexOf('?') >= 0 ? '&' : '?') + 'agent=' + encodeURIComponent(window.GOHORT_AGENT_ID || '');
      }
      // openHomeThread lands on the agent's home thread — a pinned session in
      // the normal list, not a nav row (channel model: the home thread is just
      // a session). Used on entering a channel agent and after a channel-wide
      // action. Non-channel agents have no pinned thread, so it's a no-op there.
      function openHomeThread() {
        var hs = altPinnedSession(window.GOHORT_AGENT_ID);
        if (hs) openSession(hs);
      }
      function selectOrchNav(idx) {
        var item = (cfg.orchestrator_nav || [])[idx] || {};
        // Action items are buttons (clear / decommission): POST to the URL
        // for the current agent after an optional confirm, then refresh.
        if (item.action_url) {
          (async function() {
            if (item.confirm && window.uiConfirm && !(await window.uiConfirm(item.confirm))) return;
            var url = item.action_url + (item.action_url.indexOf('?') >= 0 ? '&' : '?') + 'agent=' + encodeURIComponent(window.GOHORT_AGENT_ID || '');
            fetch(url, {method: 'POST'})
              .then(function() { refreshChannelBadges(); closeDrawer(); openHomeThread(); })
              .catch(function(err) { console.error('channel action failed: ' + err.message); });
          })();
          return;
        }
        orchBtns.forEach(function(b, i) {
          var on = (i === idx);
          var navItem = (cfg.orchestrator_nav || [])[i] || {};
          if (navItem.pinned) {
            // Pinned rail rows show "selected" as the accent border (matching
            // Master Control); background is left to refreshChannelBadges (its
            // pending tint) so the two signals don't fight.
            b.style.border = on ? '1px solid var(--accent, #4a9eff)' : '1px solid transparent';
          } else {
            b.style.background = on ? 'var(--bg-2, rgba(127,127,127,0.18))' : 'transparent';
            b.style.fontWeight = on ? '600' : '400';
          }
        });
        // On mobile the sidebar is a slide-in drawer that sits ABOVE the content
        // overlay (z-index 30 vs 5), so close it after a selection — otherwise
        // the chosen view (the orchView table, or the channel chat) renders
        // hidden behind the still-open drawer. No-op on desktop.
        closeDrawer();
        if (!orchView) return;
        if (item.source) {
          // A data view (Permissions / Enabled agents / …): overlay the chat
          // pane with the fetched table. The overlay now covers the chat, so
          // de-accent the Master Control hero — the selected indicator belongs
          // to the nav row whose view is showing, not the underlying thread.
          orchView.style.display = '';
          var heroBtn = primaryEl && primaryEl.querySelector('.ui-channel-hero');
          // De-accent the SELECTED state (border + strong bg) but keep the
          // Cortex's persistent faint gold tint — it's the standing-thread
          // marker, not a selection cue.
          if (heroBtn) { heroBtn.style.border = '1px solid transparent'; heroBtn.style.background = 'rgba(217,184,108,0.07)'; }
          // On mobile, start the overlay BELOW the header bar so the ☰ stays
          // uncovered and tappable (otherwise inset:0 paints over it and there's
          // no way back). Desktop has no mobile header — pin to the top.
          orchView.style.top = (drawer && window.innerWidth <= 700) ? (drawer.mobileHdr.offsetHeight + 'px') : '0';
          // Reflect the loaded view in the mobile header (e.g. "Authorizations")
          // instead of leaving the stale session title.
          if (drawer && drawer.mobileTitle) drawer.mobileTitle.textContent = item.label || '';
          orchView.textContent = 'Loading…';
          var reload = function() { selectOrchNav(idx); };
          fetch(orchSourceURL(item.source)).then(function(r) { return r.ok ? r.json() : []; })
            .then(function(rows) { renderOrchTable(rows, item, reload); })
            .catch(function(err) { orchView.textContent = 'Failed to load: ' + err.message; });
        } else {
          // The Channel (chat) row: hide the overlay so the conversation
          // shows, and resume this agent's pinned home thread.
          orchView.style.display = 'none';
          var altSid = altPinnedSession(window.GOHORT_AGENT_ID);
          // Use this panel's own session var (activeSessionId). currentSessionId
          // belongs to a different component (chat_panel) and is undeclared here
          // — referencing it threw a ReferenceError that swallowed the channel
          // open, so selecting Channel silently fell through to a new session
          // and the home thread never auto-opened.
          if (altSid && activeSessionId !== altSid) openSession(altSid);
          // Restore the chat's title (openSession refreshes it when it actually
          // switches; when already on the home thread it doesn't fire, so put
          // back the remembered session title rather than the view label).
          if (drawer && drawer.mobileTitle) drawer.mobileTitle.textContent = lastSessionTitle || (cfg.new_label || 'New');
        }
      }
      // refreshChannelBadges fetches each management view's row count and
      // shows it as a badge on that row (hidden when zero). core/ui stays
      // agnostic: any nav item with a source gets a count badge.
      // updateManageDot lights the dot on the Manage button when ANY management
      // view currently shows a nonzero count — the at-a-glance "you have pending
      // items" signal the old rail-box badges gave, now that the per-view badges
      // live inside a closed dropdown.
      function updateManageDot() {
        if (!manageDot) return;
        // Pinned items live in the rail, not the Manage menu — exclude them so
        // the Manage dot reflects only what's actually inside the dropdown.
        var any = (cfg.orchestrator_nav || []).some(function(item, i) {
          if (item.pinned) return false;
          var b = orchBadges[i];
          return b && b.style.display !== 'none' && b.textContent && b.textContent !== '0';
        });
        manageDot.style.display = any ? '' : 'none';
      }
      function refreshChannelBadges() {
        (cfg.orchestrator_nav || []).forEach(function(item, i) {
          var badge = orchBadges[i];
          if (!item.source || !badge) return;
          fetch(orchSourceURL(item.source)).then(function(r) { return r.ok ? r.json() : []; })
            .then(function(rows) {
              // BadgeField counts only matching rows (e.g. _pending on a page
              // that also lists granted permissions); empty counts every row.
              var n = item.badge_field
                ? (rows || []).filter(function(rw) { return rw && rw[item.badge_field]; }).length
                : ((rows && rows.length) || 0);
              badge.textContent = n ? String(n) : '';
              badge.style.display = n ? '' : 'none';
              // A pinned action-queue row keeps a faint always-on tint (marks it
              // like the Cortex row) and STRENGTHENS it when items are pending.
              if (item.pinned && orchBtns[i]) {
                orchBtns[i].style.background = n ? 'rgba(88,166,255,0.18)' : 'rgba(88,166,255,0.06)';
              }
              updateManageDot();
            })
            .catch(function() {});
        });
      }
      // Channel/fleet management is a "Manage ▾" dropdown in the topbar — NOT a
      // box in the session rail (channel model: the rail is threads only). navEl
      // is the absolutely-positioned dropdown panel; each item is a management
      // view (Enabled agents / Event monitors / Authorizations) with a live
      // count badge, or a channel-wide action (Clear / Decommission).
      navEl = el('div', {class: 'ui-channel-menu', style: 'display:none;position:absolute;right:0;top:calc(100% + 4px);z-index:40;min-width:210px;flex-direction:column;gap:0.1rem;padding:0.35rem;border:1px solid var(--border, rgba(127,127,127,0.3));border-radius:6px;background:var(--bg-1, #1b1b2b);box-shadow:0 6px 24px rgba(0,0,0,0.35)'});
      function closeManageMenu() { if (navEl) navEl.style.display = 'none'; clearOpenTopbarMenu(closeManageMenu); }
      // Pinned items (action queues like Permissions) get a prominent row ABOVE
      // the session list; everything else lives in the Manage dropdown. One pass
      // keeps orchBtns/orchBadges index-aligned with cfg.orchestrator_nav.
      pinnedEl = el('div', {class: 'ui-channel-pinned', style: 'display:none;flex-direction:column;gap:0.2rem;padding:0.45rem 0.5rem;border-bottom:1px solid var(--border, rgba(127,127,127,0.3))'});
      // The "Manage ▾" dropdown only earns its place when there's at least one
      // NON-pinned nav item to put in it (a management view or a channel action).
      // An alt-nav agent with no such items (e.g. a published dashboard agent that
      // carries the Cortex hero thread but no management surface) shows no empty
      // Manage button — applyOrchMode gates manageControl on this.
      var hasManageMenu = (cfg.orchestrator_nav || []).some(function(it){ return !it.pinned; });
      (cfg.orchestrator_nav || []).forEach(function(item, i) {
        var badge = null;
        var b;
        if (item.pinned) {
          // Modernized pinned row (Permissions etc.) — mirrors the Cortex marked
          // row: a colored glyph + bold title (+ optional subtitle) + count pill,
          // rounded with a faint always-on accent tint that strengthens when the
          // queue has pending items (set in refreshChannelBadges).
          var pAccent = '#58a6ff';
          var plabel = el('span', {style: 'font-weight:700;overflow:hidden;text-overflow:ellipsis;min-width:0'}, [item.label || ('View ' + (i + 1))]);
          badge = el('span', {class: 'ui-channel-badge', style: 'display:none;min-width:1.3rem;text-align:center;padding:0.05rem 0.45rem;border-radius:999px;font-size:0.7rem;font-weight:700;background:' + pAccent + ';color:#fff;flex:0 0 auto'}, ['']);
          var ptitle = el('div', {style: 'display:flex;align-items:center;gap:0.4rem;white-space:nowrap;overflow:hidden'}, [plabel, badge]);
          var pbody = [ptitle];
          if (item.subtitle) {
            pbody.push(el('div', {style: 'font-size:0.74rem;color:var(--text-mute, #999);margin-top:0.1rem;overflow:hidden;text-overflow:ellipsis;white-space:nowrap'}, [item.subtitle]));
          }
          var pkids = [
            el('span', {style: 'flex:0 0 1.1rem;text-align:center;font-size:0.95rem;color:' + pAccent}, [item.icon || '🛡']),
            el('div', {style: 'flex:1;min-width:0'}, pbody),
          ];
          // Transparent border by default (reserves the space, no layout shift);
          // selectOrchNav swaps in the accent border when this view is selected,
          // matching the Cortex row's "selected" treatment.
          b = el('button', {type: 'button', class: 'ui-channel-row ui-channel-pinned-row',
            style: 'display:flex;align-items:flex-start;gap:0.5rem;text-align:left;padding:0.5rem 0.6rem;border:1px solid transparent;border-radius:7px;cursor:pointer;font:inherit;color:var(--text, inherit);background:rgba(88,166,255,0.06);width:100%',
            onclick: function() { selectOrchNav(i); }}, pkids);
          pinnedEl.appendChild(b);
        } else {
          var label = el('span', {style: 'flex:1;text-align:left;overflow:hidden;text-overflow:ellipsis;white-space:nowrap'}, [item.label || ('View ' + (i + 1))]);
          var kids = [label];
          if (item.action_url) {
            // A channel action (clear / decommission): muted, color by variant.
            var ac = (item.variant === 'danger') ? 'var(--danger, #d9534f)' : (item.variant === 'warning') ? 'var(--warning, #d98c34)' : 'var(--text-mute, #999)';
            label.style.color = ac;
            label.style.fontSize = '0.85rem';
          } else if (item.source) {
            badge = el('span', {class: 'ui-channel-badge', style: 'display:none;min-width:1.2rem;text-align:center;padding:0.05rem 0.4rem;border-radius:999px;font-size:0.75rem;background:var(--bg-2, rgba(127,127,127,0.22));color:var(--text-mute, #999)'}, ['']);
            kids.push(badge);
          }
          b = el('button', {type: 'button', class: 'ui-channel-row',
            style: 'display:flex;align-items:center;gap:0.4rem;text-align:left;padding:0.45rem 0.6rem;border:none;border-radius:4px;cursor:pointer;font:inherit;color:var(--text, inherit);background:transparent;width:100%',
            onclick: function() { closeManageMenu(); selectOrchNav(i); }}, kids);
          navEl.appendChild(b);
        }
        orchBtns.push(b);
        orchBadges.push(badge);
      });
      // Pinned rows sit at the very top of the rail, above the session header.
      if (sideHdrEl && pinnedEl.childNodes.length) side.insertBefore(pinnedEl, sideHdrEl);
      // The toggle. Wrapped (position:relative) so the dropdown anchors to it.
      // Hidden by default; applyOrchMode reveals it for fleet agents. A small
      // dot lights when any management view has pending items (the signal the
      // old rail-box badges carried).
      manageBtn = el('button', {type: 'button', class: 'ui-row-btn', title: 'Channel & fleet management',
        onclick: function(ev) {
          ev.stopPropagation();
          var open = navEl.style.display === 'none' || !navEl.style.display;
          if (open) {
            setOpenTopbarMenu(closeManageMenu); // close any open toolbar menu first
            navEl.style.display = 'flex';
            refreshChannelBadges();
          } else {
            closeManageMenu();
          }
        }}, ['Manage ▾']);
      manageDot = el('span', {class: 'ui-unread-dot', title: 'Pending items',
        style: 'display:none;width:7px;height:7px;border-radius:50%;background:var(--accent, #4a9eff);margin-left:0.35rem;flex:0 0 auto'}, ['']);
      manageBtn.appendChild(manageDot);
      manageControl = el('div', {style: 'position:relative;display:none'}, [manageBtn, navEl]);
      // Close on any outside click.
      document.addEventListener('click', function(ev) {
        if (navEl && navEl.style.display && navEl.style.display !== 'none' &&
            manageControl && !manageControl.contains(ev.target)) closeManageMenu();
      });
      var lastOrchAgent; // last agent applyOrchMode saw — tells a real switch from the double-fire on initial load
      function applyOrchMode(agentId) {
        var isOrch = isAltNavAgent(agentId);
        // Fleet agents get the "Manage ▾" control in the topbar; the rail is
        // threads only. Non-fleet agents hide it (and any open overlay/menu).
        if (manageControl) manageControl.style.display = (isOrch && hasManageMenu) ? '' : 'none';
        if (pinnedEl) pinnedEl.style.display = isOrch ? '' : 'none';
        // Hide the Channel hero immediately for non-fleet agents; loadSessions
        // re-shows + fills it for fleet agents from the home thread.
        if (!isOrch && primaryEl) primaryEl.style.display = 'none';
        closeManageMenu();
        if (!isOrch && orchView) orchView.style.display = 'none';
        if (isOrch) {
          refreshChannelBadges();
          // Land on the surface this agent was LAST on: its cortex (standing
          // thread) → the cortex; a session → a NEW session (sessions are
          // task-shaped, so a fresh one, not the last). The cortex is always one
          // click away via its hero row. A real agent switch re-lands; the first
          // page load respects a ?session deep-link (which set activeSessionId).
          var switched = (typeof lastOrchAgent !== 'undefined' && lastOrchAgent !== agentId);
          if (switched || !activeSessionId) {
            if (getLanding(agentId) === 'cortex' && altPinnedSession(agentId)) openHomeThread();
            else openSession(null);
          }
        }
        lastOrchAgent = agentId;
      }
      window.addEventListener('gohort-agent-id-changed', function(e) {
        applyOrchMode(e && e.detail && e.detail.agent_id);
      });
      // web_assets dispatches the change event after picker init; apply now
      // too in case the default-selected agent is already an orchestrator.
      setTimeout(function() { applyOrchMode(window.GOHORT_AGENT_ID || ''); }, 0);

      // Unread/badge poll. A channel agent receives background wakes (a
      // monitor fires, a standing agent reports, a goal conversation
      // finishes) that append to sessions the user isn't viewing — so the
      // unread dots + management counts must refresh on their own, not only
      // on navigation. Light and guarded: only for channel agents, only when
      // the tab is visible, and never mid bulk-select (which would fight the
      // user's row selection). Non-channel agents never get background
      // appends, so there's nothing to poll for them.
      if (hasList) {
        setInterval(function() {
          if (document.hidden) return;
          if (bulkState && bulkState.mode) return;
          if (!isAltNavAgent(window.GOHORT_AGENT_ID)) return;
          loadSessions();
          refreshChannelBadges();
        }, 30000);
      }

      // Mobile top bar carries the hamburger + active session title +
      // a "+ N" new-session button. The rail header also has a "+ New"
      // (visible when the drawer is open), but the always-visible
      // mobile-header button saves the user a drawer round-trip to
      // start a fresh session — worth the small duplication on a
      // surface where every tap counts.
      drawer = makeDrawer(side, {
        title:          cfg.new_label || 'New',
        hamburgerTitle: cfg.list_title || 'Sessions',
        newTitle:       cfg.new_label || 'New',
        onNew:          function(){ openSession(null); },
      });
    }

    // --- Main: conversation + activity panes ------------------------------
    // .ui-agent is a flex column: topbar (status + actions) above a
    // grid row that holds the side rail and the main column.
    var topbar = el('div', {class: 'ui-agent-topbar'});
    var gridRow = el('div', {class: 'ui-agent-grid'});

    var main = el('div', {class: 'ui-agent-main'});
    main.style.position = 'relative';
    // Orchestrator table view — overlays the chat pane when a non-chat nav
    // item (Enabled agents / Authorizations) is selected; hidden for the Chat
    // view and for every normal agent.
    orchView = el('div', {class: 'ui-orch-view', style: 'display:none;position:absolute;inset:0;overflow:auto;background:var(--bg-1, #1b1b2b);padding:0.85rem;z-index:5'});
    main.appendChild(orchView);
    if (drawer) main.appendChild(drawer.mobileHdr);

    // Floating expand-tab shown when the left rail is collapsed on
    // desktop. Sits pinned against the conversation pane's left edge
    // so the user can always pull the list back. Hamburger icon for
    // symmetry with the in-rail collapse button.
    var expandTab = null;
    if (hasList) {
      expandTab = el('button', {
        class: 'ui-agent-expand', title: 'Show ' + (cfg.list_title || 'list'),
        onclick: function(){ toggleSideCollapse(); },
      }, ['☰']);
    }
    // Side collapse state — desktop only. Persisted in localStorage
    // so the user's preference sticks across reloads. Default is
    // collapsed (rail hidden) because the conversation is the
    // primary surface in most flows.
    var sideCollapsed = true;
    try {
      var stored = localStorage.getItem('agent.sideCollapsed');
      if (stored === '0') sideCollapsed = false;
    } catch (_) {}
    function applySideCollapse() {
      if (!hasList) return;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      try { localStorage.setItem('agent.sideCollapsed', sideCollapsed ? '1' : '0'); } catch (_) {}
    }
    function toggleSideCollapse() {
      sideCollapsed = !sideCollapsed;
      applySideCollapse();
    }
    applySideCollapse();

    // Activity collapse state — same persistence pattern as the
    // side rail. Default comes from cfg.hide_activity; user's
    // toggle preference overrides on reload. cfg.lock_activity
    // pins it hidden AND disables the toggle — used by apps that
    // route everything into the conversation pane and don't want
    // the activity-pane affordance at all.
    var activityCollapsed = !!cfg.hide_activity || !!cfg.lock_activity;
    if (!cfg.lock_activity) {
      try {
        var aStored = localStorage.getItem('agent.activityCollapsed');
        if (aStored === '1') activityCollapsed = true;
        else if (aStored === '0') activityCollapsed = false;
      } catch (_) {}
    }
    function applyActivityCollapse() {
      wrap.classList.toggle('activity-collapsed', activityCollapsed);
      if (cfg.lock_activity) {
        wrap.classList.add('activity-locked-hidden');
      }
      if (!cfg.lock_activity) {
        try { localStorage.setItem('agent.activityCollapsed', activityCollapsed ? '1' : '0'); } catch (_) {}
      }
    }
    function toggleActivityCollapse() {
      if (cfg.lock_activity) return; // pane is locked off; ignore toggle
      activityCollapsed = !activityCollapsed;
      applyActivityCollapse();
    }
    // Floating expand tab pinned to the right edge — visible only
    // when the activity pane is collapsed. Mirror of the side
    // rail's ☰ expand tab.
    var activityExpandTab = el('button', {
      class: 'ui-agent-activity-expand', title: 'Show activity',
      onclick: function(){ toggleActivityCollapse(); },
    }, ['☰']);
    applyActivityCollapse();

    var statusBar = el('div', {class: 'ui-agent-status', style: 'display:none'});
    topbar.appendChild(statusBar);

    // ListPosition: "top" — chat-app layout. Sessions rail stays
    // permanently visible on the left; the chat-pane gets its own
    // topbar (assembled below into main) that holds the action
    // buttons only. No ☰ toggle, no "+ New session" duplicate — the
    // sidebar header already carries New, and the rail isn't meant
    // to collapse in this mode.
    var listPosTop = hasList && cfg.list_position === 'top';
    if (listPosTop) {
      wrap.classList.add('ui-agent-list-top');
      // Force rail expanded, ignore any stored collapse preference.
      sideCollapsed = false;
      applySideCollapse();
    }

    var actionsBar = el('div', {class: 'ui-agent-actions'});
    // Session diagnostics — the framework's decisions made on the user's
    // behalf in this conversation (suppressed replies, discarded inputs,
    // retries), which otherwise vanish into server logs. Generic: the app
    // supplies DiagnosticsURL; entries are [{at, kind, detail}].
    if (cfg.diagnostics_url) {
      var diagBtn = el('button', {class: 'ui-row-btn', type: 'button',
        title: 'Session diagnostics — what the framework suppressed, discarded, or retried in this conversation'}, ['⚠']);
      diagBtn.addEventListener('click', function() {
        var sid = activeSessionId || '';
        if (!sid) { showToast('No active session yet.'); return; }
        var url = substituteExtras(cfg.diagnostics_url).replace('{session}', encodeURIComponent(sid));
        fetchJSON(url).then(function(list) {
          if (!Array.isArray(list)) list = [];
          window.uiOpenModal({
            title: 'Session diagnostics',
            subtitle: 'Framework decisions in this conversation — content suppressed, discarded, or retried on your behalf. Newest first.',
            width: 'min(640px, 94vw)',
            actions: [{label: 'Close'}],
            mount: function(body) {
              if (!list.length) {
                body.appendChild(el('div', {style: 'color:var(--text-mute);font-size:0.85rem'},
                  ['Nothing to report — no guard has intervened in this session.']));
                return;
              }
              list.forEach(function(e) {
                var row = el('div', {style: 'padding:0.5rem 0;border-bottom:1px solid var(--border);font-size:0.82rem;line-height:1.45'});
                var when = '';
                try { when = e.at ? new Date(e.at).toLocaleString() : ''; } catch (_) {}
                row.appendChild(el('div', {style: 'color:var(--text-mute);font-size:0.72rem;margin-bottom:0.15rem'},
                  [when + (e.kind ? ' · ' + e.kind : '')]));
                row.appendChild(el('div', {style: 'white-space:pre-wrap;word-break:break-word'}, [e.detail || '']));
                body.appendChild(row);
              });
            },
          });
        }).catch(function(err) { showToast('Diagnostics unavailable: ' + (err && err.message || err)); });
      });
      actionsBar.appendChild(diagBtn);
    }
    // runToolbarAction performs one toolbar action — shared by flat buttons AND
    // grouped-dropdown items so both behave identically.
    async function runToolbarAction(action, btn) {
      if (action.confirm && !(await window.uiConfirm(action.confirm))) return;
      var method = action.method || 'post';
      if (method === 'client') {
        var name = action.url || '';
        var fn = window.UIClientActions && window.UIClientActions[name];
        if (typeof fn === 'function') {
          fn({
            sessionId: activeSessionId,
            button:    btn,
            action:    action,
            // clearConvo() / clearActivity() — wipe a pane. Used by app-defined
            // Clear actions that mirror the legacy chat-header Clear button.
            clearConvo: function() {
              msgEls = {}; blockEls = {};
              convoLog.innerHTML = '';
              emptyMsg = el('div', {class: 'ui-agent-empty'},
                [cfg.empty_text || 'Start typing below.']);
              convoLog.appendChild(emptyMsg);
            },
            clearActivity: function() {
              activityEls = {};
              activityLog.innerHTML = '';
            },
            // subscribe(url) — wire an EventSource to url and pipe its events
            // through handleEvent so a server job's progress shows in the
            // activity pane. Registers as activeEventSource so cancelMessage()
            // and the 'done' handler close it cleanly (else the browser
            // auto-reconnects and the snapshot-then-stream translator replays
            // every buffered event forever after cancel).
            subscribe: function(url) {
              if (activeEventSource) {
                activeEventSource.close();
                activeEventSource = null;
              }
              var es = new EventSource(url);
              activeEventSource = es;
              es.onmessage = function(ev) {
                try { handleEvent(JSON.parse(ev.data)); } catch (_) {}
              };
              es.onerror = function() {
                if (es.readyState === EventSource.CLOSED) {
                  if (activeEventSource === es) activeEventSource = null;
                  enableInput();
                }
              };
              disableInput();
              return es;
            },
          });
        } else {
          showToast('No handler for client action: ' + name);
        }
        return;
      }
      var url = (action.url || '').replace('{id}',
        encodeURIComponent(activeSessionId || ''));
      if (method === 'open')          { window.open(url, '_blank', 'noopener'); }
      else if (method === 'redirect') { window.location.href = url; }
      else {
        fetchJSON(url, {method: 'POST'}).catch(function(err) {
          showToast('Failed: ' + (err && err.message || err));
        });
      }
    }
    function makeActionButton(action) {
      var classes = 'ui-row-btn';
      if (action.variant) classes += ' ' + action.variant;
      var btn = el('button', {class: classes, title: action.title || '',
        'data-action-label': action.label || ''}, [action.label || '(action)']);
      btn.addEventListener('click', function() { runToolbarAction(action, btn); });
      return btn;
    }
    // Render ungrouped actions as flat buttons; collapse each Group into a
    // "<Group> ▾" dropdown so a crowded toolbar sheds its rarely-used actions.
    (function() {
      var groupOrder = [], groupMap = {};
      (cfg.actions || []).forEach(function(action) {
        if (action.group) {
          if (!groupMap[action.group]) { groupMap[action.group] = []; groupOrder.push(action.group); }
          groupMap[action.group].push(action);
        } else {
          actionsBar.appendChild(makeActionButton(action));
        }
      });
      groupOrder.forEach(function(gname) {
        // The menu is portaled to <body> (position:fixed, placed from the
        // button's rect) — the toolbar's list-top cascade kept pulling an
        // in-toolbar menu into normal flow, so we get it out of the toolbar's
        // DOM entirely. Only the toggle button lives in the actions bar.
        var menu = el('div', {class: 'ui-toolbar-menu'});
        var toggle = el('button', {type: 'button', class: 'ui-row-btn', title: gname + ' actions'}, [gname + ' ▾']);
        function closeMenu() { menu.classList.remove('open'); clearOpenTopbarMenu(closeMenu); }
        function openMenu() {
          setOpenTopbarMenu(closeMenu); // close any other open top-bar menu first
          var r = toggle.getBoundingClientRect();
          menu.style.left = Math.round(r.left) + 'px';
          menu.style.top = Math.round(r.bottom + 4) + 'px';
          menu.classList.add('open');
        }
        groupMap[gname].forEach(function(action) {
          var item = el('button', {type: 'button', class: (action.variant ? action.variant : ''),
            title: action.title || '', 'data-action-label': action.label || ''}, [action.label || '(action)']);
          item.addEventListener('click', function() { closeMenu(); runToolbarAction(action, item); });
          menu.appendChild(item);
        });
        document.body.appendChild(menu);
        toggle.addEventListener('click', function(ev) {
          ev.stopPropagation();
          if (menu.classList.contains('open')) closeMenu(); else openMenu();
        });
        document.addEventListener('click', function(e) {
          if (menu.classList.contains('open') && !menu.contains(e.target) && !toggle.contains(e.target)) closeMenu();
        });
        actionsBar.appendChild(toggle);
      });
    })();
    // Copy session — registered as a built-in client action so apps
    // can place it wherever they want in their Actions list. Closes
    // over substituteExtras + cfg.load_url + activeSessionId so the
    // export URL resolves the same way regardless of where the action
    // surfaces in the UI. Apps that want the button do:
    //   {Label: "Copy session", Method: "client", URL: "copy_session"}
    // No-op when cfg.load_url isn't set (no export endpoint to hit).
    if (cfg.load_url && window.uiRegisterClientAction) {
      window.uiRegisterClientAction('copy_session', function(ctx) {
        var btn = ctx && ctx.button;
        if (!activeSessionId) {
          showToast('No active session.');
          return;
        }
        var loaded = substituteExtras(
          cfg.load_url.replace('{id}', encodeURIComponent(activeSessionId))
        );
        if (loaded.indexOf('{agent_id}') >= 0) {
          var agentId = '';
          // Resolve agent_id from FOUR sources in priority order so
          // desktop (no URL ?agent=) + browser (no body dataset) both
          // work without depending on a single source.
          var sel = document.querySelector('.ui-agent-extras select[name="agent_id"], .ui-agent-extras-label select');
          if (sel && sel.value) agentId = sel.value;
          if (!agentId) {
            try {
              agentId = new URL(window.location.href).searchParams.get('agent') || '';
            } catch (_) {}
          }
          if (!agentId && document.body && document.body.dataset) {
            agentId = document.body.dataset.agentId || '';
          }
          if (!agentId) {
            showToast('Copy session: could not resolve agent_id. Make sure an agent is selected.');
            return;
          }
          loaded = loaded.replace('{agent_id}', encodeURIComponent(agentId));
        }
        var qIdx = loaded.indexOf('?');
        var path = (qIdx >= 0 ? loaded.slice(0, qIdx) : loaded) + '/export' + (qIdx >= 0 ? loaded.slice(qIdx) : '');
        var prior = btn ? btn.textContent : '';
        var setBusy = function() { if (btn) { btn.disabled = true; btn.textContent = 'Copying…'; } };
        var setDone = function() {
          if (btn) {
            btn.textContent = 'Copied';
            setTimeout(function() { btn.disabled = false; btn.textContent = prior; }, 1100);
          }
        };
        var setFail = function(err) {
          if (btn) { btn.disabled = false; btn.textContent = prior; }
          var dispUrl = path.length > 120 ? path.slice(0, 117) + '…' : path;
          showToast('Copy failed: ' + (err && err.message || err) + ' (url: ' + dispUrl + ')');
        };
        setBusy();
        var fetchText = function() {
          return fetch(path).then(function(r) {
            if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
            return r.text();
          });
        };
        // Gesture-safe async clipboard. WKWebView (and Safari) drop the
        // transient user-activation across an awaited fetch, so a
        // post-fetch writeText / execCommand / host-clipboard call
        // silently no-ops while still resolving — the "says Copied but
        // pastes nothing" bug. (The per-message copy buttons work because
        // they writeText synchronously inside the click, gesture intact.)
        // Handing the fetch PROMISE to ClipboardItem keeps the activation
        // valid until the text resolves — the one path that survives the
        // async gap. Falls back to a plain fetch-then-write chain where
        // promise-valued ClipboardItem isn't supported, and only ever
        // reports success on a write that actually lands.
        if (navigator.clipboard && navigator.clipboard.write && typeof window.ClipboardItem === 'function') {
          try {
            var item = new ClipboardItem({
              'text/plain': fetchText().then(function(t) { return new Blob([t], {type: 'text/plain'}); }),
            });
            navigator.clipboard.write([item]).then(setDone).catch(function() { copyChain(); });
            return;
          } catch (_) { /* promise-valued ClipboardItem unsupported → chain */ }
        }
        copyChain();

        function copyChain() {
          fetchText().then(function(md) {
            // Browser clipboard first — it's what the per-message copy
            // buttons use and works in this webview — then the host-native
            // Wails clipboard, then a hidden-textarea execCommand.
            if (navigator.clipboard && navigator.clipboard.writeText) {
              return navigator.clipboard.writeText(md).then(setDone).catch(function() { hostCopy(md); });
            }
            hostCopy(md);
          }).catch(setFail);
        }
        function hostCopy(md) {
          if (typeof window.__uiClipboardImpl === 'function') {
            window.__uiClipboardImpl(md).then(setDone).catch(function() { legacyCopy(md); });
            return;
          }
          legacyCopy(md);
        }
        function legacyCopy(md) {
          var ta = document.createElement('textarea');
          ta.value = md;
          ta.style.position = 'fixed'; ta.style.opacity = '0';
          document.body.appendChild(ta);
          ta.select();
          var ok = false;
          try { ok = document.execCommand('copy'); } catch (_) {}
          document.body.removeChild(ta);
          if (ok) { setDone(); } else { setFail(new Error('clipboard unavailable')); }
        }
      });
    }
    if ((cfg.actions || []).length === 0) actionsBar.style.display = 'none';
    // Drop the "Manage ▾" fleet control into the topbar actions (built earlier
    // in the rail block, where the nav machinery was in scope). Travels with
    // actionsBar to wherever the layout places it. Force the bar visible since
    // the control alone justifies it even when the app declared no other actions.
    if (manageControl) {
      actionsBar.style.display = '';
      actionsBar.appendChild(manageControl);
    }
    topbar.appendChild(actionsBar);
    // statusPill removed — the in-chat thinking spinner (rendered
    // by showThinking, floating above the active assistant bubble)
    // already signals "agent working." A second top-bar indicator
    // turned out to be visual noise. Kept the variable as null so
    // the existing disable/enable null-guards still compile.
    var statusPill = null;
    // ExtraFields strip lives in the topbar so context selectors
    // (active appliance, project, …) sit alongside the toolbar
    // buttons. The strip itself is built further below; we just
    // reserve its DOM slot here. In the DEFAULT (rail) layout the
    // topbar is flex-direction:column, so DOM order = vertical
    // order — actions stays ABOVE the extras+modes row (operator
    // controls on top, mode pills + context picker below). The
    // list-top horizontal layout uses CSS order: to flip the
    // visual side without changing DOM order.
    var extrasSlot = el('div', {class: 'ui-agent-extras-slot'});
    topbar.appendChild(extrasSlot);

    var split = el('div', {class: 'ui-agent-split'});
    var convoPane = el('div', {class: 'ui-agent-convo'});
    var convoLog  = el('div', {class: 'ui-agent-convo-log'});
    convoPane.appendChild(convoLog);
    var emptyMsg = el('div', {class: 'ui-agent-empty'},
      [cfg.empty_text || 'Start typing below.']);
    convoLog.appendChild(emptyMsg);
    var divider  = el('div', {class: 'ui-agent-divider', title: 'Drag to resize'});

    // Right pane — when cfg.terminal is set, this column splits
    // vertically into activity (top) + terminal (bottom). Otherwise
    // activity fills the column on its own.
    var rightPane = el('div', {class: 'ui-agent-right'});
    var activityPane = el('div', {class: 'ui-agent-activity'});
    if (cfg.hide_activity) activityPane.classList.add('collapsed');
    var activityHdr = el('div', {class: 'ui-agent-activity-h'});
    activityHdr.appendChild(el('span', {text: 'Activity'}));
    // × button that collapses the activity column. The floating
    // expand tab on the right edge pulls it back open.
    var activityCollapseBtn = el('button', {
      class: 'ui-agent-activity-toggle', title: 'Hide activity',
      onclick: function(){ toggleActivityCollapse(); },
    }, ['×']);
    activityHdr.appendChild(activityCollapseBtn);
    var activityLog = el('div', {class: 'ui-agent-activity-log'},
      [el('div', {class: 'ui-agent-act ui-agent-act-status'},
        ['Tool calls and outputs appear here.'])]);
    activityPane.appendChild(activityHdr);
    activityPane.appendChild(activityLog);
    rightPane.appendChild(activityPane);

    var terminalPane = null;
    if (cfg.terminal && cfg.terminal.url) {
      var hDivider = el('div', {class: 'ui-agent-hdivider', title: 'Drag to resize'});
      terminalPane = el('div', {class: 'ui-agent-terminal'});
      var termHdr = el('div', {class: 'ui-agent-terminal-h'},
        [el('span', {text: cfg.terminal.title || 'Terminal'})]);
      var termBody = el('div', {class: 'ui-agent-terminal-body'},
        [el('div', {class: 'ui-agent-terminal-placeholder'},
          ['(terminal pane — xterm.js wiring deferred)'])]);
      terminalPane.appendChild(termHdr);
      terminalPane.appendChild(termBody);
      rightPane.appendChild(hDivider);
      rightPane.appendChild(terminalPane);

      var hResizing = false, hStartY = 0, hStartH = 0;
      hDivider.addEventListener('mousedown', function(ev) {
        hResizing = true; hStartY = ev.clientY;
        hStartH = activityPane.getBoundingClientRect().height;
        document.body.style.cursor = 'row-resize';
        ev.preventDefault();
      });
      document.addEventListener('mousemove', function(ev) {
        if (!hResizing) return;
        var dy = ev.clientY - hStartY;
        var newH = Math.max(80, hStartH + dy);
        activityPane.style.flex = '0 0 ' + newH + 'px';
      });
      document.addEventListener('mouseup', function() {
        if (hResizing) { hResizing = false; document.body.style.cursor = ''; }
      });
    }

    split.appendChild(convoPane);
    split.appendChild(divider);
    split.appendChild(rightPane);
    main.appendChild(split);

    // Resize handling — drag the divider to flex convo/activity widths.
    var resizing = false, startX = 0, startConvo = 0;
    divider.addEventListener('mousedown', function(ev) {
      resizing = true; startX = ev.clientX;
      startConvo = convoPane.getBoundingClientRect().width;
      document.body.style.cursor = 'col-resize';
      ev.preventDefault();
    });
    document.addEventListener('mousemove', function(ev) {
      if (!resizing) return;
      var dx = ev.clientX - startX;
      var newW = Math.max(280, startConvo + dx);
      convoPane.style.flex = '0 0 ' + newW + 'px';
    });
    document.addEventListener('mouseup', function() {
      if (resizing) { resizing = false; document.body.style.cursor = ''; }
    });

    // --- Input row --------------------------------------------------------
    var inputRow = el('div', {class: 'ui-agent-input-row'});
    var inputArea = el('textarea', {
      class: 'ui-agent-input',
      placeholder: cfg.placeholder || 'Ask something…',
      rows: 2,
    });
    inputArea.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) { ev.preventDefault(); sendMessage(); }
    });
    // Auto-grow the textarea as content changes so a multi-line
    // paste doesn't get crammed into a 2-line scroller. Resets on
    // each input event by measuring scrollHeight; capped via CSS
    // max-height so an absurdly long paste doesn't push the chat
    // pane off-screen.
    function autosizeInput() {
      inputArea.style.height = 'auto';
      // +2px for the border so the last line isn't clipped.
      inputArea.style.height = (inputArea.scrollHeight + 2) + 'px';
    }
    inputArea.addEventListener('input', autosizeInput);
    // Defer the initial sizing past mount so the element has a real
    // computed scrollHeight to read.
    setTimeout(autosizeInput, 0);

    // Large-paste marker: when the user pastes more than ~500 chars
    // (a code block, log dump, doc excerpt), insert a compact marker
    // like "[Pasted text #2 — 47 lines / 1834 chars]" at the cursor
    // instead of jamming the textarea with the entire block. The full
    // content lives in pasteMap keyed by the marker's N; sendMessage
    // substitutes it back in just before submit, so the LLM gets the
    // real text but the user's composition surface stays readable.
    // Same shape Claude Code uses for terminal pastes.
    //
    // Small pastes (< threshold) flow through normally — no marker,
    // the content lands directly in the textarea.
    var pasteSnippetThreshold = 500;
    var pasteMap = {};        // {n: full-text-content}
    var pasteCounter = 0;     // monotonic; marker uses this number
    inputArea.addEventListener('paste', function(ev) {
      var clip = ev.clipboardData || window.clipboardData;
      if (!clip) return;
      // Pasted image (e.g. a macOS screenshot taken with Ctrl-Cmd-Shift-4,
      // which copies the grab to the clipboard): attach it the same way
      // the paperclip does, so the flow is shoot -> paste -> send with no
      // "get a screenshot" round-trip. Only when attachments are enabled.
      if (cfg.attachments && clip.items) {
        var imgItems = Array.prototype.slice.call(clip.items).filter(function(it) {
          return it.type && it.type.indexOf('image/') === 0;
        });
        if (imgItems.length) {
          ev.preventDefault();
          imgItems.forEach(function(it) {
            var file = it.getAsFile();
            if (!file) return;
            var reader = new FileReader();
            reader.onload = function() {
              if (window.uiAddPendingAttachment) {
                window.uiAddPendingAttachment({
                  name: 'pasted-image.png',
                  dataURL: reader.result,
                  mime: file.type || 'image/png',
                  kind: 'image',
                });
              }
            };
            reader.readAsDataURL(file);
          });
          return;
        }
      }
      var text = clip.getData('text/plain') || '';
      if (text.length < pasteSnippetThreshold) return; // small paste — normal
      ev.preventDefault();
      var n = ++pasteCounter;
      var lines = text.split('\n').length;
      var marker = '[Pasted text #' + n + ' — ' + lines + ' lines / ' + text.length + ' chars]';
      pasteMap[n] = text;
      // Insert at the cursor position, replacing any selection. Mirrors
      // standard textarea-paste semantics so existing typed text
      // around the cursor is preserved.
      var start = inputArea.selectionStart;
      var end = inputArea.selectionEnd;
      var before = inputArea.value.substring(0, start);
      var after = inputArea.value.substring(end);
      inputArea.value = before + marker + after;
      var caret = before.length + marker.length;
      inputArea.selectionStart = inputArea.selectionEnd = caret;
      // Trigger autosize + any other input listeners.
      inputArea.dispatchEvent(new Event('input'));
      // Park the cursor in the textarea so the next keystroke lands
      // there — the source pane sometimes retains focus after paste.
      inputArea.focus();
    });

    var attachInput = null, attachBtn = null;
    if (cfg.attachments) {
      // Images go to vision via .images[]; PDFs / DOCX / text files
      // go to .documents[] where the server extracts the text and
      // prepends it to the user message. accept covers both.
      // Build accept conditionally — only include audio types when
      // transcription is enabled at the server. window.GOHORT_TRANSCRIBE_ENABLED
      // is set by TranscribeRuntimeFlagScript at page render time.
      var attachAccept = 'image/*,.pdf,application/pdf,.docx,application/vnd.openxmlformats-officedocument.wordprocessingml.document,.doc,application/msword,.txt,.md,text/*';
      if (window.GOHORT_TRANSCRIBE_ENABLED) {
        attachAccept += ',audio/*,.mp3,.wav,.m4a,.aac,.ogg,.flac,.webm,.opus';
      }
      attachInput = el('input', {
        type: 'file', style: 'display:none',
        accept: attachAccept,
      });
      attachInput.addEventListener('change', function(ev) {
        var files = Array.prototype.slice.call(ev.target.files || []);
        files.forEach(function(file) {
          var reader = new FileReader();
          reader.onload = function() {
            var kind = 'document';
            if ((file.type || '').indexOf('image/') === 0) kind = 'image';
            pendingAttachments.push({
              name: file.name,
              dataURL: reader.result,
              mime: file.type || '',
              kind: kind,
            });
            renderAttachments();
          };
          reader.readAsDataURL(file);
        });
        attachInput.value = '';
      });
      attachBtn = el('button', {
        class: 'ui-row-btn ui-agent-attach', title: 'Attach image, PDF, or document',
        onclick: function(){ attachInput.click(); },
      }, ['📎']);
    }

    // uiAddPendingAttachment is the framework hook apps use to push
    // files into the paperclip queue from app-specific UI (intake
    // forms with file fields, custom toolbar uploaders, etc.).
    // Without this, app code couldn't reach the IIFE-scoped
    // pendingAttachments array. Same shape the built-in paperclip
    // uses: {name, dataURL, mime, kind:'image'|'document'}.
    window.uiAddPendingAttachment = function(att) {
      if (!att || !att.dataURL) return;
      pendingAttachments.push({
        name:    att.name || 'attachment',
        dataURL: att.dataURL,
        mime:    att.mime || '',
        kind:    att.kind || ((att.mime || '').indexOf('image/') === 0 ? 'image' : 'document'),
      });
      renderAttachments();
    };

    // uiSetPendingMessageExtras lets app code attach arbitrary JSON
    // fields to the next send body — used when an app surface (intake
    // form, custom toolbar) needs to ride domain-specific metadata
    // alongside the standard message + attachments. Object is merged
    // shallowly into the send body and cleared after one send. Multiple
    // calls before a send merge cumulatively (later keys overwrite).
    window.uiSetPendingMessageExtras = function(obj) {
      if (!obj || typeof obj !== 'object') return;
      Object.keys(obj).forEach(function(k) { pendingMessageExtras[k] = obj[k]; });
    };

    // uiRegisterMessageReplayHook lets apps decorate replayed bubbles
    // without touching the framework's replay loop. fn(bubble, msg) runs
    // for EVERY message replayed in openSession — the app's matcher
    // decides whether to act on each one. Used today by the intake
    // form to swap an intake-derived user message's body for the
    // re-editable form widget.
    window.uiRegisterMessageReplayHook = function(fn) {
      if (typeof fn === 'function') messageReplayHooks.push(fn);
    };

    // uiRegisterBubbleAction lets apps add buttons to the per-bubble
    // action bar (Edit/Retry/Delete on user; Retry/Copy on assistant).
    // Each registered action gets appended after the built-ins. The
    // action object is { role, label, title?, danger?, onclick(ctx) }
    // where ctx = { bubble, getText() }. role must be 'user' or
    // 'assistant'. Action buttons get re-rendered every time the
    // bubble's action bar is rebuilt (initial render + after Edit
    // commit), so registering once on page load is enough.
    var bubbleActionRegistry = {user: [], assistant: []};
    window.uiRegisterBubbleAction = function(opts) {
      if (!opts || typeof opts !== 'object') return;
      var role = opts.role === 'user' ? 'user' : 'assistant';
      if (typeof opts.onclick !== 'function') return;
      bubbleActionRegistry[role].push(opts);
    };
    // appendBubbleActions is called by renderUserActions /
    // renderAssistantActions after they've appended built-ins. Each
    // registered action becomes one button; failures are swallowed so
    // a buggy app action doesn't break the bar.
    function appendBubbleActions(bar, role, bubble) {
      var list = bubbleActionRegistry[role] || [];
      list.forEach(function(act) {
        var btn = el('button', {
          class: 'ui-agent-msg-act' + (act.danger ? ' danger' : ''),
          title: act.title || act.label || '',
          onclick: function() {
            try {
              act.onclick({
                bubble: bubble,
                getText: function() {
                  // Prefer the RAW markdown (msgEls[].rawText) — it
                  // preserves newlines / paragraph breaks. The bubble's
                  // textContent is the RENDERED markdown, which collapses
                  // \n\n into nothing, so exporting from it strips the
                  // article's structure. Fall back to dataset.raw (set on
                  // ChatPanel/pipeline bubbles) then textContent.
                  if (bubble) {
                    for (var mk in msgEls) {
                      if (msgEls[mk] && msgEls[mk].bubble === bubble) {
                        return msgEls[mk].rawText || '';
                      }
                    }
                    if (bubble.dataset && bubble.dataset.raw) return bubble.dataset.raw;
                    return bubble.textContent || '';
                  }
                  return '';
                },
              });
            } catch (e) { /* isolate */ }
          },
        }, [act.label || 'Action']);
        bar.appendChild(btn);
      });
    }

    // Extra fields strip — same shape as ChatPanel: each ChatField
    // becomes one input that rides on every send body. Values also
    // get substituted into ListURL / LoadURL / DeleteURL templates
    // via {field_name} placeholders, so the list rail can be scoped
    // to the active value (e.g. workspaces for the active appliance).
    var extraInputs = {};
    var extrasRow = el('div', {class: 'ui-agent-extras', style: 'display:none'});
    (cfg.extra_fields || []).forEach(function(f) {
      var label = el('label', {class: 'ui-agent-extras-label', text: f.label || f.name});
      var input;
      if (f.type === 'select') {
        input = el('select', {class: 'ui-form-select'});
        // option_pairs (value/label) wins over options (value==label).
        // When pairs carry a non-empty group field, render the options
        // nested under optgroup labels so the dropdown visually
        // separates categories (e.g. built-in vs custom options).
        var pairs = f.option_pairs || [];
        if (pairs.length) {
          // Preserve source order both across groups and within.
          // Bare (no-group) options go first; then each group keeps
          // its options together in source-order.
          var groupMap = {}, groupOrder = [], bareOpts = [];
          pairs.forEach(function(p) {
            var g = p.group || '';
            if (!g) { bareOpts.push(p); return; }
            if (!groupMap[g]) { groupMap[g] = []; groupOrder.push(g); }
            groupMap[g].push(p);
          });
          bareOpts.forEach(function(p) {
            input.appendChild(el('option', {value: p.value}, [p.label || p.value]));
          });
          groupOrder.forEach(function(g) {
            var og = document.createElement('optgroup');
            og.label = g;
            groupMap[g].forEach(function(p) {
              og.appendChild(el('option', {value: p.value}, [p.label || p.value]));
            });
            input.appendChild(og);
          });
        } else {
          (f.options || []).forEach(function(opt) {
            input.appendChild(el('option', {value: opt}, [opt]));
          });
        }
        if (f.default) input.value = f.default;
      } else if (f.type === 'number') {
        input = el('input', {type: 'number', class: 'ui-form-input',
          min: f.min || undefined, max: f.max || undefined, value: f.default || ''});
      } else {
        input = el('input', {type: 'text', class: 'ui-form-input', value: f.default || ''});
      }
      extraInputs[f.name] = input;
      // On change, refresh the list rail. The list URL gets the new
      // extra value substituted into its {field_name} placeholder,
      // so changing the appliance picker reloads the workspace list
      // for that appliance.
      input.addEventListener('change', function() {
        if (hasList) loadSessions();
      });
      label.appendChild(input);
      extrasRow.appendChild(label);
    });
    if ((cfg.extra_fields || []).length > 0) extrasRow.style.display = '';

    // --- Mode toggles (per-session boolean flags rendered as pill
    // buttons above the input). Same shape as ChatPanel.Modes; state
    // is mirrored into modeState and mixed into every send body.
    var modeState = {};
    var modesRow = el('div', {class: 'ui-agent-modes', style: 'display:none'});
    (cfg.modes || []).forEach(function(m) {
      var btn = el('button', {
        class: 'ui-agent-mode',
        type:  'button',
        title: m.title || m.label,
        'data-mode-label': m.label,
      }, [m.label]);
      btn.addEventListener('click', function() {
        var key = m.send_field || m.field;
        var next = !modeState[key];
        modeState[key] = next;
        btn.classList.toggle('active', next);
        var body = {};
        body[m.field] = next;
        // Per-(user, agent) scoping — include agent_id so the server
        // saves the toggle as a per-agent override. window.GOHORT_AGENT_ID
        // is set by the host page (Agency dropdown / public agent app
        // page render). Empty string when not set = falls back to the
        // global user-level toggle on the server.
        body.agent_id = window.GOHORT_AGENT_ID || '';
        try {
          console.log('[gohort/mode-toggle:agent] sending', m.field, '=', next,
            'agent_id=', JSON.stringify(window.GOHORT_AGENT_ID || ''));
        } catch (_) {}
        fetchJSON(m.post_url, {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(body),
        }).catch(function(err) {
          // POST failed — roll back local state so the pill matches reality.
          modeState[key] = !next;
          btn.classList.toggle('active', !next);
          showToast('Save failed: ' + (err && err.message || err));
        });
      });
      modesRow.appendChild(btn);
      // Initial state from GET — fire-and-forget; runs after the
      // panel mounts. Appends agent_id when the host page has set
      // window.GOHORT_AGENT_ID, so the server returns the per-agent
      // override (falls back to global when no override exists).
      // withAgentParam (defined in ChatPanel's scope) isn't reachable
      // here — inline the URL building rather than chasing scope.
      var refresh = function() {
        var url = m.get_url;
        var aid = window.GOHORT_AGENT_ID;
        if (aid && url) {
          url += (url.indexOf('?') >= 0 ? '&' : '?') + 'agent_id=' + encodeURIComponent(aid);
        }
        fetchJSON(url).then(function(d) {
          var v = !!(d && d[m.field]);
          modeState[m.send_field || m.field] = v;
          btn.classList.toggle('active', v);
          // Locked flag = ForcePrivate / DisableInferred is set on the
          // agent → the toggle is enforced ON regardless of user
          // choice. Per-field behavior:
          //   Private — show a visible 🔒 indicator (user wants to
          //     see that network tools are locked off, since it
          //     changes what the agent can do)
          //   Clean / anything else — hide the toggle (the
          //     behavior is internal-corpus-only; less user-visible
          //     impact, so a missing toggle is less confusing than
          //     a locked one)
          if (d && d.locked) {
            if (m.field === 'private_mode') {
              btn.classList.add('ui-chat-mode-locked');
              btn.classList.add('active');
              btn.disabled = true;
              btn.textContent = '🔒 ' + m.label;
              btn.title = m.label + ' is force-enabled by the operator — can\'t be toggled off.';
              btn.style.display = '';
            } else {
              btn.style.display = 'none';
            }
          } else {
            btn.classList.remove('ui-chat-mode-locked');
            btn.disabled = false;
            btn.textContent = m.label;
            btn.title = m.title || m.label;
            btn.style.display = '';
          }
        }).catch(function(){});
      };
      refresh();
      // Re-fetch when the agent changes so the toggle state reflects
      // the new agent's override.
      window.addEventListener('gohort-agent-id-changed', refresh);
    });
    if ((cfg.modes || []).length > 0) modesRow.style.display = '';

    function substituteExtras(url) {
      if (!url) return url;
      Object.keys(extraInputs).forEach(function(k) {
        var v = extraInputs[k].value || '';
        url = url.replace('{' + k + '}', encodeURIComponent(v));
      });
      return url;
    }

    var sendBtn = el('button', {class: 'ui-row-btn primary',
      onclick: function(){ sendMessage(); }}, [cfg.submit_label || 'Send']);
    // The cancel button carries a spinner and only shows while a run is
    // in-flight — so it doubles as a persistent "still working" signal that
    // (unlike the activity trail or a status line) can't scroll out of view
    // during a long investigation.
    var cancelLabel = el('span', {}, ['Cancel']);
    var cancelBtn = el('button', {class: 'ui-row-btn ui-agent-cancel',
      style: 'display:none',
      onclick: function(){ cancelMessage(); }},
      [el('span', {class: 'ui-agent-cancel-spinner', 'aria-hidden': 'true'}), cancelLabel]);
    // statusPill is created earlier in this function (right after
    // actionsBar) and appended into the top bar then. The input row
    // only carries Send / Cancel now.

    inputRow.appendChild(inputArea);
    if (attachBtn) inputRow.appendChild(attachBtn);
    inputRow.appendChild(sendBtn);
    inputRow.appendChild(cancelBtn);
    // Copy session no longer auto-appended here. Apps that want it
    // in the input row can register their own client action and place
    // it via Actions or via ExtraHeadHTML DOM massage; built-in
    // surface is the top-bar ToolbarAction now.

    var attachStrip = el('div', {class: 'ui-agent-attach-strip', style: 'display:none'});

    // ExtraFields strip placement: either inside the chat-pane
    // topbar's extras-slot (default) OR in the sessions rail header
    // (cfg.extra_fields_in_sidebar). When in the rail, it sits
    // between the rail's title row and the session list — natural
    // for context pickers that scope the list itself (an Agent picker
    // whose sessions belong to the active pick, say). Modes always
    // stay in the topbar regardless.
    if (cfg.extra_fields_in_sidebar && side) {
      // Lift the strip OUT of the rail AND pull the action buttons
      // up alongside it into a top bundle that spans the full width
      // above gridRow. The picker pins to a rail-width left column;
      // the right column hosts a 2-row <table> of buttons so the
      // record actions sit above the mode toggles + relocated
      // context buttons without fighting the bundle's CSS grid.
      //
      //   ┌─────────────────┬──────────────────────────────────┐
      //   │ Agent: <picker> │ Row 1: New Edit Clone Export … │
      //   │                 │ Row 2: Private Clean Tools …    │
      //   └─────────────────┴──────────────────────────────────┘
      //
      // Row 1: record-level operations (mint / mutate / export the
      // agent record). Row 2: per-session affordances (mode toggles
      // + Tools/Memory/etc., which apps relocate into the modes row
      // via the standard .ui-agent-actions → .ui-agent-modes hop).
      extrasRow.classList.add('ui-agent-extras-in-side');
      var topBundle = el('div', {class: 'ui-agent-top-bundle'});
      topBundle.appendChild(extrasRow);
      var bundleTable = el('table', {class: 'ui-agent-top-bundle-table'});
      var bundleTbody = el('tbody', {});
      var row1Tr = el('tr', {});
      var row1Td = el('td', {});
      row1Td.appendChild(actionsBar);
      row1Tr.appendChild(row1Td);
      bundleTbody.appendChild(row1Tr);
      if ((cfg.modes || []).length > 0) {
        modesRow.classList.add('ui-agent-modes-in-bundle');
        var row2Tr = el('tr', {});
        var row2Td = el('td', {class: 'ui-agent-top-bundle-row-2'});
        row2Td.appendChild(modesRow);
        row2Tr.appendChild(row2Td);
        bundleTbody.appendChild(row2Tr);
      }
      bundleTable.appendChild(bundleTbody);
      topBundle.appendChild(bundleTable);
      wrap.insertBefore(topBundle, wrap.firstChild);
      // Bypass CSS specificity battles by pinning the rail-button
      // dimensions directly on each action button. Color and border
      // are left UNSET inline so each variant's CSS still wins:
      // .ui-row-btn.danger keeps its red text/border, everything
      // else inherits .ui-row-btn's default text color.
      actionsBar.querySelectorAll('button').forEach(function(b) {
        b.style.borderRadius = '6px';
        b.style.background = 'transparent';
        b.style.padding = '0.2rem 0.55rem';
        b.style.fontSize = '0.75rem';
        b.style.minWidth = '0';
        b.style.minHeight = '0';
      });
    } else {
      extrasSlot.appendChild(extrasRow);
      extrasSlot.appendChild(modesRow);
    }
    convoPane.appendChild(inputRow);
    convoPane.appendChild(attachStrip);

    function renderAttachments() {
      attachStrip.innerHTML = '';
      if (!pendingAttachments.length) { attachStrip.style.display = 'none'; return; }
      attachStrip.style.display = '';
      pendingAttachments.forEach(function(att, idx) {
        var chip = el('span', {class: 'ui-agent-attach-chip'}, [att.name]);
        chip.appendChild(el('button', {
          class: 'ui-agent-attach-x', title: 'Remove',
          onclick: function() {
            pendingAttachments.splice(idx, 1);
            renderAttachments();
          },
        }, ['×']));
        attachStrip.appendChild(chip);
      });
    }

    // --- Conversation / activity rendering --------------------------------

    function clearEmpty() {
      if (emptyMsg && emptyMsg.parentNode) emptyMsg.remove();
    }

    // Smart scroll for the convo pane: auto-stick to the bottom while
    // the user is already there, but stop pulling them down once they
    // scroll up to read. Mirrors the standard chat-app pattern
    // (ChatGPT / Claude). Tolerance avoids judging the user "scrolled
    // up" during momentum scrolls and content-growth jiggles.
    var convoStickToBottom = true;
    var convoUserScrollTimer = null;
    var convoBottomTolerance = 80; // px from bottom counts as "at bottom"
    function convoIsAtBottom() {
      var gap = convoLog.scrollHeight - convoLog.scrollTop - convoLog.clientHeight;
      return gap <= convoBottomTolerance;
    }
    function scrollConvo(force) {
      if (force || convoStickToBottom) {
        convoLog.scrollTop = convoLog.scrollHeight;
      }
    }
    convoLog.addEventListener('scroll', function() {
      // Debounce — scrollend would be cleaner but it has spotty
      // browser support. 120ms is short enough to feel responsive
      // and long enough to coalesce momentum frames.
      if (convoUserScrollTimer) clearTimeout(convoUserScrollTimer);
      convoUserScrollTimer = setTimeout(function() {
        convoStickToBottom = convoIsAtBottom();
      }, 120);
    });

    function addMessage(role, id, text, senderOverride) {
      clearEmpty();
      var classes = 'ui-agent-msg ui-agent-msg-' + (role || 'system');
      // Hide empty assistant bubbles (lazy-bubble materializations for
      // tool_call events). Without this the convo log shows BOTH the
      // thinking spinner AND an empty card while the model is still
      // working through tool rounds. unmarkEmptyBubble strips the
      // class as soon as text or tools land.
      if (role === 'assistant' && !(text || '').length) {
        classes += ' ui-agent-msg-empty';
      }
      var bubble = el('div', {class: classes});
      // Channel-room transcript: name each line by who said it (contact vs the
      // bound agent), the usual chat-transcript treatment. The hover-only
      // timestamp on the action bar still carries the "when". Only when a
      // channel session is open — plain web sessions stay anonymous.
      if (channelTranscript && (role === 'user' || role === 'assistant')) {
        bubble.classList.add('ui-agent-msg-named');
        // Prefer the stored per-message author (set on every channel message,
        // so a GROUP thread names each distinct sender); fall back to the
        // session's contact/agent labels for 1:1 or older messages.
        var who = senderOverride || ((role === 'user') ? channelTranscript.contact : channelTranscript.agent);
        bubble.appendChild(el('div', {class: 'ui-agent-msg-sender'}, [who || '']));
      }
      var body = el('div', {class: 'ui-agent-msg-body'});
      if (cfg.markdown && role === 'assistant' && text) {
        uiRenderMarkdown(body, text);
      } else {
        body.textContent = text || '';
        // Assistant bubbles that arrive without pre-rendered HTML
        // are streaming targets — flag them so the body honors
        // raw newlines via the .ui-agent-msg-streaming CSS rule.
        // finalizeMessage strips the flag once markdown takes over.
        if (role === 'assistant') {
          bubble.classList.add('ui-agent-msg-streaming');
        }
      }
      bubble.appendChild(body);
      // Spinner-above-streaming pattern: if a thinking indicator
      // exists, move it to the end of convoLog FIRST (so it sits
      // after any existing content), THEN append the new bubble —
      // the bubble lands just below the spinner, visually
      // indicating "this bubble is being worked on." On the next
      // round's new bubble, the same sequence relocates the
      // spinner above THAT one.
      if (thinkingEl && thinkingEl.parentNode === convoLog && role === 'assistant') {
        convoLog.appendChild(thinkingEl); // move-to-end (no clone, same node)
      }
      convoLog.appendChild(bubble);
      // A new user message means the user just sent — force-scroll
      // so their own message lands in view + reset the stick-to-
      // bottom state. New assistant bubbles obey the user's stick
      // state (so reading-up doesn't get yanked back down).
      if (role === 'user') {
        convoStickToBottom = true;
        scrollConvo(true);
      } else {
        scrollConvo(false);
      }
      msgEls[id] = {
        bubble: bubble, body: body, role: role, rawText: text || '',
        // Capture wall-clock time at bubble creation. openSession's
        // replay overrides this via setMessageMeta when the server
        // supplied a created field on the saved record.
        created: Date.now(),
      };
      // Action bar gets attached to every user AND assistant bubble.
      // User: Edit / Delete / Retry. Assistant: Retry / Copy. Both
      // show the timestamp on hover. Retry/Edit/Delete on a mid-
      // history bubble trigger a truncate-and-replay — matches
      // Claude's branch-on-edit semantics rather than blocking it.
      if (role === 'user' && (text || '').length > 0) {
        // User actions (Edit / Delete / Retry) all need truncate-and-
        // replay, so they only attach when the app supports it.
        if (cfg.truncate_url) attachUserActions(bubble);
      } else if (role === 'assistant') {
        // Copy is client-only and always available — attach for every
        // app, including ones without a truncate_url (e.g. servitor).
        // Retry inside renderAssistantActions self-gates on truncate_url.
        attachAssistantActions(bubble);
      }
      return msgEls[id];
    }

    // setMessageMeta applies post-creation metadata to a bubble. Used
    // by openSession to pass through server-saved created (timestamp)
    // and usage (per-message stats) so replayed bubbles surface the
    // same hover-only timestamp + stats footer the live flow does.
    function setMessageMeta(id, meta) {
      var m = msgEls[id];
      if (!m || !meta) return;
      if (meta.created) {
        // Accept ISO strings, epoch ms, or epoch seconds. Date.parse
        // covers the first; the bare-number branch handles the rest.
        var t = (typeof meta.created === 'number')
          ? (meta.created > 1e12 ? meta.created : meta.created * 1000)
          : Date.parse(meta.created);
        if (!isNaN(t)) m.created = t;
        // Re-render the existing action bar's timestamp span so the
        // visible value matches the just-applied timestamp.
        var tsEl = m.bubble.querySelector(':scope > .ui-agent-msg-actions .ui-agent-msg-timestamp');
        if (tsEl) tsEl.textContent = formatTimestamp(m.created);
      }
      if (meta.usage) {
        renderMessageStats({
          id: id,
          input_tokens:     meta.usage.input_tokens || meta.usage.prompt_tokens,
          output_tokens:    meta.usage.output_tokens || meta.usage.completion_tokens,
          reasoning_tokens: meta.usage.reasoning_tokens,
          tokens_per_sec:   meta.usage.tokens_per_sec,
          prompt_per_sec:   meta.usage.prompt_per_sec,
          elapsed_ms:       meta.usage.elapsed_ms,
        });
      }
    }

    // formatTimestamp renders a Date.now()-shaped value as a short
    // chat-style label: "10:23 AM" within today, "Yesterday 14:05" /
    // "Mon 09:12" within the past week, full date thereafter. Not
    // locale-aware on purpose — chat UX wants stable visual width.
    function formatTimestamp(ms) {
      if (!ms) return '';
      var d = new Date(ms);
      var now = new Date();
      var hours = d.getHours();
      var mins = d.getMinutes();
      var hm = (hours < 10 ? '0' : '') + hours + ':' + (mins < 10 ? '0' : '') + mins;
      var sameDay = d.getFullYear() === now.getFullYear() &&
                    d.getMonth() === now.getMonth() &&
                    d.getDate() === now.getDate();
      if (sameDay) return hm;
      var dayDiff = Math.floor((now - d) / 86400000);
      if (dayDiff <= 1) return 'Yesterday ' + hm;
      if (dayDiff < 7) {
        var dn = ['Sun','Mon','Tue','Wed','Thu','Fri','Sat'][d.getDay()];
        return dn + ' ' + hm;
      }
      return d.toISOString().slice(0, 10) + ' ' + hm;
    }

    // attachUserActions adds Edit / Delete / Retry + a hover-only
    // timestamp to a user bubble. Called from addMessage (for the
    // newly-arrived bubble) and from openSession's replay loop (for
    // each restored bubble). Idempotent — re-renders if an existing
    // bar is present, so timestamp updates land cleanly.
    function attachUserActions(bubble) {
      if (!cfg.truncate_url) return;
      var existing = bubble.querySelector(':scope > .ui-agent-msg-actions');
      if (existing) existing.remove();
      renderUserActions(bubble);
    }

    // attachAssistantActions adds Copy (always) + Retry (when the app
    // supports truncate-and-replay) + a hover-only timestamp to an
    // assistant bubble. Copy is client-only — it needs no server
    // endpoint — so it renders for every app, including ones without a
    // truncate_url (e.g. read-only or replay-immutable conversations).
    function attachAssistantActions(bubble) {
      var existing = bubble.querySelector(':scope > .ui-agent-msg-actions');
      if (existing) existing.remove();
      renderAssistantActions(bubble);
    }

    function renderUserActions(bubble) {
      var bar = el('div', {class: 'ui-agent-msg-actions'});
      var ts = msgEntryForBubble(bubble);
      var tsEl = el('span', {class: 'ui-agent-msg-timestamp'},
        [ts && ts.created ? formatTimestamp(ts.created) : '']);
      bar.appendChild(tsEl);
      // Buttons pack LEFT right after the timestamp, matching the
      // assistant-side layout. No spacer — both sides read identically.
      bar.appendChild(el('button', {
        class: 'ui-agent-msg-act',
        title: 'Edit this message and resend',
        onclick: function(){ beginUserEdit(bubble); },
      }, ['Edit']));
      bar.appendChild(el('button', {
        class: 'ui-agent-msg-act',
        title: 'Resend this exact message',
        onclick: function(){ retryUserMessage(bubble); },
      }, ['Retry']));
      bar.appendChild(el('button', {
        class: 'ui-agent-msg-act danger',
        title: 'Delete this message and everything after',
        onclick: function(){ deleteUserMessage(bubble); },
      }, ['Delete']));
      maybeAppendScrub(bar, bubble);
      appendBubbleActions(bar, 'user', bubble);
      bubble.appendChild(bar);
    }

    function renderAssistantActions(bubble) {
      var bar = el('div', {class: 'ui-agent-msg-actions'});
      var ts = msgEntryForBubble(bubble);
      var tsEl = el('span', {class: 'ui-agent-msg-timestamp'},
        [ts && ts.created ? formatTimestamp(ts.created) : '']);
      bar.appendChild(tsEl);
      // Retry / Copy pack LEFT right after the timestamp — no spacer.
      // Retry needs truncate-and-replay, so it's gated on truncate_url;
      // Copy is client-only and always available.
      if (cfg.truncate_url) {
        bar.appendChild(el('button', {
          class: 'ui-agent-msg-act',
          title: 'Regenerate this response (re-runs the previous user message)',
          onclick: function(){ retryAssistantMessage(bubble); },
        }, ['Retry']));
      }
      var copyBtn = el('button', {
        class: 'ui-agent-msg-act',
        title: 'Copy this message to the clipboard',
        onclick: function(){ copyAssistantMessage(bubble, copyBtn); },
      }, ['Copy']);
      bar.appendChild(copyBtn);
      maybeAppendScrub(bar, bubble);
      appendBubbleActions(bar, 'assistant', bubble);
      bubble.appendChild(bar);
    }

    // msgEntryForBubble does the reverse lookup msgEls[*].bubble ===
    // target. Cheap enough at chat-history scale; avoids a separate
    // bubble→id map.
    function msgEntryForBubble(bubble) {
      for (var k in msgEls) {
        if (msgEls[k] && msgEls[k].bubble === bubble) return msgEls[k];
      }
      return null;
    }

    // retryUserMessage re-fires the bubble's stored text through the
    // edit-commit pipeline — same as Edit-with-no-changes. Truncates
    // the session at this bubble's index, drops later DOM, replays.
    function retryUserMessage(bubble) {
      if (!activeSessionId) return;
      var entry = msgEntryForBubble(bubble);
      var raw = (entry && entry.rawText) || bubble.querySelector(':scope > .ui-agent-msg-body').textContent || '';
      if (!raw) return;
      commitUserEdit(bubble, raw).catch(function(err) {
        window.uiAlert('Retry failed: ' + (err && err.message || err));
      });
    }

    // retryAssistantMessage finds the user bubble that preceded this
    // assistant and re-fires it. The framework already truncates +
    // resends via commitUserEdit, which drops this assistant + every
    // later bubble before sending — exactly the "regenerate" UX.
    function retryAssistantMessage(bubble) {
      if (!activeSessionId) return;
      var prev = bubble.previousElementSibling;
      while (prev && !prev.classList.contains('ui-agent-msg-user')) {
        prev = prev.previousElementSibling;
      }
      if (!prev) {
        window.uiAlert('No prior user message to retry from.');
        return;
      }
      retryUserMessage(prev);
    }

    // copyAssistantMessage writes the bubble's rendered text content
    // to the clipboard. Falls back to a textarea+execCommand path
    // for browsers without Clipboard API permission. Brief "Copied"
    // flash on the button so the user sees the action took.
    function copyAssistantMessage(bubble, btn) {
      var body = bubble.querySelector(':scope > .ui-agent-msg-body');
      if (!body) return;
      var text = body.innerText || body.textContent || '';
      var flash = function() {
        if (!btn) return;
        var prior = btn.textContent;
        btn.textContent = 'Copied';
        setTimeout(function(){ btn.textContent = prior; }, 900);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(flash).catch(function() {
          fallback();
        });
        return;
      }
      fallback();
      function fallback() {
        var ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed'; ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); flash(); } catch (_) {}
        document.body.removeChild(ta);
      }
    }

    // copyTurnForTuning was the per-bubble "Copy turn" handler — its
    // button has been removed in favor of the bottom-row Copy session
    // button (which captures the same conversation in one click rather
    // than per-turn). Function kept below for reference but unreachable;
    // delete it once the prompt-tuning flow has been on Copy session
    // for a release and no caller has been added back.
    function copyTurnForTuning(bubble, btn) {
      // Walk back to the user bubble that started this turn.
      var userBubble = bubble;
      while (userBubble && !userBubble.classList.contains('ui-agent-msg-user')) {
        userBubble = userBubble.previousElementSibling;
      }
      if (!userBubble) {
        window.uiAlert('No user prompt found for this turn.');
        return;
      }
      var userEntry = msgEntryForBubble(userBubble);
      var userText = (userEntry && userEntry.rawText) ||
        (userBubble.querySelector(':scope > .ui-agent-msg-body') &&
          userBubble.querySelector(':scope > .ui-agent-msg-body').innerText) || '';
      var lines = ['## User', '', userText.trim(), ''];
      // Walk forward from the user bubble through assistant bubbles
      // until the next user (or end). Each assistant bubble becomes
      // a round; tools attached to the bubble become subsections.
      var roundNum = 0;
      var next = userBubble.nextElementSibling;
      while (next && !next.classList.contains('ui-agent-msg-user')) {
        if (next.classList.contains('ui-agent-msg-assistant')) {
          roundNum++;
          lines.push('## Assistant (round ' + roundNum + ')');
          lines.push('');
          var body = next.querySelector(':scope > .ui-agent-msg-body');
          var txt = body ? (body.innerText || body.textContent || '').trim() : '';
          if (txt) {
            lines.push(txt);
            lines.push('');
          }
          var tools = next.tools || [];
          var fence = '```';
          tools.forEach(function(t) {
            lines.push('### Tool call: ' + (t.name || '(unnamed)'));
            if (t.args !== undefined) {
              var argsStr;
              try { argsStr = JSON.stringify(t.args, null, 2); }
              catch (_) { argsStr = String(t.args); }
              lines.push('args:');
              lines.push(fence + 'json');
              lines.push(argsStr);
              lines.push(fence);
            }
            if (t.output !== undefined && t.output !== null && t.output !== '') {
              lines.push('result:');
              lines.push(fence);
              lines.push(String(t.output));
              lines.push(fence);
            }
            lines.push('');
          });
        }
        next = next.nextElementSibling;
      }
      var blob = lines.join('\n').replace(/\n{3,}/g, '\n\n').trim() + '\n';
      var flash = function() {
        if (!btn) return;
        var prior = btn.textContent;
        btn.textContent = 'Copied';
        setTimeout(function(){ btn.textContent = prior; }, 1100);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(blob).then(flash).catch(function() {
          fallback();
        });
        return;
      }
      fallback();
      function fallback() {
        var ta = document.createElement('textarea');
        ta.value = blob;
        ta.style.position = 'fixed'; ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); flash(); } catch (_) {}
        document.body.removeChild(ta);
      }
    }

    // userBubbleIndex returns the index of this bubble's CORRESPONDING
    // user message in the persisted session — counting ONLY user +
    // assistant bubbles that actually landed in the server's flat
    // sess.Messages slice. System bubbles, empty placeholder bubbles,
    // and activity-log entries are filtered out because the server
    // never persisted them; including them in the count drifts the
    // PATCH-truncate index and leaves the original user message in
    // server storage → next send sees [old user, old reply, new user]
    // and the LLM answers both.
    function userBubbleIndex(target) {
      var all = convoLog.querySelectorAll('.ui-agent-msg');
      var idx = 0;
      for (var i = 0; i < all.length; i++) {
        var b = all[i];
        // Match server-persisted bubbles only: role must be user or
        // assistant, and the bubble must have actually committed
        // content (empty placeholders don't persist).
        var isUser = b.classList.contains('ui-agent-msg-user');
        var isAsst = b.classList.contains('ui-agent-msg-assistant');
        if (!isUser && !isAsst) continue;
        if (b.classList.contains('ui-agent-msg-empty')) continue;
        if (b === target) return idx;
        idx++;
      }
      return -1;
    }

    // commitUserEdit handles the persistence side of an in-place user
    // bubble edit: truncates the session to drop this bubble + every
    // assistant reply after it, then routes newText through the same
    // sendMessage pipeline a fresh user turn uses. Used by the default
    // editor below AND by registered message editors (uiRegisterMessageEditor)
    // so app-specific edit UIs get the same commit semantics for free.
    function commitUserEdit(bubble, newText) {
      var at = userBubbleIndex(bubble);
      if (at < 0) return Promise.reject(new Error('bubble not found'));
      // Cancel any in-flight turn before truncating + re-sending.
      // Without this, an active stream keeps writing assistant
      // bubbles below the just-truncated point AND the server
      // persists that response under the original-message session
      // state, fighting the truncation we're about to do. Calling
      // cancelMessage aborts the active EventSource + POSTs the
      // server's /api/cancel so the in-flight runner stops cleanly.
      cancelMessage();
      return truncateSession(at).then(function() {
        while (convoLog.lastChild && convoLog.lastChild !== bubble) {
          convoLog.removeChild(convoLog.lastChild);
        }
        convoLog.removeChild(bubble);
        Object.keys(msgEls).forEach(function(k) {
          if (!msgEls[k] || !msgEls[k].bubble || !msgEls[k].bubble.parentNode) {
            delete msgEls[k];
          }
        });
        inputArea.value = newText;
        autosizeInput();
        sendMessage();
      });
    }

    // messageEditors is the registry of override editors. Each entry
    // is {match, edit}; beginUserEdit walks it in registration order
    // and the first match takes over from the default textarea path.
    // Apps register via window.uiRegisterMessageEditor so bubbles they
    // own (e.g. an intake-form bubble whose visual is the form widgets)
    // can present a domain-appropriate edit surface instead of "raw
    // markdown in a textarea".
    var messageEditors = [];
    window.uiRegisterMessageEditor = function(matchFn, editFn) {
      if (typeof matchFn !== 'function' || typeof editFn !== 'function') return;
      messageEditors.push({match: matchFn, edit: editFn});
    };

    function beginUserEdit(bubble) {
      if (!activeSessionId) return;
      var body = bubble.querySelector(':scope > .ui-agent-msg-body');
      var actions = bubble.querySelector(':scope > .ui-agent-msg-actions');
      if (!body) return;
      var raw = (msgEls && Object.keys(msgEls).reduce(function(acc, k) {
        if (msgEls[k] && msgEls[k].bubble === bubble) return msgEls[k].rawText || body.textContent;
        return acc;
      }, '')) || body.textContent;
      // Check registered editors first. The override gets a ctx with
      // bubble + body + actions + rawText + commit(newText) so it can
      // build whatever UI fits its bubble type. cancel() is a no-op
      // hint — the override owns DOM cleanup, but if it just wants the
      // default body+actions visibility restored, it can call this.
      for (var i = 0; i < messageEditors.length; i++) {
        var entry = messageEditors[i];
        var hit = false;
        try { hit = !!entry.match(bubble); } catch (e) { hit = false; }
        if (hit) {
          entry.edit({
            bubble: bubble,
            body: body,
            actions: actions,
            rawText: raw,
            commit: function(newText) { return commitUserEdit(bubble, newText); },
            cancel: function() {
              body.style.display = '';
              if (actions) actions.style.display = '';
            },
          });
          return;
        }
      }
      body.style.display = 'none';
      if (actions) actions.style.display = 'none';
      // Default rows is a starting hint; autosizeEdit grows the
      // textarea to fit the actual content immediately after mount
      // (so a long message gets a tall edit area without the user
      // having to drag the resize handle). Capped via CSS max-height
      // so a huge message doesn't push the rest of the chat off-screen.
      var ta = el('textarea', {class: 'ui-agent-msg-edit-ta', rows: '8'});
      ta.value = raw;
      function autosizeEdit() {
        ta.style.height = 'auto';
        ta.style.height = (ta.scrollHeight + 2) + 'px';
      }
      ta.addEventListener('input', autosizeEdit);
      var save = el('button', {class: 'ui-agent-msg-act primary', onclick: function() {
        var newText = ta.value.trim();
        if (!newText) return;
        save.disabled = true;
        commitUserEdit(bubble, newText).catch(function(err) {
          save.disabled = false;
          window.uiAlert('Edit failed: ' + (err && err.message || err));
        });
      }}, ['Save & resend']);
      var cancel = el('button', {class: 'ui-agent-msg-act', onclick: function() {
        editBar.remove();
        body.style.display = '';
        if (actions) actions.style.display = '';
      }}, ['Cancel']);
      var editBar = el('div', {class: 'ui-agent-msg-edit-bar'},
        [ta, el('div', {class: 'ui-agent-msg-edit-actions'}, [save, cancel])]);
      bubble.appendChild(editBar);
      // Defer the initial sizing past mount so the element has a real
      // computed scrollHeight to read (same pattern as the main input).
      setTimeout(autosizeEdit, 0);
      ta.focus();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    }

    async function deleteUserMessage(bubble) {
      if (!activeSessionId) return;
      if (!(await window.uiConfirm('Delete this message and everything below it?'))) return;
      var at = userBubbleIndex(bubble);
      if (at < 0) return;
      truncateSession(at).then(function() {
        // Wipe DOM from this bubble onward.
        while (convoLog.lastChild && convoLog.lastChild !== bubble) {
          convoLog.removeChild(convoLog.lastChild);
        }
        convoLog.removeChild(bubble);
        Object.keys(msgEls).forEach(function(k) {
          if (!msgEls[k] || !msgEls[k].bubble || !msgEls[k].bubble.parentNode) {
            delete msgEls[k];
          }
        });
        // No re-attach needed — every user bubble already has its
        // own action bar (we no longer treat the latest specially).
      }).catch(function(err) {
        window.uiAlert('Delete failed: ' + (err && err.message || err));
      });
    }

    // truncateSession PATCHes the server's session record to drop
    // messages from index "at" onward. Returns the fetch promise so
    // callers can chain DOM updates on success or surface the error.
    function truncateSession(at) {
      var url = substituteExtras(cfg.truncate_url.replace('{id}', encodeURIComponent(activeSessionId)));
      return fetch(url, {
        method: 'PATCH',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({at: at}),
      }).then(function(r) {
        if (!r.ok) {
          return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        }
        return r.json();
      });
    }

    // maybeAppendScrub adds a ✕ that deletes JUST this one message (not a
    // truncate-from-here) when the app opted in via cfg.msg_scrub AND this
    // bubble has a known raw storage index (replayed bubbles only). Keeps every
    // later turn intact — the in-thread replacement for the old History
    // row-delete. Live bubbles have no index yet; they get one on reload.
    function maybeAppendScrub(bar, bubble) {
      if (!cfg.msg_scrub || !cfg.truncate_url) return;
      var entry = msgEntryForBubble(bubble);
      if (!entry || typeof entry.storageIndex !== 'number') return;
      bar.appendChild(el('button', {
        class: 'ui-agent-msg-act danger',
        title: 'Delete just this message (keep the rest of the thread)',
        onclick: function(){ scrubMessage(bubble); },
      }, ['✕']));
    }

    // scrubMessage deletes one message by its raw storage index, keeping the
    // rest of the thread, then reloads the session so every bubble's
    // storageIndex re-syncs after the array shifts down.
    async function scrubMessage(bubble) {
      if (!activeSessionId) return;
      var entry = msgEntryForBubble(bubble);
      if (!entry || typeof entry.storageIndex !== 'number') return;
      if (!(await window.uiConfirm('Delete just this message? The rest of the thread stays.'))) return;
      var url = substituteExtras(cfg.truncate_url.replace('{id}', encodeURIComponent(activeSessionId)));
      fetch(url, {
        method: 'PATCH',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({delete_at: entry.storageIndex}),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function() {
        openSession(activeSessionId);
      }).catch(function(err) {
        window.uiAlert('Delete failed: ' + (err && err.message || err));
      });
    }

    // unmarkEmptyBubble strips the .ui-agent-msg-empty class once
    // the bubble actually has visible content. Called by chunk
    // handlers + finalizeMessage so an empty assistant bubble (lazy
    // materialization for tool_call events) stays hidden until real
    // text streams.
    function unmarkEmptyBubble(m) {
      if (m && m.bubble) m.bubble.classList.remove('ui-agent-msg-empty');
    }
    function appendChunk(id, text) {
      var m = msgEls[id];
      if (!m) { m = addMessage('assistant', id, ''); }
      m.rawText = (m.rawText || '') + text;
      // Streaming text stays plain — markdown pass on message_done.
      m.body.textContent = m.rawText;
      if (m.rawText.length > 0) unmarkEmptyBubble(m);
      scrollConvo(false);
    }

    function replaceChunk(id, text) {
      var m = msgEls[id];
      if (!m) { m = addMessage('assistant', id, ''); }
      m.rawText = text || '';
      m.body.textContent = m.rawText;
      if (m.rawText.length > 0) unmarkEmptyBubble(m);
      scrollConvo(false);
    }

    function finalizeMessage(id) {
      var m = msgEls[id];
      if (!m) return;
      if (cfg.markdown && m.role === 'assistant') {
        uiRenderMarkdown(m.body, m.rawText || '');
      }
      // If the turn ended without any text (rare, e.g. tool-only
      // round), keep the bubble hidden so the user doesn't see an
      // empty card. The next assistant turn will land in a fresh
      // bubble; spinner stays cleared by message_done's caller.
      if ((m.rawText || '').length > 0) unmarkEmptyBubble(m);
      // Streaming-mode pre-wrap is no longer needed once mdToHTML
      // emits structured block elements (p / pre / lists handle
      // their own whitespace). Leaving it on would add weird gaps
      // between block tags.
      if (m.bubble) m.bubble.classList.remove('ui-agent-msg-streaming');
      // Fire registered decorators so apps can append per-message
      // affordances (save buttons, copy actions, …).
      var decorators = window.UIMessageDecorators || [];
      for (var i = 0; i < decorators.length; i++) {
        try {
          decorators[i]({
            role:    m.role,
            id:      id,
            wrap:    m.bubble,
            body:    m.body,
            rawText: m.rawText || '',
          });
        } catch (_) {}
      }
      // Tell a host surface (e.g. a WorkbenchPanel) a reply finalized — its
      // co-author tool may have written into the open document. Idempotent.
      try { window.dispatchEvent(new CustomEvent('ui-chat-round-done')); } catch (e) {}
    }

    // --- Inline tool-call rendering ---
    // attachAgentToolToggle creates (or refreshes) the "🔧 N tools"
    // toggle on a message bubble and re-renders the panel when open.
    // Bubble must have a .tools = [{name, args, output}] array
    // attached. Mirrors ChatPanel's attachToolToggle, restyled for
    // AgentLoopPanel. Idempotent — safe to call after each
    // tool_call / tool_result event to refresh the count + open
    // panels with fresh result data.
    function renderAgentToolPanel(panel) {
      var tools = panel.parentNode && panel.parentNode.tools || [];
      panel.innerHTML = '';
      tools.forEach(function(t) {
        // Pipeline-mode temp tools get a 🪈 prefix in the entry name
        // so the user can see at a glance which calls dispatched a
        // sub-agent loop vs a regular registered tool.
        var displayName = (t.kind === 'pipeline' ? '🪈 ' : '') + '→ ' + t.name;
        var summaryChildren = [el('span', {class: 'ui-agent-tool-name'}, [displayName])];
        if (t.args) summaryChildren.push(el('span', {class: 'ui-agent-tool-args'}, [t.args]));
        var summary = el('summary', {class: 'ui-agent-tool-summary'}, summaryChildren);
        var det = el('details', {class: 'ui-agent-tool'});
        det.appendChild(summary);
        var body = el('div', {class: 'ui-agent-tool-body'});
        if (t.output === null) {
          body.appendChild(el('div', {class: 'ui-agent-tool-empty'}, ['(running…)']));
        } else {
          var trimmed = String(t.output || '').trim();
          if (!trimmed) {
            body.appendChild(el('div', {class: 'ui-agent-tool-empty'}, ['(no output)']));
          } else {
            var pre = el('pre', {class: 'ui-agent-tool-result'});
            pre.textContent = t.output;
            body.appendChild(pre);
          }
        }
        det.appendChild(body);
        panel.appendChild(det);
      });
    }
    function attachAgentToolToggle(msgEl) {
      if (!msgEl || !msgEl.tools) return;
      var count = msgEl.tools.length;
      var label = '🔧 ' + count + ' tool' + (count === 1 ? '' : 's');
      // Toggle button lives in the action bar (right of Retry/Copy)
      // so it sits with the other hover-revealed meta controls. The
      // expansion panel still drops INTO the bubble (between body
      // and action bar) so when opened the tool list expands inline
      // under the message rather than floating from the bar.
      var bar = msgEl.querySelector(':scope > .ui-agent-msg-actions');
      var panel = msgEl.querySelector(':scope > .ui-agent-tools-panel');
      if (!panel) {
        panel = el('div', {class: 'ui-agent-tools-panel', style: 'display:none'});
        if (bar) msgEl.insertBefore(panel, bar);
        else msgEl.appendChild(panel);
      }
      // Look for an existing toggle on this host in BOTH places it
      // might live: inside the action bar (regular assistant bubbles)
      // OR as a direct child (plan/intent cards have no action bar,
      // so the toggle was appended at the card level). Without the
      // second lookup, every tool_call on a plan card created a NEW
      // toggle instead of updating the existing one.
      var toggle = bar ? bar.querySelector('.ui-agent-tools-toggle')
                       : msgEl.querySelector(':scope > .ui-agent-tools-toggle');
      if (toggle) {
        toggle.textContent = label;
        if (panel.style.display !== 'none') renderAgentToolPanel(panel);
        return;
      }
      toggle = el('button', {
        class: 'ui-agent-msg-act ui-agent-tools-toggle',
        title: 'Show tool calls used in this turn',
        onclick: function() {
          var open = panel.style.display !== 'none';
          panel.style.display = open ? 'none' : '';
          toggle.classList.toggle('open', !open);
          if (!open) renderAgentToolPanel(panel);
        },
      }, [label]);
      if (bar) bar.appendChild(toggle);
      else msgEl.appendChild(toggle); // fallback: no action bar (e.g. truncate_url off)
    }

    // ensureMsgBubbleFor materializes (or returns) a bubble keyed by
    // id. Used by tool_call / tool_result handlers — when the server
    // emits a tool event before any chunk has streamed, we need a
    // bubble to attach the tool affordances to.
    function ensureMsgBubbleFor(id, role) {
      if (!id) return null;
      var m = msgEls[id];
      if (!m || !m.bubble) {
        m = addMessage(role || 'assistant', id, '');
      }
      return m;
    }

    // toolHostFor picks WHERE a tool_call's pill+panel should hang.
    // Default is the bubble itself. When the bubble is the hidden
    // lazy-materialized empty (.ui-agent-msg-empty), tool affordances
    // would be invisible — instead we walk back through the convo
    // log to the previous visible block (plan card, intent card,
    // prior assistant turn) and pin the pill there so the user sees
    // tool activity on the most recent on-screen surface. Cached on
    // msgEls[id].toolHost so tool_result can find the same target.
    function toolHostFor(m) {
      if (!m || !m.bubble) return null;
      if (m.toolHost) return m.toolHost;
      var host = m.bubble;
      if (m.bubble.classList.contains('ui-agent-msg-empty')) {
        var prev = m.bubble.previousElementSibling;
        while (prev) {
          // Skip the thinking spinner + other empty bubbles.
          if (prev === thinkingEl || prev.classList.contains('ui-agent-msg-empty')) {
            prev = prev.previousElementSibling;
            continue;
          }
          host = prev;
          break;
        }
      }
      m.toolHost = host;
      return host;
    }

    // agentMsgAttachmentBox returns (creating on first call) the per-
    // bubble container that holds inline image / file / video deliveries.
    // Box hangs off the bottom of the bubble so it lands BELOW the text
    // body and any tool-toggle panel.
    function agentMsgAttachmentBox(bubble) {
      var box = bubble.querySelector(':scope > .ui-agent-msg-attachments');
      if (!box) {
        box = el('div', {class: 'ui-agent-msg-attachments'});
        bubble.appendChild(box);
      } else {
        bubble.appendChild(box); // keep it last
      }
      return box;
    }

    // openImageLightbox shows the given src full-screen as a dimmed
    // overlay; click anywhere or press Esc to dismiss. Used by both
    // user-attached and tool-delivered images. Browsers block
    // window.open() with data: URLs, so an in-page overlay is the
    // only reliable click-to-zoom for inline base64 images.
    function openImageLightbox(src) {
      var overlay = el('div', {class: 'ui-img-lightbox'});
      var imgEl = el('img', {src: src, class: 'ui-img-lightbox-img', alt: 'zoomed'});
      overlay.appendChild(imgEl);
      function dismiss() {
        if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
        document.removeEventListener('keydown', onKey);
      }
      function onKey(ev) { if (ev.key === 'Escape') dismiss(); }
      overlay.addEventListener('click', dismiss);
      document.addEventListener('keydown', onKey);
      document.body.appendChild(overlay);
    }

    function renderAgentImage(bubble, b64) {
      // Detect the actual mime from base64 magic-byte prefixes.
      // Browsers usually sniff regardless of the data-URL declared
      // mime, but matching the declared mime to the bytes avoids
      // edge cases and is just correct. PNG was the historical
      // hardcoded default but tools can emit JPG/GIF/WEBP too.
      var mime = 'image/png';
      if (b64.indexOf('iVBORw0KGgo') === 0) mime = 'image/png';
      else if (b64.indexOf('/9j/') === 0) mime = 'image/jpeg';
      else if (b64.indexOf('R0lG') === 0) mime = 'image/gif';
      else if (b64.indexOf('UklGR') === 0) mime = 'image/webp';
      var src = 'data:' + mime + ';base64,' + b64;
      var img = el('img', {src: src, class: 'ui-agent-msg-image', alt: 'image'});
      img.addEventListener('click', function() { openImageLightbox(src); });
      agentMsgAttachmentBox(bubble).appendChild(img);
    }

    function renderAgentVideo(bubble, b64) {
      var vid = el('video', {
        src: 'data:video/mp4;base64,' + b64,
        class: 'ui-agent-msg-video',
        controls: true,
      });
      agentMsgAttachmentBox(bubble).appendChild(vid);
    }

    function renderAgentFile(bubble, ev) {
      var mt = ev.mime_type || 'application/octet-stream';
      var sizeStr = '';
      if (ev.size) {
        var n = ev.size;
        if (n >= 1048576) sizeStr = (n/1048576).toFixed(1) + ' MB';
        else if (n >= 1024) sizeStr = (n/1024).toFixed(1) + ' KB';
        else sizeStr = n + ' B';
      }
      var name = ev.name || 'file';
      var label = '📎 ' + name + (sizeStr ? ' (' + sizeStr + ')' : '');
      var link = el('a', {
        class: 'ui-agent-msg-file',
        href: 'data:' + mt + ';base64,' + (ev.data || ''),
        download: name,
      }, [label]);
      var box = agentMsgAttachmentBox(bubble);
      // HTML deliveries also get a View button: decode the payload and
      // open it in the shared artifact pane. Authored-HTML mode — the
      // file is generated content, so it renders SANDBOXED (opaque
      // origin), same trust as a show_html document, never with the
      // app's own origin.
      var isHTML = /text\/html/i.test(mt) || /\.html?$/i.test(name);
      if (isHTML && window.uiOpenArtifactPane) {
        var row = el('span', {class: 'ui-agent-msg-file-row'});
        row.appendChild(link);
        var viewBtn = el('button', {class: 'ui-row-btn', type: 'button',
          title: 'Open in the viewer pane'}, ['View']);
        viewBtn.addEventListener('click', function() {
          var html = '';
          try {
            var bytes = atob(ev.data || '');
            var arr = new Uint8Array(bytes.length);
            for (var i = 0; i < bytes.length; i++) arr[i] = bytes.charCodeAt(i);
            html = new TextDecoder('utf-8').decode(arr);
          } catch (_) {}
          if (!html) { showToast('Could not decode ' + name); return; }
          window.uiOpenArtifactPane({id: 'file:' + name, title: name, html: html});
        });
        row.appendChild(viewBtn);
        box.appendChild(row);
        return;
      }
      box.appendChild(link);
    }

    // renderMessageStats appends a small footer ("12.3 tk/s · 230 out
    // · 1450 in · 187 think · 18.7s") to an assistant bubble when the
    // server emits a {kind:"stats", id, ...} payload. Same shape the
    // chat app uses; nil-safe when fields are missing. Replaces an
    // existing footer if the same bubble gets multiple stats events
    // (e.g. multi-round agent loops emitting per-round stats).
    function renderMessageStats(ev) {
      if (!ev || !ev.id) return;
      var m = msgEls[ev.id];
      if (!m || !m.bubble) return;
      // Only render when there's at least one meaningful number;
      // tool-only rounds without LLM output get a sparse payload
      // that doesn't deserve a footer.
      if (!ev.output_tokens && !ev.input_tokens && !ev.elapsed_ms) return;
      var parts = [];
      if (ev.tokens_per_sec)   parts.push(ev.tokens_per_sec.toFixed(1) + ' tk/s');
      if (ev.prompt_per_sec)   parts.push(ev.prompt_per_sec.toFixed(0) + ' prefill');
      if (ev.elapsed_ms)       parts.push((ev.elapsed_ms / 1000).toFixed(1) + 's');
      if (ev.input_tokens)     parts.push(ev.input_tokens + ' in');
      if (ev.output_tokens)    parts.push(ev.output_tokens + ' out');
      if (ev.reasoning_tokens) parts.push(ev.reasoning_tokens + ' think');
      if (!parts.length) return;
      var existing = m.bubble.querySelector(':scope > .ui-agent-stats');
      if (existing) existing.remove();
      var footer = el('div', {class: 'ui-agent-stats'}, [parts.join(' · ')]);
      // Place stats ABOVE the action bar (Retry/Copy/timestamp).
      // The bar lives at the bottom of the bubble container, so we
      // insert stats before it when present.
      var bar = m.bubble.querySelector(':scope > .ui-agent-msg-actions');
      if (bar) m.bubble.insertBefore(footer, bar);
      else m.bubble.appendChild(footer);
    }

    function addActivity(type, id, text) {
      var line = el('div', {class: 'ui-agent-act ui-agent-act-' + (type || 'status')});
      if (type === 'cmd') {
        line.textContent = '$ ' + (text || '');
      } else if (type === 'output') {
        line.classList.add('collapsed');
        line.textContent = text || '';
        line.addEventListener('click', function() {
          if (line.classList.contains('no-truncate')) return;
          line.classList.toggle('collapsed');
        });
        // After the line lands in the DOM we measure: if the
        // content fits within the collapsed max-height, drop the
        // show-more affordance entirely. Output rows that are
        // genuinely short shouldn't pretend to be truncated.
        setTimeout(function() {
          if (line.scrollHeight <= line.clientHeight + 2) {
            line.classList.add('no-truncate');
            line.classList.remove('collapsed');
          }
        }, 0);
      } else if (type === 'watch') {
        line.appendChild(el('span', {class: 'ui-spinner'}));
        line.appendChild(document.createTextNode(' ' + (text || '')));
      } else if (type === 'error') {
        line.textContent = 'Error: ' + (text || '');
      } else {
        line.textContent = text || '';
      }
      if (id) activityEls[id] = line;
      activityLog.appendChild(line);
      activityLog.scrollTop = activityLog.scrollHeight;
    }

    function updateActivity(id, text) {
      var line = activityEls[id];
      if (!line) return;
      // Preserve type-specific structure when updating (watch
      // keeps its spinner). Fallback to textContent.
      var spin = line.querySelector('.ui-spinner');
      if (spin) {
        line.innerHTML = '';
        line.appendChild(spin);
        line.appendChild(document.createTextNode(' ' + (text || '')));
      } else if (line.classList.contains('ui-agent-act-cmd')) {
        line.textContent = '$ ' + (text || '');
      } else {
        line.textContent = text || '';
      }
    }

    function addConfirm(d) {
      var id = d.id || '';
      var card = el('div', {class: 'ui-agent-confirm', id: 'confirm-' + id});
      card.appendChild(el('div', {class: 'ui-agent-confirm-prompt'}, [d.prompt || 'Confirm?']));
      if (d.detail) {
        card.appendChild(el('div', {class: 'ui-agent-confirm-detail'}, [d.detail]));
      }
      var btns = el('div', {class: 'ui-agent-confirm-btns'});
      (d.actions || []).forEach(function(a) {
        var cls = 'ui-row-btn';
        if (a.variant) cls += ' ' + a.variant;
        var b = el('button', {class: cls,
          onclick: function() { submitConfirm(id, a, card, b); }},
          [a.label || a.value || 'OK']);
        btns.appendChild(b);
      });
      card.appendChild(btns);
      // Render approval prompts in the MAIN conversation pane, not the
      // activity pane. They're decisions the user must act on — they
      // belong in the primary reading flow, and this keeps them visible
      // on surfaces that hide the activity pane (e.g. mobile). Falls
      // back to the activity log only if the convo pane is somehow
      // unavailable.
      (convoLog || activityLog).appendChild(card);
      scrollConvo(true);
    }

    function submitConfirm(id, action, card, btn) {
      if (!cfg.confirm_url) return;
      var value = (action && action.value) || '';
      // Disable all buttons immediately so a double-click doesn't
      // submit the same answer twice; flag the chosen one so the
      // resolved state can highlight which way the operator went.
      card.querySelectorAll('button').forEach(function(b) { b.disabled = true; });
      if (btn) btn.classList.add('chosen');
      fetchJSON(cfg.confirm_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: id, value: value}),
      }).then(function() {
        // Reflect the decision on the card instead of leaving greyed-
        // out buttons that read as "nothing happened." The button row
        // is replaced with a one-line resolution stamp (✓ Allowed /
        // ✓ Always / ✕ Denied) and the card gets a value class so CSS
        // can tint it. Label comes from the action so app-defined
        // wording (Allow / Always allow / Deny) carries through.
        card.classList.add('ui-agent-confirm-resolved', 'is-' + (value || 'done'));
        var deny = value === 'deny' || value === 'no' || value === 'reject';
        var label = (action && action.label) || value || 'Done';
        var stamp = el('div', {class: 'ui-agent-confirm-status'},
          [(deny ? '✕ ' : '✓ ') + label]);
        var row = card.querySelector('.ui-agent-confirm-btns');
        if (row) { row.replaceWith(stamp); } else { card.appendChild(stamp); }
      }).catch(function(err) {
        // Failed to record — re-enable so the operator can retry.
        card.querySelectorAll('button').forEach(function(b) { b.disabled = false; });
        if (btn) btn.classList.remove('chosen');
        showToast('Confirm failed: ' + (err && err.message || err));
      });
    }

    // App-registered block renderer dispatcher — same shape as
    // PipelinePanel uses. Apps register via window.uiRegisterBlockRenderer.
    function addBlock(d) {
      var id = d.id || '';
      var fn = window.UIBlockRenderers && window.UIBlockRenderers[d.type];
      if (typeof fn !== 'function') {
        // Fallback: render as a plain activity row so the event isn't
        // lost. App can register a proper renderer later.
        if (window.console && console.warn) {
          console.warn('[ui] no block renderer for type:', d.type,
            '— registered:', Object.keys(window.UIBlockRenderers || {}));
        }
        addActivity('status', id, '[' + d.type + '] ' + (d.text || d.title || ''));
        return;
      }
      // Update-in-place when the same id arrives again. The renderer
      // can opt in by returning {wrap, body, onUpdate}; onUpdate gets
      // the new event data and is expected to refresh the existing
      // DOM. Use case: plan checklists that stream status changes.
      var existing = blockEls[id];
      if (existing && typeof existing.onUpdate === 'function') {
        try { existing.onUpdate(d); } catch (_) {}
        return;
      }
      var built = fn(d, {sessionId: activeSessionId});
      if (!built || !built.wrap) return;
      blockEls[id] = built;
      // App blocks default to the conversation pane (the more
      // visible area). If the block specifies pane:"activity",
      // route there instead.
      var target = d.pane === 'activity' ? activityLog : convoLog;
      target.appendChild(built.wrap);
      if (target === convoLog) {
        scrollConvo(false);
      } else {
        target.scrollTop = target.scrollHeight;
      }
    }

    function setStatus(text) {
      if (!text) { statusBar.style.display = 'none'; statusBar.textContent = ''; return; }
      statusBar.style.display = '';
      statusBar.textContent = text;
    }

    // --- SSE handling -----------------------------------------------------

    // Heartbeat watch — fires "Still processing… (Ns)" into the
    // activity pane after a configurable quiet period (28s). Lets
    // the user see that a long LLM round is still in flight, not
    // a hung session. Cleared on every incoming event and torn
    // down on enableInput.
    var lastEventTime = 0;
    var heartbeatTimer = null;
    var heartbeatEl = null;
    function startHeartbeat() {
      stopHeartbeat();
      lastEventTime = Date.now();
      heartbeatTimer = setInterval(function() {
        var elapsed = Date.now() - lastEventTime;
        if (elapsed <= 28000) {
          if (heartbeatEl) { heartbeatEl.remove(); heartbeatEl = null; }
          return;
        }
        var secs = Math.round(elapsed / 1000);
        if (!heartbeatEl) {
          heartbeatEl = el('div', {class: 'ui-agent-act ui-agent-act-watch'});
          heartbeatEl.appendChild(el('span', {class: 'ui-spinner'}));
          heartbeatEl.appendChild(document.createTextNode(
            ' Still processing… (' + secs + 's)'));
          activityLog.appendChild(heartbeatEl);
          activityLog.scrollTop = activityLog.scrollHeight;
        } else {
          // Re-use the spinner span; just refresh the trailing text.
          var spin = heartbeatEl.querySelector('.ui-spinner');
          heartbeatEl.innerHTML = '';
          if (spin) heartbeatEl.appendChild(spin);
          heartbeatEl.appendChild(document.createTextNode(
            ' Still processing… (' + secs + 's)'));
        }
      }, 5000);
    }
    function stopHeartbeat() {
      if (heartbeatTimer) { clearInterval(heartbeatTimer); heartbeatTimer = null; }
      if (heartbeatEl) { heartbeatEl.remove(); heartbeatEl = null; }
    }

    function handleEvent(ev) {
      // Bump the heartbeat clock on every event — clears the
      // "still processing" indicator if it was showing.
      lastEventTime = Date.now();
      if (heartbeatEl) { heartbeatEl.remove(); heartbeatEl = null; }
      if (!ev || !ev.kind) return;
      // Track received-event count so a /api/runs/<id>/stream
      // reconnect can resume from the gap with ?since=<count>.
      // Every event passing handleEvent — regardless of kind — is
      // one server-Seq tick on the run buffer (Ping/keepalives stay
      // out of the buffer; see sseWriter.emit in runner.go).
      runSeqReceived++;
      // Drop the thinking indicator only on events that PRODUCE
      // CONVERSATION-PANE content. activity rows go to the activity
      // pane (which some apps lock off entirely), so they
      // shouldn't kill the spinner — otherwise an early
      // emitStatus("Thinking…") would clear the spinner before any
      // real reply arrives and leave the user staring at empty space.
      // session/status events also don't clear; they fire before
      // content arrives and the spinner bridges that gap.
      switch (ev.kind) {
        case 'chunk':
        case 'chunk_replace':
          // Spinner used to clear here (on first response text), but
          // the new behavior keeps it visible across the whole turn —
          // it RELOCATES to sit above each new assistant bubble (see
          // the move-to-end in appendMessage). Clear only happens on
          // enableInput (turn end).
          break;
        case 'message':
          // Same: don't clear mid-turn. The spinner stays as a "more
          // is still coming" anchor above whichever bubble is most
          // recent; relocated automatically when a new bubble is
          // minted.
          break;
      }
      switch (ev.kind) {
        case 'session':
          activeSessionId = ev.id || '';
          if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, activeSessionId);
          // Refresh the side rail so brand-new sessions land in the
          // list the moment the server creates them, instead of
          // waiting for the next page load or session click.
          if (hasList) loadSessions();
          // App-side hook — fires the full session event so app
          // handlers can react (e.g. set the appliance picker on
          // reconnect from an app-specific session-id field).
          try {
            window.dispatchEvent(new CustomEvent('ui-agent-session',
              {detail: ev}));
          } catch (_) {}
          break;
        case 'run':
          // Server-issued run identifier. Arrives once per turn
          // (right after the session event, at server Seq=2). The
          // top-of-handleEvent counter already ticked, so just
          // capture the id and let the count keep accumulating.
          activeRunId = ev.id || '';
          break;
        case 'message':
          addMessage(ev.role || 'assistant', ev.id || ('m-' + Date.now()), ev.text || '');
          break;
        case 'chunk':
          appendChunk(ev.id, ev.text || '');
          break;
        case 'chunk_replace':
          replaceChunk(ev.id, ev.text || '');
          break;
        case 'message_done':
          finalizeMessage(ev.id);
          break;
        case 'stats':
          renderMessageStats(ev);
          break;
        case 'tool_call': {
          // Inline tool-call card on the targeted bubble — OR on the
          // previous visible block when the bubble's still empty (so
          // tool activity surfaces on a plan/intent card the user is
          // already looking at instead of a hidden materialization).
          // ev.name and ev.args describe the call; output lands via
          // tool_result. ev.call_id pairs the result back to THIS
          // specific call (see tool_result case below).
          var bm = ensureMsgBubbleFor(ev.msg_id, 'assistant');
          if (!bm || !bm.bubble) break;
          var host = toolHostFor(bm);
          if (!host) break;
          if (!host.tools) host.tools = [];
          host.tools.push({
            call_id: ev.call_id || '',
            name: ev.name || 'tool',
            args: ev.args || '',
            output: null,
            kind: ev.tool_kind || '',
          });
          attachAgentToolToggle(host);
          scrollConvo(false);
          break;
        }
        case 'tool_result': {
          var bm2 = msgEls[ev.msg_id];
          if (!bm2 || !bm2.bubble) break;
          var host2 = toolHostFor(bm2);
          if (!host2 || !host2.tools) break;
          // Pair the result back to the exact tool_call that emitted
          // it via call_id. Without call_id the renderer used to fall
          // back to "last unmatched call" positional pairing, which
          // silently mis-attributes results when calls don't strictly
          // settle in emission order (async dispatch, parallel tool
          // calls in one model response, cached short-circuits
          // emitted alongside live calls). The result was a 401/404
          // showing under the wrong tool name in the UI.
          //
          // Fallback: when an event arrives without a call_id (older
          // server, in-flight buffered events during a deploy), keep
          // the legacy "last unmatched" pairing so nothing breaks.
          var resultText = ev.result;
          if (resultText == null) resultText = ev.output;
          var matched = false;
          if (ev.call_id) {
            for (var ti = host2.tools.length - 1; ti >= 0; ti--) {
              if (host2.tools[ti].call_id === ev.call_id) {
                host2.tools[ti].output = String(resultText == null ? '' : resultText);
                matched = true;
                break;
              }
            }
          }
          if (!matched) {
            for (var ti2 = host2.tools.length - 1; ti2 >= 0; ti2--) {
              if (host2.tools[ti2].output === null) {
                host2.tools[ti2].output = String(resultText == null ? '' : resultText);
                break;
              }
            }
          }
          attachAgentToolToggle(host2); // refresh count + open panel
          scrollConvo(false);
          break;
        }
        case 'event': {
          // App-defined event channel — relay to a window CustomEvent
          // named "ui-agent-event:<name>" so app-side JS (registered
          // via ExtraHeadHTML) can react without each new event
          // needing a framework-side dispatcher case. ev.detail (or
          // the full event payload) is forwarded as detail.
          if (ev.name) {
            try {
              window.dispatchEvent(new CustomEvent('ui-agent-event:' + ev.name,
                {detail: ev.detail || ev}));
            } catch (_) {}
          }
          break;
        }
        case 'image': {
          // Inline image delivered by a tool — base64 PNG/JPEG. Lands
          // in the targeted bubble's attachment box (lazy-materialize
          // an assistant bubble if the call arrived before any text).
          var bmi = ensureMsgBubbleFor(ev.msg_id, 'assistant');
          if (bmi && bmi.bubble && ev.data) {
            renderAgentImage(bmi.bubble, ev.data);
            scrollConvo(false);
          }
          break;
        }
        case 'file': {
          var bmf = ensureMsgBubbleFor(ev.msg_id, 'assistant');
          if (bmf && bmf.bubble && ev.data) {
            renderAgentFile(bmf.bubble, ev);
            scrollConvo(false);
          }
          break;
        }
        case 'video': {
          var bmv = ensureMsgBubbleFor(ev.msg_id, 'assistant');
          if (bmv && bmv.bubble && ev.data) {
            renderAgentVideo(bmv.bubble, ev.data);
            scrollConvo(false);
          }
          break;
        }
        case 'activity':
          addActivity(ev.type || 'status', ev.id || '', ev.text || '');
          break;
        case 'activity_update':
          updateActivity(ev.id, ev.text || '');
          break;
        case 'confirm':
          addConfirm(ev);
          break;
        case 'block':
          addBlock(ev);
          break;
        case 'block_done': {
          var be = blockEls[ev.id];
          if (be && be.onDone) be.onDone();
          break;
        }
        case 'block_remove': {
          var be2 = blockEls[ev.id];
          if (be2 && be2.wrap && be2.wrap.parentNode) be2.wrap.remove();
          delete blockEls[ev.id];
          break;
        }
        case 'status':
          setStatus(ev.text || '');
          break;
        case 'status_note':
          // Persistent mid-turn status from send_status, rendered as a
          // real message bubble (same card chrome as a normal reply) so
          // it reads like the agent talking — just tinted + accent-
          // striped to mark it as interim status, not the settled
          // answer. Unlike the ephemeral topbar 'status' bar (cleared on
          // 'done'), this stays in the conversation flow above the reply.
          // No action bar / msgEls registration — it isn't a turn message
          // and must not interfere with the streaming-bubble bookkeeping.
          if (ev.text) {
            clearEmpty();
            var snBubble = el('div', {class: 'ui-agent-msg ui-agent-msg-status'});
            var snBody = el('div', {class: 'ui-agent-msg-body'});
            if (cfg.markdown) { uiRenderMarkdown(snBody, ev.text); }
            else { snBody.textContent = ev.text; }
            snBubble.appendChild(snBody);
            convoLog.appendChild(snBubble);
            // Keep the thinking spinner BELOW the note (work continues).
            if (thinkingEl && thinkingEl.parentNode === convoLog) {
              convoLog.appendChild(thinkingEl);
            }
            scrollConvo(false);
          }
          break;
        case 'done':
          enableInput();
          setStatus('');
          // Refresh side rail so the just-completed turn's timestamp
          // bumps to the top. Second delayed refresh catches the
          // async title summarizer that some apps fire as a
          // background goroutine after the stream closes.
          if (hasList) {
            loadSessions();
            setTimeout(function() { loadSessions(); }, 6000);
          }
          break;
        case 'error':
          addActivity('error', '', ev.text || 'unknown error');
          setStatus('');
          enableInput();
          break;
      }
    }

    function updateURLParam(key, value) {
      try {
        var u = new URL(window.location.href);
        if (value) u.searchParams.set(key, value);
        else u.searchParams.delete(key);
        window.history.replaceState({}, '', u.toString());
      } catch (_) {}
    }

    // Thinking indicator — three-dot typing animation that sits in
    // the convo log after a send while the server warms up. Removed
    // as soon as ANY content arrives (chunk, message, block,
    // tool_call), so the user gets immediate feedback even when the
    // first orchestrator round takes a few seconds before its first
    // chunk. Reuses ChatPanel's .ui-chat-typing keyframes.
    var thinkingEl = null;
    function showThinking() {
      if (thinkingEl) return;
      thinkingEl = el('div', {class: 'ui-agent-msg ui-agent-msg-assistant ui-agent-thinking'});
      var body = el('div', {class: 'ui-agent-msg-body'});
      body.innerHTML = '<span class="ui-chat-typing" aria-label="Thinking">' +
        '<span></span><span></span><span></span></span>';
      thinkingEl.appendChild(body);
      convoLog.appendChild(thinkingEl);
      // Force-scroll: the user just sent, they expect to see
      // activity at the bottom of the thread.
      convoStickToBottom = true;
      scrollConvo(true);
    }
    function clearThinking() {
      if (thinkingEl && thinkingEl.parentNode) thinkingEl.remove();
      thinkingEl = null;
    }

    function disableInput() {
      sendBtn.disabled = true;
      sendBtn.style.display = 'none';
      cancelBtn.style.display = '';
      cancelBtn.disabled = false;      // fresh run — cancel is clickable again
      cancelLabel.textContent = 'Cancel';
      if (statusPill) statusPill.style.display = '';
      // Drop the empty-state placeholder as soon as any work
      // starts (chat send, Map subscribe, reconnect) so the user
      // sees a clean canvas the events fill into. Without this the
      // "Pick an appliance below…" hint sits above the first
      // activity / intent / plan event.
      clearEmpty();
      showThinking();
      startHeartbeat();
    }
    function enableInput() {
      sendBtn.disabled = false;
      sendBtn.style.display = '';
      cancelBtn.style.display = 'none';
      cancelBtn.disabled = false;
      cancelLabel.textContent = 'Cancel'; // reset from any "Cancelling…" state
      if (statusPill) statusPill.style.display = 'none';
      clearThinking();
      if (activeStream) { try { activeStream.abort(); } catch(_) {} activeStream = null; }
      if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
      stopHeartbeat();
    }

    function sendMessage() {
      var text = inputArea.value.trim();
      if (!text && !pendingAttachments.length) return;
      // Paste-marker substitution: expand any "[Pasted text #N — X
      // lines / Y chars]" markers back to their full content before
      // the send. The marker UX keeps the textarea readable while
      // composing (paste of a 200-line block doesn't fill the screen),
      // but the LLM + the bubble + the saved transcript all see the
      // expanded text — same shape Claude Code uses for terminal
      // pastes. Markers that don't have a stored entry (rare — e.g. a
      // session-resume edit re-introducing a marker that was never in
      // pasteMap) are left as literal text. After substitution we
      // clear the map so a follow-up paste starts fresh.
      if (text.indexOf('[Pasted text #') !== -1) {
        text = text.replace(/\[Pasted text #(\d+) — \d+ lines \/ \d+ chars\]/g,
          function(match, n) {
            var content = pasteMap[parseInt(n, 10)];
            return content == null ? match : content;
          });
        pasteMap = {};
        pasteCounter = 0;
      }
      // Interjection path — when a session is already running and
      // the app configured an InjectURL, route this send into the
      // running session's note queue instead of starting a new
      // session. The agent picks queued notes up between rounds.
      var inFlight = !!(activeStream || activeEventSource);
      if (inFlight && cfg.inject_url && activeSessionId) {
        var noteId = 'u-' + Date.now();
        addMessage('user', noteId, text);
        // Tag the bubble so the notes_consumed handler can find it
        // when the agent drains the queue (server-issued note_id
        // overwrites this once the POST returns). Session id is
        // also tagged so an app-side decorator can wire edit /
        // delete against /api/inject without needing access to
        // the panel's internal state.
        var noteBubble = msgEls[noteId];
        if (noteBubble && noteBubble.bubble) {
          noteBubble.bubble.classList.add('ui-agent-interjection');
          noteBubble.bubble.dataset.sessionId = activeSessionId;
          noteBubble.bubble.dataset.injectUrl = cfg.inject_url;
          // addMessage appended the note at the very bottom — but an
          // in-flight assistant bubble (a lazy/empty tool-call bubble,
          // or one mid-stream) may be sitting just above it. When that
          // bubble later fills, the response materializes ABOVE the
          // interjection, which reads as "my message landed above the
          // answer." Move the note ABOVE the CURRENT (last) in-flight
          // assistant bubble so the response fills in below it.
          //
          // Iterate from the BOTTOM so we find the most recent in-
          // flight bubble — older turns occasionally leave a stale
          // ui-agent-msg-empty / ui-agent-msg-streaming class on
          // their assistant bubble (lazy-materialized tool-call
          // bubbles whose class transition didn't fully settle),
          // and a top-down search would anchor on THAT ancient
          // bubble, yanking the note all the way back near the
          // original user turn instead of leaving it next to the
          // current turn. Reverse iteration finds the in-flight
          // bubble closest to the note, which is always the right one.
          var nb = noteBubble.bubble;
          var anchor = null;
          var kids = convoLog.children;
          for (var ci = kids.length - 1; ci >= 0; ci--) {
            var k = kids[ci];
            if (k === nb) continue;
            if (k.classList && k.classList.contains('ui-agent-msg-assistant') &&
                (k.classList.contains('ui-agent-msg-empty') ||
                 k.classList.contains('ui-agent-msg-streaming'))) {
              anchor = k;
              break;
            }
          }
          if (anchor && anchor !== nb) {
            convoLog.insertBefore(nb, anchor);
          }
        }
        inputArea.value = '';
        // Match the normal-send path: reset the inline style.height
        // so a multi-line message (or a retry that put long text
        // back into the input) doesn't leave the textarea visually
        // taller than its empty content warrants.
        inputArea.style.height = '';
        fetch(cfg.inject_url, {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({id: activeSessionId, text: text}),
        }).then(function(r) {
          if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
          return r.json();
        }).then(function(d) {
          if (noteBubble && noteBubble.bubble && d && d.note_id) {
            noteBubble.bubble.dataset.noteId = d.note_id;
          }
        }).catch(function(err) {
          if (noteBubble && noteBubble.bubble) {
            noteBubble.bubble.classList.add('ui-agent-interjection-failed');
          }
          showToast('Note failed: ' + (err && err.message || err));
        });
        return;
      }
      // One-shot suppression flag: when set, the caller (typically an
      // ask_user card whose submitted state already shows the picked
      // options) has already painted the user's input. Skip the new
      // user bubble locally AND tag the outgoing body so the server
      // marks the persisted message hidden, keeping replay consistent.
      var suppressBubble = !!window.uiSuppressNextUserBubble;
      window.uiSuppressNextUserBubble = false;
      var localMsgId = 'u-' + Date.now();
      if (!suppressBubble) {
        addMessage('user', localMsgId, text);
      }
      // Render the user's own attachments under their bubble so they
      // see what they sent — same surface we use for tool-delivered
      // images/files on the assistant side. Partition by kind:
      // images render as thumbnails (vision LLMs get .images[]);
      // documents render as file pills (server extracts text + folds
      // into the message via .documents[]).
      var userBubble = !suppressBubble && msgEls[localMsgId] && msgEls[localMsgId].bubble;
      var images = [];
      var documents = [];
      pendingAttachments.forEach(function(a) {
        var s = a.dataURL || '';
        var comma = s.indexOf(',');
        var b64 = comma >= 0 ? s.substring(comma + 1) : s;
        if (a.kind === 'image') {
          if (userBubble) {
            (function(srcSnapshot) {
              var img = el('img', {src: srcSnapshot, class: 'ui-agent-msg-image', alt: a.name || 'image'});
              img.addEventListener('click', function() { openImageLightbox(srcSnapshot); });
              agentMsgAttachmentBox(userBubble).appendChild(img);
            })(s);
          }
          images.push(b64);
        } else {
          if (userBubble) {
            agentMsgAttachmentBox(userBubble).appendChild(
              el('div', {class: 'ui-agent-msg-file'},
                ['📄 ' + (a.name || 'document')]));
          }
          documents.push({
            name:      a.name || 'document',
            mime_type: a.mime || '',
            data:      b64,
          });
        }
      });
      pendingAttachments = [];
      renderAttachments();
      inputArea.value = '';
      // Clear the inline style.height left over from autosizeInput
      // when the user typed a multi-line message. Without this, the
      // textarea stays at its grown height for the entire run even
      // though the value is empty — looks like the input "grew" when
      // really it just never shrank back. Clearing forces a return to
      // the CSS-governed min-height: 2.2rem until the user types again.
      inputArea.style.height = '';

      disableInput();

      var body = {
        session_id: activeSessionId || '',
        message:    text,
        images:     images,
        documents:  documents,
      };
      if (suppressBubble) body.hidden = true;
      if (cfg.list_is_context) {
        // In CONTEXT mode, the rail's active id ships under a
        // user-configured key (default "context_id"). session_id
        // is reserved for the server-issued chat session and is
        // empty on every new send unless the server provides one.
        var contextKey = cfg.list_body_field || 'context_id';
        body[contextKey] = activeContextId || '';
        body.session_id = '';
      }
      Object.keys(extraInputs).forEach(function(k) {
        body[k] = extraInputs[k].value;
      });
      // Mix in the active per-session mode flags (private_mode,
      // api_explorer_mode, etc.) — keyed by the mode's send_field.
      Object.keys(modeState).forEach(function(k) {
        body[k] = !!modeState[k];
      });
      // App-supplied extras (set via uiSetPendingMessageExtras) ride on
      // this send and then clear. Merged AFTER framework keys so an app
      // that intentionally needs to override one (rare) can.
      Object.keys(pendingMessageExtras).forEach(function(k) {
        body[k] = pendingMessageExtras[k];
      });
      pendingMessageExtras = {};

      activeStream = new AbortController();
      var resp = fetch(cfg.send_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
        signal: activeStream.signal,
      });
      dispatchResponse(resp);
    }

    // dispatchResponse branches based on the SendURL response shape:
    //   - text/event-stream  → stream SSE directly off this response
    //   - application/json   → expect {session_id}; subscribe to EventsURL
    // The JSON pattern fits apps with a queue-backed session store —
    // it lets the client reconnect to the same event stream after a
    // page reload.
    function dispatchResponse(respPromise) {
      respPromise.then(function(resp) {
        if (!resp.ok) {
          return resp.text().then(function(t) { throw new Error(t || resp.statusText); });
        }
        var ct = (resp.headers.get('Content-Type') || '').toLowerCase();
        if (ct.indexOf('application/json') >= 0 && cfg.events_url) {
          return resp.json().then(function(d) {
            var sid = (d && (d.session_id || d.id)) || '';
            if (!sid) throw new Error('server did not return a session_id');
            activeSessionId = sid;
            if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid);
            subscribeEvents(sid);
          });
        }
        // Fallback: parse this response as the SSE stream directly.
        return streamSSE(resp);
      }).catch(function(err) {
        if (err.name === 'AbortError') return;
        addActivity('error', '', err.message || String(err));
        enableInput();
      });
    }

    function streamSSE(resp) {
      var reader = resp.body.getReader();
      var decoder = new TextDecoder('utf-8');
      var buffer = '';
      function pump() {
        return reader.read().then(function(r) {
          if (r.done) { enableInput(); return; }
          buffer += decoder.decode(r.value, {stream: true});
          var lines = buffer.split('\n');
          buffer = lines.pop();
          lines.forEach(function(line) {
            if (line.startsWith('data: ')) {
              try { handleEvent(JSON.parse(line.slice(6))); }
              catch(e) {}
            }
          });
          return pump();
        });
      }
      return pump();
    }

    // subscribeEvents — used for the POST-ack + subscribe flow. Opens
    // an EventSource to EventsURL?id=<sid>. The server stream is
    // expected to replay buffered events on connect (so reconnects
    // are safe) and emit new events as the in-flight session
    // produces them.
    var activeEventSource = null;
    function subscribeEvents(sid) {
      if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
      var url = cfg.events_url + (cfg.events_url.indexOf('?') >= 0 ? '&' : '?') +
        'id=' + encodeURIComponent(sid);
      activeEventSource = new EventSource(url);
      activeEventSource.onmessage = function(ev) {
        try { handleEvent(JSON.parse(ev.data)); } catch (_) {}
      };
      activeEventSource.onerror = function() {
        // EventSource auto-reconnects on transient errors. The server
        // closes the stream once the session ends; we get a final
        // onerror in that case and tear down here.
        if (activeEventSource && activeEventSource.readyState === EventSource.CLOSED) {
          activeEventSource = null;
          enableInput();
        }
      };
    }

    function cancelMessage() {
      // Show a tearing-down state on the button — keep it visible + spinning,
      // relabeled and disabled — until the cancel POST resolves, so the user
      // gets feedback that the stop is in progress rather than the button just
      // vanishing while the run winds down server-side.
      cancelBtn.disabled = true;
      cancelLabel.textContent = 'Cancelling…';
      if (activeStream) {
        try { activeStream.abort(); } catch(_) {}
        activeStream = null;
      }
      if (activeEventSource) {
        activeEventSource.close();
        activeEventSource = null;
      }
      if (activeSessionId && cfg.cancel_url) {
        fetchJSON(cfg.cancel_url + '?id=' + encodeURIComponent(activeSessionId),
          {method: 'POST'}).then(enableInput, enableInput);
      } else {
        enableInput();
      }
    }

    // detachActiveStream drops THIS client's view of an in-flight run WITHOUT
    // telling the server to cancel it — the agent loop keeps running server-side
    // (runs.go) and we re-attach via the resume probe. Distinct from
    // cancelMessage, which also POSTs cancel_url to stop the run. Called when
    // opening/switching a session so a stale send (or resume) stream from the
    // previous view can't keep appending deltas into the new one — that overlap
    // is what produced token-by-token doubling ("AllAll four four …") when a
    // user left a running session and came back: the old stream and the resume
    // stream both fed the same bubble.
    function detachActiveStream() {
      if (activeStream) { try { activeStream.abort(); } catch(_) {} activeStream = null; }
      if (activeEventSource) { try { activeEventSource.close(); } catch(_) {} activeEventSource = null; }
    }

    // --- Session list / load / delete -------------------------------------

    // Refresh the list rail when any uiInvalidate fires for our
    // list source. Compares the base URL (strip query string) so
    // an invalidate for "api/workspace/list" matches a listener
    // configured with "api/workspace/list?appliance_id={appliance_id}".
    window.addEventListener('ui-data-changed', function(ev) {
      if (!hasList) return;
      var sources = ev.detail && ev.detail.sources;
      if (!sources) return;
      var baseURL = (cfg.list_url || '').split('?')[0];
      for (var i = 0; i < sources.length; i++) {
        if ((sources[i] || '').split('?')[0] === baseURL) {
          loadSessions();
          return;
        }
      }
    });

    // railChannelKey — the session id a channel's thread lives under (mirrors
    // core ChannelSessionKey: per-contact channels thread by handle).
    function railChannelKey(ch) { return 'chan:' + (ch.address || ''); }

    // railFieldLabel — a small labeled wrapper for a form input in the modal.
    function railFieldLabel(lbl, input) {
      return el('div', {style: 'margin:0.4rem 0'}, [
        el('div', {style: 'font-size:0.7rem;color:var(--text-mute);margin-bottom:0.15rem'}, [lbl]),
        input,
      ]);
    }

    // railRulesEditor — an add/remove rules list (one rule per row + "+ Add
    // rule"), matching the FormField type="rules" editor used for
    // gatekeeper rules. Returns {el, getValue} — getValue joins the non-empty
    // rows with newlines (the stored gatekeeper format).
    function railRulesEditor(initial) {
      var wrap = el('div', {class: 'ui-rules'});
      var rules = parseRules(String(initial || ''));
      function render() {
        wrap.innerHTML = '';
        if (!rules.length) {
          wrap.appendChild(el('div', {class: 'ui-rules-empty'}, ['No rules yet — add one below.']));
        }
        rules.forEach(function(r, idx) {
          var ti = el('input', {type: 'text', class: 'ui-rules-input', placeholder: 'rule…'});
          ti.value = r;
          ti.addEventListener('blur', function() { rules[idx] = ti.value; });
          ti.addEventListener('keydown', function(ev) {
            if (ev.key === 'Enter') {
              ev.preventDefault();
              rules[idx] = ti.value;
              rules.splice(idx + 1, 0, '');
              render();
              var ins = wrap.querySelectorAll('.ui-rules-input');
              if (ins[idx + 1]) ins[idx + 1].focus();
            }
          });
          var del = el('button', {class: 'ui-rules-del', type: 'button', title: 'Remove this rule'}, ['×']);
          del.addEventListener('click', function() { rules[idx] = ti.value; rules.splice(idx, 1); render(); });
          wrap.appendChild(el('div', {class: 'ui-rules-row'}, [el('span', {class: 'ui-rules-num'}, [String(idx + 1) + '.']), ti, del]));
        });
        var addBtn = el('button', {class: 'ui-rules-add', type: 'button'}, ['+ Add rule']);
        addBtn.addEventListener('click', function() {
          rules.push('');
          render();
          var ins = wrap.querySelectorAll('.ui-rules-input');
          var last = ins[ins.length - 1];
          if (last) last.focus();
        });
        wrap.appendChild(addBtn);
      }
      render();
      return {
        el: wrap,
        getValue: function() {
          var ins = wrap.querySelectorAll('.ui-rules-input');
          var vals = [];
          for (var i = 0; i < ins.length; i++) {
            var v = ins[i].value.trim();
            if (v) vals.push(v);
          }
          return vals.join('\n');
        },
        // Replace all rows with a fresh set parsed from text (the newline-joined
        // stored format) — used by the channel editor's "Reset to default".
        setValue: function(text) {
          rules = parseRules(String(text || ''));
          render();
        },
      };
    }

    // channelForm — add/edit modal for a channel. A channel is the INTERFACE
    // (pipe to/from the agent): specifying it is just name / description /
    // direction / auto-reply / gatekeeper. The SERVICE/connector is attached
    // separately (and shown as "Name (service)" in the list) — not asked for
    // here. Posts to cfg.channel_save_url (id on edit).
    function channelForm(ch) {
      ch = ch || {};
      var isEdit = !!ch.id;
      var dlg = el('dialog', {class: 'ui-modal-dialog'});
      var nameIn = el('input', {class: 'ui-modal-input', type: 'text', placeholder: 'Name'});
      nameIn.value = ch.name || '';
      var descIn = el('input', {class: 'ui-modal-input', type: 'text', placeholder: 'What this interface is for (optional)'});
      descIn.value = ch.description || '';
      var dirSel = el('select', {class: 'ui-modal-input'});
      [['bidirectional', 'Bi-directional'], ['inbound', 'Inbound'], ['outbound', 'Outbound']].forEach(function(o) {
        var opt = el('option', {value: o[0]}, [o[1]]);
        if ((ch.direction || 'bidirectional') === o[0]) opt.selected = true;
        dirSel.appendChild(opt);
      });
      var arIn = el('input', {type: 'checkbox'});
      if (ch.auto_reply) arIn.checked = true;
      // Per-channel outbound name-tag controls: override the name the bound
      // agent signs its messages with on THIS channel, or turn the tag off here.
      // Both are inert unless the bound agent enabled tagging in its editor.
      var tagOverIn = el('input', {class: 'ui-modal-input', type: 'text', placeholder: "Name tag override (optional)"});
      tagOverIn.value = ch.tag_override || '';
      var tagDisIn = el('input', {type: 'checkbox'});
      if (ch.tag_disabled) tagDisIn.checked = true;
      var gkEditor = railRulesEditor(ch.gatekeeper);
      // Bound-agent picker — only on EDIT, to RE-POINT an existing channel at a
      // different agent. On ADD there's no selector: a new channel binds to the
      // agent you're on (the save URL carries its agent_id), so picking one would
      // be redundant. Populated async from cfg.channel_agents_url.
      var agentSel = el('select', {class: 'ui-modal-input'});
      var body = el('div', {}, [
        el('div', {class: 'ui-modal-msg'}, [isEdit ? 'Edit channel' : 'Add channel']),
        railFieldLabel('Name', nameIn),
        railFieldLabel('Description', descIn),
        railFieldLabel('Direction', dirSel),
      ]);
      if (cfg.channel_agents_url && isEdit) {
        var agentField = railFieldLabel('Agent', agentSel);
        body.appendChild(agentField);
        fetchJSON(substituteExtras(cfg.channel_agents_url)).then(function(list) {
          if (!Array.isArray(list)) list = [];
          agentSel.innerHTML = '';
          list.forEach(function(a) {
            var opt = el('option', {value: a.id}, [a.name || a.id]);
            if (ch.agent_id && a.id === ch.agent_id) opt.selected = true;
            agentSel.appendChild(opt);
          });
        }).catch(function() { agentField.style.display = 'none'; });
      }
      body.appendChild(el('label', {style: 'display:flex;align-items:center;gap:0.4rem;margin:0.5rem 0',
        title: 'On: an inbound message wakes the agent to read and reply. Off: the message is recorded but the agent stays asleep on this channel.'},
        [arIn, el('span', {}, ['Wake on message'])]));
      body.appendChild(railFieldLabel('Gatekeeper rules', gkEditor.el));
      // Reset to default — swaps the rules above for the app's canonical wake
      // rule (source of truth is Go; text arrives via cfg). The user can still
      // tweak before saving; nothing persists until Save.
      if (cfg.default_gatekeeper_rule) {
        var gkReset = el('button', {class: 'ui-rules-add', type: 'button',
          title: 'Replace the rules above with the built-in default wake rule',
          style: 'margin-top:0.25rem'}, ['↺ Reset to default']);
        gkReset.addEventListener('click', function() { gkEditor.setValue(cfg.default_gatekeeper_rule); });
        body.appendChild(gkReset);
      }
      body.appendChild(railFieldLabel('Name tag override', tagOverIn));
      body.appendChild(el('label', {style: 'display:flex;align-items:center;gap:0.4rem;margin:0.5rem 0',
        title: 'On: the bound agent does NOT prefix its name to messages sent on this channel, even if it signs its messages elsewhere. Off: inherit the agent (and any global) name-tag setting.'},
        [tagDisIn, el('span', {}, ['Disable name tag on this channel'])]));
      var saveB = el('button', {class: 'ui-btn-primary', onclick: function() {
        var payload = {id: ch.id || '', name: nameIn.value.trim(), description: descIn.value.trim(),
          direction: dirSel.value, auto_reply: arIn.checked, gatekeeper: gkEditor.getValue(),
          tag_override: tagOverIn.value.trim(), tag_disabled: tagDisIn.checked};
        if (isEdit && cfg.channel_agents_url && agentSel.value) payload.agent_id = agentSel.value;
        fetchJSON(substituteExtras(cfg.channel_save_url), {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(payload)})
          .then(function() { try { dlg.close(); } catch (e) {} dlg.remove(); loadChannels(); })
          .catch(function(err) { showToast('Save failed: ' + (err && err.message || err)); });
      }}, [isEdit ? 'Save' : 'Add']);
      var cancelB = el('button', {onclick: function() { try { dlg.close(); } catch (e) {} dlg.remove(); }}, ['Cancel']);
      body.appendChild(el('div', {class: 'ui-modal-actions'}, [cancelB, saveB]));
      dlg.appendChild(body);
      document.body.appendChild(dlg);
      if (typeof dlg.showModal === 'function') dlg.showModal(); else dlg.setAttribute('open', '');
    }

    // loadChannels — render the Channels rail section (its own header + Add,
    // then one row per binding with open / edit / remove). Hidden when the app
    // didn't provide a channels_url.
    function loadChannels() {
      if (!cfg.channels_url || !channelsEl) return;
      fetchJSON(substituteExtras(cfg.channels_url)).then(function(list) {
        if (!Array.isArray(list)) list = [];
        channelsEl.innerHTML = '';
        // Channels stay LABELED — a distinct tier under the cortex, set off by its
        // "Channels" header + the section divider. (Sessions below are the
        // unlabeled default list; the cortex above carries its own badge.)
        var hdr = el('div', {class: 'ui-channels-h'}, [el('span', {class: 'ui-channels-h-title'}, ['Channels'])]);
        if (cfg.channel_save_url) {
          hdr.appendChild(el('button', {class: 'ui-channels-add', title: 'Add channel',
            onclick: function(ev) { ev.stopPropagation(); channelForm(null); }}, ['+']));
        }
        channelsEl.appendChild(hdr);
        if (!list.length) {
          // No empty-state line — the labeled header with its + button IS the
          // affordance; a "No channels yet." placeholder was just noise. When
          // there's no add button either (read-only surface), a bare header
          // says even less, so hide the section entirely until a channel exists.
          channelsEl.style.display = cfg.channel_save_url ? '' : 'none';
          return;
        }
        list.forEach(function(ch) {
          var sid = railChannelKey(ch);
          // List as "Name (Service)" when a connector is attached, else just the
          // name (with an inert badge below). service_label is the brand-correct
          // display (iMessage), falling back to the raw service id.
          var svcLabel = ch.service_label || ch.service;
          var title = ch.name || ch.address || svcLabel || 'channel';
          if (ch.name && ch.service) title = ch.name + ' (' + svcLabel + ')';
          var dir = ch.direction || 'bidirectional';
          var dirShort = dir === 'inbound' ? 'in' : (dir === 'outbound' ? 'out' : 'both');
          var rowKids = [
            el('span', {class: 'ui-chat-side-title', title: ch.description || ''}, [title]),
            el('span', {class: 'ui-channels-dir', title: 'Direction: ' + dir}, [dirShort]),
          ];
          // No source hooked in → the interface is inert (nothing routes yet).
          if (!ch.service) {
            rowKids.push(el('span', {class: 'ui-channels-inert', title: 'No source hooked in — inert'}, ['inert']));
          }
          // manage_only: the channel relays into its agent's cortex (no thread of
          // its own — the conversation lives in the cortex home thread). Clicking
          // the row opens THAT conversation (what the user expects: "show what's
          // happened in this channel"), not the edit dialog — edit stays on the ✎
          // button below. Other channels (per-room) open their own thread.
          var manageOnly = !!ch.manage_only;
          var row = el('div', {class: 'ui-chat-side-item ui-channels-item ui-chat-side-item-renable' + (!manageOnly && sid === activeSessionId ? ' active' : '')}, rowKids);
          row.addEventListener('click', function() {
            if (manageOnly) { openHomeThread(); closeDrawer(); return; }
            openSession(sid); closeDrawer();
          });
          if (cfg.channel_save_url) {
            row.appendChild(el('button', {class: 'ui-chat-side-ren', title: 'Edit channel',
              onclick: function(ev) { ev.stopPropagation(); channelForm(ch); }}, ['✎']));
          }
          if (cfg.channel_delete_url) {
            row.appendChild(el('button', {class: 'ui-chat-side-del', title: 'Delete channel',
              onclick: async function(ev) {
                ev.stopPropagation();
                if (!(await window.uiConfirm('Delete this channel? Inbound stops routing to the agent. The agent itself is kept.'))) return;
                var url = substituteExtras(cfg.channel_delete_url.replace('{id}', encodeURIComponent(ch.id)));
                fetchJSON(url, {method: 'DELETE'}).then(function() {
                  if (activeSessionId === sid) openSession(null);
                  loadChannels();
                }).catch(function(err) { showToast('Delete failed: ' + (err && err.message || err)); });
              }}, ['×']));
          }
          channelsEl.appendChild(row);
        });
        channelsEl.style.display = '';
      }).catch(function() { /* leave the section as-is on error */ });
    }

    // loadSchedules — render a single "Scheduler" rail entry carrying the TOTAL
    // count of the agent's schedulable/triggered entries (recurring tasks,
    // scheduled agents, event monitors — from cfg.schedules_url). Clicking it
    // opens a modal that lists every entry grouped by category. Stays generic:
    // rows carry their own action URLs and a server-supplied category /
    // category_label; core/ui never names a category itself. Hidden when none.
    function loadSchedules() {
      if (!cfg.schedules_url || !schedulesEl) return;
      function renderSchedulerRow(list) {
        if (!Array.isArray(list)) list = [];
        schedulesEl.innerHTML = '';
        // Always render the Scheduler entry when the app wired a
        // schedules_url — it's the entry point for CREATING schedules, so
        // it must stay reachable even with none yet (the modal shows an
        // empty state). The count badge appears only when there's >= 1.
        var kids = [
          el('span', {style: 'flex:none'}, ['🕐']),
          el('div', {style: 'flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap'}, ['Scheduler'])
        ];
        if (list.length) {
          kids.push(el('span', {style: 'margin-left:auto;flex:none;background:var(--accent,#6366f1);color:#fff;border-radius:10px;padding:0 0.5em;font-size:0.72em;line-height:1.5;min-width:1.4em;text-align:center'},
            [String(list.length)]));
        }
        var btn = el('div', {class: 'ui-chat-side-item ui-channels-item', style: 'cursor:pointer;display:flex;align-items:center;gap:0.4em', title: 'View all schedules'}, kids);
        btn.addEventListener('click', openSchedulerModal);
        schedulesEl.appendChild(btn);
        schedulesEl.style.display = '';
      }
      // Render the row immediately (empty state), then refresh with the
      // real list — so the entry is present even if the fetch is slow or
      // fails, matching the "always visible" policy.
      renderSchedulerRow([]);
      fetchJSON(substituteExtras(cfg.schedules_url)).then(renderSchedulerRow).catch(function() { renderSchedulerRow([]); });
    }

    // buildScheduleRow — one schedule entry (name + detail + edit/pause/delete),
    // used inside the Scheduler modal. `reload` re-renders after any action.
    // Domain-agnostic: it only knows the row's own URLs and edit_action.
    function buildScheduleRow(s, reload) {
      var main = el('div', {style: 'flex:1;min-width:0;overflow:hidden'}, [
        el('div', {style: 'white-space:nowrap;overflow:hidden;text-overflow:ellipsis'}, [s.name || 'schedule']),
        el('div', {style: 'font-size:0.75em;opacity:0.6;white-space:nowrap;overflow:hidden;text-overflow:ellipsis'},
          [(s.paused ? 'paused · ' : '') + (s.detail || '')])
      ]);
      var row = el('div', {class: 'ui-chat-side-item ui-channels-item'}, [main]);
      // Optional per-row edit: when the server tags a row with edit_action,
      // clicking its body invokes that app-registered client action with the
      // row id (+ a reload cb). core/ui doesn't know what the action does.
      if (s.edit_action && window.UIClientActions && window.UIClientActions[s.edit_action]) {
        main.style.cursor = 'pointer';
        main.title = 'Edit';
        main.addEventListener('click', function() {
          window.UIClientActions[s.edit_action]({id: s.id, reload: reload});
        });
      }
      var toggleUrl = s.paused ? s.resume_url : s.pause_url;
      if (toggleUrl) {
        row.appendChild(el('button', {class: 'ui-chat-side-ren', title: s.paused ? 'Resume' : 'Pause',
          onclick: function(ev) {
            ev.stopPropagation();
            fetchJSON(substituteExtras(toggleUrl), {method: 'POST'})
              .then(function() { reload(); })
              .catch(function(err) { showToast('Failed: ' + (err && err.message || err)); });
          }}, [s.paused ? '▶' : '⏸']));
      }
      if (s.delete_url) {
        row.appendChild(el('button', {class: 'ui-chat-side-del', title: 'Delete schedule',
          onclick: async function(ev) {
            ev.stopPropagation();
            if (!(await window.uiConfirm('Delete this schedule? It stops running.'))) return;
            fetchJSON(substituteExtras(s.delete_url), {method: 'DELETE'})
              .then(function() { reload(); })
              .catch(function(err) { showToast('Delete failed: ' + (err && err.message || err)); });
          }}, ['×']));
      }
      return row;
    }

    // openSchedulerModal — the unified Scheduler "page": every entry grouped
    // under its category header, in the server's first-seen category order.
    // Actions re-render the modal AND refresh the rail count via loadSchedules.
    function openSchedulerModal() {
      var modal = window.uiOpenModal({
        title: 'Scheduler',
        subtitle: 'Everything this agent runs on a timer or trigger.',
        width: '560px'
      });
      function render() {
        fetchJSON(substituteExtras(cfg.schedules_url)).then(function(list) {
          if (!Array.isArray(list)) list = [];
          modal.body.innerHTML = '';
          loadSchedules(); // keep the rail badge in sync
          // App-provided "+ New …" create buttons at the top (shown even in the
          // empty state, since this modal is the create entry point). Each invokes
          // an app-registered client action; core/ui never knows the schedule kind.
          var creators = cfg.schedule_creators;
          if (Array.isArray(creators) && creators.length) {
            var bar = el('div', {style: 'display:flex;flex-wrap:wrap;gap:0.4rem;margin-bottom:0.7rem'});
            creators.forEach(function(c) {
              if (!c || !c.action) return;
              var b = el('button', {style: 'padding:0.35em 0.7em;border:none;border-radius:6px;background:var(--accent,#6366f1);color:#fff;cursor:pointer;font-size:0.85em'}, ['＋ ' + (c.label || 'New')]);
              b.addEventListener('click', function() {
                var fn = window.UIClientActions && window.UIClientActions[c.action];
                if (fn) fn({reload: render, sessionId: activeSessionId});
              });
              bar.appendChild(b);
            });
            modal.body.appendChild(bar);
          }
          if (!list.length) {
            modal.body.appendChild(el('div', {style: 'opacity:0.6;padding:0.3rem'}, ['No schedules yet.']));
            return;
          }
          var order = [], groups = {};
          list.forEach(function(s) {
            var cat = s.category || 'other';
            if (!groups[cat]) { groups[cat] = {label: s.category_label || 'Other', rows: []}; order.push(cat); }
            groups[cat].rows.push(s);
          });
          order.forEach(function(cat) {
            var g = groups[cat];
            var sect = el('div', {}, [
              el('div', {class: 'ui-channels-h', style: 'margin-top:0'},
                [el('span', {class: 'ui-channels-h-title'}, [g.label + ' (' + g.rows.length + ')'])])
            ]);
            g.rows.forEach(function(s) { sect.appendChild(buildScheduleRow(s, render)); });
            modal.body.appendChild(sect);
          });
        }).catch(function() {
          modal.body.innerHTML = '';
          modal.body.appendChild(el('div', {style: 'opacity:0.6'}, ['Could not load schedules.']));
        });
      }
      render();
    }

    // appendChannelMessage — render one newly-arrived channel message (live
    // poll). Mirrors the replay's per-message render minus the edit/scrub/tool
    // plumbing (channel threads are read-only and append-only).
    function appendChannelMessage(m) {
      if (!m || m.hidden) return;
      var mid = m.id || ('m-' + Math.random().toString(36).slice(2));
      addMessage(m.role || 'assistant', mid, m.content || m.text || '', m.sender);
      if (cfg.markdown && m.role === 'assistant') finalizeMessage(mid);
      if (m.created || m.usage) setMessageMeta(mid, {created: m.created, usage: m.usage});
    }

    function stopChannelPolling() {
      if (channelPollTimer) { clearInterval(channelPollTimer); channelPollTimer = null; }
    }

    // startChannelPolling — while a channel thread is open, re-fetch its session
    // every few seconds and append any messages beyond what's on screen, so new
    // inbound + the agent's responses show up live without a manual reload.
    function startChannelPolling(sid, initialCount) {
      stopChannelPolling();
      if (!cfg.load_url || (sid || '').indexOf('chan:') !== 0) return;
      channelPollCount = initialCount || 0;
      channelPollTimer = setInterval(function() {
        if (activeSessionId !== sid) { stopChannelPolling(); return; }
        fetchJSON(substituteExtras(cfg.load_url.replace('{id}', encodeURIComponent(sid)))).then(function(rec) {
          if (activeSessionId !== sid) return;
          var msgs = rec && rec[msgsF];
          if (!Array.isArray(msgs) || msgs.length <= channelPollCount) return;
          for (var i = channelPollCount; i < msgs.length; i++) appendChannelMessage(msgs[i]);
          channelPollCount = msgs.length;
        }).catch(function() {});
      }, 3000);
    }

    // obsKey identifies a cortex observation card across polls — its server id
    // when present, else a stable composite (source + time + content head). Lets
    // the poll skip cards already on screen without relying on array position
    // (the cortex thread is also interactive, so chat turns shift indices).
    function obsKey(m) {
      return (m && m.id) || (((m && m.report_from) || '') + '|' + ((m && m.created) || '') + '|' + (((m && m.content) || '').slice(0, 40)));
    }
    // renderObservation appends one cortex observation card — same path the
    // initial replay uses (bubble + meta + the app's report-card replay hook).
    // applyPersistedToolCalls hydrates a bubble's tool toggle from a message's
    // persisted tool_calls ([ ]PersistedToolCall stored per assistant message).
    // The live SSE path (tool_call / tool_result events) builds the same
    // host.tools[] structure; on replay the server has already paired call+result
    // into one entry, so we collapse them into a single push and refresh the
    // toggle once. Shared by the openSession replay loop AND renderObservation, so
    // a live-polled report card shows the same tool chips a reloaded one does
    // (before this, a scheduled/monitor card rendered live via polling showed the
    // prose but none of its tool activity). No-op when there are no tool calls.
    function applyPersistedToolCalls(mid, m) {
      var toolCalls = m && (m.tool_calls || m.ToolCalls);
      if (!m || m.role !== 'assistant' || !Array.isArray(toolCalls) || !toolCalls.length) return;
      var rmEntry = msgEls[mid];
      var rmHost = rmEntry && toolHostFor(rmEntry);
      if (!rmHost) return;
      if (!rmHost.tools) rmHost.tools = [];
      toolCalls.forEach(function(tc, idx) {
        var argsStr = '';
        try { argsStr = JSON.stringify(tc.args || tc.Args || {}); }
        catch (_) { argsStr = String(tc.args || tc.Args || ''); }
        var resultText = tc.result || tc.Result || '';
        var errText = tc.err || tc.Err || '';
        var output = errText ? ('ERROR: ' + errText) : resultText;
        var cached = tc.cached || tc.Cached;
        rmHost.tools.push({
          call_id: 'replay-' + mid + '-' + idx,
          name: (tc.name || tc.Name || 'tool') + (cached ? ' ♻' : ''),
          args: argsStr,
          output: String(output == null ? '' : output),
          kind: '',
        });
      });
      attachAgentToolToggle(rmHost);
    }
    function renderObservation(m) {
      var mid = (m && m.id) || ('obs-' + Math.random().toString(36).slice(2));
      addMessage(m.role || 'assistant', mid, m.content || m.text || '', m.sender);
      if (cfg.markdown && (m.role === 'assistant')) finalizeMessage(mid);
      if (m.created || m.usage) setMessageMeta(mid, {created: m.created, usage: m.usage});
      applyPersistedToolCalls(mid, m);
      if (messageReplayHooks.length) {
        var entry = msgEls[mid], bubble = entry && entry.bubble;
        if (bubble) messageReplayHooks.forEach(function(fn) { try { fn(bubble, m); } catch (e) {} });
      }
    }
    // startCortexPolling — while the cortex home thread is open, re-fetch it and
    // render any NEW observation cards (report_from set) live, like a channel
    // thread. ONLY observations: chat turns the user has with the cortex agent
    // ride the SSE/replay path, so the poll never touches them (no duplication).
    // Reuses the channel poll's timer.
    function startCortexPolling(sid) {
      stopChannelPolling();
      if (!cfg.load_url || (sid || '').indexOf('channel:') !== 0) return;
      channelPollTimer = setInterval(function() {
        if (activeSessionId !== sid) { stopChannelPolling(); return; }
        fetchJSON(substituteExtras(cfg.load_url.replace('{id}', encodeURIComponent(sid)))).then(function(rec) {
          if (activeSessionId !== sid) return;
          var msgs = rec && rec[msgsF];
          if (!Array.isArray(msgs)) return;
          msgs.forEach(function(m) {
            if (!m || !m.report_from) return; // observations only — chat turns ride SSE/replay
            var k = obsKey(m);
            if (cortexObsSeen[k]) return;
            cortexObsSeen[k] = true;
            renderObservation(m);
          });
        }).catch(function() {});
      }, 3000);
    }

    function loadSessions() {
      if (!hasList) return;
      loadChannels();
      loadSchedules();
      var activeID = cfg.list_is_context ? activeContextId : activeSessionId;
      fetchJSON(substituteExtras(cfg.list_url)).then(function(items) {
        sideList.innerHTML = '';
        // Channel model: the agent's home thread is just a session, pinned to
        // the TOP of this list (not a separate Channel row). Pull it out of the
        // normal items so it doesn't also appear under Active/Recent, and render
        // it first below. For a channel agent whose home thread has no turns yet
        // (not in the items list), synthesize a placeholder row so there's always
        // an entry point — sending into it creates it on the first turn.
        var chanSid = altPinnedSession(window.GOHORT_AGENT_ID);
        var homeRec = null;
        if (chanSid && Array.isArray(items)) {
          items = items.filter(function(s) {
            if (s && s[idF] === chanSid) { homeRec = s; return false; }
            return true;
          });
          if (!homeRec) { homeRec = {}; homeRec[idF] = chanSid; }
          if (!homeRec[ttlF]) homeRec[ttlF] = 'Home';
        }
        // Fill the Channel hero row — the agent's main thread, styled to stand
        // apart from the session list. Hidden for non-channel agents (no home
        // thread). Sits above Permissions + the session header (see primaryEl).
        if (primaryEl) {
          primaryEl.innerHTML = '';
          if (homeRec) {
            var chId = homeRec[idF];
            var chActive = chId === activeSessionId;
            // Cortex — the standing/home thread, rendered as a MARKED ROW (gold
            // brain glyph + "home" badge + faint always-on gold tint) so it reads
            // as a session, just the special one, pinned at the top of the list.
            var gold = '#d9b86c';
            var titleLine = el('div', {style: 'display:flex;align-items:center;gap:0.4rem;white-space:nowrap;overflow:hidden'}, [
              el('span', {style: 'font-weight:700;overflow:hidden;text-overflow:ellipsis'}, [cfg.alt_primary_label || 'Cortex']),
              el('span', {style: 'font-size:0.56rem;text-transform:uppercase;letter-spacing:0.05em;font-weight:700;color:' + gold + ';border:1px solid ' + gold + ';border-radius:999px;padding:0.02rem 0.4rem;flex:0 0 auto'}, ['home']),
            ]);
            if (homeRec.unread && !chActive) {
              titleLine.appendChild(el('span', {class: 'ui-unread-dot', title: 'New activity',
                style: 'width:7px;height:7px;border-radius:50%;background:var(--accent, #4a9eff);flex:0 0 auto'}, ['']));
            }
            var chKids = [
              el('span', {style: 'flex:0 0 1.1rem;text-align:center;font-size:0.95rem;color:' + gold}, ['🧠']),
              el('div', {style: 'flex:1;min-width:0'}, [
                titleLine,
                el('div', {style: 'font-size:0.74rem;color:var(--text-mute, #999);margin-top:0.1rem;overflow:hidden;text-overflow:ellipsis;white-space:nowrap'}, ['standing thread']),
              ]),
            ];
            // Faint gold tint ALWAYS (marks it as the standing thread); a gold
            // border adds when it's the active thread.
            var heroBorder = chActive ? gold : 'transparent';
            var heroBg = chActive ? 'rgba(217,184,108,0.16)' : 'rgba(217,184,108,0.07)';
            var chRow = el('button', {type: 'button', class: 'ui-channel-hero' + (chActive ? ' active' : ''),
              style: 'display:flex;align-items:flex-start;gap:0.5rem;width:100%;text-align:left;padding:0.5rem 0.6rem;border:1px solid ' + heroBorder + ';border-radius:7px;cursor:pointer;font:inherit;color:var(--text, inherit);background:' + heroBg,
              onclick: function() { openSession(chId); closeDrawer(); }}, chKids);
            primaryEl.appendChild(chRow);
            primaryEl.style.display = '';
          } else {
            primaryEl.style.display = 'none';
          }
        }
        if (!Array.isArray(items)) items = [];
        // Channel threads live in the dedicated Channels section above — keep
        // their chan: rows out of the chat-session list so they aren't shown
        // twice. (Defensive: the server already omits them.)
        items = items.filter(function(s) { return (s[idF] || '').indexOf('chan:') !== 0; });
        if (!homeRec && !items.length) {
          if (cfg.bulk_select && bulkState.mode) {
            renderBulkBar([], sideList, bulkState, bulkSelected,
              function(s){ return s[idF]; }, loadSessions, function(){});
          }
          sideList.appendChild(el('div', {class: 'ui-chat-side-empty'}, ['(none)']));
          return;
        }
        // Drop ids from bulkSelected that no longer exist in the
        // list (e.g. a session got deleted from another tab).
        var ids = {}; items.forEach(function(s){ ids[s[idF]] = true; });
        Object.keys(bulkSelected).forEach(function(k){ if (!ids[k]) delete bulkSelected[k]; });
        if (cfg.bulk_select) {
          renderBulkBar(items, sideList, bulkState, bulkSelected,
            function(s){ return s[idF]; },
            loadSessions,
            async function() {
              var chosen = Object.keys(bulkSelected);
              if (!chosen.length) return;
              if (!(await window.uiConfirm('Delete ' + chosen.length + ' session(s) permanently?'))) return;
              Promise.all(chosen.map(function(id) {
                var url = substituteExtras(cfg.delete_url.replace('{id}', encodeURIComponent(id)));
                return fetchJSON(url, {method: 'DELETE'}).catch(function(){});
              })).then(function() {
                if (!cfg.list_is_context && bulkSelected[activeSessionId]) openSession(null);
                if (cfg.list_is_context && bulkSelected[activeContextId]) activeContextId = '';
                bulkSelected = {};
                bulkState.mode = false;
                // Reset the sidebar's Select pill — without this,
                // it stays showing "✓ Selecting" with the active
                // class even though we've programmatically exited
                // select mode.
                if (sideSelectBtn) {
                  sideSelectBtn.classList.remove('active');
                  sideSelectBtn.textContent = 'Select';
                }
                loadSessions();
              });
            });
        }
        // Build one rail row and append it to sideList. Pulled out of the
        // forEach so the list renders in two passes (Active group, then Recent).
        function buildAndAppendRow(rec) {
          var sid = rec[idF];
          var ttl = rec[ttlF] || sid;
          var inMode = cfg.bulk_select && bulkState.mode;
          var selected = !!bulkSelected[sid];
          var rowClass = 'ui-chat-side-item' +
            (inMode ? ' selectable' : '') +
            (selected ? ' selected' : '');
          // Use the shared .ui-chat-side-item class so the row gets
          // the framework's hover / active styling AND the
          // position:relative context that the absolutely-positioned
          // ✎/× buttons need to land on the right edge.
          var rowKids = [el('span', {class: 'ui-chat-side-title', text: ttl})];
          // Unread dot — a background append (monitor wake / report / goal
          // completion) landed here while it wasn't open. Cleared on open, and
          // never shown on the session you're currently viewing.
          if (rec.unread && sid !== activeSessionId) {
            rowKids.unshift(el('span', {class: 'ui-unread-dot', title: 'New activity',
              style: 'display:inline-block;width:7px;height:7px;border-radius:50%;background:var(--accent, #4a9eff);margin-right:0.45rem;flex:0 0 auto;vertical-align:middle'}, ['']));
          }
          // Active-work badge — LIVE watchers/dispatches attached to this
          // session. Distinct from the unread dot (a past append) and the
          // running pulse (this session mid-turn): this persists as long as
          // the background work does, so sessions with ongoing work stay
          // findable. Shown regardless of whether the session is open.
          var wq = rec.watchers || 0, dq = rec.dispatches || 0;
          if (wq > 0 || dq > 0) {
            var parts = [];
            if (wq > 0) parts.push('👁 ' + wq);
            if (dq > 0) parts.push('⚙ ' + dq);
            var tip = 'Active background work: ' +
              (wq > 0 ? wq + ' watcher' + (wq === 1 ? '' : 's') : '') +
              (wq > 0 && dq > 0 ? ', ' : '') +
              (dq > 0 ? dq + ' dispatch' + (dq === 1 ? '' : 'es') : '');
            rowKids.push(el('span', {class: 'ui-badge accent', title: tip,
              style: 'margin-left:0.4rem;font-size:0.66rem;padding:0 0.3rem;flex:0 0 auto;vertical-align:middle'}, [parts.join('  ')]));
          }
          var row = el('div', {class: rowClass}, rowKids);
          // Tag the row with its id so a search-scoped Select-all (renderBulkBar)
          // can pick only the rows the active filter currently shows.
          row.setAttribute('data-bulk-id', sid);
          row.addEventListener('click', function() {
            if (inMode) {
              if (bulkSelected[sid]) delete bulkSelected[sid];
              else bulkSelected[sid] = true;
              loadSessions();
            } else {
              openSession(sid);
              // Auto-close the rail drawer after picking a session.
              // On mobile the rail is a drawer that overlays the chat
              // pane; staying open after a pick obscures the freshly
              // loaded conversation. On desktop, closeDrawer() is a
              // no-op (the .open class isn't used in that layout) so
              // calling unconditionally is safe.
              closeDrawer();
            }
          });
          if (!inMode) {
            // Optional rename button (✎). When the app provides
            // a RenameURL, each row gets an inline edit affordance
            // that prompts for a new name and POSTs {id, name}.
            if (cfg.rename_url) {
              // Bump right padding so the title doesn't run under
              // both action buttons.
              row.classList.add('ui-chat-side-item-renable');
              var renBtn = el('button', {
                class: 'ui-chat-side-ren', title: 'Rename',
                onclick: function(ev) {
                  ev.stopPropagation();
                  // uiPrompt (not native prompt) so this works on hosts where
                  // window.prompt is unsupported — e.g. the gohort-desktop
                  // Wails webview, which injects __uiPromptImpl + a modal.
                  uiPrompt('Rename to:', ttl).then(function(next) {
                    if (next == null) return;
                    next = next.trim();
                    if (!next || next === ttl) return;
                    fetchJSON(substituteExtras(cfg.rename_url), {
                      method: 'POST',
                      headers: {'Content-Type': 'application/json'},
                      body: JSON.stringify({id: sid, name: next}),
                    }).then(function() { loadSessions(); })
                      .catch(function(err) {
                        showToast('Rename failed: ' + (err && err.message || err));
                      });
                  });
                },
              }, ['✎']);
              row.appendChild(renBtn);
            }
            var delBtn = el('button', {class: 'ui-chat-side-del', title: (rec.channel_id ? 'Delete channel' : 'Delete'),
              onclick: async function(ev) {
                ev.stopPropagation();
                // Channel rows delete the BINDING, not just the transcript:
                // removing the channel stops inbound from routing to the agent.
                // The agent itself is kept; the thread is cleared too so the
                // row goes away cleanly.
                if (rec.channel_id && cfg.channel_delete_url) {
                  if (!(await window.uiConfirm('Delete this channel? Inbound messages stop routing to the agent and this thread is cleared. The agent itself is kept.'))) return;
                  var curl = substituteExtras(cfg.channel_delete_url.replace('{id}', encodeURIComponent(rec.channel_id)));
                  fetchJSON(curl, {method: 'DELETE'}).then(function() {
                    if (cfg.delete_url) {
                      var surl = substituteExtras(cfg.delete_url.replace('{id}', encodeURIComponent(sid)));
                      return fetchJSON(surl, {method: 'DELETE'}).catch(function() {});
                    }
                  }).then(function() {
                    if (activeSessionId === sid) openSession(null);
                    loadSessions();
                  }).catch(function(err) { showToast('Delete channel failed: ' + (err && err.message || err)); });
                  return;
                }
                // Warn when the thread has LIVE producers attached (watchers /
                // in-flight dispatches): deleting only clears the transcript —
                // the producers keep running and will recreate the thread on
                // their next fire. So this is really a clear, not a delete.
                var wq = rec.watchers || 0, dq = rec.dispatches || 0;
                var delMsg = 'Delete this item?';
                if (wq > 0 || dq > 0) {
                  var bits = [];
                  if (wq > 0) bits.push(wq + ' active watcher' + (wq === 1 ? '' : 's'));
                  if (dq > 0) bits.push(dq + ' running dispatch' + (dq === 1 ? '' : 'es'));
                  delMsg = 'This thread has ' + bits.join(' and ') + '. Deleting it only clears the conversation — they keep running and will re-create the thread on their next report. To stop them, use Decommission. Delete anyway?';
                }
                if (!(await window.uiConfirm(delMsg))) return;
                var url = substituteExtras(cfg.delete_url.replace('{id}', encodeURIComponent(sid)));
                fetchJSON(url, {method: 'DELETE'}).then(function() {
                  if (cfg.list_is_context) {
                    if (activeContextId === sid) activeContextId = '';
                  } else {
                    if (activeSessionId === sid) openSession(null);
                  }
                  loadSessions();
                });
              }}, ['×']);
            row.appendChild(delBtn);
          }
          if (sid === activeID) row.classList.add('active');
          sideList.appendChild(row);
        }

        // Two-pass render: sessions with live background work (watchers or
        // in-flight dispatches) lift into an "Active" group so they don't
        // scroll away under chat history; the rest follow under "Recent".
        // "running" (mid-turn) is intentionally NOT grouped — it's transient
        // and already shown by its own pulse — so the list doesn't reshuffle
        // every time a turn starts or ends.
        var hasBgWork = function(rec) { return (rec.watchers || 0) > 0 || (rec.dispatches || 0) > 0; };
        // Headerless: one flat list, no "Active"/"Recent" labels (the Cortex row
        // is above via primaryEl; channels render below). Sessions with live
        // background work still float to the TOP — just without a group label.
        var actives = items.filter(hasBgWork);
        var rest = items.filter(function(rec) { return !hasBgWork(rec); });
        actives.forEach(buildAndAppendRow);
        rest.forEach(buildAndAppendRow);
        // Re-apply the side-search filter to the freshly rebuilt rows so an
        // active query survives reloads (entering select mode, bulk delete).
        // Without this the rebuilt rows come back all-visible and Select-all,
        // which is scoped to visible rows, would grab everything not just matches.
        if (sideSearch && sideSearch.applyFilter) sideSearch.applyFilter();
      }).catch(function() {
        sideList.innerHTML = '';
        sideList.appendChild(el('div', {class: 'ui-chat-side-empty'}, ['(failed to load)']));
      });
    }

    // setHeaderTitle updates the mobile drawer header's title text so
    // it reflects the active session/context instead of staying on the
    // generic "New" label. No-op when there's no drawer (desktop-only
    // or no list).
    function setHeaderTitle(t) {
      // Remember the active session's title so the header can be restored when
      // returning to the Channel chat from a management view (which overrides
      // the title with its own label below).
      lastSessionTitle = t || (cfg.new_label || 'New');
      if (drawer && drawer.mobileTitle) {
        drawer.mobileTitle.textContent = lastSessionTitle;
      }
    }

    function openSession(sid) {
      // Channel agents no longer force every open onto the home thread — they
      // have a channel thread AND ordinary sessions. The Channel row opens the
      // home thread explicitly (via altPinnedSession); a normal session row /
      // "+ New" / deep link opens its own id as requested.
      // Opening ANY session returns to the chat pane — hide any management /
      // History overlay (orchView) that was covering it, so clicking a session
      // from the list isn't masked by a table that's still up. No nav row maps
      // to "in chat" anymore (the home thread is a pinned session, not a Channel
      // row), so clear every management-nav highlight instead of marking row 0.
      if (orchView) orchView.style.display = 'none';
      if (typeof orchBtns !== 'undefined' && orchBtns && orchBtns.length) {
        orchBtns.forEach(function(b, i) {
          var navItem = (cfg.orchestrator_nav || [])[i] || {};
          b.style.border = '1px solid transparent'; // clear any "selected" accent border
          // Pinned rows (Permissions) keep their PERSISTENT faint tint — that's
          // their always-on marker, not a selection state — so clear only the
          // selected border here and leave the background to refreshChannelBadges.
          // Non-pinned rows reset fully.
          if (navItem.pinned) return;
          b.style.background = 'transparent';
          b.style.fontWeight = '400';
        });
      }
      // Any session open / switch / new collapses the mobile drawer —
      // done at the top so it fires for EVERY entry point (row click,
      // rail-header "+ New", mobile-top-bar "+", deep link). No-op on
      // desktop (the .open class isn't used there).
      closeDrawer();
      // Tear down any in-flight stream from the view we're leaving BEFORE we
      // clear bubbles / reset the seq counter / re-attach below. Client-only —
      // the server run keeps going and we re-attach via tryResumeRun, so a
      // stale send/resume stream can't double-feed the new view's bubble.
      detachActiveStream();
      // CONTEXT mode — list rows are reference contexts (workspaces,
      // projects, …). Selecting one binds future sends to that id
      // via cfg.list_body_field. Server-side LoadURL still gets
      // fetched if set, and any messages it returns replay into the
      // conversation pane so the user sees the context's history
      // when picking it. The activity pane is NOT cleared — that's
      // live per-send state, distinct from saved context history.
      if (cfg.list_is_context) {
        activeContextId = sid || '';
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid || '');
        msgEls = {};
        convoLog.innerHTML = '';
        if (!sid) {
          emptyMsg = el('div', {class: 'ui-agent-empty'},
            [cfg.empty_text || 'Start typing below.']);
          convoLog.appendChild(emptyMsg);
          setHeaderTitle('');
          if (hasList) loadSessions();
          return;
        }
        if (cfg.load_url) {
          var url = substituteExtras(cfg.load_url.replace('{id}', encodeURIComponent(sid)));
          fetchJSON(url).then(function(rec) {
            setHeaderTitle(rec && rec[ttlF]);
            var msgs = rec && rec[msgsF];
            if (Array.isArray(msgs)) {
              msgs.forEach(function(m) {
                var mid = m.id || ('m-' + Math.random().toString(36).slice(2));
                addMessage(m.role || 'assistant', mid, m.content || m.text || '');
                if (cfg.markdown && (m.role === 'assistant')) finalizeMessage(mid);
                if (m.created || m.usage) {
                  setMessageMeta(mid, {created: m.created, usage: m.usage});
                }
                // Replay persisted tool calls (same shape as the
                // SESSION-mode branch below — see that comment for
                // rationale). Skipped silently when the message has
                // no tool_calls field.
                var ctxToolCalls = m.tool_calls || m.ToolCalls;
                if (m.role === 'assistant' && Array.isArray(ctxToolCalls) && ctxToolCalls.length) {
                  var ctxEntry = msgEls[mid];
                  var ctxHost = ctxEntry && toolHostFor(ctxEntry);
                  if (ctxHost) {
                    if (!ctxHost.tools) ctxHost.tools = [];
                    ctxToolCalls.forEach(function(tc, idx) {
                      var argsStr = '';
                      try { argsStr = JSON.stringify(tc.args || tc.Args || {}); }
                      catch (_) { argsStr = String(tc.args || tc.Args || ''); }
                      var resultText = tc.result || tc.Result || '';
                      var errText = tc.err || tc.Err || '';
                      var output = errText ? ('ERROR: ' + errText) : resultText;
                      var cached = tc.cached || tc.Cached;
                      ctxHost.tools.push({
                        call_id: 'replay-' + mid + '-' + idx,
                        name: (tc.name || tc.Name || 'tool') + (cached ? ' ♻' : ''),
                        args: argsStr,
                        output: String(output == null ? '' : output),
                        kind: '',
                      });
                    });
                    attachAgentToolToggle(ctxHost);
                  }
                }
              });
            }
            if (hasList) loadSessions();
          }).catch(function(err) {
            addActivity('error', '', err.message || String(err));
          });
        } else if (hasList) {
          loadSessions();
        }
        return;
      }
      // SESSION mode — replay messages from the saved conversation.
      activeSessionId = sid || '';
      // Start every open as a plain session; the load below re-flags channel
      // rooms so transcript labels never leak from a previously-open channel.
      channelTranscript = null;
      // Channel threads are READ-ONLY in the web UI — messages arrive from the
      // messaging surface, not by typing here. Hide the composer for a channel
      // thread (id "chan:…") and restore it for ordinary sessions.
      if (inputRow) inputRow.style.display = ((sid || '').indexOf('chan:') === 0) ? 'none' : '';
      // Stop any prior channel poll; a channel open re-starts it after replay.
      stopChannelPolling();
      // Reset run-tracking state — the new session may have its own
      // in-flight run discovered via the active-run probe below, or
      // no run at all. Either way, start counting from zero.
      activeRunId = '';
      runSeqReceived = 0;
      msgEls = {}; activityEls = {}; blockEls = {};
      convoLog.innerHTML = '';
      activityLog.innerHTML = '';
      if (!sid) {
        emptyMsg = el('div', {class: 'ui-agent-empty'},
          [cfg.empty_text || 'Start typing below.']);
        convoLog.appendChild(emptyMsg);
        setHeaderTitle('');
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, '');
        if (hasList) loadSessions();
        return;
      }
      if (!hasList) {
        // Without a list URL, there's nothing to load from. The
        // app is treating sessions as ephemeral; just clear the
        // pane and let the next send carry the session forward.
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid);
        return;
      }
      var url = substituteExtras(cfg.load_url.replace('{id}', encodeURIComponent(sid)));
      // External-source row? Append source + chat_id
      // so the server's handleSessionOne knows which registered
      // ExtraSessionsSource owns this row and how to route to its
      // per-source storage scope.
      var src = sessionSources[sid];
      if (src && src.source) {
        url += (url.indexOf('?') >= 0 ? '&' : '?') +
          'source=' + encodeURIComponent(src.source) +
          '&chat_id=' + encodeURIComponent(src.chat_id || '');
      }
      fetchJSON(url).then(function(rec) {
        setHeaderTitle(rec && rec[ttlF]);
        // Channel rooms render as a who-said-what transcript: the session is
        // a 1:1 messaging thread, so "user" lines are the contact (named by
        // the session title) and "assistant" lines are the bound agent. Plain
        // web sessions keep the anonymous bubbles (channelTranscript = null).
        if ((sid || '').indexOf('chan:') === 0) {
          channelTranscript = {
            contact: (rec && rec[ttlF]) || 'Contact',
            agent: currentAgentLabel(),
          };
        } else {
          channelTranscript = null;
        }
        var msgs = rec && rec[msgsF];
        if (Array.isArray(msgs)) {
          msgs.forEach(function(m, i) {
            // Hidden messages still ride along for the LLM's history
            // view but the prior bubble already shows their content
            // (e.g. submitted ask_user card). Skip rendering the dupe.
            // NOTE: hidden messages stay in the array, so i is the TRUE
            // storage index (not the rendered position) — exactly what the
            // per-turn scrub needs to delete the right message server-side.
            if (m && m.hidden) return;
            var mid = m.id || ('m-' + Math.random().toString(36).slice(2));
            addMessage(m.role || 'assistant', mid, m.content || m.text || '', m.sender);
            // Remember the raw storage index so the ✕ scrub affordance can
            // PATCH {delete_at: i}. Only replayed bubbles carry it; live ones
            // get one on the next load (reload re-syncs after a scrub/delete).
            if (msgEls[mid]) msgEls[mid].storageIndex = i;
            // Re-attach the action bar now that storageIndex is set — addMessage
            // attached it during creation (before we knew the index), so the
            // scrub button wouldn't have rendered. attach* is idempotent.
            if (cfg.msg_scrub && cfg.truncate_url && msgEls[mid]) {
              if (m.role === 'user' && (m.content || m.text || '').length > 0) attachUserActions(msgEls[mid].bubble);
              else if (m.role === 'assistant') attachAssistantActions(msgEls[mid].bubble);
            }
            if (cfg.markdown && (m.role === 'assistant')) finalizeMessage(mid);
            if (m.created || m.usage) {
              setMessageMeta(mid, {created: m.created, usage: m.usage});
            }
            // Replay persisted tool calls onto this bubble's host.
            applyPersistedToolCalls(mid, m);
            // App-registered replay hooks let app code decorate
            // replayed bubbles (e.g. swap an intake-derived user msg's
            // body for the re-editable form widget). Each hook decides
            // by inspecting m. Failures isolated per-hook so one bad
            // app doesn't break replay.
            if (messageReplayHooks.length) {
              var entry = msgEls[mid];
              var bubble = entry && entry.bubble;
              if (bubble) {
                messageReplayHooks.forEach(function(fn) {
                  try { fn(bubble, m); } catch (e) { /* isolate */ }
                });
              }
            }
          });
        }
        // Replay persisted UI blocks — session-level artifacts (dashboards
        // and other block-rendered surfaces) that a tool emitted live as
        // {kind:"block"} SSE events and the server upserted onto the session
        // record. They route through the same addBlock dispatcher as live
        // blocks; the server strips any auto-open hint at persist time, so a
        // reload shows the cards without popping panes unasked.
        var uiBlocks = rec && (rec.ui_blocks || rec.UIBlocks);
        if (Array.isArray(uiBlocks)) {
          // Collapse duplicate cards for the same surface before rendering.
          // The UIBlocks contract is one card per artifact, but sessions
          // persisted before the server upserted by destination carry one
          // block per emission (a link card re-announced every turn, an
          // artifact update the agent forgot to pass the id for). Identity
          // is the renderer payload's destination — url when present, else
          // title — scoped by type; newest content wins, first position kept.
          var byKey = {}, keyOrder = [];
          uiBlocks.forEach(function(b, i) {
            if (!b || !b.type) return;
            var key = b.type + '\x00' + (b.url || b.title || b.id || i);
            if (!(key in byKey)) keyOrder.push(key);
            byKey[key] = b;
          });
          keyOrder.forEach(function(k) { addBlock(byKey[k]); });
        }
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid);
        loadSessions();
        // Channel threads are append-only and fed server-side (from the
        // messaging surface) — poll so new inbound + the agent's replies show
        // up live while watching, without a manual reload.
        if (channelTranscript) {
          startChannelPolling(sid, Array.isArray(msgs) ? msgs.length : 0);
        } else if ((sid || '').indexOf('channel:') === 0) {
          // Cortex home thread: seed the seen-set from what just replayed, then
          // poll for NEW observation cards so they appear live — like a channel
          // thread, but only the background report cards (chat turns ride SSE).
          cortexObsSeen = {};
          if (Array.isArray(msgs)) {
            msgs.forEach(function(m) { if (m && m.report_from) cortexObsSeen[obsKey(m)] = true; });
          }
          startCortexPolling(sid);
        }
        // After the saved transcript renders, ask the server
        // whether this session has an in-flight run we should
        // attach to. Server-side, the agent loop is decoupled from
        // the original /api/send request (see runs.go); if the
        // earlier client navigated away or refreshed mid-turn, the
        // loop is still running and the buffer has its events
        // queued. Subscribing here picks up live where the prior
        // client left off.
        tryResumeRun(sid);
      }).catch(function(err) {
        addActivity('error', '', err.message || String(err));
      });
    }

    // tryResumeRun queries /api/runs/active for the session; if an
    // in-flight run exists, opens an EventSource that replays
    // missed events from runSeqReceived (zero after a fresh load,
    // higher if the same session was already on screen) and tails
    // live until the run completes.
    //
    // No-op when cfg.runs_url_base is unset — apps that haven't
    // adopted the run-registry layer keep their current
    // load-and-stop behavior.
    function tryResumeRun(sid) {
      if (!cfg.runs_url_base || !sid) return;
      var activeUrl = cfg.runs_url_base + 'active?session_id=' + encodeURIComponent(sid);
      fetchJSON(activeUrl).then(function(d) {
        if (!d || !d.run_id) return;
        activeRunId = d.run_id;
        // disableInput shows the in-flight UI affordances
        // (cancel button, spinner) so the user knows a turn is
        // running even though they didn't start it from this tab.
        disableInput();
        subscribeRunStream(d.run_id, runSeqReceived);
      }).catch(function() { /* silent — no run is the common case */ });
    }

    // subscribeRunStream opens an EventSource on
    // /api/runs/<id>/stream?since=<n>. Events flow into the same
    // handleEvent path as the live /api/send response, so all
    // existing chunk/message/block handling Just Works.
    function subscribeRunStream(runId, since) {
      // Tear down any prior subscription before starting a new one
      // (e.g. fast session-switch could trigger double-subscribe).
      if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
      var url = cfg.runs_url_base + encodeURIComponent(runId) + '/stream?since=' + (since || 0);
      activeEventSource = new EventSource(url);
      activeEventSource.onmessage = function(ev) {
        try { handleEvent(JSON.parse(ev.data)); } catch (_) {}
      };
      activeEventSource.onerror = function() {
        // EventSource auto-reconnects on transient errors. When
        // the server closes the stream (run completed), we get a
        // final onerror; tear down and re-enable input.
        if (activeEventSource && activeEventSource.readyState === EventSource.CLOSED) {
          activeEventSource = null;
          enableInput();
        }
      };
    }

    // Deep-link bootstrapping: if the URL carries the configured
    // session param, open it on mount. In CONTEXT mode this
    // restores the active context; in SESSION mode it replays the
    // saved conversation.
    if (cfg.deep_link_param) {
      try {
        var qs = new URL(window.location.href).searchParams;
        var sid = qs.get(cfg.deep_link_param);
        if (sid) {
          if (cfg.list_is_context) activeContextId = sid;
          else activeSessionId = sid;
          // Defer openSession until any extra inputs (Agency's
          // agent picker, an app's custom picker, …) have
          // populated. This bootstrap fires very early during panel
          // mount — before those picker fetches resolve — and the
          // session-load URL templates expect every extra to be
          // substituted (e.g. api/sessions/{id}?agent_id={agent_id}).
          // Loading early ships agent_id="", server returns 400,
          // and the rail flashes "(failed to load)" until the user
          // navigates away and back. Polling for value() lets the
          // load fire as soon as the picker resolves, with a
          // 3-second wall-clock cap as the retry-anyway fallback.
          var openAttempts = 0;
          function tryOpenFromDeepLink() {
            var ready = true;
            Object.keys(extraInputs || {}).forEach(function(k) {
              if (!extraInputs[k] || !extraInputs[k].value) ready = false;
            });
            if (ready || openAttempts >= 30) {
              openSession(sid);
              return;
            }
            openAttempts++;
            setTimeout(tryOpenFromDeepLink, 100);
          }
          tryOpenFromDeepLink();
        }
      } catch (_) {}
    }
    // Live-reconnect bootstrapping: if the URL carries
    // ?reconnect=<id> and EventsURL is configured, hop straight
    // into a running session's stream. Used by the global "live
    // sessions" pill to attach to an in-flight job (map run,
    // long-running chat) after a page navigation.
    if (cfg.events_url) {
      try {
        var rid = new URL(window.location.href).searchParams.get('reconnect');
        if (rid) {
          activeSessionId = rid;
          disableInput();
          subscribeEvents(rid);
        }
      } catch (_) {}
    }

    // Assemble.
    //
    // Default layout: topbar spans full width across the top, then
    // the grid row below holds the side rail + main column.
    //
    // ListPosition: "top" layout: the topbar lives INSIDE the main
    // column (as the first child) so it sits ONLY above the chat
    // pane, not across the sessions rail. The rail keeps its own
    // sidebar-header (with the New button) and stays full-height.
    //
    // When extra_fields_in_sidebar=true, both actionsBar AND the
    // extras row have already been pulled out of the topbar into
    // topBundle (above gridRow). The leftover topbar holds only the
    // statusBar div, which is display:none whenever there's no text
    // — but the topbar itself is styled as a fixed 2.5rem-tall bar
    // with a border-bottom, so it still reserves 40px of empty space
    // above the chat. Skip inserting it in that case; setStatus still
    // works on the orphaned statusBar reference (it just never paints
    // because the node isn't in the DOM), which matches the intended
    // behavior for apps that don't surface status text.
    if (listPosTop) {
      if (!cfg.extra_fields_in_sidebar) {
        // Insert topbar as the first child of main so it lands above
        // the conversation log + input area.
        main.insertBefore(topbar, main.firstChild);
      }
    } else {
      wrap.appendChild(topbar);
    }
    if (hasList) {
      gridRow.appendChild(side);
      wrap.appendChild(drawer.backdrop);
    }
    if (expandTab) {
      // Always absolute-position over the grid row. Earlier we
      // experimented with inserting into the Agency topBundle as a
      // flex/grid child — that shifted the bundle's action buttons
      // to a second row when collapsed because the button took a
      // grid cell. Absolute positioning leaves the bundle's layout
      // untouched in both modes; the CSS rules for
      // .ui-agent.side-collapsed .ui-agent-expand handle visible
      // placement (see runtime.go's CSS block).
      gridRow.appendChild(expandTab);
    }
    gridRow.appendChild(main);
    gridRow.appendChild(activityExpandTab);
    wrap.appendChild(gridRow);

    if (hasList) loadSessions();

    // AutoSend — a deep-link handoff (e.g. a Builder brief the page stamped
    // server-side) sends ONE message automatically via the panel's own
    // sendMessage(), once the panel is actually mounted. No DOM-scraping or
    // simulated clicks — that's why the old approach silently failed (it looked
    // for a chat input class this panel doesn't use). Fires a fresh turn in a
    // new session, so the agent responds immediately without the user retyping.
    if (cfg.auto_send) {
      var pendingAuto = cfg.auto_send, autoTries = 0;
      (function fireAutoSend() {
        if (wrap.isConnected) {
          inputArea.value = pendingAuto;
          inputArea.dispatchEvent(new Event('input', {bubbles: true}));
          sendMessage();
          return;
        }
        if (autoTries++ < 100) setTimeout(fireAutoSend, 50);
      })();
    }

    // On mobile, the page header (back button) + top bundle (buttons
    // row) sit above the chat grid in flow but aren't useful
    // mid-conversation. Scroll the grid into the top of the viewport
    // on first paint so chat + input fill the screen; the user
    // reveals everything above by scrolling up.
    //
    // iOS Safari quirk: scrollIntoView is sometimes ignored on first
    // render before layout settles, AND a separate document.scrollTop
    // assignment is needed (vs window.scrollTo) on some versions.
    // Multiple retries + both call shapes hedge against both.
    if (window.matchMedia && window.matchMedia('(max-width: 900px)').matches) {
      var scrollPastTries = 0;
      function scrollPastBundle() {
        if (!wrap.isConnected) {
          if (scrollPastTries++ < 60) setTimeout(scrollPastBundle, 50);
          return;
        }
        var rect = gridRow.getBoundingClientRect();
        var target = Math.round(rect.top + (window.scrollY || window.pageYOffset || 0));
        if (target <= 0) {
          if (scrollPastTries++ < 60) setTimeout(scrollPastBundle, 50);
          return;
        }
        // Belt-and-suspenders: every scroll API mobile Safari has
        // honored at some point in its history. Cheap to call all.
        try { window.scrollTo({top: target, behavior: 'auto'}); } catch (_) {}
        try { window.scrollTo(0, target); } catch (_) {}
        if (document.documentElement) document.documentElement.scrollTop = target;
        if (document.body) document.body.scrollTop = target;
      }
      // Multi-tick retry: first attempt on next frame (layout done),
      // then again after a short delay (font/image loads finalized),
      // and once more after the typical first-paint settle window.
      if (window.requestAnimationFrame) {
        window.requestAnimationFrame(function() {
          window.requestAnimationFrame(scrollPastBundle);
        });
      } else {
        setTimeout(scrollPastBundle, 0);
      }
      setTimeout(scrollPastBundle, 250);
      setTimeout(scrollPastBundle, 800);
    }
    return wrap;
  };

  // Pipeline panel — submit form on top, structured streaming
  // transcript below, sessions sidebar on the left. Designed for
  // originally built for one app but reusable for any "kick off a multi-stage run, watch
  // it fill in, save the result" workflow (multi-stage runs, pipelines, ...).
