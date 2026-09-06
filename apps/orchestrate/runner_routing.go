package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// --- LLM rounds -------------------------------------------------------------

// shouldUseLeadModel reports whether this agent's main reasoning should
// escalate to the lead model this turn. Honors the per-agent opt-in, but
// the privacy gate wins (gate 2): a ForcePrivate agent or a Private-toggled
// turn keeps reasoning on the local worker so the conversation never leaves
// for the remote lead model. Gate 1 (no lead configured) and gate 3 (admin
// route ceiling) are enforced downstream by LeadChat / the route stage.
//
// Gate 2 is conditional on the lead actually being remote. When the operator
// has declared every model private, escalating keeps the conversation on
// hardware they control, and holding a private turn to the weaker model buys
// nothing — see core/llm_privacy.go.
func (t *chatTurn) shouldUseLeadModel() bool {
	if !t.agent.LeadModel {
		return false
	}
	return t.leadPermitted()
}

// leadPermitted answers the PRIVACY half of the tier question on its own: may
// this turn reach the lead at all, whatever anybody prefers.
//
// Split out because a machine phase that pins "lead" overrules the agent's own
// preference — and must not overrule this. A preference is a routing choice; the
// pin exists because the lead may be a third party, and "the phase said so" is
// exactly the kind of thing that would otherwise become the exception to it.
func (t *chatTurn) leadPermitted() bool {
	if AllLLMsPrivate() {
		return true
	}
	// The SPEC as well as the record. An app agent's per-user shadow can be
	// older than the flag — the same drift that let a stale shadow carry
	// Hidden=false — and here the consequence is an investigator holding SSH
	// credentials talking to a hosted model.
	if agentForcesPrivate(t.agent) {
		return false
	}
	return !t.privateMode
}

// turnRouting resolves this turn's tier ONCE, folding in the machine phase's
// pin, and returns both the per-run pin the agent loop takes and the route key
// that carries the stage's thinking preference.
//
// One answer with two consumers, because they used to be computed
// independently: the route key from the agent's own "Use Lead model" toggle,
// the pin from the phase. A phase naming "lead" therefore moved the pin and
// left the key behind — and the key is what RouteThink reads AND what the
// streaming path resolves the tier from, so a phase asking for the lead got
// neither the model nor its reasoning. Every interactive turn streams, so the
// phase's Model field did nothing at all on the path that matters.
func (t *chatTurn) turnRouting() (pin LLMTier, routeKey string) {
	lead := t.shouldUseLeadModel()
	switch t.machine.Tier() {
	case LEAD:
		// The most specific ROUTING setting in the chain, so it beats the
		// agent's toggle — but it is still a preference, and leadPermitted is
		// not. A pin that cannot be honored resolves DOWN rather than being
		// dropped, so the loop and the route key never disagree.
		lead = t.leadPermitted()
		pin = WORKER
		if lead {
			pin = LEAD
		}
		if !lead {
			// Said out loud, in the place the reader is already looking. A step
			// configured for the lead that runs on the worker is indistinguishable
			// from a step that ignored its configuration, and this is the
			// difference between "gohort is broken" and "turn private mode off".
			t.turnDiag("machine-tier-denied", "Step "+t.machine.Name()+" asks for the lead model, but this turn may not reach it (private mode, or this agent forces private). It ran on the worker.")
			Log("[orchestrate.routing] agent=%s step=%q pinned LEAD but privacy holds this turn on the worker (private=%v force=%v)",
				t.agent.ID, t.machine.Name(), t.privateMode, t.agent.ForcePrivate)
		}
	case WORKER:
		lead, pin = false, WORKER
	}
	return pin, orchestratorRouteKey(t.agent.ID, lead)
}

// dispatchListNamesARunnable reports whether this agent's dispatch list names a
// non-agent target that still exists — a pipeline or a machine.
//
// It exists to stop the deleted-target self-heal from firing on a list that is
// perfectly healthy. That heal reads "no listed AGENT still exists" as "this
// allowlist is stale, fall back to Allow all" — which was right while the list
// could only hold agents, and became a privilege escalation the moment it could
// hold anything else: an owner who allowed exactly one pipeline and no agents
// would have been granted the entire fleet.
//
// Machines joined the list the moment they became dispatch targets, and this is
// the function that had to be told. It is the second time the same lesson has
// been paid for: whenever a shared identifier list gains a new kind of member,
// re-check every place that reasons about the list being "empty" or "stale".
func (t *chatTurn) dispatchListNamesARunnable() bool {
	if len(t.agent.AllowedDispatchTargets) == 0 {
		return false
	}
	for _, d := range ListPipelineDefs(t.udb, t.user) {
		if dispatchListNames(t.agent.AllowedDispatchTargets, d.ID, d.Name) {
			return true
		}
	}
	for _, d := range ListMachineDefs(t.udb, t.user) {
		if dispatchListNames(t.agent.AllowedDispatchTargets, d.ID, d.Name) {
			return true
		}
	}
	return false
}

// frameworkConversationalTools is the single source of truth for the always-on
// framework toolset every CONVERSATIONAL turn gets — IDENTICAL on the web
// (runPlan) and the channel/dispatch (buildDispatchTurnExtras) surfaces. Built
// once here so the two catalogs can't drift: the recurring "works in the web UI
// but not over a channel" class of bug (send_status, stay_silent, graph memory,
// fetch_knowledge_doc, …) all came from these being hand-maintained in two
// places. Genuinely path-specific tools stay in their callers: compact_context
// (web closure var), the Builder authoring set, the agents-grouped tool's
// per-path gating, recurring, Fleet, and attached pipelines (the web wraps those
// for its activity pane). Session-bound where the handler needs it: find_tools
// searches the session catalog, send_status reaches the StatusCallback,
// stay_silent sets Silenced.
func (t *chatTurn) frameworkConversationalTools(sess *ToolSession) []AgentToolDef {
	out := []AgentToolDef{t.introspectToolDef()} // self-awareness — always
	// Knowledge — only when the agent has a corpus, else it hallucinates
	// doc_ids the handler must refuse. Skipped under the unified surface:
	// recall fronts knowledge search, and recall(id="doc:…") the drill-down.
	if !unifiedMemoryEnabled() && t.agentHasRetrievableContent() {
		out = append(out, t.searchKnowledgeToolDef(), t.fetchKnowledgeDocToolDef())
	}
	for _, n := range []string{"find_tools", "send_status", "stay_silent", "keep_going"} {
		if ct, ok := LookupChatTool(n); ok {
			out = append(out, ChatToolToAgentToolDefWithSession(ct, sess))
		}
	}
	if t.hasMachineExit() {
		out = append(out, t.changePhaseToolDef()) // way out of a resident phase (machine.go)
	}
	// The appeal channel belongs here, not in the web caller. Guardrails are
	// enforced on the dispatch path too (subTurn.guardrailEnforcer), and a
	// contestable block there appends "you may say so ONCE by calling
	// guardrail_appeal" — so a dispatched agent was invited to call a tool its
	// catalog did not contain, which is an invitation to improvise instead of
	// report. Same gate the web turn used, so no agent gains a schema it could
	// never use; the def also self-gates at call time, refusing unless a block
	// is actually pending.
	if agentHasContestableRule(t.agent) {
		out = append(out, t.guardrailAppealToolDef())
	}
	out = append(out, t.loadToolToolDef(sess)) // gateway for the agent's lazy custom tools
	out = append(out, t.skillToolDefs()...)    // read_skill / skill_knowledge_*; nil when skills off
	if unifiedMemoryEnabled() {
		// Collapsed surface: remember / recall / forget replace the six
		// memory + knowledge tools (knowledge_search + fetch were skipped near
		// the top of this function). Graph tools aren't part of the collapse
		// and stay gated by explicitOff.
		if t.hasAnyMemoryLayer() {
			out = append(out, t.unifiedMemoryTools()...)
		}
		if !t.explicitOff() {
			out = append(out, t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
		}
	} else {
		if !t.inferredOff() {
			out = append(out, t.memoryToolDef()) // Reference Memory (memory_save / search / forget)
		}
		if !t.explicitOff() {
			// Explicit (store_fact / forget_fact) + Graph (link_entities / recall_about).
			out = append(out, t.storeFactToolDef(), t.forgetFactToolDef(), t.searchFactsToolDef(), t.linkEntitiesToolDef(), t.recallAboutToolDef(), t.forgetGraphToolDef())
		}
	}
	// Working notes (rewritable running-state block) — its own opt-in layer,
	// independent of the Explicit/Reference memory toggles.
	if t.agent.EnableNotes {
		out = append(out, t.updateNotesToolDef())
	}
	out = append(out, cortexDeliverableTools(t.udb, t.agent.ID)...) // file_deliverable + note_to_cortex; nil for non-cortex
	// (send_to_builder removed — agents reach Builder by DIRECT dispatch
	// (agents action="run", agent="builder") and iterate with it in-thread, rather
	// than handing the user a one-click link into a separate Builder session.)
	return out
}

// dispatchExtraTools assembles the framework + custom-tool catalog every
// SUB-AGENT surface shares: the channel/dispatch path (RunAgentSync /
// RunAgentSyncContinuingRich) and the inline agents(action="run") path. It
// operates on an ALREADY-BUILT subTurn (receiver t) so each caller keeps its own
// subTurn wiring — dispatchChain, network, topic — that this shared assembly must
// not clobber. Returns the framework conversational tools (+ the agents grouped
// tool, attached pipelines, and direct custom tools) plus the custom-tool prompt
// section the caller appends to the system prompt. The caller MUST also wire
// ToolFallbackResolver = t.lazyToolFallback and DynamicTools =
// t.dynamicNewTempTools(sess) on the agent loop, and supplies poolUser/poolDB =
// the AGENT OWNER (so a synthetic channel runtime user still draws the owner's
// custom-tool pool). channel-chat tools and parent-tool inheritance stay in the
// callers — their gating differs per surface.
func (t *chatTurn) dispatchExtraTools(sess *ToolSession, poolUser string, poolDB Database) (extraTools []AgentToolDef, customToolPrompt string) {
	extraTools = append(extraTools, t.frameworkConversationalTools(sess)...)
	// agents grouped tool — sub-agents (OwnedBy set) are LEAVES (no dispatch
	// surface → no depth cascades); top-level targets get the full surface;
	// Builder targets stay read-only on dispatch.
	if t.agent.OwnedBy == "" {
		extraTools = append(extraTools, t.agentsGroupedToolDef(!isBuilderAgent(t.agent.ID)))
	}
	extraTools = append(extraTools, t.buildAttachedPipelineToolDefs()...)
	// App-contributed tools for this agent (core/agent_tool_providers.go). The
	// pool USER is the agent's owner, not the acting runtime identity, for the
	// same reason the custom-tool pool below uses it: a channel run acts as a
	// synthetic per-chat user, and asking an app about that identity would find
	// nothing bound to it.
	extraTools = append(extraTools, AgentProvidedTools(sess, poolUser, t.agent.ID)...)
	// Custom (temp) tools — hydrate the session from the owner's pool + the
	// agent-scoped kit, then split direct/lazy. Without the hydrate the session
	// is empty (the ts3_client_status-over-a-channel bug).
	t.privateMode = agentForcesPrivate(t.agent)
	t.loadAgentTempTools(sess, poolUser, poolDB)
	direct, ctp := t.setupCustomTools(sess)
	extraTools = append(extraTools, direct...)
	return extraTools, ctp
}

// setupCustomTools resolves the agent's custom (temp) tools the SAME way on the
// web (runPlan) and channel/dispatch surfaces — the third catalog beside the
// framework set. Zero-arg customs (and the agent's own deliberately-attached
// kit) come back as DIRECTLY callable tools; has-args customs are presented as a
// name+desc prompt section (returned) and loaded on demand via load_tool, with
// the turn's lazy-tool maps populated so load_tool + lazyToolFallback resolve
// them. Callers must: append the direct tools to the catalog, append the prompt
// section to the system prompt, and wire ToolFallbackResolver = lazyToolFallback
// + DynamicTools = dynamicNewTempTools(sess) on the agent loop. Without this on
// the dispatch path, a channel agent simply never sees its own custom tools
// (e.g. ts3_client_status works in the web chat but is absent over a channel).
func (t *chatTurn) setupCustomTools(sess *ToolSession) (direct []AgentToolDef, lazyPromptSection string) {
	allCustomTools := temptool.BuildAgentToolDefs(sess)
	// Credential scope enforcement: drop any tool whose backing credential this
	// agent may not use. credentialDenySet resolves the effective deny set — tier 1
	// (a credential's AllowedUsers vs the session user) ∪ tier 2 (the agent's own
	// DisabledCredentials opt-outs). The backing TempTool carries .Credential;
	// api/toolbox tools have one, shell tools don't (empty → never denied).
	if deny := credentialDenySet(t.agent, sess.Username); len(deny) > 0 {
		kept := allCustomTools[:0]
		var dropped []string
		for _, td := range allCustomTools {
			if lt := sess.LookupTempTool(td.Tool.Name); lt != nil && deny[lt.Credential] {
				dropped = append(dropped, td.Tool.Name)
				continue
			}
			kept = append(kept, td)
		}
		allCustomTools = kept
		if len(dropped) > 0 {
			Log("[orchestrate.scope] agent=%s credential-denied, dropped %d tool(s): %v", t.agent.ID, len(dropped), dropped)
		}
	}
	t.wrapToolsForActivity(sess, allCustomTools, t.agent)
	t.staticTempToolNames = map[string]bool{}
	t.lazyCustomToolNames = map[string]bool{}
	t.loadedCustomTools = map[string]bool{}
	t.lazyCustomToolDefs = map[string]AgentToolDef{}
	// agentOwnTools is populated by loadAgentTempTools on EVERY surface (web,
	// channel, dispatch, scheduled) — the agent's scoped kit rides the direct
	// catalog everywhere. Only shared-pool has-args tools take the lazy path.
	agentKit := t.agentOwnTools
	// Auto-elevation (visibility only, never access): a lazy shared tool the
	// turn's intent literally names (Tier 1) or that this agent load_tool's
	// every run (Tier 2) joins the direct catalog for this turn — the
	// fetch_url-over-moltbook class dies here instead of relying on the
	// owner noticing and scoping.
	elevated := t.elevatedToolSet(sess, allCustomTools, agentKit)
	var lazyCustomTools []AgentToolDef
	var trialDemoted int
	for _, td := range allCustomTools {
		// The agent-kit bypass exists so an agent's CURATED kit is callable
		// without a load_tool round-trip — worth the schema's prompt cost
		// because someone chose to put it there. A Trial tool is the opposite:
		// it landed on this agent because an authoring turn had to put it
		// somewhere, and nobody has vouched for it yet. Letting those through
		// the bypass means an authoring agent's prompt grows by a full JSON
		// schema for every tool it has ever drafted — the whole reason the
		// lazy split exists. Trial tools take the lazy path until confirmed.
		kit := agentKit[td.Tool.Name]
		if kit && isTrialTool(sess, td.Tool.Name) {
			kit = false
			trialDemoted++
		}
		if reason := elevated[td.Tool.Name]; reason != "" && !kit {
			Log("[orchestrate.elevate] agent=%s: %q elevated to the direct catalog (%s)", t.agent.ID, td.Tool.Name, reason)
		}
		if len(td.Tool.Parameters) == 0 || kit || elevated[td.Tool.Name] != "" {
			direct = append(direct, td)
			t.staticTempToolNames[td.Tool.Name] = true
		} else {
			lazyCustomTools = append(lazyCustomTools, td)
			t.lazyCustomToolNames[td.Tool.Name] = true
			t.lazyCustomToolDefs[td.Tool.Name] = td
		}
	}
	if trialDemoted > 0 {
		// Not silent: a tool moving out of the always-loaded catalog changes
		// how the LLM must reach it (load_tool first), so leave a trail.
		Log("[orchestrate.tools] agent=%s: %d unconfirmed (trial) tool(s) kept out of the inline catalog — reachable via load_tool; Confirm them in Extensions › Tools to pin their schemas", t.agent.ID, trialDemoted)
	}
	lazyPromptSection = lazyToolSectionFor(lazyCustomTools)

	return direct, lazyPromptSection
}

// lazyToolSectionFor renders the held-back-tools section of the prompt.
// Extracted so its wording can be asserted on: the difference between an agent
// using its own tools and reaching for fetch_url instead lives in this text,
// and nothing else would catch it changing.
func lazyToolSectionFor(lazyCustomTools []AgentToolDef) string {
	if len(lazyCustomTools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Your custom tools (load before use)\n")
	// These lose an unfair fight without this paragraph. A tool listed here
	// is a BULLET; a generic tool like fetch_url or web_search is a real
	// entry in the tool-calling API with a full schema, one call away. A
	// model comparing "purpose-built, but load it first" against "generic,
	// callable right now" takes the generic one nearly every time — and
	// nothing in the old wording said the first was preferable, only that
	// it was possible.
	//
	// Reported live: an agent built to talk to a social network reached for
	// fetch_url on every turn while its own posting tools sat in this list.
	// It was not ignoring instructions; it was never told these were the
	// right tools, only that they existed.
	b.WriteString("These are tools built for THIS agent's job. Their full definitions aren't loaded yet, so call `load_tool(names=[\"<name>\", ...])` first — pass ALL the ones you'll need in that one call; it returns their parameters and makes them callable. Then call them normally.\n\n")
	b.WriteString("**Prefer these over a generic tool.** If one of them covers what the user asked for, load it and use it — do NOT reach for `fetch_url`, `browse_page`, `web_search` or your own knowledge to do the same job by hand. A purpose-built tool here knows the service's endpoints, auth and shapes; doing it generically re-derives all of that and usually gets it wrong. The extra `load_tool` call is cheap and expected.\n\n")
	for _, td := range lazyCustomTools {
		desc := strings.TrimSpace(td.Tool.Description)
		if len(desc) > 200 {
			desc = desc[:200] + "…"
		}
		b.WriteString("- `" + td.Tool.Name + "` — " + desc + "\n")
	}
	return b.String()
}

// isTrialTool reports whether the session's live record for name is an
// unconfirmed (Trial) tool — authored on some turn and attached to an agent
// because it needed a durable home, not because anyone chose to keep it.
// Absent record → false: the caller's fallback is the existing behavior.
func isTrialTool(sess *ToolSession, name string) bool {
	if sess == nil {
		return false
	}
	if lt := sess.LookupTempTool(name); lt != nil {
		return lt.Trial
	}
	return false
}
