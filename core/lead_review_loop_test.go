package core

import (
	"context"
	"strings"
	"testing"
)

func searchTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{Name: "search", Description: "test search", Parameters: map[string]ToolParam{"q": {Type: "string"}}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return "Two results: the 9:40 train is cancelled; the 10:10 runs on time.", nil
		},
	}
}

// A turn that did work goes past the lead's review before its reply goes out.
// Sent back, the model gets the changes and answers again, labelled as a
// revision; approved, the reply goes out. The reviewer is given the request,
// the reply and what the call returned.
func TestALeadReviewSendsAReplyBackAndTheRevisionGoesOut(t *testing.T) {
	stub := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "s1", Name: "search", Args: map[string]any{"q": "trains"}}}},
		{Content: "Your train is on time."},
		{Content: "The 9:40 is cancelled; take the 10:10, which is on time.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	var seen []TurnClaimEvidence
	var diags, labels []string
	resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "is my 9:40 train running?"}}, AgentLoopConfig{
		MaxRounds: 8,
		Tools:     []AgentToolDef{searchTool()},
		TurnReview: func(ev TurnClaimEvidence) (bool, string, bool) {
			seen = append(seen, ev)
			if len(seen) == 1 {
				return true, "1. The search says the 9:40 is cancelled; the reply says it is on time.", true
			}
			return false, "", true
		},
		OnDiag:         func(kind, detail string) { diags = append(diags, kind) },
		SettleRound:    func() {},
		LabelNextRound: func(l string) { labels = append(labels, l) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("reviewed %d time(s), want the draft and the revision", len(seen))
	}
	first := seen[0]
	if first.Reply != "Your train is on time." || !strings.Contains(first.Request, "9:40") ||
		!strings.Contains(first.ReturnsBlock(), "cancelled") {
		t.Errorf("the reviewer was not given the turn: %+v", first)
	}
	if resp == nil || !strings.Contains(resp.Content, "10:10") {
		t.Errorf("the revision did not go out: %+v", resp)
	}
	corrected := false
	for _, m := range stub.Sent(2) {
		if m.Role == "user" && strings.Contains(m.Content, "sends it back") && strings.Contains(m.Content, "9:40 is cancelled") {
			corrected = true
		}
	}
	if !corrected {
		t.Error("the model was not given the reviewer's changes")
	}
	if !strings.Contains(strings.Join(diags, " "), "lead-review-sent-back") || len(labels) != 1 || labels[0] != reviewedLabel {
		t.Errorf("the trail or the label is missing: diags=%v labels=%v", diags, labels)
	}
}

// Only turns that did work are reviewed, a review that could not be had lets
// the reply go, and a reply sent back twice goes out the third time with a
// note saying so.
func TestALeadReviewIsBoundedAndNeverHoldsAReplyUp(t *testing.T) {
	run := func(turns []FakeTurn, tools []AgentToolDef, review func(TurnClaimEvidence) (bool, string, bool)) (*Response, []string, int) {
		stub := &FakeLLM{Turns: turns}
		app := &AppCore{LLM: stub, LeadLLM: stub}
		var diags []string
		resp, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: "hi"}}, AgentLoopConfig{
			MaxRounds: 10, Tools: tools, TurnReview: review,
			OnDiag: func(kind, detail string) { diags = append(diags, kind) },
		})
		if err != nil {
			t.Fatal(err)
		}
		return resp, diags, stub.Calls()
	}
	reviews := 0
	count := func(send bool, ok bool) func(TurnClaimEvidence) (bool, string, bool) {
		return func(TurnClaimEvidence) (bool, string, bool) { reviews++; return send, "1. Fix it.", ok }
	}

	resp, _, _ := run([]FakeTurn{{Content: "Hello!", Repeat: true}}, nil, count(true, true))
	if reviews != 0 || resp.Content != "Hello!" {
		t.Errorf("a turn that ran nothing was reviewed (%d) or held: %+v", reviews, resp)
	}

	worked := []FakeTurn{{ToolCalls: []ToolCall{{ID: "s1", Name: "search", Args: map[string]any{"q": "x"}}}}, {Content: "Done.", Repeat: true}}
	reviews = 0
	resp, _, calls := run(worked, []AgentToolDef{searchTool()}, count(true, false))
	if reviews != 1 || resp.Content != "Done." || calls != 2 {
		t.Errorf("a review that could not be had held the reply: reviews=%d calls=%d %+v", reviews, calls, resp)
	}

	reviews = 0
	resp, diags, _ := run(worked, []AgentToolDef{searchTool()}, count(true, true))
	if reviews != maxCorrectionsPerKind || resp.Content != "Done." {
		t.Errorf("reviews=%d, want %d before the reply goes out anyway: %+v", reviews, maxCorrectionsPerKind, resp)
	}
	if !strings.Contains(strings.Join(diags, " "), "lead-review-uncorrected") {
		t.Errorf("the trail does not say the last version went out unreviewed: %v", diags)
	}
}
