package orchestrate

import (
	"context"
	"strings"
	"testing"
)

// The consultant sees the request, the recent conversation and what the
// caller pastes, never its tools. A question is required; evidence is not,
// since a judgement ("which approach?") may have none, and the request and the
// conversation always go with it (consultContext), so the prompt is never
// empty.
func TestConsultToolRequiresAQuestion(t *testing.T) {
	td := consultTool(&chatTurn{})
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"no question", map[string]any{"evidence": "HTTP 400 ..."}, "question is required"},
		{"blank question", map[string]any{"question": "  ", "evidence": "x"}, "question is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := td.Handler(context.Background(), tc.args); err == nil {
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
	if len(td.Tool.Required) != 1 || td.Tool.Required[0] != "question" {
		t.Errorf("only the question is required: %v", td.Tool.Required)
	}
}

// The consultant knows the case: the person's request and the conversation
// just before it go with every question, clipped, without the worker pasting
// them. A turn with no conversation sends the question alone.
func TestTheConsultantSeesTheConversation(t *testing.T) {
	sess := &ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "Plan a weekend in Fresno."},
		{Role: "assistant", Content: "Which weekend?"},
		{Role: "user", Content: "This one, and keep it cheap."},
	}}
	got := (&chatTurn{session: sess}).consultContext()
	for _, want := range []string{"Person: Plan a weekend in Fresno.", "Agent: Which weekend?", "Person: This one, and keep it cheap."} {
		if !strings.Contains(got, want) {
			t.Errorf("the consultant's context lacks %q:\n%s", want, got)
		}
	}
	if (&chatTurn{}).consultContext() != "" {
		t.Error("a turn with no conversation should add nothing")
	}
}
