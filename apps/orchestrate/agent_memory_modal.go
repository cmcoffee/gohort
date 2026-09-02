// agent_memory_modal.go — the editable Memory surface (Saved facts,
// Reference Memory, Graph Memory) lifted out of apps/agents so every
// surface that fronts an orchestrate agent renders the SAME modal. The
// admin per-agent page, the public /agents app, and per-appliance app
// surfaces (servitor) all call AgentMemoryModalScript; the script is
// defined exactly once and parameterized at the two seams that differ.
//
// core/ui stays domain-agnostic (CLAUDE.md), so this agent-domain JS
// lives in orchestrate — the package both apps/agents and apps/servitor
// already import — not in core/ui.
package orchestrate

import "strings"

// AgentMemoryModalScript returns an ExtraHeadHTML <script> that registers
// a uiRegisterClientAction opening the Memory modal. Two seams vary per
// surface:
//
//	actionName the client-action name a toolbar button targets (e.g.
//	           "agents_memory_modal").
//	baseExpr   a JS expression, evaluated ONCE when the modal opens, that
//	           yields the prefix for the modal's data endpoints (the modal
//	           builds each URL as base + "facts", base + "graph", etc.). A
//	           fixed-URL surface passes a quoted literal — the public agent
//	           app passes "'api/'" so it hits api/facts, api/graph,
//	           api/inferred, api/agent, …. A surface whose target varies per
//	           open passes a call — servitor passes a helper that reads the
//	           selected appliance and returns "/servitor/api/appliances/<id>/".
//	           If the expression yields null/undefined the modal aborts
//	           opening (so a resolver can alert "pick a target first" and
//	           bail), so it should return a falsy value to suppress the modal.
func AgentMemoryModalScript(actionName, baseExpr string) string {
	return strings.NewReplacer(
		"__UI_ACTION__", actionName,
		"__BASE_EXPR__", baseExpr,
	).Replace(agentMemoryModalTemplate)
}

const agentMemoryModalTemplate = `<script>
(function(){
  function register() {
    if (!window.uiRegisterClientAction) { setTimeout(register, 50); return; }
    window.uiRegisterClientAction('__UI_ACTION__', function() {
      // Resolve the endpoint base ONCE at open. A surface whose target
      // varies (servitor's appliance picker) passes a resolver call; if it
      // returns falsy (nothing selected, after alerting), abort opening.
      var MEMBASE = (__BASE_EXPR__);
      if (MEMBASE == null) { return; }
      // Shared modal chrome via uiOpenModal (plain overlay, mobile-safe,
      // Escape-to-close, no backdrop-close). It owns the overlay/card and
      // teardown; we fill the body and footer below. dlg.close/.remove are
      // wired to the full teardown so the existing call sites keep working.
      var _m = window.uiOpenModal({ title: 'Memory', width: '640px', actions: [] });
      var dlg = _m.dialog, body = _m.body;
      function closeDlg() { _m.close(); }
      dlg.close = closeDlg; dlg.remove = closeDlg;

      // Section visibility — set after the agent record loads.
      // Sections always build in the DOM; we just hide via display:none
      // based on flags. If both disable_explicit AND disable_inferred
      // are set, the Memory toolbar button should be hidden by
      // gateMemoryButton (admin surface has its own equivalent); this
      // is defensive in case the modal opens anyway.
      var disabledNotice = document.createElement('div');
      disabledNotice.style.cssText = 'color:var(--text-mute);font-style:italic;padding:1rem 0;text-align:center;display:none';
      disabledNotice.textContent = 'Both Explicit and Reference Memory are disabled for this agent — nothing to manage.';
      body.appendChild(disabledNotice);

      // --- Search section (base + 'memsearch') ---
      // Two modes: 'grep' sweeps everything stored; 'recall' runs the
      // agent's own recall pipeline and shows what a turn would inject.
      // Deletable hits carry an inline remove. Mirrors the admin modal.
      var searchSection = document.createElement('div');
      searchSection.style.cssText = 'margin-bottom:1rem;padding-bottom:0.8rem;border-bottom:1px solid var(--border)';
      var searchRow = document.createElement('div');
      searchRow.style.cssText = 'display:flex;gap:0.4rem;align-items:center';
      var searchInput = document.createElement('input');
      searchInput.type = 'text';
      searchInput.placeholder = 'Search this agent’s memory…';
      searchInput.style.cssText = 'flex:1;background:var(--bg-0);color:var(--text);border:1px solid var(--border);border-radius:4px;padding:0.35rem 0.55rem;font:inherit';
      searchRow.appendChild(searchInput);
      var searchMode = 'grep';
      function modeBtn(label, mode, title) {
        var b = document.createElement('button');
        b.type = 'button'; b.textContent = label; b.title = title; b._mode = mode;
        b.style.cssText = 'padding:0.3rem 0.6rem;border:1px solid var(--border);border-radius:4px;font-size:0.76rem;cursor:pointer;background:var(--bg-1);color:var(--text-mute)';
        b.onclick = function() { searchMode = mode; styleModeBtns(); if (searchInput.value.trim()) runMemSearch(); };
        return b;
      }
      var grepBtn = modeBtn('Stored text', 'grep', 'Literal search of everything stored: facts, findings, knowledge, cortex observations, working notes.');
      var recallBtn = modeBtn('Recall preview', 'recall', 'Run the agent’s own recall pipeline for this query and show exactly what a turn would inject, ranked.');
      function styleModeBtns() {
        [grepBtn, recallBtn].forEach(function(b) {
          var on = b._mode === searchMode;
          b.style.background = on ? 'var(--accent, #6366f1)' : 'var(--bg-1)';
          b.style.color = on ? '#fff' : 'var(--text-mute)';
        });
      }
      styleModeBtns();
      searchRow.appendChild(grepBtn); searchRow.appendChild(recallBtn);
      searchSection.appendChild(searchRow);
      var searchHelp = document.createElement('p');
      searchHelp.style.cssText = 'margin:0.35rem 0 0;color:var(--text-mute);font-size:0.78rem';
      searchHelp.textContent = 'Find memories steering this agent. “Stored text” greps every layer; “Recall preview” shows what the agent actually sees for a query.';
      searchSection.appendChild(searchHelp);
      var searchResults = document.createElement('div');
      searchResults.style.cssText = 'margin-top:0.5rem;display:none;max-height:16rem;overflow-y:auto';
      searchSection.appendChild(searchResults);
      body.appendChild(searchSection);
      function layerChip(layer) {
        var colors = { pinned: '#6366f1', finding: '#0ea5e9', knowledge: '#10b981', history: '#a855f7', cortex: '#f59e0b', notes: '#64748b' };
        var c = document.createElement('span');
        c.textContent = layer;
        c.style.cssText = 'display:inline-block;font-size:0.66rem;text-transform:uppercase;letter-spacing:0.04em;padding:0.05rem 0.4rem;border-radius:3px;color:#fff;flex:0 0 auto;background:' + (colors[layer] || '#64748b');
        return c;
      }
      function renderMemSearch(d) {
        searchResults.innerHTML = '';
        searchResults.style.display = '';
        var items = (d && d.items) || [];
        if (!items.length) {
          var empty = document.createElement('div');
          empty.style.cssText = 'color:var(--text-mute);font-style:italic;font-size:0.82rem;padding:0.3rem 0;white-space:pre-wrap';
          empty.textContent = (d && d.note) ? d.note : 'No matches.';
          searchResults.appendChild(empty);
          return;
        }
        items.forEach(function(item) {
          var row = document.createElement('div');
          row.style.cssText = 'display:flex;gap:0.5rem;align-items:flex-start;padding:0.4rem 0;border-bottom:1px solid var(--border)';
          row.appendChild(layerChip(item.layer));
          var col = document.createElement('div');
          col.style.cssText = 'flex:1;font-size:0.82rem;line-height:1.4;min-width:0';
          if (item.title) {
            var tt = document.createElement('div');
            tt.style.fontWeight = '600'; tt.textContent = item.title;
            col.appendChild(tt);
          }
          var tx = document.createElement('div');
          tx.style.cssText = 'white-space:pre-wrap;word-break:break-word';
          tx.textContent = item.text || '';
          col.appendChild(tx);
          if (item.date || item.note) {
            var meta = document.createElement('div');
            meta.style.cssText = 'color:var(--text-mute);font-size:0.7rem;margin-top:0.1rem';
            meta.textContent = [item.date, item.note].filter(Boolean).join(' · ');
            col.appendChild(meta);
          }
          row.appendChild(col);
          if (item.deletable && item.id) {
            var del = document.createElement('button');
            del.type = 'button';
            del.textContent = String.fromCharCode(215);
            del.title = 'Delete this memory (' + item.id + ')';
            del.style.cssText = 'background:transparent;border:0;color:var(--text-mute);cursor:pointer;font-size:1rem;padding:0 0.4rem;align-self:flex-start';
            del.onclick = function() {
              if (!confirm('Delete this ' + item.layer + ' memory?\n\n' + (item.text || item.title || item.id).slice(0, 200))) return;
              fetch(MEMBASE + 'memsearch?id=' + encodeURIComponent(item.id), {method: 'DELETE'})
                .then(function(r) { if (!r.ok && r.status !== 204) throw new Error('HTTP ' + r.status); row.remove(); })
                .catch(function(err) { alert('Delete failed: ' + (err && err.message || err)); });
            };
            row.appendChild(del);
          }
          searchResults.appendChild(row);
        });
      }
      var memSearchBusy = false;
      function runMemSearch() {
        var q = searchInput.value.trim();
        if (!q || memSearchBusy) return;
        memSearchBusy = true;
        searchResults.style.display = '';
        searchResults.innerHTML = '<div style="color:var(--text-mute);font-style:italic;font-size:0.82rem;padding:0.3rem 0">Searching…</div>';
        fetch(MEMBASE + 'memsearch?q=' + encodeURIComponent(q) + '&mode=' + searchMode)
          .then(function(r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
          .then(renderMemSearch)
          .catch(function(err) {
            searchResults.innerHTML = '';
            var e = document.createElement('div');
            e.style.cssText = 'color:var(--danger,#ff7b72);font-size:0.82rem;padding:0.3rem 0';
            e.textContent = 'Search failed: ' + (err && err.message || err);
            searchResults.appendChild(e);
          })
          .then(function() { memSearchBusy = false; });
      }
      searchInput.addEventListener('keydown', function(ev) {
        if (ev.key === 'Enter') { ev.preventDefault(); runMemSearch(); }
      });

      function renderRowList(container, arr, addLabel, emptyText) {
        container.innerHTML = '';
        arr.forEach(function(text, idx) {
          var row = document.createElement('div');
          row.style.cssText = 'display:flex;gap:0.4rem;align-items:flex-start';
          var inp = document.createElement('input');
          inp.type = 'text';
          inp.value = text;
          inp.style.cssText = 'flex:1;background:var(--bg-0);color:var(--text);border:1px solid var(--border);border-radius:4px;padding:0.3rem 0.5rem;font:inherit';
          inp.addEventListener('input', function(){ arr[idx] = inp.value; });
          var del = document.createElement('button');
          del.textContent = String.fromCharCode(215);
          del.style.cssText = 'background:transparent;border:0;color:var(--text-mute);cursor:pointer;font-size:1rem;padding:0 0.4rem';
          del.addEventListener('click', function(){ arr.splice(idx, 1); renderRowList(container, arr, addLabel, emptyText); });
          row.appendChild(inp); row.appendChild(del);
          container.appendChild(row);
        });
        if (arr.length === 0) {
          var emp = document.createElement('div');
          emp.textContent = emptyText;
          emp.style.cssText = 'color:var(--text-mute);font-size:0.78rem;font-style:italic;padding:0.2rem 0';
          container.appendChild(emp);
        }
        var add = document.createElement('button');
        add.type = 'button';
        add.className = 'ui-row-btn';
        add.style.cssText = 'align-self:flex-start;font-size:0.78rem;padding:0.25rem 0.6rem;margin-top:0.2rem';
        add.textContent = addLabel;
        add.addEventListener('click', function() {
          arr.push('');
          renderRowList(container, arr, addLabel, emptyText);
          var inputs = container.querySelectorAll('input[type=text]');
          var last = inputs[inputs.length - 1];
          if (last) last.focus();
        });
        container.appendChild(add);
      }
      // --- Needs attention (the memory audit) ---
      // Placed FIRST and rendered on open rather than behind a button: nobody
      // clicks an audit, and the note that motivated this survived months of
      // not being looked for. Read-only by design — it points at the layer's
      // own editor below and the owner decides, because wrongly evicting
      // someone's memory is worse than a stale entry.
      var auditWrap = document.createElement('div');
      auditWrap.style.cssText = 'margin-bottom:1rem;padding:0.6rem 0.7rem;border:1px solid var(--danger,#ff7b72);border-radius:6px;background:var(--bg-1);display:none';
      var auditTitle = document.createElement('div');
      auditTitle.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
      auditWrap.appendChild(auditTitle);
      var auditIntro = document.createElement('p');
      auditIntro.style.cssText = 'margin:0 0 0.5rem;color:var(--text-mute);font-size:0.83rem';
      auditIntro.textContent = 'Entries that name something no longer there, or that record work instead of state. Nothing is removed for you — fix each one in its section below.';
      auditWrap.appendChild(auditIntro);
      var auditList = document.createElement('div');
      auditList.style.cssText = 'display:flex;flex-direction:column;gap:0.45rem';
      auditWrap.appendChild(auditList);
      body.appendChild(auditWrap);

      fetch(MEMBASE + 'memaudit').then(function(r){ return r.ok ? r.json() : null; }).then(function(d) {
        var found = (d && d.findings) || [];
        if (!found.length) return;              // silent when clean
        auditWrap.style.display = '';
        auditTitle.textContent = 'Needs attention (' + found.length + ')';
        found.forEach(function(f) {
          var row = document.createElement('div');
          row.style.cssText = 'border-left:2px solid var(--danger,#ff7b72);padding-left:0.55rem';
          var where = document.createElement('div');
          where.style.cssText = 'font-size:0.78rem;font-weight:600;color:var(--text)';
          where.textContent = f.layer;
          row.appendChild(where);
          var why = document.createElement('div');
          why.style.cssText = 'font-size:0.82rem;color:var(--text-mute);margin:0.1rem 0';
          why.textContent = f.detail;
          row.appendChild(why);
          if (f.quote) {
            var q = document.createElement('div');
            q.style.cssText = 'font-size:0.78rem;color:var(--text);opacity:0.85;white-space:pre-wrap;word-break:break-word;margin-top:0.15rem';
            q.textContent = '“' + f.quote + '”';
            row.appendChild(q);
          }
          auditList.appendChild(row);
        });
      });

      // --- Facts section (store_fact entries, framing-aware) ---
      var facts = [];
      var factsWrap = document.createElement('div');
      var factsTitle = document.createElement('div');
      factsTitle.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
      factsTitle.textContent = 'Saved facts';
      factsWrap.appendChild(factsTitle);
      var factsIntro = document.createElement('p');
      factsIntro.style.cssText = 'margin:0 0 0.5rem;color:var(--text-mute);font-size:0.85rem';
      factsIntro.textContent = 'Short notes auto-injected into every system prompt. Remove anything wrong or stale.';
      factsWrap.appendChild(factsIntro);
      var factsList = document.createElement('div');
      factsList.style.cssText = 'display:flex;flex-direction:column;gap:0.35rem';
      factsWrap.appendChild(factsList);
      body.appendChild(factsWrap);
      renderRowList(factsList, facts, '+ Add', '(no entries yet)');
      fetch(MEMBASE + 'facts').then(function(r){ return r.ok ? r.json() : null; }).then(function(d) {
        if (!d) return;
        facts = (d.notes || []).slice();
        var fr = d.framing || {};
        if (fr.block_header) factsTitle.textContent = String(fr.block_header).replace(/^#+\s*/, '');
        if (fr.block_intro) factsIntro.textContent = fr.block_intro;
        renderRowList(factsList, facts, '+ Add', '(no entries yet)');
      });

      // --- Working notes section (the update_notes block) ---
      // The agent rewrites this itself, unprompted, and it renders nearest the
      // TOP of every prompt. It was also the only memory layer with no panel
      // here, so a wrong note steered every turn with nowhere to go and look
      // at it — which is how a stale parked tool call survived across sessions.
      var notesWrap = document.createElement('div');
      notesWrap.style.cssText = 'margin-top:1rem;padding-top:0.8rem;border-top:1px solid var(--border)';
      var notesHeader = document.createElement('div');
      notesHeader.style.cssText = 'display:flex;align-items:center;justify-content:space-between;margin-bottom:0.3rem';
      var notesTitle = document.createElement('div');
      notesTitle.style.cssText = 'font-weight:600;color:var(--text)';
      notesTitle.textContent = 'Working notes';
      notesHeader.appendChild(notesTitle);
      var notesMeta = document.createElement('div');
      notesMeta.style.cssText = 'color:var(--text-mute);font-size:0.75rem';
      notesHeader.appendChild(notesMeta);
      notesWrap.appendChild(notesHeader);
      var notesIntro = document.createElement('p');
      notesIntro.style.cssText = 'margin:0 0 0.5rem;color:var(--text-mute);font-size:0.85rem';
      notesIntro.textContent = 'The agent keeps its own running state here and rewrites it as work moves. Trim anything stale — especially a parked tool call ("pending task: some_tool with x=y"), which it cannot make from a note and will try to work around.';
      notesWrap.appendChild(notesIntro);
      var notesArea = document.createElement('textarea');
      notesArea.rows = 5;
      notesArea.spellcheck = false;
      notesArea.style.cssText = 'width:100%;box-sizing:border-box;font:inherit;font-size:0.82rem;line-height:1.4;padding:0.45rem;border:1px solid var(--border);border-radius:6px;background:var(--bg-1);color:var(--text);resize:vertical';
      notesWrap.appendChild(notesArea);
      var notesBar = document.createElement('div');
      notesBar.style.cssText = 'display:flex;align-items:center;gap:0.4rem;margin-top:0.4rem';
      var notesSave = document.createElement('button');
      notesSave.type = 'button';
      notesSave.style.cssText = 'padding:0.2rem 0.6rem;background:var(--accent,#6366f1);border:1px solid var(--accent,#6366f1);border-radius:4px;color:#fff;font-size:0.76rem;cursor:pointer';
      notesSave.textContent = 'Save';
      notesBar.appendChild(notesSave);
      var notesClear = document.createElement('button');
      notesClear.type = 'button';
      notesClear.style.cssText = 'padding:0.2rem 0.55rem;background:var(--bg-1);border:1px solid var(--border);border-radius:4px;color:var(--danger,#ff7b72);font-size:0.74rem;cursor:pointer';
      notesClear.textContent = 'Clear';
      notesBar.appendChild(notesClear);
      var notesStatus = document.createElement('span');
      notesStatus.style.cssText = 'color:var(--text-mute);font-size:0.76rem';
      notesBar.appendChild(notesStatus);
      notesWrap.appendChild(notesBar);
      // What each register costs. The agent names its own sections and one of
      // them eventually eats the block; the total alone says it is full and not
      // which part to trim. Server-measured (see notes.SectionSizes) and
      // refreshed on load and after each save, so this and the refusal the
      // agent gets quote the same number rather than two parsers' opinions.
      var notesSizes = document.createElement('div');
      notesSizes.style.cssText = 'margin-top:0.35rem;color:var(--text-mute);font-size:0.74rem';
      notesWrap.appendChild(notesSizes);
      body.appendChild(notesWrap);

      var notesCap = 0;
      function notesCount() {
        // The cap is what update_notes enforces; showing the overage here
        // beats a 400 after the user has typed a paragraph.
        if (!notesCap) { notesMeta.textContent = ''; return; }
        var n = notesArea.value.length;
        notesMeta.textContent = n + ' / ' + notesCap;
        notesMeta.style.color = n > notesCap ? 'var(--danger,#ff7b72)' : 'var(--text-mute)';
      }
      notesArea.addEventListener('input', function() { notesCount(); notesStatus.textContent = ''; });
      function renderNotesSizes(sections) {
        // Only worth showing once there is a choice to make: naming the single
        // section of a one-section block tells the reader what they can already
        // see. Editing goes stale until the next save, which is honest — this
        // is a measurement of what is STORED.
        if (!sections || sections.length < 2) { notesSizes.textContent = ''; return; }
        notesSizes.textContent = 'Sections: ' + sections.slice(0, 4).map(function(x) {
          return x.name + ' (' + x.runes + ')';
        }).join(', ') + (sections.length > 4 ? ', …' : '');
      }
      function refreshNotesSizes() {
        fetch(MEMBASE + 'notes').then(function(r){ return r.ok ? r.json() : null; }).then(function(d) {
          if (d) renderNotesSizes(d.sections);
        }).catch(function() {});
      }
      function notesPut(text, okMsg) {
        notesStatus.style.color = 'var(--text-mute)';
        notesStatus.textContent = 'Saving...';
        fetch(MEMBASE + 'notes', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({text: text})
        }).then(function(r) {
          if (!r.ok) { return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); }); }
          notesStatus.textContent = okMsg;
          notesCount();
          refreshNotesSizes();
        }).catch(function(e) {
          notesStatus.style.color = 'var(--danger,#ff7b72)';
          notesStatus.textContent = String(e.message || e);
        });
      }
      notesSave.addEventListener('click', function() { notesPut(notesArea.value, 'Saved.'); });
      notesClear.addEventListener('click', function() {
        if (!confirm('Clear this agent\'s working notes? It will start the next turn with none.')) return;
        notesArea.value = '';
        notesPut('', 'Cleared.');
      });
      fetch(MEMBASE + 'notes').then(function(r){ return r.ok ? r.json() : null; }).then(function(d) {
        if (!d) { notesWrap.style.display = 'none'; return; }
        if (!d.enabled && !d.can_enable) {
          // Off, and this reader has no way to turn them on: an app agent's
          // flags live in its code-registered spec and its record is hidden
          // from the pickers. A section explaining a setting nobody can reach
          // is worse than no section — it reads as something broken, and the
          // remedy it names does not exist.
          notesWrap.style.display = 'none';
          return;
        }
        if (!d.enabled) {
          // Opt-in per agent, and reachable. Say so rather than showing an
          // editor whose contents would never reach a prompt.
          notesArea.disabled = true; notesSave.disabled = true; notesClear.disabled = true;
          notesIntro.textContent = 'Working notes are turned off for this agent, so nothing here reaches its prompt. Enable them in the agent editor to give it a running-state scratchpad.';
        }
        notesCap = d.cap || 0;
        notesArea.value = d.text || '';
        notesCount();
        renderNotesSizes(d.sections);
        if (d.from_seed) {
          notesStatus.textContent = 'Showing the configured seed — the agent has not written notes yet.';
        } else if (d.updated_at && String(d.updated_at).indexOf('0001-01-01') !== 0) {
          notesStatus.textContent = 'Agent last rewrote this ' + new Date(d.updated_at).toLocaleString() + '.';
        }
      });

      // --- Reference Memory section (read-only list w/ delete + wipe) ---
      // Vector-grown derived chunks (memory_save findings, synthesis
      // auto-ingest). Read-only — editing embeddings doesn't make
      // sense; the affordance is "prune drift" not "rewrite."
      var inferredWrap = document.createElement('div');
      inferredWrap.style.cssText = 'margin-top:1rem;padding-top:0.8rem;border-top:1px solid var(--border)';
      var inferredHeader = document.createElement('div');
      inferredHeader.style.cssText = 'display:flex;align-items:center;justify-content:space-between;margin-bottom:0.3rem';
      var inferredTitle = document.createElement('div');
      inferredTitle.style.cssText = 'font-weight:600;color:var(--text)';
      inferredTitle.textContent = 'Reference Memory';
      inferredHeader.appendChild(inferredTitle);
      var wipeBtn = document.createElement('button');
      wipeBtn.type = 'button';
      wipeBtn.style.cssText = 'padding:0.2rem 0.55rem;background:var(--bg-1);border:1px solid var(--border);border-radius:4px;color:var(--danger,#ff7b72);font-size:0.74rem;cursor:pointer';
      wipeBtn.textContent = 'Wipe all';
      wipeBtn.disabled = true;
      inferredHeader.appendChild(wipeBtn);
      inferredWrap.appendChild(inferredHeader);
      var inferredIntro = document.createElement('p');
      inferredIntro.style.cssText = 'margin:0 0 0.5rem;color:var(--text-mute);font-size:0.85rem';
      inferredIntro.textContent = 'Vector-grown chunks from memory_save + synthesis auto-ingest. Searchable by similarity, not always in prompt. Delete individual entries that drifted, or wipe all if recall is biasing the agent toward stale patterns.';
      inferredWrap.appendChild(inferredIntro);
      var inferredList = document.createElement('div');
      inferredList.style.cssText = 'display:flex;flex-direction:column;gap:0.35rem';
      inferredWrap.appendChild(inferredList);
      body.appendChild(inferredWrap);

      // --- Graph Memory section (read-only list + per-entity / per-link delete) ---
      // The visitor's OWN entities + relationships the agent linked about them via
      // link_entities, recalled on demand with recall_about. Rides the Explicit
      // Memory gate (hidden when disable_explicit), same as in Agency. Read + delete.
      var graphWrap = document.createElement('div');
      graphWrap.style.cssText = 'margin-top:1rem;padding-top:0.8rem;border-top:1px solid var(--border)';
      var graphTitle = document.createElement('div');
      graphTitle.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
      graphTitle.textContent = 'Graph Memory';
      graphWrap.appendChild(graphTitle);
      var graphIntro = document.createElement('div');
      graphIntro.style.cssText = 'color:var(--text-mute);font-size:0.8rem;margin-bottom:0.5rem';
      graphIntro.textContent = 'Entities and relationships this agent has recorded about you. Delete an entity (with its links) or a single relationship to prune what it remembers.';
      graphWrap.appendChild(graphIntro);
      var graphList = document.createElement('div');
      graphWrap.appendChild(graphList);
      body.appendChild(graphWrap);

      function renderGraph(data) {
        graphList.innerHTML = '';
        var ents = (data && data.entities) || [];
        var counts = (data && data.counts) || {};
        if (counts.entities != null) {
          graphTitle.textContent = 'Graph Memory (' + counts.entities + ' entit' + (counts.entities === 1 ? 'y' : 'ies') + ', ' + (counts.edges || 0) + ' link' + (counts.edges === 1 ? '' : 's') + ')';
        }
        if (!ents.length) {
          var empty = document.createElement('div');
          empty.style.cssText = 'color:var(--text-mute);font-style:italic;padding:0.4rem 0';
          empty.textContent = 'No graph entries yet. Relationships the agent records about you will appear here.';
          graphList.appendChild(empty);
          return;
        }
        ents.forEach(function(e) {
          var row = document.createElement('div');
          row.style.cssText = 'display:flex;align-items:flex-start;gap:0.5rem;padding:0.4rem 0;border-bottom:1px solid var(--border)';
          var col = document.createElement('div');
          col.style.cssText = 'flex:1;font-size:0.85rem;line-height:1.4';
          var head = document.createElement('div');
          var nm = document.createElement('span');
          nm.style.fontWeight = '600';
          nm.textContent = e.name;
          head.appendChild(nm);
          if (e.kind) {
            var kd = document.createElement('span');
            kd.style.cssText = 'color:var(--text-mute);font-size:0.74rem;margin-left:0.35rem';
            kd.textContent = '(' + e.kind + ')';
            head.appendChild(kd);
          }
          col.appendChild(head);
          if (e.aliases && e.aliases.length) {
            var al = document.createElement('div');
            al.style.cssText = 'color:var(--text-mute);font-size:0.72rem';
            al.textContent = 'aka ' + e.aliases.join(', ');
            col.appendChild(al);
          }
          if (e.attrs) {
            Object.keys(e.attrs).sort().forEach(function(k) {
              var at = document.createElement('div');
              at.style.cssText = 'color:var(--text-mute);font-size:0.74rem';
              at.textContent = k + ': ' + e.attrs[k];
              col.appendChild(at);
            });
          }
          (e.edges || []).forEach(function(ed) {
            var er = document.createElement('div');
            er.style.cssText = 'display:flex;align-items:center;gap:0.35rem;margin-top:0.15rem';
            var lbl = document.createElement('span');
            lbl.style.fontSize = '0.8rem';
            lbl.textContent = String.fromCharCode(8594) + ' ' + ed.rel + ' ' + (ed.to_name || ed.to) + (ed.note ? ' (' + ed.note + ')' : '');
            er.appendChild(lbl);
            var edel = document.createElement('span');
            edel.style.cssText = 'cursor:pointer;color:var(--text-mute);font-size:0.85rem';
            edel.textContent = String.fromCharCode(215);
            edel.title = 'Remove this relationship';
            edel.onclick = function() {
              if (!confirm('Remove the relationship: ' + e.name + ' ' + ed.rel + ' ' + (ed.to_name || ed.to) + '?')) return;
              var u = MEMBASE + 'graph/edge?from=' + encodeURIComponent(e.id) + '&rel=' + encodeURIComponent(ed.rel) + '&to=' + encodeURIComponent(ed.to);
              fetch(u, {method: 'DELETE'}).then(function(r){ if (!r.ok && r.status !== 204) throw new Error('HTTP ' + r.status); er.remove(); }).catch(function(err){ alert('Delete failed: ' + (err && err.message || err)); });
            };
            er.appendChild(edel);
            col.appendChild(er);
          });
          var del = document.createElement('button');
          del.type = 'button';
          del.style.cssText = 'padding:0.15rem 0.45rem;background:var(--bg-1);border:1px solid var(--border);border-radius:4px;color:var(--danger,#ff7b72);font-size:0.85rem;cursor:pointer;flex:0 0 auto';
          del.textContent = String.fromCharCode(215);
          del.title = 'Delete this entity and all its relationships';
          del.onclick = function() {
            if (!confirm('Delete ' + e.name + ' and all its relationships?')) return;
            fetch(MEMBASE + 'graph/entity/' + encodeURIComponent(e.id), {method: 'DELETE'}).then(function(r){ if (!r.ok && r.status !== 204) throw new Error('HTTP ' + r.status); row.remove(); }).catch(function(err){ alert('Delete failed: ' + (err && err.message || err)); });
          };
          row.appendChild(col);
          row.appendChild(del);
          graphList.appendChild(row);
        });
      }

      fetch(MEMBASE + 'graph').then(function(r){ return r.ok ? r.json() : null; }).then(function(d){ renderGraph(d); }).catch(function(){ renderGraph(null); });

      function renderInferred(items) {
        inferredList.innerHTML = '';
        wipeBtn.disabled = !items || !items.length;
        if (!items || !items.length) {
          var emp = document.createElement('div');
          emp.style.cssText = 'color:var(--text-mute);font-size:0.78rem;font-style:italic;padding:0.2rem 0';
          emp.textContent = 'No memory entries yet. memory_save findings will appear here once the agent decides something is worth remembering.';
          inferredList.appendChild(emp);
          return;
        }
        items.forEach(function(item) {
          var row = document.createElement('div');
          row.style.cssText = 'display:flex;gap:0.4rem;align-items:flex-start;padding:0.35rem 0;border-bottom:1px solid var(--border)';
          var col = document.createElement('div');
          col.style.cssText = 'flex:1;font-size:0.85rem;line-height:1.4';
          // Collapsed by default (match Agency): the topic line is the disclosure
          // trigger; the chunk text stays hidden until clicked, so the list reads
          // as a scannable set of topics even with many entries.
          var topic = document.createElement('div');
          topic.style.cssText = 'color:var(--text-mute);font-size:0.7rem;text-transform:uppercase;letter-spacing:0.04em;cursor:pointer;user-select:none';
          var topicCaret = document.createElement('span');
          topicCaret.style.cssText = 'display:inline-block;margin-right:0.4rem;transition:transform 0.15s';
          topicCaret.textContent = String.fromCharCode(9656); // ▸
          topic.appendChild(topicCaret);
          topic.appendChild(document.createTextNode((item.topic || 'general') + (item.source_doc ? ' · ' + item.source_doc : '')));
          col.appendChild(topic);
          var content = document.createElement('div');
          content.style.cssText = 'white-space:pre-wrap;margin-top:0.15rem;display:none';
          content.textContent = item.content || '';
          col.appendChild(content);
          topic.addEventListener('click', function() {
            var open = content.style.display === 'none';
            content.style.display = open ? '' : 'none';
            topicCaret.style.transform = open ? 'rotate(90deg)' : '';
          });
          var del = document.createElement('button');
          del.type = 'button';
          del.textContent = String.fromCharCode(215);
          del.title = 'Delete this entry';
          del.style.cssText = 'background:transparent;border:0;color:var(--text-mute);cursor:pointer;font-size:1rem;padding:0 0.4rem;align-self:flex-start';
          del.addEventListener('click', function() {
            if (!confirm('Delete this Reference Memory entry?')) return;
            fetch(MEMBASE + 'inferred/' + encodeURIComponent(item.id), {method: 'DELETE'})
              .then(function(r){ if (!r.ok && r.status !== 204) throw new Error('HTTP ' + r.status); row.remove(); })
              .catch(function(err){ alert('Delete failed: ' + (err && err.message || err)); });
          });
          row.appendChild(col); row.appendChild(del);
          inferredList.appendChild(row);
        });
      }

      wipeBtn.addEventListener('click', function() {
        if (!confirm('Wipe every Reference Memory entry for this agent. Uploaded files in Knowledge are NOT affected. Continue?')) return;
        wipeBtn.disabled = true;
        fetch(MEMBASE + 'knowledge/auto-inferred', {method: 'DELETE'})
          .then(function(r){ return r.ok ? r.json() : null; })
          .then(function(d){
            renderInferred([]);
            if (d) inferredIntro.textContent = 'Wiped ' + (d.removed || 0) + ' entr' + (d.removed === 1 ? 'y' : 'ies') + '. ' + inferredIntro.textContent;
          })
          .catch(function(err){ alert('Wipe failed: ' + (err && err.message || err)); wipeBtn.disabled = false; });
      });

      fetch(MEMBASE + 'inferred')
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(d){ renderInferred(d ? d.items : []); })
        .catch(function(){ renderInferred([]); });

      // --- Gate sections based on agent's disable flags ---
      fetch(MEMBASE + 'agent').then(function(r){ return r.ok ? r.json() : null; }).then(function(a) {
        if (!a) return;
        if (a.disable_explicit) factsWrap.style.display = 'none';
        if (a.disable_explicit) graphWrap.style.display = 'none'; // graph rides the Explicit gate
        if (a.disable_inferred) inferredWrap.style.display = 'none';
        if (a.disable_explicit && a.disable_inferred) {
          disabledNotice.style.display = '';
        }
      }).catch(function(){});

      // --- Footer: Cancel + Save (saves facts only — Inferred is
      // per-entry delete; Notes auto-write paths are gone) ---
      var actions = document.createElement('div');
      actions.style.cssText = 'display:flex;gap:0.5rem;justify-content:flex-end;margin-top:0.8rem;padding-top:0.6rem;border-top:1px solid var(--border)';
      var cancel = document.createElement('button');
      cancel.textContent = 'Cancel';
      cancel.className = 'ui-row-btn';
      cancel.addEventListener('click', function(){ dlg.close(); dlg.remove(); });
      var save = document.createElement('button');
      save.textContent = 'Save';
      save.className = 'ui-row-btn primary';
      save.addEventListener('click', function() {
        var cleanFacts = facts.map(function(n){ return String(n||'').trim(); }).filter(Boolean);
        save.disabled = true;
        fetch(MEMBASE + 'facts', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({notes: cleanFacts}),
        }).then(function(r){
          if (!r.ok && r.status !== 204) return r.text().then(function(t){ throw new Error(t); });
          dlg.close(); dlg.remove();
        }).catch(function(err){ save.disabled = false; alert('Save failed: ' + (err && err.message || err)); });
      });
      actions.appendChild(cancel);
      actions.appendChild(save);
      dlg.appendChild(actions);
    });
  }
  register();
})();
</script>`
