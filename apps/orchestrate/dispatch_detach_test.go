package orchestrate

// Handing work off versus waiting for it. The framework decides detaching from
// duration everywhere else; here it cannot, because the question is not how
// long the sub-agent takes but whether THIS turn's reply depends on its answer.

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func TestOnlyAHandoffDispatchDetaches(t *testing.T) {
	turn := &chatTurn{}
	p := turn.agentsDispatchPolicy(true)
	if p.Always == nil {
		t.Fatal("the run action must be able to detach")
	}
	cases := []struct {
		name string
		args map[string]any
		want bool
	}{
		{"handoff", map[string]any{"action": "run", "await": false}, true},
		{"awaited", map[string]any{"action": "run", "await": true}, false},
		// Omitted must mean waiting: a caller that needed the answer and got
		// "started" instead writes its reply around a hole.
		{"omitted", map[string]any{"action": "run"}, false},
		// Some models emit booleans as strings.
		{"string false", map[string]any{"action": "run", "await": "false"}, true},
		// Only run dispatches; list/get are local reads.
		{"list", map[string]any{"action": "list", "await": false}, false},
		{"get", map[string]any{"action": "get", "await": false}, false},
	}
	for _, c := range cases {
		if got := p.Always(c.args, nil); got != c.want {
			t.Errorf("%s: detach=%v, want %v", c.name, got, c.want)
		}
	}
	// On a messaging conversation saying nothing hands off, and the agent
	// still waits when it says so.
	msg := &ToolSession{ChannelChatID: "chat-1"}
	for _, c := range []struct {
		name string
		args map[string]any
		want bool
	}{
		{"omitted on messaging", map[string]any{"action": "run"}, true},
		{"awaited on messaging", map[string]any{"action": "run", "await": true}, false},
		{"handoff on messaging", map[string]any{"action": "run", "await": false}, true},
		{"list on messaging", map[string]any{"action": "list"}, false},
	} {
		if got := p.Always(c.args, msg); got != c.want {
			t.Errorf("%s: detach=%v, want %v", c.name, got, c.want)
		}
	}
	if !p.Always(map[string]any{"action": "run"}, &ToolSession{ChannelHandle: "+15550100"}) {
		t.Error("a conversation known only by its handle is a messaging one too")
	}
}

// The read-only variant has no dispatch at all, so it must declare no policy —
// otherwise it claims a background slot for work it cannot do.
func TestTheReadOnlyAgentsToolDeclaresNoPolicy(t *testing.T) {
	if p := (&chatTurn{}).agentsDispatchPolicy(false); p.Tool != "" {
		t.Errorf("read-only agents tool declared policy %q", p.Tool)
	}
}

// Preflight runs the gates only for the calls that would detach — an awaited
// dispatch raises its own errors inline at the right moment already.
func TestPreflightOnlyGatesAHandoff(t *testing.T) {
	p := (&chatTurn{}).agentsDispatchPolicy(true)
	if p.Preflight == nil {
		t.Fatal("a detaching dispatch must be refusable before it detaches")
	}
	if err := p.Preflight(map[string]any{"action": "run", "await": true}, nil); err != nil {
		t.Errorf("an awaited dispatch must not be pre-gated: %v", err)
	}
}

func TestTheAwaitParamIsDescribedForHandoff(t *testing.T) {
	def := (&chatTurn{agent: AgentRecord{ID: "a"}}).agentsGroupedToolDef(true)
	p, ok := def.Tool.Parameters["await"]
	if !ok {
		t.Fatal("the caller needs a way to say it is not waiting")
	}
	for _, want := range []string{"TRUE (the default)", "handing the work off"} {
		if !strings.Contains(p.Description, want) {
			t.Errorf("await description missing %q:\n%s", want, p.Description)
		}
	}
	// On a messaging conversation the agent is told the default it gets there.
	msg := &chatTurn{agent: AgentRecord{ID: "a"}, dispatchSess: &ToolSession{ChannelChatID: "chat-1"}}
	md := msg.agentsGroupedToolDef(true).Tool.Parameters["await"].Description
	for _, want := range []string{"FALSE (the default on this messaging conversation)", "TRUE when your reply cannot be written"} {
		if !strings.Contains(md, want) {
			t.Errorf("messaging await description missing %q:\n%s", want, md)
		}
	}
	// The read-only variant must not advertise it.
	if _, ok := (&chatTurn{agent: AgentRecord{ID: "a"}}).agentsGroupedToolDef(false).Tool.Parameters["await"]; ok {
		t.Error("a tool that cannot dispatch must not offer to hand work off")
	}
}

// toolResult is what the agent was handed back for one of its calls.
func (df *dispatchFixture) toolResult(id string) string {
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			for _, r := range m.ToolResults {
				if r.ID == id {
					return r.Content
				}
			}
		}
	}
	return ""
}

// On a messaging conversation a delegation goes to the background unless the
// agent says it is waiting: the person sees nothing while it waits. The
// answer comes back into the conversation that asked.
func TestOnAMessagingConversationADelegationGoesToTheBackground(t *testing.T) {
	room := "room-" + UUIDv4()
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "agents", Args: map[string]any{"action": "run", "agent": "Scout", "message": "find the venue"}}}},
		FakeTurn{Content: "On it.", Repeat: true})
	if _, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You find venues."}); err != nil {
		t.Fatal(err)
	}
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	prevRunner, prevLink := TaskRunnerFunc, ConversationLinkFunc
	df.app.installTaskRunner()
	t.Cleanup(func() { TaskRunnerFunc, ConversationLinkFunc = prevRunner, prevLink })
	df.run(AgentSyncRun{Kind: "channel", SubSessionID: room, ChannelChatID: "chat-1", Message: "have Scout find the venue",
		MessageSender: "u", SenderIsOwner: true, Interactive: true})
	if out := df.toolResult("c1"); !strings.Contains(out, "STARTED, NOT FINISHED") {
		t.Fatalf("the delegation should have gone to the background: %s", out)
	}
	// The answer wakes the same thread, which replies into the same chat.
	back := func() bool {
		for _, m := range df.thread(room).Messages {
			if m.Role == "user" && strings.Contains(m.Content, "BACKGROUND TASK FINISHED") && strings.Contains(m.Content, "Result from Scout") {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(10 * time.Second)
	for !back() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !back() {
		t.Error("the delegate's answer never came back to the conversation that asked")
	}
}

// Background work is delivered where the run's session says. A run's own
// session is used only when it names the conversation that asked; a
// delegate's synthetic thread does not, and a result sent there is lost.
func TestBackgroundWorkIsDeliveredOnlyToTheConversationThatAsked(t *testing.T) {
	app := &OrchestrateApp{}
	for _, c := range []struct {
		name string
		sess *ToolSession
		want bool
	}{
		{"a messaging chat", &ToolSession{ChatSessionID: "room-1", ChannelChatID: "chat-1"}, true},
		{"told where to deliver", &ToolSession{ChatSessionID: "external-dispatch:u:a", DeliverySessionID: "sess-9"}, true},
		{"a delegate's own thread", &ToolSession{ChatSessionID: "external-dispatch:u:a"}, false},
	} {
		turn := &chatTurn{app: app, dispatchSess: c.sess}
		if got := turn.detachSession() == c.sess; got != c.want {
			t.Errorf("%s: used the run's session = %v, want %v", c.name, got, c.want)
		}
	}
	if (&chatTurn{app: app}).detachSession() == nil {
		t.Error("a turn with no run session still needs one to start from")
	}
}
