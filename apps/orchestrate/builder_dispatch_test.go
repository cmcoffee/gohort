package orchestrate

// Builder dispatch grant: the per-agent opt-in that lifts the Fleet-only
// reservation, and the visibility half without which the grant is inert.

import "testing"

func TestCanDispatchBuilder(t *testing.T) {
	cases := []struct {
		name string
		rec  AgentRecord
		want bool
	}{
		{"plain agent", AgentRecord{}, false},
		{"fleet controller (historic rule)", AgentRecord{Fleet: true}, true},
		{"granted", AgentRecord{AllowBuilderDispatch: true}, true},
		{"both", AgentRecord{Fleet: true, AllowBuilderDispatch: true}, true},
		// The grant is independent of the authoring capability: Author has
		// the agent build things itself, this has it ask Builder to.
		{"author flag alone does not grant it", AgentRecord{Author: true}, false},
		// Builder is not one of the user's agents: the explicit grant holds
		// under Allow none, and only the explicit grant.
		{"granted, Allow none", AgentRecord{AllowBuilderDispatch: true, DispatchMode: dispatchNone}, true},
		{"fleet, Allow none", AgentRecord{Fleet: true, DispatchMode: dispatchNone}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			turn := &chatTurn{agent: c.rec}
			if got := turn.canDispatchBuilder(); got != c.want {
				t.Errorf("canDispatchBuilder() = %v, want %v", got, c.want)
			}
		})
	}
	if (*chatTurn)(nil).canDispatchBuilder() {
		t.Error("nil turn must not grant Builder dispatch")
	}
}
