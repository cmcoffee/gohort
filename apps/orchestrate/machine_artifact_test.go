package orchestrate

// The "machine" artifact type. What matters is the two-way closure with
// agents: an agent's recipe carries its Machine pointer, so the bundle
// has to carry the machine — an agent imported without it walks and
// talks while quietly not being what its author built.

import (
	"encoding/json"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func machineArtifactApp(t *testing.T) *OrchestrateApp {
	t.Helper()
	app := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	RegisterAgentArtifactType(app)
	RegisterMachineArtifactType(app)
	return app
}

func TestMachineArtifact_ExportShapeAndDelegateNames(t *testing.T) {
	app := machineArtifactApp(t)
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	analyst, _ := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Log analyst", OrchestratorPrompt: "dig"})
	def := SaveMachineDef(udb, MachineDef{
		Owner: "alice", Name: "Investigation", Start: "triage",
		Phases: []MachinePhase{
			{Name: "triage", Prompt: "route", Choices: []string{"verify"}, Next: "verify", Agent: analyst.ID},
			{Name: "verify", Prompt: "check", Resident: true},
		},
	})

	m := &machineArtifact{app: app}
	raw, err := m.ExportArtifact(root, "Investigation", "alice")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var got MachineDef
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("recipe: %v", err)
	}
	if got.ID != def.ID {
		t.Fatalf("ID must travel — it is what an agent's Machine pointer references, got %q want %q", got.ID, def.ID)
	}
	if got.Owner != "" || !got.Created.IsZero() || !got.Updated.IsZero() {
		t.Fatalf("owner/timestamps must not travel: %+v", got)
	}
	if got.Phases[0].Agent != "Log analyst" {
		t.Fatalf("a delegate ID must normalize to the agent's name, got %q", got.Phases[0].Agent)
	}
	if stored, _ := LoadMachineDef(udb, "alice", def.ID); stored.Phases[0].Agent != analyst.ID {
		t.Fatalf("export must not mutate the stored def: %+v", stored.Phases[0])
	}

	// The machine's own dependency walk carries the delegate.
	deps := m.Dependencies(root, "Investigation", "alice")
	found := false
	for _, d := range deps {
		if d.Type == "agent" && d.Name == "Log analyst" {
			found = true
		}
	}
	if !found {
		t.Errorf("the delegate should ride in the bundle: %+v", deps)
	}
}

// The gap this type exists to close: the agent's dependency walk names
// the machine its recipe points at, so an exported agent arrives with
// its whole procedure rather than a dangling pointer.
func TestAgentBundleCarriesItsMachine(t *testing.T) {
	app := machineArtifactApp(t)
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(app.DB, "alice")
	def := SaveMachineDef(udb, MachineDef{Owner: "alice", Name: "Investigation", Start: "s",
		Phases: []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}})
	if _, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Wren",
		OrchestratorPrompt: "hi", Machine: def.ID}); err != nil {
		t.Fatal(err)
	}

	a := &agentArtifact{app: app}
	deps := a.Dependencies(root, "Wren", "alice")
	found := false
	for _, d := range deps {
		if d.Type == "machine" && d.Name == def.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the agent's machine must ride in the bundle: %+v", deps)
	}

	// And the machine type resolves that ID-shaped reference on export —
	// the closure addresses it the way the agent does.
	m := &machineArtifact{app: app}
	if _, err := m.ExportArtifact(root, def.ID, "alice"); err != nil {
		t.Fatalf("export by ID: %v", err)
	}
}

// Bundle import must SKIP a same-ID machine, not copy it: the existing
// one already serves the agent pointer the traveled ID exists for.
func TestMachineArtifact_ImportSkipRules(t *testing.T) {
	app := machineArtifactApp(t)
	m := &machineArtifact{app: app}
	phases := []MachinePhase{{Name: "s", Prompt: "p", Resident: true}}
	recipe, _ := json.Marshal(MachineDef{ID: "mach-7", Name: "Investigation", Phases: phases})

	name, skip, err := m.ImportArtifact(nil, recipe, "alice")
	if err != nil || skip != "" {
		t.Fatalf("first import should land: %q %q %v", name, skip, err)
	}
	udb := UserDB(app.DB, "alice")
	if _, ok := LoadMachineDef(udb, "alice", "mach-7"); !ok {
		t.Fatal("the traveled ID must be preserved — it is the agent's pointer")
	}

	// Same recipe again: skipped, not copied.
	if _, skip, err = m.ImportArtifact(nil, recipe, "alice"); err != nil || skip == "" {
		t.Fatalf("a same-id machine should skip: %q %v", skip, err)
	}
	if got := len(ListMachineDefs(udb, "alice")); got != 1 {
		t.Fatalf("skip means skip — expected 1 machine, got %d", got)
	}

	// Same name under a different ID: also skipped, never clobbered.
	other, _ := json.Marshal(MachineDef{ID: "mach-8", Name: "investigation", Phases: phases})
	if _, skip, err = m.ImportArtifact(nil, other, "alice"); err != nil || skip == "" {
		t.Fatalf("a same-named machine should skip: %q %v", skip, err)
	}
}

// A stored draft with outstanding problems exports; it must also come
// BACK. The import door refusing what the editor legally keeps meant an
// agent bundled with its half-built machine landed pointing at a
// machine the refusal threw away.
func TestAnImperfectDraftRoundTripsThroughImport(t *testing.T) {
	app := machineArtifactApp(t)
	m := &machineArtifact{app: app}
	// One problem: a transient step that goes nowhere. Stored fine by
	// the editor; the checklist reports it as work remaining.
	recipe, _ := json.Marshal(MachineDef{ID: "mach-9", Name: "Half-built", Phases: []MachinePhase{
		{Name: "route", Prompt: "decide"},
		{Name: "answer", Prompt: "reply", Resident: true},
	}})
	name, skip, err := m.ImportArtifact(nil, recipe, "alice")
	if err != nil || skip != "" {
		t.Fatalf("an imperfect draft must land, got %q %q %v", name, skip, err)
	}
	udb := UserDB(app.DB, "alice")
	saved, ok := LoadMachineDef(udb, "alice", "mach-9")
	if !ok {
		t.Fatal("the draft was not stored")
	}
	if len(saved.Problems()) == 0 {
		t.Error("the problem should survive too — the checklist is where it belongs")
	}

	// Structural emptiness is still refused: that is a decode accident,
	// not a draft.
	empty, _ := json.Marshal(MachineDef{Name: "Nothing"})
	if _, _, err := m.ImportArtifact(nil, empty, "alice"); err == nil {
		t.Error("a recipe with no steps should be refused")
	}
}
