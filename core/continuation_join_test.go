package core

import (
	"context"
	"strings"
	"testing"
)

// cutThenContinueLLM answers the first call with a reply cut at the output
// limit and the second with the continuation the loop asks for.
type cutThenContinueLLM struct {
	n         int
	sawNotice bool
}

func (s *cutThenContinueLLM) Chat(ctx context.Context, m []Message, o ...ChatOption) (*Response, error) {
	s.n++
	if s.n == 1 {
		return &Response{Content: "The service listens on 8080 and its config", StopReason: "length"}, nil
	}
	for _, msg := range m {
		if msg.Role == "user" && strings.Contains(msg.Content, "CUT OFF") {
			s.sawNotice = true
		}
	}
	return &Response{Content: "lives under /etc/app. Nothing else is bound.", StopReason: "stop"}, nil
}
func (s *cutThenContinueLLM) ChatStream(ctx context.Context, m []Message, h StreamHandler, o ...ChatOption) (*Response, error) {
	return s.Chat(ctx, m, o...)
}

// A one-round call — a synthesis pass, a summary — whose only round is cut
// off used to have no way to finish: the continuation was gated on rounds to
// spare, and there were none. The fragment shipped as the report. Now the
// continuation gets its own round, and a caller that never displayed the cut
// part (no SettleRound) gets the whole reply back in one piece.
func TestCutReplyIsContinuedAndJoinedForHeadlessCaller(t *testing.T) {
	stub := &cutThenContinueLLM{}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	resp, _, err := app.RunAgentLoop(context.Background(),
		[]Message{{Role: "user", Content: "Summarize the findings."}},
		AgentLoopConfig{SystemPrompt: "Summarize.", MaxRounds: 1})
	if err != nil {
		t.Fatal(err)
	}
	if stub.n != 2 || !stub.sawNotice {
		t.Fatalf("the cut reply must be continued once with the cut-off notice: calls=%d notice=%v", stub.n, stub.sawNotice)
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
	stub := &cutThenContinueLLM{}
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
