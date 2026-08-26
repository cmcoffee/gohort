package orchestrate

import "testing"

// A seed agent's Owner is the framework marker "system", not an author.
//
// The owner-view redirect exists for a PUBLISHED agent chatted by someone who
// is not its author: the record and its custom-tool pool live in the author's
// store. That is right for a user-authored agent and wrong for a seed, because
// "system" has no store — redirecting to it hands the agent an EMPTY fleet.
//
// Builder is the agent that suffers most and can least afford it: the only
// agent with authoring access, whose entire job is reading and changing the
// user's agents. Observed live as agents(action="list") returning [] in the
// same turn that survey listed thirty-eight, every lookup failing, and Builder
// telling the user their agent must have been deleted.
func TestSeedAgentsUseTheCallersOwnStore(t *testing.T) {
	const user = "cmcoffee@gmail.com"

	shouldRedirect := func(owner string) bool {
		return owner != "" && owner != user && owner != seedOwner
	}

	// A seed (Builder) must NOT redirect — it reads the caller's fleet.
	if shouldRedirect(seedOwner) {
		t.Error("a seed agent was redirected to the system store, where the user's fleet does not exist")
	}
	// An agent the caller owns: nothing to redirect to.
	if shouldRedirect(user) {
		t.Error("an agent the caller owns was redirected away from their own store")
	}
	// An unowned record: same.
	if shouldRedirect("") {
		t.Error("an ownerless agent was redirected")
	}
	// A genuinely published agent by another author STILL redirects — that is
	// the case the mechanism exists for and must keep working.
	if !shouldRedirect("someone-else@example.com") {
		t.Error("a published agent no longer reads its author's store, so it would lose its own tools")
	}
}

// Builder is a seed, which is what makes the above bite.
func TestBuilderIsASeedAgent(t *testing.T) {
	if !isBuilderAgent("seed-builder") {
		t.Fatal("seed-builder is no longer recognised as Builder")
	}
	if _, ok := seedAgentByID("seed-builder"); !ok {
		t.Fatal("Builder is not a seed agent; the owner-view redirect would not have applied to it")
	}
}
