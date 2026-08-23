package core

import (
	"context"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// The admin stamp is the whole basis of the "admin only" unsandboxed bypass,
// so what it refuses to stamp matters more than what it stamps. Every path
// that cannot PROVE admin-ness must leave the context unmarked, because
// unmarked means confined.

func withAuthDB(t *testing.T) Database {
	t.Helper()
	db := &DBase{Store: kvlite.MemStore()}
	prev := AuthDB
	AuthDB = func() Database { return db }
	t.Cleanup(func() { AuthDB = prev })
	return db
}

// stamped reads the flag through the SAME accessor the sandbox consults, so a
// test can never pass against a stamp the sandbox would not see.
func stamped(ctx context.Context) bool { return CallerIsAdmin(ctx) }

func TestOnlyAnAdminGetsStamped(t *testing.T) {
	db := withAuthDB(t)
	AuthSetUser(db, "boss", "pw", true)
	AuthSetUser(db, "peon", "pw", false)

	if !stamped(ContextWithSandboxUser(context.Background(), "boss")) {
		t.Error("an admin was not stamped")
	}
	for _, who := range []string{"peon", "ghost", "", "   "} {
		if stamped(ContextWithSandboxUser(context.Background(), who)) {
			t.Errorf("%q was stamped as an admin", who)
		}
	}
}

// A session is just a way of naming the user; it must not become a second,
// weaker route to the same stamp.
func TestSessionStampAgreesWithTheUserStamp(t *testing.T) {
	db := withAuthDB(t)
	AuthSetUser(db, "boss", "pw", true)
	AuthSetUser(db, "peon", "pw", false)

	if !stamped((&ToolSession{Username: "boss"}).ContextWithSandboxCaller(context.Background())) {
		t.Error("an admin's session was not stamped")
	}
	if stamped((&ToolSession{Username: "peon"}).ContextWithSandboxCaller(context.Background())) {
		t.Error("a non-admin's session was stamped")
	}
	var nilSess *ToolSession
	if stamped(nilSess.ContextWithSandboxCaller(context.Background())) {
		t.Error("a nil session produced an admin stamp")
	}
}

// No auth store attached (early boot, a stripped test harness) is not evidence
// of admin-ness. It used to be tempting to treat an unavailable store as
// "can't check, carry on" — that is the reading that opens the host.
func TestNoAuthStoreStampsNothing(t *testing.T) {
	prev := AuthDB
	AuthDB = nil
	t.Cleanup(func() { AuthDB = prev })
	if stamped(ContextWithSandboxUser(context.Background(), "boss")) {
		t.Error("an unavailable auth store was read as admin")
	}

	AuthDB = func() Database { return nil }
	if stamped(ContextWithSandboxUser(context.Background(), "boss")) {
		t.Error("a nil auth store was read as admin")
	}
}
