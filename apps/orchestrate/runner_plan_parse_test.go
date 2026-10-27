package orchestrate

import (
	"strings"
	"testing"
)

// A plan whose every step only replies is refused; a step that does work is
// not, however its brief happens to be worded.
func TestVacuousPlan(t *testing.T) {
	cases := []struct {
		name  string
		steps []PlanStep
		want  bool
	}{
		{"reply-only step", []PlanStep{{Title: "Respond to user", Intent: "respond directly"}}, true},
		{"brief names respond_directly", []PlanStep{{Title: "Wrap up", Intent: "close out", WorkerBrief: "Call respond_directly with the summary."}}, true},
		{"tools only respond_directly", []PlanStep{{Title: "Reply to the user", Tools: []string{"respond_directly"}}}, true},
		// Observed: a one-step probe refused on a phrase in its brief.
		{"tool step with a reply phrase in its brief", []PlanStep{{
			Title:       "Probe the endpoint the app will call",
			Intent:      "Find a working model and request shape.",
			Tools:       []string{"workspace"},
			WorkerBrief: "Try each model and report what works in your final response.",
		}}, false},
		{"no tools, reply phrase only in the brief", []PlanStep{{
			Title:       "Check the release notes",
			Intent:      "Find what changed in v2.",
			WorkerBrief: "Summarize it in your final response.",
		}}, false},
		{"one reply step after real work", []PlanStep{
			{Title: "Search the docs", Tools: []string{"web_search"}},
			{Title: "Reply to the user"},
		}, false},
	}
	for _, c := range cases {
		if got := looksLikeVacuousPlan(c.steps) != ""; got != c.want {
			t.Errorf("%s: vacuous=%v, want %v", c.name, got, c.want)
		}
	}
}

// The report: "sometimes the agent uses ask_user with multiple-choice forms but
// I don't get any dropdown, it just says Choose answer with nothing to select."
//
// type=select renders a <select> whose only entry is the "— choose —"
// placeholder. The control appears, looks answerable, and is not: there is
// nothing to pick and the turn sits parked on the question. A text field is the
// safe degradation, so the type is dropped rather than the step.
func TestSelectStepWithNoOptionsFallsBackToAText(t *testing.T) {
	step, warn := normalizeFormStep(map[string]any{
		"question": "Which environment?",
		"type":     "select",
	})
	if got, ok := step["type"]; ok {
		t.Errorf("step kept type=%v with no options — that renders an empty dropdown", got)
	}
	if warn == "" {
		t.Error("the step changed shape with no breadcrumb; nobody can attribute that afterward")
	}
	if !strings.Contains(warn, "Which environment?") {
		t.Errorf("warning does not name the step: %s", warn)
	}
}

// The same step WITH options is exactly what the dropdown is for.
func TestSelectStepWithOptionsKeepsItsType(t *testing.T) {
	step, warn := normalizeFormStep(map[string]any{
		"question": "Which environment?",
		"type":     "select",
		"options":  []any{"staging", "production"},
	})
	if step["type"] != "select" {
		t.Errorf("type = %v, want select", step["type"])
	}
	if warn != "" {
		t.Errorf("unexpected warning: %s", warn)
	}
	if got := step["options"].([]string); len(got) != 2 || got[0] != "staging" {
		t.Errorf("options = %v", got)
	}
}

// A set of choices under a near-miss key is not a question with no answers.
// Reading only the documented key rendered it as one and stranded the turn.
func TestOptionsAreReadFromTheKeysModelsActuallyWrite(t *testing.T) {
	for _, key := range []string{"options", "choices", "opts", "values", "answers"} {
		step, warn := normalizeFormStep(map[string]any{
			"question": "Pick one",
			"type":     "select",
			key:        []any{"a", "b"},
		})
		if step["type"] != "select" {
			t.Errorf("options under %q were not found — step degraded to %v (%s)", key, step["type"], warn)
		}
	}
}

// stringSliceFromArgs already coerces the option VALUES; this pins that the
// alias lookup does not lose that. Object-shaped options are what smaller
// models emit when pattern-matching a SelectOption shape.
func TestObjectShapedOptionsUnderAnAliasStillCount(t *testing.T) {
	step, _ := normalizeFormStep(map[string]any{
		"question": "Pick one",
		"type":     "select",
		"choices": []any{
			map[string]any{"label": "Staging"},
			map[string]any{"value": "Production"},
		},
	})
	got, _ := step["options"].([]string)
	if len(got) != 2 || got[0] != "Staging" || got[1] != "Production" {
		t.Errorf("options = %v, want the two labels", got)
	}
}

// An unknown type has always fallen through to the client's own choice/text
// decision. That is right and must stay — "choice" is not in the enum, and a
// step that names it with options should still render as choices.
func TestUnknownTypeIsLeftForTheRenderer(t *testing.T) {
	step, warn := normalizeFormStep(map[string]any{
		"question": "Pick one",
		"type":     "choice",
		"options":  []any{"a", "b"},
	})
	if _, ok := step["type"]; ok {
		t.Errorf("unknown type was passed through as %v", step["type"])
	}
	if warn != "" {
		t.Errorf("unexpected warning for a type the renderer handles: %s", warn)
	}
	if got, _ := step["options"].([]string); len(got) != 2 {
		t.Errorf("options were lost: %v", got)
	}
}

// ask_user takes the same tolerance: choices under a near-miss key used to make
// a multiple-choice question render as bare prose with the options gone.
func TestAskUserOptionsShareTheAliasTolerance(t *testing.T) {
	for _, key := range []string{"options", "choices", "values"} {
		got := formStepOptions(map[string]any{"question": "Pick", key: []any{"yes", "no"}})
		if len(got) != 2 {
			t.Errorf("options under %q were not read: %v", key, got)
		}
	}
}

// An ask_user question that arrives as a list, the question first and its
// choices after it, is drawn as a question with buttons rather than as raw
// JSON. Observed from a model that sent the whole multiple-choice question as
// one JSON-encoded string under "questions".

func TestAQuestionSentAsAListKeepsItsChoices(t *testing.T) {
	q, opts := askQuestionArgs(map[string]any{
		"questions": `["Which best describes where the pain is?", "upper right side, under your ribs", "lower right side", "it moves around"]`,
	})
	if q != "Which best describes where the pain is?" || len(opts) != 3 || opts[1] != "lower right side" {
		t.Errorf("a JSON-encoded list: question %q, options %v", q, opts)
	}

	q, opts = askQuestionArgs(map[string]any{"question": []any{"Which repo?", "api", "web"}})
	if q != "Which repo?" || len(opts) != 2 {
		t.Errorf("a real list: question %q, options %v", q, opts)
	}

	// Choices given as options win over the list's.
	_, opts = askQuestionArgs(map[string]any{"question": `["Pick one?", "a", "b"]`, "options": []any{"x", "y", "z"}})
	if strings.Join(opts, ",") != "x,y,z" {
		t.Errorf("options should win: %v", opts)
	}

	// Several questions in a list are one question in parts, not a question
	// and its choices.
	q, opts = askQuestionArgs(map[string]any{"questions": []any{"Where does it hurt?", "Since when?"}})
	if q != "Where does it hurt?\nSince when?" || len(opts) != 0 {
		t.Errorf("several questions: question %q, options %v", q, opts)
	}

	// The documented shape, and a bracket that is just text, are unchanged.
	if q, opts = askQuestionArgs(map[string]any{"question": "Which feed?", "options": []any{"news", "sports"}}); q != "Which feed?" || len(opts) != 2 {
		t.Errorf("the documented shape: %q %v", q, opts)
	}
	if q, _ = askQuestionArgs(map[string]any{"question": "[draft] ok to send?"}); q != "[draft] ok to send?" {
		t.Errorf("a bracket in prose was read as a list: %q", q)
	}
}

// TestSanitizePlanText covers the "messed-up plan" symptom: a model handed a
// plan-step field a JSON-ENCODED value (literal \n and a stray trailing ",")
// instead of plain text. The sanitizer decodes it at the parse boundary.
func TestSanitizePlanText(t *testing.T) {
	// The exact leaked value the user reported (literal backslash-n, trailing ",).
	leaked := `What kind of notification bridge do you want to create?\n\n1. A different service?\n2. Modify the existing one?\n\nTell me what to monitor.",`
	got := sanitizePlanText(leaked)
	if strings.Contains(got, `\n`) {
		t.Errorf("literal \\n not decoded: %q", got)
	}
	if strings.HasSuffix(got, `",`) || strings.HasSuffix(got, `"`) {
		t.Errorf("stray trailing quote/comma not stripped: %q", got)
	}
	if !strings.Contains(got, "What kind of notification bridge") || !strings.Contains(got, "\n\n") {
		t.Errorf("expected decoded multi-line text, got %q", got)
	}

	// Clean input must pass through untouched.
	for _, clean := range []string{
		"Find the API docs",
		"Verify the credential works, then wire the tool",
		"", // empty stays empty
	} {
		if got := sanitizePlanText(clean); got != strings.TrimSpace(clean) {
			t.Errorf("clean text altered: %q -> %q", clean, got)
		}
	}

	// A legitimate trailing comma (no quote) must survive — only the exact
	// quote+comma leak signature is stripped.
	if got := sanitizePlanText("do X, then Y,"); got != "do X, then Y," {
		t.Errorf("legit trailing comma stripped: %q", got)
	}
}
