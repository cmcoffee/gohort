package orchestrate

// Describe a change, against a machine already on screen, through ✨ Get
// assistance: drafted and shown first, then applied.
//
// The contract: nothing is saved until it is applied; the ID and every
// untouched setting survive; what changed is REPORTED in steps rather than
// as the word "revised"; and the version it replaced can be put back.

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

// proposeM drafts a change to a machine for review and returns the reply.
func proposeM(t *testing.T, app *OrchestrateApp, user, id, description string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"description": description})
	r := httptest.NewRequest("POST", "/api/machines/"+id+"/propose", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, user))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// applyM applies a proposal as the dialog does, and returns the reply.
func applyM(t *testing.T, app *OrchestrateApp, user, id string, proposal any, from string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"proposal": proposal, "from": from})
	r := httptest.NewRequest("POST", "/api/machines/"+id+"/revise", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, user))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 {
		t.Logf("revise: %d %s", w.Code, w.Body.String())
	}
	return w.Code, out
}

const addDig = `{
		"name": "Triage",
		"start": "sort",
		"phases": [
			{"name": "sort", "prompt": "the original instructions, written by hand",
			 "choices": ["dig", "answer"], "next": "answer"},
			{"name": "dig", "prompt": "look at the logs", "next": "answer"},
			{"name": "answer", "prompt": "reply", "resident": true}
		]}`

// A change is drafted and shown before anything is saved: the proposal
// names what it would change, step by step, and the machine is untouched
// until it is applied.
func TestAProposalIsShownBeforeItIsSaved(t *testing.T) {
	app, udb, user, def := reviseFixture(t)
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: addDig, Repeat: true}}}
	code, out := proposeM(t, app, user, def.ID, "let sort choose between digging and answering")
	if code != 200 {
		t.Fatalf("propose: %d %v", code, out)
	}
	stored, _ := LoadMachineDef(udb, user, def.ID)
	if len(stored.Phases) != 2 || stored.Previous != nil {
		t.Fatalf("a proposal was saved: %d steps", len(stored.Phases))
	}
	var labels []string
	for _, c := range out["changes"].([]any) {
		labels = append(labels, c.(map[string]any)["label"].(string))
	}
	joined := strings.Join(labels, "; ")
	if !strings.Contains(joined, `New step "dig"`) || !strings.Contains(joined, `Step "sort": settings`) {
		t.Errorf("the review should show the new step and sort's new wiring: %q", joined)
	}
	if strings.Contains(joined, `Step "sort": instructions`) {
		t.Errorf("an untouched prompt should not be shown as changed: %q", joined)
	}
	if out["from"] == "" || out["proposal"] == nil {
		t.Error("the proposal does not carry what applying it needs")
	}
}

func TestARevisionKeepsIdentityAndSaysWhatItChanged(t *testing.T) {
	app, udb, user, def := reviseFixture(t)
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: addDig, Repeat: true}}}
	_, prop := proposeM(t, app, user, def.ID, "let sort choose between digging and answering")
	code, out := applyM(t, app, user, def.ID, prop["proposal"], prop["from"].(string))
	if code != 200 {
		t.Fatalf("revise: %d", code)
	}
	if out["id"] != def.ID {
		t.Fatalf("the id must survive — every agent points at it: %v vs %q", out["id"], def.ID)
	}
	// Checkable against the machine on screen. "Revised" is not: a model
	// that ignored the instruction and one that followed it produce the
	// same word, and a rewrite nobody asked for is the whole risk here.
	var changed []string
	for _, c := range out["changed"].([]any) {
		changed = append(changed, c.(string))
	}
	joined := strings.Join(changed, "; ")
	if !strings.Contains(joined, "added dig") || !strings.Contains(joined, "changed sort (wiring)") ||
		strings.Contains(joined, "changed sort (instructions)") {
		t.Errorf("the reply should name exactly what changed: %q", joined)
	}
	stored, _ := LoadMachineDef(udb, user, def.ID)
	if len(stored.Phases) != 3 || stored.Phases[0].Prompt != "the original instructions, written by hand" {
		t.Fatalf("the revision was not stored as proposed: %+v", stored.Phases)
	}
}

// What a revision does not describe stays as it was. The drafter writes a
// name, a description and steps, and the whole of its answer used to be
// saved, so every revision dropped who the machine was shared with, its
// flags and its deny list.
func TestARevisionKeepsTheMachinesOtherSettings(t *testing.T) {
	app, udb, user, def := reviseFixture(t)
	def.Unattended, def.RouteEachMessage, def.Published = true, true, true
	def.Deny = []string{"send_email"}
	def.Editors, def.AllowedUsers = []string{"carol"}, []string{"bob", "carol"}
	def = SaveMachineDef(udb, def)
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: addDig, Repeat: true}}}
	_, prop := proposeM(t, app, user, def.ID, "let sort choose")
	if code, _ := applyM(t, app, user, def.ID, prop["proposal"], prop["from"].(string)); code != 200 {
		t.Fatalf("revise: %d", code)
	}
	got, _ := LoadMachineDef(udb, user, def.ID)
	if !got.Unattended || !got.RouteEachMessage || !got.Published || len(got.Deny) != 1 ||
		len(got.Editors) != 1 || len(got.AllowedUsers) != 2 {
		t.Errorf("a revision dropped settings it does not describe: %+v", got)
	}
}

// The proposal comes back from the page, so applying it takes only what a
// revision may change: one carrying sharing of its own changes nothing
// about who the machine is shared with.
func TestAProposalCannotChangeWhoTheMachineIsSharedWith(t *testing.T) {
	app, udb, user, def := reviseFixture(t)
	from := definitionFingerprint(def)
	forged := map[string]any{"name": "Triage", "start": "sort",
		"phases":  []map[string]any{{"name": "sort", "prompt": "x", "next": "answer"}, {"name": "answer", "prompt": "reply", "resident": true}},
		"editors": []string{"mallory"}, "allowed_users": []string{"mallory"}, "published": true, "owner": "mallory", "id": "other"}
	if code, _ := applyM(t, app, user, def.ID, forged, from); code != 200 {
		t.Fatalf("revise: %d", code)
	}
	got, _ := LoadMachineDef(udb, user, def.ID)
	if len(got.Editors) != 0 || len(got.AllowedUsers) != 0 || got.Published || got.Owner != user || got.ID != def.ID {
		t.Errorf("a posted proposal changed sharing or identity: %+v", got)
	}
}

// A proposal drafted against a version that is no longer the stored one is
// refused rather than applied over whatever changed in between.
func TestAProposalForAnOlderVersionIsRefused(t *testing.T) {
	app, udb, user, def := reviseFixture(t)
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: addDig, Repeat: true}}}
	_, prop := proposeM(t, app, user, def.ID, "let sort choose")
	def.Phases[1].Prompt = "edited in another tab"
	SaveMachineDef(udb, def)
	if code, _ := applyM(t, app, user, def.ID, prop["proposal"], prop["from"].(string)); code != 409 {
		t.Errorf("a stale proposal was not refused: %d", code)
	}
	got, _ := LoadMachineDef(udb, user, def.ID)
	if got.Phases[1].Prompt != "edited in another tab" || len(got.Phases) != 2 {
		t.Error("a stale proposal overwrote the edit made after it")
	}
}

// The conversation answers, and when it agrees a change, hands it over in
// plain words for drafting.
func TestTheMachineConversationHandsOverAChange(t *testing.T) {
	app, _, user, def := reviseFixture(t)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: `{"reply": "I would add a step that reads the logs.", "change": "Add a dig step after sort that reads the logs."}`, Repeat: true}}}
	app.LLM = fake
	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/assist", strings.NewReader(`{"message":"it guesses too much"}`))
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, user))
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("assist: %d %s", w.Code, w.Body.String())
	}
	if out["reply"] != "I would add a step that reads the logs." || out["change"] != "Add a dig step after sort that reads the logs." {
		t.Errorf("the reply and the change were not handed over: %v", out)
	}
	if sp := fake.Config(0).SystemPrompt; !strings.Contains(sp, "the original instructions, written by hand") {
		t.Error("the conversation was not shown the machine as it stands")
	}
}

// A revision can rewrite every prompt in the machine, and prompts are
// the part somebody actually wrote. Without a way back it is a control
// people are right not to press.
func TestARevisionCanBeTakenBack(t *testing.T) {
	app, udb, user, def := reviseFixture(t)
	app.LLM = &FakeLLM{Turns: []FakeTurn{{Content: `{"name": "Triage", "start": "sort", "phases": [
		{"name": "sort", "prompt": "SOMETHING ELSE ENTIRELY", "next": "answer"},
		{"name": "answer", "prompt": "reply", "resident": true}]}`, Repeat: true}}}
	_, prop := proposeM(t, app, user, def.ID, "change it")
	// The review shows the rewrite as it is and as it would be, which is
	// what tells somebody whether to apply it.
	shown := false
	for _, c := range prop["changes"].([]any) {
		m := c.(map[string]any)
		if m["label"] == `Step "sort": instructions` && m["current"] == "the original instructions, written by hand" &&
			m["value"] == "SOMETHING ELSE ENTIRELY" {
			shown = true
		}
	}
	if !shown {
		t.Errorf("the review does not show the rewritten instructions against the originals: %v", prop["changes"])
	}
	if code, _ := applyM(t, app, user, def.ID, prop["proposal"], prop["from"].(string)); code != 200 {
		t.Fatalf("revise: %d", code)
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
	if !strings.Contains(page, "Undo the revision") || !strings.Contains(page, "machine_undo") || !strings.Contains(page, "Last change") {
		t.Error("the page does not offer the undo it just made possible")
	}

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/undo", nil)
	w := httptest.NewRecorder()
	routed(app)(w, asUser(r, user))
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
	// And with nothing to undo, the section is gone and the endpoint says so.
	if p := renderMachinePage(t, app, user, def.ID); strings.Contains(p, "Undo the revision") || strings.Contains(p, "Last change") {
		t.Error("a machine with no revision behind it should not offer to undo one")
	}
	r = httptest.NewRequest("POST", "/api/machines/"+def.ID+"/undo", nil)
	w = httptest.NewRecorder()
	routed(app)(w, asUser(r, user))
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
