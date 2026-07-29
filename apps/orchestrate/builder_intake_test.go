package orchestrate

// Builder's intake: an all-button "pick a starting point" for the human
// surface, whose options double as the dispatch brief hint.
//
// The two halves used to be mutually exclusive — dispatchBriefHint
// skipped button fields entirely, so the shape that reads best to a
// human (button-only) published no brief guidance at all.

import (
	"strings"
	"testing"
)

func TestDispatchBriefHint_ButtonOptions(t *testing.T) {
	// Button-only: the options ARE what a caller must state, so they
	// belong in the hint even though a human clicks rather than types.
	buttons := AgentRecord{IntakeForm: IntakeFormSpec{{
		Name: "start", Label: "What do you want to build?", Type: "button",
		Options: []string{"An agent", "An app"},
	}}}
	hint := dispatchBriefHint(buttons)
	if hint == "" {
		t.Fatal("a button-only intake must still produce a brief hint")
	}
	for _, want := range []string{"What do you want to build?", "An agent", "An app"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint missing %q: %s", want, hint)
		}
	}

	// A button with no options has nothing to tell a caller — still skipped.
	if got := dispatchBriefHint(AgentRecord{IntakeForm: IntakeFormSpec{
		{Name: "go", Label: "Go", Type: "button"},
	}}); got != "" {
		t.Errorf("an option-less button should contribute nothing, got %q", got)
	}

	// Mixed forms keep both halves.
	mixed := AgentRecord{IntakeForm: IntakeFormSpec{
		{Name: "start", Label: "Kind", Type: "button", Options: []string{"A", "B"}},
		{Name: "goal", Label: "What it should do", Required: true},
	}}
	hint = dispatchBriefHint(mixed)
	if !strings.Contains(hint, "Kind (A | B)") || !strings.Contains(hint, "What it should do (required)") {
		t.Errorf("mixed form hint wrong: %s", hint)
	}

	// No intake at all stays silent.
	if got := dispatchBriefHint(AgentRecord{}); got != "" {
		t.Errorf("an agent with no intake form should produce no hint, got %q", got)
	}
}

func TestBuilderSeed_HasStartingPoints(t *testing.T) {
	var builder AgentRecord
	for _, a := range seedAgents() {
		if isBuilderAgent(a.ID) {
			builder = a
		}
	}
	if builder.ID == "" {
		t.Fatal("Builder seed not found")
	}
	if len(builder.IntakeForm) != 1 || builder.IntakeForm[0].Type != "button" {
		t.Fatalf("Builder should carry a single button field, got %+v", builder.IntakeForm)
	}
	opts := builder.IntakeForm[0].Options
	if len(opts) < 4 {
		t.Errorf("expected the build kinds as starting points, got %v", opts)
	}
	// An all-button form is what renders as "Pick a starting point" with
	// no submit button and leaves the composer live — the whole reason
	// this doesn't gate the "fix one thing" case.
	for _, f := range builder.IntakeForm {
		if f.Type != "button" {
			t.Errorf("a non-button field would turn the starting points into a form with a submit gate: %+v", f)
		}
	}
	if hint := dispatchBriefHint(builder); hint == "" {
		t.Error("Builder's starting points should reach callers as a brief hint")
	}
}

// Builder's intake offers four build kinds plus "Fix something". The four ARE
// the answer once clicked; the fifth is only a category, and submitting it bare
// left Builder with no target — it swept every agent, monitor, schedule and run
// (~50k tokens) and then asked what was meant anyway.
func TestFixOptionAsksWhatToFix(t *testing.T) {
	seed, ok := seedAgentByID("seed-builder")
	if !ok {
		t.Fatal("seed-builder should exist")
	}
	if len(seed.IntakeForm) == 0 {
		t.Fatal("Builder should have an intake form")
	}
	f := seed.IntakeForm[0]

	ask, hasDetail := f.Detail["Fix something"]
	if !hasDetail {
		t.Fatal(`"Fix something" must ask what to fix — bare, it starts a turn with no target`)
	}
	if !strings.Contains(strings.ToLower(ask), "fix") {
		t.Errorf("the question should name what it wants: %q", ask)
	}

	// The build kinds must NOT ask — they're already specific, and a question
	// on every option turns a one-click intake into a form.
	for _, opt := range []string{"Agent", "App", "Tool", "Pipeline"} {
		if _, asks := f.Detail[opt]; asks {
			t.Errorf("%q is already the answer — it should submit immediately", opt)
		}
	}

	// Every option carrying a question must still be a real option, or the
	// map silently does nothing.
	for opt := range f.Detail {
		found := false
		for _, o := range f.Options {
			if o == opt {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("detail question for %q, which is not an option", opt)
		}
	}
}
