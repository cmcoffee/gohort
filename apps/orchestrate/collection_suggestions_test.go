package orchestrate

// Suggestions: a reading agent files what it learned, whoever looks after the
// collection decides. What these hold is who may file one, that filing changes
// nothing in the collection, and that the curator, or else the owner, gets it.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// suggestionSetup gives alice a collection her agent Reader reads, shared with
// carol as a contributor and bob as a reader, each with an agent attached.
func suggestionSetup(t *testing.T) (*OrchestrateApp, map[string]AgentRecord) {
	t.Helper()
	store := stewardStores(t)
	T := &OrchestrateApp{AppCore: AppCore{DB: CollectionsDB()}}
	saveCollection(store("alice"), Collection{ID: "col-a", Name: "Runbooks", Owner: "alice",
		AllowedUsers: []string{"bob", "carol"}, Contributors: []string{"carol"}})
	agents := map[string]AgentRecord{}
	for _, u := range []string{"alice", "bob", "carol"} {
		a, err := saveAgent(store(u), AgentRecord{Name: "Reader", Owner: u, OrchestratorPrompt: "reads",
			AttachedCollections: []string{"col-a"}})
		if err != nil {
			t.Fatal(err)
		}
		agents[u] = a
	}
	return T, agents
}

func suggestTool(t *testing.T, T *OrchestrateApp, user string, a AgentRecord) (AgentToolDef, bool) {
	t.Helper()
	return (&chatTurn{app: T, user: user, agent: a}).suggestCollectionUpdateTool()
}

// The owner's and a contributor's agents may suggest; a reader's may not, as
// it may not add either.
func TestWhoMaySuggestIsWhoMayAlreadyAdd(t *testing.T) {
	T, agents := suggestionSetup(t)
	for user, want := range map[string]bool{"alice": true, "carol": true, "bob": false} {
		if _, ok := suggestTool(t, T, user, agents[user]); ok != want {
			t.Errorf("%s's agent has the suggestion tool: %v, want %v", user, ok, want)
		}
	}
}

// Filing changes nothing in the collection and lands in the queue, once.
func TestASuggestionIsQueuedNotApplied(t *testing.T) {
	T, agents := suggestionSetup(t)
	td, ok := suggestTool(t, T, "carol", agents["carol"])
	if !ok {
		t.Fatal("carol's agent has no suggestion tool")
	}
	if _, has := td.Tool.Parameters["collection"]; has {
		t.Error("one collection should need no collection parameter")
	}
	if _, err := td.Handler(context.Background(), map[string]any{"suggestion": "The restart steps changed."}); err == nil {
		t.Error("a suggestion with no source was filed")
	}
	args := map[string]any{"suggestion": "The restart steps changed.", "source": "https://docs.example/restart"}
	out, err := td.Handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nothing in") || !strings.Contains(out, "alice") {
		t.Errorf("the reply should say nothing changed and who decides: %s", out)
	}
	if out, _ := td.Handler(context.Background(), args); !strings.Contains(out, "already suggested") {
		t.Errorf("a repeat was filed again: %s", out)
	}
	c, _ := loadCollection(UserDB(CollectionsDB(), "alice"), "alice", "col-a")
	list := T.listSuggestions(c)
	if len(list) != 1 || list[0].User != "carol" || list[0].Agent != agents["carol"].ID || list[0].Source != "https://docs.example/restart" {
		t.Fatalf("queue = %+v", list)
	}
	if len(c.IngestedURLs) != 0 {
		t.Error("filing a suggestion changed the collection")
	}
}

// The owner's switch takes the tool away, and the queue has a cap.
func TestTheOwnerCanTurnSuggestionsOffAndTheQueueIsBounded(t *testing.T) {
	T, agents := suggestionSetup(t)
	udb := UserDB(CollectionsDB(), "alice")
	c, _ := loadCollection(udb, "alice", "col-a")
	for i := 0; i < maxPendingSuggestions; i++ {
		if _, err := T.addSuggestion(c, collectionSuggestion{Text: "s" + strings.Repeat("!", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := T.addSuggestion(c, collectionSuggestion{Text: "one more"}); err == nil {
		t.Error("the queue took one past its cap")
	}
	c.SuggestionsOff = true
	saveCollection(udb, c)
	if _, ok := suggestTool(t, T, "alice", agents["alice"]); ok {
		t.Error("suggestions are off and the agent still has the tool")
	}
}

// The agent in charge of a collection changes it directly, so it gets no
// suggestion tool for it, and its own tool carries the queue.
func TestTheCuratorWorksTheQueue(t *testing.T) {
	T, agents := suggestionSetup(t)
	udb := UserDB(CollectionsDB(), "alice")
	c, _ := loadCollection(udb, "alice", "col-a")
	c.CuratorAgent = agents["alice"].ID
	saveCollection(udb, c)
	if _, ok := suggestTool(t, T, "alice", agents["alice"]); ok {
		t.Error("the curator was handed a suggestion tool for the collection it writes")
	}
	td, ok := suggestTool(t, T, "carol", agents["carol"])
	if !ok {
		t.Fatal("carol's agent lost its tool when a curator was named")
	}
	if !strings.Contains(td.Tool.Description, "the agent in charge of it, Reader") {
		t.Errorf("the tool should say suggestions go to the curator: %s", td.Tool.Description)
	}
	if _, err := td.Handler(context.Background(), map[string]any{"suggestion": "Add the v2 runbook.", "source": "the user said so"}); err != nil {
		t.Fatal(err)
	}

	gt := T.stewardToolWithQueue([]Collection{c}).(*GroupedTool)
	if !strings.Contains(gt.Desc(), "1 suggestion") {
		t.Errorf("the curator's tool does not say one is waiting: %s", gt.Desc())
	}
	list, _ := gt.Action("suggestions")
	out, err := list.Handler(map[string]any{}, &ToolSession{})
	if err != nil || !strings.Contains(out, "Add the v2 runbook.") {
		t.Fatalf("the curator cannot see the queue: %v %s", err, out)
	}
	sid := T.listSuggestions(c)[0].ID
	resolve, _ := gt.Action("resolve_suggestion")
	if _, err := resolve.Handler(map[string]any{"suggestion_id": sid, "outcome": "maybe"}, &ToolSession{}); err == nil {
		t.Error("an outcome other than applied or declined was taken")
	}
	if _, err := resolve.Handler(map[string]any{"suggestion_id": sid, "outcome": "declined", "note": "no source"}, &ToolSession{}); err != nil {
		t.Fatal(err)
	}
	if n := len(T.listSuggestions(c)); n != 0 {
		t.Errorf("a resolved suggestion is still waiting (%d)", n)
	}
	// The plain steward tool, which Builder-side tests derive, is unchanged.
	if got := strings.Join(collectionStewardTool([]Collection{c}).(*GroupedTool).ActionNames(), ","); got != "add_text,add_url,docs,remove_doc" {
		t.Errorf("the queue leaked into the plain grant: %s", got)
	}
}

// Deleting a collection takes its queue with it.
func TestDeletingACollectionDropsItsQueue(t *testing.T) {
	T, _ := suggestionSetup(t)
	c, _ := loadCollection(UserDB(CollectionsDB(), "alice"), "alice", "col-a")
	if _, err := T.addSuggestion(c, collectionSuggestion{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	T.dropAllSuggestions(c)
	if n := len(T.listSuggestions(c)); n != 0 {
		t.Errorf("%d suggestions outlived the drop", n)
	}
}
