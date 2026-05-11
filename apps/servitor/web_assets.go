// App-specific UI bits loaded into the servitor chat page's <head>.
// Two responsibilities:
//
//  1. CSS for servitor-specific block shapes (intent narration, plan
//     checklist, draft preview, etc.) so the shared core/ui CSS stays
//     domain-agnostic.
//  2. JS registering block renderers for the four servitor event
//     kinds that the chat_bridge.go translator routes through
//     `kind: "block"`: servitor_intent, servitor_plan,
//     servitor_notes_consumed, servitor_draft. The framework calls
//     window.UIBlockRenderers[<type>] with the event data and
//     expects a {wrap, body, onDone?} object back.
//  3. JS registering client actions for the chat toolbar
//     (servitor_open_facts, servitor_open_rules, servitor_run_map).
//     Each opens a modal that hits the existing legacy endpoints.

package servitor

const servitorWebAssets = `<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css">
<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
<style>
.ui-servitor-intent {
  background: var(--bg-2);
  border-left: 3px solid var(--accent);
  border-radius: 4px;
  padding: 0.55rem 0.75rem;
  margin: 0.3rem 0;
  align-self: flex-start;
  max-width: 92%;
}
.ui-servitor-intent-label {
  font-size: 0.72rem; font-weight: 600;
  color: var(--accent);
  text-transform: uppercase; letter-spacing: 0.04em;
  margin-bottom: 0.2rem;
}
.ui-servitor-intent-task   { color: var(--text-hi); line-height: 1.4; }
.ui-servitor-intent-reason {
  color: var(--text-mute);
  font-size: 0.82rem;
  margin-top: 0.25rem;
  font-style: italic;
}

.ui-servitor-plan {
  background: var(--bg-2);
  border-left: 3px solid var(--accent);
  border-radius: 4px;
  padding: 0.65rem 0.85rem;
  margin: 0.4rem 0;
  align-self: stretch;
  max-width: 100%;
}
.ui-servitor-plan-h {
  font-size: 0.78rem; font-weight: 600;
  color: var(--accent);
  text-transform: uppercase; letter-spacing: 0.04em;
  margin-bottom: 0.5rem;
}
.ui-servitor-plan-steps { list-style: none; padding: 0; margin: 0; }
.ui-servitor-plan-step {
  display: grid; grid-template-columns: 1.4em 1fr;
  column-gap: 0.4rem; row-gap: 0.1rem;
  padding: 0.25rem 0;
  border-bottom: 1px solid var(--border);
  font-size: 0.88rem; line-height: 1.4;
}
.ui-servitor-plan-step:last-child { border-bottom: none; }
.ui-servitor-plan-mark {
  font-family: monospace; color: var(--text-mute);
  text-align: center; align-self: start;
}
.ui-servitor-plan-title { color: var(--text-hi); }
.ui-servitor-plan-detail {
  grid-column: 2;
  color: var(--text-mute); font-size: 0.78rem; font-style: italic;
}
.ui-servitor-plan-findings {
  grid-column: 2;
  color: var(--text); font-size: 0.82rem;
  background: var(--bg-1); padding: 0.2rem 0.4rem;
  border-left: 2px solid #3fb950;
  border-radius: 0 3px 3px 0;
  margin-top: 0.15rem;
}
.ui-servitor-plan-blocked-reason {
  grid-column: 2;
  color: #f85149; font-size: 0.82rem;
}
.ui-servitor-plan-step.in_progress .ui-servitor-plan-mark { color: var(--accent); }
.ui-servitor-plan-step.done        .ui-servitor-plan-mark { color: #3fb950; }
.ui-servitor-plan-step.blocked     .ui-servitor-plan-mark { color: #f85149; }
.ui-servitor-plan-step.done .ui-servitor-plan-title {
  color: var(--text-mute); text-decoration: line-through;
}

.ui-servitor-draft {
  background: var(--bg-2);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 0.55rem 0.75rem;
  margin: 0.3rem 0;
  align-self: flex-start;
  max-width: 92%;
  white-space: pre-wrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.85rem;
}
.ui-servitor-draft-h {
  font-size: 0.72rem; font-weight: 600; text-transform: uppercase;
  letter-spacing: 0.04em; color: var(--text-hi);
  margin-bottom: 0.35rem;
  font-family: inherit;
}

.ui-servitor-facts-list { margin-bottom: 0.6rem; }
.ui-servitor-fact {
  padding: 0.35rem 0.5rem;
  border-bottom: 1px solid var(--border);
  /* Long unbroken tokens (paths, hashes, command output without
   * spaces) must wrap within the row instead of pushing the modal
   * wider than the viewport. */
  min-width: 0;
  overflow-wrap: anywhere;
  word-break: break-word;
}
.ui-servitor-fact-head {
  display: flex; align-items: center; gap: 0.5rem;
}
.ui-servitor-fact-head .ui-servitor-fact-k { flex: 1 1 auto; }
.ui-servitor-fact-k {
  font-weight: 600; color: var(--text-hi);
  font-size: 0.85rem;
  overflow-wrap: anywhere;
  word-break: break-word;
}
.ui-servitor-fact-v {
  color: var(--text); font-size: 0.85rem;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  word-break: break-word;
  margin-top: 0.15rem;
}
.ui-servitor-facts-add {
  display: flex; gap: 0.4rem;
  padding-top: 0.4rem;
  border-top: 1px solid var(--border);
}

.ui-servitor-profile-h {
  font-size: 0.78rem; color: var(--text-mute);
  margin-bottom: 0.5rem;
}
.ui-servitor-app-tabs {
  display: flex; gap: 0.4rem; margin: 0.5rem 0;
}
.ui-servitor-app-section {
  display: flex; flex-direction: column; gap: 0.3rem;
  margin: 0.5rem 0;
  padding-bottom: 0.5rem;
  border-bottom: 1px solid var(--border);
}
.ui-servitor-app-section label {
  font-size: 0.78rem; color: var(--text-mute);
  margin-top: 0.2rem;
}
.ui-form-hint {
  font-size: 0.78rem; color: var(--text-mute);
  font-style: italic;
}
.ui-servitor-profile-body {
  white-space: pre-wrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.82rem;
  line-height: 1.5;
  background: var(--bg-2);
  padding: 0.6rem 0.8rem;
  border-radius: 4px;
  overflow-wrap: anywhere;
  word-break: break-word;
}

.ui-servitor-rules-list { margin-bottom: 0.6rem; }
.ui-servitor-rule {
  display: flex; gap: 0.5rem; align-items: center;
  padding: 0.35rem 0.5rem;
  border-bottom: 1px solid var(--border);
}
.ui-servitor-rule-text { flex: 1 1 auto; font-size: 0.85rem; }
.ui-servitor-rules-add {
  display: flex; gap: 0.4rem;
  padding-top: 0.4rem;
  border-top: 1px solid var(--border);
}

.ui-servitor-supp-h {
  font-size: 0.78rem; font-weight: 600;
  color: var(--text-hi); text-transform: uppercase;
  letter-spacing: 0.04em;
  margin-bottom: 0.4rem;
}
.ui-servitor-supp-list { margin-bottom: 0.6rem; }
.ui-servitor-supp-row {
  padding: 0.4rem 0.5rem; border-bottom: 1px solid var(--border);
}
.ui-servitor-supp-hdr {
  display: flex; gap: 0.5rem; align-items: center;
  margin-bottom: 0.3rem;
}
.ui-servitor-supp-name {
  flex: 1; font-weight: 600; color: var(--text-hi);
  overflow-wrap: anywhere; word-break: break-word;
}
.ui-servitor-supp-status {
  font-size: 0.72rem; color: var(--text-mute);
}
.ui-servitor-supp-attach {
  display: flex; flex-direction: column; gap: 0.4rem;
  padding-top: 0.5rem;
  border-top: 1px solid var(--border);
}

.ui-servitor-revs-list { margin-bottom: 0.6rem; }
.ui-servitor-rev-row {
  display: grid; grid-template-columns: 1fr auto auto auto;
  gap: 0.5rem; align-items: center;
  padding: 0.35rem 0.5rem;
  border-bottom: 1px solid var(--border);
}
.ui-servitor-rev-date { font-size: 0.85rem; color: var(--text-hi); }
.ui-servitor-rev-size { font-size: 0.72rem; color: var(--text-mute); }
.ui-servitor-interject-actions {
  display: flex; gap: 0.3rem; margin-top: 0.4rem;
  justify-content: flex-end;
}
.ui-servitor-interject-btn {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 4px;
  padding: 0.15rem 0.5rem; font-size: 0.72rem; cursor: pointer;
}
.ui-servitor-interject-btn:hover { color: var(--text-hi); border-color: var(--accent); }
.ui-servitor-interject-btn.primary { color: var(--accent); border-color: var(--accent); }
.ui-servitor-interject-btn.danger { color: var(--danger); border-color: var(--danger); }
.ui-servitor-interject-btn:disabled { opacity: 0.5; cursor: default; }
.ui-servitor-interject-edit-row {
  display: flex; gap: 0.3rem; margin-top: 0.3rem;
  justify-content: flex-end;
}
/* Once the orchestrator drains the note (notes_consumed handler adds
 * .consumed), the edit/delete affordances disappear — the note is
 * locked-in history. */
.ui-agent-interjection.consumed .ui-servitor-interject-actions { display: none; }

.ui-servitor-revs-preview {
  margin-top: 0.5rem; padding: 0.6rem 0.8rem;
  background: var(--bg-1); border-radius: 4px;
  white-space: pre-wrap;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.82rem; line-height: 1.5;
  max-height: 16em; overflow-y: auto;
  overflow-wrap: anywhere; word-break: break-word;
}

.ui-servitor-save-row {
  display: flex; gap: 0.4rem; flex-wrap: wrap;
  margin-top: 0.5rem;
  padding-top: 0.5rem;
  border-top: 1px solid var(--border);
}
.ui-servitor-save-btn {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 4px;
  padding: 0.2rem 0.55rem; font-size: 0.78rem; cursor: pointer;
}
.ui-servitor-save-btn:hover { color: var(--text-hi); border-color: var(--accent); }
.ui-servitor-save-btn:disabled { opacity: 0.6; cursor: default; }
.ui-servitor-save-btn.saved {
  color: #3fb950; border-color: #3fb950;
}

.ui-servitor-modal-footer {
  display: flex; gap: 0.4rem; justify-content: flex-end;
  padding-top: 0.8rem; margin-top: 0.8rem;
  border-top: 1px solid var(--border);
}
</style>
<script>
(function() {
  function register() {
    if (!window.uiRegisterBlockRenderer) return;
    var el = window.uiEl;

    // Intent narration — orchestrator delegating an investigation
    // task. Renders in the conversation pane as a labeled block so
    // the user can read the agent's strategic thinking inline.
    window.uiRegisterBlockRenderer('servitor_intent', function(d) {
      var wrap = el('div', {class: 'ui-servitor-intent'});
      wrap.appendChild(el('div', {class: 'ui-servitor-intent-label'}, ['▸ Investigating']));
      if (d.text) {
        wrap.appendChild(el('div', {class: 'ui-servitor-intent-task'}, [d.text]));
      }
      if (d.reason) {
        wrap.appendChild(el('div', {class: 'ui-servitor-intent-reason'}, [d.reason]));
      }
      return {wrap: wrap, body: null};
    });

    // Plan checklist — mirrors legacy's renderPlan. One block per
    // session (id="servitor-plan"); the dispatcher calls onUpdate
    // on subsequent plan_set/plan_step events so the checklist
    // refreshes in place. Status values come straight from the
    // server-side PlanStep: pending / in_progress / done / blocked.
    window.uiRegisterBlockRenderer('servitor_plan', function(d, ctx) {
      var wrap = el('div', {class: 'ui-servitor-plan'});
      var hdr = el('div', {class: 'ui-servitor-plan-h'}, ['▸ Investigation Plan']);
      wrap.appendChild(hdr);
      var stepsBox = el('ul', {class: 'ui-servitor-plan-steps'});
      wrap.appendChild(stepsBox);
      function render(plan) {
        stepsBox.innerHTML = '';
        (plan || []).forEach(function(step) {
          var status = (step.status || 'pending').toLowerCase();
          var mark = '·';
          if (status === 'pending')     mark = '○';
          if (status === 'in_progress') mark = '●';
          if (status === 'done')        mark = '✓';
          if (status === 'blocked')     mark = '⚠';
          var row = el('li', {class: 'ui-servitor-plan-step ' + status});
          row.appendChild(el('span', {class: 'ui-servitor-plan-mark'}, [mark]));
          row.appendChild(el('span', {class: 'ui-servitor-plan-title'},
            [step.title || '']));
          if (step.what_to_find) {
            row.appendChild(el('span', {class: 'ui-servitor-plan-detail'},
              [step.what_to_find]));
          }
          if (step.findings) {
            row.appendChild(el('span', {class: 'ui-servitor-plan-findings'},
              ['↳ ' + step.findings]));
          }
          if (step.blocked_reason) {
            row.appendChild(el('span', {class: 'ui-servitor-plan-blocked-reason'},
              ['blocked: ' + step.blocked_reason]));
          }
          stepsBox.appendChild(row);
        });
      }
      render(d.plan);
      return {
        wrap: wrap, body: stepsBox,
        onUpdate: function(next) { render(next.plan); },
      };
    });

    // Per-note edit/delete on interjections. The runtime tags
    // bubbles with .ui-agent-interjection + data-session-id +
    // data-inject-url; the server-issued note id lands on
    // data-note-id once the inject POST returns. We watch the
    // conversation log for new bubbles and decorate them with
    // Edit + Delete buttons. Buttons hide automatically once the
    // bubble gets the .consumed class (notes_consumed handler).
    function decorateInterjection(bubble) {
      if (!bubble || bubble.querySelector('.ui-servitor-interject-actions')) return;
      var actions = el('div', {class: 'ui-servitor-interject-actions'});
      var editBtn = el('button', {class: 'ui-servitor-interject-btn',
        onclick: function(ev) { ev.stopPropagation(); editInterjection(bubble); }},
        ['Edit']);
      var delBtn = el('button',
        {class: 'ui-servitor-interject-btn danger',
         onclick: function(ev) { ev.stopPropagation(); deleteInterjection(bubble); }},
        ['Delete']);
      actions.appendChild(editBtn);
      actions.appendChild(delBtn);
      bubble.appendChild(actions);
    }

    function editInterjection(bubble) {
      var noteID = bubble.dataset.noteId;
      var sid    = bubble.dataset.sessionId;
      var url    = bubble.dataset.injectUrl || 'api/inject';
      if (!noteID || !sid) {
        alert('Note has no id yet — try again in a moment.');
        return;
      }
      // Lock first so the orchestrator doesn't drain it mid-edit.
      fetch(url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: sid, note_id: noteID, action: 'lock'}),
      }).then(function(r) {
        if (r.status === 410) {
          alert('Note has already been picked up by the agent.');
          return null;
        }
        if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
        return r;
      }).then(function(r) {
        if (!r) return;
        var body = bubble.querySelector('.ui-agent-msg-body');
        var actions = bubble.querySelector('.ui-servitor-interject-actions');
        if (!body) return;
        var oldText = body.textContent;
        var ta = document.createElement('textarea');
        ta.value = oldText; ta.className = 'ui-form-input';
        ta.style.width = '100%'; ta.rows = 3;
        body.style.display = 'none';
        if (actions) actions.style.display = 'none';
        bubble.appendChild(ta);
        var editRow = document.createElement('div');
        editRow.className = 'ui-servitor-interject-edit-row';
        var saveBtn = document.createElement('button');
        saveBtn.className = 'ui-servitor-interject-btn primary';
        saveBtn.textContent = 'Save';
        saveBtn.onclick = function() {
          var newText = ta.value.trim();
          if (!newText) { newText = oldText; }
          saveBtn.disabled = true;
          fetch(url, {
            method: 'PATCH', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id: sid, note_id: noteID, text: newText}),
          }).then(function(r) {
            if (r.status === 410) { alert('Note already picked up.'); }
            else if (!r.ok) { throw new Error('save failed'); }
            body.textContent = newText;
            cancel();
          }).catch(function(err) {
            saveBtn.disabled = false;
            alert('Save failed: ' + (err && err.message || err));
          });
        };
        var cancelBtn = document.createElement('button');
        cancelBtn.className = 'ui-servitor-interject-btn';
        cancelBtn.textContent = 'Cancel';
        cancelBtn.onclick = function() {
          fetch(url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({id: sid, note_id: noteID, action: 'unlock'}),
          }).then(cancel);
        };
        function cancel() {
          ta.remove(); editRow.remove();
          body.style.display = '';
          if (actions) actions.style.display = '';
        }
        editRow.appendChild(saveBtn);
        editRow.appendChild(cancelBtn);
        bubble.appendChild(editRow);
        ta.focus();
      }).catch(function(err) {
        alert('Lock failed: ' + (err && err.message || err));
      });
    }

    function deleteInterjection(bubble) {
      var noteID = bubble.dataset.noteId;
      var sid    = bubble.dataset.sessionId;
      var url    = bubble.dataset.injectUrl || 'api/inject';
      if (!noteID || !sid) return;
      if (!confirm('Delete this queued note?')) return;
      // Server's handleInject reads from JSON body regardless of
      // method, so DELETE carries the same {id, note_id} shape as
      // the other ops.
      fetch(url, {
        method: 'DELETE',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: sid, note_id: noteID}),
      }).then(function(r) {
        if (r.status === 410) { alert('Note already picked up by the agent.'); return; }
        if (!r.ok) { throw new Error('delete failed'); }
        bubble.remove();
      }).catch(function(err) {
        alert('Delete failed: ' + (err && err.message || err));
      });
    }

    // MutationObserver scans the conversation log for new
    // interjection bubbles and decorates them. Wait until the
    // convo log exists (panel mounts after DOMContentLoaded).
    function watchInterjections() {
      var log = document.querySelector('.ui-agent-convo-log');
      if (!log) { setTimeout(watchInterjections, 200); return; }
      // Decorate any already present.
      log.querySelectorAll('.ui-agent-interjection').forEach(decorateInterjection);
      var mo = new MutationObserver(function(records) {
        records.forEach(function(rec) {
          rec.addedNodes.forEach(function(node) {
            if (node.nodeType !== 1) return;
            if (node.classList && node.classList.contains('ui-agent-interjection')) {
              decorateInterjection(node);
            }
          });
        });
      });
      mo.observe(log, {childList: true});
    }
    watchInterjections();

    // notes_consumed — orchestrator drained these queued notes.
    // Mark the matching interjection bubbles in the conversation
    // pane as consumed (solid styling) so the user sees their
    // note has been picked up. Also emit a quiet status row in
    // the activity pane for tracebility.
    window.uiRegisterBlockRenderer('servitor_notes_consumed', function(d) {
      var ids = d.ids || [];
      ids.forEach(function(noteID) {
        var bubble = document.querySelector(
          '.ui-agent-interjection[data-note-id="' +
          (window.CSS && CSS.escape ? CSS.escape(noteID) : noteID) +
          '"]'
        );
        if (bubble) bubble.classList.add('consumed');
      });
      var n = ids.length;
      var wrap = el('div', {class: 'ui-agent-act ui-agent-act-status'},
        [n + ' note' + (n === 1 ? '' : 's') + ' picked up by agent']);
      return {wrap: wrap, body: null, pane: 'activity'};
    });

    // draft — workspace draft preview emitted when the user asks
    // the agent to compose a writeup. For v1 we just dump the text
    // into the conversation as a monospace block; the legacy
    // "open the draft in a side panel" UX comes later.
    window.uiRegisterBlockRenderer('servitor_draft', function(d) {
      var wrap = el('div', {class: 'ui-servitor-draft'});
      wrap.appendChild(el('div', {class: 'ui-servitor-draft-h'}, ['Draft']));
      wrap.appendChild(document.createTextNode(d.text || ''));
      return {wrap: wrap, body: null};
    });

    // --- Toolbar client actions -------------------------------------
    // Each action opens a simple modal that hits the existing
    // legacy endpoint. v1 of the port keeps the modal HTML inline;
    // a later pass can promote these to dedicated routes.

    function getApplianceID() {
      var sel = document.querySelector('.ui-agent-extras select');
      return sel ? sel.value : '';
    }

    // openModal — title bar with × icon in the upper-right, then
    // bodyEl. Returns an object so callers can append a footer
    // (e.g. Cancel + Save buttons) and close programmatically:
    //   var m = openModal('Title', body);
    //   m.footer(saveBtn, cancelBtn);     // optional
    //   m.close();                         // programmatic dismiss
    // Esc closes the modal; clicking the dim overlay does too.
    function openModal(title, bodyEl) {
      var overlay = el('div', {class: 'ui-pl-modal-overlay'});
      var modal = el('div', {class: 'ui-pl-modal'});
      // × in upper-right corner, no button-styled "Close" in header.
      var closeBtn = el('button', {
        class: 'ui-pl-modal-close', title: 'Close',
        onclick: function(){ close(); },
      }, ['×']);
      var hdr = el('div', {class: 'ui-pl-modal-h'},
        [el('span', {text: title})]);
      modal.appendChild(hdr);
      modal.appendChild(closeBtn);
      modal.appendChild(bodyEl);
      var footer = null;
      overlay.appendChild(modal);
      document.body.appendChild(overlay);
      function close() {
        overlay.remove();
        document.removeEventListener('keydown', onKey);
      }
      // Esc closes — but only when focus is on the body / a button,
      // never on an input or textarea. A stray Escape from a form
      // field (browser autocomplete, IME, password manager) would
      // otherwise wipe the modal in the middle of editing. Also
      // skip when the modal hasn't been around for at least 200ms,
      // so synthetic key events from openModal's caller don't trip.
      var openedAt = Date.now();
      function onKey(ev) {
        if (ev.key !== 'Escape') return;
        if (Date.now() - openedAt < 200) return;
        var a = document.activeElement;
        if (a && (a.tagName === 'INPUT' || a.tagName === 'TEXTAREA' ||
                  a.tagName === 'SELECT' || a.isContentEditable)) {
          return;
        }
        close();
      }
      document.addEventListener('keydown', onKey);
      overlay.addEventListener('click', function(ev) {
        if (ev.target === overlay) close();
      });
      return {
        close: close,
        // footer(...buttons) — adds a button row at the bottom of
        // the modal. Use for action+Cancel pairs on modals that
        // commit state (forms, confirmations); skip on read-only
        // modals where the × is enough.
        footer: function() {
          if (footer) footer.remove();
          footer = el('div', {class: 'ui-servitor-modal-footer'});
          for (var i = 0; i < arguments.length; i++) {
            footer.appendChild(arguments[i]);
          }
          modal.appendChild(footer);
        },
      };
    }

    // Refresh the count badge on the Facts toolbar button. The
    // legacy UI shows "Facts (N)"; we mirror that. Fires on appliance
    // change + after the facts modal commits an add/delete.
    function refreshFactsBadge() {
      var aid = getApplianceID();
      var btn = null;
      // Toolbar buttons live in .ui-agent-actions; find by label.
      var btns = document.querySelectorAll('.ui-agent-actions button');
      for (var i = 0; i < btns.length; i++) {
        if (btns[i].textContent.indexOf('Facts') === 0) { btn = btns[i]; break; }
      }
      if (!btn) return;
      if (!aid) { btn.textContent = 'Facts'; return; }
      fetch('api/facts?id=' + encodeURIComponent(aid))
        .then(function(r) { return r.ok ? r.json() : []; })
        .then(function(facts) {
          var n = Array.isArray(facts) ? facts.length : 0;
          btn.textContent = n > 0 ? 'Facts (' + n + ')' : 'Facts';
        });
    }
    // Hook the appliance picker so the badge stays in sync.
    document.addEventListener('change', function(ev) {
      if (ev.target && ev.target.matches &&
          ev.target.matches('.ui-agent-extras select')) {
        refreshFactsBadge();
      }
    });
    // Initial fetch — wait a tick so the toolbar is mounted.
    setTimeout(refreshFactsBadge, 50);

    window.uiRegisterClientAction('servitor_open_facts', function() {
      var aid = getApplianceID();
      if (!aid) { alert('Pick an appliance first'); return; }
      var listBox = el('div', {class: 'ui-servitor-facts-list'},
        [el('div', {class: 'ui-pl-empty'}, ['Loading…'])]);
      // Add-fact row at the top — key + value inputs + Add button.
      var keyIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'Key', style: 'flex:1'});
      var valIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'Value', style: 'flex:2'});
      var addBtn = el('button', {class: 'ui-row-btn primary',
        onclick: function() {
          var k = keyIn.value.trim(), v = valIn.value.trim();
          if (!k || !v) return;
          addBtn.disabled = true;
          fetch('api/facts', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({appliance_id: aid, key: k, value: v}),
          }).then(function(r) {
            addBtn.disabled = false;
            if (!r.ok) { alert('Failed to add fact'); return; }
            keyIn.value = ''; valIn.value = '';
            refresh();
            refreshFactsBadge();
          });
        }}, ['Add']);
      var addRow = el('div', {class: 'ui-servitor-facts-add'},
        [keyIn, valIn, addBtn]);

      function refresh() {
        fetch('api/facts?id=' + encodeURIComponent(aid))
          .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.json();
          })
          .then(function(facts) {
            listBox.innerHTML = '';
            if (!Array.isArray(facts) || !facts.length) {
              listBox.appendChild(el('div', {class: 'ui-pl-empty'},
                ['No facts yet.']));
              return;
            }
            facts.forEach(function(f) {
              var item = el('div', {class: 'ui-servitor-fact'});
              var head = el('div', {class: 'ui-servitor-fact-head'},
                [el('div', {class: 'ui-servitor-fact-k'}, [f.key || ''])]);
              var delBtn = el('button', {class: 'ui-row-btn danger',
                onclick: function() {
                  if (!confirm('Delete fact "' + (f.key || '') + '"?')) return;
                  fetch('api/facts?key=' + encodeURIComponent(f.id),
                    {method: 'DELETE'}).then(function() {
                    refresh();
                    refreshFactsBadge();
                  });
                }}, ['Delete']);
              head.appendChild(delBtn);
              item.appendChild(head);
              item.appendChild(el('div', {class: 'ui-servitor-fact-v'},
                [f.value || '']));
              listBox.appendChild(item);
            });
          })
          .catch(function(err) {
            listBox.innerHTML = '';
            listBox.appendChild(el('div', {class: 'ui-pl-empty'},
              ['Failed to load: ' + (err && err.message || err)]));
          });
      }
      var body = el('div', {class: 'ui-pl-modal-body'}, [listBox, addRow]);
      var m = openModal('Facts', body);
      m.footer(el('button', {class: 'ui-row-btn primary',
        onclick: function(){ m.close(); }}, ['Done']));
      refresh();
    });

    window.uiRegisterClientAction('servitor_open_rules', function() {
      var aid = getApplianceID();
      if (!aid) { alert('Pick an appliance first'); return; }
      // Rules are stored as a LIST of records (one rule each), not
      // a single blob. Render a list + an "add new" row.
      var listBox = el('div', {class: 'ui-servitor-rules-list'},
        [el('div', {class: 'ui-pl-empty'}, ['Loading…'])]);
      var addInput = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'New rule…', style: 'flex:1'});
      var addBtn = el('button', {class: 'ui-row-btn primary',
        onclick: function() {
          var text = addInput.value.trim();
          if (!text) return;
          addBtn.disabled = true;
          fetch('api/rules?appliance_id=' + encodeURIComponent(aid), {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({appliance_id: aid, rule: text}),
          }).then(function(r) {
            addBtn.disabled = false;
            if (!r.ok) { alert('Failed to add rule'); return; }
            addInput.value = '';
            refresh();
          });
        }}, ['Add']);
      var addRow = el('div', {class: 'ui-servitor-rules-add'}, [addInput, addBtn]);

      function refresh() {
        fetch('api/rules?appliance_id=' + encodeURIComponent(aid))
          .then(function(r) {
            if (!r.ok) { throw new Error('HTTP ' + r.status); }
            return r.json();
          })
          .then(function(rules) {
            listBox.innerHTML = '';
            if (!Array.isArray(rules) || !rules.length) {
              listBox.appendChild(el('div', {class: 'ui-pl-empty'}, ['No rules yet.']));
              return;
            }
            rules.forEach(function(rec) {
              var row = el('div', {class: 'ui-servitor-rule'});
              row.appendChild(el('div', {class: 'ui-servitor-rule-text'},
                [rec.rule || '']));
              row.appendChild(el('button', {
                class: 'ui-row-btn danger',
                onclick: function() {
                  if (!confirm('Delete this rule?')) return;
                  fetch('api/rules/' + encodeURIComponent(rec.id),
                    {method: 'DELETE'}).then(refresh);
                },
              }, ['Delete']));
              listBox.appendChild(row);
            });
          })
          .catch(function(err) {
            listBox.innerHTML = '';
            listBox.appendChild(el('div', {class: 'ui-pl-empty'},
              ['Failed to load: ' + (err && err.message || err)]));
          });
      }
      var body = el('div', {class: 'ui-pl-modal-body'}, [listBox, addRow]);
      var m = openModal('Rules', body);
      // Footer with Done — rules autosave on Add/Delete, so the
      // footer is just a dismissal affordance with proper button
      // styling. The × in the corner does the same thing; both
      // present matches the rest of the framework's modal UX.
      var doneBtn = el('button', {class: 'ui-row-btn primary',
        onclick: function(){ m.close(); }}, ['Done']);
      m.footer(doneBtn);
      refresh();
    });

    window.uiRegisterClientAction('servitor_open_profile', function() {
      var aid = getApplianceID();
      if (!aid) { alert('Pick an appliance first'); return; }
      var body = el('div', {class: 'ui-pl-modal-body'},
        [el('div', {text: 'Loading…'})]);
      var m = openModal('System Profile', body);
      fetch('api/profile?appliance_id=' + encodeURIComponent(aid))
        .then(function(r) {
          if (!r.ok) throw new Error('HTTP ' + r.status);
          return r.json();
        })
        .then(function(d) {
          body.innerHTML = '';
          if (!d.profile) {
            body.appendChild(el('div', {class: 'ui-pl-empty'},
              ['No profile yet. Run "Map System" to scan this appliance.']));
            return;
          }
          var hdr = el('div', {class: 'ui-servitor-profile-h'},
            [d.name || '', d.scanned ? ' — scanned ' + d.scanned : '']);
          body.appendChild(hdr);
          body.appendChild(el('div', {class: 'ui-servitor-profile-body'},
            [d.profile]));
        })
        .catch(function(err) {
          body.innerHTML = '';
          body.appendChild(el('div', {class: 'ui-pl-empty'},
            ['Failed to load: ' + (err && err.message || err)]));
        });
    });

    // Build the inline appliance editor — same field set as the
    // legacy modal and the /manage form. Two flavors: empty (new)
    // or prefilled (edit). Saves via POST /api/appliances; after
    // success the appliance picker reloads so the new/changed
    // record shows up immediately.
    function openApplianceModal(existing) {
      var isEdit = !!(existing && existing.id);
      var rec = existing || {type: 'ssh', port: 22};

      var nameIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'e.g. web-prod-01', value: rec.name || ''});

      // Type tabs — SSH vs Local Command.
      var typeSsh = el('button', {class: 'ui-row-btn',
        onclick: function(){ setType('ssh'); }}, ['SSH']);
      var typeCmd = el('button', {class: 'ui-row-btn',
        onclick: function(){ setType('command'); }}, ['Local Command']);
      var typeRow = el('div', {class: 'ui-servitor-app-tabs'}, [typeSsh, typeCmd]);

      // SSH section
      var hostIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'hostname or IP', value: rec.host || ''});
      var portIn = el('input', {type: 'number', class: 'ui-form-input',
        min: 1, max: 65535, value: rec.port || 22});
      var userIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'root', value: rec.user || ''});
      var passIn = el('input', {type: 'password', class: 'ui-form-input',
        placeholder: isEdit
          ? '(unchanged — type to replace)'
          : 'leave blank to use server SSH key',
        value: ''});
      var sshSection = el('div', {class: 'ui-servitor-app-section'}, [
        el('label', {}, ['Host *']), hostIn,
        el('label', {}, ['Port']),   portIn,
        el('label', {}, ['User']),   userIn,
        el('label', {}, ['Password']), passIn,
        el('div', {class: 'ui-form-hint'},
          ['Password is stored in the server database. If omitted, the server\'s default SSH key is used.']),
      ]);

      // Command section
      var cmdIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'e.g. kubectl, ./manage.py', value: rec.command || ''});
      var workIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'e.g. /opt/myapp (optional)', value: rec.work_dir || ''});
      var envIn = el('textarea', {class: 'ui-form-input', rows: 3,
        placeholder: 'KEY=VALUE (one per line)'});
      envIn.value = (rec.env_vars || []).join('\n');
      var cmdSection = el('div', {class: 'ui-servitor-app-section'}, [
        el('label', {}, ['Command *']), cmdIn,
        el('div', {class: 'ui-form-hint'},
          ['The command the AI will invoke. Arguments can be included.']),
        el('label', {}, ['Working Directory']), workIn,
        el('label', {}, ['Environment Variables']), envIn,
      ]);

      function setType(t) {
        rec.type = t;
        typeSsh.classList.toggle('primary', t === 'ssh');
        typeCmd.classList.toggle('primary', t === 'command');
        sshSection.style.display = t === 'ssh' ? '' : 'none';
        cmdSection.style.display = t === 'command' ? '' : 'none';
      }
      setType(rec.type || 'ssh');

      // Shared fields
      var instrIn = el('textarea', {class: 'ui-form-input', rows: 3,
        placeholder: 'Optional. App-specific CLI tools, known quirks, workflow notes…'});
      instrIn.value = rec.instructions || '';
      var personaNameIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'Persona name (e.g. Support, QA, DevOps)',
        value: rec.persona_name || ''});
      var personaPromptIn = el('textarea', {class: 'ui-form-input', rows: 3,
        placeholder: 'Describe how the AI should approach this appliance…'});
      personaPromptIn.value = rec.persona_prompt || '';

      var body = el('div', {class: 'ui-pl-modal-body'}, [
        el('label', {}, ['Name *']), nameIn,
        typeRow, sshSection, cmdSection,
        el('label', {}, ['Custom Instructions']), instrIn,
        el('label', {}, ['Persona']),
        personaNameIn, personaPromptIn,
      ]);

      var m = openModal(isEdit ? 'Edit Appliance' : 'New Appliance', body);

      var saveBtn = el('button', {class: 'ui-row-btn primary',
        onclick: function() {
          var payload = {
            id:             rec.id || '',
            type:           rec.type,
            name:           nameIn.value.trim(),
            host:           hostIn.value.trim(),
            port:           parseInt(portIn.value, 10) || 22,
            user:           userIn.value.trim(),
            password:       passIn.value,
            command:        cmdIn.value.trim(),
            work_dir:       workIn.value.trim(),
            env_vars:       envIn.value.split('\n').map(function(s){return s.trim();}).filter(Boolean),
            instructions:   instrIn.value,
            persona_name:   personaNameIn.value.trim(),
            persona_prompt: personaPromptIn.value,
          };
          if (!payload.name) { alert('Name is required'); return; }
          if (payload.type === 'ssh' && !payload.host) { alert('Host is required'); return; }
          if (payload.type === 'command' && !payload.command) { alert('Command is required'); return; }
          saveBtn.disabled = true;
          fetch('api/appliances', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(payload),
          }).then(function(r) {
            saveBtn.disabled = false;
            if (!r.ok) { return r.text().then(function(t){ alert('Save failed: ' + t); }); }
            m.close();
            // Reload the page so the appliance picker reflects the
            // change. Simpler than threading a refresh callback up
            // through ExtraFields rebuild — the page is cheap.
            window.location.reload();
          });
        }}, [isEdit ? 'Save' : 'Create']);
      var cancelBtn = el('button', {class: 'ui-row-btn',
        onclick: function(){ m.close(); }}, ['Cancel']);
      m.footer(cancelBtn, saveBtn);

      // Edit-only: delete affordance.
      if (isEdit) {
        var delBtn = el('button', {class: 'ui-row-btn danger',
          style: 'margin-right: auto',
          onclick: function() {
            if (!confirm('Delete this appliance? All saved facts, rules, and workspaces will remain but become orphaned.')) return;
            fetch('api/appliance/' + encodeURIComponent(rec.id),
              {method: 'DELETE'}).then(function() {
              m.close();
              window.location.reload();
            });
          }}, ['Delete']);
        m.footer(delBtn, cancelBtn, saveBtn);
      }
    }

    window.uiRegisterClientAction('servitor_new_appliance', function() {
      openApplianceModal(null);
    });
    window.uiRegisterClientAction('servitor_edit_appliance', function() {
      var aid = getApplianceID();
      if (!aid) { alert('Pick an appliance first'); return; }
      fetch('api/appliance/' + encodeURIComponent(aid))
        .then(function(r) {
          if (!r.ok) throw new Error('HTTP ' + r.status);
          return r.json();
        })
        .then(function(rec) { openApplianceModal(rec); })
        .catch(function(err) {
          alert('Failed to load appliance: ' + (err && err.message || err));
        });
    });

    // Per-reply save buttons. Mirrors legacy's addChatMsg
    // affordances: assistant replies get Workspace / TechWriter /
    // CodeWriter buttons depending on which destinations are
    // available. The destinations probe happens once per page.
    var saveDestinations = null;
    function loadSaveDestinations() {
      return fetch('api/save_destinations')
        .then(function(r) { return r.ok ? r.json() : {}; })
        .then(function(d) { saveDestinations = d || {}; })
        .catch(function() { saveDestinations = {}; });
    }
    loadSaveDestinations();

    function getWorkspaceID() {
      // Active workspace lives in the AgentLoopPanel rail; its id
      // is the value of any session row carrying the 'active'
      // class. Picked from the DOM at button-click time so a
      // workspace switch mid-conversation reflects correctly.
      var active = document.querySelector('.ui-chat-side-item.active');
      if (!active) return '';
      // The rail row stores nothing public, but its first text
      // child is the title and the runtime sets activeContextId
      // internally — we don't have direct access. Fall back to
      // reading the URL's deep-link param (matches what the
      // runtime mirrors there).
      try {
        var qs = new URL(window.location.href).searchParams;
        return qs.get('session') || '';
      } catch (_) { return ''; }
    }

    window.uiRegisterMessageDecorator(function(m) {
      if (m.role !== 'assistant') return;
      if (!m.rawText || !m.rawText.trim()) return;
      // De-dupe: re-finalize calls (markdown re-render) shouldn't
      // append another button row to the same bubble.
      if (m.wrap.querySelector('.ui-servitor-save-row')) return;
      // Wait for the destinations probe if it hasn't returned yet.
      if (saveDestinations === null) {
        loadSaveDestinations().then(function(){ decorate(); });
        return;
      }
      decorate();
      function decorate() {
        var row = document.createElement('div');
        row.className = 'ui-servitor-save-row';

        var wsBtn = makeBtn(getWorkspaceID() ? 'Save to Workspace' : 'Create Workspace', function() {
          var wsID = getWorkspaceID();
          // Find the user question that preceded this assistant
          // bubble — walk back through the convo log. Server's
          // create endpoint requires a non-empty question to use
          // as the workspace name.
          var question = '';
          var prev = m.wrap.previousElementSibling;
          while (prev) {
            if (prev.classList && prev.classList.contains('ui-agent-msg-user')) {
              var body = prev.querySelector('.ui-agent-msg-body');
              if (body) question = body.textContent.trim();
              break;
            }
            prev = prev.previousElementSibling;
          }
          if (!question) question = 'Saved from chat';

          wsBtn.disabled = true; wsBtn.textContent = 'Saving…';
          if (wsID) {
            // Append entry to existing workspace.
            fetch('api/workspace/save', {
              method: 'POST', headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({
                workspace_id: wsID,
                question:     question,
                answer:       m.rawText,
              }),
            }).then(function(r) {
              wsBtn.disabled = false;
              wsBtn.textContent = r.ok ? 'Saved ✓' : 'Failed';
              if (r.ok) wsBtn.classList.add('saved');
            });
            return;
          }
          // No active workspace — create one named after the user
          // question (server truncates if too long). After success
          // we navigate to the new workspace so future saves attach
          // to it and the left rail picks it up.
          var aid = getApplianceID();
          if (!aid) {
            alert('Pick an appliance first');
            wsBtn.disabled = false; wsBtn.textContent = 'Create Workspace';
            return;
          }
          fetch('api/workspace/create', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
              appliance_id: aid,
              question:     question,
              answer:       m.rawText,
            }),
          }).then(function(r) {
            if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
            return r.json();
          }).then(function(d) {
            wsBtn.classList.add('saved');
            wsBtn.textContent = '✓ Workspace Created';
            // Navigate to the new workspace so it becomes the active
            // context for future saves and shows up in the rail.
            try {
              var u = new URL(window.location.href);
              u.searchParams.set('session', d.id);
              window.history.replaceState({}, '', u.toString());
              // Update labels on any other Save buttons still on
              // the page so they read "Save to Workspace" now.
              document.querySelectorAll('.ui-servitor-save-btn').forEach(function(b) {
                if (!b.classList.contains('saved') && b.textContent === 'Create Workspace') {
                  b.textContent = 'Save to Workspace';
                }
              });
            } catch (_) {}
            // Refresh the left rail so the new workspace shows up
            // immediately. AgentLoopPanel matches by base URL so
            // the {appliance_id}-templated source still gets hit.
            window.uiInvalidate('api/workspace/list');
          }).catch(function(err) {
            wsBtn.disabled = false; wsBtn.textContent = 'Create Workspace';
            alert('Create workspace failed: ' + (err && err.message || err));
          });
        });
        row.appendChild(wsBtn);

        if (saveDestinations.techwriter) {
          var twBtn = makeBtn('↗ TechWriter', function() {
            twBtn.disabled = true; twBtn.textContent = 'Saving…';
            fetch('api/save_article', {
              method: 'POST', headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({text: m.rawText}),
            }).then(function(r) {
              twBtn.disabled = false;
              twBtn.textContent = r.ok ? 'Saved ✓' : 'Failed';
              if (r.ok) twBtn.classList.add('saved');
            });
          });
          row.appendChild(twBtn);
        }
        if (saveDestinations.codewriter) {
          var cwBtn = makeBtn('↗ CodeWriter', function() {
            cwBtn.disabled = true; cwBtn.textContent = 'Saving…';
            fetch('api/save_snippet', {
              method: 'POST', headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({text: m.rawText}),
            }).then(function(r) {
              cwBtn.disabled = false;
              cwBtn.textContent = r.ok ? 'Saved ✓' : 'Failed';
              if (r.ok) cwBtn.classList.add('saved');
            });
          });
          row.appendChild(cwBtn);
        }

        m.wrap.appendChild(row);
      }
      function makeBtn(label, onclick) {
        var b = document.createElement('button');
        b.className = 'ui-servitor-save-btn';
        b.textContent = label;
        b.onclick = onclick;
        return b;
      }
    });

    // Workspace modal — draft editing, synthesis, supplements.
    // Same feature set as the legacy "Workspaces" / "Draft" panel
    // (showWorkspaces + showDraftPanel), collapsed into one modal
    // scoped to the currently active workspace.
    window.uiRegisterClientAction('servitor_open_workspace', function(ctx) {
      try {
        var qs = new URL(window.location.href).searchParams;
        var wsID = qs.get('session') || '';
      } catch (_) { wsID = ''; }
      if (!wsID) {
        alert('Pick a workspace from the left rail first (or create one with + New workspace).');
        return;
      }

      var draftArea = el('textarea', {class: 'ui-form-input',
        rows: 14, style: 'width:100%; font-family:monospace'});
      var supplementsBox = el('div', {class: 'ui-servitor-supp-list'});
      var statusLine = el('div', {class: 'ui-form-hint', style: 'min-height:1em'});

      var fileIn = el('input', {type: 'file', class: 'ui-form-input'});
      var promptIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'Usage instruction (how/when should the LLM reference this?)'});
      var attachBtn = el('button', {class: 'ui-row-btn',
        onclick: function() { attachSupplement(); }}, ['Attach Document']);
      var attachRow = el('div', {class: 'ui-servitor-supp-attach'},
        [fileIn, promptIn, attachBtn]);

      var body = el('div', {class: 'ui-pl-modal-body'}, [
        el('label', {}, ['Draft']), draftArea,
        statusLine,
        el('div', {class: 'ui-servitor-supp-h', style: 'margin-top:1rem'},
          ['Supplements']),
        supplementsBox,
        attachRow,
      ]);

      var m = openModal('Workspace', body);

      var saveBtn = el('button', {class: 'ui-row-btn',
        onclick: function() { saveDraft(); }}, ['Save Draft']);
      var genBtn = el('button', {class: 'ui-row-btn primary',
        onclick: function() { generateDraft(); }}, ['Generate Draft']);
      var viewBtn = el('button', {class: 'ui-row-btn',
        onclick: function() {
          window.open('api/workspace/view?id=' + encodeURIComponent(wsID),
            '_blank', 'noopener');
        }}, ['View Draft']);
      var revBtn = el('button', {class: 'ui-row-btn',
        onclick: function() { openRevisions(); }}, ['Revisions']);
      var closeBtn = el('button', {class: 'ui-row-btn',
        onclick: function(){ m.close(); }}, ['Close']);
      m.footer(viewBtn, revBtn, closeBtn, saveBtn, genBtn);

      // Revisions modal — list of prior drafts (newest first), each
      // with Preview + Revert. Revert snapshots the current draft
      // first so the revert is itself undoable (server-side behavior).
      function openRevisions() {
        var listBox = el('div', {class: 'ui-servitor-revs-list'},
          [el('div', {class: 'ui-pl-empty'}, ['Loading…'])]);
        var preview = el('div', {class: 'ui-servitor-revs-preview',
          style: 'display:none'});
        var body = el('div', {class: 'ui-pl-modal-body'}, [listBox, preview]);
        var rm = openModal('Revisions', body);

        fetch('api/workspace/revisions?id=' + encodeURIComponent(wsID))
          .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.json();
          })
          .then(function(revs) {
            listBox.innerHTML = '';
            if (!Array.isArray(revs) || !revs.length) {
              listBox.appendChild(el('div', {class: 'ui-pl-empty'},
                ['No revisions yet. Save the draft (or run Generate Draft) to create one.']));
              return;
            }
            revs.forEach(function(rev) {
              var row = el('div', {class: 'ui-servitor-rev-row'});
              row.appendChild(el('div', {class: 'ui-servitor-rev-date',
                text: rev.date || ''}));
              var sizeKB = Math.max(1, Math.round((rev.body || '').length / 1024));
              row.appendChild(el('div', {class: 'ui-servitor-rev-size',
                text: sizeKB + ' KB'}));
              var prevBtn = el('button', {class: 'ui-row-btn',
                onclick: function() {
                  preview.style.display = '';
                  preview.textContent = rev.body || '';
                }}, ['Preview']);
              var revertBtn = el('button', {class: 'ui-row-btn primary',
                onclick: function() {
                  if (!confirm('Replace the current draft with this revision? The current draft will itself become a revision so this is undoable.')) return;
                  revertBtn.disabled = true; revertBtn.textContent = 'Reverting…';
                  fetch('api/workspace/revert', {
                    method: 'POST', headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({
                      workspace_id: wsID, revision_id: rev.id,
                    }),
                  }).then(function(r) {
                    if (!r.ok) throw new Error('revert failed');
                    rm.close();
                    // Refresh the underlying workspace modal so the
                    // user sees the reverted draft text.
                    loadWorkspace();
                  }).catch(function(err) {
                    revertBtn.disabled = false;
                    revertBtn.textContent = 'Revert';
                    alert('Revert failed: ' + (err && err.message || err));
                  });
                }}, ['Revert']);
              row.appendChild(prevBtn);
              row.appendChild(revertBtn);
              listBox.appendChild(row);
            });
          })
          .catch(function(err) {
            listBox.innerHTML = '';
            listBox.appendChild(el('div', {class: 'ui-pl-empty'},
              ['Failed to load: ' + (err && err.message || err)]));
          });

        rm.footer(el('button', {class: 'ui-row-btn',
          onclick: function(){ rm.close(); }}, ['Done']));
      }

      function loadWorkspace() {
        return fetch('api/workspace/' + encodeURIComponent(wsID))
          .then(function(r){ return r.json(); })
          .then(function(ws) {
            draftArea.value = (ws && ws.draft) || '';
            renderSupplements((ws && ws.supplements) || []);
            return ws;
          });
      }
      loadWorkspace().catch(function(err) {
        statusLine.textContent = 'Load failed: ' + (err && err.message || err);
      });

      function saveDraft() {
        saveBtn.disabled = true; saveBtn.textContent = 'Saving…';
        fetch('api/workspace/draft', {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({workspace_id: wsID, draft: draftArea.value}),
        }).then(function(r) {
          saveBtn.disabled = false;
          saveBtn.textContent = r.ok ? 'Saved' : 'Failed';
          setTimeout(function(){ saveBtn.textContent = 'Save Draft'; }, 1500);
        });
      }

      function generateDraft() {
        genBtn.disabled = true; genBtn.textContent = 'Generating…';
        fetch('api/workspace/synthesize', {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({workspace_id: wsID}),
        }).then(function(r) {
          if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
          return r.json();
        }).then(function(d) {
          if (!d || !d.session_id) throw new Error('no session id');
          // Tap the event stream so synthesis progress shows in
          // the panel (status / cmd / output / activity). Close
          // the modal so the user can watch it run; reload the
          // draft when 'done' arrives.
          m.close();
          ctx.subscribe('api/chat/v2/events?id=' + encodeURIComponent(d.session_id));
        }).catch(function(err) {
          genBtn.disabled = false; genBtn.textContent = 'Generate Draft';
          alert('Synthesis failed: ' + (err && err.message || err));
        });
      }

      function renderSupplements(list) {
        supplementsBox.innerHTML = '';
        if (!list || !list.length) {
          supplementsBox.appendChild(el('div', {class: 'ui-pl-empty'},
            ['No supplements attached.']));
          return;
        }
        list.forEach(function(s) {
          var head = el('div', {class: 'ui-servitor-supp-hdr'});
          head.appendChild(el('span', {class: 'ui-servitor-supp-name',
            text: s.name || ''}));
          if (s.processing) {
            head.appendChild(el('span', {class: 'ui-servitor-supp-status'},
              ['Analyzing…']));
          } else if (s.content) {
            var kb = Math.max(1, Math.round((s.content || '').length / 1024));
            head.appendChild(el('span', {class: 'ui-servitor-supp-status'},
              [kb + ' KB']));
          }
          var delBtn = el('button', {class: 'ui-row-btn danger',
            onclick: function() {
              if (!confirm('Remove "' + s.name + '" from the workspace?')) return;
              fetch('api/workspace/supplement/delete', {
                method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({workspace_id: wsID, supplement_id: s.id}),
              }).then(function() { loadWorkspace(); });
            }}, ['Remove']);
          head.appendChild(delBtn);

          var promptArea = el('textarea', {class: 'ui-form-input', rows: 2,
            placeholder: 'How/when should the LLM reference this document?'});
          promptArea.value = s.sub_prompt || '';
          var saveSubBtn = el('button', {class: 'ui-row-btn',
            onclick: function() {
              saveSubBtn.disabled = true; saveSubBtn.textContent = 'Saving…';
              fetch('api/workspace/supplement/prompt', {
                method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({
                  workspace_id: wsID, supplement_id: s.id,
                  sub_prompt: promptArea.value.trim(),
                }),
              }).then(function(r) {
                saveSubBtn.disabled = false;
                saveSubBtn.textContent = r.ok ? 'Saved' : 'Failed';
                setTimeout(function(){ saveSubBtn.textContent = 'Save Instruction'; }, 1500);
              });
            }}, ['Save Instruction']);

          var row = el('div', {class: 'ui-servitor-supp-row'},
            [head, promptArea, saveSubBtn]);
          supplementsBox.appendChild(row);
        });
        // If any supplement is still processing, poll until done.
        if (list.some(function(s){ return s.processing; })) {
          setTimeout(loadWorkspace, 5000);
        }
      }

      function attachSupplement() {
        if (!fileIn.files || !fileIn.files.length) {
          alert('Pick a file first.');
          return;
        }
        attachBtn.disabled = true; attachBtn.textContent = 'Attaching…';
        var fd = new FormData();
        fd.append('workspace_id', wsID);
        fd.append('sub_prompt', promptIn.value.trim());
        fd.append('file', fileIn.files[0]);
        fetch('api/workspace/supplement/add', {method: 'POST', body: fd})
          .then(function(r) {
            if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
            return r.json();
          })
          .then(function() {
            fileIn.value = ''; promptIn.value = '';
            attachBtn.disabled = false; attachBtn.textContent = 'Attach Document';
            loadWorkspace();
          })
          .catch(function(err) {
            attachBtn.disabled = false; attachBtn.textContent = 'Attach Document';
            alert('Attach failed: ' + (err && err.message || err));
          });
      }
    });

    window.uiRegisterClientAction('servitor_clear', function(ctx) {
      // Mirrors legacy's clearChat + clearActivity. Server-side
      // history (saved workspace entries) is untouched — this just
      // wipes the on-screen panes.
      ctx.clearConvo();
      ctx.clearActivity();
    });

    window.uiRegisterClientAction('servitor_clear_memory', function(ctx) {
      var aid = getApplianceID();
      if (!aid) { alert('Pick an appliance first'); return; }
      var msg = 'Clear all memory for this appliance?\n\n' +
        'This removes:\n' +
        '• System profile (map data)\n' +
        '• Knowledge docs\n' +
        '• Stored facts\n' +
        '• Notes and techniques\n\n' +
        'The appliance connection settings are kept. Run "Map System" to rebuild.\n\n' +
        'This cannot be undone.';
      if (!confirm(msg)) return;
      fetch('api/memory/clear', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({appliance_id: aid}),
      }).then(function(r) {
        if (r.ok) {
          ctx.clearConvo();
          ctx.clearActivity();
          // Reload so the Facts badge re-counts to zero.
          setTimeout(function(){ refreshFactsBadge(); }, 100);
        } else {
          alert('Failed to clear memory');
        }
      }).catch(function(err) {
        alert('Failed: ' + (err && err.message || err));
      });
    });

    // Map App — enumerate the subcommands/flags of a single
    // command on an SSH appliance. Modal collects the command
    // name, POSTs to /api/mapapp, then taps the same event stream
    // the regular Map System uses.
    window.uiRegisterClientAction('servitor_run_mapapp', function(ctx) {
      var aid = getApplianceID();
      if (!aid) { alert('Pick an appliance first'); return; }
      var cmdIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'e.g. kubectl, redis-cli, systemctl',
        style: 'width: 100%'});
      setTimeout(function(){ cmdIn.focus(); }, 50);
      var hint = el('div', {class: 'ui-form-hint'},
        ['The agent will run --help on this command, walk its subcommand tree, and produce a structured reference document.']);
      var body = el('div', {class: 'ui-pl-modal-body'},
        [el('label', {}, ['Command']), cmdIn, hint]);
      var m = openModal('Map App', body);
      var run = function() {
        var cmd = cmdIn.value.trim();
        if (!cmd) { alert('Command is required.'); return; }
        runBtn.disabled = true;
        fetch('api/mapapp', {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({appliance_id: aid, command: cmd}),
        }).then(function(r) {
          if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
          return r.json();
        }).then(function(d) {
          m.close();
          if (d && d.session_id) {
            ctx.subscribe('api/chat/v2/events?id=' + encodeURIComponent(d.session_id));
          }
        }).catch(function(err) {
          runBtn.disabled = false;
          alert('Map App failed: ' + (err && err.message || err));
        });
      };
      cmdIn.addEventListener('keydown', function(ev) {
        if (ev.key === 'Enter') { ev.preventDefault(); run(); }
      });
      var runBtn = el('button', {class: 'ui-row-btn primary', onclick: run}, ['Run']);
      var cancelBtn = el('button', {class: 'ui-row-btn', onclick: m.close}, ['Cancel']);
      m.footer(cancelBtn, runBtn);
    });

    window.uiRegisterClientAction('servitor_run_map', function(ctx) {
      var aid = getApplianceID();
      if (!aid) { alert('Pick an appliance first'); return; }
      if (!confirm('Run a full system map on this appliance? This may take a few minutes.')) return;
      fetch('api/map', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({appliance_id: aid}),
      }).then(function(r) {
        if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
        return r.json();
      }).then(function(d) {
        if (!d || !d.session_id) {
          alert('Map did not return a session id');
          return;
        }
        // Tap the same event queue the chat uses — status / cmd /
        // output / plan / intent / etc. flow into the activity +
        // conversation panes as the map progresses. The bridge
        // translator (chat_bridge.go) takes care of mapping the
        // legacy event kinds to AgentLoopPanel events.
        ctx.subscribe('api/chat/v2/events?id=' + encodeURIComponent(d.session_id));
      }).catch(function(err) {
        alert('Map failed to start: ' + (err && err.message || err));
      });
    });

    // --- xterm.js terminal wiring -------------------------------------
    // The framework's AgentLoopPanel reserves the bottom-right pane
    // when Terminal is configured (see chat_page.go). xterm itself
    // lives here because it's servitor-specific. Wire-up:
    //   1. Wait for the .ui-agent-terminal-body slot to land
    //   2. Read the appliance id from the picker
    //   3. Open WebSocket to /api/terminal?id=<aid>
    //   4. Pipe both directions through xterm
    //   5. Reconnect on disconnect with exponential backoff
    //   6. Watch the picker for changes and re-open on appliance swap
    var termInstance = null, termFit = null, termWs = null;
    var termAutoReconnect = false;
    var termReconnectDelay = 2000;
    var termReconnectTimer = null;
    var termLastID = '';

    function makeWsUrl(path) {
      var proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      var a = document.createElement('a');
      a.href = path;
      return proto + '//' + a.host + a.pathname + (a.search || '');
    }

    function openTerminal() {
      if (typeof Terminal === 'undefined' || typeof FitAddon === 'undefined') return;
      var cont = document.querySelector('.ui-agent-terminal-body');
      if (!cont) return;
      var aid = getApplianceID();
      if (!aid) {
        closeTerminalSocket();
        cont.innerHTML = '';
        var ph = document.createElement('div');
        ph.className = 'ui-agent-terminal-placeholder';
        ph.textContent = '(pick an appliance to connect)';
        cont.appendChild(ph);
        termLastID = '';
        return;
      }
      if (aid === termLastID && termWs && termWs.readyState === WebSocket.OPEN) {
        return; // already connected to this appliance
      }
      termLastID = aid;
      closeTerminalSocket();
      termAutoReconnect = true;
      cont.innerHTML = '';

      if (!termInstance) {
        termInstance = new Terminal({
          theme: { background: '#0d1117', foreground: '#e6edf3',
            cursor: '#58a6ff', selectionBackground: '#264f78' },
          fontSize: 13,
          fontFamily: 'ui-monospace, Menlo, Consolas, "Courier New", monospace',
          cursorBlink: true,
          scrollback: 3000,
        });
        termFit = new FitAddon.FitAddon();
        termInstance.loadAddon(termFit);
        termInstance.open(cont);
        try { termFit.fit(); } catch (_) {}
        var ro = new ResizeObserver(function() { try { termFit.fit(); } catch (_) {} });
        ro.observe(cont);
        termInstance.onResize(function(sz) {
          if (termWs && termWs.readyState === WebSocket.OPEN) {
            termWs.send(JSON.stringify({type:'resize', cols:sz.cols, rows:sz.rows}));
          }
        });
        termInstance.onData(function(data) {
          if (termWs && termWs.readyState === WebSocket.OPEN) {
            termWs.send(data);
          }
        });
      } else {
        termInstance.open(cont);
        try { termFit.fit(); } catch (_) {}
      }

      termWs = new WebSocket(makeWsUrl('api/terminal?id=' + encodeURIComponent(aid)));
      termWs.binaryType = 'arraybuffer';
      termWs.onopen = function() {
        termReconnectDelay = 2000;
        clearTimeout(termReconnectTimer);
        termWs.send(JSON.stringify({type:'resize',
          cols:termInstance.cols, rows:termInstance.rows}));
      };
      termWs.onmessage = function(e) {
        if (e.data instanceof ArrayBuffer) {
          termInstance.write(new Uint8Array(e.data));
        } else {
          termInstance.write(e.data);
        }
      };
      termWs.onclose = function() {
        if (termInstance) termInstance.write('\r\n\x1b[2m[session closed]\x1b[0m\r\n');
        scheduleTerminalReconnect();
      };
      termWs.onerror = function() {
        if (termInstance) termInstance.write('\r\n\x1b[31m[connection error]\x1b[0m\r\n');
      };
    }

    function scheduleTerminalReconnect() {
      if (!termAutoReconnect) return;
      clearTimeout(termReconnectTimer);
      var delay = termReconnectDelay;
      termReconnectDelay = Math.min(termReconnectDelay * 2, 16000);
      termReconnectTimer = setTimeout(function() {
        if (!termAutoReconnect) return;
        if (termInstance) termInstance.write('\x1b[2m[reconnecting…]\x1b[0m\r\n');
        openTerminal();
      }, delay);
    }

    function closeTerminalSocket() {
      termAutoReconnect = false;
      clearTimeout(termReconnectTimer);
      if (termWs) {
        try { termWs.onclose = null; termWs.onerror = null; termWs.close(); } catch (_) {}
        termWs = null;
      }
    }

    // Wait for the framework to mount AgentLoopPanel (the slot
    // appears once mount completes). Poll briefly then attempt
    // open. Subsequent appliance picker changes re-trigger.
    function bootTerminal(tries) {
      var cont = document.querySelector('.ui-agent-terminal-body');
      if (!cont) {
        if (tries < 30) setTimeout(function(){ bootTerminal(tries + 1); }, 100);
        return;
      }
      openTerminal();
    }
    bootTerminal(0);

    // On reconnect, the bridge enriches the session event with the
    // session's appliance_id. Set the picker so the terminal + the
    // workspace list scope to the right context automatically.
    window.addEventListener('ui-agent-session', function(ev) {
      var aid = ev.detail && ev.detail.appliance_id;
      if (!aid) return;
      var sel = document.querySelector('.ui-agent-extras select');
      if (!sel || sel.value === aid) return;
      sel.value = aid;
      // Fire change so existing listeners (terminal + workspace
      // list refresh + facts badge) re-run for the new appliance.
      sel.dispatchEvent(new Event('change'));
    });

    // Hook the appliance picker so swaps re-open the terminal.
    document.addEventListener('change', function(ev) {
      if (ev.target && ev.target.matches &&
          ev.target.matches('.ui-agent-extras select')) {
        openTerminal();
      }
    });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', register);
  } else { register(); }
})();
</script>`
