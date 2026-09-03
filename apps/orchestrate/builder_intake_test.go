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

// A bare "Fix something" is not actionable, and surveying to guess costs ~50k
// tokens and still ends in a question. Builder asks instead — in the
// CONVERSATION, so it can follow up on the answer, rather than via a text box
// grafted onto a row of buttons.
func TestFixRequestsAskBeforeSurveying(t *testing.T) {
	seed, ok := seedAgentByID("seed-builder")
	if !ok {
		t.Fatal("seed-builder should exist")
	}
	p := seed.OrchestratorPrompt
	if !strings.Contains(p, "FIX REQUESTS START WITH A QUESTION") {
		t.Fatal("Builder must be told to ask before acting on an untargeted fix request")
	}
	// Both beats the user asked for, in order.
	what := strings.Index(p, "What would you like to fix?")
	audit := strings.Index(p, "general audit")
	if what < 0 {
		t.Error("should ask WHAT to fix")
	}
	if audit < 0 {
		t.Error("should offer a general audit vs a specific issue")
	}
	if what > 0 && audit > 0 && audit < what {
		t.Error("the target question comes before the audit-or-issue question")
	}
	// It must not become a wall: a request that already names thing + symptom
	// skips straight to work.
	if !strings.Contains(p, "skip both questions") {
		t.Error("a fully-specified request must bypass the questions")
	}
	// And the form must not ask the same thing — one place asks, not two.
	if len(seed.IntakeForm) > 0 {
		if _, asks := seed.IntakeForm[0].Detail["Fix something"]; asks {
			t.Error("the conversation asks; the intake form must not ask it too")
		}
	}
}

// Every kind Builder can AUTHOR gets a door on the starting row, and the row is
// where anybody finds out what Builder does. Machine was the one missing: it is
// authored by a Builder-only tool declared right beside pipeline and app_def,
// and offering the siblings without it read as "Builder does not do machines" —
// which is how a two-phase machine kept being asked for as a setting.
func TestTheStartingRowOffersEveryKindBuilderAuthors(t *testing.T) {
	seed, ok := seedAgentByID("seed-builder")
	if !ok {
		t.Fatal("seed-builder should exist")
	}
	if len(seed.IntakeForm) == 0 {
		t.Fatal("Builder has no intake form")
	}
	opts := strings.Join(seed.IntakeForm[0].Options, "|")
	for _, kind := range []string{"Agent", "App", "Tool", "Pipeline", "Machine"} {
		if !strings.Contains(opts, kind) {
			t.Errorf("the starting row must offer %q — Builder authors it", kind)
		}
	}
	// The options double as the dispatch brief hint, so a caller composing a
	// brief is told the same list. A kind missing from the row is a kind
	// nobody knows to ask for, by either door.
	hint := dispatchBriefHint(seed)
	if !strings.Contains(hint, "Machine") {
		t.Errorf("callers must be told Machine is askable: %s", hint)
	}
	// Fix something stays last: it leads with a verb where the others are bare
	// nouns, and it is the row's catch-all rather than another build kind.
	if last := seed.IntakeForm[0].Options[len(seed.IntakeForm[0].Options)-1]; last != "Fix something" {
		t.Errorf("the catch-all belongs at the end, got %q", last)
	}
}
