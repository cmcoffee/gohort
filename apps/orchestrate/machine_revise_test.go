package orchestrate

// Describe a change, against a machine already on screen.
//
// The contract: the ID and every untouched setting survive, what
// changed is REPORTED in steps rather than as the word "revised", and
// the version it replaced can be put back.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func reviseFixture(t *testing.T) (*OrchestrateApp, Database, string, MachineDef) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Triage", Start: "sort",
		Phases: []MachinePhase{
			{Name: "sort", Prompt: "the original instructions, written by hand", Next: "answer"},
			{Name: "answer", Prompt: "reply", Resident: true},
		}})
	return app, udb, user, def
}

func TestARevisionKeepsIdentityAndSaysWhatItChanged(t *testing.T) {
	app, udb, user, def := reviseFixture(t)
	// The model returns the machine with one step added.
	app.LLM = &stubLLM{reply: `{
		"name": "Triage",
		"start": "sort",
		"phases": [
			{"name": "sort", "prompt": "the original instructions, written by hand",
			 "choices": ["dig", "answer"], "next": "answer"},
			{"name": "dig", "prompt": "look at the logs", "next": "answer"},
			{"name": "answer", "prompt": "reply", "resident": true}
		]}`}

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/revise",
		strings.NewReader(`{"description": "let sort choose between digging and answering"}`))
	w := httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("revise: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		ID      string   `json:"id"`
		Changed []string `json:"changed"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.ID != def.ID {
		t.Fatalf("the id must survive — every agent points at it: %q vs %q", out.ID, def.ID)
	}
	// Checkable against the machine on screen. "Revised" is not: a model
	// that ignored the instruction and one that followed it produce the
	// same word, and a rewrite nobody asked for is the whole risk here.
	joined := strings.Join(out.Changed, "; ")
	if !strings.Contains(joined, "added dig") {
		t.Errorf("the reply should name what changed: %q", joined)
	}
	if strings.Contains(joined, "changed sort (instructions)") {
		t.Errorf("an untouched prompt should not be reported as changed: %q", joined)
	}
	if !strings.Contains(joined, "changed sort (wiring)") {
		t.Errorf("sort gained choices, which is a change a picture would show: %q", joined)
	}

	stored, _ := LoadMachineDef(udb, user, def.ID)
	if len(stored.Phases) != 3 {
		t.Fatalf("the revision was not stored: %d phases", len(stored.Phases))
	}
	if stored.Phases[0].Prompt != "the original instructions, written by hand" {
		t.Error("a prompt the change did not touch was rewritten")
	}
}

// A revision can rewrite every prompt in the machine, and prompts are
// the part somebody actually wrote. Without a way back it is a control
// people are right not to press.
func TestARevisionCanBeTakenBack(t *testing.T) {
	app, udb, user, def := reviseFixture(t)
	app.LLM = &stubLLM{reply: `{"name": "Triage", "start": "sort", "phases": [
		{"name": "sort", "prompt": "SOMETHING ELSE ENTIRELY", "next": "answer"},
		{"name": "answer", "prompt": "reply", "resident": true}]}`}

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/revise",
		strings.NewReader(`{"description": "change it"}`))
	w := httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("revise: %d %s", w.Code, w.Body.String())
	}
	after, _ := LoadMachineDef(udb, user, def.ID)
	if after.Previous == nil {
		t.Fatal("nothing was kept, so there is nothing to undo")
	}
	// One deep. A snapshot carrying its own snapshot grows without
	// bound and promises an undo history this does not have.
	if after.Previous.Previous != nil {
		t.Error("the snapshot should not carry a snapshot")
	}
	// The editor offers it, and only now that there is one.
	page := renderMachinePage(t, app, user, def.ID)
	if !strings.Contains(page, "Undo the revision") || !strings.Contains(page, "machine_undo") {
		t.Error("the page does not offer the undo it just made possible")
	}

	r = httptest.NewRequest("POST", "/api/machines/"+def.ID+"/undo", nil)
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("undo: %d %s", w.Code, w.Body.String())
	}
	back, _ := LoadMachineDef(udb, user, def.ID)
	if back.Phases[0].Prompt != "the original instructions, written by hand" {
		t.Fatalf("the original was not restored: %q", back.Phases[0].Prompt)
	}
	if back.Previous != nil {
		t.Error("undo is one step, not a toggle — pressing it twice must not walk forward again")
	}
	// And with nothing to undo, the button is gone and the endpoint says so.
	if strings.Contains(renderMachinePage(t, app, user, def.ID), "Undo the revision") {
		t.Error("a machine with no revision behind it should not offer to undo one")
	}
	r = httptest.NewRequest("POST", "/api/machines/"+def.ID+"/undo", nil)
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 400 {
		t.Errorf("a second undo should refuse, got %d", w.Code)
	}
}

// The snapshot is storage, not a recipe. A bundle carrying it would
// double in size for something the importer can never take back.
func TestAnExportedRecipeLeavesTheSnapshotBehind(t *testing.T) {
	def := MachineDef{Name: "M", Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}}
	def.Previous = &MachineDef{Name: "M", Phases: []MachinePhase{{Name: "s", Prompt: "old", Resident: true}}}
	if ExportMachine(def).Previous != nil {
		t.Error("the undo snapshot should not travel")
	}
	raw, _ := json.Marshal(ExportMachine(def))
	if strings.Contains(string(raw), "previous") {
		t.Errorf("and should not appear in the recipe at all:\n%s", raw)
	}
}

func renderMachinePage(t *testing.T, app *OrchestrateApp, user, id string) string {
	t.Helper()
	r := httptest.NewRequest("GET", "/orchestrate/machine?id="+id, nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("page: %d", w.Code)
	}
	return w.Body.String()
}
