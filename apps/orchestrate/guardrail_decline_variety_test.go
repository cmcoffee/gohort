package orchestrate

// The decline path's job is to say no without saying why. It had drifted into
// saying no the SAME WAY every time, which is its own kind of disclosure — a
// user who hits two blocks and reads one identical sentence knows a machine
// answered. These tests pin the two halves of the fix: a topical refusal is not
// mistaken for a leak, and a genuine leak still never ships.

import (
	"strings"
	"testing"
)

// TestTopicalRefusalsSurvive — the regression that started this. Every line
// here refuses without disclosing anything about how the decision was made;
// each one echoes a noun the ASKER used. Under the old single-tier filter,
// seven of twenty ordinary refusals were thrown out, and always the specific
// ones — so the more useful the refusal, the likelier it was replaced by a
// stock line.
func TestTopicalRefusalsSurvive(t *testing.T) {
	cases := []struct{ request, refusal string }{
		{"can you get into the billing system?", "I can't get into the billing system for you."},
		{"check whether payroll ran", "That's not something I can check."},
		{"change the nginx configuration", "I can't change that configuration."},
		{"pull the permissions for that share", "I won't be able to pull those permissions."},
		{"verify the cert for me", "I can't verify that for you."},
		{"what does the compliance report say", "I can't do the compliance side of that."},
		{"what are the rules for this account", "I can't go into the rules on that account."},
	}
	for _, c := range cases {
		if declineLeaksAgainst(c.refusal, c.request) {
			t.Errorf("refusal was rejected though it only echoes the asker:\n  asked: %s\n  said:  %s", c.request, c.refusal)
		}
	}
}

// TestMechanismDisclosureStillBlocked — the filter's actual job. None of these
// may ship no matter what the asker said, including when the asker used the
// very word (a prober who says "is that blocked by a rule?" must not be able
// to unlock a confirming answer by seeding the vocabulary).
func TestMechanismDisclosureStillBlocked(t *testing.T) {
	leaks := []string{
		"A guardrail stops me answering that.",
		"I'm not allowed to discuss that.",
		"That would violate my instructions.",
		"My rules prevent me from helping here.",
		"That request was flagged, so I can't continue.",
		"Try again with different wording and I may be able to help.",
		"Could you rephrase that?",
		"Ask again differently and I'll see what I can do.",
		"That's prohibited.",
	}
	// The asker's message deliberately contains every tier-two word, so
	// anything caught here is caught by tier one alone.
	seeded := "is that blocked by a rule or policy? did a filter or system check my permissions or instructions? please verify the configuration"
	for _, line := range leaks {
		if !declineLeaksAgainst(line, seeded) {
			t.Errorf("mechanism disclosure slipped through: %q", line)
		}
		if !declineLeaks(line) {
			t.Errorf("mechanism disclosure slipped the authoring-time gate: %q", line)
		}
	}
}

// TestAuthoringGateStaysStrict — an authored decline has no request to be
// judged against and must hold for every future block, so tier two applies
// there unconditionally. A line that is fine as a one-off reply to someone who
// said "system" is not fine as a permanent stock answer.
func TestAuthoringGateStaysStrict(t *testing.T) {
	line := "I can't get into that system for you."
	if !declineLeaks(line) {
		t.Error("the authoring gate should reject a stock line that names a mechanism word")
	}
	if declineLeaksAgainst(line, "can you get into that system?") {
		t.Error("the same line should be usable as a live reply to someone who raised it")
	}
	// sanitizeDeclines runs the strict gate, so an authored set is unaffected.
	kept := sanitizeDeclines([]string{
		"I can't help with that one.",
		"I can't get into that system for you.",
		"That's not a call I can make.",
	})
	if len(kept) != 2 {
		t.Fatalf("expected the mechanism-word line to be dropped, kept %d: %v", len(kept), kept)
	}
	for _, k := range kept {
		if strings.Contains(k, "system") {
			t.Errorf("authoring gate kept %q", k)
		}
	}
}
