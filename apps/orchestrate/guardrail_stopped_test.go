package orchestrate

// A check that a Stop ended is not a check that failed. Stopped mid-turn, the
// pre-input check beside the model's first call ends with the turn, and it
// was recorded as "a check could not run; the turn proceeded unchecked": a
// stop, edit and resend after a server restart read in the export as two
// guardrail trips on a turn that never ran.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAStoppedTurnRecordsNoFailedCheck(t *testing.T) {
	diags := func(stop bool) []SessionDiag {
		root := &DBase{Store: kvlite.MemStore()}
		udb := UserDB(root, "u")
		ctx, cancel := context.WithCancel(context.Background())
		if stop {
			cancel()
		} else {
			defer cancel()
		}
		// No model: the warden cannot run, which is the failure a live turn
		// must still record.
		turn := &chatTurn{app: &OrchestrateApp{AppCore: AppCore{DB: root}}, ctx: ctx, udb: udb, user: "u",
			agent:   AgentRecord{ID: "a1", Owner: "u", Guardrails: "never mention salary", GuardrailHooks: []string{guardHookPreInput}},
			session: &ChatSession{ID: "s1", AgentID: "a1"}}
		if d, blocked := turn.guardrailInputDirective("what does the team earn?"); d != "" || blocked {
			t.Fatalf("a check that cannot run must fail open: %q %v", d, blocked)
		}
		var list []SessionDiag
		udb.Get(sessionDiagTable, "a1:s1", &list)
		return list
	}
	if got := diags(true); len(got) != 0 {
		t.Errorf("a stopped turn recorded %d diag(s): %+v", len(got), got)
	}
	live := diags(false)
	if len(live) != 1 || live[0].Kind != "guardrail-error" {
		t.Errorf("a check that could not run on a live turn must still be recorded: %+v", live)
	}
}

// The export's guardrail section says whether anything was changed. It told
// the reader the replies were not what the model wrote for a session whose
// only events were checks that changed nothing.
func TestTheExportSaysWhenNoReplyWasChanged(t *testing.T) {
	kept := []guardrailExportEvent{{What: "a check could not run", Kept: true}, {What: "a check could not run", Kept: true}}
	if lead := guardrailExportLead(kept); !strings.Contains(lead, "none of them changed a reply") || strings.Contains(lead, "not\nwhat the model") {
		t.Errorf("kept only: %q", lead)
	}
	acted := []guardrailExportEvent{{What: "a reply was blocked"}}
	if lead := guardrailExportLead(acted); !strings.Contains(lead, "acted 1 time(s)") || strings.Contains(lead, "more event") {
		t.Errorf("acted only: %q", lead)
	}
	if lead := guardrailExportLead(append(acted, kept...)); !strings.Contains(lead, "acted 1 time(s)") || !strings.Contains(lead, "2 more event(s) left the reply as written") {
		t.Errorf("mixed: %q", lead)
	}
	for _, k := range []string{"guardrail-error", "guardrail-no-verdict", "turn-judge-overturned"} {
		if !guardrailKeptKinds[k] {
			t.Errorf("%s changes no reply and should not count as one that did", k)
		}
	}
	if guardrailKeptKinds["guardrail-blocked"] {
		t.Error("a block changed the reply")
	}
}
