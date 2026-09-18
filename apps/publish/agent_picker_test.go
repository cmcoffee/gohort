package publish

// The Agent column is a picker now. What it is keyed BY is the whole of what
// makes it correct, so that is what these hold.

import (
	"net/http/httptest"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// TestTheAgentPickerIsKeyedByNameNotID is the one that would otherwise ship
// broken and look fine. An agent id is a UUID minted in ONE user's store; this
// destination is resolved again in the store of whoever presses Publish. A
// picker that saved the id would work for the admin who configured it and
// silently resolve to nothing for everybody else.
func TestTheAgentPickerIsKeyedByNameNotID(t *testing.T) {
	t.Cleanup(func() { RegisterAgentNamer(nil) })
	RegisterAgentNamer(func(string) []ui.SelectOption {
		return []ui.SelectOption{
			{Value: "3f2a…-uuid", Label: "Tickets"},
			{Value: "9c1b…-uuid", Label: "Release Notes"},
		}
	})
	got := agentNameChoices("alice")
	if len(got) != 2 {
		t.Fatalf("expected both agents, got %+v", got)
	}
	for _, o := range got {
		if o.Value != o.Label {
			t.Errorf("the stored value must be the NAME, not an id: value=%q label=%q", o.Value, o.Label)
		}
		if o.Value == "3f2a…-uuid" || o.Value == "9c1b…-uuid" {
			t.Errorf("an agent id reached the picker as a value (%q) — it resolves in one store only", o.Value)
		}
	}
}

// TestTheAgentPickerSurvivesNoNamer pins the degradation: a deployment where
// nothing agent-aware has registered still renders the admin page, with the
// column as the free-text box it was before.
func TestTheAgentPickerSurvivesNoNamer(t *testing.T) {
	t.Cleanup(func() { RegisterAgentNamer(nil) })
	RegisterAgentNamer(nil)
	if got := agentNameChoices("alice"); got != nil {
		t.Errorf("no namer must mean no suggestions, not a panic: %+v", got)
	}
	if got := agentNameChoices(""); got != nil {
		t.Errorf("an unauthenticated render must be survivable: %+v", got)
	}
}

// TestTheAgentColumnStaysOpenToTyping pins that this did NOT become a fence.
// The suggestions are the ADMIN's agents, which is a good guess at what exists
// in other people's stores and not a guarantee — the right answer may be an
// agent only other people have, and a select would make that unsayable.
func TestTheAgentColumnStaysOpenToTyping(t *testing.T) {
	sec := adminSection(httptest.NewRequest("GET", "/", nil))
	panel, ok := sec.Body.(ui.FormPanel)
	if !ok {
		t.Fatalf("publishing settings are no longer a FormPanel: %T", sec.Body)
	}
	var col *ui.FormField
	for _, f := range panel.Fields {
		if f.Field != "agents" {
			continue
		}
		for i := range f.Columns {
			if f.Columns[i].Field == "agent" {
				col = &f.Columns[i]
			}
		}
	}
	if col == nil {
		t.Fatal("the Agent column is gone")
	}
	if col.Type != "combo" {
		t.Errorf("the Agent column is %q; a select would refuse an agent the admin does not have but other users do", col.Type)
	}
}
