package core

import (
	"context"
	"testing"
)

// A reply whose last line visibly stops is unfinished; a complete one is not,
// including a casual one with no full stop, and anything that ends a block by
// design.
func TestAReplyThatStopsMidSentenceIsCaught(t *testing.T) {
	stops := []string{
		"Okay, \"Briefing Bot\" it is!\n\nNow, to set up your Briefing Bot, I need a couple more details:",
		"The board is set up, and",
		"I checked the feed and found three stories, including",
		"Posting it to your",
		"Here are the options (",
		"It runs every morning,",
	}
	complete := []string{
		"lol that's great",
		"Sounds good",
		"Done.",
		"Which board should it post to?",
		"Paste the error message here:",
		"Tell me which one you want:",
		"Here are your options:\n- News\n- Guides",
		"```go\nfunc main() {}\n```",
		"## Summary",
		"https://example.com/post/1",
		"| name | value |",
		"I'll post it every morning at 8",
		"",
	}
	for _, r := range stops {
		if !replyEndsMidSentence(r) {
			t.Errorf("should be caught as unfinished: %q", r)
		}
	}
	for _, r := range complete {
		if replyEndsMidSentence(r) {
			t.Errorf("a complete reply was flagged: %q", r)
		}
	}
}

// The loop asks for the rest, and the finished reply is the answer.
func TestAnUnfinishedReplyIsAskedToFinish(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{
		{Content: "The board is set up, and"},
		{Content: "The board is set up, and Briefing Bot posts to it every morning.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	h := &correctionHooks{}
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "set it up"}}, h.wire(AgentLoopConfig{MaxRounds: 6}))
	if err != nil {
		t.Fatal(err)
	}
	if stub.Calls() != 2 || !h.sawDiag("unfinished-reply-corrected") {
		t.Fatalf("one request for the rest: calls=%d diags=%v", stub.Calls(), h.diags)
	}
	if resp == nil || resp.Content != "The board is set up, and Briefing Bot posts to it every morning." {
		t.Errorf("the finished reply is the answer: %+v", resp)
	}
}
