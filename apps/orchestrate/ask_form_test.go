package orchestrate

import (
	"context"
	"strings"
	"testing"
)

// A form that asks nothing is refused, not relayed: the turn used to end on
// "Form relayed (0 steps)" with the agent waiting on a blank form. Steps sent
// as a valid JSON string still go through.
func TestAFormWithNoQuestionsIsRefused(t *testing.T) {
	pr, _ := emitFixture()
	cancelled := false
	pr.cancelOrch = func() { cancelled = true }
	form := pr.askUserFormToolDef().Handler

	_, err := form(context.Background(), map[string]any{"steps": `[{"question": "Name?", "options": ["A", "B"]`})
	if err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("broken JSON should be refused with the reason: %v", err)
	}
	if _, err := form(context.Background(), map[string]any{"steps": []any{}}); err == nil {
		t.Error("an empty form was relayed")
	}
	if cancelled || len(pr.capturedFormSteps) != 0 {
		t.Fatal("a refused form ended the turn or was captured")
	}

	out, err := form(context.Background(), map[string]any{"steps": `[{"question": "Name?", "options": ["A", "B"]}]`})
	if err != nil || !strings.Contains(out, "1 step") || !cancelled {
		t.Errorf("a valid string-encoded form should still go through: %q %v", out, err)
	}
}

// Several questions with their own choices sent to ask_user are drawn as the
// form they are. Read as one question, the person was shown the raw JSON:
// observed in Builder, as a list of objects under "question".
func TestQuestionsWithTheirOwnChoicesAreAForm(t *testing.T) {
	sent := []any{
		map[string]any{"question": "Should the lead model handle the lyric rewriting?", "options": []any{"on", "off"}},
		map[string]any{"question": "How should the agent get lyrics?", "options": []any{"web_search", "user must provide lyrics"}},
	}
	for name, args := range map[string]map[string]any{
		"a list":        {"question": sent},
		"a JSON string": {"questions": `[{"question": "Should the lead model handle the lyric rewriting?", "options": ["on", "off"]}, {"question": "How should the agent get lyrics?", "options": ["web_search", "user must provide lyrics"]}]`},
	} {
		pr, _ := emitFixture()
		cancelled := false
		pr.cancelOrch = func() { cancelled = true }
		out, err := pr.askUserToolDef().Handler(context.Background(), args)
		if err != nil || !cancelled || !strings.Contains(out, "2 steps") {
			t.Fatalf("%s: should be relayed as a form: %q %v", name, out, err)
		}
		if len(pr.capturedFormSteps) != 2 || pr.capturedQuest != "" {
			t.Fatalf("%s: captured %d step(s) and question %q", name, len(pr.capturedFormSteps), pr.capturedQuest)
		}
		if q := pr.capturedFormSteps[1]["question"]; q != "How should the agent get lyrics?" {
			t.Errorf("%s: second step question %v", name, q)
		}
		if opts, _ := pr.capturedFormSteps[0]["options"].([]string); len(opts) != 2 {
			t.Errorf("%s: the first step lost its choices: %#v", name, pr.capturedFormSteps[0]["options"])
		}
	}
	// A list of plain strings is still one question with its choices.
	pr, _ := emitFixture()
	pr.cancelOrch = func() {}
	pr.askUserToolDef().Handler(context.Background(), map[string]any{"question": []any{"Which repo?", "api", "web"}})
	if len(pr.capturedFormSteps) != 0 || pr.capturedQuest != "Which repo?" {
		t.Errorf("a plain list became a form: steps %d question %q", len(pr.capturedFormSteps), pr.capturedQuest)
	}
	// A list mixing the two is not clearly a form: left a question.
	if steps := askFormSteps(map[string]any{"question": []any{"Which repo?", map[string]any{"question": "Which branch?"}}}); steps != nil {
		t.Errorf("a mixed list became a form: %v", steps)
	}
}
