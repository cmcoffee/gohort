package orchestrate

// Builder asked for where nobody is watching (a channel message, a wake, a
// schedule) waits for the owner as one request. Run there before, it made new
// things but every edit to something that existed queued on its own and was
// refused, and approving those later redid nothing. Approved, the request runs
// Builder with its edits allowed and its result goes back where it was asked.

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

func builderQueued(t *testing.T) []Authorization {
	t.Helper()
	var out []Authorization
	for _, a := range ListAuthorizations(RootDB, "u") {
		if a.Action == "" && a.Agent == "Builder" {
			out = append(out, a)
		}
	}
	return out
}

func TestBuilderAskedForWithNobodyWatchingWaitsForTheOwner(t *testing.T) {
	turn, _, ctx := builderAsks(t)
	run := map[string]any{"agent": "seed-builder", "message": "make a tool that lists the maps"}

	note, held := turn.queueAskedDispatch(ctx, run)
	if !held {
		t.Fatal("Builder from a turn nobody watches ran instead of waiting for the owner")
	}
	if !strings.Contains(note, "queued for the user's approval") || !strings.Contains(note, "Tell them it is waiting") {
		t.Errorf("the agent should be told it waits and to say so: %q", note)
	}
	q := builderQueued(t)
	if len(q) != 1 || q[0].Brief != "make a tool that lists the maps" || q[0].FromAgent != turn.agent.ID || q[0].FromSession != "chan:room-1" {
		t.Fatalf("the request should be queued with where it came from: %+v", q)
	}

	// The other door to Builder waits the same way.
	ran := false
	gated := turn.gateDelegate([]AgentToolDef{{Tool: Tool{Name: "delegate"},
		Handler: func(context.Context, map[string]any) (string, error) { ran = true; return "ran", nil }}})
	if out, _ := gated[0].Handler(ctx, map[string]any{"agent": "seed-builder", "brief": "fix the map tool"}); ran || !strings.Contains(out, "queued") {
		t.Errorf("delegate reached Builder unwatched: ran=%v %q", ran, out)
	}
	if n := len(builderQueued(t)); n != 2 {
		t.Errorf("delegate's request was not queued: %d queued", n)
	}

	// Someone watching answers Builder's questions themselves: it runs.
	turn.askVia = approveEveryChange
	if _, held := turn.holdForOwner(ToolSessionFromContext(ctx), run); held {
		t.Error("a watched turn's Builder run was held")
	}
	turn.askVia = nil

	// Another agent is not held for this: only Builder changes things.
	if _, held := turn.holdForOwner(ToolSessionFromContext(ctx), map[string]any{"agent": "Comedian", "message": "a joke"}); held {
		t.Error("a run of an agent that is not Builder was held")
	}
}

// The owner's own setting for Builder decides first: Allow runs at once, Block
// is left to the tool's refusal, Ask waits as unset does.
func TestTheOwnersBuilderSettingStillDecides(t *testing.T) {
	turn, _, ctx := builderAsks(t)
	run := map[string]any{"agent": "seed-builder", "message": "make a tool"}
	for policy, want := range map[string]bool{PolicyAllow: false, PolicyBlock: false, PolicyAsk: true} {
		SetDelegationPolicy(RootDB, "u", turn.agent.ID, "seed-builder", policy)
		if _, held := turn.holdForOwner(ToolSessionFromContext(ctx), run); held != want {
			t.Errorf("Builder set to %s: held = %v, want %v", policy, held, want)
		}
	}
}

// Approving the request is the owner's say-so for Builder's edits in that run,
// and for nothing past it: not a deletion, not another agent's run.
func TestAnApprovedBuilderRunMakesItsEdits(t *testing.T) {
	udb := changeTestStores(t)
	approved := withOwnerApprovedRun(context.Background(), "seed-builder")
	sess := &ToolSession{Username: "u", ChatSessionID: "deleg-1", DB: udb, Ctx: approved, DispatchParentAgentID: "a-wiwee"}
	approve := changeApprover(sess, "u", "seed-builder", "Builder", nil)
	if msg := approve("tool", "map_list", "Change its url.", ChangeEdit); msg != "" {
		t.Errorf("an approved run's edit was refused: %q", msg)
	}
	if msg := approve("tool", "old_tool", "Delete it.", ChangeDelete); !strings.Contains(msg, "approval queue") {
		t.Errorf("an approved run's deletion went ahead without asking: %q", msg)
	}
	other := changeApprover(&ToolSession{Username: "u", ChatSessionID: "deleg-2", DB: udb, Ctx: approved}, "u", "a-scout", "Scout", nil)
	if msg := other("tool", "map_list", "", ChangeEdit); msg == "" {
		t.Error("an agent the approved run dispatched inherited its approval")
	}
	// The authoring tools that ask on the turn read the same mark.
	turn := &chatTurn{user: "u", udb: udb, agent: AgentRecord{ID: "seed-builder", Name: "Builder"}, ctx: approved}
	if msg := turn.approveChange("app", "maps", "Change its page.", ChangeEdit); msg != "" {
		t.Errorf("an approved run's app edit was refused: %q", msg)
	}
	turn.ctx = context.Background()
	if msg := turn.approveChange("app", "maps2", "", ChangeEdit); msg == "" {
		t.Error("a run nobody approved made an edit without asking")
	}
}

// End to end: the approved request reaches Builder's edit through the real
// delegated run, so the change is made rather than queued.
func TestAnApprovedBuilderRequestChangesWhatExisted(t *testing.T) {
	// Unapproved first: an approval lasts for its session, in memory.
	for _, approved := range []bool{false, true} {
		app, udb := familyFixture(t)
		registerStandingRunner(app)
		t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
		app.LLM = &FakeLLM{Turns: []FakeTurn{
			{ToolCalls: []ToolCall{{ID: "c1", Name: "update_agent", Args: map[string]any{"id": "a-critic", "description": "Reviews the drafts."}}}},
			{Content: "Done.", Repeat: true},
		}}
		a := Authorization{Owner: "alice", Agent: "Builder", Brief: "describe the critic", FromAgent: "a-writer"}
		if approved {
			app.runApprovedDelegation(a, a.Agent)
		} else {
			RunDelegation(context.Background(), RootDB, "alice", "seed-builder", a.Brief, a.FromAgent)
		}
		critic, _ := loadAgent(udb, "a-critic")
		queued := 0
		for _, q := range ListAuthorizations(RootDB, "alice") {
			if q.Action == changeApprovalAction {
				queued++
			}
		}
		switch {
		case approved && (critic.Description != "Reviews the drafts." || queued != 0):
			t.Errorf("approved: the edit should be made, not queued (description %q, %d queued)", critic.Description, queued)
		case !approved && (critic.Description == "Reviews the drafts." || queued != 1):
			t.Errorf("not approved: the edit should wait in the queue (description %q, %d queued)", critic.Description, queued)
		}
	}
}

// From a real channel turn, the held request knows the conversation that
// asked, so the approved run's result goes back there and not to the ledger.
func TestAHeldBuilderRequestRemembersTheChatThatAsked(t *testing.T) {
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "agents", Args: map[string]any{"action": "run", "agent": "seed-builder", "message": "make a map tool"}}}},
		FakeTurn{Content: "Queued for your approval.", Repeat: true})
	df.agent.AllowBuilderDispatch = true
	if _, err := saveAgent(df.udb, df.agent); err != nil {
		t.Fatal(err)
	}
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "have Builder make a map tool",
		MessageSender: "u", SenderIsOwner: true, ReplyAuthorizedKey: "chat-1", ChannelChatID: "chat-1"})
	var held []Authorization
	for _, a := range ListAuthorizations(RootDB, "u") {
		if a.Agent == "Builder" {
			held = append(held, a)
		}
	}
	if len(held) != 1 {
		t.Fatalf("the channel's Builder request was not held: %+v", held)
	}
	if held[0].FromSession != "room-1" || held[0].FromAgent != df.agent.ID {
		t.Errorf("the held request lost the conversation that asked: session %q agent %q", held[0].FromSession, held[0].FromAgent)
	}
}

// The same from a delegated run: the held request records the run that asked.
func TestAHeldBuilderRequestFromADelegatedRunKnowsWhereItCameFrom(t *testing.T) {
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
		{Content: "Queued.", Repeat: true},
	}}
	RunDelegation(context.Background(), RootDB, "alice", "a-writer", "get Builder to make a map tool", "")
	var held []Authorization
	for _, a := range ListAuthorizations(RootDB, "alice") {
		if a.Agent == "Builder" {
			held = append(held, a)
		}
	}
	if len(held) != 1 || held[0].FromSession == "" || held[0].FromAgent != "a-writer" {
		t.Fatalf("the delegated run's Builder request should be held with where it came from: %+v", held)
	}
}
