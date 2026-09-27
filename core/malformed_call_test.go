package core

import (
	"context"
	"testing"
)

// A tool call the provider dropped as malformed leaves only the text before
// it, which read as a finished reply ending on its own lead-in. It is now
// retried with the reason, and the retry's reply is the answer.
func TestADroppedMalformedCallIsRetried(t *testing.T) {
	if geminiStopReason("MALFORMED_FUNCTION_CALL") != stopMalformedCall || geminiStopReason("STOP") != "stop" {
		t.Fatal("Gemini's malformed-call finish should map to the malformed-call stop")
	}
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "Now, to set up your Briefing Bot, I need a couple more details:", StopReason: stopMalformedCall},
		{Content: "Which board should it post to, and how often?", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "Briefing Bot"}}, h.wire(AgentLoopConfig{MaxRounds: 6}))
	if err != nil {
		t.Fatal(err)
	}
	if stub.Calls() != 2 || !h.sawDiag("malformed-call-retried") {
		t.Fatalf("one retry: calls=%d diags=%v", stub.Calls(), h.diags)
	}
	if resp == nil || resp.Content != "Which board should it post to, and how often?" {
		t.Errorf("the retry's reply is the answer: %+v", resp)
	}
}
