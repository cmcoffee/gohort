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
	"crypto/sha256"
	"encoding/hex"
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
	// PrivateDB opts this app into its OWN dedicated, hardware-locked kvlite
	// database file (via OpenCustomAppDB) instead of the shared customapps store.
	// Its records live in an isolated, independently disposable file — the right
	// choice for a data-heavy app. Opt-in per app, no migration: existing apps
	// (PrivateDB=false) keep using the shared store untouched.
	PrivateDB bool `json:"private_db,omitempty"`
	// DataSources are script-backed data endpoints (see AppDataSource), referenced
	// by a table/display section's source_script. Served at /custom/<slug>/data/<name>.
	// This is the "logic" seam: structure stays declarative, computation/integration
	// is a sandboxed script.
	DataSources []AppDataSource `json:"data_sources,omitempty"`
	// Actions are script-backed buttons (see AppAction) — the write-side of the
	// logic seam. Served at /custom/<slug>/action/<name>; surfaced by an "actions"
	// section.
	Actions []AppAction `json:"actions,omitempty"`
	// Disabled blocks the app from serving (the host 403s every sub-route) until
	// the owner enables it from the Custom Apps index. It exists as the bundle-
	// import review gate: a spec can carry sandboxed data-source/action scripts,
	// so an imported app lands disabled and nothing it brought can run before
	// the owner has looked. A local mute, not part of the app's shape — export
	// clears it.
	Disabled bool `json:"disabled,omitempty"`
	// Shared marks the app shared to every AUTHENTICATED user as a "per-user
	// copy": the definition + scripts are shared (scripts run in the OWNER's
	// sandbox), but each user gets their OWN record store — nobody sees anyone
	// else's data. A global shared-slug index in the customapps store is the
	// discovery source of truth; this bool mirrors it for the owner's list and
	// export stripping. Deployment-local — cleared on export.
	Shared bool `json:"shared,omitempty"`
	// PublicToken, when non-empty, publishes the app at /custom/pub/<token>/ as a
	// STATELESS, read/compute-only CAPABILITY URL: anyone with the (unguessable)
	// link loads the page and runs its data sources — in the OWNER's sandbox,
	// input via query params — but nothing is stored and every write endpoint is
	// refused. The token IS the access control; unpublishing clears it and
	// revokes the link. Regenerated on each publish. Deployment-local — cleared
	// on export.
	PublicToken string `json:"public_token,omitempty"`
	Created     string `json:"created"`
	Updated     string `json:"updated"`
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
	// Schedule, when set, makes this action ALSO fire unattended on a timer, with
	// no human clicking it — the seam that lets a custom app keep itself fresh
	// (dashboard / tracker) while nobody has it open. Each fire runs the script in
	// the OWNER's sandbox and upserts the records it returns, identical to a button
	// click (a tracker returns a new-id record to append a row; a dashboard returns
	// a fixed-id record to replace the snapshot). The host registers a
	// ScheduledTrigger per scheduled action on spec save. Part of the app's SHAPE
	// (it exports); the registered trigger is deployment-local and re-registered on
	// import, and only for ENABLED apps, so an imported app runs no unattended
	// script before its owner has reviewed it.
	Schedule *AppSchedule `json:"schedule,omitempty"`
}

// AppSchedule is the cadence for a self-updating AppAction. Exactly one of
// IntervalSeconds / Cron is set. It rides on the unified trigger engine
// (core/trigger.go) as target_kind "customapp_action" — not a parallel
// scheduler, so a scheduled action shows up in the same admin trigger surface as
// every other standing trigger.
type AppSchedule struct {
	IntervalSeconds int    `json:"interval_seconds,omitempty"` // fixed cadence (floored, see MinAppScheduleSeconds)
	Cron            string `json:"cron,omitempty"`             // NextCronOccurrence spec, e.g. "FRI 21:30"
	// MaxIdleDays pauses the schedule after N days with no page view, so a tracker
	// nobody looks at stops burning the sandbox. 0 = never pause. A page load
	// touches the app's last-viewed stamp and re-arms a paused schedule.
	MaxIdleDays int `json:"max_idle_days,omitempty"`
}

// MinAppScheduleSeconds floors a background action's cadence. Unattended network
// in the owner's sandbox warrants a higher floor than the trigger engine's
// foreground minimum (minTriggerInterval, 30s).
const MinAppScheduleSeconds = 300

// Scheduled reports whether this schedule actually fires (nil-safe: an action
// with no Schedule returns false).
func (s *AppSchedule) Scheduled() bool {
	return s != nil && (s.IntervalSeconds > 0 || s.Cron != "")
}

// OpenCustomAppDB returns the dedicated private database for one custom app,
// used when its AppSpec.PrivateDB is set. Keyed by slug plus a short hash of the
// owner so two owners' same-slug apps never share a file. Returns nil when the
// private-DB opener isn't wired (non-serve context) — callers fall back to the
// shared customapps store.
func OpenCustomAppDB(owner, slug string) Database {
	return OpenAppDB(customAppDBName(owner, slug))
}

// customAppDBName derives the logical (and thus file) name for a custom app's
// private DB. The slug stays readable; the owner is folded in as a short hash so
// the name is filesystem-safe and unique per owner regardless of the raw uid.
func customAppDBName(owner, slug string) string {
	sum := sha256.Sum256([]byte(owner))
	return "customapp_" + slug + "_" + hex.EncodeToString(sum[:6])
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

// appSpecSavedHooks / appSpecDeletedHooks let a host react to spec lifecycle
// without core knowing what the reaction is (customapps uses them to keep a
// scheduled action's standing trigger in sync). Domain-agnostic: core owns the
// registry, the app supplies the behavior. Registered once at startup.
var (
	appSpecSavedHooks   []func(AppSpec)
	appSpecDeletedHooks []func(owner, slug string)
)

// RegisterAppSpecSavedHook installs a callback fired after every SaveAppSpec.
func RegisterAppSpecSavedHook(fn func(AppSpec)) {
	if fn != nil {
		appSpecSavedHooks = append(appSpecSavedHooks, fn)
	}
}

// RegisterAppSpecDeletedHook installs a callback fired after every DeleteAppSpec.
func RegisterAppSpecDeletedHook(fn func(owner, slug string)) {
	if fn != nil {
		appSpecDeletedHooks = append(appSpecDeletedHooks, fn)
	}
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
	for _, fn := range appSpecSavedHooks {
		fn(s)
	}
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
	for _, fn := range appSpecDeletedHooks {
		fn(owner, slug)
	}
}
