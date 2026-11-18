package orchestrate

import (
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
