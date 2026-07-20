package orchestrate

import "testing"

func TestPickerAgentsFiltersSeedsForNonAdmins(t *testing.T) {
	agents := []AgentRecord{
		{ID: "seed-chat", Owner: seedOwner},
		{ID: "seed-builder", Owner: seedOwner},
		{ID: "seed-research", Owner: "alice"}, // shadowed seed: Owner rewritten, still a seed
		{ID: "ag-1", Owner: "alice", Name: "My Assistant"},
	}

	got := pickerAgents(agents, false)
	if len(got) != 1 || got[0].ID != "ag-1" {
		ids := []string{}
		for _, a := range got {
			ids = append(ids, a.ID)
		}
		t.Errorf("non-admin picker should keep only own agents; got %v", ids)
	}

	if got := pickerAgents(agents, true); len(got) != len(agents) {
		t.Errorf("admin picker must keep seeds; got %d of %d", len(got), len(agents))
	}
}
