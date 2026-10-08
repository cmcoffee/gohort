package appscript

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An alias has to survive a restart, or every player's leaderboard entry
// orphans on the next deploy: the key is kept in RootDB and read back.
func TestCallerAliasSurvivesARestart(t *testing.T) {
	saved := RootDB
	t.Cleanup(func() { RootDB = saved })
	store := kvlite.MemStore()
	RootDB = &DBase{Store: store}
	spec := AppSpec{Slug: "game", Owner: "alice"}
	before := CallerAlias(spec, "bob")

	aliasKeys.mu.Lock()
	aliasKeys.db, aliasKeys.key = nil, nil // the process restarts
	aliasKeys.mu.Unlock()
	RootDB = &DBase{Store: store}
	if after := CallerAlias(spec, "bob"); after != before {
		t.Fatalf("alias changed across a restart: %s -> %s", before, after)
	}
	if CallerAlias(spec, before) != before {
		t.Error("an alias given back is not hashed again")
	}
	if CallerAlias(spec, "") != "" {
		t.Error("nobody has no alias")
	}
}

// Naming a tool to call adds to the default grant; it must not take fetch
// away from the rest of the script.
func TestToolCapsAddToTheDefaults(t *testing.T) {
	if !onlyAddedCaps([]string{"tool:get_weather"}) || !onlyAddedCaps([]string{"ask", "tool:x"}) || onlyAddedCaps([]string{"tool:x", "fetch"}) || onlyAddedCaps(nil) {
		t.Fatal("onlyAddedCaps")
	}
}
