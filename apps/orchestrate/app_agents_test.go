package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
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

// Deleting an app deletes what it owns (docs/design/app-history.md, decision
// 9): its agent and pipeline go with it; an agent not the app's stays; and
// an agent of the app something outside it still uses is kept, and said.
func TestDeletingAnAppDeletesWhatItOwns(t *testing.T) {
	pinRootDB(t)
	prevHost := agenthost.Get()
	o := &OrchestrateApp{}
	o.DB = &DBase{Store: kvlite.MemStore()}
	agenthost.Register(o)
	t.Cleanup(func() { agenthost.Register(prevHost) })
	db := UserDB(o.DB, "u")
	turn := &chatTurn{user: "u", udb: db, session: &ChatSession{ID: "s1"}}
	SaveAppSpec(AppSpec{Owner: "u", Slug: "weather", Name: "Weather", RecordKey: "id"})
	SaveAppSpec(AppSpec{Owner: "u", Slug: "almanac", Name: "Almanac", RecordKey: "id", AgentID: "a3"})
	for _, a := range []AgentRecord{
		{ID: "a1", Name: "Forecaster", Owner: "u", OwningApp: "weather", OrchestratorPrompt: "Forecast.", appEdit: "Builder"},
		{ID: "a2", Name: "Helper", Owner: "u", OrchestratorPrompt: "Help."},
		{ID: "a3", Name: "Sky", Owner: "u", OwningApp: "weather", OrchestratorPrompt: "Look up.", appEdit: "Builder"},
	} {
		if _, err := saveAgent(db, a); err != nil {
			t.Fatal(err)
		}
	}
	SavePipelineDefBy(db, PipelineDef{Name: "Daily brief", Owner: "u", OwningApp: "weather"}, "create", "Builder")
	if got := AppOwnedCount("u", "weather"); got != "2 agents and 1 pipeline" {
		t.Errorf("the delete confirmation's count: %q", got)
	}
	out, err := turn.appDefDelete(map[string]any{"id": "weather"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `agent "Forecaster"`) || !strings.Contains(out, `pipeline "Daily brief"`) || !strings.Contains(out, `agent "Sky" (app "Almanac" uses it)`) || strings.Contains(out, "Helper") {
		t.Fatalf("delete said: %s", out)
	}
	if _, ok := loadAgent(db, "a1"); ok {
		t.Error("the app's agent stayed after the app was deleted")
	}
	for _, id := range []string{"a2", "a3"} {
		if _, ok := loadAgent(db, id); !ok {
			t.Errorf("agent %s was deleted with an app it was not only the app's", id)
		}
	}
	if len(ListPipelineDefs(db, "u")) != 0 {
		t.Error("the app's pipeline stayed after the app was deleted")
	}
}
