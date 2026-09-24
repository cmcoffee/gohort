package orchestrate

// The narrow grant. Two agents, one corpus: A keeps it, B reads it. What these
// hold is that A's reach stops at the collection it was put in charge of.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func stewardCols() []Collection {
	return []Collection{{ID: "col-a", Name: "Runbooks", Owner: "alice", CuratorAgent: "agent-7"}}
}

// TestTheStewardToolCarriesOnlyCorpusActions pins what the grant IS. The whole
// reason for it is that the authoring catalog is too much: an agent keeping one
// collection current must not arrive holding create_agent or tool_def, and must
// not be able to mint or rename collections either.
func TestTheStewardToolCarriesOnlyCorpusActions(t *testing.T) {
	st := collectionStewardTool(stewardCols())
	if st == nil {
		t.Fatal("an agent in charge of a collection got no tool")
	}
	gt, ok := st.(*GroupedTool)
	if !ok {
		t.Fatalf("expected a grouped tool, got %T", st)
	}
	got := strings.Join(gt.ActionNames(), ",")
	if got != "add_text,add_url,docs,remove_doc" {
		t.Errorf("the grant is not the four corpus actions: %s", got)
	}
	for _, banned := range []string{"create", "update", "list", "get"} {
		for _, have := range gt.ActionNames() {
			if have == banned {
				t.Errorf("%q is authoring, not keeping a corpus, and must not be in the grant", banned)
			}
		}
	}
}

// TestASingleCollectionNeedsNoID pins the reason a one-collection grant takes
// no parameter: a model cannot target the wrong corpus if it is never asked
// which, so the id never has to survive a round trip through a prompt.
func TestASingleCollectionNeedsNoID(t *testing.T) {
	gt := collectionStewardTool(stewardCols()).(*GroupedTool)
	for _, name := range gt.ActionNames() {
		a, _ := gt.Action(name)
		if _, has := a.Params["id"]; has {
			t.Errorf("action %q still asks which collection although there is only one", name)
		}
		for _, r := range a.Required {
			if r == "id" {
				t.Errorf("action %q still REQUIRES an id it is never given", name)
			}
		}
	}
}

// TestAnUngrantedCollectionIsRefused is the gate. Agent A must not be able to
// prune Agent B's corpus, and the refusal must not silently redirect: quietly
// rewriting the call to the granted collection would teach the agent that any
// id works, and one wrong guess writes to a corpus nobody asked it to touch.
func TestAnUngrantedCollectionIsRefused(t *testing.T) {
	gt := collectionStewardTool([]Collection{
		{ID: "col-a", Name: "Runbooks", Owner: "alice", CuratorAgent: "agent-7"},
		{ID: "col-b", Name: "Policies", Owner: "alice", CuratorAgent: "agent-7"},
	}).(*GroupedTool)

	a, ok := gt.Action("remove_doc")
	if !ok {
		t.Fatal("remove_doc is missing")
	}
	_, err := a.Handler(map[string]any{"id": "col-someone-else", "doc_id": "d1"}, &ToolSession{})
	if err == nil {
		t.Fatal("an agent reached a collection it is not in charge of")
	}
	if !strings.Contains(err.Error(), "not in charge") {
		t.Errorf("the refusal should say why: %v", err)
	}
	// And it must name what IS granted, or the agent has no way to correct itself.
	if !strings.Contains(err.Error(), "col-a") {
		t.Errorf("the refusal does not say what the agent may use: %v", err)
	}
}

// TestDerivingTheToolDoesNotNarrowTheOriginal pins the deep copy. The steward
// tool is derived from the same `collections` tool Builder carries; if the
// derivation edited it in place, Builder would silently inherit the
// restrictions and lose the id parameter it depends on.
func TestDerivingTheToolDoesNotNarrowTheOriginal(t *testing.T) {
	_ = collectionStewardTool(stewardCols())
	base := collectionsListTool().(*GroupedTool)
	a, ok := base.Action("remove_doc")
	if !ok {
		t.Fatal("remove_doc vanished from the base tool")
	}
	if _, has := a.Params["id"]; !has {
		t.Fatal("deriving the steward tool stripped the id param from the tool it derived FROM")
	}
}

// TestNoCollectionMeansNoTool pins that this is opt-in: an ordinary agent is
// unchanged and pays nothing.
func TestNoCollectionMeansNoTool(t *testing.T) {
	if st := collectionStewardTool(nil); st != nil {
		t.Errorf("an agent in charge of nothing was handed a corpus tool: %v", st.Name())
	}
}

// stewardStores pins RootDB and VectorDB to fresh stores (sharing reads both)
// and returns the per-user collections store for user, which is also where the
// runner keeps that user's agents.
func stewardStores(t *testing.T) func(user string) Database {
	t.Helper()
	savedRoot, savedVec := RootDB, VectorDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	VectorDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB, VectorDB = savedRoot, savedVec })
	return func(user string) Database {
		udb := UserDB(CollectionsDB(), user)
		if udb == nil {
			t.Fatal("no per-user store")
		}
		return udb
	}
}

// TestInChargeMatchesTheOwnersAgentByIDOrResolvedName pins the resolver
// agreement. A CuratorAgent stored as the id, or as a name the owner's agents
// resolve to that id (older records, or set through the API), grants the
// steward tool, in any case, because the dispatch and the status line resolve
// it that way too.
func TestInChargeMatchesTheOwnersAgentByIDOrResolvedName(t *testing.T) {
	store := stewardStores(t)
	udb := store("alice")
	lib, err := saveAgent(udb, AgentRecord{Name: "Librarian", Owner: "alice", OrchestratorPrompt: "keeps runbooks"})
	if err != nil {
		t.Fatal(err)
	}
	for i, stored := range []string{lib.ID, "Librarian", "librarian"} {
		saveCollection(udb, Collection{
			ID: "col-" + string(rune('a'+i)), Name: "C", Owner: "alice", CuratorAgent: stored,
		})
	}
	got := curatedCollectionsFor(udb, "alice", lib.ID)
	if len(got) != 3 {
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ID+"/"+c.CuratorAgent)
		}
		t.Fatalf("expected all three spellings to resolve, got %v", ids)
	}
}

// TestInChargeOnlyOfTheOwnersCollections is the name collision. Carol's
// collection, shared with alice, names Carol's "Librarian" as its curator; the
// deployment's own collection names a "Librarian" too. Alice's agent that is
// also called Librarian was handed the corpus tools for both, because the match
// ran by name over everything alice could read. It must get neither: a name
// means an agent of whoever chose it.
func TestInChargeOnlyOfTheOwnersCollections(t *testing.T) {
	store := stewardStores(t)
	alice, carol := store("alice"), store("carol")
	mine, err := saveAgent(alice, AgentRecord{Name: "Librarian", Owner: "alice", OrchestratorPrompt: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := saveAgent(carol, AgentRecord{Name: "Librarian", Owner: "carol", OrchestratorPrompt: "theirs"})
	if err != nil {
		t.Fatal(err)
	}
	saveCollection(carol, Collection{
		ID: "col-carol", Name: "Legal", Owner: "carol", AllowedUsers: []string{"alice"}, CuratorAgent: "Librarian",
	})
	saveCollection(carol, Collection{
		ID: "col-carol-id", Name: "Contracts", Owner: "carol", AllowedUsers: []string{"alice"}, CuratorAgent: theirs.ID,
	})
	RootDB.Set(GlobalCollectionsTable, "col-dep", Collection{
		ID: "col-dep", Name: "Everyone", Scope: CollectionScopeDeployment, CuratorAgent: "Librarian",
	})
	saveCollection(alice, Collection{ID: "col-alice", Name: "Mine", Owner: "alice", CuratorAgent: "Librarian"})

	// Alice can read all four, so the old match had all three foreign ones to
	// pick from.
	if n := len(ListCollections(alice, "alice")); n != 4 {
		t.Fatalf("setup: alice should see 4 collections, sees %d", n)
	}
	got := curatedCollectionsFor(alice, "alice", mine.ID)
	if len(got) != 1 || got[0].ID != "col-alice" {
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ID)
		}
		t.Fatalf("alice's Librarian must steward only alice's own collection, got %v", ids)
	}
	// And carol's agent is still in charge of carol's.
	if got := curatedCollectionsFor(carol, "carol", theirs.ID); len(got) != 2 {
		t.Fatalf("carol's Librarian lost its own collections: %+v", got)
	}
}
