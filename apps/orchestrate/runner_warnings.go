package orchestrate

import (
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// agentAuthoringToolNames is the set of tools whose successful
// invocation actually advances the build plan. Used by the
// hallucinated-authoring check below to decide whether a turn that
// closed without firing any of these "really" did the work it
// claimed in its text reply.
var agentAuthoringToolNames = map[string]bool{
	"create_agent": true,
	"update_agent": true,
	"clone_agent":  true,
	"delete_agent": true,
	"add_tool":     true,
	"tool_def":     true,
	"app_def":      true,
	"skill_def":    true,
	"pipeline_def": true,
}

// authoringWriteActions are the grouped-authoring-tool actions that CHANGE
// something. The grouped tools (app_def, tool_def, …) also read — get, list,
// help, test, verify — and a successful read must not vouch for a failed
// write. The live case: app_def get succeeded, all three app_def updates
// errored, and the reply announced the fixes as applied; counting the read as
// authoring-that-worked is what let the claim through.
var authoringWriteActions = map[string]bool{
	"create": true, "update": true, "delete": true, "patch": true,
	"add": true, "remove": true, "set": true, "save": true,
}

// authoringCallIsWrite reports whether a persisted authoring call attempted a
// change. Single-purpose tools (create_agent, add_tool) always do; a grouped
// tool is judged by its action argument, and an unreadable/absent action is
// treated as a write so the guard fails toward catching the claim.
func authoringCallIsWrite(tc PersistedToolCall) bool {
	raw, ok := tc.Args["action"]
	if !ok {
		return true
	}
	action, ok := raw.(string)
	if !ok {
		return true
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		return true
	}
	return authoringWriteActions[action]
}

// injectFalseUnavailabilityWarning catches a reply that blames the
// framework for a tool the agent actually has.
//
// The live case: Builder spent three turns insisting "I am unable to
// access tool_def", "this is a framework problem on my end", "report
// this error to the platform administrators" — and never emitted a
// single tool_def call. tool_def is in builderAuthoringTools(); a real
// refusal would have produced a visible ERROR tool result. The user was
// sent to debug a platform that was working.
//
// This is worse than an ordinary failure, so it earns its own guard: a
// plain error tells the user what broke, while a confident false
// unavailability tells them to go fix the wrong thing.
//
// Scoped to the FRAMEWORK AUTHORING tools, whose presence is a pure
// function of the agent record — no catalog plumbing, and no risk of
// firing on a tool the agent genuinely lacks.
func injectFalseUnavailabilityWarning(sess *ChatSession, turnToolCalls []PersistedToolCall, reply string, agent AgentRecord) bool {
	if sess == nil || strings.TrimSpace(reply) == "" {
		return false
	}
	// An agent without authoring rights is telling the truth.
	if !isBuilderAgent(agent.ID) && !agentCanAuthor(agent) {
		return false
	}
	named := falselyClaimedUnavailable(reply)
	if named == "" {
		return false
	}
	// If it actually called the tool this turn, the complaint is about a
	// real result, not a hallucinated absence.
	for _, tc := range turnToolCalls {
		if tc.Name == named {
			return false
		}
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role: "user",
		Content: "FRAMEWORK NOTICE: your previous reply said " + named + " is unavailable to you, or blamed a framework/platform error for not being able to use it. That is incorrect: " + named +
			" IS in your tool catalog for this turn, and you did not call it. Nothing is broken and there is nothing for the user to report. Call " + named +
			" now with real arguments. If a call fails, quote the actual error you received — do not infer unavailability from a call you never made.",
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// falselyClaimedUnavailable returns the authoring tool a reply claims to
// be unable to use, or "" when the reply makes no such claim.
//
// Requires the tool NAME and an inability phrase in the same sentence,
// so "tool_def failed with a validation error" (an honest report) and
// "I will call tool_def" (a promise) both stay clear of it.
func falselyClaimedUnavailable(reply string) string {
	inability := []string{
		"not in my available tools", "not available to me", "unable to access",
		"unable to use", "cannot access", "can't access", "cannot use",
		"can't use", "do not have access", "don't have access",
		"no access to", "is not available", "isn't available",
		"not able to access", "not able to use",
	}
	for _, sentence := range sentencesOf(strings.ToLower(reply)) {
		if !containsAnyOf(sentence, inability) {
			continue
		}
		for name := range agentAuthoringToolNames {
			if strings.Contains(sentence, name) {
				return name
			}
		}
		if strings.Contains(sentence, "skill_def") {
			return "skill_def"
		}
	}
	return ""
}

// injectAuthoringMismatchWarning detects the "claimed but didn't fire"
// pattern and, on a match, appends a synthetic user-role message to
// sess.Messages that the next turn's toLLMMessages will surface to
// the LLM as a corrective system note. Returns true when a warning
// was injected so the caller can flush the session save and emit a
// visible SSE warning.
func injectAuthoringMismatchWarning(sess *ChatSession, turnToolCalls []PersistedToolCall, reply string) bool {
	if sess == nil || sess.BuildPlan == nil {
		return false
	}
	pending := 0
	for _, s := range sess.BuildPlan.Steps {
		if s.Status != "done" && s.Status != "blocked" {
			pending++
		}
	}
	if pending == 0 {
		return false
	}
	if strings.TrimSpace(reply) == "" {
		return false
	}
	for _, tc := range turnToolCalls {
		if agentAuthoringToolNames[tc.Name] {
			return false
		}
	}
	note := "FRAMEWORK NOTICE: your previous reply described authoring work but no add_tool / create_agent / update_agent call fired during that turn. The build plan still has " +
		fmt.Sprintf("%d pending step", pending)
	if pending != 1 {
		note += "s"
	}
	note += ". Do not describe work you haven't done. Re-do the next pending step by ACTUALLY calling the right authoring tool with concrete arguments. One tool call per turn — that is the only path that advances the plan."
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "user",
		Content: note,
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// injectSkippedGapReportWarning is the enforcement half of report_build_gaps.
// The tool's contract — "call this BEFORE your final reply" — lived only in the
// prompt and the tool description: BuildPlanState.GapsReported was set and
// never read, so a model that simply never called it wrapped up unchecked, and
// every unverified tool it authored went unmentioned.
//
// Fires only when the plan LOOKS FINISHED (no pending or in-progress steps) and
// the turn produced a reply anyway without ever gap-checking. That precondition
// matters: the plan-approval turn and every mid-execution turn legitimately end
// with a reply and no gap report, and warning on those would train the model to
// ignore the notice. The complement of injectAuthoringMismatchWarning above,
// which covers the pending-steps-but-nothing-fired case.
//
// Names the unverified tools directly, since skipping the call is exactly how
// their standing stays invisible — the correction has to carry the payload the
// skipped tool would have delivered, or the next turn just re-runs blind.
func injectSkippedGapReportWarning(sess *ChatSession, udb Database, reply string) bool {
	if sess == nil || sess.BuildPlan == nil || len(sess.BuildPlan.Steps) == 0 {
		return false
	}
	if sess.BuildPlan.GapsReported || strings.TrimSpace(reply) == "" {
		return false
	}
	for _, s := range sess.BuildPlan.Steps {
		if s.Status != "done" && s.Status != "blocked" {
			return false // still executing — a reply here is legitimate
		}
	}
	note := "FRAMEWORK NOTICE: your previous reply closed out the build plan without calling report_build_gaps. That call is required before any reply that presents the build as finished — it is what surfaces blocked steps and tools that are not verified. Marking a step done is your OWN claim and is not evidence the tool works."
	if un := unverifiedTools(udb, sess.ID); len(un) > 0 {
		note += "\n\nTools you authored that do NOT currently stand verified:"
		for _, u := range un {
			note += fmt.Sprintf("\n  - %s — %s", u.Tool, u.Reason)
		}
		note += "\n\nYou may have told the user these are working. Verify each one now (add_tool with test_args, or tool_def(action=\"test\")), then say plainly what was actually confirmed and what was not."
	} else {
		note += " Call it now and address whatever it returns before restating that the work is done."
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "user",
		Content: note,
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// injectFailedAuthoringWarning catches the OTHER half of the hallucinated-
// authoring pattern: an authoring tool DID fire this turn but EVERY such call
// errored, yet the reply reads as a success claim. Unlike the build-plan check
// above it needs no BuildPlan, so it also covers a regular agent (the live
// moltbook case: tool_def create/update errored repeatedly, then the agent said
// "Done! I've rebuilt the toolbox"). Injects a hidden corrective note so the
// next turn stops claiming success and actually fixes the call.
func injectFailedAuthoringWarning(sess *ChatSession, turnToolCalls []PersistedToolCall, reply string) bool {
	if sess == nil || strings.TrimSpace(reply) == "" {
		return false
	}
	fired, errored, ok := 0, 0, 0
	for _, tc := range turnToolCalls {
		if !agentAuthoringToolNames[tc.Name] || !authoringCallIsWrite(tc) {
			continue
		}
		fired++
		if strings.TrimSpace(tc.Err) != "" {
			errored++
		} else {
			ok++
		}
	}
	// Only when authoring was attempted, ALL of it errored, and the reply claims
	// success without acknowledging the failure.
	if fired == 0 || ok > 0 || errored == 0 || !claimsSuccessWithoutAck(reply) {
		return false
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "user",
		Content: "FRAMEWORK NOTICE: your reply says a tool, agent, app, skill, or pipeline was created, updated, or fixed — but EVERY authoring call this turn that tried to CHANGE something returned an error, so nothing was saved. The old version is still live and the user still has the problem. Do NOT tell the user it's done, and do NOT describe the fixes as applied. Read the error text, correct the arguments, and make ONE fixed authoring call (prefer action=\"update\" over delete+recreate).",
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// injectPromisedAuthoringWarning catches the THIRD hallucinated-authoring
// quadrant, the one the other two miss by construction:
// injectAuthoringMismatchWarning needs a BuildPlan with pending steps, and
// injectFailedAuthoringWarning needs an authoring call that fired and errored.
// A turn that describes a toolbox in prose, calls nothing, and sets no plan
// satisfies neither — it closes clean and the work silently evaporates.
//
// The tell is tense. claimsSuccessWithoutAck looks for a past-tense claim
// ("Done! I've rebuilt it"); this looks for the forward-looking promise that
// precedes it ("Great! I will now create the vapi_calls toolbox") and is never
// followed by the call. Both observed shapes are lead-side, not a small-model
// artifact, so the check is deliberately tense-driven rather than model-gated.
//
// Conservative, in the same spirit as its siblings — a missed catch beats a
// false one, since a spurious notice trains the model to ignore all three:
//   - any authoring tool fired → not this pattern, stay quiet
//   - ask_user or plan_set fired → "I'll create X" is a legitimate promise
//     about a turn that hasn't happened yet (the approval and plan-card paths)
//   - the reply is a question → it's proposing, not promising
func injectPromisedAuthoringWarning(sess *ChatSession, turnToolCalls []PersistedToolCall, reply string) bool {
	if sess == nil || strings.TrimSpace(reply) == "" {
		return false
	}
	for _, tc := range turnToolCalls {
		if agentAuthoringToolNames[tc.Name] {
			return false
		}
		switch tc.Name {
		case "ask_user", "plan_set":
			return false
		}
	}
	if strings.HasSuffix(strings.TrimSpace(reply), "?") {
		return false
	}
	if !promisesAuthoringWithoutAction(reply) {
		return false
	}
	sess.Messages = append(sess.Messages, ChatMessage{
		Role:    "user",
		Content: "FRAMEWORK NOTICE: your previous reply said you were about to create or update a tool or agent, but no authoring call fired during that turn — tool_def / add_tool / create_agent / update_agent never ran, so nothing was saved and the user is waiting on work that never started. Describing the change is not making it. Make ONE concrete authoring call now with real arguments. If you need the user to approve first, call ask_user — do not promise in prose and end the turn.",
		Created: time.Now(),
		Hidden:  true,
	})
	return true
}

// promisesAuthoringWithoutAction reports whether reply contains a forward-
// looking promise to author something. Requires a promise marker, an authoring
// verb, and an authoring object in the SAME sentence — all three, or "I'll use
// the get_weather tool" (promise + object, ordinary dispatch) would trip it.
func promisesAuthoringWithoutAction(reply string) bool {
	promise := []string{
		"i will", "i'll", "i am going to", "i'm going to", "let me",
		"going to now", "next i", "now i", "proceeding to",
	}
	verbs := []string{
		"create", "creating", "build", "building", "author", "authoring",
		"add", "adding", "set up", "setting up", "define", "defining",
		"register", "registering", "make", "making", "update", "updating",
		"wire up", "wiring up",
	}
	objects := []string{
		"tool", "toolbox", "agent", "action", "pipeline", "skill",
	}
	for _, s := range sentencesOf(strings.ToLower(reply)) {
		if containsAnyOf(s, promise) && containsAnyOf(s, verbs) && containsAnyOf(s, objects) {
			return true
		}
	}
	return false
}

// sentencesOf splits text on sentence terminators and newlines. Crude by
// design — it exists so the three-signal test above can't match across
// unrelated clauses, not to parse prose correctly.
func sentencesOf(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n' || r == ';'
	})
}

func containsAnyOf(hay string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

// claimsSuccessWithoutAck reports whether reply asserts authoring success while
// NOT acknowledging a failure — the heuristic separating a false "it's done"
// from an honest "it didn't work." Conservative: any failure-acknowledging word
// suppresses it, so it errs toward NOT firing (a missed catch over a false one).
func claimsSuccessWithoutAck(reply string) bool {
	r := strings.ToLower(reply)
	for _, neg := range []string{
		"error", "fail", "couldn't", "could not", "didn't", "did not", "wasn't able",
		"was not able", "unable", "not able", "still broken", "isn't working", "not working",
		"went wrong", "try again", "reject", "no luck", "hasn't worked", "won't",
	} {
		if strings.Contains(r, neg) {
			return false
		}
	}
	for _, pos := range []string{
		"done", "created", "rebuilt", "rebuild", "fixed", "i've ", "i have ", "is live",
		"now live", "ready to use", "all set", "up and running", "successfully",
		"is working now", "it's working", "built the", "set up",
		// The changelog shape: a reply that never says "done" but lists the
		// repairs as landed and hands the thing back. The live app_def session
		// closed with exactly this ("Fixes applied: … Try it now —") and slipped
		// past a list built around "done"/"created".
		"fixes applied", "fix applied", "changes applied", "corrected",
		"try it now", "should work now", "now works", "updated the",
	} {
		if strings.Contains(r, pos) {
			return true
		}
	}
	return false
}
