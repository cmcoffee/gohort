package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An agent written for one of the owner's apps is filed under that app in
// every picker, by the app's name, and is otherwise an ordinary agent.
func TestAnAppsOwnAgentIsListedUnderTheApp(t *testing.T) {
	pinRootDB(t)
	SaveAppSpec(AppSpec{Owner: "u", Slug: "weather", Name: "Weather", RecordKey: "id"})
	rec := agentRecordFromArgs(map[string]any{"name": "Forecaster", "owning_app": "Weather"})
	if rec.OwningApp != "weather" {
		t.Fatalf("owning_app = %q, want the slug", rec.OwningApp)
	}
	rec.ID, rec.Owner = "a1", "u"
	if g, ok := agentAppGroup(rec); !ok || g != "App agents: Weather" {
		t.Fatalf("group = %q %v", g, ok)
	}
	if isAppAgent(rec.ID) {
		t.Fatal("an app's own agent crossed the framework app-agent boundary")
	}
	plain := AgentRecord{ID: "a2", Name: "Helper", Owner: "u"}
	if agentGroupFor(plain, "Your agents") != "Your agents" {
		t.Fatal("an ordinary agent left its group")
	}
	opts, _, _ := agentPickerOptions([]AgentRecord{rec, plain})
	groups := map[string]string{}
	for _, o := range opts {
		groups[o.Value] = o.Group
	}
	if groups["a1"] != "App agents: Weather" || groups["a2"] != "Specialized Agents" {
		t.Fatalf("picker groups = %v", groups)
	}
}

// Deleting an app names the agents and pipelines written for it, for the
// owner to decide on, and deletes none of them.
func TestDeletingAnAppOffersItsOwnAgents(t *testing.T) {
	pinRootDB(t)
	db := &DBase{Store: kvlite.MemStore()}
	turn := &chatTurn{user: "u", udb: db, session: &ChatSession{ID: "s1"}}
	SaveAppSpec(AppSpec{Owner: "u", Slug: "weather", Name: "Weather", RecordKey: "id"})
	if _, err := saveAgent(db, AgentRecord{ID: "a1", Name: "Forecaster", Owner: "u", OwningApp: "weather", OrchestratorPrompt: "Forecast."}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(db, AgentRecord{ID: "a2", Name: "Helper", Owner: "u", OrchestratorPrompt: "Help."}); err != nil {
		t.Fatal(err)
	}
	SavePipelineDef(db, PipelineDef{Name: "Daily brief", Owner: "u", OwningApp: "weather"})
	out, err := turn.appDefDelete(map[string]any{"id": "weather"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `agent "Forecaster"`) || !strings.Contains(out, `pipeline "Daily brief"`) || strings.Contains(out, "Helper") || !strings.Contains(out, "do not delete them unasked") {
		t.Fatalf("delete said: %s", out)
	}
	if len(listAgents(db, "u")) < 2 {
		t.Fatal("an agent was deleted with the app")
	}
}
