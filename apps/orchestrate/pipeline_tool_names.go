// Catching a mistyped stage tool name at SAVE time.
//
// The machine editor has done this since the class was first seen: a phase's
// tool list is an exact-name filter, a name that misses matches nothing, and
// the cost lands at RUN time on a turn nobody is watching. A pipeline stage's
// list is the same filter (resolveStageTools — literally the same function),
// and it had no such check, so the identical typo failed identically and
// silently in the other primitive.
//
// A pipeline is WORSE placed for it, not better. A machine's steps run for
// whichever agent carries the machine, and the checker can ask those agents
// what they hold. A pipeline is invoked by whichever agent ATTACHED it, and
// several can, so "the catalog" is a different set per caller — which is the
// argument for a stage declaring a reach rather than a list, and the reason
// this check is generous where it cannot be certain.
//
// It reports and does not refuse, for the same reason the machine one does:
// the catalog is genuinely dynamic — an MCP server registers its tools when it
// first connects, a credential can be added tomorrow — so a name we cannot
// resolve today is usually a typo and occasionally early.

package orchestrate

import (
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// unknownStageToolFindings reports stage tool names nothing in the catalog
// answers to, one line each, with the likely correction when the name is
// recoverable. The judgement is phaseToolFindings' — same rules, same
// suggestions — over stages flattened out of their nesting.
func unknownStageToolFindings(udb Database, user string, def PipelineDef) []string {
	named := false
	for _, s := range flattenStages(def.Stages) {
		if len(s.Tools) > 0 {
			named = true
			break
		}
	}
	if !named {
		return nil
	}
	// The agents that can INVOKE this pipeline, rather than the ones that
	// carry it: an attachment is what puts a pipeline in front of a catalog.
	// Any of them holding the name is enough — a name only one caller has is
	// a narrower pipeline, not a broken one.
	return stageToolFindings(def, knownPipelineToolNames(udb, user, def))
}

// stageToolFindings is the judgement, separated from gathering the catalog so
// the rules can be tested against a catalog written out by hand rather than
// against whatever the test binary happens to have registered. The machine
// side splits the same way, for the same reason.
func stageToolFindings(def PipelineDef, known map[string]bool) []string {
	var out []string
	for _, s := range flattenStages(def.Stages) {
		matched := 0
		var missing []string
		for _, n := range s.Tools {
			if n = strings.TrimSpace(n); n == "" || n == NoToolsMarker {
				continue // the explicit "nothing" is a setting, not a name
			}
			if known[n] {
				matched++
				continue
			}
			missing = append(missing, n)
		}
		for _, n := range missing {
			line := "stage " + s.Name + ": tool " + strconv.Quote(n) + " is not a tool any agent that can run this pipeline holds"
			if suggestion := didYouMeanTool(n, known); suggestion != "" {
				line += " — did you mean " + strconv.Quote(suggestion) + "?"
			} else {
				line += ` — names must match the catalog exactly (a remote MCP tool is published as "<server>_<tool>", lowercased). A reach ("read", "none") says what you mean without naming anything.`
			}
			out = append(out, line)
		}
		// The whole list missing is its own finding: the stage keeps its
		// inherited catalog rather than losing it (resolveStageTools returns
		// the pool when nothing matches is NOT true here — it returns the
		// empty intersection), so the stage runs TOOL-LESS, which is the
		// opposite of what a list of names was written for.
		if matched == 0 && len(missing) > 0 {
			out = append(out, "stage "+s.Name+": none of its "+strconv.Itoa(len(missing))+
				" tool names resolve, so the stage runs with NO tools rather than the ones it names")
		}
	}
	return out
}

// knownPipelineToolNames is every name a stage could legitimately hold.
// Deliberately generous, for the reason didYouMeanTool's neighbours give: a
// false positive puts a permanent red mark against a correct name, and a
// checklist that cries wolf gets ignored wholesale.
func knownPipelineToolNames(udb Database, user string, def PipelineDef) map[string]bool {
	known := make(map[string]bool, 256)
	for _, o := range phaseToolOptions(user) {
		known[o.Value] = true
	}
	for _, ct := range RegisteredChatTools() {
		known[ct.Name()] = true
	}
	sess := &ToolSession{Username: user}
	for _, td := range Secure().BuildTools(sess) {
		known[td.Tool.Name] = true
	}
	// App-contributed tools attach per AGENT at run time. Ask on behalf of
	// every agent this pipeline is attached to — those are its callers, and
	// their catalogs are the ones a stage narrows.
	for _, a := range listAgents(udb, user) {
		if !agentRunsPipeline(a, def) {
			continue
		}
		for _, td := range AgentProvidedTools(sess, user, a.ID) {
			known[td.Tool.Name] = true
		}
	}
	return known
}

// agentRunsPipeline reports whether this agent can invoke the pipeline —
// today, whether it is attached. Matched on NAME as well as id because that
// is what an attachment stores (a pipeline is portable, so the recipe names
// what somebody wrote rather than the row it landed in).
func agentRunsPipeline(a AgentRecord, def PipelineDef) bool {
	for _, p := range a.AttachedPipelines {
		p = strings.TrimSpace(p)
		if p != "" && (strings.EqualFold(p, def.Name) || p == def.ID) {
			return true
		}
	}
	return false
}

// flattenStages walks a stage list and everything nested inside it, so a
// loop's or a fanout's body is checked like any other stage. A typo does not
// become correct by being one level down.
func flattenStages(stages []PipelineStage) []PipelineStage {
	var out []PipelineStage
	for _, s := range stages {
		out = append(out, s)
		if len(s.Body) > 0 {
			out = append(out, flattenStages(s.Body)...)
		}
	}
	return out
}

// stageReachAdvice is reachAdvice for a pipeline's stages, flattened so a
// loop's or a fanout's body is judged like any other stage.
//
// A stage needs this MORE than a step does. A machine runs for whichever agent
// carries it; a pipeline runs for whichever agent ATTACHED it, and several can,
// so a list of exact names describes one caller's catalog and misdescribes the
// next one's.
func stageReachAdvice(user string, def PipelineDef) []string {
	stages := flattenStages(def.Stages)
	units := make([]toolScopeUnit, 0, len(stages))
	for _, s := range stages {
		units = append(units, toolScopeUnit{Label: "stage " + s.Name, Reach: StageReach(s), Tools: s.Tools})
	}
	return reachAdviceFor(units, user)
}

// panelVoiceFindings reports a panel whose voices are PART agents and part
// not, because that mixture is where a misspelled agent name hides.
//
// A voice that matches no agent is a ROLE the worker answers as — a real
// option, and the reason a panel of perspectives works on a deployment where
// nobody has authored three agents. So an unmatched name is not an error and
// must never be reported as one. But resolution is case-insensitive, which
// means the names that fall through to roles are genuine misspellings, and
// "Skpetic" quietly becomes a worker impersonating your Skeptic agent — same
// transcript, different thinking, nothing said.
//
// FIRES ONLY ON A MIX. An all-role panel is deliberate and an all-agent panel
// is fine; naming two agents and one stranger is the shape that is usually a
// typo. Advice that fires on a correct configuration is advice people learn to
// scroll past, and it takes the real findings with it.
func panelVoiceFindings(udb Database, user string, def PipelineDef) []string {
	var out []string
	for _, s := range flattenStages(def.Stages) {
		if s.Kind != StagePanel || len(s.Panel) < 2 {
			continue
		}
		var agents, roles []string
		for _, v := range s.Panel {
			if v = strings.TrimSpace(v); v == "" {
				continue
			}
			if _, ok := findAgentByNameOrID(udb, user, v); ok {
				agents = append(agents, v)
				continue
			}
			roles = append(roles, v)
		}
		if len(agents) == 0 || len(roles) == 0 {
			continue
		}
		out = append(out, "stage "+s.Name+" mixes agents and roles: "+strings.Join(agents, ", ")+
			" resolve to agents, and "+strings.Join(roles, ", ")+" do not, so the worker will answer as them. "+
			"That is a real option — a role needs no agent. Check the spelling if one of those was meant to be an agent, "+
			"because a name that misses becomes a role rather than an error.")
	}
	return out
}

// pipelineChecklist is what every pipeline surface shows as work remaining:
// the soft advice the definition works out for itself, then the tool names
// that will not resolve. One function, for the reason machineChecklist is
// one: a check added in one place must not quietly fail to exist in the
// others.
func pipelineChecklist(udb Database, user string, def PipelineDef) []string {
	out := append(def.Advice(), unknownStageToolFindings(udb, user, def)...)
	out = append(out, panelVoiceFindings(udb, user, def)...)
	return append(out, stageReachAdvice(user, def)...)
}
