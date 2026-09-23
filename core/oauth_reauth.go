// Recovering from a rejected OAuth token instead of asking the user to
// reconnect.
//
// Both per-user OAuth surfaces — SecureAPI authorization_code credentials and
// the MCP client's per-user tokens — refreshed on a CLOCK and nothing else. Two
// gaps followed from that, and they compound:
//
//   - A provider that returns no expires_in leaves Expiry zero, which both
//     freshness checks read as "good forever". Such a token is never refreshed,
//     so it is used until the provider stops honouring it and then used some
//     more.
//   - When a refresh did fail, both fell back to returning the stale access
//     token and left it stored. Every later call repeated that, so one bad
//     moment wedged the credential until a human reconnected it.
//
// The server is the authority on whether a token is still good, and it says so
// with a 401. So a 401 now INVALIDATES the stored access token while keeping
// the refresh token, and the next call mints a fresh one. The call that got the
// 401 still fails — nothing is replayed, so streaming uploads and non-idempotent
// requests are untouched — but the credential heals itself instead of dead-ending
// on a re-authorization the user did not actually need.
//
// The one case that IS terminal stays terminal: invalid_grant means the refresh
// token itself is dead (revoked, expired, or already rotated away), and no
// number of retries brings it back. That clears the record and asks for a real
// reconnect, which is the honest answer there.
package core

import (
	"context"
	"strings"
	"sync"
	"time"
)

// --- one prompt per dead credential, not one per tool call ----------------
//
// A turn that calls six tools through the same credential used to produce six
// identical "reconnect this on your Account page" failures: each call resolved
// the credential on its own, found the same dead token, and reported it. The
// model read each as a fresh failure and retried around them, so the count was
// usually worse than the number of tools.
//
// So the FIRST call to find it dead announces, and every other call for that
// credential waits on the announcement instead of making its own. When the
// reconnect lands the waiters are released and finish the work they were
// already doing - which is the difference between "six errors and a dead turn"
// and "one prompt and a pause".
//
// Keyed on (user, credential), because a credential is per-user here: one
// person reconnecting theirs says nothing about anybody else's.
//
// The waiters have a deadline. A human is being asked to leave the page, visit
// a provider and come back, and a tool call cannot hold a turn open
// indefinitely waiting for that - so a wait that runs out reports that a
// reconnect is already pending and tells the model not to ask again, which is
// the other half of not producing six prompts.

// reauthWaitWindow is how long a tool call will hold for a reconnect that
// somebody has already been asked to do.
//
// Long enough to cover actually doing it - a provider login with an MFA prompt
// in it - and short enough that a turn nobody is attending to ends rather than
// hanging. A wait that expires is not a failure of the barrier: the
// announcement stands, and the next call joins the same one.
const reauthWaitWindow = 3 * time.Minute

type reauthPending struct {
	done chan struct{}
	at   time.Time
}

var (
	reauthMu      sync.Mutex
	reauthWaiting = map[string]*reauthPending{}
)

func reauthKey(user, cred string) string { return user + "\x00" + cred }

// ReauthAnnounce marks a credential as awaiting reconnection and reports
// whether THIS caller is the one that should say so.
//
// True exactly once per outstanding reconnect. The caller that gets it returns
// the user-facing message; everyone else calls ReauthWait instead.
func ReauthAnnounce(user, cred string) bool {
	if strings.TrimSpace(user) == "" || strings.TrimSpace(cred) == "" {
		return true // nothing to coalesce on; behave as before
	}
	reauthMu.Lock()
	defer reauthMu.Unlock()
	k := reauthKey(user, cred)
	if p, ok := reauthWaiting[k]; ok && time.Since(p.at) < reauthWaitWindow {
		return false
	}
	// A pending entry older than the window is stale - the person never came
	// back, or the process missed the completion - and a stale entry that
	// never expires would silence the prompt forever. Replacing it lets the
	// next turn ask again.
	reauthWaiting[k] = &reauthPending{done: make(chan struct{}), at: time.Now()}
	return true
}

// ReauthWait blocks until the pending reconnect for this credential completes,
// the window closes, or the context ends. Reports whether it completed.
func ReauthWait(ctx context.Context, user, cred string) bool {
	reauthMu.Lock()
	p := reauthWaiting[reauthKey(user, cred)]
	reauthMu.Unlock()
	if p == nil {
		return false
	}
	left := reauthWaitWindow - time.Since(p.at)
	if left <= 0 {
		return false
	}
	t := time.NewTimer(left)
	defer t.Stop()
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-p.done:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// ReauthPending reports whether somebody has already been asked.
func ReauthPending(user, cred string) bool {
	reauthMu.Lock()
	defer reauthMu.Unlock()
	p, ok := reauthWaiting[reauthKey(user, cred)]
	return ok && time.Since(p.at) < reauthWaitWindow
}

// ReauthResolved releases every waiter for this credential.
//
// Called where a new token is STORED, which is the only event that means the
// person actually finished - not where the browser was sent to the provider,
// which means only that they were asked.
func ReauthResolved(user, cred string) {
	reauthMu.Lock()
	p, ok := reauthWaiting[reauthKey(user, cred)]
	delete(reauthWaiting, reauthKey(user, cred))
	reauthMu.Unlock()
	if ok {
		close(p.done)
	}
}

// oauthGrantRejected reports whether a token-endpoint failure means the REFRESH
// TOKEN is finished, as opposed to the attempt having failed.
//
// The distinction decides whether the user has to do anything. A network blip,
// a 500, a timeout — those leave a perfectly good refresh token and deserve
// another attempt on the next call. invalid_grant does not: RFC 6749 uses it for
// a grant that is expired, revoked, or already redeemed, and every later attempt
// gets the same answer.
//
// Matched on the error text because that is what both call paths have: the token
// request helpers already fold the endpoint's error body into the returned
// error, and neither surfaces a typed OAuth error to key off.
func oauthGrantRejected(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{"invalid_grant", "invalid_token", "unauthorized_client"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
