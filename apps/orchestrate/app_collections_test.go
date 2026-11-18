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

// How often an app's collection refreshes is its blueprint, kept on it;
// whether it refreshes here is the app's setting, which turns its schedule
// off and on again and never changes the blueprint.
func TestAnAppsCurationSettingLeavesItsBlueprint(t *testing.T) {
	turn, udb := copyTestTurn(t)
	copyTestApp(t, turn, nil)
	PreInitScheduler()
	t.Cleanup(func() {
		for _, task := range ListScheduledTasks(collectionRefreshKind) {
			UnscheduleTask(task.ID)
		}
	})
	id, err := applyCollectionFile(udb, "u", "desk", "Builder", "", collectionFile{Name: "News", Refresh: "week"})
	if err != nil {
		t.Fatal(err)
	}
	scheduled := func() string {
		if p, _, _, ok := refreshScheduled("u", id); ok {
			return p.Every
		}
		return ""
	}
	if scheduled() != "week" {
		t.Fatalf("the blueprint's refresh is not scheduled: %q", scheduled())
	}
	SetAppCurationOff("u", "desk", true)
	c, _ := LoadCollection(udb, "u", id)
	if scheduled() != "" || c.Refresh != "week" || collectionFileOf(udb, "u", c).Refresh != "week" {
		t.Errorf("switched off: scheduled %q, blueprint %q", scheduled(), c.Refresh)
	}
	if err := setCollectionRefresh(udb, c, "month", ""); err != nil {
		t.Fatal(err)
	}
	if c, _ = LoadCollection(udb, "u", id); c.Refresh != "month" || scheduled() != "" {
		t.Errorf("a change of interval while switched off: blueprint %q, scheduled %q", c.Refresh, scheduled())
	}
	SetAppCurationOff("u", "desk", false)
	if scheduled() != "month" {
		t.Errorf("switched back on: scheduled %q", scheduled())
	}
}

// What an app's agents read becomes the app's own: a collection of the
// person's an agent of the app reads is copied into the app (its blueprint
// and its documents, as they are indexed) and the agent reads the copy; the
// original and the person's agent reading it are left as they are, and a
// collection that is not the person's own is not copied.
func TestAnAppCopiesTheCollectionsItsAgentsRead(t *testing.T) {
	turn, udb := copyTestTurn(t)
	savedVec := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = savedVec; InvalidateChunkCache() })
	notes := Collection{ID: "notes", Owner: "u", Name: "Notes", Description: "House notes.", SearchTerms: []string{"widget"}}
	SaveCollectionBy(udb, notes, "")
	if _, err := pasteIntoCollection(context.Background(), VectorDB, notes, "Switch", "The widget turns on with the red switch; hold it for two seconds."); err != nil {
		t.Fatal(err)
	}
	SaveCollectionBy(udb, Collection{ID: "shared", Owner: "colleague", Name: "Theirs"}, "")
	orig, _ := saveAgent(udb, AgentRecord{Owner: "u", Name: "Helper", OrchestratorPrompt: "Help.", AllowedTools: []string{"web_search"}, AttachedCollections: []string{"notes", "shared"}})

	out := copyTestApp(t, turn, map[string]any{"agent_id": orig.ID})
	if !strings.Contains(out, `collection "Notes" (1 documents)`) {
		t.Errorf("the save's reply: %s", out)
	}
	spec, _ := LoadAppSpec("u", "desk")
	cp, _ := loadAgent(udb, spec.AgentID)
	if len(cp.AttachedCollections) != 2 || cp.AttachedCollections[0] == "notes" || cp.AttachedCollections[1] != "shared" {
		t.Fatalf("the app's agent reads %v", cp.AttachedCollections)
	}
	copied, ok := LoadCollection(udb, "u", cp.AttachedCollections[0])
	if !ok || copied.OwningApp != "desk" || copied.CopiedFrom == nil || copied.CopiedFrom.ID != "notes" || copied.Description != "House notes." {
		t.Fatalf("the app's copy: %+v", copied)
	}
	if seeds := collectionSeeds(copied); len(seeds) != 1 || !strings.Contains(seeds["switch.txt"], "red switch") {
		t.Errorf("the copy's documents: %v", seeds)
	}
	if seeds := collectionSeeds(notes); len(seeds) != 1 {
		t.Errorf("the original lost its documents: %v", seeds)
	}
	if got, _ := loadAgent(udb, orig.ID); got.AttachedCollections[0] != "notes" {
		t.Errorf("the person's agent reads %v", got.AttachedCollections)
	}

	// The app's own agent pointed at another of the person's later: the
	// next save copies that one too.
	SaveCollectionBy(udb, Collection{ID: "more", Owner: "u", Name: "More"}, "")
	cp.AttachedCollections, cp.appEdit = append(cp.AttachedCollections, "more"), "Builder"
	if _, err := saveAgent(udb, cp); err != nil {
		t.Fatal(err)
	}
	if out, err := turn.appDefCreateOrUpdate(map[string]any{"id": "desk", "description": "A desk."}, true); err != nil || !strings.Contains(out, `collection "More"`) {
		t.Errorf("a later save: %v %s", err, out)
	}
}
