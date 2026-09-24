package temptool

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// Tool names are per-user, and the cache was keyed by the bare name, so a
// "global" entry one user's tool wrote was served to another user's different
// tool of the same name.
func TestCacheGlobalScopeIsPerToolOwner(t *testing.T) {
	withMemRootDB(t)
	mine := TempTool{Name: "lookup", CommandTemplate: "echo a", Cache: &TempToolCache{Scope: "global"}}
	theirs := TempTool{Name: "lookup", CommandTemplate: "echo b", Cache: &TempToolCache{Scope: "global"}}
	if err := AdminPersistTempTool(RootDB, "user1", mine); err != nil {
		t.Fatal(err)
	}
	if err := AdminPersistTempTool(RootDB, "user2", theirs); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"q": "x"}
	storeTempToolCache(newSess("user1", "", ""), &mine, args, "planted")
	if got, ok := lookupTempToolCache(newSess("user2", "", ""), &theirs, args); ok {
		t.Fatalf("another user's same-named tool read the entry: %q", got)
	}

	// A shared tool keeps one global partition, its owner's, for everyone.
	shared := TempTool{Name: "shared_q", CommandTemplate: "echo s", Cache: &TempToolCache{Scope: "global"}}
	if err := AdminPersistTempTool(RootDB, "user2", shared); err != nil {
		t.Fatal(err)
	}
	if err := SetPersistentTempToolShared(RootDB, "user2", "shared_q", true); err != nil {
		t.Fatal(err)
	}
	storeTempToolCache(newSess("user1", "", ""), &shared, args, "from-shared")
	if got, ok := lookupTempToolCache(newSess("user3", "", ""), &shared, args); !ok || got != "from-shared" {
		t.Fatalf("a shared tool's global entry should reach its other users: %q %v", got, ok)
	}
}

// Session ids repeat across users (channel and MCP threads use fixed names),
// so a session-scoped entry has to carry the user as well.
func TestCacheSessionScopeIsPerUser(t *testing.T) {
	withMemRootDB(t)
	tt := &TempTool{Name: "sess_tool", Cache: &TempToolCache{Scope: "session"}}
	args := map[string]any{"q": "x"}
	storeTempToolCache(newSess("user1", "fixed-thread", ""), tt, args, "user1-val")
	if got, ok := lookupTempToolCache(newSess("user2", "fixed-thread", ""), tt, args); ok {
		t.Fatalf("a session-scoped entry crossed users on a shared session id: %q", got)
	}
}
