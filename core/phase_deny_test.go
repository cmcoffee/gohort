package core

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/toolrules"
)

func denyCatalog() []AgentToolDef {
	return []AgentToolDef{
		{Tool: Tool{Name: "web_search", Caps: []Capability{CapNetwork, CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork, CapRead}}},
		{Tool: Tool{Name: "knowledge_search", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "write_note", Caps: []Capability{CapWrite}}},
	}
}

func names(tools []AgentToolDef) []string {
	out := make([]string, 0, len(tools))
	for _, td := range tools {
		out = append(out, td.Tool.Name)
	}
	return out
}

// The list you want when a step keeps everything EXCEPT one thing. The allow
// list can only express that by enumerating the catalog minus one, which
// freezes the step at the catalog of the day it was written.
func TestDenyKeepsEverythingElse(t *testing.T) {
	got := names(PhaseTools(MachinePhase{Deny: []string{"web_search"}}, denyCatalog()))
	if len(got) != 3 {
		t.Fatalf("expected the catalog minus one, got %v", got)
	}
	for _, n := range got {
		if n == "web_search" {
			t.Fatal("the denied tool survived")
		}
	}
	// And a tool added to the agent later is present without touching the step.
	grown := append(denyCatalog(), AgentToolDef{Tool: Tool{Name: "post_update"}})
	if got := names(PhaseTools(MachinePhase{Deny: []string{"web_search"}}, grown)); len(got) != 4 {
		t.Errorf("a deny must not freeze the step's catalog, got %v", got)
	}
}

// Deny is the last word: after the reach, after the allow list, whatever those
// two permitted.
func TestDenySubtractsAfterReachAndAllowList(t *testing.T) {
	// Named by the allow list AND denied: the deny wins.
	got := names(PhaseTools(MachinePhase{
		Tools: []string{"web_search", "knowledge_search"},
		Deny:  []string{"web_search"},
	}, denyCatalog()))
	if len(got) != 1 || got[0] != "knowledge_search" {
		t.Errorf("deny must outrank the allow list, got %v", got)
	}
	// Reach already dropped the network tools; the deny is simply satisfied.
	got = names(PhaseTools(MachinePhase{
		Reach: ReachRead,
		Deny:  []string{"web_search"},
	}, denyCatalog()))
	for _, n := range got {
		if n == "web_search" || n == "fetch_url" || n == "write_note" {
			t.Errorf("read reach should have dropped %s already: %v", n, got)
		}
	}
	if len(got) != 1 || got[0] != "knowledge_search" {
		t.Errorf("expected only the read tool, got %v", got)
	}
}

// A deny naming a tool this deployment does not have is the NORMAL case for a
// machine carried between deployments. It is satisfied, not suspicious — and
// above all it must not be read as a reason to widen anything.
func TestADenyThatMatchesNothingIsSatisfied(t *testing.T) {
	got := names(PhaseTools(MachinePhase{Deny: []string{"tool_from_another_deployment"}}, denyCatalog()))
	if len(got) != 4 {
		t.Errorf("a deny that matches nothing changes nothing, got %v", got)
	}
}

// Denying everything is a legible instruction, not a mistake to be undone.
func TestDenyingEverythingLeavesNothing(t *testing.T) {
	got := PhaseTools(MachinePhase{Deny: []string{"web_search", "fetch_url", "knowledge_search", "write_note"}}, denyCatalog())
	if len(got) != 0 {
		t.Errorf("expected an empty result, got %v", names(got))
	}
}

// The step's block has to NAME what it withholds. A tool the model used all
// conversation and now cannot see reads as a fault to work around — by
// retrying it, by reaching for a neighbour, or by authoring a replacement.
func TestThePhaseBlockNamesWhatItWithholds(t *testing.T) {
	ph := MachinePhase{Name: "investigate", Prompt: "look into it", Deny: []string{"web_search"}}
	block := MachineDef{Phases: []MachinePhase{ph}}.PhaseBlock(ph, MachineState{}, PhaseVars{})
	if !strings.Contains(block, "web_search") {
		t.Errorf("the block must name the withheld tool:\n%s", block)
	}
	if !strings.Contains(block, "do not substitute") && !strings.Contains(block, "not substitute") {
		t.Errorf("the block must forbid the workaround, not just the tool:\n%s", block)
	}
}

// A step that cannot change_phase is stranded, not restricted — and "deny
// everything risky" is exactly how an author arrives at that by accident.
// Refused at authoring time rather than silently ignored, because someone who
// wrote it believes it took effect.
func TestAuthoringRefusesDenyingTheWayOut(t *testing.T) {
	toolrules.RegisterWorkflowControlTool("change_phase")

	def := MachineDef{
		Name: "investigation",
		Phases: []MachinePhase{
			{Name: "look", Prompt: "look into it", Deny: []string{"change_phase"}, Next: "done"},
			{Name: "done", Prompt: "report", Resident: true},
		},
	}
	probs := def.Problems()
	var found bool
	for _, p := range probs {
		if strings.Contains(p, "change_phase") && strings.Contains(p, "stranded") {
			found = true
		}
	}
	if !found {
		t.Errorf("denying the workflow control must be refused, got %v", probs)
	}
}

// Naming a tool in both lists is a contradiction the author should see: the
// deny wins, so the allow entry does nothing.
func TestAuthoringFlagsAToolInBothLists(t *testing.T) {
	def := MachineDef{
		Name: "investigation",
		Phases: []MachinePhase{
			{Name: "look", Prompt: "look", Tools: []string{"web_search"}, Deny: []string{"web_search"}, Next: "done"},
			{Name: "done", Prompt: "report", Resident: true},
		},
	}
	var found bool
	for _, p := range def.Problems() {
		if strings.Contains(p, "both tools and deny") {
			found = true
		}
	}
	if !found {
		t.Errorf("a tool in both lists should be flagged, got %v", def.Problems())
	}
}
