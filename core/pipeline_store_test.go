package core

import (
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// An app's pipeline is the app's: what it is changes only by Builder or
// Anvil, and the plain save, which every path takes, refuses anything else,
// keeping the stored version. Who it is shared with is not what it is, and a
// pipeline of no app is not held. An import goes through.
func TestAnAppsPipelineChangesOnlyThroughBuilderOrAnvil(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	stage := []PipelineStage{{Name: "draft", Kind: StageWorker, Prompt: "Draft it."}}
	app, err := SavePipelineDefBy(udb, PipelineDef{Owner: "u", Name: "Brief", OwningApp: "news", Stages: stage}, "create", "Builder")
	if err != nil {
		t.Fatalf("Builder making an app's pipeline: %v", err)
	}
	edit := app
	edit.Stages = []PipelineStage{{Name: "draft", Kind: StageWorker, Prompt: "Draft it, and add rumours."}}
	if _, err := SavePipelineDefBy(udb, edit, "update", ""); err == nil || !strings.Contains(err.Error(), "change it with Builder or Anvil") {
		t.Errorf("an app's pipeline changed outside Builder and Anvil: %v", err)
	}
	if got := SavePipelineDef(udb, edit); got.Stages[0].Prompt != "Draft it." {
		t.Errorf("the plain save changed an app's pipeline: %q", got.Stages[0].Prompt)
	}
	if got, _ := LoadPipelineDef(udb, "u", app.ID); got.Stages[0].Prompt != "Draft it." {
		t.Errorf("stored: %q", got.Stages[0].Prompt)
	}
	share := app
	share.AllowedUsers = []string{"sam"}
	if _, err := SavePipelineDefBy(udb, share, "update", ""); err != nil {
		t.Errorf("sharing an app's pipeline: %v", err)
	}
	if _, err := SavePipelineDefBy(udb, edit, "update", "Anvil"); err != nil {
		t.Errorf("Anvil changing an app's pipeline: %v", err)
	}
	mine := SavePipelineDef(udb, PipelineDef{Owner: "u", Name: "Mine", Stages: stage})
	mine.Stages = edit.Stages
	if _, err := SavePipelineDefBy(udb, mine, "update", ""); err != nil {
		t.Errorf("a pipeline of no app was held: %v", err)
	}
	if _, err := ImportPipeline(udb, "u", PipelineDef{Name: "Imported", OwningApp: "news", Stages: stage}); err != nil {
		t.Errorf("an import of an app's pipeline: %v", err)
	}
}
