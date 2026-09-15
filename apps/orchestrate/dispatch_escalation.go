// Delegation as a way around a narrowed toolset, and the approval that closes it.
//
// An agent's AllowedTools list is not a containment boundary, and the shape of
// that is worth stating plainly because it is not obvious from any one file.
// The `agents` grouped tool is appended by structure rather than by allowlist —
// dispatchExtraTools adds it to every top-level agent, and runner_tool_catalog
// says of the same class of tools "Deliberately NOT intersected with
// AllowedTools". Dispatch then defaults to reaching any non-Hidden agent
// (effectiveDispatchMode → dispatchAll), and the target runs with ITS catalog,
// never intersected with the caller's. So an owner who narrows an agent to
// [web_search] has narrowed what it does DIRECTLY, and nothing else: it can ask
// the agent next to it to do the rest.
//
// The fix is not a computed tool diff. There is no reliable way to enumerate an
// agent's effective catalog without building its session — effectiveExtraToolsets
// is deliberately prose for exactly that reason ("the exact membership is
// assembled per session") — and a diff that drifts fails in the direction that
// matters: it reports no escalation and waves through the case the gate exists
// for. So the gate is IDENTITY, which is exact: the first time a narrowed agent
// delegates to a given target, the owner says yes or no. The diff appears only
// in the question, where being approximate costs nothing because a human is
// reading it.
//
// Who is gated: agents whose owner narrowed them. An agent on the default pool
// was never restricted, so delegating grants it nothing it did not have, and
// gating it would be friction with no boundary behind it.
//
// What counts as already approved: an explicit link (AllowedDispatchTargets) is
// the owner having already said this edge is fine, in the editor, deliberately.
// The gate honors it and never writes to it — a standing "always allow" goes to
// the tool-grant store instead, because adding one entry to an empty dispatch
// list would silently flip the mode from "all" to "only" and narrow every other
// target as a side effect of approving one.

package orchestrate

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// agentIsNarrowed reports whether an owner has restricted this agent's tools.
//
// The no-tools sentinel counts: it is a non-empty list meaning "none", which is
// the most narrowed an agent gets and the last one that should be able to
// delegate its way out.
func agentIsNarrowed(a AgentRecord) bool { return len(a.AllowedTools) > 0 }

// dispatchGrantScope namespaces edge approvals per CALLER, so approving
// "Research may delegate to Ops" does not approve it for every other agent.
func dispatchGrantScope(callerID string) string { return "dispatch:" + callerID }

// dispatchEdgeApproved is the non-blocking half: is this delegation already
// allowed, without asking anybody. Used as-is on the detached path, where there
// is no one to ask and a question would only ever resolve to "denied".
func (t *chatTurn) dispatchEdgeApproved(target AgentRecord) bool {
	if !agentIsNarrowed(t.agent) {
		return true // never restricted; delegation grants it nothing new
	}
	if dispatchListContains(t.agent, target.ID) {
		return true // the owner drew this link themselves
	}
	_, ok := findToolGrant(t.udb, dispatchGrantScope(t.agent.ID), "agents", target.ID)
	return ok
}

// confirmDispatchEdge is the whole policy for one delegation: allowed outright,
// already granted, or a question. A non-nil return is the refusal.
func (t *chatTurn) confirmDispatchEdge(target AgentRecord) error {
	if t.dispatchEdgeApproved(target) {
		return nil
	}
	note := dispatchEscalationNote(t.agent, target)
	prompt := fmt.Sprintf("Let %s hand this to %s?", t.agent.Name, target.Name)
	if note != "" {
		prompt = fmt.Sprintf("Let %s hand this to %s, which %s?", t.agent.Name, target.Name, note)
	}
	ok := t.escalateToolConfirm(toolConfirmRequest{
		tool:   "agents",
		prompt: prompt,
		detail: fmt.Sprintf("%s runs with a tool list you narrowed. %s runs with its own.", t.agent.Name, target.Name),
		// Read by the fail-closed path, which has no viewer to show a prompt
		// to, so this sentence is the whole explanation the trail gets.
		because: fmt.Sprintf("%s has a narrowed tool list and has not been approved to delegate to %s", t.agent.Name, target.Name),
		grant: &ToolGrant{
			Scope:  dispatchGrantScope(t.agent.ID),
			Tool:   "agents",
			Prefix: target.ID,
			Label:  t.agent.Name + " → " + target.Name,
		},
		grantLabel: "Always allow " + t.agent.Name + " → " + target.Name,
	})
	if ok {
		return nil
	}
	// Named in the refusal: the owner-side control is the dispatch list, and an
	// agent that says so is telling the user something they can act on. The
	// mechanism is not a secret here the way a guardrail's is — a guardrail
	// hides its rule so the model cannot reason around it, while this is a
	// permission the user is meant to grant or withhold knowingly.
	return fmt.Errorf("agents(run): %q was not run. This agent has a narrowed tool list, so delegating to another agent needs the user's approval and it was not given. "+
		"Ask the user to allow it, or to add %q to this agent's dispatch targets (Security & Access). Do what you can with your own tools, or say plainly that you could not.",
		target.Name, target.Name)
}

// dispatchEscalationNote says what the target can do that the caller cannot, in
// one clause, for the question.
//
// BEST EFFORT, and safe to be wrong: the gate is the edge, so an understated
// note costs a vaguer question and never a silent bypass. It compares the two
// allowlists and the two capability flags, which is what a record can answer;
// the per-session catalog (credential tools, custom tools, app-provided tools)
// is not knowable here and is not claimed.
func dispatchEscalationNote(caller, target AgentRecord) string {
	var parts []string
	if !agentIsNarrowed(target) {
		// The sharpest case and the easiest to say: the caller was given a list
		// and the target was given everything.
		parts = append(parts, "runs with the full tool pool")
	} else if extra := toolsBeyond(caller, target); len(extra) > 0 {
		parts = append(parts, "can "+strings.Join(extra, ", "))
	}
	if agentCanAuthor(target) && !agentCanAuthor(caller) {
		parts = append(parts, "can author agents and tools")
	}
	if target.Fleet && !caller.Fleet {
		parts = append(parts, "can manage the fleet")
	}
	return strings.Join(parts, " and ")
}

// toolsBeyond names what is on the target's allowlist and not on the caller's,
// capped so a question stays a question rather than becoming a catalog.
func toolsBeyond(caller, target AgentRecord) []string {
	have := map[string]bool{}
	for _, n := range caller.AllowedTools {
		have[strings.TrimSpace(n)] = true
	}
	var extra []string
	for _, n := range target.AllowedTools {
		if n = strings.TrimSpace(n); n != "" && !have[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(extra)
	const show = 3
	if len(extra) > show {
		rest := len(extra) - show
		extra = append(extra[:show:show], fmt.Sprintf("%d more", rest))
	}
	return extra
}

// --- the recipe doors -------------------------------------------------------
//
// A pipeline and a machine are recipes, not identities, and most of what they
// do is already bounded by the caller: a worker stage's Tools list is an
// INTERSECTION with the catalog it inherits (resolveStageTools), and on the
// dispatch path that catalog is nil — RunPipelineDefSync passes no tools — so
// those stages run tool-less. Nothing there to escalate, and gating it would be
// friction over an empty set.
//
// What escalates is a stage or phase that names an AGENT. That runs through
// RunAgentSync with the agent's OWN catalog, and it does not pass the agent
// gate (agentsRunGate) on the way, so this door is the only place it can be
// caught. A recipe that runs ANOTHER recipe counts too: this does not follow
// the reference, so an unfollowed one is treated as reaching something unknown
// rather than as reaching nothing.

// recipeReach is what a saved recipe can get to beyond its caller's own reach.
type recipeReach struct {
	Agents []string // agents it dispatches to, by the name the recipe uses
	Nested bool     // it runs another saved recipe, which is not followed from here
}

func (r recipeReach) escalates() bool { return len(r.Agents) > 0 || r.Nested }

// note is the clause the question uses.
func (r recipeReach) note() string {
	var parts []string
	if len(r.Agents) > 0 {
		shown := r.Agents
		const show = 3
		if len(shown) > show {
			shown = append(shown[:show:show], fmt.Sprintf("%d more", len(r.Agents)-show))
		}
		parts = append(parts, "runs "+strings.Join(shown, ", "))
	}
	if r.Nested {
		parts = append(parts, "runs other saved recipes")
	}
	return strings.Join(parts, " and ")
}

// pipelineReach walks the stages, bodies included — a fanout's or a loop's body
// dispatches exactly like a top-level stage does.
func pipelineReach(def PipelineDef) recipeReach {
	var r recipeReach
	seen := map[string]bool{}
	for _, s := range flattenStages(def.Stages) {
		if a := strings.TrimSpace(s.Agent); a != "" && !seen[a] {
			seen[a] = true
			r.Agents = append(r.Agents, a)
		}
		if strings.TrimSpace(s.Machine) != "" {
			r.Nested = true
		}
	}
	sort.Strings(r.Agents)
	return r
}

// machineReach is the same question of a machine's phases.
func machineReach(def MachineDef) recipeReach {
	var r recipeReach
	seen := map[string]bool{}
	for _, p := range def.Phases {
		if a := strings.TrimSpace(p.Agent); a != "" && !seen[a] {
			seen[a] = true
			r.Agents = append(r.Agents, a)
		}
		if strings.TrimSpace(p.Pipeline) != "" || strings.TrimSpace(p.Machine) != "" {
			r.Nested = true
		}
	}
	sort.Strings(r.Agents)
	return r
}

// recipeEdgeApproved is the non-blocking half, for the detached path.
func (t *chatTurn) recipeEdgeApproved(id, name string, reach recipeReach) bool {
	if !agentIsNarrowed(t.agent) || !reach.escalates() {
		return true
	}
	if dispatchListNames(t.agent.AllowedDispatchTargets, id, name) {
		return true
	}
	_, ok := findToolGrant(t.udb, dispatchGrantScope(t.agent.ID), "agents", id)
	return ok
}

// confirmRecipeEdge is the agent-door policy at the pipeline and machine doors:
// a narrowed caller running a recipe that reaches an agent is a question, asked
// once per edge.
func (t *chatTurn) confirmRecipeEdge(kind, id, name string, reach recipeReach) error {
	if t.recipeEdgeApproved(id, name, reach) {
		return nil
	}
	prompt := fmt.Sprintf("Let %s run the %s %s, which %s?", t.agent.Name, kind, name, reach.note())
	ok := t.escalateToolConfirm(toolConfirmRequest{
		tool:    "agents",
		prompt:  prompt,
		detail:  fmt.Sprintf("%s runs with a tool list you narrowed. The agents this %s runs have their own.", t.agent.Name, kind),
		because: fmt.Sprintf("%s has a narrowed tool list and has not been approved to run %s %q", t.agent.Name, kind, name),
		grant: &ToolGrant{
			Scope:  dispatchGrantScope(t.agent.ID),
			Tool:   "agents",
			Prefix: id,
			Label:  t.agent.Name + " → " + kind + " " + name,
		},
		grantLabel: "Always allow " + t.agent.Name + " → " + name,
	})
	if ok {
		return nil
	}
	return fmt.Errorf("agents(run, %s=%q) was not run. This agent has a narrowed tool list, and that %s hands work to other agents, so it needs the user's approval and it was not given. "+
		"Ask the user to allow it, or to add %q to this agent's dispatch targets (Security & Access). Do what you can with your own tools, or say plainly that you could not.",
		kind, name, kind, name)
}
