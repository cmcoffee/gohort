// A run with nobody watching runs the same STEPS as a conversation.
//
// The failure this pins: the delegate / pipeline / child-machine seam used to
// live on the turn, so a schedule, the Run button, and a pipeline's machine
// stage put those steps through the bare inline worker. The step ran as an
// ordinary prompt and the machine came back with an answer that read right and
// had done none of the work its author arranged — silently, at whatever hour it
// fired. Dispatch was the only door that noticed, and it dealt with it by
// refusing such machines by name.
package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// A tool step in an unattended run calls the tool. No model is configured in
// this test, so if the step ever goes back to the inline worker this fails
// rather than quietly passing.
func TestAnUnattendedRunHonoursAToolStep(t *testing.T) {
	udb, user := preflightFixture(t)
	var gotArgs map[string]any
	RegisterChatTool(&fakeEchoTool{name: "uh_echo", onRun: func(a map[string]any) { gotArgs = a }})

	app := &OrchestrateApp{}
	app.DB = udb
	cur := &MachineCursor{}
	pool, err := GetAgentToolsWithSession(&ToolSession{Username: user, DB: AuthDB()}, "uh_echo")
	if err != nil {
		t.Fatalf("tool pool: %v", err)
	}
	host := app.unattendedHost(unattendedRun{User: user, ID: "run-1", Tools: pool, Cursor: cur})

	ph := MachinePhase{Name: "grab", Tool: "uh_echo", Args: map[string]string{"what": "{prev}", "fixed": "yes"}}
	out, err := host.phaseRunner()(context.Background(), ph, "what the last step said")
	if err != nil {
		t.Fatalf("tool step in an unattended run: %v", err)
	}
	if !strings.Contains(out, "uh_echo ran") {
		t.Errorf("the tool's result should be the step's result: %q", out)
	}
	if gotArgs["what"] != "what the last step said" || gotArgs["fixed"] != "yes" {
		t.Errorf("args should template values and keep the author's keys: %+v", gotArgs)
	}
}

// A step naming an agent that does not exist here falls back inline and leaves a
// breadcrumb. The breadcrumb is the evidence the DELEGATE path ran at all: the
// bare worker has no opinion about ph.Agent and would have left nothing.
func TestAnUnattendedRunSaysWhenADelegateIsMissing(t *testing.T) {
	udb, user := preflightFixture(t)
	app := &OrchestrateApp{}
	app.DB = udb

	var notes []string
	host := app.unattendedHost(unattendedRun{
		User: user, ID: "run-2",
		Note: func(kind, detail string) { notes = append(notes, kind+": "+detail) },
	})
	// The inline fallback needs no model here: this base stands in for it, the
	// same way the conversational test does.
	var ranInline bool
	base := func(ctx context.Context, ph MachinePhase, prompt string) (string, error) {
		ranInline = true
		return "inline answer", nil
	}
	out, err := host.runDelegatedPhase(context.Background(), MachinePhase{Name: "ask"}, "Nobody", "do it", base)
	if err != nil {
		t.Fatalf("a missing delegate must not fail the run: %v", err)
	}
	if !ranInline || out != "inline answer" {
		t.Errorf("expected the inline fallback, got %q", out)
	}
	trail := strings.Join(notes, "\n")
	if !strings.Contains(trail, "phase_delegate_missing") || !strings.Contains(trail, "Nobody") {
		t.Fatalf("the fallback must say what was missing, or the machine quietly stopped being what it was built as: %s", trail)
	}
}

// Reach "none" means no tools, in a run as much as in a conversation: a step
// that only decides or reshapes what it was given should not be handed the run's
// whole pool.
func TestAnUnattendedRunHonoursReachNone(t *testing.T) {
	udb, user := preflightFixture(t)
	app := &OrchestrateApp{}
	app.DB = udb
	pool, _ := GetAgentToolsWithSession(&ToolSession{Username: user, DB: AuthDB()}, "uh_echo")
	if len(pool) == 0 {
		t.Skip("no tools resolved in this fixture; the narrowing below has nothing to narrow")
	}
	host := app.unattendedHost(unattendedRun{User: user, ID: "run-3", Tools: pool})
	if got := host.catalog(MachinePhase{Name: "decide", Reach: ReachNone}); len(got) != 0 {
		t.Errorf("a step that asked for no tools was handed %d", len(got))
	}
	if got := host.catalog(MachinePhase{Name: "work"}); len(got) == 0 {
		t.Error("a step that asked for nothing in particular should still reach the run's pool")
	}
}
