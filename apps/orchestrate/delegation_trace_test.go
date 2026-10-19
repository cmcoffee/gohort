package orchestrate

// A channel turn that delegated showed a bare agents/run: what the agent it
// asked went and did was recorded and then dropped, because the channel path
// saves its calls from the model transcript, where they never appear. They are
// now saved beneath the call that ran them, as a web turn saves them.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestADelegationOnAChannelTurnKeepsWhatTheAgentDid(t *testing.T) {
	df := newDispatchFixture(t,
		FakeTurn{ToolCalls: []ToolCall{{ID: "call-run", Name: "agents", Args: map[string]any{"action": "run", "agent": "Scout", "message": "find the map list"}}}},
		// Scout's model numbers its calls as the parent's does: the same id
		// must not make its call the agents call.
		FakeTurn{ToolCalls: []ToolCall{{ID: "call-run", Name: "recall", Args: map[string]any{"query": "maps"}}}},
		FakeTurn{Content: "Done.", Repeat: true})
	if _, err := saveAgent(df.udb, AgentRecord{Name: "Scout", Owner: "u", OrchestratorPrompt: "You find things."}); err != nil {
		t.Fatal(err)
	}
	df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "what maps are there?", MessageSender: "u"})

	s := df.thread("room-1")
	var calls []PersistedToolCall
	for _, m := range s.Messages {
		if m.Role == "assistant" {
			calls = m.ToolCalls
		}
	}
	var run, found *PersistedToolCall
	for i := range calls {
		switch calls[i].Name {
		case "agents":
			if run != nil {
				t.Errorf("the agents call was kept twice: %+v", calls)
			}
			run = &calls[i]
		case "recall":
			if found != nil {
				t.Errorf("Scout's one call was kept twice: %+v", calls)
			}
			found = &calls[i]
		}
	}
	if run == nil || run.ID != "call-run" {
		t.Fatalf("the reply should keep the agents call under the model's id: %+v", calls)
	}
	if found == nil {
		t.Fatalf("what Scout ran was not kept on the reply: %+v", calls)
	}
	if found.Parent != "call-run" {
		t.Errorf("Scout's call should nest under the agents call, parent %q", found.Parent)
	}
	if found.ID == "" || found.ID == run.ID {
		t.Errorf("Scout's call needs an id of its own, got %q", found.ID)
	}
	if found.Label != "↳ [Scout] recall" {
		t.Errorf("Scout's call should say whose it is: label %q", found.Label)
	}
}
