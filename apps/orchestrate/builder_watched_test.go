package orchestrate

// Builder is offered only where somebody is watching. What it changes is
// approved, one change at a time, by the person who asked, in the
// conversation they asked in; a channel message, a schedule or a monitor's
// wake has nobody there, so Builder is not an option on those turns at all.
// Who may send it work is the agent's own setting, so sending it work is never
// a request of its own, and approving any request that runs Builder does not
// approve the changes it goes on to make.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func builderAsks(t *testing.T) (turn *chatTurn, sess *ToolSession, ctx context.Context) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	turn, _ = newRunGateTurn(t, "")
	turn.agent.AllowBuilderDispatch = true
	turn.ctx = context.Background()
	sess = &ToolSession{Username: "u", ChatSessionID: "chan:room-1", AgentID: turn.agent.ID}
	return turn, sess, sess.ContextWithSession(context.Background())
}

func builderQueued(t *testing.T, owner string) []Authorization {
	t.Helper()
	var out []Authorization
	for _, a := range ListAuthorizations(RootDB, owner) {
		if a.Agent == "Builder" || a.Agent == "seed-builder" {
			out = append(out, a)
		}
	}
	return out
}

func TestBuilderIsNotOfferedWithNobodyWatching(t *testing.T) {
	turn, _, ctx := builderAsks(t)
	run := map[string]any{"agent": "seed-builder", "message": "make a tool that lists the maps"}

	if turn.canDispatchBuilder() {
		t.Error("Builder is offered on a turn nobody is watching")
	}
	if _, _, err := turn.agentsRunGate(run); err == nil || !strings.Contains(err.Error(), "offered only in a conversation the user is watching") {
		t.Errorf("an unwatched turn reached Builder: %v", err)
	}
	if _, held := turn.queueAskedDispatch(ctx, run); held {
		t.Error("sending work to Builder became a request of its own")
	}
	if q := builderQueued(t, "u"); len(q) != 0 {
		t.Errorf("a request to run Builder was queued: %+v", q)
	}

	// Somebody watching: the agent's own setting decides, and nothing asks.
	turn.askVia = approveEveryChange
	if !turn.canDispatchBuilder() {
		t.Error("Builder is not offered to a watched turn whose agent may send it work")
	}
	if _, _, err := turn.agentsRunGate(run); err != nil {
		t.Errorf("a watched turn whose agent may send Builder work was refused: %v", err)
	}
	turn.agent.AllowBuilderDispatch = false
	if turn.canDispatchBuilder() {
		t.Error("Builder is offered to an agent whose setting is off")
	}
}

// End to end from a real channel turn: Builder is not run and nothing waits
// for the owner, so the agent says what it would have changed instead.
func TestAChannelTurnHasNoBuilder(t *testing.T) {
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "agents", Args: map[string]any{"action": "run", "agent": "seed-builder", "message": "make a map tool"}}}},
		FakeTurn{Content: "Builder is not available here.", Repeat: true})
	df.agent.AllowBuilderDispatch = true
	if _, err := saveAgent(df.udb, df.agent); err != nil {
		t.Fatal(err)
	}
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "have Builder make a map tool",
		MessageSender: "u", SenderIsOwner: true, ReplyAuthorizedKey: "chat-1", ChannelChatID: "chat-1"})
	if q := builderQueued(t, "u"); len(q) != 0 {
		t.Errorf("a channel message queued Builder for the owner: %+v", q)
	}
}

// A delegated run nobody watches cannot send Builder work either.
func TestADelegatedRunNobodyWatchesHasNoBuilder(t *testing.T) {
	app, udb := familyFixture(t)
	writer, _ := loadAgent(udb, "a-writer")
	writer.AllowBuilderDispatch = true
	if _, err := saveAgent(udb, writer); err != nil {
		t.Fatal(err)
	}
	registerStandingRunner(app)
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	app.LLM = &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "agents", Args: map[string]any{"action": "run", "agent": "seed-builder", "message": "make a map tool"}}}},
		{Content: "Not available.", Repeat: true},
	}}
	RunDelegation(context.Background(), RootDB, "alice", "a-writer", "get Builder to make a map tool", "")
	if q := builderQueued(t, "alice"); len(q) != 0 {
		t.Fatalf("an unwatched delegated run queued Builder: %+v", q)
	}
}

// Approving a request that runs Builder (one queued before this change, or a
// request_build) runs it, and each change it makes to something that existed
// still waits for its own card.
func TestApprovingARunDoesNotApproveItsChanges(t *testing.T) {
	app, udb := familyFixture(t)
	registerStandingRunner(app)
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	app.LLM = &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "c1", Name: "update_agent", Args: map[string]any{"id": "a-critic", "description": "Reviews the drafts."}}}},
		{Content: "Done.", Repeat: true},
	}}
	app.runApprovedDelegation(Authorization{Owner: "alice", Agent: "Builder", Brief: "describe the critic", FromAgent: "a-writer"}, "Builder")
	critic, _ := loadAgent(udb, "a-critic")
	queued := 0
	for _, q := range ListAuthorizations(RootDB, "alice") {
		if q.Action == changeApprovalAction {
			queued++
		}
	}
	if critic.Description == "Reviews the drafts." || queued != 1 {
		t.Errorf("an approved run's change should wait for its own approval (description %q, %d queued)", critic.Description, queued)
	}
}

// The authoring tools that ask on the turn of a run another agent sent ask
// under that run's thread, for that job, and the card says so. They used to
// ask under one blank session every such run shared, so an approval given to
// one covered the next.
func TestASentRunsTurnAsksForItsJob(t *testing.T) {
	udb := changeTestStores(t)
	var bodies []string
	ask := func(prompt, detail string, yes []string) string { bodies = append(bodies, detail); return yes[0] }
	turn := &chatTurn{user: "u", udb: udb, agent: AgentRecord{ID: "seed-builder", Name: "Builder"}, ctx: context.Background(),
		changeAsk: ask, changeThread: "dispatch:conv-1:seed-builder", sentBy: "a-scout"}
	turn.approveChange("app", "maps", "Change its page.", ChangeEdit)
	turn.approveChange("app", "maps", "Change its page again.", ChangeEdit)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "this job") {
		t.Fatalf("a sent run should ask once per job, and say so: %q", bodies)
	}
	other := &chatTurn{user: "u", udb: udb, agent: AgentRecord{ID: "seed-builder", Name: "Builder"}, ctx: context.Background(),
		changeAsk: ask, changeThread: "dispatch:conv-2:seed-builder", sentBy: "a-scout"}
	other.approveChange("app", "maps", "", ChangeEdit)
	if len(bodies) != 2 {
		t.Errorf("one run's approval covered another run's: asked %d times", len(bodies))
	}
}
