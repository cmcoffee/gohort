package orchestrate

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
	"github.com/cmcoffee/snugforge/kvlite"
)

// From an observed Builder session: asked to fold a set of calendar tools into
// one, Builder re-pointed an agent by sending update_agent the agent's tools
// in the shape agents(action="get") lists them, {name, mode}. Each entry was
// read as a new, EMPTY definition, so the owner was asked to "replace the
// definition" of an unrelated phone-call tool the agent already had, and a yes
// would have wiped it.

func TestANameOnlyToolEntryAttachesTheToolUnchanged(t *testing.T) {
	sess, parent, helper := addToolTestSess(t)
	calls := TempTool{Name: "phone_calls", Description: "Places calls.", Mode: "api", CommandTemplate: "/calls", Credential: "phone_api", Method: "POST"}
	if err := bundleAgentToolByID(sess.DB, sess.Username, parent.ID, calls); err != nil {
		t.Fatal(err)
	}
	asked := 0
	sess.ApproveChange = func(kind, name, detail string, op ChangeOp) string {
		if kind == "tool" {
			asked++
			return "NOT DONE: the owner declined this change to the tool " + name
		}
		return ""
	}

	// Both agents get it by name: no ask, the definition kept, and the second
	// agent added to its scope.
	for _, a := range []AgentRecord{parent, helper} {
		out, err := updateAgentTool{}.RunWithSession(map[string]any{"id": a.ID, "tools": []any{map[string]any{"name": "phone_calls", "mode": "api"}}}, sess)
		if err != nil || strings.Contains(out, "NOT DONE") {
			t.Fatalf("%s: a name-only entry was refused: %v %s", a.Name, err, out)
		}
	}
	if asked != 0 {
		t.Errorf("a name-only entry asked to replace the tool %d time(s)", asked)
	}
	got, ok := UserToolByName(sess.DB, sess.Username, "phone_calls")
	if !ok || !got.Tool.SameDefinition(calls) {
		t.Fatalf("the tool's definition changed: %+v", got.Tool)
	}
	if !got.ScopedToAgent(parent.ID) || !got.ScopedToAgent(helper.ID) {
		t.Errorf("scope = %v, want both agents", got.ScopeAgents)
	}

	// A name that is no tool of theirs has nothing to attach.
	out, err := updateAgentTool{}.RunWithSession(map[string]any{"id": parent.ID, "tools": []any{map[string]any{"name": "no_such_tool"}}}, sess)
	if err != nil || !strings.Contains(out, "no tool by that name") {
		t.Errorf("an unknown name-only entry: %v %q", err, out)
	}
	if _, ok := UserToolByName(sess.DB, sess.Username, "no_such_tool"); ok {
		t.Error("an empty tool was saved under an unknown name")
	}

	// A real new definition under the same name still asks.
	changed := calls
	changed.CommandTemplate = "/calls/v2"
	out, _ = updateAgentTool{}.RunWithSession(map[string]any{"id": parent.ID, "tools": []any{changed}}, sess)
	if asked != 1 || !strings.Contains(out, "declined") {
		t.Errorf("a changed definition went ahead unasked: asked %d, %q", asked, out)
	}
}

// Phase 0 of the AgentRecord split: app agents can't hold LLM-authored tools and
// aren't scope targets — enforced by IDENTITY (the appagents registry), not the
// drift-able Hidden/Owner proxies. A VISIBLE app agent (Hidden:false) is the
// case the old proxies leaked, so these register one and assert it's still
// walled off.

const phase0AppID = "test-phase0-appagent-guard"

func init() {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID: phase0AppID, Name: "Phase0App", Prompt: "p", Hidden: false, // deliberately VISIBLE
	})
}

func TestBundleAgentToolRefusesAppAgent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	base := AgentRecord{ID: phase0AppID, Name: "Phase0App"}
	err := bundleAgentTool(db, "alice", base, TempTool{Name: "ts3_list_clients"})
	if err == nil || !strings.Contains(err.Error(), "app agent") {
		t.Fatalf("bundleAgentTool must refuse an app agent; got %v", err)
	}
}

func TestBundleAgentToolByIDRefusesAppAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := agentUserDB(root, "alice")
	err := bundleAgentToolByID(udb, "alice", phase0AppID, TempTool{Name: "ts3_list_clients"})
	if err == nil || !strings.Contains(err.Error(), "app agent") {
		t.Fatalf("bundleAgentToolByID must refuse an app agent; got %v", err)
	}
}

// Removal must stay OPEN so an already-mis-scoped tool can be cleaned off an app
// agent — only the write (bundle) path is guarded, not unbundle.
func TestUnbundleFromAppAgentStillWorks(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := agentUserDB(root, "alice")
	// Seed a stuck tool: flattened namespace, so the mis-scope is a store row
	// whose ScopeAgents names the app agent (bundleAgentToolByID refuses app
	// agents, so write the bad state directly, as the old bug did).
	if _, err := saveAgent(udb, AgentRecord{
		ID: phase0AppID, Name: "Phase0App", OrchestratorPrompt: "p", Owner: seedOwner,
	}); err != nil {
		t.Fatalf("seed app agent: %v", err)
	}
	if err := AdminPersistTempTool(udb, "alice", TempTool{Name: "ts3_list_clients"}); err != nil {
		t.Fatalf("seed tool: %v", err)
	}
	if !SetUserToolScopeAgents(udb, "alice", "ts3_list_clients", []string{phase0AppID}) {
		t.Fatal("seed mis-scope failed")
	}
	if err := unbundleAgentToolByID(udb, "alice", phase0AppID, "ts3_list_clients"); err != nil {
		t.Fatalf("must be able to strip a mis-scoped tool off an app agent; got %v", err)
	}
}

func TestToolScopePillExcludesVisibleAppAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)
	user, err := saveAgent(udb, AgentRecord{Name: "Mine", OrchestratorPrompt: "p", Owner: owner})
	if err != nil {
		t.Fatalf("save user agent: %v", err)
	}
	if err := AdminPersistTempTool(root, owner, TempTool{Name: "gtool"}); err != nil {
		t.Fatalf("persist global tool: %v", err)
	}

	st, ok := toolScopeState(root, owner, "gtool")
	if !ok {
		t.Fatal("expected scope state for a global tool")
	}
	sawUser, sawApp := false, false
	for _, ag := range st.Agents {
		if ag.ID == user.ID {
			sawUser = true
		}
		if ag.ID == phase0AppID {
			sawApp = true
		}
	}
	if sawApp {
		t.Fatal("a VISIBLE app agent must NOT be a tool-scope pill target (identity exclusion)")
	}
	if !sawUser {
		t.Fatal("the user's own agent should be a scope target")
	}
}

// Framework seeds are retired from the scope-pill surfaces, and the exclusion
// has to be by IDENTITY. A seed the user customized exists as a SHADOW — the
// seed's ID with the user's Owner — and the old Owner==seedOwner test let every
// shadow through, which is how "Chat" kept turning up in the pills after its
// retirement while Builder (correctly excluded, invisibly) did not.
func TestScopePillsRetireSeedsByIdentity(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	// toolScopeState resolves agents via agentUserDB = UserDB(root.Bucket(
	// "orchestrate"), owner) — mirror that store here or the saved agents are
	// invisible to it.
	udb := agentUserDB(root, "u")
	prevRoot := RootDB
	RootDB = root
	t.Cleanup(func() { RootDB = prevRoot })

	// A user agent, and a seed-chat SHADOW: seed ID, user Owner — the shape a
	// customized seed takes, and the one the ownership test missed.
	if _, err := saveAgent(udb, AgentRecord{ID: "agent-real", Name: "Wren", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := saveAgent(udb, AgentRecord{ID: "seed-chat", Name: "Chat", Owner: "u", OrchestratorPrompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(udb, "u", TempTool{Name: "shared_tool", Description: "d", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}

	st, found := toolScopeState(root, "u", "shared_tool")
	if !found {
		t.Fatal("tool not found")
	}
	byID := map[string]bool{}
	for _, a := range st.Agents {
		byID[a.ID] = true
	}
	if !byID["agent-real"] {
		t.Error("a real user agent must be offered as a target")
	}
	if byID["seed-chat"] {
		t.Error("a retired seed's SHADOW must not be offered — identity, not ownership")
	}
	if byID["seed-builder"] {
		t.Error("Builder must never be a pill — it reads the whole pool by identity")
	}

	// The revocability exception: a seed that already HOLDS a grant stays
	// listed, so the grant can be turned off — and then it leaves.
	if !SetUserToolScopeAgents(udb, "u", "shared_tool", []string{"seed-chat"}) {
		t.Fatal("scope write failed")
	}
	st, _ = toolScopeState(root, "u", "shared_tool")
	held := false
	for _, a := range st.Agents {
		if a.ID == "seed-chat" && a.On {
			held = true
		}
	}
	if !held {
		t.Error("a seed holding an explicit grant must stay visible and ON, or the grant can never be revoked")
	}
}

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

// Turning a tool off for an agent that does not carry it is already done, not
// an error to show ("not bundled on agent Critic").
func TestTurningOffAnAgentThatLacksTheToolIsANoOp(t *testing.T) {
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	db := RootDB
	udb := agentUserDB(db, "alice")
	for _, id := range []string{"a1", "a2"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Name: "Agent " + id, Owner: "alice", OrchestratorPrompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := bundleAgentToolByID(udb, "alice", "a1", TempTool{Name: "make_event", Description: "d", CommandTemplate: "echo"}); err != nil {
		t.Fatal(err)
	}
	if err := setToolScope(db, "alice", "make_event", "a2", false); err != nil {
		t.Errorf("off for an agent without it: %v", err)
	}
	if p, ok := UserToolByName(db, "alice", "make_event"); !ok || !p.ScopedToAgent("a1") {
		t.Error("the no-op touched the agent that does carry it")
	}
}

// TestUnbundleAgentToolByIDStripsSeedOwnedAgent reproduces the "Case Analyzer
// keeps coming back" bug: a tool mis-scoped onto an APP agent (Owner=seedOwner,
// not the human) could never be removed, because the admin scope path used
// unbundleAgentTool — whose rec.Owner!=owner guard rejects seed/app/sub agents
// with "not your agent". That made the Access-selector unselect fail and made
// promoteScopedToGlobal's strip silently no-op, so the scoped copy survived and
// resurfaced. unbundleAgentToolByID (the admin-scope twin, guard-free) must
// strip it. The runtime-guarded unbundleAgentTool is asserted to still reject,
// so we don't accidentally drop the guard where it's intended.
func TestUnbundleAgentToolByIDStripsSeedOwnedAgent(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	const owner = "alice"
	udb := agentUserDB(root, owner)

	// An app-agent-shaped record: Owner is the framework (seedOwner), not the
	// human — exactly like Casefile's "Case Analyzer" resolves.
	rec, err := saveAgent(udb, AgentRecord{
		Name:               "Case Analyzer",
		OrchestratorPrompt: "p",
		Owner:              seedOwner,
	})
	if err != nil {
		t.Fatalf("save agent: %v", err)
	}
	// Flattened namespace: the mis-scoped tool is a store row scoped to the
	// seed-owned agent, not an embedded record copy.
	if err := bundleAgentToolByID(udb, owner, rec.ID, TempTool{Name: "ts3_list_clients"}); err != nil {
		t.Fatalf("seed scoped tool: %v", err)
	}

	// The guarded runtime path rejects it — documents why the bug existed.
	if err := unbundleAgentTool(udb, owner, rec.ID, "ts3_list_clients"); err == nil {
		t.Fatal("expected unbundleAgentTool to reject a seed-owned agent (the guard); it didn't")
	}

	// The admin-scope path must succeed and actually remove the tool.
	if err := unbundleAgentToolByID(udb, owner, rec.ID, "ts3_list_clients"); err != nil {
		t.Fatalf("unbundleAgentToolByID must strip the mis-scoped tool, got: %v", err)
	}
	if p, ok := UserToolByName(udb, owner, "ts3_list_clients"); ok && p.ScopedToAgent(rec.ID) {
		t.Fatal("tool still scoped after unbundleAgentToolByID — it would resurrect")
	}

	// Idempotent: a second strip reports not-bundled rather than succeeding
	// silently (so a caller can tell the difference).
	if err := unbundleAgentToolByID(udb, owner, rec.ID, "ts3_list_clients"); err == nil {
		t.Fatal("expected not-bundled error on second strip")
	}
}
