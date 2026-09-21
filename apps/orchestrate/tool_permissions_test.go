package orchestrate

// The permission on a tool lives on the row that grants the tool.
//
// It used to be two checklists in the agent editor — "Pre-approved tools" and
// "Never unattended" — over the same option set, on a page that never let you
// pick a tool at all. You ticked a name there and went to the Tools list to
// find out whether the agent could even call it.

import (
	"strings"
	"testing"
)

// One place asks it now. A checklist reappearing in the editor is the
// regression this guards.
func TestTheEditorNoLongerAsksAboutToolPermissions(t *testing.T) {
	src := packageSource(t)
	i := strings.Index(src, "func agentFormFields")
	if i < 0 {
		i = strings.Index(src, `Field: "daily_spend_usd"`)
	}
	if i < 0 {
		t.Fatal("cannot find the editor's field list")
	}
	for _, gone := range []string{
		`{Field: "auto_approve_tools", Type: "checklist"`,
		`{Field: "no_unattended_tools", Type: "checklist"`,
	} {
		if strings.Contains(src, gone) {
			t.Errorf("the editor asks again: %s", gone)
		}
	}
}

// The ladder is offered on exactly the tools that could stop and ask. Two
// definitions of that would drift into a row offering a setting the gate never
// consults, so the names come from the options.
func TestTheLadderIsOfferedOnWhatCanActuallyPrompt(t *testing.T) {
	opts := approvableToolOptions("")
	names := approvableToolNames("")
	if len(names) != len(opts) {
		t.Fatalf("names = %d, options = %d; they must be the same pool", len(names), len(opts))
	}
	for i, o := range opts {
		if names[i] != o.Value {
			t.Errorf("name %d = %q, want %q", i, names[i], o.Value)
		}
	}
}

// The page hands the modal that pool, or every row renders as read-only and
// the ladder never appears.
func TestThePageShipsTheApprovablePool(t *testing.T) {
	src := packageSource(t)
	if !strings.Contains(src, "window.ORCH_APPROVABLE_TOOLS = ") {
		t.Error("the modal is never told which tools can prompt")
	}
	if !strings.Contains(src, "approvableToolNames(user)") {
		t.Error("the pool is not built from the shared predicate")
	}
}
