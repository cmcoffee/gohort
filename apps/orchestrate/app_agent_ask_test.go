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

	text, _, err := app.AppAgentAsk(context.Background(), "alice", "Innkeeper", "Greet the player.", true)
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
	if _, _, err := app.AppAgentAsk(context.Background(), "alice", "nobody", "hi", false); err == nil {
		t.Error("an ask to a missing agent answered")
	}
	if _, _, err := app.AppAgentAsk(context.Background(), "bob", "Innkeeper", "hi", false); err == nil {
		t.Error("another user's agent answered for bob's app")
	}
}
