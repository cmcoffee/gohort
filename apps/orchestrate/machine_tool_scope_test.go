package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// scopeCatalog is a stand-in for an assembled turn catalog: the control
// plane the framework supplies, plus a couple of real capabilities.
func scopeCatalog() []AgentToolDef {
	names := []string{
		"plan_set", "stay_silent", "keep_going", "respond_directly", "change_phase",
		"web_search", "atlassian_getconfluencepage", "atlassian_search",
	}
	out := make([]AgentToolDef, 0, len(names))
	for _, n := range names {
		out = append(out, AgentToolDef{Tool: Tool{Name: n}})
	}
	return out
}

func scopeNames(tools []AgentToolDef) []string {
	out := make([]string, 0, len(tools))
	for _, td := range tools {
		out = append(out, td.Tool.Name)
	}
	return out
}

func hasScopeName(tools []AgentToolDef, name string) bool {
	for _, td := range tools {
		if td.Tool.Name == name {
			return true
		}
	}
	return false
}

// A phase narrows REACH, not the machinery. change_phase in particular is
// the way out of the phase: a narrowing that took it left the session
// standing in a step it could never leave.
func TestPhaseNarrowingKeepsTheControlPlane(t *testing.T) {
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Tools: []string{"web_search"}}}
	out, dropped, unmatched, fellBack := m.narrowCatalog(scopeCatalog())

	if fellBack {
		t.Fatal("a phase that matched a tool should not fall back")
	}
	if len(unmatched) != 0 {
		t.Errorf("nothing was misnamed, got unmatched %v", unmatched)
	}
	for _, n := range []string{"plan_set", "stay_silent", "keep_going", "respond_directly", "change_phase"} {
		if !hasScopeName(out, n) {
			t.Errorf("phase narrowing revoked %q — the model cannot end the turn or leave the phase without it", n)
		}
	}
	if !hasScopeName(out, "web_search") {
		t.Error("the tool the phase actually asked for is missing")
	}
	// Everything else goes, and says so.
	if hasScopeName(out, "atlassian_search") {
		t.Error("a tool the phase did not name survived the narrowing")
	}
	if len(dropped) != 2 {
		t.Errorf("dropped = %v, want the two atlassian tools", dropped)
	}
}

// The catalog order is the payload order, and a payload that reshuffles
// between turns is a cold prompt cache every turn.
func TestPhaseNarrowingPreservesCatalogOrder(t *testing.T) {
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Tools: []string{"atlassian_search", "web_search"}}}
	out, _, _, _ := m.narrowCatalog(scopeCatalog())

	got := scopeNames(out)
	want := []string{"plan_set", "stay_silent", "keep_going", "respond_directly", "change_phase", "web_search", "atlassian_search"}
	if len(got) != len(want) {
		t.Fatalf("catalog = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("catalog = %v, want %v (order follows the catalog, not the phase list)", got, want)
		}
	}
}

// The live failure this came from: a phase authored by copying the remote
// server's own tool names. Atlassian ships camelCase; mcpExposedName
// lowercases. Every name missed, the catalog narrowed 112 → 0, and the
// model reported its own tools as unknown and rerouted.
func TestPhaseThatMatchesNothingKeepsTheWholeCatalog(t *testing.T) {
	catalog := scopeCatalog()
	m := turnMachine{on: true, phase: MachinePhase{
		Name:  "investigate",
		Tools: []string{"atlassian_getConfluencePage", "atlassian.search"},
	}}
	out, dropped, unmatched, fellBack := m.narrowCatalog(catalog)

	if !fellBack {
		t.Fatal("a phase whose every name missed should be reported as a misconfiguration")
	}
	if len(out) != len(catalog) {
		t.Errorf("catalog = %d tool(s), want the full %d — an unmatched list must not resolve to nothing", len(out), len(catalog))
	}
	if len(dropped) != 0 {
		t.Errorf("nothing was deliberately narrowed away, got dropped %v", dropped)
	}
	if len(unmatched) != 2 {
		t.Errorf("unmatched = %v, want both misnamed tools so the log can name them", unmatched)
	}
}

// A machine that never mentions tools changes nothing about the agent.
func TestPhaseWithNoToolsInheritsEverything(t *testing.T) {
	catalog := scopeCatalog()
	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate"}}
	out, dropped, unmatched, fellBack := m.narrowCatalog(catalog)

	if len(out) != len(catalog) || len(dropped) != 0 || len(unmatched) != 0 || fellBack {
		t.Errorf("an empty phase list should pass the catalog through untouched: %d tools, dropped %v, unmatched %v, fellBack %v",
			len(out), dropped, unmatched, fellBack)
	}
}

// An agent with no machine at all is never narrowed.
func TestNoMachineNeverNarrows(t *testing.T) {
	catalog := scopeCatalog()
	out, dropped, unmatched, fellBack := turnMachine{}.narrowCatalog(catalog)

	if len(out) != len(catalog) || len(dropped) != 0 || len(unmatched) != 0 || fellBack {
		t.Errorf("no machine means no narrowing: %d tools, dropped %v, unmatched %v, fellBack %v",
			len(out), dropped, unmatched, fellBack)
	}
}
