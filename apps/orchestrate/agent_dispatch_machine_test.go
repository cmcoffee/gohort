package orchestrate

// Dispatching a machine: resolution, policy, and the refusals that have to
// differ from each other. What a run DOES is covered by the machine runtime's
// own tests; what these pin is who may ask for one and what they are told when
// they may not.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// newMachineDispatchTurn gives a caller agent in one user store, plus whatever
// machines a test wants reachable from it.
func newMachineDispatchTurn(t *testing.T, mode string, targets []string, build func(udb Database)) *chatTurn {
	t.Helper()
	root := &DBase{Store: kvlite.MemStore()}
	prevBase, prevRoot := orchestrateBaseDB, RootDB
	orchestrateBaseDB, RootDB = root, root
	t.Cleanup(func() { orchestrateBaseDB, RootDB = prevBase, prevRoot })

	udb := UserDB(root, "u")
	if build != nil {
		build(udb)
	}
	caller, err := saveAgent(udb, AgentRecord{
		Name: "Caller", Owner: "u", DispatchMode: mode,
		AllowedDispatchTargets: targets, OrchestratorPrompt: "p",
	})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	return &chatTurn{user: "u", udb: udb, agent: caller, ctx: context.Background()}
}

// saveRunnableMachine stores an unattended machine with no outstanding
// problems — the only kind a dispatch will actually run.
func saveRunnableMachine(t *testing.T, udb Database, name string) MachineDef {
	t.Helper()
	def := SaveMachineDef(udb, MachineDef{
		Name: name, Owner: "u", Unattended: true,
		Phases: []MachinePhase{{Name: "work", Prompt: "do the work"}},
	})
	if probs := def.Problems(); len(probs) > 0 {
		t.Fatalf("test machine %q is not runnable: %v", name, probs)
	}
	return def
}

// A machine resolves by name or id, the way the other two targets do — the
// model has names, so an id-only lookup would strand it.
func TestDispatchableMachineResolvesByNameAndID(t *testing.T) {
	var def MachineDef
	turn := newMachineDispatchTurn(t, dispatchAll, nil, func(udb Database) {
		def = saveRunnableMachine(t, udb, "Outage Review")
	})
	for _, ref := range []string{"Outage Review", "outage review", def.ID} {
		got, err := turn.dispatchableMachine(ref)
		if err != nil {
			t.Fatalf("resolving %q: %v", ref, err)
		}
		if got.ID != def.ID {
			t.Fatalf("resolving %q gave %q, want %q", ref, got.ID, def.ID)
		}
	}
	// A name that matches nothing is refused, and the refusal names what IS
	// runnable so the model can correct itself in one round.
	_, err := turn.dispatchableMachine("No Such Thing")
	if err == nil {
		t.Fatal("a machine that does not exist should be refused")
	}
	if !strings.Contains(err.Error(), "Outage Review") {
		t.Errorf("refusal should list what can be run; got: %v", err)
	}
}

// A conversational machine is not a target that is temporarily unavailable —
// it is a different kind of thing, and the refusal has to say so rather than
// reading as "no such machine", which would send the model hunting spellings.
func TestAConversationalMachineIsRefusedByName(t *testing.T) {
	turn := newMachineDispatchTurn(t, dispatchAll, nil, func(udb Database) {
		SaveMachineDef(udb, MachineDef{
			Name: "Intake", Owner: "u",
			Phases: []MachinePhase{{Name: "talk", Prompt: "greet", Resident: true}},
		})
	})
	_, err := turn.dispatchableMachine("Intake")
	if err == nil {
		t.Fatal("a machine that converses must not be dispatchable")
	}
	if !strings.Contains(err.Error(), "converses") {
		t.Errorf("refusal should say it converses; got: %v", err)
	}
	// And it is not advertised, because a target that always refuses teaches
	// the model that dispatch is unreliable.
	if names := turn.dispatchableMachineNames(10); len(names) != 0 {
		t.Errorf("a conversational machine was advertised: %v", names)
	}
}

// A machine with outstanding problems refuses at the GATE rather than half-way
// through a run — the same list the Run button and the schedule refuse on.
func TestAHalfBuiltMachineRefusesBeforeItSpendsAnything(t *testing.T) {
	turn := newMachineDispatchTurn(t, dispatchAll, nil, func(udb Database) {
		SaveMachineDef(udb, MachineDef{
			Name: "Half Built", Owner: "u", Unattended: true,
			// Hands off to a step that does not exist: saveable, not runnable.
			Phases: []MachinePhase{{Name: "work", Prompt: "do", Next: "nowhere"}},
		})
	})
	_, _, err := turn.machineDispatchGate(map[string]any{"machine": "Half Built", "message": "go"})
	if err == nil {
		t.Fatal("a machine with outstanding problems should be refused")
	}
	if !strings.Contains(err.Error(), "will not run yet") {
		t.Errorf("refusal should say what is wrong; got: %v", err)
	}
}

// A step that hands off to an agent, a pipeline or a child machine only runs in
// a conversation. A dispatched run would run it as an ordinary prompt and hand
// back an answer that looks right and did none of the arranged work — so it is
// refused, and the refusal NAMES the steps, because that is where the reader
// has to go.
func TestAMachineThatDelegatesCannotBeDispatched(t *testing.T) {
	turn := newMachineDispatchTurn(t, dispatchAll, nil, func(udb Database) {
		SaveMachineDef(udb, MachineDef{
			Name: "Hands Off", Owner: "u", Unattended: true,
			Phases: []MachinePhase{
				{Name: "gather", Prompt: "collect", Next: "ask"},
				{Name: "ask", Prompt: "have them look", Agent: "Researcher"},
			},
		})
	})
	_, _, err := turn.machineDispatchGate(map[string]any{"machine": "Hands Off", "message": "go"})
	if err == nil {
		t.Fatal("a machine with a delegating step must not be dispatched")
	}
	if !strings.Contains(err.Error(), `"ask"`) {
		t.Errorf("refusal should name the step; got: %v", err)
	}
	for _, field := range []string{"Pipeline", "Machine"} {
		ph := MachinePhase{Name: "ask", Prompt: "x"}
		switch field {
		case "Pipeline":
			ph.Pipeline = "Nightly"
		case "Machine":
			ph.Machine = "Child"
		}
		def := MachineDef{Name: "X", Phases: []MachinePhase{{Name: "gather", Prompt: "c"}, ph}}
		if got := machineStepsNeedingAConversation(def); len(got) != 1 {
			t.Errorf("a %s step should be named as needing a conversation; got %v", field, got)
		}
	}
}

// Allow none is absolute: it covers machines exactly as it covers agents and
// pipelines. An agent whose dispatch is off does not acquire a delegation
// channel because the target happens to be a third kind of thing.
func TestAllowNoneCoversMachines(t *testing.T) {
	turn := newMachineDispatchTurn(t, dispatchNone, nil, func(udb Database) {
		saveRunnableMachine(t, udb, "Outage Review")
	})
	if got := turn.dispatchableMachines(); len(got) != 0 {
		t.Fatalf("Allow none still advertised machines: %+v", got)
	}
	if _, err := turn.dispatchableMachine("Outage Review"); err == nil {
		t.Fatal("Allow none should refuse a machine")
	}
}

// Only mode governs machines too: the list holds TARGETS. An agent restricted
// to two named ones must not reach every machine its owner has.
func TestOnlyModeGovernsMachines(t *testing.T) {
	var allowed, other MachineDef
	turn := newMachineDispatchTurn(t, dispatchOnly, nil, func(udb Database) {
		allowed = saveRunnableMachine(t, udb, "Allowed")
		other = saveRunnableMachine(t, udb, "Other")
	})
	turn.agent.AllowedDispatchTargets = []string{allowed.ID}

	if _, err := turn.dispatchableMachine("Allowed"); err != nil {
		t.Fatalf("a listed machine should be reachable: %v", err)
	}
	_, err := turn.dispatchableMachine("Other")
	if err == nil {
		t.Fatal("an unlisted machine should be refused in Only mode")
	}
	// Named-and-refused is a different answer from never-existed, and
	// conflating them teaches the model to go looking for a spelling that
	// works.
	if !strings.Contains(err.Error(), "not on this agent's dispatch target list") {
		t.Errorf("refusal should name the policy; got: %v", err)
	}
	_ = other
}

// The self-heal that reads "no listed target still exists" as "this list is
// stale, fall back to Allow all" must see a machine as a live member. It could
// only see agents and pipelines, so a list naming exactly one machine looked
// empty — and an owner who allowed one procedure would have been granted the
// entire fleet.
func TestAListNamingOnlyAMachineIsNotStale(t *testing.T) {
	var def MachineDef
	turn := newMachineDispatchTurn(t, dispatchOnly, nil, func(udb Database) {
		def = saveRunnableMachine(t, udb, "Outage Review")
	})
	turn.agent.AllowedDispatchTargets = []string{def.ID}
	if !turn.dispatchListNamesARunnable() {
		t.Fatal("a dispatch list naming a live machine read as stale")
	}
	turn.agent.AllowedDispatchTargets = []string{"deleted-id"}
	if turn.dispatchListNamesARunnable() {
		t.Error("a list of dead ids read as healthy")
	}
}

// Authority must never GROW along a chain: a delegated agent cannot reach a
// machine the agent that delegated to it could not.
func TestTransitiveAuthorityBoundsAMachineDispatch(t *testing.T) {
	var def MachineDef
	turn := newMachineDispatchTurn(t, dispatchAll, nil, func(udb Database) {
		def = saveRunnableMachine(t, udb, "Outage Review")
	})
	turn.dispatchOrigin = &dispatchAuthority{
		AgentID: "origin", AgentName: "Origin", Mode: dispatchOnly, Targets: []string{"something-else"},
	}
	_, _, err := turn.machineDispatchGate(map[string]any{"machine": def.Name, "message": "go"})
	if err == nil {
		t.Fatal("a chain must not reach further than its originator")
	}
	if !strings.Contains(err.Error(), "on behalf of") {
		t.Errorf("refusal should explain the chain; got: %v", err)
	}
	// The same authority in Allow-all mode constrains nothing.
	turn.dispatchOrigin = &dispatchAuthority{AgentID: "origin", AgentName: "Origin", Mode: dispatchAll}
	if _, _, err := turn.machineDispatchGate(map[string]any{"machine": def.Name, "message": "go"}); err != nil {
		t.Errorf("an unrestricted originator should constrain nothing: %v", err)
	}
}

// A machine whose step dispatches back into the same machine is a loop no
// depth counter catches quickly — each hop resets the per-turn depth.
func TestAMachineCannotReEnterItself(t *testing.T) {
	var def MachineDef
	turn := newMachineDispatchTurn(t, dispatchAll, nil, func(udb Database) {
		def = saveRunnableMachine(t, udb, "Outage Review")
	})
	turn.ctx = withDispatchedMachine(context.Background(), def.ID)
	_, _, err := turn.machineDispatchGate(map[string]any{"machine": def.Name, "message": "go"})
	if err == nil {
		t.Fatal("a machine already running above this call must not be re-entered")
	}
	if !strings.Contains(err.Error(), "already running above this call") {
		t.Errorf("refusal should name the cycle; got: %v", err)
	}
}

// A machine shared with this user is a dispatch target like any other: the run
// happens in the REQUESTER's namespace, so no guard turns on whose recipe it
// is. Provenance shows in the label, because "why is my agent running Outage
// Review" has a different answer depending on whose Outage Review it is.
func TestASharedMachineIsDispatchable(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevBase, prevRoot, prevSaved := orchestrateBaseDB, RootDB, MachineSavedHook
	orchestrateBaseDB, RootDB, MachineSavedHook = root, root, syncMachineShareIndex
	t.Cleanup(func() { orchestrateBaseDB, RootDB, MachineSavedHook = prevBase, prevRoot, prevSaved })

	SaveMachineDef(UserDB(root, "owner"), MachineDef{
		Name: "Outage Review", Owner: "owner", Unattended: true, AllowedUsers: []string{"u"},
		Phases: []MachinePhase{{Name: "work", Prompt: "do"}},
	})
	udb := UserDB(root, "u")
	caller, err := saveAgent(udb, AgentRecord{Name: "Caller", Owner: "u", DispatchMode: dispatchAll, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	turn := &chatTurn{user: "u", udb: udb, agent: caller, ctx: context.Background()}

	got, err := turn.dispatchableMachine("Outage Review")
	if err != nil {
		t.Fatalf("a shared machine should be dispatchable: %v", err)
	}
	if !turn.foreignMachine(got) {
		t.Error("a shared machine should read as somebody else's")
	}
	if label := machineRunLabel(turn, got); !strings.Contains(label, "shared by owner") {
		t.Errorf("the run label should carry provenance; got %q", label)
	}
}

// A shared machine whose name collides with one of the user's own is dropped
// rather than listed: the model has names and not ids, so listing it would
// advertise a target it can never reach.
func TestASharedMachineWithACollidingNameIsDropped(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	prevBase, prevRoot, prevSaved := orchestrateBaseDB, RootDB, MachineSavedHook
	orchestrateBaseDB, RootDB, MachineSavedHook = root, root, syncMachineShareIndex
	t.Cleanup(func() { orchestrateBaseDB, RootDB, MachineSavedHook = prevBase, prevRoot, prevSaved })

	SaveMachineDef(UserDB(root, "owner"), MachineDef{
		Name: "Outage Review", Owner: "owner", Unattended: true, AllowedUsers: []string{"u"},
		Phases: []MachinePhase{{Name: "work", Prompt: "theirs"}},
	})
	udb := UserDB(root, "u")
	mine := SaveMachineDef(udb, MachineDef{
		Name: "Outage Review", Owner: "u", Unattended: true,
		Phases: []MachinePhase{{Name: "work", Prompt: "mine"}},
	})
	caller, err := saveAgent(udb, AgentRecord{Name: "Caller", Owner: "u", DispatchMode: dispatchAll, OrchestratorPrompt: "p"})
	if err != nil {
		t.Fatalf("save caller: %v", err)
	}
	turn := &chatTurn{user: "u", udb: udb, agent: caller, ctx: context.Background()}

	if got := turn.machineUniverse(); len(got) != 1 || got[0].ID != mine.ID {
		t.Fatalf("the colliding shared machine should be dropped; got %+v", got)
	}
	resolved, err := turn.dispatchableMachine("Outage Review")
	if err != nil || resolved.ID != mine.ID {
		t.Fatalf("the name must resolve to the user's OWN machine: %+v (%v)", resolved, err)
	}
}
