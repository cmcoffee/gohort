package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestClaimsSuccessWithoutAck(t *testing.T) {
	yes := []string{
		"Done! I've rebuilt the moltbook toolbox with proper POST actions.",
		"The tool is live and ready to use.",
		"Fixed — post_message is now a POST.",
		"All set — the agent is up and running.",
	}
	no := []string{
		"I didn't fix it yet — the toolbox still has post_message as a GET.",
		"That failed with an error; let me try again.",
		"I wasn't able to update it.",
		"Here's a joke about robots.", // no success phrase at all
	}
	for _, s := range yes {
		if !claimsSuccessWithoutAck(s) {
			t.Errorf("expected a success-claim: %q", s)
		}
	}
	for _, s := range no {
		if claimsSuccessWithoutAck(s) {
			t.Errorf("expected NOT a success-claim: %q", s)
		}
	}
}

func TestInjectFailedAuthoringWarning(t *testing.T) {
	allErrored := []PersistedToolCall{
		{Name: "tool_def", Err: "actions is required"},
		{Name: "tool_def", Err: "required param sent NOWHERE"},
	}

	// All authoring errored + success claim → fires + appends a hidden note.
	sess := &ChatSession{}
	if !injectFailedAuthoringWarning(sess, allErrored, "Done! I've rebuilt the toolbox.") {
		t.Fatal("expected the warning to fire (all authoring errored + success claim)")
	}
	if len(sess.Messages) != 1 || !sess.Messages[0].Hidden {
		t.Fatalf("expected one hidden corrective note; got %d messages", len(sess.Messages))
	}

	// One authoring call SUCCEEDED → no warning.
	mixed := []PersistedToolCall{
		{Name: "tool_def", Err: "first try failed"},
		{Name: "tool_def", Result: "Created toolbox tool"},
	}
	if injectFailedAuthoringWarning(&ChatSession{}, mixed, "Done! Created it.") {
		t.Fatal("must NOT fire when an authoring call succeeded")
	}

	// Errored but the reply acknowledges the failure → no warning.
	if injectFailedAuthoringWarning(&ChatSession{}, allErrored, "That didn't work — the update errored.") {
		t.Fatal("must NOT fire when the reply acknowledges the failure")
	}

	// No authoring tool fired → no warning.
	if injectFailedAuthoringWarning(&ChatSession{}, []PersistedToolCall{{Name: "fetch_url", Err: "x"}}, "Done!") {
		t.Fatal("must NOT fire when no authoring tool fired")
	}
}

func TestPromisesAuthoringWithoutAction(t *testing.T) {
	yes := []string{
		// Both replies from the live vapi session that stalled.
		"Great! I will now create the `vapi_calls` toolbox with the specified actions.",
		"I can help you build a toolbox for Vapi calls. I'll create actions for placing a call, hanging up a call, and getting transcripts.",
		"Let me set up the agent with those three tools.",
		"Next I'll add the get_transcript action to the toolbox.",
	}
	no := []string{
		"I'll use the get_weather tool to check that.",   // dispatch, no authoring verb
		"I've created the toolbox and verified it works.", // past tense — sibling guard's job
		"The toolbox already exists, so nothing to do.",
		"Here's a joke about robots.",
		"I'll look up the API docs first.", // no authoring object
		// Three signals present but split across sentences.
		"I'll check the credential. Creating a toolbox is the next step for someone.",
	}
	for _, s := range yes {
		if !promisesAuthoringWithoutAction(s) {
			t.Errorf("expected an authoring promise: %q", s)
		}
	}
	for _, s := range no {
		if promisesAuthoringWithoutAction(s) {
			t.Errorf("expected NOT an authoring promise: %q", s)
		}
	}
}

func TestInjectPromisedAuthoringWarning(t *testing.T) {
	const promise = "Great! I will now create the vapi_calls toolbox with the specified actions."

	// The live failure: check_credential fired, then prose. No plan, no error.
	sess := &ChatSession{}
	calls := []PersistedToolCall{{Name: "check_credential", Result: "READY"}}
	if !injectPromisedAuthoringWarning(sess, calls, promise) {
		t.Fatal("expected the warning to fire (promise + zero authoring calls)")
	}
	if len(sess.Messages) != 1 || !sess.Messages[0].Hidden {
		t.Fatalf("expected one hidden corrective note; got %d messages", len(sess.Messages))
	}

	// An authoring call DID fire → the promise was kept.
	kept := []PersistedToolCall{{Name: "tool_def", Result: "Created toolbox tool"}}
	if injectPromisedAuthoringWarning(&ChatSession{}, kept, promise) {
		t.Fatal("must NOT fire when an authoring tool fired")
	}

	// Waiting on approval or laying down a plan card — the promise is about a
	// future turn, not this one.
	for _, name := range []string{"ask_user", "plan_set"} {
		if injectPromisedAuthoringWarning(&ChatSession{}, []PersistedToolCall{{Name: name}}, promise) {
			t.Fatalf("must NOT fire when %s ended the turn", name)
		}
	}

	// Proposing, not promising.
	if injectPromisedAuthoringWarning(&ChatSession{}, calls, "I can build a vapi toolbox — should I create it now?") {
		t.Fatal("must NOT fire on a question")
	}

	// Empty reply (streamed nothing) → nothing to correct.
	if injectPromisedAuthoringWarning(&ChatSession{}, calls, "  ") {
		t.Fatal("must NOT fire on an empty reply")
	}
}

func TestMigrationTargetAgent(t *testing.T) {
	// Authoring for another agent → the draft follows the focus, not the
	// agent running the turn (the Builder case).
	if got := migrationTargetAgent("seed-builder", "agent-vapi"); got != "agent-vapi" {
		t.Errorf("authoring focus must win: got %q", got)
	}
	// No focus → the running agent keeps its own work.
	if got := migrationTargetAgent("seed-chat", ""); got != "seed-chat" {
		t.Errorf("no focus → running agent: got %q", got)
	}
	// Neither known → caller leaves the draft alone.
	if got := migrationTargetAgent("", ""); got != "" {
		t.Errorf("expected empty target: got %q", got)
	}
}

func TestIsTrialTool(t *testing.T) {
	sess := &ToolSession{}
	if err := sess.AppendTempTool(&TempTool{Name: "draft_tool", Trial: true}); err != nil {
		t.Fatalf("seed trial: %v", err)
	}
	if err := sess.AppendTempTool(&TempTool{Name: "kept_tool"}); err != nil {
		t.Fatalf("seed confirmed: %v", err)
	}
	if !isTrialTool(sess, "draft_tool") {
		t.Error("expected draft_tool to be trial")
	}
	if isTrialTool(sess, "kept_tool") {
		t.Error("confirmed tool must not read as trial")
	}
	// Unknown name and nil session fall back to the pre-existing behavior.
	if isTrialTool(sess, "nope") || isTrialTool(nil, "draft_tool") {
		t.Error("absent record must report false")
	}
}
