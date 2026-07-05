// App-specific UI bits loaded into the servitor chat page's <head>.
// Two responsibilities:
//
//  1. CSS for servitor-specific block shapes (intent narration, plan
//     checklist, draft preview, etc.) so the shared core/ui CSS stays
//     domain-agnostic.
//  2. JS registering block renderers for the four servitor event
//     kinds that the chat_bridge.go translator routes through
//     `kind: "block"`: servitor_intent, servitor_plan,
//     servitor_notes_consumed. The framework calls
//     window.UIBlockRenderers[<type>] with the event data and
//     expects a {wrap, body, onDone?} object back.
//  3. JS registering client actions for the chat toolbar
//     (servitor_open_rules, servitor_run_map).
//     Each opens a modal that hits the existing legacy endpoints.

package servitor

const servitorWebAssets = `<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css">
<script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
<script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
<style>
/* Mobile: hide the activity + terminal (xterm) panes so the
   conversation fills the screen. On a phone there's no room for the
   3-pane investigator layout, and the activity/terminal are
   observability surfaces the user rarely drives from a phone.
   Approval prompts render in the conversation pane (not the activity
   pane), so nothing the user must act on is hidden by this. The
   convo's flex-grow makes it claim the freed width. */
@media (max-width: 900px) {
  .ui-agent-right { display: none !important; }
  .ui-agent-divider { display: none !important; }
  .ui-agent-activity-expand { display: none !important; }
  .ui-agent-convo { flex: 1 1 100% !important; }
}
/* Repo appliances have no terminal, so the right column is activity-only and
   code Q&A doesn't need the default 40%. Halve it to 20% and give the freed
   width to the conversation. Both panes keep growing (basis sums to 100%, same
   math as the default 60/40), so there's no dead gap. servitor-repo-mode is
   toggled in JS from the appliance picker's type. */
.ui-agent.servitor-repo-mode .ui-agent-convo { flex: 1 1 74%; }
.ui-agent.servitor-repo-mode .ui-agent-right { flex: 1 1 26%; }
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
        window.uiAlert('Note has no id yet — try again in a moment.');
        return;
      }
      // Lock first so the orchestrator doesn't drain it mid-edit.
      fetch(url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: sid, note_id: noteID, action: 'lock'}),
      }).then(function(r) {
        if (r.status === 410) {
          window.uiAlert('Note has already been picked up by the agent.');
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
            if (r.status === 410) { window.uiAlert('Note already picked up.'); }
            else if (!r.ok) { throw new Error('save failed'); }
            body.textContent = newText;
            cancel();
          }).catch(function(err) {
            saveBtn.disabled = false;
            window.uiAlert('Save failed: ' + (err && err.message || err));
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
        window.uiAlert('Lock failed: ' + (err && err.message || err));
      });
    }

    async function deleteInterjection(bubble) {
      var noteID = bubble.dataset.noteId;
      var sid    = bubble.dataset.sessionId;
      var url    = bubble.dataset.injectUrl || 'api/inject';
      if (!noteID || !sid) return;
      if (!(await window.uiConfirm('Delete this queued note?'))) return;
      // Server's handleInject reads from JSON body regardless of
      // method, so DELETE carries the same {id, note_id} shape as
      // the other ops.
      fetch(url, {
        method: 'DELETE',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: sid, note_id: noteID}),
      }).then(function(r) {
        if (r.status === 410) { window.uiAlert('Note already picked up by the agent.'); return; }
        if (!r.ok) { throw new Error('delete failed'); }
        bubble.remove();
      }).catch(function(err) {
        window.uiAlert('Delete failed: ' + (err && err.message || err));
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

    // --- Toolbar client actions -------------------------------------
    // Each action opens a simple modal that hits the existing
    // legacy endpoint. v1 of the port keeps the modal HTML inline;
    // a later pass can promote these to dedicated routes.

    function getApplianceID() {
      var sel = document.querySelector('.ui-agent-extras select');
      return sel ? sel.value : '';
    }

    // servitorMemBase resolves the endpoint prefix for the shared agent-memory
    // modal (orchestrate.AgentMemoryModalScript) at open time. Returns a
    // RELATIVE base (matching every other servitor fetch) for the selected
    // appliance, or null when nothing is picked so the modal aborts opening.
    // Exposed on window: the shared modal script is a SEPARATE <script>, so it
    // can't see functions local to this IIFE — it calls servitorMemBase() at
    // global scope. (Defined here so it still closes over getApplianceID.)
    window.servitorMemBase = function() {
      var aid = getApplianceID();
      if (!aid) { (window.uiAlert || window.alert)('Pick an appliance first'); return null; }
      return 'api/appliances/' + encodeURIComponent(aid) + '/';
    };

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

    window.uiRegisterClientAction('servitor_open_rules', function() {
      var aid = getApplianceID();
      if (!aid) { window.uiAlert('Pick an appliance first'); return; }
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
            if (!r.ok) { window.uiAlert('Failed to add rule'); return; }
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
                onclick: async function() {
                  if (!(await window.uiConfirm('Delete this rule?'))) return;
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
      if (!aid) { window.uiAlert('Pick an appliance first'); return; }
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

      // Type tabs — SSH vs Local Command vs Git Repository.
      var typeSsh = el('button', {class: 'ui-row-btn',
        onclick: function(){ setType('ssh'); }}, ['SSH']);
      var typeCmd = el('button', {class: 'ui-row-btn',
        onclick: function(){ setType('command'); }}, ['Local Command']);
      var typeRepo = el('button', {class: 'ui-row-btn',
        onclick: function(){ setType('repo'); }}, ['Git Repository']);
      var typeWorkspace = el('button', {class: 'ui-row-btn',
        onclick: function(){ setType('workspace'); }}, ['Workspace']);
      var typeRow = el('div', {class: 'ui-servitor-app-tabs'}, [typeSsh, typeCmd, typeRepo, typeWorkspace]);

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

      // Repository section — cloned + encrypted at rest; ask it questions
      // like any other appliance.
      var repoUrlIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'https://github.com/owner/repo', value: rec.repo_url || ''});
      var repoBranchIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'default branch if blank', value: rec.repo_branch || ''});
      var repoTokenIn = el('input', {type: 'password', class: 'ui-form-input',
        placeholder: isEdit ? '(unchanged — type to replace)' : 'for private repos (optional)',
        value: ''});
      var repoSkipIn = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'e.g. testdata, generated, docs/build',
        value: (rec.repo_skip_dirs || []).join(', ')});
      var repoSection = el('div', {class: 'ui-servitor-app-section'}, [
        el('label', {}, ['Git URL *']), repoUrlIn,
        el('label', {}, ['Branch']), repoBranchIn,
        el('label', {}, ['Access Token']), repoTokenIn,
        el('label', {}, ['Skip Directories']), repoSkipIn,
        el('div', {class: 'ui-form-hint'},
          ['The repository is cloned, its text files ingested into an encrypted store, and the plaintext clone discarded. Token is stored encrypted, used only for private clones. Skip Directories (comma-separated) are excluded from ingest on top of the built-in defaults (.git, node_modules, dist, build, target, vendor, …).']),
      ]);

      // Workspace section — a master appliance that investigates several existing
      // appliances (repos and/or SSH boxes) together. The member list is fetched
      // lazily the first time the Workspace tab is opened.
      var wsSelected = (rec.members || []).slice();
      var wsMembersBox = el('div', {}, ['Loading appliances…']);
      var wsLoaded = false;
      function loadWorkspaceMembers() {
        if (wsLoaded) return;
        wsLoaded = true;
        fetch('api/appliances')
          .then(function(r){ return r.ok ? r.json() : []; })
          .then(function(list) {
            list = (list || []).filter(function(a) {
              // Members are concrete appliances; a workspace can't nest a
              // workspace, and can't include itself.
              return a.type !== 'workspace' && a.id !== (rec.id || '');
            });
            wsMembersBox.innerHTML = '';
            if (!list.length) {
              wsMembersBox.appendChild(el('div', {class: 'ui-form-hint'},
                ['No repos or SSH appliances yet — create some first, then add them here.']));
              return;
            }
            list.forEach(function(a) {
              var cb = el('input', {type: 'checkbox'});
              cb.checked = wsSelected.indexOf(a.id) >= 0;
              cb.onchange = function() {
                var i = wsSelected.indexOf(a.id);
                if (cb.checked && i < 0) wsSelected.push(a.id);
                else if (!cb.checked && i >= 0) wsSelected.splice(i, 1);
              };
              var typeTag = a.type === 'repo' ? 'repo' : (a.type === 'command' ? 'cmd' : 'ssh');
              wsMembersBox.appendChild(el('label', {
                style: 'display:flex; align-items:center; gap:0.5rem; padding:0.2rem 0;'},
                [cb, el('span', {style:'opacity:0.6; font-size:0.85em; min-width:2.6em;'}, [typeTag]), (a.name || a.id)]));
            });
          })
          .catch(function() {
            wsMembersBox.innerHTML = '';
            wsMembersBox.appendChild(el('div', {class: 'ui-form-hint'}, ['Failed to load appliances.']));
          });
      }
      var workspaceSection = el('div', {class: 'ui-servitor-app-section'}, [
        el('label', {}, ['Member Appliances *']), wsMembersBox,
        el('div', {class: 'ui-form-hint'},
          ['Pick the repos and SSH boxes this workspace investigates together. A question fans out to the relevant members and the answers are combined — e.g. trace a log line seen on a box back to the repo that emits it.']),
      ]);

      function setType(t) {
        rec.type = t;
        typeSsh.classList.toggle('primary', t === 'ssh');
        typeCmd.classList.toggle('primary', t === 'command');
        typeRepo.classList.toggle('primary', t === 'repo');
        typeWorkspace.classList.toggle('primary', t === 'workspace');
        sshSection.style.display = t === 'ssh' ? '' : 'none';
        cmdSection.style.display = t === 'command' ? '' : 'none';
        repoSection.style.display = t === 'repo' ? '' : 'none';
        workspaceSection.style.display = t === 'workspace' ? '' : 'none';
        if (t === 'workspace') loadWorkspaceMembers();
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

      // Linked Knowledge — collections the investigator can search (via
      // search_knowledge) to ground answers in curated reference material.
      // Applies to every appliance type, so it loads as soon as the form opens.
      var kbSelected = (rec.collections || []).slice();
      var kbBox = el('div', {}, ['Loading collections…']);
      fetch('api/collections')
        .then(function(r){ return r.ok ? r.json() : []; })
        .then(function(list) {
          kbBox.innerHTML = '';
          list = list || [];
          if (!list.length) {
            kbBox.appendChild(el('div', {class: 'ui-form-hint'},
              ['No knowledge collections yet — create some in the Knowledge app, then link them here.']));
            return;
          }
          list.forEach(function(c) {
            var cb = el('input', {type: 'checkbox'});
            cb.checked = kbSelected.indexOf(c.id) >= 0;
            cb.onchange = function() {
              var i = kbSelected.indexOf(c.id);
              if (cb.checked && i < 0) kbSelected.push(c.id);
              else if (!cb.checked && i >= 0) kbSelected.splice(i, 1);
            };
            kbBox.appendChild(el('label', {style: 'display:flex; align-items:center; gap:0.5rem; padding:0.2rem 0;'},
              [cb, (c.name || c.id)]));
          });
        })
        .catch(function(){ kbBox.innerHTML = ''; kbBox.appendChild(el('div', {class: 'ui-form-hint'}, ['Failed to load collections.'])); });
      var knowledgeSection = el('div', {class: 'ui-servitor-app-section'}, [
        el('label', {}, ['Linked Knowledge']), kbBox,
        el('div', {class: 'ui-form-hint'},
          ['Curated collections the investigator can search (via search_knowledge) to ground answers in reference material — runbooks, vendor docs, a guide — alongside what it finds on the system itself.']),
      ]);

      // Shared toggle — when on, every user can discover and use this
      // appliance/repo (owner's creds, one repo clone, shared accumulated
      // knowledge) while keeping their own chat sessions.
      var sharedIn = el('input', {type: 'checkbox'});
      sharedIn.checked = !!rec.shared;
      var sharedRow = el('label', {class: 'ui-servitor-shared-row',
        style: 'display:flex; align-items:center; gap:0.5rem; margin-top:0.4rem; font-weight:600;'},
        [sharedIn, 'Shared with all users']);
      var sharedHint = el('div', {class: 'ui-form-hint'},
        ['Everyone can open and use it (with the stored credentials); chat sessions stay per-user. Only you or an admin can change or delete it.']);

      var body = el('div', {class: 'ui-pl-modal-body'}, [
        el('label', {}, ['Name *']), nameIn,
        typeRow, sshSection, cmdSection, repoSection, workspaceSection,
        el('label', {}, ['Custom Instructions']), instrIn,
        el('label', {}, ['Persona']),
        personaNameIn, personaPromptIn,
        knowledgeSection,
        sharedRow, sharedHint,
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
            repo_url:       repoUrlIn.value.trim(),
            repo_branch:    repoBranchIn.value.trim(),
            repo_token:     repoTokenIn.value,
            repo_skip_dirs: repoSkipIn.value.split(',').map(function(s){return s.trim();}).filter(Boolean),
            instructions:   instrIn.value,
            persona_name:   personaNameIn.value.trim(),
            persona_prompt: personaPromptIn.value,
            shared:         sharedIn.checked,
            members:        wsSelected.slice(),
            collections:    kbSelected.slice(),
          };
          // Name is optional for repos — the server derives owner/repo from the URL.
          if (!payload.name && payload.type !== 'repo') { window.uiAlert('Name is required'); return; }
          if (payload.type === 'ssh' && !payload.host) { window.uiAlert('Host is required'); return; }
          if (payload.type === 'command' && !payload.command) { window.uiAlert('Command is required'); return; }
          if (payload.type === 'repo' && !payload.repo_url) { window.uiAlert('Git URL is required'); return; }
          if (payload.type === 'workspace' && !payload.members.length) { window.uiAlert('Select at least one member appliance'); return; }
          saveBtn.disabled = true;
          fetch('api/appliances', {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(payload),
          }).then(function(r) {
            saveBtn.disabled = false;
            if (!r.ok) { return r.text().then(function(t){ window.uiAlert('Save failed: ' + t); }); }
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
          onclick: async function() {
            if (!(await window.uiConfirm('Delete this appliance? All saved facts, rules, and sessions will remain but become orphaned.'))) return;
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
      if (!aid) { window.uiAlert('Pick an appliance first'); return; }
      fetch('api/appliance/' + encodeURIComponent(aid))
        .then(function(r) {
          if (!r.ok) throw new Error('HTTP ' + r.status);
          return r.json();
        })
        .then(function(rec) { openApplianceModal(rec); })
        .catch(function(err) {
          window.uiAlert('Failed to load appliance: ' + (err && err.message || err));
        });
    });

    // Per-reply save buttons: assistant replies get TechWriter /
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
        // Push this reply into a Guide — but only guides that already list THIS
        // appliance/repo as a source (fetched on click). One match pushes
        // directly; several show a picker.
        if (saveDestinations.guide) {
          var gBtn = makeBtn('↗ Guide', function() {
            var aid = getApplianceID();
            if (!aid) { window.uiAlert('Pick an appliance first'); return; }
            function pushTo(gid) {
              gBtn.disabled = true; gBtn.textContent = 'Incorporating…';
              fetch('api/push-to-guide?appliance_id=' + encodeURIComponent(aid), {
                method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({appliance_id: aid, guide_id: gid, text: m.rawText}),
              }).then(function(r) {
                gBtn.disabled = false;
                gBtn.textContent = r.ok ? 'Saved ✓' : 'Failed';
                if (r.ok) gBtn.classList.add('saved');
              });
            }
            gBtn.disabled = true; gBtn.textContent = 'Loading…';
            fetch('api/push-to-guide?appliance_id=' + encodeURIComponent(aid))
              .then(function(r) { return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); }); })
              .then(function(d) {
                gBtn.disabled = false; gBtn.textContent = '↗ Guide';
                var guides = (d && d.guides) || [];
                if (!guides.length) {
                  window.uiAlert("No guide lists this system as a source yet. Add it via a guide's Sources picker, then push here.");
                  return;
                }
                if (guides.length === 1 || !window.uiOpenSimpleModal) { pushTo(guides[0].id); return; }
                window.uiOpenSimpleModal({title: 'Add to which guide?', width: '420px', mount: function(body, dlg) {
                  guides.forEach(function(g) {
                    var b = document.createElement('button');
                    b.className = 'ui-row-btn'; b.textContent = g.title;
                    b.style.display = 'block'; b.style.width = '100%'; b.style.margin = '0.25rem 0'; b.style.textAlign = 'left';
                    b.onclick = function() { try { dlg.close(); dlg.remove(); } catch(e){} pushTo(g.id); };
                    body.appendChild(b);
                  });
                }});
              })
              .catch(function(err) { gBtn.disabled = false; gBtn.textContent = '↗ Guide'; window.uiAlert('Failed: ' + (err && err.message || err)); });
          });
          row.appendChild(gBtn);
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

    window.uiRegisterClientAction('servitor_refresh_repo', async function(ctx) {
      var aid = getApplianceID();
      if (!aid) { window.uiAlert('Pick an appliance first'); return; }
      if (!isRepoAppliance(aid)) { window.uiAlert('Refresh re-clones a repository — it only applies to Git repository appliances.'); return; }
      if (!(await window.uiConfirm('Re-clone this repository to pick up new code? The current ingested copy is replaced.'))) return;
      fetch('api/repo/refresh', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({appliance_id: aid}),
      }).then(function(r) {
        if (!r.ok) {
          return r.text().then(function(t){ window.uiAlert('Refresh failed: ' + t); });
        }
        return r.json().then(function(d) {
          if (d && d.session_id) {
            // Subscribe to the same event stream Map uses: the AgentLoopPanel
            // renders the spinner, live status lines, and a Cancel button.
            ctx.subscribe('api/chat/v2/events?id=' + encodeURIComponent(d.session_id));
          }
        });
      }).catch(function(err) {
        window.uiAlert('Refresh failed: ' + (err && err.message || err));
      });
    });

    window.uiRegisterClientAction('servitor_clear_memory', async function(ctx) {
      var aid = getApplianceID();
      if (!aid) { window.uiAlert('Pick an appliance first'); return; }
      var isRepo = isRepoAppliance(aid);
      var msg = 'Clear all memory for this appliance?\n\n' +
        'This removes:\n' +
        '• System profile (map data)\n' +
        '• Knowledge docs\n' +
        '• Stored facts\n' +
        '• Notes and techniques\n' +
        (isRepo ? '• Ingested code files (the repository will need re-cloning)\n' : '') +
        '\n' +
        'The appliance connection settings are kept. Run "' +
        (isRepo ? 'Refresh" to re-clone, then "Map System' : 'Map System') +
        '" to rebuild.\n\n' +
        'This cannot be undone.';
      if (!(await window.uiConfirm(msg))) return;
      fetch('api/memory/clear', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({appliance_id: aid}),
      }).then(function(r) {
        if (r.ok) {
          ctx.clearConvo();
          ctx.clearActivity();
        } else {
          window.uiAlert('Failed to clear memory');
        }
      }).catch(function(err) {
        window.uiAlert('Failed: ' + (err && err.message || err));
      });
    });

    // Map App — enumerate the subcommands/flags of a single
    // command on an SSH appliance. Modal collects the command
    // name, POSTs to /api/mapapp, then taps the same event stream
    // the regular Map System uses.
    window.uiRegisterClientAction('servitor_run_mapapp', function(ctx) {
      var aid = getApplianceID();
      if (!aid) { window.uiAlert('Pick an appliance first'); return; }
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
        if (!cmd) { window.uiAlert('Command is required.'); return; }
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
            // Map App runs in its OWN session (the server minted a fresh
            // session_id) — clear the panes so it doesn't append to whatever
            // conversation is currently open.
            if (ctx.clearConvo) ctx.clearConvo();
            if (ctx.clearActivity) ctx.clearActivity();
            ctx.subscribe('api/chat/v2/events?id=' + encodeURIComponent(d.session_id));
          }
        }).catch(function(err) {
          runBtn.disabled = false;
          window.uiAlert('Map App failed: ' + (err && err.message || err));
        });
      };
      cmdIn.addEventListener('keydown', function(ev) {
        if (ev.key === 'Enter') { ev.preventDefault(); run(); }
      });
      var runBtn = el('button', {class: 'ui-row-btn primary', onclick: run}, ['Run']);
      var cancelBtn = el('button', {class: 'ui-row-btn', onclick: m.close}, ['Cancel']);
      m.footer(cancelBtn, runBtn);
    });

    window.uiRegisterClientAction('servitor_run_map', async function(ctx) {
      var aid = getApplianceID();
      if (!aid) { window.uiAlert('Pick an appliance first'); return; }
      if (!(await window.uiConfirm('Run a full system map on this appliance? This may take a few minutes.'))) return;
      fetch('api/map', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({appliance_id: aid}),
      }).then(function(r) {
        if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
        return r.json();
      }).then(function(d) {
        if (!d || !d.session_id) {
          window.uiAlert('Map did not return a session id');
          return;
        }
        // Tap the same event queue the chat uses — status / cmd /
        // output / plan / intent / etc. flow into the activity +
        // conversation panes as the map progresses. The bridge
        // translator (chat_bridge.go) takes care of mapping the
        // legacy event kinds to AgentLoopPanel events.
        ctx.subscribe('api/chat/v2/events?id=' + encodeURIComponent(d.session_id));
      }).catch(function(err) {
        window.uiAlert('Map failed to start: ' + (err && err.message || err));
      });
    });

    // Export the appliance's accumulated knowledge as a downloadable .md
    // (credentials/secrets excluded server-side) for handing to Claude or
    // another LLM to help build/improve a support tool for this system.
    window.uiRegisterClientAction('servitor_export_knowledge', function() {
      var aid = getApplianceID();
      if (!aid) { window.uiAlert('Pick an appliance first'); return; }
      fetch('api/knowledge/export?appliance_id=' + encodeURIComponent(aid))
        .then(function(r) {
          if (!r.ok) { return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); }); }
          return r.text();
        })
        .then(function(text) {
          if (!text) { window.uiAlert('Nothing to export yet — map the system first.'); return; }
          var blob = new Blob([text], {type: 'text/markdown'});
          var url = URL.createObjectURL(blob);
          var a = document.createElement('a');
          a.href = url;
          a.download = aid + '-knowledge.md';
          document.body.appendChild(a);
          a.click();
          setTimeout(function() { URL.revokeObjectURL(url); a.remove(); }, 0);
        })
        .catch(function(err) { window.uiAlert('Export failed: ' + (err && err.message || err)); });
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

    // Repo appliances have nothing to attach a shell to — hide the whole
    // terminal pane (and its resize divider) so the activity column fills
    // the space. show=false collapses it; show=true restores it.
    function setTerminalPaneVisible(show) {
      var pane = document.querySelector('.ui-agent-terminal');
      if (pane) pane.style.display = show ? '' : 'none';
      var hdiv = document.querySelector('.ui-agent-hdivider');
      if (hdiv) hdiv.style.display = show ? '' : 'none';
    }
    function isRepoAppliance(aid) {
      var types = window.servitorApplianceTypes || {};
      return !!aid && types[aid] === 'repo';
    }
    // Repos and workspaces have nothing to attach a shell to.
    function isShelllessAppliance(aid) {
      var t = (window.servitorApplianceTypes || {})[aid] || 'ssh';
      return t === 'repo' || t === 'workspace';
    }
    // Current appliance type ('' when nothing is selected).
    function currentApplianceType() {
      var aid = getApplianceID();
      if (!aid) return '';
      var types = window.servitorApplianceTypes || {};
      return types[aid] || 'ssh';
    }
    // Show/hide a toolbar action (flat button OR overflow-menu item) by label.
    function setActionVisible(label, show) {
      var nodes = document.querySelectorAll('[data-action-label="' + label + '"]');
      for (var i = 0; i < nodes.length; i++) {
        nodes[i].style.display = show ? '' : 'none';
      }
    }
    // Gate type-specific toolbar actions to the appliance types they apply to:
    // Map App lets you pick WHICH CLI command to enumerate, so it only makes sense
    // on SSH hosts (many commands). A command-type appliance IS a single command —
    // the only mappable thing — so it doesn't need it; repos have no commands.
    // Refresh Repo re-clones (repo-type only). Everything else stays visible.
    function applyToolbarForType() {
      var t = currentApplianceType();
      setActionVisible('Map App', t === 'ssh');
      setActionVisible('Refresh Repo', t === 'repo');
    }
    // Repo appliances have no terminal, so halve the activity column's default
    // width (via the servitor-repo-mode CSS class) and give the conversation the
    // freed space. Both panes keep growing, so no dead gap.
    function applyRepoLayout() {
      var wrap = document.querySelector('.ui-agent');
      if (wrap) wrap.classList.toggle('servitor-repo-mode', isShelllessAppliance(getApplianceID()));
    }

    function openTerminal() {
      if (typeof Terminal === 'undefined' || typeof FitAddon === 'undefined') return;
      var cont = document.querySelector('.ui-agent-terminal-body');
      if (!cont) return;
      var aid = getApplianceID();
      if (isShelllessAppliance(aid)) {
        // No shell for a repository or workspace — tear down any open socket and hide the pane.
        closeTerminalSocket();
        termLastID = '';
        setTerminalPaneVisible(false);
        return;
      }
      setTerminalPaneVisible(true);
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

    // Gate type-specific toolbar actions once the actions bar has rendered.
    function bootToolbarGate(tries) {
      if (!document.querySelector('[data-action-label]')) {
        if (tries < 30) setTimeout(function(){ bootToolbarGate(tries + 1); }, 100);
        return;
      }
      applyToolbarForType();
      applyRepoLayout();
    }
    bootToolbarGate(0);

    // On reconnect, the bridge enriches the session event with the
    // session's appliance_id. Set the picker so the terminal + the
    // session list scope to the right context automatically.
    window.addEventListener('ui-agent-session', function(ev) {
      var aid = ev.detail && ev.detail.appliance_id;
      if (!aid) return;
      var sel = document.querySelector('.ui-agent-extras select');
      if (!sel || sel.value === aid) return;
      sel.value = aid;
      // Fire change so existing listeners (terminal + session
      // list refresh + facts badge) re-run for the new appliance.
      sel.dispatchEvent(new Event('change', {bubbles: true}));
    });

    // Hook the appliance picker so swaps re-open the terminal, re-gate the
    // type-specific toolbar actions, and remember the selection across reloads.
    document.addEventListener('change', function(ev) {
      if (ev.target && ev.target.matches &&
          ev.target.matches('.ui-agent-extras select')) {
        try { localStorage.setItem('servitor_last_appliance', ev.target.value || ''); } catch (_) {}
        openTerminal();
        applyToolbarForType();
        applyRepoLayout();
      }
    });

    // Restore the last-selected appliance on load. Without this the picker
    // resets to the placeholder on every reload, which (a) loses the user's
    // selection and (b) makes a deep-linked session (?session=<id>) load with
    // an empty appliance_id — the server returns 404 and the activity pane
    // shows "Error: 404 page not found". Setting the value early (and firing
    // change) gives the framework's deep-link resume a valid appliance_id
    // before its retry-anyway fallback fires.
    function bootAppliancePicker(tries) {
      var sel = document.querySelector('.ui-agent-extras select');
      if (!sel) {
        if (tries < 30) setTimeout(function(){ bootAppliancePicker(tries + 1); }, 100);
        return;
      }
      if (sel.value) return; // already set (deep-link / session event) — don't override
      var saved = '';
      try { saved = localStorage.getItem('servitor_last_appliance') || ''; } catch (_) {}
      if (!saved) return;
      var exists = false;
      for (var i = 0; i < sel.options.length; i++) {
        if (sel.options[i].value === saved) { exists = true; break; }
      }
      if (!exists) return; // stale id (deleted appliance / different user) — leave placeholder
      sel.value = saved;
      sel.dispatchEvent(new Event('change', {bubbles: true}));
    }
    bootAppliancePicker(0);
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', register);
  } else { register(); }
})();
</script>`
