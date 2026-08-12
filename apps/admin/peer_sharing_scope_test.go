package admin

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// TestSplitPeerApplianceScopeRefusesTwoOwners — a key reaching two people's
// machines is a permission nobody can describe in a sentence, and silently
// dropping half the selection is worse than saying no.
func TestSplitPeerApplianceScopeRefusesTwoOwners(t *testing.T) {
	if _, _, err := splitPeerApplianceScope([]string{"craig:a", "dana:b"}); err == nil {
		t.Fatal("a selection spanning two owners was accepted")
	} else if !strings.Contains(err.Error(), "craig") || !strings.Contains(err.Error(), "dana") {
		t.Errorf("error should name both owners: %v", err)
	}
}

// TestSplitPeerApplianceScopeDerivesTheOwner — the owner comes out of the
// selection, so the operator never sets it separately and cannot set it wrong.
func TestSplitPeerApplianceScopeDerivesTheOwner(t *testing.T) {
	owner, ids, err := splitPeerApplianceScope([]string{"craig:a", " craig:b ", "malformed", "", "craig:"})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if owner != "craig" {
		t.Errorf("owner = %q", owner)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("ids = %v, want [a b]", ids)
	}
	owner, ids, err = splitPeerApplianceScope(nil)
	if err != nil || owner != "" || len(ids) != 0 {
		t.Errorf("empty selection = %q %v %v", owner, ids, err)
	}
}

// TestPeerKeyScopeValuesRoundTrips — the modal prefills from these, so a key
// whose stored scope renders back to something the checklist cannot match would
// silently show as ungranted and be cleared on the next save.
func TestPeerKeyScopeValuesRoundTrips(t *testing.T) {
	k := PeerKey{Owner: "craig", Appliances: []string{"a", "b"}}
	vals := peerKeyScopeValues(k)
	owner, ids, err := splitPeerApplianceScope(vals)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if owner != k.Owner || len(ids) != len(k.Appliances) {
		t.Errorf("round trip = %q %v, want %q %v", owner, ids, k.Owner, k.Appliances)
	}
	if len(peerKeyScopeValues(PeerKey{Appliances: []string{"a"}})) != 0 {
		t.Error("a key with no owner should render no selections")
	}
}
