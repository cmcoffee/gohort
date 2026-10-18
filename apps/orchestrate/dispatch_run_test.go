package orchestrate

// A continuing dispatch end to end, against the scripted model: the run a
// channel message, a monitor wake, a machine step and agents(run) all go
// through. What its callers depend on is the reply it hands back and the
// thread it keeps, so that is what these read.

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

type dispatchFixture struct {
	t      *testing.T
	app    *OrchestrateApp
	udb    Database
	agent  AgentRecord
	llm    *FakeLLM
	looked []string // the order ids the app tool was asked for
}

func newDispatchFixture(t *testing.T, turns ...FakeTurn) *dispatchFixture {
	t.Helper()
	root := depStores(t, "u")
	df := &dispatchFixture{t: t, app: orchRef, udb: UserDB(root, "u"), llm: &FakeLLM{Turns: turns}}
	df.app.LLM = df.llm
	a, err := saveAgent(df.udb, AgentRecord{Name: "Clerk", Owner: "u", OrchestratorPrompt: "You answer questions about orders."})
	if err != nil {
		t.Fatal(err)
	}
	df.agent = a
	return df
}

// lookupTool is the caller's own tool, the way an app hands one in.
func (df *dispatchFixture) lookupTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "lookup_order",
			Description: "Look up an order by id.",
			Parameters:  map[string]ToolParam{"id": {Type: "string", Description: "The order id."}},
			Required:    []string{"id"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			id, _ := args["id"].(string)
			df.looked = append(df.looked, id)
			return "order " + id + ": shipped Tuesday", nil
		},
	}
}

func (df *dispatchFixture) run(r AgentSyncRun) AgentSyncResult {
	df.t.Helper()
	if r.AgentOwner == "" {
		r.AgentOwner = "u"
	}
	if r.AgentKey == "" {
		r.AgentKey = df.agent.ID
	}
	r.AppTools = append(r.AppTools, df.lookupTool())
	res, err := df.app.RunAgentSyncContinuingRich(df.t.Context(), r)
	if err != nil {
		df.t.Fatalf("the dispatch did not run: %v", err)
	}
	return res
}

func (df *dispatchFixture) thread(id string) ChatSession {
	s, _ := loadChatSession(df.udb, df.agent.ID, id)
	return s
}

// request is the call that carried want as its live message, and that call's
// messages: the judges call the same model, so position says nothing.
func (df *dispatchFixture) request(want string) []Message {
	for i := 0; i < df.llm.Calls(); i++ {
		msgs := df.llm.Sent(i)
		if n := len(msgs); n > 0 && msgs[n-1].Role == "user" && strings.Contains(msgs[n-1].Content, want) {
			return msgs
		}
	}
	return nil
}

func lookupCall(id string) FakeTurn {
	return FakeTurn{ToolCalls: []ToolCall{{ID: "c-" + id, Name: "lookup_order", Args: map[string]any{"id": id}}}}
}

func TestADispatchAnswersAndKeepsItsThread(t *testing.T) {
	const reply = "Order A17 shipped Tuesday."
	df := newDispatchFixture(t, lookupCall("A17"), FakeTurn{Content: reply, Repeat: true})
	// By name, as a schedule or a pipeline stage names its agent.
	res := df.run(AgentSyncRun{AgentKey: "Clerk", SubSessionID: "thread-1", Message: "Where is order A17?"})

	if res.Text != reply {
		t.Errorf("reply = %q, want %q", res.Text, reply)
	}
	if len(df.looked) != 1 || df.looked[0] != "A17" {
		t.Errorf("the caller's tool was not run with the model's arguments: %v", df.looked)
	}
	if len(res.ToolCalls) == 0 || res.ToolCalls[0].Name != "lookup_order" {
		t.Errorf("the result does not carry what the turn ran: %+v", res.ToolCalls)
	}

	s := df.thread("thread-1")
	if len(s.Messages) != 2 || s.Messages[0].Role != "user" || s.Messages[0].Content != "Where is order A17?" ||
		s.Messages[1].Role != "assistant" || s.Messages[1].Content != reply {
		t.Fatalf("the thread does not hold the exchange: %+v", s.Messages)
	}
	if calls := s.Messages[1].ToolCalls; len(calls) == 0 || calls[0].Name != "lookup_order" {
		t.Errorf("the stored reply has no tool chips: %+v", calls)
	}

	// The next message on the thread is asked with the exchange before it.
	df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "And order B2?"})
	sent := df.request("And order B2?")
	if sent == nil {
		t.Fatal("the second message never reached the model")
	}
	prior := false
	for _, m := range sent[:len(sent)-1] {
		if strings.Contains(m.Content, "Where is order A17?") {
			prior = true
		}
	}
	if !prior {
		t.Errorf("the second message was asked without the thread before it: %+v", sent)
	}
	if n := len(df.thread("thread-1").Messages); n != 4 {
		t.Errorf("the thread should hold both exchanges, has %d messages", n)
	}
}

func TestAFreshSessionStartsTheThreadOver(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Noted.", Repeat: true})
	df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "Remember order A17."})
	df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "Start over.", FreshSession: true})
	s := df.thread("thread-1")
	if len(s.Messages) != 2 || s.Messages[0].Content != "Start over." {
		t.Errorf("a fresh session should hold only its own exchange: %+v", s.Messages)
	}
	for _, m := range df.request("Start over.") {
		if strings.Contains(m.Content, "Remember order A17.") {
			t.Error("a fresh session was asked with the wiped thread")
		}
	}
}

func TestADispatchWithNoThreadGetsItsOwn(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Hello.", Repeat: true})
	df.run(AgentSyncRun{Message: "Hi."})
	if s := df.thread("external-dispatch:u:" + df.agent.ID); len(s.Messages) != 2 {
		t.Errorf("a dispatch with no thread should keep one per runtime user and agent: %+v", s.Messages)
	}
}

// A channel turn records who wrote each side, and the room takes its name
// from the caller.
func TestAChannelTurnRecordsWhoSaidWhat(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "It shipped.", Repeat: true})
	res := df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "Did it ship?", MessageSender: "Alice", Title: "Alice"})
	if res.Text != "It shipped." {
		t.Errorf("reply = %q", res.Text)
	}
	s := df.thread("room-1")
	if s.Title != "Alice" {
		t.Errorf("the room is titled %q, want the caller's", s.Title)
	}
	if len(s.Messages) != 2 || s.Messages[0].Sender != "Alice" || s.Messages[1].Sender != df.agent.Name {
		t.Errorf("each side should carry its author: %+v", s.Messages)
	}
}

// A wake's input is the event that fired, kept as its card, and the thread it
// lands in takes that name when it has none.
func TestAWakeIsKeptAsItsEventCard(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "The price dropped; I told the owner.", Repeat: true})
	df.run(AgentSyncRun{SubSessionID: "wake-1", Message: "price is now 41", InputReportFrom: "Price watch", InputReportKind: "monitor"})
	s := df.thread("wake-1")
	if s.Title != "Price watch" {
		t.Errorf("the thread is titled %q, want the event's", s.Title)
	}
	if len(s.Messages) != 2 || s.Messages[0].ReportFrom != "Price watch" || s.Messages[0].ReportKind != "monitor" {
		t.Errorf("the input should be kept as the event's card: %+v", s.Messages)
	}
}

// A note the person queued while the run was going reaches the model.
func TestAMidFlightNoteReachesTheRun(t *testing.T) {
	df := newDispatchFixture(t, lookupCall("A17"), FakeTurn{Content: "Done.", Repeat: true})
	q := RegisterInjectionQueue("q-1", "u", df.agent.ID)
	t.Cleanup(func() { ReleaseInjectionQueue("q-1") })
	q.Push("Use the express rate.")
	df.run(AgentSyncRun{SubSessionID: "thread-1", InjectionQueueID: "q-1", Message: "Reship order A17."})
	for i := 0; i < df.llm.Calls(); i++ {
		for _, m := range df.llm.Sent(i) {
			if strings.Contains(m.Content, "[MID-FLIGHT NOTE") && strings.Contains(m.Content, "Use the express rate.") {
				return
			}
		}
	}
	t.Error("the queued note never reached the model")
}

// A turn that runs tools and then says nothing leaves a trail saying so.
func TestAReplyWithNothingToSayLeavesATrail(t *testing.T) {
	df := newDispatchFixture(t, lookupCall("A17"), FakeTurn{Content: "", Repeat: true})
	res := df.run(AgentSyncRun{SubSessionID: "thread-1", Message: "Check order A17."})
	if res.HitRoundCap {
		t.Skip("the loop spent its rounds; the trail is for a clean finish")
	}
	if res.Text != "" {
		t.Fatalf("want an empty reply, got %q", res.Text)
	}
	if diagsOf(df.app.DB, "u", df.agent.ID, "thread-1", "empty-reply") != 1 {
		t.Error("an empty reply left no breadcrumb")
	}
}

func TestADispatchWhoseModelFailsReturnsTheError(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Err: errors.New("model unavailable"), Repeat: true})
	_, err := df.app.RunAgentSyncContinuingRich(t.Context(), AgentSyncRun{AgentOwner: "u", AgentKey: df.agent.ID, SubSessionID: "thread-1", Message: "Hi."})
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Errorf("err = %v, want the model's", err)
	}
	if s := df.thread("thread-1"); len(s.Messages) != 0 {
		t.Errorf("a failed run stored %d message(s)", len(s.Messages))
	}
}

// What the run refuses, it refuses before asking the model anything.
func TestADispatchRefusesWhatItCannotRun(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "should not be asked", Repeat: true})
	for _, c := range []struct {
		name string
		run  AgentSyncRun
		want string
	}{
		{"no owner", AgentSyncRun{AgentKey: df.agent.ID, Message: "Hi."}, "agentOwner is required"},
		{"no message", AgentSyncRun{AgentOwner: "u", AgentKey: df.agent.ID}, "message is required"},
		{"no such agent", AgentSyncRun{AgentOwner: "u", AgentKey: "Nobody", Message: "Hi."}, "not found"},
	} {
		if _, err := df.app.RunAgentSyncContinuingRich(t.Context(), c.run); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if n := df.llm.Calls(); n != 0 {
		t.Errorf("a refused run asked the model %d time(s)", n)
	}
	df.app.LLM = nil
	if _, err := df.app.RunAgentSyncContinuingRich(t.Context(), AgentSyncRun{AgentOwner: "u", AgentKey: df.agent.ID, Message: "Hi."}); err == nil {
		t.Error("a run with no model configured was not refused")
	}
}
