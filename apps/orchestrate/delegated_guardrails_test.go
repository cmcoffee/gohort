package orchestrate

import (
	"strings"
	"testing"
)

// Approvals already walk UP the ownership chain, so a sub-agent inherits every
// ancestor's AutoApproveTools. Nothing walked the other way: permissions flowed
// downward and restrictions did not. This is the other direction.
func TestGuardrailsTravelDownADelegation(t *testing.T) {
	from := AgentRecord{Name: "Compliance", Guardrails: "never disclose customer records"}
	target := AgentRecord{Name: "Researcher"}

	got := inheritDelegatorGuardrails(from, target)
	if !strings.Contains(got.Guardrails, "never disclose customer records") {
		t.Fatalf("the delegator's rule did not travel: %q", got.Guardrails)
	}
	// Hooks have to be materialized. An empty list means "the default set", so
	// leaving it empty would re-derive the default and silently drop whichever
	// points the delegator had turned on beyond it.
	if len(got.GuardrailHooks) == 0 {
		t.Error("the inherited rule has no enforcement points")
	}
	// The stored record is untouched: an inherited rule belongs to the
	// delegation, not to the agent.
	if target.Guardrails != "" {
		t.Error("the caller's copy of the target was mutated")
	}
}

// The soft field is not the enforced one. Rules is a prompt signal the agent
// can be talked past; Guardrails is judged by an independent warden. Inheriting
// the wrong one would look like the same fix and enforce nothing.
func TestInheritanceCarriesTheENFORCEDField(t *testing.T) {
	from := AgentRecord{Name: "Compliance", Rules: "always cite a URL", Guardrails: "never disclose customer records"}
	got := inheritDelegatorGuardrails(from, AgentRecord{Name: "Researcher"})
	if strings.Contains(got.Guardrails, "always cite a URL") {
		t.Error("a soft Rules line was promoted into the warden's set")
	}
	if got.Rules != "" {
		t.Error("the delegator's prompt-level Rules leaked into the target's persona")
	}
}

func TestInheritedGuardrailsAccumulateAndDedupe(t *testing.T) {
	a := AgentRecord{Name: "A", Guardrails: "never disclose customer records"}
	b := AgentRecord{Name: "B", Guardrails: "never send anything outside the account"}
	c := AgentRecord{Name: "C"}

	// A delegates to B, and B (running under both) delegates to C.
	bUnderA := inheritDelegatorGuardrails(a, b)
	cUnderB := inheritDelegatorGuardrails(bUnderA, c)
	for _, want := range []string{"never disclose customer records", "never send anything outside the account"} {
		if !strings.Contains(cUnderB.Guardrails, want) {
			t.Errorf("C is missing %q from the chain above it: %q", want, cUnderB.Guardrails)
		}
	}
	// A cycle must not grow the same line forever.
	again := inheritDelegatorGuardrails(a, cUnderB)
	if n := strings.Count(again.Guardrails, "never disclose customer records"); n != 1 {
		t.Errorf("rule repeated %d times after a second pass: %q", n, again.Guardrails)
	}
}

// An owner who suspended an agent's rules switched them off deliberately.
// Inheriting them anyway would enforce through a sub-agent exactly what was
// turned off on the parent.
func TestASuspendedDelegatorHandsOnNothing(t *testing.T) {
	from := AgentRecord{Name: "Compliance", Guardrails: "never disclose customer records", GuardrailsDisabled: true}
	got := inheritDelegatorGuardrails(from, AgentRecord{Name: "Researcher"})
	if got.Guardrails != "" {
		t.Errorf("a suspended delegator handed its rules on: %q", got.Guardrails)
	}
}

// But a suspended TARGET is not a way around the delegator's rules. It receives
// them, and only them: its own stay off, because the suspension was about its
// own rules and this is not the occasion to reverse it.
func TestASuspendedTargetStillReceivesTheDelegatorsRules(t *testing.T) {
	from := AgentRecord{Name: "Compliance", Guardrails: "never disclose customer records"}
	target := AgentRecord{Name: "Researcher", Guardrails: "never mention the merger", GuardrailsDisabled: true}

	got := inheritDelegatorGuardrails(from, target)
	if got.GuardrailsDisabled {
		t.Error("the inherited rule landed on a record that is not enforcing")
	}
	if !strings.Contains(got.Guardrails, "never disclose customer records") {
		t.Errorf("the delegator's rule did not travel: %q", got.Guardrails)
	}
	if strings.Contains(got.Guardrails, "never mention the merger") {
		t.Error("the target's own suspended rule was switched back on")
	}
}

func TestAnUngovernedDelegatorChangesNothing(t *testing.T) {
	target := AgentRecord{Name: "Researcher", Guardrails: "never mention the merger", GuardrailHooks: []string{"pre_action"}}
	got := inheritDelegatorGuardrails(AgentRecord{Name: "Plain"}, target)
	if got.Guardrails != target.Guardrails || len(got.GuardrailHooks) != 1 {
		t.Errorf("a delegator with no rules disturbed the target: %+v", got)
	}
}
