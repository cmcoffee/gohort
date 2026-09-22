package temptool

// Which entry point a network call comes from decides whether the workspace's
// reach ceiling applies to it.

import (
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A granted tool may reach the network through the broker even when the
// agent's workspace may not dial out. A draft the agent wrote this turn may
// not, or tool_def is the way around the ceiling.
func TestOnlyAToolInThePoolEarnsTheReachLift(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	sess := &ToolSession{DB: db, Username: "alice"}
	if err := AdminPersistTempTool(db, "alice", TempTool{
		Name: "granted_fetcher", CommandTemplate: "echo hi"}); err != nil {
		t.Fatal(err)
	}
	if !toolIsGranted(sess, &TempTool{Name: "granted_fetcher"}) {
		t.Error("a tool in the owner's pool was not recognized as granted")
	}
	if toolIsGranted(sess, &TempTool{Name: "just_minted"}) {
		t.Error("a draft the agent authored this turn earned the lift, so tool_def routes around the ceiling")
	}
	// Unresolvable reads as NOT granted. A privilege handed out when the
	// answer could not be determined is a hole, not a privilege.
	if toolIsGranted(nil, &TempTool{Name: "granted_fetcher"}) {
		t.Error("no session read as granted")
	}
	if toolIsGranted(sess, &TempTool{Name: "  "}) {
		t.Error("a blank name read as granted")
	}
}
