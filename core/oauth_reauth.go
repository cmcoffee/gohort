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

import "strings"

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
