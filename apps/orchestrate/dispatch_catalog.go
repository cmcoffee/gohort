package orchestrate

import . "github.com/cmcoffee/gohort/core"

// A handed-over run's catalog, given what a web turn's gets.
//
// A web turn builds its catalog through resolveWorkerTools and runPlan, which
// apply three things over ALL of it: the agent's guardrails withhold the tools
// they make unusable, every result is scanned and fenced as the agent's scan
// scope says, and the session learns which tools and actions this caller
// really has. A dispatched run assembled its catalog elsewhere and applied the
// first two to its custom tools only and the third not at all, so a tool a
// rule forbade was still offered, a built-in's web result went unscanned, and
// an action the owner turned off (DisabledToolActions) could still be called.

// finalizeDispatchCatalog applies those passes to a dispatched run's whole
// catalog. Call it once the catalog is complete and before the machine
// narrows it.
func (t *chatTurn) finalizeDispatchCatalog(sess *ToolSession, tools []AgentToolDef) []AgentToolDef {
	tools = t.applyGuardrailToolWithholding(tools)
	for i := range tools {
		if !t.activityWrapped[tools[i].Tool.Name] {
			t.wrapToolsForActivity(sess, tools[i:i+1], t.agent)
		}
	}
	names := make([]string, 0, len(tools))
	for _, td := range tools {
		names = append(names, td.Tool.Name)
	}
	sess.SetAvailableTools(names)
	sess.SetWithheldActions(t.withheldToolActions())
	return tools
}
