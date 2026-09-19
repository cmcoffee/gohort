package core

// Contact permissions are scoped to the agent that earned them. See
// contactScopeKey for why, and docs/task-notes.md's sibling reasoning about a
// restriction that has to travel with the thing it restricts.

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// A permission and a guardrail have to share a scope, or the tighter one is
// decorative: an agent carries a persona and its own rules about what it may
// say to a person, and if the permission to reach that person belongs to the
// OWNER, handing the message to a different agent walks around all of it. That
// is not a bypass anybody has to intend; it is an ordinary dispatch.
func TestAContactGrantBelongsToTheAgentThatEarnedIt(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SetContactPolicy(db, "alice", "careful-agent", "dana", PolicyAllow)

	if !IsContactPreAuthorized(db, "alice", "careful-agent", "dana") {
		t.Error("the agent that was granted cannot message the contact")
	}
	if IsContactPreAuthorized(db, "alice", "blunt-agent", "dana") {
		t.Error("another agent spent a grant it was never given")
	}
}

// Specific beats all-agents in BOTH directions, which is what makes the strict
// case expressible: an owner can promote a contact and still hold one agent
// back from them.
func TestAnAgentsOwnDecisionWinsOverThePromotedOne(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SetContactPolicy(db, "alice", "", "dana", PolicyAllow) // every agent
	SetContactPolicy(db, "alice", "blunt-agent", "dana", PolicyBlock)

	if got := ContactPolicy(db, "alice", "careful-agent", "dana"); got != PolicyAllow {
		t.Errorf("an agent with no decision of its own should read the promoted one, got %q", got)
	}
	if got := ContactPolicy(db, "alice", "blunt-agent", "dana"); got != PolicyBlock {
		t.Errorf("a promoted grant overrode an agent the owner had already held back: %q", got)
	}
	// And the reverse: one agent allowed where nobody else is.
	SetContactPolicy(db, "alice", "", "erin", PolicyBlock)
	SetContactPolicy(db, "alice", "careful-agent", "erin", PolicyAllow)
	if got := ContactPolicy(db, "alice", "careful-agent", "erin"); got != PolicyAllow {
		t.Errorf("an agent's own exception was overridden by the general rule: %q", got)
	}
	if got := ContactPolicy(db, "alice", "blunt-agent", "erin"); got != PolicyBlock {
		t.Errorf("the general rule stopped applying to everybody else: %q", got)
	}
}

// Every grant made before contacts were scoped means "any agent of mine may
// message this person", because that is what it meant when it was made. Reading
// the old key shape as the all-agents row is what makes this change need no
// migration and break nothing an owner has working.
func TestGrantsMadeBeforeScopingStillWork(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	// Exactly what the old code wrote: the legacy preauth flag, unscoped.
	db.Set("contact_preauth", "alice:dana", true)

	for _, agent := range []string{"one", "two", ""} {
		if !IsContactPreAuthorized(db, "alice", agent, "dana") {
			t.Errorf("agent %q lost a grant that already existed", agent)
		}
	}
	// And an agent can still be held back from it afterwards.
	SetContactPolicy(db, "alice", "two", "dana", PolicyBlock)
	if IsContactPreAuthorized(db, "alice", "two", "dana") {
		t.Error("a legacy grant could not be narrowed")
	}
	if !IsContactPreAuthorized(db, "alice", "one", "dana") {
		t.Error("narrowing one agent changed another")
	}
}

// The listing has to say which scope a row is for, or the settings page cannot
// tell "this agent" from "every agent" and the promote button acts on a row the
// owner did not mean.
func TestListingCarriesTheScope(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	SetContactPolicy(db, "alice", "", "dana", PolicyAllow)
	SetContactPolicy(db, "alice", "careful-agent", "dana", PolicyBlock)

	var all, scoped int
	for _, e := range ListContactPolicies(db, "alice") {
		if e.Target != "dana" {
			t.Errorf("a scoped key leaked its agent into the target: %+v", e)
		}
		switch e.Scope {
		case "":
			all++
		case "careful-agent":
			scoped++
		default:
			t.Errorf("unexpected scope %q", e.Scope)
		}
	}
	if all != 1 || scoped != 1 {
		t.Errorf("expected one row per scope, got all=%d scoped=%d", all, scoped)
	}
}
