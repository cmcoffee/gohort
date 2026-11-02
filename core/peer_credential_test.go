package core

// The peer key is a credential nobody chose to create and nobody should
// be offered as something to wire up.
//
// Peering saves it when a peer is added and deletes it when the peer is
// forgotten. Left as an ordinary credential it did two things wrong: it
// appeared in every credential menu as a binding target that vanishes
// with its peer, and — the part that matters more — it generated an
// automatic call_<name> tool in every agent's catalog, handing the
// default pool an open request path to the peer, carrying the sharing
// key, for a credential no operator granted to anything.

import (
	"testing"
)

func TestThePeerKeyIsSecuredAndManaged(t *testing.T) {
	// The predicate the menus filter on.
	if !(SecureCredential{Managed: "peer"}).ManagedElsewhere() {
		t.Error("a managed credential should say so")
	}
	if (SecureCredential{Name: "ordinary"}).ManagedElsewhere() {
		t.Error("an operator's own credential is not managed elsewhere")
	}
	if (SecureCredential{Managed: "   "}).ManagedElsewhere() {
		t.Error("whitespace is not a manager")
	}
}
