// Shared knowledge: domains whose plain-HTTP responses are useless
// (anti-bot 403 / JS-skeleton / login wall) and which both fetch_url
// and the sandbox-hook gohort.fetch need to auto-route through a real
// browser (browse_page / headless Chromium).
//
// Lives here in core/ so the script-side fetch and the LLM-side
// fetch_url consult the SAME list. Previously fetch_url owned its
// own copy and silently routed Reddit URLs through Chromium, while
// gohort.fetch went straight to plain HTTP and got 403'd — giving
// the LLM the false signal that "fetch_url works but the same URL
// in a script doesn't." Same list, same behavior, one place to
// update when a new domain joins the JS-heavy club.

package core

import "strings"

// jsHeavyDomains lists base domains (www. stripped) that need a
// real browser. Plain HTTP fetches against these return either a
// JS-only skeleton, a 403/captcha, or a login wall.
var jsHeavyDomains = map[string]bool{
	"reddit.com":    true,
	"linkedin.com":  true,
	"x.com":         true,
	"twitter.com":   true,
	"instagram.com": true,
	"facebook.com":  true,
	"tiktok.com":    true,
}

// IsJSHeavyDomain reports whether the given hostname is in the
// JS-heavy list (case-insensitive, www-stripped). Used by both
// fetch_url and the sandbox-hook fetch to route plain-HTTP-hostile
// URLs through browse_page automatically.
//
// API subdomains (api.*, oauth.*) are excluded — those endpoints
// serve real JSON/HTTP and routing them through Chromium gives the
// caller a pretty-printed rendered-text view, NOT parseable data.
func IsJSHeavyDomain(host string) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	if strings.HasPrefix(host, "api.") || strings.HasPrefix(host, "oauth.") {
		return false
	}
	return jsHeavyDomains[host]
}

// ShouldAutoBrowseURL is the URL-aware variant of IsJSHeavyDomain.
// Same host check, with a few path exclusions for endpoints that are
// CLEARLY API surfaces (and therefore serve usable data over plain
// HTTP) even on a JS-heavy host.
//
// Important non-exclusion: content extensions like .json, .xml, .rss.
// Sites like Reddit serve their SPA's content at /r/<sub>.json — the
// extension is a content-type hint on the SAME SPA infrastructure
// that needs a real browser, not an indication of a clean public API.
// Plain HTTP to reddit.com/r/<sub>.json gets 403'd by the anti-bot
// just like the HTML view does; both need Chromium to succeed.
//
// Excluded (genuinely-API patterns):
//   - api.* / oauth.* subdomains (host-level — see IsJSHeavyDomain)
//   - /api/ /v1/ /v2/ /v3/ /v4/ /rest/ /graphql /oauth/ path segments
//
// Returns false on parse failure (caller defaults to plain HTTP).
func ShouldAutoBrowseURL(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	lower := strings.ToLower(rawURL)
	host := ""
	if i := strings.Index(lower, "://"); i >= 0 {
		rest := lower[i+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			host = rest[:slash]
		} else {
			host = rest
		}
	}
	if at := strings.IndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if colon := strings.IndexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	if !IsJSHeavyDomain(host) {
		return false
	}
	path := ""
	if i := strings.Index(lower, "://"); i >= 0 {
		rest := lower[i+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			path = rest[slash:]
		}
	}
	if q := strings.IndexAny(path, "?#"); q >= 0 {
		path = path[:q]
	}
	// API path markers — conventional "this is a real API, not the SPA"
	// signals. Content-extension hints (.json/.xml/.rss) are NOT here
	// because JS-heavy sites serve content-typed views of their SPA at
	// those paths; routing through Chromium is still the right move.
	if strings.Contains(path, "/api/") || strings.HasPrefix(path, "/api/") ||
		strings.Contains(path, "/v1/") || strings.Contains(path, "/v2/") ||
		strings.Contains(path, "/v3/") || strings.Contains(path, "/v4/") ||
		strings.Contains(path, "/rest/") || strings.Contains(path, "/graphql") ||
		strings.Contains(path, "/oauth/") {
		return false
	}
	return true
}
