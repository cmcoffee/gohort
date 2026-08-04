// A turn gets ONE background-job allowance, however many sessions it mints.
//
// The ledger used to live on the ToolSession, and runWorkerStep mints a fresh
// session per plan step — so a three-step plan carried three allowances and the
// user got three deliveries for a request they made once. The cap was counting
// sessions when the thing worth capping is turns.
package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestEverySessionOfATurnSharesOneDetachLedger(t *testing.T) {
	turn := &chatTurn{
		app:   &OrchestrateApp{},
		user:  "craig",
		udb:   &DBase{Store: kvlite.MemStore()},
		agent: AgentRecord{ID: "wiwee"},
	}

	// Three sessions, the way a three-step plan mints them.
	a, b, c := turn.newToolSession(), turn.newToolSession(), turn.newToolSession()
	if a.Detach == nil {
		t.Fatal("a turn's session must carry the turn's ledger")
	}
	if a.Detach != b.Detach || b.Detach != c.Detach {
		t.Fatal("sessions of one turn must share ONE ledger, not a copy each")
	}

	if _, free := a.ClaimDetachSlot("image"); !free {
		t.Fatal("the first step must get the slot")
	}
	a.RecordDetachSlot("image", TaskRun{ID: "task-1", Label: "editing image#1"})
	for name, later := range map[string]*ToolSession{"step 2": b, "step 3": c} {
		prior, free := later.ClaimDetachSlot("image")
		if free {
			t.Errorf("%s started a second background job for one request", name)
		}
		if prior.ID != "task-1" {
			t.Errorf("%s must be told which job holds the slot, got %+v", name, prior)
		}
	}

	// A different turn is a different allowance.
	other := &chatTurn{app: turn.app, user: "craig", udb: turn.udb, agent: turn.agent}
	if _, free := other.newToolSession().ClaimDetachSlot("image"); !free {
		t.Error("the next turn must start with its slot free")
	}
}
