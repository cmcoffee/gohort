package orchestrate

import (
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// A thread an external caller opens is named from what was said in it, at
// once and then by a written title; a run that does not ask keeps no name.
func TestAnAPIThreadIsNamedFromItsConversation(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Order 42 ships Tuesday", Repeat: true})

	df.run(AgentSyncRun{SubSessionID: "ext:k:conv:1", Message: "hello", AutoTitle: true})
	if s := df.thread("ext:k:conv:1"); s.Title != "hello" || s.AutoTitle != "hello" {
		t.Errorf("a new thread should be named from its first message, got %q / %q", s.Title, s.AutoTitle)
	}

	df.run(AgentSyncRun{SubSessionID: "plain", Message: "hello"})
	if s := df.thread("plain"); s.Title != "" {
		t.Errorf("a run that does not ask for a name should leave none, got %q", s.Title)
	}

	const asked = "when does order 42 ship"
	df.run(AgentSyncRun{SubSessionID: "ext:k:conv:2", Message: asked, AutoTitle: true})
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := df.thread("ext:k:conv:2")
		if s.Title != "" && s.Title != titleFromFirstMessage(asked) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the thread was never given a written title, still %q", s.Title)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A caller with no thread of its own continues the conversation it is in,
// and one that went quiet starts another.
func TestACallerContinuesItsRecentConversationOnly(t *testing.T) {
	df := newDispatchFixture(t)
	tbl := sessionTable(df.agent.ID)
	put := func(id string, ago time.Duration) {
		df.udb.Set(tbl, id, ChatSession{ID: id, AgentID: df.agent.ID, LastAt: time.Now().Add(-ago)})
	}
	const prefix = "ext:k:conv:"
	if got := df.app.RecentThread("u", df.agent.ID, prefix, 30*time.Minute); got != "" {
		t.Errorf("no conversation yet, got %q", got)
	}
	put(prefix+"a", 2*time.Minute)
	put(prefix+"b", 10*time.Minute)
	put("ext:j:conv:c", time.Second) // another key's, newer
	if got := df.app.RecentThread("u", df.agent.ID, prefix, 30*time.Minute); got != prefix+"a" {
		t.Errorf("the latest of this caller's conversations should continue, got %q", got)
	}
	put(prefix+"a", 2*time.Hour)
	put(prefix+"b", time.Hour)
	if got := df.app.RecentThread("u", df.agent.ID, prefix, 30*time.Minute); got != "" {
		t.Errorf("a conversation quiet past the pause should not continue, got %q", got)
	}
}
