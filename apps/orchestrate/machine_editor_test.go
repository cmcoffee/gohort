package orchestrate

// The structured editor. Its whole claim is that somebody who does not
// know the vocabulary can build a machine, so the tests are about
// whether the form carries the concepts and whether a partial edit is
// safe — not about JSON shapes.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func editorFixture(t *testing.T) (*OrchestrateApp, Database, string, MachineDef) {
	t.Helper()
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{
		Owner: user, Name: "Investigation", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Prompt: "decide", NextFrom: "next_phase", Next: "answer",
				Output: []PipelineField{{Name: "next_phase", Type: "string", Required: true}}},
			{Name: "answer", Prompt: "answer", Resident: true},
		},
	})
	return app, udb, user, def
}

func TestEditorAsksQuestionsRatherThanNamingFields(t *testing.T) {
	_, _, _, def := editorFixture(t)
	raw, err := json.Marshal(machineEditorSpec(def))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// The two concepts people trip on must be stated where the choice is
	// made, not left to the field name.
	for _, want := range []string{
		"The conversation waits here",
		"a turn ENDS in this phase",
		"unless this step decides",
		"Then go to",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form does not explain %q at the point of choice", want)
		}
	}
	// Routing targets are the machine's REAL phases, so a typo is not
	// something the form can produce.
	if !strings.Contains(body, `"value":"triage"`) || !strings.Contains(body, `"value":"answer"`) {
		t.Errorf("phase selects should list the actual phases:\n%s", body)
	}
	// next_from picks a DECLARED output field rather than free text.
	if !strings.Contains(body, `"value":"next_phase"`) {
		t.Error("next_from should offer the declared output fields")
	}
	// Outputs use the repeating-rows field rather than a JSON blob —
	// the whole reason the primitive was built.
	if !strings.Contains(body, `"type":"rows"`) {
		t.Error("phase outputs should be a rows editor")
	}
}

// The checklist is Validate's findings shown as work remaining. Same
// function behind both, so it cannot disagree with what a save accepts.
func TestChecklistIsTheValidatorsOwnFindings(t *testing.T) {
	_, udb, user, _ := editorFixture(t)
	broken := SaveMachineDef(udb, MachineDef{
		Owner: user, Name: "Half built",
		Phases: []MachinePhase{{Name: "one", Prompt: "do a thing"}},
	})
	spec := machineEditorSpec(broken)
	list, _ := spec["checklist"].([]string)
	if len(list) == 0 {
		t.Fatal("a machine with no resident phase should report work remaining")
	}
	if len(list) != len(broken.Problems()) {
		t.Errorf("checklist and Validate disagree: %v vs %v", list, broken.Problems())
	}
	// And a complete machine says so rather than showing an empty list.
	if done := machineEditorSpec(mustValid(t, udb, user)); len(done["checklist"].([]string)) != 0 {
		t.Errorf("a valid machine should have nothing outstanding: %v", done["checklist"])
	}
}

func mustValid(t *testing.T, udb Database, user string) MachineDef {
	t.Helper()
	def := SaveMachineDef(udb, MachineDef{
		Owner: user, Name: "Fine", Start: "here",
		Phases: []MachinePhase{{Name: "here", Prompt: "talk", Resident: true}},
	})
	if err := def.Validate(); err != nil {
		t.Fatalf("fixture is not valid: %v", err)
	}
	return def
}

// A form holding three fields must not take the phases with it — the
// failure patchAgent exists to prevent, on a different record.
func TestEditingOneSectionKeepsTheRest(t *testing.T) {
	app, udb, user, def := editorFixture(t)

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/meta", strings.NewReader(`{"name":"Renamed"}`))
	w := httptest.NewRecorder()
	app.handleMachineMeta(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	after, _ := LoadMachineDef(udb, user, def.ID)
	if after.Name != "Renamed" {
		t.Errorf("name did not save: %q", after.Name)
	}
	if len(after.Phases) != 2 {
		t.Fatalf("editing the name took the phases with it: %d left", len(after.Phases))
	}
	if after.Description != def.Description || after.Start != "triage" {
		t.Error("untouched fields were rewritten by a partial save")
	}

	// Same for a phase: saving one section must not blank another's field.
	body := `{"prompt":"decide, carefully"}`
	r = httptest.NewRequest("POST", "/api/machines/"+def.ID+"/phases?name=triage", strings.NewReader(body))
	w = httptest.NewRecorder()
	after, _ = LoadMachineDef(udb, user, def.ID)
	app.handleMachinePhases(w, asUser(r, user), udb, user, after)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	after, _ = LoadMachineDef(udb, user, def.ID)
	ph, _ := after.Phase("triage")
	if ph.Prompt != "decide, carefully" {
		t.Errorf("prompt did not save: %q", ph.Prompt)
	}
	if ph.NextFrom != "next_phase" || len(ph.Output) != 1 {
		t.Errorf("a partial phase save dropped its routing or outputs: %+v", ph)
	}
}

// Adding a phase to an empty machine has to set the start, because there
// is nowhere else it could begin.
func TestFirstPhaseBecomesTheStart(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Empty"})

	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/phases",
		strings.NewReader(`{"name":"first","prompt":"hello","resident":true}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	after, _ := LoadMachineDef(udb, user, def.ID)
	if after.Start != "first" {
		t.Errorf("start = %q, want the only phase there is", after.Start)
	}
	// And it saved despite being incomplete a moment ago — an editor that
	// refuses to store the third field until the tenth exists is a puzzle.
	if len(after.Phases) != 1 {
		t.Fatalf("phase was not added: %+v", after.Phases)
	}
}

// The table describes phases in the terms the form asks about.
func TestPhaseRowReadsAsEnglish(t *testing.T) {
	row := phaseRow(MachinePhase{Name: "triage", NextFrom: "next_phase", Next: "answer",
		Output: []PipelineField{{Name: "observation"}, {Name: "asked"}}})
	if row["kind"] != "hands on" {
		t.Errorf("kind = %v", row["kind"])
	}
	if row["goes"] != "decided by next_phase (else answer)" {
		t.Errorf("goes = %v", row["goes"])
	}
	if row["declares"] != "observation, asked" {
		t.Errorf("declares = %v", row["declares"])
	}
	// A phase that hands off nowhere should say so rather than showing a
	// blank cell — it is the most common thing left unfinished.
	if got := phaseRow(MachinePhase{Name: "x"})["goes"]; got != "nowhere yet" {
		t.Errorf("an unrouted phase should say so, got %v", got)
	}
}

// Both halves of the modal are wired in files that know nothing about
// each other, which is the pair that half-lands.
func TestBuilderIsWiredIntoTheModal(t *testing.T) {
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(assets)
	if !strings.Contains(src, "function openMachineBuilder(") {
		t.Fatal("the structured editor is never defined")
	}
	// The row opens the EDITOR PAGE. Authoring outgrew the dialog — a
	// machine with four phases does not fit one — and this modal's job is
	// picking a machine for the open agent.
	if !strings.Contains(src, "window.open('/orchestrate/machine?id=") {
		t.Error("the row should open the full editor page")
	}
	// The JSON path stays reachable — it is what the machine tool writes.
	if !strings.Contains(src, "text: 'Edit as JSON'") {
		t.Error("the JSON editor should stay available as the other door")
	}
	// el() lives in a different IIFE; a bare call throws inside a click
	// handler, where nobody sees it.
	if !strings.Contains(src, "var el = window.uiEl;\n        fetch(") {
		t.Error("the builder must bind uiEl locally")
	}
}

// The Extensions section is the index into the editor. Its value is the
// two facts a row carries — is it finished, and does anything use it.
func TestMachinesSectionSaysWhatIsInertAndWhatIsUnfinished(t *testing.T) {
	if got := usedByText(nil); got != "nobody yet" {
		t.Errorf("an unattached machine should say so plainly, got %q", got)
	}
	if got := usedByText([]string{"Investigator", "Chat"}); got != "Investigator, Chat" {
		t.Errorf("used_by = %q", got)
	}
	ready := MachineDef{Name: "ok", Start: "here", Phases: []MachinePhase{
		{Name: "here", Prompt: "talk", Resident: true},
	}}
	if got := machineStatusText(ready); got != "ready" {
		t.Errorf("a complete machine should read as ready, got %q", got)
	}
	half := MachineDef{Name: "half", Phases: []MachinePhase{{Name: "a", Prompt: "x"}}}
	if got := machineStatusText(half); !strings.Contains(got, "to fix") {
		t.Errorf("an unfinished machine should say how much is left, got %q", got)
	}
}

// The page renders the same component spec the modal mounts — that is
// the payoff for building the spec server-side, and the thing that would
// silently rot if the page grew its own copy.
func TestEditorPageUsesTheSharedSpec(t *testing.T) {
	src, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "machineEditorSpec(def)") {
		t.Error("the page should render the shared editor spec, not a second copy of it")
	}
	if !strings.Contains(string(src), "checklistText(def)") {
		t.Error("the page should show what is still missing")
	}
}

// EVERY field a machine can hold must have a control, or an imported
// machine has settings the editor cannot show. That is worse than it
// sounds: a phase pinned to the lead model, or one that wipes state on
// re-entry, behaves differently for a reason nothing on screen explains.
//
// Reflection rather than a hand-kept list, because the failure this
// guards against IS somebody adding a field and forgetting the form —
// and a hand-kept list is a second thing to forget.
func TestEditorCoversEveryMachineField(t *testing.T) {
	_, _, _, def := editorFixture(t)
	raw, _ := json.Marshal(machineEditorSpec(def))
	spec := string(raw)

	// Storage identity and audit stamps are the server's; showing them
	// invites edits that silently do nothing.
	skip := map[string]bool{"id": true, "owner": true, "created": true, "updated": true, "phases": true}

	check := func(what string, typ reflect.Type) {
		for i := 0; i < typ.NumField(); i++ {
			tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" || skip[tag] {
				continue
			}
			if !strings.Contains(spec, `"field":"`+tag+`"`) {
				t.Errorf("%s.%s has no control in the editor — an imported machine using it would be invisible and uneditable",
					what, tag)
			}
		}
	}
	check("MachineDef", reflect.TypeOf(MachineDef{}))
	check("MachinePhase", reflect.TypeOf(MachinePhase{}))
}

// And a field the form does not send must survive a save of the ones it
// does — the merge is what makes a partial form safe on a record that
// came from somewhere else.
func TestImportedFieldsSurviveAFormSave(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{
		Owner: user, Name: "Imported", Start: "work",
		Phases: []MachinePhase{
			{Name: "work", Prompt: "do", Next: "talk", Model: "lead", Keep: []string{"work"},
				Output: []PipelineField{{Name: "found", Type: "string"}}},
			{Name: "talk", Prompt: "reply", Resident: true},
		},
	})
	// Save ONLY the prompt, as the form does when somebody edits that box.
	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/phases?name=work",
		strings.NewReader(`{"prompt":"do it carefully"}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	after, _ := LoadMachineDef(udb, user, def.ID)
	ph, _ := after.Phase("work")
	if ph.Prompt != "do it carefully" {
		t.Fatalf("prompt did not save: %q", ph.Prompt)
	}
	if ph.Model != "lead" || len(ph.Keep) != 1 || len(ph.Output) != 1 || ph.Next != "talk" {
		t.Errorf("a partial save dropped fields the form did not send: %+v", ph)
	}
}
