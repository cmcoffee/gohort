package orchestrate

// Builder's collections tool puts an agent in charge of a collection: one of
// the owner's own, resolved to the id the grant matches on, never a guess.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestTheCollectionsToolNamesACurator(t *testing.T) {
	store := stewardStores(t)
	udb := store("alice")
	saveCollection(udb, Collection{ID: "col-a", Name: "Runbooks", Owner: "alice", AllowedUsers: []string{"carol"}, Contributors: []string{"carol"}})
	cur, err := saveAgent(udb, AgentRecord{Name: "Keeper", Owner: "alice", OrchestratorPrompt: "keeps", OwnedBy: ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"Twin", "Twin"} {
		if _, err := saveAgent(udb, AgentRecord{Name: n, Owner: "alice", OrchestratorPrompt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	gt := collectionsListTool().(*GroupedTool)
	update, _ := gt.Action("update")
	as := func(user string) *ToolSession { return &ToolSession{Username: user, DB: store(user)} }

	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": "Keeper"}, as("alice")); err != nil {
		t.Fatal(err)
	}
	if c, _ := loadCollection(udb, "alice", "col-a"); c.CuratorAgent != cur.ID {
		t.Errorf("the curator should be stored as the agent's id: %q", c.CuratorAgent)
	}
	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": "Twin"}, as("alice")); err == nil {
		t.Error("a name two agents answer to was taken")
	}
	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": "Nobody"}, as("alice")); err == nil || !strings.Contains(err.Error(), "no agent of yours") {
		t.Errorf("an unknown agent should be refused, saying so: %v", err)
	}
	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": "Keeper"}, as("carol")); err == nil {
		t.Error("a contributor put somebody in charge of the owner's collection")
	}
	if c, _ := loadCollection(udb, "alice", "col-a"); c.CuratorAgent != cur.ID {
		t.Error("a refused change changed the curator")
	}
	if _, err := update.Handler(map[string]any{"id": "col-a", "curator": ""}, as("alice")); err != nil {
		t.Fatal(err)
	}
	if c, _ := loadCollection(udb, "alice", "col-a"); c.CuratorAgent != "" {
		t.Errorf("an empty curator should clear it: %q", c.CuratorAgent)
	}
}

// An agent shared with the user is somebody else's: it cannot be put in
// charge of the user's collection.
func TestACuratorIsTheUsersOwnAgent(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Researcher", OrchestratorPrompt: "research",
		AllowedUsers: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	_, err := collectionCuratorFor(&ToolSession{Username: "bob", DB: UserDB(orchestrateBaseDB, "bob")}, "a1")
	if err == nil || !strings.Contains(err.Error(), "one of your own agents") {
		t.Errorf("an agent shared with the user was taken as their curator: %v", err)
	}
}
