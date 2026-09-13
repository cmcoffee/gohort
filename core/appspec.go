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
	"strings"
	"time"
)

// AppSpecTable is the per-user kvlite table holding AppSpecs, keyed by slug.
const AppSpecTable = "app_specs"

// appSpecSchema is the spec's wire schema, stamped on every save and carried
// in exports. Bump it ONLY when a field's MEANING changes (a rename, a value
// that reads differently), never for an added field — unknown fields already
// decode away and a missing one reads as its zero value, so additions cost an
// importer nothing. What an importer cannot do is read a schema it has never
// seen: a recipe stamped higher than this refuses to land, rather than landing
// with sections it would render empty. 0 on a stored spec means "written
// before the stamp existed" and reads as 1. Read through SchemaVersion.
const appSpecSchema = 1

// appNotesCap bounds the notes field (runes). It is deliberately small: notes
// are rewritten as a whole, and a cap is what keeps them a summary someone
// reads before editing rather than a log nobody does. Read through NotesCap.
const appNotesCap = 3000

// AppSpec is one data-driven app. Page holds the pageConfig JSON (from
// ui.Page.ConfigJSON) served verbatim — no Go Component round-trip. RecordKey is
// the primary-key field of the per-app record store. AgentID optionally binds an
// agent that powers the app's chat surface.
type AppSpec struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Desc    string `json:"desc"`
	Owner   string `json:"owner"`
	AgentID string `json:"agent_id,omitempty"`
	// PipelineID optionally binds a stored PipelineDef that a `pipeline`
	// section runs: the app's page becomes a submit form + streaming stage
	// transcript + run history, served by the host proxying to orchestrate's
	// existing run surface. The agent binding's counterpart — AgentID is "a
	// conversation lives here", PipelineID is "a multi-stage RUN lives here".
	// Resolved against the OWNER's pipelines (like scripts, which run in the
	// owner's sandbox); the run TRANSCRIPTS are per calling user, so a shared
	// app gives everyone the same recipe and their own history.
	PipelineID string          `json:"pipeline_id,omitempty"`
	Page       json.RawMessage `json:"page"`
	// Sections is the AUTHORING form of the page — the declarative sections
	// array the author passed to app_def, stored verbatim alongside the Page it
	// compiled into. Page is the RENDERED shape (bodies keyed by component
	// type); it is not valid authoring input, so without this an author who
	// reads a spec back has nothing to edit and every revision is a blind
	// re-write from scratch. Keeping the source next to the artifact makes
	// get → edit → update a real round trip. Empty for specs written before
	// this field existed (the reader reconstructs what it can and says so).
	Sections  json.RawMessage `json:"sections,omitempty"`
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
	// by a table/display section's source_script. Served at /apps/<slug>/data/<name>.
	// This is the "logic" seam: structure stays declarative, computation/integration
	// is a sandboxed script.
	DataSources []AppDataSource `json:"data_sources,omitempty"`
	// Actions are script-backed buttons (see AppAction) — the write-side of the
	// logic seam. Served at /apps/<slug>/action/<name>; surfaced by an "actions"
	// section.
	Actions []AppAction `json:"actions,omitempty"`
	// Settings are the app's declared tunables — the knobs a person would
	// plausibly change without re-authoring the app (an interval, a threshold,
	// a unit, which source). Declared by the author, stored per app (and per
	// user for Scope "user"), edited on the app's Settings page, and handed
	// to every script as environment variables named after each setting. The
	// declaration is part of the app's shape and exports with it; the VALUES
	// are deployment-local and do not.
	Settings []AppSetting `json:"settings,omitempty"`
	// Disabled blocks the app from serving (the host 403s every sub-route) until
	// the owner enables it from the My Apps index. It exists as the bundle-
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
	// PublicToken, when non-empty, publishes the app at /apps/pub/<token>/ as a
	// STATELESS, read/compute-only CAPABILITY URL: anyone with the (unguessable)
	// link loads the page and runs its data sources — in the OWNER's sandbox,
	// input via query params — but nothing is stored and every write endpoint is
	// refused. The token IS the access control; unpublishing clears it and
	// revokes the link. Regenerated on each publish. Deployment-local — cleared
	// on export.
	PublicToken string `json:"public_token,omitempty"`
	Created     string `json:"created"`
	Updated     string `json:"updated"`
	// ChangeNote is the author's one-line reason for the edit that PRODUCED this
	// revision ("added a city filter", "fixed the empty-array bug"). It travels
	// into history with the spec, so the revision listing reads as intent rather
	// than as a column of verbs, and a later author — a different session, a
	// different agent — can tell a deliberate choice from an accident before
	// undoing it. Cleared by any edit that gives no note: a note that outlives
	// the revision it described would describe the wrong one.
	ChangeNote string `json:"change_note,omitempty"`
	// Notes is the author's standing account of the app, for whoever revises it
	// next: what it is for, the decisions made and why, and what the owner asked
	// for that is not done. Rewritten as a whole, never appended, bounded by
	// AppNotesCap. ChangeNote is the per-revision half of this; Notes is the
	// per-app half. Part of the app's shape — it travels with an export.
	Notes string `json:"notes,omitempty"`
	// Schema is the wire schema the spec was last saved under (appSpecSchema).
	Schema int `json:"schema,omitempty"`
	// RecordFields is the record schema DERIVED from the sections at save time:
	// field name → the form type that writes it ("text", "number", "select"…),
	// or "column" for a field a table reads that no form writes, or "key" for
	// the record key. Nothing declares it; the host computes it so a later
	// author can see what the records look like without parsing the sections,
	// and so an update that drops a field can be checked against the records
	// that still carry it before it strands them.
	RecordFields map[string]string `json:"record_fields,omitempty"`
	// Sample is the last example-record set an author handed to test or verify,
	// kept on the app so the next run — and the auto-check on every save —
	// exercises the same form→record→data-source chain without inventing
	// records again. It doubles as documentation of what a record holds. Part
	// of the app's shape: it exports.
	Sample []map[string]any `json:"sample,omitempty"`
	// Verify is the last verify outcome, pinned to the revision it ran against.
	// Verify used to print "if you updated after this, this report is about the
	// OLD revision" and trust the author to remember; nothing stored the answer,
	// so a get could not say whether the app serving now had ever passed, and an
	// author who batched an update and a verify shipped the unverified one.
	// Deployment-local — a browser load on one host says nothing about another —
	// so export and import both clear it.
	Verify *AppVerifyState `json:"verify,omitempty"`
}

// AppVerifyState records one verify run. Against is the Updated stamp of the
// spec it checked — the same value the tool results call "revision" — so a
// reader compares it with the spec's current Updated to know whether the
// verdict still describes what is serving.
type AppVerifyState struct {
	At      string `json:"at"`
	Against string `json:"against"`
	Pass    bool   `json:"pass"`
	// Summary is the verdict line, short enough to quote in a listing.
	Summary string `json:"summary,omitempty"`
}

// Current reports whether the verdict describes the spec as it is now.
func (v *AppVerifyState) Current(spec AppSpec) bool {
	return v != nil && v.Against != "" && v.Against == spec.Updated
}

// VerifyStatus renders the spec's verify standing in one clause, for a tool
// result or a listing: never verified, verified against an earlier revision,
// or a current PASS/FAIL. The phrasing names the action to take, since the
// point of storing the state is that the author no longer has to remember.
func (s AppSpec) VerifyStatus() string {
	v := s.Verify
	switch {
	case v == nil:
		return "never verified — run app_def(action=\"verify\") before telling the user it is ready"
	case !v.Current(s):
		verdict := "FAIL"
		if v.Pass {
			verdict = "PASS"
		}
		return "last verify (" + verdict + ", " + v.At + ") was against an EARLIER revision (" + v.Against + "); the revision serving now (" + s.Updated + ") is unverified — run verify again"
	case v.Pass:
		return "verified PASS at " + v.At + " against this revision"
	default:
		return "verified FAIL at " + v.At + " against this revision: " + v.Summary
	}
}

// RecordVerify stores a verify outcome for THIS revision (Against = the
// spec's Updated stamp) WITHOUT touching Updated or history. It has to bypass
// SaveAppSpecAs: that stamps Updated on every write, and a verdict pinned to
// a stamp that the act of storing it just changed would be stale the moment
// it landed. The saved hooks are skipped too — a verdict changes nothing a
// schedule reconciles against. The receiver is read for its identity and its
// revision only; the stored row is what gets the verdict.
func (s AppSpec) RecordVerify(pass bool, summary string) bool {
	db := appSpecStore(s.Owner)
	if db == nil {
		return false
	}
	var stored AppSpec
	if !db.Get(AppSpecTable, s.Slug, &stored) {
		return false
	}
	stored.Verify = &AppVerifyState{
		At:      time.Now().UTC().Format(time.RFC3339),
		Against: s.Updated,
		Pass:    pass,
		Summary: strings.TrimSpace(summary),
	}
	db.Set(AppSpecTable, s.Slug, stored)
	return true
}

// AppSetting is one tunable an app declares. Type is one of "string" (the
// default), "number", "toggle" or "choice" (Options lists the values). Default
// is the value every script sees until someone sets one; it is a string
// because that is what an environment variable is. Scope decides where a set
// value lives for a SHARED app: "owner" (the default) is one value everybody
// gets, for things that run against the owner's credentials — which repo,
// which calendar; "user" is a value per person, stored beside their own copy
// of the records — their city, their units. Min/Max bound a number when
// Max > Min; both zero means unbounded (a pointer would not survive gob).
type AppSetting struct {
	Name    string   `json:"name"`
	Label   string   `json:"label,omitempty"`
	Type    string   `json:"type,omitempty"`
	Default string   `json:"default,omitempty"`
	Help    string   `json:"help,omitempty"`
	Scope   string   `json:"scope,omitempty"`
	Options []string `json:"options,omitempty"`
	Min     int      `json:"min,omitempty"`
	Max     int      `json:"max,omitempty"`
}

// PerUser reports whether a set value of this setting is one person's own
// (Scope "user") rather than the owner's, shared by every user of the app.
func (s AppSetting) PerUser() bool { return strings.EqualFold(strings.TrimSpace(s.Scope), "user") }

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
func SaveAppSpec(s AppSpec) AppSpec { return SaveAppSpecAs(s, "") }

// SaveAppSpecAs is SaveAppSpec with a note about what is doing the writing,
// filed against the version being REPLACED so an author reading the history
// sees "update" or "replace_function drawBird" next to each entry instead of a
// column of timestamps.
//
// Snapshotting here rather than at each call site is deliberate: history that
// depends on every writer remembering to ask for it is history with holes in
// exactly the sessions that needed it. Only writes that change what the app
// SERVES are kept — toggling an app disabled is not a revision of its
// document, and letting it push one out of the ring would trade a real version
// for a metadata flip. Pass AppSaveNoHistory to suppress (the rollback paths
// do, so a broken revision never gets filed as history).
func SaveAppSpecAs(s AppSpec, reason string) AppSpec {
	db := appSpecStore(s.Owner)
	if db == nil {
		return s
	}
	if reason != AppSaveNoHistory {
		if prior, ok := LoadAppSpec(s.Owner, s.Slug); ok && specPageChanged(prior, s) {
			PushAppRevision(prior, reason)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if s.Created == "" {
		s.Created = now
	}
	s.Updated = now
	s.Schema = appSpecSchema
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
	DeleteAppRevisions(owner, slug)
	for _, fn := range appSpecDeletedHooks {
		fn(owner, slug)
	}
}

// SchemaVersion reads the stamp with its pre-stamp default.
func (s AppSpec) SchemaVersion() int {
	if s.Schema <= 0 {
		return 1
	}
	return s.Schema
}

// upgradeAppSpec brings a spec written under an earlier schema up to
// appSpecSchema, one step per version. Every step is a pure rewrite of the
// record; nothing here touches storage. It is the import path's seam for a
// meaning change and today has nothing to do, because no meaning has changed
// yet — the point of having it now is that the first change lands in a switch
// arm rather than as a scattered "if the field is empty, assume" in readers.
// A spec from a NEWER schema is returned untouched with ok=false; the caller
// refuses it.
func upgradeAppSpec(s AppSpec) (AppSpec, bool) {
	from := s.SchemaVersion()
	if from > appSpecSchema {
		return s, false
	}
	for v := from; v < appSpecSchema; v++ {
		switch v {
		// case 1: s = upgradeAppSpecV1toV2(s)
		}
	}
	s.Schema = appSpecSchema
	return s, true
}

// NotesCap is the bound on Notes, in runes, for a caller composing a refusal.
func (AppSpec) NotesCap() int { return appNotesCap }

// NotesOver reports by how many runes Notes exceeds the cap; 0 when it fits.
func (s AppSpec) NotesOver() int {
	if n := len([]rune(s.Notes)); n > appNotesCap {
		return n - appNotesCap
	}
	return 0
}

// RecordSample stores an example-record set on the app WITHOUT touching
// Updated or history, the way RecordVerify stores a verdict: a sample is a
// test fixture, not a revision of the document. nil clears.
func (s AppSpec) RecordSample(sample []map[string]any) bool {
	db := appSpecStore(s.Owner)
	if db == nil {
		return false
	}
	var stored AppSpec
	if !db.Get(AppSpecTable, s.Slug, &stored) {
		return false
	}
	stored.Sample = sample
	db.Set(AppSpecTable, s.Slug, stored)
	return true
}
