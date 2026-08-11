package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The standing runner's approval hold is the single guard covering BOTH
// standing fires and delegations, and it resolves the target out of the store
// before deciding. RunDelegation fills StandingAgent.AgentID with whatever
// string the caller typed — a display name as readily as an id — so the
// resolution has to accept both or the guard is addressable-around.
//
// loadAgent is id-only. A delegation to "Research Assistant" therefore looked
// up nothing, ok came back false, and a sub-agent still awaiting its owner's
// approval ran anyway, silently: no refusal, no attention entry, nothing to
// notice. Dispatching the same agent by id was correctly held. A gate that
// depends on how the target was spelled is not a gate.
func TestApprovalHoldResolvesTargetByNameNotJustID(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")

	held, err := saveAgent(udb, AgentRecord{
		Name: "Research Assistant", Owner: "u",
		OrchestratorPrompt: "p", PendingApproval: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if held.ID == "Research Assistant" {
		t.Fatal("precondition: the record's id must differ from its display name")
	}

	// By id — the path that always worked.
	if rec, ok := loadAgent(udb, held.ID); !ok || !rec.PendingApproval {
		t.Fatalf("id lookup should find the held agent: ok=%v", ok)
	}

	// By display name — what a delegation actually carries. This is the lookup
	// the guard now performs; an id-only lookup returns nothing here, which is
	// precisely how the hold got skipped.
	if _, ok := loadAgent(udb, held.Name); ok {
		t.Fatal("loadAgent is documented id-only — if it now resolves names, the guard's fix needs revisiting")
	}
	rec, ok := findAgentByNameOrID(udb, "u", held.Name)
	if !ok {
		t.Fatal("name lookup found nothing — a delegation by display name would skip the approval hold")
	}
	if !rec.PendingApproval {
		t.Errorf("resolved %q but PendingApproval was lost, so the hold would not fire", rec.Name)
	}
	if rec.ID != held.ID {
		t.Errorf("resolved to %q, want the held agent %q", rec.ID, held.ID)
	}
}
