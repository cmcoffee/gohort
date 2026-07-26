package orchestrate

import "testing"

// agentCanAuthor is the de-silo predicate: the Builder seed authors by identity;
// any other agent authors iff its Author flag is set. Fleet is irrelevant to it.
func TestAgentCanAuthor(t *testing.T) {
	cases := []struct {
		name string
		rec  AgentRecord
		want bool
	}{
		{"builder seed authors by identity", AgentRecord{ID: "seed-builder"}, true},
		{"builder seed authors even without flag", AgentRecord{ID: "seed-builder", Author: false}, true},
		{"plain agent cannot author", AgentRecord{ID: "abc", Name: "Chat"}, false},
		{"author-flagged agent can author", AgentRecord{ID: "abc", Author: true}, true},
		{"fleet alone does not grant authoring", AgentRecord{ID: "abc", Fleet: true}, false},
		{"author + fleet can author", AgentRecord{ID: "abc", Fleet: true, Author: true}, true},
	}
	for _, c := range cases {
		if got := agentCanAuthor(c.rec); got != c.want {
			t.Errorf("%s: agentCanAuthor = %v, want %v", c.name, got, c.want)
		}
	}
}
