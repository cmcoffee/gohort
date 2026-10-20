package orchestrate

// A run whose caller has the person there can ask them: an app's own window
// (Scribe's Discuss) gets the question and its choices back, the thread keeps
// the question, and nothing is relayed as though another agent had asked.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestARunWithThePersonThereCanAskThem(t *testing.T) {
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "q1", Name: "ask_user", Args: map[string]any{
			"question": "Who reads this guide?", "options": []any{"Operators", "New hires"}}}}},
		FakeTurn{Content: "Wrote it anyway.", Repeat: true})
	res := df.run(AgentSyncRun{AgentOwner: "u", AgentKey: df.agent.ID, SubSessionID: "discuss-1",
		Message: "help me write the brief", AskUser: true})
	if res.Question != "Who reads this guide?" || strings.Join(res.Options, "|") != "Operators|New hires" {
		t.Fatalf("the caller should get the question and its choices: %+v", res)
	}
	if df.llm.Calls() != 1 {
		t.Errorf("the turn went on after asking: %d model calls", df.llm.Calls())
	}
	if strings.Contains(res.Text, "needs an answer") || strings.Contains(res.Text, `agents(action="run")`) {
		t.Errorf("a question for the person there was dressed as a relay: %q", res.Text)
	}
	s := df.thread("discuss-1")
	if n := len(s.Messages); n == 0 || !strings.Contains(s.Messages[n-1].Content, "Who reads this guide?") {
		t.Errorf("the thread should keep the question, so the answer reads as a reply: %+v", s.Messages)
	}

	// Without AskUser there is no ask_user, and nothing comes back as a question.
	plain := newDispatchFixture(t, FakeTurn{Content: "Here it is.", Repeat: true})
	if r := plain.run(AgentSyncRun{AgentOwner: "u", AgentKey: plain.agent.ID, SubSessionID: "x", Message: "hi"}); r.Question != "" {
		t.Errorf("a run without AskUser asked: %+v", r)
	}
	for _, td := range plain.llm.Config(0).Tools {
		if td.Name == "ask_user" {
			t.Error("a run without AskUser was offered ask_user")
		}
	}
}
