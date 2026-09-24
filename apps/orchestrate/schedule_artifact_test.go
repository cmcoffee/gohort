package orchestrate

// An agent travels with the standing schedule that runs it, and the schedule
// lands paused: nothing an import brought in fires until its owner resumes it.

import (
	"encoding/json"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAnAgentTravelsWithItsScheduleAndItLandsPaused(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	RegisterAgentArtifactType(T)
	RegisterAgentMemoryArtifactType(T)
	prevResolve := ResolveAgentNameForExport
	ResolveAgentNameForExport = func(owner, key string) (string, bool) {
		if a, ok := findAgentByNameOrID(UserDB(T.DB, owner), owner, key); ok {
			return a.Name, true
		}
		return "", false
	}
	t.Cleanup(func() { ResolveAgentNameForExport = prevResolve })

	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Digest", OrchestratorPrompt: "summarize"})
	if err != nil {
		t.Fatal(err)
	}
	SaveStandingAgent(RootDB, StandingAgent{
		Name: "weekly-digest", Owner: user, AgentID: a.ID,
		Mission: "Summarize the week.", Cron: "FRI 17:00",
		SchedulerID: "sched-1", ReportSessionID: "sess-1",
	})

	b, err := ExportArtifactBundleAsUser(RootDB, user, []ArtifactSel{{Type: "agent", Name: a.ID}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	var rec json.RawMessage
	for _, x := range b.Artifacts {
		if x.Type == "schedule" {
			rec = x.Recipe
		}
	}
	if rec == nil {
		t.Fatalf("the schedule did not travel with its agent: %+v", b.Artifacts)
	}
	var r scheduleRecipeProbe
	_ = json.Unmarshal(rec, &r)
	if r.Agent != "Digest" || r.SchedulerID != "" || r.ReportSessionID != "" {
		t.Errorf("recipe should name the agent and carry no run state: %+v", r)
	}

	AuthDB().Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	data, _ := json.Marshal(b)
	res, err := ImportArtifactBundleAsUser(RootDB, data, "bob")
	if err != nil || res.Imported != 2 {
		t.Fatalf("import: %v %+v", err, res.Outcomes)
	}
	got, ok := GetStandingAgent(RootDB, "bob", "weekly-digest")
	if !ok {
		t.Fatal("the schedule did not land")
	}
	if !got.Paused || got.SchedulerID != "" || got.AgentID != "Digest" || got.Cron != "FRI 17:00" {
		t.Errorf("imported schedule: %+v", got)
	}
}

// scheduleRecipeProbe reads the fields a recipe must NOT carry, if it did.
type scheduleRecipeProbe struct {
	Agent           string `json:"agent"`
	SchedulerID     string `json:"scheduler_id"`
	ReportSessionID string `json:"report_session_id"`
}
