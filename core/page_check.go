package core

// Headless page verification — the bridge that lets an authoring flow
// (Builder's app_def action=verify) load a dashboard page in the real
// headless browser AS a logged-in user and read back what actually
// happened client-side: console errors, uncaught exceptions, failed
// requests, and a caller-supplied DOM probe. The browser half lives in
// tools/browser (rod); it wires BrowserCheckPage at init, the same
// pattern as BrowserFetchFunc. Keeping the session mint here means the
// auth cookie's name and lifecycle never leave this package.

import (
	"errors"
	"net"
	"strings"
)

// PageCheckCookie is one cookie handed to the headless browser for a
// page check — in practice the freshly minted auth session.
type PageCheckCookie struct {
	Name     string
	Value    string
	Domain   string
	Path     string
	Secure   bool
	HTTPOnly bool
}

// PageRequest is one network response the page received while loading —
// the evidence trail for "did the page actually call its data
// endpoints, and what came back".
type PageRequest struct {
	URL    string
	Status int
}

// PageCheckReport is what the headless browser observed while loading a
// page: the client-side failures a plain HTTP fetch can never see.
type PageCheckReport struct {
	// ConsoleErrors are console.error emissions (first N).
	ConsoleErrors []string
	// PageErrors are uncaught JS exceptions.
	PageErrors []string
	// FailedRequests are network responses with status >= 400,
	// formatted "URL → HTTP nnn".
	FailedRequests []string
	// Requests is every network response (URL + status, first N) — lets
	// the caller confirm a specific endpoint WAS fetched, not just that
	// nothing failed.
	Requests []PageRequest
	// PendingRequests are URLs the page requested whose responses had
	// still not arrived when the report was snapshotted. Distinguishes
	// "the page never called this endpoint" (a wiring bug) from "the
	// endpoint was called but is slow" (a latency problem) — a check
	// that only sees responses conflates the two.
	PendingRequests []string
	// ProbeJSON is the string returned by the caller's probe script
	// (run after the page settles), "" when no probe was supplied.
	ProbeJSON string
	// BodyText is the rendered document.body.innerText, trimmed.
	BodyText string
}

// BrowserCheckPage is wired by tools/browser at init. Loads target in
// the headless browser with the given cookies, waits for the page to
// settle, runs probeJS (a JS function-expression returning a string),
// and reports what happened. insecureTLS accepts the local self-signed
// certificate — set only for loopback targets.
var BrowserCheckPage func(target string, cookies []PageCheckCookie, probeJS string, insecureTLS bool) (*PageCheckReport, error)

// localWebBase builds a loopback base URL for the running dashboard.
// The listen address may be wildcard ("0.0.0.0:8181", ":8181") — a
// browser can't navigate a wildcard host, so it collapses to 127.0.0.1.
func localWebBase() (base, host string, err error) {
	if strings.TrimSpace(WebListenAddr) == "" {
		return "", "", errors.New("web dashboard is not running")
	}
	h, port, splitErr := net.SplitHostPort(WebListenAddr)
	if splitErr != nil {
		h, port = WebListenAddr, ""
	}
	if h == "" || h == "0.0.0.0" || h == "::" {
		h = "127.0.0.1"
	}
	scheme := "http"
	if TLSEnabled() {
		scheme = "https"
	}
	hostPort := h
	if port != "" {
		hostPort = net.JoinHostPort(h, port)
	}
	return scheme + "://" + hostPort, h, nil
}

// CheckPageAsUser loads a same-origin dashboard path in the headless
// browser authenticated as username, and reports the client-side result.
// A session is minted just for this load and destroyed on return, so
// the check sees exactly what that user would see — per-user apps,
// group gating, frozen-seed shadows — without borrowing a live session.
func CheckPageAsUser(db Database, username, path, probeJS string) (*PageCheckReport, error) {
	if BrowserCheckPage == nil {
		return nil, errors.New("headless browser is not available in this build")
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("path must be a same-origin path starting with \"/\"")
	}
	base, host, err := localWebBase()
	if err != nil {
		return nil, err
	}
	token := AuthCreateSession(db, username)
	defer AuthDestroySession(db, token)
	cookie := PageCheckCookie{
		Name:     auth_cookie_name,
		Value:    token,
		Domain:   host,
		Path:     "/",
		Secure:   TLSEnabled(),
		HTTPOnly: true,
	}
	// The local cert is self-signed; only this loopback load trusts it.
	return BrowserCheckPage(base+path, []PageCheckCookie{cookie}, probeJS, TLSEnabled())
}
