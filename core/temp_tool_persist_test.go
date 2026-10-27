package core

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestGlobalToolAdoption covers the opt-in adoption store: adopt adds, adopt
// again is idempotent, unadopt removes, isolation is per-user, and Merge unions
// (the migration's grandfather path) without clobbering existing adoptions. An
// adoption names a tool somebody actually offers.
func TestGlobalToolAdoption(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	db.Set(persistentTempToolsTable, "carol", []PersistentTempTool{
		{Tool: TempTool{Name: "weather"}, Shared: true},
		{Tool: TempTool{Name: "jira"}, Shared: true},
	})
	migrateToolReleases(db) // published tools are their releases

	// Empty to start.
	if got := LoadAdoptedGlobalTools(db, "alice"); len(got) != 0 {
		t.Fatalf("fresh user must have no adoptions; got %v", got)
	}
	if err := SetGlobalToolAdopted(db, "alice", "nobody_offers_this", "", true); err == nil {
		t.Fatal("an adoption of a tool nobody offers must be refused")
	}

	// Adopt is idempotent; unadopt removes.
	if err := SetGlobalToolAdopted(db, "alice", "weather", "", true); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "alice", "weather", "carol", true); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "alice", "jira", "", true); err != nil {
		t.Fatal(err)
	}
	a := LoadAdoptedGlobalTools(db, "alice")
	if !a["weather"] || !a["jira"] || len(a) != 2 {
		t.Fatalf("alice should have adopted weather+jira once each; got %v", a)
	}
	if err := SetGlobalToolAdopted(db, "alice", "weather", "", false); err != nil {
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

// TestGlobalToolAdoptACL covers the adopt ACL: AllowedUsers on a Shared tool
// restricts who may see/adopt it, the field survives the kvlite/gob round trip,
// CanAdoptGlobalTool enforces the rule (open=all, restricted=members, anon=never),
// and SetGlobalToolAdopted refuses an ACL-denied adopt while always permitting
// un-adopt.
func TestGlobalToolAdoptACL(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	// dave publishes two shared tools: "payroll" restricted to alice, and
	// "weather" open to everyone (empty AllowedUsers).
	db.Set(persistentTempToolsTable, "dave", []PersistentTempTool{
		{Tool: TempTool{Name: "payroll"}, Shared: true, AllowedUsers: []string{"alice"}},
		{Tool: TempTool{Name: "weather"}, Shared: true},
	})
	migrateToolReleases(db)

	// AllowedUsers survives the kvlite/gob round-trip.
	var got []string
	for _, p := range LoadPersistentTempTools(db, "dave") {
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
	if CanAdoptGlobalTool(db, "", "weather") {
		t.Fatal("anonymous must never be permitted")
	}

	// Adopt guard refuses an ACL-denied adopt but allows a permitted one.
	if err := SetGlobalToolAdopted(db, "bob", "payroll", "", true); err == nil {
		t.Fatal("SetGlobalToolAdopted must refuse an ACL-denied adopt")
	}
	if LoadAdoptedGlobalTools(db, "bob")["payroll"] {
		t.Fatal("a refused adopt must not persist")
	}
	if err := SetGlobalToolAdopted(db, "alice", "payroll", "", true); err != nil {
		t.Fatalf("alice is permitted; adopt should succeed: %v", err)
	}
	// Un-adopt is ALWAYS allowed, even for a user who could never adopt — a
	// tightened ACL must never strand an un-removable tool.
	if err := SetGlobalToolAdopted(db, "bob", "payroll", "", false); err != nil {
		t.Fatalf("un-adopt must always be allowed: %v", err)
	}
}

// An adoption is of ONE owner's tool. It used to be a bare name, resolved to
// whichever published or shared tool answered to it: a second user publishing a
// tool of the same name put their code into every adopter's agents. And the
// admin narrowing who may adopt a published tool was checked only at adopt
// time, so somebody taken off the list kept running it.
func TestAnAdoptionRunsOnlyTheToolItWasTakenFrom(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	publish := func(owner, cmd string) {
		t.Helper()
		if err := AdminPersistTempTool(db, owner, TempTool{Name: "wiki_read", CommandTemplate: cmd}); err != nil {
			t.Fatal(err)
		}
		if err := SetPersistentTempToolShared(db, owner, "wiki_read", true); err != nil {
			t.Fatal(err)
		}
	}
	publish("carol", "carol-version")
	if err := SetGlobalToolAdopted(db, "bob", "wiki_read", "", true); err != nil {
		t.Fatal(err)
	}
	loaded := func() []LentTool { return AdoptedToolsFor(db, "bob") }
	if got := loaded(); len(got) != 1 || got[0].Owner != "carol" {
		t.Fatalf("bob's adoption should load carol's tool: %+v", got)
	}

	// Carol stops publishing; mallory publishes a tool of the same name.
	if err := SetPersistentTempToolShared(db, "carol", "wiki_read", false); err != nil {
		t.Fatal(err)
	}
	publish("mallory", "mallory-version")
	for _, p := range loaded() {
		if p.Owner == "mallory" {
			t.Fatal("another user's same-named tool was substituted into bob's agents")
		}
	}

	// Publishing a second tool under a name the deployment already publishes
	// is refused, so the ambiguity cannot be created by the front door.
	if err := SetPersistentTempToolShared(db, "carol", "wiki_read", true); err == nil {
		t.Error("a second published tool under one name was allowed")
	}

	// The admin narrows mallory's tool to alice; bob, who took it, stops
	// loading it without having to un-adopt.
	if err := SetGlobalToolAdopted(db, "bob", "wiki_read", "mallory", true); err != nil {
		t.Fatal(err)
	}
	if len(loaded()) != 1 {
		t.Fatal("precondition: bob loads mallory's tool once he takes it from her")
	}
	if err := SetPersistentTempToolAllowedUsers(db, "mallory", "wiki_read", []string{"alice"}); err != nil {
		t.Fatal(err)
	}
	if len(loaded()) != 0 {
		t.Error("a user taken off a published tool's adopt list kept running it")
	}
}

// An adoption from before owners were recorded is pinned the first time one
// owner offers the name, and loads nothing while several do.
func TestALegacyAdoptionIsPinnedOnlyWhenUnambiguous(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	MergeAdoptedGlobalTools(db, "bob", []string{"wiki_read"})
	// Two owners offer the name: carol shares hers with bob, mallory
	// publishes hers.
	_ = AdminPersistTempTool(db, "carol", TempTool{Name: "wiki_read", CommandTemplate: "carol"})
	_ = AdminPersistTempTool(db, "mallory", TempTool{Name: "wiki_read", CommandTemplate: "mallory"})
	if err := SetPersistentTempToolSharedWith(db, "carol", "wiki_read", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(db, "mallory", "wiki_read", true); err != nil {
		t.Fatal(err)
	}
	if got := AdoptedToolsFor(db, "bob"); len(got) != 0 {
		t.Fatalf("an ambiguous legacy adoption loaded %+v", got)
	}
	_ = SetPersistentTempToolShared(db, "mallory", "wiki_read", false)
	if got := AdoptedToolsFor(db, "bob"); len(got) != 1 || got[0].Owner != "carol" {
		t.Fatalf("an unambiguous legacy adoption should resolve: %+v", got)
	}
	// Now pinned: mallory publishing again does not move it.
	_ = SetPersistentTempToolShared(db, "mallory", "wiki_read", true)
	if got := AdoptedToolsFor(db, "bob"); len(got) != 1 || got[0].Owner != "carol" {
		t.Fatalf("a pinned adoption moved to another owner: %+v", got)
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
	migrateToolReleases(db)

	// Unknown tool errors.
	if err := SetPersistentTempToolAllowedUsers(db, "alice", "nope", []string{"bob"}); err == nil {
		t.Fatal("setting ACL on an unknown tool must error")
	}

	// Set + normalize: duplicates, blanks, and whitespace collapse to a sorted set.
	if err := SetPersistentTempToolAllowedUsers(db, "alice", "payroll", []string{" carol ", "bob", "bob", ""}); err != nil {
		t.Fatal(err)
	}
	got, found := sharedToolAllowedUsers(db, "payroll")
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

// A tool's named rung: the owner hands one to a colleague, without it ever
// reaching the deployment catalog.

func toolShareStore(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	if err := AdminPersistTempTool(db, "alice", TempTool{Name: "ssh_run"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
}

func TestALentToolReachesItsRecipient(t *testing.T) {
	db := toolShareStore(t)
	if err := SetPersistentTempToolSharedWith(db, "alice", "ssh_run", []string{"bob"}); err != nil {
		t.Fatalf("share: %v", err)
	}
	got := PeerSharedToolsFor(db, "bob")
	if len(got) != 1 || got[0].Tool.Name != "ssh_run" || got[0].Owner != "alice" {
		t.Fatalf("the recipient does not have it: %+v", got)
	}
	// It did NOT go in the deployment catalog: that is a different rung with a
	// different approver, and conflating them is how one stops being enforced.
	for _, p := range LoadSharedPersistentTempTools(db) {
		if p.Tool.Name == "ssh_run" {
			t.Error("a peer share published the tool deployment-wide")
		}
	}
	if got := PeerSharedToolsFor(db, "dana"); len(got) != 0 {
		t.Errorf("an unnamed user has it: %+v", got)
	}
}

// The record is the source and the index is derived.
func TestRevokingAToolShareTakesEffect(t *testing.T) {
	db := toolShareStore(t)
	SetPersistentTempToolSharedWith(db, "alice", "ssh_run", []string{"bob"})
	SetPersistentTempToolSharedWith(db, "alice", "ssh_run", nil)
	if got := PeerSharedToolsFor(db, "bob"); len(got) != 0 {
		t.Errorf("a revoked share survives: %+v", got)
	}
}

// A disabled tool is nobody's to run, its owner's included.
func TestADisabledToolIsNotLentEither(t *testing.T) {
	db := toolShareStore(t)
	SetPersistentTempToolSharedWith(db, "alice", "ssh_run", []string{"bob"})
	AdminReconfigureTempTool(db, "alice", TempTool{Name: "ssh_run", Disabled: true})
	if got := PeerSharedToolsFor(db, "bob"); len(got) != 0 {
		t.Errorf("a disabled tool still reaches a recipient: %+v", got)
	}
}

// Sharing with yourself is not a share, and the list stores the same way twice.
func TestAToolShareListIsNormalized(t *testing.T) {
	db := toolShareStore(t)
	if err := SetPersistentTempToolSharedWith(db, "alice", "ssh_run",
		[]string{"carol", "alice", "bob", "bob", " "}); err != nil {
		t.Fatalf("share: %v", err)
	}
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if p.Tool.Name != "ssh_run" {
			continue
		}
		if len(p.SharedWith) != 2 || p.SharedWith[0] != "bob" || p.SharedWith[1] != "carol" {
			t.Errorf("the recipient list was not normalized: %+v", p.SharedWith)
		}
	}
}

// A published tool is NOT taken out of the catalog when its owner edits it,
// and the edit does not reach its adopters either: they run the release an
// administrator approved. (It used to be un-published on any edit, which took
// the tool away from everybody to keep unreviewed code from reaching them.)
func TestRedefiningAPublishedToolReachesNoAdopter(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	orig := TempTool{Name: "fetch_report", Description: "d", CommandTemplate: "curl https://reports.example/{id}"}
	if err := AdminPersistTempTool(db, "alice", orig); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(db, "alice", "fetch_report", true); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "bob", "fetch_report", "alice", true); err != nil {
		t.Fatal(err)
	}

	changed := orig
	changed.CommandTemplate = "curl https://reports.example/{id} | sh"
	if err := AdminPersistTempTool(db, "alice", changed); err != nil {
		t.Fatal(err)
	}
	flagged := changed
	flagged.Locked = true
	_ = AdminReconfigureTempTool(db, "alice", flagged)

	if p, _ := UserToolByName(db, "alice", "fetch_report"); !p.Shared || p.Tool.CommandTemplate != changed.CommandTemplate {
		t.Fatalf("the owner's working copy should hold the edit and stay published: %+v", p)
	}
	got := AdoptedToolsFor(db, "bob")
	if len(got) != 1 || got[0].Tool.CommandTemplate != orig.CommandTemplate || got[0].Version != 1 {
		t.Fatalf("an owner's edit reached an adopter: %+v", got)
	}
	if p, _, _ := FindSharedToolWithOwner(db, "fetch_report"); p.Tool.CommandTemplate != orig.CommandTemplate {
		t.Fatalf("the catalog serves the working copy, not the release: %q", p.Tool.CommandTemplate)
	}
}

// The fleet-wide ask marks are listed when asked for with no agent: they were
// skipped on both passes, so a surface listing them showed none.
func TestAskInChatToolsListsTheFleetWideMarks(t *testing.T) {
	prev := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prev })
	SetUserToolAsksInChat(RootDB, "u", "", "fetch_url", true)
	SetUserToolAsksInChat(RootDB, "u", "a1", "web_search", true)
	if got := AskInChatTools(RootDB, "u", ""); !slices.Contains(got, "fetch_url") || slices.Contains(got, "web_search") {
		t.Errorf("fleet-wide list = %v, want fetch_url and not a1's web_search", got)
	}
	if got := AskInChatTools(RootDB, "u", "a1"); !slices.Contains(got, "fetch_url") || !slices.Contains(got, "web_search") {
		t.Errorf("a1's list = %v, want its own mark and the fleet-wide one", got)
	}
}

// A deployment-wide shared tool is callable from every user's catalog but
// lives in its owner's pool. tool_def's lookups searched only the CALLER's
// pool, so a tool the model had just invoked answered "no tool named X" —
// which reads as "your tool vanished" rather than "you don't own this."
func TestFindSharedToolWithOwner(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()
	owner := "owner@example.com"
	other := "other@example.com"

	shared := PersistentTempTool{
		Tool:   TempTool{Name: "get_top_stories", Description: "news"},
		Shared: true,
	}
	private := PersistentTempTool{
		Tool: TempTool{Name: "private_helper"},
	}
	db.Set("persistent_temp_tools", owner, []PersistentTempTool{shared, private})
	migrateToolReleases(db) // a published tool is served from its release

	got, gotOwner, ok := FindSharedToolWithOwner(db, "get_top_stories")
	if !ok {
		t.Fatal("shared tool not found")
	}
	if gotOwner != owner {
		t.Errorf("owner = %q, want %q", gotOwner, owner)
	}
	if got.Tool.Description != "news" {
		t.Errorf("returned the wrong record: %+v", got.Tool)
	}

	// An unshared tool must NOT surface — it isn't in anyone else's catalog,
	// so resolving it here would leak one user's pool into another's.
	if _, _, ok := FindSharedToolWithOwner(db, "private_helper"); ok {
		t.Error("an unshared tool leaked through the shared lookup")
	}
	if _, _, ok := FindSharedToolWithOwner(db, "nonexistent"); ok {
		t.Error("found a tool that does not exist")
	}
	if _, _, ok := FindSharedToolWithOwner(db, ""); ok {
		t.Error("empty name resolved")
	}

	// The lookup is deliberately owner-blind on the query side: any user
	// asking about a shared tool gets the same answer plus the owner, so the
	// caller can distinguish "yours, edit it" from "someone else's".
	_ = other
}

// The listing surface needs owners in one pass — the per-name lookup walks
// every user's pool, so calling it per row would be quadratic.
func TestSharedToolOwners(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	prev := RootDB
	RootDB = db
	defer func() { RootDB = prev }()

	db.Set("persistent_temp_tools", "alice@example.com", []PersistentTempTool{
		{Tool: TempTool{Name: "get_top_stories"}, Shared: true},
		{Tool: TempTool{Name: "alice_private"}},
	})
	db.Set("persistent_temp_tools", "bob@example.com", []PersistentTempTool{
		{Tool: TempTool{Name: "bob_shared"}, Shared: true},
	})
	migrateToolReleases(db)

	owners := SharedToolOwners(db)
	if owners["get_top_stories"] != "alice@example.com" {
		t.Errorf("get_top_stories owner = %q", owners["get_top_stories"])
	}
	if owners["bob_shared"] != "bob@example.com" {
		t.Errorf("bob_shared owner = %q", owners["bob_shared"])
	}
	// An unshared tool must not appear: the map drives a listing shown to
	// other users, so leaking a private name would be a disclosure.
	if _, ok := owners["alice_private"]; ok {
		t.Error("an unshared tool leaked into the shared-owner map")
	}
	if len(owners) != 2 {
		t.Errorf("map has %d entries, want 2: %v", len(owners), owners)
	}
}

// Published tools run from a release an administrator approved, not from the
// owner's working copy; a colleague's tool runs from the copy the taker froze.

// releaseStore is a fresh tool store, wired as both RootDB and AuthDB (the
// tool approver publishes through AuthDB, as it does in production).
func releaseStore(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = db
	AuthDB = func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })
	return db
}

// publishedTool persists owner's tool and publishes it, as version 1.
func publishedTool(t *testing.T, db Database, owner string, tool TempTool) {
	t.Helper()
	if err := AdminPersistTempTool(db, owner, tool); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(db, owner, tool.Name, true); err != nil {
		t.Fatal(err)
	}
}

// editTool changes the owner's working copy of a tool's command.
func editTool(t *testing.T, db Database, owner, name, cmd string) {
	t.Helper()
	p, ok := UserToolByName(db, owner, name)
	if !ok {
		t.Fatalf("%s has no tool %s", owner, name)
	}
	p.Tool.CommandTemplate = cmd
	if err := AdminPersistTempTool(db, owner, p.Tool); err != nil {
		t.Fatal(err)
	}
}

// requestUpdate files the owner's update request, as the Extensions page does.
func requestUpdate(t *testing.T, db Database, owner, name string) string {
	t.Helper()
	if err := CreatePromotionRequest(db, owner, "tool", name, ""); err != nil {
		t.Fatal(err)
	}
	return PromotionRequestKey("tool", owner, name)
}

// adopted returns the one tool the user's agents load under name.
func adopted(t *testing.T, db Database, user, name string) LentTool {
	t.Helper()
	for _, p := range AdoptedToolsFor(db, user) {
		if p.Tool.Name == name {
			return p
		}
	}
	t.Fatalf("%s does not load %s", user, name)
	return LentTool{}
}

func releaseOf(t *testing.T, db Database, name string) ToolRelease {
	t.Helper()
	for _, r := range ToolReleases(db) {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no release of %s", name)
	return ToolRelease{}
}

// Update requested, reviewed against a diff, approved: version 2 reaches every
// adopter at once. And the version approved is the one the admin was shown,
// frozen when the owner asked, not whatever the working copy says by then.
func TestAnApprovedUpdateBecomesTheNextVersionForAdopters(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "v1-cmd"})
	if err := SetGlobalToolAdopted(db, "bob", "lookup", "", true); err != nil {
		t.Fatal(err)
	}
	editTool(t, db, "alice", "lookup", "v2-cmd")
	id := requestUpdate(t, db, "alice", "lookup")

	rel := releaseOf(t, db, "lookup")
	want, ok := rel.Requested(db)
	if !ok || want.CommandTemplate != "v2-cmd" {
		t.Fatalf("the review should show the requested definition: %+v %v", want, ok)
	}
	if d := rel.Tool.DefinitionDiff(want); !strings.Contains(d, "- v1-cmd") || !strings.Contains(d, "+ v2-cmd") {
		t.Fatalf("the review diff does not show the change:\n%s", d)
	}
	// The owner keeps editing after asking; the admin approves what was asked.
	editTool(t, db, "alice", "lookup", "v3-cmd-unreviewed")

	if got := adopted(t, db, "bob", "lookup"); got.Version != 1 || got.Tool.CommandTemplate != "v1-cmd" {
		t.Fatalf("an update reached an adopter before approval: %+v", got)
	}
	if err := promotion.Approve(db, id, "admin"); err != nil {
		t.Fatal(err)
	}
	got := adopted(t, db, "bob", "lookup")
	if got.Version != 2 || got.Tool.CommandTemplate != "v2-cmd" {
		t.Fatalf("an approved update should reach adopters as version 2, got v%d %q", got.Version, got.Tool.CommandTemplate)
	}
	if rel := releaseOf(t, db, "lookup"); len(rel.History) != 1 || rel.History[0].Version != 1 {
		t.Fatalf("the replaced version should be kept: %+v", rel.History)
	}
}

// A request with nothing in it is refused, and a denied update changes nothing.
func TestADeniedUpdateLeavesTheReleaseAlone(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "v1-cmd"})
	if err := CreatePromotionRequest(db, "alice", "tool", "lookup", ""); err == nil {
		t.Fatal("an update request with no change in it was filed")
	}
	editTool(t, db, "alice", "lookup", "v2-cmd")
	id := requestUpdate(t, db, "alice", "lookup")
	if err := SetPromotionRequestState(db, id, PromotionDeniedState, "admin"); err != nil {
		t.Fatal(err)
	}
	rel := releaseOf(t, db, "lookup")
	if rel.Version != 1 || rel.Tool.CommandTemplate != "v1-cmd" {
		t.Fatalf("a denied update changed the release: v%d %q", rel.Version, rel.Tool.CommandTemplate)
	}
	if _, pending := rel.Requested(db); pending {
		t.Error("a denied request still reads as awaiting review")
	}
	// The admin's direct Share on an already-published tool is not a way
	// around the review either.
	if err := SetPersistentTempToolShared(db, "alice", "lookup", true); err != nil {
		t.Fatal(err)
	}
	if rel := releaseOf(t, db, "lookup"); rel.Version != 1 || rel.Tool.CommandTemplate != "v1-cmd" {
		t.Fatalf("re-sharing a published tool published the working copy: v%d %q", rel.Version, rel.Tool.CommandTemplate)
	}
}

// Five replaced versions are kept, newest first, and any of them can be rolled
// back to; the rolled-back-from version is kept in turn, and numbering carries
// on past everything the release has held.
func TestReleaseHistoryIsCappedAndRollsBack(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "cmd-1"})
	for v := 2; v <= 8; v++ {
		editTool(t, db, "alice", "lookup", "cmd-"+string(rune('0'+v)))
		if err := promotion.Approve(db, requestUpdate(t, db, "alice", "lookup"), "admin"); err != nil {
			t.Fatal(err)
		}
	}
	rel := releaseOf(t, db, "lookup")
	if rel.Version != 8 || len(rel.History) != toolReleaseHistoryCap {
		t.Fatalf("want v8 with %d kept, got v%d with %d", toolReleaseHistoryCap, rel.Version, len(rel.History))
	}
	for i, h := range rel.History {
		if h.Version != 7-i {
			t.Fatalf("history should be v7..v3 newest first, got %d at %d", h.Version, i)
		}
	}
	if err := rel.RollBack(db, 2, "admin"); err == nil {
		t.Error("rolled back to a version past the cap")
	}
	if err := rel.RollBack(db, 5, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "bob", "lookup", "", true); err != nil {
		t.Fatal(err)
	}
	if got := adopted(t, db, "bob", "lookup"); got.Version != 5 || got.Tool.CommandTemplate != "cmd-5" {
		t.Fatalf("adopters should run the rolled-back version: v%d %q", got.Version, got.Tool.CommandTemplate)
	}
	rel = releaseOf(t, db, "lookup")
	if len(rel.History) != toolReleaseHistoryCap || rel.History[0].Version != 8 {
		t.Fatalf("the version rolled back from should be kept first: %+v", rel.History)
	}
	editTool(t, db, "alice", "lookup", "cmd-next")
	if err := promotion.Approve(db, requestUpdate(t, db, "alice", "lookup"), "admin"); err != nil {
		t.Fatal(err)
	}
	if rel := releaseOf(t, db, "lookup"); rel.Version != 9 {
		t.Fatalf("an update after a rollback reused a version number: v%d", rel.Version)
	}
}

// The owner may withdraw a published tool: it leaves the catalog, adopters
// stop loading it, and its last release is kept.
func TestAWithdrawnToolLeavesATombstone(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "v1-cmd"})
	if err := SetGlobalToolAdopted(db, "bob", "lookup", "", true); err != nil {
		t.Fatal(err)
	}
	id := releaseOf(t, db, "lookup").ID
	editTool(t, db, "alice", "lookup", "working-copy")
	if err := SetPersistentTempToolShared(db, "alice", "lookup", false); err != nil {
		t.Fatal(err)
	}
	if got := AdoptedToolsFor(db, "bob"); len(got) != 0 {
		t.Fatalf("a withdrawn tool still loads for an adopter: %+v", got)
	}
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Fatalf("a withdrawn tool is still in the catalog: %+v", got)
	}
	gone, ok := withdrawnToolRelease(db, id)
	if !ok || gone.Owner != "alice" || gone.Name != "lookup" || gone.Version != 1 ||
		gone.Tool.CommandTemplate != "v1-cmd" || gone.WithdrawnAt.IsZero() {
		t.Fatalf("the withdrawn release should be kept as it was: %+v %v", gone, ok)
	}
	// Deleting a published tool withdraws it the same way.
	publishedTool(t, db, "carol", TempTool{Name: "other", CommandTemplate: "c"})
	cid := releaseOf(t, db, "other").ID
	if err := DeletePersistentTempTool(db, "carol", "other"); err != nil {
		t.Fatal(err)
	}
	if _, ok := withdrawnToolRelease(db, cid); !ok {
		t.Error("deleting a published tool left no tombstone")
	}
}

// Upgrading gives every published tool its release, version 1 = what it is
// now, so nothing anybody runs changes; running it again changes nothing.
func TestTheReleaseMigrationIsIdempotent(t *testing.T) {
	db := releaseStore(t)
	db.Set(persistentTempToolsTable, "alice", []PersistentTempTool{
		{Tool: TempTool{Name: "lookup", CommandTemplate: "as-published"}, Shared: true},
		{Tool: TempTool{Name: "private"}},
	})
	// Before the migration there is no release: the catalog is empty.
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Fatalf("a Shared row without a release is in the catalog: %+v", got)
	}
	if n := migrateToolReleases(db); n != 3 {
		t.Fatalf("want 2 IDs + 1 release, got %d changes", n)
	}
	rel := releaseOf(t, db, "lookup")
	if rel.Version != 1 || rel.Tool.CommandTemplate != "as-published" || rel.ID == "" {
		t.Fatalf("the migration should publish what the tool is now as version 1: %+v", rel)
	}
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if p.ID == "" {
			t.Errorf("%s got no ID", p.Tool.Name)
		}
	}
	ids := map[string]string{}
	for _, p := range LoadPersistentTempTools(db, "alice") {
		ids[p.Tool.Name] = p.ID
	}
	if n := migrateToolReleases(db); n != 0 {
		t.Errorf("a second run changed %d records", n)
	}
	for _, p := range LoadPersistentTempTools(db, "alice") {
		if ids[p.Tool.Name] != p.ID {
			t.Errorf("a second run re-minted %s's ID", p.Tool.Name)
		}
	}
	if again := releaseOf(t, db, "lookup"); again.ApprovedAt != rel.ApprovedAt || again.Version != 1 {
		t.Error("a second run rewrote the release")
	}
}

// A colleague's tool runs as the copy the taker froze. The colleague editing
// it offers an update the taker sees and may accept; until then they keep
// running what they took. Revoking the share still stops it.
func TestAPeerToolRunsTheFrozenCopyUntilAccepted(t *testing.T) {
	db := releaseStore(t)
	if err := AdminPersistTempTool(db, "alice", TempTool{Name: "ssh_run", CommandTemplate: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(db, "alice", "ssh_run", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalToolAdopted(db, "bob", "ssh_run", "alice", true); err != nil {
		t.Fatal(err)
	}
	editTool(t, db, "alice", "ssh_run", "second")

	got := adopted(t, db, "bob", "ssh_run")
	if got.Tool.CommandTemplate != "first" {
		t.Fatalf("the colleague's edit reached the taker unaccepted: %q", got.Tool.CommandTemplate)
	}
	if got.Update == nil || got.Update.CommandTemplate != "second" {
		t.Fatalf("the taker should see an update available: %+v", got.Update)
	}
	if d := got.Tool.DefinitionDiff(*got.Update); !strings.Contains(d, "+ second") {
		t.Fatalf("the update's diff does not show the change:\n%s", d)
	}
	// Accept: take it again.
	if err := SetGlobalToolAdopted(db, "bob", "ssh_run", "alice", true); err != nil {
		t.Fatal(err)
	}
	got = adopted(t, db, "bob", "ssh_run")
	if got.Tool.CommandTemplate != "second" || got.Update != nil {
		t.Fatalf("accepting should switch to the current definition: %q %+v", got.Tool.CommandTemplate, got.Update)
	}
	if err := SetPersistentTempToolSharedWith(db, "alice", "ssh_run", nil); err != nil {
		t.Fatal(err)
	}
	if got := AdoptedToolsFor(db, "bob"); len(got) != 0 {
		t.Fatalf("a revoked share still loads its frozen copy: %+v", got)
	}
}

// Adoptions written before records existed ("name<TAB>owner" and a bare name)
// still resolve, and gain an ID and, for a colleague's tool, the copy they
// were running, on first resolve.
func TestLegacyAdoptionEntriesStillResolve(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "published_one", CommandTemplate: "p"})
	if err := AdminPersistTempTool(db, "carol", TempTool{Name: "lent_one", CommandTemplate: "l1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolSharedWith(db, "carol", "lent_one", []string{"bob"}); err != nil {
		t.Fatal(err)
	}
	db.Set(adoptedGlobalToolsTable, "bob", []string{"lent_one\tcarol", "published_one"})

	got := AdoptedToolsFor(db, "bob")
	if len(got) != 2 {
		t.Fatalf("legacy adoptions should both resolve: %+v", got)
	}
	recs := loadAdoptions(db, "bob")
	if r := recs["published_one"]; r.Owner != "alice" || r.ID == "" || r.Copy != nil {
		t.Errorf("a legacy published adoption should be pinned to its owner and ID: %+v", r)
	}
	if r := recs["lent_one"]; r.ID == "" || r.Copy == nil || r.Copy.CommandTemplate != "l1" {
		t.Errorf("a legacy colleague adoption should freeze what it ran: %+v", r)
	}
	// The list stays readable in its own terms by anything that reads only it.
	if pins := loadAdoptionPins(db, "bob"); pins["published_one"] != "alice" || pins["lent_one"] != "carol" {
		t.Errorf("the adoption list lost its owners: %+v", pins)
	}
	editTool(t, db, "carol", "lent_one", "l2")
	if p := adopted(t, db, "bob", "lent_one"); p.Tool.CommandTemplate != "l1" || p.Update == nil {
		t.Fatalf("after its first resolve a legacy adoption is a frozen copy like any other: %+v", p)
	}
}

// A tool deleted and made again under the same name is a new tool: a new ID,
// and neither a publish nor a share of it reattaches an old adoption.
func TestARecreatedToolIsNotReattached(t *testing.T) {
	db := releaseStore(t)
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "original"})
	if err := SetGlobalToolAdopted(db, "bob", "lookup", "", true); err != nil {
		t.Fatal(err)
	}
	first := releaseOf(t, db, "lookup").ID
	if err := DeletePersistentTempTool(db, "alice", "lookup"); err != nil {
		t.Fatal(err)
	}
	publishedTool(t, db, "alice", TempTool{Name: "lookup", CommandTemplate: "replacement"})
	if second := releaseOf(t, db, "lookup").ID; second == first || second == "" {
		t.Fatalf("a recreated tool kept the old ID %q", second)
	}
	if got := AdoptedToolsFor(db, "bob"); len(got) != 0 {
		t.Fatalf("an adoption reattached to a recreated tool: %+v", got)
	}

	// The same for a colleague's share.
	if err := AdminPersistTempTool(db, "carol", TempTool{Name: "lent", CommandTemplate: "original"}); err != nil {
		t.Fatal(err)
	}
	_ = SetPersistentTempToolSharedWith(db, "carol", "lent", []string{"dana"})
	if err := SetGlobalToolAdopted(db, "dana", "lent", "carol", true); err != nil {
		t.Fatal(err)
	}
	_ = DeletePersistentTempTool(db, "carol", "lent")
	_ = AdminPersistTempTool(db, "carol", TempTool{Name: "lent", CommandTemplate: "replacement"})
	_ = SetPersistentTempToolSharedWith(db, "carol", "lent", []string{"dana"})
	if got := AdoptedToolsFor(db, "dana"); len(got) != 0 {
		t.Fatalf("an adoption reattached to a recreated shared tool: %+v", got)
	}
}

// The review diff names every changed field, and only changed ones; the
// governance flags are not changes.
func TestDefinitionDiffShowsChangedFieldsOnly(t *testing.T) {
	a := TempTool{Name: "t", Description: "old desc", CommandTemplate: "run {x}",
		ScriptBody: "line one\nline two\nline three", Params: map[string]ToolParam{"x": {Type: "string"}}}
	b := a
	b.Description = "new desc"
	b.ScriptBody = "line one\nline 2\nline three"
	b.Locked, b.Disabled = true, true
	d := a.DefinitionDiff(b)
	for _, want := range []string{"description:", "- old desc", "+ new desc", "script body:", "- line two", "+ line 2"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	for _, not := range []string{"line one", "locked", "disabled", "command"} {
		if strings.Contains(d, not) {
			t.Errorf("diff shows %q, which did not change:\n%s", not, d)
		}
	}
	if a.DefinitionDiff(a) != "" {
		t.Error("identical definitions produced a diff")
	}
	c := a
	c.Params = map[string]ToolParam{"x": {Type: "string"}, "y": {Type: "integer"}}
	if d := a.DefinitionDiff(c); !strings.Contains(d, "parameters:") || !strings.Contains(d, "+") {
		t.Errorf("a new parameter is not shown:\n%s", d)
	}
}

// A tool the user took and lost can be recreated as their own, from what they
// were running, when the owner withdrew or deleted it. Not when the owner took
// it away from this user in particular: that is a decision about them.
func TestALostToolIsRecreatedOnlyWhenItWasWithdrawn(t *testing.T) {
	setup := func(t *testing.T) Database {
		db := &DBase{Store: kvlite.MemStore()}
		saved := RootDB
		RootDB = db
		t.Cleanup(func() { RootDB = saved })
		if err := AdminPersistTempTool(db, "alice", TempTool{Name: "wiki_read", Description: "d", CommandTemplate: "echo alice-v1"}); err != nil {
			t.Fatal(err)
		}
		return db
	}
	own := func(db Database, user string) (TempTool, bool) {
		for _, p := range LoadPersistentTempTools(db, user) {
			if p.Tool.Name == "wiki_read" {
				return p.Tool, true
			}
		}
		return TempTool{}, false
	}

	t.Run("published then withdrawn", func(t *testing.T) {
		db := setup(t)
		_ = SetPersistentTempToolShared(db, "alice", "wiki_read", true)
		if err := SetGlobalToolAdopted(db, "bob", "wiki_read", "alice", true); err != nil {
			t.Fatal(err)
		}
		if _, err := RecreateLostTool(db, "bob", "wiki_read", false); err == nil {
			t.Fatal("a tool that still works was offered for recreation")
		}
		_ = SetPersistentTempToolShared(db, "alice", "wiki_read", false)
		if _, err := RecreateLostTool(db, "bob", "wiki_read", true); err != nil {
			t.Fatalf("a withdrawn tool could not be recreated: %v", err)
		}
		got, ok := own(db, "bob")
		if !ok || got.CommandTemplate != "echo alice-v1" {
			t.Fatalf("bob's copy is not what he was running: %+v", got)
		}
		if LoadAdoptedGlobalTools(db, "bob")["wiki_read"] {
			t.Error("the adoption of the gone tool was left behind")
		}
	})
	t.Run("colleague deleted it", func(t *testing.T) {
		db := setup(t)
		_ = SetPersistentTempToolSharedWith(db, "alice", "wiki_read", []string{"bob"})
		_ = SetGlobalToolAdopted(db, "bob", "wiki_read", "alice", true)
		AdoptedToolsFor(db, "bob") // takes the frozen copy
		_ = DeletePersistentTempTool(db, "alice", "wiki_read")
		if _, err := RecreateLostTool(db, "bob", "wiki_read", true); err != nil {
			t.Fatalf("a deleted colleague's tool could not be recreated from the copy: %v", err)
		}
		if got, ok := own(db, "bob"); !ok || got.CommandTemplate != "echo alice-v1" {
			t.Fatalf("recreated from the wrong definition: %+v", got)
		}
	})
	t.Run("share revoked", func(t *testing.T) {
		db := setup(t)
		_ = SetPersistentTempToolSharedWith(db, "alice", "wiki_read", []string{"bob"})
		_ = SetGlobalToolAdopted(db, "bob", "wiki_read", "alice", true)
		AdoptedToolsFor(db, "bob")
		_ = SetPersistentTempToolSharedWith(db, "alice", "wiki_read", nil)
		if _, err := RecreateLostTool(db, "bob", "wiki_read", true); err == nil {
			t.Error("recreating undid a revocation aimed at this user")
		}
		if _, ok := own(db, "bob"); ok {
			t.Error("a refused recreate still landed a tool")
		}
	})
	t.Run("left off the adopt list", func(t *testing.T) {
		db := setup(t)
		_ = SetPersistentTempToolShared(db, "alice", "wiki_read", true)
		_ = SetGlobalToolAdopted(db, "bob", "wiki_read", "alice", true)
		_ = SetPersistentTempToolAllowedUsers(db, "alice", "wiki_read", []string{"carol"})
		if _, err := RecreateLostTool(db, "bob", "wiki_read", true); err == nil {
			t.Error("recreating undid an administrator narrowing the tool")
		}
	})
}

// The migration that grandfathered users into the global tools they used to
// see put their OWN published tools on their adopt list. Nobody adopts what
// they own, so those entries never loaded, and read downstream as tools
// somebody had taken back. They are dropped; a real adoption is kept.
func TestAnAdoptionOfYourOwnToolIsDropped(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	for _, owner := range []string{"alice", "bob"} {
		if err := AdminPersistTempTool(db, owner, TempTool{Name: owner + "_tool", Description: "d", CommandTemplate: "echo " + owner}); err != nil {
			t.Fatal(err)
		}
		_ = SetPersistentTempToolShared(db, owner, owner+"_tool", true)
	}
	MergeAdoptedGlobalTools(db, "alice", []string{"alice_tool", "bob_tool"})

	var loaded []string
	for _, p := range AdoptedToolsFor(db, "alice") {
		loaded = append(loaded, p.Tool.Name)
	}
	if strings.Join(loaded, ",") != "bob_tool" {
		t.Errorf("only the colleague's tool loads as adopted: %v", loaded)
	}
	adopted := LoadAdoptedGlobalTools(db, "alice")
	if adopted["alice_tool"] || !adopted["bob_tool"] {
		t.Errorf("the self-adoption should be dropped and the real one kept: %v", adopted)
	}
}

// TestSharedPersistentTempTools covers the deployment-wide shared pool: a tool
// is private to its owner until marked Shared, then surfaces in the shared pool
// (one per name: a second owner's same-named publish is refused), and drops back
// out when unshared.
func TestSharedPersistentTempTools(t *testing.T) {
	db := OpenCache()

	for _, tt := range []struct{ user, name string }{
		{"alice", "weather"}, {"alice", "deploy"}, {"bob", "weather"},
	} {
		if err := AdminPersistTempTool(db, tt.user, TempTool{Name: tt.name}); err != nil {
			t.Fatalf("persist %s/%s: %v", tt.user, tt.name, err)
		}
	}

	// Nothing shared yet.
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Fatalf("no tools shared yet, got %d", len(got))
	}

	// Sharing a tool that isn't in the pool errors.
	if err := SetPersistentTempToolShared(db, "alice", "nope", true); err == nil {
		t.Error("sharing a missing tool should error")
	}

	// Share alice's weather + deploy. Bob's weather is REFUSED: the deployment
	// publishes one tool per name, because two would leave every lookup by name
	// to pick one, and whichever it picked is whose code an adopter runs.
	for _, tt := range []struct{ user, name string }{
		{"alice", "weather"}, {"alice", "deploy"},
	} {
		if err := SetPersistentTempToolShared(db, tt.user, tt.name, true); err != nil {
			t.Fatalf("share %s/%s: %v", tt.user, tt.name, err)
		}
	}
	if err := SetPersistentTempToolShared(db, "bob", "weather", true); err == nil {
		t.Fatal("a second published tool called weather was allowed")
	}
	got := LoadSharedPersistentTempTools(db)
	names := map[string]int{}
	for _, p := range got {
		if !p.Shared {
			t.Errorf("shared pool returned a non-shared tool %q", p.Tool.Name)
		}
		names[p.Tool.Name]++
	}
	if names["weather"] != 1 || names["deploy"] != 1 || len(got) != 2 {
		t.Fatalf("shared pool = %v (want one weather + one deploy)", names)
	}

	// Once alice unpublishes weather, bob may publish his.
	if err := SetPersistentTempToolShared(db, "alice", "weather", false); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(db, "bob", "weather", true); err != nil {
		t.Fatalf("bob could not publish weather once the name was free: %v", err)
	}
	if got := LoadSharedPersistentTempTools(db); len(got) != 2 {
		t.Errorf("bob shares weather + alice shares deploy → 2, got %d", len(got))
	}
	// Unshare the rest → empty pool.
	_ = SetPersistentTempToolShared(db, "bob", "weather", false)
	_ = SetPersistentTempToolShared(db, "alice", "deploy", false)
	if got := LoadSharedPersistentTempTools(db); len(got) != 0 {
		t.Errorf("after unsharing all, expected empty pool, got %d", len(got))
	}
}

func toolCollisionStore(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	return db
}

// toolBundle is a one-tool bundle as an export would write it.
func toolBundle(t *testing.T, tt TempTool) []byte {
	t.Helper()
	recipe, err := json.Marshal(tt)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(ArtifactBundle{Bundle: ArtifactBundleFormat,
		Artifacts: []PortableArtifact{{Type: "tool", Name: tt.Name, Recipe: recipe}}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func pendingNamed(db Database, user, name string) (PendingTempTool, bool) {
	for _, p := range LoadPendingTempTools(db, user) {
		if p.Tool.Name == name {
			return p, true
		}
	}
	return PendingTempTool{}, false
}

// Preview said "skip" for a tool whose name matched one already waiting for
// review, and the import then replaced that pending tool: different code under
// the name an administrator may have been halfway through reading. Import
// skips it too now, so the preview is the truth.
func TestToolImportSkipsAPendingToolAsThePreviewSays(t *testing.T) {
	db := toolCollisionStore(t)
	if err := QueuePendingTempTool(db, "alice", TempTool{Name: "wiki_read", CommandTemplate: "echo first"}, "s1"); err != nil {
		t.Fatal(err)
	}
	bundle := toolBundle(t, TempTool{Name: "wiki_read", CommandTemplate: "echo second"})

	prev, err := PreviewArtifactBundleAsUser(db, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if prev.WouldSkip != 1 {
		t.Fatalf("precondition: preview skips it: %+v", prev.Items)
	}
	res, err := ImportArtifactBundleAsUser(db, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 0 || res.Skipped != 1 {
		t.Errorf("the import disagreed with its preview: %+v", res.Outcomes)
	}
	if p, _ := pendingNamed(db, "alice", "wiki_read"); p.Tool.CommandTemplate != "echo first" {
		t.Errorf("the pending tool was replaced: %q", p.Tool.CommandTemplate)
	}
}

// An import meets the rules every authoring path applies to a new name: a
// bundle could queue a tool named after a built-in, and approving it put a
// stub in front of the real one. Preview predicts the same skip.
func TestToolImportRefusesNamesNoAuthoringPathWouldAccept(t *testing.T) {
	db := toolCollisionStore(t)
	RegisterReservedToolName("zz_reserved_for_import_test")
	for _, name := range []string{"zz_reserved_for_import_test", "Not-Snake"} {
		bundle := toolBundle(t, TempTool{Name: name, CommandTemplate: "echo hi"})
		prev, err := PreviewArtifactBundleAsUser(db, bundle, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if prev.WouldImport != 0 {
			t.Errorf("%q: preview predicts an import", name)
		}
		res, err := ImportArtifactBundleAsUser(db, bundle, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if res.Imported != 0 {
			t.Errorf("%q: imported", name)
		}
		if _, queued := pendingNamed(db, "alice", name); queued {
			t.Errorf("%q: queued for approval", name)
		}
	}
}

// Two imports queueing one name are two authors. The second used to replace
// the first silently, scope and all, so the agent the first was scoped to
// lost its tool on approval. The same definition now keeps its place and gains
// the scope; a different one is refused and the pending tool left alone.
func TestQueueingANameAlreadyPendingNeverReplacesIt(t *testing.T) {
	db := toolCollisionStore(t)
	tool := TempTool{Name: "wiki_read", CommandTemplate: "echo one"}
	if err := QueuePendingTempToolScoped(db, "alice", tool, "import", []string{"agent-1"}); err != nil {
		t.Fatal(err)
	}
	if err := QueuePendingTempToolScoped(db, "alice", tool, "import", []string{"agent-2"}); err != nil {
		t.Fatalf("the same definition again should merge: %v", err)
	}
	p, _ := pendingNamed(db, "alice", "wiki_read")
	if !sliceHas(p.ScopeAgents, "agent-1") || !sliceHas(p.ScopeAgents, "agent-2") {
		t.Errorf("the scopes were not merged: %v", p.ScopeAgents)
	}
	if n := len(LoadPendingTempTools(db, "alice")); n != 1 {
		t.Errorf("one name, %d pending entries", n)
	}

	other := TempTool{Name: "wiki_read", CommandTemplate: "echo two"}
	if err := QueuePendingTempToolScoped(db, "alice", other, "import", []string{"agent-3"}); err == nil {
		t.Error("a different definition replaced the pending tool")
	}
	p, _ = pendingNamed(db, "alice", "wiki_read")
	if p.Tool.CommandTemplate != "echo one" || sliceHas(p.ScopeAgents, "agent-3") {
		t.Errorf("the refused queue changed the pending tool: %+v", p)
	}
}

// The share ledger said "Taken: your agents load it" whenever the name was on
// the adoption list: also when the adoption was pinned to somebody else's tool
// of that name, and when the user's own tool held the name.
func TestTheLedgerSaysWhichToolTheAgentsReallyLoad(t *testing.T) {
	db := toolCollisionStore(t)
	for _, owner := range []string{"lender_a", "lender_b"} {
		if err := AdminPersistTempTool(db, owner, TempTool{Name: "wiki_read", CommandTemplate: "echo " + owner}); err != nil {
			t.Fatal(err)
		}
		if err := SetPersistentTempToolSharedWith(db, owner, "wiki_read", []string{"taker"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetGlobalToolAdopted(db, "taker", "wiki_read", "lender_b", true); err != nil {
		t.Fatal(err)
	}
	detail := func() map[string]string {
		out := map[string]string{}
		for _, g := range shareledger.ToMe("taker") {
			if g.Kind == "tool" {
				out[g.Owner] = g.Detail
			}
		}
		return out
	}
	got := detail()
	if got["lender_b"] != "Taken: your agents load it." {
		t.Errorf("the taken tool: %q", got["lender_b"])
	}
	if strings.HasPrefix(got["lender_a"], "Taken") || !strings.Contains(got["lender_a"], "lender_b") {
		t.Errorf("an offer of the same name from somebody else read as taken: %q", got["lender_a"])
	}
	if m := shareledger.Manifest("tool", "lender_a", "wiki_read", "taker"); len(m) == 0 {
		t.Error("lender_a's tool does not load for taker, and the manifest said nothing")
	}

	// The user's own tool of the name wins over both.
	if err := AdminPersistTempTool(db, "taker", TempTool{Name: "wiki_read", CommandTemplate: "echo own"}); err != nil {
		t.Fatal(err)
	}
	if d := detail()["lender_b"]; strings.HasPrefix(d, "Taken") {
		t.Errorf("a taken tool the user's own copy shadows read as loaded: %q", d)
	}
	if m := shareledger.Manifest("tool", "lender_b", "wiki_read", "taker"); len(m) == 0 {
		t.Error("the manifest said nothing about the user's own copy running instead")
	}
}

// Confirmation must survive a Builder edit.
//
// Trial says nobody has vouched for a tool yet, and clearing it is a user
// action taken in Extensions › Tools — never something tool_def carries. So a
// Builder edit reconstructing the record silently UN-CONFIRMED a tool the user
// had confirmed, and the consequence was invisible: a trial tool is kept out
// of the inline catalog and reachable only via load_tool, so the agent stopped
// seeing it and reached for whatever it could see instead.
//
// Reported live as "the moltbook agent keeps using fetch_url over the moltbook
// tools" — a tool that had been confirmed, was edited, and quietly left the
// catalog. Nothing failed; it simply was not offered.
func TestConfirmationSurvivesABuilderEdit(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const user = "owner@example.test"

	// A confirmed tool: the user vouched for it, so Trial is false.
	confirmed := TempTool{Name: "moltbook", Description: "post to moltbook"}
	if err := AdminPersistTempTool(db, user, confirmed); err != nil {
		t.Fatal(err)
	}

	// Builder edits it — reconstructing the record from tool_def args, which
	// know nothing about confirmation.
	edited := TempTool{Name: "moltbook", Description: "post to moltbook (v2)", Trial: true}
	if err := AdminPersistTempTool(db, user, edited); err != nil {
		t.Fatal(err)
	}

	for _, got := range LoadPersistentTempTools(db, user) {
		if got.Tool.Name != "moltbook" {
			continue
		}
		if got.Tool.Trial {
			t.Fatal("an edit un-confirmed a confirmed tool; it would drop out of the inline catalog and the agent would stop seeing it")
		}
		// The edit's real content still lands.
		if got.Tool.Description != "post to moltbook (v2)" {
			t.Errorf("the edit did not apply: %q", got.Tool.Description)
		}
	}
}

// And in the other direction: an edit is not a vouching either. A tool nobody
// has confirmed stays unconfirmed after Builder touches it.
func TestAnEditDoesNotConfirmAnUnvouchedTool(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const user = "u"
	since := time.Now().Add(-48 * time.Hour).UTC()

	trial := TempTool{Name: "draft", Trial: true, TrialSince: since}
	if err := AdminPersistTempTool(db, user, trial); err != nil {
		t.Fatal(err)
	}
	// Builder edits it without setting Trial at all.
	if err := AdminPersistTempTool(db, user, TempTool{Name: "draft", Description: "edited"}); err != nil {
		t.Fatal(err)
	}
	for _, got := range LoadPersistentTempTools(db, user) {
		if got.Tool.Name != "draft" {
			continue
		}
		if !got.Tool.Trial {
			t.Fatal("an edit confirmed a tool nobody vouched for")
		}
		// The reaper's clock must not restart on every edit, or an unconfirmed
		// tool edited regularly would never age out.
		if !got.Tool.TrialSince.Equal(since) {
			t.Errorf("TrialSince moved from %v to %v — the reaper clock restarted", since, got.Tool.TrialSince)
		}
	}
}

// The flags that were already preserved must stay preserved — this change adds
// to that list rather than replacing it.
func TestTheOtherGovernanceFlagsStillSurvive(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	const user = "u"

	if err := AdminPersistTempTool(db, user, TempTool{
		Name: "t", Disabled: true, BuilderOnly: true, BoundOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(db, user, TempTool{Name: "t", Description: "edited"}); err != nil {
		t.Fatal(err)
	}
	for _, got := range LoadPersistentTempTools(db, user) {
		if got.Tool.Name != "t" {
			continue
		}
		if !got.Tool.Disabled || !got.Tool.BuilderOnly || !got.Tool.BoundOnly {
			t.Fatalf("an edit cleared a governance flag: %+v", got.Tool)
		}
	}
}
