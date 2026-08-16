package orchestrate

// The structured editor. Its whole claim is that somebody who does not
// know the vocabulary can build a machine, so the tests are about
// whether the form carries the concepts and whether a partial edit is
// safe — not about JSON shapes.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
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
	raw, err := json.Marshal(machineEditorSpec(def, editorCatalog{}))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	// The two concepts people trip on must be stated where the choice is
	// made, not left to the field name.
	for _, want := range []string{
		"The conversation waits here",
		"a turn ENDS here",
		"or let this step choose between",
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
	spec := machineEditorSpec(broken, editorCatalog{})
	list, _ := spec["checklist"].([]string)
	if len(list) == 0 {
		t.Fatal("a machine with no resident phase should report work remaining")
	}
	if len(list) != len(broken.Problems()) {
		t.Errorf("checklist and Validate disagree: %v vs %v", list, broken.Problems())
	}
	// And a complete machine says so rather than showing an empty list.
	if done := machineEditorSpec(mustValid(t, udb, user), editorCatalog{}); len(done["checklist"].([]string)) != 0 {
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
	if row["kind"] != "moves on" {
		t.Errorf("kind = %v", row["kind"])
	}
	if row["goes"] != "decided by next_phase (else answer)" {
		t.Errorf("goes = %v", row["goes"])
	}
	if row["establishes"] != "observation, asked" {
		t.Errorf("establishes = %v", row["establishes"])
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
	if !strings.Contains(string(src), "machineEditorSpec(def,") {
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
	raw, _ := json.Marshal(machineEditorSpec(def, editorCatalog{}))
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

// Navigation, from a user's side rather than a route table's.
func TestYouCanStartAndFinishWithoutLeavingThePage(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)

	// A create affordance must exist where machines are AUTHORED. Without
	// it the only "new" lived in the chat modal and opened the JSON
	// editor — the page built for authoring could not start one.
	// Admin, so the section's access gate admits — the gate is not what
	// this test is about.
	adminAuth(t, user)
	sec, ok := machinesExtensionSection(asUser(httptest.NewRequest("GET", "/gateways", nil), user), user)
	if !ok {
		t.Fatal("the section did not build for a user who can reach the app")
	}
	raw, _ := json.Marshal(sec)
	if !strings.Contains(string(raw), "/orchestrate/machine?new=1") {
		t.Error("no way to create a machine from the page that authors them")
	}

	// And that link has to land IN the editor, on a machine that would
	// run — not on an empty box whose save refuses.
	r := httptest.NewRequest("GET", "/orchestrate/machine?new=1", nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect into the editor, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/orchestrate/machine?id=") {
		t.Fatalf("redirect went somewhere else: %q", loc)
	}
	id := strings.TrimPrefix(loc, "/orchestrate/machine?id=")
	made, found := LoadMachineDef(udb, user, id)
	if !found {
		t.Fatal("the new machine was not saved")
	}
	if err := made.Validate(); err != nil {
		t.Errorf("a new machine should already be one that runs: %v", err)
	}
}

// The page shows the work before the criticism. Landing on two lists of
// what is wrong, above the thing you are building, reads as a rebuke —
// and the row you clicked already said how much was outstanding.
func TestEditorPageLeadsWithTheMachine(t *testing.T) {
	src, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	// The steps are now a section PER STEP, appended after the header
	// section — so "the machine comes first" is about where the criticism
	// sits relative to the phase sections, not a literal "Steps" title.
	steps := strings.Index(body, `Title:    p.Name,`)
	missing := strings.Index(body, `Title: "What is still missing"`)
	advice := strings.Index(body, `Title: "Worth a look"`)
	if steps < 0 || missing < 0 || advice < 0 {
		t.Fatal("expected sections are missing")
	}
	if steps > missing || steps > advice {
		t.Error("the machine should come before the list of what is wrong with it")
	}
}

// adminAuth marks the test user an admin so app-access gates admit them.
func adminAuth(t *testing.T, user string) {
	t.Helper()
	db := AuthDB()
	if db == nil {
		t.Skip("no auth store wired")
	}
	db.Set(AuthTable, "user:"+user, AuthUser{Username: user, Admin: true})
}

// The point of a per-phase panel: every choice is computed FROM that
// phase, so nothing is typed from memory. A shared form could only offer
// everything in the machine and hope.
func TestEachPhaseGetsItsOwnComputedChoices(t *testing.T) {
	def := MachineDef{
		Name: "Investigation", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Next: "answer", NextFrom: "next_phase",
				Output: []PipelineField{
					{Name: "next_phase", Type: "string"},
					{Name: "observation", Type: "string"},
					{Name: "parts", Type: "list"},
				}},
			{Name: "hunch", Next: "verify",
				Output: []PipelineField{{Name: "hypothesis", Type: "string"}}},
			{Name: "verify", Resident: true, Guard: "they moved on", GuardTo: "triage"},
		},
	}
	agents := []ui.SelectOption{{Value: "", Label: "— this agent —"}, {Value: "ag-9", Label: "Log analyst"}}

	fieldsOf := func(phase string) string {
		p, ok := def.Phase(phase)
		if !ok {
			t.Fatalf("no phase %q", phase)
		}
		b, _ := json.Marshal(phaseFieldsFor(def, p, editorCatalog{agents: agents}))
		return string(b)
	}

	// next_from offers only THIS phase's own STRING fields. A list field
	// cannot name a phase, and another phase's field is not readable here.
	tri := fieldsOf("triage")
	if !strings.Contains(tri, `"value":"next_phase"`) {
		t.Error("triage should be able to route on its own string field")
	}
	if strings.Contains(tri, `"value":"parts"`) {
		t.Error("a list field was offered as a routing target — it cannot name a phase")
	}
	if strings.Contains(tri, `"value":"hypothesis"`) {
		t.Error("another phase's field was offered as this phase's routing target")
	}

	// The prompt help SPELLS OUT the state refs available here, which is
	// the single biggest thing an author otherwise types from memory.
	hun := fieldsOf("hunch")
	for _, want := range []string{"{state:triage.observation}", "{state:triage.next_phase}"} {
		if !strings.Contains(hun, want) {
			t.Errorf("hunch's prompt help should list %s", want)
		}
	}
	if strings.Contains(hun, "{state:hunch.") {
		t.Error("a phase was offered its own fields — it cannot read what it is about to write")
	}

	// A resident phase gets the guard and no output/next_from; a transient
	// one gets the reverse. The form stops offering what does not apply
	// rather than relying on the validator to say so afterwards.
	ver := fieldsOf("verify")
	if !strings.Contains(ver, `"field":"guard"`) {
		t.Error("a resident phase should be able to declare a guard")
	}
	if strings.Contains(ver, `"field":"next_from"`) || strings.Contains(ver, `"field":"output"`) {
		t.Error("a resident phase was offered routing/output fields it cannot use")
	}
	if strings.Contains(ver, `"field":"agent"`) {
		t.Error("a resident phase was offered delegation, which Problems() forbids")
	}
	if !strings.Contains(tri, `"field":"output"`) {
		t.Error("a transient phase should be able to declare fields")
	}

	// Delegation is a PICK, not a remembered name.
	if !strings.Contains(tri, `"value":"ag-9"`) {
		t.Error("the delegate should be chosen from the user's agents")
	}
	// keep lists the other phases, never itself.
	if !strings.Contains(tri, `"field":"keep"`) || !strings.Contains(tri, `"value":"hunch"`) {
		t.Error("keep should offer the other phases by name")
	}
}

// Each step's section must hold THAT step's form and nothing else.
//
// It did not: the spec returned three components — meta, a Stack of every
// phase panel, and the add button — and the page indexed into that flat
// list, so the first step's section received every step's form and the
// second received the "add" button. The menu and the diagram were right
// and the bodies under them were not, which is worse than either being
// wrong alone: it teaches that the structure means nothing.
func TestEachStepSectionHoldsItsOwnForm(t *testing.T) {
	def := MachineDef{
		Name: "Starter", Start: "decompose_question",
		Phases: []MachinePhase{
			{Name: "decompose_question", Next: "answer_directly", Prompt: "split it",
				Output: []PipelineField{{Name: "parts", Type: "list"}}},
			{Name: "answer_directly", Prompt: "answer", Resident: true},
		},
	}
	spec := machineEditorSpec(def, editorCatalog{})

	panels, ok := spec["phases"].([]ui.Component)
	if !ok {
		t.Fatal("the spec must expose the phase panels as their own list, not buried in a flat one")
	}
	if len(panels) != len(def.Phases) {
		t.Fatalf("expected one panel per step, got %d for %d steps", len(panels), len(def.Phases))
	}
	if _, ok := spec["meta"].(ui.FormPanel); !ok {
		t.Error("the machine's own fields should be reachable by name")
	}
	if _, ok := spec["add"].(ui.ModalButton); !ok {
		t.Error("the add control should be reachable by name")
	}

	// Panel i belongs to step i: it posts to that step and carries the
	// fields computed for it.
	for i, p := range def.Phases {
		raw, _ := json.Marshal(panels[i])
		body := string(raw)
		if !strings.Contains(body, "/phases?name="+p.Name) {
			t.Errorf("panel %d does not target step %q:\n%s", i, p.Name, body)
		}
		for j, other := range def.Phases {
			if i == j {
				continue
			}
			if strings.Contains(body, "/phases?name="+other.Name) {
				t.Errorf("step %q's panel also contains step %q's form", p.Name, other.Name)
			}
		}
	}

	// A resident step's panel is not empty — the earlier symptom was a
	// section that looked blank because it had the wrong body entirely.
	last, _ := json.Marshal(panels[len(panels)-1])
	for _, want := range []string{`"field":"prompt"`, `"field":"resident"`, `"field":"guard"`} {
		if !strings.Contains(string(last), want) {
			t.Errorf("the resident step's panel is missing %s — it should never render empty", want)
		}
	}
}

// An editor that can only ever grow is not an editor. Remove lived on the
// old table's row and did not survive the move to per-step sections.
func TestEachStepCanBeRemoved(t *testing.T) {
	def := MachineDef{
		Name: "x", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Next: "answer", Prompt: "route"},
			{Name: "answer", Prompt: "reply", Resident: true, GuardTo: "triage", Guard: "new subject"},
		},
	}
	panels, _ := machineEditorSpec(def, editorCatalog{})["phases"].([]ui.Component)
	if len(panels) != 2 {
		t.Fatalf("expected two panels, got %d", len(panels))
	}
	raw, _ := json.Marshal(panels[0])
	body := string(raw)
	if !strings.Contains(body, "machine_remove_step") {
		t.Fatal("a step has no way to be removed")
	}
	// The handler is told WHICH step — a client action spends its URL on
	// the handler name, so without a payload it knows nothing about what
	// it acts on.
	if !strings.Contains(body, `"data":"triage"`) {
		t.Errorf("the remove control does not say which step it removes:\n%s", body)
	}

	// The confirmation names what else breaks, because that is knowable
	// here and invisible to the person deciding.
	c := removeStepConfirm(def, "triage")
	if !strings.Contains(c, "conversation STARTS") {
		t.Errorf("removing the start step should say so: %s", c)
	}
	if !strings.Contains(c, "answer") {
		t.Errorf("should name the step left pointing at nothing: %s", c)
	}
	// And a step nothing depends on gets a plain confirmation rather than
	// invented consequences. ("answer" would not do: triage routes to it,
	// so the warning there is correct — which the first draft of this
	// test got wrong.)
	def.Phases = append(def.Phases, MachinePhase{Name: "orphan", Prompt: "unreferenced"})
	plain := removeStepConfirm(def, "orphan")
	if strings.Contains(plain, "routes here") || strings.Contains(plain, "STARTS") {
		t.Errorf("a step nothing references should not claim dependants: %s", plain)
	}
}

// The client action must be registered on the page, or the button is a
// no-op that logs to a console nobody has open.
func TestRemoveStepIsWiredOnThePage(t *testing.T) {
	src, err := os.ReadFile("machine_page.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, `ClientAction("machine_remove_step"`) {
		t.Error("the remove action is named by the form but never registered")
	}
	// It must reload: deleting a step changes what every other step can
	// route to, so their forms are stale the moment it succeeds.
	if !strings.Contains(body, "window.location.reload()") {
		t.Error("removing a step should refresh — the other steps' options just changed")
	}
}

// A step's form should read in the order the step itself runs: what it
// is, what it is told, whether the turn ends here, who does it, what it
// works out, where it goes, and only then the dials.
//
// Order is not decoration here. next_from offers only the fields THIS
// step declares, so routing above the fields asks somebody to pick from
// a list they have not filled in yet — which is how the form was built
// and why it read backwards.
func TestPhaseFormReadsInTheOrderTheStepRuns(t *testing.T) {
	def := previewFixture()
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	body := string(raw)

	order := []string{
		// A step that passes through asks HOW to go about it — the fields
		// carry what to produce.
		"How to go about it",
		"What kind of step is this?",
		"Who runs this step",
		"What this step establishes",
		"Where it goes next",
		"How this step runs",
	}
	prev := -1
	for _, label := range order {
		at := strings.Index(body, `"label":"`+label+`"`)
		if at < 0 {
			t.Fatalf("the form has no %q section", label)
		}
		if at < prev {
			t.Errorf("%q appears out of order — the form should read as the step runs", label)
		}
		prev = at
	}

	// The word "hands on" must mean exactly one thing. It used to label
	// both the data a step produces and the movement to the next step, on
	// the same screen.
	if strings.Contains(body, "hands on") {
		t.Error(`"hands on" is ambiguous here — it read as both the data and the movement`)
	}
}

// The built-ins are only useful if somebody knows they exist. The list
// is generated from core's own table, so a variable added there shows up
// here without anybody remembering to write a sentence about it.
func TestTheFormListsTheBuiltInVariables(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	form := string(raw)

	for _, v := range MachineVars() {
		if !strings.Contains(form, v.Ref) {
			t.Errorf("%s is a built-in but the form never mentions it — nobody would know to use it", v.Ref)
		}
	}
	// And the two the framework supplies unasked say so, because
	// "do I have to place this?" is the question that follows.
	if !strings.Contains(form, "handed to the step anyway") {
		t.Error("the form should say which variables arrive without being placed")
	}
}

// The wheel sits on the field NAME: pick a built-in and the field is
// filled from it. If the options are not there, the feature exists only
// for whoever edits the JSON.
func TestTheValueWheelOffersTheBuiltIns(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	form := string(raw)

	if !strings.Contains(form, `"combo"`) {
		t.Fatal("the name column is not a combo — there is no wheel to pick a built-in from")
	}
	for _, want := range []string{"original_input", "now", "user"} {
		if !strings.Contains(form, `"value":"`+want+`"`) {
			t.Errorf("%s is a built-in but not offered on the wheel", want)
		}
	}
	// Names, not template syntax: the braces are how a value is
	// referenced in a prompt, not part of what a field is called.
	if strings.Contains(form, `"value":"{now}"`) {
		t.Error("the wheel should offer field NAMES, not {braced} references")
	}
	// A whole block is not one field's value.
	if strings.Contains(form, `"value":"established"`) {
		t.Error("{established} is a block, not something a single field can hold")
	}
}

// Picking one has to mean something on the way back, or the wheel names
// a field and leaves the model guessing a value the framework holds.
//
// The form stores what somebody typed and nothing more — the rule that
// a field named after a built-in IS that built-in lives in core, so it
// is true for the machine tool and an imported file as well.
func TestTheValueWheelRoundTrips(t *testing.T) {
	out := outputsFromAny([]any{
		map[string]any{"name": "original_input", "type": "string"},
		map[string]any{"name": "kind", "type": "string"},
		map[string]any{"name": "asked", "type": "string", "from": "{original_input}"},
	})
	if len(out) != 3 {
		t.Fatalf("expected three fields, got %+v", out)
	}
	// A hand-authored from on a field with its own name survives an
	// editor save — the JSON door and the form door agree.
	if out[2].From != "{original_input}" {
		t.Errorf("an explicit from should be kept as written: %+v", out[2])
	}

	// And what the STEP establishes reflects both.
	ph := MachinePhase{Name: "triage", Output: out}
	filled := map[string]string{}
	for _, f := range ph.StaticFields() {
		filled[f.Name] = f.From
	}
	if filled["original_input"] != "{original_input}" {
		t.Errorf("a field named after a built-in should be filled from it: %+v", filled)
	}
	if filled["asked"] != "{original_input}" {
		t.Errorf("an explicit from should fill too: %+v", filled)
	}
	if _, isFilled := filled["kind"]; isFilled {
		t.Errorf("a field nobody named after a built-in stays the step's work: %+v", filled)
	}

	rec := phaseRecord(ph)
	rows, _ := rec["output"].([]map[string]any)
	if len(rows) == 0 || rows[0]["name"] != "original_input" {
		t.Errorf("the record should carry the fields back to the form: %+v", rows)
	}
}

// Picked when the field is added, and settled after that. A built-in
// field has nothing left to configure — its name is the choice, its
// value comes from the framework, its type is text — so the name locks
// and the rest of the row goes away rather than sitting there offering
// changes that would be ignored.
func TestABuiltInFieldSettlesWhenItIsPicked(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	form := string(raw)

	if !strings.Contains(form, `"lock_when":"name:`) {
		t.Error("the name column should lock once it names a built-in")
	}
	if !strings.Contains(form, `"hide_when":"name:`) {
		t.Error("type/required/instruction should go away for a built-in field")
	}
	// The condition has to list every built-in, or the ones it misses
	// keep a type control that does nothing.
	for _, v := range MachineVars() {
		name := BuiltinFieldName(v.Ref)
		if name == "established" {
			continue
		}
		if !strings.Contains(builtinNameExpr(), name) {
			t.Errorf("%s is a built-in the row condition never mentions", name)
		}
	}
}

// The tools box was the last thing in this editor typed from memory: a
// tags field into which somebody spelled tool names and found out later.
// It is a checklist of the user's actual pool now — and a name an
// imported machine uses that this deployment does not have is KEPT as a
// labelled option, because a checklist only persists what it can show,
// and a save that silently drops a tool is how a machine quietly stops
// being what its author built.
func TestToolNarrowingIsPickedNotTyped(t *testing.T) {
	_, _, _, def := editorFixture(t)
	def.Phases[0].Tools = []string{"search_web", "ghost_tool"}
	tri := def.Phases[0]

	cat := editorCatalog{tools: []ui.SelectOption{
		{Value: "search_web", Label: "search_web", Group: "Network", Help: "Search the web"},
		{Value: "read_file", Label: "read_file", Group: "Read"},
	}}
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, cat))
	form := string(raw)

	if strings.Contains(form, `"type":"tags"`) {
		t.Fatal("the tools field is still typed from memory")
	}
	for _, want := range []string{`"value":"search_web"`, `"value":"read_file"`} {
		if !strings.Contains(form, want) {
			t.Errorf("the pool should be offered: missing %s", want)
		}
	}
	if !strings.Contains(form, `"value":"ghost_tool"`) {
		t.Error("a tool the machine names but the pool lacks must stay visible, or the next save drops it silently")
	}
	if !strings.Contains(form, "not available here") {
		t.Error("the kept name should say what it is, not blend into the pool")
	}
}

// And offering must not mutate the shared pool slice: two steps with
// different unknown tools would otherwise leak each other's extras.
func TestKeptToolNamesDoNotLeakBetweenSteps(t *testing.T) {
	pool := []ui.SelectOption{{Value: "search_web"}}
	a := toolChecklistOptions(pool, []string{"only_in_a"})
	b := toolChecklistOptions(pool, []string{"only_in_b"})
	if len(pool) != 1 {
		t.Fatalf("the shared pool was mutated: %+v", pool)
	}
	got := func(opts []ui.SelectOption) (names []string) {
		for _, o := range opts {
			names = append(names, o.Value)
		}
		return
	}
	if strings.Join(got(a), ",") != "search_web,only_in_a" {
		t.Errorf("step A options wrong: %v", got(a))
	}
	if strings.Join(got(b), ",") != "search_web,only_in_b" {
		t.Errorf("step B options wrong: %v", got(b))
	}
}

// Renaming a step from the form is one edit: every reference the
// definition holds follows, and a rename onto an EXISTING step is
// refused — the references would silently re-point at the other step.
func TestRenamingAStepFromTheFormRewritesReferences(t *testing.T) {
	app, udb, user, def := editorFixture(t)

	r := httptest.NewRequest("POST", "/orchestrate/api/machines/"+def.ID+"/phases?name=triage",
		strings.NewReader(`{"name": "intake"}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("rename failed: %d %s", w.Code, w.Body.String())
	}
	saved, _ := LoadMachineDef(udb, user, def.ID)
	if saved.Start != "intake" {
		t.Errorf("start should follow the rename, got %q", saved.Start)
	}
	if _, stale := saved.Phase("triage"); stale {
		t.Error("the old name should be gone")
	}
	if probs := saved.Problems(); len(probs) > 0 {
		t.Errorf("a rename must not leave dangling references: %v", probs)
	}

	// Renaming answer onto intake would re-point everything at intake.
	r = httptest.NewRequest("POST", "/orchestrate/api/machines/"+def.ID+"/phases?name=answer",
		strings.NewReader(`{"name": "intake"}`))
	w = httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, saved)
	if w.Code != 400 {
		t.Fatalf("a rename onto an existing step should be refused, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("the refusal should say why: %s", w.Body.String())
	}
}

// A form still addressing a renamed step must fail loudly, not recreate
// it. And the rename field reloads the page, because every control on it
// carries the old name until it does.
func TestAStaleFormCannotResurrectARenamedStep(t *testing.T) {
	app, udb, user, def := editorFixture(t)

	r := httptest.NewRequest("POST", "/orchestrate/api/machines/"+def.ID+"/phases?name=triage",
		strings.NewReader(`{"name": "intake"}`))
	app.handleMachinePhases(httptest.NewRecorder(), asUser(r, user), udb, user, def)
	renamed, _ := LoadMachineDef(udb, user, def.ID)

	// The stale section saves another field against the OLD address.
	r = httptest.NewRequest("POST", "/orchestrate/api/machines/"+def.ID+"/phases?name=triage",
		strings.NewReader(`{"desc": "typed into a stale form"}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, renamed)
	if w.Code != 404 {
		t.Fatalf("a stale save should 404, got %d: %s", w.Code, w.Body.String())
	}
	after, _ := LoadMachineDef(udb, user, def.ID)
	if _, ghost := after.Phase("triage"); ghost {
		t.Error("the stale save resurrected the renamed step")
	}

	// And the form declares the reload, so the common path never gets here.
	tri, _ := after.Phase("intake")
	raw, _ := json.Marshal(phaseFieldsFor(after, tri, editorCatalog{}))
	if !strings.Contains(string(raw), `"reload_on_change":true`) {
		t.Error("the name field should reload the editor after a rename")
	}
}

// The review's three editor catches, each proven here.
//
// Rename clobber: RenameStep rewrites the renamed step's OWN references
// too (a guard_to naming itself), and a stale copy assigned after it
// quietly undid exactly those.
func TestRenamingASelfReferencingStepKeepsItsRewrites(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Loop", Start: "answer",
		Phases: []MachinePhase{
			{Name: "answer", Prompt: "a", Resident: true,
				Guard: "they moved on", GuardTo: "answer"},
		}})
	r := httptest.NewRequest("POST", "/x?name=answer", strings.NewReader(`{"name": "reply"}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("rename failed: %d %s", w.Code, w.Body.String())
	}
	saved, _ := LoadMachineDef(udb, user, def.ID)
	ph, _ := saved.Phase("reply")
	if ph.GuardTo != "reply" {
		t.Errorf("the step's own self-reference must survive the rename, got guard_to=%q", ph.GuardTo)
	}
	if probs := saved.Problems(); len(probs) > 0 {
		t.Errorf("a self-referencing rename must not leave danglers: %v", probs)
	}
}

// Enum wipe: the form posts the whole record — output rows (which carry
// no enum) AND targets together — so targets must be applied onto the
// NEW rows, not onto rows about to be replaced.
func TestRoutingTargetsSurviveAFullRecordSave(t *testing.T) {
	app, udb, user, def := editorFixture(t)
	r := httptest.NewRequest("POST", "/x?name=triage", strings.NewReader(`{
		"prompt": "decide",
		"output": [{"name": "next_phase", "type": "string", "required": true}],
		"next_from": "next_phase",
		"targets": ["answer"]
	}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
	}
	saved, _ := LoadMachineDef(udb, user, def.ID)
	tri, _ := saved.Phase("triage")
	if got := strings.Join(tri.RoutingChoices(), ","); got != "answer" {
		t.Errorf("the routing set was wiped by its own save, got %v", tri.RoutingChoices())
	}
}

// Create clobber: the ADD form naming an existing step must refuse, not
// quietly merge onto the step somebody already built.
func TestAddingAStepUnderAnExistingNameIsRefused(t *testing.T) {
	app, udb, user, def := editorFixture(t)
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name": "triage", "desc": "typed into the add form"}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 400 {
		t.Fatalf("a create onto an existing name should refuse, got %d: %s", w.Code, w.Body.String())
	}
	saved, _ := LoadMachineDef(udb, user, def.ID)
	tri, _ := saved.Phase("triage")
	if tri.Desc == "typed into the add form" {
		t.Error("the existing step was clobbered by the add form")
	}
}

// Adding a step used to close the dialog and change nothing on screen:
// the sections, the rail and every other step's selects are built
// server-side from the phase list, so the new step existed only in the
// store until a manual refresh. The add form now redirects to the
// editor with the new step's section anchor — three parts that have to
// agree: the form declares the redirect, the POST answers with what it
// substitutes, and the slug matches what the section rail computes.
func TestAddingAStepLandsOnTheNewStep(t *testing.T) {
	app, udb, user, def := editorFixture(t)

	raw, _ := json.Marshal(addPanel(def, "api/machines/"+def.ID))
	spec := string(raw)
	if !strings.Contains(spec, `"redirect_url":"/orchestrate/machine?id={id}#{slug}"`) {
		t.Fatalf("the add form must reopen the editor on the new step: %s", spec)
	}
	if !strings.Contains(spec, `"redirect_target":"_self"`) {
		t.Error("the redirect should replace the page, not open a tab")
	}

	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name": "log check", "desc": "dig"}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("add failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct{ ID, Name, Slug string }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != def.ID || resp.Name != "log check" {
		t.Errorf("the response must carry what the redirect substitutes: %+v", resp)
	}
	// The anchor has to be the slug the rail computes from the section
	// title, or the redirect lands on the page but not on the step.
	if resp.Slug != ui.SectionSlug("log check") || resp.Slug != "log-check" {
		t.Errorf("slug %q does not match the section rail's own transform", resp.Slug)
	}
	saved, _ := LoadMachineDef(udb, user, def.ID)
	if _, ok := saved.Phase("log check"); !ok {
		t.Error("the step should exist in the store")
	}
}
