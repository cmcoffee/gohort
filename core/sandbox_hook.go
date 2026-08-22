// Sandbox hook: a per-dispatch Unix domain socket that lets a script
// running inside the no-network bwrap sandbox call back into gohort
// for narrow, capability-gated operations (HTTP fetch, log, etc.).
//
// Why UDS works inside --unshare-net: a Unix domain socket lives in
// the filesystem namespace, not the network namespace. The sandbox's
// workspace dir is bind-mounted into the sandbox at the same path it
// has on the host, so a socket file gohort listens on at
// <workspace>/.gohort_hook_<token>.sock is reachable from inside via
// the same path. socket.AF_UNIX connections work normally.
//
// Wire format: newline-delimited JSON over a connection-per-call
// socket. Client opens, sends ONE line of JSON, reads ONE line of
// JSON, closes. Keeps the server logic trivially stateless.
//
//	request:  {"method": "...", "params": {...}}\n
//	response: {"result": ...}\n   or   {"error": "..."}\n
//
// Capability gating: only methods listed in the tool's
// HookCapabilities are answered; others return a clear error. Empty
// HookCapabilities ⇒ no hook is started at all (no socket file, no
// env var). This keeps the privacy posture clean — a tool without
// declared capabilities has zero surface area to gohort, same as
// before the hook existed.

package core

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/iotimeout"
)

// BrowserFetchFunc is the registration shim that lets the sandbox-hook
// fetch raw text from the headless browser without core importing
// tools/browser (which would be circular — tools/browser depends on
// core). Set by tools/browser's init() to browser.Fetch. Returns the
// page's extracted readable text WITHOUT the "Fetched X via browser
// (N chars):" preamble the LLM-callable BrowsePageTool.Run wraps on
// — that preamble is the LLM-shaped part of the surface; script-side
// callers consume raw body. nil-safe: when unset (no browser package
// linked), the hook falls through to plain HTTP.
var BrowserFetchFunc func(url string, maxChars int) (string, error)

// SandboxHook is the per-dispatch UDS server. SocketPath is the
// listener's filesystem path; expose it to the sandbox via the env
// var GOHORT_HOOK_PATH. Capabilities is the allowed-method list —
// only these are dispatched, everything else returns an error.
//
// Sess carries the calling ToolSession for handlers that need it
// (fetch_via plumbs through Secure().DispatchToolCall which uses
// the session's workspace for save_to support). Plain fetch / log /
// secret don't need it; they ignore the field.
type SandboxHook struct {
	SocketPath   string
	Capabilities []string
	Sess         *ToolSession
	// ToolName identifies the temp tool this hook serves, for secured-credential
	// binding enforcement on fetch_via (see EnforceSecuredBinding). Empty for
	// unnamed callers (run_local / persistent shell), which aren't binding-gated.
	// Set by the dispatcher right after NewSandboxHook, before the sandbox runs.
	ToolName string

	listener net.Listener
	wg       sync.WaitGroup
	closed   chan struct{}
}

// NewSandboxHook starts a hook listener inside workspaceDir. Returns
// nil (no error) when capabilities is empty — caller treats this as
// "no hook attached, no env var to set."
//
// sess is the calling ToolSession; the hook stashes it so handlers
// that need session context (fetch_via, future tool-dispatch) can use
// it. Nil sess is permitted — only fetch_via cares, and it will
// refuse cleanly if called without one.
//
// The socket file is created under workspaceDir so it inherits the
// bind-mount that already exists for the sandbox. The path includes
// a random token so concurrent dispatches sharing the same workspace
// don't collide.
//
// Caller MUST call Close() after the dispatch returns. Close removes
// the socket file and waits for in-flight handlers to drain.
func NewSandboxHook(workspaceDir string, capabilities []string, sess *ToolSession) (*SandboxHook, error) {
	if len(capabilities) == 0 {
		return nil, nil
	}
	if workspaceDir == "" {
		return nil, fmt.Errorf("sandbox hook needs a workspace dir to bind under")
	}
	token, err := randomHookToken()
	if err != nil {
		return nil, fmt.Errorf("hook token: %w", err)
	}
	path, err := hookSocketPath(workspaceDir, token)
	if err != nil {
		return nil, err
	}
	// Pre-cleanup any stale file at this path. With a fresh random
	// token the collision is astronomically unlikely, but a leftover
	// from a hard-killed prior process would otherwise wedge net.Listen.
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("hook listen %s: %w", path, err)
	}
	// Tighten perms — only the gohort process user can connect.
	// Inside the sandbox the script runs as the same UID (bwrap
	// inherits), so it can still open it; an unrelated user on the
	// host can't.
	_ = os.Chmod(path, 0600)
	h := &SandboxHook{
		SocketPath:   path,
		Capabilities: append([]string(nil), capabilities...),
		Sess:         sess,
		listener:     l,
		closed:       make(chan struct{}),
	}
	h.wg.Add(1)
	go h.acceptLoop()
	Debug("[hook] listening at %s caps=%v", path, capabilities)
	return h, nil
}

// maxUnixSocketPath is the usable length of sun_path on Linux: the
// struct field is 108 bytes and one is the terminator. Bind returns a
// bare EINVAL — "invalid argument" — when a path exceeds it, which
// names neither the limit nor the path as the problem and sent an
// operator looking at permissions and filesystems instead.
const maxUnixSocketPath = 107

// hookSocketDirName is the host directory hook sockets bind under. Kept
// SHORT and deliberately outside the workspace.
const hookSocketDirName = "gohort-hk"

// hookSocketPath picks where this hook's socket lives.
//
// It used to be <workspace>/.gohort_hook_<token>.sock, because the
// workspace is bind-mounted into the sandbox at its own path and a
// socket inside it is therefore reachable from both sides for free.
// That works until the workspace path gets long. A per-agent workspace
// is <root>/.agents/<email>/<uuid>/ — 92 characters on a plain install
// — and the socket name is another 34, which is 126 against a hard
// limit of 107. Shortening the NAME cannot fix it: the prefix alone
// leaves 15 characters, and ".gohort_hook_.sock" is 18 with no token
// at all. The location had to move.
//
// So: a short host dir, and the single socket FILE bind-mounted into
// the sandbox at that same short path (see bwrapArgvWithEnv). Both ends
// stay well inside the limit no matter how deep the workspace is.
//
// A side benefit worth stating, because it would otherwise look like a
// regression: the socket is no longer sitting in the workspace where
// the model's own script can list it. Only the one file is mounted, so
// a sandboxed script sees its own socket and cannot enumerate anyone
// else's.
//
// Falls back to the old in-workspace path if the short dir cannot be
// created, and fails with an explanation rather than an EINVAL if
// neither fits.
func hookSocketPath(workspaceDir, token string) (string, error) {
	name := token + ".sock"
	dir := filepath.Join(os.TempDir(), hookSocketDirName)
	if err := os.MkdirAll(dir, 0700); err == nil {
		if p := filepath.Join(dir, name); len(p) <= maxUnixSocketPath {
			return p, nil
		}
	}
	// TMPDIR itself is unusable or absurdly deep. The workspace is the
	// only other place both sides can reach, so try it and say plainly
	// what is wrong if it does not fit either.
	p := filepath.Join(workspaceDir, ".gohort_hook_"+name)
	if len(p) > maxUnixSocketPath {
		return "", fmt.Errorf("no usable socket path: %q is %d bytes and a unix socket cannot exceed %d. "+
			"The short path under %s could not be created either — check that the temp dir is writable",
			p, len(p), maxUnixSocketPath, os.TempDir())
	}
	return p, nil
}

// Close tears down the listener, removes the socket file, and waits
// for in-flight handlers. Safe to call on a nil receiver.
func (h *SandboxHook) Close() {
	if h == nil {
		return
	}
	select {
	case <-h.closed:
		return // already closed
	default:
		close(h.closed)
	}
	_ = h.listener.Close()
	h.wg.Wait()
	_ = os.Remove(h.SocketPath)
	Debug("[hook] closed %s", h.SocketPath)
}

func (h *SandboxHook) acceptLoop() {
	defer h.wg.Done()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			select {
			case <-h.closed:
				return // expected on Close
			default:
				Debug("[hook] accept error: %v", err)
				return
			}
		}
		h.wg.Add(1)
		go func(c net.Conn) {
			defer h.wg.Done()
			h.handleConn(c)
		}(conn)
	}
}

// handleConn reads one JSON request, dispatches it, writes one JSON
// response, closes the connection. Two-phase deadline:
//
//  1. Read phase (tight, 10s) — request must arrive quickly. Catches
//     stalled / partial-write scripts.
//  2. Dispatch phase — extended to a per-method natural budget so
//     long-but-legitimate operations (fetch with timeout=120, browse_page
//     with first-launch Chromium download, fetch_via against a slow API)
//     don't trip the hook deadline before the HTTP itself finishes.
//
// Logs at every transition so an "intermittent hang" can be located
// in time and method (see hookMethodDeadline for the per-method
// budgets).
//
//   - "[hook] conn accepted"        — connection started, before any read
//   - "[hook] req method=X ..."     — request decoded, before handler dispatch
//   - "[hook] deadline extended ..." — phase 2 deadline applied (method-scoped)
//   - "[hook/<method>] start ..."   — handler entered (per-handler log)
//   - "[hook/<method>] done elapsed=Xms ..." — handler returned
//   - "[hook] conn closed elapsed=Xms method=X" — connection teardown
//
// Grep "elapsed=" for slow calls. A hang surfaces as a "start ..." with
// no matching "done ...".
func (h *SandboxHook) handleConn(conn net.Conn) {
	connStart := time.Now()
	defer conn.Close()
	method := "?"
	defer func() {
		Log("[hook] conn closed elapsed=%s method=%s", time.Since(connStart).Round(time.Millisecond), method)
	}()
	Log("[hook] conn accepted")
	// Phase 1: tight read-phase deadline. The request body should be
	// on the wire immediately; a stalled write here means the script
	// is misbehaving. 10s is generous.
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	line, err := br.ReadBytes('\n')
	if err != nil && err != io.EOF {
		Log("[hook] read error: %v (elapsed=%s)", err, time.Since(connStart).Round(time.Millisecond))
		return
	}
	var req struct {
		Method string                 `json:"method"`
		Params map[string]interface{} `json:"params"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		Log("[hook] invalid JSON request: %v (body=%d bytes)", err, len(line))
		writeHookError(conn, "invalid JSON request: "+err.Error())
		return
	}
	method = req.Method
	if !h.granted(req.Method) {
		Log("[hook] method %q not granted; capabilities=%v", req.Method, h.Capabilities)
		writeHookError(conn, fmt.Sprintf("method %q not granted; capabilities=%v", req.Method, h.Capabilities))
		return
	}
	// Phase 2: extend deadline based on the natural budget of the
	// requested method. Without this, fetch(timeout=120) would trip
	// the original 60s read-phase deadline mid-stream even though
	// the HTTP call is still proceeding.
	methodDeadline := hookMethodDeadline(req.Method, req.Params)
	_ = conn.SetDeadline(time.Now().Add(methodDeadline))
	Log("[hook] req method=%s params=%v deadline=%s", req.Method, paramKeySummary(req.Params), methodDeadline)
	// Private-mode network gate. fetch / fetch_via reach outside the
	// sandbox via gohort's HTTP stack — those have to honor the
	// session-level NetworkConnector the same way the bwrap
	// --unshare-net path does. Without this check a shell tool with
	// hook_capabilities=["fetch"] could still phone home in Private
	// mode, defeating the toggle. secret / log don't touch the
	// network so they stay available (reading a stored credential is
	// not an outbound call; it's a DB read).
	if (req.Method == "fetch" || req.Method == "fetch_via" || req.Method == "browse_page") && h.Sess != nil && !h.Sess.NetworkAllowed() {
		Log("[hook/%s] DENIED — session network blocked (privacy mode)", req.Method)
		writeHookError(conn, fmt.Sprintf("hook %q refused — session network blocked (privacy mode is on)", req.Method))
		return
	}
	dispatchStart := time.Now()
	switch req.Method {
	case "fetch":
		h.handleFetch(conn, req.Params)
	case "log":
		handleHookLog(conn, req.Params)
	case "secret":
		h.handleSecret(conn, req.Params)
	case "fetch_via":
		h.handleFetchVia(conn, req.Params)
	case "browse_page":
		h.handleBrowsePage(conn, req.Params)
	default:
		Log("[hook] unknown method: %s", req.Method)
		writeHookError(conn, "unknown method: "+req.Method)
	}
	// Slow-call warning when the handler used over 80% of its budget.
	// Tells the operator a per-method deadline is being approached
	// for a workload pattern; if this fires repeatedly for a method,
	// the deadline may need raising.
	if elapsed := time.Since(dispatchStart); elapsed > (methodDeadline*8)/10 {
		Log("[hook] SLOW handler method=%s elapsed=%s budget=%s (>80%% used — operation near deadline)",
			req.Method, elapsed.Round(time.Millisecond), methodDeadline)
	}
}

// hookMethodDeadline returns the per-connection deadline appropriate
// for the given hook method. Sized to the natural budget of each
// operation plus a small buffer for I/O and the JSON response write.
// A method not enumerated here defaults to 10s — same as the read
// phase, since unknown methods will only generate an error response.
//
//   - fetch: caller-supplied timeout (0 < t < 300, default 30s) plus
//     15s IO buffer. Lets fetch(timeout=120) actually use 120s.
//   - browse_page: 75s. Internal budget is 45s (browsePageTotalBudget),
//     plus headroom for Chromium first-launch download spikes and the
//     response-write phase.
//   - fetch_via: 90s. Credentialed dispatch can be slow under rate-
//     limit / retry; conservative.
//   - secret, log: 10s. Trivial operations.
func hookMethodDeadline(method string, params map[string]interface{}) time.Duration {
	switch method {
	case "fetch":
		callTimeout := 30 * time.Second
		if t, ok := params["timeout"].(float64); ok && t > 0 && t < 300 {
			callTimeout = time.Duration(t * float64(time.Second))
		}
		return callTimeout + 15*time.Second
	case "browse_page":
		return 75 * time.Second
	case "fetch_via":
		return 90 * time.Second
	case "secret", "log":
		return 10 * time.Second
	}
	return 10 * time.Second
}

// fetchDoneLog picks the sink for a fetch-completion line: a 2xx success goes to
// the log FILE only (AuxLog), so a watched terminal isn't flooded by routine
// polling; a non-2xx stays on the terminal where it's the actual signal. Both
// still land in the log file.
func fetchDoneLog(status int) func(...interface{}) {
	if status >= 200 && status < 300 {
		return AuxLog
	}
	return Log
}

// granted is the first-pass gate: does any capability entry name this
// method? Matches both bare ("fetch") and qualified ("secret:openweather")
// forms — the per-method handler does the qualifier check when needed
// (handleSecret checks the specific name).
func (h *SandboxHook) granted(method string) bool {
	prefix := method + ":"
	for _, c := range h.Capabilities {
		if c == method || strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// grantedSecret enforces per-credential gating. Only entries of the
// exact form "secret:<name>" grant access — bare "secret" is not
// honored even if it slipped past authoring, so a misconfigured tool
// fails closed rather than silently exposing every credential.
func (h *SandboxHook) grantedSecret(name string) bool {
	needle := "secret:" + name
	for _, c := range h.Capabilities {
		if c == needle {
			return true
		}
	}
	return false
}

// grantedFetchVia enforces per-credential gating for fetch_via.
// Only "fetch_via:<credential>" entries grant access; bare
// "fetch_via" is rejected the same way bare "secret" is — every
// credential must be named explicitly so the tool record self-
// documents which endpoints it can reach. The credential's own
// AllowedURLPattern then bounds which URLs within that credential
// the script can actually hit.
func (h *SandboxHook) grantedFetchVia(credName string) bool {
	needle := "fetch_via:" + credName
	for _, c := range h.Capabilities {
		if c == needle {
			return true
		}
	}
	return false
}

// --- method handlers ---

// handleFetch proxies an HTTP request through gohort's network
// stack. Capped at 10MiB response and 30s default timeout — the
// sandbox can't escalate either. The request's ctx derives from
// the session's NetworkConnector so a Private toggle mid-flight
// CANCELS this call (returns context.Canceled to the script)
// rather than letting it complete.
func (h *SandboxHook) handleFetch(conn net.Conn, params map[string]interface{}) {
	rawURL, _ := params["url"].(string)
	if rawURL == "" {
		writeHookError(conn, "fetch requires url")
		return
	}
	// Same URL validation + SSRF guard the LLM-callable fetch_url tool
	// uses (tools/websearch/websearch.go). Keeps the two surfaces 1:1
	// on what they accept — a script can't reach hosts the LLM tool
	// refuses, and vice versa. Only the OUTPUT shape differs (script
	// gets raw body in a dict; LLM gets extracted text with a preamble
	// wrap). See feedback_shell_tool_symmetry.
	parsed, err := neturl.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeHookError(conn, "fetch refused: url must be an http:// or https:// URL"+SameOriginURLHint(rawURL))
		return
	}
	// Refuse URLs with raw whitespace — almost always an unencoded
	// f-string substitution like f"...?name={city}" where city is
	// "Santa Cruz". Go's http.Client will accept and forward the
	// request but the upstream server typically responds with a 400
	// + empty body / HTML error page, and the script then crashes
	// downstream with JSONDecodeError when it tries to parse the
	// empty body. Catch here so the error is directive instead of
	// cryptic. The fix is urllib.parse.quote() on user-supplied
	// values before stitching into the URL.
	for i := 0; i < len(rawURL); i++ {
		c := rawURL[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			writeHookError(conn, fmt.Sprintf("fetch refused: URL contains unescaped whitespace at position %d (%q) — likely an unencoded f-string param. Wrap user-supplied values with urllib.parse.quote() before stitching them into the URL: from urllib.parse import quote; url = f\"...?q={quote(city)}\"", i, rawURL))
			return
		}
	}
	// Credential auto-route — symmetry with the LLM-callable fetch_url
	// (tools/websearch): a plain fetch to a credential-covered host dispatches
	// THROUGH the credential (auth injected server-side) instead of going out
	// anonymous and 401'ing. Lets a script rely on gohort.fetch / fetch_url for
	// covered APIs without declaring fetch_via — the common case just works.
	// NOTE: this intentionally does NOT require a fetch_via:<cred> capability
	// (matching the LLM tool, which has no per-credential gate); the credential
	// still bounds sends to its own host + AllowedURLPattern and logs to its
	// audit ledger.
	//
	// This runs BEFORE the non-public-host refusal below: a covered INTERNAL
	// host (a self-hosted API on .local / a private IP that an admin registered
	// as a credential's BaseURL) must dispatch through that credential, exactly
	// as the LLM-callable fetch_url_<cred> tool already does. Refusing it here
	// on the "non-public host" rule — before the auto-route — is the bug that
	// made an internal credential unreachable from a script once its host got
	// scoped behind a credential. Only hosts covered by NO credential fall
	// through to the SSRF refusal.
	if h != nil && h.Sess != nil {
		if credName, rerr := Secure().AutoRouteCredential(rawURL); rerr != nil {
			writeHookError(conn, rerr.Error())
			return
		} else if credName != "" && h.Sess.CredentialDenied(credName) {
			// Credential scope: mirror the LLM fetch_url — a covered host whose
			// credential is denied for this agent is blocked, not routed, so a
			// script can't bypass the scope pill by fetching the host directly.
			writeHookError(conn, "fetch blocked: host is served by credential \""+credName+"\", which this agent is not allowed to use (revoked in its credential scope)")
			return
		} else if credName != "" {
			method := "GET"
			if m, ok := params["method"].(string); ok && m != "" {
				method = strings.ToUpper(strings.TrimSpace(m))
			}
			args := map[string]interface{}{"url": rawURL, "method": method}
			if b, ok := params["body"].(string); ok && b != "" {
				args["body"] = b
			}
			Log("[hook/fetch] auto-routing credential-covered URL via %q: %s", credName, rawURL)
			out, derr := Secure().DispatchToolCallArgs(h.Sess, credName, args)
			if derr != nil {
				writeHookError(conn, "fetch (credential "+credName+"): "+derr.Error())
				return
			}
			// DispatchToolCallArgs returns "HTTP <code> <text>\n<body>". Parse the
			// numeric code so the script keeps the plain-fetch {status:int} shape.
			status, respBody := 200, out
			if strings.HasPrefix(out, "HTTP ") {
				line := out
				if nl := strings.IndexByte(out, '\n'); nl >= 0 {
					line, respBody = out[:nl], out[nl+1:]
				}
				if f := strings.Fields(line); len(f) >= 2 {
					if code, err := strconv.Atoi(f[1]); err == nil {
						status = code
					}
				}
			}
			writeHookResult(conn, map[string]interface{}{
				"status":  status,
				"headers": map[string]string{"X-Gohort-Fetched-Via": "credential:" + credName},
				"body":    respBody,
			})
			return
		}
	}
	// Non-public-host refusal — SSRF guard, now AFTER the credential auto-route
	// above so a covered internal host has already been dispatched. Anything
	// reaching here is covered by NO credential, so a private IP / .local /
	// .internal target is a genuine SSRF attempt and is refused, same rule as
	// the LLM-callable fetch_url tool.
	if host := parsed.Hostname(); host != "" && IsNonPublicHost(host) {
		writeHookError(conn, fmt.Sprintf("fetch refused: refusing to reach non-public host %s — same rule as the LLM-callable fetch_url tool", host))
		return
	}

	// JS-heavy auto-route — match fetch_url's behavior so a URL that
	// works there also works in a script. When the host needs a real
	// browser (Reddit, Twitter/X, etc. — see core/js_domains.go),
	// dispatch through browse_page and return its raw output as body.
	// No preamble, no "Fetched X (N chars)" wrapper — same shape the
	// script would have received if it called gohort.browse_page(url)
	// directly. Status synthesized as 200 so the caller's normal
	// `if status != 200` guard passes; a special header marks the
	// route taken so anyone curious can see what happened. Plain-HTTP
	// failure falls through; never harder than plain fetch alone.
	if ShouldAutoBrowseURL(rawURL) && BrowserFetchFunc != nil {
		Log("[hook/fetch] auto-routing JS-heavy URL via browse_page: %s", rawURL)
		callStart := time.Now()
		// Use the same byte cap as plain HTTP (10 MiB). Scripts process
		// the body programmatically; the 10000-char cap that fetch_url's
		// LLM-callable path uses is sized for LLM token budgets and is
		// too tight when a script asks for, e.g., a Reddit listing's
		// JSON (often 50-100KB for 25 posts).
		out, runErr := BrowserFetchFunc(rawURL, 10*1024*1024)
		if runErr != nil {
			Log("[hook/fetch] browse_page auto-route failed for %s: %v (elapsed=%s) — falling through to plain HTTP", rawURL, runErr, time.Since(callStart).Round(time.Millisecond))
		} else {
			Log("[hook/fetch] browse_page auto-route done elapsed=%s chars=%d", time.Since(callStart).Round(time.Millisecond), len(out))
			writeHookResult(conn, map[string]interface{}{
				"status":  200,
				"headers": map[string]string{"X-Gohort-Fetched-Via": "browse_page"},
				"body":    out,
			})
			return
		}
	}
	url := rawURL
	method := "GET"
	if m, ok := params["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}
	var body io.Reader
	if b, ok := params["body"].(string); ok && b != "" {
		body = strings.NewReader(b)
	}
	timeout := 30 * time.Second
	if t, ok := params["timeout"].(float64); ok && t > 0 && t < 300 {
		timeout = time.Duration(t * float64(time.Second))
	}
	// Derive a cancel ctx from the session connector so SetAllowed(false)
	// cancels this request mid-flight. Then layer the per-call timeout
	// on top so the request still respects its own deadline cap.
	var connector *NetworkConnector
	if h != nil && h.Sess != nil {
		connector = h.Sess.Network
	}
	baseCtx, releaseConn := connector.DeriveCancelCtx(context.Background())
	defer releaseConn()
	ctx, cancelTimeout := context.WithTimeout(baseCtx, timeout)
	defer cancelTimeout()

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		writeHookError(conn, "build request: "+err.Error())
		return
	}
	// Default headers. UA matches fetch_url's browser-shaped article
	// path so anti-bot WAFs treat both surfaces identically. Accept
	// MUST be wide (*/*) — narrowing it to "text/html,..." causes
	// content-negotiating CDNs (Reddit's i.redd.it, etc.) to serve
	// the HTML wrapper instead of the actual file when the URL
	// points at binary content (.jpg, .png, etc.). curl's default is
	// also */*, which is why curl works against those endpoints when
	// our previous Accept didn't. Caller-supplied request_headers
	// below override either.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if hh, ok := params["headers"].(map[string]interface{}); ok {
		// Same raw-auth-header guard as the LLM-callable fetch_url
		// (tool/shell symmetry): a script inlining a key to a
		// credential-covered host must go through fetch_via instead.
		if gerr := CredentialAuthGuard(url, hh); gerr != nil {
			writeHookError(conn, gerr.Error())
			return
		}
		for k, v := range hh {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}
	// NewBoundedHTTPClient applies the operator-configured Network
	// Timeouts (HTTPConnectTimeout for dial/TLS, HTTPRequestTimeout
	// for response-headers / TTFB). Matches the rest of gohort's
	// outbound HTTP — fetch_url, source hooks, etc. — so a dead host
	// fails at the configured request-timeout instead of stalling
	// until the script-side socket times out.
	client := NewBoundedHTTPClient()
	Log("[hook/fetch] start %s %s (timeout=%s)", method, url, timeout)
	callStart := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		Log("[hook/fetch] done elapsed=%s error=%v", time.Since(callStart).Round(time.Millisecond), err)
		writeHookError(conn, "fetch: "+err.Error())
		return
	}
	defer resp.Body.Close()
	// iotimeout wraps the body with an IDLE-read deadline (resets on
	// every successful read). A slow-but-steady stream completes;
	// a stalled one fails fast. Same pattern the LLM clients use to
	// avoid hangs on slow servers. Sized to the call's timeout so the
	// idle window matches the caller's expectation.
	resp.Body = iotimeout.NewReadCloser(resp.Body, timeout)
	// save_to path — stream the response body straight to a workspace
	// file instead of returning bytes through the JSON envelope. Right
	// shape for binary downloads (images, PDFs, audio, video). Body in
	// the return dict becomes a short metadata line so the script's
	// downstream code can still introspect status / content-type
	// without expecting the actual bytes. Caps at 100MB to match the
	// LLM-callable fetch_url's save cap.
	if saveTo, ok := params["save_to"].(string); ok && strings.TrimSpace(saveTo) != "" {
		saveTo = strings.TrimSpace(saveTo)
		wsDir := ""
		if h != nil && h.Sess != nil {
			wsDir = strings.TrimSpace(h.Sess.WorkspaceDir)
		}
		if wsDir == "" {
			writeHookError(conn, "save_to requires a workspace — call from a tool that has WorkspaceDir set")
			return
		}
		// Resolve save_to into a workspace path. Accept either:
		//   1. a workspace-relative path ("meme.png", "out/img.jpg") — joined to wsDir
		//   2. an absolute path that falls UNDER the workspace dir — rewritten to its relative form
		// Reject anything that escapes (absolute outside workspace, "..", symlink traversal).
		// Auto-accepting absolute paths inside the workspace removes a class
		// of Builder retries — scripts often build absolute paths like
		// f"{os.environ['workspace']}/meme.png" expecting them to "just work."
		var savePath string
		if filepath.IsAbs(saveTo) {
			cleanedSave := filepath.Clean(saveTo)
			cleanedWS := filepath.Clean(wsDir)
			rel, relErr := filepath.Rel(cleanedWS, cleanedSave)
			if relErr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
				writeHookError(conn, "save_to absolute path must fall under the workspace dir (or pass a workspace-relative path)")
				return
			}
			savePath = cleanedSave
			saveTo = rel
		} else {
			if strings.Contains(saveTo, "..") {
				writeHookError(conn, "save_to must not contain parent-traversal segments")
				return
			}
			savePath = filepath.Join(wsDir, saveTo)
		}
		if err := os.MkdirAll(filepath.Dir(savePath), 0700); err != nil {
			writeHookError(conn, "create parent dir for save_to: "+err.Error())
			return
		}
		f, ferr := os.Create(savePath)
		if ferr != nil {
			writeHookError(conn, "save_to create: "+ferr.Error())
			return
		}
		written, werr := io.Copy(f, io.LimitReader(resp.Body, 100*1024*1024+1))
		f.Close()
		if werr != nil {
			os.Remove(savePath)
			writeHookError(conn, "save_to write: "+werr.Error())
			return
		}
		if written > 100*1024*1024 {
			os.Remove(savePath)
			writeHookError(conn, "save_to: response exceeded 100MB cap")
			return
		}
		fetchDoneLog(resp.StatusCode)("[hook/fetch] done elapsed=%s status=%d saved=%dB path=%s", time.Since(callStart).Round(time.Millisecond), resp.StatusCode, written, saveTo)
		headers := make(map[string]string, len(resp.Header))
		for k, vs := range resp.Header {
			if len(vs) > 0 {
				headers[k] = vs[0]
			}
		}
		writeHookResult(conn, map[string]interface{}{
			"status":  resp.StatusCode,
			"headers": headers,
			"body":    fmt.Sprintf("[Saved %d bytes to %s]", written, saveTo),
			"path":    saveTo,
			"bytes":   written,
		})
		return
	}
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		Log("[hook/fetch] done elapsed=%s error=%v (read error after status=%d)", time.Since(callStart).Round(time.Millisecond), err, resp.StatusCode)
		writeHookError(conn, "read response: "+err.Error())
		return
	}
	// Log status + body size so a future "empty body" diagnostic
	// doesn't require sandbox-side print debugging. A 200 with 0
	// bytes is rare but legitimate; a 4xx/5xx with 0 bytes is the
	// "site blocked us" shape — both are now visible in the trace.
	fetchDoneLog(resp.StatusCode)("[hook/fetch] done elapsed=%s status=%d bytes=%d", time.Since(callStart).Round(time.Millisecond), resp.StatusCode, len(respBody))
	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	writeHookResult(conn, map[string]interface{}{
		"status":  resp.StatusCode,
		"headers": headers,
		"body":    string(respBody),
	})
}

// handleBrowsePage routes a script-side browse_page call through the
// framework's BrowsePageTool — a real headless Chromium that executes
// JavaScript and handles cookies. This is the script-callable
// counterpart of the LLM's browse_page tool: when a Python script's
// gohort.fetch(url) comes back blocked (403, captcha, Cloudflare
// interstitial, JS-required skeleton), the script can fall through to
// gohort.browse_page(url) for the same URL without leaving the sandbox.
//
// Capability gate: requires "browse_page" in HookCapabilities. The
// Private-mode network gate above also covers this (browse_page makes
// real outbound requests). Cost: 5-20s per call + Chromium overhead,
// so scripts should use fetch() as the default and reach for this only
// on a block.
//
// Returns a string of extracted text (up to ~10000 chars, matching the
// LLM-facing tool's cap). Failure returns a hook error with the
// underlying Chromium error.
func (h *SandboxHook) handleBrowsePage(conn net.Conn, params map[string]interface{}) {
	rawURL, _ := params["url"].(string)
	if rawURL == "" {
		writeHookError(conn, "browse_page requires url")
		return
	}
	// Same URL validation + SSRF guard as gohort.fetch_url. browse_page
	// IS a fetch backend — Chromium dialing out to a URL — so the same
	// guards apply: http/https only, no loopback/private/localhost. The
	// LLM-callable browse_page tool has its own SSRF guard inside Run;
	// duplicating it here keeps the failure mode consistent (same
	// error message, same error path) and dodges a Chromium launch
	// for a URL we'd have refused anyway.
	parsed, err := neturl.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeHookError(conn, "browse_page refused: url must be an http:// or https:// URL"+SameOriginURLHint(rawURL))
		return
	}
	if err := RefuseNonPublicHost(rawURL); err != nil {
		writeHookError(conn, "browse_page refused: "+err.Error()+" — same rule as the LLM-callable browse_page tool")
		return
	}
	// Same whitespace guard as fetch — almost always an unencoded
	// f-string substitution; surface a clear error instead of a
	// confusing browser timeout.
	for i := 0; i < len(rawURL); i++ {
		c := rawURL[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			writeHookError(conn, fmt.Sprintf("browse_page refused: URL contains unescaped whitespace at position %d (%q) — likely an unencoded f-string param. Wrap user-supplied values with urllib.parse.quote() before stitching them into the URL.", i, rawURL))
			return
		}
	}
	if BrowserFetchFunc == nil {
		writeHookError(conn, "browse_page not available (browser package not linked into this build)")
		return
	}
	Log("[hook/browse_page] start %s", rawURL)
	callStart := time.Now()
	// Call BrowserFetchFunc directly — returns raw text. BrowsePageTool.Run
	// would wrap with "Fetched X via browser (N chars):" preamble, which is
	// the LLM-shaped surface, not what a script wants. See
	// feedback_shell_tool_symmetry.
	//
	// Cap at 10 MiB (same as plain HTTP) — scripts process bytes; the
	// 10000-char cap the LLM-callable BrowsePageTool.Run uses is sized
	// for LLM token budgets and would chop a verbose Reddit JSON
	// listing in half. Script side gets the full response.
	out, err := BrowserFetchFunc(rawURL, 10*1024*1024)
	if err != nil {
		Log("[hook/browse_page] done elapsed=%s error=%v", time.Since(callStart).Round(time.Millisecond), err)
		writeHookError(conn, "browse_page: "+err.Error())
		return
	}
	Log("[hook/browse_page] done elapsed=%s chars=%d", time.Since(callStart).Round(time.Millisecond), len(out))
	// Return shape matches gohort.fetch_url's {status, headers, body}
	// so the script-side parsing pattern is identical:
	//   result = browse_page(url)
	//   if result["status"] != 200: ...
	//   text = result["body"]
	writeHookResult(conn, map[string]interface{}{
		"status":  200,
		"headers": map[string]string{"X-Gohort-Fetched-Via": "browse_page"},
		"body":    out,
	})
}

// handleHookLog routes script-side log messages through gohort's log
// stream. Level maps to Debug / Log / Err so a misbehaving script
// surfaces in the right channel.
func handleHookLog(conn net.Conn, params map[string]interface{}) {
	msg, _ := params["msg"].(string)
	if msg == "" {
		writeHookError(conn, "log requires msg")
		return
	}
	level := strings.ToLower(strings.TrimSpace(stringFromParams(params, "level")))
	switch level {
	case "error", "err":
		Err(fmt.Errorf("[hook/script] %s", msg))
	case "warn", "warning":
		Log("[hook/script WARN] %s", msg)
	case "debug":
		Debug("[hook/script] %s", msg)
	default:
		Log("[hook/script] %s", msg)
	}
	writeHookResult(conn, map[string]bool{"ok": true})
}

// handleSecret looks up a registered credential's decrypted secret
// and returns it as a string. The credential name must be explicitly
// listed in the tool's HookCapabilities as "secret:<name>" — bare
// "secret" without a name doesn't grant access. Audit-logged at Log
// level (the value itself is NEVER logged, only the credential name).
//
// Credentials of type "no_auth" have no stored secret by design;
// they're rejected with a clear message rather than returning an
// empty string (which the script would then silently inject into a
// header, producing a confusing 401 from the real API).
func (h *SandboxHook) handleSecret(conn net.Conn, params map[string]interface{}) {
	name, _ := params["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		writeHookError(conn, "secret requires name")
		return
	}
	if !h.grantedSecret(name) {
		Log("[hook/secret] DENIED %q (not in capabilities=%v)", name, h.Capabilities)
		writeHookError(conn, fmt.Sprintf("secret %q not granted to this tool — add \"secret:%s\" to hook_capabilities", name, name))
		return
	}
	sec := Secure()
	// Owner-aware: a user's own My-API-credentials entry resolves for that
	// user's tools, not just global (admin) credentials. Falls back to
	// global when the session has no username.
	owner := ""
	if h.Sess != nil {
		owner = h.Sess.Username
	}
	cred, ok := sec.Resolve(name, owner)
	if !ok {
		writeHookError(conn, fmt.Sprintf("no credential named %q registered — register it via Extensions > API credentials (or Admin > APIs) first", name))
		return
	}
	if cred.Secured {
		// Secured credentials NEVER hand out the raw secret — even to a bound
		// tool — so the key can't be exfiltrated by tool code. Server-side
		// dispatch only: use fetch_via, which applies auth on the server and
		// returns just the response. (A tool that used secret:<name> before the
		// credential was secured must be reworked to fetch_via to keep working.)
		Log("[hook/secret] DENIED %q — credential is SECURED (fetch_via-only)", name)
		writeHookError(conn, fmt.Sprintf("credential %q is SECURED — the raw secret is never returned. Use fetch_via:%s (server-side dispatch: auth is applied on the server and the script never sees the secret) instead of secret:%s", name, name, name))
		return
	}
	if cred.Type == SecureCredNone {
		writeHookError(conn, fmt.Sprintf("credential %q is no_auth — it has no stored secret to return", name))
		return
	}
	secret, ok := sec.loadSecret(credStoreKey(cred.Owner, cred.Name))
	if !ok || secret == "" {
		writeHookError(conn, fmt.Sprintf("credential %q has no stored secret value (was it created without one?)", name))
		return
	}
	Log("[hook/secret] %q (type=%s, %dB) → script", name, cred.Type, len(secret))
	writeHookResult(conn, map[string]string{"secret": secret, "type": cred.Type})
}

// handleFetchVia routes an HTTP call through a named credential's
// Secure.Dispatch path. The credential's AllowedURLPattern bounds
// which URLs are reachable, the credential's auth (bearer / header /
// query / basic) gets injected server-side, the call is rate-limited
// + audit-logged via the same machinery api-mode tools use, and the
// response body comes back to the script as text. The script NEVER
// sees the credential's secret value.
//
// Use over plain fetch when:
//   - you want per-tool URL scoping (a tool granted fetch_via:reddit
//     can ONLY hit URLs matching reddit's pattern)
//   - the endpoint needs auth (bearer / query token) and you'd rather
//     not handle the secret yourself
//   - audit / rate-limit policy from the credential record should apply
//
// For unauth, public endpoints, register a no_auth credential with
// an AllowedURLPattern scoping which public endpoints are reachable,
// then grant fetch_via:<that-name>. The script gets gohort's full
// audit trail without the credential machinery injecting any auth.
func (h *SandboxHook) handleFetchVia(conn net.Conn, params map[string]interface{}) {
	credName, _ := params["credential"].(string)
	credName = strings.TrimSpace(credName)
	if credName == "" {
		writeHookError(conn, "fetch_via requires credential")
		return
	}
	if !h.grantedFetchVia(credName) {
		Log("[hook/fetch_via] DENIED credential=%q (not in capabilities=%v)", credName, h.Capabilities)
		writeHookError(conn, fmt.Sprintf("fetch_via credential %q not granted to this tool — add \"fetch_via:%s\" to hook_capabilities", credName, credName))
		return
	}
	// Secured-credential enforcement: a secured cred is reachable only by the users
	// it's shared with (WHO) AND through APPROVED declaring tools (WHAT). Grandfathers
	// a legacy declaring tool, refuses a revoked one. Open creds pass through here
	// (their WHO is enforced at tool-build via the deny set).
	securedUser := ""
	if h.Sess != nil {
		securedUser = h.Sess.Username
	}
	if err := Secure().EnforceSecuredBinding(credName, h.ToolName, securedUser); err != nil {
		Log("[hook/fetch_via] binding refused credential=%q tool=%q: %v", credName, h.ToolName, err)
		writeHookError(conn, err.Error())
		return
	}
	url, _ := params["url"].(string)
	url = strings.TrimSpace(url)
	if url == "" {
		writeHookError(conn, "fetch_via requires url")
		return
	}
	method := "GET"
	if m, ok := params["method"].(string); ok && m != "" {
		method = strings.ToUpper(strings.TrimSpace(m))
	}
	body, _ := params["body"].(string)
	// Optional caller headers (e.g. Depth for CalDAV PROPFIND). Routed
	// through the args entry point so they land as request_headers —
	// symmetric with api-mode tools and fetch_url. Credential auth is
	// still injected server-side and overrides any caller auth header.
	args := map[string]any{"url": url, "method": method}
	if body != "" {
		args["body"] = body
	}
	if hdrs, ok := params["headers"].(map[string]interface{}); ok && len(hdrs) > 0 {
		args["request_headers"] = hdrs
	}
	Log("[hook/fetch_via] start %s %s via %q", method, url, credName)
	callStart := time.Now()
	out, err := Secure().DispatchToolCallArgs(h.Sess, credName, args)
	if err != nil {
		Log("[hook/fetch_via] done elapsed=%s error=%v", time.Since(callStart).Round(time.Millisecond), err)
		writeHookError(conn, "fetch_via: "+err.Error())
		return
	}
	Log("[hook/fetch_via] done elapsed=%s bytes=%d", time.Since(callStart).Round(time.Millisecond), len(out))
	// DispatchToolCall returns "<status>\n<body>" or similar status-
	// prefixed text. Split the first line as the status hint and
	// return both pieces structured so the script doesn't have to
	// parse the prefix itself.
	statusLine := ""
	respBody := out
	if idx := strings.IndexByte(out, '\n'); idx >= 0 {
		first := strings.TrimSpace(out[:idx])
		if len(first) <= 100 {
			statusLine = first
			respBody = out[idx+1:]
		}
	}
	// "status" is the numeric code, matching fetch_url / browse_page and the
	// documented guard `if result["status"] != 200`. It used to be the raw
	// status LINE ("HTTP 207 Multi-Status"), so the same comparison that
	// worked with fetch_url silently failed here — authored scripts ended up
	// carrying `int(r['status'].split()[1]) if isinstance(r['status'], str)
	// else r['status']` to paper over the difference. The full line stays
	// available as status_line for anything that wants the reason phrase.
	writeHookResult(conn, map[string]interface{}{
		"status":      statusCodeFromLine(statusLine),
		"status_line": statusLine,
		"body":        respBody,
	})
}

// statusCodeFromLine pulls the numeric code out of a status line like
// "HTTP 207 Multi-Status" (the shape SecureAPI.DispatchToolCall prefixes onto
// a response). Returns 0 when there's no code to find, which reads as "not
// 2xx" in the standard `if result["status"] != 200` guard rather than
// masquerading as success.
func statusCodeFromLine(line string) int {
	for _, f := range strings.Fields(line) {
		if n, err := strconv.Atoi(f); err == nil && n >= 100 && n <= 599 {
			return n
		}
	}
	return 0
}

// --- wire helpers ---

// IsNonPublicHost reports whether a hostname is a loopback/private/link-local IP
// or a non-public suffix (localhost / .local / .internal) — the SSRF rule the
// sandbox fetch hook enforces. Exposed so other SERVER-SIDE fetch paths (e.g. the
// outbound-attachment URL fetch) apply the identical guard. An empty host is
// treated as non-public (refuse).
func IsNonPublicHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
	}
	lower := strings.ToLower(host)
	return lower == "localhost" || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal")
}

func writeHookResult(conn net.Conn, result interface{}) {
	b, _ := json.Marshal(map[string]interface{}{"result": result})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		Log("[hook] write result failed: %v (response_bytes=%d) — script side may have hung or disconnected", err, len(b)+1)
	}
}

func writeHookError(conn net.Conn, msg string) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		Log("[hook] write error failed: %v (orig_error=%q) — script side may have hung or disconnected", err, msg)
	}
}

func stringFromParams(p map[string]interface{}, key string) string {
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}

func paramKeySummary(p map[string]interface{}) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	return strings.Join(keys, ",")
}

func randomHookToken() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- Python helper ---

// The two in-sandbox mount paths those shims are reached through
// (SandboxGohortLibMountPath / SandboxGohortBinMountPath) moved out with the
// confinement mechanics — where something is mounted inside a sandbox is a fact
// about the sandbox, and the mount args are built there. core.go re-exports them
// under the same names, so this file's use of them is unchanged.

// sandboxToolShim is one executable file dropped into the bin dir.
type sandboxToolShim struct {
	name    string
	content string
}

// sandboxToolShims are written (0755) into <lib>/bin by EnsureGohortLibDir.
// _gohort_shim.py carries the logic; the three bare-named wrappers are what
// a script actually invokes (fetch_url, fetch_via, browse_page). A wrapper
// runs from the bin dir, so `from _gohort_shim import main` resolves the
// sibling, and `import gohort` inside it resolves via PYTHONPATH.
var sandboxToolShims = []sandboxToolShim{
	{"_gohort_shim.py", gohortShimDispatcher},
	{"fetch_url", "#!/usr/bin/env python3\nimport sys\nfrom _gohort_shim import main\nraise SystemExit(main(\"fetch_url\", sys.argv[1:]))\n"},
	{"fetch_via", "#!/usr/bin/env python3\nimport sys\nfrom _gohort_shim import main\nraise SystemExit(main(\"fetch_via\", sys.argv[1:]))\n"},
	{"browse_page", "#!/usr/bin/env python3\nimport sys\nfrom _gohort_shim import main\nraise SystemExit(main(\"browse_page\", sys.argv[1:]))\n"},
}

// SandboxReachableNames returns the identifiers a sandboxed script may
// legitimately name to reach gohort: the PATH shims written into <lib>/bin,
// plus the hook methods `from gohort import ...` exposes.
//
// Callers that flag a shell command for naming an LLM tool MUST exclude these.
// They are the one case where a gohort name inside a shell command is correct,
// and refusing them would break the documented iterate-and-test flow (a script
// under test calls fetch_url exactly as the authored tool will at dispatch).
func SandboxReachableNames() map[string]bool {
	out := make(map[string]bool, len(sandboxToolShims)+8)
	for _, s := range sandboxToolShims {
		out[strings.TrimSuffix(s.name, ".py")] = true
	}
	// The hook surface itself — see SandboxHookPythonShim's import forms.
	for _, n := range []string{"fetch", "fetch_url", "fetch_via", "browse_page", "secret", "log", "gohort", "HookError"} {
		out[n] = true
	}
	return out
}

// gohortShimDispatcher is the shared logic behind the bin-dir wrappers. It
// parses a small curl-ish arg surface (--url / --method / --header / --body
// / --credential, or a bare positional URL), calls the matching gohort hook
// method, prints the response body to stdout, and exits non-zero on a non-2xx
// upstream status so a shell caller can branch on it. No backticks (keeps
// this a clean Go raw string).
const gohortShimDispatcher = `#!/usr/bin/env python3
# gohort tool shims: let a sandboxed script call gohort's fetch family as
# ordinary commands -- fetch_url / fetch_via / browse_page -- instead of
# subprocessing an LLM tool name (fetch_url_<cred>, send_message, ...),
# which is NOT a shell binary and only fails with FileNotFoundError. These
# proxy to the SAME hook that "from gohort import ..." uses, so the hook
# still enforces this tool's granted capabilities. No new privilege.
import sys
import json

try:
    import gohort
except Exception as e:
    sys.stderr.write("gohort shim: cannot import gohort helper (%s)\n" % e)
    raise SystemExit(2)

_USAGE = {
    "fetch_url": "fetch_url [--credential NAME] [--method M] [--header 'K: V'] [--body DATA] URL",
    "fetch_via": "fetch_via CREDENTIAL [--method M] [--header 'K: V'] [--body DATA] URL",
    "browse_page": "browse_page URL",
}


def _die(msg, code=2):
    sys.stderr.write(str(msg).rstrip("\n") + "\n")
    return code


def _val(argv, i):
    return argv[i + 1] if i + 1 < len(argv) else ""


def _parse(argv):
    url = None
    method = "GET"
    body = None
    cred = None
    headers = {}
    positional = []
    i = 0
    n = len(argv)
    while i < n:
        a = argv[i]
        if a in ("--url", "-u"):
            url = _val(argv, i); i += 2; continue
        if a in ("--credential", "--cred", "-c"):
            cred = _val(argv, i); i += 2; continue
        if a in ("--method", "-X"):
            method = _val(argv, i).upper() or "GET"; i += 2; continue
        if a in ("--body", "--data", "-d"):
            body = _val(argv, i); i += 2; continue
        if a in ("--header", "-H"):
            h = _val(argv, i); i += 2
            if ":" in h:
                k, v = h.split(":", 1)
                headers[k.strip()] = v.strip()
            continue
        positional.append(a)
        i += 1
    if url is None and positional:
        url = positional[0]
    return url, method, body, cred, headers


def main(op, argv):
    argv = list(argv)
    if op == "fetch_via":
        cred = None
        if argv and not argv[0].startswith("-"):
            cred = argv[0]
            argv = argv[1:]
        url, method, body, credflag, headers = _parse(argv)
        cred = cred or credflag
        if not cred or not url:
            return _die("usage: " + _USAGE["fetch_via"])
        try:
            r = gohort.fetch_via(cred, url, method=method, body=body, headers=headers)
        except Exception as e:
            return _die("fetch_via error: %s" % e)
    elif op == "browse_page":
        url, _, _, _, _ = _parse(argv)
        if not url:
            return _die("usage: " + _USAGE["browse_page"])
        try:
            r = gohort.browse_page(url)
        except Exception as e:
            return _die("browse_page error: %s" % e)
    else:
        url, method, body, cred, headers = _parse(argv)
        if not url:
            return _die("usage: " + _USAGE["fetch_url"])
        try:
            if cred:
                r = gohort.fetch_via(cred, url, method=method, body=body, headers=headers)
            else:
                r = gohort.fetch_url(url, method=method, headers=headers, body=body)
        except Exception as e:
            return _die("fetch_url error: %s" % e)
    if isinstance(r, dict):
        body_out = r.get("body", "")
        status = r.get("status")
    else:
        body_out = r
        status = None
    if not isinstance(body_out, str):
        body_out = json.dumps(body_out)
    sys.stdout.write(body_out)
    if not body_out.endswith("\n"):
        sys.stdout.write("\n")
    if isinstance(status, int) and not (200 <= status < 300):
        sys.stderr.write("gohort: upstream HTTP %s\n" % status)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1] if len(sys.argv) > 1 else "fetch_url", sys.argv[2:]))
`

// gohortLibDirOnce caches the host-side path to the gohort library
// dir so EnsureGohortLibDir doesn't redo the filesystem work on
// every sandbox dispatch.
var (
	gohortLibDirMu   sync.Mutex
	gohortLibDirPath string
	gohortLibWarned  bool // one loud line per process, not per dispatch
)

// gohortLibUnavailable reports that the helper could not be deployed,
// once, at Log level.
//
// Everything here used to be best-effort and Debug-only, and one path
// (no workspaces dir) said nothing at all. That makes the failure
// invisible in exactly the way that matters: the only symptom reaching
// anyone is ModuleNotFoundError on the first line of a script, which
// names the SCRIPT's import rather than the deployment that never
// happened. An agent reads that as "this tool is broken", reports an
// environmental fault it cannot describe, and goes off to improvise an
// answer some other way — which is how a missing directory becomes a
// confidently wrong result three tool calls later.
func gohortLibUnavailable(why string, args ...any) {
	if gohortLibWarned {
		return
	}
	gohortLibWarned = true
	Log("[hook/helpers] the gohort python helper is NOT available to sandboxed tools: " + fmt.Sprintf(why, args...) +
		". Scripts using `from gohort import fetch_url` will fail with ModuleNotFoundError, and the tool will look broken rather than unconfigured.")
}

// EnsureGohortLibDir writes the gohort helper package to a host-side
// library directory (sibling of WorkspacesDir, prefixed with "_" so
// it can't be a valid user name) and returns the absolute path. The
// directory is bind-mounted READ-ONLY into every sandbox at
// SandboxGohortLibMountPath, with PYTHONPATH pointing there.
//
// Living OUTSIDE the workspace is the security property: an LLM
// authoring scripts can't see, write, or delete the helper because
// it's not in the workspace tree at all. Inside the sandbox the
// mount is RO so even a shell escape wouldn't write to it. Workspace
// listings (ls) show only user-created files; the framework helper
// is invisible.
//
// Idempotent: rewrites the __init__.py only when content differs
// from the embedded source. Returns the host path on success,
// empty string on failure (best-effort — the sandbox still works,
// just without gohort imports).
func EnsureGohortLibDir() string {
	gohortLibDirMu.Lock()
	defer gohortLibDirMu.Unlock()
	if gohortLibDirPath != "" {
		// Already populated this process; re-verify content hasn't
		// drifted (cheap stat + read).
		path := filepath.Join(gohortLibDirPath, "gohort", "__init__.py")
		if existing, err := os.ReadFile(path); err == nil {
			if string(existing) == SandboxHookPythonShim {
				return gohortLibDirPath
			}
			// Drifted — re-write below.
		}
	}
	base := WorkspacesDir()
	if base == "" {
		// The silent one: no log at any level lived here, so a
		// deployment with no workspaces dir configured produced a
		// helper that simply never existed, with nothing anywhere
		// saying so.
		gohortLibUnavailable("no workspaces directory is configured, so there is nowhere to deploy it")
		return ""
	}
	// Sibling of the workspaces dir: same parent, name "_gohort_lib".
	// User-id validation in EnsureWorkspaceDir rejects names with
	// `/`, `..`, etc., so a malicious "_gohort_lib" user-id can't
	// collide with this path either.
	libBase := filepath.Join(filepath.Dir(base), "_gohort_lib")
	pkgDir := filepath.Join(libBase, "gohort")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		gohortLibUnavailable("cannot create %s (%v)", pkgDir, err)
		return ""
	}
	path := filepath.Join(pkgDir, "__init__.py")
	needWrite := true
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == SandboxHookPythonShim {
			needWrite = false
		}
	}
	if needWrite {
		if err := os.WriteFile(path, []byte(SandboxHookPythonShim), 0644); err != nil {
			gohortLibUnavailable("cannot write %s (%v)", path, err)
			return ""
		}
		Debug("[hook/helpers] deployed gohort package (%dB) at %s (host) — mounted RO at %s (sandbox)", len(SandboxHookPythonShim), path, SandboxGohortLibMountPath)
	}
	ensureGohortShims(libBase)
	gohortLibDirPath = libBase
	return libBase
}

// ensureGohortShims writes the executable fetch-family shims into
// <libBase>/bin (mounted RO at SandboxGohortBinMountPath, on PATH). Content-
// idempotent like the package file; best-effort — a write failure just means
// scripts fall back to `from gohort import ...` (still works), so failures are
// logged at Debug, never fatal. The exec bit is re-asserted even on a content
// match so a prior umask-clobbered file self-heals.
func ensureGohortShims(libBase string) {
	binDir := filepath.Join(libBase, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		Debug("[hook/helpers] failed to mkdir shim bin %s: %v", binDir, err)
		return
	}
	for _, s := range sandboxToolShims {
		p := filepath.Join(binDir, s.name)
		if existing, err := os.ReadFile(p); err == nil && string(existing) == s.content {
			_ = os.Chmod(p, 0755)
			continue
		}
		if err := os.WriteFile(p, []byte(s.content), 0755); err != nil {
			Debug("[hook/helpers] failed to write shim %s: %v", p, err)
			continue
		}
		_ = os.Chmod(p, 0755) // WriteFile mode is pre-umask; force the exec bit
	}
}

// SandboxHookPythonShim is the canonical helper file, deployed as
// gohort.py. Every import shape works from this one source:
//
//	from gohort import fetch, secret, fetch_via, log
//	data = fetch("https://...")
//
//	from gohort import gohort                # singleton style
//	data = gohort.fetch("https://...")
//
//	import gohort                            # module dot access
//	data = gohort.fetch("https://...")
//
//	from gohort import HookError             # exception
//
// All three shapes resolve against this one file. There is no
// gohort_hook.py — old back-compat was retired since the small
// number of approved tools using it will be re-authored.
const SandboxHookPythonShim = `# gohort.py — script-side helper for sandboxed tool dispatches.
#
# Provides a narrow callback channel back to gohort for capabilities
# the tool was granted (fetch, log, secret, fetch_via). The sandbox
# runs with --unshare-net by default — raw HTTP from urllib / curl
# fails. Use this module's fetch() to do HTTP through gohort instead.
#
# Every reasonable Python import shape works:
#
#   from gohort import fetch_url                      # function style
#   data = fetch_url("https://api.example.com/x")
#
#   from gohort import gohort                         # singleton style
#   data = gohort.fetch_url("https://api.example.com/x")
#
#   import gohort                                     # module dot access
#   data = gohort.fetch_url("https://api.example.com/x")
#
# fetch is a back-compat alias for fetch_url — older authored tools
# still work; new scripts should use fetch_url to match the
# LLM-callable tool of the same name.
#
# If GOHORT_HOOK_PATH isn't set in env, the tool wasn't granted hook
# capabilities — calls raise HookError with a clear remediation hint.

import json
import os
import socket


class HookError(RuntimeError):
    pass


class _Gohort:
    def __init__(self):
        self._path = os.environ.get("GOHORT_HOOK_PATH")

    def _call(self, method, params=None):
        if not self._path:
            raise HookError(
                "GOHORT_HOOK_PATH not set — this tool was not granted hook "
                "capabilities. Re-author the tool with "
                "hook_capabilities=[\"fetch\", ...] to enable."
            )
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            # Per-I/O timeout (NOT a total operation budget). Set
            # generous enough to cover the longest server-side method
            # budget: fetch with timeout=300 → server deadline ~315s.
            # The total operation is bounded by whatever the request's
            # timeout param is set to + a small server buffer; this
            # cap is just defense against a wedged UDS connection.
            s.settimeout(330)
            s.connect(self._path)
            payload = (json.dumps({"method": method, "params": params or {}})
                       + "\n").encode("utf-8")
            s.sendall(payload)
            buf = b""
            while b"\n" not in buf:
                chunk = s.recv(65536)
                if not chunk:
                    break
                buf += chunk
            line = buf.split(b"\n", 1)[0]
            resp = json.loads(line.decode("utf-8"))
        finally:
            try:
                s.close()
            except Exception:
                pass
        if "error" in resp:
            raise HookError(resp["error"])
        if "result" not in resp:
            # A response carrying neither key used to return None, and None then
            # failed somewhere else entirely — json.loads(None) raises a
            # TypeError about NoneType with nothing pointing back here. Fail at
            # the call instead, naming the method.
            raise HookError(
                method + ": the host returned no result and no error. This is a "
                "framework fault, not a problem with your script — report it "
                "rather than working around it."
            )
        return resp["result"]

    def fetch_url(self, url, method="GET", headers=None, body=None, timeout=30, save_to=None):
        """HTTP request via gohort. Returns dict {status, headers, body}.
        body is a string. Capped at 10MiB response (text mode) or
        100MiB (save_to mode).

        Same name + same routing as the LLM-callable fetch_url tool:
        JS-heavy hosts (Reddit, Twitter/X, etc.) auto-route through
        browse_page; data-shaped URLs (.json, /api/, etc.) plain-HTTP.

        save_to: workspace-relative path OR an absolute path inside the
        workspace. When set, the response body streams straight to that
        file instead of being returned as a string. Right shape for
        binary downloads (images, PDFs, audio, video, archives) —
        strings mangle binary content. The returned dict's "body"
        becomes a short metadata line; "path" and "bytes" carry the
        saved (always workspace-relative) location and size.

        Standard guard:

            result = fetch_url(url)
            if result["status"] != 200:
                return f"upstream {result['status']}: {result['body'][:200]}"
            data = json.loads(result["body"])   # if JSON

        Image-download example:

            r = fetch_url(image_url, save_to="meme.png")
            if r["status"] != 200: ...
            # file now exists at <workspace>/meme.png; r["path"] = "meme.png"
        """
        return self._call("fetch", {
            "url": url,
            "method": method,
            "headers": headers or {},
            "body": body or "",
            "timeout": timeout,
            "save_to": save_to or "",
        })

    # fetch — back-compat alias for fetch_url. Older tools authored
    # before the rename still work. New scripts should use fetch_url
    # so the name matches the LLM-callable tool of the same name.
    def fetch(self, url, method="GET", headers=None, body=None, timeout=30):
        return self.fetch_url(url, method=method, headers=headers,
                              body=body, timeout=timeout)

    def log(self, msg, level="info"):
        """Route a message into gohort's log stream.
        level: debug, info, warn, error."""
        return self._call("log", {"level": level, "msg": str(msg)})

    def secret(self, name):
        """Return the decrypted secret for a registered credential.
        Tool must declare "secret:<name>" in hook_capabilities."""
        result = self._call("secret", {"name": name})
        return result["secret"] if isinstance(result, dict) else result

    def fetch_via(self, credential, url, method="GET", body=None, headers=None, request_headers=None):
        """HTTP via a named gohort credential: URL allowlist enforced,
        auth injected server-side, audit logged. Tool must declare
        "fetch_via:<credential>" in hook_capabilities. headers is an
        optional dict of extra request headers (e.g. {"Depth": "1"} for
        CalDAV PROPFIND); the credential's auth header always wins.
        request_headers is accepted as an alias for headers (the name
        api-mode tools use), so either spelling works.

        Returns {status, status_line, body} — status is the NUMERIC code,
        the same shape fetch_url returns, so the standard guard works:

            r = fetch_via("apple_caldav", url, method="PROPFIND",
                          body=xml, headers={"Depth": "1"})
            if r["status"] != 207: ...

        status_line carries the full "HTTP 207 Multi-Status" text when you
        want the reason phrase."""
        hdrs = dict(headers or {})
        if request_headers:
            hdrs.update(request_headers)
        return self._call("fetch_via", {
            "credential": credential,
            "url": url,
            "method": method,
            "body": body or "",
            "headers": hdrs,
        })

    def browse_page(self, url):
        """Load url in a real headless browser (Chromium) — JavaScript
        executed, cookies handled — and return dict {status, headers,
        body}, same shape as fetch_url(). body is the rendered page's
        readable text (up to ~10000 chars). Tool must declare
        "browse_page" in hook_capabilities.

        Same return shape and same SSRF guards as fetch_url() — only
        the underlying fetch engine differs (Chromium vs raw HTTP).
        Standard guard:

            result = browse_page(url)
            if result["status"] != 200: ...
            text = result["body"]

        Use directly when you KNOW the page needs JS (Reddit, Twitter/X,
        SPA news) — gohort.fetch_url already auto-routes these hosts
        through browse_page transparently, so you usually don't need to
        reach for browse_page yourself. Slow (5-20s) and heavier than
        fetch_url, so prefer fetch_url when the page might be static."""
        return self._call("browse_page", {"url": url})


gohort = _Gohort()


# Module-level function aliases — same operations, function-call style.
# Every method on the singleton has a matching free function here so
# the "from gohort import X" import shape works for any X the
# singleton exposes.
def fetch_url(url, method="GET", headers=None, body=None, timeout=30, save_to=None):
    return gohort.fetch_url(url, method=method, headers=headers,
                            body=body, timeout=timeout, save_to=save_to)


def fetch(url, method="GET", headers=None, body=None, timeout=30, save_to=None):
    return gohort.fetch_url(url, method=method, headers=headers,
                            body=body, timeout=timeout, save_to=save_to)


def browse_page(url):
    return gohort.browse_page(url)


def log(msg, level="info"):
    return gohort.log(msg, level=level)


def secret(name):
    return gohort.secret(name)


def fetch_via(credential, url, method="GET", body=None, headers=None, request_headers=None):
    return gohort.fetch_via(credential, url, method=method, body=body, headers=headers, request_headers=request_headers)
`
