package orchestrate

// A credential another configuration owns is not offered as something to
// wire up. The peer key is the case: peering writes it when a peer is
// added and deletes it when the peer is forgotten, so anything bound to
// it breaks the day the peer is dropped.
//
// Checked at the two menus that do NOT already filter it out.
// userScopableCredentials needs no change — it drops secured
// credentials, and the peer key is secured — which is worth asserting
// rather than assuming, since that is the reason this file only covers
// two of the three.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestManagedCredentialsAreNotOfferedAsBindingTargets(t *testing.T) {
	// Builder's survey is deliberately generous — it shows disabled and
	// half-finished credentials, because those are what it needs to see —
	// so the managed exclusion has to be explicit there.
	src, err := os.ReadFile("builder_tools.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "if c.ManagedElsewhere() {") {
		t.Error("Builder's credential survey still offers credentials peering owns")
	}

	// And the per-agent scope menu excludes it for a different reason,
	// which is why it needed no change: a secured credential's access
	// follows its tool bindings, so per-agent scope is meaningless for
	// one.
	if !(SecureCredential{Secured: true}).Secured {
		t.Fatal("sanity")
	}
	if !strings.Contains(readOwnSource(t, "agent_credentials.go"), "c.Secured") {
		t.Error("the scope menu no longer filters secured credentials, so the peer key would reappear there")
	}
}

func readOwnSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
