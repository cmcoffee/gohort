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
	"os"
	"strings"
	"testing"
)

// readGoSource reads one of this package's own files, for the handful of
// facts that live in a struct literal rather than behind a function.
func readGoSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestThePeerKeyIsSecuredAndManaged(t *testing.T) {
	// The name it lands under, so the test is about the real record.
	name := peerCredentialName("gohort local")
	if name != "peer_gohort_local_key" {
		t.Fatalf("unexpected credential name: %q", name)
	}

	// Read the values off the source: this is a struct literal deep in a
	// provisioning path that needs a live store to exercise, and the two
	// fields are the whole point.
	src := readGoSource(t, "peer_remote_images.go")
	at := strings.Index(src, "Name:              credName,")
	if at < 0 {
		t.Fatal("the peer credential save moved")
	}
	block := src[at : at+1400]
	if !strings.Contains(block, "Secured: true") {
		t.Error("the peer key should be reachable only through tools that declare it — " +
			"otherwise it grows an automatic call_<name> tool in every agent's catalog")
	}
	if !strings.Contains(block, `Managed: "peer"`) {
		t.Error("the peer key should be marked as owned by peering, so menus stop offering it")
	}
	// The connectors that use it name it explicitly, which is what makes
	// Secured safe here rather than a lockout.
	if !strings.Contains(src, "Credential:     credName") {
		t.Error("the image connectors must declare the credential, or securing it breaks them")
	}

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
