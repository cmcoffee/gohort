package orchestrate

import (
	"strings"
	"testing"
)

// The consulted model sees ONLY what the caller pastes — no conversation, no
// tools. Missing evidence is therefore a hard error, not a best-effort call:
// answering from an empty prompt is how a confident wrong answer gets made.
func TestConsultToolRequiresQuestionAndEvidence(t *testing.T) {
	td := consultTool(&chatTurn{})
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no question", map[string]any{"evidence": "HTTP 400 ..."}, "question is required"},
		{"no evidence", map[string]any{"question": "What is the body?"}, "evidence is required"},
		{"blank question", map[string]any{"question": "  ", "evidence": "x"}, "question is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := td.Handler(tc.args); err == nil {
				t.Fatal("expected an error")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should say %q: %v", tc.want, err)
			}
		})
	}
}

// The cap is shared by both surfaces that consult (the tool and the loop's
// failure-shape guard), so one of each can't spend double.
func TestConsultPerTurnCap(t *testing.T) {
	turn := &chatTurn{consultCount: consultMaxPerTurn}
	_, err := turn.consult("q", "e")
	if err == nil || !strings.Contains(err.Error(), "limit reached") {
		t.Fatalf("expected the per-turn cap to refuse: %v", err)
	}
	// Refusing must not burn another slot.
	if turn.consultCount != consultMaxPerTurn {
		t.Errorf("a refused consult must not increment the count: %d", turn.consultCount)
	}
}

// Whatever comes back is advice. The tool description and the returned text
// both have to say so — this is the guard against a consulted answer being
// reported to the user as verified.
func TestConsultToolFramesAnswerAsAdvice(t *testing.T) {
	td := consultTool(&chatTurn{})
	if !strings.Contains(td.Tool.Description, "VERIFY") {
		t.Error("tool description must tell the model to verify the advice")
	}
	for _, want := range []string{"question", "evidence"} {
		if _, ok := td.Tool.Parameters[want]; !ok {
			t.Errorf("missing %q parameter", want)
		}
	}
	if len(td.Tool.Required) != 2 {
		t.Errorf("question and evidence must both be required: %v", td.Tool.Required)
	}
}
