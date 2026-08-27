package orchestrate

import (
	"encoding/json"
	"testing"
)

// TestAgentEchoJSONHidesOwnerOnlyState — the "Saved record:" blob create/update/
// clone hand back must carry only what the CALL could have written.
//
// It used to marshal the whole AgentRecord. An authoring model then read a
// per-agent credential opt-out that predated the credential being secured,
// could not tell stale from current (nothing on the record says which), and
// reported it to the user as a live misconfiguration in the agent it had just
// edited.
func TestAgentEchoJSONHidesOwnerOnlyState(t *testing.T) {
	rec := AgentRecord{
		ID:                  "abc",
		Name:                "TechGuru",
		Description:         "expert",
		AllowedTools:        []string{"web_search"},
		Triggers:            []string{"kiteworks"},
		DisabledCredentials: []string{"gitlab"},
		DisabledPipelines:   []string{"p1"},
		GuardrailFailClosed: true,
		Machine:             "m-1",
		Owner:               "alice",
		Exposed:             true,
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(agentEchoJSON(rec)), &got); err != nil {
		t.Fatalf("echo is not valid JSON: %v", err)
	}
	for _, k := range []string{"disabled_credentials", "disabled_pipelines", "guardrail_fail_closed", "machine", "owner", "exposed"} {
		if _, ok := got[k]; ok {
			t.Errorf("owner-only field %q must not be echoed to the authoring model", k)
		}
	}
	for _, k := range []string{"id", "name", "description", "allowed_tools", "triggers"} {
		if _, ok := got[k]; !ok {
			t.Errorf("writable field %q must be echoed so the model can confirm what stuck", k)
		}
	}
}
