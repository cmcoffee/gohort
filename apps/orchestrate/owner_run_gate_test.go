package orchestrate

import "testing"

// ownerRunFor mirrors the gate in resolveWorkerTools. Kept as a helper so the
// rule is testable without standing up a whole chatTurn.
func ownerRunFor(agent AgentRecord, user string) bool {
	return agent.Owner == "" || agent.Owner == seedOwner || agent.Owner == user
}

// THE BUG: loadAgent stamps an unshadowed seed with Owner = seedOwner
// ("system") so callers can tell in-code defaults from DB records. The
// authoring gate tested only ""/username, so every user's own Builder failed
// it and silently lost the entire authoring catalog — tool_def, create_agent,
// survey, all of it — while still being described as the authoring agent.
//
// Builder was telling the truth when it said it could not author.
func TestVirginSeedIsOwnerRun(t *testing.T) {
	builder := AgentRecord{ID: "seed-builder", Owner: seedOwner}
	if !ownerRunFor(builder, "craig@example.com") {
		t.Fatal("a virgin Builder seed must count as an owner run — this is the regression that cost the authoring catalog")
	}
	if !agentCanAuthor(builder) {
		t.Fatal("Builder must be able to author")
	}
	// Both conditions together are what actually gates the grant.
	if !(agentCanAuthor(builder) && ownerRunFor(builder, "craig@example.com")) {
		t.Error("Builder still fails the combined authoring gate")
	}
}

func TestOwnerRunAcceptsTheRealOwner(t *testing.T) {
	a := AgentRecord{ID: "x", Author: true, Owner: "craig@example.com"}
	if !ownerRunFor(a, "craig@example.com") {
		t.Error("an agent's own owner must pass")
	}
}

func TestOwnerRunAcceptsUnsetOwner(t *testing.T) {
	if !ownerRunFor(AgentRecord{ID: "x", Author: true}, "anyone@example.com") {
		t.Error("an unset owner must still pass")
	}
}

// The security property the gate exists for must survive the fix: an agent
// owned by a REAL user must not hand authoring to a different real user.
func TestOwnerRunStillBlocksAnotherUser(t *testing.T) {
	a := AgentRecord{ID: "x", Author: true, Owner: "alice@example.com"}
	if ownerRunFor(a, "bob@example.com") {
		t.Fatal("authoring leaked to a non-owner — the gate's whole purpose")
	}
}

// A shadowed seed carries a real owner, and that still gates normally.
func TestShadowedSeedGatesOnItsOwner(t *testing.T) {
	shadowed := AgentRecord{ID: "seed-builder", Owner: "alice@example.com"}
	if !ownerRunFor(shadowed, "alice@example.com") {
		t.Error("the shadow's owner should pass")
	}
	if ownerRunFor(shadowed, "bob@example.com") {
		t.Error("a shadowed seed must not grant authoring to another user")
	}
}
