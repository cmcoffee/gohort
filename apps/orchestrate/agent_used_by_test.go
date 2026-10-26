package orchestrate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func usedByFixture(t *testing.T) (Database, AgentRecord) {
	t.Helper()
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	udb := UserDB(RootDB, "u")
	a, err := saveAgent(udb, AgentRecord{ID: "a-research", Name: "Researcher", Owner: "u", OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	return udb, a
}

// What runs through an agent is worked out from the references themselves:
// an app's agent_id (by id), a pipeline stage and a machine phase (by name, as
// imported bundles carry it).
func TestAgentUsedByFindsAppsPipelinesAndMachines(t *testing.T) {
	udb, a := usedByFixture(t)
	SaveAppSpec(AppSpec{Slug: "screener", Name: "Screener", Owner: "u", AgentID: a.ID})
	SaveAppSpec(AppSpec{Slug: "other", Name: "Other", Owner: "u", AgentID: "someone-else"})
	SavePipelineDef(udb, PipelineDef{Name: "Brief", Owner: "u", Stages: []PipelineStage{{Name: "s1", Kind: "agent", Agent: "researcher"}}})
	SaveMachineDef(udb, MachineDef{Name: "Desk", Owner: "u", Start: "p1", Phases: []MachinePhase{{Name: "p1", Prompt: "x", Agent: a.ID}}})

	uses := agentUsedBy(udb, "u", a)
	if got := agentUsesText(uses); got != "Screener (app), Desk (machine), Brief (pipeline)" {
		t.Errorf("used by: %q", got)
	}
	if usedByKind(uses, "app") != "Screener" {
		t.Errorf("apps: %q", usedByKind(uses, "app"))
	}
	lone, _ := saveAgent(udb, AgentRecord{ID: "a-lone", Name: "Lone", Owner: "u", OrchestratorPrompt: "p"})
	if len(agentUsedBy(udb, "u", lone)) != 0 {
		t.Error("an agent nothing names is reported as used")
	}
}

// The delete confirmation names what stops working.
func TestDeleteConfirmationNamesWhatBreaks(t *testing.T) {
	_, a := usedByFixture(t)
	SaveAppSpec(AppSpec{Slug: "screener", Name: "Screener", Owner: "u", AgentID: a.ID})
	app := &OrchestrateApp{AppCore: AppCore{DB: RootDB}}
	w := httptest.NewRecorder()
	routed(app)(w, as(t, httptest.NewRequest(http.MethodGet, "/api/agents/"+a.ID+"/schedules", nil), "u"))
	if !strings.Contains(w.Body.String(), `"used_by":["Screener (app)"]`) {
		t.Errorf("delete pre-check: %s", w.Body.String())
	}
}

// An app that stops using an agent nothing else uses offers it for deletion:
// as made for the app when it was, more carefully when it was not, and not at
// all while something else still runs through it.
func TestAnAgentTheAppDroppedIsOfferedForDeletion(t *testing.T) {
	udb, general := usedByFixture(t)
	made, _ := saveAgent(udb, AgentRecord{ID: "a-made", Name: "Screener Brain", Owner: "u", OrchestratorPrompt: "p", OwningApp: "screener"})
	SaveAppSpec(AppSpec{Slug: "screener", Name: "Screener", Owner: "u", AgentID: general.ID})
	turn := &chatTurn{user: "u", udb: udb}

	if note := turn.appAgentLeftoverNote("screener", made.ID); !strings.Contains(note, "was made for this app") || !strings.Contains(note, made.ID) {
		t.Errorf("an app-made agent left behind: %q", note)
	}
	if note := turn.appAgentLeftoverNote("screener", general.ID); note != "" {
		t.Errorf("the agent the app still uses was offered: %q", note)
	}
	lone, _ := saveAgent(udb, AgentRecord{ID: "a-lone", Name: "Lone", Owner: "u", OrchestratorPrompt: "p"})
	if note := turn.appAgentLeftoverNote("screener", lone.ID); !strings.Contains(note, "still want it") {
		t.Errorf("a general agent left behind is offered too gently or not at all: %q", note)
	}
	SavePipelineDef(udb, PipelineDef{Name: "Brief", Owner: "u", Stages: []PipelineStage{{Name: "s1", Kind: "agent", Agent: made.ID}}})
	if note := turn.appAgentLeftoverNote("screener", made.ID); note != "" {
		t.Errorf("an agent a pipeline still runs was offered for deletion: %q", note)
	}
}
