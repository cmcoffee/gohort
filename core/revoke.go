// Taking a person's access away.
//
// Removing a user used to unset one record. Everything that authenticates AS
// that user outlived them: live sessions kept validating from the session table
// (and sliding renewal kept pushing their expiry out while they were in use),
// personal access tokens kept resolving through a validator that only maps a
// secret to an owner string, and the same was true of desktop keys, bridge
// keys, and per-user OAuth tokens. The account was gone from the user list and
// the credentials went on working.
//
// The peer subsystem already learned this lesson and wrote it down:
// SetPeerKeyDisabled revokes issued tokens because "a disabled key kept working
// for the life of its access token, which is the gap between revoked on screen
// and revoked in fact". This file closes the same gap for people.
//
// Two entry points, because they answer different questions. Deleting an
// account should take everything; "sign this person out everywhere" is a
// smaller, reversible thing an admin needs when a laptop goes missing and the
// account itself is fine.
package core

import (
	"sort"
	"strconv"
	"strings"
	"sync"
)

// UserCredentialRevoker lets an app destroy its own per-user credentials when
// an account is removed.
//
// It exists because core must not know that a bridge key is a thing. The
// alternative was for core to reach into every app's tables, which is the
// coupling the whole app/core split is there to prevent — and which would
// silently miss the next app to mint a credential. An app that issues
// something a user can authenticate with registers here, and deletion reaches
// it without core learning what it is.
//
// Deliberately separate from UserDataHandler. That registry is about CONTENT
// (articles, documents), it is admin-driven, and reassign/anonymize/purge are
// choices somebody makes. A credential is not a choice: an account that no
// longer exists must not have working keys, and nobody should have to remember
// to tick a box for that.
type UserCredentialRevoker interface {
	// CredentialKind names what is being revoked, for the audit line.
	// Plural and lowercase, e.g. "bridge keys".
	CredentialKind() string
	// RevokeUserCredentials destroys every credential this app holds for
	// user and returns how many it removed.
	RevokeUserCredentials(user string) int
}

var (
	revokerMu sync.Mutex
	revokers  []UserCredentialRevoker
)

// RegisterUserCredentialRevoker adds an app's credential revoker. Called from
// an app's route registration, once its store is live.
func RegisterUserCredentialRevoker(r UserCredentialRevoker) {
	if r == nil {
		return
	}
	revokerMu.Lock()
	revokers = append(revokers, r)
	revokerMu.Unlock()
}

// registeredRevokers returns the revokers in a stable order.
func registeredRevokers() []UserCredentialRevoker {
	revokerMu.Lock()
	defer revokerMu.Unlock()
	out := make([]UserCredentialRevoker, len(revokers))
	copy(out, revokers)
	sort.Slice(out, func(i, j int) bool { return out[i].CredentialKind() < out[j].CredentialKind() })
	return out
}

// AuthRevokeUserSessions ends every login session belonging to user and returns
// how many it ended. The account itself is untouched, so the person can sign
// back in — this is the "that laptop is gone, sign me out everywhere" action,
// not a punishment.
//
// Clears the in-memory cache as well as the stored record. The cache is what
// AuthValidateSession consults first, so a store-only sweep would leave a
// revoked session working until its entry happened to expire.
func AuthRevokeUserSessions(db Database, user string) int {
	user = strings.TrimSpace(user)
	if db == nil || user == "" {
		return 0
	}
	n := 0
	for _, token := range db.Keys(AuthSessionTable) {
		var s authSession
		if db.Get(AuthSessionTable, token, &s) && s.User == user {
			db.Unset(AuthSessionTable, token)
			n++
		}
	}
	// Sweep the cache by value rather than by the tokens just collected: an
	// entry can be cached without a matching store record (a concurrent
	// delete), and leaving that one behind is exactly the case this is for.
	sessionMu.Lock()
	for token, s := range sessionCache {
		if s != nil && s.User == user {
			delete(sessionCache, token)
		}
	}
	sessionMu.Unlock()
	return n
}

// revokeUserResetTokens drops any outstanding password-reset links for user.
//
// A live reset token is a way back INTO the account, so leaving one behind
// while removing the account is the same mistake in a quieter form.
func revokeUserResetTokens(db Database, user string) int {
	if db == nil || user == "" {
		return 0
	}
	n := 0
	for _, token := range db.Keys(AuthResetTable) {
		var rt resetToken
		if db.Get(AuthResetTable, token, &rt) && rt.Username == user {
			db.Unset(AuthResetTable, token)
			n++
		}
	}
	return n
}

// revokeUserAccountTokens drops every personal access token owned by user.
// These are the keys pasted into external clients, and they authenticate
// through a validator that never asks whether the owner still exists.
func revokeUserAccountTokens(user string) int {
	if RootDB == nil || user == "" {
		return 0
	}
	n := 0
	for _, secret := range RootDB.Keys(accountTokenTable) {
		var t AccountToken
		if RootDB.Get(accountTokenTable, secret, &t) && t.Owner == user {
			RootDB.Unset(accountTokenTable, secret)
			n++
		}
	}
	return n
}

// revokeUserDesktopKeys drops the user's desktop bridge credentials.
func revokeUserDesktopKeys(user string) int {
	if RootDB == nil || user == "" {
		return 0
	}
	n := 0
	for _, id := range RootDB.Keys(desktopKeyTable) {
		var dk DesktopKey
		if RootDB.Get(desktopKeyTable, id, &dk) && dk.Owner == user {
			RootDB.Unset(desktopKeyTable, id)
			n++
		}
	}
	return n
}

// RevokeUserCredentials destroys every credential that authenticates as user,
// across core's own stores and every registered app revoker. Returns a count
// per kind, for the caller's audit line.
//
// Safe to call for a user who has none of these; every sweep is a no-op on an
// empty result. Idempotent, so a partial failure can simply be re-run.
func RevokeUserCredentials(db Database, user string) map[string]int {
	user = strings.TrimSpace(user)
	out := map[string]int{}
	if user == "" {
		return out
	}
	if n := AuthRevokeUserSessions(db, user); n > 0 {
		out["sessions"] = n
	}
	if n := revokeUserResetTokens(db, user); n > 0 {
		out["reset tokens"] = n
	}
	if n := revokeUserAccountTokens(user); n > 0 {
		out["access tokens"] = n
	}
	if n := revokeUserDesktopKeys(user); n > 0 {
		out["desktop keys"] = n
	}
	if n := Secure().RevokeUserTokens(user); n > 0 {
		out["connected accounts"] = n
	}
	for _, r := range registeredRevokers() {
		if n := r.RevokeUserCredentials(user); n > 0 {
			out[r.CredentialKind()] = n
		}
	}
	return out
}

// FormatRevocation renders a revocation result for a log line, in a stable
// order. Empty when nothing was revoked, so a caller can skip the line.
func FormatRevocation(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, strconv.Itoa(counts[k])+" "+k)
	}
	return strings.Join(parts, ", ")
}
