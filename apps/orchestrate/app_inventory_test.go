package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
	line := appInventoryLine(spec)
	if !strings.Contains(line, "sections: pipeline") {
		t.Errorf("name the sections that exist, got:\n%s", line)
	}
	// The most over-claimed thing in three transcripts running.
	if !strings.Contains(line, "NO action buttons") {
		t.Errorf("an app with no actions must say so in capitals — a save button was described on exactly this shape:\n%s", line)
	}
	if !strings.Contains(line, "pipeline: forge_pipeline") {
		t.Errorf("bindings are part of the truth, got:\n%s", line)
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
	line := appInventoryLine(spec)
	if !strings.Contains(line, "table×2") {
		t.Errorf("repeated sections carry a count, got:\n%s", line)
	}
	if !strings.Contains(line, `"Save to History"`) {
		t.Errorf("name the buttons by their LABEL — that is what the user is told exists, got:\n%s", line)
	}
	if !strings.Contains(line, "1 data source(s)") || !strings.Contains(line, "agent: judge") {
		t.Errorf("data sources and the agent binding belong in the inventory, got:\n%s", line)
	}
}

// An app with nothing in it says nothing is in it, rather than rendering an
// empty list that reads as "fine".
func TestInventoryIsBluntAboutAnEmptyApp(t *testing.T) {
	line := appInventoryLine(AppSpec{Name: "Empty"})
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
