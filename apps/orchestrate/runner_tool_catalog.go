package orchestrate

import (
	"fmt"
	"slices"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// resolveWorkerTools builds the agent's effective tool surface for
// this turn: default pool (Read+Network non-blocked tools + unannotated
// orchestrate-internal tools) intersected with agent.AllowedTools when
// the agent set an explicit allowlist; Private mode then drops
// internet-flagged tools. Returns the resolved AgentToolDefs (handlers
// bound to sess) and the final name list for logging.
//
// Shared by runPlan (orchestrator's inline tool surface) and
// runWorkerStep (worker step execution) so the two pipelines agree on
// what's available without drift.
// resolveWorkerTools builds the per-turn tool catalog. The
// forOrchestrator flag distinguishes Builder's own chat-orchestrator
// turn (the planner — gets a LEAN authoring catalog, no workhorses)
// from a plan_set worker step (gets the FULL authoring catalog).
// Non-Builder agents are unaffected by the flag — the gate at
// isBuilderAgent only fires for the Builder seed.
func (t *chatTurn) resolveWorkerTools(sess *ToolSession, forOrchestrator bool) ([]AgentToolDef, []string, error) {
	defaultNames := availableWorkerToolNames()
	var toolNames []string
	switch {
	case isNoToolsSentinel(t.agent.AllowedTools):
		// Sentinel — admin explicitly unchecked every tool in the
		// Tools modal. Effective optional-tool list is empty.
		// Framework always-on tools (plan_set, respond_directly,
		// stay_silent, keep_going) get appended later in the catalog
		// build, so the agent still has the minimum it needs to
		// produce a response — just no read/network/write tools.
		toolNames = nil
	case len(t.agent.AllowedTools) > 0:
		allow := make(map[string]bool, len(t.agent.AllowedTools))
		for _, n := range t.agent.AllowedTools {
			allow[canonicalToolName(n)] = true
		}
		matched := 0
		for _, n := range defaultNames {
			if allow[n] {
				toolNames = append(toolNames, n)
				matched++
			}
		}
		// Credential-backed tools (fetch_url_<cred>) are synthesized per
		// session by Secure().BuildTools and never appear in defaultNames
		// (the registered worker pool), so the intersect above silently
		// dropped any credential tool the author explicitly allowlisted.
		// The agent then fell back to the generic fetch_url — which has no
		// injected auth and refuses private hosts — instead of the bound,
		// authed tool it was configured with. Re-add allowlisted names that
		// resolve to a live credential tool for this session. (The dispatch
		// path in agent_dispatch.go already resolves these via
		// GetAgentToolsWithSession's per-session fallback; this brings the
		// interactive / published-app surface to parity.) Only build the
		// per-session credential set when some allowlisted name went
		// unmatched above — the common all-registered case skips it.
		if matched < len(allow) {
			for _, td := range Secure().BuildTools(sess) {
				if n := td.Tool.Name; allow[n] && !slices.Contains(toolNames, n) {
					toolNames = append(toolNames, n)
				}
			}
		}
	default:
		// No explicit allow-list — the agent runs the full default pool.
		// defaultNames is the REGISTERED worker pool and never contains the
		// per-session credential tools (fetch_url_<cred>) synthesized by
		// Secure().BuildTools, so "allow everything" would paradoxically be
		// LESS capable than a hand-picked list that named a credential tool:
		// the model can't see fetch_url_<cred> and falls back to the generic,
		// unauthenticated fetch_url. Append every enabled credential tool so
		// the most permissive setting genuinely means everything. These carry
		// CapNetwork, so the Private-mode filter below still drops them per
		// turn when the agent is running network-restricted.
		toolNames = defaultNames
		for _, td := range Secure().BuildTools(sess) {
			if n := td.Tool.Name; !slices.Contains(toolNames, n) {
				toolNames = append(toolNames, n)
			}
		}
	}
	// Always include `workspace` regardless of cap filtering. Workspace
	// owns the delivery primitive (action="attach") that every producer
	// tool routes through — it's universal infrastructure, not a
	// per-agent capability choice. Without this, agents whose AllowedTools
	// is empty (default pool) miss workspace because its CapWrite +
	// CapExecute actions get it filtered out of the Read+Network default
	// pool. The LLM then sees producer tools telling it to call
	// workspace(attach) but has no workspace tool in its catalog —
	// silent never-attach failure.
	//
	// Skip the auto-include when the no-tools sentinel is set: the
	// admin explicitly wants NO optional tools, and workspace is an
	// optional tool. Without producers there's nothing to deliver
	// either, so workspace would be dead weight.
	if !isNoToolsSentinel(t.agent.AllowedTools) && !slices.Contains(toolNames, "workspace") {
		toolNames = append(toolNames, "workspace")
	}
	// The counterpart to a capability the framework grants unilaterally.
	//
	// Detaching is the framework's decision, not the agent's: work it started
	// as one call keeps running after the turn ends whether it chose that or
	// not. An agent that can start background work without asking must be able
	// to STOP it without being allowlisted for the privilege, or "actually,
	// forget the rest" is a sentence it can only apologize to while the results
	// keep arriving. Force-included like workspace, for the same reason —
	// deliberately NOT in frameworkUtilityTools, which is reserved for pure
	// side-effect-free helpers and this is not one.
	if !isNoToolsSentinel(t.agent.AllowedTools) && !slices.Contains(toolNames, "background_work") {
		toolNames = append(toolNames, "background_work")
	}
	// Framework utility tools — pure CapRead helpers (calculate, date_math,
	// time_in_zone) kept always-on so an agent never fails basic math /
	// dates / timezones just because nobody allowlisted them. Hidden from
	// the curation UI (frameworkUtilityTools); force-included here like
	// workspace. Added BEFORE the Private-mode filter — they're non-network
	// so they survive it. Skip on the no-tools sentinel (admin wants none).
	if !isNoToolsSentinel(t.agent.AllowedTools) {
		for _, n := range frameworkUtilityTools {
			if !slices.Contains(toolNames, n) {
				toolNames = append(toolNames, n)
			}
		}
	}
	// Per-turn Private mode drops tools that contact the internet.
	// Applied AFTER the agent's allowlist so non-network tools the
	// agent allowed (list_agents, calculate, …) keep working.
	// Checks BOTH the legacy IsInternetTool interface AND the modern
	// Caps() declaration — a tool tagged CapNetwork via Caps() but
	// missing IsInternetTool would otherwise slip past this filter
	// (silently breaking Private mode's contract).
	if t.privateMode {
		filtered := toolNames[:0]
		for _, n := range toolNames {
			ct, ok := FindChatTool(n)
			if !ok {
				continue
			}
			if isNetworkTool(ct) {
				continue
			}
			filtered = append(filtered, n)
		}
		toolNames = filtered
	}
	// Strip client-bridge tool names (from_client_*) before the
	// global-registry lookup. They're never in the registered
	// ChatTools pool (they're per-user, injected at runtime by the
	// bridge), so GetAgentToolsWithSession would 404 on every one
	// and abort the whole turn. The runtime hook later in this
	// function reads them from the user's connected desktops and
	// appends as ChatToolToAgentToolDef so the LLM still sees them.
	if len(toolNames) > 0 {
		filtered := toolNames[:0]
		for _, n := range toolNames {
			if IsClientToolName(n) {
				continue
			}
			filtered = append(filtered, n)
		}
		toolNames = filtered
	}
	tools, err := GetAgentToolsWithSession(sess, toolNames...)
	if err != nil {
		// One unresolvable name used to abort the whole catalog build, which
		// took the capability toolsets appended BELOW down with it — an agent
		// could lose its entire authoring set because a single allowlisted
		// tool had gone missing. The dispatch path already recovers per-name;
		// do the same here so a stale allowlist entry costs one tool, not all
		// of them.
		Log("[orchestrate.tools] catalog build failed for agent=%s (%v) — resolving per-name", t.agent.ID, err)
		tools = nil
		resolved := toolNames[:0]
		for _, n := range toolNames {
			if td, terr := GetAgentToolsWithSession(sess, n); terr == nil && len(td) > 0 {
				tools = append(tools, td[0])
				resolved = append(resolved, n)
				continue
			}
			Log("[orchestrate.tools] dropping unresolvable tool %q for agent=%s", n, t.agent.ID)
		}
		toolNames = resolved
	}
	// Builder gets the unregistered authoring tools appended here —
	// they don't live in any global registry, so they reach the
	// catalog only via this explicit code path. Identity check
	// against the seed-builder ID prevents the appendage from
	// leaking to other agents.
	//
	// forOrchestrator chooses between two catalogs that reflect the
	// assembler-vs-research split:
	//   - true  → Builder's OWN chat-orchestrator turn. Builder is
	//             the assembler: it reads worker drafts and calls
	//             create_agent / tool_def / etc. itself, so it
	//             needs the FULL authoring catalog right here.
	//   - false → A plan_set worker step under Builder. Workers
	//             research / draft / smoke-test and report COMPONENTS
	//             back. They get the worker-research extras only
	//             (raw call_<credential> for API probing); authoring
	//             tools stay with the orchestrator.
	// Owner-only, same guarantee as the Fleet/operator block below: the
	// authoring toolset mutates the OWNER's gohort (creates agents/tools/apps,
	// drafts credentials) and reaches owner-scoped stores, so it attaches ONLY
	// when the runtime user IS the agent's owner. Without this an EXPOSED Author
	// agent would hand create_agent / tool_def / draft_*_credential to a public
	// visitor — the hole the de-silo opened once authoring stopped being pinned
	// to the never-published Builder seed. Seeds load with Owner unset until
	// shadowed; treat that as the caller's own so the owner keeps authoring on
	// their own Builder seed. publiclyExposable stays permissive (Publish means
	// published) precisely because this gate, not the publish gate, is where the
	// owner-only concern lives.
	// A VIRGIN SEED is the caller's own. loadAgent stamps the in-code
	// default with Owner = seedOwner ("system") precisely so callers can
	// tell "came from code" from "came from the DB" — so an unshadowed
	// Builder has Owner "system", never "" and never the username.
	//
	// This check used to test only ""/username, on the stated assumption
	// that "seeds load with Owner unset". They don't. The result: every
	// user's own Builder failed the gate and silently lost the ENTIRE
	// authoring catalog — no tool_def, no create_agent, no survey — while
	// the UI still described it as the authoring agent. Builder then
	// truthfully reported it could not author, and the missing tools read
	// as a model hallucination for three debugging sessions.
	//
	// Every other owner check in the package already admits seedOwner
	// (agent_credentials.go, agents_grouped_tool.go, admin_tool_scope.go);
	// this one was the outlier.
	ownerRun := t.agent.Owner == "" || t.agent.Owner == seedOwner || t.agent.Owner == t.user
	// An authoring agent running for someone who is NOT its owner loses the
	// whole authoring catalog. That is the correct security answer, but it
	// used to happen in total silence: the model found no tool_def, invented
	// a reason ("a framework problem on my end — report this to the platform
	// administrators"), and the user went debugging a system that was working
	// as designed. Say it out loud, in the log AND in the model's own context,
	// so the absence has a stated cause instead of a guessed one.
	if agentCanAuthor(t.agent) && !ownerRun {
		Log("[orchestrate.tools] authoring catalog WITHHELD from agent %q (%s): owner=%q but this turn runs as %q — authoring tools are owner-only",
			t.agent.Name, t.agent.ID, t.agent.Owner, t.user)
		t.turnDiag("authoring_withheld", "Owned by "+t.agent.Owner+" but this turn runs as "+t.user+
			". Authoring is owner-only, so tool_def / create_agent / update_agent are absent BY DESIGN — nothing is broken.")
	}
	if agentCanAuthor(t.agent) && ownerRun {
		Log("[orchestrate.tools] authoring catalog GRANTED to agent %q (%s) for user %q (orchestrator=%v)",
			t.agent.Name, t.agent.ID, t.user, forOrchestrator)
		var extra []AgentToolDef
		if forOrchestrator {
			extra = builderAuthoringTools(sess, t)
		} else {
			extra = builderWorkerResearchTools(sess, t)
		}
		// Builder authors constantly, so it carries the catalog inline. Any OTHER
		// agent has authoring as a CAPABILITY it uses occasionally — and the
		// catalog is ~18.7k tokens, about a third of such an agent's whole prompt,
		// paid on every turn including the eight-word ones. For them the catalog
		// goes behind load_tool: an index of names and one-liners in the prompt,
		// full schemas on demand. Cost is one extra round on a turn that actually
		// authors; saving is ~17.6k tokens on every turn that does not.
		if forOrchestrator && !isBuilderAgent(t.agent.ID) {
			t.authoringLazyPrompt = registerLazyAuthoringTools(t, extra)
			Log("[orchestrate.tools] agent=%s: %d authoring tool(s) deferred behind load_tool — index only in the prompt", t.agent.ID, len(extra))
		} else {
			tools = append(tools, extra...)
			for _, td := range extra {
				toolNames = append(toolNames, td.Tool.Name)
			}
		}
	}
	// Fleet agents get the exclusive fleet-management catalog on their
	// conversational turn — delegate + create/list/run/pause standing
	// agents + read the run-ledger + event-monitor management + history
	// recall. Not globally registered; appended here only when Fleet is on,
	// same shape as Builder's authoring tools.
	// Owner-only: the Fleet toolset reaches owner-scoped management endpoints
	// (delegate, standing agents, run ledger, monitors), so it attaches ONLY
	// when the runtime user IS the agent's owner. A public-app visitor (or any
	// granted non-owner user) running a Fleet agent never sees these — which is
	// what lets publiclyExposable honor Publish on a Fleet agent without leaking
	// owner controls. Seeds load with Owner unset until shadowed; treat that as
	// the caller's own so the owner keeps Fleet on their own seed.
	// (ownerRun computed above, alongside the authoring-grant gate.)
	// The Builder gets the operator toolset too — create_event_monitor,
	// recurring, create_standing_agent — so it can WIRE a tool it just authored
	// into a schedule/watch. "Build X and run it every 30s" was structurally
	// impossible for an author-only agent: it could build the tool and then had
	// no way to schedule it, so it stopped half-done or handed off.
	if (t.agent.Fleet || agentCanAuthor(t.agent)) && forOrchestrator && ownerRun {
		om := operatorManagementTools(sess, t.agent.ID)
		// History drill-in is its own pair (recall_history / expand_history) in
		// legacy mode; the unified `recall` tool already spans folded-away
		// history, so drop the pair to avoid two tools that search the past.
		if !unifiedMemoryEnabled() {
			om = append(om, operatorHistoryTools(sess, t.agent.ID)...)
		}
		tools = append(tools, om...)
		for _, td := range om {
			toolNames = append(toolNames, td.Tool.Name)
		}
		// FLEET drops the generic interval scheduler — the Operator schedules
		// through the fleet (create_standing_agent, proper cron / start+interval
		// timing, surfaces in Enabled agents); without this the LLM reaches for
		// "recurring" and bypasses the fleet. The BUILDER keeps recurring: it's
		// authoring a scheduled TOOL, and recurring / create_event_monitor are
		// exactly the right primitives for "run this tool on an interval".
		if t.agent.Fleet {
			tools, toolNames = dropToolsByName(tools, toolNames, "recurring")
		}
	}
	// request_build — the COMPLEMENT of Fleet's live Builder dispatch. A Fleet
	// agent hands authoring to Builder directly; a non-Fleet agent can't, so
	// without this it has no path to "create a sub-agent" and flails. This gives
	// it one: queue the build as an approval the user sees, and on approve Builder
	// authors it OwnedBy this agent. Owner-run conversational turn only, and not
	// Builder itself (Builder authors directly).
	if forOrchestrator && ownerRun && !t.agent.Fleet && !isBuilderAgent(t.agent.ID) {
		rb := requestBuildTool(t.user, t.agent.ID, t.agent.Name)
		tools = append(tools, rb)
		toolNames = append(toolNames, rb.Tool.Name)
	}
	// Channel-scoped chat tools — ANY agent that has channels gets list_chats /
	// read_chat over ITS channels (a whole-service binding widens to the global
	// view). Gated on having channels, independent of Fleet; conversational turn
	// only, like the Fleet block.
	if forOrchestrator {
		if chTools := channelChatTools(sess, t.user, t.agent.ID); len(chTools) > 0 {
			tools = append(tools, chTools...)
			for _, td := range chTools {
				toolNames = append(toolNames, td.Tool.Name)
			}
		}
		// (cortex deliverables — file_deliverable / note_to_cortex — now come from
		// frameworkConversationalTools, the shared web+channel set.)

		// App-contributed tools (core/agent_tool_providers.go) — servitor's
		// request_capability + per-machine tools, and whatever the next app
		// binds. The channel/dispatch path adds these in dispatchExtraTools;
		// this surface never did, so the SAME agent held its appliance tools
		// on a channel run but sat empty-handed in its own chat page — where
		// the owner naturally goes to ask "can you talk to lab-box?".
		// Deliberately NOT intersected with AllowedTools: the app's own grant
		// record is the consent (mirrors the agents tool and credential
		// tools), and the tool-curation UI never lists these names, so an
		// allowlist agent could not have named them anyway.
		if appTools := AgentProvidedTools(sess, t.user, t.agent.ID); len(appTools) > 0 {
			before := len(tools)
			tools = mergeToolsDedup(tools, appTools)
			for _, td := range tools[before:] {
				toolNames = append(toolNames, td.Tool.Name)
			}
		}
	}
	// Parent-tool inheritance — an owned sub-agent that opted in (InheritParentTools)
	// resolves its parent's NON-consequential catalog at runtime in addition to
	// its own allowlist: the parent's worker tools (no Fleet block) plus the
	// read-only phantom tools. Lets a Builder-authored summarizer read the chat
	// it summarizes without being a Fleet agent (so no texting / delegation).
	// Guarded to top-level parents so inheritance can't chain, and deduped so
	// shared names don't double-register.
	if t.agent.InheritParentTools && t.agent.OwnedBy != "" {
		if parent, ok := loadAgent(t.udb, t.agent.OwnedBy); ok && parent.OwnedBy == "" {
			inherited := t.inheritableParentTools(parent, sess)
			before := len(tools)
			tools = mergeToolsDedup(tools, inherited)
			for _, td := range tools[before:] {
				toolNames = append(toolNames, td.Tool.Name)
			}
		}
	}
	// (Skill-granted tools are NOT resolved here anymore. Activation is
	// per-turn: t.skillsActive is empty when this static catalog is built
	// at turn start. A skill's tools are surfaced by the per-round
	// DynamicTools feed (dynamicNewTempTools → AppendSkillGrantedTools) the
	// round AFTER activate_skill fires, so they appear this same turn.)
	// Local tools from the user's gohort-desktop surface (from_client_*).
	// Exposed ONLY when this request came from the gohort-desktop viewer
	// itself (its proxy stamps the bridge key — see t.fromDesktopClient). A
	// remote browser / phone logged into the same account never sees them, so
	// the local machine's filesystem / screenshot / contacts can't be reached
	// remotely — not even with auto-approve on (the old "approval modal is the
	// enforcement point" model failed exactly there). When the desktop is
	// offline at call time the tool's wrapper still returns a clean "open your
	// desktop" error. Keyed off the chat user so seed agents (Chat) get the
	// surface for whoever is chatting at the desktop.
	have := map[string]bool{}
	for _, n := range toolNames {
		have[n] = true
	}
	// Only OPEN-POOL agents (empty AllowedTools — the general Chat/seed
	// assistants) receive the desktop's local surface. A CURATED agent with an
	// explicit allowlist (a Guide Author, a techwriter, any app agent) scoped
	// itself to a specific toolset and never opted into local-filesystem /
	// screenshot / contacts access — appending it silently let those ambient
	// tools SHADOW the agent's purpose-built ones (a Guide Author rummaging the
	// local disk instead of dispatching investigate_<system> to servitor). The
	// allowlist is the opt-in signal: no list ⇒ open pool ⇒ desktop surface; an
	// explicit list ⇒ exactly those tools, nothing ambient.
	var fromClient []AgentToolDef
	if t.fromDesktopClient && len(t.agent.AllowedTools) == 0 {
		for _, lt := range LocalToolsForUser(t.user) {
			if have[lt.Name()] {
				continue
			}
			fromClient = append(fromClient, ChatToolToAgentToolDef(lt))
			toolNames = append(toolNames, lt.Name())
		}
	}
	// NOTE: do NOT wrapToolsForActivity here. The client-bridge tools are
	// part of the returned slice, which both callers wrap as a whole
	// (resolveWorkerTools' result → wrapToolsForActivity at the orchestrator
	// and worker call sites). Wrapping here too double-wrapped ONLY the
	// from_client_* tools, so each fired two tool_call/tool_result SSE events
	// and two recordToolCall entries — the catalog showed (and the tool log
	// recorded) every desktop call twice. The single call-site wrap gives
	// them the same inline chips + cache as every other tool, once.
	// hand_to_builder — pass a WORKING artifact to Builder rather than a
	// description of one. Nil unless this agent may already dispatch Builder,
	// so it grants no new reach.
	if h := handToBuilderTool(t); h != nil {
		tools = append(tools, *h)
		toolNames = append(toolNames, h.Tool.Name)
	}
	tools = append(tools, fromClient...)
	// Dedupe by tool name at the single exit.
	//
	// The catalog is assembled by appending several independent sets — the
	// allowlist resolution, the capability toolsets (authoring, conductor),
	// framework utilities, credential tools, client-bridge tools. Each
	// append is individually correct, but a name can legitimately appear in
	// two of them (Builder's seed allowlists stay_silent / keep_going, and
	// the framework also supplies them), so the assembled slice carried
	// duplicates.
	//
	// The agent loop already coped — it keeps the first and logs a
	// collision — but the log line blamed "an expanded toolbox action and a
	// standalone tool", which is a real cause of collisions and NOT this
	// one, so the noise pointed every reader at the wrong thing. Dedupe
	// here, with the same first-wins rule, and the loop's warning goes back
	// to meaning what it says.
	tools, toolNames = dedupeToolsByName(tools, toolNames)
	// Tell the session what actually resolved, so a tool's OUTPUT can name
	// only tools the caller really has. fetch_url's binary-response message
	// used to hardcode "use read_file/run_local", both servitor-only: a
	// research pipeline stage that downloaded a PDF was handed a recovery
	// path that did not exist for it, and re-fetched the same 4MB file
	// instead. Single exit, so every caller gets this.
	sess.SetAvailableTools(toolNames)
	return tools, toolNames, nil
}

// dedupeToolsByName keeps the FIRST definition of each tool name, matching
// the agent loop's own collision rule so behavior is unchanged — only the
// duplicate entries (and the misleading warnings they produced) go away.
func dedupeToolsByName(tools []AgentToolDef, names []string) ([]AgentToolDef, []string) {
	seen := make(map[string]bool, len(tools))
	outTools := tools[:0]
	for _, td := range tools {
		n := td.Tool.Name
		if n != "" && seen[n] {
			continue
		}
		if n != "" {
			seen[n] = true
		}
		outTools = append(outTools, td)
	}
	seenName := make(map[string]bool, len(names))
	outNames := names[:0]
	for _, n := range names {
		if seenName[n] {
			continue
		}
		seenName[n] = true
		outNames = append(outNames, n)
	}
	return outTools, outNames
}

// wrapToolsForActivity decorates each tool's handler with:
//   - per-turn cache short-circuit (skip the call if the same
//     (name, args) returned already)
//   - activity-pane cmd / output / error rows (for apps that show
//     the activity pane — orchestrate locks it off, servitor uses it)
//   - inline tool_call / tool_result SSE events attached to the
//     active conversation-pane bubble (chat-app style — the only
//     way users see tool use when the activity pane is hidden)
//   - per-turn tool log recording (for later step prompts)
//
// Lazy-materializes an "orch-…" bubble if a tool fires before any
// text has streamed in this round, so the call has somewhere to land
// agentCanAuthorAgents reports whether the active agent has access to
// the create_agent tool — i.e. whether it's an agent-authoring agent.
// True when AllowedTools is empty (= default pool, which includes
// create_agent) OR explicitly lists "create_agent". Used by
// create_pipeline_tool's handler to gate the user-wide-with-approval
// path: agents that can author other agents almost always mean to
// either bundle inline (case A in the prompt) or attach via for_agent
// (case B). The user-wide approval queue strands the LLM because the
// tool name doesn't resolve until admin review.
func (t *chatTurn) agentCanAuthorAgents() bool {
	if t == nil {
		return false
	}
	if len(t.agent.AllowedTools) == 0 {
		return true
	}
	for _, n := range t.agent.AllowedTools {
		if n == "create_agent" || n == "*" {
			return true
		}
	}
	return false
}

// filterToolAuthoringWithoutFocus prunes overlapping or unfireable
// authoring tools from the catalog. Two distinct surfaces survive:
//
//   - **add_tool** attaches a tool to the agent currently in
//     AuthoringAgentID focus. Refuses without focus, so hide it when
//     no focus is set (otherwise the LLM pattern-matches and burns
//     rounds calling a tool that always errors).
//   - **tool_def** is the standalone session/user-scoped grouped tool
//     builder. Stays visible in BOTH states — when there's no focus
//     it's the only authoring path; when there IS focus the LLM picks
//     between "attach to this agent" (add_tool) and "one-off session
//     tool" (tool_def) based on intent. The Chat prompt teaches the
//     distinction.
//
// The legacy unbundled trio (create_temp_tool / create_api_tool /
// create_pipeline_tool) is always dropped — tool_def is the grouped
// replacement and exposing both shapes guarantees the
// oscillation-between-authoring-tools loop we already saw.
//
// Returns filtered (tools, names) preserving original order.
func filterToolAuthoringWithoutFocus(tools []AgentToolDef, names []string, session *ChatSession) ([]AgentToolDef, []string) {
	hasFocus := session != nil && session.AuthoringAgentID != ""

	// Compute which tools to drop based on the active conditions.
	drop := map[string]bool{
		// Always dropped — superseded by tool_def's grouped action surface.
		"create_pipeline_tool": true,
		"create_temp_tool":     true,
		"create_api_tool":      true,
	}
	if !hasFocus {
		// add_tool refuses without focus — hide it so the LLM doesn't
		// burn a round calling something that's guaranteed to error.
		// tool_def stays visible: it's the standalone authoring path
		// (one-off session/user tool, no agent attachment).
		drop["add_tool"] = true
	}
	if len(drop) == 0 {
		return tools, names
	}

	filteredTools := tools[:0]
	for _, td := range tools {
		if drop[td.Tool.Name] {
			continue
		}
		filteredTools = append(filteredTools, td)
	}
	filteredNames := names[:0]
	for _, n := range names {
		if drop[n] {
			continue
		}
		filteredNames = append(filteredNames, n)
	}
	return filteredTools, filteredNames
}

// explicitOff returns whether the Explicit Memory layer (always-in-
// prompt facts via store_fact) is suppressed for this turn. Gated by
// DisableExplicit only — Explicit Memory is configuration-level,
// either the agent has a facts surface or it doesn't. Callers gate
// store_fact / list_facts / forget_fact registration + the "Saved
// facts" prompt-injection block on this helper.
func (t *chatTurn) explicitOff() bool {
	if t == nil {
		return true
	}
	return t.agent.DisableExplicit
}

// inferredOff returns whether the Reference Memory layer (vector-grown
// derived chunks via memory_save + synthesis auto-ingest) is
// suppressed for this turn. True when EITHER the agent has
// DisableInferred=true OR the per-turn Clean toggle is on. Callers
// gate memory_save / memory_search / memory_forget registration,
// synthesis auto-ingest, derived-chunk recall in auto-inject, and
// the skills classifier (skills emit derived chunks via self-training)
// on this helper.
func (t *chatTurn) inferredOff() bool {
	if t == nil {
		return true
	}
	return t.agent.DisableInferred || t.inferredDisabled
}

// gateAgentCRUDTools wraps the agent-CRUD tool handlers with a build-
// plan auto-advance hook so each successful authoring tool ticks the
// next pending step off the user-visible plan card even when the LLM
// forgets to call mark_step_done in the same response.
//
// The previous two-turn gate (propose-then-act with mandatory
// ask_user in between) was removed — the rhythm is now enforced via
// prompt + plan_set decomposition instead of a runtime block. The
// gate kept producing false-positive blocks on legitimate flows
// (e.g. plan_set worker steps where the confirm phase had already
// happened but the awaiting-confirm flag hadn't propagated by the
// time the worker fired). Trust the prompt's "Phase 3 — CONFIRM" +
// plan_set boundary instead.
func (t *chatTurn) gateAgentCRUDTools(tools []AgentToolDef) {
	autoAdvance := map[string]bool{
		"create_agent": true,
		"update_agent": true,
		"clone_agent":  true,
		"delete_agent": true,
		"add_tool":     true,
	}
	for i := range tools {
		name := tools[i].Tool.Name
		orig := tools[i].Handler
		if orig == nil || !autoAdvance[name] {
			continue
		}
		toolName := name // closure capture
		tools[i].Handler = func(args map[string]any) (string, error) {
			result, err := orig(args)
			if err == nil {
				// Auto-advance the next pending plan step. Summary is
				// the first line of the tool result (typically the
				// directive line like "AGENT_CREATED ok. id=…").
				summary := firstLineSnippet(result, 120)
				if n := t.autoAdvanceBuildPlan(summary); n > 0 {
					Debug("[orchestrate.build_plan] auto-advanced step %d after %s success", n, toolName)
				}
			}
			return result, err
		}
	}
}

// firstLineSnippet returns the first non-empty line of s clipped to
// max chars. Used to derive a one-line summary for auto-advanced
// build-plan steps when the tool result is multi-line.
func firstLineSnippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[:nl]
	}
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// in the conversation pane. Mutates the slice in place. Returns it
// for chaining.
//
// Optional labelPrefix prepends to every cmd row emitted by the
// wrapped tools — used by sub-agent dispatches to visually nest
// the sub-agent's tool calls under the parent ("↳ [Pickleball
// Coach] knowledge_search(...)"). Empty prefix = no nesting marker
// (the default for top-level / parent wraps).
// untrustedContentFence prefixes the result of any network tool: its payload is
// EXTERNAL data (fetched pages, API/feed responses, search snippets) that can
// carry prompt-injection. The fence tells the model to treat the payload as data,
// not instructions — the front-line mitigation for an agent that ingests untrusted
// content. Kept to one line to bound the per-result token cost.
// untrustedContentFence is the package-local spelling of the framework banner.
// The text lives in core (UntrustedToolResultFence) because tools in other
// packages self-fence individual actions with the identical marker — a tool
// result that says "untrusted" in two different voices trains the model to
// treat the wording, rather than the meaning, as the signal.
const untrustedContentFence = UntrustedToolResultFence

// toolCarriesNetworkCap reports whether a tool's declared capabilities include
// network access — the signal that its result is external, untrusted content.
func toolCarriesNetworkCap(t Tool) bool {
	for _, c := range t.Caps {
		if c == CapNetwork {
			return true
		}
	}
	return false
}

// toolHeartbeat is how long a tool call runs before the activity pane
// starts saying so, and the interval between those lines afterwards.
//
// 15s because that is roughly where a person stops reading a pause as
// latency and starts reading it as a failure. Short enough to answer the
// question, long enough that an ordinary tool call never emits one.
const toolHeartbeat = 15 * time.Second

// receiver is the agent whose CONTEXT these results land in, which is not
// always the turn's own agent: a dispatched sub-agent's tools are wrapped here
// by the caller's turn, and it is the sub-agent that reads what they return and
// can be steered by it. Fencing does not care (it is derived from the tool's
// capabilities and is the same for everyone); the injection scan does, because
// the scan scope is a per-agent setting and reading it off the wrong record
// would mean an agent whose owner turned scanning ON goes unscanned whenever
// something else dispatches it.
func (t *chatTurn) wrapToolsForActivity(sess *ToolSession, tools []AgentToolDef, receiver AgentRecord, labelPrefix ...string) []AgentToolDef {
	prefix := ""
	if len(labelPrefix) > 0 {
		prefix = labelPrefix[0]
	}
	for i := range tools {
		name := tools[i].Tool.Name
		orig := tools[i].Handler
		if orig == nil {
			continue
		}
		// Fence network-tool results as untrusted external content. Wrap the RAW
		// handler so the fence rides through the cache + activity emission below
		// (cache stores fenced; the model always sees the marker). TrustedOutput
		// tools opt out: their result is framework-generated control/authoring
		// text (tool_def / add_tool confirmations), not fetched content — the
		// CapNetwork on their union comes from a verify/test sub-action, not
		// from their everyday output.
		//
		// A FRAMEWORK-authored result is exempt however the tool is declared:
		// the notice a detached call returns is our own control text, and
		// fencing it tells the model to disregard the very instructions
		// ("nothing has been delivered", "do not call this again") that keep it
		// from claiming a finished render or starting a second one.
		//
		// The injection SCAN rides the same wrap and defaults to the same set —
		// see toolResultPolicyFor. Resolved once here rather than per call, so a
		// turn with scanning off pays two bools and nothing else.
		policy := toolResultPolicyFor(receiver, tools[i].Tool)
		if policy.fence {
			// Network-capable and not framework-authored: the same predicate
			// that decides fencing decides what can carry data out.
			t.noteOutboundTool(name)
		}
		inner := orig
		orig = func(args map[string]any) (string, error) {
			out, err := inner(args)
			return t.applyToolResultPolicy(name, policy, args, out, err)
		}
		tools[i].Handler = func(args map[string]any) (string, error) {
			// Activity-pane cmd / inline tool_call go in parallel so
			// both views work: apps with activity visible (servitor)
			// see the cmd row; apps with it hidden (orchestrate)
			// see the inline chip.
			//
			// Tools listed in hiddenToolChips don't render either —
			// they have a better-than-chip rendering elsewhere (the
			// reply itself for respond_directly, the status line for
			// send_status) and the chip is redundant noise. We still
			// run the handler, snapshot attachments, and record the
			// call for the toolLogPromptSection — only the user-
			// facing emissions are skipped.
			hidden := hiddenToolChips[name]
			callLabel := prefix + formatToolCall(name, args)
			if cached, ok := t.lookupToolCache(name, args); ok {
				// Second+ re-serve of the same cached body → stub, not the body.
				// See the cacheServes field comment: identical re-served bytes
				// make a repetition loop perfectly stable and double-bill the
				// context; the stub breaks the fixed point and says stop.
				t.toolMu.Lock()
				if t.cacheServes == nil {
					t.cacheServes = map[string]int{}
				}
				ck := toolCallKey(name, args)
				t.cacheServes[ck]++
				serves := t.cacheServes[ck]
				t.toolMu.Unlock()
				if serves > 1 {
					cached = "♻ You already have this exact result from earlier THIS TURN — it has not changed, and it is not being repeated here. Do NOT make this call again: scroll up and use the result you already received, or take a genuinely different action (different tool, different arguments, or answer the user now)."
					Debug("[orchestrate.tools] cache stub for %s (re-serve #%d this turn)", name, serves)
				}
				if !hidden {
					t.sse.Send(map[string]any{
						"kind": "activity",
						"type": "cmd",
						"id":   activityCheapID(),
						"text": "♻ " + callLabel + " (cached)",
					})
					if msgID := t.ensureBubbleForTool(); msgID != "" {
						callID := t.emitToolCall(msgID, name, args, " (cached)", prefix)
						t.emitToolResult(msgID, callID, name, cached, nil)
					}
				}
				// Persist cache-hit calls. The live UI shows them via
				// the cmd/inline chip emissions above; without this
				// record, the saved transcript + Copy session export
				// drop them silently — same call visible to the user
				// at the time, invisible to anyone reading the log
				// afterwards. Cached=true distinguishes from a fresh
				// dispatch so a downstream consumer can tell.
				t.recordToolCall(toolCallRecord{
					Name:   name,
					Args:   args,
					Result: cached,
					Cached: true,
				})
				return cached, nil
			}
			// Dispatch-cap path — refuse the Nth identical (name, args)
			// call regardless of cache hit/miss. The cache only stores
			// SUCCESSFUL results, so a tool that's been failing (timeout,
			// 503, blocked-by-bot) won't short-circuit via lookupToolCache
			// and the LLM can re-dispatch the same call indefinitely
			// until budget runs out. Applies only to cacheableTools
			// (pure-read tools where identical args genuinely give the
			// same answer); state-mutating calls fall through unchanged.
			//
			// Tool calls fire from a per-call goroutine in RunAgentLoop,
			// so dispatchCounts MUST be protected — toolMu is the existing
			// mutex on chatTurn that also guards toolCache and toolCalls.
			if cacheableTools[name] {
				key := toolCallKey(name, args)
				t.toolMu.Lock()
				if t.dispatchCounts == nil {
					t.dispatchCounts = map[string]int{}
				}
				prior := t.dispatchCounts[key]
				if prior >= dispatchCallCap {
					t.dispatchCounts[key] = prior + 1
					attempted := t.dispatchCounts[key] - 1
					t.toolMu.Unlock()
					msg := fmt.Sprintf("You've already dispatched %s with these exact args %d times this turn. The result is whatever it was — re-dispatching won't change it. Either USE the result from one of the prior calls, or call something DIFFERENT (different URL, different query, different tool). Don't retry the same call.",
						name, attempted)
					if !hidden {
						t.sse.Send(map[string]any{
							"kind": "activity",
							"type": "error",
							"id":   activityCheapID(),
							"text": "⛔ " + callLabel + " refused (dispatch cap)",
						})
					}
					// Persist refused dispatches too — they're real LLM
					// attempts that consumed budget. Without recording,
					// the saved transcript shows N calls, the export
					// reader is confused about where the "you've already
					// dispatched" feedback came from. Err carries the
					// refusal message so it renders as an error row.
					t.recordToolCall(toolCallRecord{
						Name: name,
						Args: args,
						Err:  msg,
					})
					return msg, nil
				}
				t.dispatchCounts[key] = prior + 1
				t.toolMu.Unlock()
			}
			var msgID, callID string
			if !hidden {
				t.sse.Send(map[string]any{
					"kind": "activity",
					"type": "cmd",
					"id":   activityCheapID(),
					"text": callLabel,
				})
				msgID = t.ensureBubbleForTool()
				if msgID != "" {
					callID = t.emitToolCall(msgID, name, args, "", prefix)
				}
			}
			imgN, vidN, fileN := sessAttachmentCounts(sess)
			// Heartbeat. A tool that runs for minutes is indistinguishable
			// from a hung one: the last thing the log or the pane says is
			// that the call started, and then nothing. A handler gets no
			// context and no channel, so it cannot report progress itself
			// — but the wrapper knows when the call began, and that is
			// enough to say "still going" for ANY slow tool rather than
			// teaching each one to.
			//
			// Deliberately not a timeout. Some work legitimately takes
			// minutes (a regex over a multi-gigabyte support bundle), and
			// cutting it short trades a slow answer for a wrong one.
			stopBeat := make(chan struct{})
			if !hidden {
				go func(label string) {
					began := time.Now()
					tick := time.NewTicker(toolHeartbeat)
					defer tick.Stop()
					for {
						select {
						case <-stopBeat:
							return
						case <-tick.C:
							t.sse.Send(map[string]any{
								"kind": "activity",
								"type": "cmd",
								"id":   activityCheapID(),
								"text": fmt.Sprintf("%s — still running (%s)", label, time.Since(began).Round(time.Second)),
							})
						}
					}
				}(prefix + name)
			}
			out, err := orig(args)
			close(stopBeat)
			// Spill-to-workspace guard. When a tool result is larger
			// than the inline cap, write the full body to the session
			// workspace and replace `out` with a small stub that points
			// at it. The LLM then reads slices via workspace(head/tail/
			// grep/read_lines/stat). Prevents one fat result (a large
			// read_local_file, a verbose agents() dispatch, an unbounded
			// pipeline output) from blowing the context window.
			if err == nil {
				if spilled, ok := maybeSpillToolResult(sess, name, out); ok {
					out = spilled
				}
			}
			rec := toolCallRecord{Name: name, Args: args, Result: out}
			if err != nil {
				rec.Err = err.Error()
				if !hidden {
					t.sse.Send(map[string]any{
						"kind": "activity",
						"type": "error",
						"id":   activityCheapID(),
						"text": fmt.Sprintf("%s → %v", name, err),
					})
				}
			} else if trimmed := strings.TrimSpace(out); trimmed != "" && !hidden {
				t.sse.Send(map[string]any{
					"kind": "activity",
					"type": "output",
					"id":   activityCheapID(),
					"text": truncate(out, 4000),
				})
			}
			if msgID != "" {
				t.emitToolResult(msgID, callID, name, out, err)
				t.flushNewAttachments(sess, msgID, imgN, vidN, fileN)
			}
			if err == nil && agentMutationTools[name] {
				// Signal the chat page that the dropdown's option list
				// is stale. The page's listener re-fetches /api/agents
				// and rebuilds the select; UI stays in sync without
				// the user having to reload after a Chat-driven
				// create_agent / update_agent / delete_agent / clone.
				t.sse.Send(map[string]any{
					"kind": "event",
					"name": "orchestrate_agents_changed",
				})
			}
			t.recordToolCall(rec)
			// Tier-3 elevation evidence: a CUSTOM tool that returned a result
			// for this agent joins its working set, so the next session's
			// catalog carries the schema instead of leaving the model to
			// discover it cannot reach the tool. Registered tools are never
			// lazy, so recording them would be noise. The turn-local set keeps
			// a loop that calls one tool repeatedly to a single store read.
			if err == nil && !t.toolSuccessNoted[name] && sess.LookupTempTool(name) != nil {
				if t.toolSuccessNoted == nil {
					t.toolSuccessNoted = map[string]bool{}
				}
				t.toolSuccessNoted[name] = true
				recordToolSuccess(t.udb, t.agent.ID, sess.ChatSessionID, name)
			}
			return out, err
		}
	}
	return tools
}
