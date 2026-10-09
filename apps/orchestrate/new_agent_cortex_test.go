package orchestrate

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An agent Builder creates without saying decides for itself whether it
// reads its Cortex, the way the wizard's two kinds do: a chat assistant
// resumes its standing thread, a specialist starts clean. The focused kinds
// are a sub-agent, an agent with an intake form, and one whose memory is
// framed for lessons ("agent"); the rest are assistants.
func TestANewAgentReadsItsCortexWhenItIsAChatAssistant(t *testing.T) {
	cases := []struct {
		name string
		rec  AgentRecord
		want bool
	}{
		{"a plain conversational agent", AgentRecord{Name: "Helper"}, true},
		{"a chatbot-memory agent", AgentRecord{Name: "Helper", MemoryMode: "chatbot"}, true},
		{"an app's chat agent", AgentRecord{Name: "Helper", OwningApp: "crate-club"}, true},
		{"a task agent", AgentRecord{Name: "Prober", MemoryMode: "agent"}, false},
		{"a sub-agent", AgentRecord{Name: "Prober", OwnedBy: "parent"}, false},
		{"an agent with an intake form", AgentRecord{Name: "Report", IntakeForm: IntakeFormSpec{{Name: "topic"}}}, false},
	}
	for _, c := range cases {
		if got := newAgentReadsCortex(c.rec); got != c.want {
			t.Errorf("%s: reads cortex = %v, want %v", c.name, got, c.want)
		}
	}
	// Saying so wins over the default either way.
	rec := agentRecordFromArgs(map[string]any{"name": "Prober", "memory_mode": "agent", "cortex": true})
	if !rec.Cortex {
		t.Errorf("cortex: true on a task agent was not kept")
	}
}

// An app's agent is reached inside its app, so the dashboard draws no card
// for it even when it is published: the same agent twice, once without the
// app around it, is what the card would be.
func TestAnAppsAgentGetsNoDashboardCard(t *testing.T) {
	T := slugClashFixture(t)
	UserDB(T.DB, "alice").Set(agentsTable, "app-brain", AgentRecord{ID: "app-brain", Name: "Crate Club brain", OwningApp: "crate-club", Everyone: true, ShowOnDashboard: true})
	for _, c := range T.DashboardCards(slugViewer(t, "alice")) {
		if c.Name == "Crate Club brain" {
			t.Fatalf("an app's agent got a dashboard card: %+v", c)
		}
	}
	for _, e := range T.ListExposedAgents() {
		if e.AgentID == "app-brain" && !e.AppOwned {
			t.Errorf("the pool does not mark the app's agent as the app's")
		}
	}
}
