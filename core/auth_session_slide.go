// Keeping an ACTIVE session alive.
//
// A session was stamped with an expiry once, at login, and never moved. So the
// clock ran against wall time rather than against use: someone working in
// gohort all week was logged out mid-task on the seventh day, and the only
// signal was the login page appearing over whatever they were doing. Nothing
// was wrong with the session and nothing had gone stale — the deadline simply
// arrived. Re-authenticating changed nothing except restarting the same timer.
//
// The fix is the ordinary one: slide the window while the session is being
// used. Each request past the halfway mark pushes the expiry back out to a full
// duration from now, so a session in daily use never expires, and one that goes
// quiet still lapses on schedule — which is the property the expiry existed for.
//
// Two things keep that from becoming "sessions live forever":
//
//   - An ABSOLUTE cap measured from Created, which sliding cannot cross. A
//     stolen cookie is renewable by the thief exactly as it is by the owner, so
//     without a ceiling the sliding window hands out an indefinite credential.
//     The cap is what still forces a real re-authentication eventually.
//   - Renewal only past the halfway mark, so this costs one write per half
//     window per session rather than one per request.
//
// Renewal lives on the ONE path that both sees every browser request and can
// write a cookie — the auth middleware. AuthValidateSession stays a pure read,
// so the many places that merely ask "who is this" (AuthCurrentUser, AuthIsAdmin,
// per-request permission checks) cannot silently extend anyone's credential.
package core

import (
	"net/http"
	"time"
)

// AuthSessionAbsoluteDays returns the maximum age a session may reach through
// renewal, in days, measured from when it was created. <= 0 disables the cap.
// Wired by the host alongside AuthSessionDays; nil means the default.
var AuthSessionAbsoluteDays func() int

// DefaultSessionAbsoluteDays is the ceiling when the host wires nothing.
// Generous, because its job is to bound a stolen cookie's usefulness, not to
// log a working user out — that was the behavior this replaced.
//
// Exported so the host can return it for an UNSET setting: a hook returning 0
// means the operator chose "no ceiling", which is a different answer from
// having chosen nothing, and the two must not collapse into each other.
const DefaultSessionAbsoluteDays = 90

// sessionAbsoluteDuration is the cap sliding cannot cross. Zero means no cap.
func sessionAbsoluteDuration() time.Duration {
	days := DefaultSessionAbsoluteDays
	if AuthSessionAbsoluteDays != nil {
		days = AuthSessionAbsoluteDays()
	}
	if days <= 0 {
		return 0
	}
	abs := time.Duration(days) * 24 * time.Hour
	// A cap SHORTER than one window would expire sessions earlier than the
	// setting they were issued under, which is the opposite of this file's
	// purpose and reads as the sliding window making things worse.
	if d := sessionDuration(); abs < d {
		return d
	}
	return abs
}

// renewedExpiry returns the expiry a session should carry after this request,
// and whether that is a change worth persisting.
//
// Renews only past the halfway mark of the window. Before that the session has
// more than half its life left and rewriting it would buy nothing but a
// database write on every request.
func renewedExpiry(sess authSession, now time.Time) (int64, bool) {
	window := sessionDuration()
	if window <= 0 {
		return sess.Expires, false
	}
	if now.Add(window/2).Unix() < sess.Expires {
		return sess.Expires, false // more than half the window left
	}
	next := now.Add(window)
	if abs := sessionAbsoluteDuration(); abs > 0 {
		if ceiling := time.Unix(sess.Created, 0).Add(abs); next.After(ceiling) {
			next = ceiling
		}
	}
	if next.Unix() <= sess.Expires {
		return sess.Expires, false // at the absolute ceiling; nothing to extend
	}
	return next.Unix(), true
}

// AuthValidateSessionSliding validates a session token and, when it has passed
// its renewal point, extends it and re-stamps the browser cookie.
//
// The cookie has to be re-stamped too. Its MaxAge is the browser's own copy of
// the deadline, and a server-side expiry the browser has already discarded is a
// session that ends at exactly the moment this code exists to prevent.
//
// Falls back to plain validation whenever it cannot renew — no writer, no
// cookie, an unknown token — so this is never the reason a request fails.
func AuthValidateSessionSliding(db Database, w http.ResponseWriter, token string) (string, bool) {
	user, ok := AuthValidateSession(db, token)
	if !ok || w == nil || db == nil {
		return user, ok
	}
	sess, found := loadAuthSession(db, token)
	if !found {
		return user, ok
	}
	now := time.Now()
	next, renew := renewedExpiry(sess, now)
	if !renew {
		return user, ok
	}
	sess.Expires = next
	saveAuthSession(db, token, sess)
	http.SetCookie(w, &http.Cookie{
		Name:     auth_cookie_name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   TLSEnabled(),
		// Seconds REMAINING, not the full window: at the absolute ceiling the
		// two differ, and handing the browser the full window there would
		// leave it holding a cookie the server has already stopped honouring.
		MaxAge: int(time.Until(time.Unix(next, 0)).Seconds()),
	})
	return user, ok
}

// loadAuthSession reads a session record, preferring the cache. Returns a COPY:
// the cache holds pointers shared across requests, and mutating one in place
// would edit another request's view of it without the lock.
func loadAuthSession(db Database, token string) (authSession, bool) {
	sessionMu.RLock()
	cached, ok := sessionCache[token]
	sessionMu.RUnlock()
	if ok && cached != nil {
		return *cached, true
	}
	var sess authSession
	if !db.Get(AuthSessionTable, token, &sess) {
		return authSession{}, false
	}
	return sess, true
}

// saveAuthSession persists a session record and refreshes the cache entry.
// A NEW pointer replaces the cached one rather than the old one being written
// through, so a concurrent reader holding the previous pointer keeps reading a
// consistent record instead of a half-updated one.
func saveAuthSession(db Database, token string, sess authSession) {
	db.Set(AuthSessionTable, token, sess)
	sessionMu.Lock()
	sessionCache[token] = &sess
	sessionMu.Unlock()
}
