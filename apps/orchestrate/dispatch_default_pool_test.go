package orchestrate

// An agent with no allowlist must get the SAME catalog whether it is talked to
// or dispatched to. The dispatch path filled that case from the raw registry
// while the interactive path filtered it, so being dispatched was the
// permissive reading of a field nobody had set.

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Pinned against the source because both paths resolve their pool from process
// state a unit test cannot stand up: the point is that they name the same
// function, not what that function returns on an empty registry.
func TestDispatchAndInteractiveShareTheDefaultPool(t *testing.T) {
	dispatch, err := os.ReadFile("agent_dispatch.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dispatch), "range RegisteredChatTools()") {
		t.Error("dispatch fills an empty allowlist from the RAW registry — that is " +
			"BlockedTools and the framework sets included, a wider catalog than the " +
			"same agent gets interactively. Use availableWorkerToolNames().")
	}
	// Counting CODE lines, not mentions: the comment above each site names the
	// function too, which made an exact count measure the prose.
	sites := 0
	for _, line := range strings.Split(string(dispatch), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") {
			continue
		}
		if strings.Contains(t, "availableWorkerToolNames()") {
			sites++
		}
	}
	if sites != 2 {
		t.Errorf("both dispatch sites should fill from the shared pool, found %d", sites)
	}

	interactive, err := os.ReadFile("runner_tool_catalog.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(interactive), "availableWorkerToolNames()") {
		t.Error("the interactive path no longer names the shared pool — the two have drifted apart again")
	}
}

// The filtered pool is genuinely narrower than the raw registry, or the change
// above is cosmetic.
func TestTheDefaultPoolIsFilteredAtAll(t *testing.T) {
	raw := map[string]bool{}
	for _, td := range RegisteredChatTools() {
		raw[td.Name()] = true
	}
	if len(raw) == 0 {
		t.Skip("no chat tools registered in this test binary")
	}
	pool := map[string]bool{}
	for _, n := range availableWorkerToolNames() {
		pool[n] = true
	}
	for name := range BlockedTools {
		if raw[name] && pool[name] {
			t.Errorf("%q is blocked yet present in the default pool", name)
		}
	}
}
