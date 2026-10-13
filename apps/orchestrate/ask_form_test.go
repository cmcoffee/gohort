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
