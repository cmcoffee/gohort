package orchestrate

import "testing"

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
