package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/webui"
)

// WebApp is implemented by agents that can serve a web UI.
// The central web server discovers these and mounts them under their prefix.
type WebApp interface {
	WebPath() string // URL prefix, e.g. "/investigate"
	WebName() string // Display name for dashboard
	WebDesc() string // Short description
	RegisterRoutes(mux *http.ServeMux, prefix string)
}

// WebAppOrder is optionally implemented by WebApps to control their
// position on the dashboard. Lower values appear first. Apps without
// this default to 50.
type WebAppOrder interface {
	WebOrder() int
}

// WebAppRestricted is optionally implemented by WebApps that should be
// hidden from certain requests (e.g. IP-based access control).
type WebAppRestricted interface {
	WebRestricted(r *http.Request) bool
}

// WebAppAccess is optionally implemented by WebApps that expose access
// flags to other apps via /api/access (e.g. "techwriter": true/false).
type WebAppAccess interface {
	WebAccessKey() string                    // JSON key name
	WebAccessCheck(r *http.Request) bool     // returns the flag value for this request
}

var (
	webAppMu         sync.Mutex
	registeredWebApps []WebApp
)

// RegisterWebApp registers a web-capable agent for the central server.
func RegisterWebApp(app WebApp) {
	webAppMu.Lock()
	defer webAppMu.Unlock()
	registeredWebApps = append(registeredWebApps, app)
}

// RegisteredWebApps returns all registered web apps.
func RegisteredWebApps() []WebApp {
	webAppMu.Lock()
	defer webAppMu.Unlock()
	return registeredWebApps
}

// SetupWebAgentFunc is set by the application to initialize agents for the
// central web server (sets up DB buckets, LLM config, etc.).
var SetupWebAgentFunc func(agent Agent)

// MaxConcurrentTasks is the maximum number of simultaneous apps allowed.
// Set by the --max_concurrent flag. Default 1.
var MaxConcurrentTasks = 1

// globalQueue is a shared queue for all apps. Tasks acquire a slot before
// starting and release it when done. Other tasks wait in FIFO order.
var globalQueue = &TaskQueue{
	notify: make(chan struct{}, 1),
}

// GlobalQueue returns the shared task queue.
func GlobalQueue() *TaskQueue { return globalQueue }


// TaskQueue manages a shared FIFO queue across all apps.
type TaskQueue struct {
	mu      sync.Mutex
	active  int
	queue   []queueWaiter
	notify  chan struct{}
}

type queueWaiter struct {
	id    string
	label string
	ready chan struct{}
	ctx   context.Context
}

// Acquire blocks until a slot is available or ctx is cancelled.
// onQueue is called with the queue position whenever it changes.
// Returns false if ctx was cancelled.
func (q *TaskQueue) Acquire(ctx context.Context, id string, label string, onQueue func(position int)) bool {
	q.mu.Lock()
	if q.active < MaxConcurrentTasks {
		q.active++
		q.mu.Unlock()
		return true
	}

	// Queue this request.
	w := queueWaiter{
		id:    id,
		label: label,
		ready: make(chan struct{}, 1),
		ctx:   ctx,
	}
	q.queue = append(q.queue, w)
	pos := len(q.queue)
	q.mu.Unlock()

	Log("[queue] %s queued at position %d: %s", id[:8], pos, label)

	if onQueue != nil {
		onQueue(pos)
	}

	for {
		select {
		case <-w.ready:
			Log("[queue] %s promoted — starting", id[:8])
			return true
		case <-ctx.Done():
			Log("[queue] %s context cancelled — removing from queue", id[:8])
			q.mu.Lock()
			for i, qw := range q.queue {
				if qw.id == id {
					q.queue = append(q.queue[:i], q.queue[i+1:]...)
					break
				}
			}
			q.mu.Unlock()
			return false
		case <-q.notify:
			// Check if we moved up in the queue.
			q.mu.Lock()
			found := false
			for i, qw := range q.queue {
				if qw.id == id {
					found = true
					q.mu.Unlock()
					if onQueue != nil {
						onQueue(i + 1)
					}
					break
				}
			}
			if !found {
				q.mu.Unlock()
			}
		}
	}
}

// CancelQueued removes a queued item by ID. Returns true if found and removed.
func (q *TaskQueue) CancelQueued(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, w := range q.queue {
		if w.id == id {
			q.queue = append(q.queue[:i], q.queue[i+1:]...)
			return true
		}
	}
	return false
}

// QueuedEntries returns a list of items waiting in the queue.
func (q *TaskQueue) QueuedEntries() []LiveEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	var entries []LiveEntry
	for _, w := range q.queue {
		entries = append(entries, LiveEntry{ID: w.id, Label: w.label, Queued: true})
	}
	return entries
}

// Release frees a slot and starts the next queued task.
func (q *TaskQueue) Release() {
	q.mu.Lock()
	if len(q.queue) > 0 {
		// Start the next queued task — don't decrement active.
		next := q.queue[0]
		q.queue = q.queue[1:]
		remaining := len(q.queue)
		q.mu.Unlock()
		Log("[queue] slot released — promoting %s (%d still queued)", next.id[:8], remaining)
		select {
		case next.ready <- struct{}{}:
		default:
		}
		// Notify remaining queue members of position change.
		select {
		case q.notify <- struct{}{}:
		default:
		}
	} else {
		q.active--
		q.mu.Unlock()
		Log("[queue] slot released — no items queued (active: %d)", q.active)
	}
}

// faviconLinkTag is the inline-SVG favicon link injected into every page
// served via ServeHTMLWithBase. Mirrors the favicon used by webui.RenderPage
// so legacy and migrated pages share the same browser-tab icon.
const faviconLinkTag = `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;utf8,` +
	`%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 64 64'%3E` +
	`%3Crect width='64' height='64' rx='12' fill='%230d1117'/%3E` +
	`%3Ctext x='50%25' y='50%25' dominant-baseline='central' text-anchor='middle' ` +
	`font-family='-apple-system,BlinkMacSystemFont,Segoe UI,Helvetica,sans-serif' ` +
	`font-size='40' font-weight='700' fill='%2358a6ff'%3EG%3C/text%3E` +
	`%3C/svg%3E">`

// ServeHTMLWithBase serves an HTML string, injecting a <base href> tag and
// converting absolute API paths to relative when a prefix is set.
// When prefix is empty (standalone mode), the HTML is served unchanged
// (other than favicon injection).
func ServeHTMLWithBase(w http.ResponseWriter, html string, prefix string) {
	// Inject the favicon into every page, regardless of prefix. Replaces
	// the first <head> opener with <head><favicon>. Skipped if the page
	// already has a favicon link (e.g., pages rendered via webui.RenderPage).
	if !strings.Contains(html, `rel="icon"`) {
		html = strings.Replace(html, "<head>", "<head>"+faviconLinkTag, 1)
	}

	// Inject the shared @font-face declaration (Orbitron) right after
	// <head>, so apps that render their own HTML templates (debate,
	// research, investigate, etc.) pick up the header font without
	// wiring it per-app.
	if ff := webui.FontFaceCSS(); ff != "" && !strings.Contains(html, "@font-face") {
		html = strings.Replace(html, "<head>", "<head><style>"+ff+"</style>", 1)
	}

	if prefix != "" {
		base_href := prefix + "/"
		html = strings.Replace(html, "<head>",
			fmt.Sprintf("<head><base href=\"%s\">", base_href), 1)
		// Convert absolute API paths to relative so <base> resolves them.
		html = strings.ReplaceAll(html, "'/api/", "'api/")
		html = strings.ReplaceAll(html, "\"/api/", "\"api/")
		// Inject a floating back-to-dashboard icon.
		back_btn := `<a id="dashboard-back" href="/" title="Dashboard" style="` +
			`position:fixed;top:12px;left:12px;z-index:9999;` +
			`display:inline-flex;align-items:center;justify-content:center;` +
			`width:32px;height:32px;border-radius:6px;` +
			`background:#161b22;border:1px solid #30363d;` +
			`color:#8b949e;text-decoration:none;font-size:1rem;` +
			`transition:border-color 0.2s,color 0.2s,background 0.2s;` +
			`" onmouseover="this.style.borderColor='#58a6ff';this.style.color='#f0f6fc';this.style.background='#1c2128'"` +
			` onmouseout="this.style.borderColor='#30363d';this.style.color='#8b949e';this.style.background='#161b22'"` +
			`>` +
			`<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">` +
			`<path d="M7.78 12.53a.75.75 0 01-1.06 0L2.47 8.28a.75.75 0 010-1.06l4.25-4.25a.75.75 0 011.06 1.06L4.81 7h7.44a.75.75 0 010 1.5H4.81l2.97 2.97a.75.75 0 010 1.06z"/>` +
			`</svg></a>`
		dashboard_style := `<style>#dashboard-back~*{} body{padding-top:3.5rem!important;}</style>`
		// Replace the LAST </body> tag — earlier occurrences may be inside JS strings.
		if idx := strings.LastIndex(html, "</body>"); idx >= 0 {
			html = html[:idx] + dashboard_style + back_btn + html[idx:]
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// MountSubMux registers a sub-mux under a prefix using StripPrefix.
// When prefix is empty (standalone mode), mounts at root.
func MountSubMux(mux *http.ServeMux, prefix string, sub *http.ServeMux) {
	if prefix != "" {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, sub))
	} else {
		mux.Handle("/", sub)
	}
}

// ServeDashboard starts the unified web dashboard on the given address.
// It discovers all registered WebApps, initializes them, and mounts
// them under their prefix paths with a landing page at /.
type dashApp struct {
	name string
	desc string
	path string
	app  WebApp
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

func ServeDashboard(addr string) error {
	WebListenAddr = addr
	DefaultPleaseWait()

	mux := http.NewServeMux()

	var apps []dashApp

	// Collect all web-capable components: explicitly registered WebApps
	// plus any registered Agent/App that implements the WebApp interface.
	seen := make(map[string]bool)
	var webApps []WebApp
	for _, wa := range RegisteredWebApps() {
		if !seen[wa.WebPath()] {
			seen[wa.WebPath()] = true
			webApps = append(webApps, wa)
		}
	}
	for _, a := range RegisteredApps() {
		if wa, ok := a.(WebApp); ok && !seen[wa.WebPath()] {
			seen[wa.WebPath()] = true
			webApps = append(webApps, wa)
		}
	}
	for _, a := range RegisteredAgents() {
		if wa, ok := a.(WebApp); ok && !seen[wa.WebPath()] {
			seen[wa.WebPath()] = true
			webApps = append(webApps, wa)
		}
	}

	// First pass: initialize all web app databases so cross-app
	// lookups via FindAgent see a fully wired agent.
	for _, wa := range webApps {
		if agent, ok := wa.(Agent); ok && SetupWebAgentFunc != nil {
			SetupWebAgentFunc(agent)
		}
	}

	// Second pass: register routes now that all agents are ready.
	for _, wa := range webApps {
		prefix := wa.WebPath()
		wa.RegisterRoutes(mux, prefix)
		// Apps implementing WebHidden get routes but no dashboard card.
		type hidden interface{ WebHidden() bool }
		if h, ok := wa.(hidden); ok && h.WebHidden() {
			Log("  Registered (hidden): %s -> %s/\n", wa.WebName(), prefix)
			continue
		}
		apps = append(apps, dashApp{
			name: wa.WebName(),
			desc: wa.WebDesc(),
			path: prefix,
			app:  wa,
		})
		Log("  Registered: %s -> %s/\n", wa.WebName(), prefix)
	}

	// Sort by WebOrder (if implemented), then alphabetically.
	sort.Slice(apps, func(i, j int) bool {
		oi, oj := 50, 50
		if o, ok := apps[i].app.(WebAppOrder); ok { oi = o.WebOrder() }
		if o, ok := apps[j].app.(WebAppOrder); ok { oj = o.WebOrder() }
		if oi != oj { return oi < oj }
		return apps[i].name < apps[j].name
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		visible := make([]dashApp, 0, len(apps))
		for _, a := range apps {
			if ra, ok := a.app.(WebAppRestricted); ok && ra.WebRestricted(r) {
				continue
			}
			// Per-user app access (skip admin app, it has its own gating).
			if a.path != "/admin" && !UserHasAppAccess(r, a.path) {
				continue
			}
			visible = append(visible, a)
		}
		serve_dashboard(w, r, visible)
	})

	// Global live view endpoint for the dashboard.
	mux.HandleFunc("/api/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AllLiveSessions())
	})

	// Persistent notify preference: GET returns it, POST toggles and persists.
	mux.HandleFunc("/api/notify-preference", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		username := AuthCurrentUser(r)
		if username == "" || AuthDB == nil {
			json.NewEncoder(w).Encode(map[string]bool{"notify": false})
			return
		}
		db := AuthDB()
		if r.Method == http.MethodPost {
			var req struct {
				Notify bool `json:"notify"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			AuthSetNotifyDefault(db, username, req.Notify)
			json.NewEncoder(w).Encode(map[string]bool{"notify": req.Notify})
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"notify": AuthGetNotifyDefault(db, username)})
	})

	// Per-request access flags. Apps implement WebAppAccess to expose
	// named boolean flags (e.g. "techwriter": true/false).
	mux.HandleFunc("/api/access", func(w http.ResponseWriter, r *http.Request) {
		flags := make(map[string]bool)
		for _, a := range apps {
			if aa, ok := a.app.(WebAppAccess); ok {
				flags[aa.WebAccessKey()] = aa.WebAccessCheck(r)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(flags)
	})

	// Authentication endpoints.
	if AuthDB != nil {
		db := AuthDB()
		mux.HandleFunc("/login", LoginHandler(db))
		mux.HandleFunc("/logout", LogoutHandler(db))
		mux.HandleFunc("/signup", SignupHandler(db))
		mux.HandleFunc("/forgot", ForgotHandler(db))
		mux.HandleFunc("/reset", ResetHandler(db))
	}

	// Restore persisted queue items after all apps are initialized
	// so handlers are registered.
	QueueRestore()

	PleaseWait.Hide()
	scheme := "http"
	if TLSEnabled() {
		scheme = "https"
	}
	Log("Gohort Dashboard: %s://%s\n", scheme, addr)

	var handler http.Handler = accessLogMiddleware(mux)
	if AuthDB != nil {
		handler = AuthMiddleware(AuthDB(), handler)
	}
	return ListenAndServeTLS(addr, handler)
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
		if path == "/api/live" || strings.HasSuffix(path, "/api/live") ||
			strings.HasSuffix(path, "/api/events") || strings.HasSuffix(path, "/api/reconnect") {
			return
		}
		ip := clientIP(r)
		ip_str := "-"
		if ip != nil {
			ip_str = ip.String()
		}
		Log("[http] %s %s %s %d (%s)", ip_str, r.Method, path, lw.status, time.Since(start).Round(time.Millisecond))
	})
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

func serve_dashboard(w http.ResponseWriter, r *http.Request, apps []dashApp) {
	var cards strings.Builder
	for _, a := range apps {
		fmt.Fprintf(&cards, `<a class="card" href="%s/">
			<div class="card-name">%s</div>
			<div class="card-desc">%s</div>
		</a>`, a.path, a.name, a.desc)
	}

	// Detect logged-in user for the auth bar.
	username := AuthCurrentUser(r)
	auth_html := ""
	if username != "" {
		auth_html = fmt.Sprintf(
			`<div class="auth-bar"><span class="auth-user">%s</span><a class="auth-link" href="/logout">Logout</a></div>`,
			username)
	}

	html := `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
%FAVICON%
<title>Gohort Dashboard</title>
<style>
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, Arial, sans-serif;
    background: #0d1117; color: #c9d1d9; min-height: 100vh;
    display: flex; flex-direction: column; align-items: center;
    padding: 80px 20px;
  }
  .ascii-logo {
    font-family: 'JetBrains Mono', 'Fira Code', 'SF Mono', ui-monospace, Menlo, Consolas, monospace;
    font-size: 1rem; line-height: 1.15; white-space: pre; letter-spacing: 0.02em;
    margin-bottom: 0.5rem; text-align: center;
    background: linear-gradient(180deg, #f0f6fc 0%, #30363d 100%);
    -webkit-background-clip: text; -webkit-text-fill-color: transparent;
    background-clip: text;
  }
  .subtitle { color: #8b949e; margin-bottom: 3rem; font-size: 1rem; }
  .grid {
    display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 1.5rem; width: 100%; max-width: 700px;
  }
  .card {
    display: block; text-decoration: none; color: #c9d1d9;
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 1.5rem; transition: border-color 0.2s, transform 0.2s;
  }
  .card:hover { border-color: #58a6ff; transform: translateY(-2px); }
  .card-name { font-size: 1.25rem; font-weight: 600; color: #f0f6fc; margin-bottom: 0.5rem; }
  .card-desc { font-size: 0.9rem; color: #8b949e; line-height: 1.4; }
  #live-panel {
    width: 100%; max-width: 700px; margin-top: 2rem;
  }
  #live-panel h3 { color: #8b949e; font-size: 0.9rem; margin-bottom: 0.75rem; cursor: pointer; }
  #live-panel h3:hover { color: #c9d1d9; }
  .live-item {
    display: flex; align-items: center; gap: 0.75rem;
    padding: 0.6rem 0.8rem; background: #161b22; border: 1px solid #21262d;
    border-radius: 6px; margin-bottom: 0.4rem; cursor: pointer;
    text-decoration: none; color: #c9d1d9; font-size: 0.85rem;
  }
  .live-item:hover { border-color: #30363d; }
  .live-badge {
    font-size: 0.7rem; padding: 0.15rem 0.4rem; border-radius: 4px;
    font-weight: 600; white-space: nowrap;
  }
  .live-badge.running { background: #238636; color: #fff; }
  .live-badge.queued { background: #d29922; color: #fff; }
  .live-label { flex: 1; }
  .live-status { color: #8b949e; font-size: 0.8rem; }
  .auth-bar {
    position: fixed; top: 12px; right: 12px; z-index: 9999;
    display: flex; align-items: center; gap: 0.6rem;
    font-size: 0.8rem;
  }
  .auth-user { color: #8b949e; }
  .auth-link {
    color: #8b949e; text-decoration: none;
    padding: 0.3rem 0.7rem; border: 1px solid #30363d; border-radius: 6px;
    background: #161b22; transition: border-color 0.2s, color 0.2s;
  }
  .auth-link:hover { border-color: #58a6ff; color: #f0f6fc; }
</style>
</head>
<body>
  %AUTH%
  <div class="ascii-logo">
  ____       _                _
 / ___| ___ | |__   ___  _ __| |_
| |  _ / _ \| '_ \ / _ \| '__| __|
| |_| | (_) | | | | (_) | |  | |_
 \____|\___/|_| |_|\___/|_|   \__|</div>
  <p class="subtitle">Agent Dashboard</p>
  <div class="grid">%CARDS%</div>
  <div id="live-panel"><h3 onclick="toggleLive()">Live Sessions</h3><div id="live-list"></div></div>
<script>
var liveHidden = false;
function toggleLive() {
  liveHidden = !liveHidden;
  document.getElementById('live-list').style.display = liveHidden ? 'none' : 'block';
  if (!liveHidden) refreshLive();
}
function refreshLive() {
  if (liveHidden) return;
  var list = document.getElementById('live-list');
  fetch('/api/live').then(function(r){return r.json()}).then(function(items){
    if (!items || items.length === 0) {
      list.innerHTML = '<div style="color:#484f58;padding:0.5rem;font-size:0.85rem">No active sessions.</div>';
      return;
    }
    items.sort(function(a, b) {
      if (a.spawned !== b.spawned) return a.spawned ? 1 : -1;
      return (a.app || '').localeCompare(b.app || '');
    });
    var html = '';
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      var badge = it.queued ? '<span class="live-badge queued">Queued</span>' : '<span class="live-badge running">Running</span>';
      var app = it.app ? '<span class="live-badge" style="background:#30363d;color:#8b949e">' + it.app + '</span>' : '';
      var url = it.url || (it.path || '') + '/?id=' + encodeURIComponent(it.id);
      html += '<a class="live-item" href="' + url + '">';
      html += app + badge;
      html += '<span class="live-label">' + (it.topic || it.label || 'Untitled') + '</span>';
      if (it.status) html += '<span class="live-status">' + it.status + '</span>';
      html += '</a>';
    }
    list.innerHTML = html;
  });
}
refreshLive();
setInterval(refreshLive, 10000);
</script>
</body>
</html>`
	html = strings.Replace(html, "%FAVICON%", faviconLinkTag, 1)
	html = strings.Replace(html, "%AUTH%", auth_html, 1)
	html = strings.Replace(html, "%CARDS%", cards.String(), 1)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// Dated is the minimal interface a history record must satisfy.
type Dated interface {
	GetDate() string
}

// HistoryHandlers returns HTTP handler functions for list, detail, and delete
// operations on a database history table. The summarize function converts a
// full record into whatever summary struct the list endpoint should return.
// R must implement Dated so results can be sorted newest-first.
func HistoryHandlers[R Dated, S any](db func() Database, table string, summarize func(R) S) (list, detail, delete_handler http.HandlerFunc) {
	list = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		store := db()
		if store == nil {
			fmt.Fprint(w, "[]")
			return
		}
		type entry struct {
			date string
			item S
		}
		var entries []entry
		for _, key := range store.Keys(table) {
			var rec R
			if store.Get(table, key, &rec) {
				entries = append(entries, entry{date: rec.GetDate(), item: summarize(rec)})
			}
		}
		if entries == nil {
			fmt.Fprint(w, "[]")
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].date > entries[j].date })
		items := make([]S, len(entries))
		for i, e := range entries {
			items[i] = e.item
		}
		data, _ := json.Marshal(items)
		w.Write(data)
	}

	detail = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(r.URL.Path, "/api/history/")
		store := db()
		if id == "" || store == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var rec R
		if !store.Get(table, id, &rec) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		data, _ := json.Marshal(rec)
		w.Write(data)
	}

	delete_handler = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/history/delete/")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		if store := db(); store != nil {
			store.Unset(table, id)
		}
		w.WriteHeader(http.StatusNoContent)
	}

	return
}

// --- Generic live session management for SSE-based agents ---

// LiveSession tracks a single running agent session so it can be cancelled
// independently of the HTTP connection. Events are buffered so reconnecting
// clients can catch up.
type LiveSession[T any] struct {
	ID      string
	Label   string              // Human-readable label (topic, question, etc.)
	Cancel  context.CancelFunc
	Events  []T
	Done    bool
	Status  string              // Last status message for live view
	Spawned bool                // True if spawned by a parent session. Cannot be cancelled directly -- cancel the parent instead.
}

// LiveSessionMap manages concurrent live sessions with mutex protection,
// concurrency limits, and 10-minute cleanup after completion.
type LiveSessionMap[T any] struct {
	mu       sync.Mutex
	sessions map[string]*LiveSession[T]
}

// NewLiveSessionMap creates a new session map.
func NewLiveSessionMap[T any](maxConcurrent int) *LiveSessionMap[T] {
	return &LiveSessionMap[T]{
		sessions: make(map[string]*LiveSession[T]),
	}
}

// Register adds a new session to the map.
func (m *LiveSessionMap[T]) Register(id, label string, cancel context.CancelFunc) *LiveSession[T] {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &LiveSession[T]{ID: id, Label: label, Cancel: cancel}
	m.sessions[id] = s
	return s
}

// UpdateCancel replaces the cancel function for a session.
func (m *LiveSessionMap[T]) UpdateCancel(id string, cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Cancel = cancel
	}
}

// Get returns the session with the given ID, or nil.
func (m *LiveSessionMap[T]) Get(id string) *LiveSession[T] {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// AppendEvent adds an event to a session's buffer. Thread-safe.
func (m *LiveSessionMap[T]) AppendEvent(id string, event T, isDone bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Events = append(s.Events, event)
		if isDone {
			s.Done = true
		}
	}
}

// UpdateStatus sets the latest status message for a session (shown in live view).
func (m *LiveSessionMap[T]) UpdateStatus(id, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Status = status
	}
}

// SnapshotEvents returns a copy of the session's events and its done status.
func (m *LiveSessionMap[T]) SnapshotEvents(id string) ([]T, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, true
	}
	cp := make([]T, len(s.Events))
	copy(cp, s.Events)
	return cp, s.Done
}

// ScheduleCleanup removes a session after 10 minutes (for reconnect window).
func (m *LiveSessionMap[T]) ScheduleCleanup(id string) {
	m.ScheduleCleanupAfter(id, 10*time.Minute)
}

// ScheduleCleanupAfter removes a session after the given duration.
func (m *LiveSessionMap[T]) ScheduleCleanupAfter(id string, d time.Duration) {
	go func() {
		time.Sleep(d)
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}()
}

// HandleCancel returns an http.HandlerFunc that cancels a live or queued session by ID.
func (m *LiveSessionMap[T]) HandleCancel(logPrefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id parameter required", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		if s, ok := m.sessions[id]; ok && !s.Done {
			if s.Spawned {
				m.mu.Unlock()
				Log("[web] %s %s cancel rejected — spawned by parent session, cancel the parent instead", logPrefix, id)
				http.Error(w, "this session was spawned by a parent pipeline -- cancel the parent instead", http.StatusConflict)
				return
			}
			s.Cancel()
			s.Done = true // Mark done so it disappears from Live immediately.
			Log("[web] %s %s cancelled by user", logPrefix, id)
		}
		m.mu.Unlock()
		// Also try removing from the global queue if it was queued.
		if GlobalQueue().CancelQueued(id) {
			Log("[web] %s %s removed from queue by user", logPrefix, id)
		}
		m.ScheduleCleanup(id)
		w.WriteHeader(http.StatusOK)
	}
}

// LiveEntry is a JSON-serializable summary of an active or queued session.
type LiveEntry struct {
	ID      string `json:"id"`
	Label   string `json:"topic"`   // "topic" for backwards compat with JS
	Queued  bool   `json:"queued,omitempty"`
	Spawned bool   `json:"spawned,omitempty"` // spawned by a parent app
	Status  string `json:"status,omitempty"`  // last status message
	App     string `json:"app,omitempty"`     // which app owns this session
	Path    string `json:"path,omitempty"`    // web path prefix
	URL     string `json:"url,omitempty"`     // full reconnect URL (if set, used instead of path+id)
}

// LiveProvider returns active sessions for a specific app.
type LiveProvider func() []LiveEntry

var (
	liveProviderMu  sync.Mutex
	liveProviders   []LiveProvider
)

// RegisterLiveProvider adds a provider that contributes to the global live view.
func RegisterLiveProvider(p LiveProvider) {
	liveProviderMu.Lock()
	defer liveProviderMu.Unlock()
	liveProviders = append(liveProviders, p)
}

// AllLiveSessions aggregates active sessions from all registered providers plus the global queue.
func AllLiveSessions() []LiveEntry {
	liveProviderMu.Lock()
	providers := make([]LiveProvider, len(liveProviders))
	copy(providers, liveProviders)
	liveProviderMu.Unlock()

	var all []LiveEntry
	for _, p := range providers {
		all = append(all, p()...)
	}
	all = append(all, GlobalQueue().QueuedEntries()...)
	return all
}

// ActiveSessions returns a list of currently active (not done, not queued) sessions.
func (m *LiveSessionMap[T]) ActiveSessions() []LiveEntry {
	// Get queued IDs to exclude them.
	queuedIDs := make(map[string]bool)
	for _, q := range globalQueue.QueuedEntries() {
		queuedIDs[q.ID] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []LiveEntry
	for _, s := range m.sessions {
		if !s.Done && !queuedIDs[s.ID] {
			entries = append(entries, LiveEntry{ID: s.ID, Label: s.Label, Status: s.Status, Spawned: s.Spawned})
		}
	}
	return entries
}

// HandleLive returns an http.HandlerFunc that lists active and queued sessions as JSON.
func (m *LiveSessionMap[T]) HandleLive() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := m.ActiveSessions()
		entries = append(entries, GlobalQueue().QueuedEntries()...)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}
}

// HandleEvents returns an http.HandlerFunc that returns session events as JSON
// for polling-based watchers (avoids consuming an SSE connection).
func (m *LiveSessionMap[T]) HandleEvents() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		events, _ := m.SnapshotEvents(id)
		if events == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(events)
	}
}

// HandleReconnect returns an http.HandlerFunc that replays buffered events
// for a reconnecting client, then streams new events until the session completes.
func (m *LiveSessionMap[T]) HandleReconnect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		events, done := m.SnapshotEvents(id)
		if events == nil {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}

		sse, err := NewSSEWriter(w)
		if err != nil {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		// Replay buffered events.
		for _, ev := range events {
			sse.Send(ev)
		}
		if done {
			return
		}

		// Stream new events as they arrive.
		sent := len(events)
		for {
			time.Sleep(500 * time.Millisecond)
			current, isDone := m.SnapshotEvents(id)
			if current == nil {
				return
			}
			for i := sent; i < len(current); i++ {
				sse.Send(current[i])
			}
			sent = len(current)
			if isDone {
				return
			}
		}
	}
}
