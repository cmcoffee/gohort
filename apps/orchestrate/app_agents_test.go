package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An agent written for one of the owner's apps lives inside the app: no
// picker offers it, no agent outside the app reaches it or is told of it,
// it is no surface of its own, and what it ran reads as the app's.
func TestAnAppsOwnAgentLivesInsideTheApp(t *testing.T) {
	pinRootDB(t)
	SaveAppSpec(AppSpec{Owner: "u", Slug: "weather", Name: "Weather", RecordKey: "id"})
	rec := agentRecordFromArgs(map[string]any{"name": "Forecaster", "owning_app": "Weather"})
	if rec.OwningApp != "weather" {
		t.Fatalf("owning_app = %q, want the slug", rec.OwningApp)
	}
	rec.ID, rec.Owner = "a1", "u"
	if isAppAgent(rec.ID) {
		t.Fatal("an app's own agent crossed the framework app-agent boundary")
	}
	plain := AgentRecord{ID: "a2", Name: "Helper", Owner: "u"}
	sibling := AgentRecord{ID: "a3", Name: "Sky", Owner: "u", OwningApp: "weather"}
	other := AgentRecord{ID: "a4", Name: "Tides", Owner: "u", OwningApp: "almanac"}
	picked := pickerAgents([]AgentRecord{rec, plain, sibling})
	if len(picked) != 1 || picked[0].ID != "a2" {
		t.Fatalf("the picker offers %v, want only the person's own agent", picked)
	}
	if dispatchReachable(plain, rec) || dispatchReachable(other, rec) {
		t.Error("an agent outside the app reaches the app's agent")
	}
	if !dispatchReachable(sibling, rec) {
		t.Error("an agent of the same app cannot reach its sibling")
	}
	if agentSurfaceEligible(rec) || reachableThrough(rec, "u", func(string) bool { return true }) {
		t.Error("an app's agent is a surface of its own, or reached by a key")
	}
	if !agentSurfaceEligible(plain) {
		t.Error("an ordinary agent lost its surface")
	}
}

// What an app's agent ran and cost reads in Fleet as the app's, by its name.
func TestAnAppsAgentRunsReadAsTheApps(t *testing.T) {
	pinRootDB(t)
	SaveAppSpec(AppSpec{Owner: "u", Slug: "weather", Name: "Weather", RecordKey: "id"})
	db := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(db, "u")
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "Forecaster", Owner: "u", OwningApp: "weather", OrchestratorPrompt: "p", appEdit: "Builder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "a2", Name: "Helper", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	names := agentNames(udb, "u")
	if names["a1"] != "Weather (app)" || names["a2"] != "Helper" {
		t.Fatalf("names = %v", names)
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

// An app's own pipeline and machine are run from inside the app alone: an
// agent of the app may, an agent outside it may not, and neither is in the
// person's lists or offered as a machine's child step outside the app.
func TestAnAppsPipelinesAndMachinesLiveInsideTheApp(t *testing.T) {
	inApp := AgentRecord{ID: "a1", Owner: "u", OwningApp: "weather"}
	outside := AgentRecord{ID: "a2", Owner: "u"}
	pipe := PipelineDef{ID: "p1", Name: "Brief", Owner: "u", OwningApp: "weather"}
	mach := MachineDef{ID: "m1", Name: "Rounds", Owner: "u", OwningApp: "weather", Unattended: true}
	mine := PipelineDef{ID: "p2", Name: "Mine", Owner: "u"}
	for _, c := range []struct {
		who  AgentRecord
		want bool
	}{{inApp, true}, {outside, false}} {
		turn := &chatTurn{agent: c.who}
		if got := turn.pipelineDispatchAllowed(dispatchAll, pipe); got != c.want {
			t.Errorf("%s runs the app's pipeline: %v, want %v", c.who.ID, got, c.want)
		}
		if got := turn.machineDispatchAllowed(dispatchAll, mach); got != c.want {
			t.Errorf("%s runs the app's machine: %v, want %v", c.who.ID, got, c.want)
		}
		if !turn.pipelineDispatchAllowed(dispatchAll, mine) {
			t.Errorf("%s cannot run a pipeline no app owns", c.who.ID)
		}
	}
	if !inAppOf(outside, "", "u") || inAppOf(AgentRecord{Owner: "v", OwningApp: "weather"}, "weather", "u") {
		t.Error("inAppOf: an unowned item, or another person's copy of the app")
	}
	db := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(db, "u")
	if _, err := SaveMachineDefBy(udb, mach, "create", "Builder"); err != nil {
		t.Fatal(err)
	}
	offered := func(self MachineDef) bool {
		for _, o := range childMachineOptions(udb, "u", self) {
			if o.Value == "Rounds" {
				return true
			}
		}
		return false
	}
	if offered(MachineDef{ID: "m9", Owner: "u"}) {
		t.Error("the app's machine is offered as a child step of the person's own")
	}
	if !offered(MachineDef{ID: "m8", Owner: "u", OwningApp: "weather"}) {
		t.Error("the app's machine is not offered to another machine of the app")
	}
	if adminMarked("Brief", "u", "") != "Brief" || !strings.Contains(adminMarked("Brief", "u", "weather"), "part of weather") {
		t.Error("the administrator's mark")
	}
}

// The agent host says which app an agent is part of, by its id or its name,
// and "" for the person's own agent or one not found.
func TestTheHostSaysWhichAppAnAgentIsPartOf(t *testing.T) {
	pinRootDB(t)
	SaveAppSpec(AppSpec{Owner: "u", Slug: "weather", Name: "Weather", RecordKey: "id"})
	T := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	udb := UserDB(T.DB, "u")
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Name: "Forecaster", Owner: "u", OwningApp: "weather", OrchestratorPrompt: "p", appEdit: "Builder"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "a2", Name: "Helper", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"a1": "weather", "Forecaster": "weather", "a2": "", "nobody": ""} {
		if got := T.AgentOwningApp("u", id); got != want {
			t.Errorf("%q is part of %q, want %q", id, got, want)
		}
	}
}
