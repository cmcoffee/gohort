package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/peershare"
)

// A deleted account's agents move with their sub-agents and their sharing;
// purge deletes what was built. Seeds and their shadows are not the
// account's to give away.
func TestADeletedAccountsAgentsMoveOrGo(t *testing.T) {
	root := depStores(t, "bob", "alice", "carol")
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	h := &orchUserData{app: app}
	bob := UserDB(root, "bob")
	parent, err := saveAgent(bob, AgentRecord{Owner: "bob", Name: "Lead", OrchestratorPrompt: "lead", AllowedUsers: []string{"carol"}})
	if err != nil {
		t.Fatal(err)
	}
	child, err := saveAgent(bob, AgentRecord{Owner: "bob", Name: "Helper", OrchestratorPrompt: "help", OwnedBy: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	pipe := SavePipelineDefAs(bob, PipelineDef{Owner: "bob", Name: "Flow"}, "create")

	if s := h.Describe("bob"); s.Counts["agents"] != 2 || s.Counts["pipelines"] != 1 || s.Counts["shared"] != 1 {
		t.Fatalf("describe: %+v", s.Counts)
	}
	if err := h.Reassign("bob", "alice"); err != nil {
		t.Fatal(err)
	}
	alice := UserDB(root, "alice")
	for _, id := range []string{parent.ID, child.ID} {
		a, ok := loadAgent(alice, id)
		if !ok || a.Owner != "alice" {
			t.Errorf("agent %s did not arrive as alice's: %+v", id, a)
		}
		if _, still := loadAgent(bob, id); still {
			t.Errorf("agent %s is still in bob's store", id)
		}
	}
	if got, _ := loadAgent(alice, child.ID); got.OwnedBy != parent.ID {
		t.Error("the sub-agent lost its parent in the move")
	}
	if refs := peershare.List(RootDB, SharedAgentsTable, "carol"); len(refs) != 1 || refs[0].Owner != "alice" {
		t.Errorf("carol's share did not follow the agent: %+v", refs)
	}
	if d, ok := LoadPipelineDef(alice, "alice", pipe.ID); !ok || d.Owner != "alice" {
		t.Error("the pipeline did not move")
	}

	// Purge: what alice now has goes.
	if err := h.Purge("alice"); err != nil {
		t.Fatal(err)
	}
	if n := len(ownedAgents(alice, "alice")); n != 0 {
		t.Errorf("%d agent(s) survived the purge", n)
	}
	if len(ListPipelineDefs(alice, "alice")) != 0 {
		t.Error("the pipeline survived the purge")
	}
}

// An agent or pipeline shared WITH a deleted account is taken back from it,
// in the record and in the index, so a reused name inherits nothing.
func TestADeletedAccountLosesWhatWasSharedWithIt(t *testing.T) {
	root := depStores(t, "alice", "bob", "carol")
	app := &OrchestrateApp{AppCore: AppCore{DB: root}}
	alice := UserDB(root, "alice")
	a, err := saveAgent(alice, AgentRecord{Owner: "alice", Name: "Lead", OrchestratorPrompt: "lead", AllowedUsers: []string{"bob", "carol"}})
	if err != nil {
		t.Fatal(err)
	}
	pipe := SavePipelineDefAs(alice, PipelineDef{Owner: "alice", Name: "Flow", AllowedUsers: []string{"bob"}}, "create")

	(&orchUserData{app: app}).forgetRecipient("bob")

	got, _ := loadAgent(alice, a.ID)
	if len(got.AllowedUsers) != 1 || got.AllowedUsers[0] != "carol" {
		t.Errorf("agent share list: %v", got.AllowedUsers)
	}
	if refs := peershare.List(RootDB, SharedAgentsTable, "bob"); len(refs) != 0 {
		t.Errorf("bob is still in the agents index: %+v", refs)
	}
	if refs := peershare.List(RootDB, SharedAgentsTable, "carol"); len(refs) != 1 {
		t.Error("carol's share went with bob's")
	}
	if d, _ := LoadPipelineDef(alice, "alice", pipe.ID); len(d.AllowedUsers) != 0 {
		t.Errorf("pipeline still shared with %v", d.AllowedUsers)
	}
}
