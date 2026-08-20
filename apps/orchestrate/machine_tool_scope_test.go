package orchestrate

import (
	"strings"
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
	out, dropped, unmatched, fellBack := m.narrowCatalog(scopeCatalog(), nil)

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
	out, _, _, _ := m.narrowCatalog(scopeCatalog(), nil)

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
	out, dropped, unmatched, fellBack := m.narrowCatalog(catalog, nil)

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
	out, dropped, unmatched, fellBack := m.narrowCatalog(catalog, nil)

	if len(out) != len(catalog) || len(dropped) != 0 || len(unmatched) != 0 || fellBack {
		t.Errorf("an empty phase list should pass the catalog through untouched: %d tools, dropped %v, unmatched %v, fellBack %v",
			len(out), dropped, unmatched, fellBack)
	}
}

// An agent with no machine at all is never narrowed.
func TestNoMachineNeverNarrows(t *testing.T) {
	catalog := scopeCatalog()
	out, dropped, unmatched, fellBack := turnMachine{}.narrowCatalog(catalog, nil)

	if len(out) != len(catalog) || len(dropped) != 0 || len(unmatched) != 0 || fellBack {
		t.Errorf("no machine means no narrowing: %d tools, dropped %v, unmatched %v, fellBack %v",
			len(out), dropped, unmatched, fellBack)
	}
}

// A phase's tool list is a selection out of the WORKER POOL — that is the
// pool the picker offers, and the only one the author is choosing from. An
// attached source is a separate grant, made in a different picker for a
// different reason, and the tools it mints appear on no tool list anywhere.
//
// So a phase that names a couple of worker tools was silently revoking every
// attachment the agent had, with no box the author could tick to keep them.
// The limit was six days old and nobody chose it: before resident phases
// existed there was no way for a step to subtract anything from a live
// conversation at all.
func TestPhaseNarrowingDoesNotRevokeAttachments(t *testing.T) {
	catalog := append(scopeCatalog(),
		AgentToolDef{Tool: Tool{Name: "search_support_bundles"}},
		AgentToolDef{Tool: Tool{Name: "investigate_kiteworks"}})
	attached := map[string]bool{"search_support_bundles": true, "investigate_kiteworks": true}

	m := turnMachine{on: true, phase: MachinePhase{Name: "investigate", Tools: []string{"web_search"}}}
	out, dropped, _, _ := m.narrowCatalog(catalog, attached)

	for _, n := range []string{"search_support_bundles", "investigate_kiteworks"} {
		if !hasScopeName(out, n) {
			t.Errorf("the phase revoked %q, which attaching the source is what granted", n)
		}
	}
	for _, n := range dropped {
		if attached[n] {
			t.Errorf("%q was reported as dropped even though it survived", n)
		}
	}
	if !hasScopeName(out, "web_search") {
		t.Error("the tool the phase asked for is missing")
	}
	if hasScopeName(out, "atlassian_search") {
		t.Error("an ordinary worker tool the phase did not name should still be narrowed away")
	}
}

// The other half of the rule: a phase that names ONE attachment's tool has
// clearly been written with attachments in mind, and gets exactly what it
// wrote. "That source, not this one" has to be sayable, or the control is no
// control at all.
func TestAPhaseThatNamesAnAttachmentGovernsThemAll(t *testing.T) {
	catalog := append(scopeCatalog(),
		AgentToolDef{Tool: Tool{Name: "search_support_bundles"}},
		AgentToolDef{Tool: Tool{Name: "investigate_kiteworks"}})
	attached := map[string]bool{"search_support_bundles": true, "investigate_kiteworks": true}

	m := turnMachine{on: true, phase: MachinePhase{Name: "scan", Tools: []string{"search_support_bundles"}}}
	out, _, _, fellBack := m.narrowCatalog(catalog, attached)

	if fellBack {
		t.Fatal("the phase named a tool that exists; nothing should have fallen back")
	}
	if !hasScopeName(out, "search_support_bundles") {
		t.Fatal("the attachment the phase named is missing")
	}
	if hasScopeName(out, "investigate_kiteworks") {
		t.Error("naming one attachment must be able to mean 'that one, not the others'")
	}
}

// A list where nothing matched keeps the whole catalog (the author meant a
// selection, not emptiness). With attachments exempt there is a second way
// to be non-empty, and it must not swallow that rescue: a total miss is
// still a total miss, and still says so.
func TestATotalMissStillFallsBackWithAttachmentsPresent(t *testing.T) {
	catalog := append(scopeCatalog(), AgentToolDef{Tool: Tool{Name: "search_support_bundles"}})
	attached := map[string]bool{"search_support_bundles": true}

	m := turnMachine{on: true, phase: MachinePhase{Name: "scan", Tools: []string{"serch_web"}}}
	out, _, unmatched, fellBack := m.narrowCatalog(catalog, attached)

	if !fellBack || len(unmatched) != 1 {
		t.Fatalf("a wholly misnamed list must fall back and say so (fellBack=%v unmatched=%v)", fellBack, unmatched)
	}
	if len(out) != len(catalog) {
		t.Errorf("the fallback keeps the catalog whole, got %v", scopeNames(out))
	}
}

// Reach is the control that survives being run by a different agent: a
// capability class rather than a list of exact strings assembled per turn
// out of MCP connections, per-session credentials and per-agent
// attachments. Read-only keeps what reads and drops the rest, without the
// author having to know a single tool name.
func TestReadOnlyReachDropsWhatWrites(t *testing.T) {
	catalog := []AgentToolDef{
		{Tool: Tool{Name: "search_support_bundles", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork, CapRead}}},
		{Tool: Tool{Name: "run_shell", Caps: []Capability{CapExecute}}},
		{Tool: Tool{Name: "change_phase"}},
	}
	m := turnMachine{on: true, phase: MachinePhase{Name: "gather", Reach: ReachRead}}
	out, dropped, _, fellBack := m.narrowCatalog(catalog, nil)

	if fellBack {
		t.Fatal("a reach is not a name list and cannot miss")
	}
	if !hasScopeName(out, "search_support_bundles") {
		t.Error("a read tool should survive a read-only step")
	}
	for _, n := range []string{"fetch_url", "run_shell"} {
		if hasScopeName(out, n) {
			t.Errorf("%q reaches past reading and should be gone", n)
		}
	}
	if !hasScopeName(out, "change_phase") {
		t.Error("the control plane survives a reach, the same as it survives a name list")
	}
	if len(dropped) != 2 {
		t.Errorf("both should be reported as dropped, got %v", dropped)
	}
}

// Reach "none" is the explicit nothing, and it keeps what a grant gave:
// the workflow controls, and the agent's attachments.
func TestReachNoneKeepsOnlyTheGrants(t *testing.T) {
	catalog := append(scopeCatalog(), AgentToolDef{Tool: Tool{Name: "search_support_bundles"}})
	attached := map[string]bool{"search_support_bundles": true}

	m := turnMachine{on: true, phase: MachinePhase{Name: "triage", Reach: ReachNone}}
	out, _, unmatched, fellBack := m.narrowCatalog(catalog, attached)

	if fellBack || len(unmatched) != 0 {
		t.Fatalf("an explicit nothing is not a miss (fellBack=%v unmatched=%v)", fellBack, unmatched)
	}
	if hasScopeName(out, "web_search") || hasScopeName(out, "atlassian_search") {
		t.Errorf("nothing means nothing from the worker pool: %v", scopeNames(out))
	}
	if !hasScopeName(out, "change_phase") {
		t.Error("the step still has to be able to end the turn and move on")
	}
}

// The legacy marker predates the field and said exactly what reach "none"
// says. A machine saved before the field existed must keep meaning it.
func TestTheOldMarkerReadsAsReachNone(t *testing.T) {
	if got := PhaseReach(MachinePhase{Tools: []string{NoToolsMarker}}); got != ReachNone {
		t.Errorf("a stored %q should read as reach none, got %q", NoToolsMarker, got)
	}
	if got := PhaseReach(MachinePhase{}); got != ReachAll {
		t.Errorf("an untouched step inherits everything, got %q", got)
	}
}

// Two controls that argue with each other: a step whose reach is read-only
// naming a tool that writes. It has to say WHICH control won, or the author
// reads it as the tool having gone missing and goes looking for it.
func TestANameTheReachDroppedSaysSo(t *testing.T) {
	catalog := []AgentToolDef{
		{Tool: Tool{Name: "search_support_bundles", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork}}},
	}
	m := turnMachine{on: true, phase: MachinePhase{
		Name: "gather", Reach: ReachRead, Tools: []string{"search_support_bundles", "fetch_url"}}}
	_, _, unmatched, _ := m.narrowCatalog(catalog, nil)

	if len(unmatched) != 1 || !strings.Contains(unmatched[0], "fetch_url") ||
		!strings.Contains(unmatched[0], "reach") {
		t.Errorf("the report should name the tool AND the control that took it: %v", unmatched)
	}
}
