package orchestrate

import "testing"

// The live Builder transcript: three turns insisting tool_def was
// inaccessible and telling the user to report a platform error, with no
// tool_def call ever emitted. tool_def is in builderAuthoringTools().
func TestFalseUnavailabilityFiresOnBuilderClaim(t *testing.T) {
	builder := AgentRecord{ID: "seed-builder"}
	replies := []string{
		"I made a mistake and tried to use tool_def directly, which is not in my available tools.",
		"It appears I am unable to access the tool_def function, which is essential for creating tools.",
		"I cannot access tool_def — this is a framework problem on my end that I need to report.",
		"I am unable to use tool_def at this time.",
	}
	for _, reply := range replies {
		sess := &ChatSession{}
		if !injectFalseUnavailabilityWarning(sess, nil, reply, builder) {
			t.Errorf("no warning for %q", reply)
			continue
		}
		if len(sess.Messages) != 1 || !sess.Messages[0].Hidden {
			t.Errorf("expected one hidden corrective note for %q", reply)
		}
	}
}

// An honest report of a real failure must not be rewritten as a
// hallucination — the tool DID run and said something.
func TestFalseUnavailabilityIgnoresRealCallResults(t *testing.T) {
	builder := AgentRecord{ID: "seed-builder"}
	calls := []PersistedToolCall{{Name: "tool_def", Err: "validation failed"}}
	if injectFalseUnavailabilityWarning(&ChatSession{}, calls,
		"I cannot use tool_def for this — it rejected the params.", builder) {
		t.Error("fired even though tool_def actually ran this turn")
	}
}

// An agent without authoring rights genuinely lacks these tools, so the
// same sentence is true and must pass.
func TestFalseUnavailabilityRespectsAgentsWithoutAuthoring(t *testing.T) {
	plain := AgentRecord{ID: "some-agent"}
	if injectFalseUnavailabilityWarning(&ChatSession{}, nil,
		"I do not have access to tool_def.", plain) {
		t.Error("fired for an agent that really has no authoring tools")
	}
}

// Promises, honest error reports, and unrelated prose must stay clear.
func TestFalseUnavailabilityDoesNotOverfire(t *testing.T) {
	builder := AgentRecord{ID: "seed-builder"}
	clean := []string{
		"I will call tool_def now to update the tool.",
		"tool_def returned an error about the params.",
		"Let me use tool_def to fix this.",
		"I cannot access the remote API without a credential.",
		"The get_weather tool is not available to me.",
		"",
		"   ",
	}
	for _, reply := range clean {
		if injectFalseUnavailabilityWarning(&ChatSession{}, nil, reply, builder) {
			t.Errorf("over-fired on %q", reply)
		}
	}
}

// The name and the inability phrase must share a sentence, or an
// unrelated complaint next to a mention of the tool would trip it.
func TestFalseUnavailabilityRequiresSameSentence(t *testing.T) {
	builder := AgentRecord{ID: "seed-builder"}
	if injectFalseUnavailabilityWarning(&ChatSession{}, nil,
		"I will use tool_def. I cannot access the appliance over SSH.", builder) {
		t.Error("matched across two unrelated sentences")
	}
}

func TestFalselyClaimedUnavailableNamesTheTool(t *testing.T) {
	cases := map[string]string{
		"tool_def is not available to me":          "tool_def",
		"I am unable to access create_agent":       "create_agent",
		"I don't have access to update_agent here": "update_agent",
		"I cannot use skill_def":                   "skill_def",
		"everything is fine":                       "",
		"I will call tool_def":                     "",
	}
	for reply, want := range cases {
		if got := falselyClaimedUnavailable(reply); got != want {
			t.Errorf("falselyClaimedUnavailable(%q) = %q, want %q", reply, got, want)
		}
	}
}
