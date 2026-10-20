package orchestrate

// Who can reach an agent, and by whose decision.
//
// A published agent reaches the people the admin granted the app AND the
// people the owner named: Everyone is one more choice in the owner's share
// list, so the names beside it add to it. For a while the list narrowed
// inside the grant, which made "Everyone, Bob" read as "only Bob".

import (
	"net/http"
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// ceilingFixture stands up an auth store with three people: an admin, and two
// ordinary users of whom only one has been granted the agents app.
func reachCeilingFixture(t *testing.T) *OrchestrateApp {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob", Apps: []string{"/agents/tro"}})
	adb.Set(AuthTable, "user:carol", AuthUser{Username: "carol"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })
	return &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
}

// signedInAs mints a real session for who and carries it on a request, since
// the gate reads the caller's identity from the cookie like every other door.
func signedInAs(t *testing.T, who string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/agents/tro", nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), who)})
	return r
}

func TestAPublishedAgentReachesTheGrantedAndTheNamed(t *testing.T) {
	T := reachCeilingFixture(t)

	// Bob has the admin's grant, carol has nothing.
	if !T.AgentReachableBy(signedInAs(t, "bob"), "tro", "alice", nil, true) {
		t.Error("a granted user cannot reach a published agent")
	}
	if T.AgentReachableBy(signedInAs(t, "carol"), "tro", "alice", nil, true) {
		t.Error("a user with no grant and no name reached a published agent")
	}
	// Carol named by the owner reaches it with no grant, as she did before it
	// was published: publishing does not take a share away.
	if !T.AgentReachableBy(signedInAs(t, "carol"), "tro", "alice", []string{"carol"}, true) {
		t.Error("publishing took the share away from somebody the owner named")
	}
	// And naming carol does not take it from bob: the names add, they do not
	// narrow.
	if !T.AgentReachableBy(signedInAs(t, "bob"), "tro", "alice", []string{"carol"}, true) {
		t.Error("the owner's list narrowed the administrator's grant")
	}
}

// Unpublished is the owner's business alone. Nobody's grant is involved,
// because handing an agent to two colleagues does not go through an admin.
func TestAnUnpublishedAgentIsTheOwnersAlone(t *testing.T) {
	T := reachCeilingFixture(t)
	// Carol has no app grant at all, and still reaches it: the owner said so.
	if !T.AgentReachableBy(signedInAs(t, "carol"), "tro", "alice", []string{"carol"}, false) {
		t.Error("a peer share was blocked by an app grant nobody asked about")
	}
	// And an empty list on an UNPUBLISHED agent is private, not public — the
	// opposite reading from the published case, because there is no ceiling
	// behind it to mean anything.
	if T.AgentReachableBy(signedInAs(t, "bob"), "tro", "alice", nil, false) {
		t.Error("an unshared, unpublished agent was reachable")
	}
}

// The owner always reaches their own, published or not, narrowed or not.
func TestTheOwnerAlwaysReachesTheirOwn(t *testing.T) {
	T := reachCeilingFixture(t)
	for _, published := range []bool{true, false} {
		if !T.AgentReachableBy(signedInAs(t, "alice"), "tro", "alice", []string{"bob"}, published) {
			t.Errorf("the owner was refused their own agent (published=%v)", published)
		}
	}
}

// An admin passes the narrowing. They can already read the record, revoke the
// share and un-publish it; locking them out of the thing they govern would be
// a gate that protects nothing and confuses whoever is debugging it.
func TestAnAdminIsNotNarrowedOut(t *testing.T) {
	T := reachCeilingFixture(t)
	if !T.AgentReachableBy(signedInAs(t, "root"), "tro", "alice", []string{"bob"}, true) {
		t.Error("an admin was locked out of an agent they govern")
	}
}

// Nobody signed in reaches nothing, whatever the lists say.
func TestAnAnonymousRequestReachesNothing(t *testing.T) {
	T := reachCeilingFixture(t)
	r := httptest.NewRequest(http.MethodGet, "/agents/tro", nil)
	if T.AgentReachableBy(r, "tro", "alice", nil, true) {
		t.Error("an anonymous request reached a published agent")
	}
}
