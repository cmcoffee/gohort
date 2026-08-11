package orchestrate

import (
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/appagents"
)

// survey is Builder's orient-first tool: zero-arg, read-only, named "survey".
func TestSurveyToolDef(t *testing.T) {
	def := surveyWorkspaceToolDef(nil)
	if def.Tool.Name != "survey" {
		t.Errorf("tool name = %q, want survey", def.Tool.Name)
	}
	if len(def.Tool.Parameters) != 0 {
		t.Errorf("survey should take no params, got %d", len(def.Tool.Parameters))
	}
	if def.Handler == nil {
		t.Fatal("survey handler is nil")
	}
	// An empty/unwired owner must not panic and must still render every section
	// header so the shape is stable for the model. (Counts aren't asserted:
	// Secure()'s global store is process-shared, so sibling tests can seed
	// credentials this call legitimately sees.)
	out := surveyWorkspace("")
	for _, h := range []string{"AGENTS (", "TOOLS (", "CREDENTIALS (", "APPS (", "PIPELINES (", "EVENT MONITORS (", "STANDING AGENTS ("} {
		if !strings.Contains(out, h) {
			t.Errorf("survey output missing section %q\n---\n%s", h, out)
		}
	}
}

func TestSurveyHelpers(t *testing.T) {
	if got := orNone("  "); got != "(none)" {
		t.Errorf("orNone(blank) = %q, want (none)", got)
	}
	if got := orNone("ts3_api"); got != "ts3_api" {
		t.Errorf("orNone(value) = %q", got)
	}
	if got := oneLine("  a\n\tb   c  ", 40); got != "a b c" {
		t.Errorf("oneLine collapse = %q, want 'a b c'", got)
	}
	if got := oneLine("", 10); got != "(no description)" {
		t.Errorf("oneLine(empty) = %q", got)
	}
	if got := oneLine("abcdefghij", 5); got != "abcde…" {
		t.Errorf("oneLine truncate = %q, want abcde…", got)
	}
}

// The survey is the Builder's picture of the fleet. Retired seeds and hidden
// app templates showing up there is not cosmetic: it invites the Builder to
// propose delegating to Chat, or to build on a per-appliance template that
// nothing can address. All four exclusions converge on one predicate so the
// answer cannot differ between the survey and the dispatch surfaces.
func TestFleetHiddenCoversRetiredSeedsAndAppTemplates(t *testing.T) {
	// A Hidden app agent, registered here because hiddenAppAgent consults the
	// app-agent registry and no app is linked into this test binary. This is
	// the same shape the Servitor Investigator registers with (Hidden: true,
	// a per-appliance template).
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: "app-test-template", OwningApp: "Test",
		Name: "Test Template", Hidden: true,
	})

	for _, id := range []string{
		"seed-chat",         // hard-retired
		"seed-research",     // soft-retired archetype seed
		"seed-kb",           // soft-retired archetype seed
		"app-test-template", // Hidden app template
	} {
		if !fleetHidden(id) {
			t.Errorf("%s must be hidden from every discovery surface", id)
		}
	}
	// Real, user-facing agents must still be listed — this is a filter, not a
	// blanket suppression.
	for _, id := range []string{"seed-builder", "some-user-agent"} {
		if fleetHidden(id) {
			t.Errorf("%s is a real fleet member and must stay visible", id)
		}
	}
}
