package core

// An admin changing a user's role or password keeps that user's own
// preferences: AuthSetUser used to rebuild the record from five fields.

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestChangingARoleKeepsTheUsersPreferences(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	db.Set(AuthTable, "user:alice", AuthUser{
		Username: "alice", PassHash: "old", Timezone: "America/Los_Angeles", NotifyForward: "phone",
		PrivateMode: true, InferredDisabled: true, Apps: []string{"/x"},
		PrivateModePerAgent: map[string]bool{"a1": true},
	})
	AuthSetUser(db, "alice", "", true)
	u, _ := AuthGetUser(db, "alice")
	if !u.Admin || u.Timezone != "America/Los_Angeles" || u.NotifyForward != "phone" || !u.PrivateMode ||
		!u.InferredDisabled || !u.PrivateModePerAgent["a1"] || u.PassHash != "old" || len(u.Apps) != 1 {
		t.Fatalf("promotion lost something: %+v", u)
	}
	AuthSetUser(db, "alice", "new-password-123", false)
	u, _ = AuthGetUser(db, "alice")
	if u.Admin || u.PassHash == "old" || u.Timezone != "America/Los_Angeles" {
		t.Fatalf("a password reset should change the hash and nothing else: %+v", u)
	}
}
