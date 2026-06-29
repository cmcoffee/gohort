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
//        /custom/<slug>/          → render the stored spec's Page (from JSON)
//        /custom/<slug>/records   → GET list | POST upsert  (Table / FormPanel)
//        /custom/<slug>/record    → DELETE one              (row action)
//        /custom/_apps            → JSON app list (index Table source)
//
// Every endpoint a component references resolves here, relative to the app's
// own mount — a spec cannot point a data binding outside it.
//
// Not enabled by default. Turn it on with a blank import in agents.go:
//
//	_ "github.com/cmcoffee/gohort/apps/customapps"
package customapps

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	"github.com/cmcoffee/gohort/tools/temptool"
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

func (T *CustomApps) Routes() { T.HandleFunc("/", T.route) }

// route parses "/<slug>/<rest>" off the (prefix-stripped) sub-mux and
// dispatches. "_apps" is reserved for the index data feed so it can't collide
// with a real slug.
func (T *CustomApps) route(w http.ResponseWriter, r *http.Request) {
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}

	path := strings.Trim(r.URL.Path, "/")
	switch path {
	case "":
		T.handleIndex(w, r)
		return
	case "_apps":
		T.handleAppsList(w, r, user)
		return
	case "_app":
		// DELETE ?slug=… removes a custom app (its spec + records + active state).
		T.handleDeleteApp(w, r, user, udb)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	slug := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	spec, found := loadSpec(user, slug)
	if !found {
		http.NotFound(w, r)
		return
	}
	switch {
	case rest == "":
		// Component Source/PostURL are relative ("records"), so the page must
		// live at a trailing-slash URL or they resolve one level too high.
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, T.WebPath()+"/"+slug+"/", http.StatusFound)
			return
		}
		_ = ui.RenderPageJSON(w, spec.Page, "", "", spec.Name) // "" → resolved theme (see RegisterThemeResolver)
	case strings.HasPrefix(rest, "data/"):
		T.handleData(w, r, user, udb, spec, strings.TrimPrefix(rest, "data/"))
	case rest == "actions":
		T.handleActionsList(w, r, spec)
	case strings.HasPrefix(rest, "action/"):
		T.handleAction(w, r, user, udb, spec, strings.TrimPrefix(rest, "action/"))
	case rest == "records":
		T.handleRecords(w, r, udb, spec)
	case rest == "record":
		T.handleRecord(w, r, udb, spec)
	case rest == "chat" || strings.HasPrefix(rest, "chat/"):
		// The app's chat surface: a chat section's AgentLoopPanel points at
		// chat/* and these dispatch into orchestrate's PublicHandle* methods,
		// bound to the app's agent. Reuses ALL the chat/session/runner plumbing
		// — customapps stores no chat state of its own.
		T.handleChat(w, r, udb, spec, strings.TrimPrefix(strings.TrimPrefix(rest, "chat"), "/"))
	default:
		http.NotFound(w, r)
	}
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
		Sections: []ui.Section{{
			Title:    "Your apps",
			Subtitle: "Data-driven apps composed from ui primitives.",
			Body: ui.Table{
				Source: "_apps",
				RowKey: "slug",
				Columns: []ui.Col{
					{Field: "name", Flex: 1},
					{Field: "desc", Flex: 2, Mute: true},
				},
				EmptyText: "No custom apps yet.",
				RowActions: []ui.RowAction{
					{Type: "button", Label: "Open", Method: "GET", PostTo: "{slug}/"},
					{Type: "button", Label: "Delete", Method: "DELETE", PostTo: "_app?slug={slug}",
						Confirm: "Delete this app and all its data? This can't be undone."},
				},
			},
		}},
	}.ServeHTTP(w, r)
}

// handleDeleteApp removes a custom app: its spec, its per-app record store, and
// any workbench active-selection state. The demo "notes" app re-seeds on next
// visit (by design); delete a real app and it stays gone.
func (T *CustomApps) handleDeleteApp(w http.ResponseWriter, r *http.Request, user string, udb Database) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	if slug == "" {
		http.Error(w, "slug required", http.StatusBadRequest)
		return
	}
	DeleteAppSpec(user, slug)    // shared per-owner spec store
	udb.Drop(recTable(slug))     // this app's records (customapps bucket)
	udb.Unset(activeTable, slug) // workbench open-document marker
	writeJSON(w, map[string]bool{"ok": true})
}

func (T *CustomApps) handleAppsList(w http.ResponseWriter, r *http.Request, owner string) {
	out := []map[string]string{}
	for _, s := range listSpecs(owner) {
		out = append(out, map[string]string{"slug": s.Slug, "name": s.Name, "desc": s.Desc})
	}
	writeJSON(w, out)
}

// --- script-backed data sources (the "logic" seam) ---------------------------

// handleData serves a table/display section's script-backed data endpoint:
// GET /custom/<slug>/data/<name>. It runs the named AppDataSource script
// (sandboxed) with the app's stored records + the request's query params as
// input, and passes the script's JSON stdout straight through as the response.
// Owner-only by construction — custom apps are per-owner, so only the owner ever
// reaches this; the script runs in the owner's sandbox with the owner's network
// gate. Read-only: a data source computes a view, it never writes the store
// (which keeps the framework as the sole owner of persistence — no workspace-vs-
// store divergence).
func (T *CustomApps) handleData(w http.ResponseWriter, r *http.Request, user string, udb Database, spec AppSpec, name string) {
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

	// Gather the app's stored records to hand the script as input.
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

	out, err := runDataSource(user, udb, spec.Slug, *ds, args)
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

// runAppScript executes one custom-app script (a data source or an action) as a
// sandboxed shell TempTool and returns its stdout. Reuses the whole temptool
// execution path (bwrap sandbox, the gohort hook for fetch/log, params-as-env-
// vars) — the script is just a TempTool the framework dispatches on the app
// owner's behalf. The script file is named per (kind, slug, name) so concurrent
// apps/scripts don't collide in the owner's workspace.
func runAppScript(user string, db Database, slug, kind, name, language, script string, caps []string, args map[string]any) (string, error) {
	ws, err := EnsureWorkspaceDir(user)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	interp, ext := "python3", "py"
	if l := strings.ToLower(strings.TrimSpace(language)); l == "bash" || l == "sh" {
		interp, ext = "bash", "sh"
	}
	scriptName := fmt.Sprintf("%s_%s_%s.%s", kind, sanitizeName(slug), sanitizeName(name), ext)
	if caps == nil {
		caps = []string{"fetch", "log"} // sensible default: read external data + log
	}
	params := map[string]ToolParam{}
	for k := range args {
		params[k] = ToolParam{Type: "string"}
	}
	tt := &TempTool{
		Name:             "app_" + kind + ":" + slug + ":" + name,
		Description:      "custom app " + kind,
		Mode:             "shell",
		ScriptBody:       script,
		ScriptName:       scriptName,
		CommandTemplate:  interp + " {workspace_dir}/" + scriptName,
		HookCapabilities: caps,
		Params:           params,
	}
	sess := &ToolSession{
		Username:     user,
		WorkspaceDir: ws,
		DB:           db,
		// The owner is acting in their own app — allow the hook's fetch/browse to
		// reach the network (the sandbox itself stays network-isolated).
		Network: NewNetworkConnector(false),
	}
	return temptool.DispatchTempToolDirect(sess, tt, args)
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
// and returns {message} for the button. Owner-only (per-owner specs).
func (T *CustomApps) handleAction(w http.ResponseWriter, r *http.Request, user string, udb Database, spec AppSpec, name string) {
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

	out, err := runAppScript(user, udb, spec.Slug, "action", act.Name, act.Language, act.Script, act.Capabilities, args)
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

// sanitizeName reduces a slug/name to a safe filename fragment (alnum + _).
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
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
