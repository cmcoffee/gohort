package orchestrate

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/ui"
)

// PATCH exists so ONE record can be edited from several forms. A FormPanel
// POSTs the fields IT holds as the whole record, so splitting a long editor
// across page-level sections used to mean each save wiped every field it
// didn't carry. The allowlist is what keeps that safe.
func TestPatchAllowlistCoversTheEditorsFields(t *testing.T) {
	// Every field the agent editor actually renders must be patchable, or
	// splitting the form would silently drop that control's saves.
	for _, f := range []string{
		"name", "description", "orchestrator_prompt", "plan_guidance", "rules",
		"triggers", "allowed_tools", "max_plan_steps", "max_worker_rounds",
		"think", "think_budget", "gap_check", "lead_model", "memory_mode",
		"context_depth", "channel", "fleet", "author", "exposed", "hidden",
		"dispatch_mode", "intake_form", "evals",
	} {
		if !patchAgentFields[f] {
			t.Errorf("editor field %q is not patchable — its section's saves would be lost", f)
		}
	}
}

// The protected fields must be absent BY NAME. PATCH merges onto the stored
// record, so anything it accepts is something it can overwrite — and these
// each have their own owner-only endpoint precisely so an ordinary edit path
// cannot weaken them.
func TestPatchAllowlistExcludesProtectedFields(t *testing.T) {
	for _, f := range []string{
		"guardrails", "guardrail_hooks", "guardrail_fail_closed", "guardrail_declines",
		"locked", "id", "owner", "created",
	} {
		if patchAgentFields[f] {
			t.Errorf("%q is patchable — a partial save could reach a field that has its own protected endpoint", f)
		}
	}
}

// A guardrail is the one thing an agent must not be able to talk its way out
// of, so this is worth asserting on its own rather than trusting the list
// above to stay correct.
func TestPatchCannotTouchGuardrails(t *testing.T) {
	for k := range patchAgentFields {
		if len(k) >= 9 && k[:9] == "guardrail" {
			t.Fatalf("PATCH accepts %q — guardrails must only be settable through their owner-only endpoint", k)
		}
	}
}

// The split turns one long form into page-level sections so the page's own
// left rail navigates them, instead of eight accordions inside one section.
func TestSplitAgentFormSections(t *testing.T) {
	fields := []ui.FormField{
		{Field: "name", Type: "text"},
		{Field: "description", Type: "text"},
		{Type: "header", Label: "Budgets", Help: "how much it may spend"},
		{Field: "max_plan_steps", Type: "number"},
		{Type: "header", Label: "Memory"},
		{Field: "memory_mode", Type: "select"},
	}
	got := splitAgentFormSections("a1", "../api/agents/a1", fields, "identity")
	if len(got) != 2 {
		t.Fatalf("want 2 sections (Agent + Memory), got %d", len(got))
	}
	if got[0].Title != "Agent" || got[1].Title != "Memory" {
		t.Errorf("section titles = %q, %q", got[0].Title, got[1].Title)
	}
	// Identity fields lead the FIRST group rather than getting a rail entry of
	// their own — burying name/description behind a click hides the one thing
	// you always want visible.
	first := got[0].Body.(ui.FormPanel)
	if len(first.Fields) != 3 {
		t.Errorf("first section should hold name+description+Budgets fields, got %d", len(first.Fields))
	}
	// Every panel must PATCH. A POST would send only that section's fields as
	// the whole record and blank everything else.
	for i, sec := range got {
		p := sec.Body.(ui.FormPanel)
		if p.Method != "PATCH" {
			t.Errorf("section %d saves with %q — POST would wipe the fields it doesn't show", i, p.Method)
		}
		// The id must ride in the URL: a FormPanel's PATCH body is just the
		// changed field and names no record.
		if !strings.Contains(p.PostURL, "id=a1") {
			t.Errorf("section %d PostURL %q does not name the record", i, p.PostURL)
		}
	}
}

// No headers means nothing to split on — the caller keeps its single form.
func TestSplitAgentFormSectionsNoHeaders(t *testing.T) {
	got := splitAgentFormSections("a1", "s", []ui.FormField{{Field: "name"}}, "x")
	if got != nil {
		t.Errorf("a headerless form should not split, got %d sections", len(got))
	}
}

// The lead-model toggle chooses WHICH MODEL does the reasoning, so it belongs
// in Reasoning. It previously sat under "Autonomous runs" purely by position —
// a header owns the fields until the next header, and this one was appended to
// the end of the form — which read as an unattended-run option when it governs
// every turn.
func TestLeadModelFieldSitsInReasoning(t *testing.T) {
	fields := []ui.FormField{
		{Type: "header", Label: "Reasoning"},
		{Field: "think", Type: "select"},
		leadModelField(true),
		{Type: "header", Label: "Autonomous runs"},
		{Field: "auto_approve_tools", Type: "tags"},
	}
	got := splitAgentFormSections("a1", "s", fields, "x")
	if len(got) != 2 {
		t.Fatalf("want Reasoning + Autonomous runs, got %d", len(got))
	}
	reasoning := got[0].Body.(ui.FormPanel)
	found := false
	for _, f := range reasoning.Fields {
		if f.Field == "lead_model" {
			found = true
		}
	}
	if !found {
		t.Error("lead_model is not in the Reasoning group")
	}
	autonomous := got[1].Body.(ui.FormPanel)
	for _, f := range autonomous.Fields {
		if f.Field == "lead_model" {
			t.Error("lead_model leaked into Autonomous runs")
		}
	}
}

// With no distinct lead wired the toggle would be a no-op, so it renders
// nothing — but it must still be a field, so it can hold its place INLINE in
// Reasoning rather than being appended to the end of the form (which is how it
// got mis-filed in the first place).
func TestLeadModelFieldHiddenWithoutDistinctLead(t *testing.T) {
	f := leadModelField(false)
	if f.Type != "hidden" {
		t.Errorf("without a distinct lead the toggle should render nothing, got type %q", f.Type)
	}
	if f.Default != "" {
		t.Error("the hidden placeholder must not contribute a value to the save payload")
	}
}

// The dispatch POLICY select and the LIST it draws from belong together.
// Split apart, you set "Only allow" in one rail entry and then had to find a
// different one to say WHICH agents — the policy's own help text even pointed
// at "the Dispatch target list section below".
func TestDelegationFoldsInTheTargetList(t *testing.T) {
	sections := []ui.Section{
		{Title: "Agent", Body: ui.FormPanel{}},
		{Title: "Delegation", Body: ui.FormPanel{Fields: []ui.FormField{{Field: "dispatch_mode"}}}},
	}
	picker := ui.ChipPicker{Field: "allowed_dispatch_targets"}
	if !foldIntoDelegation(sections, picker) {
		t.Fatal("the picker was not folded into the Delegation section")
	}
	stack, ok := sections[1].Body.(ui.Stack)
	if !ok {
		t.Fatalf("Delegation body is %T, want a Stack holding the form + picker", sections[1].Body)
	}
	if len(stack.Children) != 2 {
		t.Fatalf("stack holds %d children, want the form and the picker", len(stack.Children))
	}
	if _, ok := stack.Children[0].(ui.FormPanel); !ok {
		t.Error("the policy form should come first — you choose the policy, then the targets")
	}
	if _, ok := stack.Children[1].(ui.ChipPicker); !ok {
		t.Error("the target picker should follow the policy form")
	}
}

// Create mode does not split, so there is no Delegation section to fold into
// and the picker must stand alone rather than vanishing.
func TestDelegationFoldNoOpWithoutSection(t *testing.T) {
	sections := []ui.Section{{Title: "Agent", Body: ui.FormPanel{}}}
	if foldIntoDelegation(sections, ui.ChipPicker{}) {
		t.Error("reported a fold with no Delegation section present")
	}
	if _, ok := sections[0].Body.(ui.FormPanel); !ok {
		t.Error("an unrelated section was modified")
	}
}
