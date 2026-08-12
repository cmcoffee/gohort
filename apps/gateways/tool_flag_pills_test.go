package gateways

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// A pill in the scope selector is normally an AGENT: clicking it moves the tool
// into or out of that agent's scope. Two of them are not — Builder-only and
// bound-only are flags ON the tool record — and a flag pill that falls through
// to the provider is looked up as an agent, which is how a rendered, clickable
// control failed with `agent "bound_only" not found`.

// TestEveryFlagPillIsInterceptedBeforeTheProvider — the sweep that would have
// caught it. The list of flag pills and the list of intercepted targets are
// written in two places and neither can see the other disagree.
func TestEveryFlagPillIsInterceptedBeforeTheProvider(t *testing.T) {
	src, err := os.ReadFile("gateways.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// Every pill key offered by the selector.
	offered := map[string]bool{}
	for _, m := range regexp.MustCompile(`\{Key: "(\w+)", Label:`).FindAllStringSubmatch(body, -1) {
		offered[m[1]] = true
	}
	if len(offered) == 0 {
		t.Fatal("found no pills — the sweep is no longer looking where they live")
	}
	// The interception point.
	i := strings.Index(body, `if target == "builder_only"`)
	if i < 0 {
		t.Fatal("the flag-pill interception has moved")
	}
	guard := body[i : i+200]
	for key := range offered {
		if !strings.Contains(guard, `"`+key+`"`) {
			t.Errorf("pill %q is offered but not intercepted before the agent provider — "+
				"clicking it looks up an AGENT by that name and fails", key)
		}
	}
}

// TestTheTwoFlagsAreMutuallyExclusive — one reserves a tool for authoring, the
// other says it belongs to whatever binds it. Holding both would leave the
// selector describing a state no filter produces.
func TestTheTwoFlagsAreMutuallyExclusive(t *testing.T) {
	src, err := os.ReadFile("gateways.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, `if target == "builder_only" || target == "bound_only"`)
	if i < 0 {
		t.Fatal("the combined flag handler has moved")
	}
	// Wide enough to cover the orphan-adoption path that now sits between the
	// guard and the flag writes; a fixed window that ends before them fails on a
	// change that moved code rather than broke it.
	block := body[i : i+2200]
	if !strings.Contains(block, "row.Tool.BoundOnly = false") ||
		!strings.Contains(block, "row.Tool.BuilderOnly = false") {
		t.Error("turning one flag on does not clear the other — a tool could hold both, " +
			"and the selector would describe a state no filter produces")
	}
}

// TestAnOrphanIsAdoptedRatherThanRefused — the reported friction. An orphan is
// a committed tool that lost its agent, so it is not in the persistent pool and
// the lookup missed: clicking the flag reported "tool not found" on a tool
// sitting right there in the list, and the workaround was to pick "All my
// agents" first and come back.
//
// Especially wrong for bound-only, whose whole point is that the tool belongs to
// a BINDING rather than to any agent. Requiring it to be given to every agent
// first, so it can then be taken away from all of them, is the opposite of the
// intent.
func TestAnOrphanIsAdoptedRatherThanRefused(t *testing.T) {
	src, err := os.ReadFile("gateways.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, `if target == "builder_only" || target == "bound_only"`)
	if i < 0 {
		t.Fatal("the flag handler has moved")
	}
	block := body[i : i+1600]
	if !strings.Contains(block, "LoadOrphanedTempTools(db, user)") {
		t.Error("the flag handler does not look in the orphan pool, so setting a flag on a tool " +
			"with no agent fails with \"tool not found\"")
	}
	if !strings.Contains(block, `AdminRehomeOrphanTool(db, user, name, "global")`) {
		t.Error("an orphan is not adopted before the flag is written — the operator is still " +
			"made to home it by hand first")
	}
	// The refusal must survive for a tool that genuinely does not exist.
	if !strings.Contains(block, `http.Error(w, "tool not found", http.StatusNotFound)`) {
		t.Error("a genuinely missing tool no longer reports not-found")
	}
}
