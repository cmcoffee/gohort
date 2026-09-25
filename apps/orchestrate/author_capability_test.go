package orchestrate

import "testing"

// agentCanAuthor is Builder's alone: the seed authors by identity, and the
// retired Author flag grants nothing to any other agent. Fleet is irrelevant.
func TestAgentCanAuthor(t *testing.T) {
	cases := []struct {
		name string
		rec  AgentRecord
		want bool
	}{
		{"builder seed authors by identity", AgentRecord{ID: "seed-builder"}, true},
		{"builder seed authors even without flag", AgentRecord{ID: "seed-builder", Author: false}, true},
		{"plain agent cannot author", AgentRecord{ID: "abc", Name: "Chat"}, false},
		{"the retired flag grants nothing", AgentRecord{ID: "abc", Author: true}, false},
		{"fleet alone does not grant authoring", AgentRecord{ID: "abc", Fleet: true}, false},
		{"flag + fleet grants nothing", AgentRecord{ID: "abc", Fleet: true, Author: true}, false},
	}
	for _, c := range cases {
		if got := agentCanAuthor(c.rec); got != c.want {
			t.Errorf("%s: agentCanAuthor = %v, want %v", c.name, got, c.want)
		}
	}
}
