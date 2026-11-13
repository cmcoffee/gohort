package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An ask over MCP runs in its own thread: a stored session under the agent,
// titled and attributed as the client sent it, and nothing in Cortex. Run
// as a channel inbound it went into Cortex, and for an agent whose Cortex is
// its one channel's home it ran in the Cortex itself. Its prompt reads as a
// conversation, since the client waits on the reply. The threads are listed
// back by their key's prefix, whole: another key's are not among them.
func TestAnMCPTurnRunsInItsOwnThread(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Shipped on Tuesday.", Repeat: true})
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	// A dedicated-Cortex agent: Cortex on, one channel.
	df.agent.Cortex = true
	if _, err := saveAgent(df.udb, df.agent); err != nil {
		t.Fatal(err)
	}
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "sms", AgentID: df.agent.ID})

	thread := MCPThreadPrefix + ":k1:orders"
	reply, err := df.app.runMCPTurn(context.Background(), MCPTurn{Owner: "u", AgentID: df.agent.ID, Thread: thread,
		Title: "Orders", Sender: "Claude Code", Text: "Where is order A-1042?"})
	if err != nil || reply.Text != "Shipped on Tuesday." {
		t.Fatalf("the reply: %+v %v", reply, err)
	}
	s, ok := loadChatSession(df.udb, df.agent.ID, thread)
	if !ok || s.Title != "Orders" || len(s.Messages) < 2 {
		t.Fatalf("the thread: ok=%v title=%q messages=%d", ok, s.Title, len(s.Messages))
	}
	if s.Messages[0].Sender != "Claude Code" {
		t.Errorf("the ask's author: %q", s.Messages[0].Sender)
	}
	if c, ok := loadChatSession(df.udb, df.agent.ID, cortexSessionID(df.agent.ID)); ok && len(c.Messages) > 0 {
		t.Errorf("Cortex took %d message(s) from an MCP turn", len(c.Messages))
	}

	other := MCPThreadPrefix + ":k10"
	df.udb.Set(sessionTable(df.agent.ID), other, ChatSession{ID: other, AgentID: df.agent.ID})
	got := df.app.listMCPThreads("u", MCPThreadPrefix+":k1")
	if len(got) != 1 || got[0].Thread != thread || got[0].Title != "Orders" || got[0].AgentID != df.agent.ID {
		t.Errorf("the key's threads: %+v", got)
	}

	if surfaceForRun(AgentSyncRun{Kind: kindMCP}) != surfaceConversation {
		t.Error("an MCP run's prompt reads as work nobody is waiting on")
	}
}
