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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/gohort/core/promotion"
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
	db := revokeFixture(t, "alice")
	token := AuthCreateSession(db, "alice")
	if u, ok := AuthValidateSession(db, token); !ok || u != "alice" {
		t.Fatal("fixture: the session should start valid")
	}

	AuthDeleteUser(db, "alice")

	if _, ok := AuthValidateSession(db, token); ok {
		t.Error("a deleted user's session still validates")
	}
	// And it is gone from the store, not merely from the cache.
	var s authSession
	if db.Get(AuthSessionTable, sessionStoreKey(token), &s) {
		t.Error("the session record survived the delete")
	}
}

func TestDeleteUserRevokesAccessTokens(t *testing.T) {
	db := revokeFixture(t, "alice")
	tok := MintAccountToken("alice", "laptop")
	if owner, ok := lookupAccountTokenOwner(tok.Token); !ok || owner != "alice" {
		t.Fatal("fixture: the token should start valid")
	}

	AuthDeleteUser(db, "alice")

	if _, ok := lookupAccountTokenOwner(tok.Token); ok {
		t.Error("a deleted user's personal access token still resolves")
	}
}

func TestDeleteUserRevokesDesktopKeys(t *testing.T) {
	db := revokeFixture(t, "alice")
	key, ok := MintDesktopKey("alice")
	if !ok {
		t.Fatal("fixture: could not mint a desktop key")
	}
	if _, ok := LookupDesktopKey(key); !ok {
		t.Fatal("fixture: the key should start valid")
	}

	AuthDeleteUser(db, "alice")

	if _, ok := LookupDesktopKey(key); ok {
		t.Error("a deleted user's desktop key still resolves")
	}
}

func TestDeleteUserRevokesResetLinks(t *testing.T) {
	// A live reset link is a way back INTO the account, so it has to go with it.
	db := revokeFixture(t, "alice")
	token := createResetToken(db, "alice")
	if _, ok := validateResetToken(db, token); !ok {
		t.Fatal("fixture: the reset token should start valid")
	}

	AuthDeleteUser(db, "alice")

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
	db := revokeFixture(t, "alice")
	f := &fakeRevoker{keys: map[string]string{"s1": "alice", "s2": "dana"}}

	prev := revokers
	RegisterUserCredentialRevoker(f)
	t.Cleanup(func() { revokerMu.Lock(); revokers = prev; revokerMu.Unlock() })

	AuthDeleteUser(db, "alice")

	if _, still := f.keys["s1"]; still {
		t.Error("the app's credential for the deleted user survived")
	}
	if _, ok := f.keys["s2"]; !ok {
		t.Error("another user's credential was destroyed too")
	}
}

func TestRevokeReportsWhatItTook(t *testing.T) {
	db := revokeFixture(t, "alice")
	AuthCreateSession(db, "alice")
	MintAccountToken("alice", "laptop")

	counts := RevokeUserCredentials(db, "alice")
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
	db := revokeFixture(t, "alice")
	AuthSetUser(db, "dana", "pw", false)
	danaToken := AuthCreateSession(db, "dana")
	AuthCreateSession(db, "alice")

	first := RevokeUserCredentials(db, "alice")
	if first["sessions"] != 1 {
		t.Fatalf("first sweep: %v", first)
	}
	// Re-running must be safe — a partial failure should be re-runnable.
	if second := RevokeUserCredentials(db, "alice"); len(second) != 0 {
		t.Errorf("second sweep should find nothing, got %v", second)
	}
	if _, ok := AuthValidateSession(db, danaToken); !ok {
		t.Error("dana was signed out by craig's revocation")
	}
}

func TestRevokeSessionsKeepsTheAccount(t *testing.T) {
	// The "lost laptop" action: signed out everywhere, account intact.
	db := revokeFixture(t, "alice")
	token := AuthCreateSession(db, "alice")

	if n := AuthRevokeUserSessions(db, "alice"); n != 1 {
		t.Fatalf("expected 1 session revoked, got %d", n)
	}
	if _, ok := AuthValidateSession(db, token); ok {
		t.Error("the session survived")
	}
	if _, ok := AuthGetUser(db, "alice"); !ok {
		t.Error("the account should still exist")
	}
	// And they can sign back in.
	if !AuthCheckPassword(db, "alice", "pw") {
		t.Error("the account should still accept its password")
	}
}

func TestSessionForAVanishedUserDiesOnReload(t *testing.T) {
	// The backstop. Checked on the store-load path rather than per request, so
	// it costs nothing on a cache hit — which also means a restart clears out
	// any session whose user is gone.
	db := revokeFixture(t, "alice")
	token := AuthCreateSession(db, "alice")

	// Remove the user WITHOUT the revocation sweep, standing in for any path
	// that drops a user record directly.
	db.Unset(AuthTable, "user:"+"alice")
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
	db := revokeFixture(t, "alice")
	token := AuthCreateSession(db, "alice")

	user, _ := AuthGetUser(db, "alice")
	user.Pending = true
	db.Set(AuthTable, "user:"+"alice", user)
	sessionMu.Lock()
	delete(sessionCache, token)
	sessionMu.Unlock()

	if _, ok := AuthValidateSession(db, token); ok {
		t.Error("a session for a pending user still validates")
	}
}

// A session's token is never stored: the store holds its hash, so reading the
// store (or a backup of it) is not a login. A session written before that, under
// the raw token, keeps working and is moved to its hash on first use.
func TestSessionTokensAreStoredHashed(t *testing.T) {
	db := revokeFixture(t, "alice")
	token := AuthCreateSession(db, "alice")
	var s authSession
	if db.Get(AuthSessionTable, token, &s) {
		t.Fatal("the raw session token is a key in the store")
	}
	if !db.Get(AuthSessionTable, sessionStoreKey(token), &s) || s.User != "alice" {
		t.Fatal("the session is not stored under its hash")
	}
	if _, ok := AuthValidateSession(db, sessionStoreKey(token)); ok {
		t.Error("the stored hash signed somebody in")
	}

	// A pre-upgrade record under the raw token, not yet in the cache.
	legacy := "legacy-token-0123456789abcdef"
	db.Set(AuthSessionTable, legacy, authSession{User: "alice", Created: time.Now().Unix(), Expires: time.Now().Add(time.Hour).Unix()})
	if u, ok := AuthValidateSession(db, legacy); !ok || u != "alice" {
		t.Fatal("a session live across the upgrade stopped working")
	}
	if db.Get(AuthSessionTable, legacy, &s) {
		t.Error("the legacy raw-keyed record was left behind")
	}
	if !db.Get(AuthSessionTable, sessionStoreKey(legacy), &s) {
		t.Error("the legacy record was not moved to its hash")
	}
}

// Deleting an account clears what is filed under its name, so a new account
// created later with the same name starts empty. A longer name is untouched.
func TestDeletingAUserClearsWhatIsFiledUnderTheName(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	UserDB(RootDB, "bob").Set("agents", "a1", "mine")
	UserDB(RootDB, "bob").Sub("app").Set("records", "r1", "nested")
	UserDB(RootDB, "bob2").Set("agents", "a2", "not bob's")

	if n := dropUserScopes("bob"); n == 0 {
		t.Fatal("nothing was cleared")
	}
	var v string
	if UserDB(RootDB, "bob").Get("agents", "a1", &v) || UserDB(RootDB, "bob").Sub("app").Get("records", "r1", &v) {
		t.Error("bob's data outlived his account: a new bob would inherit it")
	}
	if !UserDB(RootDB, "bob2").Get("agents", "a2", &v) {
		t.Error("clearing bob took bob2's data with it")
	}
}

// Moving a deleted account's tools: a name the new owner already uses refuses
// the whole move with nothing changed; otherwise the tool arrives, and its
// release and the people who adopted it name the new owner.
func TestADeletedAccountsToolsSkillsAndCollectionsMove(t *testing.T) {
	saved, savedVec := RootDB, VectorDB
	RootDB, VectorDB = &DBase{Store: kvlite.MemStore()}, &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB, VectorDB = saved, savedVec })
	h := coreUserData{}

	RootDB.Set(persistentTempToolsTable, "bob", []PersistentTempTool{{Tool: TempTool{Name: "weather"}}, {Tool: TempTool{Name: "jokes"}}})
	RootDB.Set(persistentTempToolsTable, "alice", []PersistentTempTool{{Tool: TempTool{Name: "jokes"}}})
	if err := h.Reassign("bob", "alice"); err == nil {
		t.Fatal("a tool name the new owner already uses should refuse the move")
	}
	if len(LoadPersistentTempTools(nil, "bob")) != 2 {
		t.Fatal("a refused move changed something")
	}

	RootDB.Set(persistentTempToolsTable, "bob", []PersistentTempTool{{Tool: TempTool{Name: "weather"}}})
	RootDB.Set(toolReleasesTable, "weather", ToolRelease{Owner: "bob", Name: "weather"})
	RootDB.Set(adoptedGlobalToolsTable, "carol", []string{"weather" + adoptionOwnerSep + "bob"})
	RootDB.Set(skillsTable, "bob", []SkillRecord{{ID: "skill-1", Name: "Tone", Owner: "bob"}})
	UserDB(CollectionsDB(), "bob").Set(CollectionsTable, "c1", Collection{ID: "c1", Name: "Docs", Owner: "bob"})
	if s := h.Describe("bob"); s.Counts["tools"] != 1 || s.Counts["skills"] != 1 || s.Counts["collections"] != 1 {
		t.Fatalf("describe: %+v", s.Counts)
	}
	if err := h.Reassign("bob", "alice"); err != nil {
		t.Fatal(err)
	}
	if len(LoadPersistentTempTools(nil, "bob")) != 0 || len(LoadPersistentTempTools(nil, "alice")) != 2 {
		t.Error("the tool did not move")
	}
	var rel ToolRelease
	if RootDB.Get(toolReleasesTable, "weather", &rel); rel.Owner != "alice" {
		t.Errorf("the release still names %q", rel.Owner)
	}
	if pins := loadAdoptionPins(RootDB, "carol"); pins["weather"] != "alice" {
		t.Errorf("carol's adoption still points at %q", pins["weather"])
	}
	if sk := LoadSkills(nil, "alice"); len(sk) != 1 || sk[0].Owner != "alice" {
		t.Errorf("the skill did not move: %+v", sk)
	}
	var c Collection
	if !UserDB(CollectionsDB(), "alice").Get(CollectionsTable, "c1", &c) || c.Owner != "alice" {
		t.Error("the collection did not move")
	}
}

// What was shared WITH a deleted account is taken back from it, so a new
// account given the same name does not inherit it. A list where empty means
// everyone keeps a marker rather than opening up.
func TestADeletedAccountIsTakenOffWhatWasSharedWithIt(t *testing.T) {
	saved, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth })
	AuthSetUser(RootDB, "alice", "pw", false)
	AuthSetUser(RootDB, "bob", "pw", false)

	RootDB.Set(skillsTable, "alice", []SkillRecord{{ID: "s1", Owner: "alice", AllowedUsers: []string{"bob", "carol"}}})
	peershare.SetRecipients(RootDB, sharedSkillsTable, "alice", "s1", []string{"bob", "carol"})
	RootDB.Set(persistentTempToolsTable, "alice", []PersistentTempTool{{Tool: TempTool{Name: "w"}, SharedWith: []string{"bob"}, AllowedUsers: []string{"bob"}}})
	RootDB.Set(featureAccessTable, "openai_api", FeatureAccessPolicy{Feature: "openai_api", AllowedUsers: []string{"bob"}})

	forgetRecipient("bob")

	var skills []SkillRecord
	RootDB.Get(skillsTable, "alice", &skills)
	if len(skills[0].AllowedUsers) != 1 || skills[0].AllowedUsers[0] != "carol" {
		t.Errorf("skill share list: %v", skills[0].AllowedUsers)
	}
	if refs := peershare.List(RootDB, sharedSkillsTable, "bob"); len(refs) != 0 {
		t.Errorf("bob is still in the skills index: %+v", refs)
	}
	if refs := peershare.List(RootDB, sharedSkillsTable, "carol"); len(refs) != 1 {
		t.Error("carol's share went with bob's")
	}
	tools := LoadPersistentTempTools(nil, "alice")
	if len(tools[0].SharedWith) != 0 {
		t.Errorf("tool still shared with %v", tools[0].SharedWith)
	}
	if got := tools[0].AllowedUsers; len(got) != 1 || got[0] != removedUserMarker("bob") {
		t.Errorf("an adopt list of only bob should keep a marker, not open to everyone: %v", got)
	}
	var p FeatureAccessPolicy
	RootDB.Get(featureAccessTable, "openai_api", &p)
	if len(p.AllowedUsers) != 1 || p.AllowedUsers[0] == "bob" {
		t.Errorf("feature access: %v", p.AllowedUsers)
	}
}

// A move a handler would refuse is caught before anything moves: the core
// handler names the tool that clashes, and nothing is checked when nothing
// clashes.
func TestAMoveThatWouldClashIsCaughtFirst(t *testing.T) {
	revokeFixture(t, "bob")
	for _, owner := range []string{"bob", "carol"} {
		if err := AdminPersistTempTool(RootDB, owner, TempTool{Name: "weather", Description: "d", CommandTemplate: "echo w"}); err != nil {
			t.Fatal(err)
		}
	}
	problems := CheckUserDataReassign("bob", "carol")
	found := false
	for _, p := range problems {
		if strings.Contains(p, "tools, skills and collections: carol already has a tool named weather") {
			found = true
		}
	}
	if !found {
		t.Errorf("the clash was not caught: %v", problems)
	}
	if got := LoadPersistentTempTools(RootDB, "bob"); len(got) != 1 {
		t.Error("checking moved something")
	}
	if p := CheckUserDataReassign("bob", "dave"); len(p) != 0 {
		t.Errorf("a move with no clash was refused: %v", p)
	}
}

// A deleted account's pictures leave the disk (kept, recent, delivered), and
// its pending promotion requests leave the queue; another person's stay.
func TestDeleteUserRemovesPicturesAndPendingRequests(t *testing.T) {
	db := revokeFixture(t, "bob")
	prevImg := ImageDir()
	SetImageDir(t.TempDir())
	t.Cleanup(func() { SetImageDir(prevImg) })
	for _, owner := range []string{"bob", "carol"} {
		for _, sub := range []string{"kept", "recent", "delivered"} {
			dir := filepath.Join(ImageDir(), sub, safeRecentUser(owner), "x")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "a.png"), []byte("png"), 0o644)
		}
		if err := promotion.CreatePromotionRequest(db, owner, "app", "game", ""); err != nil {
			t.Fatal(err)
		}
	}
	AuthDeleteUser(db, "bob")
	for _, sub := range []string{"kept", "recent", "delivered"} {
		if _, err := os.Stat(filepath.Join(ImageDir(), sub, safeRecentUser("bob"))); err == nil {
			t.Errorf("bob's %s pictures are still on disk", sub)
		}
		if _, err := os.Stat(filepath.Join(ImageDir(), sub, safeRecentUser("carol"), "x", "a.png")); err != nil {
			t.Errorf("carol's %s pictures went too", sub)
		}
	}
	for _, req := range promotion.ListPromotionRequests(db, true) {
		if req.Owner == "bob" {
			t.Error("bob's pending request is still queued")
		}
	}
	if !promotion.PendingPromotion(db, "carol", "app", "game") {
		t.Error("carol's request went too")
	}
}
