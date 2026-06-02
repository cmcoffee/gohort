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
func new_gohort_proxy(cfg *core.Config, configure_html []byte, cookies *core.PersistentCookieJar) http.Handler {
	return &gohort_proxy{
		cfg:            cfg,
		configure_html: configure_html,
		cookies:        cookies,
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
const popup_shim_script = `<script>(function(){` +
	// --- uiConfirm / uiAlert custom-modal implementation ---
	// WKWebView's WKUIDelegate doesn't implement runJavaScriptConfirmPanel
	// or runJavaScriptAlertPanel, so native confirm()/alert() silently
	// no-op. The core/ui runtime defines window.uiConfirm/uiAlert as
	// async wrappers that delegate to window.__uiConfirmImpl /
	// __uiAlertImpl when present — this shim provides those impls
	// so the desktop gets real prompts via a CSS modal.
	//
	// Both return a Promise. uiConfirm resolves to bool (true=OK,
	// false=Cancel/Esc). uiAlert resolves once the user dismisses.
	`function __desktop_modal_open(opts){` +
	`return new Promise(function(resolve){` +
	`try{` +
	`var overlay=document.createElement('div');` +
	`overlay.setAttribute('data-gohort-modal','1');` +
	// Explicit top/right/bottom/left (not inset:0 shorthand) for
	// maximum WKWebView compatibility. Flex centering puts the
	// card in the viewport middle. Earlier version had inset:0
	// which was being interpreted partially in some WebKit
	// builds — the card dropped to the bottom.
	`overlay.style.position='fixed';` +
	`overlay.style.top='0';overlay.style.left='0';` +
	`overlay.style.right='0';overlay.style.bottom='0';` +
	`overlay.style.width='100vw';overlay.style.height='100vh';` +
	`overlay.style.zIndex='2147483646';` +
	`overlay.style.display='flex';` +
	`overlay.style.alignItems='center';` +
	`overlay.style.justifyContent='center';` +
	`overlay.style.background='rgba(0,0,0,0.55)';` +
	`overlay.style.font='14px -apple-system,system-ui,sans-serif';` +
	`var card=document.createElement('div');` +
	`card.style.minWidth='340px';card.style.maxWidth='520px';` +
	`card.style.padding='20px 22px';` +
	`card.style.background='#161b22';card.style.color='#e6edf3';` +
	`card.style.border='1px solid #30363d';card.style.borderRadius='10px';` +
	`card.style.boxShadow='0 12px 40px rgba(0,0,0,0.6)';` +
	`var msg;` +
	// _customBody lets callers (e.g. the approval modal) provide
	// a pre-built DOM element instead of a plain string — needed
	// to render structured content like a name + args breakdown.
	`if(opts._customBody){msg=opts._customBody;}` +
	`else{msg=document.createElement('div');msg.textContent=opts.msg||'';}` +
	`msg.style.marginBottom='18px';msg.style.lineHeight='1.5';` +
	`msg.style.whiteSpace=opts._customBody?'normal':'pre-wrap';msg.style.wordBreak='break-word';` +
	`var actions=document.createElement('div');` +
	`actions.style.display='flex';actions.style.justifyContent='flex-end';actions.style.gap='8px';` +
	`function done(v){` +
	`document.removeEventListener('keydown',on_key,true);` +
	`if(overlay.parentNode)overlay.parentNode.removeChild(overlay);` +
	`resolve(v);` +
	`}` +
	`function mk_btn(label,primary,value){` +
	`var b=document.createElement('button');` +
	`b.textContent=label;` +
	`b.style.padding='7px 16px';b.style.borderRadius='6px';b.style.cursor='pointer';` +
	`b.style.font='13px -apple-system,system-ui,sans-serif';` +
	`b.style.border='1px solid '+(primary?'#3a6ea5':'#30363d');` +
	`b.style.background=primary?'#3a6ea5':'#21262d';` +
	`b.style.color=primary?'#fff':'#c9d1d9';` +
	`b.onclick=function(){done(value);};` +
	`return b;` +
	`}` +
	`if(opts.kind==='confirm'){` +
	`actions.appendChild(mk_btn('Cancel',false,false));` +
	`actions.appendChild(mk_btn(opts.ok||'OK',true,true));` +
	`}else{` +
	`actions.appendChild(mk_btn('OK',true,undefined));` +
	`}` +
	`card.appendChild(msg);card.appendChild(actions);` +
	`overlay.appendChild(card);` +
	`function on_key(ev){` +
	`if(ev.key==='Escape'){ev.preventDefault();done(opts.kind==='confirm'?false:undefined);}` +
	`else if(ev.key==='Enter'){ev.preventDefault();done(opts.kind==='confirm'?true:undefined);}` +
	`}` +
	`document.addEventListener('keydown',on_key,true);` +
	`(document.body||document.documentElement).appendChild(overlay);` +
	`var btns=actions.querySelectorAll('button');` +
	`if(btns.length)btns[btns.length-1].focus();` +
	`console.log('[gohort-desktop] modal open:',opts.kind,opts.msg);` +
	`}catch(err){` +
	`console.error('[gohort-desktop] modal error:',err);` +
	`resolve(opts.kind==='confirm'?true:undefined);` +
	`}` +
	`});` +
	`}` +
	`window.__uiConfirmImpl=function(msg){return __desktop_modal_open({kind:'confirm',msg:msg});};` +
	`window.__uiAlertImpl=function(msg){return __desktop_modal_open({kind:'alert',msg:msg});};` +
	// --- clipboard implementation ---
	// WKWebView restricts navigator.clipboard.writeText at non-https /
	// non-user-gesture contexts; the loopback proxy origin trips both,
	// so the browser-side fallback in core/ui (a hidden textarea +
	// document.execCommand('copy')) also silently fails. Route Copy
	// session and any other framework clipboard writes through Wails'
	// native runtime.ClipboardSetText instead. The shim returns a
	// Promise so the caller's then/catch chain works unchanged.
	`window.__uiClipboardImpl=function(text){` +
	`if(window.runtime&&window.runtime.ClipboardSetText){` +
	`try{var r=window.runtime.ClipboardSetText(text||'');` +
	`return (r&&typeof r.then==='function')?r:Promise.resolve(true);}` +
	`catch(e){return Promise.reject(e);}` +
	`}` +
	`return Promise.reject(new Error('Wails clipboard runtime unavailable'));` +
	`};` +
	// --- bridge tool-call approval modal ---
	// When the WS bridge receives a tool invocation from the
	// server, ws_client.go's handleInvoke calls
	// App.RequestApprovalBlocking, which emits a "bridge-approval"
	// Wails event carrying {id, name, args}. We listen for it,
	// render a modal with the tool/args, and call ApproveInvoke
	// with the user's choice. App is responsible for short-
	// circuiting auto-approve mode before reaching here.
	`function __desktop_pretty(args){` +
	`try{return JSON.stringify(args,null,2);}catch(e){return String(args);}` +
	`}` +
	`function __desktop_approval_open(req){` +
	`var msg=document.createElement('div');` +
	`var head=document.createElement('div');` +
	`head.textContent='An agent on the server is asking to run a local tool:';` +
	`head.style.cssText='margin-bottom:12px;';` +
	`msg.appendChild(head);` +
	`var nameRow=document.createElement('div');` +
	`nameRow.style.cssText='font-family:ui-monospace,Menlo,monospace;font-size:13px;background:#0d1117;padding:8px 10px;border-radius:6px;margin-bottom:10px;border:1px solid #30363d;color:#79c0ff;';` +
	`nameRow.textContent=req.name||'(unnamed)';` +
	`msg.appendChild(nameRow);` +
	`var argsLabel=document.createElement('div');` +
	`argsLabel.textContent='Arguments:';` +
	`argsLabel.style.cssText='font-size:12px;color:#8b949e;margin-bottom:4px;';` +
	`msg.appendChild(argsLabel);` +
	`var argsBox=document.createElement('pre');` +
	`argsBox.style.cssText='font-family:ui-monospace,Menlo,monospace;font-size:12px;background:#0d1117;padding:8px 10px;border-radius:6px;margin:0;border:1px solid #30363d;color:#c9d1d9;max-height:200px;overflow:auto;white-space:pre-wrap;word-break:break-word;';` +
	`argsBox.textContent=__desktop_pretty(req.args||{});` +
	`msg.appendChild(argsBox);` +
	`__desktop_modal_open({kind:'confirm',msg:'',ok:'Allow',_customBody:msg}).then(function(allow){` +
	`if(window.go&&window.go.main&&window.go.main.App&&window.go.main.App.ApproveInvoke){` +
	`window.go.main.App.ApproveInvoke(req.id||'',!!allow);` +
	`}` +
	`});` +
	`}` +
	`if(window.runtime&&window.runtime.EventsOn){` +
	`window.runtime.EventsOn('bridge-approval',function(req){__desktop_approval_open(req);});` +
	`console.log('[gohort-desktop] bridge approval listener installed');` +
	`}` +
	// --- in-app log viewer ---
	// Listens for the "show-logs" event fired by the Account →
	// Show Logs menu item. Renders the ring-buffer as a scrollable
	// dark overlay; polls every 2s for new lines while open so the
	// viewer stays live without leaving the app.
	`function __desktop_logs_open(initial){` +
	`var prev=document.getElementById('__desktop_logs');` +
	`if(prev){prev.remove();}` +
	`var overlay=document.createElement('div');` +
	`overlay.id='__desktop_logs';` +
	`overlay.style.position='fixed';overlay.style.top='0';overlay.style.left='0';` +
	`overlay.style.right='0';overlay.style.bottom='0';overlay.style.zIndex='2147483645';` +
	`overlay.style.background='rgba(0,0,0,0.7)';` +
	`overlay.style.display='flex';overlay.style.alignItems='center';overlay.style.justifyContent='center';` +
	`overlay.style.font='13px -apple-system,system-ui,sans-serif';` +
	`var card=document.createElement('div');` +
	`card.style.width='min(900px,92vw)';card.style.height='min(70vh,720px)';` +
	`card.style.background='#0d1117';card.style.color='#c9d1d9';` +
	`card.style.border='1px solid #30363d';card.style.borderRadius='10px';` +
	`card.style.boxShadow='0 12px 40px rgba(0,0,0,0.6)';` +
	`card.style.display='flex';card.style.flexDirection='column';overlay.appendChild(card);` +
	`var head=document.createElement('div');` +
	`head.style.padding='10px 14px';head.style.borderBottom='1px solid #30363d';` +
	`head.style.display='flex';head.style.alignItems='center';head.style.gap='8px';` +
	`var title=document.createElement('div');` +
	`title.textContent='Gohort Desktop — Logs';title.style.flex='1';title.style.fontWeight='600';` +
	`head.appendChild(title);` +
	`var copyBtn=document.createElement('button');` +
	`copyBtn.textContent='Copy';` +
	`copyBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #30363d;background:#21262d;color:#c9d1d9;';` +
	`head.appendChild(copyBtn);` +
	`var closeBtn=document.createElement('button');` +
	`closeBtn.textContent='Close';` +
	`closeBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #3a6ea5;background:#3a6ea5;color:#fff;';` +
	`head.appendChild(closeBtn);` +
	`card.appendChild(head);` +
	`var body=document.createElement('pre');` +
	`body.style.cssText='flex:1;margin:0;padding:10px 14px;overflow:auto;font:12px ui-monospace,Menlo,monospace;line-height:1.45;white-space:pre-wrap;word-break:break-word;background:#0d1117;color:#c9d1d9;';` +
	`card.appendChild(body);` +
	`function colorFor(level){` +
	`if(level==='error'||level==='fatal')return '#ff7b72';` +
	`if(level==='warn')return '#d29922';` +
	`if(level==='notice')return '#79c0ff';` +
	`return '#c9d1d9';` +
	`}` +
	`function render(lines){` +
	`body.innerHTML='';` +
	`lines.forEach(function(ln){` +
	`var row=document.createElement('div');` +
	`row.style.color=colorFor(ln.level);` +
	`var t=ln.when?new Date(ln.when).toLocaleTimeString():'';` +
	`row.textContent='['+(t)+' '+(ln.level||'').toUpperCase().padEnd(6)+'] '+(ln.text||'');` +
	`body.appendChild(row);` +
	`});` +
	`body.scrollTop=body.scrollHeight;` +
	`}` +
	`function refresh(){` +
	`if(!window.go||!window.go.main||!window.go.main.App||!window.go.main.App.GetLogs)return;` +
	`window.go.main.App.GetLogs().then(function(lines){render(lines||[]);});` +
	`}` +
	`if(initial&&initial.length){render(initial);}` +
	`var pollId=setInterval(refresh,2000);refresh();` +
	`function teardown(){clearInterval(pollId);overlay.remove();document.removeEventListener('keydown',onKey,true);}` +
	`function onKey(e){if(e.key==='Escape'){e.preventDefault();teardown();}}` +
	`document.addEventListener('keydown',onKey,true);` +
	`closeBtn.onclick=teardown;` +
	`copyBtn.onclick=function(){` +
	`if(navigator.clipboard&&navigator.clipboard.writeText){` +
	`navigator.clipboard.writeText(body.innerText).then(function(){copyBtn.textContent='Copied';setTimeout(function(){copyBtn.textContent='Copy';},1500);});` +
	`}` +
	`};` +
	`(document.body||document.documentElement).appendChild(overlay);` +
	`}` +
	`window.__desktop_logs_open=__desktop_logs_open;` +
	`if(window.runtime&&window.runtime.EventsOn){` +
	`window.runtime.EventsOn('show-logs',function(){__desktop_logs_open();});` +
	`}` +
	// --- allowed-folders manager ---
	// Listens for the "show-allowed-folders" event fired by the
	// Account → Show Allowed Folders menu item. Lists the current
	// allowlist with a Remove button per row, plus an Add button
	// that triggers the native folder picker via App.PickReadRoot.
	// Refreshes on "allowed-folders-changed" so adds from any source
	// (menu picker, this modal, the WS bridge later) show up live.
	`function __desktop_folders_open(){` +
	`var prev=document.getElementById('__desktop_folders');` +
	`if(prev){prev.remove();}` +
	`var overlay=document.createElement('div');` +
	`overlay.id='__desktop_folders';` +
	`overlay.style.position='fixed';overlay.style.top='0';overlay.style.left='0';` +
	`overlay.style.right='0';overlay.style.bottom='0';overlay.style.zIndex='2147483645';` +
	`overlay.style.background='rgba(0,0,0,0.7)';` +
	`overlay.style.display='flex';overlay.style.alignItems='center';overlay.style.justifyContent='center';` +
	`overlay.style.font='13px -apple-system,system-ui,sans-serif';` +
	`var card=document.createElement('div');` +
	`card.style.width='min(720px,92vw)';card.style.maxHeight='min(70vh,600px)';` +
	`card.style.background='#0d1117';card.style.color='#c9d1d9';` +
	`card.style.border='1px solid #30363d';card.style.borderRadius='10px';` +
	`card.style.boxShadow='0 12px 40px rgba(0,0,0,0.6)';` +
	`card.style.display='flex';card.style.flexDirection='column';overlay.appendChild(card);` +
	`var head=document.createElement('div');` +
	`head.style.padding='10px 14px';head.style.borderBottom='1px solid #30363d';` +
	`head.style.display='flex';head.style.alignItems='center';head.style.gap='8px';` +
	`var title=document.createElement('div');` +
	`title.textContent='Allowed Folders — gohort-desktop';title.style.flex='1';title.style.fontWeight='600';` +
	`head.appendChild(title);` +
	`var addBtn=document.createElement('button');` +
	`addBtn.textContent='Add Folder…';` +
	`addBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #2ea043;background:#238636;color:#fff;';` +
	`head.appendChild(addBtn);` +
	`var closeBtn=document.createElement('button');` +
	`closeBtn.textContent='Close';` +
	`closeBtn.style.cssText='padding:5px 12px;border-radius:6px;cursor:pointer;font:12px sans-serif;border:1px solid #3a6ea5;background:#3a6ea5;color:#fff;';` +
	`head.appendChild(closeBtn);` +
	`card.appendChild(head);` +
	`var help=document.createElement('div');` +
	`help.textContent='Files and folders under these roots are exposed to gohort agents through the filesystem.* tools. Remove a root and the access goes away immediately.';` +
	`help.style.cssText='padding:10px 14px;border-bottom:1px solid #30363d;color:#8b949e;font-size:12px;line-height:1.45;';` +
	`card.appendChild(help);` +
	`var listEl=document.createElement('div');` +
	`listEl.style.cssText='flex:1;overflow:auto;padding:6px 8px;';` +
	`card.appendChild(listEl);` +
	`function refresh(){` +
	`if(!window.go||!window.go.main||!window.go.main.App||!window.go.main.App.GetReadRoots){return;}` +
	`window.go.main.App.GetReadRoots().then(function(roots){` +
	`listEl.innerHTML='';` +
	`if(!roots||roots.length===0){` +
	`var empty=document.createElement('div');` +
	`empty.textContent='No folders allowed. Click Add Folder… to expose one.';` +
	`empty.style.cssText='padding:18px 14px;color:#8b949e;text-align:center;';` +
	`listEl.appendChild(empty);return;` +
	`}` +
	`roots.forEach(function(p){` +
	`var row=document.createElement('div');` +
	`row.style.cssText='display:flex;align-items:center;gap:8px;padding:8px 10px;border-bottom:1px solid #21262d;';` +
	`var pathEl=document.createElement('div');` +
	`pathEl.textContent=p;pathEl.style.cssText='flex:1;font:12px ui-monospace,Menlo,monospace;word-break:break-all;';` +
	`row.appendChild(pathEl);` +
	`var rm=document.createElement('button');` +
	`rm.textContent='Remove';` +
	`rm.style.cssText='padding:4px 10px;border-radius:5px;cursor:pointer;font:11px sans-serif;border:1px solid #4d2929;background:#3a1f1f;color:#ff9b9b;';` +
	`rm.onclick=function(){` +
	`rm.disabled=true;rm.textContent='Removing…';` +
	`window.go.main.App.RemoveReadRoot(p).then(function(res){` +
	`if(!res.ok){rm.disabled=false;rm.textContent='Remove';alert('Remove failed: '+(res.error||'unknown'));return;}` +
	`refresh();` +
	`});` +
	`};` +
	`row.appendChild(rm);` +
	`listEl.appendChild(row);` +
	`});` +
	`});` +
	`}` +
	`addBtn.onclick=function(){` +
	`if(!window.go||!window.go.main||!window.go.main.App||!window.go.main.App.PickReadRoot){return;}` +
	`addBtn.disabled=true;addBtn.textContent='Choose…';` +
	`window.go.main.App.PickReadRoot().then(function(res){` +
	`addBtn.disabled=false;addBtn.textContent='Add Folder…';` +
	`if(res.error){alert('Add failed: '+res.error);return;}` +
	`refresh();` +
	`});` +
	`};` +
	`function teardown(){overlay.remove();document.removeEventListener('keydown',onKey,true);if(unsub){unsub();}}` +
	`function onKey(e){if(e.key==='Escape'){e.preventDefault();teardown();}}` +
	`document.addEventListener('keydown',onKey,true);` +
	`closeBtn.onclick=teardown;` +
	`var unsub=null;` +
	`if(window.runtime&&window.runtime.EventsOn&&window.runtime.EventsOff){` +
	`window.runtime.EventsOn('allowed-folders-changed',refresh);` +
	`unsub=function(){window.runtime.EventsOff('allowed-folders-changed');};` +
	`}` +
	`(document.body||document.documentElement).appendChild(overlay);` +
	`refresh();` +
	`}` +
	`if(window.runtime&&window.runtime.EventsOn){` +
	`window.runtime.EventsOn('show-allowed-folders',function(){__desktop_folders_open();});` +
	`}` +
	`console.log('[gohort-desktop] uiConfirm/uiAlert modal impl installed');` +
	// Fallback for any legacy sync confirm()/alert() callers that
	// haven't migrated to window.uiConfirm/uiAlert yet (gohort has
	// ~45 of these in techwriter / static / older app code). Native
	// confirm() is broken in WKWebView so without these the action
	// would silently no-op. confirm() auto-accepts (loses safety
	// prompt but lets the action run); alert() turns into a toast.
	// Migrating each callsite to uiConfirm/uiAlert gets the proper
	// modal.
	`window.confirm=function(msg){__desktop_toast('Confirmed: '+(msg||''));return true;};` +
	`window.alert=function(msg){__desktop_toast(String(msg||''));};` +
	`function __desktop_toast(text){` +
	`var t=document.createElement('div');` +
	`t.textContent=text;` +
	`t.style.cssText='position:fixed;left:50%;bottom:32px;transform:translateX(-50%);` +
	`z-index:99999;max-width:80vw;padding:8px 14px;` +
	`background:rgba(20,20,20,0.92);color:#e8e8e8;` +
	`border:1px solid rgba(255,255,255,0.14);border-radius:6px;` +
	`font:13px -apple-system,system-ui,sans-serif;` +
	`box-shadow:0 4px 12px rgba(0,0,0,0.4);pointer-events:none;` +
	`opacity:0;transition:opacity 0.15s ease;';` +
	`(document.body||document.documentElement).appendChild(t);` +
	`requestAnimationFrame(function(){t.style.opacity='1';});` +
	`setTimeout(function(){` +
	`t.style.opacity='0';` +
	`setTimeout(function(){if(t.parentNode)t.parentNode.removeChild(t);},300);` +
	`},2400);` +
	`}` +
	// --- window.open shim ---
	`function is_external(url){return /^https?:\/\//i.test(url);}` +
	`function open_overlay(url){` +
	`var prev=document.getElementById('__gohort_desktop_overlay');` +
	`if(prev)prev.remove();` +
	`var overlay=document.createElement('div');` +
	`overlay.id='__gohort_desktop_overlay';` +
	`overlay.style.cssText='position:fixed;inset:0;z-index:1000000;background:#0d1117;display:flex;flex-direction:column;';` +
	`var bar=document.createElement('div');` +
	`bar.style.cssText='display:flex;align-items:center;gap:0.5rem;padding:6px 8px;background:#161b22;border-bottom:1px solid #30363d;font:12px -apple-system,system-ui,sans-serif;color:#8b949e;';` +
	`var label=document.createElement('span');` +
	`label.textContent=url;label.style.cssText='flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;';` +
	`var close=document.createElement('button');` +
	`close.innerHTML='✕';close.title='Close (Esc)';` +
	`close.style.cssText='width:26px;height:24px;border:1px solid #30363d;background:#21262d;color:#c9d1d9;cursor:pointer;border-radius:4px;font:14px sans-serif;line-height:1;padding:0;flex-shrink:0;';` +
	`close.onclick=function(){overlay.remove();document.removeEventListener('keydown',on_esc);};` +
	`bar.appendChild(label);bar.appendChild(close);` +
	`var iframe=document.createElement('iframe');` +
	`iframe.src=url;` +
	`iframe.style.cssText='flex:1;border:0;width:100%;background:#0d1117;';` +
	`overlay.appendChild(bar);overlay.appendChild(iframe);` +
	`function on_esc(e){if(e.key==='Escape'){overlay.remove();document.removeEventListener('keydown',on_esc);}}` +
	`document.addEventListener('keydown',on_esc);` +
	`(document.body||document.documentElement).appendChild(overlay);` +
	`}` +
	`window.open=function(url){` +
	`if(!url)return null;` +
	`if(is_external(String(url))){` +
	`if(window.runtime&&window.runtime.BrowserOpenURL){window.runtime.BrowserOpenURL(String(url));}` +
	`return null;` +
	`}` +
	`open_overlay(String(url));return null;` +
	`};` +
	// --- target="_blank" anchor click capture ---
	`document.addEventListener('click',function(ev){` +
	`var a=ev.target&&ev.target.closest&&ev.target.closest('a[target=\"_blank\"]');` +
	`if(!a||!a.href)return;` +
	`ev.preventDefault();ev.stopPropagation();` +
	`window.open(a.getAttribute('href')||a.href);` +
	`},true);` +
	// --- floating refresh button ---
	`function add_refresh(){` +
	`if(document.getElementById('__gohort_desktop_refresh'))return;` +
	`var b=document.createElement('button');` +
	`b.id='__gohort_desktop_refresh';` +
	`b.title='Refresh (⌘R)';b.setAttribute('aria-label','Refresh');b.innerHTML='↻';` +
	`b.style.cssText='position:fixed;top:6px;right:8px;z-index:99999;` +
	`width:28px;height:28px;border-radius:50%;border:1px solid rgba(255,255,255,0.12);` +
	`background:rgba(40,40,40,0.78);color:#ddd;cursor:pointer;` +
	`font:16px -apple-system,system-ui,sans-serif;line-height:24px;padding:0;` +
	`box-shadow:0 2px 6px rgba(0,0,0,0.35);transition:background 0.12s,color 0.12s;';` +
	`b.onmouseover=function(){b.style.background='rgba(60,60,60,0.95)';b.style.color='#fff';};` +
	`b.onmouseout=function(){b.style.background='rgba(40,40,40,0.78)';b.style.color='#ddd';};` +
	`b.onclick=function(){location.reload();};` +
	`(document.body||document.documentElement).appendChild(b);` +
	`}` +
	`if(document.readyState==='loading'){` +
	`document.addEventListener('DOMContentLoaded',add_refresh);` +
	`}else{add_refresh();}` +
	`})();</script>`

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
  // Bridge may not be bound yet if this page loaded outside Wails
  // (devtools refresh edge case); fall back to a plain reload.
  if (window.go && window.go.main && window.go.main.App && window.go.main.App.ResetSettings) {
    window.go.main.App.ResetSettings().then(function() { location.reload(); });
  } else {
    location.reload();
  }
}
</script>
</body>
</html>`
