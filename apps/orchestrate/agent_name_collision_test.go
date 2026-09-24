package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Two agents with the same name used to resolve to whichever listAgents
// emitted first, and that order was not even stable. A name that answers to
// two agents must be refused with both ids, never guessed.
func TestSameNamedAgentsAreAmbiguousNotFirstMatch(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	a1, err := saveAgent(db, AgentRecord{Name: "Report Writer", Owner: owner, OrchestratorPrompt: "one"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := saveAgent(db, AgentRecord{Name: "report writer", Owner: owner, OrchestratorPrompt: "two"})
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := resolveAgentRef(db, owner, "Report Writer")
	if ok || err == nil {
		t.Fatalf("a name two agents answer to resolved (ok=%v err=%v)", ok, err)
	}
	for _, id := range []string{a1.ID, a2.ID} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("the ambiguity error does not name candidate %s: %v", id, err)
		}
	}
	if _, ok := findAgentByNameOrID(db, owner, "Report Writer"); ok {
		t.Error("findAgentByNameOrID picked one of two same-named agents")
	}
	// Each id still addresses exactly its own agent.
	if got, ok := findAgentByNameOrID(db, owner, a2.ID); !ok || got.ID != a2.ID {
		t.Errorf("lookup by id resolved to %q", got.ID)
	}
}

// Destructive tools must never act on an ambiguous name: delete_agent used to
// delete whichever same-named agent sorted first.
func TestDeleteAndUpdateRefuseAnAmbiguousName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	a1, _ := saveAgent(db, AgentRecord{Name: "Dup", Owner: owner, OrchestratorPrompt: "one"})
	a2, _ := saveAgent(db, AgentRecord{Name: "Dup", Owner: owner, OrchestratorPrompt: "two"})
	sess := &ToolSession{Username: owner, DB: db}

	_, err := (deleteAgentTool{}).RunWithSession(map[string]any{"id": "Dup"}, sess)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("delete_agent on an ambiguous name: err=%v", err)
	}
	for _, id := range []string{a1.ID, a2.ID} {
		if _, ok := loadAgent(db, id); !ok {
			t.Errorf("delete_agent removed %s on an ambiguous name", id)
		}
	}
	if _, err := (updateAgentTool{}).RunWithSession(map[string]any{"id": "Dup", "description": "x"}, sess); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("update_agent on an ambiguous name: err=%v", err)
	}
	if _, err := (cloneAgentTool{}).RunWithSession(map[string]any{"id": "Dup", "name": "Copy"}, sess); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("clone_agent on an ambiguous name: err=%v", err)
	}
	// By id the same call goes through.
	if _, err := (deleteAgentTool{}).RunWithSession(map[string]any{"id": a1.ID}, sess); err != nil {
		t.Fatalf("delete_agent by id: %v", err)
	}
	if _, ok := loadAgent(db, a2.ID); !ok {
		t.Fatal("deleting one id took the other same-named agent too")
	}
}

// A stronger match in ANY group beats a weaker match in an earlier one. The
// old cascade ran every tier over the user's own agents first, so an own agent
// matching only by prefix beat a shared agent whose name was typed in full.
func TestAnExactSharedNameBeatsAPartialOwnName(t *testing.T) {
	own := []AgentRecord{{ID: "own-pro", Name: "Market Scan Pro", Owner: "u"}}
	shared := []AgentRecord{{ID: "shared-exact", Name: "Market Scan", Owner: "someone"}}
	got, ok, err := resolveAgentName(own, shared, nil, "Market Scan", "u")
	if err != nil || !ok || got.ID != "shared-exact" {
		t.Fatalf("resolved to %q (ok=%v err=%v), want the exact shared match", got.ID, ok, err)
	}
}

// Own and shared are one rank: the same name in both is a choice for the
// caller, and the error says whose each one is.
func TestOwnAndSharedSameNameIsAmbiguous(t *testing.T) {
	own := []AgentRecord{{ID: "mine", Name: "Triage", Owner: "u"}}
	shared := []AgentRecord{{ID: "theirs", Name: "Triage", Owner: "someone"}}
	_, ok, err := resolveAgentName(own, shared, nil, "Triage", "u")
	if ok || err == nil {
		t.Fatalf("own+shared tie resolved (ok=%v err=%v)", ok, err)
	}
	msg := err.Error()
	for _, want := range []string{"id=mine", "id=theirs", "shared by someone"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ambiguity error missing %q: %s", want, msg)
		}
	}
	// The deliberate exception stands: the user's own agent beats a framework
	// agent of the same name.
	fw := []AgentRecord{{ID: "seed-x", Name: "Triage"}}
	got, ok, err := resolveAgentName(own, nil, fw, "Triage", "u")
	if err != nil || !ok || got.ID != "mine" {
		t.Fatalf("own vs framework resolved to %q (ok=%v err=%v), want the user's own", got.ID, ok, err)
	}
}

// listAgents orders same-named agents by id, so every surface that shows
// them shows them the same way on every run.
func TestListAgentsOrdersSameNamesByID(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	for _, id := range []string{"zz-3", "aa-1", "mm-2"} {
		if _, err := saveAgent(db, AgentRecord{ID: id, Name: "Same", Owner: owner, OrchestratorPrompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, a := range listAgents(db, owner) {
		if a.Name == "Same" {
			got = append(got, a.ID)
		}
	}
	if strings.Join(got, ",") != "aa-1,mm-2,zz-3" {
		t.Fatalf("same-named agents listed as %v, want id order", got)
	}
}

// Agent import used to skip only an exact-case clash, so "research agent"
// landed beside "Research Agent". And a skipped clash must say what it does
// to the bundle's pipelines and machines, which name the agent and will now
// run the importer's own.
func TestAgentImportNameClashIsCaseBlindAndSaysWhatItBinds(t *testing.T) {
	app := pipelineArtifactApp(t)
	udb := UserDB(app.DB, "alice")
	if _, err := saveAgent(udb, AgentRecord{Owner: "alice", Name: "Research Agent", OrchestratorPrompt: "mine"}); err != nil {
		t.Fatal(err)
	}
	recipe, _ := json.Marshal(agentExport{AgentRecord: AgentRecord{Name: "research agent", OrchestratorPrompt: "theirs"}})
	name, skip, err := (&agentArtifact{app: app}).ImportArtifact(nil, recipe, "alice")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if skip == "" {
		t.Fatalf("%q imported beside an agent differing only in case", name)
	}
	for _, want := range []string{"Research Agent", "pipeline or machine"} {
		if !strings.Contains(skip, want) {
			t.Errorf("skip reason does not mention %q: %s", want, skip)
		}
	}
	n := 0
	for _, a := range listAgents(udb, "alice") {
		if strings.EqualFold(a.Name, "research agent") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want one research agent after the skipped import, got %d", n)
	}
	// And the preview's existence probe agrees with the import.
	if _, err := (&agentArtifact{app: app}).ExportArtifact(nil, "research agent", "alice"); err != nil {
		t.Errorf("a case-drifted name does not read as existing, so the preview would predict an import: %v", err)
	}
}

// A pipeline NAME on a dispatch list is pinned to the id it meant when saved,
// so a different pipeline later given that name is not authorized by it.
func TestDispatchTargetNamePinsToIDOnSave(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	orig := SavePipelineDef(db, PipelineDef{Owner: owner, Name: "Weekly Digest",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "x"}}})
	a, err := saveAgent(db, AgentRecord{Name: "Caller", Owner: owner, OrchestratorPrompt: "p",
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"weekly digest"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.AllowedDispatchTargets) != 1 || a.AllowedDispatchTargets[0] != orig.ID {
		t.Fatalf("saved list = %v, want the pipeline id %s", a.AllowedDispatchTargets, orig.ID)
	}
	DeletePipelineDef(db, orig.ID)
	later := SavePipelineDef(db, PipelineDef{Owner: owner, Name: "Weekly Digest",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "y"}}})
	got, _ := loadAgent(db, a.ID)
	if dispatchListNames(got.AllowedDispatchTargets, later.ID, later.Name) {
		t.Fatal("a pipeline created later under the same name is authorized by the old grant")
	}
}

// A row saved before pinning existed still names the pipeline. The first read
// pins it to what carries the name then, and a namesake made afterwards is not
// on the list.
func TestLegacyDispatchTargetNamePinsOnRead(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const owner = "u"
	orig := SavePipelineDef(db, PipelineDef{Owner: owner, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "x"}}})
	legacy := AgentRecord{ID: "legacy-caller", Name: "Caller", Owner: owner, OrchestratorPrompt: "p",
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{"Nightly"}}
	db.Set(agentsTable, legacy.ID, legacy) // raw, as an older build wrote it
	got, ok := loadAgent(db, legacy.ID)
	if !ok || len(got.AllowedDispatchTargets) != 1 || got.AllowedDispatchTargets[0] != orig.ID {
		t.Fatalf("legacy list read as %v, want pinned to %s", got.AllowedDispatchTargets, orig.ID)
	}
	var raw AgentRecord
	db.Get(agentsTable, legacy.ID, &raw)
	if len(raw.AllowedDispatchTargets) != 1 || raw.AllowedDispatchTargets[0] != orig.ID {
		t.Fatalf("pin was not persisted: %v", raw.AllowedDispatchTargets)
	}
	DeletePipelineDef(db, orig.ID)
	later := SavePipelineDef(db, PipelineDef{Owner: owner, Name: "Nightly",
		Stages: []PipelineStage{{Name: "s", Kind: StageWorker, Prompt: "y"}}})
	got, _ = loadAgent(db, legacy.ID)
	if dispatchListNames(got.AllowedDispatchTargets, later.ID, later.Name) {
		t.Fatal("a legacy name entry authorized a pipeline created after it was pinned")
	}
	// Agent ids on the list are never touched.
	b, _ := saveAgent(db, AgentRecord{Name: "Target", Owner: owner, OrchestratorPrompt: "p"})
	c, _ := saveAgent(db, AgentRecord{Name: "Caller2", Owner: owner, OrchestratorPrompt: "p",
		DispatchMode: dispatchOnly, AllowedDispatchTargets: []string{b.ID}})
	if len(c.AllowedDispatchTargets) != 1 || c.AllowedDispatchTargets[0] != b.ID {
		t.Fatalf("an agent id on the list was rewritten: %v", c.AllowedDispatchTargets)
	}
}

// A listing that addresses agents by name prints the id beside the ones whose
// names collide, and only those.
func TestAgentListingRefShowsIDOnlyOnCollision(t *testing.T) {
	list := []AgentRecord{
		{ID: "a1", Name: "Scout", Owner: "u"},
		{ID: "a2", Name: "scout", Owner: "someone"},
		{ID: "a3", Name: "Editor", Owner: "u"},
	}
	col := agentNameCollisions(list)
	if got := agentListingRef(list[0], col, "u"); got != "Scout (id=a1)" {
		t.Errorf("own colliding ref = %q", got)
	}
	if got := agentListingRef(list[1], col, "u"); got != "scout (id=a2, shared by someone)" {
		t.Errorf("shared colliding ref = %q", got)
	}
	if got := agentListingRef(list[2], col, "u"); got != "Editor" {
		t.Errorf("a unique name should print bare, got %q", got)
	}
}

// Attaching a machine by agent name took the first of several same-named
// agents. It now refuses and names the ids.
func TestMachineAttachRefusesAnAmbiguousAgentName(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	udb := agentUserDB(db, "alice")
	for _, id := range []string{"dup-1", "dup-2"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Owner: "alice", Name: "Dup", OrchestratorPrompt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	turn := &chatTurn{udb: udb, user: "alice"}
	if _, why := turn.ownAgentByNameOrID("Dup"); !strings.Contains(why, "dup-1") || !strings.Contains(why, "dup-2") {
		t.Errorf("an ambiguous name was not refused with its ids: %q", why)
	}
	if ag, why := turn.ownAgentByNameOrID("dup-2"); why != "" || ag.ID != "dup-2" {
		t.Errorf("an id should resolve directly: %q %q", ag.ID, why)
	}
}
