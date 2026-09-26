package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestADelegatedBuilderRecordsItsQuestion(t *testing.T) {
	q := &delegatedQuestion{}
	td := delegatedAskUserTool(q)
	if _, err := td.Handler(context.Background(), map[string]any{"question": "  "}); err == nil {
		t.Error("a blank question must be refused")
	}
	if q.asked() {
		t.Fatal("a refused call recorded a question")
	}
	if _, err := td.Handler(context.Background(), map[string]any{
		"question": "Which calendar should it write to?",
		"options":  []any{"Work", "Personal"},
	}); err != nil {
		t.Fatal(err)
	}
	if !q.asked() {
		t.Fatal("the question was not recorded")
	}
	thread := q.forThread()
	if !strings.Contains(thread, "Which calendar") || !strings.Contains(thread, "Work | Personal") {
		t.Errorf("the thread should hold the question and its options: %q", thread)
	}
	relay := delegatedRelay("Builder", thread)
	for _, want := range []string{"needs an answer from the user", "Which calendar", "Work | Personal", `agents(action="run")`, "without answering it yourself"} {
		if !strings.Contains(relay, want) {
			t.Errorf("the relay to the asking agent is missing %q:\n%s", want, relay)
		}
	}
	var nilQ *delegatedQuestion
	if nilQ.asked() {
		t.Error("no question holder means nothing was asked")
	}
}

// The wrapper keeps the "Brief: " tail the active-threads hint reads, and
// offers the question instead of "make defaults".
func TestTheAskingWrapperKeepsTheBriefReadable(t *testing.T) {
	msg := markAsDelegatedMayAsk("build a calendar agent")
	if !strings.Contains(msg, "call ask_user") || strings.Contains(msg, "no back-and-forth") {
		t.Errorf("the wrapper should offer ask_user, not forbid asking:\n%s", msg)
	}
	if got := lastDispatchTopic([]ChatMessage{{Role: "user", Content: msg}}); got != "build a calendar agent" {
		t.Errorf("lastDispatchTopic = %q, want the brief alone", got)
	}
}

// The loop really stops at the question: Builder asking and then building
// anyway is the failure the round abort exists to prevent.
func TestTheDelegatedQuestionEndsTheRun(t *testing.T) {
	q := &delegatedQuestion{}
	built := false
	createAgent := AgentToolDef{
		Tool: Tool{Name: "create_agent", Parameters: map[string]ToolParam{"name": {Type: "string"}}},
		Handler: func(context.Context, map[string]any) (string, error) {
			built = true
			return "created", nil
		},
	}
	stub := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{
			{ID: "1", Name: "ask_user", Args: map[string]any{"question": "Which account?"}},
			{ID: "2", Name: "create_agent", Args: map[string]any{"name": "Guess"}},
		}},
		{Content: "Built it anyway.", Repeat: true},
	}}
	app := &AppCore{LLM: stub, LeadLLM: stub}
	_, _, err := app.RunAgentLoop(context.Background(), []Message{{Role: "user", Content: markAsDelegatedMayAsk("build it")}}, AgentLoopConfig{
		Tools:           []AgentToolDef{createAgent, delegatedAskUserTool(q)},
		RoundAbortTools: []string{"ask_user"},
		MaxRounds:       4,
		Confirm:         func(string, string) bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !q.asked() {
		t.Fatal("the question was not recorded")
	}
	if built {
		t.Error("the run built something in the same round it asked")
	}
	if n := stub.Calls(); n != 1 {
		t.Errorf("the model was called %d times; the run should end at the question", n)
	}
}

// A delegated Builder that asks in prose instead of through ask_user still asks
// the user. Tonight's reply laid out options and the calling agent chose one
// for the user; now it is relayed as the question it is.
func TestABuilderQuestionAskedInProseIsStillTheUsers(t *testing.T) {
	asks := []string{
		"I found two ways to reach Suno.\n\nOption A: a hosted wrapper at one address.\nOption B: a self-hosted wrapper.\n\nWhich should I build against?",
		"Which base URL do you want the credential to use?",
		"There are two routes. Option 1 is hosted, Option 2 you run yourself. Let me know which you prefer.",
		"Should I wire it now?**",
	}
	for _, r := range asks {
		if !endsWithQuestionForUser(r) {
			t.Errorf("should read as a question for the user: %q", r)
		}
	}
	done := []string{
		"Drafted the suno credential and built the generate tool. It is waiting for your key in Extensions.",
		"I compared Option A with others last week; this build uses the documented endpoint.",
	}
	for _, r := range done {
		if endsWithQuestionForUser(r) {
			t.Errorf("a finished report is not a question: %q", r)
		}
	}
	relay := delegatedProseRelay("Builder", asks[0])
	for _, want := range []string{"Option A", "Option B", "without choosing or answering for them", `agents(action="run")`} {
		if !strings.Contains(relay, want) {
			t.Errorf("the relay should carry the whole reply and the instruction; missing %q:\n%s", want, relay)
		}
	}
}
