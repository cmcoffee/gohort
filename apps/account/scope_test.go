package account

import (
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A key's scope keeps only what the editor offers its owner: features the
// admin permits them and targets from their own grantable list. Anything else
// posted is dropped, not stored.
func TestAKeyScopeKeepsOnlyWhatTheEditorOffers(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	SetFeatureAllowedUsers(RootDB, "desktop", []string{"someone-else"})

	T := &Account{}
	T.DB = RootDB.(*DBase).Bucket("account")
	got := T.grantableScope("alice", &TokenScope{
		Features: []string{"desktop", "made-up"},
		Targets:  []string{"worker", "agent:bobs-agent", "lead"},
	})
	if len(got.Features) != 0 {
		t.Errorf("features the admin withheld or that do not exist were kept: %v", got.Features)
	}
	if len(got.Targets) != 2 || got.Targets[0] != "worker" || got.Targets[1] != "lead" {
		t.Errorf("targets: %v", got.Targets)
	}
}
