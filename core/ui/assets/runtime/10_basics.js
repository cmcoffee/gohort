  components.panic_bar = function(cfg) {
    var status = el('span', {class: 'ui-panic-status'});
    var btn = el('button', {
      class: 'ui-panic-btn',
      onclick: async function() {
        if (cfg.confirm && !(await window.uiConfirm(cfg.confirm))) return;
        status.textContent = 'Engaging…';
        fetchJSON(cfg.on_click, {method: 'POST'}).then(function(d) {
          status.textContent = (d && d.message) ? d.message : 'Done.';
        }).catch(function(err) {
          status.textContent = 'Failed: ' + err.message;
        });
      }
    }, [cfg.label]);
    return el('div', {class: 'ui-panic-bar'}, [btn, status]);
  };

  components.toggle_group = function(cfg) {
    var wrap = el('div', {class: 'ui-toggle-group'});
    var current = {};
    function render() {
      wrap.innerHTML = '';
      cfg.toggles.forEach(function(t) {
        var input = el('input', {type: 'checkbox', class: 'ui-switch'});
        input.checked = !!current[t.field];
        input.addEventListener('change', function() {
          current[t.field] = input.checked;
          fetchJSON(cfg.source, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(current)
          }).catch(function(err){ showToast('Save failed: ' + err.message); input.checked = !input.checked; });
        });
        var labelText = el('span', {class: 'ui-toggle-label'}, [t.label,
          t.help ? el('span', {class: 'ui-toggle-help'}, [t.help]) : null]);
        var row = el('label', {class: 'ui-toggle-row'}, [labelText, input]);
        wrap.appendChild(row);
      });
    }
    fetchJSON(cfg.source).then(function(d) { current = d || {}; render(); })
      .catch(function(err){ wrap.textContent = 'Failed to load: ' + err.message; });
    return wrap;
  };

  components.table = function(cfg) {
    var listEl = el('div', {class: 'ui-table-list'}, ['Loading…']);
    var refreshIndicator = null;
    var records = [];
    var openExpansions = {}; // rowKey -> {actionIndex: panel}

    // Refetch when an external action invalidates our source. See
    // window.uiInvalidate above for the helper. Compare exact URL —
    // sources with placeholders ({id} etc.) won't ever match because
    // they get resolved separately per row, so this is safe.
    window.addEventListener('ui-data-changed', function(ev) {
      var sources = ev.detail && ev.detail.sources;
      if (!sources) return;
      if (sources.indexOf(cfg.source) >= 0) reload(true);
    });

    function reload(quiet) {
      if (!quiet && refreshIndicator) refreshIndicator.textContent = '↻';
      // Skip while any expansion is open (don't slam them shut).
      if (Object.keys(openExpansions).some(function(k){ return Object.keys(openExpansions[k]).length > 0; })) return;
      fetchJSON(cfg.source).then(function(d) {
        if (cfg.records_field) {
          // Strict: when records_field is set, use it exactly. A
          // missing/null field means "empty list" — don't fall back
          // to a different key, which would silently render the
          // wrong section (an empty pending list falling through to
          // active because alphabetical key ordering picks it first).
          var v = d ? d[cfg.records_field] : null;
          records = Array.isArray(v) ? v : [];
        } else {
          records = (d && d.conversations) || (Array.isArray(d) ? d : (d && d[Object.keys(d)[0]]) || []);
        }
        if (cfg.sort_by) {
          records.sort(function(a, b) {
            var av = a[cfg.sort_by] || '', bv = b[cfg.sort_by] || '';
            return cfg.sort_desc ? String(bv).localeCompare(String(av)) : String(av).localeCompare(String(bv));
          });
        }
        renderRows();
      }).catch(function(err){ listEl.textContent = 'Failed: ' + err.message; })
        .then(function(){ if (refreshIndicator) setTimeout(function(){ refreshIndicator.textContent = ''; }, 600); });
    }

    function renderRows() {
      listEl.innerHTML = '';
      if (!records.length) {
        listEl.appendChild(el('div', {class: 'ui-table-empty'}, [cfg.empty_text || 'Nothing here yet.']));
        return;
      }
      records.forEach(function(rec) {
        var rowKey = rec[cfg.row_key];
        var row = el('div', {class: 'ui-table-row'});

        // Leading actions go directly on the row (they stay pinned to
        // the left even when cells stack on narrow viewports).
        (cfg.row_actions || []).forEach(function(act, ai) {
          if (!act.leading) return;
          appendAction(row, act, ai, rec, rowKey, row);
        });

        var cellsWrap = el('div', {class: 'ui-row-cells'});
        cfg.columns.forEach(function(col) {
          var v = lookup(rec, col.field);
          if (col.type === 'badge') {
            cellsWrap.appendChild(renderBadgeCell(col, v));
            return;
          }
          if (col.type === 'dot') {
            cellsWrap.appendChild(renderDotCell(col, v));
            return;
          }
          var cell = el('div', {class: 'ui-table-cell' + (col.mute ? ' mute' : '')});
          if (col.flex) cell.style.flex = col.flex;
          // Link column: render a safe anchor (text + href set separately so
          // nothing is HTML-injected) instead of embedding raw <a> markup, which
          // a cell would escape and show as literal text. Guard the scheme so a
          // javascript:/data: URL in the data can't become a clickable link.
          var href = col.link ? lookup(rec, col.link) : null;
          if (href != null && /^(https?:\/\/|\/)/.test(String(href))) {
            var a = el('a', {href: String(href), target: '_blank', rel: 'noopener', class: 'ui-table-link'});
            a.textContent = fmt(v, col.format);
            cell.appendChild(a);
          } else {
            cell.textContent = fmt(v, col.format);
          }
          cellsWrap.appendChild(cell);
        });
        row.appendChild(cellsWrap);

        var actionsWrap = el('div', {class: 'ui-row-actions'});
        (cfg.row_actions || []).forEach(function(act, ai) {
          if (act.leading) return;
          // parent = where to append the control (actionsWrap so all
          // buttons live in one flex group). rowEl = the actual table
          // row, so expand handlers can insert their panel as a true
          // sibling of the row in the list rather than as a child of
          // actionsWrap (which would render the panel beside or
          // beneath the buttons inside the row).
          appendAction(actionsWrap, act, ai, rec, rowKey, row);
        });
        if (actionsWrap.childNodes.length > 0) row.appendChild(actionsWrap);

        listEl.appendChild(row);
      });
    }

    function appendAction(parent, act, ai, rec, rowKey, rowEl) {
      // Conditional rendering: skip when only_if field is falsy or
      // when hide_if field is truthy. Either gate alone is enough.
      if (act.only_if && !lookup(rec, act.only_if)) return;
      if (act.hide_if && lookup(rec, act.hide_if)) return;
      if (act.type === 'toggle') {
        var input = el('input', {type: 'checkbox', class: 'ui-switch'});
        input.checked = !!rec[act.field];
        input.addEventListener('change', function() {
          var url = substitute(act.post_to, rec);
          var body = {}; body[act.field] = input.checked;
          fetchJSON(url, {
            method: act.method || 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
          }).catch(function(err){ showToast('Save failed: ' + err.message); input.checked = !input.checked; });
          rec[act.field] = input.checked;
        });
        // Optional label rendered to the LEFT of the switch so the
        // operator knows what the toggle controls. Used in tables
        // without column headers (e.g. Users → "Admin").
        if (act.label) {
          var pair = el('span', {class: 'ui-row-toggle-pair'}, [
            el('span', {class: 'ui-row-toggle-label'}, [act.label]),
            input,
          ]);
          parent.appendChild(pair);
        } else {
          parent.appendChild(input);
        }
      } else if (act.type === 'select') {
        var sel = el('select', {class: 'ui-row-select'});
        if (act.width) sel.style.width = act.width;
        var disabled = act.disable_if && rec[act.disable_if];
        var filtered = act.filter_options_if && rec[act.filter_options_if]
          ? (act.filter_options || '').split(',').map(function(s){ return s.trim(); }).filter(Boolean)
          : [];
        // act.default_field (when set) names a per-row field that
        // carries the out-of-the-box default for this row's select.
        // Append "*" to the matching option's label so a user who
        // has overridden the value can still see what the default
        // was without consulting the docs.
        var defaultValue = act.default_field ? String(rec[act.default_field] || '') : '';
        (act.options || []).forEach(function(o) {
          if (filtered.indexOf(o.value) >= 0) return;
          var label = o.label || o.value;
          if (defaultValue && String(o.value) === defaultValue) label = label + ' *';
          var opt = el('option', {value: o.value}, [label]);
          if (String(rec[act.field]) === String(o.value)) opt.selected = true;
          sel.appendChild(opt);
        });
        if (disabled) sel.disabled = true;
        sel.addEventListener('change', function() {
          var url = substitute(act.post_to, rec);
          var body = {}; body[act.field] = sel.value;
          // Routing endpoint also wants the row_key (e.g. {key: "...", value: "...", think_budget: N})
          // — when row_key is set on the table, include it so a single
          // POST endpoint can identify which stage to update. Other
          // fields the endpoint cares about ride along when present.
          if (cfg.row_key) body[cfg.row_key] = rec[cfg.row_key];
          fetchJSON(url, {
            method: act.method || 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
          }).catch(function(err){ showToast('Save failed: ' + err.message); });
          rec[act.field] = sel.value;
        });
        parent.appendChild(sel);
      } else if (act.type === 'number') {
        var ninput = el('input', {type: 'number', class: 'ui-row-number'});
        if (act.width) ninput.style.width = act.width;
        if (act.min) ninput.min = String(act.min);
        if (act.max) ninput.max = String(act.max);
        var v = rec[act.field];
        ninput.value = (v == null || v === 0) ? '' : String(v);
        if (act.label) ninput.placeholder = act.label;
        ninput.addEventListener('change', function() {
          var n = parseInt(ninput.value, 10);
          if (isNaN(n)) n = 0;
          var url = substitute(act.post_to, rec);
          var body = {}; body[act.field] = n;
          if (cfg.row_key) body[cfg.row_key] = rec[cfg.row_key];
          fetchJSON(url, {
            method: act.method || 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
          }).catch(function(err){ showToast('Save failed: ' + err.message); });
          rec[act.field] = n;
        });
        parent.appendChild(ninput);
      } else if (act.type === 'button') {
        var classes = 'ui-row-btn';
        if (act.compact) classes += ' compact';
        if (act.variant) classes += ' ' + act.variant;
        var btn = el('button', {class: classes, onclick: async function() {
          if (act.confirm && !(await window.uiConfirm(act.confirm))) return;
          // Method="client" — hand off to an app-registered browser handler
          // (window.uiRegisterClientAction), passing the row record + a reload
          // callback. Lets a row button run custom UI (e.g. a modal that shows a
          // server-returned link) instead of the fixed POST-then-reload flow.
          // post_to carries the handler NAME (no URL substitution).
          if ((act.method || '').toLowerCase() === 'client') {
            var fn = window.UIClientActions && window.UIClientActions[act.post_to];
            if (typeof fn === 'function') {
              fn({ record: rec, button: btn, reload: function(){ reload(true); } });
            }
            return;
          }
          var url = substitute(act.post_to, rec);
          // Method=GET means "pure navigation button" — skip the
          // fetch+JSON-parse dance and just navigate. Used by "Open",
          // "Edit", "New instance" — buttons whose target is a page,
          // not a JSON endpoint. RedirectURL takes precedence over
          // PostTo when both are set (lets PostTo be the nominal API
          // and RedirectURL the visual destination).
          if ((act.method || 'POST').toUpperCase() === 'GET') {
            var dest = url;
            if (act.redirect_url) dest = substitute(act.redirect_url, rec);
            var target = act.redirect_target || '_self';
            if (target === '_self') window.location.href = dest;
            else window.open(dest, target);
            return;
          }
          // Optimistic mode — hide the row immediately so the user
          // gets "thing is gone" feedback without waiting on the
          // round-trip. Restore the row on failure so the table
          // doesn't lose state when the server says no.
          var optimisticHidden = false;
          if (act.optimistic && rowEl) {
            rowEl.style.display = 'none';
            optimisticHidden = true;
          }
          fetchJSON(url, {method: act.method || 'POST'})
            .then(function(resp) {
              // Close any open expansion on this row before the
              // refresh — reload() silently skips when ANY expansion
              // is open in the table (the guard exists to keep
              // poll-driven refreshes from yanking content the user
              // is reading), but for a user-initiated row mutation
              // the data MUST refresh. The user clicked an action
              // on this row; the expansion's stale content is no
              // longer worth preserving. Without this close, the
              // "View → Approve" workflow leaves the approved row
              // still visible in the source table until manual
              // page refresh.
              if (rowEl && openExpansions[rowKey]) {
                Object.keys(openExpansions[rowKey]).forEach(function(k){
                  var panel = openExpansions[rowKey][k];
                  if (panel && panel.parentNode) panel.parentNode.removeChild(panel);
                });
                delete openExpansions[rowKey];
              }
              // RedirectURL — substitute response-JSON fields into
              // the destination URL and navigate. {id}, {session},
              // etc. resolve from the response body so a "Run"
              // button can hop straight to a watch page using the
              // freshly-allocated session ID. Falls back to a row
              // reload when no redirect is configured.
              if (act.redirect_url) {
                var dest = substitute(act.redirect_url, resp || {});
                var target = act.redirect_target || '_blank';
                if (target === '_self') window.location.href = dest;
                else window.open(dest, target);
                reload(true);
                return;
              }
              reload(true);
              // Broadcast to sibling tables sharing our source.
              // Example: "Persistent Tools (Pending)" and
              // "Persistent Tools (Active)" both source from
              // /api/persistent-tools — approving a tool moves it
              // from pending → active, and both views should
              // reflect the move. Without this broadcast the
              // active table stays stale until manual reload.
              if (window.uiInvalidate && cfg.source) {
                window.uiInvalidate(cfg.source);
              }
              // Surface a server-provided outcome message. A non-empty
              // `warnings` array escalates to a modal alert so an unmet
              // reference (e.g. a catalog install that leaves a tool
              // wired to a missing credential) can't be missed; a plain
              // message is a transient toast confirmation. Generic — any
              // POST row action that returns `message` benefits.
              if (resp && typeof resp.message === 'string' && resp.message) {
                if (resp.warnings && resp.warnings.length && window.uiAlert) window.uiAlert(resp.message);
                else showToast(resp.message);
              }
            })
            .catch(function(err){
              // Restore the optimistically-hidden row so the user
              // sees the state didn't actually change.
              if (optimisticHidden && rowEl) rowEl.style.display = '';
              showToast('Failed: ' + err.message);
            });
        }}, [act.label || '…']);
        parent.appendChild(btn);
      } else if (act.type === 'modal') {
        // Modal row action — same nested-component + {row_key} substitution
        // as 'expand', but the component opens in a dialog instead of inline
        // below the row. The mounted component receives the row record as ctx
        // plus a __closeModal hook, so a submit-mode FormPanel whose Source is
        // a per-record GET becomes a prefilled "edit this row" dialog that
        // dismisses itself on a successful save (its Invalidate list refreshes
        // the table behind it — no explicit reload needed here).
        var mclasses = 'ui-row-btn';
        if (act.compact) mclasses += ' compact';
        if (act.variant) mclasses += ' ' + act.variant;
        var mbtn = el('button', {class: mclasses, onclick: function() {
          var renderCfg = JSON.parse(JSON.stringify(act.render));
          substituteRefs(renderCfg, rec);
          var handle = window.uiOpenSimpleModal({
            title: act.label || 'Edit',
            width: act.width || '520px',
            mount: function(body) {
              var childCtx = {};
              for (var k in rec) childCtx[k] = rec[k];
              // Deferred through the closure: handle is assigned when
              // uiOpenSimpleModal returns, before any save can fire.
              childCtx.__closeModal = function() { if (handle) handle.close(); };
              mountComponent(renderCfg, body, childCtx);
            }
          });
        }}, [act.label || 'Edit']);
        parent.appendChild(mbtn);
      } else if (act.type === 'expand') {
        var btn2 = el('button', {class: 'ui-row-btn' + (act.compact ? ' compact' : ''), onclick: function() {
          // Pass the actual table row (rowEl), not parent — the expand
          // panel must insert as a sibling of the row in the list,
          // not as a child of the actions container.
          toggleExpand(rec, rowKey, ai, act, rowEl);
        }}, [act.label || 'More']);
        parent.appendChild(btn2);
      }
    }

    function toggleExpand(rec, rowKey, ai, act, row) {
      openExpansions[rowKey] = openExpansions[rowKey] || {};
      if (openExpansions[rowKey][ai]) {
        openExpansions[rowKey][ai].remove();
        delete openExpansions[rowKey][ai];
        return;
      }
      // Close any other open expansions on the same row OR other rows
      // (one-at-a-time keeps the page tidy).
      Object.keys(openExpansions).forEach(function(rk) {
        Object.keys(openExpansions[rk]).forEach(function(idx) {
          openExpansions[rk][idx].remove();
        });
        openExpansions[rk] = {};
      });
      var panel = el('div', {class: 'ui-expand'});
      // Substitute {row_key}-style placeholders into the nested
      // component's URLs before mounting. Then pass rec as ctx so
      // components like JSONView / RecordView can render row data
      // without re-fetching what the table already loaded.
      var renderCfg = JSON.parse(JSON.stringify(act.render));
      substituteRefs(renderCfg, rec);
      mountComponent(renderCfg, panel, rec);
      row.parentNode.insertBefore(panel, row.nextSibling);
      openExpansions[rowKey][ai] = panel;
    }

    function substituteRefs(obj, rec) {
      if (!obj || typeof obj !== 'object') return;
      for (var k in obj) {
        if (typeof obj[k] === 'string' && obj[k].indexOf('{') >= 0) {
          // keepUnknown=true — leave placeholders the outer rec
          // can't fill (e.g. inner Table's {id}) intact so the
          // inner component's later per-row substitute can resolve
          // them. Without this, every nested {id} got eagerly wiped
          // to "" at expand time and downstream URLs ended up
          // truncated (e.g. /api/memory/<chat>/  with no id).
          obj[k] = substitute(obj[k], rec, true);
        } else if (typeof obj[k] === 'object') {
          substituteRefs(obj[k], rec);
        }
      }
    }

    if (cfg.auto_refresh_ms && cfg.auto_refresh_ms > 0) {
      setInterval(function(){ reload(true); }, cfg.auto_refresh_ms);
    }
    if (cfg.pull_to_refresh) setupPTR(function(){ reload(false); });
    reload(true);

    // Surface refresh indicator into the parent section header (set
    // later by the section renderer, which finds .ui-section-h-r).
    setTimeout(function(){
      var section = listEl.closest('.ui-section');
      if (section) {
        var h = section.querySelector('.ui-section-h-r');
        if (h) refreshIndicator = h;
      }
    }, 0);
    return listEl;
  };

  components.history_panel = function(cfg) {
    var panel = el('div', {class: 'ui-history'}, ['Loading…']);
    var roleField = cfg.role_field || 'role';
    var textField = cfg.text_field || 'text';
    var whoField = cfg.who_field || 'display_name';
    var timeField = cfg.time_field || 'timestamp';
    var aiTag = cfg.assistant_tag || 'assistant';
    fetchJSON(cfg.source).then(function(msgs) {
      panel.innerHTML = '';
      panel.appendChild(el('div', {class: 'ui-history-h'}, [cfg.header || 'Recent messages']));
      if (!msgs || !msgs.length) {
        panel.appendChild(el('div', {class: 'ui-history-empty'}, [cfg.empty_text || 'No messages yet.']));
        return;
      }
      var slice = cfg.max_messages > 0 ? msgs.slice(-cfg.max_messages) : msgs;
      slice.forEach(function(m) {
        var isAI = m[roleField] === aiTag;
        var label = isAI ? 'AI' : (m[whoField] || m.handle || 'them');
        var ts = m[timeField] ? ' · ' + relTime(m[timeField]) : '';
        panel.appendChild(el('div', {class: 'ui-history-msg' + (isAI ? ' ai' : '')}, [
          el('div', {class: 'ui-history-who'}, [label + ts]),
          el('div', {class: 'ui-history-body'}, [m[textField] || '(no text)']),
        ]));
      });
    }).catch(function(err){ panel.textContent = 'Failed: ' + err.message; });
    return panel;
  };

  components.member_editor = function(cfg) {
    var field    = cfg.field         || 'members';
    var handleF  = cfg.handle_field  || 'handle';
    var nameF    = cfg.name_field    || 'name';
    var aliasF   = cfg.aliases_field || 'aliases';
    var ahField  = cfg.alias_handles_field || '';
    var method   = cfg.method        || 'POST';

    var wrap = el('div', {class: 'ui-mem'});
    var rowsHost = el('div', {class: 'ui-mem'});
    var addBtn = el('button', {class: 'ui-mem-add'}, ['+ Add member']);
    var aliasHandlesRow = null;
    var aliasHandlesInput = null;
    if (ahField) {
      aliasHandlesRow = el('div', {class: 'ui-mem-aliasrow'});
      aliasHandlesRow.appendChild(el('label', {}, ['Conversation alias handles (comma-separated phone/emails that map to this same chat):']));
      aliasHandlesInput = el('input', {type: 'text', placeholder: '+15551234567, alice@example.com'});
      aliasHandlesRow.appendChild(aliasHandlesInput);
    }

    wrap.appendChild(rowsHost);
    wrap.appendChild(addBtn);
    if (aliasHandlesRow) wrap.appendChild(aliasHandlesRow);

    // Local working copy of the members array. Saved (PATCHed) back to
    // the server on every blur — debouncing felt fragile here because
    // a row removal also has to round-trip immediately.
    var members = [];

    function save() {
      var body = {};
      body[field] = members;
      if (aliasHandlesInput) {
        body[ahField] = (aliasHandlesInput.value || '')
          .split(',')
          .map(function(s){ return s.trim(); })
          .filter(function(s){ return s; });
      }
      fetch(cfg.post_to, {
        method: method,
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).catch(function(err){ showToast('Save failed: ' + err.message); });
    }
    function render() {
      rowsHost.innerHTML = '';
      if (!members.length) {
        rowsHost.appendChild(el('div', {class: 'ui-mem-empty'}, [cfg.empty_text || 'No members yet. Add one below.']));
        return;
      }
      members.forEach(function(m, idx) {
        var row = el('div', {class: 'ui-mem-row'});
        var hInput = el('input', {type: 'text', placeholder: '+15551234567 or alice@example.com', value: m[handleF] || ''});
        var nInput = el('input', {type: 'text', placeholder: 'Display name', value: m[nameF] || ''});
        var aInput = el('input', {class: 'ui-mem-aliases', type: 'text',
          placeholder: 'aliases (comma-sep)',
          value: (m[aliasF] || []).join(', ')});
        var del = el('button', {class: 'ui-mem-del', title: 'Remove'}, ['×']);
        hInput.addEventListener('blur', function() {
          members[idx][handleF] = hInput.value.trim();
          save();
        });
        nInput.addEventListener('blur', function() {
          members[idx][nameF] = nInput.value.trim();
          save();
        });
        aInput.addEventListener('blur', function() {
          members[idx][aliasF] = aInput.value
            .split(',').map(function(s){ return s.trim(); })
            .filter(function(s){ return s; });
          save();
        });
        del.addEventListener('click', function() {
          members.splice(idx, 1);
          render();
          save();
        });
        row.appendChild(hInput);
        row.appendChild(nInput);
        row.appendChild(aInput);
        row.appendChild(del);
        rowsHost.appendChild(row);
      });
    }
    function load() {
      fetchJSON(cfg.source).then(function(rec) {
        members = (rec && rec[field]) ? rec[field].slice() : [];
        // Normalize: ensure each member has a handle/name/aliases shape.
        members = members.map(function(m) {
          var x = {};
          x[handleF] = m[handleF] || '';
          x[nameF]   = m[nameF]   || '';
          x[aliasF]  = Array.isArray(m[aliasF]) ? m[aliasF].slice() : [];
          return x;
        });
        if (aliasHandlesInput) {
          var ah = (rec && rec[ahField]) || [];
          aliasHandlesInput.value = Array.isArray(ah) ? ah.join(', ') : '';
          aliasHandlesInput.addEventListener('blur', save);
        }
        render();
      }).catch(function(err) {
        rowsHost.innerHTML = '';
        rowsHost.appendChild(el('div', {class: 'ui-mem-empty'}, ['Failed to load: ' + err.message]));
      });
    }
    addBtn.addEventListener('click', function() {
      var x = {};
      x[handleF] = ''; x[nameF] = ''; x[aliasF] = [];
      members.push(x);
      render();
    });

    load();
    return wrap;
  };

  components.key_manager = function(cfg) {
    var nameF    = cfg.name_field      || 'name';
    var idF      = cfg.id_field        || 'id';
    var secretF  = cfg.secret_field    || 'key';
    var createdF = cfg.created_field   || 'created';
    var lastSeenF = cfg.last_seen_field || 'last_seen';
    var newLbl   = cfg.new_label       || '+ New API key';

    var wrap     = el('div', {class: 'ui-keys'});
    var actions  = el('div', {class: 'ui-keys-actions'});
    var newBtn   = el('button', {class: 'ui-keys-new'}, [newLbl]);
    actions.appendChild(newBtn);

    var formWrap     = el('div', {class: 'ui-keys-form', style: 'display:none'});
    var formInput    = el('input', {type: 'text', placeholder: 'Friendly name, e.g. MacBook bridge'});
    var formCreate   = el('button', {}, ['Create']);
    var formCancel   = el('button', {class: 'secondary'}, ['Cancel']);
    formWrap.appendChild(formInput);
    formWrap.appendChild(formCreate);
    formWrap.appendChild(formCancel);

    var revealed = el('div', {class: 'ui-keys-revealed', style: 'display:none'});

    var listEl = el('div', {class: 'ui-keys-list'}, ['Loading…']);

    wrap.appendChild(actions);
    wrap.appendChild(formWrap);
    wrap.appendChild(revealed);
    wrap.appendChild(listEl);

    function showForm() {
      newBtn.style.display = 'none';
      revealed.style.display = 'none';
      formWrap.style.display = '';
      formInput.value = '';
      formInput.focus();
    }
    function hideForm() {
      formWrap.style.display = 'none';
      newBtn.style.display = '';
    }
    function showRevealed(rec) {
      revealed.innerHTML = '';
      revealed.appendChild(el('div', {class: 'ui-keys-revealed-h'}, ['Key created — copy now, it will not be shown again']));
      if (cfg.secret_hint) revealed.appendChild(el('div', {class: 'ui-keys-revealed-hint'}, [cfg.secret_hint]));
      var secret = rec[secretF] || '';
      revealed.appendChild(el('div', {class: 'ui-keys-revealed-secret'}, [secret]));
      var copyBtn = el('button', {}, ['Copy']);
      copyBtn.addEventListener('click', function() {
        navigator.clipboard.writeText(secret).then(function() {
          var orig = copyBtn.textContent;
          copyBtn.textContent = 'Copied!';
          copyBtn.classList.add('copied');
          setTimeout(function() {
            copyBtn.textContent = orig;
            copyBtn.classList.remove('copied');
          }, 1500);
        });
      });
      var dismissBtn = el('button', {}, ['Dismiss']);
      dismissBtn.addEventListener('click', function() {
        revealed.style.display = 'none';
        revealed.innerHTML = '';
        loadList();
      });
      revealed.appendChild(el('div', {class: 'ui-keys-revealed-row'}, [copyBtn, dismissBtn]));
      revealed.style.display = '';
    }
    function loadList() {
      listEl.innerHTML = 'Loading…';
      fetchJSON(cfg.list_url).then(function(items) {
        listEl.innerHTML = '';
        items = items || [];
        if (!items.length) {
          listEl.appendChild(el('div', {class: 'ui-keys-empty'}, [cfg.empty_text || 'No keys yet.']));
          return;
        }
        items.forEach(function(rec) {
          var row     = el('div', {class: 'ui-keys-row'});
          var name    = el('div', {class: 'ui-keys-row-name'}, [rec[nameF] || '(unnamed)']);
          var metaBits = [];
          if (rec[createdF])  metaBits.push('created ' + relTime(rec[createdF]));
          if (rec[lastSeenF]) metaBits.push('last seen ' + relTime(rec[lastSeenF]));
          var meta    = el('div', {class: 'ui-keys-row-meta'}, [metaBits.join(' · ') || '—']);
          var del     = el('button', {class: 'ui-keys-row-del', title: 'Delete this key'}, ['×']);
          del.addEventListener('click', async function() {
            if (!(await window.uiConfirm('Delete this API key? Any client using it will stop working.'))) return;
            del.disabled = true;
            var url = cfg.delete_url.replace(/\/+$/, '') + '/' + encodeURIComponent(rec[idF]);
            fetch(url, {method: 'DELETE'}).then(function(r) {
              if (!r.ok && r.status !== 204) {
                return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
              }
              loadList();
            }).catch(function(err) {
              window.uiAlert('Delete failed: ' + err.message);
              del.disabled = false;
            });
          });
          row.appendChild(name);
          row.appendChild(meta);
          row.appendChild(del);
          listEl.appendChild(row);
        });
      }).catch(function(err) {
        listEl.innerHTML = '';
        listEl.appendChild(el('div', {class: 'ui-keys-empty'}, ['Failed to load: ' + err.message]));
      });
    }
    function doCreate() {
      var name = (formInput.value || '').trim();
      if (!name) { formInput.focus(); return; }
      formCreate.disabled = true; formCancel.disabled = true;
      fetch(cfg.create_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({name: name}),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(rec) {
        hideForm();
        showRevealed(rec || {});
      }).catch(function(err) {
        window.uiAlert('Create failed: ' + err.message);
      }).then(function() {
        formCreate.disabled = false; formCancel.disabled = false;
      });
    }

    newBtn.addEventListener('click', showForm);
    formCancel.addEventListener('click', hideForm);
    formCreate.addEventListener('click', doCreate);
    formInput.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter') { ev.preventDefault(); doCreate(); }
      else if (ev.key === 'Escape') { ev.preventDefault(); hideForm(); }
    });

    loadList();
    return wrap;
  };

  // chip_picker — the framework's one multi-select-over-a-record-field
  // picker. cfg.mode "chips" (default) toggles every option inline;
  // "attach" shows the selection as removable pills with a "+ Add"
  // reveal-list. Options come from cfg.options_source (a flat array or a
  // shaped object — see extractOptions). Selection comes from either a
  // fetched record (cfg.record_source + cfg.field) or the options
  // response itself (cfg.attached_field). Saves either replace the whole
  // record or POST {cfg.save_key: [values]} to a dedicated endpoint.
  //
  // LOCAL mode: when no cfg.post_to is given, the picker persists NOTHING —
  // it seeds its selection from cfg.value (an array) and reports every change
  // via cfg.on_change(values). Use this to embed the picker as one field of a
  // larger form the caller saves itself (e.g. a create-record modal where the
  // record doesn't exist yet), instead of a dedicated attach endpoint.
  components.chip_picker = function(cfg) {
    var mode       = cfg.mode || 'chips';
    var nameField  = cfg.name_field  || 'name';
    var valueField = cfg.value_field || nameField; // key whose value is STORED
    var labelField = cfg.label_field || nameField; // display key
    var descField  = cfg.desc_field  || 'desc';
    var wrap = el('div', {class: mode === 'attach' ? 'ui-chip-picker' : 'ui-chips'}, ['Loading…']);

    // extractOptions unwraps a shaped list response to its array. Endpoints
    // use a mix of top-level keys; a flat array passes straight through.
    function extractOptions(raw) {
      if (Array.isArray(raw)) return raw;
      if (!raw) return [];
      if (cfg.records_field && Array.isArray(raw[cfg.records_field])) return raw[cfg.records_field];
      var keys = ['items', 'records', 'sources', 'collections', 'skills', 'pipelines', 'available'];
      for (var i = 0; i < keys.length; i++) { if (Array.isArray(raw[keys[i]])) return raw[keys[i]]; }
      return [];
    }
    function valueOf(opt) { return opt[valueField]; }
    function labelOf(opt) { return opt[labelField] || opt[nameField] || valueOf(opt); }

    // LOCAL mode: no endpoint to save to — the caller owns persistence and
    // just wants the live selection via cfg.on_change.
    var local = !cfg.post_to;

    // Only attach mode with a dedicated endpoint (attached_field) skips
    // the record fetch — chips and full-record attach both need it. Local
    // mode never fetches a record (its selection seeds from cfg.value).
    var needRecord = !local && !cfg.attached_field && !!cfg.record_source;
    var record = {};
    Promise.all([
      fetchJSON(cfg.options_source),
      needRecord ? fetchJSON(cfg.record_source) : Promise.resolve(null)
    ]).then(function(r) {
      var options = extractOptions(r[0]);
      record = r[1] || {};
      // Current selection: caller-seeded (local), the options response
      // (attached_field), or the fetched record's array field.
      var selected = (local ? cfg.value
                      : (cfg.attached_field ? (r[0] && r[0][cfg.attached_field]) : record[cfg.field])) || [];
      selected = selected.slice();

      // persist writes the current selection back. In local mode nothing is
      // POSTed — the caller is handed the selection via cfg.on_change and saves
      // it itself. Otherwise: {save_key: sel} to a dedicated endpoint, or the
      // patched record (PATCH = field only, POST = whole record).
      function persist() {
        if (local) {
          if (cfg.on_change) cfg.on_change(selected.slice());
          return Promise.resolve();
        }
        var body;
        if (cfg.save_key) { body = {}; body[cfg.save_key] = selected; }
        else if (cfg.method && cfg.method.toUpperCase() === 'PATCH') { body = {}; body[cfg.field] = selected; }
        else { record[cfg.field] = selected; body = record; }
        return fetchJSON(cfg.post_to, {
          method: cfg.method || 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(body)
        });
      }

      wrap.innerHTML = '';
      if (mode === 'attach') renderAttach(options, selected, persist);
      else renderChips(options, selected, persist);
    }).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });

    // --- chips mode: inline toggle chips (unchanged behavior) ---
    function renderChips(options, selected, persist) {
      options.forEach(function(opt) {
        var chip = el('span', {
          class: 'ui-chip' + (selected.indexOf(valueOf(opt)) >= 0 ? ' on' : ''),
          title: opt[descField] || ''
        }, [labelOf(opt)]);
        chip.addEventListener('click', function() {
          var i = selected.indexOf(valueOf(opt));
          if (i >= 0) { selected.splice(i, 1); chip.classList.remove('on'); }
          else { selected.push(valueOf(opt)); chip.classList.add('on'); }
          persist().catch(function(err){ showToast('Save failed: ' + err.message); });
        });
        wrap.appendChild(chip);
      });
    }

    // --- attach mode: selected-as-pills + "+ Add" reveal-list ---
    function renderAttach(options, selected, persist) {
      if (cfg.intro) wrap.appendChild(el('div', {class: 'ui-cp-intro', text: cfg.intro}));
      if (!options.length) {
        wrap.appendChild(el('div', {class: 'ui-cp-empty', text: cfg.empty_text || '(nothing to show)'}));
        return;
      }
      var byVal = {};
      options.forEach(function(o){ byVal[valueOf(o)] = o; });
      var pool = options.filter(function(o){ return !o.disabled; });
      var noun = cfg.noun || 'item';
      var isSel = {};
      selected.forEach(function(v){ isSel[v] = true; });

      var pills = el('div', {class: 'ui-cp-pills'});
      wrap.appendChild(pills);
      var addBtn = el('button', {type: 'button', class: 'ui-row-btn ui-cp-add'});
      wrap.appendChild(addBtn);
      var listWrap = el('div', {class: 'ui-cp-list', style: 'display:none'});
      wrap.appendChild(listWrap);
      var open = false;

      // toggle mutates selection, re-renders, and rolls back on save failure.
      function toggle(val, on) {
        if (on) { isSel[val] = true; selected.push(val); }
        else { delete isSel[val]; var i = selected.indexOf(val); if (i >= 0) selected.splice(i, 1); }
        renderPills(); renderList();
        persist().catch(function(err){
          showToast('Save failed: ' + err.message);
          if (on) { delete isSel[val]; var j = selected.indexOf(val); if (j >= 0) selected.splice(j, 1); }
          else { isSel[val] = true; selected.push(val); }
          renderPills(); renderList();
        });
      }

      function renderPills() {
        pills.innerHTML = '';
        var vals = selected.slice();
        if (!vals.length) { pills.appendChild(el('span', {class: 'ui-cp-none', text: 'None selected yet.'})); return; }
        vals.forEach(function(v){
          var opt = byVal[v];
          var pill = el('span', {class: 'ui-cp-pill'}, [(opt && labelOf(opt)) || v]);
          var x = el('span', {class: 'ui-cp-pill-x', title: 'Remove', text: '×'});
          x.addEventListener('click', function(){ toggle(v, false); });
          pill.appendChild(x);
          pills.appendChild(pill);
        });
      }

      function addRow(opt, host) {
        var row = el('div', {class: 'ui-cp-row'});
        var meta = el('div', {class: 'ui-cp-row-meta'});
        meta.appendChild(el('div', {class: 'ui-cp-name', text: labelOf(opt)}));
        var desc = (opt[descField] || '').trim();
        if (desc) { if (desc.length > 200) desc = desc.slice(0, 200) + '…'; meta.appendChild(el('div', {class: 'ui-cp-desc', text: desc})); }
        var bits = [];
        (cfg.meta_fields || []).forEach(function(k){ if (opt[k] != null) bits.push(opt[k] + ' ' + k); });
        if (bits.length) meta.appendChild(el('div', {class: 'ui-cp-metaline', text: bits.join(' · ')}));
        var add = el('button', {type: 'button', class: 'ui-cp-add-btn', title: 'Add', text: '+'});
        add.addEventListener('click', function(){ toggle(valueOf(opt), true); });
        row.appendChild(meta); row.appendChild(add);
        host.appendChild(row);
      }

      function renderList() {
        listWrap.style.display = open ? 'block' : 'none';
        if (!open) return;
        listWrap.innerHTML = '';
        var avail = pool.filter(function(o){ return !isSel[valueOf(o)]; });
        if (!avail.length) { listWrap.appendChild(el('div', {class: 'ui-cp-empty', text: 'Everything is added.'})); return; }
        if (cfg.group_by_field) {
          var order = [], groups = {};
          avail.forEach(function(o){ var g = o[cfg.group_by_field] || ''; if (!groups[g]) { groups[g] = []; order.push(g); } groups[g].push(o); });
          order.forEach(function(g){
            if (g) listWrap.appendChild(el('div', {class: 'ui-cp-group', text: g}));
            groups[g].forEach(function(o){ addRow(o, listWrap); });
          });
        } else {
          avail.forEach(function(o){ addRow(o, listWrap); });
        }
      }

      addBtn.addEventListener('click', function(){ open = !open; addBtn.textContent = open ? 'Hide list' : ('+ Add ' + noun); renderList(); });
      addBtn.textContent = '+ Add ' + noun;
      renderPills(); renderList();
    }

    return wrap;
  };

  components.form_panel = function(cfg, ctx) {
    var wrap = el('div', {class: 'ui-form'});
    var current = {};
    var debounceTimers = {}; // field -> setTimeout id
    var savingIndicator = el('span', {class: 'ui-form-saving'}, ['Saving…']);
    var fieldEls = {}; // field name -> rendered field wrap (for show_when)
    // Submit-button mode flips the panel from auto-save-on-change to
    // explicit-submit. Used by create-style forms ("fill in a name,
    // click Create, navigate to the new record"). The auto-save
    // pattern stays the default — every existing edit-in-place form
    // works unchanged.
    var submitMode = !!cfg.submit_label;

    // fieldSetters maps a field name to a type-aware function that
    // applies a new value AND persists. The "✨ Suggest" button reads
    // from this so the framework doesn't need a giant type-switch in
    // its handler — each field-type branch registers the right way to
    // accept a suggestion (parsing for number, list rebuild for rules,
    // straight assignment for text/textarea).
    var fieldSetters = {};

    function save(field, value) {
      if (submitMode) {
        // In submit-button mode field changes just update local
        // state — the POST fires once when the user clicks submit.
        current[field] = value;
        applyVisibility();
        return;
      }
      current[field] = value;
      // PATCH endpoints take just the changed field; POST gets full record.
      var isPatch = cfg.method && cfg.method.toUpperCase() === 'PATCH';
      var body = isPatch ? (function(){ var o = {}; o[field] = value; return o; })() : current;
      savingIndicator.classList.add('show');
      // Optional separate write target — used when the GET endpoint
      // that returned the current record shape isn't the right place
      // to POST updates (e.g. edit form GETs /api/record/{id} but
      // posts to /api/records list-endpoint that handles both create
      // and update by ID).
      var postURL = cfg.post_url || cfg.source;
      fetchJSON(postURL, {
        method: cfg.method || 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body)
      }).then(function(){
        setTimeout(function(){ savingIndicator.classList.remove('show'); }, 300);
        window.uiInvalidateSaved(cfg);
      }).catch(function(err){
        savingIndicator.classList.remove('show');
        showToast('Save failed: ' + err.message);
      });
      applyVisibility();
    }

    // matchesShowWhen evaluates a show_when expression against the
    // form's current values. Three accepted shapes:
    //   "field"                — show when current[field] is truthy
    //   "field:value"          — show when current[field] === value
    //   "field:v1|v2"          — show when current[field] ∈ {v1, v2}
    // Multiple conditions can be chained with ";" and ALL must match.
    // Backward compatible: a plain "field" still works.
    function matchesShowWhen(expr) {
      if (!expr) return true;
      var clauses = expr.split(';');
      for (var i = 0; i < clauses.length; i++) {
        var c = clauses[i].trim();
        if (!c) continue;
        var colon = c.indexOf(':');
        if (colon < 0) {
          if (!current[c]) return false;
          continue;
        }
        var fld = c.substring(0, colon);
        var rhs = c.substring(colon + 1);
        var actual = current[fld];
        var opts = rhs.split('|');
        var hit = false;
        for (var j = 0; j < opts.length; j++) {
          if (String(actual) === opts[j]) { hit = true; break; }
        }
        if (!hit) return false;
      }
      return true;
    }
    function applyVisibility() {
      cfg.fields.forEach(function(f) {
        var node = fieldEls[f.field];
        if (!node) return;
        if (f.show_when) {
          node.style.display = matchesShowWhen(f.show_when) ? '' : 'none';
        }
      });
    }

    function debounced(field, value) {
      clearTimeout(debounceTimers[field]);
      debounceTimers[field] = setTimeout(function(){ save(field, value); }, 600);
    }

    function renderField(f) {
      var fieldWrap = el('div', {class: 'ui-form-field'});
      // Tag the wrap with the field name so app-side JS can find a
      // specific field for show/hide / grouping logic without
      // relying on label-text matching.
      if (f.field) fieldWrap.dataset.field = f.field;
      var t = f.type || 'text';

      // Header field: section divider, no input, no value binding.
      // Splits a long FormPanel into visually grouped chunks without
      // breaking the single-save pattern (one FormPanel = one record).
      // Uses Label for the section title and Help for an optional
      // subtitle under it.
      if (t === 'header') {
        fieldWrap.classList.add('ui-form-section');
        if (f.label) {
          fieldWrap.appendChild(el('div', {class: 'ui-form-section-title'}, [f.label]));
        }
        if (f.help) {
          fieldWrap.appendChild(el('div', {class: 'ui-form-section-help'}, [f.help]));
        }
        return fieldWrap;
      }

      // Hidden field: contributes its default to the save payload but
      // renders no visible input. Use for context-derived values the
      // page knows up front (e.g. "this new record is owned by X")
      // that must ride on the POST without offering an edit affordance.
      // The default seeds the form state immediately so the first save
      // POST includes it.
      if (t === 'hidden') {
        if (f.field && (f.default !== undefined && f.default !== null && f.default !== '')) {
          current[f.field] = f.default;
        }
        fieldWrap.style.display = 'none';
        return fieldWrap;
      }

      if (f.label) fieldWrap.appendChild(el('label', {class: 'ui-form-label'}, [f.label]));

      var input;
      // toggleHandled — set by the 'toggle' branch when it has already
      // placed the input into a header row alongside the label. Causes
      // the generic fieldWrap.appendChild(input) below to be skipped
      // so the switch isn't double-inserted.
      var toggleHandled = false;
      var initial = current[f.field];
      if (initial == null) initial = '';

      if (t === 'textarea') {
        input = el('textarea', {class: 'ui-form-textarea', rows: String(f.rows || 3), placeholder: f.placeholder || ''});
        // JSON-stringify when the field's value is an object or
        // array — without this, String([{...}]) produces
        // "[object Object]" in the textarea (a common gotcha for
        // fields holding structured data like agent intake_form).
        // Plain strings still go through String() as before. Save
        // path is unchanged: the textarea's string value posts back
        // as-is; the server parses JSON when it expects structured
        // input (intake_form's intakeFormFromArgs already accepts
        // both shapes).
        if (initial != null && typeof initial === 'object') {
          try { input.value = JSON.stringify(initial, null, 2); }
          catch (_) { input.value = String(initial); }
        } else {
          input.value = String(initial);
        }
        input.addEventListener('input', function(){ debounced(f.field, input.value); });
        input.addEventListener('blur', function(){
          clearTimeout(debounceTimers[f.field]);
          if (current[f.field] !== input.value) save(f.field, input.value);
        });
      } else if (t === 'select') {
        input = el('select', {class: 'ui-form-select'});
        (f.options || []).forEach(function(o) {
          var opt = el('option', {value: o.value}, [o.label || o.value]);
          if (String(initial) === String(o.value)) opt.selected = true;
          input.appendChild(opt);
        });
        // Submit-mode + no initial value: the browser auto-selects
        // the first option visually but current[field] stays
        // undefined until the user changes the dropdown. Seed
        // current with the first option's value so a form that
        // accepts the default still ships a value on submit.
        if ((initial === undefined || initial === null || initial === '') &&
            f.options && f.options.length > 0) {
          current[f.field] = f.options[0].value;
        }
        input.addEventListener('change', function(){ save(f.field, input.value); });
      } else if (t === 'checklist') {
        // Multi-select checkbox list bound to a string array. Same
        // shape as "select" (uses f.options as the choice set) but
        // multi-pick — for "allowlist N items from this fixed set"
        // configs where the operator checks the ones to include and the
        // saved value is the list of checked option Values.
        // Saves immediately on each toggle (no debounce — discrete).
        // Empty value renders as no boxes checked.
        input = el('div', {class: 'ui-form-checklist'});
        input.style.cssText = 'display:flex;flex-direction:column;gap:0.35rem';
        var initialList = Array.isArray(initial) ? initial.slice() : [];
        var checkedSet = {};
        initialList.forEach(function(v){ checkedSet[String(v)] = true; });
        (f.options || []).forEach(function(o) {
          var item = el('label', {class: 'ui-form-checklist-item'});
          item.style.cssText = 'display:flex;align-items:flex-start;gap:0.5rem;padding:0.2rem 0;cursor:pointer';
          var box = document.createElement('input');
          box.type = 'checkbox';
          box.value = o.value;
          box.style.marginTop = '0.2rem';
          if (checkedSet[String(o.value)]) box.checked = true;
          var lbl = document.createElement('div');
          lbl.style.cssText = 'flex:1;font-size:0.88rem;color:var(--text)';
          lbl.textContent = o.label || o.value;
          if (o.help) {
            var sub = document.createElement('div');
            sub.style.cssText = 'font-size:0.74rem;color:var(--text-mute);margin-top:0.1rem';
            sub.textContent = o.help;
            lbl.appendChild(sub);
          }
          item.appendChild(box);
          item.appendChild(lbl);
          input.appendChild(item);
          box.addEventListener('change', function(){
            var picked = [];
            input.querySelectorAll('input[type=checkbox]:checked').forEach(function(b){
              picked.push(b.value);
            });
            save(f.field, picked);
          });
        });
        if (!f.options || f.options.length === 0) {
          var emp = document.createElement('div');
          emp.style.cssText = 'color:var(--text-mute);font-style:italic;font-size:0.78rem;padding:0.3rem 0';
          emp.textContent = f.placeholder || '(no options available)';
          input.appendChild(emp);
        }
      } else if (t === 'toggle') {
        // iOS-style switch as a form field. Saves immediately on
        // change (no debounce) since toggles are discrete decisions.
        input = el('input', {type: 'checkbox', class: 'ui-switch'});
        input.checked = !!initial;
        input.addEventListener('change', function(){ save(f.field, input.checked); });
        // Layout: switch on the FAR LEFT (fixed), label flows to the
        // right of it (flex-grow), help (if any) on its own line
        // BELOW. Reads naturally as [decision-control] [what it
        // controls] — checkbox-style scan order. Switch stays
        // anchored at the left edge regardless of label / help
        // length, so a column of toggles aligns cleanly.
        // .ui-form-toggle-field adds extra top-padding for the
        // BR-above-style separation that other forms use.
        fieldWrap.classList.add('ui-form-toggle-field');
        var existingLabel = fieldWrap.firstChild;
        if (existingLabel && existingLabel.classList && existingLabel.classList.contains('ui-form-label')) {
          fieldWrap.removeChild(existingLabel);
          existingLabel.style.marginBottom = '0';
          existingLabel.style.flex = '1';
          existingLabel.style.cursor = 'pointer';
          // Clicking the label toggles the switch — preserves the
          // natural <label> behavior even when we restructure the DOM.
          existingLabel.addEventListener('click', function() { input.click(); });
          var headerRow = el('div', {style: 'display:flex;align-items:center;gap:0.7rem'},
                             [input, existingLabel]);
          fieldWrap.insertBefore(headerRow, fieldWrap.firstChild);
        } else {
          // No label — render just the switch left-aligned on its own row.
          var hdr = el('div', {style: 'display:flex;justify-content:flex-start'}, [input]);
          fieldWrap.appendChild(hdr);
        }
        toggleHandled = true;
      } else if (t === 'tags') {
        // Tag-style array editor — values render as compact chips
        // with × removers, plus an inline input that accepts new
        // entries (Enter or blur commits). Saves the field as a
        // pure JSON array, so endpoints can keep their natural
        // shape ({keywords:[...]}) instead of round-tripping
        // through a newline-joined string. Compact horizontal flow
        // matches the legacy autoblog keyword UI.
        input = el('div', {class: 'ui-tags'});
        var values = Array.isArray(initial) ? initial.slice() : [];
        function persistTags() {
          // Drop empties + dedup (case-insensitive). Avoid noisy
          // duplicate saves — only POST when the array actually
          // changed from the loaded record.
          var clean = [];
          var seen = {};
          values.forEach(function(v) {
            var t = String(v || '').trim();
            if (!t) return;
            var k = t.toLowerCase();
            if (seen[k]) return;
            seen[k] = true;
            clean.push(t);
          });
          var prev = Array.isArray(current[f.field]) ? current[f.field] : [];
          var changed = prev.length !== clean.length;
          for (var i = 0; !changed && i < clean.length; i++) {
            if (prev[i] !== clean[i]) changed = true;
          }
          if (changed) save(f.field, clean);
        }
        function renderTags() {
          input.innerHTML = '';
          if (!values.length) {
            input.appendChild(el('div', {class: 'ui-tags-empty'}, [f.placeholder || 'No tags yet — add one below.']));
          }
          values.forEach(function(v, idx) {
            var chip = el('span', {class: 'ui-tag'});
            chip.appendChild(document.createTextNode(v));
            var del = el('button', {class: 'ui-tag-del', type: 'button', title: 'Remove'}, ['×']);
            del.addEventListener('click', function() {
              values.splice(idx, 1);
              renderTags();
              persistTags();
            });
            chip.appendChild(del);
            input.appendChild(chip);
          });
          var addInput = el('input', {type: 'text', class: 'ui-tag-input',
            placeholder: f.add_placeholder || 'Add tag…'});
          function commit() {
            var v = addInput.value.trim();
            if (!v) return;
            // Clear the value BEFORE renderTags rebuilds the DOM.
            // Without this, the now-orphan addInput's blur listener
            // (triggered as renderTags rips the input out of the
            // DOM) re-fires commit() on the still-populated value
            // and the tag gets added twice. Clearing first means
            // the orphan blur sees an empty value and early-returns.
            addInput.value = '';
            values.push(v);
            renderTags();
            persistTags();
            // Re-find the input — renderTags rebuilt the DOM.
            var fresh = input.querySelector('.ui-tag-input');
            if (fresh) fresh.focus();
          }
          addInput.addEventListener('keydown', function(ev) {
            if (ev.key === 'Enter') {
              ev.preventDefault();
              commit();
            }
          });
          addInput.addEventListener('blur', commit);
          input.appendChild(addInput);
        }
        renderTags();
        // Suggest/template setter: accept an array (preferred) or a
        // comma/newline-separated string, rebuild the chips, and persist.
        fieldSetters[f.field] = function(v) {
          if (Array.isArray(v)) {
            values = v.map(function(x) { return String(x); });
          } else {
            values = String(v == null ? '' : v).split(/[,\n]/).map(function(x) { return x.trim(); }).filter(Boolean);
          }
          renderTags();
          persistTags();
        };
      } else if (t === 'rules') {
        // Rules-list editor — each line of the underlying string field
        // becomes a removable list item, with "+ Add rule" appending a
        // new empty input. Saves on blur as the joined text. Strips
        // common bullet/number prefixes ("1. ", "2.", "- ", "* ") on
        // load so existing free-form rules don't double-prefix.
        input = el('div', {class: 'ui-rules'});
        var rules = parseRules(String(initial));
        function persist() {
          var joined = rules.filter(function(r){ return r.trim() !== ''; }).join('\n');
          if (current[f.field] !== joined) save(f.field, joined);
        }
        function renderRules() {
          input.innerHTML = '';
          if (!rules.length) {
            input.appendChild(el('div', {class: 'ui-rules-empty'}, [f.placeholder || 'No rules yet — add one below.']));
          }
          rules.forEach(function(r, idx) {
            var row = el('div', {class: 'ui-rules-row'});
            var num = el('span', {class: 'ui-rules-num'}, [String(idx + 1) + '.']);
            var ti = el('input', {type: 'text', class: 'ui-rules-input', value: r, placeholder: 'rule…'});
            ti.addEventListener('blur', function() {
              rules[idx] = ti.value;
              persist();
            });
            ti.addEventListener('keydown', function(ev) {
              if (ev.key === 'Enter') {
                ev.preventDefault();
                rules[idx] = ti.value;
                rules.splice(idx + 1, 0, '');
                renderRules();
                persist();
                var inputs = input.querySelectorAll('.ui-rules-input');
                if (inputs[idx + 1]) inputs[idx + 1].focus();
              }
            });
            var del = el('button', {class: 'ui-rules-del', title: 'Remove this rule', type: 'button'}, ['×']);
            del.addEventListener('click', function() {
              rules.splice(idx, 1);
              renderRules();
              persist();
            });
            row.appendChild(num);
            row.appendChild(ti);
            row.appendChild(del);
            input.appendChild(row);
          });
          var addBtn = el('button', {class: 'ui-rules-add', type: 'button'}, ['+ Add rule']);
          addBtn.addEventListener('click', function() {
            rules.push('');
            renderRules();
            var inputs = input.querySelectorAll('.ui-rules-input');
            var last = inputs[inputs.length - 1];
            if (last) last.focus();
          });
          input.appendChild(addBtn);
        }
        renderRules();
        // Suggest setter: accept a string (newline-separated) and
        // rebuild the rules list from scratch. parseRules strips
        // bullet/number prefixes, which is what we want for an
        // AI-generated reply that might lead each line with "- ".
        fieldSetters[f.field] = function(v) {
          rules = parseRules(String(v));
          renderRules();
          persist();
        };
      } else if (t === 'checklist') {
        // Multi-select checkbox list. Persists as []string of checked
        // values. Options may carry "help" (subtitle below the label)
        // and "group" (rows under the same group sit beneath a header
        // — relies on options being pre-sorted by group; the renderer
        // does not re-bucket).
        input = el('div', {class: 'ui-checklist'});
        var initialArr = Array.isArray(initial) ? initial.slice() : [];
        var selected = {};
        initialArr.forEach(function(v) { selected[String(v)] = true; });
        var checkboxes = [];
        var countEl = el('span', {class: 'ui-checklist-count'});
        function refreshCount() {
          var n = 0, total = (f.options || []).length;
          (f.options || []).forEach(function(o) {
            if (selected[String(o.value)]) n++;
          });
          countEl.textContent = n + ' / ' + total + ' selected';
        }
        function persist() {
          var out = [];
          (f.options || []).forEach(function(o) {
            if (selected[String(o.value)]) out.push(o.value);
          });
          refreshCount();
          save(f.field, out);
        }
        var toolbar = el('div', {class: 'ui-checklist-toolbar'});
        var allBtn = el('button', {type: 'button', class: 'ui-checklist-toolbtn'}, ['Select all']);
        var noneBtn = el('button', {type: 'button', class: 'ui-checklist-toolbtn'}, ['Clear']);
        allBtn.addEventListener('click', function() {
          (f.options || []).forEach(function(o) { selected[String(o.value)] = true; });
          checkboxes.forEach(function(cb) { cb.checked = true; });
          persist();
        });
        noneBtn.addEventListener('click', function() {
          selected = {};
          checkboxes.forEach(function(cb) { cb.checked = false; });
          persist();
        });
        toolbar.appendChild(allBtn);
        toolbar.appendChild(noneBtn);
        toolbar.appendChild(countEl);
        input.appendChild(toolbar);

        var lastGroup = '__init__';
        (f.options || []).forEach(function(o) {
          var grp = o.group || '';
          if (grp !== lastGroup) {
            if (grp) input.appendChild(el('div', {class: 'ui-checklist-group'}, [grp]));
            lastGroup = grp;
          }
          var row = el('label', {class: 'ui-checklist-row'});
          var cb = el('input', {type: 'checkbox', class: 'ui-checklist-cb'});
          cb.checked = !!selected[String(o.value)];
          cb.addEventListener('change', function() {
            selected[String(o.value)] = cb.checked;
            persist();
          });
          checkboxes.push(cb);
          row.appendChild(cb);
          var lbl = el('div', {class: 'ui-checklist-lbl'});
          lbl.appendChild(el('span', {class: 'ui-checklist-name'}, [o.label || o.value]));
          if (o.help) {
            lbl.appendChild(el('span', {class: 'ui-checklist-help'}, [o.help]));
          }
          row.appendChild(lbl);
          input.appendChild(row);
        });
        refreshCount();
      } else if (t === 'number') {
        input = el('input', {type: 'number', class: 'ui-form-input', placeholder: f.placeholder || ''});
        if (f.min) input.min = String(f.min);
        if (f.max) input.max = String(f.max);
        if (f.decimals > 0) input.step = String(Math.pow(10, -f.decimals));
        input.value = (initial === '' || initial == null) ? '' : String(initial);
        input.addEventListener('change', function(){
          var n = (f.decimals > 0) ? parseFloat(input.value) : parseInt(input.value, 10);
          save(f.field, isNaN(n) ? 0 : n);
        });
      } else if (t === 'file') {
        // Client-side file field: the picked file is read as TEXT in the
        // browser (no upload, no endpoint) and its contents become this
        // field's value in the submit body. Shows the filename as
        // confirmation rather than dumping the raw text — use for
        // "import this .json" flows where the file content IS the value.
        // f.accept sets the picker filter. Registers its own setter so
        // template/suggest paths (which write a string) still work.
        input = el('div', {class: 'ui-form-file'});
        var fileField = el('input', {type: 'file', class: 'ui-form-file-input'});
        if (f.accept) fileField.accept = f.accept;
        var fileName = el('span', {class: 'ui-form-file-name',
          style: 'margin-left:0.5rem;font-size:0.8rem;color:var(--text-mute)'});
        fileField.addEventListener('change', function(){
          var file = fileField.files && fileField.files[0];
          if (!file) { fileName.textContent = ''; save(f.field, ''); return; }
          var reader = new FileReader();
          reader.onload = function(){
            save(f.field, reader.result == null ? '' : String(reader.result));
            fileName.textContent = file.name;
          };
          reader.onerror = function(){
            showToast('Could not read file: ' + (reader.error && reader.error.message || 'error'));
          };
          reader.readAsText(file);
        });
        input.appendChild(fileField);
        input.appendChild(fileName);
        fieldSetters[f.field] = function(v){ save(f.field, String(v == null ? '' : v)); };
      } else {
        // text, tel, anything else
        input = el('input', {type: t, class: 'ui-form-input', placeholder: f.placeholder || ''});
        input.value = String(initial);
        input.addEventListener('input', function(){ debounced(f.field, input.value); });
        input.addEventListener('blur', function(){
          clearTimeout(debounceTimers[f.field]);
          if (current[f.field] !== input.value) save(f.field, input.value);
        });
      }
      // Chips row above the input — declared via f.chips_source.
      // Each chip applies a preset value to the input and saves.
      // "+ New" optionally opens a create dialog with AI Assist.
      // Append chipsHost FIRST then input — DOM order matches visual
      // order ("chips on top of input"). Earlier code did
      // insertBefore(chipsHost, input) before input was attached,
      // which threw "node is not a child of this node" when the
      // chip-source field rendered (settings + persona).
      if (f.chips_source) {
        var chipsHost = el('div', {class: 'ui-form-chips'});
        fieldWrap.appendChild(chipsHost);
        renderFormChips(chipsHost, f, input);
      }
      // Static inline presets — small one-click fills above the input.
      // Renders as a row of mini-chips; click writes the preset value
      // into the field and saves via the same path as a typed change.
      if (Array.isArray(f.presets) && f.presets.length > 0) {
        var presetsHost = el('div', {class: 'ui-form-presets'});
        f.presets.forEach(function(p) {
          var chip = el('button', {
            class: 'ui-form-preset-chip',
            type: 'button',
            title: p.hint || ('Fill with: ' + (p.value || '')),
          }, [p.label || p.value || '?']);
          chip.addEventListener('click', function() {
            input.value = p.value || '';
            save(f.field, input.value);
          });
          presetsHost.appendChild(chip);
        });
        fieldWrap.appendChild(presetsHost);
      }
      // Skip generic input append when the toggle branch already
      // placed the switch into a [label, input] header row above.
      if (!toggleHandled) fieldWrap.appendChild(input);
      if (f.help) fieldWrap.appendChild(el('span', {class: 'ui-form-help'}, [f.help]));

      // Default suggestion setter — covers text, textarea, number, and
      // anything else with a writable input.value. Branches that need
      // custom apply logic (rules, future tags/checklist) register
      // their own setter inside their branch BEFORE we get here.
      if (!fieldSetters[f.field]) {
        fieldSetters[f.field] = function(v) {
          if (t === 'number') {
            var n = (f.decimals > 0) ? parseFloat(v) : parseInt(v, 10);
            if (isNaN(n)) return;
            input.value = String(n);
            save(f.field, n);
            return;
          }
          input.value = String(v == null ? '' : v);
          save(f.field, input.value);
        };
      }

      // "✨ Suggest" button — POSTs the current record + the target
      // field name + an optional hint to f.suggest_url. The server
      // returns {value}, which the field-typed setter applies.
      if (f.suggest_url && fieldSetters[f.field]) {
        var suggestRow = el('div', {class: 'ui-form-suggest-row'});
        var sBtn = el('button', {type: 'button', class: 'ui-form-suggest-btn'},
          ['✨ Suggest']);
        sBtn.addEventListener('click', function() {
          runFieldSuggest(f, sBtn);
        });
        suggestRow.appendChild(sBtn);
        fieldWrap.appendChild(suggestRow);
      }

      fieldEls[f.field] = fieldWrap;
      return fieldWrap;
    }

    // runFieldSuggest powers the "✨ Suggest" button. Prompts the user
    // for optional guidance, ships the current record + field name to
    // f.suggest_url, and feeds the server's {value} back through the
    // field's registered setter so the right apply logic fires per
    // field type (parseInt for number, rules-rebuild for rules, etc.).
    async function runFieldSuggest(f, btn) {
      var hint = await uiPrompt('Optional guidance — what should the AI consider? Leave blank to let it decide:', '');
      if (hint === null) return; // user cancelled
      btn.classList.add('busy');
      btn.disabled = true;
      fetch(f.suggest_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({field: f.field, hint: hint || '', record: current}),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t) {
          throw new Error(t || ('HTTP ' + r.status));
        });
        return r.json();
      }).then(function(d) {
        var setter = fieldSetters[f.field];
        if (!setter) { showToast('No setter registered for ' + f.field); return; }
        if (d && d.value !== undefined && d.value !== null) {
          setter(d.value);
        }
        // Opt-in side-fill: backends that judge multiple fields are
        // best decided together (e.g. tool-group name + description
        // both derive from the member list) can return an "extra"
        // map. Each key's setter fires the same way the primary
        // field's does — so the response can land a complete record
        // from one click. Unknown keys silently skipped.
        if (d && d.extra && typeof d.extra === 'object') {
          Object.keys(d.extra).forEach(function(k) {
            var s = fieldSetters[k];
            if (s) s(d.extra[k]);
          });
        }
      }).catch(function(err) {
        window.uiAlert('Suggest failed: ' + (err && err.message || err));
      }).then(function() {
        btn.classList.remove('busy');
        btn.disabled = false;
      });
    }

    function renderFormChips(host, f, input) {
      var valueField = f.chips_value_field || 'value';
      function applyChip(chipObj) {
        var v = chipObj ? (chipObj[valueField] || '') : '';
        input.value = v;
        save(f.field, v);
        // Companion fields — chips_also_set maps {targetField: chipProp}
        // so a single click can fan a multi-property chip out into
        // multiple inputs (persona chip → personality + persona_name).
        if (f.chips_also_set && chipObj) {
          Object.keys(f.chips_also_set).forEach(function(targetField) {
            var src = f.chips_also_set[targetField];
            var newVal = chipObj[src];
            if (newVal == null) return;
            var fw = fieldEls[targetField];
            if (!fw) return;
            var targetInput = fw.querySelector('input, textarea, select');
            if (!targetInput) return;
            targetInput.value = newVal;
            save(targetField, newVal);
          });
        }
      }
      // Clear chip path — wipes both the primary input and any
      // companion fields declared via chips_also_set so the form
      // returns to a fully-empty state on a single click.
      function clearChip() {
        input.value = '';
        save(f.field, '');
        if (f.chips_also_set) {
          Object.keys(f.chips_also_set).forEach(function(targetField) {
            var fw = fieldEls[targetField];
            if (!fw) return;
            var targetInput = fw.querySelector('input, textarea, select');
            if (!targetInput) return;
            targetInput.value = '';
            save(targetField, '');
          });
        }
      }
      function refresh() {
        host.innerHTML = '';
        host.appendChild(el('span', {class: 'ui-form-chips-loading'}, ['Loading…']));
        fetchJSON(f.chips_source).then(function(items) {
          host.innerHTML = '';
          (items || []).forEach(function(p) {
            var chip = el('span', {class: 'ui-form-chip', title: p.builtin ? 'Built-in' : 'Click to apply, double-click to delete'},
              [p.name || '?']);
            chip.addEventListener('click', function() { applyChip(p); });
            if (!p.builtin && f.chips_delete_url) {
              chip.addEventListener('dblclick', async function(ev) {
                ev.stopPropagation();
                if (!(await window.uiConfirm('Delete "' + p.name + '"?'))) return;
                var url = f.chips_delete_url.replace('{id}', encodeURIComponent(p.id || ''));
                fetch(url, {method: 'DELETE'}).then(refresh);
              });
            }
            host.appendChild(chip);
          });
          // Clear chip.
          var clr = el('span', {class: 'ui-form-chip'}, ['Clear']);
          clr.addEventListener('click', clearChip);
          host.appendChild(clr);
          // + New chip (only if create endpoint set).
          if (f.chips_create_url) {
            var add = el('span', {class: 'ui-form-chip ui-form-chip-add'}, [f.chips_add_label || '+ New']);
            add.addEventListener('click', function() {
              showFormChipCreate(f, input, refresh);
            });
            host.appendChild(add);
          }
        }).catch(function() {
          host.innerHTML = '';
          host.appendChild(el('span', {class: 'ui-form-chips-loading'}, ['(failed to load)']));
        });
      }
      refresh();
    }

    function showFormChipCreate(f, targetInput, onSaved) {
      var valueField = f.chips_value_field || 'value';
      // Modal overlay rendered in document.body so it isn't clipped
      // by parent containers.
      var overlay = el('div', {class: 'ui-form-modal-overlay'});
      var modal = el('div', {class: 'ui-form-modal'});
      modal.appendChild(el('div', {class: 'ui-form-modal-h'}, ['New ' + (f.label || 'preset')]));
      modal.appendChild(el('div', {class: 'ui-form-modal-hint'},
        ['Type a name (e.g. "Hank Hill"). Click AI Assist to expand into a full prompt, then save.']));

      modal.appendChild(el('label', {class: 'ui-form-label'}, ['Name']));
      var nameIn = el('input', {type: 'text', class: 'ui-form-input', placeholder: 'e.g. Hank Hill'});
      modal.appendChild(nameIn);

      modal.appendChild(el('label', {class: 'ui-form-label'}, ['Personality']));
      var textIn = el('textarea', {class: 'ui-form-textarea', rows: '6',
        placeholder: 'Type a seed name and click AI Assist below — or write your own.'});
      modal.appendChild(textIn);

      var actions = el('div', {class: 'ui-form-modal-actions'});
      var assistBtn = null;
      if (f.chips_assist_url) {
        assistBtn = el('button', {class: 'ui-pl-btn secondary'}, ['AI Assist']);
        assistBtn.addEventListener('click', function() {
          var seed = nameIn.value.trim() || textIn.value.trim();
          if (!seed) { showToast('Type a name or seed first.'); return; }
          var orig = assistBtn.textContent;
          assistBtn.textContent = 'Generating…';
          assistBtn.disabled = true;
          fetch(f.chips_assist_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({seed: seed}),
          }).then(function(r) {
            if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
            return r.text();
          }).then(function(text) {
            textIn.value = text;
          }).catch(function(err) {
            showToast('AI Assist failed: ' + err.message);
          }).then(function() {
            assistBtn.textContent = orig;
            assistBtn.disabled = false;
          });
        });
        actions.appendChild(assistBtn);
      }
      var spacer = el('div', {style: 'flex:1'});
      actions.appendChild(spacer);
      var cancelBtn = el('button', {class: 'ui-pl-btn secondary'}, ['Cancel']);
      cancelBtn.addEventListener('click', function() { overlay.remove(); });
      actions.appendChild(cancelBtn);
      var saveBtn = el('button', {class: 'ui-pl-btn primary'}, ['Save']);
      saveBtn.addEventListener('click', function() {
        var nm = nameIn.value.trim();
        var val = textIn.value.trim();
        if (!nm) { showToast('Name required.'); return; }
        if (!val) { showToast('Value required (use AI Assist or type your own).'); return; }
        var body = {name: nm};
        body[valueField] = val;
        fetch(f.chips_create_url, {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(body),
        }).then(function(r) {
          if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
          return r.json().catch(function(){ return null; });
        }).then(function() {
          targetInput.value = val;
          save(f.field, val);
          overlay.remove();
          if (onSaved) onSaved();
        }).catch(function(err) {
          showToast('Save failed: ' + err.message);
        });
      });
      actions.appendChild(saveBtn);
      modal.appendChild(actions);

      overlay.appendChild(modal);
      // No backdrop-click-to-close: a text-selection drag ending on the
      // backdrop would dismiss mid-copy. Use Cancel to close.
      document.body.appendChild(overlay);
      nameIn.focus();
    }

    function render() {
      wrap.innerHTML = '';
      fieldEls = {};
      // "Start from template" — optional prefill dropdown. Picking a
      // template applies its values through the same per-field setters
      // the Suggest button uses, so every field type fills correctly.
      // The user then edits (e.g. adds an API key) and saves normally.
      // Reset to the blank option after applying so re-picking the same
      // template re-applies it.
      if (Array.isArray(cfg.templates) && cfg.templates.length) {
        var tplRow = el('div', {class: 'ui-form-field'});
        tplRow.appendChild(el('label', {class: 'ui-form-label'}, [cfg.templates_label || 'Start from template']));
        var tplSel = el('select', {class: 'ui-form-input'});
        tplSel.appendChild(el('option', {value: ''}, [cfg.templates_label ? ('— choose ' + cfg.templates_label.toLowerCase() + ' —') : '— choose a template —']));
        cfg.templates.forEach(function(t, i) {
          tplSel.appendChild(el('option', {value: String(i)}, [t.label || ('Template ' + (i + 1))]));
        });
        tplSel.addEventListener('change', function() {
          var idx = parseInt(tplSel.value, 10);
          tplSel.value = '';
          if (isNaN(idx) || idx < 0 || idx >= cfg.templates.length) return;
          var vals = (cfg.templates[idx] && cfg.templates[idx].values) || {};
          Object.keys(vals).forEach(function(k) {
            var s = fieldSetters[k];
            if (s) s(vals[k]);
          });
        });
        tplRow.appendChild(tplSel);
        wrap.appendChild(tplRow);
      }
      // Render fields in order. A Type==="header" with collapsed:true starts a
      // collapsible group — the fields that follow (until the next header) go
      // into a hidden body the header toggles. Backward-safe: with no collapsed
      // headers, collapseBody stays null and every field lands in wrap as before.
      var collapseBody = null;
      cfg.fields.forEach(function(f){
        var fieldEl = renderField(f);
        if ((f.type || '') === 'header') {
          collapseBody = null; // a header ends any prior group
          if (f.collapsed) {
            var body = el('div', {class: 'ui-form-collapse-body', style: 'display:none'});
            fieldEl.classList.add('ui-form-collapsible');
            fieldEl.setAttribute('role', 'button');
            fieldEl.addEventListener('click', function(){
              var open = body.style.display === 'none';
              body.style.display = open ? '' : 'none';
              fieldEl.classList.toggle('open', open);
            });
            wrap.appendChild(fieldEl);
            wrap.appendChild(body);
            collapseBody = body;
            return;
          }
        }
        (collapseBody || wrap).appendChild(fieldEl);
      });
      applyVisibility();
      // Saving indicator gets attached to the parent section header.
      var section = wrap.closest('.ui-section');
      if (section) {
        var h = section.querySelector('.ui-section-h-r');
        if (h && !h.contains(savingIndicator)) h.appendChild(savingIndicator);
      }
      // TestURL — connectivity check button. POSTs the form's current
      // working state to the test endpoint and renders a small inline
      // result next to the button. Works in both auto-save and
      // submit-button modes; in submit-button mode it sits before the
      // submit so the natural flow is [Test] then [Save].
      if (cfg.test_url) {
        var testRow = el('div', {class: 'ui-form-test-row',
          style: 'display:flex;align-items:center;gap:0.6rem;margin-top:0.5rem;flex-wrap:wrap'});
        var testBtn = el('button', {class: 'ui-row-btn', type: 'button'},
          [cfg.test_label || 'Test connectivity']);
        var testResult = el('span', {class: 'ui-form-test-result',
          style: 'font-size:0.78rem;color:var(--text-mute)'});
        testBtn.addEventListener('click', function() {
          testBtn.disabled = true;
          var originalLabel = cfg.test_label || 'Test connectivity';
          testBtn.textContent = 'Testing…';
          testResult.style.color = 'var(--text-mute)';
          testResult.textContent = '';
          fetchJSON(cfg.test_url, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(current),
          }).then(function(resp) {
            if (resp && resp.ok) {
              testResult.style.color = 'var(--accent)';
              testResult.textContent = '✓ ' + (resp.message || 'OK');
            } else {
              testResult.style.color = 'var(--danger,#ff7b72)';
              testResult.textContent = '✗ ' + ((resp && resp.error) || 'Failed');
            }
          }).catch(function(err) {
            testResult.style.color = 'var(--danger,#ff7b72)';
            testResult.textContent = '✗ ' + (err && err.message || 'Test failed');
          }).then(function() {
            testBtn.disabled = false;
            testBtn.textContent = originalLabel;
          });
        });
        testRow.appendChild(testBtn);
        testRow.appendChild(testResult);
        wrap.appendChild(testRow);
      }
      // ResetURL — "Revert to defaults" button. Confirms, POSTs to the reset
      // endpoint (server clears the stored overrides), then reloads the form
      // from Source so the fields show the reverted default values.
      if (cfg.reset_url) {
        var resetRow = el('div', {style: 'margin-top:0.5rem'});
        var resetBtn = el('button', {class: 'ui-row-btn', type: 'button'},
          [cfg.reset_label || 'Revert to defaults']);
        resetBtn.addEventListener('click', async function() {
          var msg = cfg.reset_confirm || 'Revert these settings to their defaults? Any custom values here will be cleared.';
          if (!(await window.uiConfirm(msg))) return;
          resetBtn.disabled = true;
          var orig = resetBtn.textContent;
          resetBtn.textContent = 'Reverting…';
          fetch(cfg.reset_url, {method: 'POST'})
            .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); }); load(); })
            .catch(function(err){ showToast('Revert failed: ' + (err && err.message || err)); })
            .then(function(){ resetBtn.disabled = false; resetBtn.textContent = orig; });
        });
        resetRow.appendChild(resetBtn);
        wrap.appendChild(resetRow);
      }
      // Submit-button mode — append an explicit submit button after
      // the last field. Click POSTs the full record and (if
      // RedirectURL is set) navigates on success.
      if (submitMode) {
        var submitBtn = el('button', {class: 'ui-form-submit', type: 'button'}, [cfg.submit_label]);
        // Post-submit note: when the endpoint returns a `message` string, show
        // it inline (persisting until the next submit). Generic — any create /
        // import form can report an outcome without app-specific wiring. The
        // `warnings` array, when present and non-empty, tints the note so an
        // import that leaves an unmet reference reads as a caution, not a clean
        // success.
        var msgEl = el('div', {class: 'ui-form-msg', style: 'display:none;white-space:pre-wrap;margin-top:0.5rem;padding:0.5rem 0.7rem;border-left:3px solid var(--border);border-radius:4px;font-size:0.85rem;line-height:1.45;background:var(--bg-1)'});
        submitBtn.addEventListener('click', function() {
          submitBtn.disabled = true;
          submitBtn.textContent = 'Saving…';
          msgEl.style.display = 'none';
          msgEl.textContent = '';
          var postURL = cfg.post_url || cfg.source;
          fetchJSON(postURL, {
            method: cfg.method || 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(current),
          }).then(function(resp) {
            window.uiInvalidateSaved(cfg);
            if (cfg.redirect_url) {
              var dest = substitute(cfg.redirect_url, resp || {});
              var target = cfg.redirect_target || '_self';
              if (target === '_self') window.location.href = dest;
              else window.open(dest, target);
            } else if (ctx && typeof ctx.__closeModal === 'function') {
              // A submit-mode form inside a ModalButton: the submit button IS the
              // commit, so dismiss the dialog on success (the invalidate above has
              // already refreshed the panels behind it). Without this the modal
              // stays open and the save looks like it did nothing.
              ctx.__closeModal();
            } else {
              submitBtn.textContent = cfg.submit_label;
              submitBtn.disabled = false;
              if (resp && typeof resp.message === 'string' && resp.message) {
                msgEl.textContent = resp.message;
                var warned = resp.warnings && resp.warnings.length;
                msgEl.style.borderLeftColor = warned ? 'var(--warn, #d97706)' : 'var(--border)';
                msgEl.style.color = warned ? 'var(--warn, #d97706)' : 'var(--text)';
                msgEl.style.display = '';
              }
            }
          }).catch(function(err) {
            submitBtn.textContent = cfg.submit_label;
            submitBtn.disabled = false;
            showToast('Save failed: ' + err.message);
          });
        });
        wrap.appendChild(submitBtn);
        wrap.appendChild(msgEl);
      }
    }

    // Source empty / unset → render with an empty record. Lets a
    // FormPanel act as a create-form when there's nothing to load,
    // posting the typed fields to PostURL on save.
    function load() {
      if (cfg.source) {
        fetchJSON(cfg.source).then(function(d){ current = d || {}; render(); })
          .catch(function(err){ wrap.textContent = 'Failed to load: ' + err.message; });
      } else {
        render();
      }
    }
    load();
    return wrap;
  };

  components.display_panel = function(cfg) {
    var wrap = el('div', {class: 'ui-display'}, ['Loading…']);
    function reload() {
      fetchJSON(cfg.source).then(function(d) {
        wrap.innerHTML = '';
        var data = d || {};
        (cfg.pairs || []).forEach(function(p) {
          var row = el('div', {class: 'ui-display-row'}, [
            el('span', {class: 'ui-display-label'}, [p.label]),
            el('span', {class: 'ui-display-value' + (p.mono ? ' mono' : '')}, [fmt(data[p.field], p.format)]),
          ]);
          wrap.appendChild(row);
        });
        // Action row — panel-level buttons rendered below the pairs.
        // Same URL templating + method + confirm semantics as toolbar
        // actions elsewhere; substituteRefs already resolved any row
        // placeholders before this component mounted. Useful for
        // "wipe", "rotate", "regenerate" operator actions on read-only
        // panels where a full Form would be overkill.
        if (cfg.actions && cfg.actions.length) {
          var actionsRow = el('div', {class: 'ui-display-actions',
            style: 'display:flex;gap:0.5rem;margin-top:0.7rem;flex-wrap:wrap'});
          cfg.actions.forEach(function(act) {
            var classes = 'ui-row-btn';
            if (act.variant) classes += ' ' + act.variant;
            var btn = el('button', {class: classes, title: act.title || '',
              onclick: async function() {
                if (act.confirm && !(await window.uiConfirm(act.confirm))) return;
                btn.disabled = true;
                fetch(act.url, {method: act.method || 'POST'})
                  .then(function(r) {
                    if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
                    reload();
                    btn.disabled = false;
                  })
                  .catch(function(err) {
                    btn.disabled = false;
                    showToast(act.label + ' failed: ' + (err && err.message || err));
                  });
              }}, [act.label || 'Action']);
            actionsRow.appendChild(btn);
          });
          wrap.appendChild(actionsRow);
        }
      }).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    }
    // Refetch when an external action invalidates our source — same as Table.
    // Without this, a display backed by source_script (e.g. a computed weather
    // panel) never updated after a form save, only on a manual page reload.
    window.addEventListener('ui-data-changed', function(ev) {
      var sources = ev.detail && ev.detail.sources;
      if (sources && cfg.source && sources.indexOf(cfg.source) >= 0) reload();
    });
    reload();
    if (cfg.auto_refresh_ms && cfg.auto_refresh_ms > 0) setInterval(reload, cfg.auto_refresh_ms);
    return wrap;
  };

  components.pipeline_watch_panel = function(cfg) {
    // Live pipeline-follow view. Header + stage bar + status feed +
    // article body + completion actions. SSE-driven; reconnects on
    // dropped connections; shows a completion view when DoneStage
    // fires (or on graceful disconnect after that).
    var topicField = cfg.topic_field || 'topic';
    var doneField  = cfg.done_field  || 'done';
    var articleStage = cfg.article_stage || 'article';
    var draftStage   = cfg.draft_stage   || 'rough_draft';
    var doneStage    = cfg.done_stage    || 'done';
    var errorStage   = cfg.error_stage   || 'error';
    var stageList = (cfg.stages || []);
    var stageOrder = stageList.map(function(s) { return s.key; });
    var stageMeta = {};
    stageList.forEach(function(s) { stageMeta[s.key] = s; });

    var wrap = el('div', {class: 'ui-watch'});
    var header = el('div', {class: 'ui-watch-header'});
    if (cfg.app_name) header.appendChild(el('div', {class: 'ui-watch-app'}, [cfg.app_name]));
    var titleRow = el('div', {class: 'ui-watch-title-row'});
    var titleText = el('span', {class: 'ui-watch-title'},
      [el('span', {class: 'ui-watch-spinner'}), ' Pipeline']);
    titleRow.appendChild(titleText);
    var cancelBtn = null;
    if (cfg.cancel_url) {
      cancelBtn = el('button', {class: 'ui-watch-cancel'}, ['Cancel']);
      titleRow.appendChild(cancelBtn);
    }
    header.appendChild(titleRow);
    var topicEl = el('div', {class: 'ui-watch-topic'}, ['Connecting…']);
    header.appendChild(topicEl);
    wrap.appendChild(header);

    var stageBar = el('div', {class: 'ui-watch-stages'});
    wrap.appendChild(stageBar);
    var pills = {};
    var currentSubPillKey = null;

    function setStageState(key, state) {
      var pill = pills[key];
      if (!pill) {
        pill = el('span', {class: 'ui-watch-pill'});
        pill.dataset.key = key;
        var meta = stageMeta[key] || {};
        var label = meta.label || (key.charAt(0).toUpperCase() + key.slice(1));
        if (meta.icon) label = meta.icon + ' ' + label;
        pill._label = label;
        pill.textContent = label;
        stageBar.appendChild(pill);
        pills[key] = pill;
      }
      pill.classList.remove('active', 'done', 'error');
      if (state) pill.classList.add(state);
      if (state === 'active') {
        pill.innerHTML = '';
        pill.appendChild(el('span', {class: 'ui-watch-spinner'}));
        pill.appendChild(document.createTextNode(' ' + pill._label));
      } else {
        pill.textContent = pill._label;
      }
    }

    function ensureSubPill(parentKey, subKey, subLabel, state) {
      var key = parentKey + '::' + subKey;
      if (!pills[key]) {
        var meta = stageMeta[parentKey] || {};
        var icon = meta.icon ? (meta.icon + ' ') : '';
        var pill = el('span', {class: 'ui-watch-pill'});
        pill._label = icon + subLabel;
        pill.textContent = pill._label;
        stageBar.appendChild(pill);
        pills[key] = pill;
      }
      // Mark previous sub-pill of the same parent as done when a new
      // one becomes active — matches legacy "Gap 1 → Gap 2 → Main".
      if (currentSubPillKey && currentSubPillKey !== key) {
        var prev = pills[currentSubPillKey];
        if (prev && prev.classList.contains('active')) {
          prev.classList.remove('active');
          prev.classList.add('done');
          prev.textContent = prev._label;
        }
      }
      setStageState(key, state || 'active');
      currentSubPillKey = key;
    }

    function advancePriorStages(currentKey) {
      var idx = stageOrder.indexOf(currentKey);
      if (idx < 0) return;
      stageOrder.forEach(function(k, i) {
        var p = pills[k];
        if (p && i < idx && p.classList.contains('active')) {
          p.classList.remove('active');
          p.classList.add('done');
          p.textContent = p._label;
        }
      });
      // When leaving a stage that had sub-pills, mark them done too.
      Object.keys(pills).forEach(function(key) {
        var sep = key.indexOf('::');
        if (sep < 0) return;
        var parent = key.slice(0, sep);
        if (parent !== currentKey) {
          var p = pills[key];
          if (p && p.classList.contains('active')) {
            p.classList.remove('active');
            p.classList.add('done');
            p.textContent = p._label;
          }
        }
      });
    }

    var statusView = el('div', {class: 'ui-watch-status'});
    wrap.appendChild(statusView);
    var draftView = el('div', {class: 'ui-watch-draft', style: 'display:none'});
    wrap.appendChild(draftView);
    var articleView = el('div', {class: 'ui-watch-article', style: 'display:none'});
    var articleTitle = el('h1', {class: 'ui-watch-article-title'});
    var articleBody  = el('div', {class: 'ui-watch-article-body'});
    articleView.appendChild(articleTitle);
    articleView.appendChild(articleBody);
    wrap.appendChild(articleView);
    var doneActions = el('div', {class: 'ui-watch-done-actions', style: 'display:none'});
    wrap.appendChild(doneActions);

    function addStatus(message) {
      var line = el('div', {class: 'ui-watch-status-msg'}, [String(message || '')]);
      statusView.appendChild(line);
      statusView.scrollTop = statusView.scrollHeight;
    }

    function showArticle(title, content) {
      statusView.style.display = 'none';
      articleView.style.display = '';
      articleTitle.textContent = title || '';
      uiRenderMarkdown(articleBody, content || '');
    }

    function renderDoneActions(lastEvent) {
      if (!cfg.on_done_actions || !cfg.on_done_actions.length) return;
      doneActions.innerHTML = '';
      doneActions.style.display = '';
      cfg.on_done_actions.forEach(function(act) {
        var url = substitute(act.url, lastEvent || {});
        var classes = 'ui-watch-action';
        if (act.variant) classes += ' ' + act.variant;
        if ((act.method || 'GET').toUpperCase() === 'POST') {
          var btn = el('button', {class: classes}, [act.label || 'Action']);
          btn.addEventListener('click', function() {
            fetch(url, {method: 'POST'}).then(function(r) {
              if (!r.ok) throw new Error('HTTP ' + r.status);
            }).catch(function(err) {
              showToast('Failed: ' + err.message);
            });
          });
          doneActions.appendChild(btn);
        } else {
          var attrs = {class: classes, href: url};
          if (act.new_tab) { attrs.target = '_blank'; attrs.rel = 'noopener'; }
          doneActions.appendChild(el('a', attrs, [act.label || 'Open']));
        }
      });
    }

    var pipelineDone = false;
    function markComplete(message, lastEvent) {
      pipelineDone = true;
      titleText.innerHTML = '';
      titleText.appendChild(document.createTextNode('✅ Complete'));
      if (cancelBtn) cancelBtn.style.display = 'none';
      Object.keys(pills).forEach(function(k) {
        var p = pills[k];
        if (p.classList.contains('active')) {
          p.classList.remove('active');
          p.classList.add('done');
          p.textContent = p._label;
        }
      });
      if (message) topicEl.textContent = message;
      renderDoneActions(lastEvent || {});
    }
    function markError(message) {
      pipelineDone = true;
      titleText.innerHTML = '';
      titleText.appendChild(document.createTextNode('❌ Error'));
      if (cancelBtn) cancelBtn.style.display = 'none';
      Object.keys(pills).forEach(function(k) {
        var p = pills[k];
        if (p.classList.contains('active')) {
          p.classList.remove('active');
          p.classList.add('error');
          p.textContent = p._label;
        }
      });
      if (message) addStatus('ERROR: ' + message);
    }

    if (cancelBtn) {
      cancelBtn.addEventListener('click', async function() {
        if (!(await window.uiConfirm('Cancel the pipeline?'))) return;
        fetch(cfg.cancel_url, {method: 'POST'}).then(function() {
          titleText.innerHTML = '';
          titleText.appendChild(document.createTextNode('⛔ Cancelled'));
          topicEl.textContent = 'Pipeline cancelled by user.';
          cancelBtn.style.display = 'none';
          if (evtSource) evtSource.close();
        }).catch(function() {});
      });
    }

    // Seed the header from /api/info before SSE arrives.
    fetchJSON(cfg.info_url).then(function(info) {
      info = info || {};
      if (info[topicField]) topicEl.textContent = info[topicField];
      if (info[doneField]) markComplete(info[topicField] || 'Pipeline finished', info);
    }).catch(function() {
      topicEl.textContent = 'Session not found or expired.';
    });

    var evtSource = null;
    function connectEvents() {
      evtSource = new EventSource(cfg.events_url);
      evtSource.onmessage = function(ev) {
        var data;
        try { data = JSON.parse(ev.data); } catch (_) { return; }
        var stage = data.stage;
        if (!stage) return;

        if (stage === draftStage) {
          var details = el('details', {class: 'ui-watch-draft-details', open: true});
          var summary = el('summary', {}, ['Rough draft (pre-voice pass)']);
          details.appendChild(summary);
          details.appendChild(el('div', {class: 'ui-watch-draft-body ui-md', html: mdToHTML(String(data.message || ''))}));
          draftView.innerHTML = '';
          draftView.appendChild(details);
          draftView.style.display = '';
          return;
        }
        if (stage === articleStage) {
          showArticle(data.title || data.message || '', data.content || data.message || '');
          return;
        }
        if (stage === doneStage || stage === 'stream_end') {
          markComplete(data.title || data.message || data[topicField] || 'Pipeline finished', data);
          if (evtSource) evtSource.close();
          return;
        }
        if (stage === errorStage) {
          markError(data.message);
          if (evtSource) evtSource.close();
          return;
        }
        // Generic stage pill update.
        if (stageMeta[stage]) {
          var meta = stageMeta[stage];
          if (meta.sub_pattern) {
            try {
              var re = new RegExp(meta.sub_pattern);
              var m = String(data.message || '').match(re);
              if (m) {
                var subKey = m[0];
                var label = meta.sub_label_template || '$1';
                label = label.replace(/\$(\d)/g, function(_, n) { return m[Number(n)] || ''; });
                ensureSubPill(stage, subKey, label, 'active');
                if (data.message) addStatus(data.message);
                advancePriorStages(stage);
                if (data.message) topicEl.textContent = data.message;
                return;
              }
            } catch (_) {}
          }
          setStageState(stage, 'active');
          advancePriorStages(stage);
        }
        if (data.message) {
          addStatus(data.message);
          topicEl.textContent = data.message;
        }
      };
      evtSource.onerror = function() {
        if (!evtSource) return;
        evtSource.close();
        evtSource = null;
        if (pipelineDone) return;
        // Reconnect after a delay; if the server reports done, switch
        // to completion view instead of reconnecting.
        setTimeout(function() {
          fetchJSON(cfg.info_url).then(function(info) {
            info = info || {};
            if (info[doneField]) {
              markComplete(info[topicField] || 'Pipeline finished', info);
              return;
            }
            connectEvents();
          }).catch(function() {
            topicEl.textContent = 'Connection lost. Retrying…';
            setTimeout(connectEvents, 5000);
          });
        }, 3000);
      };
    }
    connectEvents();
    return wrap;
  };

  components.api_key_panel = function(cfg) {
    var keyField = cfg.key_field || 'key';
    var wrap = el('div', {class: 'ui-apikey'});
    var input = el('input', {type: 'text', class: 'ui-apikey-input', readonly: 'readonly',
      placeholder: cfg.placeholder || 'No key generated'});
    wrap.appendChild(input);
    function setKey(v) { input.value = String(v || ''); }
    function load() {
      fetchJSON(cfg.source).then(function(d) {
        setKey((d || {})[keyField]);
      }).catch(function(err) { showToast('Failed to load: ' + err.message); });
    }
    if (cfg.generate_url) {
      var gen = el('button', {class: 'ui-apikey-btn'}, ['Generate']);
      gen.addEventListener('click', async function() {
        if (cfg.confirm_generate && !(await window.uiConfirm(cfg.confirm_generate))) return;
        gen.disabled = true;
        fetchJSON(cfg.generate_url, {method: 'POST'}).then(function(d) {
          setKey((d || {})[keyField]);
          showToast('Key rotated.');
        }).catch(function(err) {
          showToast('Failed: ' + err.message);
        }).then(function() {
          gen.disabled = false;
        });
      });
      wrap.appendChild(gen);
    }
    if (cfg.allow_copy) {
      var cp = el('button', {class: 'ui-apikey-btn'}, ['Copy']);
      cp.addEventListener('click', function() {
        if (!input.value) return;
        // Async clipboard API where available; fall back to
        // selectAll+execCommand on older / non-secure contexts.
        var done = function() { showToast('Copied.'); };
        var fail = function() { showToast('Copy failed — select manually.'); };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(input.value).then(done).catch(function() {
            input.select();
            try {
              if (document.execCommand('copy')) { done(); return; }
            } catch (_) {}
            fail();
          });
        } else {
          input.select();
          try {
            if (document.execCommand('copy')) { done(); return; }
          } catch (_) {}
          fail();
        }
      });
      wrap.appendChild(cp);
    }
    load();
    return wrap;
  };

  components.suggest_panel = function(cfg) {
    // Suggestion list with optional direction input + per-row
    // primary (click-the-row) and secondary (per-row button)
    // actions. Mirrors the legacy autoblog "Topic Ideas" UX.
    var wrap = el('div', {class: 'ui-suggest'});
    var controls = el('div', {class: 'ui-suggest-controls'});
    var direction = el('input', {type: 'text', class: 'ui-form-input ui-suggest-direction',
      placeholder: cfg.placeholder || 'Focus area (optional)…'});
    var btn = el('button', {class: 'ui-pl-btn ui-suggest-btn'},
      [el('span', {class: 'ui-pl-prefill-icon'}, ['✨']), ' ', cfg.suggest_label || 'Suggest']);
    controls.appendChild(direction);
    controls.appendChild(btn);
    wrap.appendChild(controls);
    var list = el('div', {class: 'ui-suggest-list'});
    wrap.appendChild(list);

    var qField = cfg.question_field || 'question';
    var hField = cfg.hook_field || 'hook';
    function pickField(item, primary, fallbacks) {
      if (item[primary]) return item[primary];
      for (var i = 0; i < fallbacks.length; i++) {
        if (item[fallbacks[i]]) return item[fallbacks[i]];
      }
      return '';
    }

    async function fireAction(action, item) {
      if (!action || !action.url) return Promise.resolve();
      if (action.confirm && !(await window.uiConfirm(action.confirm))) return Promise.resolve();
      var body = null;
      if (action.body_map) {
        body = {};
        Object.keys(action.body_map).forEach(function(k) {
          var src = action.body_map[k];
          body[k] = src === '__direction__' ? direction.value : item[src];
        });
      }
      var fetchOpts = {method: action.method || 'POST'};
      if (body) {
        fetchOpts.headers = {'Content-Type': 'application/json'};
        fetchOpts.body = JSON.stringify(body);
      }
      return fetch(action.url, fetchOpts).then(function(r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        if (action.toast) {
          var msg = String(action.toast).replace(/\{question\}/g, pickField(item, qField, ['topic', 'text']))
            .replace(/\{hook\}/g, pickField(item, hField, ['description', 'summary']));
          showToast(msg);
        }
        // Refresh sibling lists declared in action.invalidate (e.g.
        // the Blog Queue table re-fetches after this row is queued).
        if (action.invalidate && action.invalidate.length) {
          window.uiInvalidate(action.invalidate);
        }
      }).catch(function(err) {
        showToast('Failed: ' + err.message);
      });
    }

    function loadSuggestions() {
      var orig = btn.textContent;
      btn.disabled = true;
      list.innerHTML = '<div class="ui-suggest-loading">Generating suggestions…</div>';
      var body = {};
      body[cfg.direction_field || 'direction'] = direction.value || '';
      fetch(cfg.url, {
        method: cfg.method || 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).then(function(r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      }).then(function(items) {
        list.innerHTML = '';
        if (!Array.isArray(items) || !items.length) {
          list.appendChild(el('div', {class: 'ui-suggest-empty'},
            [cfg.empty_text || 'No suggestions returned.']));
          return;
        }
        items.forEach(function(item) {
          var row = el('div', {class: 'ui-suggest-item'});
          var bodyEl = el('div', {class: 'ui-suggest-item-body'});
          var topic = pickField(item, qField, ['topic', 'text']);
          var hook = pickField(item, hField, ['description', 'summary']);
          if (topic) bodyEl.appendChild(el('div', {class: 'ui-suggest-item-q'}, [topic]));
          if (hook) bodyEl.appendChild(el('div', {class: 'ui-suggest-item-hook'}, [hook]));
          row.appendChild(bodyEl);
          if (cfg.primary_action) {
            bodyEl.style.cursor = 'pointer';
            bodyEl.addEventListener('click', function() {
              fireAction(cfg.primary_action, item);
            });
          }
          if (cfg.secondary_action) {
            var secBtn = el('button', {class: 'ui-pl-btn ui-suggest-secondary'},
              [cfg.secondary_action.label || 'Action']);
            secBtn.addEventListener('click', function(ev) {
              ev.stopPropagation();
              fireAction(cfg.secondary_action, item).then(function() {
                row.style.opacity = '0.4';
                row.style.pointerEvents = 'none';
              });
            });
            row.appendChild(secBtn);
          }
          list.appendChild(row);
        });
      }).catch(function(err) {
        list.innerHTML = '';
        list.appendChild(el('div', {class: 'ui-suggest-empty'}, ['Failed: ' + err.message]));
      }).then(function() {
        btn.disabled = false;
        btn.textContent = orig;
      });
    }

    btn.addEventListener('click', loadSuggestions);
    direction.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter') {
        ev.preventDefault();
        loadSuggestions();
      }
    });
    return wrap;
  };

  components.bar_chart = function(cfg) {
    var wrap = el('div', {class: 'ui-chart'}, ['Loading…']);
    var height = cfg.height_px || 200;
    var decimals = cfg.y_decimals != null ? cfg.y_decimals : 2;
    var prefix = cfg.y_prefix || '';

    function fmtX(v) {
      if (cfg.x_format === 'date' && v) {
        // Accept "YYYY-MM-DD" → "MMM DD".
        var m = String(v).match(/^(\d{4})-(\d{2})-(\d{2})$/);
        if (m) {
          var d = new Date(Number(m[1]), Number(m[2])-1, Number(m[3]));
          return d.toLocaleDateString(undefined, {month: 'short', day: 'numeric'});
        }
      }
      return String(v);
    }
    function fmtY(n) { return prefix + Number(n).toFixed(decimals); }

    fetchJSON(cfg.source).then(function(d) {
      var data = Array.isArray(d) ? d : (d && d.data) || [];
      wrap.innerHTML = '';
      // Position the wrap relatively so the tooltip can absolute-pos against it.
      wrap.style.position = 'relative';
      if (!data.length) {
        wrap.appendChild(el('div', {class: 'ui-chart-empty'}, [cfg.empty_text || 'No data yet.']));
        return;
      }
      var max = 0;
      data.forEach(function(p){ if (Number(p[cfg.y_field]) > max) max = Number(p[cfg.y_field]); });
      if (max === 0) max = 1; // avoid divide-by-zero, all-zero series renders flat

      // Instant-feedback tooltip element. Updated on bar mousemove.
      var tip = el('div', {class: 'ui-chart-tip'});
      tip.style.display = 'none';
      wrap.appendChild(tip);

      // Build SVG. Use viewBox with explicit aspect so the chart
      // scales fluidly with the section width.
      var svgNS = 'http://www.w3.org/2000/svg';
      var W = 600, H = height, P = 20; // padding
      var bw = (W - P*2) / data.length;
      var svg = document.createElementNS(svgNS, 'svg');
      svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
      svg.setAttribute('preserveAspectRatio', 'none');
      svg.setAttribute('width', '100%');
      svg.setAttribute('height', String(H));
      svg.classList.add('ui-chart-svg');

      // Y axis baseline.
      var axis = document.createElementNS(svgNS, 'line');
      axis.setAttribute('x1', String(P)); axis.setAttribute('y1', String(H - P));
      axis.setAttribute('x2', String(W - P)); axis.setAttribute('y2', String(H - P));
      axis.setAttribute('class', 'ui-chart-axis');
      svg.appendChild(axis);

      // Max-value gridline + label.
      var gridY = P;
      var grid = document.createElementNS(svgNS, 'line');
      grid.setAttribute('x1', String(P)); grid.setAttribute('y1', String(gridY));
      grid.setAttribute('x2', String(W - P)); grid.setAttribute('y2', String(gridY));
      grid.setAttribute('class', 'ui-chart-grid');
      svg.appendChild(grid);
      var maxLabel = document.createElementNS(svgNS, 'text');
      maxLabel.setAttribute('x', String(P - 4));
      maxLabel.setAttribute('y', String(gridY + 4));
      maxLabel.setAttribute('class', 'ui-chart-axis-label');
      maxLabel.setAttribute('text-anchor', 'end');
      maxLabel.textContent = fmtY(max);
      svg.appendChild(maxLabel);

      data.forEach(function(p, i) {
        var v = Number(p[cfg.y_field]) || 0;
        var x = P + i * bw + bw * 0.15;
        var w = bw * 0.7;
        var h = (v / max) * (H - P*2);
        var y = H - P - h;
        var bar = document.createElementNS(svgNS, 'rect');
        bar.setAttribute('x', String(x));
        bar.setAttribute('y', String(y));
        bar.setAttribute('width', String(w));
        bar.setAttribute('height', String(h));
        bar.setAttribute('class', 'ui-chart-bar');
        // Instant tooltip — show on enter, position on move, hide on leave.
        // Hover area extends slightly beyond the bar so thin bars are
        // still easy to grab without pixel-precise aim.
        var hover = document.createElementNS(svgNS, 'rect');
        hover.setAttribute('x', String(P + i * bw));
        hover.setAttribute('y', String(P));
        hover.setAttribute('width', String(bw));
        hover.setAttribute('height', String(H - P*2));
        hover.setAttribute('fill', 'transparent');
        hover.style.cursor = 'crosshair';
        // Build the tooltip body — headline (date + cost), then the
        // optional breakdown rows in a compact two-column layout.
        var buildTip = function() {
          var parts = [];
          parts.push('<div class="ui-chart-tip-h">' + fmtX(p[cfg.x_field]) + '</div>');
          parts.push('<div class="ui-chart-tip-y">' + fmtY(v) + '</div>');
          if (cfg.breakdown && cfg.breakdown.length) {
            parts.push('<div class="ui-chart-tip-rows">');
            cfg.breakdown.forEach(function(pair) {
              var rawV = p[pair.field];
              if (rawV == null) return;
              var fv = fmt(rawV, pair.format);
              parts.push(
                '<div class="ui-chart-tip-row">' +
                  '<span class="ui-chart-tip-label">' + pair.label + '</span>' +
                  '<span class="ui-chart-tip-val' + (pair.mono ? ' mono' : '') + '">' + fv + '</span>' +
                '</div>'
              );
            });
            parts.push('</div>');
          }
          return parts.join('');
        };
        var tipHTML = buildTip();
        var showTip = function(ev) {
          tip.innerHTML = tipHTML;
          tip.style.display = 'block';
          var rect = wrap.getBoundingClientRect();
          var x = ev.clientX - rect.left;
          var y = ev.clientY - rect.top;
          // Flip horizontally if the tooltip would clip the right edge.
          var tw = tip.offsetWidth, th = tip.offsetHeight;
          var tx = x + 12; if (tx + tw + 8 > rect.width) tx = x - tw - 12;
          var ty = y - th - 8; if (ty < 4) ty = y + 16;
          tip.style.left = Math.max(4, tx) + 'px';
          tip.style.top  = ty + 'px';
        };
        hover.addEventListener('mouseenter', showTip);
        hover.addEventListener('mousemove', showTip);
        hover.addEventListener('mouseleave', function(){ tip.style.display = 'none'; });
        svg.appendChild(bar);
        svg.appendChild(hover);

        // X-axis label every Nth bar so they don't overlap.
        var step = Math.ceil(data.length / 10);
        if (i % step === 0 || i === data.length - 1) {
          var tx = document.createElementNS(svgNS, 'text');
          tx.setAttribute('x', String(x + w/2));
          tx.setAttribute('y', String(H - 4));
          tx.setAttribute('text-anchor', 'middle');
          tx.setAttribute('class', 'ui-chart-axis-label');
          tx.textContent = fmtX(p[cfg.x_field]);
          svg.appendChild(tx);
        }
      });
      wrap.appendChild(svg);

      // Total summary below chart.
      var total = data.reduce(function(s, p){ return s + (Number(p[cfg.y_field]) || 0); }, 0);
      wrap.appendChild(el('div', {class: 'ui-chart-summary'}, [
        'Total over ' + data.length + ' periods: ' + fmtY(total),
      ]));
    }).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    return wrap;
  };

  // Generic multi-series chart renderer (bar / line / area / pie): a
  // pure spec -> SVG-string function. Wrapped in its own IIFE so helper
  // names (fmt/num/esc/…) don't collide with the runtime's globals.
  // Axis / text / grid colors reference theme tokens so the chart
  // follows light/dark; series use a fixed categorical palette led by
  // the platform accent. Lifted from the app-local prototype once its
  // rendering was proven. Exposed as window.uiChartSVG so app block
  // renderers can reuse it too.
  var uiChartSVG = (function() {
    var PALETTE = ['#6366f1','#14b8a6','#f59e0b','#ef4444','#22c55e','#a855f7','#06b6d4','#f97316'];
    function esc(s){ return String(s==null?'':s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;'); }
    function num(v){ var n = typeof v==='number'?v:parseFloat(v); return isFinite(n)?n:0; }
    function fmt(v){
      if (Math.abs(v)>=1000000) return (v/1000000).toFixed(1).replace(/\.0$/,'')+'M';
      if (Math.abs(v)>=1000) return (v/1000).toFixed(1).replace(/\.0$/,'')+'k';
      return (Math.round(v*100)/100).toString();
    }
    function niceMax(max){
      if (max<=0) return {max:1,step:0.25};
      var pow = Math.pow(10, Math.floor(Math.log(max)/Math.LN10));
      var norm = max/pow, nn = norm<=1?1:norm<=2?2:norm<=5?5:10;
      var step = (nn/4)*pow;
      return {max: Math.ceil(max/step)*step, step: step};
    }
    function barBandCenter(mL, pw, n, i){ var band = pw/Math.max(1,n); return mL + band*i + band/2; }
    // val is the DATA value (shown in the hover <title>); h is the pixel
    // height (drives geometry). They differ — passing h to the title
    // would show pixels, not the number the bar represents.
    function rect(x,y,w,h,ci,val){
      var c = PALETTE[ci % PALETTE.length];
      return '<rect x="'+x.toFixed(1)+'" y="'+y.toFixed(1)+'" width="'+w.toFixed(1)+'" height="'+Math.max(0,h).toFixed(1)+'" rx="2" fill="'+c+'"><title>'+fmt(val)+'</title></rect>';
    }
    function bars(series,n,mL,pw,ph,mT,yMax,stacked,svg){
      var band = pw/Math.max(1,n), groupW = band*0.7, nS = Math.max(1,series.length);
      for (var i=0;i<n;i++){
        var x0 = mL + band*i + (band-groupW)/2;
        if (stacked){
          var accH=0;
          for (var s=0;s<series.length;s++){ var v=num((series[s].points||[])[i]), h=(ph*v)/yMax, y=mT+ph-accH-h; svg.push(rect(x0,y,groupW,h,s,v)); accH+=h; }
        } else {
          var bw = groupW/nS;
          for (var s2=0;s2<series.length;s2++){ var v2=num((series[s2].points||[])[i]), h2=(ph*v2)/yMax, y2=mT+ph-h2; svg.push(rect(x0+bw*s2, y2, Math.max(1,bw-1), h2, s2, v2)); }
        }
      }
    }
    function lines(series,n,xFor,yFor,mT,ph,fill,svg){
      for (var s=0;s<series.length;s++){
        var c = PALETTE[s % PALETTE.length], pts = series[s].points||[], d='';
        for (var i=0;i<n;i++){ d += (i===0?'M':'L') + xFor(i).toFixed(1) + ',' + yFor(num(pts[i])).toFixed(1) + ' '; }
        if (fill && n>0){
          var area = d + 'L'+xFor(n-1).toFixed(1)+','+(mT+ph).toFixed(1)+' L'+xFor(0).toFixed(1)+','+(mT+ph).toFixed(1)+' Z';
          svg.push('<path d="'+area+'" fill="'+c+'" opacity="0.15"/>');
        }
        svg.push('<path d="'+d.trim()+'" fill="none" stroke="'+c+'" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>');
        for (var j=0;j<n;j++){ svg.push('<circle cx="'+xFor(j).toFixed(1)+'" cy="'+yFor(num(pts[j])).toFixed(1)+'" r="2.5" fill="'+c+'"/>'); }
      }
    }
    function legend(series,x,y,svg){
      var cx=x;
      for (var s=0;s<series.length;s++){
        var c = PALETTE[s % PALETTE.length], name = series[s].name || ('Series '+(s+1));
        svg.push('<rect x="'+cx+'" y="'+(y-8)+'" width="10" height="10" rx="2" fill="'+c+'"/>');
        svg.push('<text x="'+(cx+14)+'" y="'+y+'" fill="var(--text-mute)" font-size="11">'+esc(name)+'</text>');
        cx += 24 + name.length*6.2;
      }
    }
    function pie(spec,W,H,svg){
      var labels = spec.labels||[], series = spec.series||[], slices=[];
      if (series.length && series[0] && series[0].value!=null){
        slices = series.map(function(s,i){ return {name:s.name||labels[i]||('#'+(i+1)), value:num(s.value)}; });
      } else {
        var pts=(series[0]&&series[0].points)||[];
        slices = labels.map(function(l,i){ return {name:l, value:num(pts[i])}; });
      }
      var total = slices.reduce(function(a,s){ return a+Math.max(0,s.value); },0)||1;
      var cx=H/2, cy=H/2+(spec.title?8:0), r=Math.min(H, W*0.5)/2-16;
      if (spec.title) svg.push('<text x="10" y="18" fill="var(--text-hi)" font-size="14" font-weight="600">'+esc(spec.title)+'</text>');
      var ang=-Math.PI/2;
      for (var i=0;i<slices.length;i++){
        var frac = Math.max(0,slices[i].value)/total, end = ang+frac*Math.PI*2, large = frac>0.5?1:0;
        var x1=cx+r*Math.cos(ang), y1=cy+r*Math.sin(ang), x2=cx+r*Math.cos(end), y2=cy+r*Math.sin(end);
        var c = PALETTE[i % PALETTE.length];
        if (frac>=0.9999){ svg.push('<circle cx="'+cx+'" cy="'+cy.toFixed(1)+'" r="'+r.toFixed(1)+'" fill="'+c+'"/>'); }
        else {
          svg.push('<path d="M'+cx+','+cy.toFixed(1)+' L'+x1.toFixed(1)+','+y1.toFixed(1)+' A'+r.toFixed(1)+','+r.toFixed(1)+' 0 '+large+' 1 '+x2.toFixed(1)+','+y2.toFixed(1)+' Z" fill="'+c+'"><title>'+esc(slices[i].name)+': '+fmt(slices[i].value)+'</title></path>');
        }
        ang=end;
      }
      var lx=W*0.62, ly=24;
      for (var j=0;j<slices.length;j++){
        var cc = PALETTE[j % PALETTE.length], pct = Math.round((Math.max(0,slices[j].value)/total)*100);
        svg.push('<rect x="'+lx+'" y="'+(ly-9)+'" width="10" height="10" rx="2" fill="'+cc+'"/>');
        svg.push('<text x="'+(lx+15)+'" y="'+ly+'" fill="var(--text)" font-size="11">'+esc(slices[j].name)+' ('+pct+'%)</text>');
        ly+=20;
      }
      svg.push('</svg>');
      return svg.join('');
    }
    function draw(spec){
      spec = spec||{};
      var opt = spec.options||{}, W = num(opt.width)||640, H = num(opt.height)||360;
      var type = (spec.type||'bar').toLowerCase(), title = spec.title||'', labels = spec.labels||[], series = spec.series||[];
      var legendOn = opt.legend !== false;
      var svg = [];
      svg.push('<svg viewBox="0 0 '+W+' '+H+'" width="100%" font-family="-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif" style="max-width:100%;height:auto">');
      if (type==='pie') return pie(spec,W,H,svg);
      var mL=46, mR=14, mT=title?30:14, mB=legendOn?46:30, pw=W-mL-mR, ph=H-mT-mB;
      if (title) svg.push('<text x="'+mL+'" y="18" fill="var(--text-hi)" font-size="14" font-weight="600">'+esc(title)+'</text>');
      var stacked = !!opt.stacked && type==='bar';
      var n = labels.length || (series[0] && series[0].points ? series[0].points.length : 0), maxV=0;
      for (var i=0;i<n;i++){
        var sum=0;
        for (var s=0;s<series.length;s++){ var v=num((series[s].points||[])[i]); if (stacked) sum+=v; else if (v>maxV) maxV=v; }
        if (stacked && sum>maxV) maxV=sum;
      }
      var nice = niceMax(maxV), yMax = nice.max, yStep = nice.step;
      var xFor = function(i){ return mL + (n<=1 ? pw/2 : (pw*i)/(n-1)); };
      var yFor = function(v){ return mT + ph - (ph*v)/yMax; };
      for (var g=0; g<=yMax+1e-9; g+=yStep){
        var gy = yFor(g);
        svg.push('<line x1="'+mL+'" y1="'+gy.toFixed(1)+'" x2="'+(W-mR)+'" y2="'+gy.toFixed(1)+'" stroke="var(--border)" stroke-width="1" '+(g===0?'':'stroke-dasharray="2,3" ')+'opacity="0.7"/>');
        svg.push('<text x="'+(mL-6)+'" y="'+(gy+3).toFixed(1)+'" fill="var(--text-mute)" font-size="10" text-anchor="end">'+fmt(g)+'</text>');
      }
      for (var xi=0; xi<n; xi++){
        var lx = (type==='bar') ? barBandCenter(mL,pw,n,xi) : xFor(xi);
        svg.push('<text x="'+lx.toFixed(1)+'" y="'+(mT+ph+14)+'" fill="var(--text-mute)" font-size="10" text-anchor="middle">'+esc(labels[xi]!=null?labels[xi]:xi)+'</text>');
      }
      if (type==='bar') bars(series,n,mL,pw,ph,mT,yMax,stacked,svg);
      else lines(series,n,xFor,yFor,mT,ph,type==='area',svg);
      if (legendOn) legend(series,mL,H-16,svg);
      svg.push('</svg>');
      return svg.join('');
    }
    return draw;
  })();
  window.uiChartSVG = uiChartSVG;

  components.chart_panel = function(cfg) {
    var wrap = el('div', {class: 'ui-chart-panel'});
    function specFromCfg() {
      return {type: cfg.chart_type, title: cfg.title, labels: cfg.labels, series: cfg.series, options: cfg.options};
    }
    function render(spec) { wrap.innerHTML = uiChartSVG(spec); }
    if (cfg.source) {
      wrap.textContent = 'Loading…';
      fetchJSON(cfg.source).then(function(d) {
        var spec = specFromCfg();
        if (d) {
          // Endpoint fields override the declared defaults, so a
          // source_script can drive type/title/data entirely.
          if (d.chart_type) spec.type = d.chart_type;
          if (d.title != null) spec.title = d.title;
          if (d.labels) spec.labels = d.labels;
          if (d.series) spec.series = d.series;
          if (d.options) spec.options = d.options;
        }
        render(spec);
      }).catch(function(err) {
        wrap.textContent = 'Failed to load chart: ' + (err && err.message ? err.message : err);
      });
    } else {
      render(specFromCfg());
    }
    return wrap;
  };

  components.action_list = function(cfg) {
    var wrap = el('div', {class: 'ui-actionlist'}, ['Loading…']);
    var labelField = cfg.label_field || 'Label';
    var descField  = cfg.desc_field  || 'Desc';
    var btnText    = cfg.button_text || 'Run';
    function load() {
      fetchJSON(cfg.source).then(function(items) {
        wrap.innerHTML = '';
        if (!items || !items.length) {
          wrap.appendChild(el('div', {class: 'ui-actionlist-empty'}, [cfg.empty_text || 'Nothing here.']));
          return;
        }
        items.forEach(function(item) {
          var status = el('span', {class: 'ui-actionlist-status'});
          // Per-item overrides (additive, backward-compatible): an item may
          // carry its own button label + confirm prompt, so a list can render
          // distinctly-labeled buttons (e.g. app action buttons) rather than
          // one shared verb.
          var itemBtnText = item.button || btnText;
          var itemConfirm = item.confirm || cfg.confirm;
          var btn = el('button', {class: 'ui-row-btn', onclick: async function() {
            if (itemConfirm && !(await window.uiConfirm(itemConfirm))) return;
            var url = substitute(cfg.post_to, item);
            btn.disabled = true;
            status.textContent = '…';
            fetchJSON(url, {method: cfg.method || 'POST'}).then(function(r) {
              btn.disabled = false;
              // Prefer an explicit {message}; else surface a {fixed}/{removed}
              // digit; else a bare "done".
              if (r && typeof r === 'object' && r.message) {
                status.textContent = 'done';
                showToast(r.message);
              } else if (r && typeof r === 'object') {
                var n = r.fixed != null ? r.fixed : (r.removed != null ? r.removed : null);
                status.textContent = n != null ? ('done — ' + n) : 'done';
              } else {
                status.textContent = 'done';
              }
              setTimeout(function(){ status.textContent = ''; }, 4000);
              // Refresh sibling lists (a Table behind the modal) and,
              // when asked, drop the acted row from this picker.
              if (cfg.invalidate) window.uiInvalidate(cfg.invalidate);
              if (cfg.reload_self) load();
            }).catch(function(err) {
              btn.disabled = false;
              status.textContent = '';
              showToast('Failed: ' + err.message);
            });
          }}, [itemBtnText]);
          var row = el('div', {class: 'ui-actionlist-row'}, [
            el('div', {class: 'ui-actionlist-text'}, [
              // Render the label only when present — items that label their
              // OWN button (item.button) often have no separate row label, and
              // a literal '?' placeholder reads as broken.
              item[labelField] ? el('div', {class: 'ui-actionlist-label'}, [item[labelField]]) : null,
              item[descField] ? el('div', {class: 'ui-actionlist-desc'}, [item[descField]]) : null,
            ]),
            status, btn,
          ]);
          wrap.appendChild(row);
        });
      }).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    }
    load();
    return wrap;
  };

  function renderDotCell(col, value) {
    // Same badges array as the badge renderer, but only Color is
    // honored — the cell renders just a colored dot, no text. Used
    // for status indicators where a label is redundant ("Enabled"
    // next to a green dot is noise).
    var match = null;
    if (col.badges) {
      for (var i = 0; i < col.badges.length; i++) {
        var b = col.badges[i];
        if (b.value === value || (b.value === false && !value) || (b.value === true && !!value)) {
          match = b;
          break;
        }
      }
    }
    var color = match ? (match.color || 'mute') : 'mute';
    var cell = el('div', {class: 'ui-table-cell'});
    if (col.flex) cell.style.flex = col.flex;
    var label = match && match.label ? match.label : '';
    cell.appendChild(el('span', {class: 'ui-dot ' + color, title: label}));
    return cell;
  }
  function renderBadgeCell(col, value) {
    var match = null;
    if (col.badges) {
      for (var i = 0; i < col.badges.length; i++) {
        var b = col.badges[i];
        // Loose equality so JSON false/0/"" all match a Value: false rule.
        if (b.value === value || (b.value === false && !value) || (b.value === true && !!value)) {
          match = b;
          break;
        }
      }
    }
    var label = match ? match.label : (value == null ? '' : String(value));
    var color = match ? (match.color || 'mute') : 'mute';
    var cell = el('div', {class: 'ui-table-cell'});
    if (col.flex) cell.style.flex = col.flex;
    // No matching badge and no meaningful value → render an empty cell, not
    // an empty pill. Lets a column carry an "only when true" badge (e.g. a
    // draft "Needs secret" flag mapped solely for Value:true) without
    // boxing every other row with a blank badge.
    if (!match && (value == null || value === false || value === '')) {
      return cell;
    }
    cell.appendChild(el('span', {class: 'ui-badge ' + color}, [label]));
    return cell;
  }

  components.stack = function(cfg, ctx) {
    var wrap = el('div', {class: 'ui-stack'});
    (cfg.items || []).forEach(function(item) { mountComponent(item, wrap, ctx); });
    return wrap;
  };

  // nav_shell — app-shell layout: left rail of nav buttons, a content pane
  // that swaps to the selected item's body, and an optional header pinned at
  // the top of the content pane (always visible — e.g. a live activity
  // strip). Each item body is a plain component, mounted via mountComponent.
  // The first item is active by default, so the heaviest body (a ChatPanel)
  // should be first to avoid a hidden initial mount.
  components.nav_shell = function(cfg, ctx) {
    // Fill the viewport below the page header so the content pane reaches
    // the window bottom. min-height keeps it usable when dvh is unreliable.
    var outer = el('div', {class: 'ui-navshell', style: 'display:flex;flex-direction:column;height:calc(100dvh - 70px);min-height:420px;border:1px solid var(--border, rgba(127,127,127,0.3));border-radius:6px;overflow:hidden'});

    // Optional top control bar — a horizontal row of controls/dials.
    if (cfg.toolbar && cfg.toolbar.length) {
      var bar = el('div', {style: 'display:flex;gap:0.4rem;align-items:center;flex-wrap:wrap;padding:0.4rem 0.6rem;border-bottom:1px solid var(--border, rgba(127,127,127,0.3));background:var(--bg-1, rgba(127,127,127,0.06))'});
      cfg.toolbar.forEach(function(c) { mountComponent(c, bar, ctx); });
      outer.appendChild(bar);
    }

    var body = el('div', {style: 'display:flex;flex:1;min-height:0'});
    var rail = el('div', {style: 'display:flex;flex-direction:column;gap:0.25rem;padding:0.5rem;min-width:190px;border-right:1px solid var(--border, rgba(127,127,127,0.3));background:var(--bg-1, rgba(127,127,127,0.06));overflow:auto'});
    var right = el('div', {style: 'flex:1;display:flex;flex-direction:column;min-width:0'});

    // Pinned activity strip — always visible regardless of selection.
    if (cfg.header) {
      var hdr = el('div', {style: 'border-bottom:1px solid var(--border, rgba(127,127,127,0.3));padding:0.4rem 0.6rem;background:var(--bg-1, rgba(127,127,127,0.06));max-height:34vh;overflow:auto'});
      mountComponent(cfg.header, hdr, ctx);
      right.appendChild(hdr);
    }

    var content = el('div', {style: 'flex:1;padding:0.75rem;overflow:auto;min-width:0'});
    right.appendChild(content);

    var entries = [];
    function activate(idx) {
      entries.forEach(function(e, i) {
        var on = (i === idx);
        e.panel.style.display = on ? '' : 'none';
        e.btn.style.background = on ? 'var(--bg-2, rgba(127,127,127,0.18))' : 'transparent';
        e.btn.style.fontWeight = on ? '600' : '400';
      });
    }

    (cfg.items || []).forEach(function(item, i) {
      var panel = el('div', {style: 'display:none;height:100%'});
      mountComponent(item.body, panel, ctx);
      content.appendChild(panel);

      var btn = el('button', {
        type: 'button',
        style: 'text-align:left;padding:0.5rem 0.7rem;border:none;border-radius:4px;cursor:pointer;font:inherit;color:var(--text, inherit);background:transparent;width:100%',
        onclick: function() { activate(i); },
      }, [item.label || ('Item ' + (i + 1))]);
      rail.appendChild(btn);

      entries.push({btn: btn, panel: panel});
    });

    body.appendChild(rail);
    body.appendChild(right);
    outer.appendChild(body);
    if (entries.length) activate(0);
    return outer;
  };

  // toolbar — a standalone horizontal row of action buttons (ToolbarAction
  // shape). Lets any page or nav_shell host a reusable control bar; client
  // actions dispatch via window.UIClientActions (the same registry the panel
  // toolbars use), so a specialized seed agent's config controls work here.
  components.toolbar = function(cfg) {
    var bar = el('div', {style: 'display:flex;gap:0.4rem;align-items:center;flex-wrap:wrap'});
    (cfg.actions || []).forEach(function(a) {
      var cls = 'ui-row-btn';
      if (a.variant) cls += ' ' + a.variant;
      var btn = el('button', {type: 'button', class: cls, title: a.title || '', onclick: async function() {
        if (a.confirm && window.uiConfirm && !(await window.uiConfirm(a.confirm))) return;
        var method = a.method || 'POST';
        var url = a.url || '';
        if (method === 'client') {
          var fn = window.UIClientActions && window.UIClientActions[url];
          if (typeof fn === 'function') fn({button: btn, action: a});
          else console.error('toolbar: no handler for client action ' + url);
          return;
        }
        if (method.toUpperCase() === 'GET') { window.location.href = url; return; }
        fetch(url, {method: method, headers: {'Content-Type': 'application/json'}, body: '{}'})
          .catch(function(err){ console.error('toolbar action failed: ' + err.message); });
      }}, [a.label || '?']);
      bar.appendChild(btn);
    });
    return bar;
  };

  // ModalButton — a button that, on click, pops a <dialog> hosting an
  // inner component (typically a FormPanel). Framework owns the dialog
  // chrome (header, subtitle, scrollable body, Close, backdrop,
  // escape-to-close); apps just declare what's inside. Self-saving
  // children (FormPanel auto-save) don't need an explicit save button —
  // the Close button is dismissal. Each click constructs a fresh
  // dialog + mounts a fresh child so source GET fires every open.
  components.modal_button = function(cfg, ctx) {
    var row = el('div', {class: 'ui-modal-button-row'});
    var align = cfg.align || 'right';
    if (align === 'center') row.style.textAlign = 'center';
    else if (align === 'left') row.style.textAlign = 'left';
    else row.style.textAlign = 'right';

    var btnClass = 'ui-row-btn';
    if (cfg.variant) btnClass += ' ' + cfg.variant;
    var btn = el('button', {type: 'button', class: btnClass}, [cfg.label || 'Open']);
    btn.addEventListener('click', function() {
      var dlg = document.createElement('dialog');
      dlg.style.cssText = 'background:var(--bg-1);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:1rem;width:92%;max-width:' + (cfg.width || '520px') + ';max-height:88vh;display:flex;flex-direction:column';

      var title = cfg.title || cfg.label || '';
      if (title) {
        dlg.appendChild(el('h3', {style: 'margin:0 0 0.4rem'}, [title]));
      }
      if (cfg.subtitle) {
        dlg.appendChild(el('p', {style: 'margin:0 0 0.8rem;font-size:0.82rem;color:var(--text-mute);line-height:1.45'}, [cfg.subtitle]));
      }

      // flex:1 1 auto (NOT flex:1) — a flex:1 child has flex-basis:0, which
      // WebKit/WKWebView collapses to ZERO height inside a flex column whose
      // own height is indefinite (a <dialog> sizes to content). Chrome/Firefox
      // fall back to content height so it looked fine in a browser, but in the
      // gohort-desktop webview the modal opened with a blank body. flex-basis
      // auto makes the body size to its content; min-height:0 lets it shrink
      // and scroll when content exceeds the dialog's max-height.
      var body = el('div', {style: 'overflow-y:auto;flex:1 1 auto;min-height:0;padding-right:0.3rem'});
      dlg.appendChild(body);
      if (cfg.body) {
        // Hand the inner component a close hook (namespaced so it can't collide
        // with parent-record fields ctx also carries). A submit-mode FormPanel
        // calls it on a successful save so the dialog auto-dismisses instead of
        // sitting open after the user clicks the submit button.
        var childCtx = {};
        for (var k in (ctx || {})) childCtx[k] = ctx[k];
        childCtx.__closeModal = function() { try { dlg.close(); } catch (_) {} dlg.remove(); };
        mountComponent(cfg.body, body, childCtx);
      }

      var actions = el('div', {style: 'display:flex;gap:0.5rem;justify-content:flex-end;margin-top:0.8rem;padding-top:0.6rem;border-top:1px solid var(--border)'});
      var close = el('button', {type: 'button', class: 'ui-row-btn primary'}, ['Close']);
      close.addEventListener('click', function() { dlg.close(); dlg.remove(); });
      actions.appendChild(close);
      dlg.appendChild(actions);

      document.body.appendChild(dlg);
      if (typeof dlg.showModal === 'function') dlg.showModal();
      // No click-outside-to-close: a text-selection drag that ends past the
      // dialog edge fires a backdrop click and would dismiss the modal
      // mid-copy. Dismiss only via the Close button or Escape.
    });

    row.appendChild(btn);
    return row;
  };

  components.json_view = function(cfg, ctx) {
    var wrap = el('div', {class: 'ui-jsonview'});
    if (cfg.title) wrap.appendChild(el('div', {class: 'ui-jsonview-h'}, [cfg.title]));
    var pre = el('pre', {class: 'ui-jsonview-body'});
    var raw = ctx ? ctx[cfg.field] : null;
    if (raw == null) {
      pre.textContent = '(no data)';
    } else if (typeof raw === 'string') {
      // Try to parse strings that look like JSON for pretty-printing;
      // fall back to the raw string when that fails.
      try { pre.textContent = JSON.stringify(JSON.parse(raw), null, 2); }
      catch (e) { pre.textContent = raw; }
    } else {
      pre.textContent = JSON.stringify(raw, null, 2);
    }
    wrap.appendChild(pre);
    return wrap;
  };

  components.record_view = function(cfg, ctx) {
    var wrap = el('div', {class: 'ui-display'});
    function render(rec) {
      wrap.innerHTML = '';
      (cfg.pairs || []).forEach(function(p) {
        // List pairs render an array field as a readable list. For an
        // array of objects each element is rendered from p.items
        // sub-pairs; for scalars, a single sub-pair with empty field
        // shows each value. Generic — nothing here knows what the list
        // holds (toolbox actions, pipeline steps, an allowlist).
        if (p.items && p.items.length) {
          var arr = lookup(rec, p.field);
          var rowL = el('div', {class: 'ui-display-row ui-display-row-block'}, [
            el('span', {class: 'ui-display-label'}, [p.label]),
          ]);
          if (Array.isArray(arr) && arr.length) {
            var list = el('div', {class: 'ui-display-list'});
            arr.forEach(function(item) {
              var itemEl = el('div', {class: 'ui-display-list-item'});
              p.items.forEach(function(sp) {
                var raw = sp.field ? lookup(item, sp.field) : item;
                if (raw == null || raw === '') return;
                var sub = el('div', {class: 'ui-display-list-field'});
                if (sp.label) sub.appendChild(el('span', {class: 'ui-display-list-key'}, [sp.label + ': ']));
                sub.appendChild(el('span', {class: 'ui-display-list-val' + (sp.mono ? ' mono' : '')}, [fmt(raw, sp.format)]));
                itemEl.appendChild(sub);
              });
              list.appendChild(itemEl);
            });
            rowL.appendChild(list);
          } else {
            rowL.appendChild(el('span', {class: 'ui-display-value mute'}, ['—']));
          }
          wrap.appendChild(rowL);
          return;
        }
        var value = fmt(lookup(rec, p.field), p.format);
        // Block-style pairs (multi-line content: script bodies,
        // pipeline dumps, long command templates) render as a <pre>
        // block on their own row below the label, with mono font +
        // wrap on overflow. Inline pairs stay as a single-row span.
        if (p.block) {
          var rowB = el('div', {class: 'ui-display-row ui-display-row-block'}, [
            el('span', {class: 'ui-display-label'}, [p.label]),
          ]);
          var pre = el('pre', {class: 'ui-display-value-block'});
          pre.textContent = (value == null || value === '') ? '' : String(value);
          rowB.appendChild(pre);
          wrap.appendChild(rowB);
          return;
        }
        wrap.appendChild(el('div', {class: 'ui-display-row'}, [
          el('span', {class: 'ui-display-label'}, [p.label]),
          el('span', {class: 'ui-display-value' + (p.mono ? ' mono' : '')}, [value]),
        ]));
      });
    }
    if (cfg.source) {
      fetchJSON(cfg.source).then(render).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    } else {
      render(ctx || {});
    }
    return wrap;
  };

