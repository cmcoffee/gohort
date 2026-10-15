package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Builder runs only what it is building in this conversation: what it made,
// what it was let change, and the agent and pipeline of an app it made or
// changed. The rest of the fleet is refused, and other agents are unaffected.
func TestBuilderRunsOnlyWhatItBuilds(t *testing.T) {
	_, _, user := newTestOrchestrate(t)
	pinRootDB(t)
	turn := &chatTurn{user: user, agent: AgentRecord{ID: "seed-builder", Name: "Builder"}, session: &ChatSession{ID: "conv-1"}}
	set := changeApprovalsFor(user, "conv-1")
	set.add(changeKey("agent", "Ticket triage"))     // created here
	set.add(changeKey("pipeline", "Nightly digest")) // changed here, with approval
	SaveAppSpec(AppSpec{Owner: user, Slug: "prep", Name: "Case prep", RecordKey: "id", AgentID: "ag-support", PipelineID: "pl-extract"})
	set.add(changeKey("app", "prep"))

	for _, c := range []struct {
		kind, id, name string
		ok             bool
	}{
		{"agent", "ag-1", "Ticket triage", true},
		{"pipeline", "pl-9", "Nightly digest", true},
		{"agent", "ag-support", "Support", true},    // the app's agent
		{"pipeline", "pl-extract", "Extract", true}, // the app's pipeline
		{"agent", "ag-guru", "TechGuru", false},     // the fleet
		{"machine", "m-1", "Nightly digest", false}, // same name, other kind
	} {
		why := turn.builderRunRefusal(c.kind, c.id, c.name)
		if (why == "") != c.ok {
			t.Errorf("%s %s: allowed=%v, want %v (%s)", c.kind, c.name, why == "", c.ok, why)
		}
		if !c.ok && !strings.Contains(why, "do the job yourself") && !strings.Contains(why, "do it yourself") {
			t.Errorf("the refusal does not say to do the work itself: %s", why)
		}
	}
	// Another conversation has built nothing.
	other := &chatTurn{user: user, agent: turn.agent, session: &ChatSession{ID: "conv-2"}}
	if other.builderRunRefusal("agent", "ag-1", "Ticket triage") == "" {
		t.Error("another conversation ran what this one built")
	}
	// Any other agent delegates as before.
	chat := &chatTurn{user: user, agent: AgentRecord{ID: "ag-chat", Name: "Chat"}, session: &ChatSession{ID: "conv-1"}}
	if chat.builderRunRefusal("agent", "ag-guru", "TechGuru") != "" {
		t.Error("a non-Builder agent was limited")
	}
}

// Builder's agents tool offers run, worded as testing its own work.
func TestBuilderAgentsToolOffersTestRuns(t *testing.T) {
	turn := &chatTurn{user: "alice", agent: AgentRecord{ID: "seed-builder", Name: "Builder"}}
	def := turn.agentsGroupedToolDef(true)
	if !strings.Contains(def.Tool.Description, "test") || !strings.Contains(def.Tool.Description, "ONLY those") {
		t.Errorf("description: %s", def.Tool.Description)
	}
}
