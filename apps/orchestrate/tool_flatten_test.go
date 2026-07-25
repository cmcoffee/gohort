package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The namespace flatten: one record per (user, tool name) in the unified
// store, scope as data (ScopeAgents). These tests pin the three properties
// the flatten exists for: the migration folds embedded record tools without
// losing anything, an edit to a tool IS the agent's tool (no second copy to
// fork), and scope transitions flip fields instead of copying.

func TestFlattenMigrationFoldsRecordTools(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)

	// Pre-existing store rows: one identical to a record copy, one diverged.
	if err := AdminPersistTempTool(udb, owner, TempTool{Name: "same_tool", Description: "identical"}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if err := AdminPersistTempTool(udb, owner, TempTool{Name: "forked_tool", Description: "store version"}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	rec, err := saveAgent(udb, AgentRecord{
		Name: "Molty", OrchestratorPrompt: "p", Owner: owner,
		Tools: []TempTool{
			{Name: "own_tool", Description: "only on the record"},
			{Name: "same_tool", Description: "identical"},
			{Name: "forked_tool", Description: "record version"},
		},
	})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}

	migrateAgentToolsToStore(udb, owner, &rec)

	if len(rec.Tools) != 0 {
		t.Fatalf("record must be tool-free after fold, has %d", len(rec.Tools))
	}
	got, ok := loadAgent(udb, rec.ID)
	if !ok || len(got.Tools) != 0 {
		t.Fatalf("persisted record must be tool-free after fold")
	}
	// own_tool → new store row scoped to the agent.
	p, ok := UserToolByName(udb, owner, "own_tool")
	if !ok || !p.ScopedToAgent(rec.ID) {
		t.Fatalf("own_tool must land in the store scoped to the agent, got %+v", p)
	}
	// same_tool → merged; stays shared (empty scope), still one row.
	p, ok = UserToolByName(udb, owner, "same_tool")
	if !ok || len(p.ScopeAgents) != 0 {
		t.Fatalf("identical dup must merge into the shared row, got %+v", p.ScopeAgents)
	}
	// forked_tool → store version wins; record version orphaned with provenance.
	p, _ = UserToolByName(udb, owner, "forked_tool")
	if p.Tool.Description != "store version" {
		t.Fatalf("diverged fold must keep the store copy, got %q", p.Tool.Description)
	}
	var foundOrphan bool
	for _, o := range LoadOrphanedTempTools(udb, owner) {
		if o.Tool.Name == "forked_tool" && o.Tool.Description == "record version" && o.FormerAgentID == rec.ID {
			foundOrphan = true
		}
	}
	if !foundOrphan {
		t.Fatal("diverged record copy must be stashed in the orphan pool with provenance")
	}
	// Backup written verbatim.
	var backup []TempTool
	if !udb.Get(toolFlattenBackupTable, owner+":"+rec.ID, &backup) || len(backup) != 3 {
		t.Fatalf("pre-migration backup missing or short: %d", len(backup))
	}
	// Idempotent: second run no-ops.
	migrateAgentToolsToStore(udb, owner, &rec)
	if p, _ := UserToolByName(udb, owner, "own_tool"); len(p.ScopeAgents) != 1 {
		t.Fatalf("re-run must not duplicate scope entries: %v", p.ScopeAgents)
	}
}

// The property the user asked for by name: "if Builder is updating the
// moltbook tool, it should be the same moltbook tool the agent uses." An
// update through the store IS the agent's tool — same row, scope intact.
func TestEditIsTheAgentsTool(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	rec, _ := saveAgent(udb, AgentRecord{Name: "Molty", OrchestratorPrompt: "p", Owner: owner})

	if err := bundleAgentToolByID(udb, owner, rec.ID, TempTool{Name: "moltbook", Description: "v1"}); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	// Builder-style edit: re-persist the def by name (the finalize path).
	if err := AdminPersistTempTool(udb, owner, TempTool{Name: "moltbook", Description: "v2 fixed"}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	scoped := AgentScopedTools(udb, owner, rec.ID)
	if len(scoped) != 1 || scoped[0].Tool.Description != "v2 fixed" {
		t.Fatalf("the agent's kit must carry the edited definition, got %+v", scoped)
	}
	if all := LoadPersistentTempTools(udb, owner); len(all) != 1 {
		t.Fatalf("one name must be ONE record, got %d", len(all))
	}
	if shared := SharedUserTools(udb, owner); len(shared) != 0 {
		t.Fatalf("agent-scoped row must not surface as a shared/pool tool: %+v", shared)
	}
}

func TestScopeTransitionsFlipFieldsNotCopies(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	a1, _ := saveAgent(udb, AgentRecord{Name: "A1", OrchestratorPrompt: "p", Owner: owner})
	a2, _ := saveAgent(udb, AgentRecord{Name: "A2", OrchestratorPrompt: "p", Owner: owner})

	if err := bundleAgentToolByID(udb, owner, a1.ID, TempTool{Name: "flip_tool", Description: "d"}); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	if err := bundleAgentToolByID(udb, owner, a2.ID, TempTool{Name: "flip_tool", Description: "d"}); err != nil {
		t.Fatalf("bundle 2nd: %v", err)
	}
	p, _ := UserToolByName(udb, owner, "flip_tool")
	if len(p.ScopeAgents) != 2 {
		t.Fatalf("two carriers = one row with two scope entries, got %v", p.ScopeAgents)
	}
	// Promote → shared.
	if err := promoteScopedToGlobal(udb, udb, owner, "flip_tool"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if p, _ = UserToolByName(udb, owner, "flip_tool"); len(p.ScopeAgents) != 0 {
		t.Fatalf("promote must clear scope, got %v", p.ScopeAgents)
	}
	if len(LoadPersistentTempTools(udb, owner)) != 1 {
		t.Fatal("promote must not create a second record")
	}
}

func TestUnbundleLastCarrierOrphans(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	a1, _ := saveAgent(udb, AgentRecord{Name: "A1", OrchestratorPrompt: "p", Owner: owner})

	if err := bundleAgentToolByID(udb, owner, a1.ID, TempTool{Name: "solo_tool", Description: "d"}); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	if err := unbundleAgentToolByID(udb, owner, a1.ID, "solo_tool"); err != nil {
		t.Fatalf("unbundle: %v", err)
	}
	if _, ok := UserToolByName(udb, owner, "solo_tool"); ok {
		t.Fatal("last-carrier unbundle must remove the store row")
	}
	var orphaned bool
	for _, o := range LoadOrphanedTempTools(udb, owner) {
		if o.Tool.Name == "solo_tool" && strings.Contains(o.FormerAgentName, "A1") {
			orphaned = true
		}
	}
	if !orphaned {
		t.Fatal("last-carrier unbundle must orphan the definition, not destroy it")
	}
}

func TestCloneSharesScopedTools(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	src, _ := saveAgent(udb, AgentRecord{Name: "Src", OrchestratorPrompt: "p", Owner: owner})
	if err := bundleAgentToolByID(udb, owner, src.ID, TempTool{Name: "shared_kit", Description: "d"}); err != nil {
		t.Fatalf("bundle: %v", err)
	}
	clone, err := cloneAgent(udb, src.ID, owner, "Src copy", false)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	p, _ := UserToolByName(udb, owner, "shared_kit")
	if !p.ScopedToAgent(src.ID) || !p.ScopedToAgent(clone.ID) {
		t.Fatalf("clone must SHARE the scoped tool (one record, both agents), got %v", p.ScopeAgents)
	}
	if len(LoadPersistentTempTools(udb, owner)) != 1 {
		t.Fatal("clone must not fork a second record")
	}
}
