package orchestrate

// A tool that reaches past the audited hook (raw network, a persistent shell,
// or a key handed to its script) asks the person watching before its first
// call in a conversation.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestReachPastHookAsksOncePerConversation(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	tools := []*TempTool{
		{Name: "db_query", CommandTemplate: "psql -c x", RawNetwork: true},
		{Name: "repl", Mode: TempToolModePersistent, CommandTemplate: "redis-cli"},
		{Name: "signer", CommandTemplate: "python3 s.py", HookCapabilities: []string{"secret:prod_db"}},
		{Name: "both", CommandTemplate: "python3 s.py", RawNetwork: true, HookCapabilities: []string{"secret:a", "secret:b"}},
		{Name: "read", CommandTemplate: "cat x", HookCapabilities: []string{"fetch"}},
	}
	answer := "Allow for this session"
	var prompts []string
	ask := func(prompt, d string, yes []string) string {
		prompts = append(prompts, prompt)
		for _, y := range yes {
			if y == answer {
				return y
			}
		}
		return ""
	}
	turn := func(conv string) *chatTurn {
		return &chatTurn{user: "alice", udb: udb, agent: AgentRecord{ID: "a1", Owner: "alice"},
			askConversation: conv, askVia: ask}
	}
	sess := &ToolSession{Username: "alice", TempTools: tools}
	c := turn("conv-1").confirmFuncFor(sess)

	want := map[string]string{
		"db_query": "It has raw network access.",
		"repl":     "It has raw network access.",
		"signer":   `It is handed the key "prod_db".`,
		"both":     `It has raw network access and is handed the keys "a" and "b".`,
	}
	for name, sentence := range want {
		prompts = nil
		if !c(name, "{}") {
			t.Errorf("%s: allowed on the card, still refused", name)
		}
		if len(prompts) != 1 || !strings.Contains(prompts[0], name) || !strings.Contains(prompts[0], sentence) {
			t.Errorf("%s: card %q, want it to say %q", name, prompts, sentence)
		}
		prompts = nil
		if !c(name, "{}") || len(prompts) != 0 {
			t.Errorf("%s: allowed for the session, asked again: %q", name, prompts)
		}
	}

	// A tool that stays inside the hook runs unasked.
	prompts = nil
	if !c("read", "{}") || len(prompts) != 0 {
		t.Errorf("a hook-only tool asked: %q", prompts)
	}

	// Another conversation asks again; Deny refuses.
	answer = "Deny"
	prompts = nil
	if turn("conv-2").confirmFuncFor(sess)("db_query", "{}") {
		t.Error("denied on the card, still allowed")
	}
	if len(prompts) != 1 {
		t.Errorf("another conversation: asked %d times, want 1", len(prompts))
	}
}

// With nobody watching, the chat check refuses rather than approves; a run
// that is meant to be unattended goes through the autonomous gate instead.
func TestReachPastHookWithNoViewerIsRefused(t *testing.T) {
	turn, sess := confirmTurn(t, &TempTool{Name: "db_query", CommandTemplate: "psql", RawNetwork: true})
	if turn.confirmFuncFor(sess)("db_query", "{}") {
		t.Error("a raw-network call was approved with nobody to ask")
	}
}
