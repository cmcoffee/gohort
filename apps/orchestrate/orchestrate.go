// Package orchestrate is the Agent Builder. Users define Agents
// (persona + prompts + plan style + tool surface) and chat with them.
// Each agent gets its own chat surface — a split-pane AgentLoopPanel
// showing orchestrator-user conversation on one side and live plan +
// worker activity on the other.
//
// Plan-driven flow per user turn:
//
//  1. Orchestrator (thinking LLM) reads the user message + history,
//     calls plan_set with N step titles.
//  2. Activity pane renders the plan.
//  3. For each step: orchestrator delegates to the worker (no-think
//     LLM); worker output goes to the activity pane; step flips to
//     done.
//  4. Orchestrator composes a synthesis reply for the user.
//
// Plans auto-confirm — no operator pause between steps in Phase 1.
//
// Agents declare an explicit AllowedTools allowlist of worker tool
// names; empty/nil means "use the default pool" (every non-blocked
// chat tool with Read or Network capability). Editor checklist drives
// the allowlist; runner enforces it strictly.
//
// Storage layout (all kvlite-backed):
//
//	orchestrate_agents              — keyed by agent ID
//	orchestrate_sessions:<agent_id> — one bucket per agent, keyed by session ID
//	orchestrate_design_sessions     — Design-with-AI chat history
//	orchestrate_migrations          — per-user one-time migration markers
package orchestrate

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	app := new(OrchestrateApp)
	RegisterApp(app)
	// Personal preferences hook on /account: the Default agent setting
	// (account_prefs.go). Registered against the same instance so the
	// builder reads T.DB at request time, after startup wires it.
	RegisterAccountSection(AccountSectionEntry{Build: app.accountSection})
	// The temp-tool-approval hook (enableApprovedToolOnSeedChat) is left
	// UNWIRED on purpose. seed-chat now uses AllowedTools=nil (the auto-
	// include sentinel) plus DisabledPersistentTools as the only opt-out,
	// so every newly approved tool appears automatically at runtime and in
	// the Tools modal — there's nothing for an approval hook to persist.
	// The function is kept for its deny-list diagnostic, not registered.
	RegisterRouteStage(RouteStage{
		Key:     "app.orchestrate.orchestrator",
		Label:   "Agents: Orchestrator (thinking)",
		Default: "worker (thinking)",
		Group:   "Agents",
		Private: true,
	})
	RegisterRouteStage(RouteStage{
		Key:   "app.orchestrate.builder",
		Label: "Agents: Builder design (escalates to lead)",
		// Builder is low-volume + high-leverage — its agent-design reasoning
		// (decomposition, tool/credential design) benefits most from the lead
		// model, and a well-built agent saves many downstream worker turns. NOT
		// Private (unlike the other Agents stages): the admin can flip it back to
		// worker for a fully-local build flow, and it degrades to worker
		// automatically when no lead model is configured. Only the design /
		// synthesis reasoning routes here; the dispatched plan_set worker phases
		// stay on app.orchestrate.worker.
		Default: "lead",
		Group:   "Agents",
	})
	RegisterRouteStage(RouteStage{
		Key:   "app.orchestrate.orchestrator.lead",
		Label: "Agents: Agent reasoning (lead-escalated agents)",
		// Per-agent opt-in: agents with "Use Lead model" enabled route their
		// main reasoning (plan + synthesis) here instead of the worker-locked
		// orchestrator stage. NOT Private, so it can escalate; admin can flip
		// it back to worker as a global ceiling, and it degrades to worker
		// automatically when no lead model is configured. The dispatched
		// plan_set worker phases stay on app.orchestrate.worker regardless.
		Default: "lead",
		Group:   "Agents",
	})
	RegisterRouteStage(RouteStage{
		Key:   ConsultRouteKey,
		Label: "Agents: Consultation (one-shot advice)",
		// The inverse of escalating a whole turn: the worker keeps the loop
		// and asks the lead ONE self-contained question — an API's request
		// shape, a wall it has hit three times. The call carries the question
		// and its evidence and nothing else (no tool catalog, no history), so
		// it costs a few thousand tokens rather than the turn's full prompt,
		// and there is no long context for a weaker lead to lose track of.
		// Answering is what leads are reliably good at; acting is where they
		// stall. NOT Private — it must be able to escalate; degrades to worker
		// automatically when no lead is configured.
		Default: "lead",
		Group:   "Agents",
	})
	RegisterRouteStage(RouteStage{
		Key:     "app.orchestrate.worker",
		Label:   "Agents: Worker (no-think)",
		Default: "worker",
		Group:   "Agents",
		Private: true,
	})
	RegisterRouteStage(RouteStage{
		Key:     "app.orchestrate.consolidator",
		Label:   "Agents: Memory consolidator (background)",
		Default: "worker",
		Group:   "Agents",
		Private: true,
	})
	RegisterRouteStage(RouteStage{
		Key:     "app.orchestrate.gap_check",
		Label:   "Agents: Gap detection (post-plan review)",
		Default: "worker (thinking)",
		Group:   "Agents",
		Private: true,
	})
	RegisterRouteStage(RouteStage{
		Key:     "app.orchestrate.suggest",
		Label:   "Agents: Editor field-suggest",
		Default: "worker",
		Group:   "Agents",
		Private: true,
	})
	RegisterRouteStage(RouteStage{
		Key:     "app.orchestrate.title",
		Label:   "Agents: Session title summarizer (background)",
		Default: "worker",
		Group:   "Agents",
		Private: true,
	})
}

// OrchestrateApp is the entry point. Dashboard-only (no CLI Main).
type OrchestrateApp struct {
	AppCore

	// Runs is the in-memory ledger of in-flight + recently-completed
	// agent turns (see runs.go). Lazily initialized on first access
	// via runsRegistry() so handlers don't have to care whether the
	// app was bootstrapped through Init() or a hot-reload path that
	// skipped it.
	runsOnce sync.Once
	runs     *RunRegistry
}

// runsRegistry returns the lazily-initialized RunRegistry. Goroutine-
// safe; first caller wins, everyone else gets the same instance.
func (T *OrchestrateApp) runsRegistry() *RunRegistry {
	T.runsOnce.Do(func() {
		T.runs = NewRunRegistry()
	})
	return T.runs
}

func (T *OrchestrateApp) Name() string         { return "orchestrate" }
func (T *OrchestrateApp) SystemPrompt() string { return "" }
func (T *OrchestrateApp) Desc() string {
	return "Apps: Agents — define and run plan-driven AI agents."
}

func (T *OrchestrateApp) Init() error { return T.Flags.Parse() }

func (T *OrchestrateApp) Main() error {
	Log("Agents is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

func (T *OrchestrateApp) WebPath() string { return "/orchestrate" }

// HubTab makes the Agents workbench a member of the shared top-nav hub.
func (T *OrchestrateApp) HubTab() (string, int) { return "Agents", 10 }
func (T *OrchestrateApp) WebName() string       { return "Agents" }
func (T *OrchestrateApp) WebDesc() string {
	return "Build, manage, and dispatch your fleet of AI agents."
}

// WebOrder pins Agents to the top of the dashboard card grid above
// every other app. Lower number = earlier; -1000 leaves room for
// other future "top-of-list" apps without needing to bump this.
func (T *OrchestrateApp) WebOrder() int { return -1000 }

// WebFeatured marks Agents as the primary entry point. Because Agents is a
// hub member it renders inside the "Orchestrator" cluster as an equal-sized
// card (the cluster title carries the grouping), so this flag no longer blows
// it up into a full-width hero — it only WOULD if Agents ever stopped being a
// hub member, in which case it'd hero on its own. Kept to document intent.
func (T *OrchestrateApp) WebFeatured() bool { return true }

// WebRestricted hides Orchestrate from non-admin users on the landing
// page. Orchestrate is the agent workbench (CRUD, prompts, allowlists,
// memory pruning); end-users consume the agents an admin builds via
// the per-agent exposed-app surface. Same pattern as the Admin app.
func (T *OrchestrateApp) WebRestricted(r *http.Request) bool {
	if T.DB == nil {
		return true
	}
	if !AuthHasUsers(T.DB) {
		// Single-user / auth-disabled deployment — no admin concept,
		// so don't gate.
		return false
	}
	return !AuthIsAdmin(T.DB, r)
}

// Routes wires all of orchestrate's surfaces in one place. The chat
// page sits at the root with an agent dropdown that drives the active
// context (servitor's appliance-picker pattern); session URLs carry
// `agent_id` in their query string so switching the picker re-scopes
// the conversation rail.
//
//	/                          — chat page (agent dropdown)
//	/agent/new                 — agent editor (new)
//	/agent/{id}                — agent editor (edit)
//	/api/agents                — list / create
//	/api/agents/{id}           — read / update / delete one
//	/api/agents/{id}/clone     — POST: clone into a new agent
//	/api/agents/{id}/export    — GET: download the agent as a portable JSON recipe
//	/api/agents/{id}/facts     — GET/POST: Explicit Memory facts (was /memory)
//	/api/agents/import         — POST: create a new agent from an uploaded recipe
//	/api/agents/suggest        — POST: ✨ per-field AI suggestion for the editor
//	/api/agents/wizard         — POST: guided create (drafts prompt from the brief)
//	/api/sessions              — list (agent_id query param)
//	/api/sessions/{sid}        — load / delete (agent_id query param)
//	/api/send                  — SSE send (agent_id in body)
//	/api/cancel                — abort in-flight round (by ?id=<session>; agent_id optional)
//	/api/confirm               — operator confirm (Phase 1 no-op stub)
//	/api/inject                — POST/PATCH/DELETE: mid-flight note queue
func (T *OrchestrateApp) Routes() {
	// Wire the scheduled-update handler before any session runs —
	// the scheduler fires async so orchRef must be set by the time
	// the loop ticks.
	registerOrchestrateScheduledUpdates(T)

	// Wire the standing-agent runner: core owns the schedule + run-ledger;
	// this supplies the agent-execution closure (loads the agent and runs it
	// with a deny-by-default confirm so unattended runs never auto-approve
	// high-consequence tools).
	registerStandingRunner(T)

	// Register "agent" as a portable artifact type so agents export/import via
	// the unified bundle (Admin > /api/artifacts/*). Holds the app because
	// agents live in per-user stores (UserDB(T.DB, owner)), not RootDB.
	RegisterAgentArtifactType(T)

	// Register "pipeline" alongside it — pipeline defs live in the same
	// per-user stores, and the two types close each other's references: an
	// agent's AttachedPipelines closure needs the pipeline type, a pipeline's
	// agent stages need the agent type.
	RegisterPipelineArtifactType(T)

	// Agent-name resolution seam for core-side artifact types: the custom-app
	// recipe normalizes its bound AgentID to the agent's NAME on export (an
	// imported agent is reborn under a fresh ID; only the name survives).
	// Core can't reach the per-user agent store, so orchestrate supplies the
	// resolver.
	// Base store for free functions that must resolve a per-user agent from the
	// SAME store the editor writes to (UserDB(T.DB, owner)) — NOT RootDB, which
	// is a different bucket. Used by the outbound name-tag resolver.
	orchestrateBaseDB = T.DB
	// Credential → declaring-tools resolver for the admin credential UI (it
	// can't reach agent records itself). Orchestrate scans agents + pools.
	CredentialToolsResolver = credentialTools
	// Fire-path dependency guards: a monitor / standing agent whose agent,
	// credential, or tool vanished is marked broken (paused + kept) instead of
	// firing into the void.
	wireDependencyGuards()
	ResolveAgentNameForExport = func(owner, key string) (string, bool) {
		udb := UserDB(T.DB, owner)
		if udb == nil {
			return "", false
		}
		a, ok := findAgentByNameOrID(udb, owner, key)
		if !ok || strings.TrimSpace(a.Name) == "" {
			return "", false
		}
		return a.Name, true
	}

	// Channel agent runner: lets the transport (phantom) run a Channel's bound
	// agent on inbound messages (Phase 2). core owns the Channel store + seam;
	// this registers the agent-execution half. See docs/channels-and-agents.md.
	registerChannelAgentRunner(T)
	// Channel wake-rule gatekeeper: the transport calls this before dispatching
	// an inbound, so master (admin) + per-channel rules gate the agent run.
	registerChannelGatekeeper(T)
	// Cross-scope tool enumeration (agent-bundled + session drafts) for
	// surfaces outside this app (Extensions > My tools). Only this app can map
	// agents and sessions to a user.
	registerScopedToolLister(T)
	// Authored tools commit onto the requesting agent's record. Wired here
	// because only this app owns agent records.
	AttachToolToAgent = func(db Database, owner, agentID string, t TempTool) error {
		return bundleAgentToolByID(agentUserDB(db, owner), owner, agentID, t)
	}
	// FindUserAgentTool lets Builder's update path resolve a tool that lives on
	// ANOTHER of the user's agents (its own resolver only sees the user-wide pool
	// + its own tools). Scans every agent's own record; returns the first match
	// and the agent that holds it. App agents are skipped — their kit is
	// app-declared, not editable here.
	FindUserAgentTool = func(db Database, owner, name string) (TempTool, string, bool) {
		name = strings.TrimSpace(name)
		if name == "" {
			return TempTool{}, "", false
		}
		udb := agentUserDB(db, owner)
		for _, rec := range listAgents(udb, owner) {
			if isAppAgent(rec.ID) {
				continue
			}
			for _, t := range rec.Tools {
				if t.Name == name {
					return t, rec.ID, true
				}
			}
		}
		return TempTool{}, "", false
	}
	// ListUserAgentTools enumerates every tool bundled to any of the user's
	// agents — the collision guard checks proposed names against these so the
	// tool namespace is unique across the whole user, not just per session.
	ListUserAgentTools = func(db Database, owner string) []TempTool {
		udb := agentUserDB(db, owner)
		var out []TempTool
		for _, rec := range listAgents(udb, owner) {
			if isAppAgent(rec.ID) {
				continue
			}
			out = append(out, rec.Tools...)
		}
		return out
	}
	// Confirming a trial tool is the user vouching for it: the tool does not
	// move or change, only the "nobody has looked at this" mark clears.
	ConfirmAgentTool = func(db Database, owner, agentID, toolName string) error {
		udb := agentUserDB(db, owner)
		rec, ok := loadAgent(udb, agentID)
		if !ok {
			return fmt.Errorf("agent %q not found", agentID)
		}
		for i := range rec.Tools {
			if rec.Tools[i].Name == toolName {
				if !rec.Tools[i].Trial {
					return nil // already confirmed; nothing to do
				}
				rec.Tools[i].Trial = false
				rec.Tools[i].TrialSince = time.Time{}
				_, err := saveAgent(udb, rec)
				return err
			}
		}
		return fmt.Errorf("agent %q has no tool named %q", rec.Name, toolName)
	}
	// Recorded-only mirror: when the gatekeeper BLOCKS an inbound (no wake), the
	// transport calls this to append the message into the bound agent's own
	// transcript, so it shows in the agent's chat and is in-context on the next
	// wake — the agent reads along even while it stays silent.
	registerChannelSilentRecorder(T)
	// Reply overflow: when a bidirectional channel is bound to a service with no
	// output path, the transport routes the agent's undeliverable reply here — into
	// the agent's cortex feed — instead of stranding it in an outbox nothing drains.
	RegisterChannelOverflow(T.overflowChannelReply)
	// MCP agent gate: the inbound MCP server (ask_agent) calls this so only agents
	// the owner marked "Reachable over MCP" can be dispatched from an external
	// client. Reads the owner's own store; seeds resolve to their per-user shadow.
	RegisterMCPAgentGate(func(owner, agentID string) bool {
		// Agents live in the orchestrate app's per-user store (UserDB(T.DB, owner)),
		// NOT RootDB — read the SAME store the editor saves to, or every lookup
		// falls back to the in-code seed (MCPExposed=false) and nothing is reachable.
		// Name-or-id, matching the dispatch path's own resolution; reachability
		// covers app agents via their feature grant (see externallyReachable).
		a, ok := findAgentByNameOrID(UserDB(T.DB, owner), owner, agentID)
		return ok && externallyReachable(a, owner)
	})
	// Canonical external resolution for the MCP server: name-or-id → agent ID,
	// only when reachable. mcpserver needs the ID (not the caller's raw string)
	// so its per-app KEY gate can identify app agents.
	ResolveExternalAgentFn = func(db Database, owner, key string) (string, bool) {
		return ResolveExternalAgent(db, owner, key)
	}

	// Wire the event-monitor engine: webhook + poll triggers that WAKE a
	// channel agent (inject into its home thread + run a turn) when something
	// happens. core owns the store + poll schedule; this supplies the waker
	// (run the monitor's WakeAgent on its channel) and the poller (run a
	// checker agent).
	registerOperatorWake(T)

	// Channel console: agents with Channel on (Chat is the primary one) get a
	// channel sidebar — a home-thread row + the fleet/monitor/authorization
	// management box — reusing the agent runtime for chat + the shared core
	// spine for the data panels.
	T.registerConsoleRoutes()

	// One-shot Builder shadow migration. Walks every user's store,
	// finds existing seed-builder shadows, re-writes them with the
	// current in-code seed (preserving only the shadow's Rules).
	// Lazy loadAgent already does this overlay at read time; this
	// just eagerly persists the result so the DB state matches what
	// callers see. Idempotent — running it twice produces the same
	// record.
	T.migrateBuilderShadows()

	// One-shot reset of seed-chat shadows whose AllowedTools were frozen by
	// the old enableApprovedToolOnSeedChat expansion path. Restores the
	// default-pool sentinel so toolbox-mode tools and other non-standard
	// approvals are no longer silently blocked.
	T.migrateSeedChatFrozenAllowedTools()

	// One-shot removal of the retired Operator seed. It folded into Chat
	// (seed-chat) and is gone from seedAgents(); this deletes any stale
	// per-user shadow (record + old operator-thread + side data) so it
	// stops appearing in the agent menu. Idempotent.
	T.dropLegacyOperator()

	// One-shot persistent-tool snapshot migration. Walks every user's
	// agents and, for any AllowedTools name that resolves only to the
	// persistent pool (not already in agent.Tools[]), snapshots it
	// into the agent record. Closes the door on the old reference-by-
	// name model where admin pool cleanup silently broke agents.
	T.migrateAgentPersistentTools()

	// One-shot: rewrite legacy Mode=="orchestrator" agents into the split
	// Cortex + Fleet flags + clear the marker, so a legacy cortex agent's
	// Fleet flag can actually be turned off (and the agent published).
	T.migrateLegacyOrchestratorMode()

	// One-shot deploy-time grandfather for the global-tool OPT-IN model. Shared
	// tools used to auto-load for everyone; now they load only after a user
	// adopts them. This seeds every EXISTING user's adoption list with the
	// current shared-tool names so nothing they relied on silently disappears.
	// Guarded by a deployment marker; users created afterward start empty.
	T.migrateGlobalToolAdoption()

	// One-shot fact-store migration. Walks core_facts rows and
	// converts the old keyed shape ({Namespace, Key, Value, ...})
	// to the new flat shape ({Namespace, ID, Note, Created}),
	// deduping similar entries on the way. Idempotent — flat
	// rows are skipped. Runs per-user (each user's fact store
	// lives in their per-user sub-DB).
	if authDB := AuthDB(); authDB != nil {
		for _, u := range AuthListUsers(authDB) {
			if udb := UserDB(T.DB, u.Username); udb != nil {
				MigrateLegacyFactStore(udb)
			}
		}
	}

	// Wraps every handler with an admin gate. Non-admin requests get
	// a 403 instead of being silently allowed to a hidden surface —
	// the WebRestricted check above only hides the landing-page
	// card; direct URLs need their own gate. WebRestricted is a
	// LANDING-page concept; handler gating is the actual ACL.
	g := T.adminGated
	T.HandleFunc("/", g(T.handleRoot))
	T.HandleFunc("/agent/", g(T.handleAgentPage))

	T.HandleFunc("/api/agents", g(T.handleAgentList))
	// Candidate user list for the agent-page "Share with users" ACLPicker (peer
	// sharing). Any authenticated user may share their own agent.
	T.HandleFunc("/api/user-candidates", g(T.handleUserCandidates))
	// Agent-centric tier-2 credential scoping — which of the user's granted
	// credentials this agent may use (writes AgentRecord.DisabledCredentials).
	T.HandleFunc("/api/agent-credentials", g(T.handleAgentCredentials))
	// Per-tool scope pills (Tools modal): Global + per-agent toggles.
	T.HandleFunc("/api/tool-scope", g(T.handleToolScope))
	// Grouped agent-picker options (Built-in / Conversation Agents / Specialized
	// Agents / per-app) so the client rebuilds the dropdown with the SAME
	// separators the initial paint used — a group-less /api/agents rebuild
	// collapsed them to Built-in/Custom after every Builder action.
	T.HandleFunc("/api/agent-options", g(T.handleAgentPickerOptions))
	T.HandleFunc("/api/capabilities", g(T.handleAgentCapabilities))
	T.HandleFunc("/api/channels", g(T.handleChannels))
	T.HandleFunc("/api/agents/import", g(T.handleAgentImport))
	T.HandleFunc("/api/agents/suggest", g(T.handleAgentSuggest))
	// Guided create: drafts the prompt from the wizard brief, saves,
	// and echoes the new record for the redirect into the editor.
	T.HandleFunc("/api/agents/wizard", g(T.handleAgentWizard))
	// Per-user Default agent preference (surfaced on /account via the
	// account-section registry — account_prefs.go).
	T.HandleFunc("/api/default-agent", g(T.handleDefaultAgentPref))
	// Per-session diagnostics trail (session_diag.go) — the ⚠ affordance.
	T.HandleFunc("/api/session-diag", g(T.handleSessionDiag))
	T.HandleFunc("/api/agents/", g(T.handleAgentOne))
	T.HandleFunc("/api/collections", g(T.handleCollections))
	// More-specific path wins over /api/collections/ in Go's ServeMux,
	// so this route serves the pre-create draft endpoint without
	// colliding with handleCollectionOne's per-id paths.
	T.HandleFunc("/api/collections/draft-description", g(T.handleCollectionDraftDescription))
	T.HandleFunc("/api/collections/", g(T.handleCollectionOne))
	T.HandleFunc("/api/pipelines", g(T.handlePipelines))
	// More-specific path wins over /api/pipelines/ in Go's ServeMux, so
	// import gets its own handler without colliding with the per-id
	// routes (get/put/delete/export/run) in handlePipelineOne.
	T.HandleFunc("/api/pipelines/import", g(T.handlePipelineImport))
	T.HandleFunc("/api/pipelines/", g(T.handlePipelineOne))
	T.HandleFunc("/api/skills/list", g(T.handleSkillsList))
	T.HandleFunc("/api/sessions", g(T.handleSessionList))
	T.HandleFunc("/api/sessions/", g(T.handleSessionOne))
	// Staged improvement-brief retrieval for the "Send to Builder"
	// handoff (see send_to_builder.go). The brief is created by the
	// /api/sessions/{sid}/send-to-builder sub-action and consumed here.
	T.HandleFunc("/api/builder-brief/", g(T.handleBuilderBrief))
	T.HandleFunc("/api/send", g(T.handleSendRouter))
	T.HandleFunc("/api/cancel", g(T.handleCancelRouter))
	T.HandleFunc("/api/confirm", g(T.handleConfirmRouter))
	T.HandleFunc("/api/inject", g(T.handleInject))
	// Operator event webhook — PUBLIC (the unguessable per-monitor token is the
	// credential), so it bypasses cookie auth. External watchers POST here to
	// wake the Operator. Registered ungated + as a public path.
	T.HandleFunc("/api/operator/event/", T.handleOperatorEvent)
	RegisterPublicPath(T.WebPath() + "/api/operator/event/")
	// Run-registry endpoints (see runs.go / runs_http.go) — let a
	// reconnecting client discover and resume the in-flight stream
	// for a session after disconnect.
	T.HandleFunc("/api/runs/active", g(T.handleRunsActive))
	T.HandleFunc("/api/runs/", g(T.handleRunsDispatch))
	T.HandleFunc("/api/settings/private", g(T.handlePrivateModeGet))
	T.HandleFunc("/api/settings/private/set", g(T.handlePrivateModeSet))
	T.HandleFunc("/api/settings/memory", g(T.handleMemoryModeGet))
	T.HandleFunc("/api/settings/memory/set", g(T.handleMemoryModeSet))
}

// adminGated wraps an http.HandlerFunc so non-admin requests get a
// 403 before the handler ever runs. Single-user / auth-disabled
// deployments bypass the gate (no admin concept). Matches the
// WebRestricted policy above so the landing page and direct URL
// access can't disagree.
func (T *OrchestrateApp) adminGated(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if T.DB != nil && AuthHasUsers(T.DB) && !AuthIsAdmin(T.DB, r) {
			http.Error(w, "Agents is admin-only", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

// adminGatedWrite is adminGated for STATE-CHANGING console actions: it adds a
// method guard that rejects safe methods (GET/HEAD/OPTIONS). Console action
// endpoints are query-param driven, so without this a cross-site top-level GET
// could trigger them — SameSite=Lax still sends the session cookie on such GETs,
// and the core AuthMiddleware Origin check only covers non-safe methods. The UI
// already calls these with POST/DELETE (see page_chat.go action buttons), so the
// guard is invisible to legitimate use; cross-site POST/DELETE is separately
// blocked by the Origin check. Use for every console route that mutates.
func (T *OrchestrateApp) adminGatedWrite(h http.HandlerFunc) http.HandlerFunc {
	return T.adminGated(func(w http.ResponseWriter, r *http.Request) {
		if !IsStateChangingMethod(r.Method) {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	})
}

// handleRoot routes the bare prefix (e.g. "/orchestrate/") to the
// chat page and 404s anything else under "/" that fell through to
// the catch-all.
func (T *OrchestrateApp) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	T.handleChatPage(w, r)
}
