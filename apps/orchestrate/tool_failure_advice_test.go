package orchestrate

import (
	"strings"
	"testing"
)

// A broken tool leads the agent to offer the user a Builder fix, instead of
// retrying it or going around it. Asked, never done unasked; nothing for a
// contact's work or for Builder itself.
func TestABrokenToolLeadsToABuilderOffer(t *testing.T) {
	if toolFailureAdvice(AgentRecord{ID: "seed-builder"}, true, nil) != nil {
		t.Error("Builder fixes tools; it needs no offer")
	}

	fleet := AgentRecord{ID: "wren", Fleet: true}
	advise := toolFailureAdvice(fleet, true, nil)
	if a := advise("make_song", ""); a != "" {
		t.Errorf("a failure that could be the arguments is not advised the first time: %q", a)
	}
	a := advise("make_song", "")
	for _, want := range []string{"make_song looks broken", "it failed again", "Do not keep retrying", "direct API calls", "ask_user", `agents(action="run", agent="builder"`, "Fix the make_song tool"} {
		if !strings.Contains(a, want) {
			t.Errorf("the second failure should advise %q:\n%s", want, a)
		}
	}
	if again := advise("make_song", "it crashed"); again != "" {
		t.Error("each tool is advised once per run")
	}
	if s := advise("fetch_news", "it crashed, timed out or the service failed"); !strings.Contains(s, "(it crashed, timed out or the service failed)") {
		t.Errorf("a crash or timeout is advised at once: %q", s)
	}

	reply := toolFailureAdvice(fleet, false, nil)("make_song", "it crashed")
	if !strings.Contains(reply, "in your reply") || !strings.Contains(reply, "offer to have Builder fix it") || strings.Contains(reply, "ask_user") {
		t.Errorf("with no card to ask on, the offer goes in the reply: %s", reply)
	}

	contact := true
	fromContact := toolFailureAdvice(fleet, false, func() bool { return contact })
	if s := fromContact("make_song", "it crashed"); s != "" {
		t.Errorf("a contact's work gets no Builder offer: %q", s)
	}

	grounded := toolFailureAdvice(AgentRecord{ID: "plain", DispatchMode: dispatchNone}, true, nil)("make_song", "it crashed")
	if strings.Contains(grounded, "agents(action=") || !strings.Contains(grounded, "Builder can fix make_song") {
		t.Errorf("an agent that cannot reach Builder points the user there instead: %s", grounded)
	}
}

// A call made inside another agent keeps its label through the save, and the
// export shows it, so a sub-agent's edits do not read as the agent's own.
func TestADelegatedCallKeepsItsLabelInTheExport(t *testing.T) {
	turn := &chatTurn{}
	turn.recordToolCall(toolCallRecord{Name: "tool_def", Args: map[string]any{"action": "update"}, Result: "ok", Label: "↳ [Builder] tool_def"})
	turn.recordToolCall(toolCallRecord{Name: "web_search", Result: "ok"})
	turn.toolMu.Lock()
	calls := turn.persistedToolCallsFromUnlocked(0)
	turn.toolMu.Unlock()
	if len(calls) != 2 || calls[0].Label != "↳ [Builder] tool_def" || calls[1].Label != "" {
		t.Fatalf("the label should survive the save: %+v", calls)
	}
	md := renderSessionMarkdown(AgentRecord{ID: "wren", Name: "Wren"}, ChatSession{Messages: []ChatMessage{
		{Role: "user", Content: "fix it"},
		{Role: "assistant", Content: "done", ToolCalls: calls},
	}}, true)
	if !strings.Contains(md, "`↳ [Builder] tool_def(") || !strings.Contains(md, "`web_search(") {
		t.Errorf("the export should name who made each call:\n%s", md)
	}
}
