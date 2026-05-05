package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cmcoffee/gohort/core/webui"
)

// WebApp is implemented by agents that can serve a web UI.
// The central web server discovers these and mounts them under their prefix.
type WebApp interface {
	WebPath() string // URL prefix, e.g. "/myapp"
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
	id       string
	label    string
	app      string // app name for live view
	linkPath string // URL path for linking (e.g. "/myapp/?session=")
	ready    chan struct{}
	ctx      context.Context
}

// Acquire blocks until a slot is available or ctx is cancelled.
// onQueue is called with the queue position whenever it changes.
// Returns false if ctx was cancelled.
func (q *TaskQueue) Acquire(ctx context.Context, id, label, app, linkPath string, onQueue func(position int)) bool {
	q.mu.Lock()
	if q.active < MaxConcurrentTasks {
		q.active++
		q.mu.Unlock()
		return true
	}

	// Queue this request.
	w := queueWaiter{
		id:       id,
		label:    label,
		app:      app,
		linkPath: linkPath,
		ready:    make(chan struct{}, 1),
		ctx:      ctx,
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
		entry := LiveEntry{ID: w.id, Label: w.label, Queued: true, App: w.app}
		if w.linkPath != "" {
			entry.URL = w.linkPath + w.id
		}
		entries = append(entries, entry)
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

// liveRibbonCSS is the CSS for the webui live session ribbon, inlined
// for pages that render raw HTML via ServeHTMLWithBase rather than
// going through webui.RenderPage (which inlines base.css itself).
const liveRibbonCSS = `
#webui-live-ribbon {
  position: fixed; top: 0; right: 5px; max-width: 360px; margin: 0.5rem;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.3rem 0.6rem; font-size: 0.8rem; color: var(--text-mute);
  box-shadow: 0 4px 12px rgba(0,0,0,0.3); z-index: 9999; display: none;
}
@media (max-width: 640px) {
  #webui-live-ribbon {
    /* Keep on the same top row as the back arrow — sub-app pages
       don't have an auth-bar so there's nothing to clear. The
       back arrow sits at top:12 left:12 (32x32) and the ribbon
       at top:0 right:5; they're on opposite sides and don't
       collide even when the ribbon has items. */
    max-width: calc(100vw - 60px);
    font-size: 0.75rem;
  }
}
#webui-live-ribbon h4 {
  font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em;
  color: var(--text-mute); margin: 0; cursor: pointer;
  display: flex; align-items: center; gap: 0.4rem;
}
#webui-live-ribbon h4 .live-dot {
  width: 8px; height: 8px; border-radius: 50%; background: var(--good);
  display: inline-block; animation: pulse 2s infinite;
}
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.4; } }
#webui-live-ribbon .item {
  display: flex; gap: 0.4rem; align-items: center;
  padding: 0.3rem 0; color: var(--text); text-decoration: none;
}
#webui-live-ribbon .item:hover { color: var(--text-hi); }
#webui-live-ribbon .badge {
  font-size: 0.65rem; padding: 0.1rem 0.35rem; border-radius: 3px;
  background: var(--bg-2); color: var(--text-mute);
}
#webui-live-ribbon .badge.run { background: var(--good); color: #fff; }
#webui-live-ribbon .badge.q   { background: var(--warn); color: #fff; }
`

// liveRibbonJS is the JS for the webui live session ribbon, inlined
// for pages that don't use the webui framework.
const liveRibbonJS = `(function(){
  function esc(s){return String(s==null?'':s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;')}
  function ensureRibbon(){
    var el=document.getElementById('webui-live-ribbon');
    if(el)return el;
    el=document.createElement('div');
    el.id='webui-live-ribbon';
    el.innerHTML='<h4><span class="live-dot"></span>Live</h4><div class="items" style="display:none"></div>';
    document.body.appendChild(el);
    el.querySelector('h4').addEventListener('click',function(){
      var items=el.querySelector('.items');
      items.style.display=items.style.display==='none'?'':'none';
    });
    return el;
  }
  function refresh(){
    fetch(location.origin+'/api/live').then(function(r){return r.json()}).then(function(items){
      var el=ensureRibbon(),box=el.querySelector('.items');
      items=(items||[]).filter(function(it){return !it.spawned});
      if(items.length===0){el.style.display='none';box.innerHTML='';return}
      items.sort(function(a,b){var oa=a.order||0,ob=b.order||0;if(oa!==ob)return oa-ob;return(a.app||'').localeCompare(b.app||'')});
      var h='';
      for(var i=0;i<items.length;i++){
        var it=items[i];
        var badge=it.queued?'<span class="badge q">Queued</span>':'<span class="badge run">Running</span>';
        var app=it.app?'<span class="badge">'+esc(it.app)+'</span>':'';
        var url=it.url||'';
        if(url&&url.charAt(0)==='/')url=location.origin+url;
        h+='<a class="item" href="'+url+'">'+app+badge+'<span class="label">'+esc(it.topic||it.label||'Untitled')+'</span></a>';
      }
      box.innerHTML=h;el.style.display='block';
    }).catch(function(){});
  }
  refresh();setInterval(refresh,10000);
})();`

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
	// <head>, so apps that render raw HTML templates (rather than
	// going through webui.RenderPage) pick up the header font without
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
		// Inject a floating back-arrow icon. Default behavior navigates
		// to the dashboard (/). Apps that have drilled-in views (e.g.,
		// viewing a single record from a list) can override this by
		// setting window.drillBackHandler to a function — when set,
		// clicking the arrow calls that function instead of navigating,
		// letting the app return to its own list view. Apps clear the
		// handler when they leave the drilled state so the arrow
		// reverts to dashboard-navigation.
		back_btn := `<a id="dashboard-back" href="/" title="Back" style="` +
			`position:fixed;top:12px;left:12px;z-index:9999;` +
			`display:inline-flex;align-items:center;justify-content:center;` +
			`width:32px;height:32px;border-radius:6px;` +
			`background:#161b22;border:1px solid #30363d;` +
			`color:#8b949e;text-decoration:none;font-size:1rem;` +
			`transition:border-color 0.2s,color 0.2s,background 0.2s;` +
			`" onclick="if(typeof window.drillBackHandler==='function'){window.drillBackHandler();return false;}return true;"` +
			` onmouseover="this.style.borderColor='#58a6ff';this.style.color='#f0f6fc';this.style.background='#1c2128'"` +
			` onmouseout="this.style.borderColor='#30363d';this.style.color='#8b949e';this.style.background='#161b22'"` +
			`>` +
			`<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor">` +
			`<path d="M7.78 12.53a.75.75 0 01-1.06 0L2.47 8.28a.75.75 0 010-1.06l4.25-4.25a.75.75 0 011.06 1.06L4.81 7h7.44a.75.75 0 010 1.5H4.81l2.97 2.97a.75.75 0 010 1.06z"/>` +
			`</svg></a>`
		dashboard_style := `<style>#dashboard-back~*{} body{padding-top:3.5rem!important;}</style>`
		// Inject the webui live ribbon for pages that don't use the webui
		// framework. Pages that DO use webui already have it via BaseCSS/BaseJS.
		live_widget := ""
		if !strings.Contains(html, "webui-live-ribbon") && !strings.Contains(html, "refreshRibbon") {
			live_widget = `<style>` +
				`:root{--bg-1:#161b22;--bg-2:#21262d;--border:#30363d;--text:#c9d1d9;--text-hi:#f0f6fc;--text-mute:#8b949e;--good:#238636;--warn:#d29922}` +
				liveRibbonCSS + `</style><script>` + liveRibbonJS + `</script>`
		}
		// Inject webui.BaseJS for pages that don't go through
		// webui.RenderPage. This exposes shared helpers (escapeHtml,
		// renderMarkdown, etc.) as window globals so app-specific JS
		// can call them. Keyed on an arbitrary symbol from the bundle
		// so it only fires once per page.
		shared_js := ""
		if !strings.Contains(html, "window.renderMarkdown") {
			shared_js = `<script>` + webui.BaseJS() + `</script>`
		}
		// Replace the LAST </body> tag — earlier occurrences may be inside JS strings.
		if idx := strings.LastIndex(html, "</body>"); idx >= 0 {
			html = html[:idx] + dashboard_style + back_btn + live_widget + shared_js + html[idx:]
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, html)
}

// AppUIAssets holds the app-specific HTML, CSS, and JS passed to NewWebUI.
// Title, AppName, and Prefix are filled in automatically from the WebApp.
type AppUIAssets struct {
	BodyHTML string
	AppCSS   string
	AppJS    string
	HeadHTML string
}

// NewWebUI creates a sub-mux and registers the app's root HTML page on it
// using the app's WebName() for the title and toolbar. The caller is
// responsible for mounting the returned sub-mux (via MountSubMux or a
// custom gate) so apps with special access-control wrappers can interpose.
func NewWebUI(app WebApp, prefix string, assets AppUIAssets) *http.ServeMux {
	sub := http.NewServeMux()
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		webui.WriteHTML(w, webui.RenderPage(webui.PageOpts{
			Title:    app.WebName(),
			AppName:  app.WebName(),
			Prefix:   prefix,
			BodyHTML: assets.BodyHTML,
			AppCSS:   assets.AppCSS,
			AppJS:    assets.AppJS,
			HeadHTML: assets.HeadHTML,
		}))
	})
	return sub
}

// MountSubMux registers a sub-mux under a prefix using StripPrefix.
// When prefix is empty (standalone mode), mounts at root. The sub-mux
// is wrapped with UsageReportMiddleware so every handler registered on
// it emits a per-request cost summary automatically — no handler-side
// code required. The middleware skips streaming paths (SSE event
// streams) where a held-open connection would mis-attribute work.
func MountSubMux(mux *http.ServeMux, prefix string, sub *http.ServeMux) {
	label := strings.TrimPrefix(prefix, "/")
	if label == "" {
		label = "web"
	}
	wrapped := UsageReportMiddleware(label)(sub)
	if prefix != "" {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, wrapped))
	} else {
		mux.Handle("/", wrapped)
	}
}

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
// snapshotted at entry and reported at exit via FormatUsageReport.
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
			r = r.WithContext(ctx)
			start := ProcessUsage().Snapshot()
			// Defer so the report fires even if the handler panics. The
			// Go server's own panic recovery lets the middleware's defer
			// run before the connection is torn down.
			defer func() {
				if skip.Load() {
					return
				}
				d := ProcessUsage().Diff(start)
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

	// Pre-initialize the scheduler DB so apps can call ScheduleTask during RegisterRoutes.
	PreInitScheduler()

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

	// All apps are registered — start the global scheduler so handlers added
	// during RegisterRoutes are in place before any tasks can fire.
	StartGlobalScheduler(AppContext())

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
		if isStreamingPath(path) || strings.HasSuffix(path, "/api/poll") {
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

// Hijack delegates to the underlying ResponseWriter so WebSocket upgrades
// (which require http.Hijacker) work through the logging wrapper.
func (lw *loggingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := lw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
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
    max-width: calc(100vw - 64px); /* leave room for the dashboard-back icon at top-left */
  }
  .auth-user {
    color: #8b949e;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    max-width: 40vw;
  }
  .auth-link {
    color: #8b949e; text-decoration: none;
    padding: 0.3rem 0.7rem; border: 1px solid #30363d; border-radius: 6px;
    background: #161b22; transition: border-color 0.2s, color 0.2s;
    white-space: nowrap;
  }
  .auth-link:hover { border-color: #58a6ff; color: #f0f6fc; }
  @media (max-width: 640px) {
    body { padding: 60px 12px 20px; }
    .auth-bar { top: 8px; right: 8px; gap: 0.4rem; }
    .auth-user { display: none; } /* keep just the Logout button visible on narrow screens */
    .grid { grid-template-columns: 1fr; gap: 0.75rem; }
    .card { padding: 1rem; }
  }
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
    // Drop spawned child sessions. The parent session's status
    // already reflects its current stage, so showing both the parent
    // and every child turns one logical operation into several noisy
    // rows on the dashboard.
    items = (items || []).filter(function(it) { return !it.spawned; });
    if (items.length === 0) {
      list.innerHTML = '<div style="color:#484f58;padding:0.5rem;font-size:0.85rem">No active sessions.</div>';
      return;
    }
    items.sort(function(a, b) {
      return (a.app || '').localeCompare(b.app || '');
    });
    var html = '';
    for (var i = 0; i < items.length; i++) {
      var it = items[i];
      var badge = it.queued ? '<span class="live-badge queued">Queued</span>' : '<span class="live-badge running">Running</span>';
      var app = it.app ? '<span class="live-badge" style="background:#30363d;color:#8b949e">' + it.app + '</span>' : '';
      var url = it.url || '';
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
	// Restoring is true for a session that was rehydrated from the
	// persistent queue on startup but whose work goroutine has not yet
	// begun. A race between restore and a stale browser cancel (e.g. a
	// reload-after-restart firing its auto-cancel) will otherwise kill
	// the pipeline the instant it resumes. HandleCancel rejects cancels
	// in this window with 409 Conflict; set the flag in OnRegister on
	// the restore path and clear it from PipelineConfig.OnStarted.
	Restoring bool
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

// MarkRestoring flags the session as still being restored from the
// persistent queue; HandleCancel will reject cancels with 409 until
// ClearRestoring runs. Call this from the restore path's OnRegister
// right after registering the session.
func (m *LiveSessionMap[T]) MarkRestoring(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Restoring = true
	}
}

// ClearRestoring drops the restoring flag once the work goroutine is
// actually running. Wire it to PipelineConfig.OnStarted.
func (m *LiveSessionMap[T]) ClearRestoring(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		s.Restoring = false
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
			if s.Restoring {
				m.mu.Unlock()
				Log("[web] %s %s cancel rejected — session still restoring from queue", logPrefix, id)
				http.Error(w, "session is still being restored from the queue -- retry in a moment", http.StatusConflict)
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
	Label   string `json:"topic"`             // "topic" for backwards compat with JS
	Queued  bool   `json:"queued,omitempty"`
	Spawned bool   `json:"spawned,omitempty"` // spawned by a parent app
	Status  string `json:"status,omitempty"`  // last status message
	App     string `json:"app,omitempty"`     // which app owns this session
	Path    string `json:"path,omitempty"`    // web path prefix
	URL     string `json:"url,omitempty"`     // full reconnect URL (if set, used instead of path+id)
	Order   int    `json:"order,omitempty"`   // display order for the live ribbon (lower = earlier); ties break by App name
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
			if err := sse.Send(ev); err != nil {
				return
			}
		}
		if done {
			return
		}

		// Stream new events as they arrive. Send a keepalive comment every
		// 15 seconds of silence to prevent proxy and browser timeouts during
		// long LLM pauses. Return immediately if the client disconnects.
		const heartbeat = 15 * time.Second
		sent := len(events)
		lastActivity := time.Now()
		for {
			time.Sleep(500 * time.Millisecond)
			current, isDone := m.SnapshotEvents(id)
			if current == nil {
				return
			}
			if len(current) > sent {
				for i := sent; i < len(current); i++ {
					if err := sse.Send(current[i]); err != nil {
						return
					}
				}
				sent = len(current)
				lastActivity = time.Now()
			} else if time.Since(lastActivity) >= heartbeat {
				if err := sse.SendComment("heartbeat"); err != nil {
					return
				}
				lastActivity = time.Now()
			}
			if isDone {
				return
			}
		}
	}
}
