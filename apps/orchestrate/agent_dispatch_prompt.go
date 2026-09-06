package orchestrate

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// dispatchSystemPrompt assembles the system prompt for an external/channel
// dispatch (RunAgentSync / RunAgentSyncContinuingRich): the agent's context
// (rules + facts) over its OrchestratorPrompt, then the Available-agents/skills
// block, then the "Your custom tools (load before use)" section, then — for a
// Cortex agent answering OUTSIDE its own cortex thread — its recent cortex feed.
// Single source of truth so adding a prompt block (as custom-tools just did)
// can't get appended to one dispatch site and forgotten on the other.
func dispatchSystemPrompt(target AgentRecord, subFacts []MemoryFact, availableBlock, customToolPrompt, sessID string, runtimeDB Database, user string) string {
	sysPrompt := prependAgentContext(gatedPersonaFor(target, target.OrchestratorPrompt), target, subFacts, agentOperatingNotes(runtimeDB, target))
	sysPrompt += availableBlock
	sysPrompt += customToolPrompt
	if target.Cortex && sessID != cortexSessionID(target.ID) {
		sysPrompt += cortexContextBlock(runtimeDB, target.ID)
	}
	// Same per-agent capability guidance the web turn gets, so a capability
	// follows the AGENT to the channel/dispatch surface instead of silently
	// thinning out. Without this, an agent reached over a channel (e.g. an
	// iMessage bridge) ran on a materially thinner prompt than in web chat —
	// the "works in the web UI but not over a channel" drift.
	sysPrompt = appendAgentCapabilityBlocks(sysPrompt, target, runtimeDB, user, false)
	return sysPrompt
}

// appendAgentCapabilityBlocks appends the per-agent capability guidance that
// must follow an agent to ANY surface — the web runPlan turn AND the channel/
// dispatch turn — so behavior doesn't diverge by which code path built the
// prompt. This is THE single place these blocks live; both paths call it, so a
// new capability block added here reaches every surface at once (the tool
// catalog was unified this way via frameworkConversationalTools; this does the
// same for the prompt). Every block self-gates on agent config, so a worker or
// plain chat agent that lacks a capability gets nothing extra. Message-dependent
// content (triggered-skill instructions, per-turn trigger hints) is deliberately
// NOT here — it rides the user message for prompt-cache stability.
func appendAgentCapabilityBlocks(sys string, agent AgentRecord, udb Database, user string, hasPlanSet bool) string {
	// Framework orchestration blocks lifted out of individual seed personas
	// (see framework_prompts.go) so every capable agent inherits them, not
	// just whichever seed happened to hand-author the prose. Gated by
	// capability; splices in ahead of the agent's own plan-guidance addendum.
	// Passes the prompt-so-far so a block a cloned persona already carries
	// isn't injected twice.
	sys += frameworkPromptBlocks(sys, agent, hasPlanSet)
	if g := strings.TrimSpace(agent.PlanGuidance); g != "" {
		sys += "\n\n## Plan guidance\n" + g
	}
	sys += availableSkillsBlock(agent, udb, user)
	sys += searchOrderGuidanceBlock(agent, agentRecordHasRetrievableContent(agent))
	// Plan-first + pre-mortem discipline. On for an explicit PreMortem opt-in AND
	// by default for any Cortex agent: a persistent channel/home-thread presence
	// IS an orchestrator that accomplishes goals over a channel, which is exactly
	// the case this discipline is for (the Wiwee/iMessage case). The block self-
	// scopes to GOALS, so a Cortex agent still handles casual chat directly — the
	// default costs nothing on ordinary messages.
	if agent.PreMortem || agent.Cortex {
		// await_result only mounts for Fleet agents; plan_set only exists on
		// the web runPlan surface (the caller knows which surface it is).
		sys += "\n\n" + preMortemPlanningBlock(hasPlanSet, agent.Fleet)
	}
	// The guidance blocks appended above (search-order guidance, plan/pre-mortem,
	// skills) also hardcode legacy tool names in prose. Re-run the mode-aware
	// rewrite over the full assembled prompt so those are covered too on the
	// interactive surfaces. Idempotent with the prependAgentContext pass — the
	// unified replacements carry no legacy token, so a second run changes nothing.
	return rewriteMemoryToolNames(sys)
}

// resolveDispatchThink decides whether a dispatched agent thinks, the SAME way
// the chat surface does — so an agent reached via a channel, an external
// dispatch, or an inline agents(run) runs with the same default as if invoked
// directly from Agency. Base = the orchestrator route default; the target's
// explicit Think="on"/"off" wins; empty Think falls through to the route default
// rather than a dispatch-only override. Single source of truth: this was
// copy-pasted at three dispatch sites.
func resolveDispatchThink(target AgentRecord) bool {
	think := true
	if p := RouteThink("app.orchestrate.orchestrator"); p != nil {
		think = *p
	}
	switch target.Think {
	case "on":
		think = true
	case "off":
		think = false
	}
	return think
}
