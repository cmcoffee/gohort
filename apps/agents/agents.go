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
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/media"
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

// WebEveryUser lets any signed-in person past the coarse /agents gate. The
// real gate is per agent, on every route (LookupExposedAgent and
// AgentReachableBy in dispatch): a published agent still takes the admin's
// grant of its own path, a shared one its owner's list, and an agent of
// one's own its owner. The coarse gate only stood between an owner and the
// restricted chat their own dashboard card opens, and between a person and
// an agent shared with them by name, which AgentReachableBy says nobody but
// the owner decides.
func (T *AgentsApp) WebEveryUser() bool { return true }

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
	// Dictation on an agent's page: the transcriber is told that agent's
	// name, resolved the way the page itself is, among the agents this
	// viewer can reach.
	prefix := T.WebPath() + "/"
	media.RegisterDictationNames(func(p media.DictationPage) []string {
		orch := findOrchestrate()
		if orch == nil || p.Request == nil || !strings.HasPrefix(p.Path, prefix) {
			return nil
		}
		slug, _, _ := strings.Cut(strings.TrimPrefix(p.Path, prefix), "/")
		agent, entry, ok := orch.LookupExposedAgent(p.Request, slug)
		if !ok || !orch.AgentReachableBy(p.Request, entry.Slug, entry.Owner, agent.AllowedUsers, agent.Everyone) {
			return nil
		}
		return []string{orchestrate.ExposedDisplayName(agent), agent.Name}
	})
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
	// The lookup resolves among the agents THIS viewer can reach, so a
	// same-named agent somebody else owns can never shadow theirs.
	agent, entry, ok := orch.LookupExposedAgent(r, slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Per-agent access gate. A PUBLISHED agent takes both: the admin's grant of
	// this app is the ceiling, and the owner's own list narrows inside it. An
	// unpublished one is reachable by its AllowedUsers recipients and the owner,
	// with no admin involved. Without this, anyone who guessed the slug could
	// chat with every reachable agent regardless of visibility.
	//
	// The lookup already applied it; this re-check is the gate staying where a
	// reader of this handler expects it. Checked against the agent's canonical
	// slug, not the URL's: a plain slug that fell back to a suffixed agent must
	// not borrow the grant another agent holds on the plain path.
	if !orch.AgentReachableBy(r, entry.Slug, entry.Owner, agent.AllowedUsers, agent.Everyone) {
		http.NotFound(w, r) // 404 not 403 — don't leak slug existence
		return
	}
	switch {
	case rest == "" || rest == "/":
		T.handleChatPage(w, r, agent)
	case rest == "api/send":
		orch.PublicHandleSend(w, r, orchestrate.AppAgentOf(agent))
	case rest == "api/intake/options":
		// The intake form's live options ("Which one?"), fetched relative
		// to this page: the viewer's own things, as on the console.
		orch.PublicHandleIntakeOptions(w, r)
	case rest == "api/cancel":
		orch.PublicHandleCancel(w, r, orchestrate.AppAgentOf(agent))
	case rest == "api/inject":
		// A message typed while a turn is running joins it. Without this the
		// panel falls back to an ordinary send, which tears the running stream
		// down — read by whoever typed as the app cancelling their work
		// because they had a second thought.
		orch.PublicHandleInject(w, r)
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
	case rest == "api/ask-owner":
		// The recipient's route back. This surface is where somebody running an
		// agent they do not own actually lives, so the ask belongs here at
		// least as much as in the workbench.
		orch.PublicHandleAskOwner(w, r, agent)
	case rest == "api/facts":
		orch.PublicHandleAgentFacts(w, r, agent.ID)
	case rest == "api/notes":
		orch.PublicHandleAgentNotes(w, r, agent.ID)
	case rest == "api/memaudit":
		orch.PublicHandleAgentMemoryAudit(w, r, agent.ID)
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
	case rest == "api/memsearch":
		orch.PublicHandleAgentMemorySearch(w, r, agent.ID)
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

// findOrchestrate is the registered orchestrate app, or nil when it is not running.
func findOrchestrate() *orchestrate.OrchestrateApp {
	o, _ := FindAgentAs[*orchestrate.OrchestrateApp]("orchestrate")
	return o
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
	// The intake form is the console's own (orchestrate.IntakeFormHead), told
	// this page's one agent. A copy of its own here never learned steps or
	// live options, so Builder's form could not be filled in on this page.
	intakeHead := "<script>window.AGENT_INTAKE_FORM = " + intakeJSON + ";" +
		"window.GOHORT_AGENT_ID = " + string(agentIDJSON) + ";</script>" +
		orchestrate.IntakeFormHead +
		"<script>window.uiAgentIntakeSetAgent(window.AGENT_INTAKE_FORM ? {id: window.GOHORT_AGENT_ID, intake_form: window.AGENT_INTAKE_FORM} : null);</script>"
	// Private-mode toggle only renders when the agent's admin opted
	// in via AllowPrivateMode AND ForcePrivate isn't on. ForcePrivate
	// forces Private mode permanently, so showing a toggle would be
	// misleading (the user couldn't actually turn it off).
	var modes []ui.ChatMode
	if agent.AllowPrivateMode && !agent.ForcePrivate {
		modes = append(modes, ui.ChatMode{
			Label:     "Private",
			Icon:      "globe-off",
			Title:     "Private: no internet tools (web search, fetching pages), so this turn stays local. Stays on for later messages until you switch it off.",
			GetURL:    "api/settings/private",
			PostURL:   "api/settings/private/set",
			Field:     "private_mode",
			SendField: "private_mode",
		})
	}
	// "Fresh" suppresses the Reference Memory layer for this turn.
	// Hidden when the agent has DisableInferred set (nothing to control)
	// since the agent is already configured to never use that layer.
	// Otherwise any end-user gets the per-(user, agent) opt-in.
	if !agent.DisableInferred {
		modes = append(modes, ui.ChatMode{
			Label:     "Fresh",
			Icon:      "eraser",
			Title:     "Fresh: the agent answers without the notes it has gathered from past conversations. Your saved facts and uploaded documents still apply. Stays on for later messages until you switch it off.",
			GetURL:    "api/settings/memory",
			PostURL:   "api/settings/memory/set",
			Field:     "inferred_disabled",
			SendField: "inferred_disabled",
		})
	}
	// Toolbar actions, as buttons along the bar. Memory is offered only when the
	// agent actually has a memory layer to manage — when BOTH the Explicit
	// (facts + graph) and Reference layers are disabled, the Memory modal would be
	// empty, so the button is dropped entirely rather than opening to nothing.
	// Knowledge (uploads + shared base) and Copy session are independent of memory.
	var dashboardActions []ui.ToolbarAction
	if !(agent.DisableExplicit && agent.DisableInferred) {
		dashboardActions = append(dashboardActions, ui.ToolbarAction{
			Label: "Memory", Method: "client", URL: "agents_memory_modal",
			Title: "Review and prune the notes this agent has accumulated from your conversations.",
		})
	}
	dashboardActions = append(dashboardActions,
		ui.ToolbarAction{Label: "Knowledge", Method: "client", URL: "agents_knowledge_modal",
			Title: "Manage your private documents for this agent, review the agent's shared knowledge base, and wipe your accumulated corpus."},
		ui.ToolbarAction{Label: "Copy session", Method: "client", URL: "copy_session",
			Title: "Copy the full session as markdown (every user message, every assistant round, every tool call/result) for pasting into a prompt-tuning chat."},
	)
	// The route back to whoever owns this. Offered only on an agent that is not
	// yours, because on your own there is nobody to ask — you are the person
	// who would grant it. This surface is where a recipient of a shared agent
	// actually works, so it matters more here than in the workbench: everything
	// they might need (a collection it cannot read, a tool, a credential) is
	// behind a door only the owner opens, and until this there was no way to
	// knock on it from the page where they hit the wall.
	if owner := strings.TrimSpace(agent.Owner); owner != "" && owner != user {
		// Named on a peer share, "its owner" on a published one. An account
		// here is an email address, and a published agent reaches every
		// signed-in user, none of whom was told whose it is. See
		// orchestrate.OwnerLabel.
		who := orchestrate.OwnerLabel(agent, user)
		dashboardActions = append(dashboardActions,
			ui.ToolbarAction{Label: "Ask the owner", Method: "client", URL: "agents_ask_owner",
				Title: "Need something this agent cannot reach — a document collection, a tool, a credential? Ask " + who + ". It goes to their notifications; there is no reply here, so try again once they grant it."})
	}
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
		InjectURL:    "api/inject",
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
		// uploads, session export). Buttons in plain sight, one tap each: the bar
		// holds nothing else since the Private and Fresh switches moved into the
		// composer, so a "⋯" menu only hid them. What does NOT belong here is agent MANAGEMENT —
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
		ExtraHeadHTML: intakeHead + TranscribeRuntimeFlagScript() + media.ReadAloudScript() + media.DictationScript() + dashboardBarCSS + memoryModalScript + docsModalScript + askOwnerScript + blockedMarkScript +
			// A monitor wake, a scheduled report or a channel message drawn as
			// the console draws it: a titled card, not the wake's prompt in a
			// bubble from the owner.
			orchestrate.ReportCardHead,
	}
	page.ServeHTTP(w, r)
}

// dashboardBarCSS gives the dashboard app's top bar the same compact, transparent
// treatment list-top mode already ships — but applies it to rail-mode (Cortex)
// agents too, which otherwise keep the default boxy, lighter-bg ui-row-btn (the
// "square around each button" look). One horizontal row, any extra fields
// BEFORE the actions (CSS order), and the action buttons transparent and
// compact instead of standing out as boxes. App-scoped
// via ExtraHeadHTML so Agency / bridges / servitor layouts are untouched.
const dashboardBarCSS = `<style>
/* One uniform top bar: the dark background goes on the topbar itself (not the
   per-container bands), spanning the full width, with the extras + actions
   containers made transparent so they don't leave a gap where one ends. Extras
   render before the actions (CSS order), and the buttons are
   transparent and compact. Mirrors list-top mode for rail-mode Cortex agents.
   App-scoped via ExtraHeadHTML. */
.ui-agent-topbar {
  flex-direction: row !important; align-items: center; flex-wrap: wrap; gap: 0.4rem;
  background: var(--bg-1);
  border-bottom: 1px solid var(--border);
  padding: 0.3rem 0.7rem;
}
.ui-agent-extras-slot { order: 1; flex: 0 1 auto; }
/* The actions row is NOT a direct child of the
   topbar: it sits inside an unclassed wrapper the panel builds for the top
   span, and the order property only ranks siblings. So the rule below aimed at
   .ui-agent-actions never competed with the extras slot, and the bar came out
   with the actions first. Ordering the wrapper is what actually moves it. */
.ui-agent-topbar > div:not(.ui-agent-extras-slot) { order: 2; }
.ui-agent-actions {
  flex: 0 1 auto;
  background: transparent !important; border-bottom: 0 !important; padding: 0 !important;
  gap: 0.3rem; align-items: center;
}
.ui-agent-actions .ui-row-btn,
.ui-agent-actions button {
  padding: 0.2rem 0.55rem !important;
  font-size: var(--fs-xs, 0.75rem) !important;
  border-radius: var(--btn-radius, 6px) !important;
  background: transparent !important;
}
/* Compact on a desktop. Not on a touch screen, where they keep the
   framework's compact tap size: forcing 0 there made them 22px. */
@media not all and (pointer: coarse) {
  .ui-agent-actions .ui-row-btn,
  .ui-agent-actions button { min-width: 0 !important; min-height: 0 !important; }
}
</style>`

// memoryModalScript wires the toolbar's "Memory" client action to the
// shared editable Memory modal (Saved facts + Reference Memory + Graph
// Memory). The surface is defined once in orchestrate and parameterized;
// the public /agents app mounts it at the relative "api/" endpoints it
// already serves. Scoped to the end-user via /agents/<slug>/api/*; the
// calling user only ever sees their own per-(user, agent) memory.
// blockedMarkScript is the click behaviour behind the mark on a stopped turn.
//
// The mark itself is drawn by the panel: the server sets ChatMessage.Mark, so
// it renders inline before the reply and comes back on a reload without this
// script running at all. What belongs to the app is what CLICKING it does, and
// it arrives through the client-action registry like every other app behaviour.
//
// It sends the session id and whatever the person types. It asserts nothing
// about the rule: every word the owner reads about that is added server-side
// from their own block log.
const blockedMarkScript = `<script>
(function(){
  function register() {
    if (!window.uiRegisterClientAction) { setTimeout(register, 50); return; }
    window.uiRegisterClientAction('turn_blocked_report', function(ctx) {
      var session = (ctx && ctx.data && ctx.data.session) || (ctx && ctx.sessionId) || '';
      var btn = ctx && ctx.button;
      window.uiPrompt(
        'Tell the owner why this should not have been stopped. They will see which of their rules fired; they cannot see this conversation.',
        '',
        {multiline: true, rows: 3, ok: 'Send'}
      ).then(function(said) {
        if (said === null) return;
        if (btn) btn.disabled = true;
        fetch('api/ask-owner', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({request: said, session: session})
        }).then(function(r) {
          if (!r.ok) {
            return r.text().then(function(t) {
              var msg = (t || '').trim();
              throw new Error((r.status >= 400 && r.status < 500 && msg) ? msg : ('Could not send: ' + (msg || ('HTTP ' + r.status))));
            });
          }
          return r.json();
        }).then(function(res) {
          window.uiAlert((res && res.result) || 'Sent.');
        }).catch(function(e) {
          if (btn) btn.disabled = false;
          window.uiAlert(e.message || 'Could not send.');
        });
      });
    });
  }
  register();
})();
</script>`

var memoryModalScript = agenthost.AgentMemoryModalScript("agents_memory_modal", "'api/'")

// askOwnerScript powers the "Ask the owner" toolbar action, which is added only
// on an agent somebody else owns.
//
// The agent offers the same thing by tool when a turn hits a wall, and the
// model will reach for it; a button is for the person who has already decided
// to ask and should not have to phrase a sentence that makes a model choose a
// tool. Both go through api/ask-owner, so the daily cap and the wording cannot
// come apart between them.
const askOwnerScript = `<script>
(function(){
  function register() {
    if (!window.uiRegisterClientAction) { setTimeout(register, 50); return; }
    window.uiRegisterClientAction('agents_ask_owner', function(ctx) {
      window.uiPrompt(
        'What do you need from the owner of this agent? Name the thing it could not reach, since they cannot see this conversation.',
        '',
        {multiline: true, rows: 4, ok: 'Send',
         placeholder: 'e.g. it cannot read the Runbooks collection'}
      ).then(function(request) {
        if (request === null) return; // cancelled
        if (!String(request).trim()) { window.uiAlert('Say what you need, or there is nothing to send.'); return; }
        fetch('api/ask-owner', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({request: request})
        }).then(function(r) {
          if (!r.ok) {
            return r.text().then(function(t) {
              var msg = (t || '').trim();
              throw new Error((r.status >= 400 && r.status < 500 && msg) ? msg : ('Could not send: ' + (msg || ('HTTP ' + r.status))));
            });
          }
          return r.json();
        }).then(function(d) {
          // The server's own sentence: it knows whether it sent or was capped,
          // and a fixed "Sent" would be wrong half the time.
          window.uiAlert((d && d.result) || 'Sent.');
        }).catch(function(e) {
          window.uiAlert(e.message || 'Could not send.');
        });
      });
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
      // Shared modal chrome via uiOpenModal (plain overlay, mobile-safe,
      // Escape-to-close, default Close button, no backdrop-close). This modal
      // only needs a Close button, so we let uiOpenModal supply it.
      var body = window.uiOpenModal({ title: 'Knowledge', width: '640px' }).body;

      // (Shared / admin-curated docs removed: the agent's reference
      // corpus now lives in Collections attached via the editor.)

      // --- Your documents (editable) ---
      var ownWrap = document.createElement('div');
      ownWrap.style.cssText = 'padding-top:0.8rem;border-top:1px solid var(--border)';
      var oh = document.createElement('div');
      oh.style.cssText = 'font-weight:600;color:var(--text);margin-bottom:0.3rem';
      oh.textContent = 'Your documents';
      ownWrap.appendChild(oh);
      var ohHelp = document.createElement('div');
      ohHelp.style.cssText = 'font-size:var(--fs-xs, 0.74rem);color:var(--text-mute);line-height:1.45;margin-bottom:0.5rem';
      ohHelp.textContent = 'Files you’ve uploaded for this agent. Private to you: other users on the same agent don’t see them. Searched in RAG alongside any collections this agent has attached.';
      ownWrap.appendChild(ohHelp);
      var upRow = document.createElement('div');
      upRow.style.cssText = 'display:flex;align-items:center;gap:0.5rem;margin-bottom:0.5rem;flex-wrap:wrap';
      var upInp = document.createElement('input'); upInp.type = 'file';
      // Must cover every format core/media/document_extract.go handles; a
      // narrower picker makes a supported file look unsupported.
      upInp.accept = '.pdf,.docx,.doc,.txt,.md,.log,.csv,.json,.yaml,.yml,.html,.htm,application/pdf,application/vnd.openxmlformats-officedocument.wordprocessingml.document,application/json,text/plain,text/markdown,text/csv,text/html';
      upInp.style.cssText = 'flex:1;min-width:0;font-size:var(--fs-sm, 0.8rem)';
      var upBtn = document.createElement('button'); upBtn.type = 'button'; upBtn.className = 'ui-row-btn primary';
      upBtn.textContent = 'Upload';
      upBtn.disabled = true;
      upInp.addEventListener('change', function(){ upBtn.disabled = !(upInp.files && upInp.files[0]); });
      var upStatus = document.createElement('span');
      upStatus.style.cssText = 'font-size:var(--fs-2xs, 0.72rem);color:var(--text-mute)';
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
              emp.style.cssText = 'font-size:var(--fs-xs, 0.74rem);color:var(--text-mute);font-style:italic';
              emp.textContent = '(no documents uploaded yet)';
              ownList.appendChild(emp);
              return;
            }
            sources.forEach(function(s) {
              var row = document.createElement('div');
              row.style.cssText = 'display:flex;align-items:center;gap:0.5rem;padding:0.3rem 0.5rem;background:var(--bg-0);border:1px solid var(--border);border-radius:4px;font-size:var(--fs-sm, 0.8rem)';
              var nm = document.createElement('span');
              nm.style.cssText = 'flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap';
              nm.textContent = (s.name || s.id || '(unnamed)').replace(/^#+\s*/, '');
              nm.title = nm.textContent;
              var meta = document.createElement('span');
              meta.style.cssText = 'color:var(--text-mute);font-size:var(--fs-2xs, 0.7rem)';
              meta.textContent = (s.chunks || 0) + ' chunk' + (s.chunks === 1 ? '' : 's');
              var del = document.createElement('button'); del.type = 'button'; del.className = 'ui-row-btn danger ui-btn-sm';
              del.textContent = 'Remove';
              del.onclick = function() {
                window.uiConfirm('Remove ' + nm.textContent + ' from your documents?').then(function(ok) {
                  if (!ok) return;
                  del.disabled = true;
                  fetch('api/knowledge/sources/' + encodeURIComponent(s.id), {method: 'DELETE'})
                    .then(function(r){ if (!r.ok) return r.text().then(function(t){ throw new Error(t); }); refreshOwn(); })
                    .catch(function(err){ del.disabled = false; window.uiAlert('Remove failed: ' + (err && err.message || err)); });
                });
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

      // (Auto-inferred section moved to the Memory modal, that
      // surface owns Reference Memory pruning, per-entry and bulk.
      // Knowledge modal stays focused on uploaded files.)

      // Footer: uiOpenModal supplies the default Close button.
    });
  }
  register();
})();
</script>`
