// App-specific UI bits loaded into the orchestrate chat page's <head>.
// Goals:
//
//  1. CSS + renderers for the in-chat plan, intent, and draft blocks —
//     visually 1:1 with servitor's equivalents (servitor_plan,
//     servitor_intent, servitor_draft). Both apps converge on the
//     same look so a user familiar with one immediately recognizes
//     the other.
//  2. CSS for interjection bubble affordances (edit/delete).
//  3. Client actions for the chat-page toolbar (edit/clone/etc).

package orchestrate

const orchestrateWebAssets = `<style>
/* --- Intent block (per-step phase narration; mirror of
 * servitor_intent). Emitted before each worker step starts. --- */
.ui-orch-intent {
  background: var(--bg-2);
  border-left: 3px solid var(--accent);
  border-radius: 4px;
  padding: 0.55rem 0.75rem;
  margin: 0.3rem 0;
  align-self: flex-start;
  max-width: 92%;
}
.ui-orch-intent-label {
  font-size: 0.72rem; font-weight: 600;
  color: var(--accent);
  text-transform: uppercase; letter-spacing: 0.04em;
  margin-bottom: 0.2rem;
}
.ui-orch-intent-task   { color: var(--text-hi); line-height: 1.4; }
.ui-orch-intent-reason {
  color: var(--text-mute);
  font-size: 0.82rem;
  margin-top: 0.25rem;
  font-style: italic;
}

/* --- Plan checklist (mirror of servitor_plan). One block per round;
 * re-emitted on every step status flip via the same id so the
 * dispatcher routes the repeat through onUpdate and the DOM updates
 * in place. --- */
.ui-orch-plan {
  background: var(--bg-2);
  border-left: 3px solid var(--accent);
  border-radius: 4px;
  padding: 0.65rem 0.85rem;
  margin: 0.4rem 0;
  align-self: stretch;
  max-width: 100%;
}
.ui-orch-plan-h {
  font-size: 0.78rem; font-weight: 600;
  color: var(--accent);
  text-transform: uppercase; letter-spacing: 0.04em;
  margin-bottom: 0.5rem;
}
.ui-orch-plan-steps { list-style: none; padding: 0; margin: 0; }
.ui-orch-plan-step {
  display: grid; grid-template-columns: 1.4em 1fr;
  column-gap: 0.4rem; row-gap: 0.1rem;
  padding: 0.25rem 0;
  border-bottom: 1px solid var(--border);
  font-size: 0.88rem; line-height: 1.4;
}
.ui-orch-plan-step:last-child { border-bottom: none; }
.ui-orch-plan-mark {
  font-family: monospace; color: var(--text-mute);
  text-align: center; align-self: start;
}
.ui-orch-plan-title { color: var(--text-hi); }
.ui-orch-plan-detail {
  grid-column: 2;
  color: var(--text-mute); font-size: 0.78rem; font-style: italic;
}
.ui-orch-plan-findings {
  grid-column: 2;
  color: var(--text); font-size: 0.82rem;
  background: var(--bg-1); padding: 0.2rem 0.4rem;
  border-left: 2px solid #3fb950;
  border-radius: 0 3px 3px 0;
  margin-top: 0.15rem;
}
.ui-orch-plan-blocked-reason {
  grid-column: 2;
  color: #f85149; font-size: 0.82rem;
}
.ui-orch-plan-step.in_progress .ui-orch-plan-mark { color: var(--accent); }
.ui-orch-plan-step.done        .ui-orch-plan-mark { color: #3fb950; }
.ui-orch-plan-step.blocked     .ui-orch-plan-mark { color: #f85149; }
.ui-orch-plan-step.done .ui-orch-plan-title {
  color: var(--text-mute); text-decoration: line-through;
}

/* --- Interactive ask card (ask_user with options) --- */
.ui-orch-ask {
  background: var(--bg-2);
  border-left: 3px solid var(--accent);
  border-radius: 4px;
  padding: 0.7rem 0.85rem;
  margin: 0.4rem 0;
  align-self: flex-start;
  max-width: min(38rem, 100%);
}
.ui-orch-ask-q {
  color: var(--text-hi);
  margin-bottom: 0.6rem;
  line-height: 1.4;
}
.ui-orch-ask-opts {
  display: flex; flex-direction: column; gap: 0.3rem;
  margin-bottom: 0.6rem;
}
.ui-orch-ask-opt {
  display: flex; align-items: flex-start; gap: 0.5rem;
  padding: 0.25rem 0.4rem;
  border-radius: 3px;
  cursor: pointer;
}
.ui-orch-ask-opt:hover { background: var(--bg-1); }
.ui-orch-ask-opt input { margin-top: 0.2rem; flex-shrink: 0; }
.ui-orch-ask-opt-lbl { color: var(--text); font-size: 0.88rem; }
.ui-orch-ask-extra-lbl {
  display: block;
  font-size: 0.74rem; color: var(--text-mute);
  margin-bottom: 0.25rem;
  text-transform: uppercase; letter-spacing: 0.04em;
}
.ui-orch-ask-extra {
  width: 100%;
  background: var(--bg-1); border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text); font-family: inherit; font-size: 0.88rem;
  padding: 0.4rem 0.55rem;
  resize: vertical;
  min-height: 2.2rem;
  outline: none;
}
.ui-orch-ask-extra:focus { border-color: var(--accent); }
.ui-orch-ask-actions {
  display: flex; justify-content: flex-end;
  margin-top: 0.5rem;
}
.ui-orch-ask-submit {
  background: var(--accent); color: var(--bg-0);
  border: 1px solid var(--accent);
  padding: 0.35rem 0.9rem;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.82rem; font-family: inherit;
}
.ui-orch-ask-submit:hover { background: var(--accent-hi, var(--accent)); }
.ui-orch-ask-submit[disabled] { opacity: 0.5; cursor: not-allowed; }
.ui-orch-ask.submitted {
  opacity: 0.85;
}
.ui-orch-ask.submitted .ui-orch-ask-submit,
.ui-orch-ask.submitted .ui-orch-ask-extra-lbl {
  display: none;
}
/* Hide unselected option rows entirely; keep selected rows visible
   with strong styling so the historical view reads as "you picked X". */
.ui-orch-ask.submitted .ui-orch-ask-opt {
  display: none;
}
.ui-orch-ask.submitted .ui-orch-ask-opt.picked {
  display: flex;
  background: var(--bg-1);
  border-left: 3px solid var(--accent);
  font-weight: 600;
  cursor: default;
}
.ui-orch-ask.submitted .ui-orch-ask-opt.picked input {
  display: none;
}
.ui-orch-ask.submitted .ui-orch-ask-opt.picked .ui-orch-ask-opt-lbl::before {
  content: "\2713  "; /* checkmark */
  color: var(--accent);
  font-weight: bold;
}
/* The textarea collapses to a static read-only quote of what the
   user typed (if anything), or hides entirely when empty. */
.ui-orch-ask.submitted .ui-orch-ask-extra {
  border: none;
  background: transparent;
  padding: 0.25rem 0;
  resize: none;
  font-style: italic;
  color: var(--text-mute);
  cursor: default;
}
.ui-orch-ask.submitted .ui-orch-ask-extra.empty {
  display: none;
}
/* Small "answered" footer for clarity. */
.ui-orch-ask.submitted::after {
  content: "\2713 Answered";
  display: block;
  margin-top: 0.5rem;
  font-size: 0.75rem;
  color: var(--text-mute);
}

/* --- Interjection bubble affordances (Edit / Delete; consumed style) --- */
.ui-agent-interjection {
  border-left: 3px solid var(--accent);
  padding-left: 0.5rem;
}
.ui-agent-interjection.consumed {
  border-left-color: #3fb950;
  opacity: 0.85;
}
.ui-agent-interjection.consumed::after {
  content: '✓ delivered';
  display: block;
  font-size: 0.7rem;
  color: #3fb950;
  margin-top: 0.2rem;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.ui-agent-interjection-failed {
  border-left-color: #f85149;
}
.ui-orch-interject-actions {
  display: flex; gap: 0.3rem;
  margin-top: 0.3rem;
}
.ui-orch-interject-btn {
  background: var(--bg-1); border: 1px solid var(--border);
  color: var(--text-mute);
  padding: 0.15rem 0.5rem;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.72rem;
  font-family: inherit;
}
.ui-orch-interject-btn:hover { color: var(--text-hi); }
.ui-orch-interject-btn.danger:hover { color: #f85149; border-color: #f85149; }
.ui-orch-interject-btn.primary {
  background: var(--accent); color: var(--bg-0); border-color: var(--accent);
}
/* --- Modal for the top-bar Tools / Memory / Rules buttons --- */
.ui-orch-modal-overlay {
  position: fixed; inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex; align-items: center; justify-content: center;
  z-index: 1000;
  padding: 1rem;
}
.ui-orch-modal {
  background: var(--bg-1);
  border: 1px solid var(--border);
  border-radius: 8px;
  width: 100%; max-width: 36rem;
  max-height: 90vh;
  display: flex; flex-direction: column;
  overflow: hidden;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.5);
}
.ui-orch-modal-h {
  display: flex; align-items: center; justify-content: space-between;
  padding: 0.7rem 1rem;
  border-bottom: 1px solid var(--border);
  background: var(--bg-2);
}
.ui-orch-modal-h-title { font-weight: 600; color: var(--text-hi); }
.ui-orch-modal-h-close {
  background: transparent; border: none; cursor: pointer;
  color: var(--text-mute);
  font-size: 1.4rem; line-height: 1; padding: 0 0.3rem;
}
.ui-orch-modal-h-close:hover { color: var(--text-hi); }
.ui-orch-modal-body {
  padding: 0.8rem 1rem;
  overflow: auto;
  flex: 1 1 auto;
  min-height: 0;
}
.ui-orch-modal-help {
  font-size: 0.78rem; color: var(--text-mute);
  margin-bottom: 0.7rem;
  line-height: 1.45;
}
/* --- Custom tools section in the Tools modal -------------------- */
.ui-orch-custom-tools {
  margin-bottom: 1rem;
  padding: 0.6rem 0.8rem;
  background: var(--bg-1);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.ui-orch-custom-tools-h {
  font-size: 0.78rem;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 0.2rem;
}
.ui-orch-custom-tools-help {
  font-size: 0.72rem;
  color: var(--text-mute);
  margin-bottom: 0.5rem;
}
.ui-orch-custom-tool {
  display: flex;
  gap: 0.55rem;
  align-items: flex-start;
  padding: 0.4rem 0;
  border-top: 1px solid var(--border);
}
.ui-orch-custom-tool:first-of-type {
  border-top: none;
}
.ui-orch-custom-tool-icon {
  flex-shrink: 0;
  font-size: 1rem;
  width: 1.4rem;
  text-align: center;
  padding-top: 0.05rem;
}
.ui-orch-custom-tool-meta {
  flex: 1 1 auto;
  min-width: 0;
}
.ui-orch-custom-tool-name {
  font-family: var(--mono, monospace);
  font-size: 0.85rem;
  color: var(--text);
  font-weight: 600;
}
.ui-orch-custom-tool-desc {
  font-size: 0.78rem;
  color: var(--text);
  margin-top: 0.1rem;
  line-height: 1.4;
}
.ui-orch-custom-tool-summary {
  font-family: var(--mono, monospace);
  font-size: 0.72rem;
  color: var(--text-mute);
  margin-top: 0.2rem;
  white-space: pre-wrap;
  word-break: break-all;
}
.ui-orch-modal-ftr {
  display: flex; gap: 0.5rem; justify-content: flex-end;
  padding: 0.7rem 1rem;
  border-top: 1px solid var(--border);
  background: var(--bg-2);
}
.ui-orch-modal-btn {
  background: var(--bg-1); border: 1px solid var(--border);
  color: var(--text);
  padding: 0.4rem 0.9rem;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.82rem;
  font-family: inherit;
}
.ui-orch-modal-btn:hover { border-color: var(--accent); color: var(--accent); }
.ui-orch-modal-btn.primary {
  background: var(--accent); color: var(--bg-0); border-color: var(--accent);
}
.ui-orch-modal-btn.primary:hover { background: var(--accent-hi, var(--accent)); }
.ui-orch-modal-btn[disabled] { opacity: 0.6; cursor: not-allowed; }

/* Memory + rules: a list of removable line items inside the modal. */
.ui-orch-list { display: flex; flex-direction: column; gap: 0.35rem; }
.ui-orch-list-row {
  display: flex; gap: 0.4rem; align-items: flex-start;
  padding: 0.35rem 0.5rem;
  background: var(--bg-2); border: 1px solid var(--border);
  border-radius: 4px;
}
.ui-orch-list-row textarea, .ui-orch-list-row input {
  flex: 1; min-width: 0;
  background: transparent; border: none;
  color: var(--text); font-family: inherit; font-size: 0.85rem;
  outline: none; resize: vertical;
  line-height: 1.4;
}
.ui-orch-list-del {
  background: transparent; border: none; cursor: pointer;
  color: var(--text-mute); padding: 0 0.3rem;
  font-size: 1rem; line-height: 1;
}
.ui-orch-list-del:hover { color: var(--danger); }
.ui-orch-list-add {
  align-self: flex-start;
  background: var(--bg-2); border: 1px dashed var(--border);
  color: var(--text-mute); cursor: pointer;
  padding: 0.3rem 0.7rem; border-radius: 4px;
  font-size: 0.78rem; font-family: inherit;
}
.ui-orch-list-add:hover { color: var(--accent); border-color: var(--accent); }

.ui-orch-interject-edit-row {
  display: flex; gap: 0.3rem; margin-top: 0.3rem; justify-content: flex-end;
}

/* Tools / Memory / Rules buttons relocated into the modes row sit
 * next to the Private pill. Compact them to roughly the pill's
 * footprint so the row reads as one cohesive strip rather than two
 * mismatched button styles. */
.ui-agent-modes .ui-row-btn {
  padding: 0.25rem 0.65rem;
  font-size: 0.76rem;
  border-radius: 4px;
}

/* Intake form rendered in the conversation pane on the FIRST turn
 * of a new session when the active agent has intake_form configured.
 * Submitting packs values into a markdown user message and hands
 * off to the normal chat send path. */
.ui-orch-intake {
  background: var(--bg-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 0.9rem 1rem;
  margin: 1.5rem auto;
  width: 100%;
  max-width: 520px;
  align-self: center;
}
/* Decorated user bubble (intake submitted, now part of history):
 * stretch the bubble across the pane and strip its right-aligned
 * accent so the centered form inside doesn't read as a tiny card
 * pushed off to the right. The form's own border + bg carry the
 * visual identity. */
.ui-agent-msg-user[data-ui-intake="1"] {
  align-self: stretch;
  max-width: 100%;
}
.ui-agent-msg-user[data-ui-intake="1"] .ui-agent-msg-body {
  background: transparent;
  border-right: none;
  padding: 0;
}
.ui-orch-intake-header {
  font-size: 0.85rem;
  color: var(--text-mute);
  margin-bottom: 0.7rem;
}
/* Agency-side agent description block above intake form. Matches
   the look of .ui-agent-empty (which is what the public agent app
   uses for the same text via cfg.empty_text) so Agency and the
   per-app surface render the description identically. */
.ui-agency-agent-desc {
  color: var(--text-mute);
  font-style: italic;
  text-align: center;
  padding: 1.5rem 0;
}
.ui-orch-intake-row {
  display: flex; flex-direction: column; gap: 0.25rem;
  margin-bottom: 0.7rem;
}
.ui-orch-intake-label {
  font-size: 0.78rem; font-weight: 600;
  color: var(--text-hi);
}
.ui-orch-intake-input {
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 4px;
  padding: 0.4rem 0.55rem; font: inherit;
}
.ui-orch-intake-textarea { resize: vertical; min-height: 3.2rem; }
.ui-orch-intake-help {
  font-size: 0.75rem; color: var(--text-mute);
}
.ui-orch-intake-actions {
  display: flex; justify-content: flex-end; margin-top: 0.5rem;
}
.ui-orch-intake-button-row {
  display: flex; flex-wrap: wrap; gap: 0.4rem;
}
.ui-orch-intake-button {
  padding: 0.35rem 0.8rem; font-size: 0.85rem;
}
.ui-orch-intake-button:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.ui-orch-intake-button.selected {
  opacity: 1;
  color: var(--accent);
  border-color: var(--accent);
}
.ui-orch-intake-button.selected:disabled {
  opacity: 0.9;
}
.ui-orch-intake-button-only .ui-orch-intake-button-row {
  justify-content: center;
}
.ui-orch-intake-button-only .ui-orch-intake-header {
  text-align: center;
}
.ui-orch-intake-button-only .ui-orch-intake-actions {
  display: none;
}

/* Checklist (multi-select) — vertical stack of checkbox+label rows.
 * Renders inside the same .ui-orch-intake-row as other inputs. */
.ui-orch-intake-checklist {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  padding: 0.3rem 0;
}
.ui-orch-intake-checklist-item {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.9rem;
  color: var(--text);
  cursor: pointer;
}
.ui-orch-intake-checklist-item input[type=checkbox] {
  margin: 0;
  cursor: pointer;
}
.ui-orch-intake-checklist-item input[type=checkbox]:disabled {
  cursor: default;
}
.ui-orch-intake-checklist-other {
  margin-top: 0.2rem;
}
.ui-orch-intake-checklist-other-text {
  flex: 1;
  background: var(--bg-1);
  border: 1px solid var(--border);
  border-radius: 3px;
  color: var(--text);
  font-family: inherit;
  font-size: 0.88rem;
  padding: 0.25rem 0.45rem;
  outline: none;
}
.ui-orch-intake-checklist-other-text:focus {
  border-color: var(--accent);
}
.ui-orch-intake-checklist-other-text:disabled {
  background: transparent;
  border-color: transparent;
}

/* Form-result mirror for the user bubble after intake-form submit.
 * Replaces the raw markdown text with a read-only structured view
 * of the filled fields so the intake's visual shape stays visible
 * in the chat history. */
.ui-orch-intake-result {
  display: grid;
  grid-template-columns: minmax(8rem, auto) 1fr;
  gap: 0.35rem 0.85rem;
  margin: 0.1rem 0;
}
.ui-orch-intake-result-row {
  display: contents;
}
.ui-orch-intake-result-label {
  color: var(--text-mute); font-size: 0.78rem;
  font-weight: 600; padding-top: 0.05rem;
  text-transform: uppercase; letter-spacing: 0.04em;
}
.ui-orch-intake-result-value {
  color: var(--text-hi); font-size: 0.9rem;
  white-space: pre-wrap; word-wrap: break-word;
  border-left: 2px solid var(--border);
  padding-left: 0.7rem;
}

/* Tools modal: collapsible group sections + master checkbox. The
 * generic ui-checklist-group is a single line header; we add an
 * orchestrate-specific override that turns it into a clickable row
 * with a caret + master checkbox alongside the label. */
.ui-checklist-section { margin-top: 0.4rem; }
.ui-checklist-section:first-child { margin-top: 0; }
.ui-checklist-group-collapsible {
  display: flex; align-items: center; gap: 0.4rem;
  cursor: pointer; user-select: none;
  padding: 0.35rem 0.3rem;
  border-radius: 3px;
  margin-top: 0; margin-bottom: 0;
  font-size: 0.78rem;
  text-transform: none; letter-spacing: 0;
  color: var(--text);
}
.ui-checklist-group-collapsible:hover { background: var(--bg-1); }
.ui-checklist-caret {
  width: 0.9rem; text-align: center;
  color: var(--text-mute); font-size: 0.7rem;
  flex-shrink: 0;
}
.ui-checklist-master { margin: 0; flex-shrink: 0; }
.ui-checklist-group-name {
  color: var(--text-hi); font-weight: 600;
  text-transform: uppercase; letter-spacing: 0.04em;
  font-size: 0.72rem;
}
.ui-checklist-section-body {
  padding-left: 1.4rem; /* indent rows under their group header */
}

</style>
<script>
(function() {
  function register() {
    if (!window.uiRegisterBlockRenderer) {
      setTimeout(register, 50);
      return;
    }
    var el = window.uiEl;
    var md = window.uiMdToHTML;

    // Intent narration — emitted before each worker step starts.
    // Lands in the conversation pane as an accent-bordered card so
    // the user sees what the agent is about to do. 1:1 with
    // servitor_intent.
    window.uiRegisterBlockRenderer('orchestrate_intent', function(d) {
      var wrap = el('div', {class: 'ui-orch-intent'});
      wrap.appendChild(el('div', {class: 'ui-orch-intent-label'}, ['▸ Investigating']));
      if (d.text) {
        wrap.appendChild(el('div', {class: 'ui-orch-intent-task'}, [d.text]));
      }
      if (d.reason) {
        wrap.appendChild(el('div', {class: 'ui-orch-intent-reason'}, [d.reason]));
      }
      return {wrap: wrap, body: null};
    });

    // Plan checklist — mirror of servitor_plan. One block per round
    // (id="plan-<session>-<round>"); the dispatcher calls onUpdate on
    // every subsequent emit so the checklist refreshes in place.
    // Status values come straight from the server-side PlanStep:
    // pending / in_progress / done / blocked.
    window.uiRegisterBlockRenderer('orchestrate_plan', function(d) {
      var wrap = el('div', {class: 'ui-orch-plan'});
      wrap.appendChild(el('div', {class: 'ui-orch-plan-h'}, ['▸ Plan']));
      var stepsBox = el('ul', {class: 'ui-orch-plan-steps'});
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
          var row = el('li', {class: 'ui-orch-plan-step ' + status});
          row.appendChild(el('span', {class: 'ui-orch-plan-mark'}, [mark]));
          row.appendChild(el('span', {class: 'ui-orch-plan-title'},
            [step.title || '']));
          if (step.what_to_find) {
            row.appendChild(el('span', {class: 'ui-orch-plan-detail'},
              [step.what_to_find]));
          }
          if (step.findings) {
            row.appendChild(el('span', {class: 'ui-orch-plan-findings'},
              ['↳ ' + step.findings]));
          }
          if (step.blocked_reason) {
            row.appendChild(el('span', {class: 'ui-orch-plan-blocked-reason'},
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

    // uiOrchAskMd renders the ask-card question through the shared
    // markdown helper so an LLM that writes **bold**, inline code, or a
    // bulleted clarification in its question gets formatted output
    // instead of raw markup. Single-paragraph questions (the common
    // case) have their sole <p> wrapper stripped so the question keeps
    // the card's own spacing rather than inheriting <p> margins. Sets
    // the element's content in place; falls back to plain text if the
    // helper is unavailable (defensive — it's always present in core/ui).
    function uiOrchAskMd(elm, text) {
      text = text || '';
      if (!window.uiMdToHTML) { elm.textContent = text; return; }
      var html = window.uiMdToHTML(text);
      var m = html.match(/^<p>([\s\S]*)<\/p>\s*$/);
      if (m && m[1].indexOf('<p>') === -1) html = m[1];
      elm.innerHTML = html;
    }

    // Interactive ask card — orchestrator's ask_user with options.
    // Renders the question + checkbox / radio list + free-text input
    // + Submit button. Submit constructs the user's answer by joining
    // selected option labels with the free-text remarks, then fires
    // it through the chat panel's normal input row so the agent
    // sees it as a regular user message in the next turn.
    window.uiRegisterBlockRenderer('orchestrate_ask', function(d) {
      var wrap = el('div', {class: 'ui-orch-ask'});
      var q = el('div', {class: 'ui-orch-ask-q'});
      uiOrchAskMd(q, d.question);
      wrap.appendChild(q);

      var opts = (d.options || []).map(function(s) { return String(s || '').trim(); })
                                  .filter(function(s) { return s.length > 0; });
      var multi = !!d.multi;
      var inputs = [];
      if (opts.length) {
        var optsBox = el('div', {class: 'ui-orch-ask-opts'});
        opts.forEach(function(opt, i) {
          var row = el('label', {class: 'ui-orch-ask-opt'});
          var input = document.createElement('input');
          input.type = multi ? 'checkbox' : 'radio';
          input.name = 'orch-ask-' + (d.id || Math.random().toString(36).slice(2));
          input.value = opt;
          row.appendChild(input);
          row.appendChild(el('span', {class: 'ui-orch-ask-opt-lbl'}, [opt]));
          optsBox.appendChild(row);
          inputs.push(input);
        });
        wrap.appendChild(optsBox);
      }

      // Always-on freetext path. The label + placeholder lean
      // hard on "other / push back / discuss" rather than treating
      // this as an afterthought note field — the previous "Additional
      // notes (optional)" wording let users miss that they could
      // bypass the buttons entirely with their own answer. When the
      // user types here without picking an option, the submit packs
      // the text content alone (no prefix); when combined with picks,
      // it's joined "pick1, pick2. text". So "yes, but actually let's
      // discuss X" reads naturally either way.
      wrap.appendChild(el('span', {class: 'ui-orch-ask-extra-lbl'},
        [opts.length ? 'Other — write your own answer, push back, or discuss' : 'Your answer']));
      var extra = document.createElement('textarea');
      extra.className = 'ui-orch-ask-extra';
      extra.rows = opts.length ? 2 : 3;
      extra.placeholder = opts.length
        ? 'None of the above? Type your own answer, ask a follow-up, or push back…'
        : 'Type your answer…';
      wrap.appendChild(extra);

      var actions = el('div', {class: 'ui-orch-ask-actions'});
      var submit = el('button', {class: 'ui-orch-ask-submit', type: 'button'}, ['Submit']);
      actions.appendChild(submit);
      wrap.appendChild(actions);

      submit.addEventListener('click', function() {
        var picked = inputs.filter(function(i) { return i.checked; })
                          .map(function(i) { return i.value; });
        var note = (extra.value || '').trim();
        var parts = [];
        if (picked.length) parts.push(picked.join(', '));
        if (note) parts.push(note);
        var answer = parts.join('. ');
        if (!answer) {
          extra.focus();
          return;
        }
        // Fire the answer through the chat panel's input — easiest
        // way to get it into the normal send flow (events stream,
        // session bookkeeping, etc.) without poking at internals.
        // The compiled answer renders as a normal user bubble (no
        // suppression) so Retry has a role=user anchor to walk back
        // to. Without that anchor, Retry would skip past the form
        // submission and replay the conversation from the original
        // message before the form was ever asked.
        var inputArea = document.querySelector('.ui-agent-input');
        var sendBtn = document.querySelector('.ui-agent-input-row .ui-row-btn.primary');
        if (!inputArea || !sendBtn) {
          window.uiAlert('Could not find the chat input to submit your answer.');
          return;
        }
        inputArea.value = answer;
        sendBtn.click();
        // Lock the card and reshape into a historical view: mark the
        // picked options so CSS keeps them visible (.picked) while
        // hiding the rest; tag the textarea .empty when blank so the
        // empty quote line collapses out.
        wrap.classList.add('submitted');
        inputs.forEach(function(i) {
          i.disabled = true;
          var row = i.parentElement;
          if (i.checked && row && row.classList) {
            row.classList.add('picked');
          }
        });
        extra.disabled = true;
        if (!note) {
          extra.classList.add('empty');
        } else {
          extra.value = note; // normalized trimmed value
        }
        submit.disabled = true;
      });

      return {wrap: wrap, body: null};
    });

    // Multi-step form — orchestrator's ask_user_form. Renders one
    // question at a time with Next / Back navigation; final step
    // shows Submit. Each step's selections + free-text feed into a
    // compiled answer the user submits via the chat input. Mirrors
    // Claude.ai's wizard-style clarifying flow.
    window.uiRegisterBlockRenderer('orchestrate_ask_form', function(d) {
      var wrap = el('div', {class: 'ui-orch-ask'});
      var steps = (d.steps || []).filter(function(s) { return s && s.question; });
      if (!steps.length) {
        wrap.appendChild(el('div', {class: 'ui-orch-ask-q'}, ['(form had no questions)']));
        return {wrap: wrap, body: null};
      }
      // Per-step state: picked options + free-text note.
      var answers = steps.map(function() { return {picked: [], note: ''}; });
      var current = 0;

      var qEl = el('div', {class: 'ui-orch-ask-q'});
      wrap.appendChild(qEl);
      var optsBox = document.createElement('div');
      optsBox.className = 'ui-orch-ask-opts';
      wrap.appendChild(optsBox);
      var noteLbl = el('span', {class: 'ui-orch-ask-extra-lbl'});
      wrap.appendChild(noteLbl);
      var noteEl = document.createElement('textarea');
      noteEl.className = 'ui-orch-ask-extra';
      noteEl.rows = 2;
      wrap.appendChild(noteEl);

      var actions = el('div', {class: 'ui-orch-ask-actions'});
      var counter = document.createElement('span');
      counter.style.flex = '1';
      counter.style.color = 'var(--text-mute)';
      counter.style.fontSize = '0.74rem';
      counter.style.alignSelf = 'center';
      actions.appendChild(counter);
      var backBtn = document.createElement('button');
      backBtn.className = 'ui-orch-modal-btn';
      backBtn.type = 'button';
      backBtn.textContent = 'Back';
      var nextBtn = document.createElement('button');
      nextBtn.className = 'ui-orch-ask-submit';
      nextBtn.type = 'button';
      actions.appendChild(backBtn);
      actions.appendChild(nextBtn);
      wrap.appendChild(actions);

      function saveCurrent() {
        // Capture state before redrawing for the next step.
        var inputs = optsBox.querySelectorAll('input');
        var picked = [];
        inputs.forEach(function(i) { if (i.checked) picked.push(i.value); });
        answers[current].picked = picked;
        answers[current].note = noteEl.value || '';
      }

      function renderStep() {
        var step = steps[current];
        uiOrchAskMd(qEl, (current + 1) + '. ' + step.question);
        optsBox.innerHTML = '';
        var multi = !!step.multi;
        var opts = (step.options || []).map(function(s) { return String(s || '').trim(); })
                                       .filter(function(s) { return s.length > 0; });
        var saved = answers[current];
        opts.forEach(function(opt) {
          var row = el('label', {class: 'ui-orch-ask-opt'});
          var input = document.createElement('input');
          input.type = multi ? 'checkbox' : 'radio';
          input.name = 'orch-form-' + (d.id || '') + '-' + current;
          input.value = opt;
          if (saved.picked.indexOf(opt) >= 0) input.checked = true;
          row.appendChild(input);
          row.appendChild(el('span', {class: 'ui-orch-ask-opt-lbl'}, [opt]));
          optsBox.appendChild(row);
        });
        noteLbl.textContent = opts.length ? 'Additional notes (optional)' : 'Your answer';
        noteEl.value = saved.note || '';
        noteEl.placeholder = opts.length
          ? 'Add anything not covered by the options above…'
          : 'Type your answer…';
        counter.textContent = 'Step ' + (current + 1) + ' of ' + steps.length;
        backBtn.style.visibility = current === 0 ? 'hidden' : '';
        nextBtn.textContent = (current === steps.length - 1) ? 'Submit' : 'Next';
      }
      renderStep();

      backBtn.addEventListener('click', function() {
        saveCurrent();
        if (current > 0) {
          current--;
          renderStep();
        }
      });
      nextBtn.addEventListener('click', function() {
        saveCurrent();
        if (current < steps.length - 1) {
          current++;
          renderStep();
          return;
        }
        // Final step → compile + submit.
        var lines = steps.map(function(step, i) {
          var ans = answers[i];
          var parts = [];
          if (ans.picked.length) parts.push(ans.picked.join(', '));
          if ((ans.note || '').trim()) parts.push(ans.note.trim());
          var answer = parts.join(' — ');
          return (i + 1) + '. ' + step.question + ' → ' + (answer || '(no answer)');
        });
        var compiled = lines.join('\n');
        var inputArea = document.querySelector('.ui-agent-input');
        var sendBtn = document.querySelector('.ui-agent-input-row .ui-row-btn.primary');
        if (!inputArea || !sendBtn) {
          window.uiAlert('Could not find the chat input to submit your answers.');
          return;
        }
        inputArea.value = compiled;
        // No bubble suppression — the compiled answer renders as a
        // user bubble so Retry has an anchor to walk back to. Without
        // this, Retry skips past the form submission and replays the
        // entire conversation from the user's original message.
        sendBtn.click();
        wrap.classList.add('submitted');
        optsBox.querySelectorAll('input').forEach(function(i) { i.disabled = true; });
        noteEl.disabled = true;
        nextBtn.disabled = true;
        backBtn.disabled = true;
      });

      return {wrap: wrap, body: null};
    });

    // --- Interjection notes: consumed marker + per-bubble actions --
    // The runtime tags interjection bubbles with .ui-agent-interjection
    // and data-{session-id,note-id,inject-url} when the user submits
    // mid-flight notes. We register a block renderer for the server-
    // emitted notes_consumed event (mark bubbles delivered) plus a
    // MutationObserver that decorates new interjection bubbles with
    // Edit and Delete buttons (servitor's pattern).

    window.uiRegisterBlockRenderer('orchestrate_notes_consumed', function(d) {
      var ids = d.ids || [];
      ids.forEach(function(noteID) {
        var sel = '.ui-agent-interjection[data-note-id="' +
          (window.CSS && CSS.escape ? CSS.escape(noteID) : noteID) + '"]';
        var bubble = document.querySelector(sel);
        if (!bubble) return;
        bubble.classList.add('consumed');
        // Drained notes can no longer be edited / deleted; drop
        // the affordances so the user doesn't see actions that
        // would 410.
        var actions = bubble.querySelector('.ui-orch-interject-actions');
        if (actions) actions.remove();
      });
      // notes_consumed isn't a renderable block — return null so
      // addBlock doesn't try to attach a DOM node to the convo log.
      return {wrap: null, body: null};
    });

    function decorateInterjection(bubble) {
      if (!bubble || bubble.querySelector('.ui-orch-interject-actions')) return;
      if (bubble.classList.contains('consumed')) return;
      var actions = el('div', {class: 'ui-orch-interject-actions'});
      var editBtn = el('button', {
        class: 'ui-orch-interject-btn',
        onclick: function(ev) { ev.stopPropagation(); editInterjection(bubble); },
      }, ['Edit']);
      var delBtn = el('button', {
        class: 'ui-orch-interject-btn danger',
        onclick: function(ev) { ev.stopPropagation(); deleteInterjection(bubble); },
      }, ['Delete']);
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
      // Lock first so the runner doesn't drain it mid-edit.
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
        var actions = bubble.querySelector('.ui-orch-interject-actions');
        if (!body) return;
        var oldText = body.textContent;
        var ta = document.createElement('textarea');
        ta.value = oldText; ta.className = 'ui-form-input';
        ta.style.width = '100%'; ta.rows = 3;
        body.style.display = 'none';
        if (actions) actions.style.display = 'none';
        bubble.appendChild(ta);
        var editRow = document.createElement('div');
        editRow.className = 'ui-orch-interject-edit-row';
        var saveBtn = document.createElement('button');
        saveBtn.className = 'ui-orch-interject-btn primary';
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
        cancelBtn.className = 'ui-orch-interject-btn';
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
      fetch(url, {
        method: 'DELETE', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: sid, note_id: noteID}),
      }).then(function(r) {
        if (r.status === 410) { window.uiAlert('Note already picked up by the agent.'); return; }
        if (!r.ok) { throw new Error('delete failed'); }
        bubble.remove();
      }).catch(function(err) {
        window.uiAlert('Delete failed: ' + (err && err.message || err));
      });
    }

    // Activity pane is locked off for every orchestrate agent (via
    // LockActivity on the AgentLoopPanel config) — the conversation
    // pane carries all narration via per-round bubbles, plan cards,
    // intent blocks, and the draft synthesis. No per-agent JS toggle
    // needed.

    // Switching the agent dropdown should land the user on a fresh
    // conversation — staying on the prior session would mean the
    // OLD agent's history feeding the NEW agent's tool surface,
    // which surfaces confusing behavior (e.g. tools disappearing
    // because the new agent's AllowedTools narrowed the pool).
    // Wire a change-listener that triggers the framework's "+ New
    // conversation" button so the runtime's openSession(null) path
    // fires (clears activeSessionId, drops the deep-link param,
    // refreshes the rail for the new agent).
    function watchAgentSwitchForNewSession() {
      var sel = document.querySelector('.ui-agent-extras select');
      if (!sel) { setTimeout(watchAgentSwitchForNewSession, 200); return; }
      // Seed the per-(user, agent) toggle scope from the initial
      // selection so the runtime's ChatMode toggle reads the right
      // override on first paint.
      window.GOHORT_AGENT_ID = sel.value || '';
      window.dispatchEvent(new CustomEvent('gohort-agent-id-changed',
        {detail: {agent_id: window.GOHORT_AGENT_ID}}));
      // pinAgentInURL keeps the URL bar synced with the picker so
      // refresh / edit-and-back / share-link all stay on the chosen
      // agent. /orchestrate/ (no param) still defaults to seed-chat
      // server-side; once the user picks an agent, ?agent=<id> is the
      // sticky surface. replaceState (not pushState) so picker churn
      // doesn't grow the back-stack — back still takes the user out
      // of Agency, not back through agent selections.
      function pinAgentInURL(id) {
        try {
          var u = new URL(window.location.href);
          if (id) {
            u.searchParams.set('agent', id);
          } else {
            u.searchParams.delete('agent');
          }
          window.history.replaceState({}, '', u.toString());
        } catch (_) {}
      }
      pinAgentInURL(window.GOHORT_AGENT_ID);
      // Track current value so the first listener invocation (which
      // can fire as a no-op when the runtime reads cfg.default into
      // the select) doesn't reset a still-active session.
      var current = sel.value;
      sel.addEventListener('change', function() {
        if (sel.value === current) return;
        current = sel.value;
        // Update the per-(user, agent) scope BEFORE the new-session
        // click — the runtime's toggle GETs run on session refresh
        // and need the new agent id to read the right override.
        window.GOHORT_AGENT_ID = sel.value || '';
        window.dispatchEvent(new CustomEvent('gohort-agent-id-changed',
          {detail: {agent_id: window.GOHORT_AGENT_ID}}));
        pinAgentInURL(window.GOHORT_AGENT_ID);
        var newBtn = document.querySelector('.ui-chat-new');
        if (newBtn) {
          newBtn.click();
        } else {
          // Fallback when the side-rail button isn't in the DOM
          // (shouldn't happen on orchestrate, but defensive): strip
          // the session deep-link and reload so the panel resets.
          try {
            var u = new URL(window.location.href);
            u.searchParams.delete('session');
            window.location.href = u.toString();
          } catch (_) { window.location.reload(); }
        }
      });
    }
    watchAgentSwitchForNewSession();

    // Contextual sub-agent picker. When the active top-level agent has
    // owned sub-agents (per ORCH_SUB_AGENTS embedded by page_chat.go),
    // render a secondary select right under the main agent picker so
    // the user can chat directly with a specialist for testing without
    // dispatching from the parent. Empty (default) = chat with the
    // parent normally; pick a sub-agent = swap the active agent_id to
    // that sub-agent for the session. Sub-agents are absent from the
    // main dropdown (Hidden=true via enforceSubAgentPosture); we
    // transiently inject an option for the picked one so the existing
    // change-event plumbing (new session, scope swap, refresh) just
    // works without touching the chat panel internals.
    function mountSubAgentPicker() {
      var sel = document.querySelector('.ui-agent-extras select');
      if (!sel) { setTimeout(mountSubAgentPicker, 200); return; }
      var subMap = window.ORCH_SUB_AGENTS || {};

      // Track the "parent" the secondary picker is scoped to. When the
      // active value IS a sub-agent (user picked one earlier and the
      // page reloaded), find its parent so the secondary picker still
      // renders correctly.
      function findParentOf(id) {
        for (var pid in subMap) {
          var kids = subMap[pid] || [];
          for (var i = 0; i < kids.length; i++) {
            if (kids[i].id === id) return pid;
          }
        }
        return null;
      }

      // Build the secondary picker DOM, hidden by default. The agent
      // picker (.ui-agent-extras) lives directly inside
      // .ui-agent-top-bundle, a CSS GRID with two columns:
      //   col 1 (rail-width, 260px) = main picker
      //   col 2 (1fr)               = bundle-table (buttons)
      // A naked sibling falls into grid auto-flow and lands at col 2
      // row 1, pushing the bundle-table to col 1 row 2 (the exact
      // symptom the user reported). Pin the wrap to grid-column: 1
      // so it lands in col 1 row 2 — directly under the main picker,
      // leaving the bundle-table untouched at col 2 row 1.
      //
      // Theme: native <select> in a dark page needs explicit
      // background/color/border to look right; the main picker
      // happens to render fine on systems where the browser honors
      // color-scheme but that's not reliable. Apply the framework
      // tokens directly so both pickers match regardless of browser.
      var host = sel.closest('.ui-agent-extras');
      if (!host) return;
      // Put the specialist picker INSIDE the agent-extras container,
      // stacked vertically below the main picker. Earlier attempts
      // placed it as a sibling in the bundle grid — but the bundle's
      // right column (the buttons table) is tall (two button rows), so
      // the grid's row 2 visually landed below the entire button table,
      // not directly below the main picker. Putting both pickers in the
      // SAME grid cell (column 1 of the bundle) and flex-column'ing the
      // host makes the second picker hug the main picker, and the
      // buttons in column 2 stay put regardless of how tall the picker
      // stack gets.
      host.style.flexDirection = 'column';
      host.style.alignItems = 'stretch';
      host.style.gap = '0.25rem';
      // The framework pads the in-side host symmetrically for a
      // single-row layout; shift to a bit of vertical breathing room
      // now that there are two stacked rows.
      host.style.padding = '0.25rem 0.4rem 0.3rem 0.6rem';
      var label = document.createElement('label');
      label.className = 'ui-agent-extras-label ui-orch-subagent-row';
      var caption = document.createElement('span');
      caption.textContent = 'Specialist';
      label.appendChild(caption);
      var subSel = document.createElement('select');
      subSel.name = 'orch_sub_agent';
      // Use the framework's themed-select class — same rule the main
      // agent picker uses (see core/ui/runtime.go's FormPanel select
      // render path). One class binds background/color/border/radius/
      // appearance from the design tokens, so the two pickers always
      // match without per-element style drift.
      subSel.className = 'ui-form-select';
      label.appendChild(subSel);
      // Append the specialist row inside the SAME host as the main
      // picker so both share column 1 of the bundle grid.
      host.appendChild(label);

      // restoreParentOptions — if a prior sub-agent selection rewrote a
      // top-level option's underlying value, put it back. Used whenever
      // we change the active parent so the main picker's options carry
      // their real IDs again. Keyed off the data-original-value
      // attribute we set when overriding.
      function restoreParentOptions() {
        var opts = sel.querySelectorAll('option[data-orch-original-value]');
        for (var i = 0; i < opts.length; i++) {
          opts[i].value = opts[i].getAttribute('data-orch-original-value');
          opts[i].removeAttribute('data-orch-original-value');
        }
      }

      // suppressRestoreOnce gates restoreParentOptions on the synthetic
      // sel.change we dispatch from the specialist picker — without it,
      // sel.change would immediately revert the override we JUST applied
      // (sub-agent ID → parent ID), so Edit / Tools / Memory / etc. all
      // reach for the parent's record. Only real user changes on the
      // main picker should trigger restoreParentOptions.
      var suppressRestoreOnce = false;

      // Repopulate subSel based on the given parent's children. Picks
      // the current sub-agent (if the main picker IS one) as the
      // selected option. Empty parent → hide the picker entirely so
      // the chrome doesn't read as "broken" for agents with no
      // specialists; the row reappears the moment the selected parent
      // does have sub-agents.
      function populate(parentID, activeID) {
        var kids = (parentID && subMap[parentID]) || [];
        if (!kids.length) {
          label.style.display = 'none';
          subSel.innerHTML = '';
          return;
        }
        label.style.display = '';
        subSel.disabled = false;
        subSel.innerHTML = '';
        subSel.appendChild(new Option('— main agent —', ''));
        kids.forEach(function(k) {
          subSel.appendChild(new Option(k.name, k.id));
        });
        if (activeID && parentID !== activeID) {
          subSel.value = activeID;
        } else {
          subSel.value = '';
        }
      }

      // Sync the secondary picker to the main picker's current value.
      // The main picker's option may have a sub-agent ID stored as its
      // current value (because we override) — resolve back to the real
      // parent ID via data-orch-original-value when present so the
      // specialist list belongs to the right parent.
      function syncFromMain() {
        var cur = sel.value;
        // If the selected option carries a rewritten value, the
        // real parent ID is its data-orch-original-value.
        var selOpt = sel.options[sel.selectedIndex];
        var parentID = selOpt && selOpt.getAttribute('data-orch-original-value');
        if (!parentID) {
          parentID = subMap[cur] ? cur : findParentOf(cur);
        }
        populate(parentID, cur);
      }
      syncFromMain();
      sel.addEventListener('change', function() {
        // User picked a different option from the main picker — clear
        // any prior overrides so the new selection's value is its real
        // ID, then re-sync the specialist list to the new parent.
        // EXCEPT when we just dispatched this change ourselves from the
        // specialist picker; in that case the override is intentional.
        if (suppressRestoreOnce) {
          suppressRestoreOnce = false;
        } else {
          restoreParentOptions();
        }
        syncFromMain();
      });

      // Picking a sub-agent → rewrite the CURRENTLY SELECTED option's
      // value to the sub-agent's ID while leaving its label alone. The
      // main picker visibly keeps showing the parent's name; the form's
      // agent_id (read from the select's value) carries the sub-agent
      // ID so all downstream routing (sessions, chat sends, scope
      // events) targets the specialist. Restoring back to "— main
      // agent —" reverts the option's value via data-orch-original-value.
      subSel.addEventListener('change', function() {
        var picked = subSel.value;
        var selOpt = sel.options[sel.selectedIndex];
        if (!selOpt) return;
        // Remember the parent's real ID once; subsequent flips between
        // specialists read from the saved attribute.
        var realParentID = selOpt.getAttribute('data-orch-original-value') || selOpt.value;
        if (!selOpt.hasAttribute('data-orch-original-value')) {
          selOpt.setAttribute('data-orch-original-value', realParentID);
        }
        var targetID = picked || realParentID;
        selOpt.value = targetID;
        sel.value = targetID;
        // Clean up the override attribute when reverting to the parent
        // so the option matches the rest of the dropdown's state.
        if (!picked) {
          selOpt.removeAttribute('data-orch-original-value');
        }
        // Dispatch a change for downstream listeners (session swap,
        // scope events) but suppress the override-revert in our own
        // sel.change handler so the override we just applied survives.
        suppressRestoreOnce = true;
        sel.dispatchEvent(new Event('change'));
      });
    }
    mountSubAgentPicker();

    // Show the active agent's description after a selection. Two
    // shapes depending on whether the agent has an intake form:
    //   - No intake → REPLACE the "Pick an agent above…" placeholder
    //     text on .ui-agent-empty (single line, no separate block).
    //   - Intake present → ADD a separate description block ABOVE
    //     the intake form so both render (description as context;
    //     intake form as the action).
    // Inlined fetch (no fetchAgent call) because that helper lives
    // in an inner scope and isn't reachable from here — calling it
    // would throw ReferenceError and abort the rest of the script
    // (which silently breaks relocateContextButtons + intake form
    // registration). Lesson learned: only call helpers in the same
    // 4-space-indent scope from here.
    function paintAgentDescriptionPlaceholder() {
      var sel = document.querySelector('.ui-agent-extras select');
      if (!sel) { setTimeout(paintAgentDescriptionPlaceholder, 250); return; }
      function apply() {
        var id = sel.value;
        if (!id) return;
        fetch('api/agents/' + encodeURIComponent(id))
          .then(function(r) { return r.ok ? r.json() : null; })
          .then(function(agent) {
            if (!agent) return;
            var log = document.querySelector('.ui-agent-convo-log');
            if (!log) return;
            // Skip when real conversation has started.
            if (log.querySelector('.ui-agent-msg')) return;
            var hasIntake = Array.isArray(agent.intake_form) && agent.intake_form.length > 0;
            var desc = (agent.description || '').trim();
            // Always strip any prior separate description block —
            // covers the agent-switch case where the previous agent
            // had intake (so a block was added) and the new one
            // doesn't (so it should be removed).
            var existingBlock = log.querySelector('.ui-agency-agent-desc');
            if (existingBlock) existingBlock.remove();
            var empty = log.querySelector('.ui-agent-empty');
            if (hasIntake && desc) {
              // Intake path: add a separate block ABOVE the intake
              // form. Inserted as the first child so it lands above
              // both the empty placeholder AND the intake form.
              var block = document.createElement('div');
              block.className = 'ui-agency-agent-desc';
              block.textContent = desc;
              log.insertBefore(block, log.firstChild);
              // Hide the generic "Pick an agent from the rail…"
              // placeholder when we've put the actual description
              // above; otherwise the two appear stacked.
              if (empty) empty.style.display = 'none';
            } else if (!hasIntake) {
              // Non-intake path: textContent on the empty placeholder.
              // Also un-hide it in case a prior intake-agent switched
              // to a non-intake agent (we hid it above).
              if (empty) {
                empty.style.display = '';
                if (desc) empty.textContent = desc;
              }
            } else {
              // Intake agent without a description — just un-hide
              // the placeholder in case we'd hidden it before.
              if (empty) empty.style.display = '';
            }
          })
          .catch(function() {});
      }
      apply();
      sel.addEventListener('change', function() {
        // After the framework's own change handlers reset the
        // session/log, paint the new agent's description.
        setTimeout(apply, 100);
      });
    }
    paintAgentDescriptionPlaceholder();

    // gateMemoryButton hides the Memory toolbar button entirely when
    // the active agent has BOTH Explicit and Reference Memory disabled
    // (KB-style agents have nothing to manage — the button would
    // open a modal with no sections). When at least one layer is on,
    // the button stays visible; the modal itself further hides
    // individual sections based on the flags.
    function gateMemoryButton() {
      var sel = document.querySelector('.ui-agent-extras select');
      if (!sel) { setTimeout(gateMemoryButton, 250); return; }
      function apply() {
        var id = sel.value;
        var btn = document.querySelector('[data-action-label="Memory"]');
        if (!btn) return;
        if (!id) { btn.style.display = ''; return; }
        fetch('api/agents/' + encodeURIComponent(id))
          .then(function(r) { return r.ok ? r.json() : null; })
          .then(function(agent) {
            if (!agent) { btn.style.display = ''; return; }
            var bothOff = !!agent.disable_explicit && !!agent.disable_inferred;
            btn.style.display = bothOff ? 'none' : '';
          })
          .catch(function() { btn.style.display = ''; });
      }
      apply();
      sel.addEventListener('change', function() { setTimeout(apply, 100); });
    }
    gateMemoryButton();

    // Watch the conversation log for new interjection bubbles and
    // decorate them on arrival. Polls until the log exists because
    // the panel mounts after DOMContentLoaded.
    function watchInterjections() {
      var log = document.querySelector('.ui-agent-convo-log');
      if (!log) { setTimeout(watchInterjections, 200); return; }
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

    // --- Toolbar client actions ------------------------------------
    // Each one pivots off the active agent in the dropdown (mirror
    // of servitor's getApplianceID pattern). Navigation goes through
    // the parent /orchestrate/ prefix so we don't have to hard-code
    // it here — the chat page sits at /orchestrate/, so relative
    // URLs resolve cleanly.

    function getAgentID() {
      var sel = document.querySelector('.ui-agent-extras select');
      return sel ? sel.value : '';
    }
    function getAgentLabel() {
      var sel = document.querySelector('.ui-agent-extras select');
      if (!sel) return '';
      var opt = sel.options[sel.selectedIndex];
      return opt ? opt.text : '';
    }

    if (window.uiRegisterClientAction) {
      window.uiRegisterClientAction('orchestrate_edit_agent', function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        window.location.href = 'agent/' + encodeURIComponent(id);
      });

      // showCreateAgentModal — themed two-choice dialog that matches the
      // framework's other modals (.ui-form-modal-overlay / .ui-form-modal)
      // instead of the browser-native confirm(). Resolves with one of
      // "sub", "top", or null (cancel). Mounted on click, removed on
      // pick — no persistent DOM.
      function showCreateAgentModal(parentName, onPick) {
        var overlay = document.createElement('div');
        overlay.className = 'ui-form-modal-overlay';
        overlay.style.display = 'flex';
        var modal = document.createElement('div');
        modal.className = 'ui-form-modal';
        var header = document.createElement('div');
        header.className = 'ui-form-modal-h';
        header.textContent = 'Create new agent';
        var hint = document.createElement('div');
        hint.className = 'ui-form-modal-hint';
        hint.textContent = 'Top-level agents show on the main picker with the full surface (publishing, intake form, memory, etc.). Sub-agents are hidden specialists owned by "' + parentName + '" — they reach the chat through the parent\'s specialist picker and skip the surface a standalone agent gets.';
        var actions = document.createElement('div');
        actions.className = 'ui-form-modal-actions';
        actions.style.justifyContent = 'flex-end';
        actions.style.flexWrap = 'wrap';
        actions.style.gap = '0.45rem';
        function close(choice) {
          document.removeEventListener('keydown', onKey);
          overlay.remove();
          onPick(choice);
        }
        function onKey(e) {
          if (e.key === 'Escape') { e.preventDefault(); close(null); }
        }
        function mkBtn(label, primary, onClick) {
          var b = document.createElement('button');
          b.type = 'button';
          b.className = primary ? 'ui-form-submit' : 'ui-row-btn';
          b.textContent = label;
          // .ui-form-submit ships with margin-top: 1rem (designed for
          // sitting under a form column), which pushes it down relative
          // to .ui-row-btn siblings in this row layout. Zero it so all
          // three buttons share the actions-row baseline.
          if (primary) b.style.marginTop = '0';
          b.addEventListener('click', onClick);
          return b;
        }
        var cancelBtn = mkBtn('Cancel', false, function(){ close(null); });
        var topBtn = mkBtn('Top-level agent', false, function(){ close('top'); });
        var subBtn = mkBtn('Sub-agent of ' + parentName, true, function(){ close('sub'); });
        actions.appendChild(cancelBtn);
        actions.appendChild(topBtn);
        actions.appendChild(subBtn);
        modal.appendChild(header);
        modal.appendChild(hint);
        modal.appendChild(actions);
        overlay.appendChild(modal);
        // Click-outside closes as a cancel. Test against the overlay
        // (not the modal) so clicks inside the dialog don't dismiss.
        overlay.addEventListener('click', function(e) {
          if (e.target === overlay) close(null);
        });
        document.addEventListener('keydown', onKey);
        document.body.appendChild(overlay);
        // Focus the recommended action for keyboard-driven flows.
        subBtn.focus();
      }

      // Create-agent. When a parent agent is currently selected, ask
      // up front whether the new agent should be a top-level peer or
      // a sub-agent owned by that parent — the choice gates the form
      // layout (sub-agents mask publishing / intake form / memory
      // sections via enforceSubAgentPosture). The active parent is
      // derived the same way Edit does: prefer the option's
      // data-orch-original-value (set when a specialist is active) so
      // creating a sub-agent while in specialist mode parents the new
      // sub-agent under the TOP-LEVEL agent, not its sibling specialist.
      // No parent selected → skip the dialog and go straight to a
      // top-level form.
      window.uiRegisterClientAction('orchestrate_create_agent', function() {
        var sel = document.querySelector('.ui-agent-extras select');
        var opt = sel && sel.options[sel.selectedIndex];
        if (!opt || !opt.value) {
          window.location.href = 'agent/new';
          return;
        }
        var parentID = opt.getAttribute('data-orch-original-value') || opt.value;
        var parentName = (opt.textContent || '').trim() || 'the selected agent';
        showCreateAgentModal(parentName, function(choice) {
          if (choice === 'sub') {
            window.location.href = 'agent/new?owned_by=' + encodeURIComponent(parentID);
          } else if (choice === 'top') {
            window.location.href = 'agent/new';
          }
          // null = cancel — stay on the chat surface.
        });
      });

      window.uiRegisterClientAction('orchestrate_clone_agent', function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        // Fetch the source first so we know whether it's a sub-agent.
        // Cloning a sub-agent offers a promotion choice: keep the
        // parent link (clone stays a hidden specialist), or drop it
        // (clone becomes a first-class top-level agent — the only
        // way to surface a Builder-authored specialist on its own).
        fetch('api/agents/' + encodeURIComponent(id))
          .then(function(r) { return r.ok ? r.json() : null; })
          .then(async function(src) {
            var promote = false;
            if (src && src.owned_by) {
              // confirm() returns true on OK; phrase OK = promote
              // because that's the action the user usually wants when
              // they bothered to clone a hidden specialist.
              promote = await window.uiConfirm(
                'This is a sub-agent. Clone as a first-class top-level agent?\n\n' +
                'OK = promote to top-level (will appear in the main picker)\n' +
                'Cancel = keep as a sub-agent of the same parent (stays hidden)'
              );
            }
            return fetch('api/agents/' + encodeURIComponent(id) + '/clone', {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({promote: promote}),
            });
          })
          .then(function(r) {
            if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
            return r.json();
          })
          .then(function(rec) {
            if (rec && rec.id) {
              window.location.href = 'agent/' + encodeURIComponent(rec.id);
            } else {
              window.location.reload();
            }
          })
          .catch(function(err) {
            window.uiAlert('Clone failed: ' + (err && err.message || err));
          });
      });

      window.uiRegisterClientAction('orchestrate_export_agent', function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        // Trigger a download via an invisible link so the Content-
        // Disposition header lands without leaving the page. Direct
        // navigation works too but blanks the chat surface, which is
        // annoying — the link path keeps the user in place.
        var a = document.createElement('a');
        a.href = 'api/agents/' + encodeURIComponent(id) + '/export';
        a.style.display = 'none';
        document.body.appendChild(a);
        a.click();
        setTimeout(function() { a.remove(); }, 0);
      });

      // Save log → download the current session's full trace
      // (messages + tool calls + plans) as Markdown. The framework
      // passes the active session id as ctx.sessionId from the
      // AgentLoopPanel runtime when the toolbar action fires.
      window.uiRegisterClientAction('orchestrate_export_session', function(ctx) {
        var agentID = getAgentID();
        if (!agentID) { window.uiAlert('Pick an agent first.'); return; }
        var sessionID = (ctx && ctx.sessionId) || '';
        if (!sessionID) { window.uiAlert('Open a session first — there is nothing to export yet.'); return; }
        var url = 'api/sessions/' + encodeURIComponent(sessionID) +
                  '/export?agent_id=' + encodeURIComponent(agentID) +
                  '&format=md';
        var a = document.createElement('a');
        a.href = url;
        a.style.display = 'none';
        document.body.appendChild(a);
        a.click();
        setTimeout(function() { a.remove(); }, 0);
      });

      window.uiRegisterClientAction('orchestrate_import_agent', function() {
        var input = document.createElement('input');
        input.type = 'file';
        input.accept = '.json,application/json';
        input.style.display = 'none';
        input.addEventListener('change', function() {
          var file = input.files && input.files[0];
          input.remove();
          if (!file) return;
          var reader = new FileReader();
          reader.onload = function() {
            // The recipe is JSON — POST it raw. The server reassigns
            // ID + Owner so cross-install imports never collide.
            fetch('api/agents/import', {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body: reader.result,
            }).then(function(r) {
              if (!r.ok) return r.text().then(function(t) {
                throw new Error(t || ('HTTP ' + r.status));
              });
              return r.json();
            }).then(function(rec) {
              // Land the user in the new agent's editor so they can
              // review what was imported before chatting.
              if (rec && rec.id) {
                window.location.href = 'agent/' + encodeURIComponent(rec.id);
              } else {
                window.location.reload();
              }
            }).catch(function(err) {
              window.uiAlert('Import failed: ' + (err && err.message || err));
            });
          };
          reader.onerror = function() {
            window.uiAlert('Could not read file: ' + (reader.error && reader.error.message || 'unknown error'));
          };
          reader.readAsText(file);
        });
        document.body.appendChild(input);
        input.click();
      });

      // --- Modal infrastructure (Tools / Memory / Rules) ----------
      // Generic modal helper. Returns {overlay, body, footer, close};
      // caller fills body + footer. Esc closes; backdrop click too.
      function openOrchModal(title) {
        var overlay = document.createElement('div');
        overlay.className = 'ui-orch-modal-overlay';
        var modal = document.createElement('div');
        modal.className = 'ui-orch-modal';
        overlay.appendChild(modal);

        var header = document.createElement('div');
        header.className = 'ui-orch-modal-h';
        var hTitle = document.createElement('div');
        hTitle.className = 'ui-orch-modal-h-title';
        hTitle.textContent = title;
        var hClose = document.createElement('button');
        hClose.className = 'ui-orch-modal-h-close';
        hClose.type = 'button';
        hClose.textContent = '×';
        header.appendChild(hTitle);
        header.appendChild(hClose);
        modal.appendChild(header);

        var body = document.createElement('div');
        body.className = 'ui-orch-modal-body';
        modal.appendChild(body);

        var footer = document.createElement('div');
        footer.className = 'ui-orch-modal-ftr';
        modal.appendChild(footer);

        function close() {
          overlay.remove();
          document.removeEventListener('keydown', onKey);
        }
        function onKey(ev) { if (ev.key === 'Escape') close(); }
        hClose.addEventListener('click', close);
        overlay.addEventListener('click', function(ev) {
          if (ev.target === overlay) close();
        });
        document.addEventListener('keydown', onKey);

        document.body.appendChild(overlay);
        return {overlay: overlay, body: body, footer: footer, close: close};
      }

      // Helper: GET the agent's current record from /api/agents/{id}.
      // Both modals fetch + modify + POST back so concurrent saves
      // from other surfaces don't get clobbered by stale data.
      function fetchAgent(id) {
        return fetch('api/agents/' + encodeURIComponent(id))
          .then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.json();
          });
      }

      // renderAllowlistPicker — generic checkbox list of items, with
      // checked state mirrored from agent[agentField] and toggles
      // committing immediately via POST /api/agents merge. Used by
      // the Knowledge modal (collections) and Skills modal (skills).
      // Each item is expected to have {id, name, description?}.
      //
      // opts:
      //   host: container element to render into
      //   listURL: GET endpoint returning either an array or an
      //            object with an "items" / records key — auto-detected
      //   agentField: agent record key to merge (e.g. "attached_collections")
      //   agentID: the active agent's ID
      //   emptyText: shown when listURL returns no items
      //   nameField / descField: optional, default "name" / "description"
      function renderAllowlistPicker(opts) {
        var host = opts.host;
        var nameField = opts.nameField || 'name';
        var descField = opts.descField || 'description';
        host.innerHTML = '';
        var loading = document.createElement('div');
        loading.style.cssText = 'font-size:0.78rem;color:var(--text-mute);font-style:italic';
        loading.textContent = 'Loading…';
        host.appendChild(loading);

        // Pull the items list AND the agent's current state in parallel.
        // Race is fine — neither write back, both feed the render below.
        Promise.all([
          fetch(opts.listURL).then(function(r){ return r.ok ? r.json() : null; }),
          fetchAgent(opts.agentID)
        ]).then(function(results) {
          host.innerHTML = '';
          var raw = results[0];
          var agent = results[1] || {};
          // Auto-detect the items array. Top-level array first, then
          // the common shaped-response keys. Endpoints in this app
          // use a mix: collections returns {collections: [...]};
          // skills returns {skills: [...]}. If a new endpoint uses
          // yet another key, add it here OR pass opts.recordsField
          // for explicit override.
          var items = Array.isArray(raw) ? raw
            : (opts.recordsField && raw && Array.isArray(raw[opts.recordsField])) ? raw[opts.recordsField]
            : (raw && Array.isArray(raw.items)) ? raw.items
            : (raw && Array.isArray(raw.records)) ? raw.records
            : (raw && Array.isArray(raw.sources)) ? raw.sources
            : (raw && Array.isArray(raw.collections)) ? raw.collections
            : (raw && Array.isArray(raw.skills)) ? raw.skills
            : (raw && Array.isArray(raw.pipelines)) ? raw.pipelines
            : [];
          if (items.length === 0) {
            var emp = document.createElement('div');
            emp.style.cssText = 'font-size:0.78rem;color:var(--text-mute);font-style:italic';
            emp.textContent = opts.emptyText || '(nothing to show)';
            host.appendChild(emp);
            return;
          }
          var checked = {};
          (agent[opts.agentField] || []).forEach(function(id){ checked[id] = true; });
          items.forEach(function(item) {
            // Skip items the user explicitly disabled (skills carry a
            // disabled flag — hide from the picker so the user isn't
            // tempted to allowlist something muted).
            if (item.disabled) return;
            var row = document.createElement('label');
            row.style.cssText = 'display:flex;align-items:flex-start;gap:0.55rem;padding:0.35rem 0.6rem;background:var(--bg-0);border:1px solid var(--border);border-radius:4px;cursor:pointer';
            var box = document.createElement('input');
            box.type = 'checkbox';
            box.value = item.id;
            box.style.marginTop = '0.15rem';
            if (checked[item.id]) box.checked = true;
            var meta = document.createElement('div');
            meta.style.cssText = 'flex:1;min-width:0';
            var nm = document.createElement('div');
            nm.style.cssText = 'font-size:0.85rem;color:var(--text);font-weight:500';
            nm.textContent = item[nameField] || item.id;
            meta.appendChild(nm);
            var desc = (item[descField] || '').trim();
            if (desc) {
              var d = document.createElement('div');
              d.style.cssText = 'font-size:0.74rem;color:var(--text-mute);line-height:1.4;margin-top:0.15rem';
              if (desc.length > 200) desc = desc.slice(0, 200) + '…';
              d.textContent = desc;
              meta.appendChild(d);
            }
            row.appendChild(box);
            row.appendChild(meta);
            host.appendChild(row);
            box.addEventListener('change', function(){
              if (box.checked) {
                checked[item.id] = true;
              } else {
                delete checked[item.id];
              }
              var picked = Object.keys(checked);
              // POST /api/agents is a FULL-record replace, not a merge
              // (that's only the LLM-side update_agent tool). Send the
              // full agent record with the one field patched in. We
              // already have the record in the closure from the initial
              // fetch; mutate the field and ship the whole thing.
              agent[opts.agentField] = picked;
              box.disabled = true;
              fetch('api/agents', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(agent)
              })
                .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t); }); box.disabled = false; })
                .catch(function(err){
                  window.uiAlert('Save failed: ' + (err && err.message || err));
                  // Roll back the visual state since the save didn't
                  // stick. Without this the UI lies until next refresh.
                  box.checked = !box.checked;
                  if (box.checked) checked[item.id] = true; else delete checked[item.id];
                  agent[opts.agentField] = Object.keys(checked);
                  box.disabled = false;
                });
            });
          });
        }).catch(function(err){
          host.innerHTML = '';
          var fail = document.createElement('div');
          fail.style.cssText = 'font-size:0.78rem;color:var(--danger,#ff7b72)';
          fail.textContent = 'Failed to load: ' + (err && err.message || err);
          host.appendChild(fail);
        });
      }

      window.uiRegisterClientAction('orchestrate_tools_modal', function(ctx) {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        // Pull the active session id from the framework's ctx so the
        // modal can list and act on session-scoped temp tools. Empty
        // when nothing's open yet — the section just doesn't render.
        var activeSid = (ctx && ctx.sessionId) || '';
        // buildToolDetail renders the expandable "see everything"
        // panel for a TempTool record — mode-specific full template,
        // pipeline body, params, etc. Shared between the agent-
        // bundled section and the session-drafts section so the
        // visual language is identical wherever a tool is listed.
        function buildToolDetail(t) {
          var d = document.createElement('div');
          d.className = 'ui-orch-tool-detail';
          d.style.cssText = 'display:none;margin:0.4rem 0 0.6rem 1.8rem;padding:0.5rem 0.7rem;background:var(--bg-0);border:1px solid var(--border);border-radius:4px;font-size:0.78rem;line-height:1.45';
          function row(label, value, mono) {
            if (value == null || value === '' ||
                (Array.isArray(value) && value.length === 0) ||
                (typeof value === 'object' && !Array.isArray(value) && Object.keys(value).length === 0)) {
              return;
            }
            var r = document.createElement('div');
            r.style.margin = '0.25rem 0';
            var lbl = document.createElement('span');
            lbl.style.cssText = 'color:var(--text-mute);text-transform:uppercase;letter-spacing:0.04em;font-size:0.7rem;display:inline-block;min-width:9rem;vertical-align:top';
            lbl.textContent = label;
            r.appendChild(lbl);
            var val = document.createElement('span');
            if (mono) val.style.fontFamily = 'var(--mono,monospace)';
            val.style.cssText += ';white-space:pre-wrap;word-break:break-word';
            if (typeof value === 'object') {
              val.textContent = JSON.stringify(value, null, 2);
              val.style.fontFamily = 'var(--mono,monospace)';
              val.style.display = 'block';
              val.style.marginTop = '0.2rem';
              val.style.padding = '0.3rem 0.5rem';
              val.style.background = 'var(--bg-1)';
              val.style.borderLeft = '2px solid var(--border)';
            } else {
              val.textContent = String(value);
            }
            r.appendChild(val);
            d.appendChild(r);
          }
          var mode = t.mode || 'shell';
          row('Mode', mode);
          row('Description', t.description);
          row('Params', t.params);
          row('Required', (t.required || []).join(', '));
          if (mode === 'shell' || mode === '' || mode == null) {
            row('Command template', t.command_template, true);
            if (t.script_body) row('Script body', t.script_body, true);
            if (t.script_name) row('Script name', t.script_name, true);
            if (t.state_path) row('State path', t.state_path, true);
          } else if (mode === 'api') {
            row('Method', (t.method || 'GET'), true);
            row('URL template', t.command_template, true);
            row('Credential', t.credential, true);
            row('Body template', t.body_template, true);
            row('Response pipe', t.response_pipe, true);
          } else if (mode === 'pipeline') {
            row('Inner tools', (t.pipeline_tools || []).join(', '));
            if (t.pipeline_prompt) {
              row('Pipeline prompt', t.pipeline_prompt);
            }
            if (t.pipeline_steps && t.pipeline_steps.length > 0) {
              row('Steps', t.pipeline_steps);
            }
            if (t.pipeline_max_rounds) {
              row('Max rounds', t.pipeline_max_rounds, true);
            }
          }
          return d;
        }
        // attachExpandToggle wires a ▾/▸ button into the row's actions
        // area that toggles the detail block visibility. Idempotent —
        // each row gets its own button + detail pair.
        function attachExpandToggle(actionsContainer, detailEl) {
          var btn = document.createElement('button');
          btn.type = 'button';
          btn.className = 'ui-row-btn';
          btn.style.cssText = 'min-width:2rem;padding:0.15rem 0.4rem';
          btn.textContent = '▸';
          btn.title = 'Show full definition';
          btn.addEventListener('click', function() {
            var open = detailEl.style.display !== 'none';
            detailEl.style.display = open ? 'none' : 'block';
            btn.textContent = open ? '▸' : '▾';
          });
          actionsContainer.appendChild(btn);
        }
        fetchAgent(id).then(function(agent) {
          var catalog = window.ORCH_TOOL_CATALOG || [];
          // Builder's catalog is fixed by the framework (builderAuthoringTools
          // / builderAuthoringTools) — the allowed_tools checklist has no
          // effect. Hide it so the admin isn't presented with a control that
          // appears actionable but isn't. The Session tools panel below IS
          // what's useful here (pruning drafts authored by Builder's workers).
          var isBuilder = (id === 'seed-builder');
          // allowed_tools state model (non-Builder agents):
          //   []                  → "default pool" (every catalog tool, future ones auto-include)
          //   ["__none__"]        → "explicitly no optional tools" (sentinel; checklist renders all unchecked)
          //   [name1, name2, ...] → exactly these names
          //
          // Save handler maps the modal state back:
          //   all checked   → []           (preserves the default-pool semantic)
          //   none checked  → ["__none__"] (preserves explicit-no-tools)
          //   some checked  → [name, ...]  (literal list)
          var allowed = {};
          var listed = agent.allowed_tools || [];
          var isNoneSentinel = listed.length === 1 && listed[0] === '__none__';
          if (isNoneSentinel) {
            // Sentinel — render with NOTHING checked.
          } else if (listed.length === 0) {
            catalog.forEach(function(o) { allowed[o.value] = true; });
          } else {
            listed.forEach(function(n) { allowed[n] = true; });
          }
          var m = openOrchModal('Tools — ' + (agent.name || id));
          var help = document.createElement('div');
          help.className = 'ui-orch-modal-help';
          if (isBuilder) {
            help.textContent = "Builder's tool catalog is fixed by the framework — the per-agent checklist doesn't apply here. Use the Session tools panel below to review and prune the drafts Builder's workers authored mid-conversation.";
          } else {
            help.textContent = "Pick which tools this agent's worker may call. Uncheck everything to disable ALL optional tools (the agent will be left with only framework-blessed tools like plan_set / respond_directly).";
          }
          m.body.appendChild(help);

          // Custom-tools section — authored specifically for this agent
          // (the Tools field on the record). Read-only here; edits
          // happen via Builder. Renders nothing when the agent has no
          // bundled tools so the modal stays uncluttered.
          // Hide any agent-bundled tool whose name is already in the
          // global catalog — that means the same tool exists in the
          // user-wide persistent pool, so the agent.Tools copy is just
          // redundant. (Old records can still have a duplicate from
          // before the global-persist strip was added; this filters
          // them out of the UI cleanly.)
          var catalogNames = {};
          catalog.forEach(function(o){ catalogNames[o.value] = true; });
          var customTools = (agent.tools || []).filter(function(t){
            return !catalogNames[t.name];
          });
          if (customTools.length > 0) {
            var customSection = document.createElement('div');
            customSection.className = 'ui-orch-custom-tools';
            var ch = document.createElement('div');
            ch.className = 'ui-orch-custom-tools-h';
            ch.textContent = 'Custom tools bundled with this agent (' + customTools.length + ')';
            customSection.appendChild(ch);
            var ci = document.createElement('div');
            ci.className = 'ui-orch-custom-tools-help';
            ci.textContent = 'Authored for this agent specifically. Edit via Builder.';
            customSection.appendChild(ci);
            customTools.forEach(function(t) {
              var row = document.createElement('div');
              row.className = 'ui-orch-custom-tool';
              var icon = document.createElement('span');
              icon.className = 'ui-orch-custom-tool-icon';
              var mode = (t.mode || 'shell');
              icon.textContent = (mode === 'pipeline') ? '⚙' : (mode === 'api') ? '🌐' : '🐚';
              icon.title = 'mode: ' + mode;
              row.appendChild(icon);
              var meta = document.createElement('div');
              meta.className = 'ui-orch-custom-tool-meta';
              var nm = document.createElement('div');
              nm.className = 'ui-orch-custom-tool-name';
              nm.textContent = t.name || '(unnamed)';
              meta.appendChild(nm);
              if (t.description) {
                var desc = document.createElement('div');
                desc.className = 'ui-orch-custom-tool-desc';
                desc.textContent = t.description;
                meta.appendChild(desc);
              }
              // Mode-specific brief summary so the user can sanity-check
              // at a glance without expanding into JSON.
              var summary = '';
              if (mode === 'shell' && t.command_template) {
                summary = '$ ' + t.command_template;
              } else if (mode === 'api' && t.command_template) {
                summary = (t.method || 'GET') + ' ' + t.command_template;
              } else if (mode === 'pipeline') {
                var steps = (t.pipeline_steps || []);
                if (steps.length > 0) {
                  summary = steps.map(function(s){ return s.tool; }).join(' → ');
                } else if (t.pipeline_prompt) {
                  summary = '(adaptive pipeline)';
                }
              }
              if (summary) {
                var sm = document.createElement('div');
                sm.className = 'ui-orch-custom-tool-summary';
                if (summary.length > 140) summary = summary.slice(0, 140) + '…';
                sm.textContent = summary;
                meta.appendChild(sm);
              }
              row.appendChild(meta);
              // Expand toggle → reveals the full tool definition below
              // the row. Read-only here; edits flow through Builder.
              var rowActions = document.createElement('div');
              rowActions.style.cssText = 'display:flex;align-items:center;gap:0.4rem;margin-left:auto';
              var detailEl = buildToolDetail(t);
              attachExpandToggle(rowActions, detailEl);
              // Remove button — available on EVERY agent. Previously
              // gated to seed agents only ("user-authored agents'
              // bundles are a real authoring decision"), but in
              // practice that paternalism breaks the common case:
              // when an LLM re-authors a bundled tool, the new
              // version sits in the session draft / pending pool
              // while the OLD agent.Tools[] snapshot keeps running
              // at dispatch (the load order prefers persistent →
              // agent.Tools → drafts, and a draft can't override an
              // existing agent.Tools entry by AppendTempTool's
              // collision semantics). Operator needs an unbundle
              // path to clear the stale snapshot so the new version
              // can take over. Same path for seeds + user-authored.
              if (true) {
                var rm = document.createElement('button');
                rm.className = 'ui-row-btn';
                rm.style.cssText = 'color:var(--danger,#ff7b72);font-size:0.78rem;padding:0.25rem 0.55rem';
                rm.textContent = 'Remove';
                rm.title = 'Unbundle this tool from the agent (seed agents only)';
                rm.addEventListener('click', async function(){
                  if (!(await window.uiConfirm('Unbundle ' + (t.name || 'this tool') + ' from this agent?'))) return;
                  rm.disabled = true;
                  row.style.opacity = '0.4'; // optimistic: visually dim, restore on fail
                  var trimmed = (agent.tools || []).filter(function(x){ return x.name !== t.name; });
                  var patched = Object.assign({}, agent, {tools: trimmed});
                  fetch('api/agents', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify(patched)
                  }).then(function(r){
                    if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
                    // Hide the row + its expand panel. Mutate the
                    // local agent.tools too so subsequent modal opens
                    // reflect the change without a full reload.
                    row.remove();
                    detailEl.remove();
                    agent.tools = trimmed;
                  }).catch(function(err){
                    window.uiAlert('Remove failed: ' + (err && err.message || err));
                    row.style.opacity = '';
                    rm.disabled = false;
                  });
                });
                rowActions.appendChild(rm);
              }
              row.appendChild(rowActions);
              customSection.appendChild(row);
              customSection.appendChild(detailEl);
            });
            m.body.appendChild(customSection);
          }

          // Session tools — drafts the LLM authored mid-conversation
          // via tool_def / add_tool. Distinct from custom-tools
          // (agent-bundled, persistent) and the global catalog. Each
          // row gets:
          //   - Persist [Global ☐] → admin-promote out of the draft
          //     pool; Global=true lands in user-wide persistent
          //     pool, Global=false attaches to the focused agent.
          //   - Drop → remove from the draft pool.
          // Section renders UNCONDITIONALLY (so the user always knows
          // it's a panel that exists), with state-aware copy when
          // there's no active session or nothing's been authored yet.
          {
            var sec = document.createElement('div');
            sec.className = 'ui-orch-custom-tools';
            var sh = document.createElement('div');
            sh.className = 'ui-orch-custom-tools-h';
            sh.textContent = 'Session tools (this conversation)';
            sec.appendChild(sh);
            var si = document.createElement('div');
            si.className = 'ui-orch-custom-tools-help';
            si.textContent = 'Drafts the LLM authored in this conversation. Persist to keep them past the session, or Drop to clear.';
            sec.appendChild(si);
            var sessLoading = document.createElement('div');
            sessLoading.className = 'ui-orch-custom-tools-help';
            sessLoading.style.fontStyle = 'italic';
            sessLoading.textContent = activeSid ? 'Loading…' : 'No active session yet — authored tools will appear here once a session is open.';
            sec.appendChild(sessLoading);
            // Insert the empty shell early so layout doesn't reshuffle
            // when the fetch resolves; the fetch then fills it in. At
            // this point in the synchronous build, the global-catalog
            // list hasn't been appended yet — so appendChild parks the
            // shell right after the bundled-custom-tools section and
            // above the catalog (which lands next).
            m.body.appendChild(sec);
            if (!activeSid) {
              // No session — leave the placeholder copy in place and
              // skip the fetch entirely. The section stays visible so
              // the user knows where the surface lives.
            } else
            fetch('api/sessions/' + encodeURIComponent(activeSid) +
                  '/tools?agent_id=' + encodeURIComponent(id))
              .then(function(r) {
                if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
                return r.json();
              })
              .then(function(sessTools) {
                sessLoading.remove();
                if (!Array.isArray(sessTools) || sessTools.length === 0) {
                  var empty = document.createElement('div');
                  empty.className = 'ui-orch-custom-tools-help';
                  empty.style.fontStyle = 'italic';
                  empty.textContent = '(No session tools have been authored yet. The LLM authors via tool_def or add_tool.)';
                  sec.appendChild(empty);
                  return;
                }
                sh.textContent = 'Session tools (this conversation, ' + sessTools.length + ')';
                sessTools.forEach(function(t) {
                  var row = document.createElement('div');
                  row.className = 'ui-orch-custom-tool';
                  var icon = document.createElement('span');
                  icon.className = 'ui-orch-custom-tool-icon';
                  var mode = (t.mode || 'shell');
                  icon.textContent = (mode === 'pipeline') ? '⚙' : (mode === 'api') ? '🌐' : '🐚';
                  icon.title = 'mode: ' + mode;
                  row.appendChild(icon);
                  var meta = document.createElement('div');
                  meta.className = 'ui-orch-custom-tool-meta';
                  var nm = document.createElement('div');
                  nm.className = 'ui-orch-custom-tool-name';
                  nm.textContent = t.name || '(unnamed)';
                  meta.appendChild(nm);
                  if (t.description) {
                    var desc = document.createElement('div');
                    desc.className = 'ui-orch-custom-tool-desc';
                    desc.textContent = t.description;
                    meta.appendChild(desc);
                  }
                  var summary = '';
                  if (mode === 'shell' && t.command_template) {
                    summary = '$ ' + t.command_template;
                  } else if (mode === 'api' && t.command_template) {
                    summary = (t.method || 'GET') + ' ' + t.command_template;
                  } else if (mode === 'pipeline') {
                    var steps = (t.pipeline_steps || []);
                    if (steps.length > 0) {
                      summary = steps.map(function(s){ return s.tool; }).join(' → ');
                    } else if (t.pipeline_prompt) {
                      summary = '(adaptive pipeline)';
                    }
                  }
                  if (summary) {
                    var sm = document.createElement('div');
                    sm.className = 'ui-orch-custom-tool-summary';
                    if (summary.length > 140) summary = summary.slice(0, 140) + '…';
                    sm.textContent = summary;
                    meta.appendChild(sm);
                  }
                  row.appendChild(meta);
                  // Action column — Global checkbox + Persist + Drop.
                  var actions = document.createElement('div');
                  actions.className = 'ui-orch-session-tool-actions';
                  actions.style.cssText = 'display:flex;align-items:center;gap:0.4rem;margin-left:auto';
                  var globalLbl = document.createElement('label');
                  globalLbl.style.cssText = 'display:flex;align-items:center;gap:0.25rem;color:var(--text-mute);font-size:0.78rem;cursor:pointer';
                  var globalCb = document.createElement('input');
                  globalCb.type = 'checkbox';
                  globalCb.title = 'When checked, persist to your user-wide tool pool (available to every agent). When unchecked, attach to this agent only.';
                  globalLbl.appendChild(globalCb);
                  globalLbl.appendChild(document.createTextNode('Global'));
                  var persistBtn = document.createElement('button');
                  persistBtn.type = 'button';
                  persistBtn.className = 'ui-row-btn';
                  persistBtn.textContent = 'Persist';
                  var dropBtn = document.createElement('button');
                  dropBtn.type = 'button';
                  dropBtn.className = 'ui-row-btn';
                  dropBtn.style.cssText = 'color:var(--danger,#c0392b)';
                  dropBtn.textContent = 'Drop';
                  function doAction(action, extras) {
                    persistBtn.disabled = true; dropBtn.disabled = true;
                    var qs = 'action=' + action +
                             '&agent_id=' + encodeURIComponent(id);
                    if (extras && extras.global) qs += '&global=true';
                    fetch('api/sessions/' + encodeURIComponent(activeSid) +
                          '/tools/' + encodeURIComponent(t.name) + '?' + qs,
                          { method: 'POST' })
                      .then(function(r) {
                        if (!r.ok) return r.text().then(function(s) { throw new Error(s || r.statusText); });
                        // Visually mark the row + fade it out so the
                        // modal reads the outcome without a re-fetch.
                        row.style.opacity = '0.5';
                        if (action === 'persist') {
                          var done = document.createElement('div');
                          done.style.cssText = 'color:var(--accent);font-size:0.78rem;margin-left:0.4rem';
                          done.textContent = (extras && extras.global) ? 'Persisted (global)' : 'Attached to agent';
                          actions.innerHTML = '';
                          actions.appendChild(done);
                        } else {
                          actions.innerHTML = '<span style="color:var(--text-mute);font-size:0.78rem">Dropped</span>';
                        }
                      })
                      .catch(function(err) {
                        persistBtn.disabled = false; dropBtn.disabled = false;
                        window.uiAlert(action + ' failed: ' + (err && err.message || err));
                      });
                  }
                  persistBtn.addEventListener('click', function() {
                    doAction('persist', { global: globalCb.checked });
                  });
                  dropBtn.addEventListener('click', async function() {
                    if (!(await window.uiConfirm('Drop session tool ' + t.name + '?'))) return;
                    doAction('drop', null);
                  });
                  // Expand toggle → reveals the full tool definition.
                  // Sits alongside the Persist/Drop actions so the
                  // admin can audit before promoting.
                  var sessDetail = buildToolDetail(t);
                  attachExpandToggle(actions, sessDetail);
                  actions.appendChild(globalLbl);
                  actions.appendChild(persistBtn);
                  actions.appendChild(dropBtn);
                  row.appendChild(actions);
                  sec.appendChild(row);
                  sec.appendChild(sessDetail);
                });
                // Shell was already inserted above; rows just landed
                // inside it. No further DOM movement needed.
              })
              .catch(function(err) {
                sessLoading.remove();
                var errRow = document.createElement('div');
                errRow.className = 'ui-orch-custom-tools-help';
                errRow.style.color = 'var(--danger,#c0392b)';
                errRow.textContent = 'Could not load session tools: ' + (err && err.message || err);
                sec.appendChild(errRow);
                if (window.console) console.warn('session-tools fetch failed:', err);
              });
          }

          var list = document.createElement('div');
          list.className = 'ui-checklist';
          var checkboxes = [];
          // Skip the catalog checklist entirely for Builder — its
          // tool set is code-driven, the checklist has no effect, and
          // showing it next to the actionable Session tools panel
          // misleads the admin into thinking they have a knob there.
          // Custom-tools (agent-bundled) + Session-tools sections
          // already rendered above this point are the actionable
          // surfaces for Builder.
          if (isBuilder) {
            // Skip group-building entirely. Fall through to footer.
          } else {
          // Bucket the tools by group so each section can be rendered
          // as a collapsible block with its own master checkbox. Order
          // of first-occurrence is preserved (server-side sort already
          // clusters groups together; we just consume them in order).
          var groups = []; // [{name, items: [option, ...]}]
          var groupIdx = {};
          catalog.forEach(function(o) {
            var grp = o.group || '';
            if (!(grp in groupIdx)) {
              groupIdx[grp] = groups.length;
              groups.push({name: grp, items: []});
            }
            groups[groupIdx[grp]].items.push(o);
          });
          groups.forEach(function(g) {
            var section = document.createElement('div');
            section.className = 'ui-checklist-section';
            var header = document.createElement('div');
            header.className = 'ui-checklist-group ui-checklist-group-collapsible';
            // Caret toggles collapsed state. Section starts collapsed
            // so the modal isn't a wall of checkboxes on open.
            var caret = document.createElement('span');
            caret.className = 'ui-checklist-caret';
            caret.textContent = '▸';
            header.appendChild(caret);
            // Master checkbox: toggles every child checkbox in this
            // group. Tri-state: unchecked / indeterminate / checked
            // reflect the children's combined state. Click toggles
            // toward all-checked if any are unchecked, else all-off.
            var master = document.createElement('input');
            master.type = 'checkbox';
            master.className = 'ui-checklist-cb ui-checklist-master';
            master.addEventListener('click', function(ev) {
              ev.stopPropagation();
              var want = master.checked; // browser already flipped it
              g.items.forEach(function(o, idx) {
                var cb = g._cbs[idx];
                cb.checked = want;
              });
              master.indeterminate = false;
            });
            header.appendChild(master);
            var label = document.createElement('span');
            label.className = 'ui-checklist-group-name';
            label.textContent = g.name || '(ungrouped)';
            header.appendChild(label);
            section.appendChild(header);

            var body = document.createElement('div');
            body.className = 'ui-checklist-section-body';
            body.style.display = 'none'; // collapsed by default
            g._cbs = [];
            g.items.forEach(function(o) {
              var row = document.createElement('label');
              row.className = 'ui-checklist-row';
              var cb = document.createElement('input');
              cb.type = 'checkbox';
              cb.className = 'ui-checklist-cb';
              cb.value = o.value;
              cb.checked = !!allowed[o.value];
              cb.addEventListener('change', function() {
                refreshMaster(g, master);
              });
              row.appendChild(cb);
              var lbl = document.createElement('div');
              lbl.className = 'ui-checklist-lbl';
              var nm = document.createElement('span');
              nm.className = 'ui-checklist-name';
              nm.textContent = o.label || o.value;
              lbl.appendChild(nm);
              if (o.help) {
                var hp = document.createElement('span');
                hp.className = 'ui-checklist-help';
                hp.textContent = o.help;
                lbl.appendChild(hp);
              }
              row.appendChild(lbl);
              body.appendChild(row);
              checkboxes.push(cb);
              g._cbs.push(cb);
            });
            // Click anywhere on the header (except the master checkbox)
            // toggles collapsed state. Caret rotates as a visual cue.
            header.addEventListener('click', function(ev) {
              if (ev.target === master) return;
              var open = body.style.display !== 'none';
              body.style.display = open ? 'none' : '';
              caret.textContent = open ? '▸' : '▾';
            });
            section.appendChild(body);
            list.appendChild(section);
            refreshMaster(g, master);
          });
          function refreshMaster(g, master) {
            var on = 0;
            g._cbs.forEach(function(cb) { if (cb.checked) on++; });
            if (on === 0) {
              master.checked = false;
              master.indeterminate = false;
            } else if (on === g._cbs.length) {
              master.checked = true;
              master.indeterminate = false;
            } else {
              master.checked = false;
              master.indeterminate = true;
            }
          }
          m.body.appendChild(list);
          } // end !isBuilder catalog block
          var cancelBtn = document.createElement('button');
          cancelBtn.className = 'ui-orch-modal-btn';
          cancelBtn.textContent = isBuilder ? 'Close' : 'Cancel';
          cancelBtn.onclick = m.close;
          m.footer.appendChild(cancelBtn);
          if (!isBuilder) {
            var saveBtn = document.createElement('button');
            saveBtn.className = 'ui-orch-modal-btn primary';
            saveBtn.textContent = 'Save';
            saveBtn.onclick = function() {
              var picked = [];
              checkboxes.forEach(function(cb) { if (cb.checked) picked.push(cb.value); });
              // "All checked"  → empty list (default pool, future tools auto-include).
              // "None checked" → ["__none__"] sentinel (explicit "no optional tools").
              //                  Bare empty list would round-trip to default pool which
              //                  silently undoes the user's intent.
              // "Some checked" → literal name list.
              if (picked.length === checkboxes.length) {
                picked = [];
              } else if (picked.length === 0) {
                picked = ['__none__'];
              }
              agent.allowed_tools = picked;
              saveBtn.disabled = true;
              fetch('api/agents', {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(agent),
              }).then(function(r) {
                if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
                m.close();
                refreshAgentCounts();
              }).catch(function(err) {
                saveBtn.disabled = false;
                window.uiAlert('Save failed: ' + (err && err.message || err));
              });
            };
            m.footer.appendChild(saveBtn);
          }
        }).catch(function(err) {
          window.uiAlert('Failed to load agent: ' + (err && err.message || err));
        });
      });

      window.uiRegisterClientAction('orchestrate_memory_modal', function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        // Memory modal now shows two sections:
        //   1. Explicit Memory (always-in-prompt facts via store_fact).
        //      The block header / intro come from the agent's MemoryMode
        //      ("Lessons learned" for agent-mode, "Saved notes" for
        //      chatbot-mode). Editable list; save POSTs to /facts which
        //      replaces the underlying MemoryFact rows.
        //   2. Reference Memory (vector-grown derived chunks via
        //      memory_save / synthesis ingest). Read-only list with
        //      delete-per-entry. Lets users prune drift without editing
        //      embeddings in place (edits don't make sense for vectors).
        // The old AgentMemory.Notes section (auto-consolidator output)
        // is gone — Memory is now 100% LLM-driven.
        var factsURL = 'api/agents/' + encodeURIComponent(id) + '/facts';
        var agentURL = 'api/agents/' + encodeURIComponent(id);
        var m = openOrchModal('Memory');
        // Section visibility gates — flipped after the agent record
        // loads. Sections always build in the DOM (simpler than
        // conditional construction) but hide when the corresponding
        // disable flag is set on the agent.
        var disabledNotice = document.createElement('div');
        disabledNotice.style.cssText = 'color:var(--text-mute);font-style:italic;padding:1rem 0;text-align:center;display:none';
        disabledNotice.textContent = 'Both Explicit and Reference Memory are disabled for this agent — nothing to manage here.';
        m.body.appendChild(disabledNotice);
        fetch(agentURL).then(function(r){ return r.ok ? r.json() : null; }).then(function(a) {
          if (!a) return;
          if (a.disable_explicit) factsSection.style.display = 'none';
          if (a.disable_inferred) inferredSection.style.display = 'none';
          if (a.disable_explicit && a.disable_inferred) {
            disabledNotice.style.display = '';
          }
        }).catch(function(){});
        // Collapsible section helper — wraps a clickable title with a
        // caret and hides the listed content elements until clicked.
        // Both memory sections (facts + reference) use this so the
        // modal opens compact (just two title rows) and the user picks
        // which one to drill into, rather than scrolling through every
        // entry of both at once.
        function makeCollapsible(titleEl, contentEls, startExpanded) {
          titleEl.style.cursor = 'pointer';
          titleEl.style.userSelect = 'none';
          var caret = document.createElement('span');
          caret.style.cssText = 'margin-right:0.4rem;display:inline-block;color:var(--text-mute);transition:transform 0.15s';
          caret.textContent = String.fromCharCode(9656); // ▸
          titleEl.insertBefore(caret, titleEl.firstChild);
          var open = !!startExpanded;
          function apply() {
            caret.style.transform = open ? 'rotate(90deg)' : '';
            contentEls.forEach(function(el) { if (el) el.style.display = open ? '' : 'none'; });
          }
          titleEl.addEventListener('click', function(ev) {
            ev.stopPropagation();
            open = !open;
            apply();
          });
          apply();
        }

        var facts = [];
        var factsSection = document.createElement('div');
        var factsTitle = document.createElement('div');
        factsTitle.style.fontWeight = '600';
        factsTitle.style.color = 'var(--text)';
        factsTitle.style.marginBottom = '0.3rem';
        factsTitle.textContent = 'Saved facts';
        factsSection.appendChild(factsTitle);
        var factsHelp = document.createElement('div');
        factsHelp.className = 'ui-orch-modal-help';
        factsHelp.style.marginBottom = '0.5rem';
        factsHelp.textContent = 'Explicit Memory — short entries always injected into the system prompt. Loaded from the agent’s store_fact entries.';
        factsSection.appendChild(factsHelp);
        var factsList = document.createElement('div');
        factsList.className = 'ui-orch-list';
        factsSection.appendChild(factsList);
        m.body.appendChild(factsSection);

        function renderFacts() {
          factsList.innerHTML = '';
          facts.forEach(function(text, idx) {
            var row = document.createElement('div');
            row.className = 'ui-orch-list-row';
            var ta = document.createElement('textarea');
            ta.rows = 1;
            ta.value = text;
            ta.addEventListener('input', function() {
              facts[idx] = ta.value;
            });
            var del = document.createElement('button');
            del.className = 'ui-orch-list-del';
            del.type = 'button';
            del.textContent = String.fromCharCode(215);
            del.onclick = function() {
              facts.splice(idx, 1);
              renderFacts();
            };
            row.appendChild(ta);
            row.appendChild(del);
            factsList.appendChild(row);
          });
          var addBtn = document.createElement('button');
          addBtn.type = 'button';
          addBtn.className = 'ui-orch-list-add';
          addBtn.textContent = '+ Add';
          addBtn.onclick = function() {
            facts.push('');
            renderFacts();
            var rows = factsList.querySelectorAll('textarea');
            var last = rows[rows.length - 1];
            if (last) last.focus();
          };
          factsList.appendChild(addBtn);
        }
        renderFacts();

        fetch(factsURL).then(function(r){ return r.ok ? r.json() : null; })
          .then(function(d){
            if (!d) return;
            facts = (d.notes || []).slice();
            var fr = d.framing || {};
            if (fr.block_header) {
              // Strip the leading "## " markdown so the modal title
              // renders as plain text styled by our CSS.
              factsTitle.textContent = fr.block_header.replace(/^#+\s*/, '');
            }
            if (fr.block_intro) {
              factsHelp.textContent = fr.block_intro;
            }
            renderFacts();
          }).catch(function(){ /* leave empty section on failure */ });

        // --- Reference Memory section ---
        // Vector-grown derived chunks (memory_save findings, synthesis
        // auto-ingest). Read-only list with delete-per-entry — editing
        // vector embeddings in place doesn’t make sense, so the
        // affordance is "prune drift" not "rewrite."
        var inferredURL = 'api/agents/' + encodeURIComponent(id) + '/inferred';
        var inferredWipeURL = 'api/agents/' + encodeURIComponent(id) + '/knowledge/auto-inferred';
        var inferredSection = document.createElement('div');
        inferredSection.style.marginTop = '1.2rem';
        inferredSection.style.paddingTop = '0.8rem';
        inferredSection.style.borderTop = '1px solid var(--border)';
        var inferredHeader = document.createElement('div');
        inferredHeader.style.cssText = 'display:flex;align-items:center;justify-content:space-between;margin-bottom:0.3rem';
        var inferredTitle = document.createElement('div');
        inferredTitle.style.fontWeight = '600';
        inferredTitle.style.color = 'var(--text)';
        inferredTitle.textContent = 'Reference Memory';
        inferredHeader.appendChild(inferredTitle);
        var wipeBtn = document.createElement('button');
        wipeBtn.type = 'button';
        wipeBtn.style.cssText = 'padding:0.2rem 0.55rem;background:var(--bg-1);border:1px solid var(--border);border-radius:4px;color:var(--danger,#ff7b72);font-size:0.74rem;cursor:pointer';
        wipeBtn.textContent = 'Wipe all';
        wipeBtn.disabled = true;
        wipeBtn.title = 'Delete every entry in this agent’s Reference Memory at once. Uploaded files (Knowledge layer) are not touched.';
        inferredHeader.appendChild(wipeBtn);
        inferredSection.appendChild(inferredHeader);
        var inferredHelp = document.createElement('div');
        inferredHelp.className = 'ui-orch-modal-help';
        inferredHelp.style.marginBottom = '0.5rem';
        inferredHelp.textContent = 'Vector-grown chunks from memory_save + synthesis auto-ingest. Searchable by similarity, not always in prompt. Delete individual entries that drifted, or wipe all if recall is biasing the agent toward stale patterns.';
        inferredSection.appendChild(inferredHelp);
        var inferredList = document.createElement('div');
        inferredList.className = 'ui-orch-list';
        inferredSection.appendChild(inferredList);
        m.body.appendChild(inferredSection);

        wipeBtn.onclick = async function() {
          if (!(await window.uiConfirm('Wipe every Reference Memory entry for this agent (memory_save findings + synthesis auto-ingest). Uploaded files in Knowledge are NOT affected. Continue?'))) return;
          wipeBtn.disabled = true;
          fetch(inferredWipeURL, {method: 'DELETE'})
            .then(function(r) { return r.ok ? r.json() : null; })
            .then(function(d) {
              renderInferred([]);
              if (d) inferredHelp.textContent = 'Wiped ' + (d.removed || 0) + ' entr' + (d.removed === 1 ? 'y' : 'ies') + '. ' + inferredHelp.textContent;
            })
            .catch(function(err){ window.uiAlert('Wipe failed: ' + (err && err.message || err)); wipeBtn.disabled = false; });
        };

        function renderInferred(items) {
          inferredList.innerHTML = '';
          wipeBtn.disabled = !items || !items.length;
          if (!items || !items.length) {
            var empty = document.createElement('div');
            empty.style.cssText = 'color:var(--text-mute);font-style:italic;padding:0.4rem 0';
            empty.textContent = 'No memory entries yet. memory_save findings will appear here once the agent decides something is worth remembering.';
            inferredList.appendChild(empty);
            return;
          }
          items.forEach(function(item) {
            var row = document.createElement('div');
            row.className = 'ui-orch-list-row';
            row.style.alignItems = 'flex-start';
            var body = document.createElement('div');
            body.style.cssText = 'flex:1;font-size:0.85rem;line-height:1.4';
            // Topic line acts as the disclosure trigger — content
            // hidden by default to keep the list scannable even when
            // there are many entries. Click the topic to expand the
            // full chunk text.
            var topic = document.createElement('div');
            topic.style.cssText = 'color:var(--text-mute);font-size:0.72rem;text-transform:uppercase;letter-spacing:0.04em;cursor:pointer;user-select:none';
            var topicCaret = document.createElement('span');
            topicCaret.style.cssText = 'display:inline-block;margin-right:0.4rem;transition:transform 0.15s';
            topicCaret.textContent = String.fromCharCode(9656); // ▸
            topic.appendChild(topicCaret);
            topic.appendChild(document.createTextNode((item.topic || 'general') + (item.source_doc ? ' · ' + item.source_doc : '')));
            body.appendChild(topic);
            var content = document.createElement('div');
            content.style.cssText = 'white-space:pre-wrap;margin-top:0.15rem;display:none';
            content.textContent = item.content || '';
            body.appendChild(content);
            topic.addEventListener('click', function() {
              var open = content.style.display === 'none';
              content.style.display = open ? '' : 'none';
              topicCaret.style.transform = open ? 'rotate(90deg)' : '';
            });
            var del = document.createElement('button');
            del.className = 'ui-orch-list-del';
            del.type = 'button';
            del.textContent = String.fromCharCode(215);
            del.title = 'Delete this entry';
            del.onclick = async function() {
              if (!(await window.uiConfirm('Delete this Reference Memory entry?'))) return;
              fetch(inferredURL + '/' + encodeURIComponent(item.id), {method: 'DELETE'})
                .then(function(r) {
                  if (!r.ok && r.status !== 204) throw new Error('HTTP ' + r.status);
                  row.remove();
                })
                .catch(function(err) { window.uiAlert('Delete failed: ' + (err && err.message || err)); });
            };
            row.appendChild(body);
            row.appendChild(del);
            inferredList.appendChild(row);
          });
        }

        fetch(inferredURL)
          .then(function(r){ return r.ok ? r.json() : null; })
          .then(function(d){ renderInferred(d ? d.items : []); })
          .catch(function(){ renderInferred([]); });

        var cancelBtn = document.createElement('button');
        cancelBtn.className = 'ui-orch-modal-btn';
        cancelBtn.textContent = 'Cancel';
        cancelBtn.onclick = m.close;
        var saveBtn = document.createElement('button');
        saveBtn.className = 'ui-orch-modal-btn primary';
        saveBtn.textContent = 'Save';
        saveBtn.onclick = function() {
          var cleanFacts = facts.map(function(n) { return n.trim(); })
                                .filter(function(n) { return n.length > 0; });
          saveBtn.disabled = true;
          fetch(factsURL, {
            method: 'POST',
            headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({notes: cleanFacts}),
          }).then(function(r) {
            if (!r.ok && r.status !== 204) {
              return r.text().then(function(t) { throw new Error(t); });
            }
            m.close();
            refreshAgentCounts();
          }).catch(function(err) {
            saveBtn.disabled = false;
            window.uiAlert('Save failed: ' + (err && err.message || err));
          });
        };
        m.footer.appendChild(cancelBtn);
        m.footer.appendChild(saveBtn);
      });

      window.uiRegisterClientAction('orchestrate_knowledge_modal', function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        var privateURL = 'api/agents/' + encodeURIComponent(id) + '/knowledge/sources';
        var privateUploadURL = 'api/agents/' + encodeURIComponent(id) + '/knowledge/upload';
        var corpusURL = 'api/agents/' + encodeURIComponent(id) + '/knowledge';
        var autoWipeURL = 'api/agents/' + encodeURIComponent(id) + '/knowledge/auto-inferred';
        var m = openOrchModal('Knowledge');
        var body = m.body;
        var help = document.createElement('div');
        help.className = 'ui-orch-modal-help';
        help.textContent = 'Documents this agent can search. Upload private files below, or attach shared Collections.';
        body.appendChild(help);

        // Helper: build an upload + sources subsection bound to a pair of URLs.
        function buildSection(title, subHelp, listURL, uploadURL, deleteScope) {
          var wrap = document.createElement('div');
          var t = document.createElement('div');
          t.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
          t.textContent = title;
          wrap.appendChild(t);
          var sh = document.createElement('div');
          sh.style.cssText = 'font-size:0.74rem;color:var(--text-mute);line-height:1.45;margin-bottom:0.5rem';
          sh.textContent = subHelp;
          wrap.appendChild(sh);
          var upRow = document.createElement('div');
          upRow.style.cssText = 'display:flex;align-items:center;gap:0.5rem;margin-bottom:0.5rem;flex-wrap:wrap';
          var upInp = document.createElement('input'); upInp.type = 'file';
          upInp.accept = '.pdf,.docx,.txt,.md,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document,text/plain,text/markdown';
          upInp.style.cssText = 'flex:1;min-width:0;font-size:0.8rem';
          var upBtn = document.createElement('button'); upBtn.type = 'button'; upBtn.className = 'ui-row-btn primary';
          upBtn.style.cssText = 'padding:0.3rem 0.7rem;font-size:0.8rem';
          upBtn.textContent = 'Upload';
          upBtn.disabled = true;
          upInp.addEventListener('change', function(){ upBtn.disabled = !(upInp.files && upInp.files[0]); });
          var upStatus = document.createElement('span');
          upStatus.style.cssText = 'font-size:0.72rem;color:var(--text-mute)';
          upRow.appendChild(upInp); upRow.appendChild(upBtn); upRow.appendChild(upStatus);
          wrap.appendChild(upRow);
          var list = document.createElement('div');
          list.style.cssText = 'display:flex;flex-direction:column;gap:0.3rem';
          wrap.appendChild(list);
          function refresh() {
            fetch(listURL).then(function(r){ return r.ok ? r.json() : null; }).then(function(d) {
              list.innerHTML = '';
              var sources = (d && d.sources) || [];
              if (sources.length === 0) {
                var emp = document.createElement('div');
                emp.style.cssText = 'font-size:0.74rem;color:var(--text-mute);font-style:italic';
                emp.textContent = '(no documents yet)';
                list.appendChild(emp);
                return;
              }
              sources.forEach(function(s) {
                var row = document.createElement('div');
                row.style.cssText = 'display:flex;align-items:center;gap:0.5rem;padding:0.3rem 0.5rem;background:var(--bg-0);border:1px solid var(--border);border-radius:4px;font-size:0.8rem';
                var nm = document.createElement('span');
                nm.style.cssText = 'flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap';
                nm.textContent = (s.name || s.id || '(unnamed)').replace(/^#+\s*/, '');
                nm.title = nm.textContent;
                var meta = document.createElement('span');
                meta.style.cssText = 'color:var(--text-mute);font-size:0.7rem';
                meta.textContent = (s.chunks || 0) + ' chunk' + (s.chunks === 1 ? '' : 's');
                var del = document.createElement('button'); del.type = 'button'; del.className = 'ui-row-btn';
                del.style.cssText = 'color:var(--danger,#ff7b72);font-size:0.74rem;padding:0.2rem 0.5rem';
                del.textContent = 'Remove';
                del.onclick = async function() {
                  if (!(await window.uiConfirm('Remove ' + nm.textContent + ' from ' + deleteScope + '?'))) return;
                  del.disabled = true;
                  fetch(listURL + '/' + encodeURIComponent(s.id), {method: 'DELETE'})
                    .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t); }); refresh(); refreshCorpusCount(); })
                    .catch(function(err){ del.disabled = false; window.uiAlert('Remove failed: ' + (err && err.message || err)); });
                };
                row.appendChild(nm); row.appendChild(meta); row.appendChild(del);
                list.appendChild(row);
              });
            });
          }
          refresh();
          upBtn.onclick = function() {
            var f = upInp.files && upInp.files[0];
            if (!f) return;
            upBtn.disabled = true;
            upStatus.style.color = 'var(--text-mute)';
            upStatus.textContent = 'Extracting + indexing...';
            var reader = new FileReader();
            reader.onload = function() {
              var s = reader.result || '';
              var comma = s.indexOf(',');
              var b64 = comma >= 0 ? s.substring(comma + 1) : s;
              fetch(uploadURL, {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({name: f.name, mime_type: f.type || '', data: b64}),
              }).then(function(r){
                if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
                return r.json();
              }).then(function(out) {
                upStatus.style.color = 'var(--accent,#56d364)';
                upStatus.textContent = 'Added ' + (out.chunks || 0) + ' chunks';
                upInp.value = '';
                refresh(); refreshCorpusCount();
              }).catch(function(err){
                upStatus.style.color = 'var(--danger,#ff7b72)';
                upStatus.textContent = 'Upload failed: ' + (err && err.message || err);
                upBtn.disabled = false;
              });
            };
            reader.readAsDataURL(f);
          };
          return wrap;
        }

        body.appendChild(buildSection(
          'Your private documents',
          'Files YOU uploaded under this agent. Scoped to your account — other users on the same agent don\'t see them.',
          privateURL, privateUploadURL,
          'your documents'
        ));

        // --- Attached collections (curation) -----------------------
        // Lets the user attach/detach reusable Collections inline
        // without going back to the editor. Toggling a row commits
        // immediately via POST /api/agents (merge semantics on the
        // attached_collections field).
        var collectionsWrap = document.createElement('div');
        collectionsWrap.style.cssText = 'padding-top:0.8rem;margin-top:0.4rem;border-top:1px solid var(--border)';
        var ch = document.createElement('div');
        ch.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
        ch.textContent = 'Attached collections';
        collectionsWrap.appendChild(ch);
        var chHelp = document.createElement('div');
        chHelp.style.cssText = 'font-size:0.74rem;color:var(--text-mute);line-height:1.45;margin-bottom:0.5rem';
        chHelp.textContent = 'Reusable collections searched alongside this agent\'s own documents. Toggle to attach or detach.';
        collectionsWrap.appendChild(chHelp);
        var collectionsList = document.createElement('div');
        collectionsList.style.cssText = 'display:flex;flex-direction:column;gap:0.3rem';
        collectionsWrap.appendChild(collectionsList);

        // Scaffold-from-agent action — creates an empty collection
        // named "<agent> Knowledge" with the agent's description
        // copied in, attaches it. One-click "give this agent a place
        // to put its docs" for the common case where the user just
        // built the agent and wants to start uploading material.
        var scaffoldRow = document.createElement('div');
        scaffoldRow.style.cssText = 'display:flex;align-items:center;gap:0.5rem;margin-top:0.5rem';
        var scaffoldBtn = document.createElement('button');
        scaffoldBtn.type = 'button';
        scaffoldBtn.className = 'ui-row-btn';
        scaffoldBtn.style.cssText = 'padding:0.3rem 0.7rem;font-size:0.8rem';
        scaffoldBtn.textContent = '+ Create knowledge for this agent';
        scaffoldBtn.title = 'Create an empty collection named after this agent and attach it. Upload docs to fill it, or rename later under Knowledge.';
        var scaffoldStatus = document.createElement('span');
        scaffoldStatus.style.cssText = 'font-size:0.72rem;color:var(--text-mute)';
        scaffoldRow.appendChild(scaffoldBtn);
        scaffoldRow.appendChild(scaffoldStatus);
        collectionsWrap.appendChild(scaffoldRow);

        body.appendChild(collectionsWrap);

        var pickerOpts = {
          host: collectionsList,
          listURL: 'api/collections',
          agentField: 'attached_collections',
          agentID: id,
          emptyText: '(no collections yet — use + Create knowledge for this agent, or author one under Knowledge)'
        };
        renderAllowlistPicker(pickerOpts);

        scaffoldBtn.onclick = function() {
          scaffoldBtn.disabled = true;
          scaffoldStatus.style.color = 'var(--text-mute)';
          scaffoldStatus.textContent = 'Creating…';
          fetch('api/agents/' + encodeURIComponent(id) + '/knowledge/scaffold-collection', {method: 'POST'})
            .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t); }); return r.json(); })
            .then(function(out) {
              scaffoldStatus.style.color = 'var(--accent,#56d364)';
              scaffoldStatus.textContent = 'Created "' + (out.name || 'collection') + '"';
              renderAllowlistPicker(pickerOpts);
            })
            .catch(function(err){
              scaffoldStatus.style.color = 'var(--danger,#ff7b72)';
              scaffoldStatus.textContent = 'Failed: ' + (err && err.message || err);
            })
            .finally(function(){ scaffoldBtn.disabled = false; });
        };
      });

      // --- Pipelines modal -------------------------------------------
      // Attach saved pipelines to this agent. Each toggled-on pipeline
      // becomes a callable run_<pipeline> tool on the agent (see
      // buildAttachedPipelineToolDefs). Reuses the generic allowlist
      // picker against /api/pipelines + the attached_pipelines field;
      // toggling commits immediately via the full-record POST.
      window.uiRegisterClientAction('orchestrate_pipelines_modal', function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        var m = openOrchModal('Pipelines');
        var body = m.body;
        var help = document.createElement('div');
        help.className = 'ui-orch-modal-help';
        help.textContent = 'Attach saved multi-stage pipelines to this agent. Each attached pipeline becomes a callable tool (run_<pipeline>) the agent can run on demand. Author pipelines from chat with the pipeline tool.';
        body.appendChild(help);

        var pipesWrap = document.createElement('div');
        var ph = document.createElement('div');
        ph.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
        ph.textContent = 'Attached pipelines';
        pipesWrap.appendChild(ph);
        var pHelp = document.createElement('div');
        pHelp.style.cssText = 'font-size:0.74rem;color:var(--text-mute);line-height:1.45;margin-bottom:0.5rem';
        pHelp.textContent = 'Toggle to attach or detach. Attached pipelines run synchronously and return their final stage output to the agent.';
        pipesWrap.appendChild(pHelp);
        var pipesList = document.createElement('div');
        pipesList.style.cssText = 'display:flex;flex-direction:column;gap:0.3rem';
        pipesWrap.appendChild(pipesList);
        body.appendChild(pipesWrap);

        renderAllowlistPicker({
          host: pipesList,
          listURL: 'api/pipelines',
          agentField: 'attached_pipelines',
          agentID: id,
          emptyText: '(no pipelines yet — author one from chat with the pipeline tool)'
        });
      });

      // --- Skills modal ----------------------------------------------
      // Active skills allowlist for this agent.
      window.uiRegisterClientAction('orchestrate_skills_modal', function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        var m = openOrchModal('Skills');
        var body = m.body;
        var help = document.createElement('div');
        help.className = 'ui-orch-modal-help';
        help.textContent = 'Skills modify the agent\'s behavior — instructions + tools that join on activation. Strict allowlist per agent.';
        body.appendChild(help);

        // Active skills
        var skillsWrap = document.createElement('div');
        var sh = document.createElement('div');
        sh.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
        sh.textContent = 'Active skills';
        skillsWrap.appendChild(sh);
        var sHelp = document.createElement('div');
        sHelp.style.cssText = 'font-size:0.74rem;color:var(--text-mute);line-height:1.45;margin-bottom:0.5rem';
        sHelp.textContent = 'Strict allowlist of skills the classifier may fire for this agent. Every skill is opt-in per agent — nothing activates unless toggled on. Author skills via skill_def in chat.';
        skillsWrap.appendChild(sHelp);
        var skillsList = document.createElement('div');
        skillsList.style.cssText = 'display:flex;flex-direction:column;gap:0.3rem';
        skillsWrap.appendChild(skillsList);
        body.appendChild(skillsWrap);

        renderAllowlistPicker({
          host: skillsList,
          listURL: 'api/skills/list',
          agentField: 'allowed_skills',
          agentID: id,
          emptyText: '(no skills authored yet — use skill_def in Builder to make one)'
        });
      });

      window.uiRegisterClientAction('orchestrate_rules_modal', function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        fetchAgent(id).then(function(agent) {
          var m = openOrchModal('Rules — ' + (agent.name || id));
          var help = document.createElement('div');
          help.className = 'ui-orch-modal-help';
          help.textContent = 'Non-negotiable operating policy. Applied to BOTH the orchestrator and worker on every turn. Use for hard constraints like "always cite a URL" or "never quote prices from training".';
          m.body.appendChild(help);
          var list = document.createElement('div');
          list.className = 'ui-orch-list';
          m.body.appendChild(list);

          var rules = (agent.rules || '').split('\n')
            .map(function(s) { return s.replace(/^\s*([-*]|\d+\.)\s*/, '').trim(); })
            .filter(function(s) { return s.length > 0; });
          // If empty, start with one blank row so the user has something to type into.
          if (rules.length === 0) rules.push('');

          function render() {
            list.innerHTML = '';
            rules.forEach(function(text, idx) {
              var row = document.createElement('div');
              row.className = 'ui-orch-list-row';
              var input = document.createElement('input');
              input.type = 'text';
              input.value = text;
              input.placeholder = 'rule…';
              input.addEventListener('input', function() { rules[idx] = input.value; });
              var del = document.createElement('button');
              del.className = 'ui-orch-list-del';
              del.type = 'button';
              del.textContent = '×';
              del.onclick = function() {
                rules.splice(idx, 1);
                if (!rules.length) rules.push('');
                render();
              };
              row.appendChild(input);
              row.appendChild(del);
              list.appendChild(row);
            });
            var addBtn = document.createElement('button');
            addBtn.type = 'button';
            addBtn.className = 'ui-orch-list-add';
            addBtn.textContent = '+ Add rule';
            addBtn.onclick = function() {
              rules.push('');
              render();
              var inputs = list.querySelectorAll('input');
              var last = inputs[inputs.length - 1];
              if (last) last.focus();
            };
            list.appendChild(addBtn);
          }
          render();

          var cancelBtn = document.createElement('button');
          cancelBtn.className = 'ui-orch-modal-btn';
          cancelBtn.textContent = 'Cancel';
          cancelBtn.onclick = m.close;
          var saveBtn = document.createElement('button');
          saveBtn.className = 'ui-orch-modal-btn primary';
          saveBtn.textContent = 'Save';
          saveBtn.onclick = function() {
            var clean = rules.map(function(r) { return r.trim(); })
                             .filter(function(r) { return r.length > 0; });
            agent.rules = clean.join('\n');
            saveBtn.disabled = true;
            fetch('api/agents', {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body: JSON.stringify(agent),
            }).then(function(r) {
              if (!r.ok) return r.text().then(function(t) { throw new Error(t); });
              m.close();
              refreshAgentCounts();
            }).catch(function(err) {
              saveBtn.disabled = false;
              window.uiAlert('Save failed: ' + (err && err.message || err));
            });
          };
          m.footer.appendChild(cancelBtn);
          m.footer.appendChild(saveBtn);
        }).catch(function(err) {
          window.uiAlert('Failed to load agent: ' + (err && err.message || err));
        });
      });

      // --- Per-agent count refresh on the Tools / Memory / Rules buttons ---
      // Find the toolbar buttons by their visible label text and append
      // a "(N)" suffix that reflects the active agent's state. Refresh
      // on agent change (the dropdown's change event already triggers
      // a new-session reset; this listener piggybacks).
      function refreshAgentCounts() {
        var id = getAgentID();
        // Buttons may live in either the actions bar or the modes
        // row (relocated by relocateContextButtons). Match by label
        // anywhere inside the panel so the lookup survives the move.
        var btns = document.querySelectorAll('.ui-agent-actions .ui-row-btn, .ui-agent-modes .ui-row-btn');
        var tools = null, memory = null, rules = null, deleteBtn = null;
        btns.forEach(function(b) {
          var t = b.textContent.replace(/\s*\([^)]*\)\s*$/, '').trim();
          if (t === 'Tools') tools = b;
          else if (t === 'Memory') memory = b;
          else if (t === 'Rules') rules = b;
          else if (t === 'Delete' || t === 'Revert') deleteBtn = b;
        });
        // Seed agents present the Delete button as "Revert" since the
        // backend interprets DELETE on a seed-id as "drop the shadow
        // and fall back to the framework default" rather than a full
        // record removal.
        if (deleteBtn) {
          var asSeed = id && id.indexOf('seed-') === 0;
          deleteBtn.textContent = asSeed ? 'Revert' : 'Delete';
          deleteBtn.title = asSeed
            ? 'Discard your customizations and restore framework defaults for this seed agent.'
            : 'Delete the active agent';
        }
        // Builder lockdown — when the active agent is the Builder
        // seed, hide the chrome that would let a user edit it or
        // pollute its structurally-constrained authoring purpose:
        // Edit/Clone/Delete (no point editing a locked seed agent),
        // Private and Clean (network is a hard requirement; clean-
        // slate framing doesn't fit authoring). Memory stays exposed
        // because Builder NOW has a "Lessons learned" log the user
        // should be able to review and prune. Rules stays exposed
        // for the same reason — admin-curated standing guidance is
        // legitimate customization (not accumulated state) and lets
        // an operator shape Builder's authoring rhythm without
        // forking the seed.
        //
        // Tools stays VISIBLE for Builder even though its catalog
        // is implicit (the AllowedTools checklist has no effect):
        // the same modal surfaces SESSION tools — drafts Builder's
        // workers authored mid-conversation via tool_def — and
        // those need an admin remove path. Without this, drafts
        // accumulate session-by-session with no way to prune them
        // outside the conversation that authored them.
        var lock = id === 'seed-builder';
        // Match buttons by data attribute regardless of parent —
        // relocateContextButtons moves them between .ui-agent-actions
        // and .ui-agent-modes, so a strict-parent selector misses
        // anything that's been relocated. The label attributes are
        // unique enough to query bare.
        // Edit + Delete (which acts as Revert for seeds) stay exposed
        // on Builder — its persona / authoring conventions are
        // legitimately tweakable, and seed-revert is the safety net.
        // Clone stays hidden: a "cloned Builder" wouldn't get the
        // Builder-specific dispatch wiring and would silently lose
        // its authoring behavior. Knowledge stays hidden: Builder's
        // attached collections are framework-curated state.
        var lockedActions = ['Clone', 'Knowledge'];
        lockedActions.forEach(function(label) {
          var nodes = document.querySelectorAll('[data-action-label="' + label + '"]');
          nodes.forEach(function(btn) { btn.style.display = lock ? 'none' : ''; });
        });
        var lockedModes = ['Private', 'Clean'];
        lockedModes.forEach(function(label) {
          var nodes = document.querySelectorAll('[data-mode-label="' + label + '"]');
          nodes.forEach(function(btn) { btn.style.display = lock ? 'none' : ''; });
        });
        if (!tools && !memory && !rules) return;
        function setCount(btn, text) {
          if (!btn) return;
          var base = btn.textContent.replace(/\s*\([^)]*\)\s*$/, '');
          btn.textContent = text == null ? base : base + ' (' + text + ')';
        }
        // No agent picked — clear counts to plain labels.
        if (!id) {
          setCount(tools, null);
          setCount(memory, null);
          setCount(rules, null);
          return;
        }
        // Parallel fetches for the active agent.
        fetch('api/agents/' + encodeURIComponent(id)).then(function(r) {
          return r.ok ? r.json() : null;
        }).then(function(a) {
          if (!a) return;
          var allowed = (a.allowed_tools || []);
          var catalog = (window.ORCH_TOOL_CATALOG || []);
          var internet = (window.ORCH_INTERNET_TOOLS || []);
          var internetSet = {};
          internet.forEach(function(n){ internetSet[n] = true; });
          // Private mode flips off in the agent loop's tool surface
          // (runner.go filteredWorkerTools); mirror that filter here
          // so the count reflects what the LLM ACTUALLY has access
          // to this turn, not the abstract catalog total.
          var privateOn = false;
          var pToggle = document.querySelector('.ui-agent-mode-toggle[data-mode-send-field="private_mode"] input[type="checkbox"]');
          if (pToggle) privateOn = !!pToggle.checked;
          var cat = catalog.length;
          if (privateOn) {
            cat = 0;
            catalog.forEach(function(opt) { if (!internetSet[opt.value]) cat++; });
          }
          // Resolve the agent's effective tool count.
          //   Empty allowlist          → "use default pool" → cat
          //   ["__none__"] sentinel    → "explicitly no tools" → 0
          //   Explicit names           → intersect with visible catalog
          // The sentinel is the no-tools marker the Tools modal saves
          // when the user unchecks everything; without this special
          // case the count would show "1/N" (literal array length)
          // while the modal renders ALL checkboxes unchecked — a
          // confusing inconsistency.
          var n;
          if (allowed.length === 1 && allowed[0] === '__none__') {
            n = 0;
          } else if (allowed.length === 0) {
            n = cat;
          } else {
            n = 0;
            allowed.forEach(function(name) {
              if (privateOn && internetSet[name]) return;
              n++;
            });
          }
          // Custom tools authored for this specific agent (Tools field
          // on the record, bundled via add_tool / inline create_agent).
          // Append only when present so the count stays unsurprising
          // for agents that don't have any. Filter out tools whose name
          // is already in the global catalog (user-wide persistent
          // pool) — those are surfaced in the regular checklist and
          // shouldn't double-count here.
          var catalogNamesForCount = {};
          (window.ORCH_TOOL_CATALOG || []).forEach(function(o){ catalogNamesForCount[o.value] = true; });
          var customN = (a.tools || []).filter(function(t){
            return !catalogNamesForCount[t.name];
          }).length;
          // Builder's catalog is code-driven and the AllowedTools
          // checklist has no effect for it (see the Tools-modal
          // banner) — the only count that's meaningful for Builder
          // is the session-tools count (the drafts its workers
          // authored). Show JUST that on Builder's Tools button so
          // an idle count of "23 / 45" doesn't suggest there's
          // something to manage in the catalog when there isn't.
          var isBuilder = (id === 'seed-builder');
          var label = isBuilder ? '' : (n + ' / ' + cat);
          if (!isBuilder && customN > 0) {
            label += ', ' + customN + ' custom';
          }
          // Session tools — drafts authored mid-conversation via
          // tool_def / add_tool, scoped to this chat session. Fetched
          // separately because they live in session_temp_tools, not
          // on the agent record. Only attempted when a session is
          // open (deep-link param tracks the active sid).
          var sid = '';
          try {
            sid = new URL(window.location.href).searchParams.get('session') || '';
          } catch (_) {}
          function applySessionCount(n) {
            if (isBuilder) {
              // Builder: count is JUST session tools, or no count
              // parens at all when there's nothing in flight.
              setCount(tools, n > 0 ? (n + ' session') : null);
              return;
            }
            if (n > 0) {
              setCount(tools, label + ', ' + n + ' session');
            } else {
              setCount(tools, label);
            }
          }
          if (sid) {
            fetch('api/sessions/' + encodeURIComponent(sid) +
                  '/tools?agent_id=' + encodeURIComponent(id))
              .then(function(r) { return r.ok ? r.json() : null; })
              .then(function(sessTools) {
                applySessionCount(Array.isArray(sessTools) ? sessTools.length : 0);
              })
              .catch(function() { applySessionCount(0); });
          } else {
            applySessionCount(0);
          }
          var rn = (a.rules || '').split('\n').filter(function(s) { return s.trim(); }).length;
          setCount(rules, rn);
        }).catch(function(){});
        fetch('api/agents/' + encodeURIComponent(id) + '/memory').then(function(r) {
          return r.ok ? r.json() : null;
        }).then(function(m) {
          if (!m) return;
          setCount(memory, (m.notes || []).length);
        }).catch(function(){});
      }
      // Initial + on-change refresh. watchAgentSwitchForNewSession
      // (defined elsewhere) already fires on dropdown change; we add
      // our own listener for the count refresh.
      function watchAgentForCounts() {
        var sel = document.querySelector('.ui-agent-extras select');
        if (!sel) { setTimeout(watchAgentForCounts, 200); return; }
        refreshAgentCounts();
        sel.addEventListener('change', refreshAgentCounts);
      }
      watchAgentForCounts();
      // Re-run on private-mode toggle so the count reflects the
      // network-tool subtraction (Private hides every InternetTool
      // from the worker catalog at runtime — see filteredWorkerTools
      // in runner.go).
      window.addEventListener('ui-agent-mode-change', refreshAgentCounts);
      // Re-run when the runner reports an agent-affecting mutation
      // (create_agent / update_agent / clone / delete / add_tool / tool_def).
      // The runner emits an SSE event with name=orchestrate_agents_changed,
      // which the framework re-dispatches as a window event with the
      // ui-agent-event: prefix.
      window.addEventListener('ui-agent-event:orchestrate_agents_changed', refreshAgentCounts);
      // Re-run on session change so the "N session" suffix tracks the
      // ACTIVE session's draft pool — switching sessions repoints the
      // fetch at a different sid.
      window.addEventListener('ui-agent-session', refreshAgentCounts);

      // --- Intake form on new session ----------------------------------
      // When the active agent has an intake_form defined AND we're at
      // the start of a new conversation (no messages yet), render the
      // form fields above the chat input. Submitting packs the values
      // into a markdown user message and uses the framework's normal
      // sendMessage path, after which the regular text input takes
      // over for the rest of the session.
      var intakeWrap = null;
      function activeIntakeForm() {
        var a = currentAgentForIntake;
        if (!a || !Array.isArray(a.intake_form) || a.intake_form.length === 0) return null;
        return a.intake_form;
      }
      function conversationIsEmpty() {
        var log = document.querySelector('.ui-agent-convo-log');
        if (!log) return true;
        // Empty when only the placeholder "Start typing…" lives in
        // there. Real bubbles get class ui-agent-msg.
        return log.querySelector('.ui-agent-msg') == null;
      }
      function clearIntakeForm() {
        if (intakeWrap && intakeWrap.parentNode) intakeWrap.parentNode.removeChild(intakeWrap);
        intakeWrap = null;
      }
      // (setIntakeInputRowHidden removed: the chat input row stays
      // visible alongside the intake form so the user can either fill
      // the form or type a regular message — same agent reached either
      // way.)
      // buildIntakeFormDOM renders intake fields as a styled block.
      // values is an optional prefill {name: value} map; disabled
      // freezes inputs for the read-only bubble view. Returns the
      // root element + an inputs map so the caller can collect
      // values on submit.
      function buildIntakeFormDOM(fields, values, disabled) {
        values = values || {};
        var wrap = document.createElement('div');
        wrap.className = 'ui-orch-intake';
        var inputs = {};
        // safeText defends against the LLM authoring intake fields
        // with objects where strings were expected — labels, help
        // text, placeholders, option values. Without coercion, the
        // browser renders "[object Object]" and the form is broken.
        function safeText(v) {
          if (v == null) return '';
          if (typeof v === 'object') {
            // Common shape: {value, label} from training priors that
            // expect HTML-select semantics. Honor it.
            if (v.label != null) return String(v.label);
            if (v.value != null) return String(v.value);
            try { return JSON.stringify(v); } catch (_) { return ''; }
          }
          return String(v);
        }
        // optionValue extracts the submittable value from an option
        // entry. {value, label} → value; bare string → itself.
        function optionValue(o) {
          if (o != null && typeof o === 'object' && o.value != null) return String(o.value);
          return safeText(o);
        }
        function optionLabel(o) {
          if (o != null && typeof o === 'object' && o.label != null) return String(o.label);
          return optionValue(o);
        }
        fields.forEach(function(f) {
          var row = document.createElement('div');
          row.className = 'ui-orch-intake-row';
          var lbl = document.createElement('label');
          lbl.className = 'ui-orch-intake-label';
          lbl.textContent = safeText(f.label || f.name) + (f.required ? ' *' : '');
          row.appendChild(lbl);
          var inp;
          var t = f.type || 'text';
          if (t === 'textarea') {
            inp = document.createElement('textarea');
            inp.rows = 3;
            inp.className = 'ui-orch-intake-input ui-orch-intake-textarea';
          } else if (t === 'select') {
            inp = document.createElement('select');
            inp.className = 'ui-orch-intake-input';
            (f.options || []).forEach(function(o) {
              var opt = document.createElement('option');
              opt.value = optionValue(o);
              opt.textContent = optionLabel(o);
              inp.appendChild(opt);
            });
          } else if (t === 'checklist') {
            // Multi-select: vertical list of checkboxes. Selected
            // values get joined comma-separated when packed into the
            // intake markdown ("**Topics:** topic1, topic2"). Prefill
            // accepts either a comma-separated string (round-tripped
            // from a prior intake bubble) or an array.
            //
            // allow_other: when true, render an extra row with a
            // checkbox + inline text input. When checked + non-empty,
            // the text content is appended to the comma list (the
            // option value is the typed text itself, NOT the literal
            // "Other"). The text input is data-other so collect()
            // can find it without coupling to position.
            inp = document.createElement('div');
            inp.className = 'ui-orch-intake-checklist';
            var prefill = values[f.name];
            var selected = {};
            var prefillOther = '';
            if (Array.isArray(prefill)) {
              prefill.forEach(function(v){ selected[String(v)] = true; });
            } else if (typeof prefill === 'string' && prefill) {
              prefill.split(',').forEach(function(v){
                v = v.trim();
                if (v) selected[v] = true;
              });
            }
            var knownVals = {};
            (f.options || []).forEach(function(o) {
              var val = optionValue(o);
              knownVals[val] = true;
              var label = optionLabel(o);
              var item = document.createElement('label');
              item.className = 'ui-orch-intake-checklist-item';
              var box = document.createElement('input');
              box.type = 'checkbox';
              box.value = val;
              if (selected[val]) box.checked = true;
              if (disabled) box.disabled = true;
              var txt = document.createElement('span');
              txt.textContent = label;
              item.appendChild(box);
              item.appendChild(txt);
              inp.appendChild(item);
            });
            // Any prefill value not matching a known option must have
            // come from a prior Other entry — restore it into the
            // text field below.
            if (f.allow_other) {
              Object.keys(selected).forEach(function(v){
                if (!knownVals[v]) {
                  prefillOther = v;
                }
              });
              var otherRow = document.createElement('label');
              otherRow.className = 'ui-orch-intake-checklist-item ui-orch-intake-checklist-other';
              var otherBox = document.createElement('input');
              otherBox.type = 'checkbox';
              otherBox.dataset.otherBox = '1';
              if (prefillOther) otherBox.checked = true;
              if (disabled) otherBox.disabled = true;
              var otherLbl = document.createElement('span');
              otherLbl.textContent = 'Other:';
              var otherText = document.createElement('input');
              otherText.type = 'text';
              otherText.className = 'ui-orch-intake-checklist-other-text';
              otherText.dataset.otherText = '1';
              otherText.placeholder = 'type your own…';
              if (prefillOther) otherText.value = prefillOther;
              if (disabled) otherText.disabled = true;
              // Typing in the text field auto-checks the Other box so
              // the user doesn't have to click both. Symmetrical: un-
              // checking the box doesn't clear the text (the user might
              // re-tick it).
              otherText.addEventListener('input', function() {
                if (otherText.value.trim() && !otherBox.checked) {
                  otherBox.checked = true;
                }
              });
              otherRow.appendChild(otherBox);
              otherRow.appendChild(otherLbl);
              otherRow.appendChild(otherText);
              inp.appendChild(otherRow);
            }
          } else if (t === 'number') {
            inp = document.createElement('input');
            inp.type = 'number';
            inp.className = 'ui-orch-intake-input';
          } else if (t === 'file') {
            inp = document.createElement('input');
            inp.type = 'file';
            inp.className = 'ui-orch-intake-input';
          } else if (t === 'button') {
            inp = document.createElement('div');
            inp.className = 'ui-orch-intake-button-row';
            var opts = (f.options && f.options.length) ? f.options : [f.label || f.name];
            var prevSelected = safeText(values[f.name] || '');
            if (prevSelected) inp.dataset.value = prevSelected;
            opts.forEach(function(o) {
              var val = optionValue(o);
              var label = optionLabel(o);
              var btn = document.createElement('button');
              btn.type = 'button';
              btn.className = 'ui-row-btn ui-orch-intake-button';
              if (prevSelected && val === prevSelected) {
                btn.classList.add('selected');
              }
              btn.textContent = label;
              if (disabled) btn.disabled = true;
              btn.addEventListener('click', function() {
                inp.dataset.value = val;
                var submitBtn = document.querySelector('.ui-orch-intake-actions .ui-row-btn.primary');
                if (submitBtn) submitBtn.click();
              });
              inp.appendChild(btn);
            });
          } else {
            inp = document.createElement('input');
            inp.type = 'text';
            inp.className = 'ui-orch-intake-input';
          }
          // Enter-to-submit on single-line text + number inputs. Skipped
          // for textareas (Enter is newline there by convention), buttons
          // (they self-submit on click), files (no text entry), selects
          // (browser handles Enter natively).
          if (inp.tagName === 'INPUT' && (inp.type === 'text' || inp.type === 'number')) {
            inp.addEventListener('keydown', function(ev) {
              if (ev.key !== 'Enter' || ev.shiftKey || ev.altKey || ev.ctrlKey || ev.metaKey) return;
              ev.preventDefault();
              var submitBtn = (wrap.parentNode && wrap.parentNode.querySelector('.ui-orch-intake-actions .ui-row-btn.primary'))
                || document.querySelector('.ui-orch-intake-actions .ui-row-btn.primary');
              if (submitBtn) submitBtn.click();
            });
          }
          if (f.placeholder && t !== 'file' && t !== 'button' && t !== 'checklist') inp.placeholder = safeText(f.placeholder);
          if (values[f.name] != null && t !== 'file' && t !== 'button' && t !== 'checklist') inp.value = safeText(values[f.name]);
          if (disabled && t !== 'button' && t !== 'checklist') inp.disabled = true;
          inputs[f.name] = {field: f, input: inp};
          row.appendChild(inp);
          if (f.help) {
            var help = document.createElement('div');
            help.className = 'ui-orch-intake-help';
            help.textContent = safeText(f.help);
            row.appendChild(help);
          }
          wrap.appendChild(row);
        });
        return {root: wrap, inputs: inputs};
      }
      function collectIntake(fields, inputs) {
        var missing = [];
        fields.forEach(function(f) {
          var entry = inputs[f.name];
          if (!entry) return;
          if (!f.required) return;
          var t = f.type || 'text';
          if (t === 'file') {
            if (!entry.input.files || entry.input.files.length === 0) {
              missing.push(f.label || f.name);
            }
          } else if (t === 'button') {
            if (!entry.input.dataset.value) missing.push(f.label || f.name);
          } else if (t === 'checklist') {
            // Required = at least one selection that actually carries
            // a value. An Other box checked with empty text doesn't
            // count — there's no value to submit.
            var picked = 0;
            entry.input.querySelectorAll('input[type=checkbox]:checked').forEach(function(b){
              if (b.dataset.otherBox) {
                var txt = entry.input.querySelector('input[data-other-text]');
                if (txt && String(txt.value || '').trim()) picked++;
              } else {
                picked++;
              }
            });
            if (picked === 0) missing.push(f.label || f.name);
          } else if (!String(entry.input.value || '').trim()) {
            missing.push(f.label || f.name);
          }
        });
        if (missing.length > 0) { window.uiAlert('Please fill in: ' + missing.join(', ')); return null; }
        var entries = [];
        fields.forEach(function(f) {
          var entry = inputs[f.name];
          if (!entry) return;
          var t = f.type || 'text';
          if (t === 'file') {
            var file = entry.input.files && entry.input.files[0];
            if (file) {
              entries.push({name: f.name, label: f.label || f.name, value: file.name, file: file});
            }
            return;
          }
          if (t === 'button') {
            var bv = entry.input.dataset.value || '';
            if (bv) entries.push({name: f.name, label: f.label || f.name, value: bv});
            return;
          }
          if (t === 'checklist') {
            var picks = [];
            entry.input.querySelectorAll('input[type=checkbox]:checked').forEach(function(b){
              if (b.dataset.otherBox) {
                var txt = entry.input.querySelector('input[data-other-text]');
                var v = txt ? String(txt.value || '').trim() : '';
                if (v) picks.push(v);
              } else {
                picks.push(b.value);
              }
            });
            if (picks.length === 0) return;
            entries.push({name: f.name, label: f.label || f.name, value: picks.join(', ')});
            return;
          }
          var v = String(entry.input.value || '').trim();
          if (!v) return;
          entries.push({name: f.name, label: f.label || f.name, value: v});
        });
        return entries;
      }
      function packIntakeMarkdown(entries) {
        return entries.map(function(e) { return '**' + e.label + ':** ' + e.value; }).join('\n\n');
      }
      // stageIntakeFiles reads each file entry as a data URL and
      // pushes it onto the framework's pendingAttachments via
      // uiAddPendingAttachment. Returns a promise so the caller waits
      // before firing the Send click (sendMessage reads pendingAttachments
      // synchronously).
      function stageIntakeFiles(entries) {
        var pending = entries.filter(function(e){ return e.file; });
        if (pending.length === 0) return Promise.resolve();
        return Promise.all(pending.map(function(e){
          return new Promise(function(resolve){
            var reader = new FileReader();
            reader.onload = function() {
              if (window.uiAddPendingAttachment) {
                window.uiAddPendingAttachment({
                  name:    e.file.name,
                  dataURL: reader.result,
                  mime:    e.file.type || '',
                });
              }
              resolve();
            };
            reader.onerror = resolve;
            reader.readAsDataURL(e.file);
          });
        }));
      }
      function valuesByNameFromEntries(entries) {
        var m = {};
        entries.forEach(function(e) { m[e.name] = e.value; });
        return m;
      }
      // decorateIntakeBubble swaps a sent user bubble's body for the
      // intake form rendered with disabled inputs, then tags the
      // bubble + stashes the values JSON so the registered editor
      // can rehydrate them when the user clicks Edit.
      function decorateIntakeBubble(bubble, fields, valuesByName) {
        var body = bubble.querySelector(':scope > .ui-agent-msg-body');
        if (!body) return;
        body.innerHTML = '';
        var built = buildIntakeFormDOM(fields, valuesByName, true);
        body.appendChild(built.root);
        bubble.classList.remove('ui-agent-msg-streaming');
        bubble.dataset.uiIntake = '1';
        bubble.dataset.uiIntakeValues = JSON.stringify(valuesByName);
      }
      function renderIntakeForm() {
        clearIntakeForm();
        var fields = activeIntakeForm();
        if (!fields || !conversationIsEmpty()) return;
        var log = document.querySelector('.ui-agent-convo-log');
        if (!log) return;
        var built = buildIntakeFormDOM(fields, {}, false);
        intakeWrap = built.root;
        var buttonOnly = fields.every(function(f){ return (f.type||'text') === 'button'; });
        if (buttonOnly) intakeWrap.classList.add('ui-orch-intake-button-only');
        var header = document.createElement('div');
        header.className = 'ui-orch-intake-header';
        header.textContent = buttonOnly
          ? 'Pick a starting point.'
          : 'Tell me about it — fill these in to get started.';
        intakeWrap.insertBefore(header, intakeWrap.firstChild);
        var actions = document.createElement('div');
        actions.className = 'ui-orch-intake-actions';
        var submitBtn = document.createElement('button');
        submitBtn.type = 'button';
        submitBtn.className = 'ui-row-btn primary';
        submitBtn.textContent = 'Start session';
        if (buttonOnly) submitBtn.style.display = 'none';
        submitBtn.addEventListener('click', function() {
          var entries = collectIntake(fields, built.inputs);
          if (!entries) return;
          if (entries.length === 0) { window.uiAlert('Fill in at least one field.'); return; }
          var values = valuesByNameFromEntries(entries);
          submitBtn.disabled = true;
          stageIntakeFiles(entries).then(function() {
            clearIntakeForm();
            var ta = document.querySelector('.ui-agent-input');
            if (!ta) { submitBtn.disabled = false; return; }
            ta.value = packIntakeMarkdown(entries);
            // Ride intake_values alongside the send so the server can
            // persist them on the user message. Without this, re-edit
            // after page reload would have nothing to rehydrate.
            if (window.uiSetPendingMessageExtras) {
              window.uiSetPendingMessageExtras({intake_values: values});
            }
            var send = document.querySelector('.ui-agent-input-row .ui-row-btn.primary');
            if (send) send.click();
            setTimeout(function() {
              var bubbles = document.querySelectorAll('.ui-agent-msg.ui-agent-msg-user');
              var latest = bubbles[bubbles.length - 1];
              if (latest) decorateIntakeBubble(latest, fields, values);
            }, 0);
          });
        });
        actions.appendChild(submitBtn);
        intakeWrap.appendChild(actions);
        log.appendChild(intakeWrap);
      }
      // Per-bubble Edit override: when the framework's beginUserEdit
      // sees an intake bubble (data-ui-intake='1'), this fires instead
      // of the default textarea path. Inputs flip enabled, Save/Cancel
      // appear below the form; Save resubmits via the framework's
      // truncate + sendMessage pipeline (ctx.commit handles it).
      function registerIntakeEditor() {
        if (!window.uiRegisterMessageEditor) { setTimeout(registerIntakeEditor, 50); return; }
        window.uiRegisterMessageEditor(
          function(bubble) { return bubble && bubble.dataset && bubble.dataset.uiIntake === '1'; },
          function(ctx) {
            var fields = activeIntakeForm();
            if (!fields) { ctx.cancel(); return; }
            var values = {};
            try { values = JSON.parse(ctx.bubble.dataset.uiIntakeValues || '{}') || {}; } catch (e) { values = {}; }
            var body = ctx.body;
            body.innerHTML = '';
            var built = buildIntakeFormDOM(fields, values, false);
            body.appendChild(built.root);
            if (ctx.actions) ctx.actions.style.display = 'none';
            var bar = document.createElement('div');
            bar.className = 'ui-orch-intake-actions';
            var save = document.createElement('button');
            save.type = 'button';
            save.className = 'ui-row-btn primary';
            save.textContent = 'Save & resend';
            var cancel = document.createElement('button');
            cancel.type = 'button';
            cancel.className = 'ui-row-btn';
            cancel.textContent = 'Cancel';
            save.addEventListener('click', function() {
              var entries = collectIntake(fields, built.inputs);
              if (!entries) return;
              if (entries.length === 0) { window.uiAlert('Fill in at least one field.'); return; }
              save.disabled = true; cancel.disabled = true;
              var newValues = valuesByNameFromEntries(entries);
              stageIntakeFiles(entries).then(function() {
                // Same as fresh-submit: persist updated values on the
                // re-sent message so a future reload + re-edit cycle
                // sees the latest snapshot, not the original.
                if (window.uiSetPendingMessageExtras) {
                  window.uiSetPendingMessageExtras({intake_values: newValues});
                }
                return ctx.commit(packIntakeMarkdown(entries));
              }).then(function() {
                setTimeout(function() {
                  var bubbles = document.querySelectorAll('.ui-agent-msg.ui-agent-msg-user');
                  var latest = bubbles[bubbles.length - 1];
                  if (latest) decorateIntakeBubble(latest, fields, newValues);
                }, 0);
              }).catch(function(err) {
                save.disabled = false; cancel.disabled = false;
                window.uiAlert('Edit failed: ' + (err && err.message || err));
              });
            });
            cancel.addEventListener('click', function() {
              body.innerHTML = '';
              var rebuilt = buildIntakeFormDOM(fields, values, true);
              body.appendChild(rebuilt.root);
              bar.remove();
              if (ctx.actions) ctx.actions.style.display = '';
            });
            bar.appendChild(save); bar.appendChild(cancel);
            ctx.bubble.appendChild(bar);
          }
        );
      }
      registerIntakeEditor();

      // On session replay, re-decorate any user message that was
      // submitted via intake form (server persists intake_values on
      // those messages). Without this, the bubble re-renders as plain
      // text after a page reload and Edit falls back to the textarea
      // path — the form structure is lost. Idempotent: decorateIntakeBubble
      // sets data-ui-intake/values, so a duplicate hook fire is a no-op
      // beyond re-rendering the same disabled form.
      function registerIntakeReplayHook() {
        if (!window.uiRegisterMessageReplayHook) { setTimeout(registerIntakeReplayHook, 50); return; }
        window.uiRegisterMessageReplayHook(function(bubble, msg) {
          if (!bubble || !msg) return;
          if ((msg.role || '') !== 'user') return;
          var values = msg.intake_values;
          if (!values || typeof values !== 'object') return;
          var fields = activeIntakeForm();
          if (!fields) return; // agent intake spec not loaded yet — fail silent
          decorateIntakeBubble(bubble, fields, values);
        });
      }
      registerIntakeReplayHook();

      // Export-to-Techwriter — on any assistant bubble, ship the
      // rendered body to techwriter as a new article and open the
      // editor in a new tab. Subject is the closest preceding user
      // message (truncated) so the article isn't born nameless.
      function registerExportToTechwriter() {
        if (!window.uiRegisterBubbleAction) { setTimeout(registerExportToTechwriter, 50); return; }
        window.uiRegisterBubbleAction({
          role: 'assistant',
          label: '↗ Techwriter',
          title: 'Export this response to Techwriter as a new article',
          onclick: function(ctx) {
            var body = ctx.getText() || '';
            if (!body.trim()) { window.uiAlert('Nothing to export — message is empty.'); return; }
            // Find the closest preceding user message for the subject.
            var subject = 'Untitled';
            var bub = ctx.bubble;
            var cur = bub && bub.previousElementSibling;
            while (cur) {
              if (cur.classList && cur.classList.contains('ui-agent-msg-user')) {
                var t = (cur.dataset && cur.dataset.raw) || cur.textContent || '';
                t = t.replace(/\s+/g, ' ').trim();
                if (t.length > 0) {
                  subject = t.length > 80 ? t.slice(0, 77) + '...' : t;
                  break;
                }
              }
              cur = cur.previousElementSibling;
            }
            fetch('/techwriter/api/save', {
              method: 'POST',
              headers: {'Content-Type': 'application/json'},
              body: JSON.stringify({subject: subject, body: body}),
            }).then(function(r) {
              if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
              return r.json();
            }).then(function(d) {
              if (d && d.id) {
                window.open('/techwriter/?article=' + encodeURIComponent(d.id), '_blank');
              } else {
                window.uiAlert('Saved, but Techwriter returned no ID.');
              }
            }).catch(function(err) {
              window.uiAlert('Export failed: ' + (err && err.message || err));
            });
          },
        });
      }
      registerExportToTechwriter();

      // Track the active agent's intake_form. Refreshed on agent
      // change (dropdown switch); cached so renderIntakeForm doesn't
      // re-fetch every time it checks emptiness.
      var currentAgentForIntake = null;
      function refreshIntakeAgent() {
        var id = getAgentID();
        if (!id) { currentAgentForIntake = null; clearIntakeForm(); return; }
        fetchAgent(id).then(function(a) {
          currentAgentForIntake = a;
          renderIntakeForm();
        }).catch(function() { currentAgentForIntake = null; });
      }
      // Initial fetch + re-fetch on agent dropdown change.
      function watchAgentForIntake() {
        var s = document.querySelector('.ui-agent-extras select');
        if (!s) { setTimeout(watchAgentForIntake, 200); return; }
        refreshIntakeAgent();
        s.addEventListener('change', function(){ setTimeout(refreshIntakeAgent, 50); });
      }
      watchAgentForIntake();

      // Re-render the intake form on session changes (clicking "+ New
      // conversation" clears the convo log; we then check if intake
      // is configured for the current agent).
      window.addEventListener('ui-agent-session', function() {
        setTimeout(renderIntakeForm, 50);
      });
      // Also poll the convo-log: when it transitions to empty after
      // a delete or back-button, render the intake form again. This
      // is the cleanest hook without instrumenting framework events.
      var lastEmptyState = null;
      setInterval(function() {
        var empty = conversationIsEmpty();
        if (empty === lastEmptyState) return;
        lastEmptyState = empty;
        if (empty) renderIntakeForm();
        else clearIntakeForm();
      }, 500);

      // Relocate the Tools / Memory / Knowledge / Skills / Pipelines /
      // Rules buttons into
      // the modes row so they sit on the same line as the Private toggle —
      // chat-context affordances live together, leaving the actions
      // bar for record-level operations (New / Edit / Clone / Export /
      // Import / Revert). Polls because the panel mounts after
      // DOMContentLoaded and the modes row only renders when the
      // panel has at least one Mode configured.
      function relocateContextButtons() {
        var actionsBar = document.querySelector('.ui-agent-actions');
        var modesRow = document.querySelector('.ui-agent-modes');
        if (!actionsBar || !modesRow) { setTimeout(relocateContextButtons, 200); return; }
        var names = {Tools: true, Memory: true, Knowledge: true, Skills: true, Pipelines: true, Rules: true};
        var moved = 0;
        Array.prototype.slice.call(actionsBar.querySelectorAll('.ui-row-btn')).forEach(function(b) {
          var label = b.textContent.replace(/\s*\([^)]*\)\s*$/, '').trim();
          if (names[label]) {
            modesRow.appendChild(b);
            moved++;
          }
        });
        if (moved === 0) { setTimeout(relocateContextButtons, 200); }
      }
      relocateContextButtons();

      // Re-fetch /api/agents and rebuild the picker's <option> list
      // in place. Preserves the currently-selected agent if it still
      // exists; falls back to the Chat seed when the active agent
      // was just deleted. Triggered by the SSE "agents_changed"
      // event the runner emits after create_agent / update_agent /
      // delete_agent / clone_agent fires successfully.
      function refreshAgentDropdown() {
        var sel = document.querySelector('.ui-agent-extras select');
        if (!sel) return;
        fetch('api/agents').then(function(r) {
          return r.ok ? r.json() : null;
        }).then(function(list) {
          if (!list) return;
          var prev = sel.value;
          // Rebuild WITH the Built-in / Custom optgroups, mirroring the
          // server-side initial render in page_chat.go. A flat rebuild
          // here dropped the divisions whenever an agents_changed event
          // fired (create / clone / delete / Builder authoring), so the
          // categories "sometimes went away." Keep the same builtInOrder
          // partition + sort so the rebuilt dropdown matches the first
          // paint exactly. (listAgents server-side already merges seeds
          // with per-user shadows, so each agent appears once.)
          //
          // Sub-agents (owned_by set) are excluded — they appear in the
          // secondary specialist picker, not the main dropdown.
          // ORCH_SUB_AGENTS is rebuilt here so a newly-authored sub-agent
          // shows up in the specialist picker without a page reload.
          var builtInOrder = {'seed-builder': 0, 'seed-chat': 1, 'seed-research': 2, 'seed-kb': 3};
          var builtIns = [], customs = [];
          var subMap = {};
          list.forEach(function(a) {
            if (a.owned_by) {
              if (!subMap[a.owned_by]) subMap[a.owned_by] = [];
              subMap[a.owned_by].push({id: a.id, name: a.name});
              return;
            }
            // Hidden=true scopes to FLEET visibility, not the Agency
            // picker — matches the server-side page_chat.go rule.
            // Filtering Hidden here used to silently flip the user
            // off Builder onto seed-chat after every save / refresh
            // event, because Builder is permanently Hidden now.
            // Only sub-agents (handled by the owned_by branch above)
            // are picker-suppressed.
            if (a.id in builtInOrder) { builtIns.push(a); }
            else { customs.push(a); }
          });
          window.ORCH_SUB_AGENTS = subMap;
          builtIns.sort(function(a, b) { return builtInOrder[a.id] - builtInOrder[b.id]; });
          customs.sort(function(a, b) { return (a.name || '').localeCompare(b.name || ''); });
          sel.innerHTML = '';
          sel.appendChild(new Option('— select agent —', ''));
          function addAgentGroup(label, rows) {
            if (!rows.length) return;
            var og = document.createElement('optgroup');
            og.label = label;
            rows.forEach(function(a) { og.appendChild(new Option(a.name, a.id)); });
            sel.appendChild(og);
          }
          addAgentGroup('Built-in', builtIns);
          addAgentGroup('Custom', customs);
          // If the previously-active value is a sub-agent, find the
          // parent's option and rewrite its value in-place to the
          // sub-agent ID (preserving the parent's label). This keeps
          // the main picker visibly on the parent while routing
          // targets the specialist — same trick the runtime change
          // handler uses; we just re-apply it after the dropdown
          // rebuild wipes the data-orch-original-value attributes.
          var prevParentID = null;
          for (var pid in subMap) {
            for (var i = 0; i < subMap[pid].length; i++) {
              if (subMap[pid][i].id === prev) { prevParentID = pid; break; }
            }
            if (prevParentID) break;
          }
          if (prevParentID) {
            var parentOpt = sel.querySelector('option[value="' + CSS.escape(prevParentID) + '"]');
            if (parentOpt) {
              parentOpt.setAttribute('data-orch-original-value', prevParentID);
              parentOpt.value = prev;
            }
          }
          // Restore selection. If the previously-selected agent
          // was deleted, fall back to seed-chat so the page isn't
          // left in the placeholder state.
          var hasPrev = false;
          for (var i = 0; i < sel.options.length; i++) {
            if (sel.options[i].value === prev) { hasPrev = true; break; }
          }
          sel.value = hasPrev ? prev : 'seed-chat';
          // Keep dependent UI in sync (count chips, conversation rail
          // for the active agent). The change event also fires the
          // new-session reset if the agent changed; suppress that by
          // only firing when value actually changed.
          if (sel.value !== prev) {
            sel.dispatchEvent(new Event('change'));
          } else {
            refreshAgentCounts();
          }
        }).catch(function(){});
      }
      window.addEventListener('ui-agent-event:orchestrate_agents_changed', refreshAgentDropdown);

      // isSeedAgentID — seed agents have IDs starting with "seed-" and
      // resolve to in-code framework defaults. The Delete button on a
      // seed reverts (drops the user's shadow) instead of deleting.
      function isSeedAgentID(id) {
        return typeof id === 'string' && id.indexOf('seed-') === 0;
      }

      window.uiRegisterClientAction('orchestrate_delete_agent', async function() {
        var id = getAgentID();
        if (!id) { window.uiAlert('Pick an agent first.'); return; }
        var label = getAgentLabel() || id;
        var seed = isSeedAgentID(id);
        var msg = seed
          ? ('Revert "' + label + '" to framework defaults? Your customizations will be discarded; conversations are kept.')
          : ('Delete agent "' + label + '"? This also removes all of its conversations.');
        if (!(await window.uiConfirm(msg))) return;
        fetch('api/agents/' + encodeURIComponent(id), {
          method: 'DELETE',
        }).then(function(r) {
          // Seed-revert returns 400 when there's no shadow saved
          // ("nothing to revert"); surface that as a benign toast
          // rather than an error.
          if (!r.ok && r.status !== 204) {
            return r.text().then(function(t) {
              if (seed && r.status === 400 && /nothing to revert/i.test(t)) {
                window.uiAlert('Already at framework defaults — nothing to revert.');
                return null;
              }
              throw new Error(t || ('HTTP ' + r.status));
            });
          }
          if (seed) {
            // Stay on the same agent; refresh the dropdown + UI so the
            // reverted defaults take effect immediately.
            refreshAgentDropdown();
            refreshAgentCounts();
            return;
          }
          // Non-seed: drop the deep-link and land on a clean state.
          try {
            var u = new URL(window.location.href);
            u.searchParams.delete('session');
            window.location.href = u.pathname;
          } catch (_) {
            window.location.reload();
          }
        }).catch(function(err) {
          window.uiAlert('Delete failed: ' + (err && err.message || err));
        });
      });
    }
  }
  register();
})();
</script>`
