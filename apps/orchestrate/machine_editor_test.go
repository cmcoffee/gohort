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
	"regexp"
	"strconv"
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

// The modal PICKS a machine for the open agent; it does not edit one.
// It used to carry a structured editor of its own, fetching the same
// /editor spec — dead the day authoring moved to a page (nothing called
// it), and unable to work anyway: the step panels that spec returns
// carry buttons whose handlers are registered by the PAGE, so in a
// modal they would have found nothing.
func TestTheModalPicksAndLinksRatherThanEditing(t *testing.T) {
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(assets)
	if strings.Contains(src, "function openMachineBuilder(") {
		t.Error("a second structured editor is a second thing to keep in step with this one")
	}
	// The row opens the EDITOR PAGE. Authoring outgrew the dialog — a
	// machine with four phases does not fit one — and this modal's job is
	// picking a machine for the open agent.
	if !strings.Contains(src, "window.open('/orchestrate/machine?id=") {
		t.Error("the row should open the full editor page")
	}
	// The JSON path stays reachable — it is what the machine tool writes.
	if !strings.Contains(src, "jsonBtn.textContent = 'Edit as JSON';") {
		t.Error("the JSON editor should stay available as the other door")
	}
	// And that door has to be reachable for an EXISTING machine, not
	// only when creating one: the only button that opened it sat inside
	// the dead builder, so the recipe view — and the PUT behind it —
	// could be reached for a new machine and never for one that already
	// existed.
	if !strings.Contains(src, "openMachineEditor(mc.id, mc.name)") {
		t.Error("a machine in the list should be openable as JSON")
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

// Adding a field asks WHAT KIND first, as a choice rather than a text
// box that happens to have suggestions. A combo asked both questions at
// once: click it and you are typing, with the built-ins behind a
// dropdown arrow nobody looks for.
func TestAddingAFieldPicksItsKindFirst(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	form := string(raw)

	if strings.Contains(form, `"combo"`) {
		t.Error("the kind of a field is a choice, not free text with suggestions")
	}
	if !strings.Contains(form, `"field":"builtin"`) {
		t.Fatal("no control for choosing what kind of field this is")
	}
	// Every built-in, by the name a field takes when it holds one, with
	// what it means beside it — and the alternative said as work rather
	// than as a category.
	for _, want := range []string{"original_input", "now", "user"} {
		if !strings.Contains(form, `"value":"`+want+`"`) {
			t.Errorf("%s is a built-in but not offered", want)
		}
	}
	// Named plainly, in the word an author would use to a colleague —
	// this is the ordinary case, not a sentence describing one field.
	if !strings.Contains(form, `"value":"custom"`) || !strings.Contains(form, `"label":"Variable"`) {
		t.Error("the ordinary case should be offered as Variable")
	}
	// Names, not template syntax: the braces are how a value is
	// referenced in a prompt, not part of what a field is called.
	if strings.Contains(form, `"value":"{now}"`) {
		t.Error("the choices should be field NAMES, not {braced} references")
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
		// The kind column answers where the name comes from. Row 1 picked
		// a built-in and still carries a name somebody typed BEFORE
		// choosing — the box is hidden for that kind, so the stale value
		// must not win.
		map[string]any{"builtin": "original_input", "name": "leftover", "type": "string"},
		map[string]any{"builtin": "custom", "name": "kind", "type": "string"},
		map[string]any{"name": "asked", "type": "string", "from": "{original_input}"},
	})
	if len(out) != 3 {
		t.Fatalf("expected three fields, got %+v", out)
	}
	if out[0].Name != "original_input" {
		t.Errorf("the chosen kind names the field, not a stale box: %+v", out[0])
	}
	if out[1].Name != "kind" {
		t.Errorf("a custom field is named by what was typed: %+v", out[1])
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

	// And the form is told which kind each stored row is, derived from
	// the definition rather than stored beside it.
	rec := phaseRecord(ph)
	rows, _ := rec["output"].([]map[string]any)
	if len(rows) != 3 {
		t.Fatalf("the record should carry the fields back to the form: %+v", rows)
	}
	// The kind carries a built-in row's identity, and the name box it
	// hides comes back EMPTY: handing "original_input" to that box would
	// pre-fill it the moment somebody switched the row to Variable, and
	// a variable named after a built-in is read as that built-in at
	// every door — so the switch would appear to do nothing.
	if rows[0]["builtin"] != "original_input" || rows[0]["name"] != "" {
		t.Errorf("a built-in row should come back as the kind alone: %+v", rows[0])
	}
	if rows[1]["builtin"] != "custom" || rows[1]["name"] != "kind" {
		t.Errorf("a field the step works out should come back as custom: %+v", rows[1])
	}
	// A field with its own name but a hand-authored fill is still the
	// step's own field — the JSON door's case, which the form must not
	// silently convert into a built-in.
	if rows[2]["builtin"] != "custom" || rows[2]["name"] != "asked" {
		t.Errorf("an explicitly-filled field keeps its own name: %+v", rows[2])
	}
}

// A built-in row collapses to the one thing it is: its name is the
// choice, its value comes from the framework, its type is text — so the
// rest of the row goes away rather than sitting there offering changes
// that would be ignored. What it must NOT do is trap the choice: a
// mis-click should be a second click, not a remove-and-re-add.
func TestABuiltInFieldSettlesWhenItIsPicked(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	form := string(raw)

	if strings.Contains(form, `"lock_when":"builtin:`) {
		t.Error("the kind should stay changeable — everything else on this page lets you correct yourself")
	}
	if !strings.Contains(form, `"hide_when":"builtin:`) {
		t.Error("name/type/required/instruction should go away for a built-in field")
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

// Toggling "the conversation waits here" rebuilds the page, because the
// sections below it are built from that answer server-side. Without the
// reload the step kept showing controls its new kind cannot use — an
// output contract on a step that now waits, a guard on one that no
// longer does — and the toggle's own help text promised otherwise.
func TestTheKindToggleRebuildsTheForm(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	form := string(raw)

	if !strings.Contains(form, `"field":"resident"`) {
		t.Fatal("no kind toggle in the form")
	}
	// The flag has to be on the toggle specifically — it is the only
	// control on this form whose answer changes which controls exist.
	var fields []map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, f := range fields {
		if f["field"] == "resident" && f["reload_on_change"] != true {
			t.Error("toggling the step's kind must rebuild the form it changes")
		}
	}
	// And the promise it makes is the one being kept.
	if !strings.Contains(form, "the sections below change to match") {
		t.Error("the help text that motivates the reload went missing")
	}
}

// The cost line is derived, so it has to keep up with what a step can
// now do: a step with tools is a short agent loop rather than one call,
// and a delegated step spends another agent's whole turn.
func TestCostLineCountsToolsAndDelegates(t *testing.T) {
	def := MachineDef{Name: "m", Start: "route", Phases: []MachinePhase{
		{Name: "route", Prompt: "decide", Next: "dig"},
		{Name: "dig", Prompt: "look", Next: "ask", Tools: []string{"read_file"}},
		{Name: "ask", Prompt: "delegate", Next: "reply", Agent: "Log analyst"},
		{Name: "reply", Prompt: "answer", Resident: true, Guard: "moved on"},
	}}
	got := costText(def)
	for _, want := range []string{
		"route each cost one model call",
		"dig may use tools",
		"up to " + strconv.Itoa(StageToolRounds),
		"ask hand their work to another agent",
		"arriving in reply",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the cost line should mention %q:\n%s", want, got)
		}
	}
}

// One render of the whole editor, over a machine that uses every shape
// the feature has: a step that decides, one that delegates, one with
// tools, a filled field, a resident step with a guard. Everything here
// is composed server-side from that definition, so a mistake anywhere
// in the composition shows up as a missing section, a stray <nil>, or a
// panic — none of which the per-function tests can see.
func TestTheWholeEditorPageRenders(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	if _, err := saveAgent(udb, AgentRecord{Name: "Log analyst", Owner: user, OrchestratorPrompt: "dig"}); err != nil {
		t.Fatal(err)
	}
	def := SaveMachineDef(udb, MachineDef{
		Owner: user, Name: "Investigation", Description: "Explain something you are seeing.",
		Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Desc: "Which kind of turn is this?", Prompt: "Decide.",
				Choices: []string{"dig", "answer"}, Next: "answer",
				Output: []PipelineField{
					{Name: "original_input", Type: FieldString},
					{Name: "observation", Type: FieldString, Desc: "what was seen"},
				}},
			{Name: "dig", Desc: "Go and look.", Prompt: "Search for the cause.", Next: "answer",
				Agent: "Log analyst"},
			{Name: "log check", Desc: "Read the bundle.", Prompt: "Read it.", Next: "answer",
				Tools: []string{"read_file", "a_tool_this_box_does_not_have"}},
			{Name: "answer", Desc: "Reply.", Prompt: "Answer plainly.", Resident: true,
				Guard: "they moved to a different problem", GuardTo: "triage"},
		},
	})

	r := httptest.NewRequest("GET", "/orchestrate/machine?id="+def.ID, nil)
	w := httptest.NewRecorder()
	app.handleMachinePage(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("the editor did not render: %d", w.Code)
	}
	body := w.Body.String()
	if len(body) < 2000 {
		t.Fatalf("suspiciously small page (%d bytes)", len(body))
	}

	// Every section the page promises, and one per step.
	for _, want := range []string{
		"The machine", "triage", "dig", "log check", "answer",
		"Add a step", "Try it", "What a turn costs",
		"Who runs it", "Worth a look", "What is still missing",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page is missing %q", want)
		}
	}
	// The picture is the PINNED map and nothing else — a second copy in
	// a section of its own was the same diagram twice, and the pinned one
	// is on screen while you edit, which is the reason it exists.
	if strings.Count(body, "u003csvg") != 1 {
		t.Errorf("the machine should be drawn once, found %d", strings.Count(body, "u003csvg"))
	}
	// It is inline and its boxes are doors into the rail.
	// The spec is JSON inside the page, so "<" arrives escaped — assert
	// on what actually ships rather than on what the Go source looks like.
	if !strings.Contains(body, `u003csvg`) || !strings.Contains(body, `href=\"#log-check\"`) {
		t.Error("the diagram should be inlined with a link per step")
	}
	// Every client action the page's buttons name is registered on it.
	for _, action := range []string{"machine_try", "machine_try_reset", "machine_move_step",
		"machine_remove_step", "machine_duplicate"} {
		if strings.Count(body, action) < 2 {
			t.Errorf("%s is named or registered but not both", action)
		}
	}
	// A tool this deployment lacks stays visible rather than being
	// dropped by the next save.
	if !strings.Contains(body, "a_tool_this_box_does_not_have") {
		t.Error("a tool the machine names but the box lacks should still be shown")
	}
	// Composition mistakes leave fingerprints: a Sprintf that ran out of
	// arguments, or a nil that reached a string.
	for _, smell := range []string{"%!", "<nil>", "&lt;nil&gt;"} {
		if strings.Contains(body, smell) {
			t.Errorf("the rendered page contains %q — something composed wrong", smell)
		}
	}
}

// A placeholder is greyed text inside an input, so it has to be
// self-explanatory: it either shows the SHAPE of what goes there or is
// visibly an example. A bare noun ("hypothesis" in a box labelled
// Field) is neither — it just asks the reader why that word.
func TestPlaceholdersExplainThemselves(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	specs := [][]byte{}
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	specs = append(specs, raw)
	raw, _ = json.Marshal(phaseFormFields(def))
	specs = append(specs, raw)

	ph := regexp.MustCompile(`"placeholder":"([^"]*)"`)
	for _, spec := range specs {
		for _, m := range ph.FindAllStringSubmatch(string(spec), -1) {
			p := m[1]
			switch {
			case strings.HasPrefix(p, "e.g. "), // visibly an example
				strings.HasPrefix(p, "("), // an empty-state note
				strings.Contains(p, "_"),  // shows the shape
				strings.Contains(p, " "):  // a phrase that completes its label
			default:
				t.Errorf("placeholder %q is a bare word: it reads as neither a value nor an instruction", p)
			}
		}
	}
}

// A fresh row is ONE question: pick a built-in, or say this is
// something the step works out. Everything that describes a field of
// your own — the name box, its type, whether it is required, the
// instruction — stays hidden until the kind is answered, so nothing
// invites you to start typing before deciding what you are naming.
func TestAFreshFieldRowAsksOneQuestion(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))

	var fields []map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var cols []map[string]any
	for _, f := range fields {
		if f["field"] == "output" {
			raw, _ := json.Marshal(f["columns"])
			_ = json.Unmarshal(raw, &cols)
		}
	}
	if len(cols) == 0 {
		t.Fatal("no field rows in the form")
	}
	for _, c := range cols {
		hide, _ := c["hide_when"].(string)
		switch c["field"] {
		case "builtin":
			if hide != "" {
				t.Error("the kind question must always be visible — it is the one thing a fresh row asks")
			}
		case "name", "type", "required", "desc":
			if !strings.HasSuffix(hide, "|") {
				t.Errorf("%v should also hide while the kind is unanswered, got %q", c["field"], hide)
			}
			if !strings.Contains(hide, "original_input") {
				t.Errorf("%v should hide for a built-in, got %q", c["field"], hide)
			}
		}
	}
	// The unanswered case rides on an empty alternative, which the row
	// condition grammar compares as a string like any other value.
	if !strings.HasSuffix(builtinOrUnansweredExpr(), "|") {
		t.Error("the unanswered case should be part of the same expression, not a second mechanism")
	}
}

// Changing your mind has to work in both directions, and the switch
// back must not be a no-op: a variable named after a built-in IS that
// built-in at every door, so a row flipped from "now" to Variable must
// arrive with an empty name rather than "now" sitting in the box.
func TestAFieldRowCanChangeItsMind(t *testing.T) {
	// built-in → Variable: the kind changes, the name is typed fresh.
	out := outputsFromAny([]any{
		map[string]any{"builtin": "custom", "name": "when_it_started", "type": "string",
			"desc": "when the trouble began"},
	})
	if len(out) != 1 || out[0].Name != "when_it_started" || out[0].From != "" {
		t.Fatalf("a variable is the step's own work: %+v", out)
	}

	// Variable → built-in: the choice wins over whatever was typed, and
	// the framework fills it.
	out = outputsFromAny([]any{
		map[string]any{"builtin": "now", "name": "when_it_started", "type": "string"},
	})
	if len(out) != 1 || out[0].Name != "now" {
		t.Fatalf("the chosen built-in names the field: %+v", out)
	}
	ph := MachinePhase{Name: "triage", Output: out}
	if len(ph.StaticFields()) != 1 {
		t.Errorf("and it should be filled rather than asked for: %+v", ph.StaticFields())
	}

	// A row switched to Variable but not yet named is not a field yet —
	// it is dropped rather than stored nameless.
	if got := outputsFromAny([]any{map[string]any{"builtin": "custom", "name": ""}}); len(got) != 0 {
		t.Errorf("an unnamed variable should not be stored: %+v", got)
	}
}

// The row is a small record, not a strip: the cells that IDENTIFY a
// field share the top line under headers, and the instruction — the
// cell that carries the actual work — gets its own.
func TestTheFieldRowIsLaidOutToBeRead(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	var fields []map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var cols []map[string]any
	for _, f := range fields {
		if f["field"] == "output" {
			b, _ := json.Marshal(f["columns"])
			_ = json.Unmarshal(b, &cols)
		}
	}
	for _, c := range cols {
		label, _ := c["label"].(string)
		if label == "" {
			t.Errorf("column %v has no header, so nothing on screen says what it is", c["field"])
		}
		if c["field"] == "desc" && c["own_line"] != true {
			t.Error("the instruction should not share a line with the cells that name the field")
		}
		if c["field"] != "desc" && c["own_line"] == true {
			t.Errorf("%v identifies the field and belongs on the top line", c["field"])
		}
	}
}

// Flipping a step to "the conversation waits here" removes the whole
// establishes section, which reads as a bug unless something says why.
// The section keeps its place in the reading order and explains the
// absence in the same words the validator would use.
func TestAWaitingStepSaysWhyItEstablishesNothing(t *testing.T) {
	_, _, _, def := editorFixture(t)
	ans, ok := def.Phase("answer")
	if !ok || !ans.Resident {
		t.Fatal("fixture needs a step the conversation waits in")
	}
	raw, _ := json.Marshal(phaseFieldsFor(def, ans, editorCatalog{}))
	form := string(raw)

	if !strings.Contains(form, "What this step establishes") {
		t.Error("the section should keep its place, so its absence is explained rather than silent")
	}
	if !strings.Contains(form, "replies to the PERSON") {
		t.Error("the note should say WHY: the reply goes to a person, not a decoder")
	}
	// And it stays a note — offering the controls would contradict the
	// validator, which refuses output on a step that waits.
	if strings.Contains(form, `"field":"output"`) {
		t.Error("a waiting step must not be offered fields it cannot declare")
	}
	// The reason given here and the reason Validate gives must agree.
	bad := def
	for i := range bad.Phases {
		if bad.Phases[i].Name == "answer" {
			bad.Phases[i].Output = []PipelineField{{Name: "x", Type: FieldString}}
		}
	}
	if !strings.Contains(strings.Join(bad.Problems(), " "), "not to a decoder") {
		t.Errorf("the form and the validator should give the same reason: %v", bad.Problems())
	}
}

// A step routes ONE way. The validator has always refused both at once
// and the runtime gives the field precedence, but the form still
// offered the losing control — so somebody could tick five boxes and
// learn on the next reload that none of them counted. Each mechanism
// now hides the other, live.
func TestTheTwoWaysOfRoutingHideEachOther(t *testing.T) {
	_, _, _, def := editorFixture(t)
	tri, _ := def.Phase("triage")
	raw, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))

	var fields []map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, f := range fields {
		if name, ok := f["field"].(string); ok {
			show, _ := f["show_when"].(string)
			seen[name] = show
		}
	}
	if seen["choices"] != "!next_from" {
		t.Errorf("the choices list should disappear while the step routes by hand, got %q", seen["choices"])
	}
	for _, f := range []string{"next_from", "targets"} {
		if seen[f] != "!choices" {
			t.Errorf("%s should disappear while the step has choices, got %q", f, seen[f])
		}
	}
	// The static fallback belongs to BOTH, so it stays put.
	if seen["next"] != "" {
		t.Errorf("\"then go to\" is the fallback either way and should always show, got %q", seen["next"])
	}

	// And the validator stays the backstop: the JSON door and the
	// machine tool can still write both, and must still be told which
	// one wins.
	both := MachineDef{Name: "m", Phases: []MachinePhase{
		{Name: "route", Prompt: "p", NextFrom: "lane", Choices: []string{"answer"}, Next: "answer",
			Output: []PipelineField{{Name: "lane", Type: FieldString}}},
		{Name: "answer", Prompt: "a", Resident: true},
	}}
	if !strings.Contains(strings.Join(both.Problems(), " "), "Keep one") {
		t.Errorf("the validator should still refuse both at once: %v", both.Problems())
	}
}

// The control for it, and the round trip — a restriction that the
// editor cannot show is one only a JSON author can use.
func TestTheEditorOffersBoundedExits(t *testing.T) {
	app, udb, user, def := editorFixture(t)
	ans, _ := def.Phase("answer")
	raw, _ := json.Marshal(phaseFieldsFor(def, ans, editorCatalog{}))
	form := string(raw)
	if !strings.Contains(form, `"field":"exits_to"`) {
		t.Fatal("no control for bounding where the agent may move the conversation")
	}
	// Only where it can apply. A turn is only ever parked in a step the
	// conversation waits in, so on a step that passes on this would be a
	// control that could never do anything.
	tri, _ := def.Phase("triage")
	passing, _ := json.Marshal(phaseFieldsFor(def, tri, editorCatalog{}))
	if strings.Contains(string(passing), `"field":"exits_to"`) {
		t.Error("a step that passes on is never the one a conversation is moved FROM")
	}
	// Empty is the default and has to stay obviously available: a
	// conversation that changed subject must not be trapped by accident.
	if !strings.Contains(form, "Leave every box empty and the conversation can be moved to any step") {
		t.Error("the default should be stated where the choice is made")
	}

	r := httptest.NewRequest("POST", "/x?name=answer", strings.NewReader(`{"exits_to": ["triage"]}`))
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("save failed: %d %s", w.Code, w.Body.String())
	}
	saved, _ := LoadMachineDef(udb, user, def.ID)
	got, _ := saved.Phase("answer")
	if strings.Join(got.ExitsTo, ",") != "triage" {
		t.Errorf("the restriction did not survive the save: %+v", got.ExitsTo)
	}
	if rec := phaseRecord(got); rec["exits_to"] == nil {
		t.Error("and the form should read it back")
	}
}

// End to end: the editor's delete leaves a machine that is not
// complaining about the deletion, and the confirm says beforehand
// exactly what the removal will do — computed by the same walk that
// will do it, so the warning cannot promise something else.
func TestRemovingAStepFromTheEditorLeavesNoComplaint(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Prompt: "decide", Choices: []string{"dig", "answer"}, Next: "answer"},
			{Name: "dig", Prompt: "look", Next: "answer"},
			{Name: "answer", Prompt: "reply", Resident: true, ExitsTo: []string{"dig"}},
		}})

	confirm := removeStepConfirm(def, "dig")
	if !strings.Contains(confirm, "References to it in") || !strings.Contains(confirm, "answer") {
		t.Errorf("the confirm should say what else it touches: %q", confirm)
	}

	r := httptest.NewRequest("DELETE", "/x?name=dig", nil)
	w := httptest.NewRecorder()
	app.handleMachinePhases(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("remove failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Rewritten []string
		Checklist []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Checklist {
		if strings.Contains(c, "dig") {
			t.Errorf("a removal should not be reported back as work to do: %q", c)
		}
	}
	if len(resp.Rewritten) == 0 {
		t.Error("the response should say which steps it edited")
	}
	saved, _ := LoadMachineDef(udb, user, def.ID)
	tri, _ := saved.Phase("triage")
	if strings.Join(tri.Choices, ",") != "answer" {
		t.Errorf("the stored machine should have lost the reference: %v", tri.Choices)
	}
}

// Every control the machine's own form offers has to DO something. The
// "Offer to every agent" toggle was written to a field nothing read,
// and its help promised reach it could not grant — worse than inert,
// because all of a user's machines already appear for all of their
// agents, so the promise was also already true.
func TestTheMetaFormOffersNothingInert(t *testing.T) {
	_, _, _, def := editorFixture(t)
	raw, _ := json.Marshal(metaPanel(def, "api/machines/"+def.ID))
	form := string(raw)
	if strings.Contains(form, `"field":"global"`) {
		t.Error("a toggle nothing reads should not be offered")
	}
	// The fields that remain each reach the runtime: the name titles the
	// page and names the machine, the description is what a picker
	// shows, and the start is where a conversation begins.
	for _, want := range []string{`"field":"name"`, `"field":"description"`, `"field":"start"`} {
		if !strings.Contains(form, want) {
			t.Errorf("the form should still offer %s", want)
		}
	}

	// And the field is gone from the recipe, so no door can write it
	// back and imply the behaviour returned.
	var recipe map[string]any
	b, _ := json.Marshal(ExportMachine(def))
	_ = json.Unmarshal(b, &recipe)
	if _, ok := recipe["global"]; ok {
		t.Error("the recipe should not carry a scope nothing honours")
	}
}
