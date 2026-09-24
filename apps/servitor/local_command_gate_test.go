package servitor

// A local command appliance runs `sh -c` on the gohort host itself, so only an
// admin-owned one may run: owning one is owning the server.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestOnlyAnAdminOwnedLocalCommandApplianceRuns(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prev := AuthDB
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { AuthDB = prev })

	cases := []struct {
		name string
		a    Appliance
		ok   bool
	}{
		{"admin-owned command", Appliance{Name: "c", Type: "command", Owner: "root"}, true},
		{"user-owned command", Appliance{Name: "c", Type: "command", Owner: "alice"}, false},
		{"unowned command", Appliance{Name: "c", Type: "command"}, false},
		{"unknown owner", Appliance{Name: "c", Type: "command", Owner: "ghost"}, false},
		{"remote stub executes on the peer", Appliance{Name: "c", Type: "command", Owner: "alice", PeerName: "box"}, true},
		{"ssh is not local exec", Appliance{Name: "s", Type: "ssh", Owner: "alice"}, true},
	}
	for _, tc := range cases {
		err := localCommandAllowed(tc.a)
		if (err == nil) != tc.ok {
			t.Errorf("%s: allowed=%v, want %v (err=%v)", tc.name, err == nil, tc.ok, err)
		}
	}
}
