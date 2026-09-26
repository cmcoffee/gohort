  components.agent_loop_panel = function(cfg) {
    var idF   = cfg.id_field       || 'ID';
    var ttlF  = cfg.title_field    || 'Title';
    var atF   = cfg.date_field     || 'LastAt';
    var msgsF = cfg.messages_field || 'Messages';

    // The left rail is opt-in. Apps that don't supply list/load/
    // delete URLs get a single-column panel (no sidebar).
    var hasList = !!(cfg.list_url && cfg.load_url && cfg.delete_url);

    // Whether the list lives in a modal instead of a rail column. Resolved with
    // hasList, at the top, because three things built far apart all have to
    // agree about it: the rail HEADER (built first, and it must not offer to
    // collapse a column that does not exist), the expand tab, and the picker
    // itself. Each of those used to decide for itself, and the header never
    // got the message.
    var listPosModal = hasList && cfg.list_position === 'modal';

    // renderDetailValue draws one JSON value for a show_result modal, generically:
    // an object as labelled fields, an array of objects as one sub-card each,
    // a long or multi-line string as preformatted text, anything else inline.
    // Keys starting with "_" are plumbing and stay hidden, as in the table.
    function renderDetailValue(container, v, depth) {
      depth = depth || 0;
      if (v == null || v === '') return;
      if (Array.isArray(v)) {
        if (!v.length) return;
        v.forEach(function(item, i) {
          var card = el('div', {style: 'border:1px solid var(--border, rgba(127,127,127,0.25));border-radius:6px;padding:0.4rem 0.6rem;margin:0.3rem 0;background:var(--bg-2, rgba(127,127,127,0.05))'});
          if (item && typeof item === 'object' && !Array.isArray(item)) {
            card.appendChild(el('div', {style: 'color:var(--text-mute, #999);font-size:0.72rem;margin-bottom:0.2rem'}, [String(i + 1)]));
            renderDetailValue(card, item, depth + 1);
          } else {
            renderDetailValue(card, item, depth + 1);
          }
          container.appendChild(card);
        });
        return;
      }
      if (typeof v === 'object') {
        Object.keys(v).forEach(function(k) {
          if (k.charAt(0) === '_') return;
          var val = v[k];
          if (val == null || val === '' || (Array.isArray(val) && !val.length)) return;
          var row = el('div', {style: 'margin:0.35rem 0'});
          row.appendChild(el('div', {style: 'color:var(--text-mute, #999);font-size:0.74rem;margin-bottom:0.1rem'}, [k]));
          renderDetailValue(row, val, depth + 1);
          container.appendChild(row);
        });
        return;
      }
      var s = String(v);
      if (s.length > 80 || s.indexOf('\n') >= 0) {
        container.appendChild(el('pre', {style: 'white-space:pre-wrap;word-break:break-word;margin:0;font-size:0.8rem;max-height:320px;overflow:auto;background:var(--bg-1, rgba(127,127,127,0.1));padding:0.45rem;border-radius:4px'}, [s]));
      } else {
        container.appendChild(el('div', {style: 'font-size:0.85rem;word-break:break-word'}, [s]));
      }
    }

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
    // Record threads (record_nav_flag): a second app-named map, agentId -> the
    // id of a thread the app keeps as a LOG of what reached the agent. Pinned
    // at the top of the ordinary session list, opened read-only, and it swaps
    // nothing else. An alt-nav agent's pinned thread wins over its record.
    var recordNavFlag = cfg.record_nav_flag || '';
    function recordPinnedSession(agentId) {
      if (altPinnedSession(agentId)) { return ''; }
      var m = recordNavFlag && window[recordNavFlag];
      return (m && agentId && m[agentId]) || '';
    }
    // recordLocked is true while a record thread is open: nothing is sent
    // into a log. Checked by sendMessage and re-applied by enableInput, which
    // every finished run calls.
    var recordLocked = false;
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
    // cortexObsSince — the timestamp of the newest card on screen, sent back on
    // each poll so the server answers with what arrived AFTER it instead of
    // re-serving the thread. The dedupe above still runs; this stops the whole
    // transcript being transferred and parsed every six seconds just to find
    // the nothing that usually changed.
    var cortexObsSince = '';
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
    var noticeIds = {};   // framework-breadcrumb id -> true (see addNotice)
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
    // cfg.height overrides the viewport-tall default. Set inline rather than
    // via a class because it is a length the app chose, not one of a few sizes
    // the framework knows about. A panel mounted inside a row expander or a
    // modal is a PART of the page, and a viewport-tall one pushes whatever it
    // belongs to off the screen.
    if (cfg.height) { wrap.style.height = cfg.height; wrap.style.minHeight = '0'; }

    // --- Optional list sidebar -------------------------------------------
    var side = null, sideList = null, sideSearch = null, drawer = null, sideHdrEl = null, orchView = null, lastSessionTitle = '';
    // Nav dropdowns — built in the rail block (where the nav machinery is in
    // scope), shown in the topbar actions for fleet agents. One per distinct
    // item.menu, in the order their first item appears; navMenus holds them in
    // that order and navMenuByName indexes them by label.
    var navMenus = [], navMenuByName = {}, pinnedEl = null, navTopbarEl = null;
    // Only one top-bar dropdown (a nav menu / the grouped toolbar menus) is open
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
      //
      // Withheld from a modal-position panel. There, this same rail is mounted
      // inside the picker dialog, and "Hide Past sessions" is an offer to
      // collapse a rail COLUMN the layout does not have: the click toggled
      // .side-collapsed on a wrap that is already permanently collapsed, so it
      // did nothing, and the button sat in the dialog header looking like a
      // strip of a second, stuck rail. Worse on a phone, where the only rule
      // that has ever hidden this control is desktop-only (min-width: 901px,
      // and scoped to list-top at that), so it showed at full prominence in a
      // dialog that is the whole screen.
      var collapseBtn = listPosModal ? null : el('button', {
        class: 'ui-agent-collapse',
        title: 'Hide ' + (cfg.list_title || 'list'),
        onclick: function(){ toggleSideCollapse(); },
      }, ['☰']);
      // Secondary sidebar actions (Mark all read, Select) live behind ONE "⋯"
      // overflow so they don't crowd (and overlap) the "Sessions" title — the
      // header reads just "⋯ - + New". The menu is built whenever at least one
      // secondary action exists; each app opts into its members (mark_all_read_url
      // / bulk_select).
      var leftExtras = collapseBtn ? [collapseBtn] : [];
      var moreMenu = el('div', {class: 'ui-side-menu', style: 'display:none'});
      var moreAnchor = null; // set below, once the toggle exists
      function closeMoreMenu() { if (moreAnchor) moreAnchor.close(); else moreMenu.style.display = 'none'; }
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
            moreAnchor.toggle();
          }}, ['⋯']);
        // Anchored to the body rather than nested in the rail: the rail is
        // overflow:hidden, is a transformed drawer on a phone, and is inside a
        // scrolling dialog when list_position is "modal" — nested, the menu
        // opened and had nowhere to be, which reads as a dead button.
        moreAnchor = window.uiAnchorMenu(moreBtn, moreMenu);
        // Any click outside the menu closes it. The toggle stops propagation,
        // so its own click never reaches this.
        document.addEventListener('click', closeMoreMenu);
        leftExtras.push(el('div', {class: 'ui-side-menu-wrap'}, [moreBtn]));
      }
      var sideHdrBuilt = renderSideHeader({
        label:    cfg.list_title || 'Sessions',
        className: 'ui-chat-side-h',
        newTitle: cfg.new_label || 'New',
        onNew:    function(){ openSession(null); },
        // The × is a mobile-only control (.ui-chat-side-close is display:none
        // above the breakpoint), and what it should dismiss depends on where
        // this rail is mounted. In a modal-position panel it is inside the
        // picker dialog, where closeDrawer() has nothing to act on — the
        // backdrop is never appended in that mode — so it read as a second
        // dead control beside the collapse hamburger. Close what the reader is
        // actually looking at.
        onClose:  function(){ if (listPosModal) closeSessionPicker(); else closeDrawer(); },
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
      var orchBtns = [];
      var orchBadges = [];
      // orchFilterState holds the chips and search text of the view currently
      // open, so an auto-refresh does not quietly undo them.
      //
      // A live view re-fetches on a timer and re-renders through this same
      // function. Without somewhere outside it to keep the choices, a page
      // narrowed to "the ones that need me" would silently widen back to
      // everything a few seconds later, with the reader still looking at it.
      //
      // Deliberately NOT remembered across a deliberate open: coming back to a
      // view still narrowed, by a choice made minutes ago, is a list with rows
      // missing for a reason nobody remembers. clearOrchFilterState is called
      // where the user asks for a view, not where the timer redraws one.
      var orchFilterState = null;
      function clearOrchFilterState() { orchFilterState = null; }
      function renderOrchTable(rows, item, reload) {
        orchView.innerHTML = '';
        // Buttons that act on the LIST rather than on a row — creating a new
        // entry being the obvious one. Drawn BEFORE the empty check, because an
        // empty list is exactly when "add one" matters most: without it the
        // page that shows nothing also offers no way to change that, and the
        // control ends up in a navigation menu instead, which is to say
        // somewhere other than the thing it acts on.
        var vactions = (item && item.view_actions) || [];
        if (vactions.length) {
          var vbar = el('div', {style: 'display:flex;gap:0.4rem;flex-wrap:wrap;margin:0 0 0.7rem'});
          vactions.forEach(function(a) {
            var vb = el('button', {class: 'ui-row-btn' + (a.variant === 'danger' ? ' danger' : ''), type: 'button',
              onclick: function() { fireViewAction(a, reload); }}, [a.label || 'Go']);
            vbar.appendChild(vb);
          });
          orchView.appendChild(vbar);
        }
        // fireViewAction runs a list-level button. Same vocabulary as a row
        // action and deliberately a separate function: there is no row, so
        // anything that appends an id or reads a field would be wrong here
        // rather than merely unused.
        function fireViewAction(a, reload) {
          if (!a || !a.url) { return; }
          var agent = window.GOHORT_AGENT_ID || '';
          if (a.method === 'client') {
            var fn = (window.UIClientActions || {})[a.url];
            if (typeof fn !== 'function') { console.error('client action not registered: ' + a.url); return; }
            fn({reload: reload, agent: agent});
            return;
          }
          (async function() {
            if (a.confirm && !(await window.uiConfirm(a.confirm))) { return; }
            var u = a.url + (a.url.indexOf('?') >= 0 ? '&' : '?') + 'agent=' + encodeURIComponent(agent);
            fetch(u, {method: a.method || 'POST'})
              .then(function() { if (reload) reload(); })
              .catch(function(err) { console.error('view action failed: ' + err.message); });
          })();
        }
        // paintOrchRows draws the rows themselves into a host element.
        // Split out of renderOrchTable so a filter can repaint JUST the rows,
        // leaving the view actions and the filter controls where they are: a
        // control row that is torn down and rebuilt on every keystroke loses
        // the focus and the caret of the box being typed into.
        function paintOrchRows(host, rows) {
          if (!rows || !rows.length) {
            host.appendChild(el('div', {style: 'color:var(--text-mute, #999);padding:0.5rem'}, ['Nothing here yet.']));
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
          // rowActionID is the id a row action sends: the field it names in
          // id_field, else the row's _id.
          function rowActionID(a, row) {
            var v = row && row[(a && a.id_field) || '_id'];
            return v == null ? '' : String(v);
          }
          function openRowPicker(a, row) {
            var agent = window.GOHORT_AGENT_ID || '';
            var src = a.picker_source + (a.picker_source.indexOf('?') >= 0 ? '&' : '?') + 'agent=' + encodeURIComponent(agent);
            // Which ROW the choice is for. A picker_source is one URL for a
            // whole column of rows, and the right choices are not always the
            // same for each of them — a schedule that runs a pipeline needs
            // pipelines offered, not agents. The POST already carries row._id;
            // without it here, the source has to guess, and a picker offering
            // the wrong KIND of thing is worse than no picker: every choice in
            // it is refused.
            if (row && row._id) src += '&row=' + encodeURIComponent(row._id);
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
                      var u = a.url + '?id=' + encodeURIComponent(rowActionID(a, row)) + '&agent=' + encodeURIComponent(agent) + '&value=' + encodeURIComponent(opt.value);
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
          // fireRowAction runs one row action the way both layouts need: a picker
          // opens its chooser; a show_result action GETs the record and shows it
          // in a modal; everything else fires and reloads the view. One place, so
          // the cards and the table cannot drift on what a button does.
          function fireRowAction(a, row) {
            if (a.picker_source) { openRowPicker(a, row); return; }
            // A CLIENT action: hand the row to app-registered browser code
            // (uiRegisterClientAction) instead of calling an endpoint. The
            // toolbar has had this seam from the start and the schedule rail's
            // row builder grew its own; row actions were the one surface that
            // could not reach it, so an app with a per-row EDITOR had to keep a
            // second list somewhere just to own the click.
            //
            // core/ui stays a renderer: it passes the row id, the row, and a way
            // to re-render, and never learns what the action does.
            if (String(a.method || '').toLowerCase() === 'client') {
              var fn = window.UIClientActions && window.UIClientActions[a.url];
              if (!fn) { console.error('client row action not registered: ' + a.url); return; }
              fn({id: rowActionID(a, row), row: row, reload: reload});
              return;
            }
            // A NAVIGATION rather than a call: open another nav view, optionally
            // already narrowed. It exists because a summary figure had no way to
            // reach the list it counts — the number and the rows behind it lived
            // in different menus with nothing joining them, so "2 failed" was a
            // dead end. The target is named "<Menu>/<Label>" because a Source can
            // appear in two menus (the same view asked about one agent and about
            // everyone) and picking the wrong one answers the wrong question.
            if (a.view) {
              var want = String(a.view);
              var found = -1;
              (cfg.orchestrator_nav || []).forEach(function(it, k) {
                if (found >= 0) return;
                if (((it.menu || DEFAULT_NAV_MENU) + '/' + (it.label || '')) === want) found = k;
              });
              if (found < 0) { console.error('nav view not found: ' + want); return; }
              // {agent} resolves to the agent in view, so a per-agent summary can
              // hand its own scope to a view that is otherwise fleet-wide.
              var q = String(a.query || '').replace(/\{agent\}/g, encodeURIComponent(window.GOHORT_AGENT_ID || ''));
              closeNavMenus();
              selectOrchNav(found, q, a.note);
              return;
            }
            var rowURL = a.url + '?id=' + encodeURIComponent(rowActionID(a, row)) + '&agent=' + encodeURIComponent(window.GOHORT_AGENT_ID || '');
            if (a.show_result) {
              fetch(rowURL, {method: a.method || 'GET'})
                .then(function(r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
                .then(function(data) {
                  window.uiOpenModal({
                    title: a.label,
                    width: 'min(760px, 94vw)',
                    mount: function(body) {
                      var empty = data == null || (typeof data === 'object' && !Object.keys(data).length);
                      if (empty) {
                        body.appendChild(el('div', {style: 'color:var(--text-mute, #999);font-size:0.85rem'}, ['Nothing to show: this record is gone or empty.']));
                        return;
                      }
                      renderDetailValue(body, data, 0);
                    }
                  });
                })
                .catch(function(err) { console.error('row detail failed: ' + err.message); });
              return;
            }
            fetch(rowURL, {method: a.method || 'POST'})
              .then(function() { if (reload) reload(); })
              .catch(function(err) { console.error('row action failed: ' + err.message); });
          }
          if (item && item.layout === 'cards') {
            var cactions = (item && item.row_actions) || [];
            var lastSection = null;
            rows.forEach(function(row) {
              // A "_section" heading, drawn once each time the value changes. It
              // is what lets ONE source render as several titled lists (a summary
              // view) instead of one list per menu entry.
              if (row._section && row._section !== lastSection) {
                lastSection = row._section;
                host.appendChild(el('div', {style: 'margin:0.9rem 0 0.35rem;font-size:0.68rem;font-weight:700;text-transform:uppercase;letter-spacing:0.06em;color:var(--text-mute, #999)'}, [row._section]));
              }
              // Each row's OWN visible keys, not the first row's: a view that
              // groups several kinds of thing has a different shape per section,
              // and reading the shape off row one renders the rest blank.
              var ckeys = Object.keys(row).filter(function(k) { return k.charAt(0) !== '_'; });
              // "_depth" nests a row under the one above it. A list whose items
              // contain other items is an ordinary shape, so this is a property
              // of a ROW rather than a second kind of list: the server emits the
              // rows already in order and says how deep each one sits, and the
              // layout does not have to learn what the nesting means.
              //
              // Indent only, no connector glyphs. A box-drawing tree needs to
              // know whether each ancestor has more siblings coming, which is a
              // second model of the same data held in the renderer, and it is
              // wrong the first time a row is filtered out of the middle.
              var depth = Math.max(0, Math.min(6, parseInt(row._depth, 10) || 0));
              var card = el('div', {style: 'display:flex;align-items:center;gap:0.6rem;border:1px solid var(--border, rgba(127,127,127,0.25));border-radius:7px;padding:0.45rem 0.7rem;margin-bottom:0.4rem;background:var(--bg-1, rgba(127,127,127,0.03));flex-wrap:wrap'
                + (depth ? ';margin-left:' + (depth * 1.25) + 'rem' : '')});
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
                // Gated segments are dropped BEFORE the loop, so the one that
                // survives first still renders without a left border and the
                // control does not come out with a seam down its leading edge.
                var segOpts = (item.state_options || []).filter(function(opt) {
                  if (opt.only_if && !row[opt.only_if]) return false;
                  if (opt.hide_if && row[opt.hide_if]) return false;
                  return true;
                });
                segOpts.forEach(function(opt, oi) {
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
                  fireRowAction(a, row);
                }}, [a.label]);
                controls.appendChild(btn);
              });
              if (controls.childNodes.length) card.appendChild(controls);
              host.appendChild(card);
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
                  // fireRowAction stamps the in-view agent so per-agent row actions
                  // (e.g. History turn-scrub) target the right agent's thread.
                  fireRowAction(a, row);
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
          host.appendChild(tbl);
        }

        // --- Filters -------------------------------------------------------
        //
        // Declared by the app (item.filters, item.search_placeholder) and
        // applied HERE, in the browser, over the rows already fetched: "which
        // of these am I looking at" does not need a round trip, and a list that
        // disappears while it answers is worse than no filter.
        //
        // core/ui never learns what any of them MEAN. An option names a row
        // FIELD and how to test it: equal to a value, or merely truthy. Truthy
        // is what lets "only the ones in trouble" be expressed by an app
        // without this file knowing what trouble is.
        var filters = (item && item.filters) || [];
        var searchHint = (item && item.search_placeholder) || '';
        var chosen = filters.map(function() { return 0; }); // first option is the default
        var query = '';
        // Pick up where an auto-refresh left off. Keyed on the view, so a
        // stale state from a different one cannot be applied to these rows:
        // the chips would not correspond to the options on screen.
        var filterKey = (item && item.menu || '') + '\u0000' + (item && item.label || '') +
          '\u0000' + (item && item.source || '');
        if (orchFilterState && orchFilterState.key === filterKey &&
            orchFilterState.chosen.length === chosen.length) {
          chosen = orchFilterState.chosen.slice();
          query = orchFilterState.query;
        }
        function rememberFilters() {
          orchFilterState = {key: filterKey, chosen: chosen.slice(), query: query};
        }

        function optionMatches(opt, row) {
          if (!opt || !opt.field) return true; // an option with no field matches everything
          var v = row[opt.field];
          if (opt.equals) return String(v == null ? '' : v) === opt.equals;
          // Truthy, in the shapes a row field actually arrives in: a non-empty
          // string, a true, a non-zero number. "0" and "false" are values the
          // server chose to send, not marks of presence.
          if (v === true) return true;
          if (typeof v === 'number') return v !== 0;
          var sv = String(v == null ? '' : v);
          return sv !== '' && sv !== '0' && sv !== 'false';
        }
        function rowMatchesQuery(row, q) {
          if (!q) return true;
          for (var k in row) {
            if (!Object.prototype.hasOwnProperty.call(row, k)) continue;
            // Hidden plumbing is not text anybody typed at, and searching it
            // matches rows for reasons the reader cannot see on the page.
            if (k.charAt(0) === '_') continue;
            var v = row[k];
            if (v != null && String(v).toLowerCase().indexOf(q) >= 0) return true;
          }
          return false;
        }
        // passesOthers is the filter test with ONE filter left out, which is
        // what a chip's count has to be: the number of rows you would see after
        // clicking it, not the number matching it in isolation.
        function passesOthers(row, skip, q) {
          for (var i = 0; i < filters.length; i++) {
            if (i === skip) continue;
            if (!optionMatches((filters[i].options || [])[chosen[i]], row)) return false;
          }
          return rowMatchesQuery(row, q);
        }
        function visibleRows() {
          var q = query.trim().toLowerCase();
          return (rows || []).filter(function(row) { return passesOthers(row, -1, q); });
        }

        var rowsHost = el('div');
        var chipRefs = [];
        function refreshChips() {
          var q = query.trim().toLowerCase();
          chipRefs.forEach(function(ref) {
            var n = 0;
            (rows || []).forEach(function(row) {
              if (passesOthers(row, ref.fi, q) && optionMatches(ref.opt, row)) n++;
            });
            ref.btn.className = 'ui-tab' + (chosen[ref.fi] === ref.oi ? ' active' : '');
            ref.count.textContent = ' ' + n;
            // A choice that would empty the page says so before it is made.
            ref.btn.style.opacity = n ? '' : '0.55';
          });
        }
        function repaint() {
          rememberFilters();
          refreshChips();
          rowsHost.innerHTML = '';
          var vis = visibleRows();
          if (!vis.length && rows && rows.length) {
            // Distinct from "Nothing here yet": there IS something here, and
            // the reader hid it. Say which and give the way back, or a filtered
            // page is indistinguishable from an empty one.
            var back = el('button', {type: 'button', class: 'ui-row-btn',
              style: 'padding:0.15rem 0.55rem;font-size:0.74rem',
              onclick: function() {
                chosen = filters.map(function() { return 0; });
                query = '';
                if (searchBox) searchBox.value = '';
                repaint();
              }}, ['Show all']);
            rowsHost.appendChild(el('div', {style: 'display:flex;align-items:center;gap:0.6rem;flex-wrap:wrap;color:var(--text-mute, #999);padding:0.5rem'}, [
              el('span', {}, ['None of the ' + rows.length + ' here match what you have picked.']), back]));
            return;
          }
          paintOrchRows(rowsHost, vis);
        }

        var searchBox = null;
        if (filters.length || searchHint) {
          var bar = el('div', {style: 'display:flex;align-items:center;gap:0.6rem;flex-wrap:wrap;' +
            'margin:0 0 0.8rem;padding-bottom:0.6rem;border-bottom:1px solid var(--border)'});
          if (searchHint) {
            searchBox = el('input', {type: 'search', class: 'ui-filter-search', placeholder: searchHint,
              value: query, style: 'flex:0 1 15rem;min-width:9rem'});
            // Typed into, not submitted: the list narrows as you go, and the
            // bar itself is never rebuilt, so the caret stays where it was.
            searchBox.addEventListener('input', function() { query = searchBox.value || ''; repaint(); });
            bar.appendChild(searchBox);
          }
          filters.forEach(function(f, fi) {
            if (f.label) {
              bar.appendChild(el('span', {style: 'font-size:0.72rem;text-transform:uppercase;letter-spacing:0.05em;color:var(--text-mute, #999)'}, [f.label]));
            }
            // TABS, not chips. A filter group is mutually exclusive with its
            // first option as the default, which is what a tab bar is, and the
            // house already draws one: .ui-tabbar / .ui-tab, the same control
            // along the top of every admin page. Rendering the same idea two
            // ways teaches the reader that they are two ideas.
            //
            // The chip styling stays where chips are genuinely chips: the
            // multi-select picker, where several can be on at once.
            var group = el('div', {class: 'ui-tabbar', style: 'margin:0;padding:0;border:0'});
            (f.options || []).forEach(function(opt, oi) {
              var count = el('span', {style: 'opacity:0.7;margin-left:0.35rem'}, ['']);
              var btn = el('button', {type: 'button', class: 'ui-tab',
                onclick: function() { chosen[fi] = oi; repaint(); }}, [opt.label || '?', count]);
              chipRefs.push({fi: fi, oi: oi, opt: opt, btn: btn, count: count});
              group.appendChild(btn);
            });
            bar.appendChild(group);
          });
          orchView.appendChild(bar);
        }
        orchView.appendChild(rowsHost);
        repaint();
      }
      // orchSourceURL pins a nav source/action fetch to the agent currently
      // in view. Data views like History are per-agent on the server (they key
      // off ?agent=…); without this the fetch omits the param and the server
      // falls back to its default agent, so a non-default channel's History
      // always renders empty. Mirrors the action_url agent-stamping below.
      // A fleet-scoped item (scope:"fleet") is asked about everything the user
      // owns, so it must NOT carry an agent: a handler that answers fleet-wide
      // when given none can otherwise never be reached from this menu.
      // extra is a query string a NAVIGATION carried in — a view opened
      // already narrowed (the failures behind a count, say) rather than whole.
      // It rides the source for that open only; the nav item itself is
      // unchanged, so reaching the same view from its own button still asks the
      // unnarrowed question.
      function orchSourceURL(src, item, extra) {
        if (!src) return src;
        var url = src;
        // {agent} in the PATH, for a source whose id is part of the route
        // rather than a query (/agent/<id>/access). Substituted before the
        // ?agent= stamp below, which a query-keyed handler still wants: a
        // source can need either, and one that names {agent} has said which.
        if (url.indexOf('{agent}') >= 0) {
          url = url.replace(/\{agent\}/g, encodeURIComponent(window.GOHORT_AGENT_ID || ''));
        }
        if (extra) url += (url.indexOf('?') >= 0 ? '&' : '?') + extra;
        if (item && item.scope === 'fleet') return url;
        return url + (url.indexOf('?') >= 0 ? '&' : '?') + 'agent=' + encodeURIComponent(window.GOHORT_AGENT_ID || '');
      }
      // openHomeThread lands on the agent's home thread — a pinned session in
      // the normal list, not a nav row (channel model: the home thread is just
      // a session). Used on entering a channel agent and after a channel-wide
      // action. Non-channel agents have no pinned thread, so it's a no-op there.
      function openHomeThread() {
        var hs = altPinnedSession(window.GOHORT_AGENT_ID);
        if (hs) openSession(hs);
      }
      // Auto-refresh timer for the currently-open nav data view (items with
      // auto_refresh_ms). One at a time: cleared whenever the view changes.
      var orchViewTimer = null;
      function clearOrchViewTimer() {
        if (orchViewTimer) { clearInterval(orchViewTimer); orchViewTimer = null; }
      }
      // paintNarrowNote marks a view that was entered NARROWED and gives the
      // way back out. Without it a filtered pane is indistinguishable from the
      // whole one — same title, same rows, fewer of them — so the reader either
      // trusts a partial list as complete or cannot get back to the rest.
      //
      // Prepended AFTER the rows are drawn, because the render clears the pane
      // and returns early on an empty result: a narrowing that matched nothing
      // is exactly when the way back matters most.
      //
      // note is the app's own wording. When it gives none, the query itself is
      // shown — "status: failed" — which is raw but never absent, and a marker
      // that can go missing is the bug this fixes.
      function paintNarrowNote(idx, item, extraQuery, note) {
        if (!orchView || !extraQuery) return;
        var text = String(note || '').trim();
        if (!text) {
          var parts = [];
          String(extraQuery).split('&').forEach(function(pair) {
            var eq = pair.indexOf('=');
            if (eq <= 0) return;
            var k = decodeURIComponent(pair.slice(0, eq));
            // The agent stamp is scope, not a filter the reader chose; it rides
            // every per-agent source already and naming it here reads as a
            // narrowing that was never applied.
            if (k === 'agent') return;
            parts.push(k + ': ' + decodeURIComponent(pair.slice(eq + 1)));
          });
          text = parts.length ? ('Filtered: ' + parts.join(', ')) : 'Filtered';
        }
        var back = el('button', {type: 'button', class: 'ui-row-btn',
          style: 'padding:0.15rem 0.55rem;font-size:0.74rem;flex:0 0 auto',
          onclick: function() { selectOrchNav(idx); }}, ['Show all']);
        var bar = el('div', {style: 'display:flex;align-items:center;gap:0.5rem;flex-wrap:wrap;' +
          'margin:0 0 0.5rem;padding:0.35rem 0.6rem;border:1px solid var(--accent, #4a9eff);' +
          'border-radius:6px;background:rgba(88,166,255,0.08)'}, [
            el('span', {style: 'flex:1 1 auto;min-width:0;font-size:0.78rem;color:var(--text, inherit)'}, [text]),
            back,
          ]);
        if (orchView.firstChild) orchView.insertBefore(bar, orchView.firstChild);
        else orchView.appendChild(bar);
      }
      // keepFilters distinguishes a view being OPENED from one redrawing after
      // a change made inside it. Opening asks for the view whole; a redraw
      // after you set a policy is the same view you were already looking at,
      // and throwing your tab away there is what made every click bounce back
      // to All.
      function selectOrchNav(idx, extraQuery, note, keepFilters) {
        var item = (cfg.orchestrator_nav || [])[idx] || {};
        clearOrchViewTimer();
        // A nav item that opens an app's own FORM rather than listing or
        // posting: the client action owns the dialog and the endpoint. Same
        // seam as a client row action, and it is here because a list view has
        // no page-level button of its own — an app whose list you can act on
        // per row still needed somewhere to put "new one of these".
        if (String(item.action_method || '').toLowerCase() === 'client' && item.action_url) {
            var cfn = window.UIClientActions && window.UIClientActions[item.action_url];
            if (!cfn) { console.error('client nav action not registered: ' + item.action_url); return; }
            closeNavMenus();
            cfn({reload: function() { selectOrchNav(idx, extraQuery, note, true); }});
            return;
        }
        // Action items are buttons (clear / decommission): POST to the URL
        // for the current agent after an optional confirm, then refresh.
        if (item.action_url) {
          (async function() {
            if (item.confirm && window.uiConfirm && !(await window.uiConfirm(item.confirm))) return;
            var url = item.action_url + (item.action_url.indexOf('?') >= 0 ? '&' : '?') + 'agent=' + encodeURIComponent(window.GOHORT_AGENT_ID || '');
            fetch(url, {method: 'POST'})
              .then(function() {
                refreshChannelBadges(); closeDrawer();
                // A record agent has no home thread to land on: refresh its
                // list, and the record if it is the thread on screen.
                var rec = recordPinnedSession(window.GOHORT_AGENT_ID);
                if (rec) {
                  if (activeSessionId === rec) openSession(rec);
                  loadSessions();
                } else {
                  openHomeThread();
                }
              })
              .catch(function(err) { console.error('channel action failed: ' + err.message); });
          })();
          return;
        }
        orchBtns.forEach(function(b, i) {
          var on = (i === idx);
          var navItem = (cfg.orchestrator_nav || [])[i] || {};
          if (navItem.topbar) {
            // Selection reads as an outline; the fill is the pending tint, set
            // by refreshChannelBadges. Two signals, two channels.
            b.style.outline = on ? '2px solid var(--accent, #4a9eff)' : '';
            b.style.outlineOffset = on ? '-2px' : '';
            return;
          }
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
          // A nav item naming a PAGE renders that page here, with the same
          // renderer a document uses. The app declares the page once and
          // serves it both ways, so this panel and the standalone page cannot
          // drift into two surfaces that merely resemble each other.
          if (item.page_source) {
            fetch(orchSourceURL(item.page_source, item, extraQuery))
              .then(function(r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
              .then(function(pcfg) {
                orchView.textContent = '';
                // The document's own chrome is already around this: the page
                // header, its back arrow and its footer belong to a document,
                // and a second set inside a panel is two of everything.
                window.uiRenderPageBody(pcfg, orchView);
              })
              .catch(function(err) { orchView.textContent = 'Failed to load: ' + err.message; });
            return;
          }
          // A view the user ASKED for opens whole. A redraw after a change
          // does not: it is the same view, still on screen, and the tab and
          // the search text are where the reader left them.
          if (!keepFilters) clearOrchFilterState();
          var reload = function() { selectOrchNav(idx, extraQuery, note, true); };
          fetch(orchSourceURL(item.source, item, extraQuery)).then(function(r) { return r.ok ? r.json() : []; })
            .then(function(rows) { renderOrchTable(rows, item, reload); paintNarrowNote(idx, item, extraQuery, note); })
            .catch(function(err) { orchView.textContent = 'Failed to load: ' + err.message; });
          // Live views: silently re-fetch + re-render on the configured
          // interval while this view stays open. No "Loading…" flash; a
          // failed poll keeps the last rendered rows. Stops itself when the
          // overlay is hidden or detached (view switched / chat resumed).
          if (item.auto_refresh_ms > 0) {
            orchViewTimer = setInterval(function() {
              if (!orchView || orchView.style.display === 'none' || !document.body.contains(orchView)) {
                clearOrchViewTimer();
                return;
              }
              fetch(orchSourceURL(item.source, item, extraQuery)).then(function(r) { return r.ok ? r.json() : null; })
                .then(function(rows) { if (rows) { renderOrchTable(rows, item, reload); paintNarrowNote(idx, item, extraQuery, note); } })
                .catch(function() {});
            }, item.auto_refresh_ms);
          }
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
      // updateNavMenuDots lights the dot on a menu's button when ANY view
      // inside THAT menu currently shows a nonzero count — the at-a-glance "you
      // have pending items" signal the old rail-box badges gave, now that the
      // per-view badges live inside a closed dropdown. Per menu, not global: a
      // dot on every button at once says nothing about where to look.
      // Selection on a topbar control is an OUTLINE, and it has to come off
      // when the user goes somewhere else — opening a session, switching
      // agents. Nothing else clears it: the session-open reset below predates
      // this control and only knows about border-based rows.
      function clearTopbarNavSelection() {
        (cfg.orchestrator_nav || []).forEach(function(item, i) {
          if (item.topbar && orchBtns[i]) orchBtns[i].style.outline = '';
        });
      }
      function updateNavMenuDots() {
        navMenus.forEach(function(m) {
          // Only the indices that actually render in THIS menu. Pinned and
          // topbar items live elsewhere and were never added to m.items.
          var any = m.items.some(function(i) {
            var b = orchBadges[i];
            return b && b.style.display !== 'none' && b.textContent && b.textContent !== '0';
          });
          m.dot.style.display = any ? '' : 'none';
        });
      }
      // onlyAllAgents: the current agent isn't opted into the alt nav, so only
      // the always-on entries are on screen — don't fetch counts for the views
      // that aren't rendered. Keyed on the flag alone, not on placement: a menu
      // entry marked all_agents renders off the alt nav too, and testing for a
      // pinned/topbar placement left its badge permanently empty.
      //
      // onlyQueues narrows further to the items that declare a badge_field —
      // the ones whose count means "there is something for you to do". A badge
      // WITHOUT one counts every row, which is a size, not a queue: it changes
      // when anything is added and says nothing about whether you are needed.
      // A deployment can declare many all_agents items and few queues among
      // them, so refreshing every badge on a timer means re-reading each of
      // those sources in full, every tick, to keep numbers nobody is waiting
      // on. Navigation can afford that; a clock cannot.
      function refreshChannelBadges(onlyAllAgents, onlyQueues) {
        (cfg.orchestrator_nav || []).forEach(function(item, i) {
          var badge = orchBadges[i];
          if (!item.source || !badge) return;
          if (onlyAllAgents && !item.all_agents) return;
          if (onlyQueues && !item.badge_field) return;
          fetch(orchSourceURL(item.source, item)).then(function(r) { return r.ok ? r.json() : []; })
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
              // No background tint for a topbar control: the count pill already
              // says the queue is non-empty, and a persistent fill is
              // indistinguishable from "selected" — which is what the outline
              // means here.
              updateNavMenuDots();
            })
            .catch(function() {});
        });
      }
      // Channel/fleet management lives in topbar dropdowns — NOT in a box in
      // the session rail (channel model: the rail is threads only). Each menu's
      // panel is absolutely positioned under its own button; each item is a
      // management view (Enabled agents / Event monitors) with a live count
      // badge, or a channel-wide action (Compact / Clear).
      //
      // An item names its menu; the menus are built on demand in the order
      // those names first appear, so a host app decides both the buttons and
      // their order purely by how it declares its nav. Nothing here knows what
      // any of them are called.
      var DEFAULT_NAV_MENU = 'Manage';
      function navMenuFor(item) {
        var name = (item && item.menu) || DEFAULT_NAV_MENU;
        if (navMenuByName[name]) return navMenuByName[name];
        var panel = el('div', {class: 'ui-channel-menu', style: 'display:none;position:absolute;right:0;top:calc(100% + 4px);z-index:40;min-width:210px;flex-direction:column;gap:0.1rem;padding:0.35rem;border:1px solid var(--border, rgba(127,127,127,0.3));border-radius:6px;background:var(--bg-1, #1b1b2b);box-shadow:0 6px 24px rgba(0,0,0,0.35)'});
        var m = {name: name, panel: panel, items: [], hdrs: [], lastGroup: null, btn: null, dot: null, control: null};
        m.close = function() { panel.style.display = 'none'; clearOpenTopbarMenu(m.close); };
        var dot = el('span', {class: 'ui-unread-dot', title: 'Pending items',
          style: 'display:none;width:7px;height:7px;border-radius:50%;background:var(--accent, #4a9eff);margin-left:0.35rem;flex:0 0 auto'}, ['']);
        var btn = el('button', {type: 'button', class: 'ui-row-btn', title: name,
          onclick: function(ev) {
            ev.stopPropagation();
            var open = panel.style.display === 'none' || !panel.style.display;
            if (open) {
              setOpenTopbarMenu(m.close); // close any open toolbar menu first
              panel.style.display = 'flex';
              refreshChannelBadges();
            } else {
              m.close();
            }
          }}, [name + ' ▾']);
        btn.appendChild(dot);
        m.btn = btn;
        m.dot = dot;
        m.control = el('div', {style: 'position:relative;display:none'}, [btn, panel]);
        // Close on any outside click. One listener per menu, each testing only
        // its own control, so a click inside one menu closes the others via
        // their own listeners rather than through shared bookkeeping.
        document.addEventListener('click', function(ev) {
          if (panel.style.display && panel.style.display !== 'none' &&
              !m.control.contains(ev.target)) m.close();
        });
        navMenus.push(m);
        navMenuByName[name] = m;
        return m;
      }
      function closeNavMenus() { navMenus.forEach(function(m) { m.close(); }); }
      // Pinned items (action queues like Permissions) get a prominent row ABOVE
      // the session list; everything else lives in the Manage dropdown. One pass
      // keeps orchBtns/orchBadges index-aligned with cfg.orchestrator_nav.
      pinnedEl = el('div', {class: 'ui-channel-pinned', style: 'display:none;flex-direction:column;gap:0.2rem;padding:0.45rem 0.5rem;border-bottom:1px solid var(--border, rgba(127,127,127,0.3))'});
      // A dropdown only earns its place when there's at least one NON-pinned
      // nav item to put in it (a management view or a channel action). An
      // alt-nav agent with no such items (e.g. a published dashboard agent that
      // carries the Cortex hero thread but no management surface) gets no empty
      // buttons — navMenus simply stays empty, since a menu is only created
      // when an item asks for one.
      // Pinned rows flagged all_agents render for every agent, not just the
      // alt-nav ones — their queue belongs to the USER, so gating it on which
      // agent is selected would hide pending work (and strand it completely
      // when the user has no alt-nav agent at all).
      var hasAllAgentPinned = (cfg.orchestrator_nav || []).some(function(it){ return (it.pinned || it.topbar) && it.all_agents; });
      // The same question without the placement filter: is ANYTHING on screen
      // for a non-alt-nav agent? Menu entries count now that they honor the
      // flag, and badge refresh keys off this rather than the pinned-only form.
      var hasAllAgentNav = (cfg.orchestrator_nav || []).some(function(it){ return it.all_agents; });
      // Topbar-placed items: a queue whose data spans agents doesn't belong in
      // the agent's own rail. Compact button + count pill, right-aligned in the
      // action row (see the append below).
      navTopbarEl = el('div', {style: 'display:none;align-items:stretch;gap:0.3rem;flex:0 0 auto;height:100%'});
      (cfg.orchestrator_nav || []).forEach(function(item, i) {
        var badge = null;
        var b;
        if (item.topbar) {
          var tAccent = '#58a6ff';
          badge = el('span', {class: 'ui-channel-badge', style: 'display:none;min-width:1.15rem;text-align:center;padding:0.02rem 0.4rem;border-radius:999px;font-size:0.68rem;font-weight:700;background:' + tAccent + ';color:#fff'}, ['']);
          b = el('button', {type: 'button', class: 'ui-row-btn', title: item.subtitle || item.label,
            // Border stated inline rather than left to .ui-row-btn: this
            // control sits in its own table cell, outside .ui-agent-actions,
            // so none of the layout's button rules reach it and it rendered
            // with no visible edge at rest.
            style: 'display:inline-flex;flex-direction:column;align-items:center;justify-content:center;gap:0.15rem;' +
              'height:100%;padding:0.3rem 0.7rem;border:1px solid var(--border, rgba(127,127,127,0.35));' +
              'border-radius:6px;background:transparent',
            onclick: function() { selectOrchNav(i); }}, [
              // Glyph and count share the top line so the count reads as the
              // queue's depth; the label sits under it like a toolbar tile.
              el('div', {style: 'display:flex;align-items:center;gap:0.3rem'}, [
                el('span', {style: 'color:' + tAccent + ';font-size:1.05rem'}, [item.icon || '•']),
                badge,
              ]),
              el('span', {style: 'font-size:0.72rem;opacity:0.85;white-space:nowrap'}, [item.label || ('View ' + (i + 1))]),
            ]);
          navTopbarEl.appendChild(b);
        } else if (item.pinned) {
          // Modernized pinned row (Permissions etc.) — mirrors the Cortex marked
          // row: a colored glyph + bold title (+ optional subtitle) + count pill,
          // rounded with a faint always-on accent tint that strengthens when the
          // queue has pending items (set in refreshChannelBadges).
          var pAccent = '#58a6ff';
          var plabel = el('span', {style: 'font-weight:700;overflow:hidden;text-overflow:ellipsis;min-width:0'}, [item.label || ('View ' + (i + 1))]);
          // margin-left:auto parks the count on the RIGHT EDGE of the row
          // rather than letting it trail the label. Hugging the text means it
          // sits in a different place for every label length, which reads as
          // part of the title instead of as a count of what is inside; against
          // the edge it lines up with the other rows and can be scanned down
          // the column. The gap stays as a minimum for a label long enough to
          // reach it.
          badge = el('span', {class: 'ui-channel-badge', style: 'display:none;min-width:1.3rem;text-align:center;padding:0.05rem 0.45rem;border-radius:999px;font-size:0.7rem;font-weight:700;background:' + pAccent + ';color:#fff;flex:0 0 auto;margin-left:auto'}, ['']);
          var ptitle = el('div', {style: 'display:flex;align-items:center;gap:0.4rem;white-space:nowrap;overflow:hidden;width:100%'}, [plabel, badge]);
          var pbody = [ptitle];
          if (item.subtitle) {
            pbody.push(el('div', {style: 'font-size:0.74rem;color:var(--text-mute, #999);margin-top:0.1rem;overflow:hidden;text-overflow:ellipsis;white-space:nowrap'}, [item.subtitle]));
          }
          var pkids = [
            el('span', {style: 'flex:0 0 1.1rem;text-align:center;font-size:0.95rem;color:' + pAccent}, [item.icon || '•']),
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
          var menu = navMenuFor(item);
          // A new Group value draws its heading once, before this row —
          // tracked PER MENU, so the same group name may subdivide two
          // different menus without the second one silently skipping it.
          //
          // Drawn as a RULE across the panel with the name sitting on it, not
          // as one more full-width row: in a column of buttons, a bare line of
          // text at the same width reads as another button that happens not to
          // respond. The divider is what says "a different kind of thing starts
          // here". A heading that opens the menu needs no rule above it.
          if (item.group && item.group !== menu.lastGroup) {
            var firstInMenu = menu.panel.childNodes.length === 0;
            menu.lastGroup = item.group;
            // Kept so applyOrchMode can hide the heading when every row it
            // labels is hidden — a bare group name over nothing reads as a
            // broken menu.
            var ghdr = el('div', {style: 'margin:' + (firstInMenu ? '0' : '0.45rem') + ' 0.35rem 0.15rem;' +
              (firstInMenu ? '' : 'border-top:1px solid var(--border, rgba(127,127,127,0.3));') +
              'padding:' + (firstInMenu ? '0.2rem' : '0.5rem') + ' 0.25rem 0.1rem;' +
              'font-size:0.62rem;font-weight:700;text-transform:uppercase;letter-spacing:0.08em;color:var(--text-mute, #999)'}, [item.group]);
            menu.panel.appendChild(ghdr);
            menu.hdrs.push({group: item.group, el: ghdr});
          }
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
            onclick: function() { menu.close(); selectOrchNav(i); }}, kids);
          menu.panel.appendChild(b);
          menu.items.push(i);
        }
        orchBtns.push(b);
        orchBadges.push(badge);
      });
      // Pinned rows sit at the very top of the rail, above the session header.
      if (sideHdrEl && pinnedEl.childNodes.length) side.insertBefore(pinnedEl, sideHdrEl);
      var lastOrchAgent; // last agent applyOrchMode saw — tells a real switch from the double-fire on initial load
      function applyOrchMode(agentId) {
        var isOrch = isAltNavAgent(agentId);
        if (pinnedEl) pinnedEl.style.display = (isOrch || hasAllAgentPinned) ? '' : 'none';
        // Off the alt nav, only the all_agents entries survive — in the pinned
        // strip, in the topbar, and inside a menu alike. One rule, applied
        // wherever an item renders: the flag says the item's DATA does not
        // belong to the selected agent, and where it was PLACED cannot change
        // whether that is true.
        var anyTopbar = false;
        var isRecord = !!recordPinnedSession(agentId);
        var navOn = function(i) {
          var item = (cfg.orchestrator_nav || [])[i] || {};
          return isOrch || !!item.all_agents || (!!item.record_too && isRecord);
        };
        (cfg.orchestrator_nav || []).forEach(function(item, i) {
          if (!orchBtns[i]) return;
          var on = navOn(i);
          if (item.topbar) {
            orchBtns[i].style.display = on ? 'inline-flex' : 'none';
            if (on) anyTopbar = true;
            return;
          }
          orchBtns[i].style.display = on ? 'flex' : 'none';
        });
        // A menu follows its contents. Shown when it still holds something,
        // hidden when everything in it is gated away — so a dropdown never
        // opens onto an empty panel, and the Cortex actions take the whole
        // menu with them only when nothing else is left in it.
        navMenus.forEach(function(m) {
          m.control.style.display = m.items.some(navOn) ? '' : 'none';
          (m.hdrs || []).forEach(function(h) {
            var live = m.items.some(function(i) {
              var item = (cfg.orchestrator_nav || [])[i] || {};
              return (item.group || '') === h.group && navOn(i);
            });
            h.el.style.display = live ? '' : 'none';
          });
        });
        if (navTopbarEl) navTopbarEl.style.display = anyTopbar ? 'flex' : 'none';
        if (!isOrch && hasAllAgentNav) refreshChannelBadges(true);
        // Hide the Channel hero immediately for non-fleet agents; loadSessions
        // re-shows + fills it for fleet agents from the home thread.
        if (!isOrch && primaryEl) primaryEl.style.display = 'none';
        closeNavMenus();
        if (!isOrch && orchView) orchView.style.display = 'none';
        clearTopbarNavSelection();
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
          if (!isAltNavAgent(window.GOHORT_AGENT_ID)) {
            // Not a channel agent: there is no session list to reload, which is
            // what this tick was written for. But an approval queue belongs to
            // the USER, not to whichever agent is on screen — that is what
            // all_agents means — so it goes stale while you sit on an agent
            // that does not poll, and the button that is supposed to light up
            // when a decision is waiting does not, until you navigate.
            //
            // Queues only, not every all_agents badge: see refreshChannelBadges.
            if (hasAllAgentNav) refreshChannelBadges(true, true);
            return;
          }
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
    // topSpan holds the two stacked rows (status, actions) on the left and any
    // full-height topbar control on the right. Without it a control appended
    // into the action row can only ever be one row tall; a user-scoped queue
    // reads better spanning both, where it is plainly not part of either row.
    var topRows = el('div', {style: 'display:flex;flex-direction:column;flex:1;min-width:0'});
    var topSpan = el('div', {style: 'display:flex;align-items:stretch;gap:0.4rem'});
    topSpan.appendChild(topRows);
    topbar.appendChild(topSpan);
    var gridRow = el('div', {class: 'ui-agent-grid'});

    var main = el('div', {class: 'ui-agent-main'});
    main.style.position = 'relative';
    // Orchestrator table view — overlays the chat pane when a non-chat nav
    // item (Enabled agents / Authorizations) is selected; hidden for the Chat
    // view and for every normal agent.
    orchView = el('div', {class: 'ui-orch-view', style: 'display:none;position:absolute;inset:0;overflow:auto;background:var(--bg-1, #1b1b2b);padding:0.85rem;z-index:5'});
    main.appendChild(orchView);
    if (drawer && !listPosModal) main.appendChild(drawer.mobileHdr);

    // Floating expand-tab shown when the left rail is collapsed on
    // desktop. Sits pinned against the conversation pane's left edge
    // so the user can always pull the list back. Hamburger icon for
    // symmetry with the in-rail collapse button.
    //
    // Not built for a modal-position panel. That panel is permanently
    // side-collapsed (there is no rail column), and .side-collapsed shows this
    // tab — so it rendered as a second collapsed rail pinned beside whatever
    // the page already had, and clicking it pulled the session list INTO the
    // grid, which is the exact layout list_position:"modal" exists to avoid.
    // The Sessions button in the action bar is this panel's affordance.
    var expandTab = null;
    if (hasList && !listPosModal) {
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
    // sideForced: this panel's rail state is decided by its LAYOUT, not by the
    // user — a modal-position panel is always collapsed, a top-position one
    // always open. Such a state must not be written to the shared preference
    // key: it is one key for every agent-loop panel on the origin, so opening
    // a modal panel taught every other panel to start collapsed (and a
    // top-position one taught them all to start open). A layout fact is not a
    // preference and has no business outliving the panel that has it.
    var sideForced = false;
    function applySideCollapse() {
      if (!hasList) return;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      if (sideForced) return;
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
    topRows.appendChild(statusBar);

    // ListPosition: "top" — chat-app layout. Sessions rail stays
    // permanently visible on the left; the chat-pane gets its own
    // topbar (assembled below into main) that holds the action
    // buttons only. No ☰ toggle, no "+ New session" duplicate — the
    // sidebar header already carries New, and the rail isn't meant
    // to collapse in this mode.
    // "modal" — the list is a BUTTON, not a rail. Nothing is given up to a
    // column that is usually empty: the toolbar carries one control, it opens
    // the same list the rail would have shown, and picking a session cuts
    // straight to it and closes. For a panel that is already the narrowest
    // column on its page (a workbench chat), a collapsed rail is the worst of
    // both — it still costs the hamburger, the expand tab, and a mental model,
    // to reach a list you wanted for two seconds.
    var sessionModal = null;
    function openSessionPicker() {
      if (sessionModal) return;
      loadSessions(); // the rail may have been built before the first load
      sessionModal = window.uiOpenSimpleModal({
        title: cfg.list_title || 'Sessions',
        width: '520px',
        mount: function(body) {
          body.classList.add('ui-agent-session-picker');
          body.appendChild(side); // the rail itself — search, unread, rename, delete
        },
      });
      var closed = sessionModal.close;
      sessionModal.close = function() { closed(); sessionModal = null; };
    }
    function closeSessionPicker() {
      if (sessionModal) sessionModal.close();
    }

    if (listPosModal) {
      sideCollapsed = true; // the grid has no rail column to hold open
      sideForced = true;
      applySideCollapse();
    }

    var listPosTop = hasList && cfg.list_position === 'top';
    if (listPosTop) {
      wrap.classList.add('ui-agent-list-top');
      // Force rail expanded, ignore any stored collapse preference.
      sideCollapsed = false;
      sideForced = true;
      applySideCollapse();
    }

    var actionsBar = el('div', {class: 'ui-agent-actions'});
    // Per-session status pill — a one-word state the app wants beside the
    // composer (a workflow phase, a connection state, a review stage). The
    // panel knows only the shape: {label, title, tone}, empty label = render
    // nothing, so a session with nothing to report stays quiet.
    var statusPill = null;
    var statusLast = null; // the last status payload, for the click-through
    function refreshStatusPill() {
      if (!cfg.status_url || !statusPill) return;
      var sid = activeSessionId || '';
      if (!sid) { statusPill.style.display = 'none'; return; }
      var url = substituteExtras(cfg.status_url).replace('{session}', encodeURIComponent(sid));
      fetchJSON(url).then(function(s) {
        var label = (s && s.label) || '';
        if (!label) { statusPill.style.display = 'none'; statusLast = null; return; }
        statusLast = s;
        statusPill.textContent = label;
        statusPill.title = (s && s.title) || '';
        statusPill.style.color = (s && s.tone === 'active') ? 'var(--accent)' : 'var(--text-mute)';
        // A status with a detail view behind it reads as a control.
        var openable = !!(s && s.detail_url);
        statusPill.style.cursor = openable ? 'pointer' : '';
        statusPill.style.textDecoration = openable ? 'underline dotted' : '';
        statusPill.style.display = '';
      }).catch(function() {
        // A status readout is decoration. It never gets to interrupt a
        // conversation with an error toast.
        statusPill.style.display = 'none';
        statusLast = null;
      });
    }
    // openStatusDetail is the click-through: the status payload's detail_url
    // rendered generically, plus its actions as buttons. The panel knows the
    // shape and nothing about what a phase, a stage or a connection is.
    function openStatusDetail(s) {
      if (!s || !s.detail_url) return;
      fetchJSON(s.detail_url).then(function(data) {
        var handle = window.uiOpenModal({
          title: s.label || 'Status',
          subtitle: s.title || '',
          width: 'min(720px, 94vw)',
          mount: function(body) {
            var empty = data == null || (typeof data === 'object' && !Object.keys(data).length);
            if (empty) {
              body.appendChild(el('div', {style: 'color:var(--text-mute, #999);font-size:0.85rem'}, ['Nothing to show.']));
            } else {
              renderDetailValue(body, data, 0);
            }
            var actions = (s.actions || []);
            if (!actions.length) return;
            var bar = el('div', {style: 'display:flex;flex-wrap:wrap;gap:0.5rem;align-items:center;margin-top:0.9rem;padding-top:0.6rem;border-top:1px solid var(--border, rgba(127,127,127,0.25))'});
            var status = el('span', {style: 'color:var(--text-mute, #999);font-size:0.78rem'});
            actions.forEach(function(a) {
              var group = el('span', {style: 'display:inline-flex;gap:0.35rem;align-items:center'});
              var select = null;
              if (a.options_url) {
                select = el('select', {style: 'font:inherit;font-size:0.8rem;padding:0.15rem 0.3rem;border:1px solid var(--border, rgba(127,127,127,0.35));border-radius:4px;background:var(--bg-1);color:var(--text)'});
                fetchJSON(a.options_url).then(function(opts) {
                  (opts || []).forEach(function(o) {
                    var opt = el('option', {value: String(o.value)}, [String(o.label || o.value)]);
                    select.appendChild(opt);
                  });
                }).catch(function() {});
                group.appendChild(select);
              }
              var btn = el('button', {type: 'button', class: 'ui-row-btn compact' + (a.variant ? ' ' + a.variant : ''), onclick: async function() {
                if (a.confirm && window.uiConfirm && !(await window.uiConfirm(a.confirm))) return;
                var url = a.url;
                if (select) url += (url.indexOf('?') >= 0 ? '&' : '?') + 'value=' + encodeURIComponent(select.value || '');
                status.textContent = 'Working…';
                fetch(url, {method: a.method || 'POST'}).then(function(r) {
                  if (!r.ok) { return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); }); }
                  status.textContent = '';
                  if (handle && handle.close) handle.close();
                  refreshStatusPill();
                }).catch(function(err) { status.textContent = String(err.message || err); });
              }}, [a.label]);
              group.appendChild(btn);
              bar.appendChild(group);
            });
            bar.appendChild(status);
            body.appendChild(bar);
          }
        });
      }).catch(function() {});
    }
    if (cfg.status_url) {
      statusPill = el('span', {class: 'ui-status-pill', style: 'display:none;font-size:0.72rem;padding:0.15rem 0.5rem;border:1px solid var(--border);border-radius:999px;white-space:nowrap;align-self:center',
        onclick: function() { openStatusDetail(statusLast); }});
      actionsBar.appendChild(statusPill);
    }
    // Session diagnostics — the framework's decisions made on the user's
    // behalf in this conversation (suppressed replies, discarded inputs,
    // retries), which otherwise vanish into server logs. Generic: the app
    // supplies DiagnosticsURL; entries are [{at, kind, detail}].
    if (cfg.diagnostics_url) {
      var diagBtn = el('button', {class: 'ui-row-btn', type: 'button',
        title: 'Session diagnostics what the framework suppressed, discarded, or retried in this conversation'}, ['⚠']);
      diagBtn.addEventListener('click', function() {
        var sid = activeSessionId || '';
        if (!sid) { showToast('No active session yet.'); return; }
        var url = substituteExtras(cfg.diagnostics_url).replace('{session}', encodeURIComponent(sid));
        fetchJSON(url).then(function(list) {
          if (!Array.isArray(list)) list = [];
          // Copy the trail as plain text so it can be forwarded to whoever
          // is debugging the framework. The entries are timestamps, kind
          // slugs, and framework-authored sentences — no tool arguments —
          // so there is nothing here to redact. Text is already in hand
          // (fetched above), so this writes synchronously inside the click
          // and keeps the user gesture intact; the promise-valued
          // ClipboardItem dance elsewhere in this file is only needed when
          // a fetch sits between the click and the write.
          function diagAsText() {
            var lines = ['Session diagnostics: ' + list.length + ' entr' + (list.length === 1 ? 'y' : 'ies')];
            list.forEach(function(e) {
              var when = '';
              try { when = e.at ? new Date(e.at).toISOString() : ''; } catch (_) {}
              lines.push('', when + (e.kind ? ' - ' + e.kind : ''), e.detail || '');
            });
            return lines.join('\n');
          }
          function copyDiag(btn) {
            var text = diagAsText();
            var was = btn.textContent;
            function done() { btn.textContent = 'Copied'; setTimeout(function() { btn.textContent = was; }, 1500); }
            function fail() { showToast('Clipboard unavailable: select the text and copy manually.'); }
            if (navigator.clipboard && navigator.clipboard.writeText) {
              navigator.clipboard.writeText(text).then(done).catch(function() { hostCopy(text, done, fail); });
              return;
            }
            hostCopy(text, done, fail);
          }
          function hostCopy(text, done, fail) {
            if (typeof window.__uiClipboardImpl === 'function') {
              window.__uiClipboardImpl(text).then(done).catch(function() { legacyCopy(text, done, fail); });
              return;
            }
            legacyCopy(text, done, fail);
          }
          function legacyCopy(text, done, fail) {
            var ta = document.createElement('textarea');
            ta.value = text;
            ta.style.position = 'fixed'; ta.style.opacity = '0';
            document.body.appendChild(ta);
            ta.select();
            var ok = false;
            try { ok = document.execCommand('copy'); } catch (_) {}
            document.body.removeChild(ta);
            if (ok) { done(); } else { fail(); }
          }
          var modalActions = [{label: 'Close', primary: true}];
          if (list.length) {
            // onClick means the modal stays open — copying shouldn't dismiss
            // the thing you're reading.
            modalActions.unshift({label: 'Copy', onClick: function(api, btn) { copyDiag(btn); }});
          }
          window.uiOpenModal({
            title: 'Session diagnostics',
            subtitle: 'Framework decisions in this conversation: content suppressed, discarded, or retried on your behalf. Newest first.',
            width: 'min(640px, 94vw)',
            actions: modalActions,
            mount: function(body) {
              if (!list.length) {
                body.appendChild(el('div', {style: 'color:var(--text-mute);font-size:0.85rem'},
                  ['Nothing to report: no guard has intervened in this session.']));
                return;
              }
              list.forEach(function(e) {
                var row = el('div', {style: 'padding:0.5rem 0;border-bottom:1px solid var(--border);font-size:0.82rem;line-height:1.45'});
                var when = '';
                try { when = e.at ? new Date(e.at).toLocaleString() : ''; } catch (_) {}
                row.appendChild(el('div', {style: 'color:var(--text-mute);font-size:0.72rem;margin-bottom:0.15rem'},
                  [when + (e.kind ? ' - ' + e.kind : '')]));
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
      // A guided action SAYS something rather than calling something: the text
      // goes into the composer and is sent as the user's turn, so the button
      // and a person typing the same sentence produce the identical
      // conversation. Nothing app-specific reaches this code — the app owns
      // every word. Declared prompt wins over any method: an action carrying
      // one has already said what it does.
      if (action.prompt) {
        // A half-typed draft is not the framework's to throw away: sendMessage
        // reads the composer, so the prompt has to pass through it, and
        // whatever was in there comes back afterwards.
        var draft = inputArea.value;
        inputArea.value = action.prompt;
        sendMessage();
        if (inputArea.value === action.prompt) inputArea.value = draft; // refused to send
        else if (draft.trim()) inputArea.value = draft;
        return;
      }
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
              msgEls = {}; blockEls = {}; noticeIds = {};
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
    // The picker's own control, ahead of the app's actions: it is navigation,
    // not one more thing to do to the open session.
    if (listPosModal) {
      actionsBar.appendChild(el('button', {
        class: 'ui-row-btn', title: 'Open a past session',
        onclick: function(){ openSessionPicker(); },
      }, [cfg.list_title || 'Sessions']));
      // Starting a fresh conversation is not "a past session", and it was only
      // reachable THROUGH the list of them: open the picker, find New, click,
      // and the dialog you opened to browse closes again without your having
      // browsed anything. Two clicks and a detour for the commoner of the two
      // things this control is used for.
      //
      // Only in modal mode. A panel with a rail already has its New button in
      // the rail header, and a second one in the toolbar would be two controls
      // doing one job.
      actionsBar.appendChild(el('button', {
        class: 'ui-row-btn', title: 'Start a new session',
        onclick: function(){ closeSessionPicker(); openSession(null); },
      }, [cfg.new_label || 'New']));
    }
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
    // Drop the nav dropdowns into the topbar actions (built earlier in the
    // rail block, where the nav machinery was in scope), in declaration order.
    // They travel with actionsBar to wherever the layout places it.
    navMenus.forEach(function(m) { actionsBar.appendChild(m.control); });
    // Show the bar if anything is IN it, rather than asking whether the app
    // declared actions. Several things land here that cfg.actions knows nothing
    // about — the Sessions button a list_position:"modal" panel needs, the nav
    // dropdowns — so counting declarations hides a bar with controls in it.
    //
    // That was not theoretical: the old rule hid the bar whenever cfg.actions
    // was empty, and what kept it visible was an unconditional force-show from a
    // control that happened to always exist. The moment that control became
    // conditional, a panel whose only topbar control was Sessions lost its way
    // back to past conversations, and nothing about the change said it would.
    actionsBar.style.display = actionsBar.childNodes.length ? '' : 'none';
    // Topbar nav controls hang off the SPAN, not the action row, so they run
    // the full height of both rows and sit at the far right — visibly a
    // different kind of thing from the app's own per-agent actions.
    if (navTopbarEl) topSpan.appendChild(navTopbarEl);
    topRows.appendChild(actionsBar);
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
          ['(terminal pane: xterm.js wiring deferred)'])]);
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
    // like "[Pasted text #2 - 47 lines / 1834 chars]" at the cursor
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
    // makePasteMarker stashes a block of text and returns the placeholder that
    // stands for it in the composer. sendMessage expands these back by an exact
    // regex before the send, so the format lives in ONE place: a second hand-
    // built marker elsewhere would read as literal text and silently deliver the
    // placeholder instead of the content.
    function makePasteMarker(text) {
      var n = ++pasteCounter;
      pasteMap[n] = text;
      return '[Pasted text #' + n + ' - ' + text.split('\n').length + ' lines / ' + text.length + ' chars]';
    }
    // What sendMessage matches to expand a marker back.
    //
    // It depends on the STABLE parts only — the opening, the number, the
    // closing bracket — and skips whatever the middle says. It used to spell
    // the middle out, separator and units and all, and when the separator
    // changed from an em dash to a middle dot the pattern stopped matching:
    // every paste then delivered the PLACEHOLDER to the model instead of the
    // text, silently, because a marker with no stored entry is deliberately
    // left as literal text.
    //
    // The comment above makePasteMarker already said the format must live in
    // one place. It did; the reader of it was a second copy in a regex.
    var pasteMarkerRE = /\[Pasted text #(\d+)[^\]]*\]/g;
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
      var marker = makePasteMarker(text);
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

    // uiComposeMessage puts text in the composer and hands the user the cursor.
    //
    // The seam behind a button that is really a macro. A toolbar control that
    // POSTs a fixed prompt and spins is a prompt the user cannot read, cannot
    // adjust, and cannot learn from — it does one thing and never says what.
    // Seeding the composer instead keeps the one-click path (the default text
    // is already there, Enter sends it) and opens the one it never had: edit
    // it first. It also teaches, because the user SEES what the button was
    // going to say.
    //
    // opts.send dispatches it immediately. Left to the CALLER because the two
    // cases are genuinely different: a control that already asked the user what
    // they wanted has no reason to make them confirm the same intent twice,
    // while one that seeded a default unprompted should let them look at it
    // first. Seeding without sending is the default, so a caller has to mean it.
    //
    // opts.append keeps what is already typed and adds to it, for a second
    // control pressed on top of a half-written message. Default replaces,
    // because the usual case is an empty composer and a fresh intent.
    //
    // opts.body is a BLOCK the message carries but the reader does not need to
    // scroll through — an audit's findings, a log, a diff. Over the paste
    // threshold it becomes the same "[Pasted text #N …]" placeholder a large
    // paste gets: the composer stays a readable instruction with one line
    // standing for the payload, and sendMessage expands it before the send.
    // Without this, handing a control its own findings meant either dropping
    // them (and asking the agent to re-derive what was already computed) or
    // filling the composer with three thousand words nobody can edit around.
    window.uiComposeMessage = function(text, opts) {
      text = (text == null) ? '' : String(text);
      opts = opts || {};
      var body = (opts.body == null) ? '' : String(opts.body);
      if (body.trim()) {
        // Short enough to read stays visible: a placeholder over four lines of
        // text hides something the author would rather just see.
        text += (text ? '\n\n' : '') +
          (body.length >= pasteSnippetThreshold ? makePasteMarker(body) : body);
      }
      if (!text) return false;
      if (opts.append && inputArea.value.trim()) {
        inputArea.value = inputArea.value.replace(/\s*$/, '') + '\n\n' + text;
      } else {
        inputArea.value = text;
      }
      // The composer is the one thing on the page that must not be off-screen
      // when it fills: an action fired from a toolbar three columns away leaves
      // no other sign that anything happened.
      try { inputArea.scrollIntoView({block: 'nearest'}); } catch (_) {}
      // Autosize and anything else listening, the same way a paste does.
      inputArea.dispatchEvent(new Event('input'));
      inputArea.focus();
      // Cursor at the END, not selecting the text. A selection means the next
      // keystroke destroys what was seeded, which is exactly wrong for a
      // default the user is meant to amend.
      var end = inputArea.value.length;
      try { inputArea.setSelectionRange(end, end); } catch (_) {}
      // After the cursor work, so a send that fails visibly leaves the composer
      // in the state the user would want to retry from.
      if (opts.send) sendMessage();
      return true;
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

    // uiRenderMessageImage paints an image under a message bubble the same
    // way a live tool-delivered one is painted — same attachment box, same
    // sizing, same click-to-zoom. Takes ANY src (a data: URL for inline
    // bytes, an http one for something the app serves), so a replay hook
    // rebuilding a stored message doesn't have to reimplement the look and
    // then drift from it.
    window.uiRenderMessageImage = function(bubble, src, alt) {
      if (!bubble || !src) return null;
      var img = el('img', {src: src, class: 'ui-agent-msg-image', alt: alt || 'image'});
      img.addEventListener('click', function() { openImageLightbox(src); });
      agentMsgAttachmentBox(bubble).appendChild(img);
      return img;
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
      // The field's NAME is its DOM id, which ChatField documents and apps
      // rely on: a client action (a toolbar button, a block renderer) has to
      // be able to read the value the next message will carry, and the
      // extraInputs map above is closure-local. Without the id those actions
      // silently read nothing — which does not look like a bug at the point
      // it happens, it looks like an empty parameter arriving at a handler.
      if (f.name) input.id = f.name;
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
              btn.title = m.label + ' is force-enabled by the operator: can\'t be toggled off.';
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

    // panelScope reads the nearest enclosing [data-ui-scope] — a host component
    // saying which of its records this panel is currently for. Empty when the
    // panel stands alone, or when the host has nothing selected.
    function panelScope() {
      try {
        var host = wrap && wrap.closest && wrap.closest('[data-ui-scope]');
        return (host && host.getAttribute('data-ui-scope')) || '';
      } catch (e) { return ''; }
    }
    function substituteExtras(url) {
      if (!url) return url;
      Object.keys(extraInputs).forEach(function(k) {
        var v = extraInputs[k].value || '';
        url = url.replace('{' + k + '}', encodeURIComponent(v));
      });
      // {scope} asks the question about the host's OPEN record, read at request
      // time rather than latched on the server when it was opened. A server-side
      // "current" is one slot per user: a second tab overwrites it, and a fetch
      // that lands out of order scopes the answer to the previous record — which
      // looks like a list that is sometimes right and sometimes empty.
      if (url.indexOf('{scope}') >= 0) {
        url = url.split('{scope}').join(encodeURIComponent(panelScope()));
      }
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
      // A topbar control that must span BOTH rows: in this layout the two
      // rows are table rows, so a rowspan cell is the honest way to do it —
      // and, more importantly, this layout DROPS the topbar element entirely
      // (see the assembly below), so a control parented there vanishes. It
      // relocates here by appendChild; nothing to detach first.
      if (navTopbarEl) {
        var navTd = el('td', {rowspan: '2', style: 'vertical-align:middle;text-align:right;width:1%;padding-left:0.4rem'});
        navTd.appendChild(navTopbarEl);
        row1Tr.appendChild(navTd);
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
    // settleConvoScroll pins a freshly-rendered thread to its bottom and KEEPS
    // it there while the content finishes settling.
    //
    // One scrollTop assignment is not enough at the end of a replay, because the
    // pane is still growing after it. Blocks and cards are appended after the
    // message loop; markdown mounts a frame later; images and code blocks arrive
    // with no height and get some once decoded; the tool toggle is attached
    // after its bubble. Every one of those adds height BELOW a scroll position
    // that was correct when it was set, which is how a session opens parked a
    // few hundred pixels short of the end with no way to tell that anything went
    // wrong. Reopening the thread then "fixes" it, because the second render
    // measures content the browser has already laid out and cached — which is
    // exactly the shape of a bug that only happens sometimes.
    //
    // So: scroll now, again next frame, again when the layout settles, and again
    // as each image finishes decoding. Plain stick-to-bottom throughout — see
    // the reverted read-from-top experiment; this makes the existing behavior
    // reliable rather than changing it.
    //
    // Bails the moment the user scrolls away, so a slow-loading image cannot
    // yank someone back down after they have started reading.
    function settleConvoScroll() {
      convoStickToBottom = true;
      scrollConvo(true);
      var again = function() {
        if (!convoStickToBottom) return;
        convoLog.scrollTop = convoLog.scrollHeight;
      };
      if (typeof requestAnimationFrame === 'function') requestAnimationFrame(again);
      setTimeout(again, 60);
      setTimeout(again, 250);
      // Images report their real height only once decoded. decode() where it
      // exists, load/error otherwise — error matters too: a broken image still
      // resolves to a height, and skipping it would leave the pane short.
      var imgs = convoLog.querySelectorAll('img');
      for (var i = 0; i < imgs.length; i++) {
        (function(img) {
          if (img.complete) return;
          img.addEventListener('load', again, {once: true});
          img.addEventListener('error', again, {once: true});
        })(imgs[i]);
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

    // keepPendingInterjectionsLast holds a queued note at the BOTTOM of the
    // conversation until the agent actually picks it up.
    //
    // A note written mid-turn has not been delivered yet — the runner drains
    // the queue between rounds, so whatever the agent is writing when you press
    // send was decided without it. Letting that output land underneath the note
    // says the opposite: it reads as a reply to something the agent had not yet
    // read. Keeping the note last says what is true — it is waiting — and the
    // moment it IS delivered the app marks it .consumed, this stops moving it,
    // and everything the agent says next appears below it, which is then
    // exactly right.
    //
    // A note marked undelivered is settled too, and stops moving for the same
    // reason: "waiting" is over either way. Without excluding it, a cancelled
    // note would go on being dragged to the bottom of every later turn, which
    // says it is still pending — the one thing it is now known not to be.
    //
    // querySelectorAll returns a static list, so re-appending while iterating
    // is safe and keeps several pending notes in the order they were written.
    function keepPendingInterjectionsLast() {
      if (!convoLog) return;
      var pending = convoLog.querySelectorAll(
        '.ui-agent-interjection:not(.consumed):not(.ui-agent-interjection-undelivered)');
      for (var i = 0; i < pending.length; i++) convoLog.appendChild(pending[i]);
    }

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
      keepPendingInterjectionsLast();
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
    // renderMessageMark draws a per-message MARK: one glyph at the head of the
    // message, a tooltip, and an optional client action on click.
    //
    // Generic on purpose. The panel knows a message can carry a mark; what a
    // mark MEANS is the app's, supplied as {glyph, title, action} and wired to
    // the client-action registry. Idempotent, because a message can be marked
    // by replay and again by a live event.
    function renderMessageMark(bubble, mark) {
      if (!bubble || !mark || !mark.glyph) return;
      var body = bubble.querySelector('.ui-agent-msg-body') || bubble;
      if (body.querySelector('.ui-msg-mark')) return;
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'ui-msg-mark';
      b.textContent = mark.glyph;
      // Styled here rather than in the sheet: it is one control, and inlining
      // keeps a panel-level primitive from needing an app to ship CSS for it.
      b.style.cssText = 'display:inline-block;width:17px;height:17px;padding:0;' +
        'margin-right:.4rem;line-height:15px;border-radius:50%;' +
        'border:1px solid var(--warning,#b45309);background:transparent;' +
        'color:var(--warning,#b45309);font-size:11px;font-weight:700;' +
        'cursor:pointer;vertical-align:baseline;flex:none';
      if (mark.title) { b.title = mark.title; b.setAttribute('aria-label', mark.title); }
      if (mark.action) {
        b.addEventListener('click', function() {
          var fn = window.UIClientActions && window.UIClientActions[mark.action];
          if (typeof fn === 'function') {
            fn({button: b, sessionId: activeSessionId, data: mark.data || {}});
          }
        });
      } else {
        b.disabled = true;
      }
      // INSIDE the first block, not above it. A markdown pass wraps the reply
      // in a <p>, so inserting at the body's head puts the glyph on its own
      // line above the text; inserting at the paragraph's head puts it where
      // it belongs, on the first line, before the first word.
      var host = body.firstElementChild;
      if (host && /^(P|LI|DIV|H[1-6]|BLOCKQUOTE)$/.test(host.tagName)) {
        host.insertBefore(b, host.firstChild);
      } else {
        // Streaming or non-markdown: the body holds text directly, so its own
        // head IS the first line.
        body.insertBefore(b, body.firstChild);
      }
    }

    // markLastAssistant applies a mark to the most recent assistant bubble.
    // The live route: a turn is marked when it ENDS, so there is no id to
    // address and the last reply is the one it is about.
    function markLastAssistant(mark) {
      var all = convoLog.querySelectorAll('.ui-agent-msg-assistant');
      if (!all.length) return;
      renderMessageMark(all[all.length - 1], mark);
    }

    // strikeMessage keeps a message on screen but marks it as taken back: the
    // text is struck through and muted, with the reason on its own line above
    // it. Generic: the panel knows a message can be struck with a reason; the
    // reason is the server's words. The reason sits OUTSIDE the body so the
    // markdown pass (which rewrites the body) cannot wipe it, and so a copy of
    // the card text is still just the text. Idempotent: live, then replay.
    function strikeMessage(id, reason) {
      var m = msgEls[id];
      if (!m || !m.bubble) return;
      m.bubble.classList.add('ui-agent-msg-struck');
      m.struck = reason || '';
      var line = m.bubble.querySelector(':scope > .ui-agent-msg-struck-reason');
      if (!reason) { if (line) line.remove(); return; }
      if (!line) {
        line = el('div', {class: 'ui-agent-msg-struck-reason'});
        m.bubble.insertBefore(line, m.body);
      }
      line.textContent = reason;
    }

    // labelMessage puts a small label chip at the head of a message (e.g. a
    // follow-up the server wants read as a correction). What the label says
    // is the server's; the panel only shows it. Idempotent.
    function labelMessage(id, label) {
      var m = msgEls[id];
      if (!m || !m.bubble) return;
      m.label = label || '';
      var chip = m.bubble.querySelector(':scope > .ui-agent-msg-label');
      if (!label) { if (chip) chip.remove(); return; }
      if (!chip) {
        chip = el('div', {class: 'ui-agent-msg-label'});
        m.bubble.insertBefore(chip, m.body);
      }
      chip.textContent = label;
    }

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
      // A server-posted card's provenance rides on the entry so the export
      // can say what the card IS. It used to be read there and never written
      // here, so every such card, whatever its kind, exported as
      // "Scheduled: automated fire".
      if (meta.report_from !== undefined) {
        m.report_from = meta.report_from || '';
        m.report_kind = meta.report_kind || '';
        m.report_detail = meta.report_detail || '';
      }
      // The replay route for a mark, so one that was applied live is still
      // there when the thread is reopened.
      if (meta.mark) renderMessageMark(m.bubble, meta.mark);
      // The replay route for a struck or labelled message, so what was shown
      // live is still shown when the thread is reopened.
      if (meta.retracted) strikeMessage(id, meta.retracted);
      if (meta.label) labelMessage(id, meta.label);
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
      // Two copies, because they are wanted for different things. Copy is the
      // one you reach for constantly — take this answer somewhere else — and it
      // takes the text and nothing else. Copy turn is the one you reach for when
      // something went wrong and you need to SHOW what happened: the question,
      // every round of the reply, and each tool with its args and result.
      var copyBtn = el('button', {
        class: 'ui-agent-msg-act',
        title: 'Copy this reply\u2019s text',
        onclick: function(){ copyAssistantMessage(bubble, copyBtn); },
      }, ['Copy']);
      bar.appendChild(copyBtn);
      var copyTurnBtn = el('button', {
        class: 'ui-agent-msg-act',
        title: 'Copy the whole exchange \u2014 the request, every reply, and the tool calls with their args and results',
        onclick: function(){ copySubSession(bubble, copyTurnBtn); },
      }, ['Copy turn']);
      bar.appendChild(copyTurnBtn);
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

    // writeClipboard is the one clipboard path both copy buttons use: the
    // Clipboard API where it is permitted, a hidden textarea + execCommand
    // where it is not (a non-secure origin, a browser that withholds the
    // permission), and a brief "Copied" flash on the button either way so the
    // action is visibly acknowledged.
    function writeClipboard(text, btn) {
      // Every copy in this panel funnels through here, so the strip lives here
      // rather than at each call site: what the user sees is stripped at
      // render, and the clipboard has to agree or Copy hands back the markers
      // the page just hid.
      text = window.uiStripMetaTags(String(text == null ? '' : text));
      var flash = function() {
        if (!btn) return;
        var prior = btn.textContent;
        btn.textContent = 'Copied';
        setTimeout(function(){ btn.textContent = prior; }, 900);
      };
      function fallback() {
        var ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed'; ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); flash(); } catch (_) {}
        document.body.removeChild(ta);
      }
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(flash).catch(fallback);
        return;
      }
      fallback();
    }

    // copyAssistantMessage copies the bubble's TEXT and nothing else — what
    // the Copy button on a reply does. Deliberately not the transcript: the
    // common reason to copy an answer is to paste the answer, and a paste that
    // arrives wrapped in headings and tool dumps has to be edited back down by
    // hand. The transcript is Copy turn, one button over.
    function copyAssistantMessage(bubble, btn) {
      var body = bubble.querySelector(':scope > .ui-agent-msg-body');
      if (!body) return;
      writeClipboard(body.innerText || body.textContent || '', btn);
    }

    // copySubSession copies ONE exchange as a short, self-contained transcript —
    // the request, the reply(-ies), and their tool calls with args + results.
    // It's "Copy session" scoped to a single turn: the sub-session you paste to
    // show what happened in one fire or one answer without dumping the whole
    // thread. Wired to the per-message "Copy turn" button — plain Copy takes
    // the reply's text alone, which is what that button used to do and what it
    // does again.
    //
    // Starts by walking back to the user bubble that opened the turn. A REPORT
    // CARD (a scheduled fire) has no preceding user bubble — the request is the
    // card's own report label/brief — so when none is found we start from the
    // card itself and use its report heading as the request rather than bailing.
    // reportRequestLine names a server-posted card's origin for the export:
    // the kind the server stamped, then its label ("who, via what"). The
    // kinds are the app's vocabulary and are shown as sent, not translated
    // here; a card with no kind is the original case, a scheduled fire.
    function reportRequestLine(kind, label) {
      if (!kind) return 'Scheduled: ' + (label || 'automated fire');
      return kind.charAt(0).toUpperCase() + kind.slice(1) + ': ' + (label || 'unlabelled');
    }

    function copySubSession(bubble, btn) {
      var lines = [];
      var startBubble = bubble;
      var userBubble = bubble;
      var splitCard = null; // a card whose body carries "↳" action lines: they are the round
      while (userBubble && !userBubble.classList.contains('ui-agent-msg-user')) {
        userBubble = userBubble.previousElementSibling;
      }
      if (userBubble) {
        var userEntry = msgEntryForBubble(userBubble);
        var userText = (userEntry && userEntry.rawText) ||
          (userBubble.querySelector(':scope > .ui-agent-msg-body') &&
            userBubble.querySelector(':scope > .ui-agent-msg-body').innerText) || '';
        lines.push('## Request', '', userText.trim(), '');
        startBubble = userBubble;
      } else {
        // Server-posted card: the request is what the card IS — its kind and
        // label as the server stamped them.
        var entry = msgEntryForBubble(bubble);
        var label = (entry && (entry.report_from || entry.reportFrom)) || '';
        var detail = (entry && (entry.report_detail || entry.reportDetail)) || '';
        var kind = (entry && (entry.report_kind || entry.reportKind)) || '';
        var head = reportRequestLine(kind, label);
        // A card whose body ends in "↳ …" lines records what came in and,
        // under it, what the agent did about it. What came in is the request;
        // the "↳" lines are the round. A card with no such lines is a reply
        // in its own right and stays the round it always was.
        var cardBody = bubble.querySelector(':scope > .ui-agent-msg-body');
        var cardText = (entry && entry.rawText) || (cardBody ? (cardBody.innerText || cardBody.textContent || '') : '');
        var cut = cardText.indexOf('\n↳ ');
        if (cut >= 0) {
          splitCard = {bubble: bubble, tail: cardText.slice(cut + 1).trim()};
          var said = cardText.slice(0, cut).trim();
          if (said) head += ':\n\n' + said;
        } else if (detail) {
          head += ' - ' + detail;
        }
        lines.push('## Request', '', head.trim(), '');
        startBubble = bubble.previousElementSibling || bubble; // include from just before this card
      }
      // Walk forward from the request through assistant bubbles
      // until the next user (or end). Each assistant bubble becomes
      // a round; tools attached to the bubble become subsections.
      var fence = '```';
      function emitTools(tools) {
        (tools || []).forEach(function(t) {
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
      // Tool chips do not always ride the assistant bubble. When that bubble is
      // still EMPTY as a tool_call event lands — the normal case for a tool
      // round — toolHostFor pins the chips to the previous VISIBLE block: a
      // plan card, an intent card, even the request bubble itself. Harvesting
      // only assistant bubbles therefore copied prose-and-no-tools for exactly
      // the turns where the tools were the point (observed: a tool_def create
      // that the copy showed no trace of). So: harvest EVERY element in the
      // walk, in DOM order, which is chronological order.
      if (userBubble && userBubble.tools && userBubble.tools.length) {
        emitTools(userBubble.tools);
      }
      var roundNum = 0;
      var next = (startBubble === bubble ? bubble : startBubble.nextElementSibling);
      while (next && !next.classList.contains('ui-agent-msg-user')) {
        if (next.classList.contains('ui-agent-msg-assistant')) {
          var body = next.querySelector(':scope > .ui-agent-msg-body');
          var txt = body ? (body.innerText || body.textContent || '').trim() : '';
          if (splitCard && next === splitCard.bubble) txt = splitCard.tail;
          var tools = next.tools || [];
          if (!txt && !tools.length) {
            // Empty bubble (opened mid-stream / settled without content) —
            // a bare "## Assistant (round N)" header adds noise to the paste.
            next = next.nextElementSibling;
            continue;
          }
          roundNum++;
          // A struck or labelled bubble says so in the copy too: pasted as
          // plain text, a struck reply reads as an answer that stood.
          var entryFor = msgEntryForBubble(next);
          var struck = next.classList.contains('ui-agent-msg-struck');
          var tag = struck ? ', struck through' : ((entryFor && entryFor.label) ? ', ' + entryFor.label : '');
          lines.push('## Assistant (round ' + roundNum + tag + ')');
          lines.push('');
          if (struck && entryFor && entryFor.struck) {
            lines.push('> ' + entryFor.struck);
            lines.push('');
          }
          if (txt) {
            lines.push(txt);
            lines.push('');
          }
          emitTools(tools);
        } else if (next.tools && next.tools.length) {
          // A non-bubble host (plan/intent card) carrying pinned tool chips.
          emitTools(next.tools);
        }
        next = next.nextElementSibling;
      }
      writeClipboard(lines.join('\n').replace(/\n{3,}/g, '\n\n').trim() + '\n', btn);
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
      // Prefer the index the SERVER gave this message. It is the true storage
      // position: it counts hidden messages, which are never rendered, and it
      // includes the offset of a tail load, which the DOM has no way to know.
      //
      // This is a truncate point. Counting rendered bubbles under a tail would
      // return a position within the window — so "delete from here down" would
      // cut from that position in the WHOLE thread instead, taking hundreds of
      // earlier messages with it. Silent, and not recoverable.
      for (var mid in msgEls) {
        var e = msgEls[mid];
        if (e && e.bubble === target && typeof e.storageIndex === 'number') {
          return e.storageIndex;
        }
      }
      // No stored index: a bubble created this session, with no reload since.
      // Counting is right relative to what is on screen, so what is missing
      // from the front has to be added back.
      var all = convoLog.querySelectorAll('.ui-agent-msg');
      var idx = loadedMsgOffset;
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
    // markEmptyBubble puts the class BACK when a bubble's text is
    // cleared after it had some. Only unmark existed, so the class was
    // a one-way door: a bubble that streamed text and was then blanked
    // (a guardrail retracting a blocked draft, the over-long lead-in
    // clear) kept the text removed and the CARD on screen — the empty
    // grey bubble that shows up whenever a check fires.
    //
    // A bubble hosting its own tool pills or attachments is left alone.
    // Those record work that really happened, they are pinned to this
    // element (toolHostFor / agentMsgAttachmentBox), and hiding the
    // bubble would take them with it.
    function markEmptyBubble(m) {
      if (!m || !m.bubble) return;
      if (m.bubble.querySelector(':scope > .ui-agent-tools-panel, ' +
                                 ':scope > .ui-agent-tools-toggle, ' +
                                 ':scope > .ui-agent-msg-attachments')) return;
      m.bubble.classList.add('ui-agent-msg-empty');
    }
    function appendChunk(id, text) {
      var m = msgEls[id];
      if (!m) { m = addMessage('assistant', id, ''); }
      m.rawText = (m.rawText || '') + text;
      // Streaming text stays plain — markdown pass on message_done. It still
      // goes through uiStripMetaTags: the markdown pass is where the strip
      // used to happen, so an internal note was on screen in plain text for
      // the whole stream and only vanished when the turn settled. rawText
      // keeps the original for that later pass.
      m.body.textContent = window.uiStripMetaTags(m.rawText);
      if (m.rawText.length > 0) unmarkEmptyBubble(m);
      scrollConvo(false);
    }

    function replaceChunk(id, text) {
      var m = msgEls[id];
      if (!m) { m = addMessage('assistant', id, ''); }
      m.rawText = text || '';
      m.body.textContent = window.uiStripMetaTags(m.rawText);
      if (m.rawText.length > 0) unmarkEmptyBubble(m);
      else markEmptyBubble(m);
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
      // Marking, not just skipping the unmark: a round that streamed
      // text and had it cleared before settling has already lost the
      // class, and "keep hidden" has to be able to re-hide it.
      if ((m.rawText || '').length > 0) unmarkEmptyBubble(m);
      else markEmptyBubble(m);
      // Streaming-mode pre-wrap is no longer needed once mdToHTML
      // emits structured block elements (p / pre / lists handle
      // their own whitespace). Leaving it on would add weird gaps
      // between block tags.
      //
      // Only when markdown ACTUALLY ran, though: an app with
      // cfg.markdown off keeps raw textContent in the body forever, so
      // dropping the class there collapsed every newline the moment the
      // turn finished — the reply looked right while streaming and then
      // reflowed into one paragraph. Same class of bug as the user
      // bubble's missing pre-wrap.
      if (m.bubble && cfg.markdown && m.role === 'assistant') {
        m.bubble.classList.remove('ui-agent-msg-streaming');
      }
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

    // renderMessageStats appends a small footer ("12.3 tk/s - 230 out
    // - 1450 in - 187 think - 18.7s") to an assistant bubble when the
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
      var footer = el('div', {class: 'ui-agent-stats'}, [parts.join(' - ')]);
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

    // buildNotice renders one framework breadcrumb as a conversation card —
    // the record of something a guard STOPPED, placed where the stopping
    // happened rather than only behind the ⚠ trail button. Generic: the
    // server supplies a level, a kind slug and a sentence; nothing here knows
    // what any particular guard is.
    //
    // textContent, never markdown, for the same reason the failure bubble uses
    // it: the detail quotes whatever tripped the guard, and a blocked request
    // must not get to style the notice that says it was blocked.
    function buildNotice(ev) {
      var level = ev.level || 'blocked';
      var card = el('div', {class: 'ui-agent-notice ui-agent-notice-' + level});
      var head = el('div', {class: 'ui-agent-notice-head'});
      head.appendChild(el('span', {class: 'ui-agent-notice-mark'}, ['\u26a0']));
      head.appendChild(el('span', {class: 'ui-agent-notice-label'}, [level === 'blocked' ? 'Blocked' : 'Note']));
      if (ev.type) head.appendChild(el('span', {class: 'ui-agent-notice-kind'}, [ev.type]));
      // The "when" only earns its place on a REPLAYED card: live, the reader
      // watched it arrive.
      if (ev.at) {
        var when = '';
        try { when = new Date(ev.at).toLocaleTimeString(); } catch (_) {}
        if (when) head.appendChild(el('span', {class: 'ui-agent-notice-when'}, [when]));
      }
      card.appendChild(head);
      var body = el('div', {class: 'ui-agent-notice-body'});
      body.textContent = ev.text || '';
      card.appendChild(body);
      return card;
    }

    // addNotice places one live breadcrumb in the conversation flow. Not a
    // message: no msgEls registration and no action bar, so it cannot disturb
    // the streaming bubble's bookkeeping — same contract as status_note.
    function addNotice(ev) {
      if (!ev || !(ev.text || '').length) return;
      // The server names each breadcrumb the same way live and in the trail
      // (diagID), because a page that loads mid-run gets both: the run buffer
      // replays from sequence zero AND the trail replay places the same entry
      // among the messages. Shown once, wherever it arrived first.
      if (ev.id) {
        if (noticeIds[ev.id]) return;
        noticeIds[ev.id] = true;
      }
      clearEmpty();
      convoLog.appendChild(buildNotice(ev));
      // The turn is still going, so the spinner stays BELOW what just landed.
      if (thinkingEl && thinkingEl.parentNode === convoLog) {
        convoLog.appendChild(thinkingEl);
      }
      keepPendingInterjectionsLast();
      scrollConvo(false);
    }

    // replayNotices re-places the blocking breadcrumbs of a loaded session
    // among its messages, so a reload shows the same picture of the turn that
    // the reader saw live instead of a thread with an unexplained gap in it.
    //
    // anchors are [{at, node}] in render order, one per replayed message. A
    // notice lands after the last message that predates it (at the very top
    // when it predates them all); the anchor then advances to the card just
    // inserted, so several notices from one turn keep their order.
    function replayNotices(list, anchors) {
      if (!Array.isArray(list) || !list.length) return;
      // The endpoint serves newest first for the trail modal; in the flow they
      // have to run the other way.
      var entries = list.slice().reverse().filter(function(e) {
        if (!e || e.level !== 'blocked' || !(e.detail || '').length) return false;
        if (e.id && noticeIds[e.id]) return false; // already on screen, live
        if (e.id) noticeIds[e.id] = true;
        return true;
      });
      entries.forEach(function(e) {
        var at = Date.parse(e.at || '');
        var card = buildNotice({level: e.level, type: e.kind, text: e.detail, at: e.at});
        var target = null;
        if (!isNaN(at)) {
          for (var i = 0; i < anchors.length; i++) {
            if (isNaN(anchors[i].at) || anchors[i].at > at) break;
            target = anchors[i];
          }
        }
        if (target) {
          convoLog.insertBefore(card, target.node.nextSibling);
          target.node = card; // the next notice for this turn follows this one
        } else if (anchors.length) {
          convoLog.insertBefore(card, anchors[0].node);
        } else {
          convoLog.appendChild(card);
        }
      });
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

    // settleConfirmCard reflects the decision on the card instead of leaving
    // greyed-out buttons that read as "nothing happened." The button row is
    // replaced with a one-line stamp (✓ Allowed / ✓ Always / ✕ Denied) and
    // the card gets a value class so CSS can tint it. The label is passed in
    // so app-defined wording (Allow / Always allow / Deny) carries through.
    //
    // Two callers, and the second is why this isn't inline in submitConfirm:
    // the click, and a `confirm_resolved` frame from the server. A run's
    // frames are buffered for reconnect, so a reload mid-run replays the
    // escalation card — and without the server's own record of the answer it
    // would come back armed, asking again about a call already allowed.
    function settleConfirmCard(id, value, label) {
      var card = document.getElementById('confirm-' + id);
      if (!card || card.classList.contains('ui-agent-confirm-resolved')) return;
      card.classList.add('ui-agent-confirm-resolved', 'is-' + (value || 'done'));
      card.querySelectorAll('button').forEach(function(b) { b.disabled = true; });
      var deny = value === 'deny' || value === 'no' || value === 'reject';
      var stamp = el('div', {class: 'ui-agent-confirm-status'},
        [(deny ? '✕ ' : '✓ ') + (label || value || 'Done')]);
      var row = card.querySelector('.ui-agent-confirm-btns');
      if (row) { row.replaceWith(stamp); } else { card.appendChild(stamp); }
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
        settleConfirmCard(id, value, (action && action.label) || value);
      }).catch(function(err) {
        // Failed to record — re-enable so the operator can retry.
        card.querySelectorAll('button').forEach(function(b) { b.disabled = false; });
        if (btn) btn.classList.remove('chosen');
        showToast('Confirm failed: ' + (err && err.message || err));
      });
    }

    // uiResolveBlock — settle an ACTIONABLE persisted card durably.
    //
    // A card that asks the user something has two lifetimes: the DOM one,
    // which ends at the click, and the stored one, which doesn't. Settling
    // only in the DOM means the next session load replays the same card with
    // its buttons live — an answered request that reads as still pending, and
    // a second click that re-fires an already-applied change. So a renderer
    // that just got its answer calls this, and the note it would have shown
    // rides back as the block's `resolved` field on every later load.
    //
    // Fire-and-forget by design: the card's real work already succeeded at its
    // own endpoint, and a failed stamp costs a stale card, not a wrong one —
    // so it must never surface an error over a decision that went through.
    // No-op when the app didn't configure BlockResolveURL.
    window.uiResolveBlock = function(blockId, note, sessionId) {
      var sid = sessionId || activeSessionId;
      if (!cfg.block_resolve_url || !blockId || !sid) return;
      var url = substituteExtras(cfg.block_resolve_url
        .replace('{id}', encodeURIComponent(sid))
        .replace('{block_id}', encodeURIComponent(blockId)));
      fetch(url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({note: String(note == null ? '' : note)}),
      }).catch(function(err) {
        if (window.console && console.warn) {
          console.warn('[ui] could not record block resolution:', blockId, err);
        }
      });
    };

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
            '- registered:', Object.keys(window.UIBlockRenderers || {}));
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
        keepPendingInterjectionsLast();
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
          refreshStatusPill();
          // The server just told us which thread this is. Watch it — a
          // background task started by this very turn will post its result
          // here, and nothing else on the client is looking.
          ensureReportPolling(activeSessionId);
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
        // A message taken back but kept visible, and a labelled one. Both
        // address a bubble by id; what the reason or label says is the
        // server's text.
        case 'chunk_strike':
          strikeMessage(ev.id, ev.reason || '');
          break;
        case 'message_label':
          labelMessage(ev.id, ev.label || '');
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
        // The server's own record that an escalation was answered. Emitted
        // by whoever was parked on it, so it rides in the run's frame
        // buffer alongside the question — which is what lets a reconnect
        // replay the card settled instead of arming it again.
        case 'confirm_resolved':
          settleConfirmCard(ev.id, ev.value, ev.label);
          break;
        case 'block':
          addBlock(ev);
          break;
        // A mark on the reply this turn just produced. Sent at the END of a
        // turn, so it addresses no id: the last assistant bubble is the one
        // it is about. What it means belongs to the app that sent it.
        case 'message_mark':
          markLastAssistant(ev.mark);
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
        // A guard stopped something, mid-turn. It is also filed in the
        // session trail (the ⚠ button), but the trail is where you look
        // once you already suspect a guard fired — this is how you find
        // out that one did.
        case 'notice':
          addNotice(ev);
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
            keepPendingInterjectionsLast();
            scrollConvo(false);
          }
          break;
        case 'done':
          // A note that arrived after the last drain point is still sitting
          // here, and the turn is over. Not hooked into enableInput, which also
          // fires on session OPEN — marking there would condemn a note belonging
          // to a run that is still going.
          markUndeliveredInterjections('The agent finished before reading this. It stays in the conversation and goes with your next message.');
          enableInput();
          setStatus('');
          // A turn can move whatever the app's status pill reports.
          refreshStatusPill();
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
          // AND in the conversation. The activity pane is a collapsible side
          // rail; a turn that failed there and nowhere else leaves the thread
          // with the user's message, no reply, and a re-enabled composer —
          // indistinguishable from the assistant having ignored them. The
          // failure has to appear where the request was made.
          clearEmpty();
          if (thinkingEl && thinkingEl.parentNode) { thinkingEl.remove(); thinkingEl = null; }
          var errBubble = el('div', {class: 'ui-agent-msg ui-agent-msg-failed'});
          var errBody = el('div', {class: 'ui-agent-msg-body'});
          // textContent, never markdown: an error carries provider text and
          // sometimes a URL, and rendering it as markdown would let a failure
          // message style itself like a reply.
          errBody.textContent = 'Could not complete this turn: ' + (ev.text || 'unknown error');
          errBubble.appendChild(errBody);
          convoLog.appendChild(errBubble);
          markUndeliveredInterjections('The turn failed before the agent read this. It stays in the conversation and goes with your next message.');
          keepPendingInterjectionsLast();
          scrollConvo(true);
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
    // markUndeliveredInterjections says so when a queued note was never read.
    //
    // A note waits at the bottom until the runner drains it BETWEEN ROUNDS. If
    // the turn stops first — cancelled, or simply finished before the next
    // drain point — the agent never saw it, and until now nothing said so: the
    // bubble sat there in the dim "queued" style, which is also how it looks
    // while it is still waiting, and which on reload becomes an ordinary user
    // message indistinguishable from one that was answered.
    //
    // It is NOT deleted. The server keeps a leftover note by appending it to
    // the session (runner_http.go), so the text is still there and still goes
    // to the agent with the next message — dropping the bubble would claim the
    // opposite. The honest thing is to say which of the two happened.
    function markUndeliveredInterjections(why) {
      if (!convoLog) return;
      var pending = convoLog.querySelectorAll('.ui-agent-interjection:not(.consumed):not(.ui-agent-interjection-undelivered)');
      for (var i = 0; i < pending.length; i++) {
        var b = pending[i];
        b.classList.add('ui-agent-interjection-undelivered');
        var body = b.querySelector('.ui-agent-msg-body') || b;
        var note = el('div', {class: 'ui-agent-interjection-note', text: why});
        body.appendChild(note);
      }
      return pending.length;
    }

    function enableInput() {
      sendBtn.disabled = recordLocked;
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

    // applyRecordLock opens or closes the composer for the thread on screen: a
    // record is read, never written into.
    function applyRecordLock(sid) {
      recordLocked = !!(sid && sid === recordPinnedSession(window.GOHORT_AGENT_ID));
      inputArea.disabled = recordLocked;
      sendBtn.disabled = recordLocked;
      inputArea.placeholder = recordLocked
        ? (cfg.record_locked_text || 'This thread is a record. Start a new session to talk.')
        : (cfg.placeholder || 'Ask something…');
    }

    function sendMessage() {
      if (recordLocked) return;
      var text = inputArea.value.trim();
      if (!text && !pendingAttachments.length) return;
      // Paste-marker substitution: expand any "[Pasted text #N - X
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
        text = text.replace(pasteMarkerRE,
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
          // The note STAYS at the bottom from here — keepPendingInterjectionsLast
          // re-appends it under anything that arrives while it is still queued,
          // and stops the moment the agent drains it (.consumed).
          //
          // It used to be moved straight up above the in-flight assistant
          // bubble, on the reasoning that a reply filling in underneath reads as
          // "my message landed above the answer". That reasoning had the
          // delivery backwards: the runner drains between ROUNDS, so the text
          // being written when you press send was decided without your note, and
          // putting the note above it claims it was answered. Waiting is the
          // truthful position, and once the note is actually picked up,
          // everything after it genuinely does come after it.
          keepPendingInterjectionsLast();
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
      // Through substituteExtras like every other URL, so a send can name the
      // host's open record ({scope}). The server would otherwise have to
      // remember which one it was, and a remembered answer is a document behind
      // whenever the send beats the page's own notification of the switch.
      var resp = fetch(substituteExtras(cfg.send_url), {
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
            // First send in a brand-new thread: this is where its id exists
            // for the first time, so it is where watching has to begin.
            ensureReportPolling(sid);
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
      // Say it before the request goes out: the agent has already stopped
      // reading by the time the user's finger leaves the button, and a note
      // still styled as "waiting" is claiming something that is over.
      markUndeliveredInterjections('The agent was stopped before reading this. It stays in the conversation and goes with your next message.');
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
          wrap.appendChild(el('div', {class: 'ui-rules-empty'}, ['No rules yet: add one below.']));
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
            rowKids.push(el('span', {class: 'ui-channels-inert', title: 'No source hooked in: inert'}, ['inert']));
          }
          // Optional state mark: when the app says something feeding this row
          // has come to rest, show it here rather than making the reader open
          // another pane to find out why the row went quiet. core/ui knows the
          // SHAPE names only; the app decides what they mean and writes the
          // tooltip.
          if (ch.state && ch.state.icon) {
            rowKids.push(uiStateGlyph(ch.state.icon, ch.state.tone, ch.state.title));
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




    // appendChannelMessage — render one newly-arrived channel message (live
    // poll). Mirrors the replay's per-message render minus the edit/scrub/tool
    // plumbing (channel threads are read-only and append-only).
    function appendChannelMessage(m) {
      if (!m || m.hidden) return;
      var mid = m.id || ('m-' + Math.random().toString(36).slice(2));
      addMessage(m.role || 'assistant', mid, m.content || m.text || '', m.sender);
      if (cfg.markdown && m.role === 'assistant') finalizeMessage(mid);
      if (m.created || m.usage || m.report_from || m.mark || m.retracted || m.label) setMessageMeta(mid, {created: m.created, usage: m.usage, report_from: m.report_from, report_kind: m.report_kind, report_detail: m.report_detail, mark: m.mark, retracted: m.retracted, label: m.label});
    }

    function stopChannelPolling() {
      if (channelPollTimer) { clearInterval(channelPollTimer); channelPollTimer = null; }
    }

    // startChannelPolling — while a channel thread is open, re-fetch its session
    // every few seconds and append any messages beyond what's on screen, so new
    // inbound + the agent's responses show up live without a manual reload.
    // channelPollCount is an ABSOLUTE storage index — how far into the thread
    // this pane has rendered — not a count of what is on screen.
    //
    // It used to be the length of the delivered array, which was the same
    // number while every load was a full one. Against a tail it is not: the
    // window slides, so its length stays put as messages arrive, and comparing
    // lengths would decide nothing new had ever happened. A watched channel
    // would sit silent forever, which is the one thing this poll exists to
    // prevent.
    function startChannelPolling(sid, initialEnd) {
      stopChannelPolling();
      if (!cfg.load_url || (sid || '').indexOf('chan:') !== 0) return;
      channelPollCount = initialEnd || 0;
      channelPollTimer = setInterval(function() {
        if (activeSessionId !== sid) { stopChannelPolling(); return; }
        fetchJSON(substituteExtras(cfg.load_url.replace('{id}', encodeURIComponent(sid)))).then(function(rec) {
          if (activeSessionId !== sid) return;
          var msgs = rec && rec[msgsF];
          if (!Array.isArray(msgs)) return;
          var off = (typeof rec.message_offset === 'number') ? rec.message_offset : 0;
          var end = off + msgs.length;
          if (end <= channelPollCount) return;
          // Start from whichever is later: where we left off, or the oldest
          // message this response actually carries. The second case means the
          // pane was away long enough for the tail to move past it — there is
          // a gap, and inventing indices to fill it would render the wrong
          // messages. Skipping to what we have is the honest recovery.
          for (var i = Math.max(channelPollCount, off); i < end; i++) {
            appendChannelMessage(msgs[i - off]);
          }
          channelPollCount = end;
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
          name: (tc.label || tc.Label || tc.name || tc.Name || 'tool') + (cached ? ' ♻' : ''),
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
      if (m.created || m.usage || m.report_from || m.mark || m.retracted || m.label) setMessageMeta(mid, {created: m.created, usage: m.usage, report_from: m.report_from, report_kind: m.report_kind, report_detail: m.report_detail, mark: m.mark, retracted: m.retracted, label: m.label});
      applyPersistedToolCalls(mid, m);
      if (messageReplayHooks.length) {
        var entry = msgEls[mid], bubble = entry && entry.bubble;
        if (bubble) messageReplayHooks.forEach(function(fn) { try { fn(bubble, m); } catch (e) {} });
      }
    }
    // startReportPolling — while a thread is open, re-fetch it and render any
    // NEW report cards (report_from set) live — anything the server posts to
    // the thread on its own. ONLY those cards — chat turns ride the
    // SSE/replay path, so the poll never touches them (no duplication).
    // Reuses the channel poll's timer.
    // ensureReportPolling — start the report poll for whatever thread is now
    // active, from anywhere that learns a session id.
    //
    // Polling used to be started by openSession alone, which meant a thread
    // the user never OPENED was never watched — and a brand-new conversation
    // is exactly that. The client sends first and learns its session id from
    // the response (or the 'session' event), so a fresh thread ran its whole
    // life unpolled: work posted to it by the server arrived silently, and
    // reloading the page was the only way to discover it had finished. That is
    // the common case for anything long enough to run in the background, since
    // "make me a picture" is usually the first thing said in a new thread.
    //
    // Channel threads keep their own poll (startChannelPolling), which watches
    // every message rather than just server-posted cards.
    function ensureReportPolling(sid) {
      if (!sid || channelTranscript) return;
      startReportPolling(sid);
    }
    // noteObsSince advances the poll watermark to this card's timestamp.
    //
    // Last one wins, NOT the string-max: both callers walk cards in stored
    // order, so the last is the newest, and RFC3339 with nanoseconds does not
    // compare correctly as text — Go trims trailing zeros from the fraction, so
    // ".1Z" sorts above ".15Z" while 0.1 is the earlier instant. A watermark
    // that jumped ahead like that would skip the cards in between.
    function noteObsSince(m) {
      if (m && m.created) cortexObsSince = String(m.created);
    }
    function startReportPolling(sid) {
      stopChannelPolling();
      if (!cfg.load_url || !sid) return;
      channelPollTimer = setInterval(function() {
        if (activeSessionId !== sid) { stopChannelPolling(); return; }
        // cards=1 asks for observation cards ONLY, and `since` narrows that to
        // what landed after the newest one on screen. Without it this refetched
        // the whole thread — every message, every persisted tool call's args
        // and output — every six seconds, to render the nothing that usually
        // arrived. See session_cards.go.
        var url = substituteExtras(cfg.load_url.replace('{id}', encodeURIComponent(sid)));
        url += (url.indexOf('?') >= 0 ? '&' : '?') + 'cards=1';
        if (cortexObsSince) url += '&since=' + encodeURIComponent(cortexObsSince);
        fetchJSON(url).then(function(rec) {
          if (activeSessionId !== sid) return;
          var msgs = rec && rec[msgsF];
          if (!Array.isArray(msgs)) return;
          msgs.forEach(function(m) {
            if (!m || !m.report_from) return; // observations only — chat turns ride SSE/replay
            noteObsSince(m);
            var k = obsKey(m);
            if (cortexObsSeen[k]) return;
            cortexObsSeen[k] = true;
            renderObservation(m);
          });
        }).catch(function() {});
        // Slower than the channel poll: this now runs for EVERY open thread,
        // not just the cortex home, and a report card arriving six seconds
        // later still arrives while the user is looking at it.
      }, 6000);
    }

    // scheduleRunningRefresh re-reads the rail a few seconds from now while
    // any row is running, so the pulse goes out when the turn ends without
    // the reader clicking anything. One timer at a time; a fresh render
    // replaces a pending one. The 30s poll further up serves channel agents
    // only, and an ordinary agent's rail otherwise refreshes on its own
    // turns alone — a run started in another tab would show forever.
    var runningPollTimer = null;
    function scheduleRunningRefresh(items) {
      if (runningPollTimer) { clearTimeout(runningPollTimer); runningPollTimer = null; }
      var anyRunning = Array.isArray(items) && items.some(function(s) { return s && s.running; });
      if (!anyRunning) return;
      runningPollTimer = setTimeout(function() {
        runningPollTimer = null;
        if (bulkState && bulkState.mode) return;
        if (document.hidden) { scheduleRunningRefresh(items); return; }
        loadSessions();
      }, 8000);
    }

    function loadSessions() {
      if (!hasList) return;
      loadChannels();
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
        var isRecord = false;
        if (!chanSid) {
          chanSid = recordPinnedSession(window.GOHORT_AGENT_ID);
          isRecord = !!chanSid;
        }
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
            // A RECORD thread (the agent does not take turns in it) wears a
            // neutral tint and says so, so it is never mistaken for the
            // standing thread an agent resumes.
            var gold = isRecord ? '#8a93a6' : '#d9b86c';
            var heroLabel = isRecord ? (cfg.record_label || 'Activity') : (cfg.alt_primary_label || 'Cortex');
            var titleLine = el('div', {style: 'display:flex;align-items:center;gap:0.4rem;white-space:nowrap;overflow:hidden'}, [
              el('span', {style: 'font-weight:700;overflow:hidden;text-overflow:ellipsis'}, [heroLabel]),
              el('span', {style: 'font-size:0.56rem;text-transform:uppercase;letter-spacing:0.05em;font-weight:700;color:' + gold + ';border:1px solid ' + gold + ';border-radius:999px;padding:0.02rem 0.4rem;flex:0 0 auto'}, [isRecord ? 'record' : 'home']),
            ]);
            if (homeRec.unread && !chActive) {
              titleLine.appendChild(el('span', {class: 'ui-unread-dot', title: 'New activity',
                style: 'width:7px;height:7px;border-radius:50%;background:var(--accent, #4a9eff);flex:0 0 auto'}, ['']));
            }
            var chKids = [
              el('span', {style: 'flex:0 0 1.1rem;text-align:center;font-size:0.95rem;color:' + gold}, [isRecord ? '📋' : '🧠']),
              el('div', {style: 'flex:1;min-width:0'}, [
                titleLine,
                el('div', {style: 'font-size:0.74rem;color:var(--text-mute, #999);margin-top:0.1rem;overflow:hidden;text-overflow:ellipsis;white-space:nowrap'},
                  [isRecord ? (cfg.record_hint || 'what reached this agent') : 'standing thread']),
              ]),
            ];
            // Faint gold tint ALWAYS (marks it as the standing thread); a gold
            // border adds when it's the active thread.
            var heroBorder = chActive ? gold : 'transparent';
            var heroBg = isRecord
              ? (chActive ? 'rgba(138,147,166,0.16)' : 'rgba(138,147,166,0.07)')
              : (chActive ? 'rgba(217,184,108,0.16)' : 'rgba(217,184,108,0.07)');
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
          // An empty state, not a value. "(none)" reads as a session named
          // none sitting in the list; it is styled inert so it reads as the
          // list being empty. Wording stays generic — this list holds chat
          // sessions on every surface that mounts the panel.
          sideList.appendChild(el('div', {class: 'ui-chat-side-empty'}, ['No previous sessions']));
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
          // Running pulse — this session is mid-turn RIGHT NOW (the server
          // keys it off its in-flight run, so a turn started from another
          // tab or a wake counts too). The style has been in the stylesheet
          // all along; the row stopped emitting it, so a list of five
          // sessions with one working looked like five idle ones.
          if (rec.running) {
            rowClass += ' running';
            rowKids.unshift(el('span', {class: 'ui-chat-side-running-dot',
              title: 'Running now: open to watch'}, ['']));
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
                  delMsg = 'This thread has ' + bits.join(' and ') + '. Deleting it only clears the conversation: they keep running and will re-create the thread on their next report. To stop them, use Decommission. Delete anyway?';
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
        scheduleRunningRefresh(items);
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

    // How many messages to ask the server for. -1 = don't ask, let the server
    // apply its own default — which is every open except one the user pressed
    // "Load earlier" on. Reset per session, because a thread you scrolled back
    // through should not make the NEXT thread slow to open.
    var sessionLoadLimit = -1;

    // How many messages the currently-open thread was trimmed from the front,
    // at module scope because the truncate path needs it long after the load
    // that computed it has returned.
    var loadedMsgOffset = 0;

    // clearConvoPanes empties the conversation + activity panes and the
    // per-message bookkeeping that indexes into them. One function because the
    // maps and the DOM have to go together: an entry pointing at a node that is
    // no longer in the document is a scrub button that PATCHes the wrong index.
    //
    // Called either eagerly (opening a different session) or late, just before
    // the rebuild (re-rendering the same thread for "Show earlier"), so that a
    // press does not blank what the reader is looking at while it fetches.
    function clearConvoPanes() {
      msgEls = {}; activityEls = {}; blockEls = {}; noticeIds = {};
      // Cleared with the rest of the per-thread state. A stale offset carried
      // into the next thread would misplace its truncate point.
      loadedMsgOffset = 0;
      convoLog.innerHTML = '';
      activityLog.innerHTML = '';
    }

    // earlierAnchor remembers WHERE THE READER WAS across a "Show earlier"
    // press, so the older chunk arrives above them instead of moving them.
    //
    // The press re-renders the whole thread (see loadEarlierMessages), which
    // destroys every node and any scroll position tied to one. Landing at the
    // top was the first answer, and it is wrong for the obvious reason: you
    // press the button to read what came BEFORE the message you are on, and it
    // takes you somewhere else entirely — on the second press, past a chunk you
    // have not read yet.
    //
    // Anchored on a MESSAGE, not on a scroll offset or a height delta. Heights
    // are still settling when this runs (markdown, images, tool chips), so a
    // delta measured now is wrong a frame later and re-applying it double-
    // corrects. An element's position can simply be re-read as the layout
    // changes, and every re-read converges on the same answer.
    //
    // Identified by STORAGE INDEX rather than by node or id: the node is gone
    // after the re-render, and message ids are not guaranteed present on stored
    // records, while the index is what the replay already stamps on every
    // bubble for the scrub affordance.
    var earlierAnchor = null;

    // captureEarlierAnchor records the topmost message currently in view and
    // how far down the pane it sits.
    function captureEarlierAnchor() {
      earlierAnchor = null;
      var paneTop = convoLog.getBoundingClientRect().top;
      var best = null;
      for (var k in msgEls) {
        var entry = msgEls[k];
        if (!entry || typeof entry.storageIndex !== 'number' || !entry.bubble) continue;
        if (!best || entry.storageIndex < best.storageIndex) best = entry;
      }
      if (!best) return;
      earlierAnchor = {
        index: best.storageIndex,
        // Offset from the pane's top edge, via getBoundingClientRect — NOT
        // offsetTop, which is measured against the nearest positioned ancestor
        // and quietly means something else the moment one appears.
        offset: best.bubble.getBoundingClientRect().top - paneTop,
      };
    }

    // restoreEarlierAnchor puts the captured message back where it was, and
    // holds it there while the newly-loaded chunk finishes laying out above it.
    function restoreEarlierAnchor() {
      if (!earlierAnchor) {
        convoLog.scrollTop = 0; // nothing to anchor to: the old behavior
        return;
      }
      // Held by the closures below, then cleared: an anchor belongs to the one
      // press that made it, and a leftover index applied to a different thread
      // would scroll it somewhere arbitrary.
      var target = earlierAnchor;
      earlierAnchor = null;
      var apply = function() {
        var found = null;
        for (var k in msgEls) {
          var entry = msgEls[k];
          if (entry && entry.storageIndex === target.index && entry.bubble) { found = entry; break; }
        }
        if (!found) return;
        var delta = (found.bubble.getBoundingClientRect().top - convoLog.getBoundingClientRect().top) - target.offset;
        if (delta) convoLog.scrollTop += delta;
      };
      apply();
      if (typeof requestAnimationFrame === 'function') requestAnimationFrame(apply);
      setTimeout(apply, 60);
      setTimeout(apply, 250);
      // Same reason as settleConvoScroll: an image has no height until it is
      // decoded, and one that lands ABOVE the anchor pushes it down.
      var imgs = convoLog.querySelectorAll('img');
      for (var i = 0; i < imgs.length; i++) {
        (function(img) {
          if (img.complete) return;
          img.addEventListener('load', apply, {once: true});
          img.addEventListener('error', apply, {once: true});
        })(imgs[i]);
      }
    }

    // sessionChunkSize is how many messages one "Show earlier" press adds. The
    // server serves the value for the thread that is open (a cortex is bounded
    // tighter than an ordinary conversation); this is only the fallback for a
    // payload that carried none.
    var sessionChunkSize = 0;

    // loadEarlierMessages re-opens the current thread asking for ONE more chunk.
    //
    // A re-render rather than a DOM prepend: the replay path is a hundred lines
    // of bubble / tool-chip / block hydration, and a second insert-above
    // version of it would drift from this one the first time either changed.
    //
    // ONE CHUNK, not double what is already loaded. Doubling reached the top of
    // a long thread in few presses, which sounds good until you watch it happen
    // on a standing thread: the third press asks for eight chunks and re-renders
    // every one of them, so the button gets slower the more you use it and a
    // couple of presses can drop thousands of bubbles into the pane at once.
    // Reading backwards is a steady walk, so each press costs the same as the
    // last. Progress is still guaranteed — the ask always exceeds loaded +
    // skipped — and when it passes the real total the server serves the rest,
    // the offset comes back 0, and the button takes itself away.
    function loadEarlierMessages(loadedCount, offset) {
      var chunk = sessionChunkSize > 0 ? sessionChunkSize : Math.max(loadedCount, 20);
      sessionLoadLimit = loadedCount + offset + chunk;
      openSession(activeSessionId, true);
    }

    // keepLimit is set by loadEarlierMessages so the re-open does not reset the
    // ask it was made for.
    function openSession(sid, keepLimit) {
      // Picked from the modal picker: the choice is made, so get out of the way
      // rather than leaving the reader to dismiss a list they are done with.
      closeSessionPicker();
      // -1, NOT 0. Both read as "reset" here, but they are opposite requests on
      // the wire: -1 sends no limit at all and lets the server apply its own
      // default, while 0 IS a limit, and the one value the server defines as
      // "serve the whole thread" (an explicit 0 has to mean that, or "Show
      // earlier" could never reach the top of a thread longer than the steps
      // ever cover).
      //
      // So every ordinary open was asking for the entire transcript. The tail
      // limit had no effect from this client, message_offset always came back
      // 0, and with nothing trimmed there was nothing above the first message —
      // so the "Show earlier" pill never rendered either. A standing thread
      // opened by shipping and building every message it had ever held, which
      // is the slow load, and the missing button was the same bug wearing a
      // different face.
      if (!keepLimit) sessionLoadLimit = -1;
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
          // Topbar controls FIRST, before the border reset below: their
          // resting 1px border is their button chrome, not a selection mark,
          // and blanking it here left them invisible until clicked (selection
          // is an outline, a different property, which is why only the clicked
          // state showed).
          if (navItem.topbar) {
            b.style.outline = '';
            return;
          }
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
      // The composer belongs to the session on screen, not to the panel. It
      // used to keep whatever state the LAST send left it in: leave a running
      // session for an idle one and Send stayed hidden behind a Cancel that
      // would have cancelled nothing (the cancel POST keys off the OPEN
      // session), and every session looked busy while any one of them was.
      // Start every open idle; tryResumeRun below flips it back to Cancel
      // only when THIS session has a run in flight.
      enableInput();
      applyRecordLock(sid);
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
        msgEls = {}; noticeIds = {};
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
                if (m.created || m.usage || m.retracted || m.label) {
                  setMessageMeta(mid, {created: m.created, usage: m.usage, report_from: m.report_from, report_kind: m.report_kind, report_detail: m.report_detail, mark: m.mark, retracted: m.retracted, label: m.label});
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
                        name: (tc.label || tc.Label || tc.name || tc.Name || 'tool') + (cached ? ' ♻' : ''),
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
      refreshStatusPill();
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
      // Emptying the panes is what makes room for the replay, and it used to
      // happen HERE — before the request had even been sent. So the pane went
      // blank the instant you pressed "Show earlier" and stayed blank for the
      // whole round trip, showing nothing while it fetched history you already
      // had on screen a moment ago.
      //
      // For a press that re-renders the SAME thread there is no reason to show
      // you an empty pane at all: the content you were reading is still valid
      // right up to the moment its replacement is ready. So the wipe waits for
      // the response and happens immediately before the rebuild, which is one
      // synchronous block and therefore one paint — no flash between them.
      //
      // Switching to a DIFFERENT session still clears immediately: there the
      // blank IS the feedback that the click registered, and leaving the old
      // thread up while another loads reads as a click that did nothing.
      if (!keepLimit) clearConvoPanes();
      if (!sid) {
        clearConvoPanes();
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
      // Only sent when the user asked for more. Absent, the server applies its
      // own default, so an app that never wires this button still gets the tail
      // without knowing the parameter exists.
      if (sessionLoadLimit >= 0) {
        url += (url.indexOf('?') >= 0 ? '&' : '?') + 'limit=' + sessionLoadLimit;
      }
      fetchJSON(url).then(function(rec) {
        // The deferred wipe for a same-thread re-render. Everything from here
        // to the end of this handler runs synchronously, so the browser paints
        // the empty pane and the rebuilt one as a single frame — the reader
        // sees the thread grow upward, never a blank panel.
        //
        // Deliberately AFTER the request resolved and before anything is
        // appended: a failed fetch falls to .catch having touched nothing, so a
        // press that errors leaves the thread on screen instead of wiping it.
        if (keepLimit) clearConvoPanes();
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
        // How many messages the server left off the front. Zero on a full
        // load, which is every load today — but the scrub and truncate
        // affordances send a message's index back to be deleted, and that
        // index has to name the message in STORAGE, not its position in
        // whatever slice arrived. Adding the offset here means a tail load can
        // never make the ✕ delete somebody else's message.
        var msgOffset = (rec && typeof rec.message_offset === 'number') ? rec.message_offset : 0;
        loadedMsgOffset = msgOffset;
        // How much one "Show earlier" press adds, per the server's own bound for
        // THIS thread. Read on every load so switching threads switches the step.
        sessionChunkSize = (rec && typeof rec.chunk_size === 'number') ? rec.chunk_size : 0;
        // A nonzero offset means there is thread above what arrived. Say so
        // where the top of it is, rather than letting a months-old channel
        // appear to begin mid-sentence.
        if (msgOffset > 0) {
          var loaded = Array.isArray(msgs) ? msgs.length : 0;
          convoLog.appendChild(el('div', {class: 'ui-agent-earlier'}, [
            el('button', {
              type: 'button', class: 'ui-agent-earlier-btn',
              onclick: function() {
                this.disabled = true;
                this.textContent = 'Loading…';
                captureEarlierAnchor();
                loadEarlierMessages(loaded, msgOffset);
              },
            }, ['Show earlier']),
            // What one press costs and what is left, so pressing it is a known
            // quantity rather than a guess at how much thread is about to land.
            el('span', {class: 'ui-agent-earlier-note'}, [
              sessionChunkSize > 0 && msgOffset > sessionChunkSize
                ? sessionChunkSize + ' more of ' + msgOffset + ' earlier'
                : msgOffset + ' earlier',
            ]),
          ]));
        }
        // Where a replayed breadcrumb goes (replayNotices). Collected during
        // the pass below because that is the only place a message's bubble and
        // its timestamp are both in hand.
        var noticeAnchors = [];
        if (Array.isArray(msgs)) {
          msgs.forEach(function(m, idx) {
            var i = idx + msgOffset;
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
            if (m.created || m.usage || m.retracted || m.label) {
              setMessageMeta(mid, {created: m.created, usage: m.usage, report_from: m.report_from, report_kind: m.report_kind, report_detail: m.report_detail, mark: m.mark, retracted: m.retracted, label: m.label});
            }
            if (msgEls[mid] && msgEls[mid].bubble) {
              noticeAnchors.push({at: Date.parse(m.created || ''), node: msgEls[mid].bubble});
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
        // The guards that stopped something in this thread, put back where
        // they happened. Fetched rather than carried on the session record:
        // the trail lives in its own table (it is written mid-turn, and the
        // session save would race it), and the ⚠ button already reads it from
        // this endpoint. Best-effort — a thread must still open when the trail
        // does not answer.
        if (cfg.diagnostics_url) {
          var diagURL = substituteExtras(cfg.diagnostics_url).replace('{session}', encodeURIComponent(sid));
          fetchJSON(diagURL).then(function(trail) {
            // The reader may have moved on while this was in flight; those
            // anchors are detached now and belong to a thread nobody is
            // looking at.
            if (activeSessionId !== sid) return;
            replayNotices(trail, noticeAnchors);
          }).catch(function() {});
        }
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid);
        // A re-open for "Load earlier" lands on the oldest message just
        // fetched. Snapping back to the bottom would undo the press: the whole
        // point was to get away from there, and the button would look broken
        // for putting you back where you started.
        //
        // Every OTHER open ends at the bottom, explicitly. The per-message
        // scrolls during replay obey the stick flag, and that flag survives
        // across sessions — so scrolling up in one thread, or pressing "Show
        // earlier" (which sets it false right here), left the NEXT thread
        // opening wherever the scrollbar happened to be. It looked intermittent
        // because a rebuild sometimes fires a scroll event whose handler
        // recomputes the flag back to true, and sometimes does not.
        if (keepLimit) {
          convoStickToBottom = false;
          restoreEarlierAnchor();
        } else {
          settleConvoScroll();
        }
        loadSessions();
        // Channel threads are append-only and fed server-side (from the
        // messaging surface) — poll so new inbound + the agent's replies show
        // up live while watching, without a manual reload.
        if (channelTranscript) {
          // Seeded with the absolute end of what replayed — offset included,
          // since a tail load's array starts partway into the thread.
          startChannelPolling(sid, msgOffset + (Array.isArray(msgs) ? msgs.length : 0));
        } else {
          // Any other open thread: seed the seen-set from what just replayed,
          // then poll for NEW report cards so they appear live — anything
          // posted to the thread by the server rather than by this client.
          // Only the cards; chat turns ride SSE, so the poll never touches them.
          //
          // This used to be the cortex home thread only, which left an ordinary
          // session with no live path at all: a result posted to it while the
          // user sat looking at the thread simply never appeared, and reopening
          // the session was the only way to find out the work had finished.
          cortexObsSeen = {};
          cortexObsSince = '';
          if (Array.isArray(msgs)) {
            msgs.forEach(function(m) {
              if (!m || !m.report_from) return;
              cortexObsSeen[obsKey(m)] = true;
              noteObsSince(m);
            });
          }
          startReportPolling(sid);
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
        sendOpeningPrompt(rec);
      }).catch(function(err) {
        addActivity('error', '', err.message || String(err));
      });
    }

    // sendOpeningPrompt fires the first message a session was CREATED
    // with, for a handoff from a surface that already knew what the
    // conversation was about (an app that just processed something and
    // opened a thread to ask about it).
    //
    // Routed through the normal composer + sendMessage rather than a
    // side channel, so streaming, cancellation, approval cards and any
    // phase machinery all behave exactly as they do when a person types
    // it — there is no second path through a turn to keep in step.
    //
    // The server serves opening_prompt ONLY while the session is still
    // empty, so the send itself is what stops it firing again: once the
    // user message lands the session has messages and the field stops
    // arriving. No flag to clear, and no window where a reload sends it
    // twice.
    function sendOpeningPrompt(rec) {
      var text = rec && rec.opening_prompt;
      if (!text || !inputArea) return;
      // Never clobber something the person has already started typing —
      // they beat us to the box, and their words win.
      if (String(inputArea.value || '').trim()) return;
      inputArea.value = text;
      autosizeInput();
      sendMessage();
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
          // No poll here on purpose: this path attaches to a live run WITHOUT
          // replaying the thread, so cortexObsSeen is unseeded and a poll
          // would render every historical card into an empty pane. The
          // session event that arrives on the stream starts it instead.
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
      } else if (navTopbarEl && !navTopbarEl.closest('.ui-agent-top-bundle')) {
        // The topbar is being dropped and no bundle adopted the control (no
        // side rail, so that block never ran). Park it at the top of main
        // rather than letting it fall out of the DOM — a queue the user can't
        // see is the bug this whole control exists to fix.
        var navSolo = el('div', {style: 'display:flex;justify-content:flex-end;padding:0.35rem 0.5rem'}, [navTopbarEl]);
        main.insertBefore(navSolo, main.firstChild);
      }
    } else {
      wrap.appendChild(topbar);
    }
    // In modal mode `side` stays detached until the picker opens — it is the
    // same element, mounted somewhere else.
    if (hasList && !listPosModal) {
      gridRow.appendChild(side);
      wrap.appendChild(drawer.backdrop);
    }
    if (expandTab && !listPosModal) {
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
