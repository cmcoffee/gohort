package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// One choice over two stored fields: each choice stores what it means, an
// agent following the default stays following it when the form sends the
// choice back unchanged, and the old fourth combination reads as the lead.
func TestTheLeadChoiceRoundTrips(t *testing.T) {
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })

	plain := AgentRecord{ID: "a1", Name: "Plain"} // no authoring: default is off
	if got := leadUseOf(RootDB, plain); got != leadUseOff {
		t.Fatalf("an undecided plain agent reads %q", got)
	}
	a := plain
	applyLeadUse(RootDB, &a, leadUseOff)
	if a.ConsultLead != "" || a.LeadModel {
		t.Errorf("choosing what it already has pinned it: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUseConsult)
	if a.ConsultLead != settingOn || a.LeadModel || leadUseOf(RootDB, a) != leadUseConsult {
		t.Errorf("consult: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUseLead)
	if !a.LeadModel || leadUseOf(RootDB, a) != leadUseLead {
		t.Errorf("lead: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUseOff)
	if a.LeadModel || a.ConsultLead != "" || leadUseOf(RootDB, a) != leadUseOff {
		t.Errorf("back to off should follow the default again: %+v", a)
	}

	// Planning on the lead is the lead with LeadPlans; leaving it for any
	// other choice clears LeadPlans.
	applyLeadUse(RootDB, &a, leadUsePlan)
	if !a.LeadModel || !a.LeadPlans || leadUseOf(RootDB, a) != leadUsePlan {
		t.Errorf("plan: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUseLead)
	if !a.LeadModel || a.LeadPlans || leadUseOf(RootDB, a) != leadUseLead {
		t.Errorf("plan back to lead: %+v", a)
	}
	applyLeadUse(RootDB, &a, leadUsePlan)
	applyLeadUse(RootDB, &a, leadUseOff)
	if a.LeadModel || a.LeadPlans {
		t.Errorf("plan to off: %+v", a)
	}
	// LeadPlans without LeadModel is inert and reads as what it is.
	if got := leadUseOf(RootDB, AgentRecord{ID: "a3", LeadPlans: true}); got == leadUsePlan {
		t.Error("LeadPlans alone reads as planning on the lead")
	}
	// Builder plans already, so it is not offered the choice.
	for _, o := range leadUseField(true, AgentRecord{ID: "seed-builder"}).Options {
		if o.Value == leadUsePlan {
			t.Error("Builder is offered planning on the lead")
		}
	}

	// The fourth combination (lead on and consult on) is the lead.
	both := AgentRecord{ID: "a2", LeadModel: true, ConsultLead: settingOn}
	if got := leadUseOf(RootDB, both); got != leadUseLead {
		t.Errorf("lead + consult reads %q", got)
	}

	f := leadUseField(true, plain)
	if f.Field != "lead_use" || len(f.Options) != 4 || f.Options[0].Value != leadUseLead || f.Options[1].Value != leadUsePlan || f.Options[3].Label != "Never use the lead (default)" {
		t.Errorf("the dropdown: %+v", f.Options)
	}
	if m := withLeadUse(RootDB, plain); string(m["lead_use"]) != `"off"` {
		t.Errorf("the form's lead_use: %s", m["lead_use"])
	}
}
