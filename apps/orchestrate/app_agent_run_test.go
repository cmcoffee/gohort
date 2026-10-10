package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app's backend runs its own agents and pipelines: the app's agent_id /
// pipeline_id, or one made for it (owning_app), and nothing else its owner
// has. An app having a brain is a smaller promise than its scripts driving
// every agent in the account.
func TestAnAppRunsOnlyItsOwnAgentsAndPipelines(t *testing.T) {
	T := &OrchestrateApp{}
	T.DB = &DBase{Store: kvlite.MemStore()}
	udb := UserDB(T.DB, "alice")
	mustSaveAgent(t, udb, AgentRecord{ID: "dm", Name: "Voidrunner DM", Owner: "alice"})
	mustSaveAgent(t, udb, AgentRecord{ID: "qm", Name: "Quartermaster", Owner: "alice", OwningApp: "voidrunner"})
	mustSaveAgent(t, udb, AgentRecord{ID: "mail", Name: "Mailer", Owner: "alice"})

	if a, err := T.appOwnAgent("alice", "voidrunner", "Voidrunner DM", ""); err != nil || a.ID != "dm" {
		t.Errorf("the app's own agent: %+v %v", a, err)
	}
	if a, err := T.appOwnAgent("alice", "voidrunner", "dm", "Quartermaster"); err != nil || a.ID != "qm" {
		t.Errorf("an agent made for the app: %+v %v", a, err)
	}
	if _, err := T.appOwnAgent("alice", "voidrunner", "dm", "Mailer"); err == nil || !strings.Contains(err.Error(), "not one of this app's agents") {
		t.Errorf("another of the owner's agents was reachable: %v", err)
	}
	if _, err := T.appOwnAgent("alice", "voidrunner", "", ""); err == nil || !strings.Contains(err.Error(), "no agent to run") {
		t.Errorf("no agent_id and none named: %v", err)
	}

	SavePipelineDef(udb, PipelineDef{ID: "p1", Owner: "alice", Name: "turn", Stages: []PipelineStage{{Name: "a", Kind: StageWorker}}})
	SavePipelineDef(udb, PipelineDef{ID: "p2", Owner: "alice", Name: "loot", OwningApp: "voidrunner", Stages: []PipelineStage{{Name: "a", Kind: StageWorker}}})
	SavePipelineDef(udb, PipelineDef{ID: "p3", Owner: "alice", Name: "research", Stages: []PipelineStage{{Name: "a", Kind: StageWorker}}})
	if d, err := T.appOwnPipeline("alice", "voidrunner", "p1", ""); err != nil || d.ID != "p1" {
		t.Errorf("the app's own pipeline: %+v %v", d, err)
	}
	if d, err := T.appOwnPipeline("alice", "voidrunner", "p1", "loot"); err != nil || d.ID != "p2" {
		t.Errorf("a pipeline made for the app: %+v %v", d, err)
	}
	if _, err := T.appOwnPipeline("alice", "voidrunner", "p1", "research"); err == nil || !strings.Contains(err.Error(), "not one of this app's pipelines") {
		t.Errorf("another of the owner's pipelines was reachable: %v", err)
	}
}

// A save says when a script runs an agent or a pipeline it has not declared,
// a call made inside a library it imports included, or when the app has none
// to run.
func TestSaveNotesUndeclaredRunAgent(t *testing.T) {
	spec := AppSpec{Slug: "game", AgentID: "dm",
		Libraries: map[string]string{"engine": "from oddjob import run_agent\ndef turn(s):\n    return run_agent(s)\n"},
		DataSources: []AppDataSource{
			{Name: "direct", Script: "from oddjob import run_agent\nprint(run_agent('x'))", Capabilities: []string{"fetch"}},
			{Name: "via-lib", Script: "from engine import turn\nprint(turn('x'))"},
			{Name: "fine", Script: "from engine import turn\nprint(turn('x'))", Capabilities: []string{"run_agent"}},
			{Name: "pipe", Script: "from oddjob import run_pipeline\nprint(run_pipeline('x'))", Capabilities: []string{"run_pipeline"}},
		}}
	notes := strings.Join(appToolCapNotes("u", spec), "\n")
	for _, want := range []string{
		`data source "direct" calls run_agent(...) but does not declare "run_agent"`,
		`data source "via-lib" calls run_agent(...) but does not declare "run_agent"`,
		`data source "pipe" calls run_pipeline(...), and the app has no pipeline`,
	} {
		if !strings.Contains(notes, want) {
			t.Errorf("missing note %q in:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, `"fine"`) {
		t.Errorf("a declared call was noted:\n%s", notes)
	}
}
