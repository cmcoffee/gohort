// Bridge keys authenticate the iMessage hook and the desktop WS bridge, and
// they were resolved with ==, over a scan that returned on the first match.
// Both halves leak: the comparison tells you how much of a guess was right,
// and the early return tells you where in the table it landed. core/peer_key.go
// writes down why peer keys do neither; these are the same kind of credential.
package bridges

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func keyFixture(t *testing.T) *Bridges {
	t.Helper()
	T := &Bridges{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	T.saveBridgeKey(BridgeKey{ID: "k1", Key: "secret-craig", Owner: "craig", Service: "imessage", Enabled: true})
	T.saveBridgeKey(BridgeKey{ID: "k2", Key: "secret-dana", Owner: "dana", Service: "imessage", Enabled: true})
	// An ownerless record, which bridgeKeyOwner must never resolve.
	T.saveBridgeKey(BridgeKey{ID: "k3", Key: "secret-orphan", Service: "imessage"})
	return T
}

func TestBridgeKeyResolvesExactly(t *testing.T) {
	T := keyFixture(t)

	if owner, ok := T.bridgeKeyOwner("secret-craig"); !ok || owner != "craig" {
		t.Errorf("exact key did not resolve: %q ok=%v", owner, ok)
	}
	if owner, ok := T.bridgeKeyOwner("secret-dana"); !ok || owner != "dana" {
		t.Errorf("second key did not resolve: %q ok=%v", owner, ok)
	}
	// A prefix, an extension, a case change and a blank must all miss —
	// checking that the constant-time rewrite did not turn into a loose match.
	for _, bad := range []string{"", " ", "secret-crai", "secret-craigg", "SECRET-CRAIG", "nope"} {
		if owner, ok := T.bridgeKeyOwner(bad); ok {
			t.Errorf("key %q was accepted as %q", bad, owner)
		}
	}
}

func TestBridgeKeyWithNoOwnerNeverResolves(t *testing.T) {
	T := keyFixture(t)
	if owner, ok := T.bridgeKeyOwner("secret-orphan"); ok {
		t.Errorf("an ownerless key resolved to %q", owner)
	}
}

func TestValidateBridgeKeyMatchesAndStampsLastSeen(t *testing.T) {
	T := keyFixture(t)

	k, ok := T.validateBridgeKey("secret-dana")
	if !ok || k.ID != "k2" {
		t.Fatalf("validate did not find the right record: %+v ok=%v", k, ok)
	}
	if k.LastSeen == "" {
		t.Error("a successful match should stamp LastSeen")
	}
	// The stamp must have been persisted, not just returned.
	var stored BridgeKey
	if !T.DB.Get(bridgeKeysTable, "k2", &stored) || stored.LastSeen == "" {
		t.Error("LastSeen was not written back to the store")
	}
	for _, bad := range []string{"", "secret-dan", "secret-danaa"} {
		if _, ok := T.validateBridgeKey(bad); ok {
			t.Errorf("key %q was accepted", bad)
		}
	}
}
