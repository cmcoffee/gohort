package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A machine step, and a pipeline run inline from a turn, reach the owner's
// own tools. Both were built from resolveWorkerTools, which holds the
// registered tools only, so a pipeline Builder built over the owner's "geo"
// failed every try with "tool geo is not available to this pipeline" while
// geo sat in Builder's own catalog.
func TestSubRunsReachTheOwnersTools(t *testing.T) {
	turn, _ := machineTurnFixture(t, residentMachine())
	if err := AdminPersistTempTool(turn.udb, "u", TempTool{Name: "geo", Description: "Geocode a place.", CommandTemplate: "echo geo"}); err != nil {
		t.Fatal(err)
	}
	has := func(pool []AgentToolDef) bool {
		for _, td := range pool {
			if td.Tool.Name == "geo" {
				return true
			}
		}
		return false
	}

	if !has(turn.machineCatalog(MachinePhase{Name: "locate", Tools: []string{"geo"}})) {
		t.Fatal("a machine step naming the owner's tool could not reach it")
	}

	pool, _, err := turn.resolveWorkerTools(turn.newToolSession(), false)
	if err != nil {
		t.Fatal(err)
	}
	if has(pool) {
		t.Fatal("resolveWorkerTools now carries custom tools; withOwnTools may add them twice")
	}
	sess := turn.newToolSession()
	pool = turn.withOwnTools(sess, pool)
	if !has(pool) {
		t.Fatal("withOwnTools did not add the owner's tool")
	}
	n := 0
	for _, td := range turn.withOwnTools(sess, pool) {
		if td.Tool.Name == "geo" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("geo appears %d times after a second pass", n)
	}

	// Through the pipeline's own run: the tool stage resolves geo.
	def := PipelineDef{Name: "place-forecast", Stages: []PipelineStage{{Name: "locate", Kind: StageTool, Tool: "geo"}}}
	out, err := turn.runPipelineDefInline(def, "Paris")
	if err != nil && strings.Contains(err.Error(), "not available to this pipeline") {
		t.Fatalf("the inline pipeline could not reach the owner's tool: %v", err)
	}
	_ = out
}
