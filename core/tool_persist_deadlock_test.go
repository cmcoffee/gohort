package core

import (
	"testing"
	"time"

	"github.com/cmcoffee/snugforge/kvlite"
)

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
