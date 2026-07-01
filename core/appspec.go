// AppSpec is a stored, data-driven app: a Page (the client-shaped pageConfig
// JSON the runtime renders) plus a per-app record store key. It lives in core so
// BOTH the host that serves it (apps/customapps) and the authoring tool that
// writes it (the app_def Builder tool in apps/orchestrate) can reach the type +
// its storage without importing each other. The Page bytes are built once from
// ui.Page types (by whoever authors the spec) and stored verbatim — core itself
// needs no ui dependency here, only json.RawMessage.
//
// IMPORTANT — specs live in a SHARED, deployment-root store keyed by owner, NOT
// in either app's per-app bucket. Each app's AppCore.DB is global.db.Bucket(app
// name), so a spec written through orchestrate's DB would be invisible to
// customapps' DB and vice-versa. Routing all spec storage through RootDB (the
// bucket-less deployment root) under user:<owner> gives both apps one place to
// read and write, so an app authored by app_def actually shows up + serves in
// customapps.
package core

import (
	"encoding/json"
	"time"
)

// AppSpecTable is the per-user kvlite table holding AppSpecs, keyed by slug.
const AppSpecTable = "app_specs"

// AppSpec is one data-driven app. Page holds the pageConfig JSON (from
// ui.Page.ConfigJSON) served verbatim — no Go Component round-trip. RecordKey is
// the primary-key field of the per-app record store. AgentID optionally binds an
// agent that powers the app's chat surface.
type AppSpec struct {
	Slug      string          `json:"slug"`
	Name      string          `json:"name"`
	Desc      string          `json:"desc"`
	Owner     string          `json:"owner"`
	AgentID   string          `json:"agent_id,omitempty"`
	Page      json.RawMessage `json:"page"`
	RecordKey string          `json:"record_key"`
	// BodyField is the record field a workbench's viewer renders + its co-author
	// tool appends to (the document body). Empty for non-workbench apps.
	BodyField string `json:"body_field,omitempty"`
	// FullWidth renders the app's page edge-to-edge (MaxWidth 100%) instead of the
	// default centered ~900px column. The author opts in for data-heavy surfaces
	// (wide tables, dashboards). A workbench app is always full-width regardless.
	FullWidth bool `json:"full_width,omitempty"`
	// DataSources are script-backed data endpoints (see AppDataSource), referenced
	// by a table/display section's source_script. Served at /custom/<slug>/data/<name>.
	// This is the "logic" seam: structure stays declarative, computation/integration
	// is a sandboxed script.
	DataSources []AppDataSource `json:"data_sources,omitempty"`
	// Actions are script-backed buttons (see AppAction) — the write-side of the
	// logic seam. Served at /custom/<slug>/action/<name>; surfaced by an "actions"
	// section.
	Actions []AppAction `json:"actions,omitempty"`
	Created string      `json:"created"`
	Updated string      `json:"updated"`
}

// AppDataSource is a script-backed data endpoint for a custom app: a sandboxed
// script (python by default) that COMPUTES the JSON a table/display section
// renders, instead of the generic record store. It receives the app's stored
// records (JSON) plus the request's query params as environment variables, and
// must print a JSON value to stdout — an array for a table, an object for a
// display. The script may reach out via the gohort sandbox hook (capabilities
// like "fetch", "log") so it can pull + transform external data (an API,
// Confluence, …). Owner-only: custom apps are per-owner, and the script runs in
// the owner's sandbox with the owner's network gate.
type AppDataSource struct {
	Name         string   `json:"name"`                   // referenced by a section's source_script
	Language     string   `json:"language,omitempty"`     // "python" (default) | "bash"
	Script       string   `json:"script"`                 // the script body
	Capabilities []string `json:"capabilities,omitempty"` // sandbox hook caps: fetch, log, browse_page, fetch_via:<cred>
}

// AppAction is a script-backed custom-app action: a sandboxed script a button
// fires. Like AppDataSource it receives the app's stored records (env var
// `records`, JSON) + request params, but it prints a JSON OBJECT to stdout:
// {message?: string, records?: [...]}. The FRAMEWORK upserts any returned records
// into the app's store (so the result reaches the viewer — the script never
// writes the store itself, which keeps the no-workspace-divergence footgun shut)
// and shows the message. The write-side counterpart to AppDataSource.
type AppAction struct {
	Name         string   `json:"name"`                   // referenced by the button (action/<name>)
	Label        string   `json:"label,omitempty"`        // button text (default: humanized name)
	Desc         string   `json:"desc,omitempty"`         // optional sub-label
	Language     string   `json:"language,omitempty"`     // "python" (default) | "bash"
	Script       string   `json:"script"`                 // the script body
	Capabilities []string `json:"capabilities,omitempty"` // sandbox hook caps
	Confirm      string   `json:"confirm,omitempty"`      // optional confirm prompt before firing
}

// appSpecStore returns the shared per-owner spec store (RootDB → user:<owner>),
// or nil when RootDB isn't wired yet / owner is empty. Both the authoring tool
// and the host resolve the store here, so they always agree regardless of which
// app's DB bucket they happen to hold.
func appSpecStore(owner string) Database {
	if RootDB == nil || owner == "" {
		return nil
	}
	return UserDB(RootDB, owner)
}

// LoadAppSpec reads one spec by slug for an owner.
func LoadAppSpec(owner, slug string) (AppSpec, bool) {
	db := appSpecStore(owner)
	if db == nil {
		return AppSpec{}, false
	}
	var s AppSpec
	ok := db.Get(AppSpecTable, slug, &s)
	return s, ok
}

// SaveAppSpec writes a spec, stamping Owner/Created/Updated. Owner on the spec
// wins; pass it set. No-op return when RootDB isn't available.
func SaveAppSpec(s AppSpec) AppSpec {
	db := appSpecStore(s.Owner)
	if db == nil {
		return s
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if s.Created == "" {
		s.Created = now
	}
	s.Updated = now
	db.Set(AppSpecTable, s.Slug, s)
	return s
}

// ListAppSpecs returns every stored spec owned by the user.
func ListAppSpecs(owner string) []AppSpec {
	db := appSpecStore(owner)
	if db == nil {
		return nil
	}
	var out []AppSpec
	for _, k := range db.Keys(AppSpecTable) {
		var s AppSpec
		if db.Get(AppSpecTable, k, &s) {
			out = append(out, s)
		}
	}
	return out
}

// DeleteAppSpec removes a spec by slug for an owner.
func DeleteAppSpec(owner, slug string) {
	if db := appSpecStore(owner); db != nil {
		db.Unset(AppSpecTable, slug)
	}
}
