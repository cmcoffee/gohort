package orchestrate

import (
	"context"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A standing fire is ONE row on the live pill and the Monitor. The fire
// registered a run under its schedule's name and the dispatch it made
// registered a second, untied to the first, so every fire showed twice.
func TestAStandingFireIsOneLiveRow(t *testing.T) {
	root := depStores(t, "alice")
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Nothing new today."}}}
	app := &OrchestrateApp{AppCore: AppCore{DB: root, LLM: fake}}
	orchRef = app
	registerStandingRunner(app)
	t.Cleanup(func() { RegisterStandingRunner(nil); RegisterStandingReporter(nil) })
	saveAgent(UserDB(root, "alice"), AgentRecord{ID: "a-news", Owner: "alice", Name: "News", OrchestratorPrompt: "Watch the news."})
	SaveStandingAgent(RootDB, StandingAgent{Owner: "alice", Name: "Morning check", AgentID: "a-news", Mission: "Check the news.", IntervalSeconds: 3600})

	if _, err := RunStandingAgentNow(context.Background(), RootDB, "alice", "Morning check"); err != nil {
		t.Fatal(err)
	}
	runs := app.runsRegistry().Activity("alice")
	if len(runs) != 1 {
		for _, r := range runs {
			t.Logf("run: kind=%s name=%s parent=%q status=%s", r.Kind, r.AgentName, r.ParentID, r.Status)
		}
		t.Fatalf("want one run for the fire, got %d", len(runs))
	}
	if r := runs[0]; r.Kind != "standing" || r.AgentName != "Morning check" || r.Status != RunStatusCompleted {
		t.Errorf("run: %+v", r)
	}
}
