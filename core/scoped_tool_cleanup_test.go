package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// TestCleanupSessionDraftsIsUserScoped is the regression for a cross-user
// delete. Session drafts live in ONE global table keyed by chat-session id with
// no owner on the row, so the old implementation — which walked that table and
// matched on the bare tool name — cleaned every user's sessions: alice
// persisting "get_weather" silently deleted bob's unrelated draft of the same
// name.
func TestCleanupSessionDraftsIsUserScoped(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })

	// Both users hold a draft named "get_weather" in their own session.
	if err := SaveSessionTempTool(db, "s-alice", TempTool{Name: "get_weather"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionTempTool(db, "s-bob", TempTool{Name: "get_weather"}); err != nil {
		t.Fatal(err)
	}
	// The lister is what scopes sessions to a user; stub it per-user.
	RegisterScopedToolLister(func(user string) []ScopedTool {
		switch user {
		case "alice":
			return []ScopedTool{{Tool: TempTool{Name: "get_weather"}, Scope: ScopeSessionTool, SessionID: "s-alice"}}
		case "bob":
			return []ScopedTool{{Tool: TempTool{Name: "get_weather"}, Scope: ScopeSessionTool, SessionID: "s-bob"}}
		}
		return nil
	})
	t.Cleanup(func() { RegisterScopedToolLister(nil) })

	if n := cleanupSessionDraftsByName(db, "alice", "get_weather"); n != 1 {
		t.Fatalf("cleaned %d, want 1 (alice's own)", n)
	}
	if got := LoadSessionTempTools(db, "s-alice"); len(got) != 0 {
		t.Errorf("alice's draft should be gone, got %v", got)
	}
	if got := LoadSessionTempTools(db, "s-bob"); len(got) != 1 {
		t.Fatalf("bob's draft must survive alice's cleanup, got %v", got)
	}
}

// TestCleanupSessionDraftsNoLister: without the sessions app there are no
// sessions to clean, so this must be a no-op — NOT a fallback global sweep,
// which is the behavior that caused the leak.
func TestCleanupSessionDraftsNoLister(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	RegisterScopedToolLister(nil)

	if err := SaveSessionTempTool(db, "s1", TempTool{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if n := cleanupSessionDraftsByName(db, "alice", "x"); n != 0 {
		t.Errorf("cleaned %d with no lister, want 0", n)
	}
	if got := LoadSessionTempTools(db, "s1"); len(got) != 1 {
		t.Errorf("draft must survive, got %v", got)
	}
}

// TestCleanupSessionDraftsRequiresUser guards the empty-username path: without
// a user there is nothing to scope to, so it must clean nothing rather than
// fall back to everything.
func TestCleanupSessionDraftsRequiresUser(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saved := RootDB
	RootDB = db
	t.Cleanup(func() { RootDB = saved })
	RegisterScopedToolLister(func(string) []ScopedTool {
		return []ScopedTool{{Tool: TempTool{Name: "x"}, Scope: ScopeSessionTool, SessionID: "s1"}}
	})
	t.Cleanup(func() { RegisterScopedToolLister(nil) })

	if err := SaveSessionTempTool(db, "s1", TempTool{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if n := cleanupSessionDraftsByName(db, "", "x"); n != 0 {
		t.Errorf("cleaned %d with no username, want 0", n)
	}
}
