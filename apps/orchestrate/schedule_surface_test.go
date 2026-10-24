// Every scheduled thing reports into its agent's cortex (console_surface.go).
// There was a choice per schedule (the cortex, the session it was made in, or
// nowhere), and the session's only use was a conversation the schedule
// outlives.
package orchestrate

import (
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAScheduleReportsIntoItsAgentsCortex(t *testing.T) {
	if got := scheduleSession("agent-1", "sess-42"); got != cortexSessionID("agent-1") {
		t.Errorf("a fire made in a session lands in %q, want the cortex", got)
	}
	// A record from before schedules named an agent has only its session.
	if got := scheduleSession("", "sess-42"); got != "sess-42" {
		t.Errorf("a record with no agent lands in %q", got)
	}
}

// An agent without a cortex gets one at its first fire, as attaching a
// channel gives it one. A sub-agent does not: what reaches it is its
// parent's.
func TestAFireGivesATopLevelAgentACortex(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u")
	parent, err := saveAgent(udb, AgentRecord{Name: "Parent", Owner: "u", OrchestratorPrompt: "You plan."})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := saveAgent(udb, AgentRecord{Name: "Helper", Owner: "u", OwnedBy: parent.ID, OrchestratorPrompt: "You help."})
	if err != nil {
		t.Fatal(err)
	}
	T.ensureScheduleCortex("u", parent.ID)
	T.ensureScheduleCortex("u", sub.ID)
	if a, _ := loadAgent(udb, parent.ID); !a.Cortex {
		t.Error("the agent was not given a cortex for its schedule to report into")
	}
	if a, _ := loadAgent(udb, sub.ID); a.Cortex {
		t.Error("a sub-agent was given a cortex of its own")
	}
}

// The fire says where it came from, as a link back to the session the
// schedule was made in, and says nothing for one made in the cortex.
func TestAFireLinksTheSessionItWasMadeIn(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u")
	if _, err := saveChatSession(udb, ChatSession{ID: "s-build", AgentID: "a1", Title: "Build [nightly] watch"}); err != nil {
		t.Fatal(err)
	}
	note := T.scheduleSourceNote(udb, "a1", "s-build")
	if !strings.HasPrefix(note, "Scheduled from [Build (nightly) watch](") || !strings.Contains(note, "?agent=a1&session=s-build)") {
		t.Errorf("source note: %q", note)
	}
	for _, home := range []string{"", cortexSessionID("a1"), "s-gone"} {
		if got := T.scheduleSourceNote(udb, "a1", home); got != "" {
			t.Errorf("home %q should carry no note, got %q", home, got)
		}
	}
}

// A recurring task runs in the cortex whatever it was set to; a background
// job's result coming back is not a schedule and returns to its session.
func TestARecurringFireRunsInTheCortexAndATaskWakeDoesNot(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u")
	a, err := saveAgent(udb, AgentRecord{Name: "Watcher", Owner: "u", OrchestratorPrompt: "You watch."})
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range []string{"", "session", "background", "cortex"} {
		fr := &fireRun{app: T, udb: udb, p: orchUpdatePayload{AgentID: a.ID, Username: "u", SessionID: "s1", Prompt: "check the queue", Surface: surface}}
		fr.loadThread()
		if fr.loadSession != cortexSessionID(a.ID) {
			t.Errorf("surface %q ran in %q, want the cortex", surface, fr.loadSession)
		}
	}
	wake := &fireRun{app: T, udb: udb, p: orchUpdatePayload{AgentID: a.ID, Username: "u", SessionID: "s1", Prompt: "done " + taskWakeMarker}}
	wake.loadThread()
	if wake.loadSession != "s1" {
		t.Errorf("a task wake ran in %q, want the session that started the job", wake.loadSession)
	}
}

// Nothing asks where a schedule reports any more: not the tools.
func TestTheToolsTakeNoDestination(t *testing.T) {
	for _, f := range []string{"operator_schedule_tool.go", "operator_monitor_tools.go"} {
		src, _ := os.ReadFile(f)
		for _, gone := range []string{`"surface":`, `"to":`, `"move"`, "recurringMove"} {
			if strings.Contains(string(src), gone) {
				t.Errorf("%s still has %s", f, gone)
			}
		}
	}
	src, _ := os.ReadFile("operator_schedule_tool.go")
	if !strings.Contains(string(src), "write `then` to stand on its own") {
		t.Error("the schedule tool should say a fire runs without the conversation")
	}
}

// The reported failure: editing a schedule kept producing a SECOND task with the
// same name. Two causes, one test each below — matching required the task to be
// homed in the calling thread (a cortex-homed task is invisible from a
// conversation), and it compared the whole directive (a reworded edit never
// matches).
func TestARenamedRetimeEditsTheCortexHomedTaskInsteadOfDuplicatingIt(t *testing.T) {
	// The task as it lives after being created in — or moved to — the cortex.
	homed := orchUpdatePayload{
		AgentID:   "wiwee",
		SessionID: cortexSessionID("wiwee"),
		Name:      "build watch",
		Prompt:    "Check the build. Post only if it is red, with the failing job.",
		Surface:   "cortex",
	}
	// A re-issue from an ordinary conversation, same name, reworded directive.
	if !recurringEditTarget(homed, "Build Watch", "check the build and tell me when it breaks") {
		t.Error("a re-issue under the same name did not match the cortex-homed task — this is the duplicate-per-edit bug")
	}
	// A task with no name still matches on an identical directive.
	unnamed := orchUpdatePayload{AgentID: "wiwee", SessionID: "sess-9", Prompt: "poll the queue depth"}
	if !recurringEditTarget(unnamed, "", " poll the queue depth ") {
		t.Error("an unnamed task should still be matched by its identical prompt")
	}
	// And a genuinely different task is left alone.
	if recurringEditTarget(homed, "deploy watch", "watch the deploy") {
		t.Error("a different name and a different directive must create a second task, not replace this one")
	}
	// An empty prompt matches nothing — it must not sweep up an unnamed task.
	if recurringEditTarget(unnamed, "", "   ") {
		t.Error("an empty directive matched a task; a schedule call with no prompt should replace nothing")
	}
}

// The server refuses a web write into an agent's cortex, whatever the page
// does, except the answer to a question the agent asked there.
func TestTheCortexRefusesWebWritesButTakesAnAnswer(t *testing.T) {
	cortex := cortexSessionID("wren")
	asked := ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "[scheduled] morning check"},
		{Role: "assistant", Content: "Which feed?", ToolCalls: []PersistedToolCall{{Name: "ask_user"}}},
	}}
	answered := ChatSession{Messages: append(append([]ChatMessage{}, asked.Messages...), ChatMessage{Role: "user", Content: "the news one"})}
	plain := ChatSession{Messages: []ChatMessage{{Role: "assistant", Content: "Posted the digest."}}}
	switch {
	case !refusesWebWrite("wren", cortex, plain):
		t.Error("a message typed into the cortex is refused")
	case refusesWebWrite("wren", cortex, asked):
		t.Error("the answer to the agent's own question goes through")
	case !refusesWebWrite("wren", cortex, answered):
		t.Error("once answered, the cortex is closed again")
	case refusesWebWrite("wren", "some-session", plain), refusesWebWrite("wren", "", plain):
		t.Error("an ordinary session is untouched")
	case refusesWebWrite("wren", cortexSessionID("other"), plain):
		t.Error("only this agent's own cortex id counts")
	}
}

// A fire appends to the cortex without taking a turn there, and the cortex
// takes no web turns, so its storage is bounded on the append: messages
// already folded into the summary and archive are dropped, the rest kept, and
// the fold cursor moved with them.
func TestAFireBoundsTheCortexItAppendsTo(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	T := &OrchestrateApp{AppCore: AppCore{DB: root}}
	udb := UserDB(root, "u")
	a, err := saveAgent(udb, AgentRecord{Name: "Reporter", Owner: "u", OrchestratorPrompt: "You report.", Cortex: true})
	if err != nil {
		t.Fatal(err)
	}
	sid := cortexSessionID(a.ID)
	keep := storedHistoryCap
	if !GetEmbeddingConfig().Enabled {
		keep = storedHistoryCapNoEmbed
	}
	n := keep + storedHistorySlack + 10
	sess := ChatSession{ID: sid, AgentID: a.ID}
	for i := 0; i < n; i++ {
		sess.Messages = append(sess.Messages, ChatMessage{Role: "assistant", Content: "report"})
	}
	if _, err := saveChatSession(udb, sess); err != nil {
		t.Fatal(err)
	}
	saveCompactState(udb, a.ID, sid, CompactState{SummarizedThrough: n - 20, Summary: "earlier reports"})
	if err := T.appendToThread(udb, a.ID, sid, ChatSession{}, ChatMessage{Role: "assistant", Content: "the new one"}); err != nil {
		t.Fatal(err)
	}
	got, _ := loadChatSession(udb, a.ID, sid)
	if len(got.Messages) != keep || got.Messages[len(got.Messages)-1].Content != "the new one" {
		t.Errorf("stored %d messages, want %d ending in the new card", len(got.Messages), keep)
	}
	if st := loadCompactState(udb, a.ID, sid); st.SummarizedThrough != n-20-(n+1-keep) {
		t.Errorf("fold cursor %d did not move with the trim", st.SummarizedThrough)
	}
}
