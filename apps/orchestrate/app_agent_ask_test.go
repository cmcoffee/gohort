package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An app's page asks the app's agent with the agent's instructions and rules,
// a bounded reply and NO tools, whatever tools the agent holds: the page may
// use the agent's voice, never its hands.
func TestAnAppAskUsesTheAgentsVoiceButNoTools(t *testing.T) {
	root := depStores(t, "alice")
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Greetings, traveler."}}}
	app := &OrchestrateApp{AppCore: AppCore{DB: root, LLM: fake}}
	saveAgent(UserDB(root, "alice"), AgentRecord{ID: "npc", Owner: "alice", Name: "Innkeeper",
		OrchestratorPrompt: "You are a gruff innkeeper.", Rules: "Never break character.",
		AllowedTools: []string{"message_contact", "fetch_url"}})

	text, _, err := app.AppAgentAsk(context.Background(), "alice", "", "Innkeeper", "Greet the player.", true)
	if err != nil || text != "Greetings, traveler." {
		t.Fatalf("ask: %q %v", text, err)
	}
	p := fake.Prompt(0)
	for _, want := range []string{"gruff innkeeper", "Never break character.", "user: Greet the player."} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	cfg := fake.Config(0)
	if len(cfg.Tools) != 0 {
		t.Errorf("an app ask carried %d tool(s): it must carry none", len(cfg.Tools))
	}
	if cfg.MaxTokens != appAskMaxTokens || !cfg.JSONMode {
		t.Errorf("max tokens %d, json %v", cfg.MaxTokens, cfg.JSONMode)
	}
	if _, _, err := app.AppAgentAsk(context.Background(), "alice", "", "nobody", "hi", false); err == nil {
		t.Error("an ask to a missing agent answered")
	}
	if _, _, err := app.AppAgentAsk(context.Background(), "bob", "", "Innkeeper", "hi", false); err == nil {
		t.Error("another user's agent answered for bob's app")
	}
}

// An ask is a run in the ledger, under the person who asked, so the live pill
// shows the app's agent working: it answered every click with the pill dark.
func TestAnAppAskIsARunUnderWhoAsked(t *testing.T) {
	root := depStores(t, "alice")
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Bullish."}, {Content: "x"}}}
	app := &OrchestrateApp{AppCore: AppCore{DB: root, LLM: fake}}
	saveAgent(UserDB(root, "alice"), AgentRecord{ID: "hot", Owner: "alice", Name: "Hot Take", OrchestratorPrompt: "Trader."})

	if _, _, err := app.AppAgentAsk(context.Background(), "alice", "bob", "hot", "Take on NVDA", false); err != nil {
		t.Fatal(err)
	}
	runs := app.runsRegistry().Activity("bob")
	if len(runs) != 1 {
		t.Fatalf("want one run for bob, got %d", len(runs))
	}
	r := runs[0]
	if r.Kind != "ask" || r.AgentName != "Hot Take" || r.Label != "Take on NVDA" || r.Status != RunStatusCompleted {
		t.Errorf("run: %+v", r)
	}
	if len(app.runsRegistry().Activity("alice")) != 0 {
		t.Error("the run was filed under the owner, whose pill would show bob's prompt")
	}
}
