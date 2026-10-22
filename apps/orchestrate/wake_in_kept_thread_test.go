package orchestrate

// A monitor wake landing in a thread someone is using: it must not answer
// their question for them, and it must not take their tools with it.
//
// Observed on a deployment with a JIRA watcher: the person asked something
// while the watcher's wake was running in the same thread. The wake had read
// the thread just after the question was saved, took it as the last thing
// said, and answered it in its own card while the person's turn answered it
// too. And every continuing turn deleted the thread's drafted tools as it
// ended, a cleanup copied from the one-shot dispatch whose thread is thrown
// away, so the tool the person had been building was gone.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAWakeDoesNotAnswerTheQuestionBeingAnswered(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Ticket ABC-1 moved to review.", Repeat: true})
	if _, err := saveChatSession(df.udb, ChatSession{ID: "thread-j", AgentID: df.agent.ID, Messages: []ChatMessage{
		{Role: "user", Content: "what is the status of the build?"},
		{Role: "assistant", Content: "Green."},
		{Role: "user", Content: "which JIRA tickets are open for me?"}, // being answered by the person's own turn
	}}); err != nil {
		t.Fatal(err)
	}
	df.run(AgentSyncRun{SubSessionID: "thread-j", Message: "ABC-1 changed", InputReportFrom: "JIRA watch", InputReportKind: "monitor"})
	sent := df.request("ABC-1 changed")
	if sent == nil {
		t.Fatal("the wake never reached the model")
	}
	var saw []string
	for _, m := range sent {
		saw = append(saw, m.Content)
	}
	all := strings.Join(saw, "\n")
	if strings.Contains(all, "which JIRA tickets are open for me?") {
		t.Error("the wake read the person's unanswered question as part of its own turn")
	}
	if !strings.Contains(all, "what is the status of the build?") || !strings.Contains(all, "Green.") {
		t.Error("the wake should still read the conversation that was answered")
	}
	kept := df.thread("thread-j")
	found := false
	for _, m := range kept.Messages {
		if m.Content == "which JIRA tickets are open for me?" {
			found = true
		}
	}
	if !found {
		t.Error("the person's question should stay in the thread")
	}
}

// A message that arrives for the agent (no report card) is a turn of its own
// and still reads everything before it.
func TestAMessageStillReadsTheWholeThread(t *testing.T) {
	got := withoutUnansweredAsk([]ChatMessage{{Role: "assistant", Content: "a"}, {Role: "user", Content: "q"}})
	if len(got) != 1 {
		t.Fatalf("a trailing unanswered ask should be held back from an arrival: %+v", got)
	}
	got = withoutUnansweredAsk([]ChatMessage{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}})
	if len(got) != 2 {
		t.Errorf("an answered exchange should be kept whole: %+v", got)
	}
	got = withoutUnansweredAsk([]ChatMessage{{Role: "assistant", Content: "a"}, {Role: "user", Content: "fired", ReportFrom: "watch"}})
	if len(got) != 2 {
		t.Errorf("an earlier event card is not a person's question: %+v", got)
	}
}

func TestAWakeKeepsTheThreadsDraftedTools(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Noted.", Repeat: true})
	if err := SaveSessionTempTool(df.udb, "thread-d", TempTool{Name: "jira_open_tickets", Description: "List my open JIRA tickets."}); err != nil {
		t.Fatal(err)
	}
	df.run(AgentSyncRun{SubSessionID: "thread-d", Message: "ABC-2 changed", InputReportFrom: "JIRA watch", InputReportKind: "monitor"})
	tools := LoadSessionTempTools(df.udb, "thread-d")
	if len(tools) != 1 || tools[0].Name != "jira_open_tickets" {
		t.Errorf("a wake in the thread deleted the tool being built there: %+v", tools)
	}
}
