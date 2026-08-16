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
		if strings.Contains(p, "agent is not valid on a resident phase") {
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
		b, _ := json.Marshal(phaseFieldsFor(def, p, agents))
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
