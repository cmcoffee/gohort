package core

// The person doing the sharing is not one of the people they can share to.

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// Every setter drops the owner from a recipient list, so offering their own
// name was offering a choice that does nothing: pick it, save, watch the chip
// vanish. The small version of a picker that stores names and changes nothing.
func TestTheSharerIsNotOfferedTheirOwnName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	db.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	db.Set(AuthTable, "user:zed", AuthUser{Username: "zed", Pending: true})

	got := string(UserCandidatesJSON(db, "alice"))
	if strings.Contains(got, "alice") {
		t.Errorf("the sharer is offered their own name: %s", got)
	}
	if !strings.Contains(got, "bob") {
		t.Errorf("a colleague is missing: %s", got)
	}
	// Somebody who has not accepted their invitation is nobody to share with.
	if strings.Contains(got, "zed") {
		t.Errorf("a pending user is offered: %s", got)
	}
	// A caller with nobody to exclude gets everybody — an admin granting on a
	// deployment resource may legitimately be one of the grantees.
	if all := string(UserCandidatesJSON(db, "")); !strings.Contains(all, "alice") {
		t.Errorf("an unfiltered listing dropped somebody: %s", all)
	}
}
