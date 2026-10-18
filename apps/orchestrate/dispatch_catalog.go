package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

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

// dispatchTurnContext is what a web turn puts beside the person's message
// (runner.go, turnContext): the playbooks the message matches, delivered now;
// the skills already consulted; skill and agent trigger hints; recall hints.
// A dispatched run built none of it, so a skill whose rules the message called
// for was never delivered. Run-only, beside the message, never stored, and
// kept out of the system prompt so its prefix stays cached.
func (t *chatTurn) dispatchTurnContext(message string) string {
	t.playbookMsg = message
	t.autoDeliverPlaybooks(message)
	out := t.renderTriggeredSkills()
	out += t.renderSkillTriggerHints(message)
	out += t.renderAgentTriggerHints(message)
	out += t.renderRecallHints(message)
	return strings.TrimSpace(out)
}

// withTurnContext is message with the turn context after it.
func withTurnContext(message, context string) string {
	if context = strings.TrimSpace(context); context == "" {
		return message
	}
	if strings.TrimSpace(message) == "" {
		return context
	}
	return message + "\n\n" + context
}

// dispatchGapFollowUp is the agent's gap check (GapCheck) over a dispatched
// run's reply, as the follow-up that asks the agent to fill what it found, or
// "" when it found nothing or the agent does not check. A web turn checks its
// workers' findings and runs fills before synthesis; a dispatched run is one
// loop with no workers, so the check reads its reply and the fill is one more
// pass of the same loop.
func (t *chatTurn) dispatchGapFollowUp(message, reply string) string {
	if t == nil || !t.agent.GapCheck || strings.TrimSpace(reply) == "" {
		return ""
	}
	gaps := t.runGapCheck(message, []PlanStep{{ID: 1, Title: message, Status: StepDone, Findings: reply, Output: reply}}, 2)
	if len(gaps) == 0 {
		return ""
	}
	t.turnDiag("gap_check", fmt.Sprintf("the gap check found %d gap(s) in the reply; one more pass fills them", len(gaps)))
	var b strings.Builder
	b.WriteString("Before you finish: your answer leaves gaps. Fill each with something concrete (a named example, a date, a number, a source), or say plainly that you could not find it. Then give the whole answer again, complete:\n")
	for _, g := range gaps {
		b.WriteString("- " + g.Title + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
