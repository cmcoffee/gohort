package core

import (
	"github.com/cmcoffee/snugforge/kvlite"
	"testing"
	"time"
)

// TestGlobalToolAdoption covers the opt-in adoption store: adopt adds, adopt
// again is idempotent, unadopt removes, isolation is per-user, and Merge unions
// (the migration's grandfather path) without clobbering existing adoptions.
func TestGlobalToolAdoption(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	// Empty to start.
	if got := LoadAdoptedGlobalTools(db, "alice"); len(got) != 0 {
		t.Fatalf("fresh user must have no adoptions; got %v", got)
	}

	// Adopt is idempotent; unadopt removes.
	if err := SetGlobalToolAdopted(db, "alice", "weather", true); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "alice", "weather", true); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "alice", "jira", true); err != nil {
		t.Fatal(err)
	}
	a := LoadAdoptedGlobalTools(db, "alice")
	if !a["weather"] || !a["jira"] || len(a) != 2 {
		t.Fatalf("alice should have adopted weather+jira once each; got %v", a)
	}
	if err := SetGlobalToolAdopted(db, "alice", "weather", false); err != nil {
		t.Fatal(err)
	}
	a = LoadAdoptedGlobalTools(db, "alice")
	if a["weather"] || !a["jira"] {
		t.Fatalf("unadopt should drop only weather; got %v", a)
	}

	// Per-user isolation: bob is unaffected by alice.
	if b := LoadAdoptedGlobalTools(db, "bob"); len(b) != 0 {
		t.Fatalf("bob must not see alice's adoptions; got %v", b)
	}

	// Merge unions the migration's grandfathered names without dropping jira.
	MergeAdoptedGlobalTools(db, "alice", []string{"weather", "pager"})
	a = LoadAdoptedGlobalTools(db, "alice")
	if !a["jira"] || !a["weather"] || !a["pager"] || len(a) != 3 {
		t.Fatalf("merge must union with existing; got %v", a)
	}
}

// TestGlobalToolAdoptACL covers the phase-5 adopt ACL: AllowedUsers on a Shared
// tool restricts who may see/adopt it, the field survives the kvlite/gob round
// trip, CanAdoptGlobalTool enforces the rule (open=all, restricted=members,
// unpublished=harmless, anon=never), and SetGlobalToolAdopted refuses an
// ACL-denied adopt while always permitting un-adopt.
func TestGlobalToolAdoptACL(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	// alice publishes two shared tools: "payroll" restricted to herself, and
	// "weather" open to everyone (empty AllowedUsers).
	db.Set(persistentTempToolsTable, "alice", []PersistentTempTool{
		{Tool: TempTool{Name: "payroll"}, Shared: true, AllowedUsers: []string{"alice"}},
		{Tool: TempTool{Name: "weather"}, Shared: true},
	})

	// AllowedUsers survives the kvlite/gob round-trip.
	var got []string
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if p.Tool.Name == "payroll" {
			got = p.AllowedUsers
		}
	}
	if len(got) != 1 || got[0] != "alice" {
		t.Fatalf("AllowedUsers must persist through the pool round-trip; got %v", got)
	}

	// ACL predicate.
	if !CanAdoptGlobalTool(db, "alice", "payroll") {
		t.Fatal("alice is in payroll's ACL; must be permitted")
	}
	if CanAdoptGlobalTool(db, "bob", "payroll") {
		t.Fatal("bob is not in payroll's ACL; must be denied")
	}
	if !CanAdoptGlobalTool(db, "bob", "weather") {
		t.Fatal("weather is open (empty ACL); bob must be permitted")
	}
	if !CanAdoptGlobalTool(db, "carol", "unpublished") {
		t.Fatal("an unpublished name is harmless; must be permitted (existence != permission)")
	}
	if CanAdoptGlobalTool(db, "", "weather") {
		t.Fatal("anonymous must never be permitted")
	}

	// Adopt guard refuses an ACL-denied adopt but allows a permitted one.
	if err := SetGlobalToolAdopted(db, "bob", "payroll", true); err == nil {
		t.Fatal("SetGlobalToolAdopted must refuse an ACL-denied adopt")
	}
	if LoadAdoptedGlobalTools(db, "bob")["payroll"] {
		t.Fatal("a refused adopt must not persist")
	}
	if err := SetGlobalToolAdopted(db, "alice", "payroll", true); err != nil {
		t.Fatalf("alice is permitted; adopt should succeed: %v", err)
	}
	// Un-adopt is ALWAYS allowed, even for a user who could never adopt — a
	// tightened ACL must never strand an un-removable tool.
	if err := SetGlobalToolAdopted(db, "bob", "payroll", false); err != nil {
		t.Fatalf("un-adopt must always be allowed: %v", err)
	}
}

// TestSharingResolvesPromotionRequest pins the fix for a stale "Publish requested"
// badge: sharing a tool — by ANY path — fulfills a pending publish request, so the
// request queue and the owner's badge don't linger on an already-shared tool.
func TestSharingResolvesPromotionRequest(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	db.Set(persistentTempToolsTable, "alice", []PersistentTempTool{{Tool: TempTool{Name: "weather"}}})
	if err := CreatePromotionRequest(db, "alice", "tool", "weather", "please"); err != nil {
		t.Fatal(err)
	}
	if !PendingPromotion(db, "alice", "tool", "weather") {
		t.Fatal("precondition: request should be pending")
	}
	if err := SetPersistentTempToolShared(db, "alice", "weather", true); err != nil {
		t.Fatal(err)
	}
	if PendingPromotion(db, "alice", "tool", "weather") {
		t.Fatal("sharing the tool must fulfill (clear) the pending publish request")
	}
}

// TestSetPersistentTempToolAllowedUsers covers the admin adopt-ACL setter: it
// normalizes (trim/dedupe/sort), updates CanAdoptGlobalTool, errors on an unknown
// tool, and an empty list re-opens the tool to everyone.
func TestSetPersistentTempToolAllowedUsers(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	db.Set(persistentTempToolsTable, "alice", []PersistentTempTool{
		{Tool: TempTool{Name: "payroll"}, Shared: true},
	})

	// Unknown tool errors.
	if err := SetPersistentTempToolAllowedUsers(db, "alice", "nope", []string{"bob"}); err == nil {
		t.Fatal("setting ACL on an unknown tool must error")
	}

	// Set + normalize: duplicates, blanks, and whitespace collapse to a sorted set.
	if err := SetPersistentTempToolAllowedUsers(db, "alice", "payroll", []string{" carol ", "bob", "bob", ""}); err != nil {
		t.Fatal(err)
	}
	got, found := SharedToolAllowedUsers(db, "payroll")
	if !found || len(got) != 2 || got[0] != "bob" || got[1] != "carol" {
		t.Fatalf("ACL must be trimmed/deduped/sorted [bob carol]; got %v", got)
	}
	if !CanAdoptGlobalTool(db, "bob", "payroll") || CanAdoptGlobalTool(db, "dave", "payroll") {
		t.Fatal("ACL must admit bob and deny dave")
	}

	// Empty list re-opens the tool to everyone.
	if err := SetPersistentTempToolAllowedUsers(db, "alice", "payroll", nil); err != nil {
		t.Fatal(err)
	}
	if !CanAdoptGlobalTool(db, "dave", "payroll") {
		t.Fatal("an emptied ACL must re-open the tool to all users")
	}
}

// A tool name may live in exactly one home. AdminPersistTempTool has always
// enforced that between the pending queue and the active pool, and the orphan
// pool was simply left out of the rule — so deleting the last agent that
// carried a tool, then re-creating the tool under the same name, produced two
// rows the admin UI rendered side by side, and left the stale copy
// re-homeable on top of the live one.
func TestPersistingANameClearsItsOrphan(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const user, name = "craig@example.com", "get_top_stories"

	// The agent carrying the tool was deleted, so the definition went here.
	AddOrphanedTempTools(db, user, []OrphanedTempTool{{
		Tool:            TempTool{Name: name, Description: "news (old copy)"},
		FormerAgentName: "Wren",
		OrphanedAt:      time.Now(),
	}})

	// The user re-creates it. That IS the resolution of the orphan.
	if err := AdminPersistTempTool(db, user, TempTool{Name: name, Description: "news"}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	if got := LoadOrphanedTempTools(db, user); len(got) != 0 {
		t.Errorf("the orphan survived alongside the live tool: %+v", got)
	}
	live, ok := UserToolByName(db, user, name)
	if !ok {
		t.Fatal("the live tool is missing")
	}
	if live.Tool.Description != "news" {
		t.Errorf("the stale copy won: %q", live.Tool.Description)
	}
}

// The clear is inlined under the already-held tempToolPersistMu, which is not
// reentrant — the naive call to the exported RemoveOrphanedTempTool would
// hang forever rather than fail.
func TestPersistWithAnOrphanDoesNotDeadlock(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const user, name = "craig@example.com", "get_top_stories"
	AddOrphanedTempTools(db, user, []OrphanedTempTool{{Tool: TempTool{Name: name}}})

	done := make(chan error, 1)
	go func() { done <- AdminPersistTempTool(db, user, TempTool{Name: name}) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("persist returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AdminPersistTempTool deadlocked clearing the orphan under its own lock")
	}
}

// Unrelated orphans are not collateral damage.
func TestPersistLeavesOtherOrphansAlone(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const user = "craig@example.com"
	AddOrphanedTempTools(db, user, []OrphanedTempTool{
		{Tool: TempTool{Name: "get_top_stories"}},
		{Tool: TempTool{Name: "check_surf_report"}},
	})

	if err := AdminPersistTempTool(db, user, TempTool{Name: "get_top_stories"}); err != nil {
		t.Fatalf("persist: %v", err)
	}

	got := LoadOrphanedTempTools(db, user)
	if len(got) != 1 || got[0].Tool.Name != "check_surf_report" {
		t.Errorf("orphan pool = %+v, want only check_surf_report", got)
	}
}

// ApprovePendingTempTool held the process-global tempToolPersistMu and called
// RemoveSessionTempTool, which locks the SAME non-reentrant mutex. That
// goroutine blocked forever while holding the lock, so every later
// tool-persist operation in the process queued behind it — requests kept
// arriving and doing their work, but could never publish.
//
// The call site asserted "RemoveSessionTempTool takes no lock of its own so
// this is safe". True when written; the lock was added later.
//
// Run under a timeout: a deadlock does not fail, it hangs, so the assertion
// has to be "this finished at all."
func TestApprovePendingDoesNotDeadlock(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const user, name, session = "craig@example.com", "get_top_stories", "sess-1"

	// A pending tool that came from a session draft — the combination that
	// drives approval into the session-cleanup branch.
	QueuePendingTempTool(db, user, TempTool{Name: name, Description: "news"}, session)
	SaveSessionTempTool(db, session, TempTool{Name: name, Description: "news"})

	done := make(chan error, 1)
	go func() { done <- ApprovePendingTempTool(db, user, name) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("approve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ApprovePendingTempTool deadlocked — it holds tempToolPersistMu and re-locks it")
	}

	// The approval must still have done its job.
	found := false
	for _, p := range LoadPersistentTempTools(db, user) {
		if p.Tool.Name == name {
			found = true
		}
	}
	if !found {
		t.Error("approved tool never reached the persistent pool")
	}
	for _, tt := range LoadSessionTempTools(db, session) {
		if tt.Name == name {
			t.Error("the session draft survived approval — cleanup did not run")
		}
	}
}

// The mutex stays usable afterward. A deadlock leaves it held forever, so a
// later unrelated write would hang too — this is what turned one stuck
// tool_def into a process-wide stall.
func TestToolPersistLockReleasedAfterApprove(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	QueuePendingTempTool(db, "u", TempTool{Name: "t1"}, "s1")
	SaveSessionTempTool(db, "s1", TempTool{Name: "t1"})
	if err := ApprovePendingTempTool(db, "u", "t1"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	done := make(chan bool, 1)
	go func() {
		SaveSessionTempTool(db, "s2", TempTool{Name: "later"})
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the global tool mutex was never released — every later persist blocks")
	}
}

// TouchPersistentTempTool runs after EVERY custom tool execution, so it is the
// widest blast radius for any stall on the shared tool mutex. During the
// ApprovePendingTempTool deadlock this is precisely where unrelated turns
// froze: the tool had already run, and the turn died on a telemetry write.
//
// It is best-effort by contract, so it must never wait for the lock.
func TestTouchNeverBlocksOnAHeldLock(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	db.Set(persistentTempToolsTable, "u", []PersistentTempTool{{Tool: TempTool{Name: "t"}}})

	// Simulate another goroutine holding the mutex indefinitely.
	tempToolPersistMu.Lock()
	done := make(chan bool, 1)
	go func() {
		TouchPersistentTempTool(db, "u", "t")
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		tempToolPersistMu.Unlock()
		t.Fatal("Touch waited on a held lock — one stall would freeze every custom-tool turn")
	}
	tempToolPersistMu.Unlock()

	// And it still works normally when the lock is free.
	TouchPersistentTempTool(db, "u", "t")
	for _, p := range LoadPersistentTempTools(db, "u") {
		if p.Tool.Name == "t" && p.LastUsedAt.IsZero() {
			t.Error("Touch did not record LastUsedAt when uncontended")
		}
	}
}

// The tool_def update case, exactly as it hung in production:
// AdminPersistTempTool holds tempToolPersistMu and calls
// cleanupSessionDraftsByNameLocked, which removes stale session drafts. That
// helper does not lock itself — it called the LOCKING RemoveSessionTempTool,
// one level deeper than a direct call, which is why a one-level scan missed it.
//
// It only fires when a session draft with that name actually exists, which is
// precisely the tool_def update path (authoring leaves the draft behind).
func TestAdminPersistWithSessionDraftDoesNotDeadlock(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	const user, name, session = "craig@example.com", "get_top_stories", "sess-A"
	user2 := func() string { return user }

	// A session draft of the SAME name is what drives the cleanup branch.
	SaveSessionTempTool(db, session, TempTool{Name: name, Description: "old"})
	// cleanupSessionDraftsByNameLocked finds drafts through the scoped-tool
	// lister, so without one registered the loop body never runs and this test
	// would pass against the deadlocking code. Register one.
	RegisterScopedToolLister(func(user string) []ScopedTool {
		if user != user2() {
			return nil
		}
		return []ScopedTool{{Tool: TempTool{Name: name}, Scope: ScopeSessionTool, SessionID: session}}
	})
	t.Cleanup(func() { RegisterScopedToolLister(nil) })

	done := make(chan error, 1)
	go func() {
		done <- AdminPersistTempTool(db, user, TempTool{Name: name, Description: "new"})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("persist returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AdminPersistTempTool deadlocked cleaning up a session draft — this is the tool_def update hang")
	}

	found := false
	for _, p := range LoadPersistentTempTools(db, user) {
		if p.Tool.Name == name && p.Tool.Description == "new" {
			found = true
		}
	}
	if !found {
		t.Error("the updated tool never reached the persistent pool")
	}
}
