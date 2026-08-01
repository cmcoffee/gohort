package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The three summaries that promised what was not there — a history table
// nothing wrote to, runs "saved" to a store never touched, and a save button on
// an app with no actions. Every check passed on all three; the page was fine.
// The gap was between the stored spec and the sentence describing it, so the
// spec now writes that sentence's source material.
func TestInventoryNamesWhatIsAndIsNotThere(t *testing.T) {
	spec := AppSpec{
		Name: "Forge", PipelineID: "forge_pipeline",
		Sections: json.RawMessage(`[{"kind":"pipeline"}]`),
	}
	line := (&chatTurn{}).appInventoryLine(spec)
	if !strings.Contains(line, "sections: pipeline") {
		t.Errorf("name the sections that exist, got:\n%s", line)
	}
	// The most over-claimed thing in three transcripts running.
	if !strings.Contains(line, "NO action buttons") {
		t.Errorf("an app with no actions must say so in capitals — a save button was described on exactly this shape:\n%s", line)
	}
	// The binding cannot resolve without a store, and an unresolvable one has
	// to say so rather than print a bare id that reads as fine.
	if !strings.Contains(line, "DOES NOT RESOLVE") {
		t.Errorf("a binding that resolves to nothing must say so, got:\n%s", line)
	}
	if !strings.Contains(line, "SAY it is missing") {
		t.Errorf("the line has to say what to do when the ask is not in it, got:\n%s", line)
	}
}

func TestInventoryListsButtonsAndRepeatedSections(t *testing.T) {
	spec := AppSpec{
		Name: "Crossfire", AgentID: "judge",
		Sections:    json.RawMessage(`[{"kind":"form"},{"kind":"table"},{"kind":"table"},{"kind":"pipeline"}]`),
		Actions:     []AppAction{{Name: "save_debate", Label: "Save to History"}},
		DataSources: []AppDataSource{{Name: "rows"}},
	}
	line := (&chatTurn{}).appInventoryLine(spec)
	if !strings.Contains(line, "table×2") {
		t.Errorf("repeated sections carry a count, got:\n%s", line)
	}
	if !strings.Contains(line, `"Save to History"`) {
		t.Errorf("name the buttons by their LABEL — that is what the user is told exists, got:\n%s", line)
	}
	if !strings.Contains(line, "1 data source(s)") || !strings.Contains(line, "agent: judge") { //nolint
		t.Errorf("data sources and the agent binding belong in the inventory, got:\n%s", line)
	}
}

// An app with nothing in it says nothing is in it, rather than rendering an
// empty list that reads as "fine".
func TestInventoryIsBluntAboutAnEmptyApp(t *testing.T) {
	line := (&chatTurn{}).appInventoryLine(AppSpec{Name: "Empty"})
	if !strings.Contains(line, "NO sections") || !strings.Contains(line, "NO action buttons") {
		t.Errorf("an empty app must read as empty, got:\n%s", line)
	}
}

// The prompt has to point at the line, or it is just more text in a tool result.
func TestBuilderPromptDefersToTheStoredInventory(t *testing.T) {
	seed, _ := seedAgentByID("seed-builder")
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "STORED —") {
		t.Error("the prompt never names the inventory line, so nothing connects the rule to the evidence")
	}
	if !strings.Contains(p, "may not go past it") {
		t.Error("the rule needs a hard edge: the summary is bounded by the line")
	}
}

// "Up to five passes" is a claim about the PIPELINE, and the inventory used to
// print its id — a bare UUID, checkable against nothing. A one-stage tool
// pipeline shipped under exactly that description: the stage list said one
// thing, the wrapped tool's description promised five rounds, and the summary
// repeated the description.
func TestInventoryDescribesThePipelineByShape(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	app := &OrchestrateApp{}
	app.DB = root
	udb := UserDB(root, "u")
	saved := SavePipelineDef(udb, PipelineDef{
		Name: "forge", Owner: "u",
		Stages: []PipelineStage{
			{Name: "seed", Kind: StageWorker, Prompt: "x"},
			{Name: "rounds", Kind: StageLoop, Count: 5, Body: []PipelineStage{
				{Name: "critic", Kind: StageWorker, Prompt: "y"},
			}},
		},
	})
	turn := &chatTurn{app: app, user: "u"}
	line := turn.appInventoryLine(AppSpec{Name: "Forge", PipelineID: saved.ID})
	if !strings.Contains(line, `"forge"`) {
		t.Errorf("name the pipeline, not its id: %s", line)
	}
	if !strings.Contains(line, "worker → loop×5") {
		t.Errorf("the stage list is what makes 'five passes' checkable, got:\n%s", line)
	}
	if !strings.Contains(line, "do not describe rounds, passes or steps the stages do not actually perform") {
		t.Errorf("say that the stage list bounds the behavioral claims too, got:\n%s", line)
	}
}
