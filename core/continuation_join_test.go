package core

import (
	"context"
	"strings"
	"testing"
)

// cutThenContinueLLM answers the first call with a reply cut at the output
// limit and the second with the continuation the loop asks for.
// A one-round call — a synthesis pass, a summary — whose only round is cut
// off used to have no way to finish: the continuation was gated on rounds to
// spare, and there were none. The fragment shipped as the report. Now the
// continuation gets its own round, and a caller that never displayed the cut
// part (no SettleRound) gets the whole reply back in one piece.
func TestCutReplyIsContinuedAndJoinedForHeadlessCaller(t *testing.T) {
	// Cut mid-sentence, then finishing it on the continuation round.
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "The service listens on 8080 and its config", StopReason: "length"},
		{Content: "lives under /etc/app. Nothing else is bound.", StopReason: "stop", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	resp, _, err := app.RunAgentLoop(context.Background(),
		[]Message{{Role: "user", Content: "Summarize the findings."}},
		AgentLoopConfig{SystemPrompt: "Summarize.", MaxRounds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stub.Calls() != 2 {
		t.Fatalf("the cut reply must be continued exactly once: calls=%d", stub.Calls())
	}
	// The continuation has to CARRY the notice, or the model is being asked to
	// resume with no idea it was interrupted. Asserted here, on what the loop
	// actually sent, rather than inside a stub where it was invisible.
	if !strings.Contains(stub.Prompt(1), "CUT OFF") {
		t.Fatalf("the continuation round did not tell the model it was cut off:\n%s", stub.Prompt(1))
	}
	want := "The service listens on 8080 and its config lives under /etc/app. Nothing else is bound."
	if resp.Content != want {
		t.Errorf("headless caller got %q, want the joined reply %q", resp.Content, want)
	}
	if resp.HitRoundCap {
		t.Error("a continued one-round call that finished is not a round-cap hit")
	}
}

// A caller with a SettleRound already showed the cut-off part as its own
// bubble; it gets the continuation alone, as before, or the partial renders
// twice.
func TestCutReplyContinuationAloneForCallerThatSettled(t *testing.T) {
	// Cut mid-sentence, then finishing it on the continuation round.
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "The service listens on 8080 and its config", StopReason: "length"},
		{Content: "lives under /etc/app. Nothing else is bound.", StopReason: "stop", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	settled := 0
	resp, _, err := app.RunAgentLoop(context.Background(),
		[]Message{{Role: "user", Content: "Summarize the findings."}},
		AgentLoopConfig{SystemPrompt: "Summarize.", MaxRounds: 1, SettleRound: func() { settled++ }})
	if err != nil {
		t.Fatal(err)
	}
	if settled != 1 {
		t.Errorf("the partial must be settled exactly once before the continuation, got %d", settled)
	}
	if resp.Content != "lives under /etc/app. Nothing else is bound." {
		t.Errorf("a caller that settled the partial must get only the continuation, got %q", resp.Content)
	}
}

func TestJoinContinuation(t *testing.T) {
	cases := []struct{ lead, tail, want string }{
		{"and its config", "lives under /etc", "and its config lives under /etc"},
		{"and its config ", "lives under /etc", "and its config lives under /etc"},
		{"port 8080.", "\nNothing else.", "port 8080.\nNothing else."},
		{"see `/etc/app/", "config.yml`", "see `/etc/app/config.yml`"},
		{"", "whole", "whole"},
		{"whole", "", "whole"},
	}
	for _, c := range cases {
		if got := joinContinuation(c.lead, c.tail); got != c.want {
			t.Errorf("join(%q, %q) = %q, want %q", c.lead, c.tail, got, c.want)
		}
	}
}
