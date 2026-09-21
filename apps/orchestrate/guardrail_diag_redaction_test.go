package orchestrate

// What the person running somebody else's agent learns when a rule stops it.
//
// Reported live: an agent fetched a joke and replied "I'm not telling you a
// joke." The rejection writer is forbidden to mention rules at all, which is
// right for a stranger on a channel and leaves a colleague with a reply that
// reads as the agent being contrary. The diagnostics card beside it carried the
// rule text and the warden's reason verbatim. Both halves wrong, opposite ways.
//
// The first two fixes both kept the shape of the mistake: redact the card, then
// demote it to a note. A message that appears only when a rule fires IS the rule
// firing, whatever it says and however deep it sits. guardrailSafeFallbacks
// varies the refusals precisely so they are not a fingerprint; a per-turn signal
// hands that back for free.
//
// So nothing about the turn reaches them, and what they needed is said standing
// instead: this agent is somebody else's, on their configuration, and they are
// who to ask. See apps/agents.ownerConfigNote.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

const aRuleAndItsReason = `Guardrail "never tell jokes about the CEO" blocked a pre_output check: the draft told one`

// Nothing about this turn reaches the person it stopped: no card, and no entry
// in a trail of their own.
func TestARecipientLearnsNothingAboutTheTurn(t *testing.T) {
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
	if got := parentTrailOf(UserDB(root, "bob"), "a1", "s1"); len(got) != 0 {
		t.Errorf("their own trail carries a per-turn signal: %+v", got)
	}

	// The owner's copy is untouched and says everything. It is their rule, and
	// this trail is the one they read.
	owners := decorateSessionDiags(parentTrailOf(UserDB(root, "alice"), "a1", "s1"))
	if len(owners) != 1 || owners[0].Detail != aRuleAndItsReason {
		t.Fatalf("the owner's own trail lost the rule: %+v", owners)
	}
	if owners[0].Level != diagLevelBlocked {
		t.Errorf("the owner's entry stopped reading as a block: %+v", owners[0])
	}
}

// The owner's own run is untouched: card, full detail.
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

// Only guardrail breadcrumbs go quiet. A diagnostic about THEIR turn — a tool
// that was not offered, an input that was discarded — is theirs to read, and
// silencing it would be the failure this codebase least tolerates.
func TestOnlyGuardrailBreadcrumbsGoQuiet(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := sharedRunTurn(root, "alice", "bob", "a1")
	turn.session = &ChatSession{ID: "s1"}
	turn.udb, turn.ownerDB = UserDB(root, "bob"), UserDB(root, "alice")
	buf := &syncBuf{}
	turn.sse = &sseWriter{live: buf}

	turn.turnDiag("tool-denied", `Tool "post_update" was not offered: it is not in this agent's list.`)
	frames := noticeFrames(t, buf.String())
	if len(frames) != 1 {
		t.Fatalf("an unrelated blocking diagnostic was silenced: %+v", frames)
	}
	if !strings.Contains(frames[0]["text"].(string), "post_update") {
		t.Errorf("the notice lost its detail: %+v", frames[0])
	}
}

// Naming the owner is a disclosure, and how the agent reached somebody decides
// whether it is one they have already had. An account here is an email address.
func TestTheOwnerIsNamedOnlyToSomebodyWhoAlreadyKnows(t *testing.T) {
	shared := AgentRecord{ID: "a1", Owner: "alice", AllowedUsers: []string{"bob"}}
	if got := OwnerLabel(shared, "bob"); got != "alice" {
		t.Errorf("a peer share must name them; bob knows who handed him the agent: %q", got)
	}
	if got := OwnerLabel(shared, "alice"); got != "alice" {
		t.Errorf("the owner reading their own agent: %q", got)
	}
	published := AgentRecord{ID: "a2", Owner: "alice", Everyone: true}
	if got := OwnerLabel(published, "dana"); got != "its owner" {
		t.Errorf("a published agent must not hand every signed-in user the owner's account: %q", got)
	}
	if got := OwnerLabel(AgentRecord{ID: "seed-x", Owner: seedOwner}, "dana"); got != "its owner" {
		t.Errorf("a seed has no person behind it: %q", got)
	}
}
