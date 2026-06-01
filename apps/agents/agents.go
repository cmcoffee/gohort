// Package agents serves the PUBLIC surface for agents an admin has
// flipped Exposed=true on in orchestrate. Orchestrate itself is
// admin-only (build / configure / prune / delete); this app gives
// end-users a stripped chat pane for each published agent at
// /agents/<slug>/.
//
// Per-user scoping: every end-user has their own sessions + memory
// under each exposed agent. The agent's persona / prompts / allowed
// tools come from the OWNER's record (single source of truth); the
// user-facing accumulator (memory, knowledge, history) lives in the
// END-USER's per-user sub-store.
//
// The runner is shared with orchestrate: this app resolves a slug
// to an AgentRecord, then dispatches into orchestrate's exported
// PublicHandle* methods, which bypass the admin gate but reuse all
// the runner / session / memory plumbing.

package agents

import (
	"encoding/json"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

func init() { RegisterApp(new(AgentsApp)) }

// AgentsApp publishes the /agents/ public directory + per-slug chat
// surfaces. AppCore wires the framework state (DB, LLM, etc.) but
// most actual work delegates to the registered OrchestrateApp via
// the exported Public* methods.
type AgentsApp struct {
	AppCore
}

func (T AgentsApp) Name() string         { return "agents" }
func (T AgentsApp) SystemPrompt() string { return "" }
func (T AgentsApp) Desc() string {
	return "Apps: Public agents published by an admin via Agency."
}

func (T *AgentsApp) Init() error { return T.Flags.Parse() }
func (T *AgentsApp) Main() error {
	Log("Agents is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

func (T *AgentsApp) WebPath() string { return "/agents" }
func (T *AgentsApp) WebName() string { return "Agents" }
func (T *AgentsApp) WebDesc() string {
	return "Chat with the agents your admin has published."
}

// WebHidden suppresses the umbrella "Agents" tile on the dashboard.
// Each exposed agent gets its own dashboard card via OrchestrateApp's
// DashboardCards source, so a generic "Agents → directory" link is
// redundant clutter. The /agents/<slug>/ routes still serve normally,
// and the /agents/ directory page itself stays reachable by direct URL
// for users who bookmarked it.
func (T *AgentsApp) WebHidden() bool { return true }

// Routes registers ONE catch-all at "/" — net/http.ServeMux doesn't
// have path-parameter routing, so we parse the slug ourselves and
// dispatch. URLs:
//
//   /                            — directory (lists exposed agents)
//   /<slug>/                     — chat page
//   /<slug>/api/send             — SSE chat (resolves slug, dispatches)
//   /<slug>/api/cancel           — abort in-flight round
//   /<slug>/api/sessions         — list user's sessions for this agent
//   /<slug>/api/sessions/<sid>   — load / delete / truncate one session
//   /<slug>/api/memory           — read / replace this user's memory under the agent
func (T *AgentsApp) Routes() {
	T.HandleFunc("/", T.dispatch)
}

// dispatch routes by parsed URL. Single entry point so all the slug
// parsing + agent resolution happens in one place. Each branch
// either renders a page or forwards to orchestrate's exported
// public-handler methods.
func (T *AgentsApp) dispatch(w http.ResponseWriter, r *http.Request) {
	// Auth required across the board — the public surface is for
	// LOGGED-IN end-users, not the open internet. Unauthenticated
	// requests get bounced before we resolve anything.
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		T.handleDirectory(w, r, orch)
		return
	}
	parts := strings.SplitN(path, "/", 2)
	slug := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	agent, _, ok := orch.LookupExposedAgent(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Per-agent access gate — exposed agents are normal apps in the
	// permission system. Admins always pass; non-admins need the
	// agent's path in their user.Apps or the default-apps list.
	// Without this gate, anyone who guessed the slug could chat with
	// every exposed agent regardless of dashboard visibility.
	if !UserHasAppAccess(r, "/agents/"+slug) {
		http.NotFound(w, r) // 404 not 403 — don't leak slug existence
		return
	}
	switch {
	case rest == "" || rest == "/":
		T.handleChatPage(w, r, agent)
	case rest == "api/send":
		orch.PublicHandleSend(w, r, agent)
	case rest == "api/cancel":
		orch.PublicHandleCancel(w, r, agent)
	case rest == "api/sessions":
		orch.PublicHandleSessionList(w, r, agent.ID)
	case strings.HasPrefix(rest, "api/sessions/"):
		orch.PublicHandleSessionOne(w, r, agent.ID, strings.TrimPrefix(rest, "api/sessions/"))
	case rest == "api/facts":
		orch.PublicHandleAgentFacts(w, r, agent.ID)
	case rest == "api/knowledge":
		orch.PublicHandleAgentKnowledge(w, r, agent.ID)
	case rest == "api/agent":
		orch.PublicHandleAgentRecord(w, r, agent)
	case rest == "api/inferred":
		orch.PublicHandleAgentInferredList(w, r, agent.ID)
	case strings.HasPrefix(rest, "api/inferred/"):
		orch.PublicHandleAgentInferredDelete(w, r, agent.ID, strings.TrimPrefix(rest, "api/inferred/"))
	case rest == "api/knowledge/auto-inferred":
		orch.PublicHandleAgentKnowledgeAutoInferredWipe(w, r, agent.ID)
	case rest == "api/knowledge/upload":
		orch.PublicHandleAgentKnowledgeUpload(w, r, agent.ID)
	case rest == "api/knowledge/sources":
		orch.PublicHandleAgentKnowledgeSources(w, r, agent.ID)
	case strings.HasPrefix(rest, "api/knowledge/sources/"):
		orch.PublicHandleAgentKnowledgeSourceDelete(w, r, agent.ID, strings.TrimPrefix(rest, "api/knowledge/sources/"))
	case rest == "api/settings/private":
		orch.PublicHandlePrivateModeGet(w, r)
	case rest == "api/settings/private/set":
		orch.PublicHandlePrivateModeSet(w, r)
	case rest == "api/settings/memory":
		orch.PublicHandleMemoryModeGet(w, r)
	case rest == "api/settings/memory/set":
		orch.PublicHandleMemoryModeSet(w, r)
	default:
		http.NotFound(w, r)
	}
}

// findOrchestrate locates the registered OrchestrateApp instance so
// we can call its PublicHandle* methods. Cached after first hit since
// the registry doesn't change at runtime.
var cachedOrch *orchestrate.OrchestrateApp

func findOrchestrate() *orchestrate.OrchestrateApp {
	if cachedOrch != nil {
		return cachedOrch
	}
	a, ok := FindAgent("orchestrate")
	if !ok {
		return nil
	}
	o, ok := a.(*orchestrate.OrchestrateApp)
	if !ok {
		return nil
	}
	cachedOrch = o
	return cachedOrch
}

// handleDirectory used to render a per-agent grid here; that's been
// retired in favor of one card per exposed agent on the central
// dashboard (orchestrate.DashboardCards). Anyone hitting /agents/
// (a bookmark, a typo) gets bounced to the dashboard.
func (T *AgentsApp) handleDirectory(w http.ResponseWriter, r *http.Request, orch *orchestrate.OrchestrateApp) {
	_ = orch
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleChatPage renders the per-agent stripped chat surface. Same
// AgentLoopPanel orchestrate uses, but with the agent-management
// toolbar pruned (no Edit / Clone / Export / Import / Tools / Rules
// / Delete) and no agent dropdown.
func (T *AgentsApp) handleChatPage(w http.ResponseWriter, r *http.Request, agent orchestrate.AgentRecord) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	_ = udb
	_ = user
	display := orchestrate.ExposedDisplayName(agent)
	desc := strings.TrimSpace(agent.Description)
	if desc == "" {
		desc = "What do you want to do?"
	}
	// Server-render the intake spec into a window global so the JS
	// doesn't need to fetch the agent (the slug-only URL means the
	// JS can't resolve the record otherwise). Empty spec → null,
	// the intake script sees null and stays out of the way.
	intakeJSON := "null"
	if len(agent.IntakeForm) > 0 {
		if b, err := json.Marshal(agent.IntakeForm); err == nil {
			intakeJSON = string(b)
		}
	}
	// window.GOHORT_AGENT_ID seeds the per-(user, agent) scope so
	// the toggle endpoints read/write per-agent overrides instead of
	// the global user-level fallback. Fixed per-page on the public
	// agent app (one slug → one agent); no need for a dropdown-change
	// listener like Agency has.
	agentIDJSON, _ := json.Marshal(agent.ID)
	intakeHead := "<script>window.AGENT_INTAKE_FORM = " + intakeJSON + ";" +
		"window.GOHORT_AGENT_ID = " + string(agentIDJSON) + ";</script>" +
		intakeFormAssets
	// Private-mode toggle only renders when the agent's admin opted
	// in via AllowPrivateMode AND ForcePrivate isn't on. ForcePrivate
	// forces Private mode permanently, so showing a toggle would be
	// misleading (the user couldn't actually turn it off).
	var modes []ui.ChatMode
	if agent.AllowPrivateMode && !agent.ForcePrivate {
		modes = append(modes, ui.ChatMode{
			Label:     "Private",
			Title:     "Mask network-capability tools (web_search, fetch_url, …) — keeps this turn local.",
			GetURL:    "api/settings/private",
			PostURL:   "api/settings/private/set",
			Field:     "private_mode",
			SendField: "private_mode",
		})
	}
	// "Clean" toggle suppresses the Reference Memory layer for this turn.
	// Hidden when the agent has DisableInferred set (nothing to control)
	// since the agent is already configured to never use that layer.
	// Otherwise any end-user gets the per-(user, agent) opt-in.
	if !agent.DisableInferred {
		modes = append(modes, ui.ChatMode{
			Label:     "Clean",
			Title:     "Suppress the Reference Memory layer for this turn — no memory_save / memory_search / memory_forget tools, no synthesis auto-ingest, no derived chunks in auto-injection. The agent answers fresh from your question plus the Knowledge layer (uploaded files) and Explicit Memory (facts), without its own prior derived findings coloring the response.",
			GetURL:    "api/settings/memory",
			PostURL:   "api/settings/memory/set",
			Field:     "inferred_disabled",
			SendField: "inferred_disabled",
		})
	}
	page := ui.Page{
		Title:     display,
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body: ui.AgentLoopPanel{
					ListURL:       "api/sessions",
					LoadURL:       "api/sessions/{id}",
					DeleteURL:     "api/sessions/{id}",
					TruncateURL:   "api/sessions/{id}",
					ListTitle:     "Sessions",
					NewLabel:      "New session",
					// Drives sessions + New into the topbar as
					// dropdown-style buttons; no persistent rail.
					// Matches the classic chat-app surface where
					// one conversation owns the whole pane.
					ListPosition:  "top",
					SendURL:       "api/send",
					CancelURL:     "api/cancel",
					DeepLinkParam: "session",
					LockActivity:  true,
					EmptyText:     desc,
					Placeholder:   "Ask " + display + " something…",
					Markdown:      true,
					BulkSelect:    true,
					Attachments:   true,
					// Per-user surfaces:
					//   - Memory: the end-user's accumulated notes for
					//     this agent (private to them)
					//   - Documents: a unified view of the agent's
					//     shared knowledge base (read-only — admin
					//     curated) + the end-user's own uploaded docs
					//     (editable, private to them).
					Actions: []ui.ToolbarAction{
						{Label: "Memory", Title: "Review and prune the notes this agent has accumulated from your conversations.",
							Method: "client", URL: "agents_memory_modal"},
						{Label: "Knowledge", Title: "Manage your private documents for this agent, review the agent's shared knowledge base, and wipe your accumulated corpus.",
							Method: "client", URL: "agents_knowledge_modal"},
						{Label: "Copy session", Title: "Copy the full session as markdown — every user message, every assistant round, every tool call/result — for pasting into a prompt-tuning chat.",
							Method: "client", URL: "copy_session"},
					},
					Modes: modes,
				},
			},
		},
		ExtraHeadHTML: intakeHead + TranscribeRuntimeFlagScript() + memoryModalScript + docsModalScript,
	}
	page.ServeHTTP(w, r)
}

// intakeFormAssets renders the agent's intake form (when configured)
// on the first turn of each new session AND keeps the same form
// widgets rendered (disabled) inside the user bubble after submit
// so the intake's shape stays visible in history. The bubble's
// per-message Edit button re-enables the inputs via a registered
// message editor (uiRegisterMessageEditor) — clicking Edit lets the
// user tweak the form values and resubmit instead of editing the
// rendered markdown.
//
// Reads the spec from window.AGENT_INTAKE_FORM (server-rendered
// inline; slug-only URL has no agent endpoint to fetch).
//
// CSS classes mirror orchestrate's ui-orch-intake-* so visual
// styling matches the workbench. Inline (not shared via a core
// constant) until a second public surface needs the same primitive —
// at which point this is the right thing to lift into core/ui as a
// generic IntakePanel component.
const intakeFormAssets = `<style>
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
/* Button-type intake field — row of clickable buttons that each
 * submit the form immediately with their label as the value. */
.ui-orch-intake-button-row {
  display: flex; flex-wrap: wrap; gap: 0.4rem;
}
.ui-orch-intake-button {
  padding: 0.35rem 0.8rem; font-size: 0.85rem;
}
/* History-view state: buttons disabled inside the user bubble after
 * submit. .selected highlights the choice the user actually made;
 * the rest grey out so the choice is clear and the row reads as
 * "this is what was submitted" rather than an active prompt. */
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
/* Button-only intake form (no typed fields, only buttons) — center
 * the buttons so the form reads as a tidy "pick one" panel rather
 * than a left-aligned form row. */
.ui-orch-intake-button-only .ui-orch-intake-button-row {
  justify-content: center;
}
.ui-orch-intake-button-only .ui-orch-intake-header {
  text-align: center;
}
/* No visible actions row in button-only mode — the submit button is
 * hidden and there's nothing else to render. display:none on the
 * wrapper kills the margin-top that would otherwise leave a gap at
 * the bottom of the form. The hidden submit button stays findable
 * via querySelector for the button click handlers to .click(). */
.ui-orch-intake-button-only .ui-orch-intake-actions {
  display: none;
}
.ui-orch-intake-result {
  display: grid;
  grid-template-columns: minmax(8rem, auto) 1fr;
  gap: 0.35rem 0.85rem;
  margin: 0.1rem 0;
}
.ui-orch-intake-result-row { display: contents; }
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
</style>
<script>
(function(){
  var intakeWrap = null;
  function spec() {
    var f = window.AGENT_INTAKE_FORM;
    return (Array.isArray(f) && f.length > 0) ? f : null;
  }
  function conversationIsEmpty() {
    var log = document.querySelector('.ui-agent-convo-log');
    if (!log) return true;
    return log.querySelector('.ui-agent-msg') == null;
  }
  function clearIntake() {
    if (intakeWrap && intakeWrap.parentNode) intakeWrap.parentNode.removeChild(intakeWrap);
    intakeWrap = null;
    // Restore the chat input row hidden while the form was visible.
    setInputRowHidden(false);
  }
  // setInputRowHidden flips the chat-input row off (form active) or
  // back on (form submitted/cleared). The user shouldn't be able to
  // bypass the intake form by typing freeform text — hide the input
  // and send button for the duration the form is up. Per-bubble edit
  // form path is unaffected (it operates on a bubble, not the
  // bottom-of-pane input).
  function setInputRowHidden(hidden) {
    var ta = document.querySelector('.ui-agent-input');
    var row = document.querySelector('.ui-agent-input-row');
    if (ta) ta.style.display = hidden ? 'none' : '';
    if (row) row.style.visibility = hidden ? 'hidden' : '';
  }
  // buildForm renders the intake fields as a DOM block. values is
  // an optional {name: value} map for prefill; disabled keeps the
  // bubble-side variant read-only by default. Returns the container
  // plus an inputs map so the caller can read/collect values.
  function buildForm(fields, values, disabled) {
    values = values || {};
    var wrap = document.createElement('div');
    wrap.className = 'ui-orch-intake';
    var inputs = {};
    fields.forEach(function(f){
      var row = document.createElement('div');
      row.className = 'ui-orch-intake-row';
      var lbl = document.createElement('label');
      lbl.className = 'ui-orch-intake-label';
      lbl.textContent = (f.label || f.name) + (f.required ? ' *' : '');
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
        (f.options || []).forEach(function(o){
          var opt = document.createElement('option');
          opt.value = o; opt.textContent = o;
          inp.appendChild(opt);
        });
      } else if (t === 'number') {
        inp = document.createElement('input');
        inp.type = 'number';
        inp.className = 'ui-orch-intake-input';
      } else if (t === 'file') {
        inp = document.createElement('input');
        inp.type = 'file';
        inp.className = 'ui-orch-intake-input';
      } else if (t === 'button') {
        // Button-type field renders the f.options as a row of
        // buttons. Clicking any one stores its value on the input's
        // dataset.value and triggers the form's primary submit
        // immediately — no separate submit click needed.
        // In disabled mode (history-view bubble), buttons are
        // greyed out and the previously-selected one gets a
        // .selected class so the user can see which they picked.
        inp = document.createElement('div');
        inp.className = 'ui-orch-intake-button-row';
        var opts = (f.options && f.options.length) ? f.options : [f.label || f.name];
        var prevSelected = values[f.name] || '';
        if (prevSelected) inp.dataset.value = prevSelected;
        opts.forEach(function(opt) {
          var btn = document.createElement('button');
          btn.type = 'button';
          btn.className = 'ui-row-btn ui-orch-intake-button';
          if (prevSelected && opt === prevSelected) {
            btn.classList.add('selected');
          }
          btn.textContent = opt;
          if (disabled) btn.disabled = true;
          btn.addEventListener('click', function() {
            inp.dataset.value = opt;
            var submitBtn = wrap.parentNode && wrap.parentNode.querySelector('.ui-orch-intake-actions .ui-row-btn.primary');
            if (!submitBtn) submitBtn = document.querySelector('.ui-orch-intake-actions .ui-row-btn.primary');
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
      if (f.placeholder && t !== 'file' && t !== 'button') inp.placeholder = f.placeholder;
      if (values[f.name] != null && t !== 'file' && t !== 'button') inp.value = String(values[f.name]);
      if (disabled && t !== 'button') inp.disabled = true;
      inputs[f.name] = {field: f, input: inp};
      row.appendChild(inp);
      if (f.help) {
        var help = document.createElement('div');
        help.className = 'ui-orch-intake-help';
        help.textContent = f.help;
        row.appendChild(help);
      }
      wrap.appendChild(row);
    });
    return {root: wrap, inputs: inputs};
  }
  // collect pulls (label, value) pairs out of an inputs map, in
  // field order, skipping empty entries. File fields are NOT packed
  // into entries — they ride as attachments via uiAddPendingAttachment.
  // Returns null when a required field is empty (caller alerts user).
  function collect(fields, inputs) {
    var missing = [];
    fields.forEach(function(f){
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
      } else if (!String(entry.input.value||'').trim()) {
        missing.push(f.label || f.name);
      }
    });
    if (missing.length > 0) { alert('Please fill in: ' + missing.join(', ')); return null; }
    var entries = [];
    fields.forEach(function(f){
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
      var v = String(entry.input.value||'').trim();
      if (!v) return;
      entries.push({name: f.name, label: f.label || f.name, value: v});
    });
    return entries;
  }
  // stageIntakeFiles reads each file-bearing entry as a data URL and
  // pushes it onto the framework's pendingAttachments via
  // uiAddPendingAttachment. Returns a promise that resolves once
  // every file is staged so the caller can click Send afterward.
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
  function packMarkdown(entries) {
    return entries.map(function(e){ return '**' + e.label + ':** ' + e.value; }).join('\n\n');
  }
  // decorateBubble swaps a freshly-sent user bubble's body for the
  // intake form rendered with disabled inputs + tags the bubble with
  // data-ui-intake='1' and stashes values JSON so the registered
  // editor can rehydrate them on Edit.
  function decorateBubble(bubble, fields, valuesByName) {
    var body = bubble.querySelector(':scope > .ui-agent-msg-body');
    if (!body) return;
    body.innerHTML = '';
    var built = buildForm(fields, valuesByName, true);
    body.appendChild(built.root);
    bubble.classList.remove('ui-agent-msg-streaming');
    bubble.dataset.uiIntake = '1';
    bubble.dataset.uiIntakeValues = JSON.stringify(valuesByName);
  }
  function valuesByNameFromEntries(entries) {
    var m = {};
    entries.forEach(function(e){ m[e.name] = e.value; });
    return m;
  }
  function renderIntake() {
    clearIntake();
    var fields = spec();
    if (!fields || !conversationIsEmpty()) return;
    var log = document.querySelector('.ui-agent-convo-log');
    if (!log) return;
    var built = buildForm(fields, {}, false);
    intakeWrap = built.root;
    // Detect button-only forms: every field is type "button". These
    // are kiosk-style "pick a starting point" flows where each button
    // self-submits and a "Start session" button would be redundant
    // chrome. Flag the form so CSS can center the buttons.
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
    // Hidden when the form is button-only — buttons trigger submit
    // themselves. Kept in the DOM so the button click handlers can
    // still .click() it programmatically.
    if (buttonOnly) submitBtn.style.display = 'none';
    submitBtn.addEventListener('click', function(){
      var entries = collect(fields, built.inputs);
      if (!entries) return;
      if (entries.length === 0) { alert('Fill in at least one field.'); return; }
      var values = valuesByNameFromEntries(entries);
      submitBtn.disabled = true;
      // Stage any file fields onto the framework's attachment queue
      // FIRST — sendMessage reads pendingAttachments synchronously
      // on click, so the files must be in place before we click.
      stageIntakeFiles(entries).then(function(){
        clearIntake();
        var ta = document.querySelector('.ui-agent-input');
        if (!ta) { submitBtn.disabled = false; return; }
        ta.value = packMarkdown(entries);
        var send = document.querySelector('.ui-agent-input-row .ui-row-btn.primary');
        if (send) send.click();
        setTimeout(function(){
          var bubbles = document.querySelectorAll('.ui-agent-msg.ui-agent-msg-user');
          var latest = bubbles[bubbles.length-1];
          if (!latest) return;
          decorateBubble(latest, fields, values);
        }, 0);
      });
    });
    actions.appendChild(submitBtn);
    intakeWrap.appendChild(actions);
    log.appendChild(intakeWrap);
    setInputRowHidden(true);
  }
  // Register the per-bubble Edit override. When the framework's
  // beginUserEdit sees an intake bubble, this fires instead of the
  // default textarea path — inputs flip enabled, Save/Cancel show
  // beneath the form.
  function registerEditor() {
    if (!window.uiRegisterMessageEditor) { setTimeout(registerEditor, 50); return; }
    window.uiRegisterMessageEditor(
      function(bubble){ return bubble && bubble.dataset && bubble.dataset.uiIntake === '1'; },
      function(ctx){
        var fields = spec();
        if (!fields) { ctx.cancel(); return; }
        var values = {};
        try { values = JSON.parse(ctx.bubble.dataset.uiIntakeValues || '{}') || {}; } catch (e) { values = {}; }
        var body = ctx.body;
        body.innerHTML = '';
        var built = buildForm(fields, values, false);
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
        save.addEventListener('click', function(){
          var entries = collect(fields, built.inputs);
          if (!entries) return;
          if (entries.length === 0) { alert('Fill in at least one field.'); return; }
          save.disabled = true; cancel.disabled = true;
          var newValues = valuesByNameFromEntries(entries);
          stageIntakeFiles(entries).then(function(){
            return ctx.commit(packMarkdown(entries));
          }).then(function(){
            // commit dropped this bubble; the resend handler in the
            // setTimeout-after-send branch decorates the new bubble.
            setTimeout(function(){
              var bubbles = document.querySelectorAll('.ui-agent-msg.ui-agent-msg-user');
              var latest = bubbles[bubbles.length-1];
              if (latest) decorateBubble(latest, fields, newValues);
            }, 0);
          }).catch(function(err){
            save.disabled = false; cancel.disabled = false;
            alert('Edit failed: ' + (err && err.message || err));
          });
        });
        cancel.addEventListener('click', function(){
          // Restore the disabled-form view from the stashed values.
          body.innerHTML = '';
          var rebuilt = buildForm(fields, values, true);
          body.appendChild(rebuilt.root);
          bar.remove();
          if (ctx.actions) ctx.actions.style.display = '';
        });
        bar.appendChild(save); bar.appendChild(cancel);
        ctx.bubble.appendChild(bar);
      }
    );
  }
  registerEditor();
  // Render on load, on session events, and via empty-state polling
  // (covers session deletes / back-button navigations).
  function mount(){ setTimeout(renderIntake, 50); }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', mount);
  } else {
    mount();
  }
  window.addEventListener('ui-agent-session', mount);
  var lastEmpty = null;
  setInterval(function(){
    var empty = conversationIsEmpty();
    if (empty === lastEmpty) return;
    lastEmpty = empty;
    if (empty) renderIntake(); else clearIntake();
  }, 500);
})();
</script>`

// memoryModalScript wires the toolbar's "Memory" client action to a
// modal that mirrors the orchestrate admin memory modal — Notes +
// Saved facts (or framing-overridden header) + Knowledge (uploads
// + accumulated corpus + chunk count + wipe). Scoped to the end-user
// via the public /agents/<slug>/api/* endpoints; the calling user
// only ever sees their own per-(user, agent) memory.
const memoryModalScript = `<script>
(function(){
  function register() {
    if (!window.uiRegisterClientAction) { setTimeout(register, 50); return; }
    window.uiRegisterClientAction('agents_memory_modal', function() {
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
      fetch('api/facts').then(function(r){ return r.ok ? r.json() : null; }).then(function(d) {
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
          var topic = document.createElement('div');
          topic.style.cssText = 'color:var(--text-mute);font-size:0.7rem;text-transform:uppercase;letter-spacing:0.04em';
          topic.textContent = (item.topic || 'general') + (item.source_doc ? ' · ' + item.source_doc : '');
          col.appendChild(topic);
          var content = document.createElement('div');
          content.style.cssText = 'white-space:pre-wrap;margin-top:0.15rem';
          content.textContent = item.content || '';
          col.appendChild(content);
          var del = document.createElement('button');
          del.type = 'button';
          del.textContent = String.fromCharCode(215);
          del.title = 'Delete this entry';
          del.style.cssText = 'background:transparent;border:0;color:var(--text-mute);cursor:pointer;font-size:1rem;padding:0 0.4rem;align-self:flex-start';
          del.addEventListener('click', function() {
            if (!confirm('Delete this Reference Memory entry?')) return;
            fetch('api/inferred/' + encodeURIComponent(item.id), {method: 'DELETE'})
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
        fetch('api/knowledge/auto-inferred', {method: 'DELETE'})
          .then(function(r){ return r.ok ? r.json() : null; })
          .then(function(d){
            renderInferred([]);
            if (d) inferredIntro.textContent = 'Wiped ' + (d.removed || 0) + ' entr' + (d.removed === 1 ? 'y' : 'ies') + '. ' + inferredIntro.textContent;
          })
          .catch(function(err){ alert('Wipe failed: ' + (err && err.message || err)); wipeBtn.disabled = false; });
      });

      fetch('api/inferred')
        .then(function(r){ return r.ok ? r.json() : null; })
        .then(function(d){ renderInferred(d ? d.items : []); })
        .catch(function(){ renderInferred([]); });

      // --- Gate sections based on agent's disable flags ---
      fetch('api/agent').then(function(r){ return r.ok ? r.json() : null; }).then(function(a) {
        if (!a) return;
        if (a.disable_explicit) factsWrap.style.display = 'none';
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
        fetch('api/facts', {
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

// docsModalScript powers the public agent app's "Knowledge" toolbar
// action. After the Memory→Knowledge migration this surface owns the
// per-agent corpus end-to-end: shared admin-curated docs (read-only),
// the end-user's private uploads (editable), and the wipe button for
// the user's own accumulated chunks. Memory modal stays focused on
// Notes + Saved facts.
const docsModalScript = `<script>
(function(){
  function register() {
    if (!window.uiRegisterClientAction) { setTimeout(register, 50); return; }
    window.uiRegisterClientAction('agents_knowledge_modal', function() {
      // Custom overlay (not native <dialog>) — same rationale as the
      // Memory modal: native <dialog>+showModal renders blank on
      // some iOS / older Android browsers.
      var overlay = document.createElement('div');
      overlay.style.cssText = 'position:fixed;inset:0;background:rgba(0,0,0,0.5);display:flex;align-items:center;justify-content:center;z-index:1000;padding:1rem;box-sizing:border-box';
      var dlg = document.createElement('div');
      dlg.style.cssText = 'box-sizing:border-box;background:var(--bg-1);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:1rem;width:100%;max-width:640px;max-height:88vh;display:flex;flex-direction:column';
      overlay.appendChild(dlg);
      function closeDlg() { overlay.remove(); document.removeEventListener('keydown', _esc); }
      function _esc(ev) { if (ev.key === 'Escape') closeDlg(); }
      overlay.addEventListener('click', function(ev) { if (ev.target === overlay) closeDlg(); });
      document.addEventListener('keydown', _esc);
      dlg.close = closeDlg; dlg.remove = closeDlg;
      var h = document.createElement('h3'); h.textContent = 'Knowledge';
      h.style.cssText = 'margin:0 0 0.5rem';
      dlg.appendChild(h);
      var body = document.createElement('div');
      body.style.cssText = 'overflow-y:auto;flex:1;padding-right:0.3rem;display:flex;flex-direction:column;gap:1rem;-webkit-overflow-scrolling:touch';
      dlg.appendChild(body);

      // (Shared / admin-curated docs removed — the agent's reference
      // corpus now lives in Collections attached via the editor.)

      // --- Your documents (editable) ---
      var ownWrap = document.createElement('div');
      ownWrap.style.cssText = 'padding-top:0.8rem;border-top:1px solid var(--border)';
      var oh = document.createElement('div');
      oh.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
      oh.textContent = 'Your documents';
      ownWrap.appendChild(oh);
      var ohHelp = document.createElement('div');
      ohHelp.style.cssText = 'font-size:0.74rem;color:var(--text-mute);line-height:1.45;margin-bottom:0.5rem';
      ohHelp.textContent = 'Files you’ve uploaded for this agent. Private to you — other users on the same agent don’t see them. Searched in RAG alongside any collections this agent has attached.';
      ownWrap.appendChild(ohHelp);
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
      ownWrap.appendChild(upRow);
      var ownList = document.createElement('div');
      ownList.style.cssText = 'display:flex;flex-direction:column;gap:0.3rem';
      ownWrap.appendChild(ownList);
      body.appendChild(ownWrap);

      function refreshOwn() {
        fetch('api/knowledge/sources').then(function(r){ return r.ok ? r.json() : null; })
          .then(function(d) {
            ownList.innerHTML = '';
            var sources = (d && d.sources) || [];
            if (sources.length === 0) {
              var emp = document.createElement('div');
              emp.style.cssText = 'font-size:0.74rem;color:var(--text-mute);font-style:italic';
              emp.textContent = '(no documents uploaded yet)';
              ownList.appendChild(emp);
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
              del.onclick = function() {
                if (!confirm('Remove ' + nm.textContent + ' from your documents?')) return;
                del.disabled = true;
                fetch('api/knowledge/sources/' + encodeURIComponent(s.id), {method: 'DELETE'})
                  .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t); }); refreshOwn(); })
                  .catch(function(err){ del.disabled = false; alert('Remove failed: ' + (err && err.message || err)); });
              };
              row.appendChild(nm); row.appendChild(meta); row.appendChild(del);
              ownList.appendChild(row);
            });
          });
      }
      refreshOwn();

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
          fetch('api/knowledge/upload', {
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
            refreshOwn();
          }).catch(function(err){
            upStatus.style.color = 'var(--danger,#ff7b72)';
            upStatus.textContent = 'Upload failed: ' + (err && err.message || err);
            upBtn.disabled = false;
          });
        };
        reader.readAsDataURL(f);
      };

      // (Auto-inferred section moved to the Memory modal — that
      // surface owns Reference Memory pruning, per-entry and bulk.
      // Knowledge modal stays focused on uploaded files.)

      var actions = document.createElement('div');
      actions.style.cssText = 'display:flex;gap:0.5rem;justify-content:flex-end;margin-top:0.8rem;padding-top:0.6rem;border-top:1px solid var(--border)';
      var close = document.createElement('button'); close.className = 'ui-row-btn primary'; close.textContent = 'Close';
      close.addEventListener('click', function(){ dlg.close(); dlg.remove(); });
      actions.appendChild(close);
      dlg.appendChild(actions);
      document.body.appendChild(overlay);
    });
  }
  register();
})();
</script>`
