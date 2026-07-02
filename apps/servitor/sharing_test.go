package servitor

import "testing"

func TestCanManageAppliance(t *testing.T) {
	owned := Appliance{Owner: "alice"}
	if !canManageAppliance("alice", owned, false) {
		t.Error("owner should be able to manage their own appliance")
	}
	if canManageAppliance("bob", owned, false) {
		t.Error("a non-owner, non-admin must NOT manage someone else's appliance")
	}
	if !canManageAppliance("bob", owned, true) {
		t.Error("an admin should be able to manage any appliance")
	}
	// Legacy record with no Owner stamp: whoever holds it (it's in their store)
	// manages it — non-owners can't even resolve such a record.
	legacy := Appliance{}
	if !canManageAppliance("bob", legacy, false) {
		t.Error("legacy no-owner record: the holder should manage it")
	}
}
