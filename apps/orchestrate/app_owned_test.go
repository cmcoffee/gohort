package orchestrate

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An editor opens on what an app owns by saying so, before anyone types a
// change Save would refuse; one that is no app's says nothing of the kind.
func TestAnEditorSaysWhenWhatItEditsIsAnApps(t *testing.T) {
	pinRootDB(t)
	app, udb, user := newTestOrchestrate(t)
	SaveAppSpec(AppSpec{Owner: user, Slug: "desk", Name: "Help Desk", RecordKey: "id"})
	agent, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Clerk", OrchestratorPrompt: "Answer.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	own, _ := saveAgent(udb, AgentRecord{Owner: user, Name: "Mine", OrchestratorPrompt: "Mine."})
	pipe, _ := SavePipelineDefBy(udb, PipelineDef{Owner: user, Name: "Triage", OwningApp: "desk",
		Stages: []PipelineStage{{Name: "a", Kind: StageWorker, Prompt: "Sort it."}}}, "create", "Builder")
	mach, _ := SaveMachineDefBy(udb, MachineDef{Owner: user, Name: "Intake", OwningApp: "desk"}, "create", "Builder")

	page := func(serve func(w *httptest.ResponseRecorder)) string {
		w := httptest.NewRecorder()
		serve(w)
		return w.Body.String()
	}
	for what, body := range map[string]string{
		"agent": page(func(w *httptest.ResponseRecorder) {
			app.renderAgentEditor(w, asUser(httptest.NewRequest("GET", "/orchestrate/agents/edit?id="+agent.ID, nil), user), user, udb, agent.ID)
		}),
		"pipeline": page(func(w *httptest.ResponseRecorder) {
			app.handlePipelinePage(w, asUser(httptest.NewRequest("GET", "/orchestrate/pipeline?id="+pipe.ID, nil), user))
		}),
		"machine": page(func(w *httptest.ResponseRecorder) {
			app.handleMachinePage(w, asUser(httptest.NewRequest("GET", "/orchestrate/machine?id="+mach.ID, nil), user))
		}),
	} {
		if !strings.Contains(body, "Part of the app Help Desk") || !strings.Contains(body, "changes only with Builder or Anvil") {
			t.Errorf("the %s editor does not say it is the app's", what)
		}
	}
	mine := page(func(w *httptest.ResponseRecorder) {
		app.renderAgentEditor(w, asUser(httptest.NewRequest("GET", "/orchestrate/agents/edit?id="+own.ID, nil), user), user, udb, own.ID)
	})
	if strings.Contains(mine, "Part of the app") {
		t.Error("an agent that is no app's opens as an app's")
	}
}

// An app switched off stops what it owns on every way a run starts: one
// agent dispatching it, the app's own Ask, the agents(run) list, and a run
// of its pipeline. Switched back on, they run again; an agent that is no
// app's is never stopped.
func TestAnAppSwitchedOffStopsWhatItOwns(t *testing.T) {
	pinRootDB(t)
	app, udb, user := newTestOrchestrate(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Answered.", Repeat: true}}}
	app.LLM, app.LeadLLM = fake, fake
	SaveAppSpec(AppSpec{Owner: user, Slug: "desk", Name: "Help Desk", RecordKey: "id", Disabled: true})
	clerk, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Clerk", OrchestratorPrompt: "Answer.", OwningApp: "desk", appEdit: "Builder"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.RunAgentSync(context.Background(), user, user, clerk.ID, "hello"); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("a dispatch ran an agent whose app is switched off: %v", err)
	}
	if _, _, err := app.AppAgentAsk(context.Background(), user, "", clerk.ID, "hello", false); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("an app's Ask ran an agent whose app is switched off: %v", err)
	}
	if agentRunStopped(AgentRecord{Owner: user, Name: "Mine"}) != nil {
		t.Error("an agent that is no app's was stopped")
	}
	pipe := PipelineDef{Owner: user, Name: "Triage", OwningApp: "desk", Stages: []PipelineStage{{Name: "a", Kind: StageWorker, Prompt: "Sort it."}}}
	if _, err := new(AppCore).RunPipelineDefSync(context.Background(), pipe, "x", nil, nil); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Errorf("a pipeline whose app is switched off ran: %v", err)
	}

	spec, _ := LoadAppSpec(user, "desk")
	spec.Disabled = false
	SaveAppSpec(spec)
	if _, err := app.RunAgentSync(context.Background(), user, user, clerk.ID, "hello"); err != nil {
		t.Errorf("switched back on, its agent does not run: %v", err)
	}
}
