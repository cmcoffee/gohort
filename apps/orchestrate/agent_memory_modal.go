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
      // Custom overlay (not native <dialog>) — <dialog>+showModal has
      // unreliable rendering on iOS / older Android, leaving the modal
      // blank or invisible. Plain fixed-overlay div works everywhere.
      var overlay = document.createElement('div');
      overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;padding:1rem;box-sizing:border-box';
      var dlg = document.createElement('div');
      dlg.style.cssText = 'box-sizing:border-box;background:var(--bg-1);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:1rem;width:100%;max-width:640px;max-height:88vh;display:flex;flex-direction:column';
      overlay.appendChild(dlg);
      function closeDlg() { overlay.remove(); document.removeEventListener('keydown', _esc); }
      function _esc(ev) { if (ev.key === 'Escape') closeDlg(); }
      overlay.addEventListener('click', function(ev) { if (ev.target === overlay) closeDlg(); });
      document.addEventListener('keydown', _esc);
      // Shim so existing closures that call dlg.close()/.remove() keep working.
      dlg.close = closeDlg; dlg.remove = closeDlg;
      var hdr = document.createElement('h3');
      hdr.textContent = 'Memory';
      hdr.style.cssText = 'margin:0 0 0.6rem';
      dlg.appendChild(hdr);
      var body = document.createElement('div');
      body.style.cssText = 'overflow-y:auto;flex:1;padding-right:0.3rem;-webkit-overflow-scrolling:touch';
      dlg.appendChild(body);

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
      document.body.appendChild(overlay);
    });
  }
  register();
})();
</script>`
