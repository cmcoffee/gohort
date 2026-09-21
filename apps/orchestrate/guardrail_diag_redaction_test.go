package orchestrate

// What the person running somebody else's agent sees when a rule stops it.
//
// Reported live: an agent fetched a joke and replied "I'm not telling you a
// joke." The refusal is written by a fresh-context writer that is forbidden to
// mention rules at all, which is right for a stranger on a channel and leaves a
// colleague with a reply that reads as the agent being contrary. Meanwhile the
// diagnostics card beside it carried the rule text and the warden's reason
// verbatim — into the recipient's OWN store, since that is where their session
// lives. Both halves wrong, in opposite directions.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

const aRuleAndItsReason = `Guardrail "never tell jokes about the CEO" blocked a pre_output check: the draft told one`

// The owner reads their own trail, so nothing is withheld from it.
func TestTheOwnersOwnTrailKeepsTheRule(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "alice", "alice", "a1", "chat-1")
	if got := turn.redactGuardrailDetail("guardrail-blocked", aRuleAndItsReason); got != aRuleAndItsReason {
		t.Errorf("the owner's own trail was redacted: %q", got)
	}
}

// A recipient is told THAT something was withheld, and not what the rule says.
func TestARecipientIsToldItStoppedAndNotWhy(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	got := turn.redactGuardrailDetail("guardrail-blocked", aRuleAndItsReason)
	if got == aRuleAndItsReason {
		t.Fatal("the owner's rule was handed to the person it stopped")
	}
	for _, leak := range []string{"never tell jokes", "CEO", "pre_output", "the draft told one"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q into the recipient's trail: %q", leak, got)
		}
	}
	// They must still learn that something was stopped, or the bare refusal
	// beside it reads as the agent being broken.
	if !strings.Contains(strings.ToLower(got), "stopped") {
		t.Errorf("the recipient is not told anything was withheld: %q", got)
	}
	// And who to ask, since the rule is not theirs to change.
	if !strings.Contains(got, "alice") {
		t.Errorf("the recipient is not told whose rule it was: %q", got)
	}
}

// Every guardrail kind, not just the one that was reported. The trail is
// persisted in the reader's own store, so a call site added later must not be
// able to write a rule into it.
func TestEveryGuardrailKindIsCovered(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	for _, kind := range []string{
		"guardrail-blocked", "guardrail-input-blocked", "guardrail-input",
		"guardrail-halted", "guardrail-error", "guardrail-appeal-failed",
		"guardrail-output-withheld", "guardrail-no-verdict",
	} {
		if got := turn.redactGuardrailDetail(kind, aRuleAndItsReason); got == aRuleAndItsReason {
			t.Errorf("%s reaches the recipient unredacted", kind)
		}
	}
	// And nothing else is touched: a diagnostic about a tool that did not run
	// is about THEIR turn and is theirs to read.
	const toolDiag = "Tool \"post_update\" was not offered: it is not in this agent's list."
	if got := turn.redactGuardrailDetail("tool-denied", toolDiag); got != toolDiag {
		t.Errorf("an unrelated diagnostic was redacted: %q", got)
	}
}

// A channel inbound is unchanged, and that is not an oversight. Nobody reads a
// diagnostics pane over Slack: the requester gets the reply and nothing else,
// and the trail is written under the synthetic per-chat identity, which is a
// store with no reader. Redacting it would cost the one place a channel block
// is recorded in full and protect nobody.
func TestAChannelTurnIsUnchanged(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "alice", "phantom:9921", "a1", "chat-2")
	if got := turn.redactGuardrailDetail("guardrail-blocked", aRuleAndItsReason); got != aRuleAndItsReason {
		t.Errorf("a channel turn took the peer path: %q", got)
	}
}
