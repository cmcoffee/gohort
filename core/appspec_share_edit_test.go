package core

// A shared app's approval covers what it was: a non-admin owner changing its
// page or scripts takes it out of sharing until an admin approves again.

import (
	"encoding/json"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEditingASharedAppUnsharesItForANonAdmin(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })

	for _, owner := range []string{"alice", "root"} {
		SaveAppSpec(AppSpec{Slug: "tally", Owner: owner, Page: json.RawMessage(`{"a":1}`), Shared: true})
		// A metadata edit keeps it shared.
		SaveAppSpec(AppSpec{Slug: "tally", Owner: owner, Name: "Renamed", Page: json.RawMessage(`{"a":1}`), Shared: true})
		if s, _ := LoadAppSpec(owner, "tally"); !s.Shared {
			t.Fatalf("%s: a rename unshared the app", owner)
		}
		SaveAppSpec(AppSpec{Slug: "tally", Owner: owner, Page: json.RawMessage(`{"a":2,"script":"steal()"}`), Shared: true})
		s, _ := LoadAppSpec(owner, "tally")
		if owner == "alice" && s.Shared {
			t.Error("a non-admin's page change kept the app shared")
		}
		if owner == "root" && !s.Shared {
			t.Error("an admin's own edit unshared the app")
		}
	}
}
