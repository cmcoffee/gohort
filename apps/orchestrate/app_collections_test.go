package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app's collection is reached only by the app's own parts: its agents
// read it and add to it, an agent outside the app neither sees it nor adds
// to it, only Builder changes what it is, and the app's scripts add to it
// with knowledge_add, which reaches no collection but the app's own.
func TestOnlyAnAppsOwnPartsReachItsCollection(t *testing.T) {
	turn, udb := copyTestTurn(t)
	savedVec := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = savedVec; InvalidateChunkCache() })
	copyTestApp(t, turn, nil)
	SaveCollectionBy(udb, Collection{ID: "app-c", Owner: "u", Name: "Manual", OwningApp: "desk"}, "Builder")
	SaveCollectionBy(udb, Collection{ID: "mine", Owner: "u", Name: "Notes"}, "")
	clerk, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Clerk", OrchestratorPrompt: "Help.", OwningApp: "desk", appEdit: "Builder"})
	outsider, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Helper", OrchestratorPrompt: "Help."})

	if got := agentReadsCollections(clerk, []string{"app-c", "mine"}); len(got) != 2 {
		t.Errorf("the app's agent reads %v", got)
	}
	if got := agentReadsCollections(outsider, []string{"app-c", "mine"}); len(got) != 1 || got[0] != "mine" {
		t.Errorf("an agent outside the app reads %v", got)
	}

	tool := collectionsListTool().(*GroupedTool)
	sessOf := func(a AgentRecord) *ToolSession { return &ToolSession{Username: "u", DB: udb, AgentID: a.ID} }
	text := "The widget turns on with the red switch; hold it two seconds until the light shows green."
	add := map[string]any{"action": "add_text", "id": "app-c", "title": "Start", "text": text}
	if _, err := tool.RunWithSession(add, sessOf(outsider)); err == nil || !strings.Contains(err.Error(), "only that app's own agents") {
		t.Errorf("an agent outside the app added to its collection: %v", err)
	}
	if out, _ := tool.RunWithSession(map[string]any{"action": "list"}, sessOf(outsider)); strings.Contains(out, "app-c") {
		t.Errorf("an agent outside the app listed its collection: %s", out)
	}
	if _, err := tool.RunWithSession(add, sessOf(clerk)); err != nil {
		t.Errorf("the app's own agent could not add to its collection: %v", err)
	}
	rename := map[string]any{"action": "update", "id": "app-c", "name": "Handbook"}
	if _, err := tool.RunWithSession(rename, sessOf(clerk)); err == nil || !strings.Contains(err.Error(), "change with Builder") {
		t.Errorf("the app's agent renamed the app's collection: %v", err)
	}

	spec, _ := LoadAppSpec("u", "desk")
	out, err := appKnowledgeAdd(context.Background(), spec, "u", "Manual", "Care", "Wipe the widget with a dry cloth; never put it in water, which ruins the switch.")
	if err != nil || !strings.Contains(out, "Added") {
		t.Fatalf("a script adding to its app's collection: %v %s", err, out)
	}
	if out, _ := appKnowledgeAdd(context.Background(), spec, "u", "Manual", "Care", "Wipe the widget with a soft dry cloth; never put it in water, which ruins the switch."); !strings.Contains(out, "Replaced") {
		t.Errorf("adding the same title again: %s", out)
	}
	if _, err := appKnowledgeAdd(context.Background(), spec, "u", "Notes", "x", "A note that is long enough to be indexed as a document here."); err == nil {
		t.Error("a script added to a collection that is not its app's")
	}
	if seeds := collectionSeeds(Collection{ID: "app-c"}); len(seeds) != 0 {
		t.Errorf("what the agent and the script added were taken for seed files: %v", seeds)
	}
}
