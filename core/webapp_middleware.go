package core

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// streamingPaths holds URL suffixes whose handlers hold the connection
// open for extended periods (SSE event streams, websockets, live/
// reconnect loops). Snapshotting ProcessUsage over a long-lived stream
// would roll every unrelated LLM call that happened during the hold
// into this request's report, so we skip reporting entirely for these.
var streamingPaths = []string{
	"/api/events",
	"/api/live",
	"/api/reconnect",
	"/api/terminal",
}

// isStreamingPath reports whether the request path matches any
// registered long-lived endpoint suffix. Using suffix-match because
// every per-app endpoint is mounted under a prefix (e.g.
// "/servitor/api/terminal"), so an exact-match lookup never fires.
func isStreamingPath(path string) bool {
	for _, p := range streamingPaths {
		if path == p || strings.HasSuffix(path, p) {
			return true
		}
	}
	return false
}

// usageReportSkipKey identifies the context-stored flag the middleware
// checks before printing its end-of-request cost summary. Handlers
// that fire their own scoped report (UsageScope.Report etc.) flip the
// flag via MarkUsageReportHandled so the middleware doesn't print a
// near-duplicate on top of theirs.
type usageReportSkipKey struct{}

// MarkUsageReportHandled signals to UsageReportMiddleware that this
// request's cost has already been reported by the handler — skip the
// middleware's end-of-request log line. Idempotent and safe to call
// from multiple goroutines on the same request.
//
// Pattern at the handler:
//
//	defer MarkUsageReportHandled(r.Context())
//	defer scope.Report("my-op-"+id)()
//
// No-op when called on a context not wrapped by UsageReportMiddleware
// (e.g. background jobs, scheduled tasks).
func MarkUsageReportHandled(ctx context.Context) {
	if flag, ok := ctx.Value(usageReportSkipKey{}).(*atomic.Bool); ok {
		flag.Store(true)
	}
}

// UsageReportMiddleware wraps an http.Handler so per-request usage is
// tracked and reported at exit via FormatUsageReport. A fresh
// request-scoped UsageTracker rides the context (WithRequestUsage);
// the LLM instrumentation in trackTokens/trackLeadTokens and the
// image-gen path credit it alongside the process-wide tracker, so the
// end-of-request line reports THIS request's own spend. It used to
// diff the process-wide counter instead, which rolled every concurrent
// user turn, scheduled run, and background pipeline that overlapped
// the request into its line — the lines could neither be trusted
// individually nor summed, and never reconciled with the admin chart.
//
// SearchCalls is the one remaining approximation: the search tool
// stack takes no context, so it's diffed over the process-wide window
// (same trade-off UsageScope makes). Tokens and image calls are exact.
//
// Skips when no counters moved (static GETs, HTML pages) so the log
// doesn't fill with zero-delta noise. Skips streaming paths outright.
// Skips when the handler called MarkUsageReportHandled — that's how
// apps with their own scoped cost report (debate, research, etc.)
// avoid printing a near-duplicate.
//
// Wired into MountSubMux so every registered WebApp gets cost reporting
// for free; handlers don't need to import or call anything. Adding a
// new API endpoint automatically inherits the report.
func UsageReportMiddleware(label string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isStreamingPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// Wrap the request context with a skip flag handlers can
			// flip via MarkUsageReportHandled.
			skip := new(atomic.Bool)
			ctx := context.WithValue(r.Context(), usageReportSkipKey{}, skip)
			ctx, reqUsage := WithRequestUsage(ctx)
			r = r.WithContext(ctx)
			globalStart := ProcessUsage().Snapshot()
			// Defer so the report fires even if the handler panics. The
			// Go server's own panic recovery lets the middleware's defer
			// run before the connection is torn down.
			defer func() {
				if skip.Load() {
					return
				}
				// reqUsage started at zero, so its snapshot IS the
				// request's own consumption.
				d := reqUsage.Snapshot()
				// Searches are still window-diffed, so this request's
				// window contains any run that scoped itself out with
				// WithSubUsage and reported on its own line. Net those
				// out or the same search is billed twice across two
				// lines that were supposed to sum.
				d.SearchCalls = reqUsage.UnclaimedSearchCalls(
					ProcessUsage().Diff(globalStart).SearchCalls)
				if d == (UsageDiff{}) {
					return
				}
				Log("%s", FormatUsageReport(label+" "+r.URL.Path, d))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ServeDashboard starts the unified web dashboard on the given address.
// It discovers all registered WebApps, initializes them, and mounts
// them under their prefix paths with a landing page at /.
// WebListenAddr holds the address the web dashboard is listening on.
// Set by ServeDashboard so other packages can make internal HTTP calls.
var WebListenAddr string

// InternalURL builds a URL for internal inter-app HTTP calls,
// using the correct scheme (http/https) based on TLS configuration.
func InternalURL(path string) string {
	scheme := "http"
	if TLSEnabled() {
		scheme = "https"
	}
	return scheme + "://" + WebListenAddr + path
}

// --- internal inter-app calls ------------------------------------------------
//
// One gohort subsystem sometimes reaches another over HTTP rather than by a
// direct call: the blogger scheduler drives /blogger/api/auto-blog, research
// asks blogger for keywords, and so on. Those requests loop back to this same
// process and have no user behind them, so they need a way past the session
// gate.
//
// That way used to be inference: a request whose TCP peer was loopback and
// which carried no X-Forwarded-For was treated as one of ours. The trouble is
// that "carries no forwarding header" is a property of the OPERATOR'S REVERSE
// PROXY, not of gohort. nginx does not add X-Forwarded-For on its own — a plain
// `location / { proxy_pass http://127.0.0.1:8181; }`, which is what most people
// write first, forwards nothing of the sort. Behind one of those, every request
// in the world arrives on loopback with nothing to disqualify it, and any
// client that does not look like a browser walks straight past authentication.
// The safety of the whole deployment rested on a config file gohort does not
// own and cannot see.
//
// So an internal call now PROVES it is one. The token below is minted per
// process, never stored, and never leaves except on requests this process
// makes to itself; no proxy configuration can produce it by accident, and
// nothing an external client can send will match it.

// internalAuthHeader carries the per-process secret on inter-app calls.
const internalAuthHeader = "X-Gohort-Internal"

// internalAuthToken is minted once per process. Deliberately not persisted:
// it needs to be unguessable, not durable, and a restart invalidating it costs
// nothing because both ends restart together.
var internalAuthToken = func() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Failing closed here would disable inter-app calls entirely on a
		// system with no entropy, which is not a state worth shipping a
		// silent degradation for.
		panic("gohort: cannot generate internal auth token: " + err.Error())
	}
	return hex.EncodeToString(b)
}()

// NewInternalRequest builds a request to this instance's own HTTP surface,
// carrying the proof that it came from inside. Use it for every inter-app
// call; a request built by hand reaches the same endpoint as an anonymous
// stranger and is refused.
func NewInternalRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, InternalURL(path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(internalAuthHeader, internalAuthToken)
	return req, nil
}

// IsInternalRequest reports whether a request is one this process made to
// itself.
//
// Both halves are required. The token is what a reverse proxy cannot forge;
// the loopback check means a leaked token is still useless from off-box. The
// comparison is constant-time for the same reason LookupPeerKey's is.
func IsInternalRequest(r *http.Request) bool {
	presented := r.Header.Get(internalAuthHeader)
	if presented == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(internalAuthToken)) != 1 {
		return false
	}
	return IsGenuineLocalRequest(r)
}

// securityHeadersMiddleware sets baseline security response headers on every
// response: MIME-sniffing protection, clickjacking protection (the dashboard is
// never meant to be framed by another origin), referrer minimization, and — under
// TLS — HSTS. It deliberately does NOT set a Content-Security-Policy: the UI
// inlines scripts and styles, so a correct CSP needs per-response nonces (a
// larger, separate change). These headers are the high-ROI baseline that needs
// no page changes. SAMEORIGIN (not DENY) so any legitimate same-origin embed
// still works while cross-origin framing — the clickjacking vector — is blocked.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if TLSEnabled() {
			// One year; no includeSubDomains/preload so a multi-subdomain
			// deployment isn't forced to HTTPS on siblings that may not serve it.
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// accessLogMiddleware logs every HTTP request with client IP, method,
// path, status, and duration. SSE/heartbeat polling and static asset
// noise is filtered out so the log stays useful.
func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(lw, r)
		// Skip noisy poll endpoints to keep the log readable.
		path := r.URL.Path
		if isStreamingPath(path) || strings.HasSuffix(path, "/api/poll") {
			return
		}
		fullPath := accessLogPath(r)
		ip := ClientIP(r)
		ip_str := "-"
		if ip != nil {
			ip_str = ip.String()
		}
		// 2xx access lines go to the log FILE only (AuxLog) so a watched terminal
		// isn't flooded by routine success (UI auto-refresh/poll endpoints fire
		// constantly); anything non-2xx stays on the terminal where it's the signal.
		httpLog := Log
		if lw.status >= 200 && lw.status < 300 {
			httpLog = AuxLog
		}
		httpLog("[http] %s %s %s %d (%s)", ip_str, r.Method, fullPath, lw.status, time.Since(start).Round(time.Millisecond))
	})
}

// accessLogPath renders the path (and query) for one access-log line.
//
// Split out of the middleware so the guarantee is testable: a test that only
// exercised redactQuerySecrets would keep passing if the log line stopped
// calling it, which is the failure that matters. The query string is kept
// because diagnostics like "why is this 404?" usually hinge on the params
// (agent_id, session_id, format) that the bare path strips; secrets are taken
// out first, because this line goes to a file that outlives the request, and a
// credential in a URL is a credential in every log that URL touches.
func accessLogPath(r *http.Request) string {
	if raw := r.URL.RawQuery; raw != "" {
		return r.URL.Path + "?" + redactQuerySecrets(raw)
	}
	return r.URL.Path
}

// secretQueryParams names query parameters whose VALUES must never reach a
// log. Matched case-insensitively on the whole name.
//
// The deployment-wide API key travels as ?key= and is a blanket auth bypass,
// so every use of it was writing the master credential to the access log in
// plaintext — the log then being a file that outlives the request, gets
// rotated, backed up, and read by anyone debugging. The others are here
// because the same mistake is one careless endpoint away, and a redaction list
// that only covers the case someone already found is not much of a list.
var secretQueryParams = map[string]bool{
	"key": true, "api_key": true, "apikey": true, "token": true,
	"access_token": true, "refresh_token": true, "secret": true,
	"password": true, "passwd": true, "pw": true, "code": true,
	"client_secret": true, "signature": true, "sig": true,
}

// redactQuerySecrets rewrites a raw query string, replacing the value of any
// parameter named in secretQueryParams with "REDACTED".
//
// Operates on the RAW string rather than parsing and re-encoding, so a
// malformed query is logged as close to what arrived as possible — the point
// of the line is diagnosis, and re-encoding would hide the malformation that
// is often the thing being diagnosed.
func redactQuerySecrets(raw string) string {
	if raw == "" {
		return raw
	}
	parts := strings.Split(raw, "&")
	changed := false
	for i, p := range parts {
		eq := strings.IndexByte(p, '=')
		if eq <= 0 {
			continue
		}
		name, err := url.QueryUnescape(p[:eq])
		if err != nil {
			name = p[:eq]
		}
		if secretQueryParams[strings.ToLower(name)] {
			parts[i] = p[:eq] + "=REDACTED"
			changed = true
		}
	}
	if !changed {
		return raw
	}
	return strings.Join(parts, "&")
}

// loggingWriter captures the response status code for access logging.
type loggingWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (lw *loggingWriter) WriteHeader(code int) {
	if !lw.wroteHeader {
		lw.status = code
		lw.wroteHeader = true
	}
	lw.ResponseWriter.WriteHeader(code)
}

func (lw *loggingWriter) Flush() {
	if f, ok := lw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack delegates to the underlying ResponseWriter so WebSocket upgrades
// (which require http.Hijacker) work through the logging wrapper.
func (lw *loggingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := lw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}
