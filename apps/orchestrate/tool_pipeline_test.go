package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func savedTestPipeline(udb Database, name string, global bool) PipelineDef {
	return SavePipelineDef(udb, PipelineDef{Owner: "alice", Name: name, Global: global,
		Stages: []PipelineStage{{Name: "write", Kind: StageWorker, Prompt: "write"}}})
}

// A pipeline taken into a tool is removed only when nothing else uses it: an
// agent that runs it, or being global, keeps it.
func TestAPipelineFoldsIntoAToolOnlyWhenNothingElseUsesIt(t *testing.T) {
	_, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	alone := savedTestPipeline(udb, "song maker", false)
	def, fold, err := pipelineForTool(udb, user, "Song Maker")
	if err != nil || def.ID != alone.ID {
		t.Fatalf("resolve by name: %+v %v", def, err)
	}
	if note := fold(); !strings.Contains(note, "removed") {
		t.Errorf("an unused pipeline should be folded in: %s", note)
	}
	if _, ok := LoadPipelineDef(udb, user, alone.ID); ok {
		t.Error("the folded pipeline is still saved")
	}

	attached := savedTestPipeline(udb, "news digest", false)
	if _, err := saveAgent(udb, AgentRecord{Name: "Editor", Owner: user, OrchestratorPrompt: "You edit.", AttachedPipelines: []string{attached.ID}}); err != nil {
		t.Fatal(err)
	}
	global := savedTestPipeline(udb, "everywhere", true)
	for _, def := range []PipelineDef{attached, global} {
		_, fold, err := pipelineForTool(udb, user, def.ID)
		if err != nil {
			t.Fatalf("resolve %q: %v", def.Name, err)
		}
		if note := fold(); !strings.Contains(note, "stays") {
			t.Errorf("%q is used elsewhere and should stay: %s", def.Name, note)
		}
		if _, ok := LoadPipelineDef(udb, user, def.ID); !ok {
			t.Errorf("%q was removed although something uses it", def.Name)
		}
	}
	if _, _, err := pipelineForTool(udb, user, "nope"); err == nil || !strings.Contains(err.Error(), "news digest") {
		t.Errorf("an unknown name should list what exists: %v", err)
	}
}
