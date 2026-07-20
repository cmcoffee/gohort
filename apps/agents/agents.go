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
	return "Apps: Public agents published by an admin via Agents."
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
//	/                            — directory (lists exposed agents)
//	/<slug>/                     — chat page
//	/<slug>/api/send             — SSE chat (resolves slug, dispatches)
//	/<slug>/api/cancel           — abort in-flight round
//	/<slug>/api/sessions         — list user's sessions for this agent
//	/<slug>/api/sessions/<sid>   — load / delete / truncate one session
//	/<slug>/api/memory           — read / replace this user's memory under the agent
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
	agent, owner, ok := orch.LookupExposedAgent(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Per-agent access gate — a published agent is a normal app (admins + app-access
	// grants); a peer-shared agent is reachable by its AllowedUsers recipients (or
	// owner). Without this, anyone who guessed the slug could chat with every
	// reachable agent regardless of visibility.
	if !orch.AgentReachableBy(r, slug, owner, agent.AllowedUsers) {
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
	case rest == "api/runs/active":
		orch.PublicHandleRunsActive(w, r)
	case strings.HasPrefix(rest, "api/runs/"):
		orch.PublicHandleRunsDispatch(w, r)
	case rest == "api/channel/clear":
		orch.PublicHandleChannelClear(w, r, agent.ID)
	case rest == "api/sessions":
		orch.PublicHandleSessionList(w, r, agent.ID)
	case strings.HasPrefix(rest, "api/sessions/"):
		orch.PublicHandleSessionOne(w, r, agent.ID, strings.TrimPrefix(rest, "api/sessions/"))
	case rest == "api/facts":
		orch.PublicHandleAgentFacts(w, r, agent.ID)
	case rest == "api/graph":
		orch.PublicHandleAgentGraph(w, r, agent.ID)
	case rest == "api/graph/edge":
		orch.PublicHandleAgentGraphEdgeDelete(w, r, agent.ID)
	case strings.HasPrefix(rest, "api/graph/entity/"):
		sub := strings.TrimPrefix(rest, "api/graph/entity/")
		switch {
		case strings.HasSuffix(sub, "/attr"):
			orch.PublicHandleAgentGraphAttrDelete(w, r, agent.ID, strings.TrimSuffix(sub, "/attr"))
		case strings.HasSuffix(sub, "/alias"):
			orch.PublicHandleAgentGraphAliasDelete(w, r, agent.ID, strings.TrimSuffix(sub, "/alias"))
		default:
			orch.PublicHandleAgentGraphEntityDelete(w, r, agent.ID, sub)
		}
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
	// Cortex agents do NOT pin dashboard visitors to a Cortex/home thread: the
	// Cortex is the agent's MIND and lives in admin-only Agency, not on this
	// consumption surface. Granted users (and the owner viewing their own card)
	// get ordinary ad-hoc chat sessions here. Each NEW session is still SEEDED
	// from the Cortex's standing awareness at turn time (runPlan's
	// cortexContextBlock) — the owner's real Cortex when "Share Cortex awareness"
	// is on, else the visitor's own namespace — so the agent shows up already
	// aware without anyone being able to open/read/manage the thread itself.
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
	// Toolbar actions (all in the "⋯" overflow). Memory is offered only when the
	// agent actually has a memory layer to manage — when BOTH the Explicit
	// (facts + graph) and Reference layers are disabled, the Memory modal would be
	// empty, so the button is dropped entirely rather than opening to nothing.
	// Knowledge (uploads + shared base) and Copy session are independent of memory.
	var dashboardActions []ui.ToolbarAction
	if !(agent.DisableExplicit && agent.DisableInferred) {
		dashboardActions = append(dashboardActions, ui.ToolbarAction{
			Label: "Memory", Group: "⋯", Method: "client", URL: "agents_memory_modal",
			Title: "Review and prune the notes this agent has accumulated from your conversations.",
		})
	}
	dashboardActions = append(dashboardActions,
		ui.ToolbarAction{Label: "Knowledge", Group: "⋯", Method: "client", URL: "agents_knowledge_modal",
			Title: "Manage your private documents for this agent, review the agent's shared knowledge base, and wipe your accumulated corpus."},
		ui.ToolbarAction{Label: "Copy session", Group: "⋯", Method: "client", URL: "copy_session",
			Title: "Copy the full session as markdown — every user message, every assistant round, every tool call/result — for pasting into a prompt-tuning chat."},
	)
	panel := ui.AgentLoopPanel{
		ListURL:     "api/sessions",
		LoadURL:     "api/sessions/{id}",
		DeleteURL:   "api/sessions/{id}",
		TruncateURL: "api/sessions/{id}",
		ListTitle:   "Sessions",
		NewLabel:    "New session",
		// Drives sessions + New into the topbar as
		// dropdown-style buttons; no persistent rail.
		// Matches the classic chat-app surface where
		// one conversation owns the whole pane.
		ListPosition: "top",
		SendURL:      "api/send",
		CancelURL:    "api/cancel",
		// Run-stream reconnect: if the live /api/send socket drops mid-turn (a long
		// RAG turn over a flaky link), the panel resumes from the run buffer instead
		// of silently losing the reply until reload. Matches the admin console.
		RunsURLBase:   "api/runs/",
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
		// Per-visitor surfaces — the granted user managing THEIR OWN data (notes,
		// uploads, session export). These collapse into a single "⋯" overflow that
		// sits AFTER the Private / Clean mode toggles (the dashboardBarCSS order
		// rule), so the bar reads [Private] [Clean] [⋯] and the page lands you
		// straight in the chat. What does NOT belong here is agent MANAGEMENT —
		// config lives in admin-only Agency, never on the dashboard surface.
		Actions: dashboardActions,
		Modes:   modes,
	}

	// (Cortex agents intentionally get NO special dashboard layout: no pinned
	// home thread, no rail channel box, no alt-nav, no "Manage ▾". The Cortex
	// is the agent's mind and is reached only from Agency; here every published
	// agent — Cortex or not — uses the same ordinary topbar-dropdown sessions.
	// New sessions still seed from the Cortex at turn time, see intakeHead above.)

	page := ui.Page{
		Title:     display,
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "100%",
		Sections: []ui.Section{
			{
				NoChrome: true,
				Body:     panel,
			},
		},
		ExtraHeadHTML: intakeHead + TranscribeRuntimeFlagScript() + dashboardBarCSS + memoryModalScript + docsModalScript,
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

// dashboardBarCSS gives the dashboard app's top bar the same compact, transparent
// treatment list-top mode already ships — but applies it to rail-mode (Cortex)
// agents too, which otherwise keep the default boxy, lighter-bg ui-row-btn (the
// "square around each button" look). One horizontal row, the mode toggles
// (Private / Clean) BEFORE the actions / ⋯ overflow (CSS order), and the action
// buttons + the ⋯ toggle styled like the mode pills (transparent, compact, same
// padding/font/radius) instead of standing out as boxes. App-scoped via
// ExtraHeadHTML so Agency / bridges / servitor layouts are untouched.
const dashboardBarCSS = `<style>
/* One uniform top bar: the dark background goes on the topbar itself (not the
   per-container bands), spanning the full width, with the modes + actions
   containers made transparent so they don't leave a gap where one ends. Modes
   (Private / Clean) render before the actions / ⋯ overflow (CSS order), and the
   buttons match the mode pills (transparent, compact). Mirrors list-top mode for
   rail-mode Cortex agents. App-scoped via ExtraHeadHTML. */
.ui-agent-topbar {
  flex-direction: row !important; align-items: center; flex-wrap: wrap; gap: 0.4rem;
  background: var(--bg-1);
  border-bottom: 1px solid var(--border);
  padding: 0.3rem 0.7rem;
}
.ui-agent-extras-slot { order: 1; flex: 0 1 auto; }
.ui-agent-modes { background: transparent !important; border-top: 0 !important; padding: 0 !important; }
.ui-agent-actions {
  order: 2; flex: 0 1 auto;
  background: transparent !important; border-bottom: 0 !important; padding: 0 !important;
  gap: 0.3rem; align-items: center;
}
.ui-agent-actions .ui-row-btn,
.ui-agent-actions button {
  min-width: 0 !important; min-height: 0 !important;
  padding: 0.2rem 0.55rem !important;
  font-size: 0.75rem !important;
  border-radius: 6px !important;
  background: transparent !important;
}
</style>`

// memoryModalScript wires the toolbar's "Memory" client action to the
// shared editable Memory modal (Saved facts + Reference Memory + Graph
// Memory). The surface is defined once in orchestrate and parameterized;
// the public /agents app mounts it at the relative "api/" endpoints it
// already serves. Scoped to the end-user via /agents/<slug>/api/*; the
// calling user only ever sees their own per-(user, agent) memory.
var memoryModalScript = orchestrate.AgentMemoryModalScript("agents_memory_modal", "'api/'")

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
      // Shared modal chrome via uiOpenModal (plain overlay, mobile-safe,
      // Escape-to-close, default Close button, no backdrop-close). This modal
      // only needs a Close button, so we let uiOpenModal supply it.
      var body = window.uiOpenModal({ title: 'Knowledge', width: '640px' }).body;

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

      // Footer: uiOpenModal supplies the default Close button.
    });
  }
  register();
})();
</script>`
