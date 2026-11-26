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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appadmin"
	"github.com/cmcoffee/gohort/core/appassets"
	"github.com/cmcoffee/gohort/core/appcheck"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/appsource"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/gohort/core/shareledger"
	"github.com/cmcoffee/gohort/core/ui"

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

func (T CustomApps) Name() string         { return appscript.StoreName }
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
func (T *CustomApps) WebName() string { return "My Apps" }
func (T *CustomApps) WebDesc() string { return "Apps composed from primitives." }

func (T *CustomApps) Routes() {
	hostApp = T
	// What each app offers beyond its page, as tools for its owner's agents
	// and MCP clients (exposed_tools.go).
	AppToolsFor = T.exposedTools
	// An app's own rows travel with it on request (records_artifact.go).
	T.registerRecordsArtifact()
	// Which of an owner's credentials their shared apps lend whom, so a
	// person can connect their own key on one (app_credentials.go).
	Secure().SetAppLends(T.appLends)
	T.registerPipelineRestore()
	T.routes()
	// An app's running work (its actions, its asks) in the live pill, with
	// Stop: the one place work in progress is seen and stopped.
	liveAppWorkOnce.Do(func() { RegisterLiveProvider(T.liveAppWork) })
	// Wire self-updating apps: register the scheduled-action trigger dispatcher and
	// the spec-lifecycle hooks that keep each app's standing triggers in sync.
	T.registerScheduling()
	// An account's apps when the account is deleted: moved or deleted, never
	// left behind (userdata.go).
	RegisterUserDataHandler(&appsUserData{app: T})
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

	// The operator's rows in admin's list of apps, contributed as a SOURCE
	// because these are records people write while the server runs.
	T.registerAdminControls()
	RegisterAdminAppSource(T.adminApps)
	// Approving an "app" publish request is the same act as the owner's
	// Share toggle would have been: share it to every signed-in user.
	promotion.RegisterApprover("app", T.approvePublish)
	T.registerPublished()
	// Anonymous links are gone. Stopping them being served is the code above;
	// this withdraws the ones already minted and tells their owners which app
	// each belonged to. See public_link_retired.go.
	T.retirePublicLinks()
}

// routes registers the HTTP routes, apart from the registries Routes fills,
// so a test can serve the real routes.
func (T *CustomApps) routes() {
	// The person's controls over their own apps, and the operator's over
	// anybody's. These paths come before any app's, as they always have: an
	// app slugged _app or _admin would be shadowed by them.
	T.HandleFunc("/_apps", jsonapi.Get(T.DB, T.handleAppsList))
	T.HandleFunc("/_app", jsonapi.Methods{"DELETE": jsonapi.Delete(T.DB, T.handleDeleteApp)}.Serve)
	T.HandleFunc("/_app/enable", jsonapi.Post(T.DB, T.handleEnableApp))
	T.HandleFunc("/_app/share", jsonapi.Post(T.DB, T.handleShareApp))
	T.HandleFunc("/_app/share_group", jsonapi.Post(T.DB, T.handleShareGroup))
	T.HandleFunc("/_app/share_person", jsonapi.Post(T.DB, T.handleSharePerson))
	T.HandleFunc("/_app/share_maintainer", jsonapi.Post(T.DB, T.handleShareMaintainer))
	T.HandleFunc("/_app/share_manager", jsonapi.Post(T.DB, T.handleShareManager))
	T.HandleFunc("/_app/order", jsonapi.Post(T.DB, T.handleAppOrder, jsonapi.MaxBody(1<<16)))
	T.HandleFunc("/_app/schedule", jsonapi.Post(T.DB, T.handleScheduleToggle))
	T.HandleFunc("/_app/job-cancel", jsonapi.Post(T.DB, handleJobCancel))
	// Operator controls on somebody else's app (admin.go). Named one by one:
	// "/_admin/{sub}" would overlap "/{slug}/records" at /_admin/records.
	T.HandleFunc("/_admin/scripts", T.handleAdminScripts)
	for _, sub := range []string{"reach", "tiers", "review", "revoke-link", "switch"} {
		T.HandleFunc("/_admin/"+sub, T.adminRoute(sub))
	}
	// One app's JSON routes (app_routes.go), then everything else of it.
	T.appRoutes()
	// An app's scripts and stylesheets, which its sandboxed frame loads with
	// no session, are answered in route before the sign-in check: the path
	// checks its own version (asset_files.go). A pattern of their own would
	// overlap an app's /{slug}/<route> patterns, which the router refuses.
	RegisterPublicPath(T.WebPath() + appFilesPath)
	T.HandleFunc("/", T.route)
}

// route parses "/<slug>/<rest>" off the (prefix-stripped) sub-mux and
// dispatches.
func (T *CustomApps) route(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(r.URL.Path, "/")
	if strings.HasPrefix(path, strings.Trim(appFilesPath, "/")+"/") {
		T.handleAppFiles(w, r)
		return
	}

	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}

	if path == "" {
		T.handleIndex(w, r)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	slug := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	// Own-first resolution: the requester's own app shadows any shared one of the
	// same slug; otherwise an app another user shared with them (openApp).
	// ownerUser is the app's owner (== user for owned apps) — the identity its
	// sandboxed scripts run as.
	// A preview (preview.go): starting it, leaving it, and the working copy
	// in place of the installed app for the person it is theirs.
	switch rest {
	case "_preview":
		T.handlePreviewStart(w, r, user, slug)
		return
	case "_preview/stop":
		T.handlePreviewStop(w, r, slug)
		return
	}
	// The owner reaches its Settings page while the app is switched off: an
	// imported app's connections are answered there before it may run
	// (app_connections.go).
	if rest == "_settings" {
		if own, ok := loadSpec(user, slug); ok && own.Disabled {
			T.handleSettingsPage(w, r, own, user, user)
			return
		}
	}
	pv, inPreview := previewOf(r, user, slug)
	var spec AppSpec
	var ownerUser string
	var err error
	if inPreview {
		spec, ownerUser = pv.Spec, pv.Spec.Owner
	} else if spec, ownerUser, err = T.openApp(r, user, slug); err != nil {
		jsonapi.Fail(w, r, err)
		return
	}
	// appdb is the app's record store for THIS user: a dedicated per-app file when
	// the spec opts into a private DB, else today's shared customapps sub-store
	// (identical to udb). Everything below that reads/writes records, the active
	// marker, or co-authored content goes through appdb so a private app's data
	// stays entirely in its own file.
	appdb := T.recordBase(spec, user)
	// A preview on copied records is held as a page check is: its scratch
	// store, and only the routes a check may use.
	if inPreview && pv.Scratch != "" {
		sc, ok := appscript.CheckScratchFor(pv.Scratch)
		if !ok {
			jsonapi.Fail(w, r, errCheckOver)
			return
		}
		if !checkRouteAllowed(rest, r.Method) {
			jsonapi.Fail(w, r, errNotInPreview)
			return
		}
		appdb = sc.DB
		r = withCheckScratch(r, sc)
	} else if sc, isCheck, err := appCheckScratch(r, spec, ownerUser); err != nil {
		jsonapi.Fail(w, r, err)
		return
	} else if isCheck {
		if !checkRouteAllowed(rest, r.Method) {
			jsonapi.Fail(w, r, errNotInCheck)
			return
		}
		appdb = sc.DB
		r = withCheckScratch(r, sc)
	}
	switch {
	// Static assets — read-only, owner-scoped, extension-allowlisted. Served
	// before anything else so an app can reference its own artwork without the
	// asset name having to dodge every other route below.
	case rest == "assets" || strings.HasPrefix(rest, "assets/"):
		name := strings.TrimPrefix(strings.TrimPrefix(rest, "assets"), "/")
		if r.Method == http.MethodPut || r.Method == http.MethodPost || r.Method == http.MethodDelete {
			T.handleAssetWrite(w, r, user, ownerUser, slug, name)
			return
		}
		if name == "" {
			T.handleAssetList(w, user, ownerUser, slug)
			return
		}
		T.handleAsset(w, r, ownerUser, slug, name)
		return
	case rest == "file":
		T.handleRecordFile(w, r, appdb, spec, ownerUser, user)
		return
	case strings.HasPrefix(rest, "shared/"):
		T.handleShared(w, r, T.scriptBase(r, spec, ownerUser), spec, strings.TrimPrefix(rest, "shared/"))
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
		page := isolateAppHTML(appPageFor(spec, user), T.WebPath()+"/"+slug+"/")
		// Its own scripts and stylesheets load as files the frame can reach
		// without a session, kept by the browser (asset_files.go).
		page = T.linkAppFiles(page, ownerUser, slug)
		// Every open is a fresh run: each frame's localStorage lasts this
		// visit only, and what the app keeps between visits it keeps with
		// app.state (view_state.go).
		page = withFreshStorage(page)
		// The app's ⚙: what is the person's own about it (their settings,
		// what it remembers about them, what it saved for them with
		// app.state) and, for its owner and maintainers
		// (docs/design/app-maintainers.md), the way to change it with
		// Builder and, for its owner, its recent runs (settings.go,
		// app_memory.go). Warnings stay in the header: one behind a gear is
		// one nobody sees.
		if hasSettingsPage(spec, user, ownerUser) || len(loadViewState(appdb, spec.Slug)) > 0 {
			page = withNavIcon(page, "settings", "Settings...", T.WebPath()+"/"+slug+"/_settings")
		}
		// Everyone else it is shared with reports a problem instead
		// (report.go).
		head := recordsInvalidationBridge(spec)
		// Opened from a notice about work that ended, the page says how it
		// went, whatever the app's own page remembers (action_jobs.go).
		head += workResultJS(T.WebPath() + "/" + slug + "/")
		// Someone using another person's app hears when its data waited on
		// their own account (app_credentials.go).
		if user != ownerUser {
			head += accountNeedsJS(T.WebPath() + "/" + slug + "/")
		}
		if canReport(spec, ownerUser, user) {
			page = withReportLink(page)
			head += reportDialogJS
		}
		if inPreview {
			label, back := T.previewBanner(pv)
			page = withNavLink(page, label, back)
		}
		if user == ownerUser {
			// The agent it asks was deleted: say so where the owner looks,
			// with the way to fix it, rather than let its chat and asks fail.
			if !T.appAgentExists(spec) {
				page = withNavLink(page, "⚠ Its agent was deleted: pick another", T.WebPath()+"/"+slug+"/_settings")
			}
			page = withFailedRunLink(page, T.WebPath()+"/"+slug, appscript.RecentRuns(ownerUser, slug))
			page = withOpenReportsLink(page, T.WebPath()+"/"+slug, T.loadReports(ownerUser, slug))
		}
		_ = ui.RenderPageJSON(w, page, "", head, spec.Name) // "" → resolved theme (see RegisterThemeResolver)
	case rest == "_view/reset":
		T.handleViewReset(w, r, appdb, spec)
	case rest == "_settings":
		// The app's Settings page — a form over the tunables it declares.
		T.handleSettingsPage(w, r, spec, user, ownerUser)
	case rest == "_runs":
		// What its scripts did when they ran (runs.go). The owner's only.
		T.handleRunsPage(w, r, spec, user == ownerUser)
	case rest == "_reports":
		// Problems people it is shared with reported (report.go). The
		// owner's only.
		T.handleReportsPage(w, r, spec, user == ownerUser)
	case strings.HasPrefix(rest, "data/"):
		T.handleData(w, r, ownerUser, user, appdb, spec, strings.TrimPrefix(rest, "data/"))
	case strings.HasPrefix(rest, "action/"):
		T.handleAction(w, r, ownerUser, user, appdb, spec, strings.TrimPrefix(rest, "action/"))
	case rest == "ask":
		T.handleAsk(w, r, T.recordBase(spec, ownerUser), ownerUser, user, spec)
	case rest == "problem":
		// An error the page hit in the viewer's browser (problems.go).
		T.handlePageProblem(w, r, ownerUser, user, spec)
	case rest == "propose":
		T.handlePropose(w, r, T.recordBase(spec, ownerUser), ownerUser, user, spec)
	case rest == "chat" || strings.HasPrefix(rest, "chat/"):
		// The app's chat surface: a chat section's AgentLoopPanel points at
		// chat/* and these dispatch into orchestrate's PublicHandle* methods,
		// bound to the app's agent. Reuses ALL the chat/session/runner plumbing
		// — customapps stores no chat state of its own.
		T.handleChat(w, r, ownerUser, user, appdb, spec, strings.TrimPrefix(strings.TrimPrefix(rest, "chat"), "/"))
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
	orch := agenthost.Get()
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
	orch := agenthost.Get()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	def, ok := orch.LookupAppPipeline(spec.Owner, spec.PipelineID)
	if !ok {
		http.Error(w, "the app's pipeline could not be resolved", http.StatusNotFound)
		return
	}
	orch.PublicHandleAppPipeline(w, r, def, sub, T.appPipelineLive(spec), appRunKey(spec))
}

// appPipelineLive is where an app's runs can be watched and stopped from the
// global activity ribbon, and what a completion notice links to. Without it a
// run that outlives its tab is listed with nowhere to go.
func (T *CustomApps) appPipelineLive(spec AppSpec) RunLiveInfo {
	base := T.WebPath() + "/" + spec.Slug
	appName := strings.TrimSpace(spec.Name)
	if appName == "" {
		appName = spec.Slug
	}
	return RunLiveInfo{
		App:       appName,
		URL:       base + "/?session={id}",
		CancelURL: base + "/pipeline/cancel?id={id}",
	}
}

// appRunKey is how a run finds its app again after a restart: the app's owner
// and slug, which is what loadSpec takes.
func appRunKey(spec AppSpec) string { return spec.Owner + "/" + spec.Slug }

// registerPipelineRestore lets an app's pipeline runs resume after a restart.
// orchestrate is looked up when a run is restored, not now, so the order apps
// start in does not matter.
func (T *CustomApps) registerPipelineRestore() {
	T.RegisterRunRestore(agenthost.AppPipelineRunKind, func(user, _, key string) (RunSurface, bool) {
		owner, slug, ok := strings.Cut(key, "/")
		if !ok {
			return RunSurface{}, false
		}
		spec, found := loadSpec(owner, slug)
		orch := agenthost.Get()
		if !found || orch == nil || strings.TrimSpace(spec.PipelineID) == "" {
			return RunSurface{}, false
		}
		def, ok := orch.LookupAppPipeline(spec.Owner, spec.PipelineID)
		if !ok {
			return RunSurface{}, false
		}
		return orch.AppPipelineRunSurface(user, def, T.appPipelineLive(spec), key), true
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

// handleChat dispatches the app's chat sub-routes to orchestrate. sub is the
// path after "chat/" ("" | "send" | "cancel" | "active" | "sessions" |
// "sessions/<sid>"). The agent is resolved from the app's bound AgentID; session
// + memory scope come from PublicHandle* (per calling user). For a WORKBENCH app
// (spec.BodyField set) the send path injects a co-author tool so the agent can
// write a section directly into the OPEN document's record, and for an app
// with actions it hands the agent each action as a tool (agent_actions.go).
func (T *CustomApps) handleChat(w http.ResponseWriter, r *http.Request, owner, uid string, udb Database, spec AppSpec, sub string) {
	if strings.TrimSpace(spec.AgentID) == "" {
		http.Error(w, "this app has no chat agent bound", http.StatusNotFound)
		return
	}
	orch := agenthost.Get()
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
		// the open document. An app with actions or data sources → its
		// buttons, its data and its status as tools (agent_actions.go).
		var tools []AgentToolDef
		editable := workbenchEditable(spec)
		if strings.TrimSpace(spec.BodyField) != "" && !editable {
			tools = append(tools, T.coauthorTools(udb, spec)...)
		}
		if len(spec.Actions)+len(spec.DataSources) > 0 {
			out, run := T.latestRunForApp(spec, r)
			// Not what the app keeps for managers, while answering a user.
			tools = append(tools, T.agentToolsFor(owner, uid, udb, forUser(spec, uid), out, run)...)
		}
		// Records that keep files: the agent keeps what it made on the open
		// one (record_files.go). The open one is what the send says it is,
		// else the one the workbench last said was open.
		var sentID string
		if field, _, _ := workbenchFiles(spec); field != "" {
			tools = append(tools, T.keepFileTool(udb, spec, owner, uid, func() string {
				return firstNonEmpty(sentID, activeRecordID(udb, spec.Slug))
			}))
		}
		// A co-authored document: the agent reads it and proposes, never
		// writes it directly (coauthor.go).
		if editable {
			T.handleCoauthorSend(w, r, orch, agent, tools, &sentID)
			return
		}
		if len(tools) > 0 {
			orch.PublicHandleSendWithAppTools(w, r, agent, tools)
			return
		}
		orch.PublicHandleSend(w, r, agent)
	case sub == "inject":
		// Same landing every other chat surface has: a mid-flight message joins
		// the turn instead of replacing it.
		orch.PublicHandleInject(w, r)
	case sub == "cancel":
		orch.PublicHandleCancel(w, r, agent)
	case sub == "confirm":
		// An approval card's answer: the app's agent running an action with
		// confirm asks the person first, and the card had nowhere to send it.
		orch.PublicHandleConfirm(w, r)
	case sub == "sessions":
		orch.PublicHandleSessionList(w, r, agent.ID)
	case strings.HasPrefix(sub, "sessions/"):
		orch.PublicHandleSessionOne(w, r, agent.ID, strings.TrimPrefix(sub, "sessions/"))
	default:
		http.NotFound(w, r)
	}
}

// --- index (a normal Go-built page) ------------------------------------------

var liveAppWorkOnce sync.Once

// builderChangeURL opens a fresh Builder session with its starting form
// already answered for changing one of the person's apps: Change, App, that
// app. The button says Change, so the form does not ask again; only what
// should change is left. Slug "{slug}" leaves the placeholder for a row
// action to fill. "" when no Builder is running here.
func builderChangeURL(slug string) string {
	orch := agenthost.Get()
	if orch == nil {
		return ""
	}
	return orch.WebPath() + "/?agent=seed-builder&intake.action=Change&intake.kind=App&intake.target=" + slug
}

// withNavLink adds one link to an app page's header nav.
func withNavLink(page json.RawMessage, label, link string) json.RawMessage {
	return withNav(page, map[string]any{"label": label, "url": link})
}

// withNavIcon is withNavLink for an entry drawn as an icon just right of the
// app's name, its label the tooltip (core/ui NavLink.Icon).
func withNavIcon(page json.RawMessage, icon, label, link string) json.RawMessage {
	return withNav(page, map[string]any{"label": label, "url": link, "icon": icon})
}

func withNav(page json.RawMessage, entry map[string]any) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(page, &m) != nil || m == nil {
		return page
	}
	nav, _ := m["nav"].([]any)
	m["nav"] = append(nav, entry)
	out, err := json.Marshal(m)
	if err != nil {
		return page
	}
	return out
}

func (T *CustomApps) handleIndex(w http.ResponseWriter, r *http.Request) {
	changeURL := builderChangeURL("{slug}")
	ui.Page{
		Title:     "My Apps",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "900px",
		// The Share button is a client action: it opens a modal to pick the
		// sharing modes and copy the public link. App-specific behavior, so it
		// lives here (the app's own page) via the client-action registry — never
		// in core/ui.
		// Export and Import go through the shared bundle client (core
		// ArtifactClientJS) against the person's own account endpoints.
		ExtraHeadHTML: shareModalScript + ui.NewHead().
			JS(ArtifactClientJS).
			ClientAction("export_custom_app", `function(ctx){ window.gohortArtifacts.exportAction('custom_app', 'slug', 'name')(ctx); }`).
			ClientAction("customapps_import", `function(){
  window.gohortArtifacts.importFlow({
    previewURL: '/account/api/artifacts/preview',
    importURL: '/account/api/artifacts/import',
    invalidate: ['_apps'],
    subtitle: 'An app lands switched off and shared with nobody: read its data-source and action scripts, then Enable it. Its records never travel. A slug you already have is skipped, unless the file is a newer export of the copy you took that app from: then it is an update, waiting for you to try and make live.',
    onDone: function(d) {
      (d.outcomes || []).forEach(function(o) {
        if (o.type !== 'custom_app' || o.status !== 'imported') return;
        fetch('`+T.WebPath()+`/' + encodeURIComponent(o.name) + '/_settings/connections/form', {credentials: 'same-origin'})
          .then(function(r) { return r.ok ? r.json() : null; })
          .then(function(x) {
            if (!x || !x.unanswered || !window.uiOpenModal || !window.uiMountComponent) return;
            var api = window.uiOpenModal({
              title: 'Connect ' + x.name,
              subtitle: 'It names things only a deployment has, and some are not here. Map each to one of yours, or let it go without; it stays switched off until each is answered. You can change these later under its ⚙, Connections.',
              width: '680px'
            });
            var host = document.createElement('div');
            api.body.appendChild(host);
            window.uiMountComponent(x.form, host);
          });
      });
    }
  });
}`).Render(),
		Sections: []ui.Section{{
			Title:    "Your apps",
			Subtitle: "Apps Builder made for you, and apps colleagues shared with you. Open one here; the gear on your dashboard's My apps section puts it on your front page. Drag an app by its grip to reorder; your dashboard follows the same order. Import brings in an app somebody exported; Update takes a newer export of one you imported, from the same person's copy, as a draft to try before it goes live.",
			// The row reads in two lines: what the app is called and where it
			// stands, then what it does. One line held a name, a description
			// and a state in front of six buttons, and the description was the
			// part that got cut.
			Body: ui.Stack{Children: []ui.Component{ui.Toolbar{Actions: []ui.ToolbarAction{{
				Label: "Import...", Title: "Preview an app somebody exported, then bring it in",
				Method: "client", URL: "customapps_import",
			}}}, ui.Table{
				Source:  "_apps",
				RowKey:  "slug",
				GroupBy: "group",
				// The same order as the dashboard's cards (inDashboardOrder).
				ReorderURL: "_app/order",
				Columns: []ui.Col{
					{Field: "name", Flex: 2},
					{Field: "state", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "private", Label: "Private", Color: "mute"},
						{Value: "shared", Label: "Shared", Color: "success"},
						{Value: "requested", Label: "Publish requested", Color: "warning"},
						{Value: "disabled", Label: "Disabled", Color: "danger"},
						{Value: "draft", Label: "Not live yet", Color: "warning"},
					}},
					{Field: "note", Flex: 3, Mute: true},
					{Field: "desc", Flex: 1, Mute: true, Line: 2},
				},
				EmptyText: "No apps yet. Ask Builder for one, or Import an app somebody exported.",
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Open", Method: "GET", PostTo: "{slug}/", HideIf: "not_open"},
					// Builder's work in progress (drafts.go): see it, make it
					// live yourself, or drop it.
					{Type: "button", Label: "Preview", Method: "GET", PostTo: "_app/draft/preview?slug={slug}", OnlyIf: "draft"},
					{Type: "button", Label: "Make live", Method: "POST", PostTo: "_app/draft/live?slug={slug}", OnlyIf: "draft", HideIf: "update",
						Confirm: "Make Builder's changes live now? They go through the same checks as any save, but Builder may not have finished testing them."},
					{Type: "button", Label: "Discard", Method: "POST", PostTo: "_app/draft/discard?slug={slug}", OnlyIf: "draft", HideIf: "update", Variant: "danger",
						Confirm: "Discard Builder's changes in progress? The live app stays as it is; an app never live goes with them."},
					// An update from the copy an imported app was taken from
					// (core app_origin.go), waiting as its draft.
					{Type: "button", Label: "Make live", Method: "POST", PostTo: "_app/draft/live?slug={slug}", OnlyIf: "update",
						Confirm: "Make the author's update live now? Its scripts and tools run in your sandbox once it is: read what it changes first. It goes through the same checks as any save."},
					{Type: "button", Label: "Discard", Method: "POST", PostTo: "_app/draft/discard?slug={slug}", OnlyIf: "update", Variant: "danger",
						Confirm: "Discard the update? The app stays as it is. You can import the same file again later."},
					// Only an app that declares tunables gets the button; for a
					// shared app, only when some of them are the reader's own.
					{Type: "button", Label: "Settings", Method: "GET", PostTo: "{slug}/_settings", OnlyIf: "has_settings", HideIf: "disabled"},
					{Type: "button", Label: "Connect", Method: "GET", PostTo: "{slug}/_settings", OnlyIf: "needs_answers"},
					// What its scripts did when they ran (runs.go). Yours only.
					{Type: "button", Label: "Runs", Method: "GET", PostTo: "{slug}/_runs", OnlyIf: "has_runs", HideIf: "disabled"},
					// An app that kept its place in localStorage, which now
					// opens fresh: Builder, with the change already asked.
					{Type: "button", Label: "Fix with Builder", Method: "GET", PostTo: changeURL + "&intake.change=" + url.QueryEscape(freshStartChange), OnlyIf: "fresh_start", HideIf: "disabled"},
					// Builder, pointed at this app. Yours only: Builder changes
					// what you own.
					{Type: "button", Label: "Change", Method: "GET", PostTo: changeURL, OnlyIf: "can_change", HideIf: "disabled"},
					{Type: "button", Label: "Enable", Method: "POST", PostTo: "_app/enable?slug={slug}", OnlyIf: "disabled",
						Confirm: "Enable this imported app? Review its data-source and action scripts first: they run in your sandbox once the app is live."},
					// One Share button opens the sharing modal (customapps_share).
					{Type: "button", Label: "Share", Method: "client", PostTo: "customapps_share", OnlyIf: "mine"},
					// Pause / Resume a self-updating app. Only one shows at a time,
					// gated on the auto_running / auto_paused fields the list sets.
					{Type: "button", Label: "Pause", Method: "POST", PostTo: "_app/schedule?slug={slug}&on=false", OnlyIf: "auto_running"},
					{Type: "button", Label: "Resume", Method: "POST", PostTo: "_app/schedule?slug={slug}&on=true", OnlyIf: "auto_paused"},
					{Type: "button", Label: "Export", Method: "client", PostTo: "export_custom_app", OnlyIf: "mine"},
					// An app imported from a file that said where it came from
					// takes that copy's later exports as updates.
					{Type: "button", Label: "Update...", Method: "client", PostTo: "customapps_import", OnlyIf: "from_import", HideIf: "draft"},
					{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "_app?slug={slug}", OnlyIf: "mine", Variant: "danger",
						Confirm: "Delete this app and all its data? This can't be undone.", ConfirmField: "delete_confirm"},
				},
			}}},
		}},
	}.ServeHTTP(w, r)
}

// shareModalScript registers the "customapps_share" client action: a modal with
// one list of who the app is shared with (Everyone, a team, people by name),
// each change applied at once through _app/share, _app/share_group and
// _app/share_person. Everyone from a non-admin is a request, and taking it out
// while it waits withdraws it.
//
// There is ONE sharing mode. The anonymous capability link was removed: it
// served the app to whoever held a URL, with no account, running the owner's
// data sources under the owner's credentials, which is the one path on which a
// run could not be attributed to anybody. Sharing keeps its per-user copies, so
// every opener is a person the deployment knows.
//
// No backticks in this string — it is embedded in a Go raw literal, and a
// backtick would terminate it.
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
  var direct = truthy(rec.direct);
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
      // Status: what the owner cannot see from the toggles, a request
      // pending or denied, the audience an administrator set, a disable
      // that was not theirs. Server-worded, one line each; absent when
      // there is nothing to say.
      var lines = (rec.status_lines || '').split('\n').filter(function(l){ return l; });
      if (lines.length) {
        var status = document.createElement('div');
        status.style.cssText = 'margin:0 0 0.9rem;padding:0.55rem 0.7rem;border:1px solid var(--border);border-radius:6px;background:var(--bg-2);font-size:var(--fs-sm, 0.8rem);line-height:1.5;color:var(--text-mute)';
        lines.forEach(function(l){ var d = document.createElement('div'); d.textContent = l; status.appendChild(d); });
        body.appendChild(status);
      }
      // Who it is shared with, in one list: Everyone, a team, or people by
      // name. Everyone is the one an administrator approves (unless this is
      // an administrator), so until then its chip says it is waiting.
      var groups = [], people = {with: [], others: []};
      try { groups = JSON.parse(rec.share_groups || '[]') || []; } catch (e) { groups = []; }
      try { people = JSON.parse(rec.share_people || '{}') || people; } catch (e) {}
      people.with = people.with || []; people.others = people.others || [];
      var everyone = truthy(rec.shared) ? 'on' : (truthy(rec.requested) ? 'requested' : '');
      var note = document.createElement('div');
      note.style.cssText = 'font-size:var(--fs-xs, 0.78rem);color:var(--text-mute);margin:0 0 0.5rem;line-height:1.4';
      note.textContent = 'Each of them gets their own copy, and your data-source and action scripts run with your credentials for them.'
        + (direct ? '' : ' Everyone needs an administrator to approve it; a team or people by name are yours to add.');
      // The file stores they reach through it: an administrator's grant to
      // the app covers whoever uses it. Server-worded, one line each.
      var reach = (rec.store_reach || '').split('\n').filter(function(l){ return l; });
      var rb = null;
      if (reach.length) {
        rb = document.createElement('div');
        rb.style.cssText = 'margin:0 0 0.6rem;padding:0.5rem 0.7rem;border:1px solid var(--border);border-radius:6px;font-size:var(--fs-xs, 0.78rem);line-height:1.5;color:var(--text-mute)';
        var rh = document.createElement('div');
        rh.textContent = 'Through this app they also reach these file stores, as an administrator granted it:';
        rb.appendChild(rh);
        reach.forEach(function(l){ var d = document.createElement('div'); d.textContent = '- ' + l; rb.appendChild(d); });
      }
      var chips = document.createElement('div');
      chips.style.cssText = 'display:flex;flex-wrap:wrap;gap:0.35rem;margin:0.3rem 0';
      var addRow = document.createElement('div');
      addRow.style.cssText = 'display:flex;gap:0.4rem;align-items:center;margin-top:0.35rem';
      var pick = document.createElement('select');
      pick.className = 'ui-input';
      pick.style.cssText = 'max-width:18rem';
      var addBtn = document.createElement('button');
      addBtn.type = 'button'; addBtn.className = 'ui-row-btn'; addBtn.textContent = 'Add';
      addRow.appendChild(pick); addRow.appendChild(addBtn);
      function teamLabel(g) { return 'Team: ' + g.name + ' (' + g.members + (g.members === 1 ? ' person)' : ' people)'); }
      function chip(label, title, onRemove) {
        var c = document.createElement('span');
        c.className = 'ui-chip';
        c.style.cssText = 'display:inline-flex;align-items:center;gap:0.25rem';
        c.appendChild(document.createTextNode(label));
        var x = document.createElement('button');
        x.type = 'button'; x.className = 'ui-row-btn'; x.textContent = '\u00d7'; x.title = title;
        x.addEventListener('click', onRemove);
        c.appendChild(x);
        chips.appendChild(c);
      }
      function option(value, label) { var o = document.createElement('option'); o.value = value; o.textContent = label; pick.appendChild(o); }
      function draw() {
        chips.innerHTML = '';
        if (everyone) {
          chip(everyone === 'on' ? 'Everyone' : 'Everyone (waiting for an administrator)',
            everyone === 'on' ? 'Stop sharing with everyone' : 'Withdraw the request', function() {
              post('_app/share?slug=' + encodeURIComponent(slug) + '&on=false', null, function() { everyone = ''; draw(); });
            });
        }
        groups.forEach(function(g) {
          if (!g.on) return;
          chip(teamLabel(g), 'Stop sharing with ' + g.name, function() {
            post('_app/share_group?slug=' + encodeURIComponent(slug) + '&group=' + encodeURIComponent(g.id) + '&on=false', null, function() { g.on = false; draw(); });
          });
        });
        people.with.forEach(function(u) {
          chip(u, 'Stop sharing with ' + u, function() {
            post('_app/share_person?slug=' + encodeURIComponent(slug) + '&user=' + encodeURIComponent(u) + '&on=false', null, function() {
              people.with = people.with.filter(function(x) { return x !== u; });
              people.others.push(u); people.others.sort();
              // Unsharing a person drops their roles too.
              roles.forEach(function(r) { r.drop(u); });
              draw();
            });
          });
        });
        if (!chips.children.length) {
          var none = document.createElement('span');
          none.style.cssText = 'font-size:var(--fs-xs, 0.78rem);color:var(--text-mute)';
          none.textContent = 'Only you.';
          chips.appendChild(none);
        }
        pick.innerHTML = '';
        if (!everyone) option('*', direct ? 'Everyone' : 'Everyone (an administrator approves)');
        groups.forEach(function(g) { if (!g.on) option('group:' + g.id, teamLabel(g)); });
        people.others.forEach(function(u) { option('user:' + u, u); });
        addRow.style.display = pick.options.length ? 'flex' : 'none';
      }
      addBtn.addEventListener('click', function() {
        var v = pick.value;
        if (!v) return;
        if (v === '*') {
          post('_app/share?slug=' + encodeURIComponent(slug) + '&on=true', null, function(d) {
            // A non-admin's Everyone is a REQUEST: not shared yet.
            everyone = d.requested ? 'requested' : (d.shared ? 'on' : '');
            draw();
          });
        } else if (v.indexOf('group:') === 0) {
          var id = v.slice(6);
          post('_app/share_group?slug=' + encodeURIComponent(slug) + '&group=' + encodeURIComponent(id) + '&on=true', null, function() {
            groups.forEach(function(g) { if (g.id === id) g.on = true; });
            draw();
          });
        } else {
          var u = v.slice(5);
          post('_app/share_person?slug=' + encodeURIComponent(slug) + '&user=' + encodeURIComponent(u) + '&on=true', null, function() {
            people.others = people.others.filter(function(x) { return x !== u; });
            people.with.push(u); people.with.sort();
            draw();
          });
        }
      });
      var head = document.createElement('div');
      head.style.cssText = 'font-weight:600;margin-bottom:0.25rem';
      head.textContent = 'Shared with';
      body.appendChild(head);
      body.appendChild(note);
      if (rb) body.appendChild(rb);
      body.appendChild(chips);
      body.appendChild(addRow);
      // People with a role on the app, each a list the owner adds to.
      // Naming one shares the app with them; unsharing a person drops their
      // roles (above).
      function roleSection(o) {
        var data = {with: [], others: []};
        try { data = JSON.parse(rec[o.field] || '{}') || data; } catch (e) {}
        data.with = data.with || []; data.others = data.others || [];
        var h = document.createElement('div');
        h.style.cssText = 'font-weight:600;margin:1rem 0 0.25rem';
        h.textContent = o.title;
        var n = document.createElement('div');
        n.style.cssText = note.style.cssText;
        n.textContent = o.note;
        var cs = document.createElement('div');
        cs.style.cssText = chips.style.cssText;
        var row = document.createElement('div');
        row.style.cssText = addRow.style.cssText;
        var sel = document.createElement('select');
        sel.className = 'ui-input';
        sel.style.cssText = 'max-width:18rem';
        var add = document.createElement('button');
        add.type = 'button'; add.className = 'ui-row-btn'; add.textContent = 'Add';
        row.appendChild(sel); row.appendChild(add);
        function set(u, on, then) {
          post(o.endpoint + '?slug=' + encodeURIComponent(slug) + '&user=' + encodeURIComponent(u) + '&on=' + on, null, then);
        }
        function drop(u) {
          if (data.with.indexOf(u) < 0) return;
          data.with = data.with.filter(function(y) { return y !== u; });
          data.others.push(u); data.others.sort();
          redraw();
        }
        function redraw() {
          cs.innerHTML = '';
          data.with.forEach(function(u) {
            var c = document.createElement('span');
            c.className = 'ui-chip';
            c.style.cssText = 'display:inline-flex;align-items:center;gap:0.25rem';
            c.appendChild(document.createTextNode(u));
            var x = document.createElement('button');
            x.type = 'button'; x.className = 'ui-row-btn'; x.textContent = '\u00d7'; x.title = u + o.removeTitle;
            x.addEventListener('click', function() { set(u, 'false', function() { drop(u); }); });
            c.appendChild(x);
            cs.appendChild(c);
          });
          if (!data.with.length) {
            var none = document.createElement('span');
            none.style.cssText = 'font-size:var(--fs-xs, 0.78rem);color:var(--text-mute)';
            none.textContent = 'Only you.';
            cs.appendChild(none);
          }
          sel.innerHTML = '';
          data.others.forEach(function(u) { var op = document.createElement('option'); op.value = u; op.textContent = u; sel.appendChild(op); });
          row.style.display = sel.options.length ? 'flex' : 'none';
        }
        add.addEventListener('click', function() {
          var u = sel.value;
          if (!u) return;
          set(u, 'true', function() {
            data.others = data.others.filter(function(y) { return y !== u; });
            data.with.push(u); data.with.sort();
            if (people.with.indexOf(u) < 0) {
              people.others = people.others.filter(function(y) { return y !== u; });
              people.with.push(u); people.with.sort();
              draw();
            }
            redraw();
          });
        });
        [h, n, cs, row].forEach(function(e) { body.appendChild(e); });
        return {draw: redraw, drop: drop};
      }
      var roles = [
        roleSection({title: 'Maintainers', field: 'share_maintainers', endpoint: '_app/share_maintainer',
          note: 'They can change its sections and wording through Builder. Its scripts, page code, sharing and settings stay yours, and every change they make is kept in its history under their name.',
          removeTitle: ' stops maintaining it, and can still use it'}),
        roleSection({title: 'Managers', field: 'share_managers', endpoint: '_app/share_manager',
          note: 'They run the app for its users: they get what it keeps for managers (actions and sections marked for managers), and its scripts know them as managers.',
          removeTitle: ' stops managing it, and can still use it'}),
      ];
      draw();
      roles.forEach(function(r) { r.draw(); });
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
func (T *CustomApps) handleDeleteApp(c *jsonapi.Call, _ struct{}) (any, error) {
	slug := strings.TrimSpace(c.R.URL.Query().Get("slug"))
	if slug == "" {
		return nil, jsonapi.BadRequest("slug required")
	}
	// A person deleting an app deletes what it owns (its agents, pipelines,
	// machines). Here and not in deleteApp, which a move to another account
	// also runs, and what the app owns must not go with that.
	gone, kept := DeleteAppOwned(c.User, slug)
	T.deleteApp(c.User, slug)
	return map[string]any{"ok": true, "deleted": gone, "kept": kept}, nil
}

// deleteApp removes one of owner's apps and what it leaves behind: its spec,
// the owner's records, its sharing, its operator settings and its asset files.
// The delete button and an account's purge both come here.
func (T *CustomApps) deleteApp(owner, slug string) {
	// Drop the app's data from wherever it actually lives — the per-app private
	// file for a private app, else the shared store — before removing the spec.
	// Loaded before deletion so PrivateDB is still known.
	spec, _ := loadSpec(owner, slug)
	appdb := T.recordBase(spec, owner)
	// Clear any sharing this app carried so a deleted app leaves no dangling
	// index entry. Only this owner's entry: slugs are per-owner, so another
	// user's same-named shared app must stay shared.
	T.unshareOwned(owner, slug)
	DeleteAppSpec(owner, slug)     // shared per-owner spec store (and its triggers)
	appdb.Drop(recTable(slug))     // this app's records
	appdb.Unset(activeTable, slug) // workbench open-document marker
	_ = appassets.DeleteAppRecordFiles(owner, slug)
	appadmin.Drop(RootDB, owner, slug)
	applocal.DropAppCredentials(owner, slug)
	DeleteAppAssets(owner, slug) // files on disk, which no store sweep reaches
	// Its own database file goes too, once no app uses it: a move to another
	// account runs this with the moved app already saved on the same file.
	if err := spec.RemovePrivateDB(); err != nil {
		Log("[customapps] app %q of %s: its database file was not removed: %v", slug, owner, err)
	}
}

// recordBase returns the per-user record store for one app: a dedicated private
// database file when the spec opts in (PrivateDB), else today's shared customapps
// sub-store. The two are namespace-compatible — same UserDB scoping, same table
// names — so switching an app onto its own file changes only WHERE the records
// live, not how they're keyed. A nil private handle (opener unwired) falls back
// to the shared store so nothing breaks outside a serve context.
func (T *CustomApps) recordBase(spec AppSpec, uid string) Database {
	if spec.PrivateDB {
		if db := OpenCustomAppDB(spec.PrivateDBOwner(), spec.Slug); db != nil {
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
	return T.grantedEverySharedApp(r) || UserHasAppAccess(r, T.WebPath()+"/"+slug)
}

// grantedEverySharedApp reports whether the user holds the coarse /apps grant
// itself, as an admin does and as everybody does on a deployment with no
// accounts. Not UserHasAppAccess(r, "/apps"): that admits a path when any
// grant sits UNDER it (so a gate on the first path segment lets a per-app
// grant through), and here it read a person granted one app at /apps/<slug>
// as holding /apps, which opened every shared app to them.
func (T *CustomApps) grantedEverySharedApp(r *http.Request) bool {
	if AuthDB == nil {
		return true
	}
	db := AuthDB()
	if db == nil || !AuthHasUsers(db) {
		return true
	}
	name := AuthCurrentUser(r)
	if name == "" {
		return false
	}
	u, ok := AuthGetUser(db, name)
	if !ok {
		return false
	}
	return u.Admin || slices.Contains(AuthResolveUserApps(db, u), T.WebPath())
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
			Name: name + " (My Apps)",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (T *CustomApps) handleAppsList(c *jsonapi.Call) (any, error) {
	r, owner := c.R, c.User
	out := []map[string]string{}
	seen := map[string]bool{}
	direct := requestIsAdmin(r) // shares and links directly, no request
	for _, s := range listSpecs(owner) {
		seen[s.Slug] = true
		// "mine" gates the owner-only Share/Delete actions ("can_change", the
		// owner's and a maintainer's, gates Change), and "shared" carries
		// the current state into the Share modal (a client action) so it opens
		// pre-filled.
		// group bands the list; state is the badge; note is what the badge
		// cannot say (when it next updates, what a disabled app is waiting
		// on); status is the same in one string, for the API's readers.
		row := map[string]string{"slug": s.Slug, "name": s.Name, "desc": s.Desc, "mine": "1", "can_change": "1", "group": "Your apps", "state": "private"}
		if len(visibleSettings(s, true)) > 0 {
			row["has_settings"] = "1"
		}
		// An app taken from a copy that can send it updates (app_origin.go).
		if _, ok := appsource.Load(owner, s.Slug); ok {
			row["from_import"] = "1"
		}
		// An imported app waiting for its connections to be answered.
		if needsRefusal(owner, s.Slug) != "" {
			row["needs_answers"] = "1"
		}
		// The delete confirmation says what goes with the app (its own
		// agents, pipelines, machines), with counts.
		if owned := AppOwnedCount(c.User, s.Slug); owned != "" {
			row["delete_confirm"] = "Delete this app, all its data, and what it owns (" + owned + ")? This can't be undone."
		}
		// A page with no scripts can still fail in a browser (problems.go).
		if hasScripts(s) || len(appscript.RecentRuns(s.Owner, s.Slug)) > 0 {
			row["has_runs"] = "1"
		}
		if direct {
			// The modal words its toggles as acts or as requests by this.
			row["direct"] = "1"
		}
		var parts []string
		if s.Shared {
			row["shared"], row["state"] = "1", "shared"
			parts = append(parts, "shared to users")
		} else if publishRequested(owner, "app", s.Slug) {
			row["requested"], row["state"] = "1", "requested"
			parts = append(parts, "publish requested")
		}
		// The Share dialog's group choices, and the state when a group is all
		// that shares it.
		row["share_groups"] = shareGroupsJSON(s)
		if live, _ := sharedGroupNames(s); len(live) > 0 {
			if row["state"] == "private" {
				row["state"] = "shared"
			}
			parts = append(parts, "shared with "+strings.Join(live, ", "))
		}
		// And the people it is shared with by name.
		row["share_people"] = sharePeopleJSON(s)
		row["share_maintainers"] = shareMaintainersJSON(s)
		row["share_managers"] = shareManagersJSON(s)
		if n := len(s.SharedWith); n > 0 {
			if row["state"] == "private" {
				row["state"] = "shared"
			}
			parts = append(parts, "shared with "+strings.Join(s.SharedWith, ", "))
		}
		status := "private"
		if len(parts) > 0 {
			status = strings.Join(parts, " + ")
		}
		// The Share modal's status block: where each request stands, who the
		// administrator let in, who turned it off. One line per fact.
		if lines := shareStatusLines(s); len(lines) > 0 {
			row["status_lines"] = strings.Join(lines, "\n")
		}
		// The file stores whoever it is shared with reaches through it
		// (store_reach.go).
		if lines := storeReachLines(s.Owner, s.Slug); len(lines) > 0 {
			row["store_reach"] = strings.Join(lines, "\n")
		}
		if st := appadmin.Load(RootDB, s.Owner, s.Slug); s.Disabled && st.DisabledBy != "" {
			// Off by an administrator: no Enable for the owner to press.
			row["state"] = "disabled"
			row["note"] = "switched off by an administrator"
			status = "switched off by an administrator"
		} else if s.Disabled {
			row["disabled"], row["not_open"], row["state"] = "1", "1", "disabled"
			row["note"] = "review its scripts, then Enable"
			status = "disabled: review, then Enable"
		} else if has, allPaused, next := appScheduleStatus(owner, s.Slug); has {
			// Self-updating app: say its state and expose auto_running/auto_paused
			// so the Pause/Resume row actions show the right one.
			row["auto"] = "1"
			seg := "auto-updating"
			if allPaused {
				row["auto_paused"] = "1"
				seg = "auto-update paused"
			} else {
				row["auto_running"] = "1"
				if !next.IsZero() {
					seg = "auto-updating - next " + humanizeNext(next)
				}
			}
			row["note"] = seg
			if status == "private" {
				status = seg
			} else {
				status += " - " + seg
			}
		}
		// Written before every open started fresh: it kept where you were
		// in localStorage, and opens fresh now until it is changed.
		noteFreshStart(row, s)
		row["status"] = status
		out = append(out, row)
	}
	// Apps other users shared to all authenticated users — offered here as a
	// per-user copy. Own apps shadow a same-slug shared one, so skip those.
	var shared []map[string]string
	for slug, ownerName := range ListSharedOwners(T.DB, sharedAppsIndex) {
		if ownerName == owner || seen[slug] {
			continue
		}
		s, ok := loadSpec(ownerName, slug)
		if !ok || s.Disabled {
			continue
		}
		// Listing an app the reader cannot open is worse than not listing it:
		// they click it, get a 404, and read that as the app being broken.
		if !T.sharedAppReachableBy(r, slug) || !appReaches(s, owner) {
			continue
		}
		by := "shared by " + ownerName
		if !s.Shared {
			if live, _ := sharedGroupNames(s); len(live) > 0 {
				by += " with " + strings.Join(live, ", ")
			}
		}
		row := map[string]string{
			"slug": s.Slug, "name": s.Name, "desc": s.Desc,
			"status": by,
			"group":  "Shared with you", "note": by,
		}
		// A maintainer changes it through Builder, as on the app's own page
		// (docs/design/app-maintainers.md), so the list offers Change too.
		if s.MaintainedBy(owner) {
			row["can_change"] = "1"
			row["status"], row["note"] = by+", you maintain it", by+", you maintain it"
			// And is told, as the owner is, when it now opens fresh: they
			// can make the change the owner would.
			noteFreshStart(row, s)
		}
		if len(visibleSettings(s, false)) > 0 {
			row["has_settings"] = "1"
		}
		shared = append(shared, row)
	}
	// By name first, where the person has not placed them: neither the spec
	// store's keys nor the shared index (a map) promise an order, and the
	// shared apps came back in a different one on every load.
	out = draftRows(owner, out, seen)
	sort.SliceStable(out, func(i, j int) bool { return out[i]["name"] < out[j]["name"] })
	sort.SliceStable(shared, func(i, j int) bool { return shared[i]["name"] < shared[j]["name"] })
	return append(inDashboardOrder(owner, out), inDashboardOrder(owner, shared)...), nil
}

// inDashboardOrder puts rows in the order the person keeps these apps in. There
// is one order: the dashboard's own, where each app's card is /apps/<slug>, so
// moving an app here moves its card there and the other way round.
func inDashboardOrder(user string, rows []map[string]string) []map[string]string {
	paths := make([]string, len(rows))
	bySlug := make(map[string]map[string]string, len(rows))
	for i, row := range rows {
		paths[i] = "/apps/" + row["slug"]
		bySlug[paths[i]] = row
	}
	out := make([]map[string]string, 0, len(rows))
	for _, p := range DashboardOrderOf(user, paths) {
		out = append(out, bySlug[p])
	}
	return out
}

// handleAppOrder stores the order the person dragged My Apps into: POST
// {"order": [slugs]} (Table.ReorderURL). Only slugs of apps on their list are
// kept; the order lands in their dashboard's card order (inDashboardOrder).
func (T *CustomApps) handleAppOrder(c *jsonapi.Call, body struct {
	Order []string `json:"order"`
}) (any, error) {
	user := c.User
	theirs := map[string]bool{}
	for _, s := range listSpecs(user) {
		theirs[s.Slug] = true
	}
	for slug, owner := range ListSharedOwners(T.DB, sharedAppsIndex) {
		if owner != user {
			theirs[slug] = true
		}
	}
	var paths []string
	seen := map[string]bool{}
	for _, slug := range body.Order {
		if theirs[slug] && !seen[slug] {
			seen[slug] = true
			paths = append(paths, "/apps/"+slug)
		}
	}
	SetDashboardOrderOf(user, paths)
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleScheduleToggle pauses or resumes an app's self-updating action
// schedule(s): POST ?slug=&on=true|false. Owner-only (a schedule fires in the
// owner's sandbox, so only the owner controls it). Pausing leaves the trigger in
// place so Resume can re-arm it without re-authoring.
func (T *CustomApps) handleScheduleToggle(c *jsonapi.Call, _ struct{}) (any, error) {
	user, q := c.User, c.R.URL.Query()
	slug := strings.TrimSpace(q.Get("slug"))
	if slug == "" {
		return nil, jsonapi.BadRequest("slug required")
	}
	if _, ok := loadSpec(user, slug); !ok {
		return nil, errNoApp
	}
	resume := q.Get("on") == "true"
	changed := setAppSchedulesPaused(user, slug, !resume)
	return map[string]any{"ok": true, "changed": changed}, nil
}

// handleEnableApp flips an imported (disabled) app live: POST ?slug=.... This is
// the review gate for bundle imports — a recipe can carry sandboxed
// data-source/action scripts, and none of them run until the owner has looked
// and enabled the app here. Enabling an already-live app is a no-op.
func (T *CustomApps) handleEnableApp(c *jsonapi.Call, _ struct{}) (any, error) {
	slug := strings.TrimSpace(c.R.URL.Query().Get("slug"))
	if slug == "" {
		return nil, jsonapi.BadRequest("slug required")
	}
	spec, ok := loadSpec(c.User, slug)
	if !ok {
		return nil, errNoApp
	}
	// An administrator's switch-off is theirs to undo, not the owner's.
	if st := appadmin.Load(RootDB, spec.Owner, spec.Slug); spec.Disabled && st.DisabledBy != "" {
		return nil, jsonapi.Forbidden("an administrator (" + st.DisabledBy + ") switched this app off; only an administrator can switch it back on")
	}
	// An imported app runs once what it names that only a deployment has
	// is answered (app_connections.go).
	if why := needsRefusal(spec.Owner, spec.Slug); why != "" && spec.Disabled {
		return nil, jsonapi.Forbidden(why)
	}
	if spec.Disabled {
		spec.Disabled = false
		SaveAppSpec(spec)
	}
	return map[string]bool{"ok": true}, nil
}

// errNoApp answers a request naming an app the caller does not have.
var errNoApp = jsonapi.NotFound("404 page not found")

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
func (T *CustomApps) handleData(w http.ResponseWriter, r *http.Request, owner, uid string, udb Database, spec AppSpec, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ds *AppDataSource
	if i := scriptIndex(len(spec.DataSources), func(i int) string { return spec.DataSources[i].Name }, name); i >= 0 {
		ds = &spec.DataSources[i]
	}
	if ds == nil || strings.TrimSpace(ds.Script) == "" {
		http.NotFound(w, r)
		return
	}
	if ds.ManagersOnly && spec.Role(uid) == "user" {
		http.Error(w, managersOnlyRefusal, http.StatusForbidden)
		return
	}

	// Gather the REQUESTER's stored records (udb) to hand the script as input,
	// oldest first, so records[-1] is the newest.
	recJSON, _ := json.Marshal(appscript.ReadRecords(udb, spec.Slug))

	// Args become env vars in the script: the records JSON, plus each query param.
	args := map[string]any{"records": string(recJSON)}
	for k, vs := range r.URL.Query() {
		if len(vs) > 0 {
			args[k] = vs[0]
		}
	}
	// After the params, as an action does: a page must not hand its own view
	// a shared collection of its choosing.
	if len(spec.SharedCollections) > 0 {
		args["shared"] = sharedInput(T.scriptBase(r, spec, owner), spec)
	}
	T.applySettings(args, spec, uid) // last: a param never overrides a setting

	// The script executes in the OWNER's context (sandbox identity + hook DB), so
	// a shared app's data source reaches the owner's credentials/integrations.
	// A page check's run is held to a check's rules, on its scratch, uncached.
	var out string
	var err error
	if _, isCheck := checkScratchOf(r); isCheck {
		out, err = runDataSourceIn(owner, T.scriptBase(r, spec, owner), spec, *ds, args, uid, true)
	} else {
		out, err = cachedRunDataSource(owner, T.recordBase(spec, owner), spec, *ds, args, uid)
	}
	if err != nil {
		Log("[customapps] data source %q/%q failed: %v", spec.Slug, name, err)
		http.Error(w, "data source failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// The reply, set apart from what else the script printed: the sandbox
	// hands back its error stream with its output, and a warning a newer
	// Python writes (a deprecation) made a working source read as broken.
	trimmed, besides, ok := appcheck.ScriptReply(out)
	if !ok {
		Log("[customapps] data source %q/%q returned non-JSON (first 200B): %.200s", spec.Slug, name, trimmed)
		http.Error(w, "the data source script must print a JSON value (array for a table, object for a display) to stdout", http.StatusInternalServerError)
		return
	}
	if besides != "" {
		Log("[customapps] data source %q/%q printed this besides its JSON, set aside: %.300s", spec.Slug, name, besides)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(trimmed))
}

// scriptIndex finds the data source or action a request names: the exact
// name first, then the name as it would have been saved. Names are slugged on
// save ("balance_step" is stored as "balance-step"), but a page's code keeps
// whatever its author typed, so fetch('data/balance_step') was a 404 against
// a source that existed, and the page read it as the script failing.
func scriptIndex(n int, nameAt func(int) string, name string) int {
	for i := 0; i < n; i++ {
		if nameAt(i) == name {
			return i
		}
	}
	want := scriptSlug(name)
	if want == "" || want == name {
		return -1
	}
	for i := 0; i < n; i++ {
		if nameAt(i) == want {
			return i
		}
	}
	return -1
}

// scriptSlug is the saved form of a script name: lowercase, every run of
// non-alphanumerics one hyphen, trimmed. It must agree with the slugging the
// app_def tool applies when it saves the name.
func scriptSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	return b.String()
}

// runDataSource executes one data-source script and returns its stdout.
func runDataSource(user string, db Database, spec AppSpec, ds AppDataSource, args map[string]any, caller string) (string, error) {
	return runDataSourceIn(user, db, spec, ds, args, caller, false)
}

// runDataSourceIn is runDataSource, held to a check's rules when check is set
// (a page check's request).
func runDataSourceIn(user string, db Database, spec AppSpec, ds AppDataSource, args map[string]any, caller string, check bool) (string, error) {
	// A data source runs when a page asks for it: someone is looking.
	j := appscript.Job{Owner: user, DB: db, Slug: spec.Slug, Kind: "data", Name: ds.Name, Language: ds.Language, Script: ds.Script, Caps: ds.Capabilities, Args: args, Caller: caller, Libs: spec.Libraries, Interactive: true, Check: check,
		Reads: ds.Reads, KeyField: recordKeyField(spec)}
	withAppCredentials(&j, user, spec.Slug)
	// What waited on the person's own account is said on the page, which
	// the script's own answer may not (app_credentials.go).
	j.OnConnectNeeded = func(_, name string) { noteNeedsAccount(user, spec.Slug, caller, name) }
	trace := appscript.Track(&j)
	out, err := runAppScript(j)
	// A source that ran but printed no JSON value fails its page as surely
	// as one that raised: its run says so.
	ended := err
	if ended == nil {
		if _, _, ok := appcheck.ScriptReply(out); !ok {
			ended = errNoJSONReply
		}
	}
	trace.Done(ended)
	return out, err
}

// errNoJSONReply is a data source that ran and printed no JSON value.
var errNoJSONReply = errors.New("the data source script must print a JSON value (array for a table, object for a display) to stdout, and printed none")

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
func dsCacheKey(user string, spec AppSpec, ds AppDataSource, args map[string]any) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", user, spec.Slug, ds.Name, ds.Language)
	io.WriteString(h, ds.Script)
	h.Write([]byte{0})
	// The libraries it imports are part of the script.
	libs := make([]string, 0, len(spec.Libraries))
	for n := range spec.Libraries {
		libs = append(libs, n)
	}
	sort.Strings(libs)
	for _, n := range libs {
		fmt.Fprintf(h, "%s\x00%s\x00", n, spec.Libraries[n])
	}
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
// concurrent identical failing calls still shares one execution. Nor is a run
// that printed no JSON value, which failed as surely as one that raised.
func cachedRunDataSource(user string, db Database, spec AppSpec, ds AppDataSource, args map[string]any, caller string) (string, error) {
	key := dsCacheKey(user, spec, ds, args)
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

	out, err := runDataSource(user, db, spec, ds, args, caller)

	dsCacheMu.Lock()
	call.out, call.err = out, err
	if _, _, replied := appcheck.ScriptReply(out); err == nil && replied {
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
// the app_def test action run scripts through byte-identical machinery. A
// variable so a test can stand in for the sandbox.
var runAppScript = func(j appscript.Job) (string, error) { return j.Run() }

// actionsList feeds the actions section's button list: one {name, button,
// desc, confirm} per declared action, without the managers' for an ordinary
// user (managers.go).
func (T *CustomApps) actionsList(a *appCall) (any, error) {
	type item struct {
		Name    string `json:"name"`
		Button  string `json:"button"`
		Desc    string `json:"desc,omitempty"`
		Confirm string `json:"confirm,omitempty"`
	}
	out := []item{}
	for _, act := range forUser(a.spec, a.User).Actions {
		label := strings.TrimSpace(act.Label)
		if label == "" {
			label = act.Name
		}
		out = append(out, item{Name: act.Name, Button: label, Desc: act.Desc, Confirm: act.Confirm})
	}
	return out, nil
}

// handleAction runs a named action script: POST /apps/<slug>/action/<name>.
// The app's stored records + the request's params go in; the script prints a
// JSON object {message?, records?, result?}. The FRAMEWORK upserts any returned
// records into the store (so they reach the viewer — the script never writes the
// store), and answers {message, saved, records, result?}: the records as saved
// (keys and created stamps filled in) and the script's result, for a page that
// shows what the action did (a game's turn, a generated answer). The script runs in the OWNER's sandbox
// (owner param), but any records it returns are upserted into the REQUESTER's
// store (udb) — so on a shared app a user's action runs the owner's trusted
// logic against, and saves into, that user's own copy. owner == requester for
// an owned app.
func (T *CustomApps) handleAction(w http.ResponseWriter, r *http.Request, owner, uid string, udb Database, spec AppSpec, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var act *AppAction
	if i := scriptIndex(len(spec.Actions), func(i int) string { return spec.Actions[i].Name }, name); i >= 0 {
		act = &spec.Actions[i]
	}
	if act == nil || strings.TrimSpace(act.Script) == "" {
		http.NotFound(w, r)
		return
	}
	if act.ManagersOnly && spec.Role(uid) == "user" {
		http.Error(w, managersOnlyRefusal, http.StatusForbidden)
		return
	}

	// Hand the script the app's records (oldest first) + request params
	// (query + JSON body).
	recJSON, _ := json.Marshal(appscript.ReadRecords(udb, spec.Slug))
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
	// sent is what the caller sent, apart from what the app adds: the same
	// call again while it runs joins it rather than starting a second.
	sent := map[string]string{}
	for k, vs := range r.URL.Query() {
		if len(vs) > 0 {
			args[k] = vs[0]
			sent[k] = vs[0]
		}
	}
	if r.Body != nil {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			for k, v := range body {
				args[k] = fmt.Sprint(v)
				sent[k] = fmt.Sprint(v)
			}
		}
	}
	inputs, _ := json.Marshal(sent)

	T.applySettings(args, spec, uid) // last: neither a param nor the body overrides a setting
	// A job, not a call: it runs on its own context so a slow one outlasts this
	// request, and answers here if it finishes soon enough (action_jobs.go).
	ownerDB, action := T.scriptBase(r, spec, owner), *act
	_, isCheck := checkScratchOf(r)
	what := strings.TrimSpace(act.Label)
	if what == "" {
		what = act.Name
	}
	// Started from the page, so the person hears how it went if they leave
	// before it ends; a check's run tells nobody.
	told := new(atomic.Bool)
	info := appWork{UID: uid, Slug: spec.Slug, AppName: spec.Name, What: what, Kind: "action", Owner: owner, Tell: !isCheck, Told: told}
	job, _ := startOrJoinJob(actionJobKey(owner, spec.Slug, uid, name), name, string(inputs), info, func(ctx context.Context, progress func(string)) (actionOutcome, error) {
		return runActionAndPersist(owner, ownerDB, udb, spec, action, args, uid, scriptRun{Interactive: true, Long: true, Ctx: ctx, OnProgress: progress, OnNotify: func() { told.Store(true) }, Check: isCheck})
	})
	left := false
	job.waitFor(func() {
		select {
		case <-job.done:
		case <-time.After(actionInlineWait):
		case <-r.Context().Done():
			left = true // the person left; the job carries on and keeps its outcome
		}
	})
	if left {
		return
	}
	select {
	case <-job.done:
	default:
		jsonapi.Write(w, job.status())
		return
	}
	job.mu.Lock()
	res, err := job.res, job.err
	job.mu.Unlock()
	if err != nil {
		Log("[customapps] action %q/%q failed: %v", spec.Slug, name, err)
		http.Error(w, "action failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	reply := map[string]any{"message": res.Message, "saved": res.Saved, "records": res.Records}
	if res.Result != nil {
		reply["result"] = res.Result
	}
	jsonapi.Write(w, reply)
}

// scriptRun is how an action's script runs: whether a person is waiting on
// it, and whether it runs as a long job (no fixed limit, stopped by ctx or by
// going quiet) with its progress heard by OnProgress.
type scriptRun struct {
	Interactive bool
	Long        bool
	Ctx         context.Context
	OnProgress  func(string)
	// OnNotify hears that the run sent the person a notice of its own.
	OnNotify func()
	// Check holds the run to a check's rules (a page check's request).
	Check bool
	// CallerKey is the outside client's key the run is for (WithCallerKey),
	// "" for the page.
	CallerKey string
}

// actionOutcome is what one action run did: the message for the button, how
// many records it wrote, the person's records as saved, and the script's own
// result for the page.
//
// The result is why there is more than a count. An action answered only
// {message, saved}, so a game's turn (change the state, say what happened)
// was built as a data source that changed nothing on the server, with the
// page saving the whole state afterwards, and the turn's narrative was lost on
// reload until the page was patched to copy it into the record first.
type actionOutcome struct {
	Message string
	Saved   int
	Records []map[string]any
	Result  any
}

// runActionAndPersist executes one action script in the owner's sandbox (ownerDB
// is the owner's record store the script reads), parses its {message?, records?,
// result?} object, and upserts the returned records into udb keyed by RecordKey. It is the
// shared core of both a button click (handleAction) and an unattended timer fire
// (dispatchScheduledAction): the ONLY difference between the two is who builds
// args and who reads the result. The framework owns persistence — the script
// never writes the store itself.
//
// by is who triggered the run, stamped on any shared record it writes.
func runActionAndPersist(owner string, ownerDB, udb Database, spec AppSpec, act AppAction, args map[string]any, by string, how scriptRun) (res actionOutcome, err error) {
	if len(spec.SharedCollections) > 0 {
		args["shared"] = sharedInput(ownerDB, spec)
	}
	// A credential the person's call waited on their own account for is said
	// in the outcome, which is where they read how the press went.
	var needs []string
	var needsMu sync.Mutex
	j := appscript.Job{Owner: owner, DB: ownerDB, Slug: spec.Slug, Kind: "action", Name: act.Name, Language: act.Language, Script: act.Script, Caps: act.Capabilities, Args: args, Caller: by, Libs: spec.Libraries,
		Interactive: how.Interactive, Long: how.Long, Ctx: how.Ctx, OnProgress: how.OnProgress, OnNotify: how.OnNotify, Check: how.Check,
		Reads: act.Reads, KeyField: recordKeyField(spec), CallerKey: how.CallerKey,
		OnConnectNeeded: func(_, name string) {
			needsMu.Lock()
			if !slices.Contains(needs, name) {
				needs = append(needs, name)
			}
			needsMu.Unlock()
		}}
	withAppCredentials(&j, owner, spec.Slug)
	defer func() {
		needsMu.Lock()
		defer needsMu.Unlock()
		if len(needs) > 0 {
			note := "Part of this needs your own " + strings.Join(needs, " and ") + " account: connect it under Extensions, Connected accounts, then try again."
			res.Message = strings.TrimSpace(res.Message + " " + note)
		}
	}()
	// The record takes the run's whole ending: a reply the host refuses is
	// as much the action failing as the script exiting badly.
	trace := appscript.Track(&j)
	defer func() { trace.Done(err) }()
	out, err := runAppScript(j)
	if err != nil {
		return res, err
	}
	var result struct {
		Message      string                      `json:"message"`
		Records      []map[string]any            `json:"records"`
		Shared       map[string][]map[string]any `json:"shared"`
		SharedDelete map[string][]string         `json:"shared_delete"`
		Result       any                         `json:"result"`
		Files        []actionFile                `json:"files"`
	}
	// The reply is the LAST JSON object the script printed. A long action
	// prints progress lines as it goes so a person (and the no-output limit)
	// can see it is alive, and the sandbox hands back stdout and stderr
	// together, so those lines come before the reply rather than instead of it.
	trimmed := lastJSONObject(out)
	if trimmed != "" && json.Unmarshal([]byte(trimmed), &result) != nil {
		return res, fmt.Errorf("the action script must print a JSON object {message?, records?, result?, shared?, shared_delete?, files?} to stdout (got %s)", outputGist(trimmed))
	}
	// Keys outside the reply are dropped here, and the page shows nothing
	// with no error anywhere; leave the reason in the log.
	var printed map[string]any
	if json.Unmarshal([]byte(trimmed), &printed) == nil {
		if dropped := appscript.DroppedActionKeys(printed); len(dropped) > 0 {
			Log("[customapps] action %q/%q printed %s, which the page never receives: only message, records and result reach it", spec.Slug, act.Name, strings.Join(dropped, ", "))
		}
	}
	// Shared writes first, and all or nothing: a refused one (an undeclared
	// collection, an oversized record) leaves the user's records untouched too.
	sharedN, err := applySharedWrites(owner, ownerDB, spec, by, result.Shared, result.SharedDelete)
	if err != nil {
		return res, err
	}
	saved := sharedN
	if sharedN > 0 {
		noteChange(sharedChangeKey(owner, spec.Slug))
	}
	tbl := recTable(spec.Slug)
	recordWriteMu.Lock()
	for _, rec := range result.Records {
		if rec == nil {
			continue
		}
		id := recordKeyOf(rec, spec.RecordKey)
		if id == "" {
			id = newID()
			rec[spec.RecordKey] = id
		}
		if _, ok := rec["created"]; !ok {
			rec["created"] = time.Now().UTC().Format(time.RFC3339)
		}
		// The files a record keeps are the server's list, whatever the
		// script printed for it (record_files.go).
		var stored map[string]any
		udb.Get(tbl, id, &stored)
		keepStoredFiles(spec, stored, rec)
		udb.Set(tbl, id, rec)
		saved++
		res.Records = append(res.Records, rec)
	}
	recordWriteMu.Unlock()
	if saved > sharedN {
		noteChange(recordsChangeKey(owner, spec.Slug, by))
	}
	// Files the script made, kept on the records they belong to.
	kept, fileProblems := keepActionFiles(owner, by, udb, spec, result.Files)
	if len(fileProblems) > 0 {
		Log("[customapps] action %q/%q: %s", spec.Slug, act.Name, strings.Join(fileProblems, "; "))
	}
	msg := strings.TrimSpace(result.Message)
	if msg == "" {
		switch {
		case saved > 0:
			msg = fmt.Sprintf("Done, %d record(s) updated.", saved)
		case kept > 0:
			msg = fmt.Sprintf("Done, %d file(s) kept.", kept)
		default:
			msg = "Done."
		}
	}
	if len(fileProblems) > 0 {
		msg += " " + strings.Join(fileProblems, ". ") + "."
	}
	if res.Records == nil {
		res.Records = []map[string]any{}
	}
	res.Message, res.Saved, res.Result = msg, saved, result.Result
	return res, nil
}

// lastJSONObject is the JSON object at the end of a script's output: the whole
// output when it is one, else the shortest tail of whole lines that parses as
// one (a reply printed on one line or pretty-printed over several). The output
// trimmed as it was when nothing at the end parses, so the error shows it.
// outputGist is a script's output as an error quotes it: whole when short,
// else its start and its end with "..." between. The end, because a
// traceback names what went wrong on its last line, which a quote of the
// first 200 characters cut off.
func outputGist(out string) string {
	const head, tail = 120, 480
	r := []rune(out)
	if len(r) <= head+tail {
		return out
	}
	return string(r[:head]) + "\n...\n" + string(r[len(r)-tail:])
}

func lastJSONObject(out string) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || json.Valid([]byte(trimmed)) {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "{") {
			continue
		}
		tail := strings.TrimSpace(strings.Join(lines[i:], "\n"))
		var obj map[string]any
		if json.Unmarshal([]byte(tail), &obj) == nil {
			return tail
		}
	}
	return trimmed
}

// --- generic record store ----------------------------------------------------

func recTable(slug string) string { return appscript.RecordsTable(slug) }

// listRecords is the caller's records, oldest first, as scripts get them: in
// key order (random hex) a page listing "my entries" showed them shuffled.
func (T *CustomApps) listRecords(a *appCall) (any, error) {
	// ?q=<query> or ?count=<where> asks for part (record_queries.go).
	return answerRecords(a.R, appscript.ReadRecords(a.appdb, a.spec.Slug), recordKeyField(a.spec))
}

// saveRecord stores one record, keyed on the app's RecordKey, and answers it
// as stored: a new one gets a key and a created stamp.
func (T *CustomApps) saveRecord(a *appCall, rec map[string]any) (any, error) {
	if rec == nil {
		rec = map[string]any{}
	}
	id := recordKeyOf(rec, a.spec.RecordKey)
	if id == "" {
		id = newID()
		rec[a.spec.RecordKey] = id
	}
	if _, ok := rec["created"]; !ok {
		rec["created"] = time.Now().UTC().Format(time.RFC3339)
	}
	recordWriteMu.Lock()
	defer recordWriteMu.Unlock()
	var stored map[string]any
	a.appdb.Get(recTable(a.spec.Slug), id, &stored)
	keepStoredFiles(a.spec, stored, rec)
	a.appdb.Set(recTable(a.spec.Slug), id, rec)
	noteChange(recordsChangeKey(a.owner, a.spec.Slug, a.User))
	return rec, nil
}

// recordKeyOf is the key a record is stored under: its key field's text,
// or a number written out (a script keying its rows by a timestamp), and ""
// when it has neither, for the caller to make one. The record keeps the
// number as the script wrote it; only the store's key is text. A number used
// to read as no key at all and was replaced by a random id, so a script that
// keyed by ts and looked for a numeric ts found none of its rows.
func recordKeyOf(rec map[string]any, field string) string {
	switch v := rec[field].(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return ""
		}
		return v
	case float64:
		if v == math.Trunc(v) && math.Abs(v) < 1e15 {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case int, int64:
		return fmt.Sprint(v)
	}
	return ""
}

// recordID is the record ?id= names.
func recordID(a *appCall) (string, error) {
	id := strings.TrimSpace(a.R.URL.Query().Get("id"))
	if id == "" {
		return "", jsonapi.BadRequest("id required")
	}
	return id, nil
}

func (T *CustomApps) getRecord(a *appCall) (any, error) {
	id, err := recordID(a)
	if err != nil {
		return nil, err
	}
	var rec map[string]any
	if !a.appdb.Get(recTable(a.spec.Slug), id, &rec) {
		return nil, errNoApp
	}
	return rec, nil
}

func (T *CustomApps) deleteRecord(a *appCall, _ struct{}) (any, error) {
	id, err := recordID(a)
	if err != nil {
		return nil, err
	}
	a.appdb.Unset(recTable(a.spec.Slug), id)
	// The files it kept go with it (record_files.go).
	_ = appassets.DeleteRecordFiles(a.owner, a.spec.Slug, a.User, id)
	noteChange(recordsChangeKey(a.owner, a.spec.Slug, a.User))
	return map[string]bool{"ok": true}, nil
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
)

// resolveSpec finds the app a request should serve: the requester's OWN app
// first (an owned slug shadows any shared one), else an app another user has
// shared to all authenticated users. ownerUser is the app's owner (== reqUser
// for owned apps) — the identity its sandboxed scripts run as.
func (T *CustomApps) resolveSpec(reqUser, slug string) (AppSpec, string, bool) {
	if s, ok := loadSpec(reqUser, slug); ok {
		return s, reqUser, true
	}
	if owner, ok := LookupSharedOwner(T.DB, sharedAppsIndex, slug); ok && owner != reqUser {
		// Shared with everyone signed in (which an operator may narrow), or
		// with a group reqUser is in: appReaches is the one rule.
		if s, ok := loadSpec(owner, slug); ok && appReaches(s, reqUser) {
			return s, owner, true
		}
	}
	return AppSpec{}, "", false
}

// handleShareApp toggles authenticated (per-user-copy) sharing for an app the
// requester owns: POST /apps/_app/share?slug=...&on=true|false. Owner-gated by
// construction (the spec is looked up in the requester's own store). Sharing a
// slug another user already shares is rejected — shared slugs are one global
// namespace.
func (T *CustomApps) handleShareApp(c *jsonapi.Call, body *struct {
	Note string `json:"note"`
}) (any, error) {
	r, user := c.R, c.User
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	spec, ok := loadSpec(user, slug)
	if !ok {
		return nil, errNoApp
	}
	on := r.URL.Query().Get("on") != "false" // default: turn sharing ON
	if on && !spec.Shared && !requestIsAdmin(r) {
		// Sharing runs the owner's scripts, with the owner's credentials, for
		// everyone who opens the app — so it is published the way a tool is:
		// the owner asks, an administrator approves (the Requests to share
		// queue on the Administrator page), and the approver does the share.
		// An admin owner, or a deployment with nobody to ask, shares directly.
		note := "" // optional
		if body != nil {
			note = body.Note
		}
		if err := CreatePromotionRequest(AuthDB(), user, "app", slug, note); err != nil {
			return nil, jsonapi.BadRequest(err.Error())
		}
		return map[string]any{"ok": true, "shared": false, "requested": true}, nil
	}
	if !on && promotion.WithdrawPromotionRequest(AuthDB(), user, "app", slug) {
		// Everyone taken back out while it waited: the request goes, so
		// an administrator is not left approving what nobody wants now.
		Log("[customapps] %s withdrew the publish request for %q", user, slug)
	}
	if err := T.setShared(user, slug, on); err != nil {
		return nil, jsonapi.Status(http.StatusConflict, err.Error())
	}
	return map[string]any{"ok": true, "shared": on}, nil
}

// requestIsAdmin decides who may share an app directly. A var so a test can
// stand in a non-admin without building a whole auth session.
var requestIsAdmin = RequestIsAdmin

// setShared flips per-user-copy sharing on an app the owner holds: the spec's
// flag and the global shared-slug index move together, because the index is
// what discovery reads and the flag is what the owner's list reads. Turning
// on is refused when another user already shares an app at this slug — the
// index has one owner per slug.
func (T *CustomApps) setShared(owner, slug string, on bool) error {
	spec, ok := loadSpec(owner, slug)
	if !ok {
		return Error("no app " + slug + " for " + owner)
	}
	if on {
		if other, shared := LookupSharedOwner(T.DB, sharedAppsIndex, slug); shared && other != owner {
			return Error("another user already shares an app at this slug: rename yours to share it")
		}
	}
	spec.Shared = on
	SaveAppSpec(spec)
	if on {
		SetSharedOwner(T.DB, sharedAppsIndex, slug, owner, true)
	} else if !appSharedAnyWay(spec) {
		// A group or person share still needs the index to find the owner.
		T.unshareOwned(owner, slug)
	}
	return nil
}

// unshareOwned drops slug from the shared index only when owner is the one
// holding it. The index has one owner per slug but every user has their own
// slug namespace, so unsharing (or deleting) your "tool" must not unpublish
// someone else's shared "tool".
func (T *CustomApps) unshareOwned(owner, slug string) {
	if cur, ok := LookupSharedOwner(T.DB, sharedAppsIndex, slug); ok && cur == owner {
		SetSharedOwner(T.DB, sharedAppsIndex, slug, owner, false)
	}
}

// registerPublished lists what is shared with everyone, for the
// administrator to see and take back: the owner's own Share toggle, turned
// off.
func (T *CustomApps) registerPublished() {
	shareledger.Register("app", shareledger.Provider{
		Label:     "App",
		Published: T.publishedApps,
		Unpublish: func(owner, slug string) error { return T.setShared(owner, slug, false) },
	})
}

// publishedApps is every app shared with every signed-in user. The index
// also finds apps shared only with people or teams, which are not listed.
func (T *CustomApps) publishedApps() []shareledger.Grant {
	var out []shareledger.Grant
	for slug, owner := range ListSharedOwners(T.DB, sharedAppsIndex) {
		if spec, ok := loadSpec(owner, slug); ok && spec.Shared {
			name := spec.Name
			if name == "" {
				name = slug
			}
			out = append(out, shareledger.Grant{ID: slug, Name: name, Owner: owner, Reach: "Every signed-in user"})
		}
	}
	return out
}

// approvePublish is the "app" kind's promotion approver: an administrator
// granting a publish request shares the app to every signed-in user. Same
// primitive as the owner's direct share, so a slug conflict refuses here too
// and the request stays pending for the owner to rename and re-ask.
func (T *CustomApps) approvePublish(owner, slug string) error {
	return T.setShared(owner, slug, true)
}

// publishRequested reports whether the owner has asked to share this app and
// is still waiting on an administrator. Tools keep their request in the auth
// store, so apps do too — one queue for the admin to work.
func publishRequested(owner, kind, slug string) bool {
	if AuthDB == nil {
		return false
	}
	return PendingPromotion(AuthDB(), owner, kind, slug)
}

// Ceilings for the anonymous capability surface. A published app is meant to
// be read by people, so these are generous for that and bounded against a
// script; the script ceiling is the tighter one because a data-source run is a
// sandboxed SUBPROCESS on the owner's machine, and the query params that key
// its cache come from whoever holds the link.

// sessionBoundPanels are the component types whose endpoints exist only on the
// AUTHENTICATED surface — they run a model on the owner's account and keep
// per-user state, which is exactly what a public capability URL must not hand
// to anyone holding a link.
var sessionBoundPanels = map[string]string{
	"pipeline_panel":    "multi-stage run",
	"chat_panel":        "chat",
	"agent_loop_panel":  "chat",
	"workbench_panel":   "document workbench",
	"code_editor_panel": "workbench",
}

// --- helpers -----------------------------------------------------------------

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// handleAsset serves one of an app's static assets.
//
// Resolution is scoped to the app's OWNER, not the requester, so a shared app
// serves the owner's artwork to everyone who can already see the app — the
// same trust boundary the page itself sits behind. Written by the owner only:
// app_def's add_asset, or the owner's own page (handleAssetWrite).
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
	// The directory only ever holds allowlisted types (images, fonts,
	// sounds, models, and the scripts, stylesheets, WebAssembly and JSON a
	// page loads), but a browser sniffing its way to something else is
	// exactly the hole the allowlist exists to close.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Served from this origin, so an asset opened directly is a page of its
	// own: an SVG can carry script, and run as whoever opens it. The sandbox
	// directive gives it an origin of its own and no script at all; the rest
	// lets an image, a font or a sound be nothing but that.
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; media-src 'self'")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// handleAssetList is GET assets: the app's asset names, for a page that lists
// its own artwork or sounds.
// handleAssetList lists the app's assets, and whether this viewer may write
// them, so a page can show its owner an upload control and nobody else.
func (T *CustomApps) handleAssetList(w http.ResponseWriter, user, owner, slug string) {
	names, err := ListAppAssets(owner, slug)
	if err != nil || names == nil {
		names = []string{}
	}
	jsonapi.Write(w, map[string]any{"assets": names, "can_write": user != "" && user == owner})
}

// handleAssetWrite is PUT/POST (the raw bytes, the name in the path) or
// DELETE assets/<name>, from the app's OWNER only: an asset is shown to
// everyone the app is shared with, so one viewer must not replace what all of
// them see. A viewer's own files are their records' business, not the app's.
func (T *CustomApps) handleAssetWrite(w http.ResponseWriter, r *http.Request, user, owner, slug, name string) {
	if user != owner {
		http.Error(w, "only the app's owner can change its assets", http.StatusForbidden)
		return
	}
	name = strings.TrimSpace(name)
	if !ValidAppAssetName(name) {
		http.Error(w, "invalid asset name: a flat filename ending in an image, font, sound or 3D model extension (.png .jpg .gif .webp .svg .ico .woff .woff2 .mp3 .ogg .wav .m4a .glb .gltf .bin)", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodDelete {
		if err := DeleteAppAsset(owner, slug, name); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonapi.Write(w, map[string]any{"deleted": name})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxAppAssetBytes+1))
	if err != nil || len(data) > MaxAppAssetBytes {
		http.Error(w, fmt.Sprintf("asset too large: at most %d MiB", MaxAppAssetBytes>>20), http.StatusRequestEntityTooLarge)
		return
	}
	path, err := SaveAppAsset(owner, slug, name, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonapi.Write(w, map[string]any{"saved": name, "url": path})
}

// appOwnPaths are the relative endpoints an app's own page HTML may still
// reach from inside its sandbox: its data sources and actions, its records,
// and its assets. Nothing else in gohort.
var appOwnPaths = []string{"data/", "action/", "action-progress/", "action-cancel/", "actions", "records", "record", "file", "files", "assets", "shared/", "changes", "ask", "propose", "view-state", "problem", "work"}

// navigationKeys name URLs the runtime follows as a link, never fetches.
var navigationKeys = map[string]bool{"href": true, "footer_url": true, "back_url": true, "home_url": true, "redirect_url": true}

// holdEndpointsInApp blanks any endpoint in one component's config that does
// not resolve inside the app: keys ending in _url or _source, and source,
// post_to, url and action_url unless their method makes them a link (GET,
// open, redirect) or a client action's name (client). A blank method means
// POST, as it does in the runtime.
func holdEndpointsInApp(t map[string]any, appBase string) {
	navMethod := func(k string) bool {
		m, _ := t[k].(string)
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "get", "client", "open", "redirect":
			return true
		}
		return false
	}
	for k, v := range t {
		u, ok := v.(string)
		if !ok || u == "" {
			continue
		}
		// A link is followed, never fetched, so it may lead off the app; it
		// may not be a script. The runtime checks too, and once let one
		// through: an app button's redirect_url ran script for whoever
		// pressed it, an administrator reviewing the app included.
		if navigationKeys[k] {
			if !navigableLink(u) {
				t[k] = ""
			}
			continue
		}
		endpoint := k == "source" || k == "post_to" || k == "url" || strings.HasSuffix(k, "_url") || strings.HasSuffix(k, "_source") || strings.HasSuffix(k, "_url_base")
		if !endpoint {
			continue
		}
		if (k == "url" || k == "post_to") && navMethod("method") {
			if !navigableLink(u) {
				t[k] = ""
			}
			continue
		}
		if k == "action_url" && navMethod("action_method") {
			if !navigableLink(u) {
				t[k] = ""
			}
			continue
		}
		if !appRelative(u, appBase) {
			t[k] = ""
		}
	}
}

// navigableLink reports a link that leads to a page: an http(s) address, or
// one with no scheme at all (a path, a client action's name). A javascript:,
// data: or vbscript: one does not, written however (spaces and controls a
// browser drops, any case).
func navigableLink(u string) bool {
	low := strings.ToLower(strings.Map(func(r rune) rune {
		if r <= ' ' || (r >= 0x7f && r <= 0xa0) {
			return -1
		}
		return r
	}, u))
	if m := linkSchemeRE.FindString(low); m != "" {
		return m == "http:" || m == "https:"
	}
	return true
}

// linkSchemeRE is a URL's scheme, when it starts with one.
var linkSchemeRE = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:`)

// appRelative reports whether a URL resolves inside the app's own page
// directory: relative (or an absolute path under appBase, the app's own
// address), with no scheme and no "..", backslash or encoded separator or
// dot to climb out with.
func appRelative(u, appBase string) bool {
	low := strings.ToLower(strings.TrimSpace(u))
	if appBase != "" && strings.HasPrefix(low, appBase) {
		low = strings.TrimPrefix(low, appBase)
	}
	if low == "" || strings.HasPrefix(low, "/") || strings.Contains(low, "\\") || strings.Contains(low, "..") {
		return false
	}
	if i := strings.IndexAny(low, ":/?#"); i >= 0 && low[i] == ':' {
		return false // a scheme
	}
	for _, enc := range []string{"%2f", "%5c", "%2e"} {
		if strings.Contains(low, enc) {
			return false
		}
	}
	return true
}

// isolateAppHTML marks every Card and Frame in an app's page as isolated,
// and turns off the page's other routes to raw HTML (see below). Links and
// navigation are guarded in the runtime itself (uiSafeURL), for every page.
// An app's page HTML is its author's, shown to whoever opens the app,
// administrators included; on the gohort origin its script could act as the
// viewer against every endpoint the viewer can reach. Isolated, it runs in a
// sandbox with no origin and reaches only the app's own endpoints (see the
// runtime's isolatedFrame). Rewritten at serve time, so every stored app,
// old or new, is covered.
func isolateAppHTML(page json.RawMessage, base ...string) json.RawMessage {
	appBase := ""
	if len(base) > 0 {
		appBase = strings.ToLower(base[0])
	}
	var v any
	if len(page) == 0 || json.Unmarshal(page, &v) != nil {
		return page
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			if typ, _ := t["type"].(string); typ == "card" || typ == "frame" {
				t["isolate"] = true
				t["isolate_fetch"] = appOwnPaths
				t["isolate_prelude"] = appPagePrelude
			}
			// The two other ways a page writes HTML on this origin, both on
			// the document workbench: a record body taken as HTML, and a
			// history preview endpoint whose reply is. In an app both come
			// from the app's own records and endpoints, so a body is always
			// rendered as markdown here and the HTML preview is not offered.
			if _, ok := t["body_is_html"]; ok {
				t["body_is_html"] = false
			}
			delete(t, "preview_url")
			// Every endpoint the page fetches or posts to stays inside the
			// app. The page runs as the viewer, so a button, form or table
			// naming another gohort endpoint (an approval, a credential)
			// acted there with the viewer's session on one click; an app
			// reaches gohort through its own data sources and actions.
			// An app's run panel offers its pipeline's follow-ups. Added
			// here, at serve time, so an app built before follow-ups existed
			// offers them too, without being rebuilt.
			if typ, _ := t["type"].(string); typ == "pipeline_panel" {
				if sub, _ := t["submit_url"].(string); sub == "pipeline/stream" {
					if _, has := t["followups_url"]; !has {
						t["followups_url"] = "pipeline/followups"
					}
				}
			}
			holdEndpointsInApp(t, appBase)
			for _, e := range t {
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	out, err := json.Marshal(v)
	if err != nil {
		return page
	}
	return out
}
