package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// gateKey is a stored credential for one test, removed after it.
func gateKey(t *testing.T, name, secret string) {
	t.Helper()
	if AuthDB == nil {
		store := &DBase{Store: kvlite.MemStore()}
		AuthDB = func() Database { return store }
		t.Cleanup(func() { AuthDB = nil })
	}
	if err := Secure().Save(SecureCredential{Name: name, Type: SecureCredBearer, BaseURL: "https://gate.test"}, secret); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Secure().Delete(name) })
}

// A consult never carries a stored key or a secret-shaped line to the lead,
// whether it came in the conversation or the evidence the worker pasted.
func TestALeadCallCarriesNoStoredKeyAndNoSecretLine(t *testing.T) {
	gateKey(t, "gate_payments", "gk_live_9a8b7c6d5e4f3a2b")
	T, _, user := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Use the v2 endpoint."}}}
	T.LLM, T.LeadLLM = fake, fake
	turn := &chatTurn{app: T, ctx: context.Background(), user: user, agent: AgentRecord{ID: "a1"},
		session: &ChatSession{Messages: []ChatMessage{{Role: "user", Content: "My key is gk_live_9a8b7c6d5e4f3a2b, why does it fail?"}}}}
	if _, err := turn.consult("Why the 401?", "Request headers:\nAuthorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789\nBody: {}"); err != nil {
		t.Fatal(err)
	}
	sent := ""
	for _, m := range fake.Sent(0) {
		sent += m.Content
	}
	if strings.Contains(sent, "gk_live_9a8b7c6d5e4f3a2b") || strings.Contains(sent, "abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Errorf("a secret reached the lead:\n%s", sent)
	}
	if !strings.Contains(sent, "[credential: gate_payments]") || !strings.Contains(sent, "Why the 401?") {
		t.Errorf("the key must be named in its place and the question kept:\n%s", sent)
	}
}

// What a tool read from the person's own computer reaches the reviewer as kept
// local; that the call ran, and the rest of the turn, still reach it.
func TestTheReviewerIsNotShownWhatTheDesktopRead(t *testing.T) {
	T, _, user := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"verdict":"APPROVE"}`}}}
	T.LLM, T.LeadLLM = fake, fake
	turn := &chatTurn{app: T, ctx: context.Background(), user: user, agent: AgentRecord{ID: "a1", LeadReview: true}}
	turn.leadReviewer(context.Background())(TurnClaimEvidence{
		Request: "Summarise my notes and the weather.", Reply: "Done.",
		ToolCalls: []string{"from_client_filesystem_read_file", "get_weather"},
		ToolOutputs: []string{
			"from_client_filesystem_read_file: Private journal entry about the doctor's visit",
			"get_weather: Sunny, 18C",
		},
	})
	p := fake.Prompt(0)
	if strings.Contains(p, "doctor's visit") {
		t.Errorf("what the desktop read reached the reviewer:\n%s", p)
	}
	if !strings.Contains(p, "from_client_filesystem_read_file: "+keptLocal) || !strings.Contains(p, "Sunny, 18C") {
		t.Errorf("the call must show as kept local and the rest must stay:\n%s", p)
	}
}

// The consultant and hybrid levels reach the lead only through the gate.
func TestEveryHybridLeadCallGoesThroughTheGate(t *testing.T) {
	for _, f := range []string{"consult.go", "lead_plan_check.go", "lead_review.go"} {
		src := readFile(t, f)
		if strings.Contains(src, ".LeadChat(") {
			t.Errorf("%s calls the lead directly, around the privacy gate", f)
		}
		if !strings.Contains(src, "leadChatGated(") {
			t.Errorf("%s no longer goes through the privacy gate", f)
		}
	}
}

// Only the consultant and hybrid levels ask the lead, automatically or by the
// consult tool, and never on a turn that may not reach it. The automatic
// consult was wired for every agent, so a Worker only agent that hit one
// failure three times still sent it to the lead.
func TestOnlyTheLevelsBesideTheWorkerConsult(t *testing.T) {
	T, _, user := newTestOrchestrate(t)
	pinRootDB(t)
	for _, c := range []struct {
		name  string
		agent AgentRecord
		priv  bool
		want  bool
	}{
		{"worker only", AgentRecord{ID: "a1"}, false, false},
		{"consultant", AgentRecord{ID: "a2", ConsultLead: settingOn}, false, true},
		{"hybrid", AgentRecord{ID: "a3", LeadReview: true}, false, true},
		{"hybrid, Private", AgentRecord{ID: "a4", LeadReview: true}, true, false},
		{"lead only", AgentRecord{ID: "a5", LeadModel: true}, false, false},
	} {
		turn := &chatTurn{app: T, ctx: context.Background(), user: user, agent: c.agent, privateMode: c.priv}
		if got := turn.leadConsult() != nil; got != c.want {
			t.Errorf("%s: automatic consult = %v, want %v", c.name, got, c.want)
		}
		offered := false
		for _, td := range turn.finalizeDispatchCatalog(&ToolSession{Username: user}, nil) {
			offered = offered || td.Tool.Name == "consult"
		}
		if offered != c.want {
			t.Errorf("%s: consult tool on a channel, schedule or dispatch = %v, want %v", c.name, offered, c.want)
		}
	}
}

// Every run an agent makes wires the lead the same way: the automatic consult
// through leadConsult, and the review, a channel message included.
func TestEveryRunWiresTheLeadByLevel(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{"runner.go", "Consult: t.leadConsult(),"},
		{"runner_worker.go", "Consult: t.leadConsult(),"},
		{"agent_dispatch.go", "Consult:      sd.subTurn.leadConsult(),"},
		{"agent_dispatch.go", "TurnReview: cr.subTurn.leadReviewer(cr.ctx),"},
		{"agent_dispatch.go", "Consult:    cr.subTurn.leadConsult(),"},
		{"agents_grouped_tool.go", "Consult:            ar.subTurn.leadConsult(),"},
		{"scheduled_updates.go", "Consult:    fr.subTurn.leadConsult(),"},
	} {
		src := readFile(t, c.file)
		if !strings.Contains(src, c.want) {
			t.Errorf("%s no longer has %q", c.file, c.want)
		}
		if strings.Contains(src, "Consult: t.consult,") || strings.Contains(src, "subTurn.consult,") {
			t.Errorf("%s consults the lead whatever the agent's level", c.file)
		}
	}
}

// A channel message's consult carries its thread: the run has no session on
// the turn, and the consultant saw the question alone.
func TestAChannelConsultCarriesTheThread(t *testing.T) {
	T, _, _ := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Ask for the order number."}}}
	T.LLM, T.LeadLLM = fake, fake
	turn := &chatTurn{app: T, ctx: context.Background(), agent: AgentRecord{ID: "a1"},
		convo: []ChatMessage{{Role: "user", Content: "Where is my parcel?"}}}
	if _, err := turn.consult("What do I need to look it up?", ""); err != nil {
		t.Fatal(err)
	}
	if p := fake.Prompt(0); !strings.Contains(p, "Person: Where is my parcel?") {
		t.Errorf("the consult did not carry the thread:\n%s", p)
	}
	if src := readFile(t, "agent_dispatch.go"); !strings.Contains(src, "cr.subTurn.convo = ") {
		t.Error("a channel run no longer hands its thread to the turn")
	}
}
