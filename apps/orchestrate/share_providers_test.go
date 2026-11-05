package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/shareledger"
)

// Agents, pipelines and machines shared with everyone are on the
// administrator's one list of what reaches the whole deployment, with every
// other kind, and Take back there turns Everyone off and keeps the people and
// teams the owner named.
func TestEveryoneSharesOfAgentsPipelinesAndMachinesAreListedAndTakenBack(t *testing.T) {
	udb := reachFixture(t)
	registerShareProviders()
	AuthDB().Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	saveAgent(udb, AgentRecord{ID: "a1", Name: "Triage", Owner: "alice", OrchestratorPrompt: "help",
		Everyone: true, AllowedUsers: []string{"bob"}})
	saveAgent(udb, AgentRecord{ID: "a2", Name: "Private", Owner: "alice", OrchestratorPrompt: "help"})
	SavePipelineDef(udb, PipelineDef{ID: "p1", Name: "Nightly", Owner: "alice", Published: true, AllowedUsers: []string{"bob"}})
	SavePipelineDef(udb, PipelineDef{ID: "p2", Name: "Draft", Owner: "alice"})
	SaveMachineDef(udb, MachineDef{ID: "m1", Name: "Intake", Owner: "alice", Published: true, AllowedUsers: []string{"bob"}})

	listed := map[string]bool{}
	for _, g := range shareledger.Published() {
		if g.Owner == "alice" {
			listed[g.Kind+":"+g.ID] = g.Revocable
		}
	}
	for _, want := range []string{"agent:a1", "pipeline:p1", "machine:m1"} {
		if take, ok := listed[want]; !ok || !take {
			t.Errorf("%s shared with everyone: listed %v, takeable %v", want, ok, take)
		}
	}
	for _, not := range []string{"agent:a2", "pipeline:p2"} {
		if _, ok := listed[not]; ok {
			t.Errorf("%s is not shared with everyone but is listed", not)
		}
	}

	for _, k := range []struct{ kind, id string }{{"agent", "a1"}, {"pipeline", "p1"}, {"machine", "m1"}} {
		if err := shareledger.Unpublish(k.kind, "alice", k.id); err != nil {
			t.Fatalf("take back %s: %v", k.kind, err)
		}
	}
	if a, _ := loadAgent(udb, "a1"); a.Everyone || len(a.AllowedUsers) != 1 {
		t.Errorf("agent after take back: everyone %v, named %v", a.Everyone, a.AllowedUsers)
	}
	if d, _ := LoadPipelineDef(udb, "alice", "p1"); d.Published || len(d.AllowedUsers) != 1 {
		t.Errorf("pipeline after take back: published %v, named %v", d.Published, d.AllowedUsers)
	}
	if d, _ := LoadMachineDef(udb, "alice", "m1"); d.Published || len(d.AllowedUsers) != 1 {
		t.Errorf("machine after take back: published %v, named %v", d.Published, d.AllowedUsers)
	}
	for _, g := range shareledger.Published() {
		if g.Owner == "alice" {
			t.Errorf("still listed after take back: %s %s", g.Kind, g.ID)
		}
	}
}
