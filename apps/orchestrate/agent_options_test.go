package orchestrate

import "testing"

func TestPickerAgentsRetiresSeedsExceptBuilder(t *testing.T) {
	agents := []AgentRecord{
		{ID: "seed-chat", Owner: seedOwner},
		{ID: "seed-builder", Owner: seedOwner},
		{ID: "seed-research", Owner: "alice"}, // shadowed seed: Owner rewritten, still a seed
		{ID: "ag-1", Owner: "alice", Name: "My Assistant"},
	}

	got := pickerAgents(agents)
	ids := []string{}
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	if len(got) != 2 || got[0].ID != "seed-builder" || got[1].ID != "ag-1" {
		t.Errorf("picker should keep Builder + own agents only; got %v", ids)
	}
}

// The picker files an agent by its kind, not by whether it reads its Cortex:
// a chat assistant with reading off is still conversational, and a
// specialist that reads its reports is still specialized. Reading still puts
// the agent's Cortex row on the page.
func TestThePickerGroupsByKindNotCortex(t *testing.T) {
	agents := []AgentRecord{
		{ID: "a-chat", Name: "Chatty", MemoryMode: memoryModeChatbot},
		{ID: "a-watch", Name: "Watcher", MemoryMode: memoryModeAgent, Cortex: true},
		{ID: "a-unset", Name: "Unset", Cortex: true},
	}
	opts, cortex, _ := agentPickerOptions(agents)
	group := map[string]string{}
	for _, o := range opts {
		group[o.Value] = o.Group
	}
	want := map[string]string{"a-chat": "Conversation Agents", "a-watch": "Specialized Agents", "a-unset": "Specialized Agents"}
	for id, g := range want {
		if group[id] != g {
			t.Errorf("%s filed under %q, want %q", id, group[id], g)
		}
	}
	if _, ok := cortex["a-watch"]; !ok {
		t.Error("a specialist that reads its Cortex lost its Cortex row")
	}
	if _, ok := cortex["a-chat"]; ok {
		t.Error("an assistant with reading off was given a Cortex row")
	}
}
