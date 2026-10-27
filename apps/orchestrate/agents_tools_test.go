package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The Tools modal's save recomputes the per-agent deny list from the CHECKED
// set — but a scoped tool is never one of THOSE checkboxes: it has its own
// checklist group, whose decisions arrive in the deny list already made.
// Folding it in anyway re-disabled the tool on every save of the agent editor:
// the owner enabled it, a later save silently re-denied it, and nothing on
// screen explained why. "Not picked" must only ever mean a checkbox in the
// allowed_tools list that the owner saw and left empty.
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

	// A deny entry for a scoped tool now SURVIVES the fold: it is the owner
	// unchecking that tool in its own group, sent in this very save. Dropping
	// it here would erase the switch-off in the act of saving it.
	out = foldUncheckedIntoDenyList(root, "u", []string{"web_search"}, []string{"scoped_tool", "stale_other"})
	got = map[string]bool{}
	for _, n := range out {
		got[n] = true
	}
	if !got["scoped_tool"] {
		t.Error("the scoped group's own decision must survive the fold, not be recomputed away")
	}
	if !got["stale_other"] {
		t.Error("deny entries for tools that no longer exist are kept — they are not this fix's business")
	}
}

// "Everything checked" clears the deny list — that is what an all-on catalog
// means. But the scoped group is not in that catalog, so its unchecked rows
// must survive: otherwise switching a scoped tool off, with every catalog tool
// on, un-switched it in the same request that switched it off.
func TestAllCheckedKeepsScopedOptOuts(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
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

	out := keepScopedDenials(root, "u", []string{"scoped_tool", "shared_tool", "gone_tool"})
	got := map[string]bool{}
	for _, n := range out {
		got[n] = true
	}
	if !got["scoped_tool"] {
		t.Error("a scoped tool's opt-out is not part of what all-checked describes — keep it")
	}
	if got["shared_tool"] {
		t.Error("a pool tool IS in the catalog: all-checked means it is on")
	}
	if got["gone_tool"] {
		t.Error("a name that is no longer a scoped tool has nothing to preserve")
	}
	if keepScopedDenials(root, "u", nil) != nil {
		t.Error("an empty deny list stays empty")
	}
}
