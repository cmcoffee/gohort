package orchestrate

// Assignment from the LIST, the way My tools does it.
//
// Both a machine and a pipeline could only be given to an agent from
// inside their own page. Somebody deciding which agents run what is
// looking at all of them at once, and opening each one to answer that
// is the navigation the tools table already avoids.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func pillsGET(t *testing.T, app *OrchestrateApp, user, path string) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	if strings.Contains(path, "/pipelines/") {
		app.handlePipelineOne(w, asUser(r, user))
	} else {
		app.handleMachineOne(w, asUser(r, user))
	}
	if w.Code != 200 {
		t.Fatalf("pills %s: %d %s", path, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// A machine moves an agent; a pipeline joins a list. Getting that
// backwards is how somebody unplugs something they never touched, so
// each says which it is where the switch is.
func TestAssignPillsSayWhichKindOfAssignmentItIs(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	mine := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Mine", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	other := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Other", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	busy, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Busy",
		OrchestratorPrompt: "hi", Machine: other.ID})
	if err != nil {
		t.Fatal(err)
	}

	got := pillsGET(t, app, user, "/api/machines/"+mine.ID+"/agents?pills=1")
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected one agent pill, got %d", len(items))
	}
	first, _ := items[0].(map[string]any)
	// An agent already running something else is not simply "off": a
	// pill going on MOVES it, and the label says what it would leave.
	if !strings.Contains(first["label"].(string), "runs Other") {
		t.Errorf("the pill should say what it would leave: %v", first["label"])
	}
	if first["on"] != false {
		t.Errorf("it does not run this one: %v", first["on"])
	}
	if !strings.Contains(got["note"].(string), "one machine at a time") {
		t.Errorf("the note should say the assignment displaces: %v", got["note"])
	}

	// Toggling on moves it.
	r := httptest.NewRequest("POST", "/api/machines/"+mine.ID+"/agents",
		strings.NewReader(`{"target":"`+busy.ID+`","on":true}`))
	w := httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("toggle: %d %s", w.Code, w.Body.String())
	}
	if ag, _ := loadAgent(udb, busy.ID); ag.Machine != mine.ID {
		t.Errorf("the agent was not moved: %q", ag.Machine)
	}

	// A pipeline ADDS instead, and its note says so.
	pipe := SavePipelineDef(udb, PipelineDef{Owner: user, Name: "P",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "p"}}})
	pgot := pillsGET(t, app, user, "/api/pipelines/"+pipe.ID+"/agents?pills=1")
	if !strings.Contains(pgot["note"].(string), "leaves the rest alone") {
		t.Errorf("the note should say the assignment adds: %v", pgot["note"])
	}
	r = httptest.NewRequest("POST", "/api/pipelines/"+pipe.ID+"/agents",
		strings.NewReader(`{"target":"`+busy.ID+`","on":true}`))
	w = httptest.NewRecorder()
	app.handlePipelineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("toggle: %d %s", w.Code, w.Body.String())
	}
	ag, _ := loadAgent(udb, busy.ID)
	if len(ag.AttachedPipelines) != 1 || ag.Machine != mine.ID {
		t.Errorf("attaching a pipeline must not disturb the machine: %+v", ag.AttachedPipelines)
	}

	// The whole-set form the pages use still works — the single toggle
	// is an addition, not a replacement.
	r = httptest.NewRequest("POST", "/api/machines/"+mine.ID+"/agents",
		strings.NewReader(`{"agents":[]}`))
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("whole-set save: %d %s", w.Code, w.Body.String())
	}
	if ag, _ := loadAgent(udb, busy.ID); ag.Machine != "" {
		t.Errorf("the checklist should still detach: %q", ag.Machine)
	}
}

// One client action serves both tables, because the row already carries
// where it lives. Two copies would drift the day one gained a feature.
func TestOneAssignActionServesBothLists(t *testing.T) {
	src, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	head := string(src)
	if !strings.Contains(head, "uiRegisterClientAction('orchestrate_assign'") {
		t.Fatal("the action is not registered")
	}
	// It picks the endpoint from the row's own edit_url rather than
	// being told which kind it is.
	if !strings.Contains(head, "url.indexOf('/pipeline') >= 0 ? 'pipelines' : 'machines'") {
		t.Error("the action should read its kind from the row")
	}
	// The generic renderer is the framework's; this only knows the
	// endpoint, which is the app-specific half.
	if !strings.Contains(head, "window.uiRenderScopePills(") {
		t.Error("it should use the framework's pill renderer, not its own")
	}
	// Both sections contribute it, and both tables offer the button.
	for _, f := range []string{"machine_page.go", "pipeline_page.go"} {
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if !strings.Contains(string(b), "Head:  assignPillsHead") {
			t.Errorf("%s does not contribute the registration", f)
		}
		if !strings.Contains(string(b), `Label: "Assign"`) {
			t.Errorf("%s has no assignment button on its list", f)
		}
	}
}
