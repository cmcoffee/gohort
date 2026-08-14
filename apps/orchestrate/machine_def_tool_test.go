package orchestrate

// The `machine` grouped tool (machine_def_tool.go) — the surface a
// person or Builder actually authors machines through, plus the phase
// pill's endpoint and the agent-editor picker.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func machineToolFixture(t *testing.T) *chatTurn {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	app := &OrchestrateApp{}
	app.DB = root
	return &chatTurn{app: app, user: "u", udb: udb, agent: AgentRecord{ID: "builder", Owner: "u"}}
}

// toolPhases is the canonical two-phase shape as the LLM would send it:
// untyped maps straight off a JSON tool call.
func toolPhases() []any {
	return []any{
		map[string]any{
			"name": "decompose", "desc": "Split the question up.",
			"prompt": "Break down: {input}", "next": "answer",
			"output": []any{map[string]any{"name": "parts", "type": "list"}},
		},
		map[string]any{
			"name": "answer", "desc": "Reply directly.", "resident": true,
			"prompt": "Answer from {state:decompose.parts}.",
			"guard":  "the user has moved on", "guard_to": "decompose",
		},
	}
}

func TestMachineTool_CreateAttachesAndReports(t *testing.T) {
	turn := machineToolFixture(t)
	target, err := saveAgent(turn.udb, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "hi"})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	out, err := turn.machineCreateOrUpdate(map[string]any{
		"name": "Triage", "phases": toolPhases(),
		"attach_to_agents": []any{"Wren"},
	}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "Created machine") || !strings.Contains(out, "decompose, answer") {
		t.Errorf("the result should name what was built: %s", out)
	}
	if !strings.Contains(out, "Wren") {
		t.Errorf("the result should confirm the attachment: %s", out)
	}
	got, _ := loadAgent(turn.udb, target.ID)
	if got.Machine == "" {
		t.Fatal("the agent should point at the machine")
	}
	def, ok := LoadMachineDef(turn.udb, "u", got.Machine)
	if !ok || def.Name != "Triage" || def.StartPhase() != "decompose" {
		t.Errorf("stored machine wrong: %+v", def)
	}
}

func TestMachineTool_UnattachedCreateSaysSo(t *testing.T) {
	// An unattached machine is inert — it has nowhere to run. Saying
	// nothing here is how someone builds one and then wonders why the
	// agent behaves exactly as before.
	turn := machineToolFixture(t)
	out, err := turn.machineCreateOrUpdate(map[string]any{"name": "Triage", "phases": toolPhases()}, false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, "NOT attached") {
		t.Errorf("expected the result to flag that nothing runs it: %s", out)
	}
}

func TestMachineTool_RejectsAnUnrunnableMachineWithTheReason(t *testing.T) {
	turn := machineToolFixture(t)
	phases := toolPhases()
	phases[1].(map[string]any)["resident"] = false
	phases[1].(map[string]any)["next"] = "decompose"

	_, err := turn.machineCreateOrUpdate(map[string]any{"name": "Bad", "phases": phases}, false)
	if err == nil {
		t.Fatal("expected a machine with no resident phase to be refused")
	}
	if !strings.Contains(err.Error(), "resident") {
		t.Errorf("the validator's reason should reach the author, got: %v", err)
	}
	if len(ListMachineDefs(turn.udb, "u")) != 0 {
		t.Error("a refused create must store nothing")
	}
}

func TestMachineTool_ThinkAcceptsBothSpellings(t *testing.T) {
	// The field is a tri-state string, but every neighbouring surface has
	// trained the model to write think: true. Rejecting that costs a
	// round to learn a distinction the author does not care about.
	cases := map[any]string{
		true:   "on",
		false:  "off",
		"on":   "on",
		"off":  "off",
		"true": "on",
		nil:    "",
		"":     "",
	}
	for raw, want := range cases {
		if got := normalizePhaseThink(raw); got != want {
			t.Errorf("think=%#v: got %q, want %q", raw, got, want)
		}
	}
	// Anything else passes through for Validate to reject BY NAME, rather
	// than being silently coerced to a mode the author didn't ask for.
	if got := normalizePhaseThink("sometimes"); got != "sometimes" {
		t.Errorf("unknown values should reach the validator, got %q", got)
	}
}

func TestMachineTool_UpsertsOnUpdateOfSomethingThatIsNotThere(t *testing.T) {
	// After a REFUSED create nothing was stored, and the reflex is to
	// "fix it" with update. Same recovery the pipeline tool makes.
	turn := machineToolFixture(t)
	out, err := turn.machineCreateOrUpdate(map[string]any{"name": "Triage", "phases": toolPhases()}, true)
	if err != nil {
		t.Fatalf("update-as-upsert: %v", err)
	}
	if !strings.Contains(out, "nothing was stored under that name yet") {
		t.Errorf("the result should say what actually happened: %s", out)
	}
	if len(ListMachineDefs(turn.udb, "u")) != 1 {
		t.Error("the machine should have been stored")
	}
	// A partial patch with nothing to patch is still an error.
	if _, err := turn.machineCreateOrUpdate(map[string]any{"name": "Ghost"}, true); err == nil {
		t.Error("expected an error updating a machine that does not exist with no phases to store")
	}
}

func TestMachineTool_ListFlagsInertMachines(t *testing.T) {
	turn := machineToolFixture(t)
	if _, err := turn.machineCreateOrUpdate(map[string]any{"name": "Triage", "phases": toolPhases()}, false); err != nil {
		t.Fatalf("create: %v", err)
	}
	out, err := turn.machineList()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "Not attached to any agent (inert)") {
		t.Errorf("list should say which machines nothing runs: %s", out)
	}

	target, err := saveAgent(turn.udb, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "hi"})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	defs := ListMachineDefs(turn.udb, "u")
	target.Machine = defs[0].ID
	_, _ = saveAgent(turn.udb, target)

	out, _ = turn.machineList()
	if !strings.Contains(out, "Used by: Wren") {
		t.Errorf("list should name who runs it: %s", out)
	}
}

func TestMachineTool_DeleteDetachesFirst(t *testing.T) {
	// Otherwise the next session on that agent opens pointing at a
	// machine that no longer exists and quietly runs as a plain agent.
	turn := machineToolFixture(t)
	target, err := saveAgent(turn.udb, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "hi"})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := turn.machineCreateOrUpdate(map[string]any{
		"name": "Triage", "phases": toolPhases(), "attach_to_agents": []any{"Wren"},
	}, false); err != nil {
		t.Fatalf("create: %v", err)
	}

	out, err := turn.machineDelete(map[string]any{"name": "Triage"})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !strings.Contains(out, "Detached from Wren") {
		t.Errorf("delete should report the detach: %s", out)
	}
	if got, _ := loadAgent(turn.udb, target.ID); got.Machine != "" {
		t.Errorf("the agent should no longer point at a deleted machine, got %q", got.Machine)
	}
}

// --- the phase pill ---------------------------------------------------

func TestSessionStatus_ReportsThePhaseOnlyForAMachineSession(t *testing.T) {
	app, req, udb := authedApp(t)

	// A plain session reports nothing, which is every session today.
	sess, _ := saveChatSession(udb, ChatSession{ID: "s1", AgentID: "a1"})
	w := httptest.NewRecorder()
	app.handleSessionStatus(w, req(http.MethodGet, "/api/session-status?agent=a1&session=s1", nil))
	if body := strings.TrimSpace(w.Body.String()); body != "{}" {
		t.Errorf("a session with no machine should report nothing, got %s", body)
	}

	def := SaveMachineDef(udb, MachineDef{
		Name: "Triage", Owner: "alice", Start: "answer",
		Phases: []MachinePhase{{Name: "answer", Desc: "Reply directly.", Resident: true, Prompt: "x"}},
	})
	sess.MachineID = def.ID
	sess.Phase = "answer"
	sess.MachineState = MachineState{"decompose": {Text: "earlier"}}
	_, _ = saveChatSession(udb, sess)

	w = httptest.NewRecorder()
	app.handleSessionStatus(w, req(http.MethodGet, "/api/session-status?agent=a1&session=s1", nil))
	body := w.Body.String()
	if !strings.Contains(body, `"label":"answer"`) {
		t.Errorf("expected the phase as the pill label, got %s", body)
	}
	if !strings.Contains(body, "Reply directly.") || !strings.Contains(body, "pinned") {
		t.Errorf("the hover text should explain the phase and its state, got %s", body)
	}

	// Machine deleted under a live session: say so rather than showing a
	// phase from a workflow that no longer exists.
	DeleteMachineDef(udb, def.ID)
	w = httptest.NewRecorder()
	app.handleSessionStatus(w, req(http.MethodGet, "/api/session-status?agent=a1&session=s1", nil))
	if !strings.Contains(w.Body.String(), "machine gone") {
		t.Errorf("expected the pill to flag the deleted machine, got %s", w.Body.String())
	}
}

// --- the agent-editor picker ------------------------------------------

func TestAgentSave_PickerCanClearTheMachine(t *testing.T) {
	// The other half of the preservation rule: a body that OMITS the
	// field keeps it (covered in machines_http_test.go), and a body that
	// SENDS an empty one clears it. Without the key probe those two are
	// indistinguishable and "None" silently does nothing.
	app, req, udb := authedApp(t)

	w := httptest.NewRecorder()
	app.handleAgentList(w, req(http.MethodPost, "/api/agents", map[string]any{
		"name": "Wren", "orchestrator_prompt": "hi", "machine": "m-123",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var agent AgentRecord
	if err := json.Unmarshal(w.Body.Bytes(), &agent); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadAgent(udb, agent.ID); got.Machine != "m-123" {
		t.Fatalf("create should take the posted machine, got %q", got.Machine)
	}

	w = httptest.NewRecorder()
	app.handleAgentList(w, req(http.MethodPost, "/api/agents", map[string]any{
		"id": agent.ID, "name": "Wren", "orchestrator_prompt": "hi", "machine": "",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if got, _ := loadAgent(udb, agent.ID); got.Machine != "" {
		t.Errorf("picking None must actually detach, got %q", got.Machine)
	}
}

// --- the Machines modal wiring ----------------------------------------

func TestMachinesList_ReportsWhoUsesEach(t *testing.T) {
	app, req, udb := authedApp(t)
	def := SaveMachineDef(udb, MachineDef{
		Name: "Triage", Owner: "alice", Start: "answer",
		Phases: []MachinePhase{{Name: "answer", Resident: true, Prompt: "x"}},
	})

	w := httptest.NewRecorder()
	app.handleMachines(w, req(http.MethodGet, "/api/machines", nil))
	if strings.Contains(w.Body.String(), `"used_by"`) {
		t.Error("nothing points at it yet, so used_by should be absent")
	}

	ag, err := saveAgent(udb, AgentRecord{Name: "Wren", Owner: "alice", OrchestratorPrompt: "hi", Machine: def.ID})
	if err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	w = httptest.NewRecorder()
	app.handleMachines(w, req(http.MethodGet, "/api/machines", nil))
	body := w.Body.String()
	if !strings.Contains(body, `"used_by":["Wren"]`) {
		t.Errorf("the list should name who runs it: %s", body)
	}
	if !strings.Contains(body, `"phase_names":["answer"]`) {
		t.Errorf("the list should carry the phase names the modal renders: %s", body)
	}

	// Deleting through HTTP detaches, exactly like the tool's delete.
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodDelete, "/api/machines/"+def.ID, nil))
	if !strings.Contains(w.Body.String(), "Wren") {
		t.Errorf("delete should report what it detached: %s", w.Body.String())
	}
	if got, _ := loadAgent(udb, ag.ID); got.Machine != "" {
		t.Errorf("an agent must never be left pointing at a deleted machine, got %q", got.Machine)
	}
}

func TestMachineGraphEndpoint_StructureAndOverlay(t *testing.T) {
	app, req, udb := authedApp(t)
	def := SaveMachineDef(udb, MachineDef{
		Name: "Triage", Owner: "alice", Start: "decompose",
		Phases: []MachinePhase{
			{Name: "decompose", Desc: "Split it up.", Prompt: "x", Next: "answer",
				Output: []PipelineField{{Name: "parts", Type: FieldList}}},
			{Name: "answer", Desc: "Reply.", Resident: true, Prompt: "y",
				Guard: "moved on", GuardTo: "decompose"},
		},
	})

	w := httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodGet, "/api/machines/"+def.ID+"/graph", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("graph: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("expected svg, got %q", ct)
	}
	plain := w.Body.String()
	if !strings.Contains(plain, "decompose") || !strings.Contains(plain, "answer") {
		t.Error("both phases should be drawn")
	}

	// With a live session it carries the overlay.
	sess, _ := saveChatSession(udb, ChatSession{
		ID: "s1", AgentID: "a1", MachineID: def.ID, Phase: "answer",
		MachineLog: []PhaseHop{{From: "decompose", To: "answer"}},
	})
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodGet,
		"/api/machines/"+def.ID+"/graph?agent=a1&session="+sess.ID, nil))
	live := w.Body.String()
	if live == plain {
		t.Error("a session's graph should be marked with where it has been")
	}
	if !strings.Contains(live, "wf-arrow-hot") {
		t.Error("the edge this conversation took should be highlighted")
	}

	// A session pointing at a DIFFERENT machine must not overlay this
	// one — the ids have to match, not just exist.
	other, _ := saveChatSession(udb, ChatSession{ID: "s2", AgentID: "a1", MachineID: "someone-else", Phase: "answer"})
	w = httptest.NewRecorder()
	app.handleMachineOne(w, req(http.MethodGet,
		"/api/machines/"+def.ID+"/graph?agent=a1&session="+other.ID, nil))
	if w.Body.String() != plain {
		t.Error("a session running another machine must not mark this graph")
	}
}
