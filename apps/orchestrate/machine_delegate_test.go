package orchestrate

// Delegation: a phase whose work is done by a different agent. The
// interesting cases are the ones where the delegate is not available or
// not appropriate, because those are where a machine could quietly stop
// being what its author built.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func TestResidentPhaseCannotDelegate(t *testing.T) {
	def := MachineDef{
		Name: "x", Start: "talk",
		Phases: []MachinePhase{
			{Name: "talk", Prompt: "reply", Resident: true, Agent: "Someone"},
		},
	}
	probs := def.Problems()
	var found bool
	for _, p := range probs {
		if strings.Contains(p, "agent is not valid on a step the conversation waits in") {
			found = true
			// It must say WHY, not just refuse: the reason is what stops
			// somebody working around it.
			if !strings.Contains(p, "talking to something they did not open") {
				t.Errorf("the refusal should give its reason: %s", p)
			}
		}
	}
	if !found {
		t.Fatalf("delegating the phase the conversation lives in should be reported: %v", probs)
	}

	// A transient phase delegating is fine and must NOT be reported.
	ok := MachineDef{
		Name: "y", Start: "work",
		Phases: []MachinePhase{
			{Name: "work", Prompt: "do it", Agent: "Someone", Next: "talk"},
			{Name: "talk", Prompt: "reply", Resident: true},
		},
	}
	for _, p := range ok.Problems() {
		if strings.Contains(p, "agent is not valid") {
			t.Errorf("a transient phase should be allowed to delegate: %s", p)
		}
	}
}

// A machine is portable; the agent it names may not exist here. That is a
// breadcrumb and a fallback, not a failed turn — but it must not be
// silent, or the machine quietly stops being what it was built as.
func TestMissingDelegateFallsBackAndSaysSo(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	saveAgent(udb, AgentRecord{ID: "ag-1", Name: "Host", OrchestratorPrompt: "x"})
	agent, _ := loadAgent(udb, "ag-1")

	turn := &chatTurn{
		app: app, udb: udb, user: user, agent: agent,
		session: &ChatSession{ID: "s1"},
	}

	var ranInline bool
	base := func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		ranInline = true
		return "inline answer", nil
	}
	out, err := turn.runDelegatedPhase(nil, MachinePhase{Name: "work"}, "Nobody", "do it", base)
	if err != nil {
		t.Fatalf("a missing delegate must not fail the turn: %v", err)
	}
	if !ranInline || out != "inline answer" {
		t.Errorf("expected the inline fallback, got %q", out)
	}
	// The breadcrumb goes where a user can read it, not to a test hook.
	trail := diagTrail(t, udb, "ag-1", "s1")
	if !strings.Contains(trail, "phase_delegate_missing") {
		t.Fatalf("the fallback must leave a breadcrumb: %s", trail)
	}
	if !strings.Contains(trail, "Nobody") {
		t.Errorf("the breadcrumb should name what was missing: %s", trail)
	}
}

// Delegating to yourself is a second turn of the same agent with all of
// the cost and none of the benefit.
func TestSelfDelegationRunsInline(t *testing.T) {
	app, udb, user := newTestOrchestrate(t)
	saveAgent(udb, AgentRecord{ID: "ag-1", Name: "Host", OrchestratorPrompt: "x"})
	agent, _ := loadAgent(udb, "ag-1")

	turn := &chatTurn{
		app: app, udb: udb, user: user, agent: agent,
		session: &ChatSession{ID: "s1"},
	}
	var ranInline bool
	base := func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		ranInline = true
		return "inline", nil
	}
	if _, err := turn.runDelegatedPhase(nil, MachinePhase{Name: "work"}, "Host", "do it", base); err != nil {
		t.Fatal(err)
	}
	if !ranInline {
		t.Error("self-delegation should run inline")
	}
	if trail := diagTrail(t, udb, "ag-1", "s1"); !strings.Contains(trail, "phase_delegate_self") {
		t.Errorf("expected a self-delegation breadcrumb, got %s", trail)
	}
}

// The editor has to offer it, or the field exists only in JSON — which is
// the thing this editor was built to stop being the only door.
func TestEditorOffersDelegation(t *testing.T) {
	_, _, _, def := editorFixture(t)
	agents := []ui.SelectOption{{Value: "", Label: "— this agent —"}, {Value: "ag-9", Label: "Log analyst"}}
	fieldsOf := func(name string) string {
		p, ok := def.Phase(name)
		if !ok {
			t.Fatalf("no phase %q", name)
		}
		b, _ := json.Marshal(phaseFieldsFor(def, p, editorCatalog{agents: agents}))
		return string(b)
	}
	// A transient step offers delegation, as a PICK from real agents
	// rather than a remembered name.
	tri := fieldsOf("triage")
	if !strings.Contains(tri, `"field":"agent"`) || !strings.Contains(tri, `"value":"ag-9"`) {
		t.Error("a transient step should offer delegation, chosen from the user's agents")
	}
	// A resident step does not offer it at all. Better than explaining
	// the restriction: the form cannot express the thing Problems()
	// would reject, so there is nothing to explain.
	if strings.Contains(fieldsOf("answer"), `"field":"agent"`) {
		t.Error("a resident step was offered delegation, which Problems() forbids")
	}
}

// diagTrail reads back what a turn recorded for a session, so a test
// asserts on what a USER would see rather than on an injected hook.
func diagTrail(t *testing.T, udb Database, agentID, sessionID string) string {
	t.Helper()
	var list []SessionDiag
	udb.Get(sessionDiagTable, agentID+":"+sessionID, &list)
	var b strings.Builder
	for _, d := range list {
		b.WriteString(d.Kind + ": " + d.Detail + "\n")
	}
	return b.String()
}

// --- a phase run through a pipeline ------------------------------------

// The one-call path, which is the whole reason to prefer a pipeline over a
// delegate where a recipe fits: the pipeline's final stage already
// declared these fields, so nothing has to read them back out of prose.
func TestPipelinePhaseTakesThePipelinesOwnShape(t *testing.T) {
	declared := []PipelineField{{Name: "verdict", Type: FieldString}, {Name: "confident", Type: FieldBool}}
	js, ok := declaredFieldsJSON(declared, map[string]any{
		"verdict": "it fails under load", "confident": true, "extra": "ignored",
	})
	if !ok {
		t.Fatal("every declared field was present; the shape should have been taken")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(js), &got); err != nil {
		t.Fatalf("the decoder has to be able to read what we hand it: %v", err)
	}
	if got["verdict"] != "it fails under load" || got["confident"] != true {
		t.Errorf("values did not survive: %#v", got)
	}
	// Only what the phase asked for. A stray field is the pipeline's
	// business, not the blackboard's.
	if _, leaked := got["extra"]; leaked {
		t.Error("a field the phase never declared should not land on it")
	}
}

// A partial match is not a shortcut. The missing field would decode as
// empty and read as "the pipeline had nothing to say", when the truth is
// that nobody asked it.
func TestPipelinePhaseFallsBackWhenAFieldIsMissing(t *testing.T) {
	declared := []PipelineField{{Name: "verdict", Type: FieldString}, {Name: "why", Type: FieldString}}
	if _, ok := declaredFieldsJSON(declared, map[string]any{"verdict": "yes"}); ok {
		t.Error("a partial shape should go to the shaping call instead")
	}
	if _, ok := declaredFieldsJSON(declared, nil); ok {
		t.Error("a prose pipeline has no shape to take")
	}
}

// Name first, because a machine is portable: the recipe carries the name
// somebody wrote, while the id belongs to the deployment it was authored in.
func TestPipelinePhaseResolvesByNameThenID(t *testing.T) {
	_, udb, user := newTestOrchestrate(t)
	def := SavePipelineDef(udb, PipelineDef{
		Owner: user, Name: "Fact check",
		Stages: []PipelineStage{{Name: "check", Prompt: "check {input}"}},
	})
	if got, ok := findPipelineByNameOrID(udb, user, "Fact check"); !ok || got.ID != def.ID {
		t.Error("a phase should resolve its pipeline by the name an author typed")
	}
	if got, ok := findPipelineByNameOrID(udb, user, "fact CHECK"); !ok || got.ID != def.ID {
		t.Error("the name match should not care about case")
	}
	if got, ok := findPipelineByNameOrID(udb, user, def.ID); !ok || got.ID != def.ID {
		t.Error("an id should still resolve, for machines authored before names were stored")
	}
	if _, ok := findPipelineByNameOrID(udb, user, "no such thing"); ok {
		t.Error("an unknown reference must report itself missing so the phase can run inline")
	}
}

// A step is run by ONE thing.
func TestAPhaseNamingBothAnAgentAndAPipelineIsReported(t *testing.T) {
	def := MachineDef{Name: "m", Start: "work", Phases: []MachinePhase{
		{Name: "work", Prompt: "do", Next: "talk", Agent: "ag-1", Pipeline: "Fact check"},
		{Name: "talk", Prompt: "reply", Resident: true},
	}}
	var found bool
	for _, p := range def.Problems() {
		if strings.Contains(p, "both an agent and a pipeline") {
			found = true
		}
	}
	if !found {
		t.Errorf("a step with two runners should be reported: %v", def.Problems())
	}
}

// The control is visible when it is usable, and stays visible when a
// value is stored so it can be taken back out.
func TestPipelineControlHidesUnderADelegate(t *testing.T) {
	if got := pipelineShowWhen(MachinePhase{Name: "s"}); got != "!agent" {
		t.Errorf("an undelegated step should offer the choice, got %q", got)
	}
	if got := pipelineShowWhen(MachinePhase{Name: "s", Pipeline: "Fact check"}); got != "" {
		t.Errorf("a stored pipeline must stay visible so it can be removed, got %q", got)
	}
}
