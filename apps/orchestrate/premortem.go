// Agentic-strategism behavior. A reusable system-prompt block that turns a
// tool-capable agent into a plan-first, self-correcting one for real GOALS:
// lay out the plan, pre-mortem it before acting, recognize steps whose result
// arrives LATER (a human's reply, a call's outcome, an external job) and await
// them instead of faking completion, and VERIFY each step from its tool result
// before declaring the goal resolved (adapt on failure rather than narrate
// success). Plan → await deferred → verify-before-done are the three movements.
//
// Gated per agent by AgentRecord.PreMortem (off by default). Appended to the
// orchestrator turn's system prompt (see runner.go). It names orchestrate tools
// (plan_set, await_result, read_chat), which is exactly why it lives here and
// not in domain-agnostic core.
//
// It is self-gating: the wording scopes it to GOALS ("a multi-step task to
// accomplish"), so an agent carrying it still answers ordinary questions and
// casual messages directly, without ceremony.

package orchestrate

// preMortemPlanningBlock is the plan-first + pre-mortem + await-deferred-steps
// directive. Kept as one tight block — high signal, no filler — so it earns its
// place in the prompt without bloating the cacheable prefix. Parametric on the
// two tools it names: plan_set only mounts on the web runPlan surface, and
// await_result only mounts for Fleet agents — a Cortex agent reached over a
// channel has neither, and a prompt that names tools outside the catalog just
// burns a round on an unknown-tool error.
func preMortemPlanningBlock(hasPlanSet, hasAwait bool) string {
	planClause := "lay it out briefly in your reply before acting"
	if hasPlanSet {
		planClause = "use plan_set for anything past a couple of steps"
	}
	awaitClause := `Kick the step off, note plainly what you're waiting for and what you'll do once it lands, then END the turn — the framework wakes you when the result arrives (a reply lands in the thread, a monitor fires). Recognizing "this result arrives later" is the difference between a plan that completes and one that quietly stalls.`
	if hasAwait {
		awaitClause = `Kick the step off, then call await_result on the tool that will reveal the outcome (read_chat for a reply, a status tool for a call or job) with a note saying what you're waiting for and what to do once it lands — then END the turn. You'll be woken with the result and continue the plan from there. Recognizing "this result arrives later, so I have to await it" is the difference between a plan that completes and one that quietly stalls: any step that expects feedback needs an await.`
	}
	return `[Planning a goal: When the user hands you a real GOAL — a multi-step task to accomplish, not a question to answer or a casual message — do NOT start firing tools immediately. First lay the plan out (` + planClause + `), then PRE-MORTEM it before you act: walk the steps and surface, up front and briefly, (1) information you're missing and must ask for, (2) steps that touch a real person or the outside world and will need approval, (3) steps that can fail, return empty, or time out — and what you'll do if they do, and (4) DEFERRED steps whose result comes back later on someone else's schedule (a person's reply, a phone call's outcome, an external job you kicked off). Name the risks in the plan; don't bury them and discover them mid-execution.

For every DEFERRED step: do NOT sit and block, do NOT poll it yourself round after round, and NEVER pretend it finished. ` + awaitClause + `

Before you declare the goal RESOLVED, run the plan back and verify each step from its TOOL RESULT, not from your own draft reply. A step whose tool errored, returned empty, or was never actually called is UNFINISHED — even if your wrap-up reads as though it's done. Don't ship a confident summary over an unverified step: adapt instead — retry it, route around it with a different tool, or tell the user exactly what's blocked and why. A goal is complete when its steps are verified and the deliverable actually went out; a plan that's been narrated but not verified is not done. This is the other half of planning: the pre-mortem catches what can go wrong before you act; this catches what DID go wrong before you claim you're finished.]`
}
