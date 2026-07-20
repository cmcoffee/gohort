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
