package orchestrate

// A pipeline somebody shared with you is a dispatch target like any other: the
// same policy reads it, the same warden judges it, and it runs in YOUR
// namespace. These pin that it is reachable, and that being shared buys it no
// exemption from anything.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// sharedDispatchTurn: a caller agent belonging to "u", plus a pipeline that
// "colleague" shared with them.
func sharedDispatchTurn(t *testing.T, shareWith string, name string) (*chatTurn, PipelineDef) {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prevBase, prevSaved := orchestrateBaseDB, PipelineSavedHook
	orchestrateBaseDB = root
	PipelineSavedHook = syncPipelineShareIndex
	t.Cleanup(func() { orchestrateBaseDB, PipelineSavedHook = prevBase, prevSaved })

	def := SavePipelineDef(UserDB(root, "colleague"), PipelineDef{
		Name: name, Owner: "colleague", AllowedUsers: []string{shareWith},
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "do {input}"}},
	})
	udb := UserDB(root, "u")
	caller, err := saveAgent(udb, AgentRecord{
		Name: "Caller", Owner: "u", DispatchMode: dispatchAll, OrchestratorPrompt: "p",
	})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	return &chatTurn{user: "u", udb: udb, agent: caller, ctx: context.Background()}, def
}

func TestSharedPipelineIsDispatchable(t *testing.T) {
	turn, def := sharedDispatchTurn(t, "u", "Nightly Report")

	got, err := turn.dispatchablePipeline("Nightly Report")
	if err != nil {
		t.Fatalf("a pipeline shared with this user must be dispatchable: %v", err)
	}
	if got.ID != def.ID || got.Owner != "colleague" {
		t.Fatalf("resolved %q owned by %q, want the shared one", got.Name, got.Owner)
	}
	// It is advertised, or the model has no way to know it exists.
	names := turn.dispatchablePipelineNames(maxAdvertisedPipelines)
	if len(names) != 1 || names[0] != "Nightly Report" {
		t.Errorf("advertised %v, want the shared pipeline", names)
	}
	// And it is known to be somebody else's — the run label and the log say so.
	if !turn.foreignPipeline(got) {
		t.Error("a pipeline owned by another user must read as foreign")
	}
	if label := pipelineRunLabel(turn, got); !strings.Contains(label, "colleague") {
		t.Errorf("the activity label should name the sharer, got %q", label)
	}
}

// Shared is not a bypass: a pipeline nobody shared with this user stays
// unreachable, exactly as it was before dispatch was widened.
func TestUnsharedPipelineIsNotDispatchable(t *testing.T) {
	turn, _ := sharedDispatchTurn(t, "somebody-else", "Nightly Report")
	if _, err := turn.dispatchablePipeline("Nightly Report"); err == nil {
		t.Fatal("an agent reached a pipeline its user was never given")
	}
	if names := turn.dispatchablePipelineNames(5); len(names) != 0 {
		t.Errorf("advertised a pipeline nobody shared: %v", names)
	}
}

// The dispatch policy reads a shared pipeline exactly as it reads an owned one.
func TestPolicyGovernsSharedPipelines(t *testing.T) {
	turn, def := sharedDispatchTurn(t, "u", "Nightly Report")
	turn.agent.DispatchMode = dispatchOnly
	turn.agent.AllowedDispatchTargets = []string{"some-agent"}
	if _, err := turn.dispatchablePipeline("Nightly Report"); err == nil {
		t.Fatal("Only mode let through a shared pipeline nobody ticked")
	}
	turn.agent.AllowedDispatchTargets = []string{def.ID}
	if _, err := turn.dispatchablePipeline("Nightly Report"); err != nil {
		t.Fatalf("a ticked shared pipeline must be reachable: %v", err)
	}
	// An expressed denial still bites, and it is keyed by id so it works on a
	// record this user does not own.
	turn.agent.DispatchMode = dispatchAll
	turn.agent.AllowedDispatchTargets = nil
	turn.agent.DisabledPipelines = []string{def.ID}
	if _, err := turn.dispatchablePipeline("Nightly Report"); err == nil {
		t.Error("a denied shared pipeline was still reachable")
	}
}

// The model has names, not ids. A shared pipeline whose name collides with one
// of the user's own can never be reached by the only handle the model has, so
// it is dropped from the set rather than advertised as an unreachable duplicate.
func TestOwnNameShadowsASharedPipeline(t *testing.T) {
	turn, _ := sharedDispatchTurn(t, "u", "Nightly Report")
	mine := SavePipelineDef(turn.udb, PipelineDef{
		Name: "Nightly Report", Owner: "u",
		Stages: []PipelineStage{{Name: "only", Kind: StageWorker, Prompt: "x"}},
	})
	got, err := turn.dispatchablePipeline("Nightly Report")
	if err != nil {
		t.Fatalf("the user's own pipeline must still resolve: %v", err)
	}
	if got.ID != mine.ID {
		t.Fatalf("a shared pipeline shadowed the user's own %q", got.Name)
	}
	if names := turn.dispatchablePipelineNames(5); len(names) != 1 {
		t.Errorf("the colliding shared pipeline should not be advertised: %v", names)
	}
}
