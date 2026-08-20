// Removing an account has to remove what authenticates as it.
//
// Deleting a user used to unset one record, leaving live sessions, personal
// access tokens, desktop keys, bridge keys, connected OAuth accounts and
// outstanding reset links all working against a username that no longer
// existed. These tests pin each of those, plus the two backstops: a session
// whose user vanished by some other path dies when it is next loaded from the
// store, and an app can register its own credentials into the sweep.
package core

import (
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// revokeFixture stands up an auth store with one user plus a RootDB, and
// restores both hooks afterwards.
func revokeFixture(t *testing.T, user string) Database {
	t.Helper()
	prevAuth, prevRoot := AuthDB, RootDB
	db := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(db, user, "pw", false)
	AuthDB = func() Database { return db }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	return db
}

func TestDeleteUserEndsTheirSessions(t *testing.T) {
	db := revokeFixture(t, "craig")
	token := AuthCreateSession(db, "craig")
	if u, ok := AuthValidateSession(db, token); !ok || u != "craig" {
		t.Fatal("fixture: the session should start valid")
	}

	AuthDeleteUser(db, "craig")

	if _, ok := AuthValidateSession(db, token); ok {
		t.Error("a deleted user's session still validates")
	}
	// And it is gone from the store, not merely from the cache.
	var s authSession
	if db.Get(AuthSessionTable, token, &s) {
		t.Error("the session record survived the delete")
	}
}

func TestDeleteUserRevokesAccessTokens(t *testing.T) {
	db := revokeFixture(t, "craig")
	tok := MintAccountToken("craig", "laptop")
	if owner, ok := lookupAccountTokenOwner(tok.Token); !ok || owner != "craig" {
		t.Fatal("fixture: the token should start valid")
	}

	AuthDeleteUser(db, "craig")

	if _, ok := lookupAccountTokenOwner(tok.Token); ok {
		t.Error("a deleted user's personal access token still resolves")
	}
}

func TestDeleteUserRevokesDesktopKeys(t *testing.T) {
	db := revokeFixture(t, "craig")
	key, ok := MintDesktopKey("craig")
	if !ok {
		t.Fatal("fixture: could not mint a desktop key")
	}
	if _, ok := LookupDesktopKey(key); !ok {
		t.Fatal("fixture: the key should start valid")
	}

	AuthDeleteUser(db, "craig")

	if _, ok := LookupDesktopKey(key); ok {
		t.Error("a deleted user's desktop key still resolves")
	}
}

func TestDeleteUserRevokesResetLinks(t *testing.T) {
	// A live reset link is a way back INTO the account, so it has to go with it.
	db := revokeFixture(t, "craig")
	token := createResetToken(db, "craig")
	if _, ok := validateResetToken(db, token); !ok {
		t.Fatal("fixture: the reset token should start valid")
	}

	AuthDeleteUser(db, "craig")

	if _, ok := validateResetToken(db, token); ok {
		t.Error("a deleted user's password-reset link still works")
	}
}

// fakeRevoker stands in for an app-owned credential store (bridge keys).
type fakeRevoker struct {
	keys map[string]string // secret -> owner
}

func (fakeRevoker) CredentialKind() string { return "test keys" }

func (f *fakeRevoker) RevokeUserCredentials(user string) int {
	n := 0
	for secret, owner := range f.keys {
		if owner == user {
			delete(f.keys, secret)
			n++
		}
	}
	return n
}

func TestDeleteUserReachesAppRegisteredCredentials(t *testing.T) {
	// Core must not know what a bridge key is, so an app's credentials come
	// into the sweep through the registry rather than by core reaching in.
	db := revokeFixture(t, "craig")
	f := &fakeRevoker{keys: map[string]string{"s1": "craig", "s2": "dana"}}

	prev := revokers
	RegisterUserCredentialRevoker(f)
	t.Cleanup(func() { revokerMu.Lock(); revokers = prev; revokerMu.Unlock() })

	AuthDeleteUser(db, "craig")

	if _, still := f.keys["s1"]; still {
		t.Error("the app's credential for the deleted user survived")
	}
	if _, ok := f.keys["s2"]; !ok {
		t.Error("another user's credential was destroyed too")
	}
}

func TestRevokeReportsWhatItTook(t *testing.T) {
	db := revokeFixture(t, "craig")
	AuthCreateSession(db, "craig")
	MintAccountToken("craig", "laptop")

	counts := RevokeUserCredentials(db, "craig")
	if counts["sessions"] != 1 || counts["access tokens"] != 1 {
		t.Fatalf("unexpected counts: %v", counts)
	}
	// Stable order, so the audit line reads the same every time.
	if got := FormatRevocation(counts); got != "1 access tokens, 1 sessions" {
		t.Errorf("FormatRevocation: got %q", got)
	}
	if got := FormatRevocation(nil); got != "" {
		t.Errorf("nothing revoked should format empty, got %q", got)
	}
}

func TestRevokeIsIdempotentAndScoped(t *testing.T) {
	db := revokeFixture(t, "craig")
	AuthSetUser(db, "dana", "pw", false)
	danaToken := AuthCreateSession(db, "dana")
	AuthCreateSession(db, "craig")

	first := RevokeUserCredentials(db, "craig")
	if first["sessions"] != 1 {
		t.Fatalf("first sweep: %v", first)
	}
	// Re-running must be safe — a partial failure should be re-runnable.
	if second := RevokeUserCredentials(db, "craig"); len(second) != 0 {
		t.Errorf("second sweep should find nothing, got %v", second)
	}
	if _, ok := AuthValidateSession(db, danaToken); !ok {
		t.Error("dana was signed out by craig's revocation")
	}
}

func TestRevokeSessionsKeepsTheAccount(t *testing.T) {
	// The "lost laptop" action: signed out everywhere, account intact.
	db := revokeFixture(t, "craig")
	token := AuthCreateSession(db, "craig")

	if n := AuthRevokeUserSessions(db, "craig"); n != 1 {
		t.Fatalf("expected 1 session revoked, got %d", n)
	}
	if _, ok := AuthValidateSession(db, token); ok {
		t.Error("the session survived")
	}
	if _, ok := AuthGetUser(db, "craig"); !ok {
		t.Error("the account should still exist")
	}
	// And they can sign back in.
	if !AuthCheckPassword(db, "craig", "pw") {
		t.Error("the account should still accept its password")
	}
}

func TestSessionForAVanishedUserDiesOnReload(t *testing.T) {
	// The backstop. Checked on the store-load path rather than per request, so
	// it costs nothing on a cache hit — which also means a restart clears out
	// any session whose user is gone.
	db := revokeFixture(t, "craig")
	token := AuthCreateSession(db, "craig")

	// Remove the user WITHOUT the revocation sweep, standing in for any path
	// that drops a user record directly.
	db.Unset(AuthTable, "user:"+"craig")
	// Drop the cache the way a restart would.
	sessionMu.Lock()
	delete(sessionCache, token)
	sessionMu.Unlock()

	if _, ok := AuthValidateSession(db, token); ok {
		t.Error("a session for a user who no longer exists still validates")
	}
}

func TestSessionForAPendingUserDiesOnReload(t *testing.T) {
	// Same backstop, other half: an account put back into pending approval
	// must not keep working through a session issued before that.
	db := revokeFixture(t, "craig")
	token := AuthCreateSession(db, "craig")

	user, _ := AuthGetUser(db, "craig")
	user.Pending = true
	db.Set(AuthTable, "user:"+"craig", user)
	sessionMu.Lock()
	delete(sessionCache, token)
	sessionMu.Unlock()

	if _, ok := AuthValidateSession(db, token); ok {
		t.Error("a session for a pending user still validates")
	}
}
