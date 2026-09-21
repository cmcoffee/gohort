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

// The card is the per-turn signal, and the signal is the thing withheld.
//
// Varied refusals plus a deterministic card is worse than either alone: it
// tells a prober which of the varied refusals was a rule, which is the bisect
// guardrailSafeFallbacks exists to prevent.
func TestARecipientGetsNoCardPerBlock(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	turn.session = &ChatSession{ID: "s1"}
	turn.udb, turn.ownerDB = UserDB(root, "bob"), UserDB(root, "alice")

	buf := &syncBuf{}
	turn.sse = &sseWriter{live: buf}
	turn.turnDiag("guardrail-blocked", aRuleAndItsReason)
	if frames := noticeFrames(t, buf.String()); len(frames) != 0 {
		t.Errorf("a card announced the block to the person it stopped: %+v", frames)
	}

	// The owner's trail keeps everything: it is their rule.
	owners := decorateSessionDiags(parentTrailOf(UserDB(root, "alice"), "a1", "s1"))
	if len(owners) != 1 || owners[0].Detail != aRuleAndItsReason {
		t.Fatalf("the owner's own trail lost the rule: %+v", owners)
	}
	if diagLevel(owners[0].Kind) != diagLevelBlocked {
		t.Errorf("the owner's entry stopped reading as a block: %q", owners[0].Kind)
	}

	// The runner gets a quiet, redacted copy in the trail THEY can read —
	// handleSessionDiag serves out of the requesting user's own store, so
	// without this "quiet but findable" would be simply quiet.
	theirs := decorateSessionDiags(parentTrailOf(UserDB(root, "bob"), "a1", "s1"))
	if len(theirs) != 1 {
		t.Fatalf("the person stopped has nothing to find: %+v", theirs)
	}
	if diagLevel(theirs[0].Kind) != diagLevelNote {
		t.Errorf("their copy still raises a card: kind %q", theirs[0].Kind)
	}
	for _, leak := range []string{"never tell jokes", "CEO", "pre_output"} {
		if strings.Contains(theirs[0].Detail, leak) {
			t.Errorf("their copy leaked %q: %q", leak, theirs[0].Detail)
		}
	}
	if !strings.Contains(theirs[0].Detail, "alice") {
		t.Errorf("their copy does not say who to ask: %q", theirs[0].Detail)
	}
}

// The owner's own run is untouched: card, full detail, blocking level.
func TestTheOwnersOwnBlockStillRaisesACard(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "alice", "alice", "a1", "s1")
	turn.session = &ChatSession{ID: "s1"}
	buf := &syncBuf{}
	turn.sse = &sseWriter{live: buf}
	turn.turnDiag("guardrail-blocked", aRuleAndItsReason)

	frames := noticeFrames(t, buf.String())
	if len(frames) != 1 {
		t.Fatalf("the owner lost the live card on their own agent: %+v", frames)
	}
	if !strings.Contains(frames[0]["text"].(string), "never tell jokes") {
		t.Errorf("the owner's card lost their own rule: %+v", frames[0])
	}
}
