package orchestrate

import (
	"fmt"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestListSessionDraftsShadowing covers the enumeration behind Extensions >
// My tools. Session drafts are real, callable tools that live only for the life
// of a chat session and were previously visible ONLY from inside that session —
// so a user could lose work they never knew existed.
//
// Shadowed drafts (already covered by a committed tool of the same name) are
// MARKED, not dropped: the UI hides them so nobody is invited to "keep" a tool
// they already have, while cleanupSessionDraftsByName needs exactly those to
// find and delete. Dropping them here would silently disable that cleanup.
func TestListScopedToolsShadowing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot := RootDB
	RootDB = db // session + persistent tool stores resolve through RootDB
	t.Cleanup(func() { RootDB = savedRoot })

	app := &OrchestrateApp{}
	app.DB = db

	agent := AgentRecord{
		ID: "a1", Owner: "alice", Name: "Chat", OrchestratorPrompt: "x",
		Tools: []TempTool{{Name: "bundled_tool"}}, // shadows a draft of the same name
	}
	if _, err := saveAgent(UserDB(db, "alice"), agent); err != nil {
		t.Fatalf("save agent: %v", err)
	}
	udb := UserDB(db, "alice")
	sess := ChatSession{ID: "s1", AgentID: "a1", Title: "Tuesday"}
	if _, err := saveChatSession(udb, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}
	// One keepable draft, one shadowed by the agent's bundled tool, one shadowed
	// by the user-wide pool.
	for _, name := range []string{"draft_keeper", "bundled_tool", "pooled_tool"} {
		if err := SaveSessionTempTool(udb, "s1", TempTool{Name: name, Description: name}); err != nil {
			t.Fatalf("save draft %s: %v", name, err)
		}
	}
	if err := AdminPersistTempTool(udb, "alice", TempTool{Name: "pooled_tool"}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got := app.listScopedTools("alice")
	if len(got) != 4 { // 1 agent-bundled + 3 session drafts
		names := []string{}
		for _, d := range got {
			names = append(names, d.Tool.Name)
		}
		t.Fatalf("expected the agent's bundled tool + all 3 drafts (shadowing is a flag, not a filter), got %v", names)
	}
	byName := map[string]ScopedTool{}
	for _, d := range got {
		// bundled_tool appears twice — once as the agent's own tool, once as the
		// draft it shadows. Key the map on the SESSION copy for the shadow
		// assertions below, and check the agent copy separately.
		if d.Scope == ScopeSessionTool || byName[d.Tool.Name].Tool.Name == "" {
			byName[d.Tool.Name] = d
		}
	}
	// The agent's own bundled tool is reported as an agent-scoped row — the half
	// of this list that was previously admin-only.
	sawAgentRow := false
	for _, d := range got {
		if d.Scope == ScopeAgentTool && d.Tool.Name == "bundled_tool" && d.AgentID == "a1" {
			sawAgentRow = true
		}
	}
	if !sawAgentRow {
		t.Error("agent-bundled tools must be reported, not just session drafts")
	}
	if byName["bundled_tool"].Shadowed != true {
		t.Error("a draft shadowed by the agent's bundled tools must be marked Shadowed")
	}
	if byName["pooled_tool"].Shadowed != true {
		t.Error("a draft shadowed by the user-wide pool must be marked Shadowed")
	}
	d, ok := byName["draft_keeper"]
	if !ok || d.Shadowed {
		t.Fatalf("the unshadowed draft must be present and unmarked: %+v", d)
	}
	// The row has to carry enough context to act on: which session holds it (the
	// keep/discard actions key on it) and which agent it belongs to.
	if d.SessionID != "s1" || d.AgentID != "a1" {
		t.Errorf("missing action context: session=%q agent=%q", d.SessionID, d.AgentID)
	}
	if d.SessionTitle != "Tuesday" || d.AgentName != "Chat" {
		t.Errorf("missing labels: session=%q agent=%q", d.SessionTitle, d.AgentName)
	}
}

// TestListSessionDraftsScopedToUser: drafts live in one global table keyed by
// session id with no owner on the row, so the enumeration has to reach them via
// the user's own agents. A leak here would show one user another's tools.
func TestListScopedToolsScopedToUser(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = savedRoot })

	app := &OrchestrateApp{}
	app.DB = db

	for _, u := range []struct{ user, agent, sid, tool string }{
		{"alice", "a1", "s-alice", "alice_tool"},
		{"bob", "b1", "s-bob", "bob_tool"},
	} {
		udb := UserDB(db, u.user)
		if _, err := saveAgent(udb, AgentRecord{ID: u.agent, Owner: u.user, Name: u.agent, OrchestratorPrompt: "x"}); err != nil {
			t.Fatalf("save agent: %v", err)
		}
		if _, err := saveChatSession(udb, ChatSession{ID: u.sid, AgentID: u.agent}); err != nil {
			t.Fatalf("save session: %v", err)
		}
		if err := SaveSessionTempTool(udb, u.sid, TempTool{Name: u.tool}); err != nil {
			t.Fatalf("save draft: %v", err)
		}
	}

	got := app.listScopedTools("alice")
	if len(got) != 1 || got[0].Tool.Name != "alice_tool" {
		names := []string{}
		for _, d := range got {
			names = append(names, d.Tool.Name)
		}
		t.Fatalf("alice must see only her own draft, got %v", names)
	}
}

// TestListScopedToolsNoLister confirms the core seam degrades quietly: a
// deployment without the agents app renders nothing rather than erroring.
func TestListScopedToolsNoLister(t *testing.T) {
	RegisterScopedToolLister(nil)
	if got := ListScopedTools("alice"); got != nil {
		t.Errorf("expected nil with no lister, got %v", got)
	}
}

// TestPromoteSessionDraftTargets covers the session-vs-global choice, shared by
// the in-chat Tools modal and Extensions > My tools. Both must land a kept
// draft in the same place — a second, drifting implementation of the ownership
// check and the strip-redundant-agent-copy step is exactly what this shares.
func TestPromoteSessionDraftTargets(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	app := &OrchestrateApp{}
	app.DB = db
	udb := UserDB(db, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "alice", Name: "Chat", OrchestratorPrompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionTempTool(udb, "s1", TempTool{Name: "to_agent"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionTempTool(udb, "s1", TempTool{Name: "to_pool"}); err != nil {
		t.Fatal(err)
	}

	// Agent target: lands on the agent record, draft cleared.
	if scope, err := app.promoteSessionDraft(udb, "alice", "a1", "s1", "to_agent", ScopeTargetAgent); err != nil || scope != "agent" {
		t.Fatalf("agent promote: scope=%q err=%v", scope, err)
	}
	rec, _ := loadAgent(udb, "a1")
	if len(rec.Tools) != 1 || rec.Tools[0].Name != "to_agent" {
		t.Fatalf("tool not attached to the agent: %+v", rec.Tools)
	}

	// Global target: lands in the user-wide pool, draft cleared.
	if scope, err := app.promoteSessionDraft(udb, "alice", "a1", "s1", "to_pool", ScopeTargetGlobal); err != nil || scope != "global" {
		t.Fatalf("global promote: scope=%q err=%v", scope, err)
	}
	pooled := false
	for _, p := range LoadPersistentTempTools(udb, "alice") {
		if p.Tool.Name == "to_pool" {
			pooled = true
		}
	}
	if !pooled {
		t.Error("tool not persisted to the user-wide pool")
	}
	// Both drafts are gone — a kept draft left behind would show the tool twice.
	if got := LoadSessionTempTools(udb, "s1"); len(got) != 0 {
		t.Errorf("drafts should be cleared after promotion, got %v", got)
	}

	// An unknown draft is an error, not a silent no-op.
	if _, err := app.promoteSessionDraft(udb, "alice", "a1", "s1", "nope", ScopeTargetGlobal); err == nil {
		t.Error("promoting an unknown draft must error")
	}
}

// TestPromoteSessionDraftRefusesOtherOwnersAgent: attaching to an agent you
// don't own is refused — the check that keeps a shared deployment honest.
func TestPromoteSessionDraftRefusesOtherOwnersAgent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	app := &OrchestrateApp{}
	app.DB = db
	udb := UserDB(db, "alice")
	if _, err := saveAgent(udb, AgentRecord{ID: "a1", Owner: "bob", Name: "Bob's", OrchestratorPrompt: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionTempTool(udb, "s1", TempTool{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.promoteSessionDraft(udb, "alice", "a1", "s1", "x", ScopeTargetAgent); err == nil {
		t.Error("attaching to another owner's agent must be refused")
	}
}

// TestScopeTargetsIncludeHiddenAgents: Hidden means "keep this out of the
// fleet/dispatch picker", NOT "this agent may not have tools". Filtering the
// scope pills on Hidden made it impossible to give a hidden agent a tool, while
// still leaking VISIBLE app agents — wrong in both directions. Exclusions are by
// identity: app agents (app-declared kits) and clone-only seeds (templates).
func TestScopeTargetsIncludeHiddenAgents(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	// toolScopeState resolves agents through agentUserDB (the "orchestrate"
	// bucket), so the fixture has to write where it reads.
	udb := agentUserDB(db, "alice")
	mk := func(id string, hidden bool, ownedBy string) {
		if _, err := saveAgent(udb, AgentRecord{
			ID: id, Owner: "alice", Name: id, OrchestratorPrompt: "x",
			Hidden: hidden, OwnedBy: ownedBy,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("visible", false, "")
	mk("hidden_util", true, "") // a user's own hidden agent — must be offered
	mk("sub", false, "visible") // sub-agents are managed via their parent
	if err := AdminPersistTempTool(db, "alice", TempTool{Name: "get_weather"}); err != nil {
		t.Fatal(err)
	}

	st, ok := toolScopeState(db, "alice", "get_weather")
	if !ok {
		t.Fatal("expected scope state for a pooled tool")
	}
	seen := map[string]bool{}
	for _, a := range st.Agents {
		seen[a.ID] = true
	}
	if !seen["hidden_util"] {
		t.Error("a user's own HIDDEN agent must still be a tool-scope target")
	}
	if !seen["visible"] {
		t.Error("a visible agent must be a tool-scope target")
	}
	if seen["sub"] {
		t.Error("sub-agents are managed via their parent, not scoped directly")
	}
}

// TestAuthoredToolLandsOnFocusedAgent is the Builder case: an authoring turn
// builds FOR another agent, so what it writes must land on the agent in
// authoring focus, not on the agent running the turn. Getting this backwards
// piles every tool Builder ever wrote onto Builder itself.
func TestAuthoredToolLandsOnFocusedAgent(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	app := &OrchestrateApp{}
	app.DB = db
	udb := agentUserDB(db, "alice")
	for _, id := range []string{"seed-builder", "target"} {
		if _, err := saveAgent(udb, AgentRecord{ID: id, Owner: "alice", Name: id, OrchestratorPrompt: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	prevAttach := AttachToolToAgent
	AttachToolToAgent = func(d Database, owner, agentID string, tt TempTool) error {
		return bundleAgentToolByID(agentUserDB(d, owner), owner, agentID, tt)
	}
	t.Cleanup(func() { AttachToolToAgent = prevAttach })

	// Builder is running the turn; "target" is in authoring focus.
	sess := &ToolSession{
		Username: "alice", DB: db, AgentID: "seed-builder",
		AuthoringAgentFn: func() string { return "target" },
	}
	tool := &TempTool{Name: "probe_thing", Description: "d"}
	// Mirrors temptool.persistUnapprovedTool's targeting rule (focus wins over
	// the running agent) without reaching into that package's unexported half.
	target := sess.AgentID
	if sess.AuthoringAgentFn != nil {
		if f := sess.AuthoringAgentFn(); f != "" {
			target = f
		}
	}
	trial := *tool
	trial.Trial = true
	if err := AttachToolToAgent(sess.DB, sess.Username, target, trial); err != nil {
		t.Fatalf("attach: %v", err)
	}

	tgt, _ := loadAgent(udb, "target")
	if len(tgt.Tools) != 1 || tgt.Tools[0].Name != "probe_thing" {
		t.Fatalf("tool should land on the FOCUSED agent, target has %+v", tgt.Tools)
	}
	if !tgt.Tools[0].Trial {
		t.Error("an authored, unapproved tool must be marked Trial")
	}
	if b, _ := loadAgent(udb, "seed-builder"); len(b.Tools) != 0 {
		t.Errorf("nothing should land on the authoring agent itself, got %+v", b.Tools)
	}

	// Confirming clears the mark without moving the tool.
	prevConfirm := ConfirmAgentTool
	ConfirmAgentTool = func(d Database, owner, agentID, toolName string) error {
		u := agentUserDB(d, owner)
		rec, ok := loadAgent(u, agentID)
		if !ok {
			return fmt.Errorf("no agent")
		}
		for i := range rec.Tools {
			if rec.Tools[i].Name == toolName {
				rec.Tools[i].Trial = false
				_, err := saveAgent(u, rec)
				return err
			}
		}
		return fmt.Errorf("no tool")
	}
	t.Cleanup(func() { ConfirmAgentTool = prevConfirm })
	if err := ConfirmAgentTool(db, "alice", "target", "probe_thing"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	tgt2, _ := loadAgent(udb, "target")
	if tgt2.Tools[0].Trial {
		t.Error("confirm must clear the Trial mark")
	}
	if len(tgt2.Tools) != 1 {
		t.Error("confirm must not move or duplicate the tool")
	}
}

// TestReapTrialTools: an unconfirmed authored tool expires; a confirmed one and
// an unstamped one never do. The reaper's failure mode must be "left something
// behind", never "deleted work someone wanted".
func TestReapTrialTools(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	app := &OrchestrateApp{}
	app.DB = db
	udb := agentUserDB(db, "alice")
	old := time.Now().Add(-30 * 24 * time.Hour)
	if _, err := saveAgent(udb, AgentRecord{
		ID: "a1", Owner: "alice", Name: "Chat", OrchestratorPrompt: "x",
		Tools: []TempTool{
			{Name: "expired", Trial: true, TrialSince: old},
			{Name: "fresh", Trial: true, TrialSince: time.Now()},
			{Name: "confirmed"},
			// Pre-dates the stamp: unconfirmed but ageless, so never reaped.
			{Name: "unstamped", Trial: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if n := app.reapTrialTools(db, "alice"); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
	rec, _ := loadAgent(udb, "a1")
	left := map[string]bool{}
	for _, tl := range rec.Tools {
		left[tl.Name] = true
	}
	if left["expired"] {
		t.Error("an expired unconfirmed tool must be reaped")
	}
	for _, keep := range []string{"fresh", "confirmed", "unstamped"} {
		if !left[keep] {
			t.Errorf("%q must survive the reaper", keep)
		}
	}

	// Idempotent: a second pass has nothing left to take.
	if n := app.reapTrialTools(db, "alice"); n != 0 {
		t.Errorf("second pass reaped %d, want 0", n)
	}
}

// TestReapTrialToolsDisabled: TTL 0 disables reaping entirely, so a deployment
// can opt out without the sweep silently running anyway.
func TestReapTrialToolsDisabled(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	prev := TrialToolTTL
	TrialToolTTL = 0
	t.Cleanup(func() { TrialToolTTL = prev })

	app := &OrchestrateApp{}
	app.DB = db
	udb := agentUserDB(db, "alice")
	if _, err := saveAgent(udb, AgentRecord{
		ID: "a1", Owner: "alice", Name: "Chat", OrchestratorPrompt: "x",
		Tools: []TempTool{{Name: "ancient", Trial: true, TrialSince: time.Now().Add(-365 * 24 * time.Hour)}},
	}); err != nil {
		t.Fatal(err)
	}
	if n := app.reapTrialTools(db, "alice"); n != 0 {
		t.Fatalf("reaped %d with TTL disabled, want 0", n)
	}
}
