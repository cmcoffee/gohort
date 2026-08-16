package orchestrate

// Drafting a machine from a description. The contract under test: the
// drafter's reply goes through the machine tool's own decoder, an
// imperfect draft still saves (the editor's checklist is where problems
// belong), and the response carries the id the form's redirect needs.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestDraftMachineSavesAndReportsItsChecklist(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	// A plausible model reply: prose around the JSON, and one problem
	// left in it (verify names a think mode that does not exist) — the
	// draft must survive both.
	app.LLM = &stubLLM{reply: "Here is the machine:\n" + `{
		"name": "Log triage",
		"description": "Sort observations from questions.",
		"start": "triage",
		"phases": [
			{"name": "triage", "desc": "Which kind of turn is this?", "prompt": "Decide.",
			 "choices": ["dig", "answer"], "next": "answer"},
			{"name": "dig", "desc": "Investigate.", "prompt": "Go and look.", "next": "answer",
			 "think": "sometimes",
			 "output": [{"name": "finding", "type": "string", "desc": "what turned up"}]},
			{"name": "answer", "desc": "Reply.", "prompt": "Answer plainly.", "resident": true}
		]}`}

	r := httptest.NewRequest("POST", "/orchestrate/api/machines/draft",
		strings.NewReader(`{"description": "triage support questions and dig into log bundles"}`))
	w := httptest.NewRecorder()
	app.handleMachineDraft(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("draft failed: %d %s", w.Code, w.Body.String())
	}
	out := w.Body.String()
	if !strings.Contains(out, `"id":`) {
		t.Fatalf("the response must carry the id the redirect substitutes: %s", out)
	}
	// The repair pass re-asks with the problem list; the stub returns the
	// same machine, so the flawed draft must still have been SAVED — an
	// imperfect draft beats an empty editor, and the checklist carries
	// what is left.
	if !strings.Contains(out, "think must be") {
		t.Errorf("the remaining problem should ride back as the checklist: %s", out)
	}
	var saved MachineDef
	for _, d := range ListMachineDefs(udb, user) {
		if d.Name == "Log triage" {
			saved = d
		}
	}
	if len(saved.Phases) != 3 {
		t.Fatalf("the draft was not stored: %+v", saved)
	}
	tri, _ := saved.Phase("triage")
	if strings.Join(tri.Choices, ",") != "dig,answer" {
		t.Errorf("the decoder should be the machine tool's own — choices lost: %+v", tri)
	}
}

// The drafter is taught with the machine tool's own spec, so a drafted
// machine and a Builder-authored one cannot be different dialects.
func TestDraftSystemPromptIsTheToolsOwnSpec(t *testing.T) {
	if !strings.Contains(machineDraftSystem, "=== PHASE FIELDS ===") {
		t.Error("the drafter should read machineHelpText, not a paraphrase of it")
	}
	for _, want := range []string{"choices", "resident", "Reply with ONLY a JSON object"} {
		if !strings.Contains(machineDraftSystem, want) {
			t.Errorf("the draft framing is missing %q", want)
		}
	}
}

// A rehearsal is multi-turn: the cursor rides back to the browser and
// returns with the next message, so the second turn RESUMES the parked
// step — which is the only way a guard can ever be watched firing.
func TestTryPanelHoldsAConversationAndTheGuardFires(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	// The stub answers every call with a guard-shaped verdict that says
	// "this is a new problem, go back to triage". The transient step
	// declares no output, so the same reply is just text there.
	app.LLM = &stubLLM{reply: `{"stay": false, "why": "different problem", "to": "triage"}`}
	def := SaveMachineDef(udb, MachineDef{
		Owner: user, Name: "Guarded", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Desc: "route", Prompt: "Route it.", Next: "answer"},
			{Name: "answer", Desc: "reply", Prompt: "Answer.", Resident: true,
				Guard: "the person has moved to a different problem", GuardTo: "triage"},
		},
	})

	post := func(body string) map[string]any {
		r := httptest.NewRequest("POST", "/orchestrate/api/machines/"+def.ID+"/try", strings.NewReader(body))
		w := httptest.NewRecorder()
		app.handleMachineTry(w, asUser(r, user), udb, user, def)
		if w.Code != 200 {
			t.Fatalf("try failed: %d %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad reply: %v", err)
		}
		return out
	}

	one := post(`{"message": "why is the export failing?"}`)
	if one["landed"] != "answer" {
		t.Fatalf("turn 1 should land in answer: %v", one)
	}
	curJSON, _ := json.Marshal(one["cursor"])
	if len(curJSON) == 0 || string(curJSON) == "null" {
		t.Fatal("the cursor must ride back to the client")
	}

	// Turn 2 resumes the parked step; the guard judges it and trips.
	two := post(`{"message": "unrelated: the printer is on fire", "cursor": ` + string(curJSON) + `}`)
	hops, _ := json.Marshal(two["path"])
	if !strings.Contains(string(hops), `"from":"answer"`) || !strings.Contains(string(hops), `"to":"triage"`) {
		t.Errorf("turn 2 should show the guard moving answer → triage: %s", hops)
	}
	notes, _ := json.Marshal(two["notes"])
	if !strings.Contains(string(notes), "machine_guard_tripped") {
		t.Errorf("the guard's decision should be narrated: %s", notes)
	}
	// Only THIS turn's hops are reported — turn 1 already reported its own.
	if strings.Contains(string(hops), `"from":""`) {
		t.Errorf("turn 2 must not replay turn 1's entry hop: %s", hops)
	}
}

// Attaching lives where you are standing when the question comes up.
func TestMachineAgentsAttachAndDetachFromTheEditor(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	wren, _ := saveAgent(udb, AgentRecord{Name: "Wren", Owner: user, OrchestratorPrompt: "hi"})
	crow, _ := saveAgent(udb, AgentRecord{Name: "Crow", Owner: user, OrchestratorPrompt: "hi"})

	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"agents": ["`+wren.ID+`"]}`))
	w := httptest.NewRecorder()
	app.handleMachineAgents(w, asUser(r, user), udb, user, def)
	if w.Code != 200 {
		t.Fatalf("attach failed: %d %s", w.Code, w.Body.String())
	}
	got, _ := loadAgent(udb, wren.ID)
	if got.Machine != def.ID {
		t.Errorf("Wren should now run the machine, got %q", got.Machine)
	}
	if other, _ := loadAgent(udb, crow.ID); other.Machine != "" {
		t.Errorf("Crow was never in the set and must be untouched: %q", other.Machine)
	}

	// The whole-set POST detaches what is no longer checked.
	r = httptest.NewRequest("POST", "/x", strings.NewReader(`{"agents": []}`))
	app.handleMachineAgents(httptest.NewRecorder(), asUser(r, user), udb, user, def)
	if got, _ = loadAgent(udb, wren.ID); got.Machine != "" {
		t.Errorf("unchecking should detach, got %q", got.Machine)
	}
}

// The small conveniences: a copy to experiment on, an order you can
// change, and a price derived from the definition.
func TestDuplicateAndMoveAndCost(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "Inv", Start: "a",
		Phases: []MachinePhase{
			{Name: "a", Prompt: "p", Next: "c"},
			{Name: "b", Prompt: "p", Resident: true, Guard: "moved on"},
			{Name: "c", Prompt: "p", Resident: true},
		}})

	// Duplicate: a new id, a name that cannot be confused with the
	// original, same steps.
	r := httptest.NewRequest("POST", "/api/machines/"+def.ID+"/duplicate", nil)
	w := httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("duplicate failed: %d %s", w.Code, w.Body.String())
	}
	var dup struct{ ID, Name string }
	_ = json.Unmarshal(w.Body.Bytes(), &dup)
	if dup.ID == def.ID || dup.ID == "" {
		t.Fatalf("the copy needs its own id: %+v", dup)
	}
	if dup.Name != "Inv (copy)" {
		t.Errorf("the copy should say it is one, got %q", dup.Name)
	}
	// A second copy cannot collide with the first.
	r = httptest.NewRequest("POST", "/api/machines/"+def.ID+"/duplicate", nil)
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	var dup2 struct{ Name string }
	_ = json.Unmarshal(w.Body.Bytes(), &dup2)
	if dup2.Name != "Inv (copy 2)" {
		t.Errorf("copies should number themselves, got %q", dup2.Name)
	}

	// Move: b up swaps a and b; moving the first up is refused.
	r = httptest.NewRequest("POST", "/api/machines/"+def.ID+"/move",
		strings.NewReader(`{"name": "b", "dir": "up"}`))
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 200 {
		t.Fatalf("move failed: %d %s", w.Code, w.Body.String())
	}
	moved, _ := LoadMachineDef(udb, user, def.ID)
	if got := strings.Join(moved.PhaseNames(), ","); got != "b,a,c" {
		t.Errorf("expected b,a,c after the move, got %s", got)
	}
	r = httptest.NewRequest("POST", "/api/machines/"+def.ID+"/move",
		strings.NewReader(`{"name": "b", "dir": "up"}`))
	w = httptest.NewRecorder()
	app.handleMachineOne(w, asUser(r, user))
	if w.Code != 400 {
		t.Errorf("moving the first step up should be refused, got %d", w.Code)
	}

	// Cost: derived per piece — the transient step, and the guard.
	text := costText(moved)
	for _, want := range []string{"a each cost one model call", "arriving in b", "guard"} {
		if !strings.Contains(text, want) {
			t.Errorf("the cost line should mention %q: %s", want, text)
		}
	}
}

// The checklist never shows hidden or app agents, so its whole-set save
// must never touch them — without this, a hidden agent legitimately
// running the machine is silently unplugged by every save.
func TestAgentsSaveLeavesHiddenAgentsAlone(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	def := SaveMachineDef(udb, MachineDef{Owner: user, Name: "M", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	ghost, _ := saveAgent(udb, AgentRecord{Name: "Ghost", Owner: user,
		OrchestratorPrompt: "hi", Hidden: true, Machine: def.ID})
	wren, _ := saveAgent(udb, AgentRecord{Name: "Wren", Owner: user, OrchestratorPrompt: "hi"})

	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"agents": ["`+wren.ID+`"]}`))
	app.handleMachineAgents(httptest.NewRecorder(), asUser(r, user), udb, user, def)

	if got, _ := loadAgent(udb, ghost.ID); got.Machine != def.ID {
		t.Errorf("a hidden agent the checklist never showed must keep its machine, got %q", got.Machine)
	}
	if got, _ := loadAgent(udb, wren.ID); got.Machine != def.ID {
		t.Errorf("the checked agent should still attach, got %q", got.Machine)
	}
}
