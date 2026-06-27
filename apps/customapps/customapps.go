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
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() {
	RegisterApp(new(CustomApps))
	// Demo: prove the cross-app agent registry end to end — this app declares
	// an agent that orchestrate resolves + lists like one of its own seeds.
	// Replace/remove once the "App Agents" dashboard section lands.
	RegisterAppAgent(AppAgentSpec{
		ID:          "app-customapps-notes-helper",
		OwningApp:   "Custom Apps",
		Name:        "Notes Helper",
		Description: "Demo app agent registered by customapps — drafts and tidies notes.",
		Prompt:      "You are a concise notes assistant. Help the user capture, tidy, and summarize notes. Keep replies short and actionable.",
		Hidden:      true, // demo — kept out of the picker; proves the registry without cluttering the menu
	})
}

const specTable = "app_specs"

// AppSpec is a stored, data-driven app. Page holds the client-shaped pageConfig
// JSON (from ui.Page.ConfigJSON) and is served verbatim — no Go Component
// round-trip. RecordKey is the primary-key field of the per-app record store.
type AppSpec struct {
	Slug      string          `json:"slug"`
	Name      string          `json:"name"`
	Desc      string          `json:"desc"`
	Owner     string          `json:"owner"`
	AgentID   string          `json:"agent_id,omitempty"`
	Page      json.RawMessage `json:"page"`
	RecordKey string          `json:"record_key"`
	Created   string          `json:"created"`
	Updated   string          `json:"updated"`
}

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
	T.seedDemo(user, udb)

	path := strings.Trim(r.URL.Path, "/")
	switch path {
	case "":
		T.handleIndex(w, r)
		return
	case "_apps":
		T.handleAppsList(w, r, udb)
		return
	}

	parts := strings.SplitN(path, "/", 2)
	slug := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	spec, found := loadSpec(udb, slug)
	if !found {
		http.NotFound(w, r)
		return
	}
	switch rest {
	case "":
		// Component Source/PostURL are relative ("records"), so the page must
		// live at a trailing-slash URL or they resolve one level too high.
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, T.WebPath()+"/"+slug+"/", http.StatusFound)
			return
		}
		_ = ui.RenderPageJSON(w, spec.Page, "blackboard", "", spec.Name)
	case "records":
		T.handleRecords(w, r, udb, spec)
	case "record":
		T.handleRecord(w, r, udb, spec)
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
				},
			},
		}},
	}.ServeHTTP(w, r)
}

func (T *CustomApps) handleAppsList(w http.ResponseWriter, r *http.Request, udb Database) {
	out := []map[string]string{}
	for _, s := range listSpecs(udb) {
		out = append(out, map[string]string{"slug": s.Slug, "name": s.Name, "desc": s.Desc})
	}
	writeJSON(w, out)
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

// --- spec storage + demo seed ------------------------------------------------

func loadSpec(udb Database, slug string) (AppSpec, bool) {
	var s AppSpec
	ok := udb.Get(specTable, slug, &s)
	return s, ok
}

func saveSpec(udb Database, s AppSpec) { udb.Set(specTable, s.Slug, s) }

func listSpecs(udb Database) []AppSpec {
	var out []AppSpec
	for _, k := range udb.Keys(specTable) {
		if s, ok := loadSpec(udb, k); ok {
			out = append(out, s)
		}
	}
	return out
}

// seedDemo installs the "Notes" demo app on first access if absent. It builds
// the Page with the real Go ui types, marshals it via ConfigJSON, and STORES
// the bytes — so it's served back through the data-driven path, not rebuilt.
func (T *CustomApps) seedDemo(user string, udb Database) {
	if _, ok := loadSpec(udb, "notes"); ok {
		return
	}
	page := ui.Page{
		Title:     "Notes",
		ShowTitle: true,
		BackURL:   "/custom/",
		MaxWidth:  "900px",
		Sections: []ui.Section{
			{
				Title:    "Add a note",
				Subtitle: "Composed from a FormPanel — saves to this app's record store.",
				Body: ui.FormPanel{
					PostURL:     "records",
					SubmitLabel: "Add note",
					Fields: []ui.FormField{
						{Field: "title", Label: "Title", Type: "text", Placeholder: "Groceries"},
						{Field: "body", Label: "Body", Type: "textarea", Rows: 3},
					},
				},
			},
			{
				Title:    "Notes",
				Subtitle: "A Table over the same record store. Auto-refreshes so new notes appear.",
				Body: ui.Table{
					Source:        "records",
					RowKey:        "id",
					AutoRefreshMS: 2000,
					EmptyText:     "No notes yet — add one above.",
					Columns: []ui.Col{
						{Field: "title", Flex: 1},
						{Field: "body", Flex: 2, Mute: true},
					},
					RowActions: []ui.RowAction{
						{Type: "button", Label: "Delete", Method: "DELETE",
							PostTo: "record?id={id}", Confirm: "Delete this note?"},
					},
				},
			},
		},
	}
	blob, err := page.ConfigJSON()
	if err != nil {
		Log("[customapps] seed demo: build page failed: %v", err)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	saveSpec(udb, AppSpec{
		Slug: "notes", Name: "Notes", Desc: "A simple notepad — demo of a data-driven app.",
		Owner: user, Page: blob, RecordKey: "id", Created: now, Updated: now,
	})
	Log("[customapps] seeded demo app 'notes' for %s", user)
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
