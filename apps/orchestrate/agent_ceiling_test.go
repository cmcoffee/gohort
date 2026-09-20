package orchestrate

// Who can reach an agent, and by whose decision.
//
// A published agent takes TWO gates: the admin's grant of the app is the
// ceiling, and the owner's own list narrows inside it. It used to be an OR,
// which meant the owner's list could reach somebody the admin had never
// granted — a narrowing that widened.

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

func TestAPublishedAgentTakesBothGates(t *testing.T) {
	T := reachCeilingFixture(t)

	// Not narrowed: everybody the admin allowed. Carol has no grant, so the
	// ceiling alone stops her.
	if !T.AgentReachableBy(signedInAs(t, "bob"), "tro", "alice", nil, true) {
		t.Error("a granted user cannot reach an unnarrowed published agent")
	}
	if T.AgentReachableBy(signedInAs(t, "carol"), "tro", "alice", nil, true) {
		t.Error("a user with no grant reached a published agent")
	}

	// Narrowed to carol: she is on the owner's list but has no grant, so the
	// ceiling still stops her. A list that could admit her would be widening.
	if T.AgentReachableBy(signedInAs(t, "carol"), "tro", "alice", []string{"carol"}, true) {
		t.Error("the owner's list reached past the admin's grant")
	}
	// Narrowed to carol: bob HAS the grant but is not on the list.
	if T.AgentReachableBy(signedInAs(t, "bob"), "tro", "alice", []string{"carol"}, true) {
		t.Error("a granted user reached an agent the owner narrowed away from them")
	}
	// Both: granted and named.
	if !T.AgentReachableBy(signedInAs(t, "bob"), "tro", "alice", []string{"bob"}, true) {
		t.Error("a user with both the grant and the owner's nod was refused")
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
