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
// Mount: /apps/                 → index (a normal Go page listing apps)
//
//	/apps/<slug>/          → render the stored spec's Page (from JSON)
//	/apps/<slug>/records   → GET list | POST upsert  (Table / FormPanel)
//	/apps/<slug>/record    → DELETE one              (row action)
//	/apps/_apps            → JSON app list (index Table source)
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
	"github.com/cmcoffee/gohort/core/appadmin"
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

// customAppsLegacyPath is where this app was mounted before it moved. Kept as
// a constant because two things have to agree about it — the redirect and the
// grant migration — and a second literal is how they stop agreeing.
const customAppsLegacyPath = "/custom"

func (T *CustomApps) Main() error {
	Log("customapps is dashboard-only. Start with: gohort serve")
	return nil
}

// --- core.WebApp (SimpleWebApp) ----------------------------------------------

// WebPath is /apps, not /custom.
//
// "/custom" named how the app was MADE. Every compiled app owns a top-level
// path because each has a package to own one; an app authored at runtime has
// no package, so it shares a host, and /apps is what that host is — the same
// relationship /agents/<slug> already has to an exposed agent.
//
// The old mount still answers (see RegisterLegacyMount below): a published
// capability link is a URL somebody was HANDED, and breaking those is not a
// rename, it is a deletion they find out about later.
func (T *CustomApps) WebPath() string { return "/apps" }
func (T *CustomApps) WebName() string { return "Custom Apps" }
func (T *CustomApps) WebDesc() string { return "Apps composed from primitives." }

func (T *CustomApps) Routes() {
	T.HandleFunc("/", T.route)
	// Wire self-updating apps: register the scheduled-action trigger dispatcher and
	// the spec-lifecycle hooks that keep each app's standing triggers in sync.
	T.registerScheduling()
	// The anonymous capability-URL surface (/apps/pub/<token>/…) authenticates
	// via the unguessable token itself, so it must bypass the cookie-auth
	// middleware. Prefix registration (trailing slash) covers every token + its
	// sub-paths; handlePublic is then the sole access check for that subtree.
	RegisterPublicPath(T.WebPath() + "/pub/")

	// This app used to live at /custom. Bookmarks, pasted links and every
	// published capability URL still say so, and a grant stored against the
	// old path is rewritten once at startup (MigrateAppPathGrants) rather than
	// silently ceasing to match.
	RegisterLegacyMount(customAppsLegacyPath, T.WebPath())
	if AuthDB != nil {
		// Once, keyed by the rename. A grant is a path string, so the move has
		// to reach the three places one is stored or the app is still there
		// and the grant no longer names it.
		MigrateAppPathGrants(AuthDB(), customAppsLegacyPath, T.WebPath())
	}

	// The operator's tab: one section per custom app, contributed as a SOURCE
	// because these rows are records people write while the server runs.
	T.registerAdminControls()
	RegisterAdminSectionSource(T.adminSections)
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
	case "_admin/revoke-link", "_admin/reach", "_admin/tiers", "_admin/review", "_admin/scripts":
		// Operator controls on somebody else's app. Admin-gated inside the
		// handler rather than by where the control was rendered: a URL is a
		// URL, and whoever finds it is not necessarily who was shown it.
		T.handleAdmin(w, r, user, strings.TrimPrefix(path, "_admin/"))
		return
	case "_app/schedule":
		// POST ?slug=&on=… pauses / resumes an app's self-updating schedule.
		T.handleScheduleToggle(w, r, user)
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
	// A shared app is somebody else's: the framework grant has to admit this
	// user to it. Own apps skip this — an owner needs no grant to their own
	// work — and the operator allowlist was already applied in resolveSpec.
	if ownerUser != user && !T.sharedAppReachableBy(r, slug) {
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
	// Static assets — read-only, owner-scoped, extension-allowlisted. Served
	// before anything else so an app can reference its own artwork without the
	// asset name having to dodge every other route below.
	case rest == "assets" || strings.HasPrefix(rest, "assets/"):
		T.handleAsset(w, r, ownerUser, slug, strings.TrimPrefix(strings.TrimPrefix(rest, "assets"), "/"))
		return
	case rest == "":
		// Component Source/PostURL are relative ("records"), so the page must
		// live at a trailing-slash URL or they resolve one level too high.
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, T.WebPath()+"/"+slug+"/", http.StatusFound)
			return
		}
		// A view keeps any self-updating schedule alive (and re-arms an idle-paused
		// one). Keyed by the app's owner, so a shared app the owner still watches
		// keeps its owner-side tracker running.
		T.touchAppView(spec)
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
	case rest == "pipeline" || strings.HasPrefix(rest, "pipeline/"):
		// The app's RUN surface, the same trick one level over: a pipeline
		// section's PipelinePanel points at pipeline/*, and these dispatch into
		// orchestrate's run machinery bound to the app's pipeline. customapps
		// stores no transcript of its own.
		T.handlePipeline(w, r, spec, strings.TrimPrefix(strings.TrimPrefix(rest, "pipeline"), "/"))
	default:
		http.NotFound(w, r)
	}
}

// latestRunForApp returns the app pipeline's last finished run for the CALLING
// user, as (final output, full run JSON). Both empty when the app binds no
// pipeline or nothing has finished — an action script then reads its defaults
// and reports honestly, instead of failing on a missing variable.
//
// The full JSON carries the per-stage blocks, because "save the debate" means
// the rounds, not just the verdict.
func (T *CustomApps) latestRunForApp(spec AppSpec, r *http.Request) (string, string) {
	if strings.TrimSpace(spec.PipelineID) == "" {
		return "", ""
	}
	orch := findOrchestrate()
	if orch == nil {
		return "", ""
	}
	def, ok := orch.LookupAppPipeline(spec.Owner, spec.PipelineID)
	if !ok {
		return "", ""
	}
	// AuthCurrentUser, not RequireUser: this is an enrichment, so a missing
	// session means "no run to offer", never a 401 written over the action's
	// own response.
	user := AuthCurrentUser(r)
	if user == "" {
		return "", ""
	}
	run, ok := orch.PublicLatestPipelineRun(user, def.ID)
	if !ok {
		return "", ""
	}
	b, err := json.Marshal(run)
	if err != nil {
		return run.Output, ""
	}
	return run.Output, string(b)
}

// handlePipeline dispatches the app's pipeline sub-routes to orchestrate. sub is
// the path after "pipeline/" ("stream" | "sessions" | "sessions/<id>").
//
// The definition is resolved against the app's OWNER — a shared app runs the
// owner's recipe — while orchestrate scopes the run transcripts to the calling
// user, so each user keeps their own history. Same ownership split the record
// store uses.
func (T *CustomApps) handlePipeline(w http.ResponseWriter, r *http.Request, spec AppSpec, sub string) {
	if strings.TrimSpace(spec.PipelineID) == "" {
		http.Error(w, "this app has no pipeline bound", http.StatusNotFound)
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	def, ok := orch.LookupAppPipeline(spec.Owner, spec.PipelineID)
	if !ok {
		http.Error(w, "the app's pipeline could not be resolved", http.StatusNotFound)
		return
	}
	// Where this app's runs can be watched and stopped from the global activity
	// ribbon. Without it a run that outlives its tab is listed with nowhere to
	// go — visible, which is the important half, but not reachable.
	base := T.WebPath() + "/" + spec.Slug
	appName := strings.TrimSpace(spec.Name)
	if appName == "" {
		appName = spec.Slug
	}
	orch.PublicHandlePipelineLive(w, r, def, sub, RunLiveInfo{
		App:       appName,
		URL:       base + "/?session={id}",
		CancelURL: base + "/pipeline/cancel?id={id}",
	})
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
	case sub == "inject":
		// Same landing every other chat surface has: a mid-flight message joins
		// the turn instead of replacing it.
		orch.PublicHandleInject(w, r)
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
					// Pause / Resume a self-updating app. Only one shows at a time,
					// gated on the auto_running / auto_paused fields the list sets.
					{Type: "button", Label: "Pause", Method: "POST", PostTo: "_app/schedule?slug={slug}&on=false", OnlyIf: "auto_running"},
					{Type: "button", Label: "Resume", Method: "POST", PostTo: "_app/schedule?slug={slug}&on=true", OnlyIf: "auto_paused"},
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
//
// Injected via Page.ExtraHeadHTML, which renders in <head> — BEFORE /_ui/ui.js
// (loaded at body end) has defined uiRegisterClientAction. So the registration
// is deferred to DOMContentLoaded (by when ui.js has run), guarded by an
// existence check — the same pattern as apps/techwriter/web_assets.go. Calling
// uiRegisterClientAction directly at head-parse time throws and the Share button
// silently does nothing.
const shareModalScript = `<script>
(function() {
  function register() {
    if (!window.uiRegisterClientAction) return;
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
    }).then(function(d){
      if (onOk) onOk(d || {});
      // Refresh the row as each toggle lands, not only on Done: the
      // Status column IS what these toggles write (private / shared to
      // users / public link), and a modal dismissed with Escape or the
      // X left it describing the sharing the app had on the way in.
      if (ctx.reload) ctx.reload();
    }).catch(function(e){
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
          if (!on) {
            post('_app/public?slug=' + encodeURIComponent(slug) + '&on=false', cb, function(){ linkRow.style.display = 'none'; });
            return;
          }
          // Enabling public exposes the app to anyone with the URL, running the
          // owner's credentialed data sources with no login — confirm before it
          // goes live. uiConfirm is the runtime's cross-host dialog (native
          // confirm is broken in the gohort-desktop WKWebView).
          var msg = 'Create a public link? Anyone who has the URL can open this app and run its data sources with YOUR credentials, with no login. Nothing is saved, and you can revoke the link anytime by turning this off.';
          Promise.resolve(window.uiConfirm ? window.uiConfirm(msg) : window.confirm(msg)).then(function(ok){
            if (!ok) { cb.checked = false; return; }
            post('_app/public?slug=' + encodeURIComponent(slug) + '&on=true', cb, function(d){
              if (d.url) { input.value = d.url; linkRow.style.display = 'flex'; }
              // Say up front which parts the link cannot carry. Found out the
              // hard way, the answer looks like a broken app: the panel renders
              // and then every request behind it is refused.
              if (d.note && window.uiAlert) window.uiAlert(d.note);
            });
          });
        }
      );
      body.appendChild(pub);
      body.appendChild(linkRow);
    },
    actions: [{label: 'Done', primary: true, onClick: function(api){ api.close(); if (ctx.reload) ctx.reload(); }}]
  });
    });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', register);
  } else {
    register();
  }
})();
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

// sharedAppReachableBy reports whether a request's user may open somebody
// else's shared app, by the FRAMEWORK's access model.
//
// Two grants admit it and both have to keep working. The coarse /custom grant
// has always meant every shared app, so a deployment that has been handing it
// out loses nothing. A grant to /apps/<slug> admits that app alone, which is
// what makes a custom app individually grantable from the Users picker.
//
// This is separate from — and ANDed with — the operator allowlist on the admin
// tab. They answer different questions: a grant says whether this person uses
// custom apps at all, and an allowlist narrows ONE app regardless of who has
// been granted the coarse path. Neither can be expressed with the other, which
// is why both exist; the tab's copy says so where an operator will read it.
func (T *CustomApps) sharedAppReachableBy(r *http.Request, slug string) bool {
	return UserHasAppAccess(r, T.WebPath()) || UserHasAppAccess(r, T.WebPath()+"/"+slug)
}

// ListGrantableApps surfaces every SHARED custom app as its own grantable path,
// so an admin can hand out one app rather than the whole surface.
//
// Shared only: an unshared app is its owner's alone, and offering it in the
// picker would be a grant that admits nobody to anything. Returns the full list
// unfiltered by access, like the other sources — an admin has to see paths
// nobody has been granted yet, which is the point of granting.
func (T *CustomApps) ListGrantableApps() []GrantableApp {
	var out []GrantableApp
	for slug, ownerName := range ListSharedOwners(T.DB, sharedAppsIndex) {
		s, ok := loadSpec(ownerName, slug)
		if !ok || !s.Shared {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = slug
		}
		out = append(out, GrantableApp{
			Path: T.WebPath() + "/" + slug,
			Name: name + " (custom app)",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
		} else if has, allPaused, next := appScheduleStatus(owner, s.Slug); has {
			// Self-updating app: badge its state and expose auto_running/auto_paused
			// so the Pause/Resume row actions show the right one.
			row["auto"] = "1"
			seg := "auto-updating"
			if allPaused {
				row["auto_paused"] = "1"
				seg = "auto-update paused"
			} else {
				row["auto_running"] = "1"
				if !next.IsZero() {
					seg = "auto-updating · next " + humanizeNext(next)
				}
			}
			if status == "private" {
				status = seg
			} else {
				status += " · " + seg
			}
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
		// Listing an app the reader cannot open is worse than not listing it:
		// they click it, get a 404, and read that as the app being broken.
		if !T.sharedAppReachableBy(r, slug) || !appadmin.UserMayReach(RootDB, ownerName, slug, owner) {
			continue
		}
		out = append(out, map[string]string{
			"slug": s.Slug, "name": s.Name, "desc": s.Desc,
			"status": "shared by " + ownerName,
		})
	}
	writeJSON(w, out)
}

// handleScheduleToggle pauses or resumes an app's self-updating action
// schedule(s): POST ?slug=&on=true|false. Owner-only (a schedule fires in the
// owner's sandbox, so only the owner controls it). Pausing leaves the trigger in
// place so Resume can re-arm it without re-authoring.
func (T *CustomApps) handleScheduleToggle(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if slug == "" {
		http.Error(w, "slug required", http.StatusBadRequest)
		return
	}
	if _, ok := loadSpec(user, slug); !ok {
		http.NotFound(w, r)
		return
	}
	resume := r.URL.Query().Get("on") == "true"
	changed := setAppSchedulesPaused(user, slug, !resume)
	writeJSON(w, map[string]any{"ok": true, "changed": changed})
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
// GET /apps/<slug>/data/<name>. It runs the named AppDataSource script
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

// maxDSCacheEntries bounds the output cache.
//
// The key includes the query params, and on the anonymous capability surface
// those come from whoever holds the link — so varying one param per request
// both missed the cache every time AND wrote a fresh entry, and the map was
// only ever swept opportunistically at the end of a run. Attacker-controlled
// input growing a map without a ceiling is the shape of every memory-exhaustion
// bug; the ceiling is small because the cache exists to collapse a burst of
// identical loads, not to remember much.
const maxDSCacheEntries = 512

var (
	dsCacheMu       sync.Mutex
	dsCache         = map[string]dsCacheEntry{}
	dsInFlightCalls = map[string]*dsInFlight{}
)

// trimDSCache drops expired entries and then enforces the hard ceiling.
// Caller holds dsCacheMu.
//
// Two steps because they answer different problems. The sweep is housekeeping:
// a record write changes the key, so churned entries accumulate. The ceiling is
// the safety property: the sweep only removes what has EXPIRED, so a caller
// varying a query param faster than the 8s TTL grows the map without bound
// however often the sweep runs. Dropping arbitrary live entries is fine here —
// a miss costs a recompute, which is the normal path anyway.
//
// Split out so the ceiling is testable without standing up a script runner. A
// test of the constant alone would not notice the enforcement disappearing.
func trimDSCache(now time.Time) {
	for k, e := range dsCache {
		if now.After(e.expires) {
			delete(dsCache, k)
		}
	}
	for k := range dsCache {
		if len(dsCache) <= maxDSCacheEntries {
			break
		}
		delete(dsCache, k)
	}
}

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
	trimDSCache(now)
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

// handleAction runs a named action script: POST /apps/<slug>/action/<name>.
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
	// An app bound to a pipeline also hands its actions the LAST FINISHED RUN.
	// The transcript lives in the pipeline's run store, not the app's records,
	// so without this an action cannot reach the thing the user just watched
	// happen — which makes the obvious button, "save this run to history",
	// unwritable. It was written anyway, against an invented `pipeline_output`
	// env var, and it printed valid JSON saying "run a debate first" forever.
	// Always set, empty when there is no finished run, so a script reads them
	// with a default like every other input.
	out, runJSON := T.latestRunForApp(spec, r)
	args["pipeline_output"] = out
	args["pipeline_run"] = runJSON
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

	msg, saved, err := runActionAndPersist(owner, T.recordBase(spec, owner), udb, spec, *act, args)
	if err != nil {
		Log("[customapps] action %q/%q failed: %v", spec.Slug, name, err)
		http.Error(w, "action failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"message": msg, "saved": saved})
}

// runActionAndPersist executes one action script in the owner's sandbox (ownerDB
// is the owner's record store the script reads), parses its {message?, records?}
// object, and upserts the returned records into udb keyed by RecordKey. It is the
// shared core of both a button click (handleAction) and an unattended timer fire
// (dispatchScheduledAction): the ONLY difference between the two is who builds
// args and who reads the result. The framework owns persistence — the script
// never writes the store itself.
func runActionAndPersist(owner string, ownerDB, udb Database, spec AppSpec, act AppAction, args map[string]any) (msg string, saved int, err error) {
	out, err := runAppScript(owner, ownerDB, spec.Slug, "action", act.Name, act.Language, act.Script, act.Capabilities, args)
	if err != nil {
		return "", 0, err
	}
	var result struct {
		Message string           `json:"message"`
		Records []map[string]any `json:"records"`
	}
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && json.Unmarshal([]byte(trimmed), &result) != nil {
		return "", 0, fmt.Errorf("the action script must print a JSON object {message?, records?} to stdout (got %.200s)", trimmed)
	}
	tbl := recTable(spec.Slug)
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
	msg = strings.TrimSpace(result.Message)
	if msg == "" {
		if saved > 0 {
			msg = fmt.Sprintf("Done, %d record(s) updated.", saved)
		} else {
			msg = "Done."
		}
	}
	return msg, saved, nil
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
//   • Public (anonymous): the app is published at /apps/pub/<token>/ as a
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
			// An operator may narrow who a shared app reaches. An unset
			// allowlist is every signed-in user, which is what sharing has
			// always meant — so this changes nothing until somebody sets one,
			// and adding it locks nobody out of an app they already had.
			if !appadmin.UserMayReach(RootDB, owner, slug, reqUser) {
				return AppSpec{}, "", false
			}
			return s, owner, true
		}
	}
	return AppSpec{}, "", false
}

// handleShareApp toggles authenticated (per-user-copy) sharing for an app the
// requester owns: POST /apps/_app/share?slug=…&on=true|false. Owner-gated by
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
// requester owns: POST /apps/_app/public?slug=…&on=true|false. Publishing
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
		out := map[string]any{"ok": true, "public": true, "url": T.publicURL(spec.PublicToken)}
		if note := publishLimitationNote(spec); note != "" {
			out["note"] = note
		}
		writeJSON(w, out)
		return
	}
	if spec.PublicToken != "" {
		T.DB.Unset(publicAppsIndex, spec.PublicToken) // revoke the link
	}
	spec.PublicToken = ""
	SaveAppSpec(spec)
	writeJSON(w, map[string]any{"ok": true, "public": false})
}

// Ceilings for the anonymous capability surface. A published app is meant to
// be read by people, so these are generous for that and bounded against a
// script; the script ceiling is the tighter one because a data-source run is a
// sandboxed SUBPROCESS on the owner's machine, and the query params that key
// its cache come from whoever holds the link.
const (
	publicRequestsPerMinute = 120
	publicScriptsPerMinute  = 30
)

var (
	publicAppRequests = NewRateLimiter(publicRequestsPerMinute, time.Minute)
	publicAppScripts  = NewRateLimiter(publicScriptsPerMinute, time.Minute)
)

// handlePublic serves the anonymous capability-URL surface:
// /apps/pub/<token>/… . The token (validated against the public index) is the
// sole credential — this subtree is a registered public path, so the cookie
// middleware already passed it through unauthenticated. STATELESS and
// read/compute-only: the page renders, data sources RUN in the owner's sandbox
// with query-param input, "records" is always empty (no anonymous store), and
// every write / action-fire / chat endpoint is refused.
func (T *CustomApps) handlePublic(w http.ResponseWriter, r *http.Request, rest string) {
	// Anonymous and unauthenticated, so the ceiling is the only thing between
	// a copied link and as much work as requests can be issued. Applied to the
	// whole subtree rather than just the data path: an unknown token still
	// costs an index lookup, and the page render is not free either.
	if !publicAppRequests.Allow(RequestSource(r)) {
		TooManyRequests(w, time.Minute, "too many requests — slow down")
		return
	}
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
//   - Rewrites the app's own AUTH-GATED mount prefix (/apps/<slug>/) to the
//     public capability mount (/apps/pub/<token>/). The typed sections use
//     RELATIVE sources ("data/<name>") that already resolve against the page
//     URL, but a hand-written html section commonly fetches an ABSOLUTE path
//     ("/apps/<slug>/data/<name>") — served verbatim that points back at the
//     gated slug route and 302s to login (works for the owner, breaks for an
//     anonymous visitor). The prefix rewrite makes those absolute self-refs hit
//     the token-scoped endpoint instead.
//   - Marks the page public so the runtime drops the live-sessions pill (which
//     would poll the gated /api/live), and removes the Back link (it points at
//     the owner's gated /apps/ index — meaningless to an anonymous visitor).
func (T *CustomApps) publicPageBytes(spec AppSpec, token string) []byte {
	// Both mounts, because a page STORED before the app moved has the old one
	// baked into any absolute self-reference its author wrote. Rewriting only
	// the current prefix would leave those pointing at /apps/<slug>/…, which
	// redirects to the gated mount and sends an anonymous visitor to a login
	// page — the exact failure this rewrite exists to prevent, reintroduced by
	// a rename rather than by a bad link.
	gated := [][]byte{
		[]byte(T.WebPath() + "/" + spec.Slug + "/"),
		[]byte(customAppsLegacyPath + "/" + spec.Slug + "/"),
	}
	public := []byte(T.WebPath() + "/pub/" + token + "/")
	rewrite := func(b []byte) []byte {
		for _, g := range gated {
			b = bytes.ReplaceAll(b, g, public)
		}
		return b
	}
	var page map[string]any
	if err := json.Unmarshal(spec.Page, &page); err != nil {
		// Unparseable page: still rewrite the raw bytes so data fetches resolve.
		return rewrite(spec.Page)
	}
	page["public"] = true    // runtime: suppress the live-sessions pill
	delete(page, "back_url") // no Back link to the gated dashboard
	publicizeSessionPanels(page)
	out, err := json.Marshal(page)
	if err != nil {
		out = spec.Page
	}
	return rewrite(out)
}

// sessionBoundPanels are the component types whose endpoints exist only on the
// AUTHENTICATED surface — they run a model on the owner's account and keep
// per-user state, which is exactly what a public capability URL must not hand
// to anyone holding a link.
var sessionBoundPanels = map[string]string{
	"pipeline_panel":   "multi-stage run",
	"chat_panel":       "chat",
	"agent_loop_panel": "chat",
	"workbench_panel":  "document workbench",
	"codewriter_panel": "workbench",
	"article_editor":   "editor",
}

// publishLimitationNote warns, at the moment the link is minted, which parts of
// the app the link cannot carry. Empty when everything in it works publicly.
func publishLimitationNote(spec AppSpec) string {
	var page map[string]any
	if err := json.Unmarshal(spec.Page, &page); err != nil {
		return ""
	}
	secs, _ := page["sections"].([]any)
	seen := map[string]bool{}
	var kinds []string
	for _, item := range secs {
		sec, ok := item.(map[string]any)
		if !ok {
			continue
		}
		body, ok := sec["body"].(map[string]any)
		if !ok {
			continue
		}
		if kind, ok := sessionBoundPanels[strings.TrimSpace(fmt.Sprint(body["type"]))]; ok && !seen[kind] {
			seen[kind] = true
			kinds = append(kinds, kind)
		}
	}
	if len(kinds) == 0 {
		return ""
	}
	sort.Strings(kinds)
	return "Heads up: the " + strings.Join(kinds, " and the ") + " won't work on the public link. " +
		"Those run a model on your account and keep their own history, so they need a signed-in session — " +
		"visitors will see a short note in their place. Everything else on the page (tables, charts, data sources) works normally."
}

// publicizeSessionPanels swaps any session-bound panel for an empty state that
// says why it isn't there.
//
// Published, those panels rendered in full and then 403'd on every request they
// made: the run history sat on "Loading…", the submit button did nothing, and
// the page gave no clue that the link — not the app — was the reason. The app
// works perfectly for the signed-in owner, so the report is always "it's a bit
// buggy" rather than "publishing doesn't support this", and the hunt starts in
// the wrong place.
//
// A refusal the reader can see beats a control that lies. The rest of the page
// (tables, charts, data sources) still works, so a mixed app stays publishable
// and only loses the part that could never have worked.
func publicizeSessionPanels(page map[string]any) {
	secs, ok := page["sections"].([]any)
	if !ok {
		return
	}
	for _, item := range secs {
		sec, ok := item.(map[string]any)
		if !ok {
			continue
		}
		body, ok := sec["body"].(map[string]any)
		if !ok {
			continue
		}
		kind, ok := sessionBoundPanels[strings.TrimSpace(fmt.Sprint(body["type"]))]
		if !ok {
			continue
		}
		sec["body"] = map[string]any{
			"type":  "empty_state",
			"icon":  "🔒",
			"title": "This part needs a signed-in session",
			"hint": "The " + kind + " runs on the owner's account and keeps its own history, so it is not part of a shared link. " +
				"Open the app from the dashboard to use it.",
		}
		// The panel managed its own layout; an empty state wants the ordinary
		// card, and the section's title is worth keeping now that something
		// under it needs explaining.
		delete(sec, "no_chrome")
	}
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
	// A second, tighter ceiling on the one action that spawns a process.
	// Keyed on the APP, not the caller: the cost lands on the owner's machine
	// whoever asks, and a link shared widely is exactly the case where
	// per-source limiting does nothing. The page and the empty records/actions
	// responses stay under the looser subtree limit above.
	if !publicAppScripts.Allow(spec.Owner + "/" + spec.Slug) {
		Warn("[customapps] public app %q hit its script ceiling (%d/min) — refusing further runs this minute", spec.Slug, publicScriptsPerMinute)
		TooManyRequests(w, time.Minute, "this app is being asked for data too quickly — try again shortly")
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

// handleAsset serves one of an app's static assets.
//
// Read-only by design: assets are written through the authoring tool, never
// over HTTP, so there is no upload endpoint to abuse. Resolution is scoped to
// the app's OWNER, not the requester, so a shared app serves the owner's
// artwork to everyone who can already see the app — the same trust boundary
// the page itself sits behind.
func (T *CustomApps) handleAsset(w http.ResponseWriter, r *http.Request, owner, slug, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name = strings.TrimSpace(name)
	if name == "" || !ValidAppAssetName(name) {
		http.NotFound(w, r)
		return
	}
	data, ct, err := ReadAppAsset(owner, slug, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	// Assets are replaced by name, so a long cache would pin a stale sprite.
	// Short and revalidating keeps an updated asset visible on reload.
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	// The directory only ever holds allowlisted image/font types, but a
	// browser sniffing its way to something else is exactly the hole the
	// allowlist exists to close.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
