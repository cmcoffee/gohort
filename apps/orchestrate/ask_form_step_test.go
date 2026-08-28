package orchestrate

import (
	"strings"
	"testing"
)

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
