package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A form field with no matching key in the record it edits renders EMPTY over
// stored data, and posts that emptiness back on the next save.
//
// The live report: a phase's header said "Withheld tools (2)" — computed
// server-side from p.Deny — above a checklist with nothing ticked, because
// phaseRecord did not carry "deny". The two tools really were withheld, the
// prompt block said so, and the editor showed a clear list. Saving anything
// else in that phase would then have written the empty list back.
//
// Asserted over the WHOLE form rather than for deny alone: this is a class, not
// an incident, and the next field added to the form will be added by somebody
// who did not read this comment.
func TestEveryPhaseFormFieldHasARecordKey(t *testing.T) {
	p := MachinePhase{
		Name: "assess", Desc: "work it out", Prompt: "do the thing", Resident: true,
		Tools: []string{"web_search"}, Deny: []string{"fetch_url", "browse_page"},
		Reach: ReachRead, Guard: "the user moved on", GuardTo: "answer",
		Think: "on", Model: "lead", Next: "answer", Keep: []string{"findings"},
	}
	def := MachineDef{Name: "m", Start: "assess", Phases: []MachinePhase{p, {Name: "answer", Resident: true, Prompt: "answer"}}}

	rec := phaseRecord(p)
	var missing []string
	for _, f := range phaseFieldsFor(def, p, editorCatalog{}) {
		if f.Field == "" || f.Type == "header" {
			continue
		}
		if _, ok := rec[f.Field]; !ok {
			missing = append(missing, f.Field)
		}
	}
	if len(missing) > 0 {
		t.Errorf("form fields with no record key render empty over stored data and post the emptiness back on save: %v", missing)
	}
}

// The specific regression, stated plainly so a future edit that drops the key
// again fails with the reason rather than with a list.
func TestTheDenyListReachesTheFormItIsEditedIn(t *testing.T) {
	rec := phaseRecord(MachinePhase{Name: "a", Deny: []string{"fetch_url", "browse_page"}})
	v, ok := rec["deny"]
	if !ok {
		t.Fatal("the withheld list must reach the checklist that edits it, or the editor shows it empty and then saves it empty")
	}
	names, _ := v.([]string)
	if len(names) != 2 {
		t.Errorf("want both withheld names, got %v", v)
	}
}
