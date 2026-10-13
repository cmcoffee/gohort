package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/messaging"
	"github.com/cmcoffee/gohort/core/ui"
)

func familyFixture(t *testing.T) (*OrchestrateApp, Database) {
	t.Helper()
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	prevHook := messaging.ChannelAgentFor
	messaging.ChannelAgentFor = channelAgentFor
	t.Cleanup(func() { messaging.ChannelAgentFor = prevHook })
	for _, a := range []AgentRecord{
		{ID: "a-writer", Name: "Writer", Owner: "alice", OrchestratorPrompt: "p"},
		{ID: "a-critic", Name: "Critic", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-writer"},
		{ID: "a-nit", Name: "Nitpicker", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-critic"},
	} {
		if _, err := saveAgent(udb, a); err != nil {
			t.Fatal(err)
		}
	}
	return app, udb
}

// A channel is its top-level agent's: saved against a sub-agent (two levels
// down, even), it lands on the parent; one bound before that moves at start-up.
func TestAChannelBelongsToTheTopLevelAgent(t *testing.T) {
	familyFixture(t)
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "alice", Name: "Team", AgentID: "a-nit"})
	if ch, _ := GetChannel(RootDB, "alice", "c1"); ch.AgentID != "a-writer" {
		t.Errorf("bound to %q, want the top-level parent", ch.AgentID)
	}

	// One bound before the rule: written past the hook, as an old binary did.
	messaging.ChannelAgentFor = nil
	SaveChannel(RootDB, Channel{ID: "c2", Owner: "alice", Name: "Old", AgentID: "a-critic"})
	messaging.ChannelAgentFor = channelAgentFor
	moveSubAgentChannels()
	if ch, _ := GetChannel(RootDB, "alice", "c2"); ch.AgentID != "a-writer" {
		t.Errorf("an existing sub-agent channel was not moved: %q", ch.AgentID)
	}
}

// Binding one from a sub-agent's page says where it goes instead.
func TestBindingAChannelToASubAgentNamesTheParent(t *testing.T) {
	app, _ := familyFixture(t)
	r := httptest.NewRequest(http.MethodPost, "/api/channels?agent_id=a-critic", strings.NewReader(`{"name":"Team"}`))
	w := httptest.NewRecorder()
	app.handleChannels(w, asUser(r, "alice"))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Writer") {
		t.Errorf("binding to a sub-agent: %d %s", w.Code, w.Body.String())
	}
}

// The Scheduler on any member of a family lists the whole family's
// schedules: the parent's shows what its sub-agents run, and a sub-agent's
// shows the family it belongs to.
func TestTheSchedulerListsTheWholeFamily(t *testing.T) {
	app, _ := familyFixture(t)
	for _, sa := range []StandingAgent{
		{Owner: "alice", Name: "Morning digest", AgentID: "a-nit", Mission: "m", IntervalSeconds: 3600},
		{Owner: "alice", Name: "Weekly review", AgentID: "a-writer", Mission: "m", IntervalSeconds: 3600},
		{Owner: "alice", Name: "Someone else", AgentID: "a-unrelated", Mission: "m", IntervalSeconds: 3600},
	} {
		SaveStandingAgent(RootDB, sa)
	}
	for _, agent := range []string{"a-writer", "a-critic"} {
		r := httptest.NewRequest(http.MethodGet, "/api/console/scheduler?agent="+agent, nil)
		w := httptest.NewRecorder()
		app.handleConsoleScheduler(w, asUser(r, "alice"))
		body := w.Body.String()
		if !strings.Contains(body, "Morning digest") || !strings.Contains(body, "Weekly review") {
			t.Errorf("%s's Scheduler does not list the family: %s", agent, body)
		}
		if strings.Contains(body, "Someone else") {
			t.Errorf("%s's Scheduler lists an agent outside the family", agent)
		}
		if strings.Count(body, `"name":"Morning digest"`) != 1 {
			t.Errorf("%s's Scheduler lists a schedule twice", agent)
		}
	}
}

// A scheduled run by a sub-agent (a recurring task's fire, a monitor's wake)
// reports to its top-level parent's home thread as well, saying which
// sub-agent ran it; a top-level agent's report goes nowhere extra.
func TestASubAgentsScheduledReportReachesTheParent(t *testing.T) {
	_, udb := familyFixture(t)
	reportToParent(udb, "a-nit", ChatMessage{Role: "assistant", Content: "Three typos fixed.", ReportFrom: "Nightly pass", ReportKind: cortexKindScheduled})
	sess, ok := loadChatSession(udb, "a-writer", cortexSessionID("a-writer"))
	if !ok || len(sess.Messages) != 1 {
		t.Fatalf("the parent's home thread did not get the report: %+v", sess.Messages)
	}
	m := sess.Messages[0]
	if m.Content != "Three typos fixed." || m.ReportFrom != "Nightly pass (by Nitpicker)" || m.ReportKind != cortexKindScheduled {
		t.Errorf("report card = %+v", m)
	}
	reportToParent(udb, "a-writer", ChatMessage{Role: "assistant", Content: "own report"})
	if sess, _ := loadChatSession(udb, "a-writer", cortexSessionID("a-writer")); len(sess.Messages) != 1 {
		t.Error("a top-level agent's own report was copied to itself")
	}
}

// A sub-agent's page has no Scheduler or Channels of its own: the page maps
// each sub-agent to its parent (hide_flag on the Scheduler, the Channels
// section's channels_hide_flag), refreshed with the agent list. A sub-agent
// whose parent is gone is top-level again and keeps both.
func TestASubAgentsPageHidesSchedulerAndChannels(t *testing.T) {
	app, udb := familyFixture(t)
	if _, err := saveAgent(udb, AgentRecord{ID: "a-orphan", Name: "Orphan", Owner: "alice", OrchestratorPrompt: "p", OwnedBy: "a-deleted"}); err != nil {
		t.Fatal(err)
	}
	got := subAgentParentsFor(listAgents(udb, "alice"))
	if got["a-critic"] != "a-writer" || got["a-nit"] != "a-critic" {
		t.Errorf("sub-agents not mapped to their parents: %v", got)
	}
	for _, id := range []string{"a-writer", "a-orphan"} {
		if _, hidden := got[id]; hidden {
			t.Errorf("%s is top-level but would lose its Scheduler and Channels", id)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/api/agent-options", nil)
	w := httptest.NewRecorder()
	app.handleAgentPickerOptions(w, asUser(r, "alice"))
	if !strings.Contains(w.Body.String(), `"sub_agent_of":{`) || !strings.Contains(w.Body.String(), `"a-critic":"a-writer"`) {
		t.Errorf("the agent list refresh does not carry the map: %s", w.Body.String())
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	app.handleChatPage(w, asUser(r, "alice"))
	page := w.Body.String()
	for _, want := range []string{`window.ORCH_SUB_AGENT_OF = {`, `"channels_hide_flag":"ORCH_SUB_AGENT_OF"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the chat page is missing %s", want)
		}
	}
	// Only the agent's own Scheduler goes: the fleet-wide one in the Fleet
	// menu lists everybody's and stays.
	var hidden []string
	for _, item := range navItemsOf(t, page) {
		if item.HideFlag == "ORCH_SUB_AGENT_OF" {
			hidden = append(hidden, fmt.Sprintf("%s pinned=%v menu=%q", item.Label, item.Pinned, item.Menu))
		}
	}
	if len(hidden) != 1 || hidden[0] != `Scheduler pinned=true menu=""` {
		t.Errorf("hidden for sub-agents: %v, want only the pinned Scheduler", hidden)
	}
}

// navItemsOf reads the orchestrator nav out of the rendered chat page's
// panel config.
func navItemsOf(t *testing.T, page string) []ui.OrchestratorNavItem {
	t.Helper()
	i := strings.Index(page, `"orchestrator_nav":[`)
	if i < 0 {
		t.Fatal("no orchestrator_nav on the chat page")
	}
	var items []ui.OrchestratorNavItem
	dec := json.NewDecoder(strings.NewReader(page[i+len(`"orchestrator_nav":`):]))
	if err := dec.Decode(&items); err != nil {
		t.Fatalf("orchestrator_nav: %v", err)
	}
	return items
}
