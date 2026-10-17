package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	// On Builder the choice means its own thing, the lead designing apps and a
	// worker writing them (builder_app_split.go), and is named for it.
	for _, o := range leadUseField(true, AgentRecord{ID: "seed-builder"}).Options {
		if o.Value == leadUsePlan && !strings.Contains(o.Label, "Lead designs apps") {
			t.Errorf("Builder's plan choice reads %q", o.Label)
		}
	}

	// The fourth combination (lead on and consult on) is the lead.
	both := AgentRecord{ID: "a2", LeadModel: true, ConsultLead: settingOn}
	if got := leadUseOf(RootDB, both); got != leadUseLead {
		t.Errorf("lead + consult reads %q", got)
	}

	// A scale from the worker alone to the lead for everything, the default
	// said in its stop's own help line.
	f := leadUseField(true, plain)
	var order []string
	for _, o := range f.Options {
		order = append(order, o.Value)
	}
	if f.Field != "lead_use" || f.Type != "scale" || strings.Join(order, " ") != "off consult review plan lead" ||
		!strings.Contains(f.Options[0].Help, "This is the default.") {
		t.Errorf("the scale: %+v", f.Options)
	}
	if m := withLeadUse(RootDB, plain); string(m["lead_use"]) != `"off"` {
		t.Errorf("the form's lead_use: %s", m["lead_use"])
	}
}

// The lead choice saves through PATCH, which is how the editor sends it, for
// every value it offers: the translation writes lead_plans as well as
// lead_model and consult_lead, and a field the allowlist did not know made
// "Lead plans" (and Builder's "Lead designs apps") answer 400.
func TestTheLeadChoiceSavesThroughPATCH(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	for _, id := range []string{"seed-builder", ""} {
		a, err := saveAgent(udb, AgentRecord{ID: id, Owner: user, Name: "Agent " + id, OrchestratorPrompt: "Help."})
		if err != nil {
			t.Fatal(err)
		}
		for _, choice := range []string{leadUsePlan, leadUseLead, leadUseReview, leadUseOff, leadUseReview, leadUsePlan} {
			w := httptest.NewRecorder()
			body := `{"id":"` + a.ID + `","lead_use":"` + choice + `"}`
			T.patchAgent(w, httptest.NewRequest(http.MethodPatch, "/api/agents?id="+a.ID, strings.NewReader(body)), udb, user, a.ID)
			if w.Code != http.StatusOK {
				t.Fatalf("%s: lead_use=%s answered %d: %s", a.ID, choice, w.Code, w.Body.String())
			}
			got, _ := loadAgent(udb, a.ID)
			if leadUseOf(RootDB, got) != choice && !(choice == leadUseOff && leadUseOf(RootDB, got) == leadUseDefault(RootDB, got)) {
				t.Errorf("%s: chose %s, reads %s", a.ID, choice, leadUseOf(RootDB, got))
			}
		}
	}
}
