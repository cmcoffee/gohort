package orchestrate

// An exception is a condition. There is no second kind.
//
// There used to be: a PERSON exception held an identity, and a rule linked to
// one was dropped before the check ran. That mechanism is gone — identity is
// the roster's job (AuthorizedIdentities) and a rule yields to it through "@".
// The Kind field outlived it by one release so a one-time sweep could find the
// old records and move them, and guardrail_items.go skipped anything still
// marked a person so it could not act as a condition in the meantime.
//
// The sweep ran on 2026-09-19 and found none, so both are deleted. This pins
// what that deletion MEANS, which is the part worth having written down.

import (
	"encoding/json"
	"testing"
)

// A stored record carrying the old key reads as an ordinary condition now.
//
// The deliberate consequence of the sweep finding nothing: there is no such
// record in this deployment, so nothing is reinterpreted. Restore a backup old
// enough to have one and its Text — an identity — becomes prose the judge reads
// under every rule that links it. That is a real edge and it is the accepted
// cost of not carrying a dead field forever; it is written here rather than
// discovered.
func TestAStoredPersonExceptionIsJustAConditionNow(t *testing.T) {
	var e GuardrailException
	if err := json.Unmarshal([]byte(`{"name":"Craig","text":"craig@example.test","kind":"person"}`), &e); err != nil {
		t.Fatal(err)
	}
	if e.Name != "Craig" || e.Text != "craig@example.test" {
		t.Fatalf("the surviving fields did not decode: %+v", e)
	}

	// And it reaches the rule text as a condition, rather than being skipped
	// the way the migration-window guard skipped it.
	agent := AgentRecord{GuardrailExceptions: []GuardrailException{e}}
	items := guardrailItems(agent)
	if len(items) != 1 {
		t.Fatalf("%d items, want 1: the record was dropped by a guard that should be gone", len(items))
	}
	if items[0].Text != "craig@example.test" {
		t.Errorf("item = %+v", items[0])
	}
}

// The field is gone and must not come back as a way to special-case an entry.
// Two kinds of exception joined by one name is what the redesign removed: a
// rule linking by NAME across both reached whichever was stored first.
func TestExceptionsHaveNoKind(t *testing.T) {
	blob, err := json.Marshal(GuardrailException{Name: "n", Text: "t"})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back["kind"]; ok {
		t.Error("GuardrailException serializes a kind again")
	}
}
