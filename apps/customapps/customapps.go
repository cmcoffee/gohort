// Package customapps is the generic host for data-driven apps: a Page composed
// from existing ui primitives (stored as the client-shaped JSON the runtime
// already renders), plus a per-app record store and generic CRUD endpoints. No
// Go code per app, no recompile — the inverse of a hand-written WebApp package.
//
// This is the vertical slice: the host + one hardcoded demo spec ("Notes", a
// form + table over a record store) seeded on first access. The next step is an
// `app_def` Builder tool that authors AppSpecs instead of hardcoding them, and
// moving AppSpec to core so orchestrate can reach it.
//
// Mount: /custom/                 → index (a normal Go page listing apps)
//
//	/custom/<slug>/          → render the stored spec's Page (from JSON)
//	/custom/<slug>/records   → GET list | POST upsert  (Table / FormPanel)
//	/custom/<slug>/record    → DELETE one              (row action)
//	/custom/_apps            → JSON app list (index Table source)
//
// Every endpoint a component references resolves here, relative to the app's
// own mount — a spec cannot point a data binding outside it.
//
// Not enabled by default. Turn it on with a blank import in agents.go:
//
//	_ "github.com/cmcoffee/gohort/apps/customapps"
package customapps

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	"github.com/cmcoffee/gohort/tools/appscript"
)

func init() {
	RegisterApp(new(CustomApps))
}

// AppSpec + its storage now live in core (core/appspec.go) so the app_def
// Builder tool can author specs this host serves. Dot-imported, so `AppSpec`,
// `LoadAppSpec`, `SaveAppSpec`, `ListAppSpecs` below resolve to the core types.

type CustomApps struct {
	AppCore
}

// --- core.Agent interface (dashboard-only) -----------------------------------

func (T CustomApps) Name() string         { return "customapps" }
func (T CustomApps) SystemPrompt() string { return "" }
func (T CustomApps) Desc() string {
	return "Apps: host for data-driven apps composed from ui primitives."
}
func (T *CustomApps) Init() error { return T.Flags.Parse() }
func (T *CustomApps) Main() error {
	Log("customapps is dashboard-only. Start with: gohort serve")
	return nil
}

// --- core.WebApp (SimpleWebApp) ----------------------------------------------

func (T *CustomApps) WebPath() string { return "/custom" }
func (T *CustomApps) WebName() string { return "Custom Apps" }
func (T *CustomApps) WebDesc() string { return "Apps composed from primitives." }

func (T *CustomApps) Routes() {
	T.HandleFunc("/", T.route)
	// The anonymous capability-URL surface (/custom/pub/<token>/…) authenticates
	// via the unguessable token itself, so it must bypass the cookie-auth
	// middleware. Prefix registration (trailing slash) covers every token + its
	// sub-paths; handlePublic is then the sole access check for that subtree.
	RegisterPublicPath(T.WebPath() + "/pub/")
}

// route parses "/<slug>/<rest>" off the (prefix-stripped) sub-mux and
// dispatches. "_apps" is reserved for the index data feed so it can't collide
// with a real slug.
func (T *CustomApps) route(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")

	// The public capability-URL surface is served BEFORE any auth check: the
	// token in the path is its sole credential (this subtree is a registered
	// public path, so the cookie middleware already let it through anonymously).
	if path == "pub" || strings.HasPrefix(path, "pub/") {
		T.handlePublic(w, r, strings.TrimPrefix(path, "pub"))
		return
	}

	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}

	switch path {
	case "":
		T.handleIndex(w, r)
		return
	case "_apps":
		T.handleAppsList(w, r, user)
		return
	case "_app":
		// DELETE ?slug=… removes a custom app (its spec + records + active state).
		T.handleDeleteApp(w, r, user)
		return
	case "_app/enable":
		// POST ?slug=… flips an imported (disabled) app live — the review gate
		// for bundle imports.
		T.handleEnableApp(w, r, user)
		return
	case "_app/share":
		// POST ?slug=&on=… toggles authenticated (per-user-copy) sharing.
		T.handleShareApp(w, r, user)
		return
	case "_app/public":
		// POST ?slug=&on=… mints / revokes the anonymous capability URL.
		T.handlePublishApp(w, r, user)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	slug := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	// Own-first resolution: the requester's own app shadows any shared one of the
	// same slug; otherwise an app another user shared to all authenticated users.
	// ownerUser is the app's owner (== user for owned apps) — the identity its
	// sandboxed scripts run as.
	spec, ownerUser, found := T.resolveSpec(user, slug)
	if !found {
		http.NotFound(w, r)
		return
	}
	// A disabled app serves NOTHING — no page, no records, and above all no
	// data-source/action scripts. Bundle imports land disabled; the Custom
	// Apps index's Enable button is the review gate.
	if spec.Disabled {
		http.Error(w, "this app is disabled — review it and press Enable on the Custom Apps page to activate it", http.StatusForbidden)
		return
	}
	// appdb is the app's record store for THIS user: a dedicated per-app file when
	// the spec opts into a private DB, else today's shared customapps sub-store
	// (identical to udb). Everything below that reads/writes records, the active
	// marker, or co-authored content goes through appdb so a private app's data
	// stays entirely in its own file.
	appdb := T.recordBase(spec, user)
	switch {
	case rest == "":
		// Component Source/PostURL are relative ("records"), so the page must
		// live at a trailing-slash URL or they resolve one level too high.
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, T.WebPath()+"/"+slug+"/", http.StatusFound)
			return
		}
		_ = ui.RenderPageJSON(w, spec.Page, "", recordsInvalidationBridge(spec), spec.Name) // "" → resolved theme (see RegisterThemeResolver)
	case strings.HasPrefix(rest, "data/"):
		T.handleData(w, r, ownerUser, appdb, spec, strings.TrimPrefix(rest, "data/"))
	case rest == "actions":
		T.handleActionsList(w, r, spec)
	case strings.HasPrefix(rest, "action/"):
		T.handleAction(w, r, ownerUser, appdb, spec, strings.TrimPrefix(rest, "action/"))
	case rest == "records":
		T.handleRecords(w, r, appdb, spec)
	case rest == "record":
		T.handleRecord(w, r, appdb, spec)
	case rest == "chat" || strings.HasPrefix(rest, "chat/"):
		// The app's chat surface: a chat section's AgentLoopPanel points at
		// chat/* and these dispatch into orchestrate's PublicHandle* methods,
		// bound to the app's agent. Reuses ALL the chat/session/runner plumbing
		// — customapps stores no chat state of its own.
		T.handleChat(w, r, appdb, spec, strings.TrimPrefix(strings.TrimPrefix(rest, "chat"), "/"))
	default:
		http.NotFound(w, r)
	}
}

// recordsInvalidationBridge returns a <script> that refreshes the app's
// data-source panels whenever the record store changes (the "records" source is
// invalidated by a form save / action / row delete). A data source's output is
// COMPUTED from the records, so any record write must refresh every computed
// table/display — but a form's baked invalidate list only includes the data
// sources for apps authored after that wiring landed. This host-level bridge
// covers ALL apps (existing + new) without re-authoring; it skips any data
// source the firing event ALREADY carries, so a new app whose form lists them
// doesn't double-fetch. No-op for apps with no data sources.
func recordsInvalidationBridge(spec AppSpec) string {
	if len(spec.DataSources) == 0 {
		return ""
	}
	urls := make([]string, 0, len(spec.DataSources))
	for _, ds := range spec.DataSources {
		urls = append(urls, "data/"+ds.Name)
	}
	b, _ := json.Marshal(urls)
	return `<script>(function(){var U=` + string(b) + `;window.addEventListener('ui-data-changed',function(e){var s=e&&e.detail&&e.detail.sources;if(!s||s.indexOf('records')<0)return;var m=U.filter(function(u){return s.indexOf(u)<0;});if(m.length&&window.uiInvalidate)window.uiInvalidate(m);});})();</script>`
}

// findOrchestrate locates the registered OrchestrateApp so the chat routes can
// call its exported PublicHandle* methods. Cached after first hit (the registry
// is fixed at runtime). Mirrors apps/agents' accessor.
var cachedOrch *orchestrate.OrchestrateApp

func findOrchestrate() *orchestrate.OrchestrateApp {
	if cachedOrch != nil {
		return cachedOrch
	}
	a, ok := FindAgent("orchestrate")
	if !ok {
		return nil
	}
	o, ok := a.(*orchestrate.OrchestrateApp)
	if !ok {
		return nil
	}
	cachedOrch = o
	return cachedOrch
}

// handleChat dispatches the app's chat sub-routes to orchestrate. sub is the
// path after "chat/" ("" | "send" | "cancel" | "active" | "sessions" |
// "sessions/<sid>"). The agent is resolved from the app's bound AgentID; session
// + memory scope come from PublicHandle* (per calling user). For a WORKBENCH app
// (spec.BodyField set) the send path injects a co-author tool so the agent can
// write a section directly into the OPEN document's record.
func (T *CustomApps) handleChat(w http.ResponseWriter, r *http.Request, udb Database, spec AppSpec, sub string) {
	if strings.TrimSpace(spec.AgentID) == "" {
		http.Error(w, "this app has no chat agent bound", http.StatusNotFound)
		return
	}
	// "active" records which record the workbench has open, so the co-author tool
	// knows where to write. POST {id}. Stored per (user, slug) in this app's store.
	if sub == "active" {
		T.handleSetActiveRecord(w, r, udb, spec)
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	agent, ok := orch.LookupAppAgent(spec.Owner, spec.AgentID)
	if !ok {
		http.Error(w, "the app's chat agent could not be resolved", http.StatusNotFound)
		return
	}
	switch {
	case sub == "send":
		// Workbench → give the agent a co-author tool bound to THIS app's store +
		// the open document. Plain chat apps (no BodyField) get the ordinary send.
		if strings.TrimSpace(spec.BodyField) != "" {
			orch.PublicHandleSendWithAppTools(w, r, agent, T.coauthorTools(udb, spec))
			return
		}
		orch.PublicHandleSend(w, r, agent)
	case sub == "cancel":
		orch.PublicHandleCancel(w, r, agent)
	case sub == "sessions":
		orch.PublicHandleSessionList(w, r, agent.ID)
	case strings.HasPrefix(sub, "sessions/"):
		orch.PublicHandleSessionOne(w, r, agent.ID, strings.TrimPrefix(sub, "sessions/"))
	default:
		http.NotFound(w, r)
	}
}

// --- index (a normal Go-built page) ------------------------------------------

func (T *CustomApps) handleIndex(w http.ResponseWriter, r *http.Request) {
	ui.Page{
		Title:     "Custom Apps",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "900px",
		// The Share button is a client action: it opens a modal to pick the
		// sharing modes and copy the public link. App-specific behavior, so it
		// lives here (the app's own page) via the client-action registry — never
		// in core/ui.
		ExtraHeadHTML: shareModalScript,
		Sections: []ui.Section{{
			Title:    "Your apps",
			Subtitle: "Data-driven apps composed from ui primitives.",
			Body: ui.Table{
				Source: "_apps",
				RowKey: "slug",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "desc", Flex: 2, Mute: true},
					{Field: "status", Flex: 1, Mute: true},
				},
				EmptyText: "No custom apps yet.",
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Open", Method: "GET", PostTo: "{slug}/", HideIf: "disabled"},
					{Type: "button", Label: "Enable", Method: "POST", PostTo: "_app/enable?slug={slug}", OnlyIf: "disabled",
						Confirm: "Enable this imported app? Review its data-source and action scripts first — they run in your sandbox once the app is live."},
					// One Share button opens the sharing modal (customapps_share).
					{Type: "button", Label: "Share", Method: "client", PostTo: "customapps_share", OnlyIf: "mine"},
					{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "_app?slug={slug}", OnlyIf: "mine", Variant: "danger",
						Confirm: "Delete this app and all its data? This can't be undone."},
				},
			},
		}},
	}.ServeHTTP(w, r)
}

// shareModalScript registers the "customapps_share" client action: a modal that
// toggles the two sharing modes (each applied immediately via _app/share and
// _app/public) and, when a public link exists, shows it in a read-only field
// with a Copy button. The link is the ABSOLUTE server URL the endpoints return
// (DashboardURL-based), so copying works even from the gohort-desktop client
// (which reaches the server over 127.0.0.1). No backticks in this string — it is
// embedded in a Go raw literal, and a backtick would terminate it.
const shareModalScript = `<script>
window.uiRegisterClientAction('customapps_share', function(ctx) {
  var rec = ctx.record || {};
  var slug = rec.slug;
  function truthy(v){ return v === '1' || v === 1 || v === true; }
  function makeToggle(label, help, checked, onChange) {
    var wrap = document.createElement('label');
    wrap.style.cssText = 'display:block;cursor:pointer';
    var top = document.createElement('div');
    top.style.cssText = 'display:flex;align-items:center;gap:0.5rem;font-weight:600';
    var cb = document.createElement('input'); cb.type = 'checkbox'; cb.checked = !!checked;
    top.appendChild(cb); top.appendChild(document.createTextNode(label));
    var h = document.createElement('div');
    h.style.cssText = 'font-size:0.78rem;color:var(--text-mute);margin:0.25rem 0 0 1.6rem;line-height:1.4';
    h.textContent = help;
    wrap.appendChild(top); wrap.appendChild(h);
    cb.addEventListener('change', function(){ onChange(cb.checked, cb); });
    return wrap;
  }
  function post(url, cb, onOk) {
    fetch(url, {method:'POST'}).then(function(r){
      if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
      return r.json();
    }).then(function(d){ if (onOk) onOk(d || {}); }).catch(function(e){
      if (cb) cb.checked = !cb.checked;
      (window.uiAlert || window.alert)('Sharing failed: ' + e.message);
    });
  }
  window.uiOpenModal({
    title: 'Share "' + (rec.name || slug) + '"',
    width: '520px',
    mount: function(body) {
      body.appendChild(makeToggle(
        'Share with signed-in users',
        'Every signed-in user gets their own copy. Your data-source and action scripts run with your credentials for them.',
        truthy(rec.shared),
        function(on, cb){ post('_app/share?slug=' + encodeURIComponent(slug) + '&on=' + on, cb); }
      ));
      var linkRow = document.createElement('div');
      linkRow.style.cssText = 'margin:0.4rem 0 0 1.6rem;gap:0.4rem;align-items:center';
      linkRow.style.display = truthy(rec.public) ? 'flex' : 'none';
      var input = document.createElement('input');
      input.type = 'text'; input.readOnly = true; input.value = rec.public_url || '';
      input.style.cssText = 'flex:1 1 auto;min-width:0;padding:0.35rem 0.5rem;font-size:0.8rem;border:1px solid var(--border);border-radius:4px;background:var(--bg-2);color:var(--text)';
      var copyBtn = document.createElement('button');
      copyBtn.type = 'button'; copyBtn.className = 'ui-row-btn'; copyBtn.textContent = 'Copy';
      copyBtn.addEventListener('click', function(){
        var v = input.value; if (!v) return;
        function done(){ copyBtn.textContent = 'Copied!'; setTimeout(function(){ copyBtn.textContent = 'Copy'; }, 1500); }
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(v).then(done, function(){ input.select(); document.execCommand('copy'); done(); });
        } else { input.select(); document.execCommand('copy'); done(); }
      });
      linkRow.appendChild(input); linkRow.appendChild(copyBtn);
      var pub = makeToggle(
        'Public link (anyone with the URL)',
        'Anonymous, read-only. Your data sources run with your credentials for anyone who has the link. Nothing is saved. Revoke anytime by turning this off.',
        truthy(rec.public),
        function(on, cb){
          post('_app/public?slug=' + encodeURIComponent(slug) + '&on=' + on, cb, function(d){
            if (on && d.url) { input.value = d.url; linkRow.style.display = 'flex'; }
            else { linkRow.style.display = 'none'; }
          });
        }
      );
      body.appendChild(pub);
      body.appendChild(linkRow);
    },
    actions: [{label: 'Done', primary: true, onClick: function(api){ api.close(); if (ctx.reload) ctx.reload(); }}]
  });
});
</` + `script>`


// handleDeleteApp removes a custom app: its spec, its per-app record store, and
// any workbench active-selection state. The demo "notes" app re-seeds on next
// visit (by design); delete a real app and it stays gone.
func (T *CustomApps) handleDeleteApp(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if slug == "" {
		http.Error(w, "slug required", http.StatusBadRequest)
		return
	}
	// Drop the app's data from wherever it actually lives — the per-app private
	// file for a private app, else the shared store — before removing the spec.
	// Loaded before deletion so PrivateDB is still known.
	spec, _ := loadSpec(user, slug)
	appdb := T.recordBase(spec, user)
	// Clear any sharing this app carried so a deleted app leaves no dangling
	// index entry (a stale shared slug, or a live capability URL).
	SetSharedOwner(T.DB, sharedAppsIndex, slug, user, false)
	if spec.PublicToken != "" {
		T.DB.Unset(publicAppsIndex, spec.PublicToken)
	}
	DeleteAppSpec(user, slug)      // shared per-owner spec store
	appdb.Drop(recTable(slug))     // this app's records
	appdb.Unset(activeTable, slug) // workbench open-document marker
	writeJSON(w, map[string]bool{"ok": true})
}

// recordBase returns the per-user record store for one app: a dedicated private
// database file when the spec opts in (PrivateDB), else today's shared customapps
// sub-store. The two are namespace-compatible — same UserDB scoping, same table
// names — so switching an app onto its own file changes only WHERE the records
// live, not how they're keyed. A nil private handle (opener unwired) falls back
// to the shared store so nothing breaks outside a serve context.
func (T *CustomApps) recordBase(spec AppSpec, uid string) Database {
	if spec.PrivateDB {
		if db := OpenCustomAppDB(spec.Owner, spec.Slug); db != nil {
			return UserDB(db, uid)
		}
	}
	return UserDB(T.DB, uid)
}

func (T *CustomApps) handleAppsList(w http.ResponseWriter, r *http.Request, owner string) {
	out := []map[string]string{}
	seen := map[string]bool{}
	for _, s := range listSpecs(owner) {
		seen[s.Slug] = true
		// "mine" gates the owner-only Share/Delete actions. shared/public/public_url
		// carry the current sharing state into the Share modal (a client action)
		// so it opens pre-filled and can show + copy the live public link.
		row := map[string]string{"slug": s.Slug, "name": s.Name, "desc": s.Desc, "mine": "1"}
		status := "private"
		if s.Shared {
			row["shared"] = "1"
			status = "shared to users"
		}
		if s.PublicToken != "" {
			row["public"] = "1"
			row["public_url"] = T.publicURL(s.PublicToken) // absolute — copyable off 127.0.0.1
			if s.Shared {
				status = "shared to users + public link"
			} else {
				status = "public link"
			}
		}
		if s.Disabled {
			row["disabled"] = "1"
			status = "disabled — review, then Enable"
		}
		row["status"] = status
		out = append(out, row)
	}
	// Apps other users shared to all authenticated users — offered here as a
	// per-user copy. Own apps shadow a same-slug shared one, so skip those.
	for slug, ownerName := range ListSharedOwners(T.DB, sharedAppsIndex) {
		if ownerName == owner || seen[slug] {
			continue
		}
		s, ok := loadSpec(ownerName, slug)
		if !ok || !s.Shared || s.Disabled {
			continue
		}
		out = append(out, map[string]string{
			"slug": s.Slug, "name": s.Name, "desc": s.Desc,
			"status": "shared by " + ownerName,
		})
	}
	writeJSON(w, out)
}

// handleEnableApp flips an imported (disabled) app live: POST ?slug=…. This is
// the review gate for bundle imports — a recipe can carry sandboxed
// data-source/action scripts, and none of them run until the owner has looked
// and enabled the app here. Enabling an already-live app is a no-op.
func (T *CustomApps) handleEnableApp(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if slug == "" {
		http.Error(w, "slug required", http.StatusBadRequest)
		return
	}
	spec, ok := loadSpec(user, slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if spec.Disabled {
		spec.Disabled = false
		SaveAppSpec(spec)
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// --- script-backed data sources (the "logic" seam) ---------------------------

// handleData serves a table/display section's script-backed data endpoint:
// GET /custom/<slug>/data/<name>. It runs the named AppDataSource script
// (sandboxed) with the REQUESTER's stored records + the request's query params
// as input, and passes the script's JSON stdout straight through. The script
// runs in the OWNER's sandbox (owner param) with the owner's network gate and
// credentials — so a SHARED app's trusted logic executes as the owner while
// reading the opening user's own records (the per-user-copy model). For an
// owned app owner == requester, so this is byte-identical to the old behavior.
// Read-only: a data source computes a view, it never writes the store.
func (T *CustomApps) handleData(w http.ResponseWriter, r *http.Request, owner string, udb Database, spec AppSpec, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ds *AppDataSource
	for i := range spec.DataSources {
		if spec.DataSources[i].Name == name {
			ds = &spec.DataSources[i]
			break
		}
	}
	if ds == nil || strings.TrimSpace(ds.Script) == "" {
		http.NotFound(w, r)
		return
	}

	// Gather the REQUESTER's stored records (udb) to hand the script as input.
	tbl := recTable(spec.Slug)
	records := []map[string]any{}
	for _, k := range udb.Keys(tbl) {
		var rec map[string]any
		if udb.Get(tbl, k, &rec) {
			records = append(records, rec)
		}
	}
	recJSON, _ := json.Marshal(records)

	// Args become env vars in the script: the records JSON, plus each query param.
	args := map[string]any{"records": string(recJSON)}
	for k, vs := range r.URL.Query() {
		if len(vs) > 0 {
			args[k] = vs[0]
		}
	}

	// The script executes in the OWNER's context (sandbox identity + hook DB), so
	// a shared app's data source reaches the owner's credentials/integrations.
	out, err := cachedRunDataSource(owner, T.recordBase(spec, owner), spec.Slug, *ds, args)
	if err != nil {
		Log("[customapps] data source %q/%q failed: %v", spec.Slug, name, err)
		http.Error(w, "data source failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	trimmed := strings.TrimSpace(out)
	if !json.Valid([]byte(trimmed)) {
		Log("[customapps] data source %q/%q returned non-JSON (first 200B): %.200s", spec.Slug, name, trimmed)
		http.Error(w, "the data source script must print a JSON value (array for a table, object for a display) to stdout", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(trimmed))
}

// runDataSource executes one data-source script and returns its stdout.
func runDataSource(user string, db Database, slug string, ds AppDataSource, args map[string]any) (string, error) {
	return runAppScript(user, db, slug, "data", ds.Name, ds.Language, ds.Script, ds.Capabilities, args)
}

// dataSourceCacheTTL is how long a data source's output is reused before it is
// recomputed. Short on purpose: long enough to collapse a page's initial load,
// its auto-refresh poll, and any parallel tab into one execution of a script
// that may make many slow external fetches; short enough that a live dashboard
// still feels current.
const dataSourceCacheTTL = 8 * time.Second

type dsCacheEntry struct {
	out     string
	expires time.Time
}

// dsInFlight is one execution other callers with the same key wait on instead
// of launching their own — single-flight collapse.
type dsInFlight struct {
	done chan struct{}
	out  string
	err  error
}

var (
	dsCacheMu    sync.Mutex
	dsCache      = map[string]dsCacheEntry{}
	dsInFlightCalls  = map[string]*dsInFlight{}
)

// dsCacheKey identifies one data-source computation by everything its output
// depends on: the owner, the app, the source's NAME **and script/language/caps**
// (so editing the script busts the cache — vital for the author's rapid
// iterate→verify loop, which reuses the same records + params), plus the input
// records and query params. Any change to any of these misses the cache and
// recomputes; identical repeats within the TTL reuse the result.
func dsCacheKey(user, slug string, ds AppDataSource, args map[string]any) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", user, slug, ds.Name, ds.Language)
	io.WriteString(h, ds.Script)
	h.Write([]byte{0})
	for _, c := range ds.Capabilities {
		io.WriteString(h, c)
		h.Write([]byte{0})
	}
	h.Write([]byte{0})
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%v\x00", k, args[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cachedRunDataSource wraps runDataSource with a short-TTL output cache and
// single-flight execution. It is used only on the LIVE serve path (handleData)
// — the authoring test/verify path always runs scripts fresh. Errors are never
// cached (so a transient failure retries immediately), though a burst of
// concurrent identical failing calls still shares one execution.
func cachedRunDataSource(user string, db Database, slug string, ds AppDataSource, args map[string]any) (string, error) {
	key := dsCacheKey(user, slug, ds, args)
	now := time.Now()

	dsCacheMu.Lock()
	if e, ok := dsCache[key]; ok && now.Before(e.expires) {
		dsCacheMu.Unlock()
		return e.out, nil
	}
	if call, ok := dsInFlightCalls[key]; ok {
		// Someone is already computing this exact view — wait for it.
		dsCacheMu.Unlock()
		<-call.done
		return call.out, call.err
	}
	call := &dsInFlight{done: make(chan struct{})}
	dsInFlightCalls[key] = call
	dsCacheMu.Unlock()

	out, err := runDataSource(user, db, slug, ds, args)

	dsCacheMu.Lock()
	call.out, call.err = out, err
	if err == nil {
		dsCache[key] = dsCacheEntry{out: out, expires: time.Now().Add(dataSourceCacheTTL)}
	}
	delete(dsInFlightCalls, key)
	// Opportunistic sweep: a record write changes the key, so churned entries
	// would otherwise accumulate. Cheap at this cardinality.
	for k, e := range dsCache {
		if now.After(e.expires) {
			delete(dsCache, k)
		}
	}
	dsCacheMu.Unlock()
	close(call.done)
	return out, err
}

// runAppScript executes one custom-app script (a data source or an action) and
// returns its stdout. Delegates to the shared appscript.Run seam so the host and
// the app_def test action run scripts through byte-identical machinery.
func runAppScript(user string, db Database, slug, kind, name, language, script string, caps []string, args map[string]any) (string, error) {
	return appscript.Run(user, db, slug, kind, name, language, script, caps, args)
}

// handleActionsList feeds the actions section's button list: one {name, button,
// desc, confirm} per declared action. GET only.
func (T *CustomApps) handleActionsList(w http.ResponseWriter, r *http.Request, spec AppSpec) {
	type item struct {
		Name    string `json:"name"`
		Button  string `json:"button"`
		Desc    string `json:"desc,omitempty"`
		Confirm string `json:"confirm,omitempty"`
	}
	out := []item{}
	for _, a := range spec.Actions {
		label := strings.TrimSpace(a.Label)
		if label == "" {
			label = a.Name
		}
		out = append(out, item{Name: a.Name, Button: label, Desc: a.Desc, Confirm: a.Confirm})
	}
	writeJSON(w, out)
}

// handleAction runs a named action script: POST /custom/<slug>/action/<name>.
// The app's stored records + the request's params go in; the script prints a
// JSON object {message?, records?}. The FRAMEWORK upserts any returned records
// into the store (so they reach the viewer — the script never writes the store),
// and returns {message} for the button. The script runs in the OWNER's sandbox
// (owner param), but any records it returns are upserted into the REQUESTER's
// store (udb) — so on a shared app a user's action runs the owner's trusted
// logic against, and saves into, that user's own copy. owner == requester for
// an owned app.
func (T *CustomApps) handleAction(w http.ResponseWriter, r *http.Request, owner string, udb Database, spec AppSpec, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var act *AppAction
	for i := range spec.Actions {
		if spec.Actions[i].Name == name {
			act = &spec.Actions[i]
			break
		}
	}
	if act == nil || strings.TrimSpace(act.Script) == "" {
		http.NotFound(w, r)
		return
	}

	// Hand the script the app's records + request params (query + JSON body).
	tbl := recTable(spec.Slug)
	records := []map[string]any{}
	for _, k := range udb.Keys(tbl) {
		var rec map[string]any
		if udb.Get(tbl, k, &rec) {
			records = append(records, rec)
		}
	}
	recJSON, _ := json.Marshal(records)
	args := map[string]any{"records": string(recJSON)}
	for k, vs := range r.URL.Query() {
		if len(vs) > 0 {
			args[k] = vs[0]
		}
	}
	if r.Body != nil {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			for k, v := range body {
				args[k] = fmt.Sprint(v)
			}
		}
	}

	out, err := runAppScript(owner, T.recordBase(spec, owner), spec.Slug, "action", act.Name, act.Language, act.Script, act.Capabilities, args)
	if err != nil {
		Log("[customapps] action %q/%q failed: %v", spec.Slug, name, err)
		http.Error(w, "action failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Parse the script's JSON object: {message?, records?}. The framework owns
	// persistence — it upserts the returned records so they reach the viewer.
	var result struct {
		Message string           `json:"message"`
		Records []map[string]any `json:"records"`
	}
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && json.Unmarshal([]byte(trimmed), &result) != nil {
		Log("[customapps] action %q/%q returned non-object JSON (first 200B): %.200s", spec.Slug, name, trimmed)
		http.Error(w, "the action script must print a JSON object {message?, records?} to stdout", http.StatusInternalServerError)
		return
	}
	saved := 0
	for _, rec := range result.Records {
		if rec == nil {
			continue
		}
		id, _ := rec[spec.RecordKey].(string)
		if strings.TrimSpace(id) == "" {
			id = newID()
			rec[spec.RecordKey] = id
		}
		if _, ok := rec["created"]; !ok {
			rec["created"] = time.Now().UTC().Format(time.RFC3339)
		}
		udb.Set(tbl, id, rec)
		saved++
	}
	msg := strings.TrimSpace(result.Message)
	if msg == "" {
		if saved > 0 {
			msg = fmt.Sprintf("Done — %d record(s) updated.", saved)
		} else {
			msg = "Done."
		}
	}
	writeJSON(w, map[string]any{"message": msg, "saved": saved})
}

// --- generic record store ----------------------------------------------------

func recTable(slug string) string { return "custom_records:" + slug }

func (T *CustomApps) handleRecords(w http.ResponseWriter, r *http.Request, udb Database, spec AppSpec) {
	tbl := recTable(spec.Slug)
	switch r.Method {
	case http.MethodGet:
		out := []map[string]any{}
		for _, k := range udb.Keys(tbl) {
			var rec map[string]any
			if udb.Get(tbl, k, &rec) {
				out = append(out, rec)
			}
		}
		writeJSON(w, out)
	case http.MethodPost:
		var rec map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if rec == nil {
			rec = map[string]any{}
		}
		// Key on RecordKey; allocate one for new records.
		id, _ := rec[spec.RecordKey].(string)
		if strings.TrimSpace(id) == "" {
			id = newID()
			rec[spec.RecordKey] = id
		}
		if _, ok := rec["created"]; !ok {
			rec["created"] = time.Now().UTC().Format(time.RFC3339)
		}
		udb.Set(tbl, id, rec)
		writeJSON(w, rec)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (T *CustomApps) handleRecord(w http.ResponseWriter, r *http.Request, udb Database, spec AppSpec) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	tbl := recTable(spec.Slug)
	switch r.Method {
	case http.MethodGet:
		var rec map[string]any
		if !udb.Get(tbl, id, &rec) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, rec)
	case http.MethodDelete:
		udb.Unset(tbl, id)
		writeJSON(w, map[string]bool{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- spec storage (core) + demo seed -----------------------------------------
//
// Specs live in the SHARED per-owner store (core/appspec.go via RootDB), NOT in
// customapps' own DB bucket — app_def (running under orchestrate's bucket) and
// this host must agree on one location, so both key by owner. loadSpec/listSpecs
// take the current user as owner.

func loadSpec(owner, slug string) (AppSpec, bool) { return LoadAppSpec(owner, slug) }
func listSpecs(owner string) []AppSpec            { return ListAppSpecs(owner) }

// --- sharing (authenticated per-user copy + public capability URL) ------------
//
// Two independent, owner-controlled modes over the per-owner spec store:
//   • Shared (authenticated): the app is offered to every logged-in user as a
//     per-user COPY — shared definition + owner-run scripts, each user's own
//     records. A global slug→owner index makes it discoverable; slugs are a
//     single shared namespace (collisions rejected at share time).
//   • Public (anonymous): the app is published at /custom/pub/<token>/ as a
//     STATELESS, read/compute-only capability URL. A token→(owner,slug) index
//     resolves it; the token is the sole credential; unpublishing revokes it.
// Both indexes live in the customapps app-wide store (T.DB), NOT a per-user DB —
// discovery must work regardless of who asks. Primitives come from core/sharing.go.

const (
	sharedAppsIndex = "shared_custom_apps" // slug -> owner username
	publicAppsIndex = "public_custom_apps" // capability token -> publicRef
)

// publicRef is what a capability token resolves to: the owner + slug whose spec
// the token publishes.
type publicRef struct {
	Owner string `json:"owner"`
	Slug  string `json:"slug"`
}

// newPublicToken mints an unguessable capability token (128 bits, hex). The
// token IS the access control for a public app, so it must not be enumerable.
func newPublicToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// publicURL builds the ABSOLUTE capability URL for a token, using the
// deployment's configured public base (DashboardURL) rather than the request
// host. This is what makes a copied link shareable: the gohort-desktop client
// reaches the server over loopback, so a host-relative link would copy as
// 127.0.0.1 — useless to anyone else. DashboardURL resolves to the operator's
// configured WebBaseURL (the real server name) when set.
func (T *CustomApps) publicURL(token string) string {
	return DashboardURL() + T.WebPath() + "/pub/" + token + "/"
}

// lookupPublicApp resolves a capability token to its owner+slug, if published.
func lookupPublicApp(appDB Database, token string) (publicRef, bool) {
	var ref publicRef
	if appDB == nil || token == "" {
		return ref, false
	}
	if appDB.Get(publicAppsIndex, token, &ref) && ref.Owner != "" && ref.Slug != "" {
		return ref, true
	}
	return ref, false
}

// resolveSpec finds the app a request should serve: the requester's OWN app
// first (an owned slug shadows any shared one), else an app another user has
// shared to all authenticated users. ownerUser is the app's owner (== reqUser
// for owned apps) — the identity its sandboxed scripts run as.
func (T *CustomApps) resolveSpec(reqUser, slug string) (AppSpec, string, bool) {
	if s, ok := loadSpec(reqUser, slug); ok {
		return s, reqUser, true
	}
	if owner, ok := LookupSharedOwner(T.DB, sharedAppsIndex, slug); ok && owner != reqUser {
		if s, ok := loadSpec(owner, slug); ok && s.Shared {
			return s, owner, true
		}
	}
	return AppSpec{}, "", false
}

// handleShareApp toggles authenticated (per-user-copy) sharing for an app the
// requester owns: POST /custom/_app/share?slug=…&on=true|false. Owner-gated by
// construction (the spec is looked up in the requester's own store). Sharing a
// slug another user already shares is rejected — shared slugs are one global
// namespace.
func (T *CustomApps) handleShareApp(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	spec, ok := loadSpec(user, slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	on := r.URL.Query().Get("on") != "false" // default: turn sharing ON
	if on {
		if owner, shared := LookupSharedOwner(T.DB, sharedAppsIndex, slug); shared && owner != user {
			http.Error(w, "another user already shares an app at this slug — rename yours to share it", http.StatusConflict)
			return
		}
	}
	spec.Shared = on
	SaveAppSpec(spec)
	SetSharedOwner(T.DB, sharedAppsIndex, slug, user, on)
	writeJSON(w, map[string]any{"ok": true, "shared": on})
}

// handlePublishApp mints or revokes the anonymous capability URL for an app the
// requester owns: POST /custom/_app/public?slug=…&on=true|false. Publishing
// mints a fresh token (if none) and registers it; unpublishing deletes the
// token from the index — instantly revoking any shared link — and clears it
// from the spec. Returns the public URL on publish so the UI can surface it.
func (T *CustomApps) handlePublishApp(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	spec, ok := loadSpec(user, slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	on := r.URL.Query().Get("on") != "false"
	if on {
		if spec.PublicToken == "" {
			spec.PublicToken = newPublicToken()
		}
		SaveAppSpec(spec)
		T.DB.Set(publicAppsIndex, spec.PublicToken, publicRef{Owner: user, Slug: slug})
		writeJSON(w, map[string]any{"ok": true, "public": true, "url": T.publicURL(spec.PublicToken)})
		return
	}
	if spec.PublicToken != "" {
		T.DB.Unset(publicAppsIndex, spec.PublicToken) // revoke the link
	}
	spec.PublicToken = ""
	SaveAppSpec(spec)
	writeJSON(w, map[string]any{"ok": true, "public": false})
}

// handlePublic serves the anonymous capability-URL surface:
// /custom/pub/<token>/… . The token (validated against the public index) is the
// sole credential — this subtree is a registered public path, so the cookie
// middleware already passed it through unauthenticated. STATELESS and
// read/compute-only: the page renders, data sources RUN in the owner's sandbox
// with query-param input, "records" is always empty (no anonymous store), and
// every write / action-fire / chat endpoint is refused.
func (T *CustomApps) handlePublic(w http.ResponseWriter, r *http.Request, rest string) {
	rest = strings.Trim(rest, "/")
	parts := strings.SplitN(rest, "/", 2)
	token := parts[0]
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}
	ref, ok := lookupPublicApp(T.DB, token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	spec, ok := loadSpec(ref.Owner, ref.Slug)
	// Defense in depth: the spec must still name THIS token and not be disabled;
	// index/spec drift or an unpublished/disabled app reads as gone.
	if !ok || spec.PublicToken != token || spec.Disabled {
		http.NotFound(w, r)
		return
	}
	switch {
	case sub == "":
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, T.WebPath()+"/pub/"+token+"/", http.StatusFound)
			return
		}
		// No record-invalidation bridge: nothing is stored on the public surface.
		_ = ui.RenderPageJSON(w, T.publicPageBytes(spec, token), "", "", spec.Name)
	case strings.HasPrefix(sub, "data/"):
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		T.handlePublicData(w, r, spec, strings.TrimPrefix(sub, "data/"))
	case sub == "records":
		// No public store: a record-backed section fetches this on load, so it
		// must return valid (empty) JSON rather than 404 (which would error the
		// page). Writes fall through to the refusal below.
		if r.Method != http.MethodGet {
			http.Error(w, "not available on a public app", http.StatusForbidden)
			return
		}
		writeJSON(w, []map[string]any{})
	case sub == "actions":
		// A public app exposes no action buttons; return an empty list so an
		// actions section renders (empty) instead of erroring.
		writeJSON(w, []map[string]any{})
	default:
		// record write/delete, action fire, chat — none run for anonymous users.
		http.Error(w, "not available on a public app", http.StatusForbidden)
	}
}

// publicPageBytes adapts the owner's stored page for anonymous serving:
//   - Rewrites the app's own AUTH-GATED mount prefix (/custom/<slug>/) to the
//     public capability mount (/custom/pub/<token>/). The typed sections use
//     RELATIVE sources ("data/<name>") that already resolve against the page
//     URL, but a hand-written html section commonly fetches an ABSOLUTE path
//     ("/custom/<slug>/data/<name>") — served verbatim that points back at the
//     gated slug route and 302s to login (works for the owner, breaks for an
//     anonymous visitor). The prefix rewrite makes those absolute self-refs hit
//     the token-scoped endpoint instead.
//   - Marks the page public so the runtime drops the live-sessions pill (which
//     would poll the gated /api/live), and removes the Back link (it points at
//     the owner's gated /custom/ index — meaningless to an anonymous visitor).
func (T *CustomApps) publicPageBytes(spec AppSpec, token string) []byte {
	oldPrefix := []byte(T.WebPath() + "/" + spec.Slug + "/")
	newPrefix := []byte(T.WebPath() + "/pub/" + token + "/")
	var page map[string]any
	if err := json.Unmarshal(spec.Page, &page); err != nil {
		// Unparseable page: still rewrite the raw bytes so data fetches resolve.
		return bytes.ReplaceAll(spec.Page, oldPrefix, newPrefix)
	}
	page["public"] = true    // runtime: suppress the live-sessions pill
	delete(page, "back_url") // no Back link to the gated dashboard
	out, err := json.Marshal(page)
	if err != nil {
		out = spec.Page
	}
	return bytes.ReplaceAll(out, oldPrefix, newPrefix)
}

// handlePublicData runs one data source for the public surface: in the OWNER's
// sandbox, over the OWNER's stored records, with per-request input from query
// params. A public app is the owner's app served anonymously — its data source
// must see the config the owner set up (e.g. WHICH site to pull), so it reads
// the owner's records exactly as it would for the owner logged in. Only anonymous
// WRITES are withheld (records POST / actions 403 in handlePublic); the raw
// record store is never dumped by the framework — it reaches the response only
// if the owner's own script computes and emits it. Reuses the same cache +
// single-flight as the authenticated path.
func (T *CustomApps) handlePublicData(w http.ResponseWriter, r *http.Request, spec AppSpec, name string) {
	var ds *AppDataSource
	for i := range spec.DataSources {
		if spec.DataSources[i].Name == name {
			ds = &spec.DataSources[i]
			break
		}
	}
	if ds == nil || strings.TrimSpace(ds.Script) == "" {
		http.NotFound(w, r)
		return
	}
	if spec.Owner == "" {
		http.Error(w, "public app has no owner context", http.StatusInternalServerError)
		return
	}
	ownerDB := T.recordBase(spec, spec.Owner)
	// Feed the owner's stored records (their app config) plus each query param.
	tbl := recTable(spec.Slug)
	records := []map[string]any{}
	for _, k := range ownerDB.Keys(tbl) {
		var rec map[string]any
		if ownerDB.Get(tbl, k, &rec) {
			records = append(records, rec)
		}
	}
	recJSON, _ := json.Marshal(records)
	args := map[string]any{"records": string(recJSON)}
	for k, vs := range r.URL.Query() {
		if len(vs) > 0 {
			args[k] = vs[0]
		}
	}
	out, err := cachedRunDataSource(spec.Owner, ownerDB, spec.Slug, *ds, args)
	if err != nil {
		Log("[customapps] PUBLIC data source %q/%q failed: %v", spec.Slug, name, err)
		http.Error(w, "data source failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	trimmed := strings.TrimSpace(out)
	if !json.Valid([]byte(trimmed)) {
		Log("[customapps] PUBLIC data source %q/%q returned non-JSON (first 200B): %.200s", spec.Slug, name, trimmed)
		http.Error(w, "the data source script must print a JSON value to stdout", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(trimmed))
}

// --- helpers -----------------------------------------------------------------

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
