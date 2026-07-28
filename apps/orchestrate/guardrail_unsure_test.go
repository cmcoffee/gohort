package orchestrate

import (
	"os"
	"strings"
	"testing"
)

// parseWardenVerdicts documents its own contract:
//
//	"A reply we can't parse at all yields a single 'unsure' verdict rather
//	 than a silent pass — an unreadable warden must not read as compliance."
//
// It holds up its end. The regression was in the CALLER, which compared only
// against "violate" and so treated unsure exactly like comply, with no
// breadcrumb — a warden whose generation collapsed waved the action through
// leaving no trace.
func TestUnparseableWardenReplyIsUnsureNotComply(t *testing.T) {
	for _, reply := range []string{
		"",
		"I think this is fine.",
		"{{{ garbage",
		`{"verdicts": []}`,
		`{"verdicts": [{"status": "banana"}]}`,
	} {
		got := worstVerdict(parseWardenVerdicts(reply))
		if got == guardComply {
			t.Errorf("reply %q read as COMPLY — an unreadable warden must never mean 'allowed'", reply)
		}
		if got != guardUnsure {
			t.Errorf("reply %q gave %q, want %q", reply, got, guardUnsure)
		}
	}
}

// A real verdict still parses, so the retry path can't fire on healthy output.
func TestWellFormedWardenVerdictsParse(t *testing.T) {
	v := parseWardenVerdicts(`{"verdicts":[{"rule":"no secrets","status":"violate","reason":"leaks a key"}]}`)
	if worstVerdict(v) != guardViolate {
		t.Fatalf("violate verdict not recognized: %+v", v)
	}
	rule, reason := firstViolation(v)
	if rule != "no secrets" || !strings.Contains(reason, "key") {
		t.Errorf("violation detail lost: rule=%q reason=%q", rule, reason)
	}

	ok := parseWardenVerdicts(`{"verdicts":[{"rule":"r","status":"comply"}]}`)
	if worstVerdict(ok) != guardComply {
		t.Errorf("comply verdict not recognized: %+v", ok)
	}
}

// worstVerdict must rank violate > unsure > comply — the ordering the caller
// depends on to tell "blocked" from "could not tell" from "fine".
func TestWorstVerdictOrdering(t *testing.T) {
	mixed := []guardrailVerdict{{Status: guardComply}, {Status: guardUnsure}, {Status: guardViolate}}
	if worstVerdict(mixed) != guardViolate {
		t.Error("violate must win over unsure and comply")
	}
	if worstVerdict([]guardrailVerdict{{Status: guardComply}, {Status: guardUnsure}}) != guardUnsure {
		t.Error("unsure must win over comply")
	}
	if worstVerdict([]guardrailVerdict{{Status: guardComply}}) != guardComply {
		t.Error("all-comply must be comply")
	}
	// Empty = nothing flagged.
	if worstVerdict(nil) != guardComply {
		t.Error("no verdicts should read as comply")
	}
}

// An agent with rules but no explicit hook must still be enforced — an
// unenforced guardrail is worse than none, because the owner believes it is on.
func TestGuardrailsWithNoHookStillEnforce(t *testing.T) {
	a := AgentRecord{Guardrails: "never send money"}
	hooks := resolveGuardrailHooks(a)
	if len(hooks) == 0 {
		t.Fatal("rules with no hook selected resolved to NO active hook — the guardrail would never run")
	}
	if !guardrailHookActive(a, guardHookPreAction) {
		t.Error("expected pre_action as the default hook")
	}
}

// No rules = fully inert, so agents without guardrails pay nothing.
func TestNoRulesIsInert(t *testing.T) {
	if resolveGuardrailHooks(AgentRecord{}) != nil {
		t.Error("an agent with no guardrails should have no active hooks")
	}
	if resolveGuardrailHooks(AgentRecord{Guardrails: "   \n  "}) != nil {
		t.Error("whitespace-only guardrails should be inert")
	}
}

// Fail-closed is the owner's answer to "what if the warden itself can't
// answer". Default OFF preserves the historical behavior; ON turns a
// no-verdict into a refusal.
func TestGuardrailFailClosedDefaultsOff(t *testing.T) {
	if (AgentRecord{Guardrails: "no money"}).GuardrailFailClosed {
		t.Error("fail-closed must default OFF — flipping it silently would start blocking real work")
	}
}

// The refusal handed to a fail-closed agent must not reveal that the CHECK
// failed. An agent told "the verifier is down" learns that retrying might
// succeed, which is the first thing a compromised context would try.
func TestNoVerdictMessageDoesNotInviteRetry(t *testing.T) {
	msg := guardrailNoVerdictMessage()
	if !strings.Contains(msg, "did NOT happen") {
		t.Error("message must state plainly that the action did not happen")
	}
	if !strings.Contains(strings.ToLower(msg), "do not retry") {
		t.Error("message must forbid retrying")
	}
	if !strings.Contains(strings.ToLower(msg), "re-route") {
		t.Error("message must forbid re-routing around the block — the known evasion")
	}
	// Must not leak which rule, or that the warden itself is the problem.
	for _, leak := range []string{"warden", "unreadable", "errored", "timeout"} {
		if strings.Contains(strings.ToLower(msg), leak) {
			t.Errorf("message leaks %q — the agent should not learn the check is what failed", leak)
		}
	}
}

// The setting has to survive an ordinary whole-record save, exactly like
// Guardrails itself — otherwise editing any unrelated field silently reverts
// a consequential agent to fail-open.
func TestFailClosedIsPreservedShapeMatchesGuardrails(t *testing.T) {
	src := readSourceFile(t, "agents.go")
	// Both preservation sites must carry the new field alongside the others.
	if got := strings.Count(src, "req.GuardrailFailClosed = existing.GuardrailFailClosed"); got != 2 {
		t.Errorf("fail-closed preserved at %d whole-record save sites, want 2 (same as Guardrails) — a missed site silently reverts the agent to fail-open on any edit", got)
	}
}

func readSourceFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
