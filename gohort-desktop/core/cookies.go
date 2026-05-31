// Proxy-side cookie management. Required because Wails' WKWebView on
// macOS reaches the AssetServer via a custom URL scheme handler
// (WKURLSchemeHandler), which does NOT process Set-Cookie response
// headers the way regular https:// requests do. If we relied on the
// webview to manage cookies, login would set a session cookie that
// the webview immediately discards, causing the next request to be
// bounced back to /login — the loop the user hit.
//
// Solution: the desktop maintains its own cookie jar on behalf of
// the webview. The reverse proxy captures every Set-Cookie response
// header from upstream into this jar, and injects matching Cookie
// headers on every upstream request. The webview is effectively
// cookieless; the proxy is the session-bearing client.
//
// Cookies persist via the kvlite settings DB so login survives app
// restarts. The jar is cleared when the user changes server URL —
// stale cookies for a different host would just bounce back to
// /login anyway, and clearing avoids any cross-site leakage.

package core

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
	"time"
)

// PersistentCookieJar wraps net/http/cookiejar with kvlite-backed
// persistence. Concurrent-safe — both the jar interaction and the
// disk persistence are serialized through the embedded mutex.
type PersistentCookieJar struct {
	mu       sync.Mutex
	jar      *cookiejar.Jar
	settings *Settings
}

// NewPersistentCookieJar constructs the jar, loading any previously
// persisted cookies. Returns an error only if cookiejar.New itself
// fails (which it can't with a nil options arg — kept for symmetry).
func NewPersistentCookieJar(settings *Settings) (*PersistentCookieJar, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	p := &PersistentCookieJar{jar: jar, settings: settings}
	p.load_from_disk()
	return p, nil
}

// SetCookies stores cookies from a response. Updates both the
// in-memory jar AND the on-disk record so a restart preserves login.
func (p *PersistentCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jar.SetCookies(u, cookies)
	// Persist what we just received. cookiejar's stored cookies aren't
	// queryable in full form (Cookies(u) only returns Name+Value), so
	// we save exactly what came in — domain, path, expiry, all of it.
	if err := p.settings.AppendCookies(u.String(), cookies); err != nil {
		Warn("[gohort-desktop] persist cookies: %v", err)
	}
}

// Cookies returns the cookies the jar would send for a request URL.
// Returned cookies have only Name+Value populated (cookiejar's
// contract). That's fine for attaching to a Cookie header.
func (p *PersistentCookieJar) Cookies(u *url.URL) []*http.Cookie {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jar.Cookies(u)
}

// Clear wipes the jar in memory and on disk. Called when the user
// changes server URL (different host → different identity → don't
// carry old cookies over).
func (p *PersistentCookieJar) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	jar, _ := cookiejar.New(nil)
	p.jar = jar
	if err := p.settings.ClearCookies(); err != nil {
		Warn("[gohort-desktop] clear cookies: %v", err)
	}
}

// load_from_disk replays persisted cookies into a fresh jar. Drops
// any cookies that have already expired so the jar starts clean.
func (p *PersistentCookieJar) load_from_disk() {
	saved := p.settings.LoadCookies()
	now := time.Now()
	by_url := map[string][]*http.Cookie{}
	for _, sc := range saved {
		if sc.Cookie == nil {
			continue
		}
		if !sc.Cookie.Expires.IsZero() && sc.Cookie.Expires.Before(now) {
			continue
		}
		by_url[sc.URL] = append(by_url[sc.URL], sc.Cookie)
	}
	for u_str, cookies := range by_url {
		u, err := url.Parse(u_str)
		if err != nil {
			continue
		}
		p.jar.SetCookies(u, cookies)
	}
	if n := total_cookies(by_url); n > 0 {
		Debug("[gohort-desktop] restored %d cookie(s) from disk", n)
	}
}

func total_cookies(m map[string][]*http.Cookie) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}
