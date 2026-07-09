// Reverse proxy + first-run gate. Three modes, picked per-request
// based on the live config:
//
//   1. No server URL configured → serve the configure HTML form
//      (configure.html embedded from frontend/). User picks a server,
//      JS calls window.go.main.App.SaveSettings, on success reloads.
//   2. URL configured but unreachable → ErrorHandler renders the
//      "couldn't reach gohort" page with a "Change server" button.
//   3. URL configured and reachable → forward upstream, streaming SSE
//      flush, WebSocket upgrade carried by httputil's default handler.
//
// The proxy reads the live ServerURL on every request so a
// SaveSettings or ResetSettings call takes effect on the next
// navigation — no app restart needed. The parsed *ReverseProxy is
// cached per URL string so we only re-parse when the URL changes.

package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// gohort_proxy is the AssetServer.Handler. Holds a cached parsed
// proxy keyed on the URL string — when the user changes server, the
// next ServeHTTP rebuilds and the old one falls out of cache. The
// cookie jar lives at this layer (not per-cached-proxy) so the same
// session survives URL changes if the user re-saves the same host.
type gohort_proxy struct {
	cfg            *core.Config
	configure_html []byte
	cookies        *core.PersistentCookieJar
	app            *App // for native dialogs (download save) — Wails runtime is absent on proxy pages

	mu           sync.Mutex
	cached_url   string
	cached_proxy *httputil.ReverseProxy
	// landing_done — sticky flag set after the first "/" request
	// bounces to the landing path (/orchestrate/). Subsequent "/"
	// requests pass through so the user can still navigate to the
	// dashboard, click "Home", etc. Resets on URL change (so a
	// fresh server gets a fresh landing) and on launch.
	landing_done bool
}

// new_gohort_proxy constructs the handler. configure_html is the raw
// configure-page bytes pulled from the embedded frontend FS — passing
// it in (instead of read-on-each-request) keeps the proxy package
// from depending on the embed. cookies is the proxy-side cookie jar
// (required because Wails' WKURLSchemeHandler doesn't process
// Set-Cookie response headers — see core/cookies.go for the why).
func new_gohort_proxy(cfg *core.Config, configure_html []byte, cookies *core.PersistentCookieJar, app *App) http.Handler {
	return &gohort_proxy{
		cfg:            cfg,
		configure_html: configure_html,
		cookies:        cookies,
		app:            app,
	}
}

// CONFIGURE_PATH is the reserved escape-hatch URL. Always intercepted
// by the proxy BEFORE any upstream routing, so the user can recover
// from "saved a URL that responds but isn't gohort" (a hostname
// behind a reverse proxy that returns 200 for everything, a typo
// landing on a real but wrong service, etc.) by typing it into the
// webview URL bar — no need to nuke settings.db from the filesystem.
const CONFIGURE_PATH = "/__desktop/configure"

// SETTINGS_PATH renders the SAME form as CONFIGURE_PATH but WITHOUT
// clearing the saved server URL — so the user can revise the server URL
// or the bridge API key without nuking their session. Saving with an
// unchanged URL keeps cookies (see App.SaveSettings).
const SETTINGS_PATH = "/__desktop/settings"

// APIKEY_PATH is a dedicated, self-contained page that updates ONLY the
// Gohort-Bridge API key (via App.SetBridgeAPIKey). No server URL, no
// reachability probe — so it can't get stuck on "Checking server…".
const APIKEY_PATH = "/__desktop/apikey"

// SAVE_PATH receives a file attachment (name + mime + base64) from popup_shim.js
// and writes it via a native save dialog. Loopback-only; the save dialog gates
// every write, so a page can't write to disk without the user picking a path.
const SAVE_PATH = "/__desktop/save"

// OPEN_PATH receives an external URL from popup_shim.js to open in the user's
// default browser. Like SAVE_PATH, it runs in Go because proxy-served pages
// can't reach the Wails Go-bridge (window.runtime is absent there).
const OPEN_PATH = "/__desktop/open"

// SAVE_SETTINGS_PATH / GET_SETTINGS_PATH back the configure form. The
// form is served from the loopback origin (see main.go), where Wails
// does NOT inject window.go — so the page can't call
// App.SaveSettings / App.GetSettings directly. It POSTs/GETs here
// instead and the proxy invokes the same App methods in Go. Same
// pattern as APIKEY_PATH; without it, clicking Connect threw a
// ReferenceError and wedged the form on "Checking server…".
const SAVE_SETTINGS_PATH = "/__desktop/savesettings"
const GET_SETTINGS_PATH = "/__desktop/getsettings"

// apikey_page_html is the standalone bridge-API-key editor. Kept inline
// (not a proxied gohort page, not the configure form) so updating the
// key is fully decoupled from the server-connection flow.
const apikey_page_html = `<!DOCTYPE html><html><head><meta charset="utf-8">
<title>Gohort-Bridge API Key</title><style>
html,body{height:100%;margin:0}body{background:#0d1117;color:#c9d1d9;
font-family:-apple-system,BlinkMacSystemFont,Segoe UI,Helvetica,sans-serif;
display:flex;align-items:center;justify-content:center}
.card{max-width:460px;width:90%;background:#161b22;border:1px solid #30363d;
border-radius:10px;padding:28px}h1{font-size:19px;margin:0 0 6px}
p{color:#8b949e;font-size:13px;line-height:1.5;margin:0 0 16px}
input{width:100%;box-sizing:border-box;padding:10px 12px;background:#0d1117;
border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:14px}
button{margin-top:14px;padding:9px 18px;background:#238636;color:#fff;border:0;
border-radius:6px;font-size:14px;cursor:pointer}button:hover{background:#2ea043}
#msg{margin-top:12px;font-size:13px;min-height:18px}
code{background:#0d1117;padding:1px 5px;border-radius:4px;color:#79c0ff}
</style></head><body><div class="card">
<h1>Gohort-Bridge API Key</h1>
<p>The key the Gohort-Bridge agent uses to authenticate with the server.
Generate one in <code>Phantom &rarr; API Keys</code>. Saved locally for the
bridge — no server connection needed, so this can't hang.</p>
<input id="key" type="password" placeholder="paste API key" autocomplete="off" spellcheck="false" autofocus>
<div id="msg"></div>
<button onclick="savekey()">Save Key</button>
</div><script>
// NB: uses a plain HTTP POST to the local proxy, NOT window.go — Wails
// doesn't inject its Go-bridge into proxy-served pages, so it's absent
// here. The proxy handles POST /__desktop/apikey in Go.
var key=document.getElementById('key'),msg=document.getElementById('msg');
key.addEventListener('keydown',function(e){if(e.key==='Enter')savekey();});
function savekey(){var k=key.value.trim();if(!k){msg.textContent='Enter a key.';return;}
msg.style.color='';msg.textContent='Saving…';
fetch('/__desktop/apikey',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({key:k})})
.then(function(r){return r.json();})
.then(function(d){if(!d.ok){msg.style.color='#f85149';msg.textContent=d.error||'Could not save.';return;}
msg.style.color='#3fb950';msg.textContent='Saved. The bridge picks it up automatically.';key.value='';})
.catch(function(e){msg.style.color='#f85149';msg.textContent='Failed: '+(e&&e.message?e.message:e);});}
</script></body></html>`

func (gp *gohort_proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Escape hatch — always wins, regardless of whether a server is
	// configured. Lets the user reach the configure form from any
	// state, including "saved URL responds but is the wrong service".
	if r.URL.Path == CONFIGURE_PATH {
		gp.clear_and_configure(w)
		return
	}
	// Non-destructive settings: show the same form, keep the saved URL.
	if r.URL.Path == SETTINGS_PATH {
		gp.serve_configure(w)
		return
	}
	// Dedicated bridge-API-key page. GET serves the form; POST writes
	// the key to the sidecar IN GO (the page can't use window.go — Wails
	// doesn't inject its bridge into proxy-served pages — so it POSTs
	// here instead). No server probe, so it never hangs on "Checking
	// server…".
	if r.URL.Path == APIKEY_PATH {
		if r.Method == http.MethodPost {
			gp.handle_apikey_post(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(apikey_page_html))
		return
	}
	// Download save — popup_shim.js POSTs a file attachment here (it can't use
	// window.go to reach a native save dialog: Wails doesn't inject its bridge
	// into proxy-served pages, AND WKWebView won't honor <a download> on a data:
	// URI). We pop the native save dialog in Go and write the bytes.
	if r.URL.Path == SAVE_PATH {
		gp.handle_save_post(w, r)
		return
	}
	// Open external link — popup_shim.js POSTs an external URL here so it opens
	// in the user's real browser. Same reason as the others: proxy-served pages
	// don't get the Wails Go-bridge, so window.runtime.BrowserOpenURL is absent;
	// we open it from the Go side instead.
	if r.URL.Path == OPEN_PATH {
		gp.handle_open_post(w, r)
		return
	}
	// Configure-form backends. The form lives on the loopback origin
	// where window.go is absent, so it fetch()es these instead of
	// calling the bound App methods directly.
	if r.URL.Path == GET_SETTINGS_PATH {
		gp.handle_getsettings(w, r)
		return
	}
	if r.URL.Path == SAVE_SETTINGS_PATH {
		gp.handle_savesettings_post(w, r)
		return
	}

	server_url := gp.cfg.ServerURL()
	if server_url == "" {
		gp.serve_configure(w)
		return
	}

	proxy := gp.proxy_for(server_url)
	if proxy == nil {
		// Saved value isn't a valid URL — fall through to configure
		// so the user can correct it. Shouldn't happen since
		// SaveSettings validates, but defensive.
		gp.serve_configure(w)
		return
	}

	// FIRST navigation to "/" → bounce to the landing path so the
	// user opens on the chat surface, not gohort's root. Subsequent
	// "/" requests pass straight through so the user can still
	// navigate to the dashboard, click "Home" links, etc. Used to
	// be an in-place URL rewrite, but that left the BROWSER URL
	// stuck at "/" while content was orchestrate — relative URLs
	// in the page (api/sessions, …) then 404'd against "/" instead
	// of "/orchestrate/". The HTML+JS bounce lets WKWebView's
	// normal page-load path update the URL bar (WKURLSchemeHandler
	// doesn't follow HTTP redirects natively, but JS-driven
	// navigation works fine).
	if !gp.landing_done && (r.URL.Path == "" || r.URL.Path == "/" || r.URL.Path == "/index.html") {
		gp.mu.Lock()
		gp.landing_done = true
		gp.mu.Unlock()
		landing := gp.cfg.LandingPath()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8">`+
			`<meta http-equiv="refresh" content="0;url=%s">`+
			`<title>Loading…</title></head>`+
			`<body><script>location.replace(%q);</script></body></html>`,
			html.EscapeString(landing), landing)
		return
	}

	// Wails sometimes synthesizes /wails/* probe paths that aren't
	// meant for the upstream. Reject locally to avoid 502 noise.
	if strings.HasPrefix(r.URL.Path, "/wails") {
		http.NotFound(w, r)
		return
	}

	proxy.ServeHTTP(w, r)
}

// proxy_for returns the cached reverse proxy for server_url, building
// a new one when the URL changes. Returns
// nil if the URL is malformed.
func (gp *gohort_proxy) proxy_for(server_url string) *httputil.ReverseProxy {
	gp.mu.Lock()
	defer gp.mu.Unlock()
	if gp.cached_proxy != nil && gp.cached_url == server_url {
		return gp.cached_proxy
	}
	parsed, err := url.Parse(server_url)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		core.Warn("[gohort-desktop] bad server URL %q: %v", server_url, err)
		return nil
	}

	proxy := httputil.NewSingleHostReverseProxy(parsed)
	original_director := proxy.Director
	host := parsed.Host
	upstream_url := parsed
	cookie_jar := gp.cookies
	proxy.Director = func(req *http.Request) {
		original_director(req)
		req.Host = host
		req.Header.Set("X-Forwarded-For-Desktop", "gohort-desktop")
		// Suppress the X-Forwarded-For this local proxy would otherwise
		// stamp on (= 127.0.0.1, the webview's loopback connection to us).
		// If that 127.0.0.1 reaches gohort, its "loopback = trusted
		// internal call, skip auth" bypass fires: logged-out requests
		// then hit the app directly and come back 401 instead of being
		// redirected to /login. Setting the header to nil tells
		// httputil.ReverseProxy to omit it, so the REAL reverse proxy in
		// front of gohort fills in the actual client IP.
		req.Header["X-Forwarded-For"] = nil
		// Inject cookies from our jar. Strip any Cookie header the
		// webview might have set (it normally can't, but if Wails ever
		// changes its scheme behavior we don't want two competing
		// sources) and write the canonical set ourselves.
		req.Header.Del("Cookie")
		for _, c := range cookie_jar.Cookies(upstream_url) {
			req.AddCookie(c)
		}
		// Tag the request as coming from the desktop client itself, with the
		// bridge API key. The server (core.DesktopClientUser) validates it to
		// expose from_client.* tools ONLY to this desktop app — a remote
		// browser/phone on the same account never carries it. Strip any value
		// the webview might have set first, then write our authentic key
		// (mirrors the Cookie handling above; the static X-Forwarded-For-
		// Desktop marker above is spoofable and NOT used for this).
		req.Header.Del("X-Gohort-Desktop-Client-Key")
		// Use the shared resolver (sidecar-first) — the SAME key the daemon
		// authenticates with, so the server's DesktopClientUser validates it.
		// Reading ReadBridgeConfig().APIKey directly here was the bug that
		// withheld from_client.* tools: the viewer stamped the stale manual
		// key while the auto-provisioned sidecar key was the live one.
		if k := core.BridgeAPIKey(); k != "" {
			req.Header.Set("X-Gohort-Desktop-Client-Key", k)
		}
	}
	// SSE / chunked: force per-write flush so the orchestrate chat
	// streams live instead of buffering.
	proxy.FlushInterval = -1
	// Three passes on the response:
	//   1. Capture Set-Cookie headers into the jar (the webview would
	//      drop them — see core/cookies.go).
	//   2. Convert any 3xx-with-Location into an HTML+JS bounce
	//      (WKURLSchemeHandler doesn't auto-follow HTTP redirects).
	//   3. Inject the popup shim into HTML responses so window.open
	//      and target="_blank" clicks route through Wails'
	//      BrowserOpenURL (WKURLSchemeHandler blocks real popups).
	// All have to happen before WriteHeader; ModifyResponse runs at
	// exactly the right point in the proxy lifecycle for that.
	proxy.ModifyResponse = func(resp *http.Response) error {
		if cookies := resp.Cookies(); len(cookies) > 0 {
			cookie_jar.SetCookies(upstream_url, cookies)
		}
		if err := rewrite_redirect_as_js(resp); err != nil {
			return err
		}
		return inject_popup_shim(resp)
	}
	display_url := server_url
	proxy.ErrorHandler = func(rw http.ResponseWriter, _ *http.Request, err error) {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(rw, GOHORT_NOT_RUNNING_HTML, display_url, display_url, err)
	}

	gp.cached_url = server_url
	gp.cached_proxy = proxy
	gp.landing_done = false
	return proxy
}

// handle_apikey_post writes the bridge API key to the sidecar from a
// local form POST. Runs entirely in Go — no Wails Go-bridge needed,
// which is the whole point (it's unavailable on proxy-served pages).
func (gp *gohort_proxy) handle_apikey_post(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	var req struct {
		Key string `json:"key"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	key := strings.TrimSpace(req.Key)
	if key == "" {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "Enter an API key."})
		return
	}
	bc := core.ReadBridgeConfig()
	bc.APIKey = key
	if err := core.WriteBridgeConfig(bc); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	core.Log("[gohort-desktop] bridge API key updated (local POST)")
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handle_getsettings returns the current visible settings (server URL +
// whether a bridge key is set) as JSON, so the configure form can
// prefill. Runs in Go because the loopback-origin page has no window.go
// to call App.GetSettings directly.
func (gp *gohort_proxy) handle_getsettings(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if gp.app == nil {
		json.NewEncoder(w).Encode(map[string]any{})
		return
	}
	json.NewEncoder(w).Encode(gp.app.GetSettings())
}

// handle_savesettings_post validates + persists the server URL (and
// optional bridge key) by invoking the SAME App.SaveSettings the Wails
// binding would — the form just can't reach it over window.go on the
// loopback origin, so it POSTs here. Returns {ok, error} exactly like
// the bound method, so the form's success/error handling is unchanged.
func (gp *gohort_proxy) handle_savesettings_post(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if gp.app == nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "desktop not ready"})
		return
	}
	var req struct {
		ServerURL string `json:"server_url"`
		APIKey    string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "bad request: " + err.Error()})
		return
	}
	res := gp.app.SaveSettings(req.ServerURL, req.APIKey)
	json.NewEncoder(w).Encode(map[string]any{"ok": res.OK, "error": res.Error})
}

// handle_save_post pops a native save dialog and writes a file attachment the
// shim handed over. Mirrors handle_apikey_post (plain POST, the page can't reach
// the Wails Go-bridge). Returns {ok, path?, error?} so the shim can toast.
func (gp *gohort_proxy) handle_save_post(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if gp.app == nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "desktop not ready"})
		return
	}
	var req struct {
		Name string `json:"name"`
		Mime string `json:"mime"`
		B64  string `json:"b64"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "bad request: " + err.Error()})
		return
	}
	res := gp.app.SaveAttachment(req.Name, req.Mime, req.B64)
	json.NewEncoder(w).Encode(map[string]any{"ok": res.OK, "path": res.Path, "error": res.Error})
}

// handle_open_post opens an external link in the user's default browser. The
// shim POSTs {url} here because window.runtime.BrowserOpenURL is unavailable on
// proxy-served pages. openURL validates the scheme (http/https only).
func (gp *gohort_proxy) handle_open_post(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if gp.app == nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "desktop not ready"})
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "bad request: " + err.Error()})
		return
	}
	if err := gp.app.openURL(req.URL); err != nil {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// serve_configure writes the embedded configure-page HTML. Status 200
// so the webview renders it instead of treating the body as an error
// document.
func (gp *gohort_proxy) serve_configure(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	w.Write(gp.configure_html)
}

// rewrite_redirect_as_js intercepts upstream 3xx-with-Location
// responses and replaces them with a 200 HTML body that performs the
// navigation via JavaScript. Required because Wails' WKURLSchemeHandler
// (custom URL scheme) doesn't follow HTTP redirects — it would render
// the redirect body literally. JS-driven navigation goes through
// WKWebView's normal loadRequest path, which works fine.
//
// Only triggered for 3xx with a Location header AND HTML/text content
// types (or no Content-Type). Skips redirects on API endpoints — the
// JS layer in orchestrate handles its own 3xx responses, and turning
// them into HTML would break those callers.
func rewrite_redirect_as_js(resp *http.Response) error {
	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return nil
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	// Only rewrite responses meant for a browser top-level navigation.
	// JSON / SSE / other API responses are passed through unchanged.
	if ct != "" && !strings.Contains(ct, "text/html") && !strings.Contains(ct, "text/plain") {
		return nil
	}

	// Two contexts to escape for: %s sits inside an HTML attribute
	// (meta-refresh URL), needs html.EscapeString; %q is a Go
	// string-literal format that produces a valid JS string literal
	// for any URL with no further encoding.
	body := fmt.Sprintf(redirect_bounce_html, html.EscapeString(loc), loc)
	new_body := []byte(body)

	// Drain & close the upstream body before swapping it out — keeps
	// the underlying connection reusable.
	if resp.Body != nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	resp.Body = io.NopCloser(bytes.NewReader(new_body))
	resp.ContentLength = int64(len(new_body))
	resp.Header.Set("Content-Type", "text/html; charset=utf-8")
	resp.Header.Set("Content-Length", strconv.Itoa(len(new_body)))
	resp.Header.Del("Location")
	// 200 lets WKWebView render the HTML; the JS inside performs the
	// actual navigation immediately on parse.
	resp.StatusCode = http.StatusOK
	resp.Status = "200 OK"
	return nil
}

// redirect_bounce_html is the JS-redirect body. location.replace
// (not href =) so the bounce page doesn't enter the back/forward
// history; the <noscript> path covers the (vanishingly unlikely)
// case of JS being disabled.
const redirect_bounce_html = `<!DOCTYPE html><html><head>` +
	`<meta charset="utf-8"><title>Redirecting…</title>` +
	`<meta http-equiv="refresh" content="0;url=%s">` +
	`</head><body><script>location.replace(%q);</script></body></html>`

// clear_and_configure wipes the saved server URL and renders the
// configure page. Invoked when the user navigates to CONFIGURE_PATH
// (escape hatch). Clearing the URL first means a refresh from the
// configure page won't bounce back into a broken upstream.
func (gp *gohort_proxy) clear_and_configure(w http.ResponseWriter) {
	if err := gp.cfg.ClearServerURL(); err != nil {
		core.Warn("[gohort-desktop] clear server URL on escape-hatch: %v", err)
	}
	// Drop the cached proxy so even if ClearServerURL failed, the
	// next request re-evaluates from scratch.
	gp.mu.Lock()
	gp.cached_url = ""
	gp.cached_proxy = nil
	gp.landing_done = false
	gp.mu.Unlock()
	core.Log("[gohort-desktop] escape-hatch hit %s — settings cleared", CONFIGURE_PATH)
	gp.serve_configure(w)
}

// inject_popup_shim prepends a small <script> into every HTML
// response that overrides window.open and intercepts target="_blank"
// anchor clicks. WKWebView's custom URL scheme handler (which Wails
// uses) silently blocks real popups — without this shim, every
// "Open in new tab" in gohort's UI is a dead click.
//
// The shim routes external URLs to Wails' BrowserOpenURL (the
// system browser). Same-origin URLs navigate the current webview as
// a fallback — losing chat state isn't ideal but beats no response.
// Anything fancier (sibling Wails windows that share the cookie
// jar) is Phase 2 work.
//
// Only injects into 200 text/html responses that look like full
// pages (contain "<head>"). AJAX fragments returned as text/html
// without a <head> are left alone.
func inject_popup_shim(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	idx := bytes.Index(body, []byte("<head>"))
	if idx < 0 {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}
	insertion := []byte("<head>" + popup_shim_script)
	new_body := make([]byte, 0, len(body)+len(popup_shim_script))
	new_body = append(new_body, body[:idx]...)
	new_body = append(new_body, insertion...)
	new_body = append(new_body, body[idx+len("<head>"):]...)
	resp.Body = io.NopCloser(bytes.NewReader(new_body))
	resp.ContentLength = int64(len(new_body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(new_body)))
	return nil
}

// popup_shim_script is injected right after <head> on every HTML
// page. Runs before any inline page scripts so the override is in
// place before window.open is first called.
//
// Routing rules:
//   - http://… / https://… URL → external → open in system browser
//     via Wails' BrowserOpenURL (no auth — cookie jar is desktop-side
//     and not shared with the user's regular browser).
//   - Anything else (relative paths like "/techwriter/?article=42",
//     scheme-less URLs, data: URLs) → internal → mount an iframe
//     overlay that loads through our reverse proxy. The parent
//     page (Agency, etc.) keeps running — its SSE / agent-loop
//     connections stay alive in the background, and the user can
//     dismiss the overlay (✕ or Esc) to return to it.
//
// Also injects a floating Refresh button at top-right of the
// webview. Cmd+R covers the keyboard path (via the Mac menu); the
// visible button is easier to discover.
//
//go:embed assets/popup_shim.js
var popup_shim_js string

// popup_shim_script wraps the embedded shim in a <script> element for
// injection. The JS lives in assets/popup_shim.js, NOT a Go string
// concat, so it is edited with real tooling, a syntax error is caught by
// node --check in proxy_test.go, and backticks are legal in the payload.
// A single missing semicolon in the old concatenated form once silently
// broke every popup (the IIFE failed to parse); the asset + syntax test
// exist to stop that recurring.
var popup_shim_script = "<script>" + popup_shim_js + "</script>"

// GOHORT_NOT_RUNNING_HTML renders when the configured URL exists but
// is unreachable. Includes a "Change server" button that calls
// ResetSettings via the Wails bridge and reloads — the most likely
// fix from this state is correcting the URL.
const GOHORT_NOT_RUNNING_HTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>gohort not reachable</title>
<style>
  html, body { margin: 0; padding: 0; height: 100%%; }
  body {
    font: 14px -apple-system, BlinkMacSystemFont, system-ui, sans-serif;
    background: #1a1a1a; color: #d0d0d0;
    display: flex; align-items: center; justify-content: center;
  }
  .card {
    max-width: 540px; padding: 2rem;
    background: #242424; border: 1px solid #404040; border-radius: 10px;
  }
  h1 { margin: 0 0 0.6rem; font-size: 1.05rem; font-weight: 600; color: #f5f5f5; }
  p { margin: 0.4rem 0; color: #b0b0b0; line-height: 1.5; }
  code {
    background: #1a1a1a; padding: 0.15rem 0.45rem; border-radius: 4px;
    font: 12px ui-monospace, Menlo, monospace; color: #d4d4d4;
  }
  .err { margin: 1rem 0; padding: 0.7rem; background: #1a1a1a;
    border-left: 3px solid #ff6b6b; font: 11px ui-monospace, Menlo, monospace;
    color: #999; word-break: break-all; }
  .actions { display: flex; gap: 0.6rem; margin-top: 1.2rem; }
  button {
    font: 13px -apple-system, BlinkMacSystemFont, system-ui, sans-serif;
    padding: 0.5rem 1rem; border-radius: 6px; cursor: pointer;
    border: 1px solid #555; background: #2e2e2e; color: #e0e0e0;
  }
  button.primary { background: #3a6ea5; border-color: #3a6ea5; color: white; }
  button:hover { filter: brightness(1.15); }
</style>
</head>
<body>
  <div class="card">
    <h1>Couldn't reach gohort</h1>
    <p>Tried to connect to <code>%s</code> but got no response.</p>
    <p>Either gohort serve isn't running at <code>%s</code>, or the URL
       points somewhere wrong.</p>
    <div class="err">%v</div>
    <div class="actions">
      <button class="primary" onclick="location.reload()">Retry</button>
      <button onclick="change_server()">Change server…</button>
    </div>
  </div>
<script>
function change_server() {
  // This page is served from the loopback origin, where window.go is
  // absent — so we can't call App.ResetSettings here. Navigate to the
  // escape-hatch path instead; the proxy clears the saved URL in Go and
  // serves the configure form.
  location.replace('/__desktop/configure');
}
</script>
</body>
</html>`
