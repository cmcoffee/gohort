package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"
)

// The editor shows these as "action = N" lines, so that is what the record
// carries on the wire — sorted, so a save that changes nothing shows no diff.
func TestActionQuotasRoundTripThroughTheEditor(t *testing.T) {
	rec := AgentRecord{ID: "a1", ActionQuotas: ActionQuotaMap{"send_email": 2, "moltbook/create_post": 6}}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `["moltbook/create_post = 6","send_email = 2"]`) {
		t.Errorf("quotas must travel as sorted lines: %s", b)
	}

	var back AgentRecord
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ActionQuotas["moltbook/create_post"] != 6 || back.ActionQuotas["send_email"] != 2 {
		t.Errorf("round trip lost the limits: %+v", back.ActionQuotas)
	}

	// An authoring tool sending the object form means the same thing.
	var obj AgentRecord
	if err := json.Unmarshal([]byte(`{"action_quotas":{"moltbook/create_post":6}}`), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.ActionQuotas["moltbook/create_post"] != 6 {
		t.Errorf("the object form must be accepted: %+v", obj.ActionQuotas)
	}

	// Empty stays absent rather than becoming an empty control.
	var none AgentRecord
	if err := json.Unmarshal([]byte(`{"action_quotas":null}`), &none); err != nil {
		t.Fatal(err)
	}
	if len(none.ActionQuotas) != 0 {
		t.Errorf("null means no limits, got %+v", none.ActionQuotas)
	}
}

// A half-typed line is dropped, never guessed at: a quota that silently
// became a cap of 1 would be worse than one that visibly did not save.
func TestAHalfTypedQuotaLineIsDropped(t *testing.T) {
	for _, bad := range []string{"moltbook/create_post", "= 5", "post = ", "post = 0", "post = -1", "post = many", "   "} {
		if _, _, ok := parseActionQuotaLine(bad); ok {
			t.Errorf("%q must not parse as a limit", bad)
		}
	}
	for _, good := range []struct {
		line string
		name string
		n    int
	}{
		{"moltbook/create_post = 6", "moltbook/create_post", 6},
		{"send_email=2", "send_email", 2},
		{"  post : 3  ", "post", 3},
	} {
		name, n, ok := parseActionQuotaLine(good.line)
		if !ok || name != good.name || n != good.n {
			t.Errorf("%q parsed as %q/%d, want %q/%d", good.line, name, n, good.name, good.n)
		}
	}
}

// Both fields are editable, and the fields with their own protected
// endpoints still are not.
func TestTheNewLimitsAreSaveable(t *testing.T) {
	for _, f := range []string{"action_quotas", "daily_spend_usd"} {
		if !patchAgentFields[f] {
			t.Errorf("%s must be saveable from the editor", f)
		}
	}
	for _, f := range []string{"guardrails", "locked", "id", "owner"} {
		if patchAgentFields[f] {
			t.Errorf("%s must NOT be reachable from a partial save", f)
		}
	}
}

// The authoring tools read both forms, and a number an LLM sent as a string.
func TestAuthoringToolsCanSetTheLimits(t *testing.T) {
	q := actionQuotasFromArgs(map[string]any{"action_quotas": []any{"moltbook/create_post = 6", "junk", "send_email = 2"}})
	if len(q) != 2 || q["moltbook/create_post"] != 6 || q["send_email"] != 2 {
		t.Errorf("line form: %+v", q)
	}
	q = actionQuotasFromArgs(map[string]any{"action_quotas": map[string]any{"post": float64(4), "bad": float64(0)}})
	if len(q) != 1 || q["post"] != 4 {
		t.Errorf("object form: %+v", q)
	}
	if actionQuotasFromArgs(map[string]any{}) != nil {
		t.Error("absent means no limits")
	}
	if got := floatFromArgs(map[string]any{"daily_spend_usd": "2.50"}, "daily_spend_usd"); got != 2.50 {
		t.Errorf("a number sent as a string must be read, got %v", got)
	}
	if got := floatFromArgs(map[string]any{"daily_spend_usd": float64(5)}, "daily_spend_usd"); got != 5 {
		t.Errorf("plain number: %v", got)
	}
	if got := floatFromArgs(map[string]any{}, "daily_spend_usd"); got != 0 {
		t.Errorf("absent is no limit, got %v", got)
	}
}
