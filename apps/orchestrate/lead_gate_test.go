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
