// The Curator digest modal — app-specific, injected via ui.Head.ClientAction so
// none of it leaks into core/ui.
//
// The design constraint is stated in curator_web.go: the curator writes without
// asking, so this panel is the review. That drives two choices a prettier panel
// would get wrong. Every outcome renders, including the discards and holds that
// changed nothing — a list of successful placements looks identical whether the
// curator is working well or throwing away everything hard. And superseded text
// renders inline, because "what did this replace" is the question a reader is
// actually asking and sending them to the revision history to answer it is how
// this stops being read.
package guides

// guideCuratorCSS styles the digest list. Small enough to inline; uses the
// framework's theme tokens so it follows light/dark without its own palette.
const guideCuratorCSS = `
.gc-head { display:flex; align-items:baseline; gap:0.6rem; flex-wrap:wrap; margin-bottom:0.8rem; }
.gc-pending { color: var(--text-mute); font-size:0.85rem; }
.gc-run { border:1px solid var(--border); border-radius:8px; padding:0.7rem 0.8rem; margin-bottom:0.7rem; }
.gc-run-top { display:flex; align-items:baseline; gap:0.5rem; flex-wrap:wrap; }
.gc-age { color: var(--text-mute); font-size:0.8rem; }
.gc-summary { margin:0.5rem 0 0.6rem; font-size:0.9rem; }
.gc-err { color: var(--danger, #d9534f); font-size:0.85rem; margin:0.3rem 0; }
.gc-warn { color: var(--danger, #d9534f); font-size:0.8rem; margin:0.3rem 0; }
.gc-entry { border-top:1px solid var(--border); padding:0.5rem 0; display:flex; gap:0.6rem; align-items:flex-start; }
.gc-entry:last-child { padding-bottom:0; }
.gc-body { flex:1 1 auto; min-width:0; }
.gc-topic { font-size:0.9rem; }
.gc-meta { color: var(--text-mute); font-size:0.78rem; margin-top:0.15rem; }
.gc-note { font-size:0.83rem; margin-top:0.25rem; }
.gc-replaced { margin-top:0.4rem; }
.gc-replaced summary { cursor:pointer; color: var(--text-mute); font-size:0.8rem; }
.gc-replaced pre { white-space:pre-wrap; word-break:break-word; background:var(--bg-0);
  border:1px solid var(--border); border-radius:6px; padding:0.5rem; font-size:0.8rem; margin:0.35rem 0 0; }
.gc-pill { font-size:0.7rem; text-transform:uppercase; letter-spacing:0.03em; padding:0.12rem 0.4rem;
  border-radius:4px; border:1px solid var(--border); white-space:nowrap; }
.gc-placed, .gc-created { border-color: var(--accent, #6366f1); }
.gc-superseded, .gc-contradiction { border-color: var(--danger, #d9534f); }
.gc-undone { opacity:0.55; }
.gc-blocked { color: var(--text-mute); font-size:0.75rem; white-space:nowrap; }
.gc-empty { color: var(--text-mute); font-size:0.9rem; }
`

// guideCuratorAction is the 'guides_curator' client action behind the Curator
// toolbar button.
const guideCuratorAction = `function(ctx){
      if (!window.uiOpenSimpleModal) return;
      window.uiOpenSimpleModal({title:'Curator', width:'720px', mount: function(body){
        var host = el('div', {});
        body.appendChild(host);

        function pill(kind){
          var label = kind;
          if (kind === 'contradiction') label = 'flagged';
          return el('span', {class:'gc-pill gc-' + kind}, [label]);
        }

        function entryRow(runID, e){
          var row = el('div', {class:'gc-entry' + (e.undone ? ' gc-undone' : '')});
          row.appendChild(pill(e.kind));
          var b = el('div', {class:'gc-body'});
          b.appendChild(el('div', {class:'gc-topic'}, [e.topic || '(no topic given)']));
          var meta = [];
          if (e.guide_name) meta.push(e.guide_name + (e.section ? ' → ' + e.section : ''));
          if (e.origin) meta.push('from ' + e.origin);
          if (meta.length) b.appendChild(el('div', {class:'gc-meta'}, [meta.join('  ·  ')]));
          if (e.note) b.appendChild(el('div', {class:'gc-note'}, [e.note]));
          if (e.replaced) {
            var d = el('details', {class:'gc-replaced'});
            d.appendChild(el('summary', {}, ['What this replaced']));
            var pre = el('pre', {}); pre.textContent = e.replaced;
            d.appendChild(pre);
            b.appendChild(d);
          }
          row.appendChild(b);
          if (e.undone) {
            row.appendChild(el('span', {class:'gc-blocked'}, ['undone']));
          } else if (e.can_undo) {
            var btn = el('button', {class:'ui-row-btn'}, ['Undo']);
            btn.addEventListener('click', function(){
              btn.disabled = true;
              fetch('curator/undo', {method:'POST', headers:{'Content-Type':'application/json'},
                body: JSON.stringify({run_id: runID, finding_id: e.finding_id})})
                .then(function(r){
                  if (!r.ok) return r.text().then(function(t){ window.uiAlert(t); btn.disabled = false; });
                  load();
                  if (window.uiInvalidate) window.uiInvalidate('guides');
                });
            });
            row.appendChild(btn);
          } else if (e.undo_block) {
            row.appendChild(el('span', {class:'gc-blocked'}, [e.undo_block]));
          }
          return row;
        }

        function runCard(run){
          var card = el('div', {class:'gc-run'});
          var top = el('div', {class:'gc-run-top'});
          var parts = [];
          ['placed','superseded','created','contradiction','discarded','held'].forEach(function(k){
            if (run.counts && run.counts[k]) parts.push(run.counts[k] + ' ' + (k === 'contradiction' ? 'flagged' : k));
          });
          top.appendChild(el('strong', {}, [run.findings + ' findings']));
          if (parts.length) top.appendChild(el('span', {class:'gc-age'}, ['— ' + parts.join(', ')]));
          if (run.age) top.appendChild(el('span', {class:'gc-age'}, [run.age]));
          card.appendChild(top);
          if (run.error) card.appendChild(el('div', {class:'gc-err'},
            ['This run stopped early: ' + run.error + '. Anything listed below was still written.']));
          if (run.unaccounted) card.appendChild(el('div', {class:'gc-warn'},
            [run.unaccounted + ' finding(s) in this batch produced no decision — they were returned to the queue.']));
          if (run.summary) card.appendChild(el('div', {class:'gc-summary'}, [run.summary]));
          (run.entries || []).forEach(function(e){ card.appendChild(entryRow(run.id, e)); });
          return card;
        }

        function load(){
          host.innerHTML = '';
          host.appendChild(el('div', {class:'gc-empty'}, ['Loading…']));
          fetch('curator/runs').then(function(r){ return r.ok ? r.json() : null; }).then(function(d){
            host.innerHTML = '';
            if (!d) { host.appendChild(el('div', {class:'gc-empty'}, ['Could not load the digest.'])); return; }
            var head = el('div', {class:'gc-head'});
            head.appendChild(el('span', {class:'gc-pending'},
              [d.pending ? (d.pending + ' finding(s) waiting to be curated') : 'Nothing waiting']));
            if (d.pending) {
              var now = el('button', {class:'ui-row-btn primary'}, ['Curate now']);
              now.addEventListener('click', function(){
                now.disabled = true; now.textContent = 'Curating…';
                fetch('curator/run', {method:'POST'}).then(function(r){
                  if (!r.ok) return r.text().then(function(t){ window.uiAlert(t); });
                  load();
                  if (window.uiInvalidate) window.uiInvalidate('guides');
                });
              });
              head.appendChild(now);
            }
            host.appendChild(head);
            if (!d.runs || !d.runs.length) {
              host.appendChild(el('div', {class:'gc-empty'},
                ['The curator has not run yet. It runs automatically once enough findings pile up, or on a timer.']));
              return;
            }
            d.runs.forEach(function(run){ host.appendChild(runCard(run)); });
          });
        }
        load();
      }});
}`
