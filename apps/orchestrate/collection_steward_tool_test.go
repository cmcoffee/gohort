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

// TestInChargeMatchesNameOrID pins the resolver agreement. Dispatch resolves an
// agent by name OR id, so a CuratorAgent stored under either spelling has to be
// found here too — a field that runs under one and reads as unset under the
// other is the bug that already cost an afternoon on the status line.
func TestInChargeMatchesNameOrID(t *testing.T) {
	udb := &DBase{Store: kvlite.MemStore()}
	for _, stored := range []string{"agent-7", "Librarian", "librarian"} {
		saveCollection(udb, Collection{
			ID: "col-" + stored, Name: "C", Owner: "alice", CuratorAgent: stored,
		})
	}
	got := curatedCollectionsFor(udb, "alice", "agent-7", "Librarian")
	if len(got) != 3 {
		var ids []string
		for _, c := range got {
			ids = append(ids, c.ID+"/"+c.CuratorAgent)
		}
		t.Fatalf("expected all three spellings to resolve, got %v", ids)
	}
}
