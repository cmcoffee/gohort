// The two tool controls a stage carries, and the promise that a machine's
// step carries the same two.
package core

import (
	"strings"
	"testing"
)

// A pipeline stage narrows by reach first and by name second — the same two
// controls a machine step carries, deliberately identical, because those are
// the two places an author writes this and one vocabulary is the whole point.
//
// A stage needs the coarse one MORE than a step does: a machine runs for
// whichever agent carries it, but a pipeline is invoked by whichever agent
// ATTACHED it, and several can. "The catalog" is a different set per caller,
// so a name list describes one of them and a capability describes all.
func TestStageReachNarrowsBeforeNames(t *testing.T) {
	inherited := []AgentToolDef{
		{Tool: Tool{Name: "search_prod_logs", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork, CapRead}}},
		{Tool: Tool{Name: "run_shell", Caps: []Capability{CapExecute}}},
	}
	names := func(defs []AgentToolDef) string {
		var out []string
		for _, d := range defs {
			out = append(out, d.Tool.Name)
		}
		return strings.Join(out, ",")
	}

	if got := names(StageTools(PipelineStage{}, inherited)); got != "search_prod_logs,fetch_url,run_shell" {
		t.Errorf("an untouched stage inherits the caller's catalog, got %q", got)
	}
	if got := names(StageTools(PipelineStage{Reach: ReachRead}, inherited)); got != "search_prod_logs" {
		t.Errorf("read-only should keep only what reads, got %q", got)
	}
	if got := StageTools(PipelineStage{Reach: ReachNone}, inherited); got != nil {
		t.Errorf("nothing means nothing, got %v", names(got))
	}
	// Names apply on top of the reach, not instead of it: a stage that is
	// read-only cannot name its way back to the network.
	if got := names(StageTools(PipelineStage{Reach: ReachRead,
		Tools: []string{"search_prod_logs", "fetch_url"}}, inherited)); got != "search_prod_logs" {
		t.Errorf("a name must not widen past the reach, got %q", got)
	}
	// And the marker a stage could already store keeps meaning what it meant.
	if got := StageReach(PipelineStage{Tools: []string{NoToolsMarker}}); got != ReachNone {
		t.Errorf("the legacy marker should read as reach none, got %q", got)
	}
}

// The two surfaces must not drift: same values, same resolution, same
// back-compat. A phase and a stage handed the same setting and the same
// catalog have to answer identically, or "an author who learned one has
// learned the other" stops being true.
func TestAStageAndAStepAgreeOnWhatAReachMeans(t *testing.T) {
	catalog := []AgentToolDef{
		{Tool: Tool{Name: "read_file", Caps: []Capability{CapRead}}},
		{Tool: Tool{Name: "fetch_url", Caps: []Capability{CapNetwork}}},
	}
	for _, reach := range []string{ReachAll, ReachRead, ReachNone} {
		stage := StageTools(PipelineStage{Reach: reach, Tools: []string{"read_file"}}, catalog)
		phase := PhaseTools(MachinePhase{Reach: reach, Tools: []string{"read_file"}}, catalog)
		if len(stage) != len(phase) {
			t.Errorf("reach %q: stage gave %d tools, step gave %d", reach, len(stage), len(phase))
		}
	}
}
