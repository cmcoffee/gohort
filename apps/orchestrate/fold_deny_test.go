package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Tools modal's save recomputes the per-agent deny list from the CHECKED
// set — but an agent-scoped tool renders in a read-only section and can never
// be checked. Folding it in anyway re-disabled the tool on every save of the
// agent editor: the owner enabled it, a later save silently re-denied it, and
// nothing on screen explained why. "Not picked" must only ever mean a checkbox
// the owner actually saw and left empty.
func TestFoldNeverDeniesAToolTheModalCannotShow(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	// The persistent-tool helpers route through the process-global RootDB when
	// one is set (tempToolStore) — shared state across the test binary. Pin it
	// so this test's writes and the fold's reads meet in the same store.
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })

	mk := func(name string) TempTool {
		return TempTool{Name: name, Description: "d", CommandTemplate: "echo hi"}
	}
	if err := AdminPersistTempTool(udb, "u", mk("shared_tool")); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(udb, "u", mk("scoped_tool")); err != nil {
		t.Fatal(err)
	}
	if !SetUserToolScopeAgents(udb, "u", "scoped_tool", []string{"agent-1"}) {
		t.Fatal("scope write failed")
	}

	// A save where neither tool is in the picked set. The shared tool was a real
	// unchecked checkbox: denied. The scoped tool was never offered: untouched.
	out := foldUncheckedIntoDenyList(root, "u", []string{"web_search"}, nil)
	got := map[string]bool{}
	for _, n := range out {
		got[n] = true
	}
	if !got["shared_tool"] {
		t.Error("an unchecked SHARED tool is a real opt-out and must be denied")
	}
	if got["scoped_tool"] {
		t.Error("a scoped tool was never a checkbox — the fold must not deny it")
	}

	// Self-heal: a deny entry left behind by the old behavior is removed, so a
	// polluted record converges on the next save instead of needing a manual
	// re-enable that the next save would have undone again.
	out = foldUncheckedIntoDenyList(root, "u", []string{"web_search"}, []string{"scoped_tool", "stale_other"})
	got = map[string]bool{}
	for _, n := range out {
		got[n] = true
	}
	if got["scoped_tool"] {
		t.Error("existing pollution for a scoped tool must be healed, not preserved")
	}
	if !got["stale_other"] {
		t.Error("deny entries for tools that no longer exist are kept — they are not this fix's business")
	}
}
