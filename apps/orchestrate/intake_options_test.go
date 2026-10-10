package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// "Which one?" lists the user's own things of the kind picked a step earlier,
// so a Change or Fix arrives with its target named. Hidden fleet members are
// not offered, and a pipeline-mode tool is listed under pipelines, where a
// person looks for it.
func TestTheIntakePickerListsTheUsersOwnThings(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	if _, err := saveAgent(db, AgentRecord{ID: "a-wren", Name: "Wren", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(db, AgentRecord{ID: "seed-builder", Name: "Builder", Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []TempTool{{Name: "make_song", ScriptBody: "print(1)"}, {Name: "song_flow", Mode: TempToolModePipeline}, {Name: "alpha_tool", ScriptBody: "x"}} {
		if err := AdminPersistTempTool(db, "alice", tt); err != nil {
			t.Fatal(err)
		}
	}
	SavePipelineDef(db, PipelineDef{ID: "p1", Name: "daily_digest", Owner: "alice"})

	agents := map[string]string{}
	for _, a := range authoringTargets(db, "alice", "Agent") {
		agents[a.Label] = a.Value
	}
	if agents["Wren"] != "Wren (id: a-wren)" {
		t.Errorf("an agent carries its id, since two can share a name: %+v", agents)
	}
	if _, offered := agents["Builder"]; offered {
		t.Error("Builder improves other agents; it is not offered as a target")
	}
	tools := authoringTargets(db, "alice", "tool")
	if len(tools) != 2 || tools[0].Value != "alpha_tool" || tools[1].Value != "make_song" {
		t.Errorf("tools, sorted, without the pipeline-mode one: %+v", tools)
	}
	pipes := authoringTargets(db, "alice", "Pipeline")
	names := map[string]bool{}
	for _, p := range pipes {
		names[p.Value] = true
	}
	if !names["daily_digest"] || !names["song_flow"] {
		t.Errorf("pipelines include both kinds: %+v", pipes)
	}
	if got := authoringTargets(db, "alice", "spaceship"); len(got) != 0 {
		t.Errorf("an unknown kind lists nothing: %+v", got)
	}
	if got := authoringTargets(db, "bob", "tool"); len(got) != 0 {
		t.Errorf("another user's tools are not offered: %+v", got)
	}
}
